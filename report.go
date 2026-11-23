package main

// Итоговое сообщение: блоки включённых модулей по порядку, затем пометки о проблемах.
// Оформление каждого блока задаёт сам модуль.

import (
	"context"
	"strings"
)

const na = "н/д"

func (a *app) report(ctx context.Context) string {
	st := a.cur.Load()
	var blocks, warns []string
	for _, m := range st.enabled() {
		b := safeRender(ctx, m, a.env(st, m, a.st))
		if b.Text != "" {
			blocks = append(blocks, b.Text)
		}
		warns = append(warns, b.Warn...)
	}
	if len(blocks) == 0 && len(warns) == 0 {
		return "Нет включённых модулей. Админ может включить их командой /enable."
	}
	out := strings.Join(blocks, "\n\n")
	if len(warns) > 0 {
		if out != "" {
			out += "\n\n"
		}
		out += "⚠️ " + strings.Join(warns, "; ")
	}
	return out
}
