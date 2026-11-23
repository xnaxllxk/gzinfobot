package main

// Telegram без сторонних библиотек: long polling getUpdates + sendMessage.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type tgClient struct {
	token string
	base  string
	hc    *http.Client
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func (t *tgClient) redact(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.token, "***")) // токен не должен попасть в логи
}

func (t *tgClient) call(ctx context.Context, method string, params, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return t.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return t.redact(err)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// send отправляет текст; длинное сообщение режется по строкам (лимит Telegram — 4096 символов).
func (t *tgClient) send(ctx context.Context, chat int64, text string) error {
	for _, part := range splitMessage(text, 4000) {
		if err := t.call(ctx, "sendMessage", map[string]any{
			"chat_id":                  chat,
			"text":                     part,
			"disable_web_page_preview": true,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func splitMessage(text string, limit int) []string {
	r := []rune(text)
	if len(r) <= limit {
		return []string{text}
	}
	var parts []string
	for len(r) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- { // стараемся резать по переводу строки
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, string(r[:cut]))
		r = []rune(strings.TrimLeft(string(r[cut:]), "\n"))
	}
	return append(parts, string(r))
}

const helpText = "Команда /report — сводка: курсы, погода, цены на топливо и другие данные, которые подключены в боте."

func (a *app) handle(ctx context.Context, u tgUpdate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("паника в обработчике: %v", r)
		}
	}()
	m := u.Message
	if m == nil {
		return
	}
	f := strings.Fields(m.Text)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return
	}
	cmd := strings.ToLower(f[0])
	if i := strings.Index(cmd, "@"); i >= 0 {
		if a.me != "" && cmd[i+1:] != strings.ToLower(a.me) {
			return // команда адресована другому боту
		}
		cmd = cmd[:i]
	}

	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.Text), f[0]))

	var text string
	switch cmd {
	case "/report":
		text = a.report(ctx)
	case "/start", "/help":
		text = helpText
	case "/admin", "/settings", "/set", "/enable", "/disable", "/test", "/source", "/refresh", "/status":
		// Только по user ID админа, а не «из админского чата»: иначе любой участник группы
		// мог бы менять настройки. Остальным бот молчит, как и раньше.
		if !a.isAdmin(m.From.ID) {
			return
		}
		text = a.admin(ctx, m.Chat.ID, cmd, rest)
	default:
		return
	}
	if text == "" {
		return
	}
	if err := a.tg.send(ctx, m.Chat.ID, text); err != nil {
		log.Printf("отправка: %v", err)
	}
}

func (a *app) poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		var updates []tgUpdate
		err := a.tg.call(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         50,
			"allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("getUpdates: %v", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if a.cfg.adminOnly && (u.Message == nil || !a.isAdmin(u.Message.From.ID)) {
				continue // чужих не обрабатываем вообще: ни горутины, ни ответа
			}
			go a.handle(ctx, u)
		}
	}
}
