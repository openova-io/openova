// pinstore.go — in-memory PIN store for the 6-digit PIN auth flow (#688).
//
// Replaces the magic-link JWT + jtistore approach. PINs are short-lived
// (10 minutes), low-entropy (6 decimal digits, 10^6 = ~20 bits) and MUST
// NEVER be persisted to disk. Per docs/INVIOLABLE-PRINCIPLES.md #10
// (credential hygiene) the store keeps PINs in-memory only; a Pod restart
// invalidates every outstanding code, which is the desired behaviour.
//
// Design:
//
//   - Keyed by lowercased email so case-insensitive lookups + rate-limit
//     enforcement are uniform.
//   - Each entry tracks: the 6-digit PIN, expiry, attempts counter,
//     requestId (returned from /pin/issue, required on /pin/verify so a
//     stale browser tab can't resurrect a code that was already issued
//     to a newer session), and issuedAt for the per-email rate-limit
//     window.
//   - Mutations are mutex-guarded; the read-modify-write semantics on
//     verify (increment attempts, drop on 3rd wrong) MUST be atomic.
//   - A background sweeper deletes expired entries every minute so a
//     long-running Pod doesn't accumulate dead PINs forever.
package handler

import (
	"sync"
	"time"
)

// pinTTL — how long a freshly-issued PIN remains valid before /pin/verify
// rejects it as expired. 10 minutes per founder spec on #688.
const pinTTL = 10 * time.Minute

// pinIssueCooldown — minimum interval between consecutive /pin/issue calls
// for the same email. Prevents spamming the operator's inbox if a button
// is mashed; also a coarse-grained brute-force amplifier check.
const pinIssueCooldown = 60 * time.Second

// pinMaxAttempts — number of wrong PIN entries allowed before the entry
// is destroyed and the operator must request a new code. 3 attempts is
// standard for bank/Google verification flows.
const pinMaxAttempts = 3

// pinSweepInterval — how often the background goroutine sweeps the store
// for expired entries. Fast enough that memory pressure stays bounded;
// slow enough that the goroutine doesn't dominate scheduler time.
const pinSweepInterval = 1 * time.Minute

// pinEntry holds the live state for a single outstanding PIN.
//
// IMPORTANT: this struct is never logged. Per docs/INVIOLABLE-PRINCIPLES.md
// #10 the PIN field is a credential and MUST NOT enter slog. Use
// requestId/email for log correlation.
type pinEntry struct {
	pin       string
	expiresAt time.Time
	issuedAt  time.Time
	attempts  int
	requestID string
}

// pinStore is the in-memory PIN cache. Safe for concurrent use. Construct
// with newPinStore — the constructor wires the background sweeper and
// returns a *pinStore plus a stop function the caller can use to halt
// the sweeper at shutdown.
type pinStore struct {
	mu      sync.Mutex
	entries map[string]*pinEntry
	now     func() time.Time // injectable clock for tests
}

// newPinStore creates a ready-to-use *pinStore and launches the sweeper
// goroutine. Call stop() at process shutdown to terminate the goroutine
// (in-process tests; production never needs to).
func newPinStore() (s *pinStore, stop func()) {
	s = &pinStore{
		entries: make(map[string]*pinEntry),
		now:     time.Now,
	}
	stopCh := make(chan struct{})
	go s.sweepLoop(stopCh)
	return s, func() { close(stopCh) }
}

// newPinStoreNoSweeper returns a *pinStore without launching the sweeper.
// Tests use this to avoid goroutine leaks across the suite — they call
// sweep() directly when they need to exercise expiration.
func newPinStoreNoSweeper() *pinStore {
	return &pinStore{
		entries: make(map[string]*pinEntry),
		now:     time.Now,
	}
}

// canIssue reports whether a new PIN may be issued for the given email
// right now, or whether the per-email rate-limit window is still active.
// Returns (true, _) if issuing is allowed; (false, retryAfter) otherwise.
func (s *pinStore) canIssue(email string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canIssueLocked(email)
}

// canIssueLocked is the lock-free variant of canIssue. Caller MUST hold
// s.mu. Used by tryReserve to check-and-reserve atomically.
func (s *pinStore) canIssueLocked(email string) (bool, time.Duration) {
	key := normalizePinKey(email)
	e, ok := s.entries[key]
	if !ok {
		return true, 0
	}
	elapsed := s.now().Sub(e.issuedAt)
	if elapsed >= pinIssueCooldown {
		return true, 0
	}
	return false, pinIssueCooldown - elapsed
}

