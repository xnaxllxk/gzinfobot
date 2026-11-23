package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func adm(a *app, cmd, rest string) string {
	return a.admin(context.Background(), 42, cmd, rest)
}

func TestIsAdminByUserID(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	if !a.isAdmin(42) || a.isAdmin(43) || a.isAdmin(0) {
		t.Error("админом должен быть только user ID из окружения")
	}
	a.cfg.adminUser = 0
	if a.isAdmin(0) {
		t.Error("без заданного админа админ-команды отключены")
	}
}

func TestSetScheduleAppliesAndPersists(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	if out := adm(a, "/set", "schedule 07:30, 19:00"); !strings.HasPrefix(out, "✅") {
		t.Fatalf("%s", out)
	}
	st := a.cur.Load()
	if len(st.slots) != 2 || st.slots[0] != (slot{7, 30}) {
		t.Errorf("расписание не применилось: %+v", st.slots)
	}
	if !strings.Contains(readFile(t, a.cfg.settingsPath), "07:30") {
		t.Error("настройки не записаны в файл")
	}
	select {
	case <-a.wake:
	default:
		t.Error("планировщик не разбужен после смены расписания")
	}
	for _, bad := range []string{"schedule 25:00", "schedule abc", "schedule", "nomod.x 1", "weather.lat 100"} {
		if out := adm(a, "/set", bad); !strings.HasPrefix(out, "❌") && !strings.HasPrefix(out, "Формат") {
			t.Errorf("/set %s должна отклоняться: %s", bad, out)
		}
	}
	if len(a.cur.Load().slots) != 2 {
		t.Error("отклонённая команда не должна менять настройки")
	}
}

func TestSetWeatherCoordsChecksAndRefreshes(t *testing.T) {
	site := fakeSite(t, map[string]string{"/w.json": `{"current":{"temperature_2m":12.5,"wind_speed_10m":7,"uv_index":2},
		"daily":{"sunrise":["2026-11-23T06:58"],"sunset":["2026-11-23T17:19"]}}`})
	weatherAPI = site.URL + "/w.json"
	defer func() { weatherAPI = "https://api.open-meteo.com/v1/forecast" }()
	a, _ := newTestApp(t, "http://unused", site)

	out := adm(a, "/set", "weather.lat 36.5")
	if !strings.HasPrefix(out, "✅") || !strings.Contains(out, "06:58") {
		t.Fatalf("ожидалась успешная проверка с результатом погоды:\n%s", out)
	}
	if a.st.meta("weather") != "36.5,32.316" {
		t.Errorf("результат проверки должен попасть в боевой кэш с новыми координатами: %q", a.st.meta("weather"))
	}
	// Сайт недоступен → значение не сохраняется.
	weatherAPI = site.URL + "/missing.json"
	out = adm(a, "/set", "weather.lat 40")
	if !strings.HasPrefix(out, "❌") || !strings.Contains(out, "-f") {
		t.Errorf("при провале проверки ожидался отказ с подсказкой про -f:\n%s", out)
	}
	if got := a.cur.Load().set.Modules["weather"].Values["lat"]; got != "36.5" {
		t.Errorf("значение не должно меняться, а стало %s", got)
	}
	// С флагом -f сохраняется без проверки.
	if out = adm(a, "/set", "-f weather.lat 40"); !strings.HasPrefix(out, "✅") {
		t.Errorf("-f: %s", out)
	}
}

func TestEnableDisable(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	if out := adm(a, "/disable", "fuel"); !strings.Contains(out, "выключен") {
		t.Fatal(out)
	}
	if ms := a.cur.Load().set.Modules["fuel"]; ms.Enabled {
		t.Error("fuel должен быть выключен")
	}
	for _, m := range a.cur.Load().enabled() {
		if m.Name() == "fuel" {
			t.Error("выключенный модуль в списке включённых")
		}
	}
	if out := adm(a, "/disable", "nope"); !strings.HasPrefix(out, "❌") {
		t.Error(out)
	}
}

