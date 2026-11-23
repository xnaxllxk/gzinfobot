// gazibot — лёгкий Telegram-бот для чата: /report → курсы Ziraat, погода, цены на топливо
// и любые другие модули, которые подключит админ.
//
// Схема: Go делает всю механическую работу (скачивание, чистка HTML, проверки, кэш, Telegram),
// ИИ только читает вырезанный фрагмент страницы и возвращает числа в JSON. Данные обновляются
// по расписанию, а /report отвечает мгновенно из кэша.
//
// Модульность: каждая функция — модуль с единым интерфейсом (module.go). Настройки лежат в
// settings.json и меняются из Telegram админ-командами (admin.go), секреты — в окружении.
//
// Файлы:
//
//	module.go, registry.go     интерфейс модуля и список модулей
//	mod_rates.go, mod_fuel.go, mod_weather.go   встроенные модули
//	mod_source.go              универсальный источник, создаётся из чата
//	settings.go, fields.go     настройки, их описание и проверка
//	admin.go                   админ-команды
//	netsafe.go                 защита ссылок от обращений во внутреннюю сеть
//	config.go (секреты) · app.go (связка) · schedule.go (расписание) · store.go (кэш)
//	scrape.go (страницы и числа) · ai.go (ИИ-шлюз) · report.go (сообщение) · telegram.go (бот)
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	set, err := initSettings(cfg)
	if err != nil {
		log.Fatalf("настройки: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := newApp(cfg, set)
	if err != nil {
		log.Fatal(err)
	}
	if fi, err := os.Stat(cfg.settingsPath); err == nil {
		a.fileMod = fi.ModTime()
	}

	var me struct {
		Username string `json:"username"`
	}
	if err := a.tg.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		log.Fatalf("Telegram недоступен или токен неверный: %v", err)
	}
	a.me = me.Username
	log.Printf("бот @%s запущен (ИИ: %s, модель: %s, настройки: %s)", a.me, cfg.aiURL, cfg.aiModel, cfg.settingsPath)
	if cfg.adminUser == 0 {
		log.Println("ВНИМАНИЕ: ADMIN_USER_ID не задан — админ-команды (/settings, /set, /refresh …) отключены")
	}

	if a.needsCatchUp(time.Now()) {
		log.Println("пропущено плановое обновление или кэш пуст — обновляю сейчас")
		go a.runRefresh(ctx)
	}
	go a.scheduler(ctx)
	go a.watchSettings(ctx)

	a.poll(ctx)
	log.Println("остановлен")
}
