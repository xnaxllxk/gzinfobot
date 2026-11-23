package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	good := []string{"https://www.doviz.com/akaryakit-fiyatlari", "https://example.com/a?b=1", "https://93.184.216.34/x"}
	bad := []string{
		"http://example.com", "ftp://example.com", "https://localhost/x", "https://foo.local/x",
		"https://127.0.0.1/", "https://10.0.0.5/", "https://192.168.1.1/", "https://169.254.169.254/latest/meta-data",
		"https://[::1]/", "https://user:pw@example.com/", "https://example.com:8443/", "https://intranet/",
		"https://100.64.0.1/", "https://metadata.internal/", "javascript:alert(1)", "", "https://",
		"https://" + strings.Repeat("a", 600) + ".com",
	}
	for _, s := range good {
		if _, err := validateURL(s); err != nil {
			t.Errorf("%q должна проходить: %v", s, err)
		}
	}
	for _, s := range bad {
		if _, err := validateURL(s); err == nil {
			t.Errorf("%q должна отклоняться", s)
		}
	}
}

func TestPublicIP(t *testing.T) {
	for s, want := range map[string]bool{
		"8.8.8.8": true, "93.184.216.34": true, "127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false,
		"192.168.0.1": false, "169.254.169.254": false, "0.0.0.0": false, "::1": false, "fe80::1": false,
		"fc00::1": false, "::ffff:127.0.0.1": false, "2606:4700:4700::1111": true,
	} {
		if got := publicIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("publicIP(%s)=%v, ожидалось %v", s, got, want)
		}
	}
}

// Клиент для внешних страниц сам отказывается идти на внутренние адреса (в т.ч. по редиректу).
func TestSafeClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	if _, err := newSafeClient(5 * time.Second).Get(srv.URL); err == nil {
		t.Fatal("подключение к 127.0.0.1 должно быть запрещено")
	}
}

func TestFieldCheck(t *testing.T) {
	lat := Field{Key: "lat", Kind: KFloat, Min: -90, Max: 90}
	if v, err := lat.check("36,27"); err != nil || v != "36.27" {
		t.Errorf("lat: %q %v", v, err)
	}
	for _, s := range []string{"91", "abc", "NaN", "Inf", ""} {
		if _, err := lat.check(s); err == nil {
			t.Errorf("lat %q должна отклоняться", s)
		}
	}
	ch := Field{Key: "channel", Kind: KEnum, Enum: []string{"gise", "internet"}}
	if v, err := ch.check("INTERNET"); err != nil || v != "internet" {
		t.Errorf("enum: %q %v", v, err)
	}
	if _, err := ch.check("x"); err == nil {
		t.Error("enum: неизвестное значение должно отклоняться")
	}
	str := Field{Key: "place", Kind: KString, Max: 5}
	if _, err := str.check("слишком длинное"); err == nil {
		t.Error("строка длиннее Max должна отклоняться")
	}
	if _, err := str.check("a\x00b"); err == nil {
		t.Error("управляющие символы должны отклоняться")
	}
}

func TestParseSlots(t *testing.T) {
	got, err := parseSlots("18:00, 08:00,08:00")
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(formatSlots(got), ","); s != "08:00,18:00" {
		t.Errorf("получено %s", s)
	}
	for _, bad := range []string{"", "25:00", "8", "ab:cd", ",,"} {
		if _, err := parseSlots(bad); err == nil {
			t.Errorf("%q должно отклоняться", bad)
		}
	}
}

func TestSettingsSet(t *testing.T) {
	cfg := config{seedZiraatURL: defaultZiraatURL, seedFuelURL: defaultFuelURL, seedChannel: "gise", seedTimes: defaultTimes}
	s := cfg.seedSettings()
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	if mod, err := s.set("weather.lat", "36.5"); err != nil || mod != "weather" {
		t.Fatalf("%q %v", mod, err)
	}
	if s.Modules["weather"].Values["lat"] != "36.5" {
		t.Error("значение не записано")
	}
	for _, c := range [][2]string{
		{"weather.lat", "100"}, {"weather.nope", "1"}, {"nomod.x", "1"}, {"lat", "1"},
		{"fuel.url", "http://x.com"}, {"rates.url", "https://127.0.0.1/"}, {"schedule", "99:99"},
	} {
		if _, err := s.set(c[0], c[1]); err == nil {
			t.Errorf("/set %s %s должна отклоняться", c[0], c[1])
		}
	}
}

