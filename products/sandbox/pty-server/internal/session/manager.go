package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned by Manager.Get / Stop for unknown session IDs.
var ErrNotFound = errors.New("session: not found")

// Manager tracks live Sessions by ID. One Manager per pty-server
// process; the manager is concurrency-safe and is the single place
// that allocates session IDs.
//
// Wave 10 (idle-scaler): Manager also tracks `lastActivity` — a
// monotonically-updated timestamp of the most recent caller-driven
// event (session create, WS attach, WS message in/out, session stop).
// The IdleScaler in sandbox-controller polls this via the /idle
// endpoint and scales the StatefulSet to 0 after the configured
// idle window (products/sandbox/docs/architecture.md §1).
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	activityMu   sync.RWMutex
	lastActivity time.Time
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{
		sessions:     make(map[string]*Session),
		lastActivity: time.Now().UTC(),
	}
}

// Touch records a fresh activity timestamp. Callers (HTTP handlers,
// WebSocket pumps) invoke Touch on every interaction with a Session
// — the IdleScaler reads the result via LastActivity() / the /idle
// endpoint.
func (m *Manager) Touch() {
	m.activityMu.Lock()
	m.lastActivity = time.Now().UTC()
	m.activityMu.Unlock()
}

// LastActivity returns the most recent activity timestamp. If no
// caller has yet touched the manager (idle from process start),
// LastActivity is the process boot time.
func (m *Manager) LastActivity() time.Time {
	m.activityMu.RLock()
	defer m.activityMu.RUnlock()
	return m.lastActivity
}

// Create spawns a Session and registers it under a freshly minted ID.
func (m *Manager) Create(spec Spec) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return m.createWithID(id, spec)
}

// CreateWithID spawns a Session and registers it under the supplied
// id. Used by the lazy-spawn-on-attach path (TBD-P4 B3) where the
// id is the Sandbox CRD name carried in the WS URL, NOT a pty-server-
// minted hex string. Returns an error if the id is already in use.
func (m *Manager) CreateWithID(id string, spec Spec) (*Session, error) {
	if id == "" {
		return nil, errors.New("session: empty id")
	}
	m.mu.RLock()
	_, exists := m.sessions[id]
	m.mu.RUnlock()
	if exists {
		return nil, errors.New("session: id already exists")
	}
	return m.createWithID(id, spec)
}

func (m *Manager) createWithID(id string, spec Spec) (*Session, error) {
	s, err := New(id, spec)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	m.Touch()

	// Auto-evict on exit so /sessions stays accurate.
	go func() {
		<-s.Done()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		m.Touch()
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
	m.Touch()
	return s.Close()
}

// Count returns the number of active sessions.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
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
