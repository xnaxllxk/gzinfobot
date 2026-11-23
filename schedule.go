package main

// Расписание: данные обновляются только в заданные времена (по умолчанию 08:00 и 18:00
// по Стамбулу), а не по запросам пользователей. /report всегда отвечает из кэша.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type slot struct{ h, m int }

// parseSlots разбирает строку вида "08:00,18:00".
func parseSlots(s string) ([]slot, error) {
	var out []slot
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t, err := time.Parse("15:04", p)
		if err != nil {
			return nil, fmt.Errorf("время %q: нужен формат ЧЧ:ММ, например 08:00", p)
		}
		out = append(out, slot{t.Hour(), t.Minute()})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не задано ни одного времени обновления")
	}
	if len(out) > 24 {
		return nil, fmt.Errorf("не больше 24 времён обновления в сутки")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].h*60+out[i].m < out[j].h*60+out[j].m })
	// повторы убираем
	uniq := out[:1]
	for _, x := range out[1:] {
		if x != uniq[len(uniq)-1] {
			uniq = append(uniq, x)
		}
	}
	return uniq, nil
}

func formatSlots(slots []slot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = fmt.Sprintf("%02d:%02d", s.h, s.m)
	}
	return out
}

func joinSlots(ss []string) string { return strings.Join(ss, ",") }

func at(now time.Time, dayOffset int, s slot) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+dayOffset, s.h, s.m, 0, 0, trt)
}

// lastSlot — самое позднее запланированное время, которое уже наступило.
func lastSlot(slots []slot, now time.Time) time.Time {
	now = now.In(trt)
	var best time.Time
	for d := -1; d <= 0; d++ {
		for _, s := range slots {
			if t := at(now, d, s); !t.After(now) && t.After(best) {
				best = t
			}
		}
	}
	return best
}

// nextSlot — ближайшее будущее время обновления.
func nextSlot(slots []slot, now time.Time) time.Time {
	now = now.In(trt)
	for d := 0; d <= 1; d++ {
		for _, s := range slots { // slots отсортированы
			if t := at(now, d, s); t.After(now) {
				return t
			}
		}
	}
	return time.Time{}
}

// maxGap — самый длинный промежуток между обновлениями (для пометок об устаревших данных).
func maxGap(slots []slot) time.Duration {
	if len(slots) == 1 {
		return 24 * time.Hour
	}
	var gap time.Duration
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, trt)
	for i := range slots {
		cur := at(base, 0, slots[i])
		var nxt time.Time
		if i+1 < len(slots) {
			nxt = at(base, 0, slots[i+1])
		} else {
			nxt = at(base, 1, slots[0])
		}
		if d := nxt.Sub(cur); d > gap {
			gap = d
		}
	}
	return gap
}

// needsCatchUp: запускать обновление при старте нужно, только если пропущено плановое время
// (бот был выключен) или данных нет совсем. Метка LastAttempt не даёт перезапускам
// бота (например, при сбое systemd) снова и снова дёргать сайты и API.
func (a *app) needsCatchUp(now time.Time) bool {
	st := a.cur.Load()
	last := a.st.lastAttempt()
	if last.Before(lastSlot(st.slots, now)) {
		return true
	}
	missing := false
	for _, m := range st.enabled() {
		if !a.st.has(m.Name()) {
			missing = true
		}
	}
	return missing && now.Sub(last) > 10*time.Minute
}

type job struct {
	key   string // имя модуля
	title string
	run   func(context.Context) error
}

// jobs — по одной задаче на каждый включённый модуль.
func (a *app) jobs(st *state) []job {
	var out []job
	for _, m := range st.enabled() {
		m := m
		out = append(out, job{key: m.Name(), title: m.Title(), run: func(ctx context.Context) error {
			return safeRefresh(ctx, m, a.env(st, m, a.st))
		}})
	}
	return out
}

func (a *app) runJobs(ctx context.Context, jobs []job) (failed []job, errs map[string]error) {
	errs = map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			jctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			err := j.run(jctx)
			mu.Lock()
			errs[j.key] = err
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	for _, j := range jobs {
		if errs[j.key] != nil {
			failed = append(failed, j)
		}
	}
	return failed, errs
}

// runRefresh обновляет все включённые модули. Сбойные повторяются через retryDelay
// (до maxRetries раз) — это не «дёрганье», а страховка на случай временной недоступности.
// Возвращает false, если обновление уже идёт.
func (a *app) runRefresh(ctx context.Context) bool {
	if !a.runMu.TryLock() {
		return false
	}
	defer a.runMu.Unlock()
	a.st.markAttempt(time.Now())

	st := a.cur.Load()
	titles := map[string]string{}
	final := map[string]error{}
	todo := a.jobs(st)
	for _, j := range todo {
		titles[j.key] = j.title
	}
	for try := 0; len(todo) > 0 && try <= a.maxRetries; try++ {
		if try > 0 {
			log.Printf("повтор через %v: %d модул.", a.retryDelay, len(todo))
			if !sleep(ctx, a.retryDelay) {
				return true
			}
		}
		failed, errs := a.runJobs(ctx, todo)
		for key, err := range errs {
			final[key] = err
		}
		todo = failed
	}
	for key, err := range final {
		a.track(ctx, key, titles[key], err)
	}
	return true
}

func (a *app) scheduler(ctx context.Context) {
	for {
		st := a.cur.Load()
		next := nextSlot(st.slots, time.Now())
		log.Printf("следующее обновление: %s", next.In(trt).Format("02.01 15:04"))
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-a.wake: // расписание изменилось — пересчитываем
			t.Stop()
			continue
		case <-t.C:
		}
		a.runRefresh(ctx)
	}
}

// track запоминает серию неудач и один раз сообщает админу (если он задан);
// когда модуль восстановился — сообщает об этом.
func (a *app) track(ctx context.Context, key, title string, err error) {
	a.fmu.Lock()
	defer a.fmu.Unlock()
	if err == nil {
		if a.fails[key] > 0 {
			a.notify(ctx, "✅ "+title+": снова работает")
		}
		a.fails[key] = 0
		return
	}
	a.fails[key]++
	log.Printf("%s: %v", title, err)
	if a.fails[key] == 1 {
		a.notify(ctx, fmt.Sprintf("⚠️ %s: не удалось обновить (после %d попыток): %v", title, a.maxRetries+1, err))
	}
}

func (a *app) notify(ctx context.Context, text string) {
	if a.cfg.adminChat == 0 {
		return
	}
	if err := a.tg.send(ctx, a.cfg.adminChat, text); err != nil {
		log.Printf("уведомление админу: %v", err)
	}
}
