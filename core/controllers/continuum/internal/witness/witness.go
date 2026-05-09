// Package witness defines the lease-witness contract used by the
// continuum-controller to arbitrate primary ownership across regions.
//
// Per docs/SRE.md §2.4 + docs/EPICS-1-6-unified-design.md §9.3 a
// per-Application "lease" is held by exactly one region at any time,
// renewed every 10s, expiring at 30s. The PowerDNS lua-record health
// probe is the *primary* failover signal (sub-minute response); the
// lease is the *tie-breaker* that prevents split-brain promotion of
// CNPG primaries during a network partition.
//
// Two real implementations land in slice K-Cont-3 (#1101):
//
//   - cloudflare-kv: HTTP CAS against a CF Worker backed by KV (the
//     recommended path per SRE.md §2.4)
//   - dns-quorum:    2-of-3 voting across {8.8.8.8, 1.1.1.1, 9.9.9.9}
//                    via TXT-record reads, with the writer side using
//                    the existing PDM /v1/commit pattern.
//
// K-Cont-2 (this slice) ships ONLY the InMemoryClient stub used by
// unit tests. The Selector returns ErrNotImplemented for both real
// kinds so a Continuum CR pointing at a K-Cont-3 backend fails closed
// until K-Cont-3 lands. This is deliberate: per the EPIC's
// INVIOLABLE-PRINCIPLES #1 ("waterfall, not iterative MVP — never
// ship a half-shape") the controller refuses to silently degrade to a
// no-op; instead it surfaces a typed status condition so an operator
// sees the gap.
package witness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrLeaseHeldByAnother — Acquire returned because the witness reports
// the lease is currently held by a different holder. The caller MUST
// NOT promote: another region is primary.
var ErrLeaseHeldByAnother = errors.New("witness: lease held by another holder")

// ErrLeaseLost — Renew detected the witness no longer recognises this
// holder. The lease has expired (TTL exceeded) or another holder
// completed a CAS while we were not looking. The caller MUST treat
// itself as demoted.
var ErrLeaseLost = errors.New("witness: lease lost")

// ErrNotImplemented — the K-Cont-3 implementations are not wired in
// this controller build. Returned by the Selector for kinds
// `cloudflare-kv` and `dns-quorum`.
var ErrNotImplemented = errors.New("witness: implementation not built into this controller")

// State is the full lease snapshot returned by Read.
type State struct {
	// Holder is the region currently holding the lease (empty when no
	// holder claims the slot — i.e. lease is open for grabs).
	Holder string

	// AcquiredAt is when the current holder first won the slot. Used
	// by audit emit to compute observed RPO/RTO.
	AcquiredAt time.Time

	// ExpiresAt = AcquiredAt + N*RenewInterval (the witness extends
	// it on each successful Renew). When time.Now() > ExpiresAt the
	// holder is considered evicted.
	ExpiresAt time.Time

	// Generation is the witness's monotonic CAS counter. Acquire and
	// Renew each bump it; the controller threads it through subsequent
	// Renew calls so the witness can reject stale writes.
	Generation int64
}

