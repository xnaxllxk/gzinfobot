package main

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
)

// builtins — список встроенных модулей. Порядок = порядок блоков в отчёте.
// Новый модуль подключается одной строкой здесь.
var builtins = []func() Module{
	func() Module { return ratesModule{} },
	func() Module { return weatherModule{} },
	func() Module { return fuelModule{} },
}

type Registry struct {
	list []Module
	by   map[string]Module
}

// buildRegistry собирает встроенные модули и пользовательские источники из настроек.
func buildRegistry(s *Settings) *Registry {
	r := &Registry{by: map[string]Module{}}
	for _, f := range builtins {
		r.add(f())
	}
	for _, ms := range s.Sources {
		r.add(newSourceModule(ms))
	}
	return r
}

func (r *Registry) add(m Module) {
	r.list = append(r.list, m)
	r.by[m.Name()] = m
}

func (r *Registry) isBuiltin(name string) bool {
	for _, f := range builtins {
		if f().Name() == name {
			return true
		}
	}
	return false
}

// Ошибка в одном модуле не должна ронять бота и отчёт целиком, поэтому вызовы обёрнуты.

func safeRefresh(ctx context.Context, m Module, e *Env) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("паника в модуле %s: %v\n%s", m.Name(), r, debug.Stack())
			err = fmt.Errorf("внутренняя ошибка модуля: %v", r)
		}
	}()
	return m.Refresh(ctx, e)
}

func safeRender(ctx context.Context, m Module, e *Env) (b Block) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("паника в модуле %s: %v\n%s", m.Name(), r, debug.Stack())
			b = Block{Warn: []string{m.Title() + ": внутренняя ошибка"}}
		}
	}()
	return m.Render(ctx, e)
}