func TestSettingsNormalizeRejects(t *testing.T) {
	base := func() *Settings {
		cfg := config{seedZiraatURL: defaultZiraatURL, seedFuelURL: defaultFuelURL, seedChannel: "gise", seedTimes: defaultTimes}
		return cfg.seedSettings()
	}
	cases := map[string]func(s *Settings){
		"неизвестный модуль":    func(s *Settings) { s.Modules["nope"] = &ModSettings{} },
		"неизвестная настройка": func(s *Settings) { s.Modules["fuel"].Values["xxx"] = "1" },
		"имя занято встроенным": func(s *Settings) { s.Sources = []*ModSettings{{Name: "fuel"}} },
		"зарезервированное имя": func(s *Settings) { s.Sources = []*ModSettings{{Name: "schedule"}} },
		"плохое имя":            func(s *Settings) { s.Sources = []*ModSettings{{Name: "Bad Name"}} },
		"дубль имени":           func(s *Settings) { s.Sources = []*ModSettings{{Name: "gold"}, {Name: "gold"}} },
		"включён без полей": func(s *Settings) {
			s.Sources = []*ModSettings{{Name: "gold", Enabled: true, Values: map[string]string{"url": "https://example.com/", "anchor": "x"}}}
		},
		"диапазон min>=max": func(s *Settings) {
			s.Sources = []*ModSettings{{Name: "gold", Fields: []SrcField{{Key: "a", Label: "A", Min: 5, Max: 5}}}}
		},
		"дубль поля": func(s *Settings) {
			s.Sources = []*ModSettings{{Name: "gold", Fields: []SrcField{{Key: "a", Label: "A", Min: 1, Max: 5}, {Key: "a", Label: "B", Min: 1, Max: 5}}}}
		},
		"плохое время":    func(s *Settings) { s.Schedule = []string{"7"} },
		"ссылка не https": func(s *Settings) { s.Modules["rates"].Values["url"] = "http://example.com" },
		"слишком много сорсов": func(s *Settings) {
			for i := 0; i < 11; i++ {
				s.Sources = append(s.Sources, &ModSettings{Name: "s" + string(rune('a'+i)) + "x"})
			}
		},
	}
	for name, mutate := range cases {
		s := base()
		mutate(s)
		if err := s.normalize(); err == nil {
			t.Errorf("%s: normalize должна вернуть ошибку", name)
		}
	}
}

func TestSettingsFileStrictAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/settings.json"
	if err := os.WriteFile(p, []byte(`{"schedule":["08:00"],"modules":{},"lol":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(p); err == nil {
		t.Error("неизвестное поле верхнего уровня должно давать ошибку (опечатки не должны молча теряться)")
	}
	cfg := config{seedZiraatURL: defaultZiraatURL, seedFuelURL: defaultFuelURL, seedChannel: "internet", seedTimes: "09:00"}
	s := cfg.seedSettings()
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(p); err != nil {
		t.Fatal(err)
	}
	s2, err := loadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Modules["rates"].Values["channel"] != "internet" || s2.Schedule[0] != "09:00" {
		t.Errorf("настройки не сохранились: %+v", s2)
	}
}

func TestGroundedThousands(t *testing.T) {
	for text, v := range map[string]float64{
		"Gram Altın | 3.456,78 |": 3456.78, "price 3,456.78 TL": 3456.78, "kur 70,56": 70.56,
		"x 1 234,5 y": 1234.5, "fiyat 50.0448": 50.0448, "toplam 12.345": 12345,
	} {
		if !grounded(text, v) {
			t.Errorf("%v должно находиться в %q", v, text)
		}
	}
	if grounded("kur 70,56", 70.65) || grounded("fiyat 3.456,78", 3456.79) {
		t.Error("выдуманные числа не должны находиться")
	}
}

func TestSplitMessage(t *testing.T) {
	long := strings.Repeat("строка\n", 2000)
	parts := splitMessage(long, 4000)
	if len(parts) < 2 {
		t.Fatal("ожидалось несколько частей")
	}
	total := 0
	for _, p := range parts {
		if len([]rune(p)) > 4000 {
			t.Errorf("часть длиннее лимита: %d", len([]rune(p)))
		}
		total += strings.Count(p, "строка")
	}
	if total != 2000 {
		t.Errorf("потеряны строки: %d из 2000", total)
	}
}

func TestStoreLegacyMigration(t *testing.T) {
	p := t.TempDir() + "/cache.json"
	old := `{"rates":{"usd_buy":43.1,"usd_sell":43.2,"eur_buy":50.1,"eur_sell":50.3,"at":"2026-11-20T10:00:00Z"},
	"fuel":{"gasoline":70.5,"diesel":68.1,"lpg":30.2,"page_date":"20.11.2026","at":"2026-11-20T10:00:00Z"},
	"last_attempt":"2026-11-20T10:00:00Z"}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(p)
	var r Rates
	at, ok := s.load("rates", &r)
	if !ok || r.USDBuy != 43.1 || at.IsZero() {
		t.Fatalf("курсы не перенесены: %+v %v", r, ok)
	}
	var f Fuel
	if _, ok := s.load("fuel", &f); !ok || f.Diesel != 68.1 {
		t.Fatalf("топливо не перенесено: %+v", f)
	}
	if s.lastAttempt().IsZero() {
		t.Error("LastAttempt потерян")
	}
	// после сохранения старые поля верхнего уровня не записываются обратно
	s.markAttempt(time.Now())
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, p)), &top); err != nil {
		t.Fatal(err)
	}
	if _, bad := top["rates"]; bad {
		t.Error("старое поле rates не должно записываться")
	}
	if _, bad := top["fuel"]; bad {
		t.Error("старое поле fuel не должно записываться")
	}
	if _, good := top["entries"]; !good {
		t.Error("ожидался новый формат entries")
	}
}
