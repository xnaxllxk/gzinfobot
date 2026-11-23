package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func init() {
	fetchRetryDelay = 0
	aiRetryDelay = 0
}

// fakeAI — поддельный ИИ-шлюз: на каждый запрос отвечает reply(промпт).
func fakeAI(t *testing.T, reply func(prompt string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if n := len(req.Messages); n > 0 {
			prompt = req.Messages[n-1].Content
		}
		out := map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": reply(prompt)}}}}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// siteServer — поддельный внешний сайт по https; страницы можно менять по ходу теста.
type siteServer struct {
	*httptest.Server
	mu    sync.Mutex
	pages map[string]string
}

func (s *siteServer) set(path, body string) {
	s.mu.Lock()
	s.pages[path] = body
	s.mu.Unlock()
}

// fakeSite поднимает https-сервер. pages: путь → ответ.
func fakeSite(t *testing.T, pages map[string]string) *siteServer {
	t.Helper()
	s := &siteServer{pages: map[string]string{}}
	for k, v := range pages {
		s.pages[k] = v
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		body, ok := s.pages[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".json") {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

type sentLog struct {
	mu   sync.Mutex
	msgs []string
}

func (s *sentLog) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}

// newTestApp собирает приложение с поддельными Telegram и ИИ-шлюзом и временными файлами.
func newTestApp(t *testing.T, aiURL string, site *siteServer) (*app, *sentLog) {
	t.Helper()
	strictURLs = false
	t.Cleanup(func() { strictURLs = true })

	log := &sentLog{}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		log.mu.Lock()
		log.msgs = append(log.msgs, p.Text)
		log.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(tg.Close)

	dir := t.TempDir()
	cfg := config{
		tgToken: "T", tgBase: tg.URL, aiKey: "K", aiModel: "m", aiURL: aiURL,
		cachePath: filepath.Join(dir, "cache.json"), settingsPath: filepath.Join(dir, "settings.json"),
		adminUser: 42, adminChat: 42,
		seedZiraatURL: defaultZiraatURL, seedFuelURL: defaultFuelURL, seedChannel: "gise", seedTimes: defaultTimes,
	}
	set := cfg.seedSettings()
	if err := set.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := set.save(cfg.settingsPath); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, set)
	if err != nil {
		t.Fatal(err)
	}
	if site != nil {
		a.fetchHC = site.Client()
	}
	return a, log
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
