package core

import (
	"sync"

	"github.com/herrfrei/openupsd/internal/nut"
)

// Store is the shared NUT variable cache.
type Store struct {
	mu    sync.RWMutex
	name  string
	desc  string
	vars  map[string]string
	stale bool
}

func NewStore(name, desc string) *Store {
	return &Store{name: name, desc: desc, vars: map[string]string{}, stale: true}
}

func (s *Store) Update(vars map[string]string) {
	s.mu.Lock()
	s.vars, s.stale = vars, false
	s.mu.Unlock()
}

func (s *Store) MarkStale() {
	s.mu.Lock()
	s.stale = true
	s.mu.Unlock()
}

func (s *Store) ListUPS() []nut.UPSInfo {
	return []nut.UPSInfo{{Name: s.name, Desc: s.desc}}
}

func (s *Store) Vars(name string) (map[string]string, bool) {
	if name != s.name {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vars, true
}

func (s *Store) Stale(string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stale
}
