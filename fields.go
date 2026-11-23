package main

// Описание и проверка настроек модуля. Любое значение, пришедшее из чата или из settings.json,
// проходит через Field.check до сохранения.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Kind int

const (
	KString Kind = iota // текст, Max — максимальная длина (по умолчанию 100)
	KURL                // https-ссылка на внешний сайт
	KFloat              // число из диапазона Min..Max
	KInt                // целое из диапазона Min..Max
	KEnum               // одно из значений Enum
)

type Field struct {
	Key     string
	Label   string // для /settings
	Help    string // подсказка
	Kind    Kind
	Min     float64
	Max     float64
	Enum    []string
	Default string
}

// check проверяет значение и возвращает его в каноническом виде.
func (f Field) check(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("значение пустое")
	}
	switch f.Kind {
	case KURL:
		return validateURL(raw)
	case KFloat:
		v, err := strconv.ParseFloat(strings.Replace(raw, ",", ".", 1), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("%s: нужно число, получено %q", f.Key, raw)
		}
		if v < f.Min || v > f.Max {
			return "", fmt.Errorf("%s: допустимо от %v до %v", f.Key, f.Min, f.Max)
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case KInt:
		v, err := strconv.Atoi(raw)
		if err != nil {
			return "", fmt.Errorf("%s: нужно целое число, получено %q", f.Key, raw)
		}
		if float64(v) < f.Min || float64(v) > f.Max {
			return "", fmt.Errorf("%s: допустимо от %v до %v", f.Key, f.Min, f.Max)
		}
		return strconv.Itoa(v), nil
	case KEnum:
		l := strings.ToLower(raw)
		for _, e := range f.Enum {
			if e == l {
				return e, nil
			}
		}
		return "", fmt.Errorf("%s: допустимо только %s", f.Key, strings.Join(f.Enum, " / "))
	default:
		max := int(f.Max)
		if max == 0 {
			max = 100
		}
		if utf8.RuneCountInString(raw) > max {
			return "", fmt.Errorf("%s: не длиннее %d символов", f.Key, max)
		}
		for _, r := range raw {
			if unicode.IsControl(r) {
				return "", fmt.Errorf("%s: управляющие символы не допускаются", f.Key)
			}
		}
		return raw, nil
	}
}

// Cfg — настройки одного модуля с учётом значений по умолчанию.
type Cfg struct {
	fields map[string]Field
	vals   map[string]string
}

func newCfg(fs []Field, vals map[string]string) Cfg {
	m := make(map[string]Field, len(fs))
	for _, f := range fs {
		m[f.Key] = f
	}
	return Cfg{fields: m, vals: vals}
}

func (c Cfg) Str(key string) string {
	if v, ok := c.vals[key]; ok && v != "" {
		return v
	}
	return c.fields[key].Default
}

func (c Cfg) Float(key string) float64 {
	v, _ := strconv.ParseFloat(c.Str(key), 64)
	return v
}

func (c Cfg) Int(key string) int {
	v, _ := strconv.Atoi(c.Str(key))
	return v
}
