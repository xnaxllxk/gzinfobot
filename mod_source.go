package main

// Универсальный модуль-источник: «возьми эту страницу, найди слово-якорь, достань эти числа».
// Создаётся из чата командой /source, кода писать не нужно. Вся защита от выдуманных чисел та же,
// что у курсов и топлива: число должно дословно встречаться на странице и попадать в
// заданный админом диапазон.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type sourceModule struct {
	name   string
	fields []SrcField
}

func newSourceModule(ms *ModSettings) *sourceModule {
	return &sourceModule{name: ms.Name, fields: append([]SrcField(nil), ms.Fields...)}
}

func (m *sourceModule) Name() string  { return m.name }
func (m *sourceModule) Title() string { return "Источник " + m.name }

func (m *sourceModule) Fields() []Field {
	return []Field{
		{Key: "title", Label: "Заголовок в отчёте", Kind: KString, Max: 40},
		{Key: "url", Label: "Страница", Kind: KURL},
		{Key: "anchor", Label: "Слово-якорь", Kind: KString, Max: 60,
			Help: "слово на странице, вокруг которого вырезается кусок текста для ИИ"},
		{Key: "before", Label: "Захват до якоря (символов)", Kind: KInt, Min: 0, Max: 2000, Default: "50"},
		{Key: "length", Label: "Длина фрагмента (символов)", Kind: KInt, Min: 100, Max: 4000, Default: "1200"},
	}
}

// sig — отпечаток набора полей: при его смене страница читается заново, даже если текст тот же.
func (m *sourceModule) sig() string {
	var b strings.Builder
	for _, f := range m.fields {
		fmt.Fprintf(&b, "%s:%s:%v:%v;", f.Key, f.Label, f.Min, f.Max)
	}
	return b.String()
}

func (m *sourceModule) prompt(w string) string {
	var list strings.Builder
	var shape []string
	for _, f := range m.fields {
		fmt.Fprintf(&list, "- %s: %s\n", f.Key, f.Label)
		shape = append(shape, fmt.Sprintf("%q: \"...\"", f.Key))
	}
	return `The text below is a fragment of a web page. Find these numeric values:
` + list.String() + `
Return json in exactly this shape:
{` + strings.Join(shape, ", ") + `}
Copy each number exactly as it is written in the text. Use null when a value is not present.

TEXT:
` + w
}

func (m *sourceModule) Refresh(ctx context.Context, e *Env) error {
	if len(m.fields) == 0 {
		return errors.New("не заданы поля: /source field " + m.name + " <ключ> <мин> <макс> <подпись>")
	}
	url, anchor := e.Cfg.Str("url"), e.Cfg.Str("anchor")
	if url == "" || anchor == "" {
		return errors.New("не заданы url и anchor")
	}
	text, err := fetchText(ctx, e.HC, url)
	if err != nil {
		return err
	}
	w, ok := window(text, []string{anchor}, e.Cfg.Int("before"), e.Cfg.Int("length"))
	if !ok {
		return fmt.Errorf("слово-якорь %q не найдено на странице: изменилась страница или она собирается скриптом", anchor)
	}
	hash := sha(w + "|" + m.sig())
	if e.St.sameHash(m.name, hash) {
		e.St.touch(m.name)
		return nil
	}
	var raw map[string]json.RawMessage
	if err := e.AI.ask(ctx, m.prompt(w), &raw); err != nil {
		return err
	}
	cur := make([]float64, len(m.fields))
	vals := make(map[string]float64, len(m.fields))
	for i, f := range m.fields {
		rm, ok := raw[f.Key]
		if s := strings.TrimSpace(string(rm)); !ok || s == "null" || s == `""` {
			return fmt.Errorf("%s: значение не найдено в тексте", f.Key)
		}
		var n num
		if err := json.Unmarshal(rm, &n); err != nil {
			return fmt.Errorf("%s: %w", f.Key, err)
		}
		v := float64(n)
		if v < f.Min || v > f.Max {
			return fmt.Errorf("%s=%v вне заданного диапазона %v…%v", f.Key, v, f.Min, f.Max)
		}
		if !grounded(w, v) {
			return fmt.Errorf("%s=%v не найдено в тексте страницы (ответ ИИ отклонён)", f.Key, v)
		}
		cur[i], vals[f.Key] = v, v
	}
	var old []float64
	var prev map[string]float64
	if _, ok := e.St.load(m.name, &prev); ok {
		for _, f := range m.fields {
			pv, has := prev[f.Key]
			if !has {
				old = nil
				break
			}
			old = append(old, pv)
		}
	}
	if !e.St.jumpOK(m.name, old, cur) {
		return errors.New("значения изменились более чем на 15% — жду подтверждения при следующем обновлении")
	}
	now := time.Now()
	e.St.put(m.name, vals, now, "", hash)
	return nil
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (m *sourceModule) Render(_ context.Context, e *Env) Block {
	title := e.Cfg.Str("title")
	if title == "" {
		title = m.name
	}
	var vals map[string]float64
	at, ok := e.St.load(m.name, &vals)
	if !ok {
		return Block{Warn: []string{title + ": данные недоступны"}}
	}
	lines := []string{title}
	for _, f := range m.fields {
		s := na
		if v, has := vals[f.Key]; has {
			s = fmtNum(v)
		}
		lines = append(lines, f.Label+" "+s)
	}
	b := Block{Text: strings.Join(lines, "\n")}
	if e.now().Sub(at) > e.StaleAfter {
		b.Warn = append(b.Warn, title+" не обновлялся с "+at.In(trt).Format("02.01 15:04"))
	}
	return b
}
