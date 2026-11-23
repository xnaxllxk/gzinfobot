package main

// /status для админа: жив ли бот, отвечает ли ИИ-шлюз и когда каждый модуль обновлялся.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var startedAt = time.Now()

// gatewayHealth проверяет /healthz и /readyz шлюза (адрес берётся из AI_URL, ключ не нужен).
func (a *app) gatewayHealth(ctx context.Context) string {
	u, err := url.Parse(a.cfg.aiURL)
	if err != nil {
		return "❌ неверный AI_URL"
	}
	get := func(path string) (int, error) {
		u.Path, u.RawPath, u.RawQuery = path, "", ""
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(c, http.MethodGet, u.String(), nil)
		if err != nil {
			return 0, err
		}
		resp, err := a.hc.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := get("/healthz"); err != nil || code != http.StatusOK {
		return "❌ недоступен (запущен ли freellmpool?)"
	}
	if code, err := get("/readyz"); err != nil || code != http.StatusOK {
		return "⚠️ запущен, но нет готовых провайдеров"
	}
	return "✅ работает"
}

func (a *app) status(ctx context.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Статус\n\nБот работает: %s\nИИ-шлюз: %s\n\n",
		time.Since(startedAt).Round(time.Second), a.gatewayHealth(ctx))
	st := a.cur.Load()
	stale := maxGap(st.slots) * 3 / 2
	a.fmu.Lock()
	defer a.fmu.Unlock()
	for _, m := range st.reg.list {
		ms := st.set.find(m.Name())
		if ms == nil || !ms.Enabled {
			continue
		}
		line := m.Name() + ": "
		at, ok := a.st.entryAt(m.Name())
		switch {
		case !ok:
			line += "❌ данных нет"
		case time.Since(at) > stale:
			line += "⚠️ устарели, " + at.In(trt).Format("02.01 15:04")
		default:
			line += "✅ " + at.In(trt).Format("02.01 15:04")
		}
		if n := a.fails[m.Name()]; n > 0 {
			line += fmt.Sprintf(" · сбоев подряд: %d", n)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
