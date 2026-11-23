# Установка gazibot + freellmpool (Ubuntu, arm64)

Схема: `gazibot` (Go) → `127.0.0.1:8080` → `freellmpool` (Python) → бесплатные LLM-провайдеры.
Шлюз слушает только петлю. Ключа DeepSeek больше не нужно.

## 1. Telegram
1. @BotFather → `/newbot` → токен (`TELEGRAM_TOKEN`). Токен никому не показывайте.
2. @userinfobot → ваш числовой Id (`ADMIN_USER_ID`).

## 2. Ключи бесплатных провайдеров (по желанию, но советую 2–3)
Без ключей шлюз использует беcключевых провайдеров: они бесплатны, но менее стабильны и могут
логировать запросы. Для вашей задачи (публичный текст банка) приватность не критична, а вот
стабильность — да. Заведите, например, ключи Groq и Gemini на их сайтах
(подробности: docs/ACCOUNTS.md в репозитории freellmpool).

## 3. Установить freellmpool (нужен Python ≥ 3.11: `python3 --version`)
```
sudo apt install -y python3-venv
sudo useradd --system --home /var/lib/freellmpool --shell /usr/sbin/nologin freellmpool
sudo mkdir -p /opt/freellmpool && sudo python3 -m venv /opt/freellmpool/venv
sudo /opt/freellmpool/venv/bin/pip install freellmpool==0.13.0
```
Версия закреплена намеренно: обновляйте вручную, прочитав CHANGELOG.

### DeepSeek V4 Flash (free) через OrcaRouter
Ключ берётся на orcarouter.ai (`ORCAROUTER_API_KEY`). Провайдера нет во встроенном каталоге
freellmpool 0.13.0, поэтому добавлен свой `providers.toml`:
```
sudo install -D -m 644 providers.toml /etc/freellmpool/providers.toml
```
Ключ уходит только на `https://api.orcarouter.ai/v1`. Лимиты бесплатной модели сервис в открытом виде
не публикует: при превышении он отвечает 429, шлюз это учитывает и переходит на другие маршруты.
Сервис молодой и мне неизвестный: смотрите на его реальную работу в `journalctl -u freellmpool`.

## 4. Файлы с секретами
```
KEY=$(openssl rand -hex 32)
sudo cp freellmpool.env.example /etc/freellmpool.env && sudo nano /etc/freellmpool.env   # вставьте $KEY и ключи провайдеров
sudo cp gazibot.env.example   /etc/gazibot.env      && sudo nano /etc/gazibot.env        # токен, ADMIN_USER_ID, тот же $KEY в AI_API_KEY
sudo chmod 600 /etc/freellmpool.env /etc/gazibot.env
```
При обновлении старого бота: в `/etc/gazibot.env` удалите `DEEPSEEK_*`, добавьте `AI_API_KEY`.

## 5. Положить бота и запустить
```
scp gazibot-arm64 gazibot.service freellmpool.service ubuntu@СЕРВЕР:~
# на сервере:
mkdir -p ~/gazibot && mv ~/gazibot-arm64 ~/gazibot/ && chmod +x ~/gazibot/gazibot-arm64
sudo cp ~/freellmpool.service ~/gazibot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now freellmpool
sudo systemctl enable --now gazibot        # при обновлении: sudo systemctl restart gazibot
```

## 6. Проверка
```
curl -s http://127.0.0.1:8080/healthz        # {"status": "ok"}
curl -s http://127.0.0.1:8080/readyz         # 200, если есть хотя бы один готовый провайдер
sudo journalctl -u freellmpool -n 30
sudo journalctl -u gazibot -f                # «бот @имя запущен (ИИ: http://127.0.0.1:8080/…)»
```
В личном чате с ботом: `/admin`, `/settings`, `/test weather` (без ИИ), `/test rates`, `/test fuel`,
затем `/refresh` и `/report`.

## Если что-то не так
| Симптом | Причина |
|---|---|
| `ИИ-шлюз недоступен (запущен ли freellmpool?)` | сервис не запущен: `systemctl status freellmpool` |
| `ИИ-шлюз HTTP 401` | `AI_API_KEY` ≠ `FREELLMPOOL_PROXY_KEY` |
| `ИИ-шлюз HTTP 502` | все провайдеры сейчас недоступны/в лимите; бот повторит по расписанию, в отчёте останутся прошлые значения с пометкой. Добавьте ключи ещё одного провайдера |
| `ИИ-шлюз HTTP 503` | нет подходящих провайдеров (проверьте `AI_MODEL`; не включайте `AI_JSON_MODE` без `freellmpool conformance run`) |
| `число … не найдено в тексте страницы (ответ ИИ отклонён)` | модель ошиблась, защита сработала — это нормально, повтор исправит |
| `таблица не найдена` | поменялась страница источника |

## Безопасность — коротко
* Шлюз слушает только `127.0.0.1` и требует пароль; запускается отдельным пользователем с ограничениями systemd.
* Токен Telegram не попадает в окружение шлюза, ключи провайдеров — в окружение бота.
* Бот принимает любое число только если оно дословно есть на странице и в разумном диапазоне,
  поэтому слабая бесплатная модель не может подсунуть выдуманный курс.
* Бот откажется запускаться, если `AI_URL` — это не `localhost` по http (чтобы ключ не уходил открытым текстом).
