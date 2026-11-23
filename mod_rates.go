package main

// Модуль «rates»: курсы Ziraat Bankası. Страница банка → фрагмент текста → ИИ достаёт числа →
// проверки → кэш.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultZiraatURL = "https://www.ziraatbank.com.tr/tr/fiyatlar-ve-oranlar"

// Rates — курсы Ziraat (покупка/продажа банком).
type Rates struct {
	USDBuy  float64   `json:"usd_buy"`
	USDSell float64   `json:"usd_sell"`
	EURBuy  float64   `json:"eur_buy"`
	EURSell float64   `json:"eur_sell"`
	At      time.Time `json:"at"`
}

type ratesModule struct{}

func (ratesModule) Name() string  { return "rates" }
func (ratesModule) Title() string { return "Курсы Ziraat" }

func (ratesModule) Fields() []Field {
	return []Field{
		{Key: "url", Label: "Страница курсов", Kind: KURL, Default: defaultZiraatURL},
		{Key: "channel", Label: "Тип курса", Kind: KEnum, Enum: []string{"gise", "internet"}, Default: "gise",
			Help: "gise = курс в кассе, internet = интернет-курс"},
	}
}

type quad struct {
	USDBuy  num `json:"usd_buy"`
	USDSell num `json:"usd_sell"`
	EURBuy  num `json:"eur_buy"`
	EURSell num `json:"eur_sell"`
}

func ziraatPrompt(w string) string {
	return `The text below is the "Döviz Kurları" (exchange rates) section of the Ziraat Bankası page. It normally has two tables in this order: first "Gişe Kuru" (branch rates), then "İnternet Kuru" (internet rates). Columns: Döviz Türü, Döviz Adı, Döviz Alış, Döviz Satış (the first table also has Efektif Alış and Efektif Satış: ignore the Efektif columns).

Return json in exactly this shape:
{"gise": {"usd_buy": "...", "usd_sell": "...", "eur_buy": "...", "eur_sell": "..."}, "internet": {"usd_buy": "...", "usd_sell": "...", "eur_buy": "...", "eur_sell": "..."}}
The USD row is AMERIKAN DOLARI, the EUR row is EURO. buy = "Döviz Alış", sell = "Döviz Satış".

TEXT:
` + w
}

func checkRates(q quad, text string) error {
	items := []struct {
		name string
		v    float64
	}{
		{"usd_buy", float64(q.USDBuy)}, {"usd_sell", float64(q.USDSell)},
		{"eur_buy", float64(q.EURBuy)}, {"eur_sell", float64(q.EURSell)},
	}
	for _, it := range items {
		if it.v < 1 || it.v > 1000 {
			return fmt.Errorf("%s=%v вне допустимого диапазона", it.name, it.v)
		}
		if !grounded(text, it.v) {
			return fmt.Errorf("%s=%v не найдено в тексте страницы (ответ ИИ отклонён)", it.name, it.v)
		}
	}
	if q.USDBuy > q.USDSell || q.EURBuy > q.EURSell {
		return errors.New("покупка больше продажи — похоже на ошибку чтения")
	}
	if q.USDSell/q.USDBuy > 1.2 || q.EURSell/q.EURBuy > 1.2 {
		return errors.New("слишком большой спред покупка/продажа")
	}
	if x := float64(q.EURSell / q.USDSell); x < 0.7 || x > 1.6 {
		return fmt.Errorf("соотношение EUR/USD=%.2f неправдоподобно", x)
	}
	return nil
}

func (ratesModule) Refresh(ctx context.Context, e *Env) error {
	channel := e.Cfg.Str("channel")
	text, err := fetchText(ctx, e.HC, e.Cfg.Str("url"))
	if err != nil {
		return err
	}
	w, ok := window(text, []string{"Döviz Türü"}, 250, 2200)
	if !ok {
		return errors.New("таблица курсов («Döviz Türü») не найдена: изменилась страница или она собирается скриптом")
	}
	hash := sha(w + "|" + channel) // тип курса входит в хэш: смена channel должна дать новое чтение
	if e.St.sameHash("rates", hash) {
		e.St.touch("rates") // страница не изменилась — токены не тратим
		return nil
	}
	var res struct {
		Gise     quad `json:"gise"`
		Internet quad `json:"internet"`
	}
	if err := e.AI.ask(ctx, ziraatPrompt(w), &res); err != nil {
		return err
	}
	q := res.Gise
	if channel == "internet" {
		q = res.Internet
	}
	if err := checkRates(q, w); err != nil {
		return err
	}
	cur := []float64{float64(q.USDBuy), float64(q.USDSell), float64(q.EURBuy), float64(q.EURSell)}
	var old []float64
	var prev Rates
	if _, ok := e.St.load("rates", &prev); ok {
		old = []float64{prev.USDBuy, prev.USDSell, prev.EURBuy, prev.EURSell}
	}
	if !e.St.jumpOK("rates", old, cur) {
		return errors.New("курсы изменились более чем на 15% — жду подтверждения при следующем обновлении")
	}
	now := time.Now()
	e.St.put("rates", Rates{USDBuy: cur[0], USDSell: cur[1], EURBuy: cur[2], EURSell: cur[3], At: now}, now, "", hash)
	return nil
}

const ratesTemplate = `🌾Ziraat Bank🇹🇷
(Курс валют на %s)
Валюта   Покупка   Продажа
USD          %s       %s 🇺🇸
EUR          %s       %s 🇪🇺`

func (ratesModule) Render(_ context.Context, e *Env) Block {
	stamp, usdB, usdS, eurB, eurS := na, na, na, na, na
	var warn []string
	var r Rates
	if at, ok := e.St.load("rates", &r); ok {
		stamp = at.In(trt).Format("02.01.2006 15:04")
		usdB, usdS = fmt.Sprintf("%.4f", r.USDBuy), fmt.Sprintf("%.4f", r.USDSell)
		eurB, eurS = fmt.Sprintf("%.4f", r.EURBuy), fmt.Sprintf("%.4f", r.EURSell)
		if e.now().Sub(at) > e.StaleAfter {
			warn = append(warn, "курсы не обновлялись с "+at.In(trt).Format("02.01 15:04"))
		}
	} else {
		warn = append(warn, "курсы недоступны")
	}
	return Block{Text: fmt.Sprintf(ratesTemplate, stamp, usdB, usdS, eurB, eurS), Warn: warn}
}
