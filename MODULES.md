# Модули gazibot

Каждая функция бота — модуль с одним интерфейсом (`module.go`). Отчёт `/report` — это цикл по
включённым модулям, расписание вызывает `Refresh` у каждого. Остальной код модулей не знает.

## Три способа добавить функцию

1. **Из чата, без кода** — «число на странице»: `/source add …` (см. ниже и `/admin`).
2. **Новый модуль на Go** — для всего, что не сводится к «достань числа со страницы»
   (API с JSON, времена намаза, свой формат вывода).
3. **Поменять существующий** — файлы `mod_rates.go`, `mod_fuel.go`, `mod_weather.go`.

## Свой модуль на Go: один файл и одна строка

```go
// mod_gold.go
type goldModule struct{}

func (goldModule) Name() string  { return "gold" }          // ключ в settings.json, кэше и командах
func (goldModule) Title() string { return "Курс золота" }   // для сообщений админу

// Настройки: из них строится проверка /set и список в /settings.
func (goldModule) Fields() []Field {
	return []Field{{Key: "url", Label: "Страница", Kind: KURL, Default: "https://…"}}
}

// Получить данные и положить в e.St. Вернуть ошибку = «не получилось».
func (goldModule) Refresh(ctx context.Context, e *Env) error {
	text, err := fetchText(ctx, e.HC, e.Cfg.Str("url")) // e.HC не ходит во внутреннюю сеть
	// ... разобрать, проверить, затем:
	e.St.put("gold", myData, time.Now(), "", "")
	return err
}

// Собрать блок отчёта из того, что лежит в e.St. Без сети.
func (goldModule) Render(ctx context.Context, e *Env) Block {
	var d myData
	at, ok := e.St.load("gold", &d)
	if !ok { return Block{Warn: []string{"золото недоступно"}} }
	return Block{Text: fmt.Sprintf("🥇 Золото %.2f", d.Price)}
}
```

Затем в `registry.go` одна строка в `builtins`:

```go
func() Module { return goldModule{} },
```

Порядок в списке = порядок блоков в отчёте. Что получаете сразу: `/enable gold`,
`/disable gold`, `/test gold`, `/set gold.url …` с проверкой типов, расписание, повторы при
сбое, уведомление админу, перечитывание `settings.json`. Паника в модуле не роняет бота.

Что можно использовать из пакета: `fetchText`, `window`, `grounded`, `parseTR`
(`scrape.go`), `e.AI.ask` (ИИ-шлюз), `e.St.jumpOK` (защита от скачков >15%),
`sameHash`/`touch` (не тратить токены, если страница не изменилась).

Тесты модулей пишутся без сети: см. `helpers_test.go` (`fakeSite`, `fakeAI`, `newTestApp`).

## Типы настроек (`Field.Kind`)

| Kind | Проверка |
|---|---|
| `KString` | текст до `Max` символов, без управляющих |
| `KURL` | только `https`, без логина, порт 443, не localhost / внутренние адреса |
| `KFloat`, `KInt` | число в диапазоне `Min…Max` |
| `KEnum` | одно из `Enum` |

## Настройки и секреты

* `/etc/gazibot.env` — токен Telegram, `ADMIN_USER_ID`, адрес и (необязательно) ключ ИИ-шлюза. Из чата не меняются.
* `settings.json` (рядом с кэшем) — расписание, модули, их значения, свои источники.
  Меняется командами или руками; бот подхватывает файл за ≤30 секунд, ошибочный файл
  отклоняет и сообщает админу.

## Свой источник из чата

```
/source add gold https://example.com/altin Gram Altın
/source field gold gram 1000 100000 Золото, грамм
/test gold          # покажет, что бот нашёл на странице; ничего не сохраняет
/enable gold        # включится только если проверка прошла
/set gold.title Золото
```

Число принимается, только если оно дословно есть в вырезанном фрагменте страницы и попало в
заданный диапазон. Скачок больше 15% к прошлому значению ждёт подтверждения следующим чтением.
Любое изменение включённого модуля сначала проверяется на новых настройках и сохраняется только
при успехе (`/set -f …` — сохранить без проверки).
