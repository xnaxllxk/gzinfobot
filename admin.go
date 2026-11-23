package main

// Админ-команды в Telegram: посмотреть и изменить настройки, включить и выключить модули,
// проверить модуль, добавить источник без правки кода.
//
// Правила безопасности:
//   - команды принимаются только от user ID админа (см. handle);
//   - каждое значение проверяется до сохранения (fields.go, netsafe.go);
//   - изменение включённого модуля сначала проверяется «вхолостую» на новых настройках и
//     сохраняется, только если проверка прошла (обойти можно явно флагом -f).

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const adminHelp = `🛠 Команды админа

/settings — все текущие настройки
/set ключ значение — изменить, например:
  /set schedule 08:00,18:00
  /set weather.lat 36.27
  /set rates.channel internet
  /set fuel.url https://…
  (добавьте -f после /set, чтобы сохранить без проверки)
/enable имя, /disable имя — включить или выключить модуль
/test имя — прогнать модуль и показать результат, ничего не сохраняя
/refresh — обновить все данные сейчас

Расписание (время Стамбула):
/set schedule 09:40 — когда обновлять данные; можно несколько: 06:00,12:00,18:00
/set report_times 10:00 — когда сам присылать отчёт; можно несколько; off — выключить
/set report_chat here — куда присылать (here = этот чат, либо числовой ID)
/status — жив ли ИИ-шлюз и когда обновлялись модули

Свои источники (без правки кода):
/source — список источников
/source add имя https://ссылка слово-якорь
/source field имя ключ мин макс подпись…
/source unfield имя ключ
/source del имя
Затем /test имя и /enable имя. Включить источник можно только после успешной проверки.`

func (a *app) isAdmin(userID int64) bool {
	return a.cfg.adminUser != 0 && userID == a.cfg.adminUser
}

func (a *app) say(ctx context.Context, chat int64, text string) {
	if err := a.tg.send(ctx, chat, text); err != nil {
		log.Printf("отправка: %v", err)
	}
}

