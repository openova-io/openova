// Package store keeps the consents of the open-banking product.
//
// A Consent is one row of the table consent with its rows of consent_history. Every change of
// state goes through the consent package's transition table, and every accepted transition
// appends exactly one history entry in the same write as the state. An authorised consent
// whose expiry has passed is expired by the system when it is read; no timer does it.
//
// Store is the port. Memory is the implementation for tests and for a ledger of fixtures; it keeps
// everything in the process. The PostgreSQL implementation follows the same port.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/consent"
)

// Consent is one stored consent.
type Consent struct {
	ID         string
	Family     consent.Family
	ClientID   string
	State      consent.State
	Request    json.RawMessage // the request body as received
	CustomerID string          // set when the customer authorises
	Accounts   []string        // the accounts the customer chose; set when the customer authorises
	ExpiresAt  time.Time       // zero: no expiry
	CreatedAt  time.Time
	StateAt    time.Time // when the state last changed
	History    []consent.HistoryEntry
}

// ErrNotFound is returned for a consent that does not exist.
var ErrNotFound = errors.New("store: consent not found")

// ErrExists is returned when a consent with the same identifier exists already.
var ErrExists = errors.New("store: consent exists already")

// Store keeps consents. A transition that the consent package refuses returns an error that
// wraps consent.ErrTransition and changes nothing.
type Store interface {
	// Create stores a new consent in the state awaiting, created by the third party.
	// ID, Family, ClientID and Request are read from c; ExpiresAt is read when it is set.
	Create(ctx context.Context, c Consent) (Consent, error)
	// Get returns the consent. When it is authorised and its expiry has passed, Get first
	// records the event expire by the system.
	Get(ctx context.Context, id string) (Consent, bool, error)
	// Authorise records the customer's decision authorise, with the customer and the accounts.
	Authorise(ctx context.Context, id, customerID string, accounts []string) (Consent, error)
	// Apply records one event caused by actor. Use Authorise for the event authorise.
	Apply(ctx context.Context, id string, event consent.Event, actor consent.Actor, reason string) (Consent, error)
}

// Memory is a Store that keeps everything in the process. It is safe for concurrent use.
type Memory struct {
	now func() time.Time

	mu   sync.Mutex
	rows map[string]Consent
}

// NewMemory returns an empty Memory store. now may be nil: time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, rows: map[string]Consent{}}
}

// Create implements Store.
func (m *Memory) Create(_ context.Context, c Consent) (Consent, error) {
	if c.ID == "" || c.ClientID == "" || !slices.Contains(consent.Families(), c.Family) {
		return Consent{}, fmt.Errorf("store: a consent needs an identifier, a client and a family, got %q, %q, %q", c.ID, c.ClientID, c.Family)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[c.ID]; ok {
		return Consent{}, fmt.Errorf("%w: %s", ErrExists, c.ID)
	}
	now := m.now()
	r := consent.Record{Family: c.Family}
	if err := r.Apply(consent.Create, consent.ThirdParty, "created", now); err != nil {
		return Consent{}, err
	}
	row := Consent{ID: c.ID, Family: c.Family, ClientID: c.ClientID, State: r.State,
		Request: slices.Clone(c.Request), ExpiresAt: c.ExpiresAt, CreatedAt: now, StateAt: now, History: r.History}
	m.rows[c.ID] = row
	return clone(row), nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, id string) (Consent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return Consent{}, false, nil
	}
	now := m.now()
	if row.State == consent.Authorised && !row.ExpiresAt.IsZero() && !now.Before(row.ExpiresAt) {
		next, err := m.apply(row, consent.Expire, consent.System, "expires_at passed", now)
		if err != nil {
			return Consent{}, false, err
		}
		row = next
	}
	return clone(row), true, nil
}

// Authorise implements Store.
func (m *Memory) Authorise(_ context.Context, id, customerID string, accounts []string) (Consent, error) {
	if customerID == "" {
		return Consent{}, errors.New("store: authorise needs a customer")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return Consent{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	row.CustomerID, row.Accounts = customerID, slices.Clone(accounts)
	next, err := m.apply(row, consent.Authorise, consent.Customer, "authorised", m.now())
	if err != nil {
		return Consent{}, err
	}
	return clone(next), nil
}

// Apply implements Store.
func (m *Memory) Apply(_ context.Context, id string, event consent.Event, actor consent.Actor, reason string) (Consent, error) {
	if event == consent.Authorise || event == consent.Create {
		return Consent{}, fmt.Errorf("store: use Authorise or Create for the event %s", event)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return Consent{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	next, err := m.apply(row, event, actor, reason, m.now())
	if err != nil {
		return Consent{}, err
	}
	return clone(next), nil
}

// apply applies one event to row and stores the result. The caller holds m.mu.
func (m *Memory) apply(row Consent, event consent.Event, actor consent.Actor, reason string, at time.Time) (Consent, error) {
	r := consent.Record{Family: row.Family, State: row.State, History: slices.Clone(row.History)}
	if err := r.Apply(event, actor, reason, at); err != nil {
		return Consent{}, err
	}
	row.State, row.StateAt, row.History = r.State, at, r.History
	m.rows[row.ID] = row
	return row, nil
}

func clone(c Consent) Consent {
	c.Request = slices.Clone(c.Request)
	c.Accounts = slices.Clone(c.Accounts)
	c.History = slices.Clone(c.History)
	return c
}
