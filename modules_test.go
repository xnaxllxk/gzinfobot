package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const goldPage = `<html><body><h1>Altın</h1>
<table><tr><td>Gram Altın</td><td>3.456,78</td><td>3.470,10</td></tr></table></body></html>`

func goldAI(t *testing.T, reply string) func(string) string {
	return func(prompt string) string {
		if !strings.Contains(prompt, "gram: Золото, грамм") {
			t.Errorf("в промпте нет описания поля:\n%s", prompt)
		}
		return reply
	}
}

func newSource(fields ...SrcField) *sourceModule {
	return &sourceModule{name: "gold", fields: fields}
}

func srcEnv(a *app, site, anchor string) *Env {
	return &Env{
		HC: a.fetchHC, AI: a.ai, St: newStore(""), Now: time.Now(), StaleAfter: time.Hour,
		Cfg: newCfg(newSource().Fields(), map[string]string{"url": site + "/gold", "anchor": anchor, "title": "Золото"}),
	}
}

func TestSourceModuleHappyPath(t *testing.T) {
	site := fakeSite(t, map[string]string{"/gold": goldPage})
	ai := fakeAI(t, goldAI(t, `{"gram": "3.456,78"}`))
	a, _ := newTestApp(t, ai.URL, site)
	m := newSource(SrcField{Key: "gram", Label: "Золото, грамм", Min: 1000, Max: 100000})
	e := srcEnv(a, site.URL, "Gram Altın")

	if err := m.Refresh(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	b := m.Render(context.Background(), e)
	if b.Text != "Золото\nЗолото, грамм 3456.78" || len(b.Warn) != 0 {
		t.Errorf("блок: %q warn=%v", b.Text, b.Warn)
	}
	// Страница не изменилась — ИИ второй раз не вызывается.
	calls := 0
	m2 := newSource(SrcField{Key: "gram", Label: "Золото, грамм", Min: 1000, Max: 100000})
	e.AI = &aiClient{hc: a.hc, url: fakeAI(t, func(string) string { calls++; return `{}` }).URL, key: "k", model: "m"}
	if err := m2.Refresh(context.Background(), e); err != nil || calls != 0 {
		t.Errorf("повторное чтение той же страницы: err=%v, вызовов ИИ=%d", err, calls)
	}
}

func TestSourceModuleRejects(t *testing.T) {
	site := fakeSite(t, map[string]string{"/gold": goldPage})
	field := SrcField{Key: "gram", Label: "Золото, грамм", Min: 1000, Max: 100000}
	cases := map[string]struct{ ai, anchor, want string }{
		"выдуманное число":      {`{"gram": "3999,99"}`, "Gram Altın", "не найдено в тексте"},
		"null":                  {`{"gram": null}`, "Gram Altın", "значение не найдено"},
		"поле отсутствует":      {`{}`, "Gram Altın", "значение не найдено"},
		"якоря нет на странице": {`{"gram": "3.456,78"}`, "Ons Altın", "не найдено на странице"},
		"мусор вместо JSON":     {`не json`, "Gram Altın", "не разобран"},
	}
	for name, c := range cases {
		a, _ := newTestApp(t, fakeAI(t, func(string) string { return c.ai }).URL, site)
		err := newSource(field).Refresh(context.Background(), srcEnv(a, site.URL, c.anchor))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %v, ожидалось «%s»", name, err, c.want)
		}
	}

	// число есть на странице, но выходит за заданный админом диапазон
	a, _ := newTestApp(t, fakeAI(t, func(string) string { return `{"gram": "3.456,78"}` }).URL, site)
	narrow := SrcField{Key: "gram", Label: "Золото, грамм", Min: 10, Max: 100}
	if err := newSource(narrow).Refresh(context.Background(), srcEnv(a, site.URL, "Gram Altın")); err == nil || !strings.Contains(err.Error(), "диапазон") {
		t.Errorf("диапазон: %v", err)
	}
	// у источника без полей обновляться нечему
	if err := newSource().Refresh(context.Background(), srcEnv(a, site.URL, "Gram Altın")); err == nil {
		t.Error("источник без полей должен давать ошибку")
	}
}

