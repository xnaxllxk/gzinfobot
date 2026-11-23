package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Запрос к шлюзу: без ключа нет заголовка Authorization, нет DeepSeek-специфичных полей и
// response_format (он включается только явно), temperature 0.
func TestAIRequestShape(t *testing.T) {
	var gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"x\":\"1\"}"}}]}`))
	}))
	defer srv.Close()

	var out map[string]string
	c := &aiClient{hc: srv.Client(), url: srv.URL, model: "auto"}
	if err := c.ask(context.Background(), "hi", &out); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("без ключа Authorization быть не должно, а пришло %q", gotAuth)
	}
	if _, bad := body["thinking"]; bad {
		t.Error("поле thinking (DeepSeek) не должно отправляться")
	}
	if body["model"] != "auto" || body["temperature"] != float64(0) {
		t.Errorf("неожиданное тело запроса: %v", body)
	}
	if _, has := body["response_format"]; has {
		t.Error("response_format по умолчанию отправляться не должен")
	}

	c.key, c.jsonMode = "secret", true
	if err := c.ask(context.Background(), "hi", &out); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if rf, _ := body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("при jsonMode ожидался response_format=json_object, получено %v", body["response_format"])
	}
}

// Шлюз выключен: ошибка понятная и подсказывает, что проверить.
func TestAIGatewayDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // порт закрыт
	c := &aiClient{hc: &http.Client{}, url: url, model: "auto"}
	err := c.ask(context.Background(), "hi", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "freellmpool") {
		t.Fatalf("ожидалась подсказка про freellmpool, получено: %v", err)
	}
}

// 502 от шлюза (все провайдеры упали) — ошибка с кодом, без паники и без «успеха».
func TestAIAllProvidersDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"all_providers_exhausted"}}`, http.StatusBadGateway)
	}))
	defer srv.Close()
	c := &aiClient{hc: srv.Client(), url: srv.URL, model: "auto"}
	err := c.ask(context.Background(), "hi", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("ожидалась ошибка HTTP 502, получено: %v", err)
	}
}

func TestCheckAIURL(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:8080/v1/chat/completions",
		"http://localhost:8080/v1/chat/completions",
		"http://[::1]:8080/v1/chat/completions",
		"https://api.groq.com/openai/v1/chat/completions",
	}
	bad := []string{
		"http://192.168.1.5:8080/v1/chat/completions", // ключ ушёл бы по сети открытым текстом
		"http://example.com/v1/chat/completions",
		"https://user:pass@example.com/v1",
		"ftp://127.0.0.1/x",
		"127.0.0.1:8080",
		"",
	}
	for _, u := range ok {
		if err := checkAIURL(u, true); err != nil {
			t.Errorf("%s: неожиданная ошибка %v", u, err)
		}
	}
	for _, u := range bad {
		if err := checkAIURL(u, true); err == nil {
			t.Errorf("%s: должен быть отклонён", u)
		}
	}
}
