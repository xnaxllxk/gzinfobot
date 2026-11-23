package main

// Скачивание страниц и превращение их в компактный текст, плюс разбор чисел.
// Никакого ИИ здесь нет — только предсказуемая механика.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var fetchRetryDelay = 3 * time.Second // переменная, чтобы тесты могли ускорить

var (
	reCmt    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reDrop   = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg)\b.*?</(?:script|style|noscript|svg)>`)
	reCell   = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	reRow    = regexp.MustCompile(`(?i)</tr\s*>|<br\s*/?>|</(?:p|div|li|h[1-6])\s*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces = regexp.MustCompile(`[ \t\r\f\v\x{00a0}]+`)
	reNL     = regexp.MustCompile(`\n\s*\n+`)
	reNonNum = regexp.MustCompile(`[^0-9.,\-]`)
)

// fetchText скачивает страницу (одна повторная попытка) и возвращает её как чистый текст.
func fetchText(ctx context.Context, hc *http.Client, url string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && !sleep(ctx, fetchRetryDelay) {
			return "", ctx.Err()
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		req.Header.Set("Accept-Language", "tr-TR,tr;q=0.9,en;q=0.5")
		resp, err := hc.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
			continue
		}
		if rerr != nil {
			lastErr = rerr
			continue
		}
		return htmlToText(string(b)), nil
	}
	return "", lastErr
}

// htmlToText превращает HTML в текст: ячейки таблиц через « | », строки — через перевод строки.
func htmlToText(src string) string {
	s := reCmt.ReplaceAllString(src, "")
	s = reDrop.ReplaceAllString(s, " ")
	s = reCell.ReplaceAllString(s, " | ")
	s = reRow.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, "\n")
	s = reNL.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

// window вырезает фрагмент вокруг первого найденного якоря — только его увидит ИИ.
func window(text string, anchors []string, before, length int) (string, bool) {
	for _, a := range anchors {
		if i := strings.Index(text, a); i >= 0 {
			w := text[max(0, i-before):min(len(text), i+length)]
			return strings.ToValidUTF8(w, ""), true
		}
	}
	return "", false
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// parseTR разбирает числа в турецком и обычном форматах: "50,0448", "₺70,56", "1.234,56", "70.56".
func parseTR(s string) (float64, error) {
	s = reNonNum.ReplaceAllString(s, "")
	if s == "" {
		return 0, errors.New("пустое число")
	}
	hasDot, hasComma := strings.Contains(s, "."), strings.Contains(s, ",")
	switch {
	case hasDot && hasComma:
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") { // 1.234,56
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else { // 1,234.56
			s = strings.ReplaceAll(s, ",", "")
		}
	case hasComma:
		s = strings.Replace(s, ",", ".", 1)
	}
	return strconv.ParseFloat(s, 64)
}

// num принимает и число, и строку из JSON-ответа ИИ.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == "" {
		*n = 0
		return nil
	}
	v, err := parseTR(s)
	if err != nil {
		return err
	}
	*n = num(v)
	return nil
}

// grounded: число из ответа ИИ должно дословно встречаться в тексте страницы.
// Это отсекает выдуманные («галлюцинированные») значения. Учитываются запись с точкой или
// запятой и разделители тысяч: 3456.78, 3456,78, 3.456,78, 3,456.78, 3 456,78.
func grounded(text string, v float64) bool {
	for _, d := range []int{-1, 2, 3, 4, 5, 6} {
		s := strconv.FormatFloat(v, 'f', d, 64)
		for _, form := range numForms(s) {
			if strings.Contains(text, form) {
				return true
			}
		}
	}
	return false
}

// numForms возвращает варианты записи числа s (формат "1234.56") с разными разделителями.
func numForms(s string) []string {
	forms := []string{s, strings.Replace(s, ".", ",", 1)}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	digits := strings.TrimPrefix(intPart, "-")
	if len(digits) <= 3 {
		return forms
	}
	var groups []string
	for len(digits) > 3 {
		groups = append([]string{digits[len(digits)-3:]}, groups...)
		digits = digits[:len(digits)-3]
	}
	groups = append([]string{digits}, groups...)
	for _, thousands := range []string{".", ",", " "} {
		g := strings.Join(groups, thousands)
		if neg {
			g = "-" + g
		}
		if !hasFrac {
			forms = append(forms, g)
			continue
		}
		for _, dec := range []string{",", "."} {
			if dec != thousands {
				forms = append(forms, g+dec+frac)
			}
		}
	}
	return forms
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("02.01.2006", strings.TrimSpace(s), trt)
	return t, err == nil
}
