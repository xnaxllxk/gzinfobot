package main

// Модуль «fuel»: цены на топливо. Страница-источник → фрагмент текста → ИИ достаёт числа →
// проверки → кэш.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultFuelURL = "https://www.doviz.com/akaryakit-fiyatlari/antalya/gazipasa/qplus"
const defaultBrand = "Qplus"

// Fuel — цены, TL за литр.
type Fuel struct {
	Gasoline float64   `json:"gasoline"`
	Diesel   float64   `json:"diesel"`
	LPG      float64   `json:"lpg"`
	PageDate string    `json:"page_date"` // дата, указанная на странице-источнике
	At       time.Time `json:"at"`
}

type fuelModule struct{}

func (fuelModule) Name() string  { return "fuel" }
func (fuelModule) Title() string { return "Топливо" }

func (fuelModule) Fields() []Field {
	return []Field{
		{Key: "url", Label: "Страница с ценами", Kind: KURL, Default: defaultFuelURL},
		{Key: "brand", Label: "Сеть АЗС", Kind: KString, Max: 30, Default: defaultBrand,
			Help: "название как на странице, например Qplus, Opet, Shell"},
	}
}

type fuelAI struct {
	Gasoline num    `json:"gasoline"`
	Diesel   num    `json:"diesel"`
	LPG      num    `json:"lpg"`
	Date     string `json:"date"`
}

func fuelPrompt(brand, w string) string {
	also := ""
	if strings.EqualFold(brand, defaultBrand) {
		also = " (also written Q Plus or Qplus)"
	}
	return `The text below is a table of fuel prices in Turkish lira per liter. Find the row of the brand ` + brand + also + `.

Return json in exactly this shape:
{"gasoline": "...", "diesel": "...", "lpg": "...", "date": "DD.MM.YYYY"}
gasoline = Benzin (unleaded 95), diesel = Motorin, lpg = LPG (otogaz). date = the date shown in that row, if any, otherwise null.

TEXT:
` + w
}

func checkFuel(f fuelAI, text string) error {
	items := []struct {
		name string
		v    float64
	}{
		{"gasoline", float64(f.Gasoline)}, {"diesel", float64(f.Diesel)}, {"lpg", float64(f.LPG)},
	}
	for _, it := range items {
		if it.v < 5 || it.v > 500 {
			return fmt.Errorf("%s=%v вне допустимого диапазона", it.name, it.v)
		}
		if !grounded(text, it.v) {
			return fmt.Errorf("%s=%v не найдено в тексте страницы (ответ ИИ отклонён)", it.name, it.v)
		}
	}
	return nil
}

func (fuelModule) Refresh(ctx context.Context, e *Env) error {
	brand := e.Cfg.Str("brand")
	text, err := fetchText(ctx, e.HC, e.Cfg.Str("url"))
	if err != nil {
		return err
	}
	w, ok := window(text, []string{"Dağıtıcı", "Benzin"}, 50, 900)
	if !ok {
		return errors.New("таблица цен на топливо не найдена: изменилась страница или она собирается скриптом")
	}
	hash := sha(w + "|" + brand)
	if e.St.sameHash("fuel", hash) {
		e.St.touch("fuel")
		return nil
	}
	var res fuelAI
	if err := e.AI.ask(ctx, fuelPrompt(brand, w), &res); err != nil {
		return err
	}
	if err := checkFuel(res, w); err != nil {
		return err
	}
	cur := []float64{float64(res.Gasoline), float64(res.Diesel), float64(res.LPG)}
	var old []float64
	var prev Fuel
	if _, ok := e.St.load("fuel", &prev); ok {
		old = []float64{prev.Gasoline, prev.Diesel, prev.LPG}
	}
	if !e.St.jumpOK("fuel", old, cur) {
		return errors.New("цены на топливо изменились более чем на 15% — жду подтверждения при следующем обновлении")
	}
	now := time.Now()
	e.St.put("fuel", Fuel{Gasoline: cur[0], Diesel: cur[1], LPG: cur[2], PageDate: strings.TrimSpace(res.Date), At: now}, now, "", hash)
	return nil
}

const fuelTemplate = `Цены на бензин ⛽
(%s petrol)
Бензин %sTL за литр 🔻
Дизель %sTL за литр  ⚫
Газовое топливо %sTL за литр  💨`

func (fuelModule) Render(_ context.Context, e *Env) Block {
	gas, dsl, lpg := na, na, na
	var warn []string
	var f Fuel
	if at, ok := e.St.load("fuel", &f); ok {
		gas, dsl, lpg = fmt.Sprintf("%.2f", f.Gasoline), fmt.Sprintf("%.2f", f.Diesel), fmt.Sprintf("%.2f", f.LPG)
		now := e.now()
		stale := now.Sub(at) > e.StaleAfter
		label := at.In(trt).Format("02.01")
		if d, ok := parseDay(f.PageDate); ok {
			label = d.Format("02.01")
			if now.Sub(d) > 5*24*time.Hour {
				stale = true
			}
		}
		if stale {
			warn = append(warn, "цены на топливо от "+label)
		}
	} else {
		warn = append(warn, "цены на топливо недоступны")
	}
	return Block{Text: fmt.Sprintf(fuelTemplate, e.Cfg.Str("brand"), gas, dsl, lpg), Warn: warn}
}
