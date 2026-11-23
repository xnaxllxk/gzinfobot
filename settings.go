package main

// Настройки, которыми админ управляет из чата. Лежат в settings.json рядом с кэшем.
// Секреты (токен Telegram, ключ ИИ-шлюза, ID админа) сюда НЕ попадают: они остаются в
// /etc/gazibot.env и из чата не меняются — иначе взлом аккаунта админа отдал бы ключи.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxSources      = 10
	maxSourceFields = 8
)

// SrcField — одно число, которое пользовательский источник достаёт со страницы.
type SrcField struct {
	Key   string  `json:"key"`   // латиница, ключ в ответе ИИ
	Label string  `json:"label"` // подпись в отчёте и подсказка для ИИ
	Min   float64 `json:"min"`   // допустимый диапазон: всё за его пределами отклоняется
	Max   float64 `json:"max"`
}

// ModSettings — настройки одного модуля.
type ModSettings struct {
	Name    string            `json:"name,omitempty"` // только у пользовательских источников
	Enabled bool              `json:"enabled"`
	Values  map[string]string `json:"values,omitempty"`
	Fields  []SrcField        `json:"fields,omitempty"` // только у пользовательских источников
}

type Settings struct {
	Schedule []string                `json:"schedule"` // времена обновления по Стамбулу, ЧЧ:ММ
	Modules  map[string]*ModSettings `json:"modules"`

	// Плановая отправка отчёта: времена по Стамбулу (пусто = выключено) и чат (0 = чат админа).
	ReportTimes []string `json:"report_times,omitempty"`
	ReportChat  int64    `json:"report_chat,omitempty"`
	Sources  []*ModSettings          `json:"sources,omitempty"`
}