// IsHeldBy reports whether the snapshot says `holder` currently owns
// the slot AND the slot has not expired against `now`. Pure helper —
// no I/O.
func (s State) IsHeldBy(holder string, now time.Time) bool {
	if s.Holder == "" || holder == "" {
		return false
	}
	if s.Holder != holder {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// Client is the lease-witness contract. K-Cont-3's cloudflare-kv and
// dns-quorum implementations both satisfy this surface.
//
// Implementations MUST be safe for concurrent use — the controller
// invokes Renew from a per-Continuum-CR goroutine that may race with
// Read calls from the reconcile loop.
type Client interface {
	// Acquire attempts to claim the lease for `holder` with TTL `ttl`.
	// Returns the resulting State on success; ErrLeaseHeldByAnother
	// when another holder owns a non-expired slot. The witness MUST
	// implement an atomic CAS (compare-and-set) so two simultaneous
	// callers cannot both succeed.
	//
	// Re-acquiring a lease this holder already owns is allowed (it
	// simply extends the TTL like Renew). Implementations distinguish
	// the two cases by looking at the current State.Holder.
	Acquire(ctx context.Context, holder string, ttl time.Duration) (State, error)

	// Renew extends the TTL on the slot iff `holder` still owns it.
	// Returns the new State on success; ErrLeaseLost when the witness
	// no longer recognises `holder` (TTL expired, or another holder
	// completed a CAS in the meantime).
	Renew(ctx context.Context, holder string, ttl time.Duration) (State, error)

	// Release voluntarily relinquishes the slot. Idempotent: a holder
	// that doesn't own the slot can call Release safely.
	Release(ctx context.Context, holder string) error

	// Read returns the current State without modifying it. Used by
	// the reconciler to surface `status.leaseHolder` and to detect a
	// new primary after a quorum-witnessed eviction.
	Read(ctx context.Context) (State, error)
}

// Selector returns the appropriate Client based on Continuum
// CR's spec.leaseClient.kind. K-Cont-2 ships an in-memory stub for
// each `kind` value to keep tests deterministic; K-Cont-3 swaps in
// the real implementations.
//
// Tests use SelectorFunc to inject deterministic fakes per Continuum
// CR. Production code uses NewSelector which handles env-var-config
// + ErrNotImplemented for K-Cont-3 paths.
type Selector interface {
	// Select returns a Client suitable for the given lease-client
	// configuration. The first arg is the kind enum value from the CR
	// (`cloudflare-kv` | `dns-quorum`); the second is the (key,value)
	// config map from the CR.
	//
	// The returned Client's lifecycle is tied to the per-Continuum-CR
	// goroutine — Close-on-shutdown is the caller's responsibility.
	Select(kind string, cfg map[string]any) (Client, error)
}

// SelectorFunc adapts a plain function to the Selector interface for
// test injection.
type SelectorFunc func(kind string, cfg map[string]any) (Client, error)

// Select implements Selector.
func (f SelectorFunc) Select(kind string, cfg map[string]any) (Client, error) {
	return f(kind, cfg)
}

// DefaultSelector is the production Selector. K-Cont-2 returns
// ErrNotImplemented for the two real kinds (cloudflare-kv,
// dns-quorum) so the system fails CLOSED until K-Cont-3 lands. The
// special kind `in-memory` is recognised for envtest / smoke
// scenarios — DO NOT enable in production.
type DefaultSelector struct {
	// InMemoryAllowed gates the `in-memory` kind. Default false in
	// production; tests flip true.
	InMemoryAllowed bool

	// inMemoryShared is the shared backing store across all
	// in-memory clients in this process — Continuum CRs in the same
	// process share a witness so the multi-CR isolation tests work
	// against namespaced keys (witness key includes the CR's
	// NamespacedName). When InMemoryAllowed=false, this is unused.
	inMemoryShared *InMemoryStore
	mu             sync.Mutex
}

// Select implements Selector.
func (s *DefaultSelector) Select(kind string, cfg map[string]any) (Client, error) {
	switch kind {
	case "cloudflare-kv":
		return nil, fmt.Errorf("%w: cloudflare-kv (K-Cont-3)", ErrNotImplemented)
	case "dns-quorum":
		return nil, fmt.Errorf("%w: dns-quorum (K-Cont-3)", ErrNotImplemented)
	case "in-memory":
		if !s.InMemoryAllowed {
			return nil, fmt.Errorf("witness: in-memory selector is test-only; refused in production")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.inMemoryShared == nil {
			s.inMemoryShared = NewInMemoryStore()
		}
		// Every in-memory client gets its own per-CR slot keyed by
		// the `slot` config field. Tests pass slot="ns/name" so each
		// Continuum CR gets an isolated lease.
		slot, _ := cfg["slot"].(string)
		if slot == "" {
			slot = "default"
		}
		return s.inMemoryShared.Client(slot), nil
	default:
		return nil, fmt.Errorf("witness: unknown kind %q (expected one of: cloudflare-kv, dns-quorum)", kind)
	}
}