func TestSourceJumpNeedsConfirmation(t *testing.T) {
	srv := fakeSite(t, map[string]string{"/gold": "<td>Gram Altın</td><td>1000</td>"})
	var mu sync.Mutex
	cur := "1000"
	ai := fakeAI(t, func(string) string {
		mu.Lock()
		defer mu.Unlock()
		return `{"gram": "` + cur + `"}`
	})
	a, _ := newTestApp(t, ai.URL, srv)
	m := newSource(SrcField{Key: "gram", Label: "Золото, грамм", Min: 1, Max: 1e6})
	e := srcEnv(a, srv.URL, "Gram Altın")
	if err := m.Refresh(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	srv.set("/gold", "<td>Gram Altın</td><td>2000</td>")
	mu.Lock()
	cur = "2000"
	mu.Unlock()
	if err := m.Refresh(context.Background(), e); err == nil || !strings.Contains(err.Error(), "15%") {
		t.Fatalf("резкий скачок должен ждать подтверждения: %v", err)
	}
	if err := m.Refresh(context.Background(), e); err != nil {
		t.Fatalf("повторное чтение того же значения принимается: %v", err)
	}
}

// Формат отчёта по умолчанию должен остаться байт-в-байт прежним (шаблон из версии до модульности).
const legacyTemplate = `🌾Ziraat Bank🇹🇷
(Курс валют на %s)
Валюта   Покупка   Продажа
USD          %s       %s 🇺🇸
EUR          %s       %s 🇪🇺

Погода в Газипаше сегодня 🌍
Восход  %s
Закат    %s
Температура 🌡 воздуха💨 %s C°
Средний поток ветра %s км/ч
UV индекс %s

Цены на бензин ⛽
(Qplus petrol)
Бензин %sTL за литр 🔻
Дизель %sTL за литр  ⚫
Газовое топливо %sTL за литр  💨`

func TestReportMatchesLegacyFormat(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	now := time.Now()
	a.st.put("rates", Rates{USDBuy: 43.1, USDSell: 43.2, EURBuy: 50.1, EURSell: 50.3}, now, "", "h")
	a.st.put("fuel", Fuel{Gasoline: 70.5, Diesel: 68.1, LPG: 30.2, PageDate: now.In(trt).Format("02.01.2006")}, now, "", "h")
	a.st.put("weather", Weather{Temp: -0.2, Wind: 12.4, UV: 3.26, Sunrise: "06:55", Sunset: "17:20", At: now}, now, "36.269,32.316", "")

	want := fmt.Sprintf(legacyTemplate, now.In(trt).Format("02.01.2006 15:04"),
		"43.1000", "43.2000", "50.1000", "50.3000", "06:55", "17:20", "+0", "12", "3.3", "70.50", "68.10", "30.20")
	if got := a.report(context.Background()); got != want {
		t.Errorf("отчёт изменился.\nполучено:\n%s\n\nожидалось:\n%s", got, want)
	}
}

func TestReportWarningsAndDisabled(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	// weather без сети: API недоступен → «погода недоступна»; курсов и топлива нет.
	weatherAPI = "https://127.0.0.1:1/none"
	defer func() { weatherAPI = "https://api.open-meteo.com/v1/forecast" }()
	out := a.report(context.Background())
	for _, w := range []string{"курсы недоступны", "цены на топливо недоступны", "погода недоступна"} {
		if !strings.Contains(out, w) {
			t.Errorf("нет пометки %q в:\n%s", w, out)
		}
	}
	// выключенный модуль не попадает в отчёт
	a.setMu.Lock()
	cand := a.cur.Load().set.clone()
	cand.Modules["weather"].Enabled = false
	cand.Modules["fuel"].Enabled = false
	if err := a.apply(cand); err != nil {
		t.Fatal(err)
	}
	a.setMu.Unlock()
	out = a.report(context.Background())
	if strings.Contains(out, "Погода") || strings.Contains(out, "бензин") {
		t.Errorf("выключенные модули не должны попадать в отчёт:\n%s", out)
	}
}

// Паника в чужом модуле не должна ронять отчёт.
type boomModule struct{}

func (boomModule) Name() string                        { return "boom" }
func (boomModule) Title() string                       { return "Бум" }
func (boomModule) Fields() []Field                     { return nil }
func (boomModule) Refresh(context.Context, *Env) error { panic("refresh") }
func (boomModule) Render(context.Context, *Env) Block  { panic("render") }

func TestModulePanicIsContained(t *testing.T) {
	e := &Env{}
	if err := safeRefresh(context.Background(), boomModule{}, e); err == nil {
		t.Error("паника в Refresh должна стать ошибкой")
	}
	if b := safeRender(context.Background(), boomModule{}, e); len(b.Warn) == 0 {
		t.Error("паника в Render должна стать пометкой")
	}
}