var (
	reName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,15}$`)
	reKey  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
)

// Слова, которые нельзя использовать как имя источника.
var reserved = map[string]bool{"schedule": true, "all": true, "settings": true, "set": true, "enabled": true}

func (s *Settings) clone() *Settings {
	b, _ := json.Marshal(s)
	var c Settings
	_ = json.Unmarshal(b, &c)
	return &c
}

func (s *Settings) find(name string) *ModSettings {
	if m, ok := s.Modules[name]; ok {
		return m
	}
	for _, m := range s.Sources {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func (s *Settings) isSource(name string) bool {
	for _, m := range s.Sources {
		if m.Name == name {
			return true
		}
	}
	return false
}

// normalize проверяет всё целиком и приводит значения к каноническому виду.
// Вызывается при загрузке файла и перед каждым сохранением из чата.
func (s *Settings) normalize() error {
	slots, err := parseSlots(strings.Join(s.Schedule, ","))
	if err != nil {
		return err
	}
	s.Schedule = formatSlots(slots)

	if len(s.ReportTimes) > 0 {
		rs, err := parseSlots(strings.Join(s.ReportTimes, ","))
		if err != nil {
			return fmt.Errorf("report_times: %w", err)
		}
		s.ReportTimes = formatSlots(rs)
	}

	if s.Modules == nil {
		s.Modules = map[string]*ModSettings{}
	}
	for _, f := range builtins {
		name := f().Name()
		if s.Modules[name] == nil {
			s.Modules[name] = &ModSettings{Enabled: true}
		}
	}
	reg := buildRegistry(s)
	for name := range s.Modules {
		if !reg.isBuiltin(name) {
			return fmt.Errorf("неизвестный модуль %q в modules", name)
		}
	}

	if len(s.Sources) > maxSources {
		return fmt.Errorf("источников не больше %d", maxSources)
	}
	seen := map[string]bool{}
	for _, src := range s.Sources {
		if !reName.MatchString(src.Name) || reserved[src.Name] || reg.isBuiltin(src.Name) {
			return fmt.Errorf("недопустимое имя источника %q (латиница, цифры, _; 2–16 символов; не занято)", src.Name)
		}
		if seen[src.Name] {
			return fmt.Errorf("источник %q указан дважды", src.Name)
		}
		seen[src.Name] = true
		if err := checkSrcFields(src.Fields); err != nil {
			return fmt.Errorf("источник %s: %w", src.Name, err)
		}
	}

	for _, m := range reg.list {
		ms := s.find(m.Name())
		if ms == nil {
			continue
		}
		fields := map[string]Field{}
		for _, f := range m.Fields() {
			fields[f.Key] = f
		}
		for k, v := range ms.Values {
			f, ok := fields[k]
			if !ok {
				return fmt.Errorf("%s: неизвестная настройка %q", m.Name(), k)
			}
			nv, err := f.check(v)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", m.Name(), k, err)
			}
			ms.Values[k] = nv
		}
		if s.isSource(m.Name()) && ms.Enabled {
			cfg := newCfg(m.Fields(), ms.Values)
			if cfg.Str("url") == "" || cfg.Str("anchor") == "" || len(ms.Fields) == 0 {
				return fmt.Errorf("источник %s нельзя включить: нужны url, anchor и хотя бы одно поле (/source field)", m.Name())
			}
		}
	}
	return nil
}

func checkSrcFields(fs []SrcField) error {
	if len(fs) > maxSourceFields {
		return fmt.Errorf("полей не больше %d", maxSourceFields)
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if !reKey.MatchString(f.Key) {
			return fmt.Errorf("недопустимый ключ поля %q (латиница, цифры, _)", f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("поле %q указано дважды", f.Key)
		}
		seen[f.Key] = true
		if strings.TrimSpace(f.Label) == "" || len([]rune(f.Label)) > 40 {
			return fmt.Errorf("поле %s: подпись от 1 до 40 символов", f.Key)
		}
		for _, r := range f.Label {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("поле %s: управляющие символы в подписи", f.Key)
			}
		}
		if math.IsNaN(f.Min) || math.IsNaN(f.Max) || math.IsInf(f.Min, 0) || math.IsInf(f.Max, 0) || f.Min >= f.Max {
			return fmt.Errorf("поле %s: нужен диапазон min < max", f.Key)
		}
	}
	return nil
}

// set меняет одну настройку: «schedule» или «модуль.поле». Возвращает имя затронутого модуля
// ("" для расписания). Полная проверка — в normalize, её вызывает вызывающий код.
func (s *Settings) set(key, val string) (string, error) {
	if key == "schedule" {
		slots, err := parseSlots(val)
		if err != nil {
			return "", err
		}
		s.Schedule = formatSlots(slots)
		return "", nil
	}
	switch key {
	case "report_times":
		if v := strings.ToLower(strings.TrimSpace(val)); v == "off" || v == "выкл" || v == "-" {
			s.ReportTimes = nil
			return "", nil
		}
		slots, err := parseSlots(val)
		if err != nil {
			return "", err
		}
		s.ReportTimes = formatSlots(slots)
		return "", nil
	case "report_chat":
		id, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || id == 0 {
			return "", fmt.Errorf("report_chat: нужен числовой ID чата или слово here (текущий чат)")
		}
		s.ReportChat = id
		return "", nil
	}
	name, fkey, ok := strings.Cut(key, ".")
	if !ok {
		return "", fmt.Errorf("ключ должен быть «schedule» или «модуль.поле», например weather.lat")
	}
	ms := s.find(name)
	if ms == nil {
		return "", fmt.Errorf("нет модуля %q (список: /settings)", name)
	}
	reg := buildRegistry(s)
	m := reg.by[name]
	for _, f := range m.Fields() {
		if f.Key == fkey {
			nv, err := f.check(val)
			if err != nil {
				return "", err
			}
			if ms.Values == nil {
				ms.Values = map[string]string{}
			}
			ms.Values[fkey] = nv
			return name, nil
		}
	}
	var keys []string
	for _, f := range m.Fields() {
		keys = append(keys, f.Key)
	}
	return "", fmt.Errorf("у модуля %s нет настройки %q (есть: %s)", name, fkey, strings.Join(keys, ", "))
}

// loadSettings читает settings.json. Если файла нет, возвращает os.ErrNotExist (обёрнутую).
func loadSettings(path string) (*Settings, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var s Settings
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

func (s *Settings) save(path string) error {
	if path == "" {
		return errors.New("путь к settings.json не задан")
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
