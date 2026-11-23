package main

// Плановая отправка отчёта: в заданные времена (по Стамбулу) бот сам присылает /report в чат.
// Отчёт собирается из кэша, поэтому обновление данных (schedule) лучше ставить заранее,
// например обновление в 09:40, отчёт в 10:00.

import (
	"context"
	"log"
	"time"
)

// reportGrace — сколько после наступившего времени ещё можно отправить отчёт. Нужно, чтобы
// короткий перезапуск бота не терял отчёт, а долгий простой не слал его с большим опозданием.
const reportGrace = 15 * time.Minute

// reportDue: пора ли слать отчёт. last — время, на которое отчёт уже отправлен.
// Возвращает время слота, за который отправляем.
func reportDue(slots []slot, last, now time.Time) (time.Time, bool) {
	if len(slots) == 0 {
		return time.Time{}, false
	}
	ls := lastSlot(slots, now)
	if ls.IsZero() || !ls.After(last) || now.Sub(ls) > reportGrace {
		return time.Time{}, false
	}
	return ls, true
}

func (a *app) reportScheduler(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reportTick(ctx, time.Now())
		}
	}
}

func (a *app) reportTick(ctx context.Context, now time.Time) {
	st := a.cur.Load()
	if len(st.set.ReportTimes) == 0 {
		return
	}
	slots, err := parseSlots(joinSlots(st.set.ReportTimes))
	if err != nil {
		return
	}
	when, due := reportDue(slots, a.st.lastReport(), now)
	if !due {
		return
	}
	chat := st.set.ReportChat
	if chat == 0 {
		chat = a.cfg.adminChat
	}
	// Отмечаем до отправки: при сбое не будем повторять каждые 20 секунд, админ получит одно уведомление.
	a.st.markReport(when)
	if chat == 0 {
		log.Println("плановый отчёт: не задан чат (/set report_chat here)")
		return
	}
	if err := a.tg.send(ctx, chat, a.report(ctx)); err != nil {
		log.Printf("плановый отчёт: %v", err)
		a.notify(ctx, "⚠️ Не удалось отправить плановый отчёт: "+err.Error())
		return
	}
	log.Printf("плановый отчёт отправлен (слот %s)", when.In(trt).Format("02.01 15:04"))
}
