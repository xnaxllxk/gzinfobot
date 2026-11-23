package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// state — один согласованный «снимок» настроек: сами настройки, собранные из них модули и
// расписание. Заменяется целиком и атомарно, поэтому отчёт и расписание никогда не видят
// наполовину применённых настроек.
type state struct {
	set   *Settings
	reg   *Registry
	slots []slot
}

func newState(set *Settings) (*state, error) {
	slots, err := parseSlots(joinSlots(set.Schedule))
	if err != nil {
		return nil, err
	}
	return &state{set: set, reg: buildRegistry(set), slots: slots}, nil
}

// enabled — включённые модули в порядке отчёта.
func (s *state) enabled() []Module {
	var out []Module
	for _, m := range s.reg.list {
		if ms := s.set.find(m.Name()); ms != nil && ms.Enabled {
			out = append(out, m)
		}
	}
	return out
}

// app связывает модули. Сами модули друг про друга ничего не знают: общаются через Env.
type app struct {
	cfg     config
	st      *store
	hc      *http.Client // ИИ-шлюз
	fetchHC *http.Client // внешние страницы и погода: с защитой от обращений во внутреннюю сеть
	ai      *aiClient
	tg      *tgClient
	me      string // username бота (для команд вида /report@botname)

	cur     atomic.Pointer[state]
	setMu   sync.Mutex    // сериализует изменения настроек (админ-команды, перечитывание файла)
	wake    chan struct{} // будит планировщик после смены расписания
	fileMod time.Time     // mtime settings.json, который мы уже учли

	runMu      sync.Mutex // не даём запускать два обновления одновременно
	maxRetries int
	retryDelay time.Duration

	fmu   sync.Mutex
	fails map[string]int // серии неудач по модулям
}

func newApp(cfg config, set *Settings) (*app, error) {
	st, err := newState(set)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 120 * time.Second}
	a := &app{
		cfg:        cfg,
		st:         newStore(cfg.cachePath),
		hc:         hc,
		fetchHC:    newSafeClient(60 * time.Second),
		ai:         &aiClient{hc: hc, url: cfg.aiURL, key: cfg.aiKey, model: cfg.aiModel, jsonMode: cfg.aiJSON},
		tg:         &tgClient{token: cfg.tgToken, base: cfg.tgBase, hc: &http.Client{Timeout: 70 * time.Second}},
		wake:       make(chan struct{}, 1),
		maxRetries: 2,
		retryDelay: 20 * time.Minute,
		fails:      map[string]int{},
	}
	a.cur.Store(st)
	return a, nil
}

// env собирает окружение для модуля из текущих (или кандидатных) настроек.
func (a *app) env(st *state, m Module, store *store) *Env {
	var vals map[string]string
	if ms := st.set.find(m.Name()); ms != nil {
		vals = ms.Values
	}
	return &Env{
		HC:         a.fetchHC,
		AI:         a.ai,
		St:         store,
		Cfg:        newCfg(m.Fields(), vals),
		Now:        time.Now(),
		StaleAfter: maxGap(st.slots) * 3 / 2,
	}
}

// apply сохраняет настройки в файл и вводит их в действие без перезапуска.
// Вызывать под a.setMu.
func (a *app) apply(set *Settings) error {
	st, err := newState(set)
	if err != nil {
		return err
	}
	if err := set.save(a.cfg.settingsPath); err != nil {
		return err
	}
	if fi, err := os.Stat(a.cfg.settingsPath); err == nil {
		a.fileMod = fi.ModTime()
	}
	a.cur.Store(st)
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

// watchSettings: файл settings.json можно править руками — бот подхватит изменения сам.
func (a *app) watchSettings(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reloadIfChanged(ctx)
		}
	}
}

func (a *app) reloadIfChanged(ctx context.Context) {
	fi, err := os.Stat(a.cfg.settingsPath)
	if err != nil {
		return
	}
	a.setMu.Lock()
	defer a.setMu.Unlock()
	if fi.ModTime().Equal(a.fileMod) {
		return
	}
	a.fileMod = fi.ModTime() // даже при ошибке: не ругаемся каждые 30 секунд на один и тот же файл
	set, err := loadSettings(a.cfg.settingsPath)
	if err != nil {
		log.Printf("settings.json не принят, работаю на прежних настройках: %v", err)
		a.notify(ctx, "⚠️ settings.json содержит ошибку, остаются прежние настройки:\n"+err.Error())
		return
	}
	st, err := newState(set)
	if err != nil {
		log.Printf("settings.json: %v", err)
		return
	}
	a.cur.Store(st)
	select {
	case a.wake <- struct{}{}:
	default:
	}
	log.Println("settings.json перечитан")
	a.notify(ctx, "🔄 settings.json перечитан, настройки применены")
}

// initSettings читает settings.json, а если его нет — создаёт из старых переменных окружения.
func initSettings(cfg config) (*Settings, error) {
	set, err := loadSettings(cfg.settingsPath)
	if err == nil {
		return set, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	set = cfg.seedSettings()
	if err := set.normalize(); err != nil {
		return nil, err
	}
	if err := set.save(cfg.settingsPath); err != nil {
		log.Printf("не удалось создать %s: %v (работаю без сохранения настроек)", cfg.settingsPath, err)
	} else {
		log.Printf("создан %s из значений по умолчанию и переменных окружения", cfg.settingsPath)
	}
	return set, nil
}

// sleep ждёт d или отмены ctx; false — если контекст отменён.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
