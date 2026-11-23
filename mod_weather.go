package main

// Модуль «weather»: погода Open-Meteo — обычный JSON API без ключа, ИИ не нужен.
// Результат живёт в кэше 30 минут; /report обновляет его по требованию, расписание — заранее.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	weatherTTL   = 30 * time.Minute
	gazipasaLat  = "36.269"
	gazipasaLon  = "32.316"
	defaultPlace = "Газипаше"
)

type Weather struct {
	Temp    float64   `json:"temp"`
	Wind    float64   `json:"wind"`
	UV      float64   `json:"uv"`
	Sunrise string    `json:"sunrise"`
	Sunset  string    `json:"sunset"`
	At      time.Time `json:"at"`
}

// weatherMu не даёт нескольким одновременным /report запускать несколько одинаковых запросов.
var weatherMu sync.Mutex

type weatherModule struct{}

func (weatherModule) Name() string  { return "weather" }
func (weatherModule) Title() string { return "Погода" }

func (weatherModule) Fields() []Field {
	return []Field{
		{Key: "lat", Label: "Широта", Kind: KFloat, Min: -90, Max: 90, Default: gazipasaLat},
		{Key: "lon", Label: "Долгота", Kind: KFloat, Min: -180, Max: 180, Default: gazipasaLon},
		{Key: "place", Label: "Место в заголовке", Kind: KString, Max: 40, Default: defaultPlace,
			Help: "в предложном падеже: «Погода в <место> сегодня»"},
	}
}

func weatherMeta(e *Env) string { return e.Cfg.Str("lat") + "," + e.Cfg.Str("lon") }

func (weatherModule) Refresh(ctx context.Context, e *Env) error {
	w, err := fetchWeather(ctx, e)
	if err != nil {
		return err
	}
	e.St.put("weather", w, w.At, weatherMeta(e), "")
	return nil
}

const weatherTemplate = `Погода в %s сегодня 🌍
Восход  %s
Закат    %s
Температура 🌡 воздуха💨 %s C°
Средний поток ветра %s км/ч
UV индекс %s`

func (weatherModule) Render(ctx context.Context, e *Env) Block {
	weatherMu.Lock()
	defer weatherMu.Unlock()

	var w Weather
	at, ok := e.St.load("weather", &w)
	// Данные для другого места (координаты поменяли) использовать нельзя.
	if ok && e.St.meta("weather") != weatherMeta(e) {
		ok = false
	}
	stale := false
	if !ok || e.now().Sub(at) >= weatherTTL {
		nw, err := fetchWeather(ctx, e)
		switch {
		case err == nil:
			e.St.put("weather", nw, nw.At, weatherMeta(e), "")
			w, ok, stale = *nw, true, false
		default:
			log.Printf("Open-Meteo: %v", err)
			stale = ok // показываем прошлое значение, если оно есть
		}
	}

	rise, set, temp, wind, uvi := na, na, na, na, na
	var warn []string
	if ok {
		rise, set = w.Sunrise, w.Sunset
		t := math.Round(w.Temp)
		if t == 0 {
			t = 0 // убираем «-0»
		}
		temp = fmt.Sprintf("%+d", int(t))
		wind = fmt.Sprintf("%.0f", w.Wind)
		uvi = fmt.Sprintf("%.1f", w.UV)
		if stale {
			warn = append(warn, "погода может быть неактуальной")
		}
	} else {
		warn = append(warn, "погода недоступна")
	}
	return Block{Text: fmt.Sprintf(weatherTemplate, e.Cfg.Str("place"), rise, set, temp, wind, uvi), Warn: warn}
}

func fetchWeather(ctx context.Context, e *Env) (*Weather, error) {
	q := url.Values{}
	q.Set("latitude", e.Cfg.Str("lat"))
	q.Set("longitude", e.Cfg.Str("lon"))
	q.Set("current", "temperature_2m,wind_speed_10m,uv_index")
	q.Set("daily", "sunrise,sunset")
	q.Set("timezone", "Europe/Istanbul")
	q.Set("forecast_days", "1")
	u := weatherAPI + "?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && !sleep(ctx, 2*time.Second) {
			return nil, ctx.Err()
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := e.HC.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		var r struct {
			Current struct {
				Temp float64 `json:"temperature_2m"`
				Wind float64 `json:"wind_speed_10m"`
				UV   float64 `json:"uv_index"`
			} `json:"current"`
			Daily struct {
				Sunrise []string `json:"sunrise"`
				Sunset  []string `json:"sunset"`
			} `json:"daily"`
		}
		derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if derr != nil {
			lastErr = derr
			continue
		}
		if len(r.Daily.Sunrise) == 0 || len(r.Daily.Sunset) == 0 {
			lastErr = errors.New("в ответе нет восхода/заката")
			continue
		}
		return &Weather{
			Temp: r.Current.Temp, Wind: r.Current.Wind, UV: r.Current.UV,
			Sunrise: hhmm(r.Daily.Sunrise[0]), Sunset: hhmm(r.Daily.Sunset[0]),
			At: time.Now(),
		}, nil
	}
	return nil, lastErr
}

// weatherAPI — переменная, чтобы тесты могли подставить свой сервер.
var weatherAPI = "https://api.open-meteo.com/v1/forecast"

// hhmm: Open-Meteo отдаёт время без пояса ("2026-11-22T06:55"), поэтому time.RFC3339 здесь не подходит.
func hhmm(s string) string {
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, trt); err == nil {
		return t.Format("15:04")
	}
	if len(s) >= 16 {
		return s[11:16]
	}
	return s
}
