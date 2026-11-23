package main

// config — только то, что приходит из окружения (/etc/gazibot.env): секреты и пути.
// Всё остальное (время, координаты, ссылки, включённые модули) живёт в settings.json
// и меняется из чата, см. settings.go.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// ИИ-шлюз: локальный freellmpool (OpenAI-совместимый прокси, сам переключается между
	// бесплатными провайдерами). Любой другой OpenAI-совместимый адрес тоже подойдёт.
	defaultAIURL    = "http://127.0.0.1:8080/v1/chat/completions"
	defaultAIModel  = "auto" // псевдоним freellmpool: шлюз сам выбирает модель
	defaultTelegram = "https://api.telegram.org"
	defaultTimes    = "08:00,18:00" // время обновлений по Стамбулу
)

// В Турции нет перехода на летнее время (UTC+3 круглый год), поэтому tzdata на сервере не нужна.
var trt = time.FixedZone("TRT", 3*60*60)

type config struct {
	tgToken      string
	tgBase       string
	aiURL        string // куда отправляются запросы к ИИ (см. checkAIURL)
	aiKey        string // необязателен: нужен, только если шлюз запущен с FREELLMPOOL_PROXY_KEY
	aiModel      string
	aiJSON       bool // AI_JSON_MODE=1: просить у шлюза строгий JSON (см. aiClient.jsonMode)
	cachePath    string
	settingsPath string
	adminUser    int64 // кому разрешено менять настройки (user ID, а не чат: иначе сможет любой участник группы)
	adminChat    int64 // куда слать уведомления о сбоях
	adminOnly    bool  // ADMIN_ONLY=1: сообщения не от админа отбрасываются сразу, без обработки

	// Старые переменные окружения: используются только как начальные значения, когда
	// settings.json ещё не существует. Дальше источник правды — settings.json.
	seedZiraatURL string
	seedFuelURL   string
	seedChannel   string
	seedTimes     string
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string) (int64, error) {
	v := env(key, "")
	if v == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return id, nil
}

func loadConfig() (config, error) {
	c := config{
		tgToken:       env("TELEGRAM_TOKEN", ""),
		tgBase:        defaultTelegram,
		aiURL:         env("AI_URL", defaultAIURL),
		aiKey:         env("AI_API_KEY", ""),
		aiModel:       env("AI_MODEL", defaultAIModel),
		aiJSON:        env("AI_JSON_MODE", "") == "1",
		adminOnly:     env("ADMIN_ONLY", "") == "1",
		cachePath:     env("CACHE_FILE", "cache.json"),
		seedZiraatURL: env("ZIRAAT_URL", defaultZiraatURL),
		seedFuelURL:   env("FUEL_URL", defaultFuelURL),
		seedChannel:   strings.ToLower(env("ZIRAAT_CHANNEL", "gise")),
		seedTimes:     env("REFRESH_TIMES", defaultTimes),
	}
	c.settingsPath = env("SETTINGS_FILE", filepath.Join(filepath.Dir(c.cachePath), "settings.json"))
	if c.tgToken == "" {
		return c, errors.New("TELEGRAM_TOKEN не задан")
	}
	if err := checkAIURL(c.aiURL, c.aiKey != ""); err != nil {
		return c, err
	}
	var err error
	if c.adminChat, err = envInt("ADMIN_CHAT_ID"); err != nil {
		return c, err
	}
	if c.adminUser, err = envInt("ADMIN_USER_ID"); err != nil {
		return c, err
	}
	// Обратная совместимость: в личном чате ID чата равен ID пользователя.
	if c.adminUser == 0 && c.adminChat > 0 {
		c.adminUser = c.adminChat
	}
	if c.adminChat == 0 && c.adminUser != 0 {
		c.adminChat = c.adminUser
	}
	return c, nil
}

// checkAIURL защищает ключ от утечки открытым текстом: http разрешён только для петли
// (127.0.0.1, ::1, localhost), всё остальное — только https и без логина в адресе.
func checkAIURL(raw string, hasKey bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("AI_URL: неверный адрес %q", raw)
	}
	if u.User != nil {
		return errors.New("AI_URL: логин и пароль в адресе не допускаются (ключ задаётся в AI_API_KEY)")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost":
			return nil
		}
		return errors.New("AI_URL: http разрешён только для 127.0.0.1/localhost, для остального нужен https")
	}
	return errors.New("AI_URL: поддерживаются только http (локально) и https")
}

// seedSettings строит начальный settings.json из старых переменных окружения.
func (c config) seedSettings() *Settings {
	return &Settings{
		Schedule: strings.Split(c.seedTimes, ","),
		Modules: map[string]*ModSettings{
			"rates":   {Enabled: true, Values: map[string]string{"url": c.seedZiraatURL, "channel": c.seedChannel}},
			"weather": {Enabled: true, Values: map[string]string{"lat": gazipasaLat, "lon": gazipasaLon, "place": defaultPlace}},
			"fuel":    {Enabled: true, Values: map[string]string{"url": c.seedFuelURL}},
		},
	}
}
