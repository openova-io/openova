package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
)

// ErrNotFound is returned by Manager.Get / Stop for unknown session IDs.
var ErrNotFound = errors.New("session: not found")

// Manager tracks live Sessions by ID. One Manager per pty-server
// process; the manager is concurrency-safe and is the single place
// that allocates session IDs.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// Create spawns a Session and registers it under a freshly minted ID.
func (m *Manager) Create(spec Spec) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	s, err := New(id, spec)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// Auto-evict on exit so /sessions stays accurate.
	go func() {
		<-s.Done()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()

	return s, nil
}

// Get returns the Session with the given ID or ErrNotFound.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// Stop terminates the Session and removes it. Returns ErrNotFound if
// no such Session exists.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	return s.Close()
}

// List returns a snapshot of currently active session IDs.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	return out
}

// Shutdown closes every live session, used on SIGTERM.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	ids := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ids = append(ids, s)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	for _, s := range ids {
		_ = s.Close()
	}
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
