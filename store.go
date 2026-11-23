package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"time"
)

// entry — последний удачный результат одного модуля. Модуль сам решает, что в Data;
// хранилище про формат ничего не знает.
type entry struct {
	Data json.RawMessage `json:"data"`
	At   time.Time       `json:"at"`             // когда значения последний раз подтверждены источником
	Meta string          `json:"meta,omitempty"` // отпечаток настроек, при которых получены данные
}

type snapshot struct {
	Entries     map[string]entry `json:"entries,omitempty"`
	LastAttempt time.Time        `json:"last_attempt"` // последний запуск обновления (чтобы перезапуски не дёргали сайты)
	LastReport  time.Time        `json:"last_report"`  // время последнего планового отчёта (чтобы перезапуск не слал его дважды)

	// Старый формат кэша (до модульности). При загрузке переносится в Entries.
	LegacyRates *Rates `json:"rates,omitempty"`
	LegacyFuel  *Fuel  `json:"fuel,omitempty"`
}

// store — кэш последних удачных значений в JSON-файле; переживает перезапуск бота.
// С пустым path работает только в памяти — так модули проверяются командой /test.
type store struct {
	mu      sync.RWMutex
	path    string
	snap    snapshot
	pending map[string]string // ключ → подпись подозрительного значения, ждущего подтверждения
	hashes  map[string]string // ключ → хэш фрагмента страницы, по которому уже получены данные
}

func newStore(path string) *store {
	s := &store{path: path, pending: map[string]string{}, hashes: map[string]string{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &s.snap); err != nil {
				log.Printf("кэш повреждён, начинаю с пустого: %v", err)
				s.snap = snapshot{}
			}
		}
	}
	s.migrate()
	return s
}

func (s *store) migrate() {
	if s.snap.Entries == nil {
		s.snap.Entries = map[string]entry{}
	}
	if r := s.snap.LegacyRates; r != nil {
		if _, ok := s.snap.Entries["rates"]; !ok {
			b, _ := json.Marshal(r)
			s.snap.Entries["rates"] = entry{Data: b, At: r.At}
		}
		s.snap.LegacyRates = nil
	}
	if f := s.snap.LegacyFuel; f != nil {
		if _, ok := s.snap.Entries["fuel"]; !ok {
			b, _ := json.Marshal(f)
			s.snap.Entries["fuel"] = entry{Data: b, At: f.At}
		}
		s.snap.LegacyFuel = nil
	}
}

func (s *store) lastAttempt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap.LastAttempt
}

func (s *store) saveLocked() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(s.snap, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("не удалось записать кэш: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("не удалось заменить кэш: %v", err)
	}
}

// load читает данные модуля в v и возвращает время подтверждения.
func (s *store) load(key string, v any) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.snap.Entries[key]
	if !ok || len(e.Data) == 0 {
		return time.Time{}, false
	}
	if err := json.Unmarshal(e.Data, v); err != nil {
		return time.Time{}, false
	}
	return e.At, true
}

func (s *store) meta(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap.Entries[key].Meta
}

func (s *store) has(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.snap.Entries[key]
	return ok
}

func (s *store) entryAt(key string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.snap.Entries[key]
	return e.At, ok
}

// put сохраняет результат модуля. hash — отпечаток источника (см. sameHash), meta — отпечаток настроек.
func (s *store) put(key string, v any, at time.Time, meta, hash string) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("кэш %s: %v", key, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.Entries[key] = entry{Data: b, At: at, Meta: meta}
	s.hashes[key] = hash
	s.saveLocked()
}

func (s *store) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snap.Entries, key)
	delete(s.hashes, key)
	delete(s.pending, key)
	s.saveLocked()
}

// copyFrom переносит результат модуля из другого хранилища (удачный /test → боевой кэш).
func (s *store) copyFrom(o *store, key string) {
	o.mu.RLock()
	e, ok := o.snap.Entries[key]
	h := o.hashes[key]
	o.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.Entries[key] = e
	s.hashes[key] = h
	delete(s.pending, key)
	s.saveLocked()
}

func (s *store) lastReport() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap.LastReport
}

func (s *store) markReport(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.LastReport = t
	s.saveLocked()
}

func (s *store) markAttempt(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.LastAttempt = t
	s.saveLocked()
}

func (s *store) sameHash(key, h string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.snap.Entries[key]
	return ok && h != "" && s.hashes[key] == h
}

// touch: источник проверен, данные не изменились — обновляем только метку времени.
func (s *store) touch(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.snap.Entries[key]; ok {
		e.At = time.Now()
		s.snap.Entries[key] = e
		s.saveLocked()
	}
}

// maxJump — скачок больше 15% к прошлому значению считается подозрительным.
const maxJump = 0.15

// jumpOK защищает от мусора: сильно отличающееся от прошлого значение принимается только если
// то же самое пришло и при следующем обновлении. Так бот не застрянет навсегда, если цена
// действительно резко изменилась.
func (s *store) jumpOK(key string, old, cur []float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(old) != len(cur) {
		return true // прошлых значений нет
	}
	worst := 0.0
	for i := range cur {
		if old[i] > 0 {
			worst = math.Max(worst, math.Abs(cur[i]-old[i])/old[i])
		}
	}
	if worst <= maxJump {
		delete(s.pending, key)
		return true
	}
	sig := fmt.Sprint(cur)
	if s.pending[key] == sig {
		delete(s.pending, key)
		log.Printf("%s: резкое изменение (%.0f%%) подтверждено повторным чтением, принимаю", key, worst*100)
		return true
	}
	s.pending[key] = sig
	log.Printf("%s: резкое изменение (%.0f%%): %v → %v, жду подтверждения", key, worst*100, old, cur)
	return false
}
