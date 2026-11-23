package main

import (
	"strings"
	"testing"
	"time"
)

func TestReportDue(t *testing.T) {
	slots, err := parseSlots("10:00")
	if err != nil {
		t.Fatal(err)
	}
	d := func(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, trt) }
	yesterday := d(10, 0).AddDate(0, 0, -1)

	if _, ok := reportDue(slots, yesterday, d(9, 59)); ok {
		t.Error("до времени отчёт слать нельзя")
	}
	if s, ok := reportDue(slots, yesterday, d(10, 0)); !ok || !s.Equal(d(10, 0)) {
		t.Errorf("в 10:00 отчёт должен уйти, got %v %v", s, ok)
	}
	if _, ok := reportDue(slots, d(10, 0), d(10, 1)); ok {
		t.Error("повторно слать нельзя")
	}
	if _, ok := reportDue(slots, yesterday, d(10, 16)); ok {
		t.Error("слишком поздно, не шлём")
	}
	if _, ok := reportDue(nil, yesterday, d(10, 0)); ok {
		t.Error("без расписания не шлём")
	}
}

func TestSetReportKeys(t *testing.T) {
	s := &Settings{Schedule: []string{"09:40"}}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.set("report_times", "21:30, 10:00"); err != nil {
		t.Fatal(err)
	}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.ReportTimes, ","); got != "10:00,21:30" {
		t.Errorf("report_times = %q", got)
	}
	if _, err := s.set("report_times", "25:99"); err == nil {
		t.Error("ожидалась ошибка формата времени")
	}
	if _, err := s.set("report_chat", "abc"); err == nil {
		t.Error("ожидалась ошибка для нечислового чата")
	}
	if _, err := s.set("report_chat", "-1001234567"); err != nil || s.ReportChat != -1001234567 {
		t.Errorf("report_chat не сохранился: %v %d", err, s.ReportChat)
	}
	if _, err := s.set("report_times", "off"); err != nil || len(s.ReportTimes) != 0 {
		t.Errorf("off должен выключать: %v %v", err, s.ReportTimes)
	}
}