func cutSpace(s string) (head, tail string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// admin выполняет админ-команду и возвращает текст ответа.
func (a *app) admin(ctx context.Context, chat int64, cmd, rest string) string {
	switch cmd {
	case "/admin":
		return adminHelp
	case "/settings":
		return a.describe()
	case "/status":
		return a.status(ctx)
	case "/set":
		return a.cmdSet(ctx, chat, rest)
	case "/enable":
		return a.cmdEnable(ctx, chat, rest, true)
	case "/disable":
		return a.cmdEnable(ctx, chat, rest, false)
	case "/test":
		return a.cmdTest(ctx, chat, rest)
	case "/source":
		return a.cmdSource(ctx, chat, rest)
	case "/refresh":
		go func() {
			msg := "Обновление уже идёт."
			if a.runRefresh(ctx) {
				msg = "Обновление завершено. Если были ошибки, они пришли отдельными сообщениями; результат — в /report."
			}
			a.say(ctx, chat, msg)
		}()
		return "Запускаю обновление данных…"
	}
	return ""
}

// describe показывает все настройки.
func (a *app) describe() string {
	st := a.cur.Load()
	var b strings.Builder
	next := nextSlot(st.slots, time.Now())
	fmt.Fprintf(&b, "⚙️ Настройки\n\nschedule = %s\n(время Стамбула, ближайшее обновление: %s)\n",
		strings.Join(st.set.Schedule, ", "), next.In(trt).Format("02.01 15:04"))
	rt, rc := "выключена", "чат админа"
	if len(st.set.ReportTimes) > 0 {
		rt = strings.Join(st.set.ReportTimes, ", ")
	}
	if st.set.ReportChat != 0 {
		rc = strconv.FormatInt(st.set.ReportChat, 10)
	}
	fmt.Fprintf(&b, "\nreport_times = %s (автоотправка отчёта, время Стамбула)\nreport_chat = %s\n", rt, rc)
	for _, m := range st.reg.list {
		ms := st.set.find(m.Name())
		if ms == nil {
			continue
		}
		state := "⏸ выключен"
		if ms.Enabled {
			state = "✅ включён"
		}
		upd := "данных нет"
		if at, ok := a.st.entryAt(m.Name()); ok {
			upd = "обновлён " + at.In(trt).Format("02.01 15:04")
		}
		fmt.Fprintf(&b, "\n%s · %s · %s\n", m.Name(), state, upd)
		cfg := newCfg(m.Fields(), ms.Values)
		for _, f := range m.Fields() {
			v := cfg.Str(f.Key)
			if v == "" {
				v = "— не задано"
			}
			fmt.Fprintf(&b, "  %s.%s = %s\n", m.Name(), f.Key, v)
		}
		for _, f := range ms.Fields {
			fmt.Fprintf(&b, "  поле %s: %s (допустимо %v…%v)\n", f.Key, f.Label, f.Min, f.Max)
		}
	}
	b.WriteString("\n/set ключ значение · /enable и /disable имя · /test имя · /admin — справка")
	return b.String()
}

// testTarget: какой модуль нужно проверить перед сохранением. Выключенные не проверяем.
func testTarget(set *Settings, mod string) string {
	if mod == "" {
		return ""
	}
	if ms := set.find(mod); ms != nil && ms.Enabled {
		return mod
	}
	return ""
}

// dryRun прогоняет модуль на указанных настройках и временном хранилище: боевой кэш не
// трогается. Возвращает то, что модуль показал бы в отчёте.
func (a *app) dryRun(ctx context.Context, set *Settings, name string) (string, *store, error) {
	st, err := newState(set)
	if err != nil {
		return "", nil, err
	}
	m := st.reg.by[name]
	if m == nil {
		return "", nil, fmt.Errorf("нет модуля %q", name)
	}
	tmp := newStore("")
	e := a.env(st, m, tmp)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := safeRefresh(ctx, m, e); err != nil {
		return "", nil, err
	}
	blk := safeRender(ctx, m, e)
	out := blk.Text
	if len(blk.Warn) > 0 {
		out += "\n⚠️ " + strings.Join(blk.Warn, "; ")
	}
	return out, tmp, nil
}

// commit проверяет, (при необходимости) тестирует и сохраняет кандидатные настройки.
// Вызывать под a.setMu.
func (a *app) commit(ctx context.Context, chat int64, cand *Settings, mod string, force bool, okMsg, forceHint string) string {
	if err := cand.normalize(); err != nil {
		return "❌ " + err.Error()
	}
	target := testTarget(cand, mod)
	var tmp *store
	var preview string
	if target != "" && !force {
		a.say(ctx, chat, "⏳ Проверяю модуль "+target+" на новых настройках…")
		out, t, err := a.dryRun(ctx, cand, target)
		if err != nil {
			msg := "❌ Не сохранено: проверка не прошла.\n" + err.Error() + "\n\nПрежние настройки остались."
			if forceHint != "" {
				msg += " " + forceHint
			}
			return msg
		}
		tmp, preview = t, out
	}
	if err := a.apply(cand); err != nil {
		return "❌ Не удалось сохранить: " + err.Error()
	}
	if tmp != nil {
		a.st.copyFrom(tmp, target) // свежий результат проверки сразу попадает в отчёт
	}
	if preview != "" {
		okMsg += "\n\nПроверка пройдена, так модуль выглядит в отчёте:\n" + preview
	}
	return okMsg
}

func (a *app) cmdSet(ctx context.Context, chat int64, rest string) string {
	force := false
	if h, t := cutSpace(rest); h == "-f" {
		force, rest = true, t
	}
	key, val := cutSpace(rest)
	if key == "" || val == "" {
		return "Формат: /set ключ значение\nНапример: /set weather.lat 36.27\nВсе ключи: /settings"
	}
	key = strings.ToLower(key)
	if key == "report_chat" && strings.EqualFold(val, "here") {
		val = strconv.FormatInt(chat, 10)
	}

	a.setMu.Lock()
	defer a.setMu.Unlock()
	cand := a.cur.Load().set.clone()
	mod, err := cand.set(key, val)
	if err != nil {
		return "❌ " + err.Error()
	}
	shown := strings.Join(cand.Schedule, ", ")
	switch key {
	case "report_times":
		if shown = strings.Join(cand.ReportTimes, ", "); shown == "" {
			shown = "выключена"
		}
	case "report_chat":
		shown = strconv.FormatInt(cand.ReportChat, 10)
	}
	if mod != "" {
		_, f, _ := strings.Cut(key, ".")
		shown = cand.find(mod).Values[f]
	}
	res := a.commit(ctx, chat, cand, mod, force, fmt.Sprintf("✅ %s = %s", key, shown),
		"Если уверены в значении, повторите с флагом: /set -f "+key+" …")
	if (key == "report_times" || key == "report_chat") && strings.HasPrefix(res, "✅") {
		a.st.markReport(time.Now()) // новое расписание не должно сразу «догонять» прошедшее время
	}
	return res
}

func (a *app) cmdEnable(ctx context.Context, chat int64, rest string, on bool) string {
	force := false
	if h, t := cutSpace(rest); h == "-f" {
		force, rest = true, t
	}
	name, _ := cutSpace(rest)
	name = strings.ToLower(name)
	if name == "" {
		if on {
			return "Формат: /enable имя (список: /settings)"
		}
		return "Формат: /disable имя (список: /settings)"
	}

	a.setMu.Lock()
	defer a.setMu.Unlock()
	cand := a.cur.Load().set.clone()
	ms := cand.find(name)
	if ms == nil {
		return "❌ нет модуля " + name + " (список: /settings)"
	}
	ms.Enabled = on
	if !on {
		return a.commit(ctx, chat, cand, "", true, "⏸ "+name+" выключен", "")
	}
	return a.commit(ctx, chat, cand, name, force, "✅ "+name+" включён", "Принудительно: /enable -f "+name)
}

func (a *app) cmdTest(ctx context.Context, chat int64, rest string) string {
	name, _ := cutSpace(rest)
	name = strings.ToLower(name)
	if name == "" {
		return "Формат: /test имя (список: /settings)"
	}
	st := a.cur.Load()
	if st.reg.by[name] == nil {
		return "❌ нет модуля " + name + " (список: /settings)"
	}
	a.say(ctx, chat, "⏳ Прогоняю "+name+"…")
	out, _, err := a.dryRun(ctx, st.set, name)
	if err != nil {
		return "❌ " + name + ": " + err.Error()
	}
	return "✅ " + name + " работает. Так он выглядит в отчёте (в кэш не сохранено):\n\n" + out
}

func parseNum(s string) (float64, error) {
	return strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
}

const sourceUsage = `Свои источники:
/source add имя https://ссылка слово-якорь
/source field имя ключ мин макс подпись…
/source unfield имя ключ
/source del имя

Пример:
/source add gold https://example.com/altin Gram Altın
/source field gold gram 1000 100000 Золото, грамм
/test gold
/enable gold`

func (a *app) cmdSource(ctx context.Context, chat int64, rest string) string {
	sub, args := cutSpace(rest)
	sub = strings.ToLower(sub)

	a.setMu.Lock()
	defer a.setMu.Unlock()
	cur := a.cur.Load().set

	switch sub {
	case "", "list":
		if len(cur.Sources) == 0 {
			return "Своих источников пока нет.\n\n" + sourceUsage
		}
		var b strings.Builder
		for _, s := range cur.Sources {
			state := "⏸"
			if s.Enabled {
				state = "✅"
			}
			fmt.Fprintf(&b, "%s %s · полей: %d\n", state, s.Name, len(s.Fields))
		}
		b.WriteString("\nПодробности: /settings\n\n" + sourceUsage)
		return b.String()

	case "add":
		name, r := cutSpace(args)
		link, anchor := cutSpace(r)
		name = strings.ToLower(name)
		if name == "" || link == "" || anchor == "" {
			return "Формат: /source add имя https://ссылка слово-якорь\n\n" + sourceUsage
		}
		if cur.find(name) != nil {
			return "❌ имя " + name + " уже занято"
		}
		cand := cur.clone()
		cand.Sources = append(cand.Sources, &ModSettings{Name: name, Values: map[string]string{}})
		if _, err := cand.set(name+".url", link); err != nil {
			return "❌ " + err.Error()
		}
		if _, err := cand.set(name+".anchor", anchor); err != nil {
			return "❌ " + err.Error()
		}
		return a.commit(ctx, chat, cand, "", true,
			fmt.Sprintf("✅ Источник %s создан, пока выключен.\nДалее: /source field %s ключ мин макс подпись, затем /test %s и /enable %s", name, name, name, name), "")

	case "field":
		name, r := cutSpace(args)
		key, r := cutSpace(r)
		minS, r := cutSpace(r)
		maxS, label := cutSpace(r)
		name = strings.ToLower(name)
		if name == "" || key == "" || minS == "" || maxS == "" || label == "" {
			return "Формат: /source field имя ключ мин макс подпись…\nПример: /source field gold gram 1000 100000 Золото, грамм"
		}
		if !cur.isSource(name) {
			return "❌ нет источника " + name
		}
		lo, err1 := parseNum(minS)
		hi, err2 := parseNum(maxS)
		if err1 != nil || err2 != nil {
			return "❌ мин и макс должны быть числами"
		}
		cand := cur.clone()
		ms := cand.find(name)
		nf := SrcField{Key: strings.ToLower(key), Label: label, Min: lo, Max: hi}
		replaced := false
		for i := range ms.Fields {
			if ms.Fields[i].Key == nf.Key {
				ms.Fields[i], replaced = nf, true
			}
		}
		if !replaced {
			ms.Fields = append(ms.Fields, nf)
		}
		return a.commit(ctx, chat, cand, name, false,
			fmt.Sprintf("✅ Поле %s.%s сохранено (%v…%v)", name, nf.Key, lo, hi), "")

	case "unfield":
		name, key := cutSpace(args)
		name, key = strings.ToLower(name), strings.ToLower(key)
		if name == "" || key == "" {
			return "Формат: /source unfield имя ключ"
		}
		if !cur.isSource(name) {
			return "❌ нет источника " + name
		}
		cand := cur.clone()
		ms := cand.find(name)
		var kept []SrcField
		for _, f := range ms.Fields {
			if f.Key != key {
				kept = append(kept, f)
			}
		}
		if len(kept) == len(ms.Fields) {
			return "❌ у источника " + name + " нет поля " + key
		}
		ms.Fields = kept
		if len(kept) == 0 && ms.Enabled {
			ms.Enabled = false // без полей источник работать не может
		}
		return a.commit(ctx, chat, cand, name, false, "✅ Поле "+name+"."+key+" удалено", "")

	case "del", "delete", "remove":
		name, _ := cutSpace(args)
		name = strings.ToLower(name)
		if !cur.isSource(name) {
			return "❌ нет источника " + name
		}
		cand := cur.clone()
		var kept []*ModSettings
		for _, s := range cand.Sources {
			if s.Name != name {
				kept = append(kept, s)
			}
		}
		cand.Sources = kept
		res := a.commit(ctx, chat, cand, "", true, "🗑 Источник "+name+" удалён", "")
		if strings.HasPrefix(res, "🗑") {
			a.st.drop(name)
			a.fmu.Lock()
			delete(a.fails, name)
			a.fmu.Unlock()
		}
		return res
	}
	return "❌ неизвестная подкоманда\n\n" + sourceUsage
}
