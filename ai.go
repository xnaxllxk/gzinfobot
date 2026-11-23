package main

// Тонкий клиент к любому OpenAI-совместимому шлюзу (по умолчанию — локальный freellmpool):
// «вот фрагмент текста — верни JSON с числами». Шлюз сам выбирает провайдера и переключается
// при сбоях; сюда он возвращает либо ответ, либо ошибку (502/503), и модуль просто повторит позже.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var aiRetryDelay = 3 * time.Second // переменная, чтобы тесты могли ускорить

const aiSystem = `You extract numbers from the plain text of a web page (usually Turkish). Reply with a single json object and nothing else. Copy numbers exactly as they are written in the text. Never estimate, calculate or invent values; use null when a value is not present.`

type aiClient struct {
	hc    *http.Client
	url   string
	key   string
	model string
	// jsonMode добавляет response_format=json_object. По умолчанию выключен: freellmpool при
	// таком поле берёт только провайдеров, прошедших `freellmpool conformance run`, и без
	// этой проверки отвечает 503. Бот и так просит JSON в промпте и проверяет каждое число.
	jsonMode bool
}

// ask отправляет запрос и разбирает JSON-ответ в out (две попытки).
func (c *aiClient) ask(ctx context.Context, user string, out any) error {
	body := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": aiSystem},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		// Запас под скрытые «размышления» (DeepSeek V4 Flash и др.): иначе ответ может прийти
		// пустым. Сам JSON короткий, так что лишние токены не тратятся.
		"max_tokens": 4096,
	}
	if c.jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	reqBody, _ := json.Marshal(body)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && !sleep(ctx, aiRetryDelay) {
			return ctx.Err()
		}
		content, err := c.once(ctx, reqBody)
		if err == nil {
			if err = json.Unmarshal([]byte(extractJSON(content)), out); err == nil {
				return nil
			}
			err = fmt.Errorf("ответ ИИ не разобран: %w", err)
		}
		lastErr = err
	}
	return lastErr
}

func (c *aiClient) once(ctx context.Context, reqBody []byte) (string, error) {
	// Шлюз может перебрать несколько бесплатных провайдеров подряд, поэтому таймаут щедрый.
	rctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ИИ-шлюз недоступен (запущен ли freellmpool?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("ИИ-шлюз HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return "", err
	}
	if len(r.Choices) == 0 || strings.TrimSpace(r.Choices[0].Message.Content) == "" {
		return "", errors.New("ИИ вернул пустой ответ")
	}
	return r.Choices[0].Message.Content, nil
}

func extractJSON(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