// Главный сценарий: админ добавляет новый источник из чата, без правки кода.
func TestSourceFlowFromChat(t *testing.T) {
	site := fakeSite(t, map[string]string{"/gold": goldPage})
	ai := fakeAI(t, func(string) string { return `{"gram": "3.456,78"}` })
	a, log := newTestApp(t, ai.URL, site)
	url := site.URL + "/gold"

	// 1. создание: источник выключен
	if out := adm(a, "/source", "add gold "+url+" Gram Altın"); !strings.HasPrefix(out, "✅") {
		t.Fatal(out)
	}
	if ms := a.cur.Load().set.find("gold"); ms == nil || ms.Enabled || ms.Values["anchor"] != "Gram Altın" {
		t.Fatalf("источник создан неверно: %+v", ms)
	}
	// 2. без полей включить нельзя
	if out := adm(a, "/enable", "gold"); !strings.HasPrefix(out, "❌") {
		t.Errorf("источник без полей включаться не должен: %s", out)
	}
	// 3. поле
	if out := adm(a, "/source", "field gold gram 1000 100000 Золото, грамм"); !strings.HasPrefix(out, "✅") {
		t.Fatal(out)
	}
	// 4. /test ничего не сохраняет
	if out := adm(a, "/test", "gold"); !strings.Contains(out, "3456.78") || !strings.HasPrefix(out, "✅") {
		t.Fatalf("/test: %s", out)
	}
	if a.st.has("gold") {
		t.Error("/test не должен писать в боевой кэш")
	}
	// 5. включение (с проверкой) → данные сразу в отчёте
	if out := adm(a, "/enable", "gold"); !strings.HasPrefix(out, "✅") {
		t.Fatalf("/enable: %s", out)
	}
	if !a.st.has("gold") {
		t.Error("результат проверки должен попасть в кэш")
	}
	if rep := a.report(context.Background()); !strings.Contains(rep, "Золото, грамм 3456.78") {
		t.Errorf("источник не попал в отчёт:\n%s", rep)
	}
	// 6. заголовок
	if out := adm(a, "/set", "gold.title Золото"); !strings.HasPrefix(out, "✅") {
		t.Fatal(out)
	}
	if rep := a.report(context.Background()); !strings.Contains(rep, "Золото\nЗолото, грамм 3456.78") {
		t.Errorf("заголовок не применился:\n%s", rep)
	}
	// 7. плохой якорь: проверка проваливается, настройка не меняется
	if out := adm(a, "/set", "gold.anchor Несуществующее"); !strings.HasPrefix(out, "❌") {
		t.Errorf("якорь, которого нет на странице, должен отклоняться: %s", out)
	}
	if a.cur.Load().set.find("gold").Values["anchor"] != "Gram Altın" {
		t.Error("якорь не должен был измениться")
	}
	// 8. настройки переживают перезапуск
	set, err := loadSettings(a.cfg.settingsPath)
	if err != nil || set.find("gold") == nil || !set.find("gold").Enabled || len(set.find("gold").Fields) != 1 {
		t.Errorf("источник не сохранился в файле: %v %+v", err, set.find("gold"))
	}
	// 9. удаление чистит и настройки, и кэш
	if out := adm(a, "/source", "del gold"); !strings.HasPrefix(out, "🗑") {
		t.Fatal(out)
	}
	if a.cur.Load().set.find("gold") != nil || a.st.has("gold") {
		t.Error("источник и его данные должны быть удалены")
	}
	if len(log.all()) == 0 {
		t.Error("ожидались промежуточные сообщения «Проверяю…»")
	}
}