// tryReserve atomically check-and-reserves the per-email rate-limit slot.
// Returns (true, 0) if the caller may proceed with PIN issuance and stamps
// a placeholder entry (`issuedAt = now`) so concurrent callers see the
// slot as taken. Returns (false, retryAfter) when the slot is already
// reserved.
//
// The placeholder entry has empty pin/requestId; the caller MUST follow
// up with put(...) once the actual PIN is generated, or drop(...) if the
// downstream step (EnsureUser, sendPinEmail) fails and the cooldown
// should not punish the operator. This is the canonical fix for TC-R-089
// — without it, three concurrent /pin/issue goroutines all pass canIssue
// before any of them stamps the cooldown, then all three race EnsureUser
// against Keycloak.
func (s *pinStore) tryReserve(email string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok, retry := s.canIssueLocked(email); !ok {
		return false, retry
	}
	now := s.now()
	s.entries[normalizePinKey(email)] = &pinEntry{
		pin:       "",
		expiresAt: now.Add(pinTTL),
		issuedAt:  now,
		attempts:  0,
		requestID: "",
	}
	return true, 0
}

// put writes a fresh entry for the given email, replacing any existing
// entry. Caller is responsible for having already passed canIssue.
func (s *pinStore) put(email, pin, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.entries[normalizePinKey(email)] = &pinEntry{
		pin:       pin,
		expiresAt: now.Add(pinTTL),
		issuedAt:  now,
		attempts:  0,
		requestID: requestID,
	}
}

// drop deletes any entry for the given email. Used by /pin/issue's SMTP
// rollback path so a transient mail-relay failure doesn't punish the
// operator with a 60-second cooldown lockout.
func (s *pinStore) drop(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, normalizePinKey(email))
}

// verifyResult enumerates the outcomes of pinStore.verify so the handler
// can map each to the right HTTP status + error code without exposing
// the underlying entry.
type verifyResult int

const (
	verifyOK              verifyResult = iota // PIN matched; entry consumed
	verifyNotFound                            // no entry for email
	verifyExpired                             // entry exists but TTL elapsed
	verifyWrongPIN                            // mismatch, attempts left
	verifyAttemptsLocked                      // mismatch, no attempts left
	verifyRequestMismatch                     // requestId did not match the entry
)

// verify atomically validates a PIN attempt for the given email +
// requestId. On match the entry is deleted (single-use). On wrong PIN
// the attempts counter is incremented; if the counter reaches
// pinMaxAttempts the entry is deleted and verifyAttemptsLocked is
// returned so the operator must request a new code.
//
// requestId is required so a tab that observed an old issue can't replay
// the verify against a freshly-issued PIN.
func (s *pinStore) verify(email, pin, requestID string) (verifyResult, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalizePinKey(email)
	e, ok := s.entries[key]
	if !ok {
		return verifyNotFound, 0
	}
	if !s.now().Before(e.expiresAt) {
		delete(s.entries, key)
		return verifyExpired, 0
	}
	if requestID != "" && e.requestID != requestID {
		return verifyRequestMismatch, 0
	}
	if e.pin == pin {
		delete(s.entries, key)
		return verifyOK, 0
	}
	e.attempts++
	if e.attempts >= pinMaxAttempts {
		delete(s.entries, key)
		return verifyAttemptsLocked, 0
	}
	remaining := pinMaxAttempts - e.attempts
	return verifyWrongPIN, remaining
}

// sweep deletes every entry whose expiry has elapsed. Returns the number
// of entries removed (used by tests; production drops the count).
func (s *pinStore) sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	removed := 0
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			delete(s.entries, k)
			removed++
		}
	}
	return removed
}

// sweepLoop runs sweep() on a ticker until stopCh closes.
func (s *pinStore) sweepLoop(stopCh <-chan struct{}) {
	t := time.NewTicker(pinSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			s.sweep()
		}
	}
}

// size returns the number of live entries (test helper).
func (s *pinStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// normalizePinKey lowercases + trims the email so the key is canonical
// across UI variants (e.g. "Op@Example.com" vs "op@example.com").
// Defined as a package-level helper so the verify/canIssue/put paths
// agree on the same canonicalisation.
func normalizePinKey(email string) string {
	out := make([]byte, 0, len(email))
	leading := true
	for i := 0; i < len(email); i++ {
		c := email[i]
		if leading && (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
			continue
		}
		leading = false
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	for len(out) > 0 {
		last := out[len(out)-1]
		if last == ' ' || last == '\t' || last == '\r' || last == '\n' {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return string(out)
}