func TestSourceSecurityAndValidation(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	strictURLs = true // здесь нужна боевая проверка ссылок
	defer func() { strictURLs = false }()
	for _, bad := range []string{
		"add gold http://example.com/x Anchor",           // не https
		"add gold https://127.0.0.1/x Anchor",            // loopback
		"add gold https://169.254.169.254/latest Anchor", // метаданные облака
		"add gold https://192.168.1.10/x Anchor",         // локальная сеть
		"add rates https://example.com/x Anchor",         // имя занято встроенным
		"add Bad-Name https://example.com/x Anchor",      // недопустимое имя
		"add gold https://example.com/x",                 // нет якоря
		"field nosuch k 1 2 label",                       // нет такого источника
	} {
		if out := adm(a, "/source", bad); !strings.HasPrefix(out, "❌") && !strings.HasPrefix(out, "Формат") {
			t.Errorf("/source %s должна отклоняться: %s", bad, out)
		}
	}
	if len(a.cur.Load().set.Sources) != 0 {
		t.Error("ни один из некорректных источников не должен был сохраниться")
	}
	if out := adm(a, "/set", "fuel.url https://localhost/x"); !strings.HasPrefix(out, "❌") {
		t.Errorf("localhost: %s", out)
	}
	if out := adm(a, "/set", "fuel.url https://10.0.0.1/x"); !strings.HasPrefix(out, "❌") {
		t.Errorf("10.0.0.1: %s", out)
	}
}

func TestSettingsFileHotReload(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	a.fileMod = fileMTime(t, a.cfg.settingsPath)

	// правка руками
	b := strings.Replace(readFile(t, a.cfg.settingsPath), "08:00", "06:15", 1)
	if err := os.WriteFile(a.cfg.settingsPath, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(a.cfg.settingsPath, future, future)
	a.reloadIfChanged(context.Background())
	if a.cur.Load().slots[0] != (slot{6, 15}) {
		t.Errorf("правка файла не подхвачена: %+v", a.cur.Load().slots)
	}

	// битый файл не ломает работающие настройки
	if err := os.WriteFile(a.cfg.settingsPath, []byte(`{"schedule": ["99:99"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Minute)
	_ = os.Chtimes(a.cfg.settingsPath, later, later)
	a.reloadIfChanged(context.Background())
	if a.cur.Load().slots[0] != (slot{6, 15}) {
		t.Error("битый файл не должен менять рабочие настройки")
	}
}

func fileMTime(t *testing.T, p string) time.Time {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

func TestDescribeListsEverything(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	out := adm(a, "/settings", "")
	for _, want := range []string{"schedule = 08:00, 18:00", "rates.url", "rates.channel = gise", "weather.lat = 36.269", "fuel.brand = Qplus", "включён"} {
		if !strings.Contains(out, want) {
			t.Errorf("в /settings нет %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, a.cfg.tgToken+":") || strings.Contains(out, "DEEPSEEK") {
		t.Error("секреты не должны показываться")
	}
}

func TestNeedsCatchUp(t *testing.T) {
	a, _ := newTestApp(t, "http://unused", nil)
	if !a.needsCatchUp(time.Now()) {
		t.Error("бот ни разу не обновлялся — нужно догнать")
	}
	a.st.markAttempt(time.Now())
	if a.needsCatchUp(time.Now()) {
		t.Error("перезапуск сразу после попытки не должен дёргать источники")
	}
	// данных у включённых модулей так и нет, прошло больше 10 минут
	if !a.needsCatchUp(time.Now().Add(11 * time.Minute)) {
		t.Error("нет данных и прошло больше 10 минут — нужно обновить")
	}
}

// Первый запуск после обновления: settings.json нет → создаётся из старых переменных окружения.
func TestInitSettingsSeedsFromEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := config{
		settingsPath: dir + "/settings.json", seedTimes: "07:00,21:00", seedChannel: "internet",
		seedZiraatURL: "https://example.com/rates", seedFuelURL: defaultFuelURL,
	}
	set, err := initSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(set.Schedule, ",") != "07:00,21:00" || set.Modules["rates"].Values["channel"] != "internet" ||
		set.Modules["rates"].Values["url"] != "https://example.com/rates" {
		t.Errorf("стартовые значения не перенесены: %+v", set)
	}
	if _, err := os.Stat(cfg.settingsPath); err != nil {
		t.Error("settings.json должен быть создан")
	}
	// второй запуск читает файл, а не окружение
	cfg.seedTimes = "01:00"
	set2, err := initSettings(cfg)
	if err != nil || set2.Schedule[0] != "07:00" {
		t.Errorf("повторный запуск должен читать файл: %v %v", err, set2.Schedule)
	}
}
