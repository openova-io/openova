// Package audit ships the catalyst.audit JetStream event surface for
// catalyst-api: type-tagged Event records, an in-process ring buffer
// (the audit log surface for the U8 audit trail UI when no NATS is
// wired), an SSE fan-out for live-tail consumers, and a publisher
// interface that production main.go binds to a real NATS JetStream
// publisher when CATALYST_NATS_URL is set.
//
// Per ADR-0001 §3 and §9 the canonical audit transport is the
// `catalyst.audit` JetStream subject — there is NO secondary audit DB.
// The in-process ring buffer in this package is the local mirror that
// powers the catalyst-api's `GET /api/v1/sovereigns/{id}/audit/rbac`
// listing endpoint without making the UI poll JetStream directly.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 audit-type names are NEVER
// invented. The canonical RBAC events shipped by EPIC-3 (#1098) are:
//
//	rbac-grant-created
//	rbac-grant-updated
//	rbac-grant-deleted
//	rbac-tier-changed
//
// Future audit types (continuum-*, application-*, ...) plug into the
// same Event shape via their own constants — the ring buffer doesn't
// know about specific names so it stays generic.
//
// ── Why a Publisher interface (not a concrete NATS client) ────────────
//
// catalyst-api ships without a hard `nats.go` dependency in its
// go.mod (mirrors the compliance handler's `PolicyRollupPublisher`
// pattern in compliance.go). The Publisher interface lets the
// rbac_assign.go handler call `Publish()` synchronously after a
// successful UserAccess CR write, while production wiring binds a
// real NATS publisher and tests bind a recorder that asserts on
// captured events. Nil-tolerant: a nil publisher is safe — the
// in-memory ring buffer + SSE fan-out still work and the audit-trail
// UI degrades to "in-process audit log only" gracefully.
package audit

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Canonical audit-type names (per EPIC-3 #1098 §6 design doc) ──────

const (
	// AuditTypeRBACGrantCreated fires when /rbac/assign creates a new
	// UserAccess CR (Applied = "created").
	AuditTypeRBACGrantCreated = "rbac-grant-created"

	// AuditTypeRBACGrantUpdated fires when /rbac/assign updates the
	// tier on an existing UserAccess CR for the same scope set
	// (Applied = "updated").
	AuditTypeRBACGrantUpdated = "rbac-grant-updated"

	// AuditTypeRBACGrantDeleted fires when /rbac/assign or a sibling
	// endpoint removes a UserAccess CR.
	AuditTypeRBACGrantDeleted = "rbac-grant-deleted"

	// AuditTypeRBACTierChanged is the explicit-tier-change variant —
	// emitted in addition to RBACGrantUpdated when the operation
	// changed the tier (vs only the scope or labels). Lets the audit
	// trail UI render a tier-rotation pill.
	AuditTypeRBACTierChanged = "rbac-tier-changed"
)

// IsRBACAuditType reports whether `t` is one of the canonical RBAC
// audit-type names. Used by the GET /audit/rbac handler to filter the
// ring buffer; future audit-types (continuum-*, etc.) get their own
// helpers. Pure function — testable in isolation.
func IsRBACAuditType(t string) bool {
	switch t {
	case AuditTypeRBACGrantCreated,
		AuditTypeRBACGrantUpdated,
		AuditTypeRBACGrantDeleted,
		AuditTypeRBACTierChanged:
		return true
	}
	return false
}

// ── Event shape ──────────────────────────────────────────────────────

// Event is the on-the-wire record published to `catalyst.audit`. The
// shape is shared across every audit-type — readers branch on
// `AuditType` to interpret `Detail`. Every field is optional except
// `AuditType` and `Timestamp` so a RBAC event populates only the
// fields it owns and a future continuum event populates only its own.
//
// Per ADR-0001 §3 this struct is the SUPERSET surface; consumers tag
// only what's relevant.
type Event struct {
	// AuditType — canonical name (e.g. "rbac-grant-created"). REQUIRED.
	AuditType string `json:"auditType"`

	// Timestamp — RFC3339 UTC. REQUIRED.
	Timestamp time.Time `json:"ts"`

	// Actor — the operator (or system) that triggered the event. Empty
	// when unauthenticated (e.g. a controller-emitted event).
	Actor string `json:"actor,omitempty"`

	// SovereignID — the deployment id (= cluster id when on chroot)
	// the event scopes to. Used by the listing endpoint's filter.
	SovereignID string `json:"sovereignId,omitempty"`

	// Result — "ok" | "denied" | "error". Defaults to "ok" when
	// unset. Lets the audit trail UI render a result pill without
	// scanning Detail.
	Result string `json:"result,omitempty"`

	// ── RBAC-event-specific fields ──
	//
	// All optional. Populated by the rbac_assign.go emit-side; ignored
	// by non-RBAC consumers.

	// TargetUser — the user the grant was applied to (Keycloak
	// subject UUID OR email when subject is unknown).
	TargetUser string `json:"targetUser,omitempty"`

	// TargetUserEmail — convenience, always populated when known.
	TargetUserEmail string `json:"targetUserEmail,omitempty"`

	// TargetApplication — the Application name the scope binds to.
	// Empty for global grants (wildcard scope).
	TargetApplication string `json:"targetApp,omitempty"`

	// Tier — the catalog tier (`viewer`/`developer`/`operator`/`admin`/`owner`).
	Tier string `json:"tier,omitempty"`

	// PreviousTier — only set on `rbac-tier-changed` events.
	PreviousTier string `json:"previousTier,omitempty"`

	// Scopes — the full scope set the UserAccess CR carries. Same shape
	// as the rbac_assign.go RBACScope.
	Scopes []EventScope `json:"scopes,omitempty"`

	// UserAccessRef — the K8s name of the UserAccess CR.
	UserAccessRef string `json:"userAccessRef,omitempty"`

	// Detail — opaque human-readable summary (for debugging or for
	// rendering when the structured fields don't capture the full
	// nuance).
	Detail string `json:"detail,omitempty"`
}

// EventScope is one (key, value) pair in Event.Scopes. Mirrors
// rbacAssignScopeBody so the rbac_assign.go emit-side can pass a
// converted slice without redeclaring the shape in this package.
type EventScope struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ── Publisher interface (NATS adapter binding point) ─────────────────

// Publisher publishes an Event to the catalyst.audit JetStream
// subject. Production main.go binds this to a real NATS-JetStream
// client (when CATALYST_NATS_URL is set); tests bind a recorder.
// Nil-tolerant on the consumer side — see audit.Bus.Publish.
type Publisher interface {
	// Publish writes the event to NATS. MUST NOT panic on error;
	// production callers log and continue so a transient NATS outage
	// never wedges the in-process hot path.
	Publish(ctx context.Context, ev Event) error
}

// ── In-process Bus (ring buffer + SSE fan-out) ───────────────────────

// Bus is the in-process audit-log surface the GET /audit/rbac handler
// reads from and the GET /audit/rbac/stream handler subscribes to.
// Owns:
//
//   - a bounded ring buffer (capacity configurable, default 1000)
//     so the handler can serve "last N events" listings without
//     hitting NATS
//   - a subscriber map for SSE fan-out (mirrors the compliance handler
//     pattern; see internal/handler/compliance.go's subscribers field)
//   - an optional Publisher for cross-instance / persistent storage
//     (catalyst.audit JetStream subject)
//
// Bus.Publish is the single entry point — it appends to the ring,
// fans out to subscribers, AND forwards to the Publisher when set.
// Callers don't have to coordinate.
type Bus struct {
	mu sync.RWMutex

	// ring is a fixed-size circular buffer. Newest event at index
	// (head-1) % cap. Empty slots are zero-valued Event{} until
	// `count` reaches `cap` for the first time.
	ring  []Event
	head  int
	count int

	// subscribers map of id → channel. The id is for unsub on
	// cancel; the channel is buffered so a slow subscriber can't
	// stall the publisher — when full, the publisher drops the event
	// for that subscriber (best-effort SSE).
	subscribers map[uint64]chan Event
	subID       uint64

	// publisher — optional cross-process forwarder. Nil-tolerant:
	// when nil, ring + SSE still work; when set, every Publish() is
	// also forwarded.
	publisher Publisher
}

// BusConfig lets the caller tune the ring + subscriber buffer sizes.
// Production main.go uses the defaults; tests inject a tiny ring so
// the eviction-on-overflow path can be exercised.
type BusConfig struct {
	// RingCapacity is the max number of events the in-memory ring
	// retains. Older events are evicted FIFO. Zero ⇒ DefaultRingCapacity.
	RingCapacity int

	// SubscriberBuffer is the per-subscriber channel size. Zero ⇒
	// DefaultSubscriberBuffer. Larger = more tolerant of slow SSE
	// consumers; smaller = faster eviction of dead consumers.
	SubscriberBuffer int

	// Publisher is the optional NATS forwarder. Nil ⇒ in-process only.
	Publisher Publisher
}

const (
	DefaultRingCapacity     = 1000
	DefaultSubscriberBuffer = 64
)

// NewBus constructs an audit Bus with the given config. Defaults are
// applied for zero-valued fields.
func NewBus(cfg BusConfig) *Bus {
	cap := cfg.RingCapacity
	if cap <= 0 {
		cap = DefaultRingCapacity
	}
	return &Bus{
		ring:        make([]Event, cap),
		subscribers: make(map[uint64]chan Event),
		publisher:   cfg.Publisher,
	}
}

// Publish appends `ev` to the ring buffer, fans out to all
// subscribers (best-effort, drops to full channels), and forwards to
// the Publisher when configured. Always non-blocking — the publisher
// call goes through the caller's context but the SSE fan-out drops
// on full to keep the in-process path bounded.
//
// `ev.Timestamp` is set to time.Now().UTC() if zero. `ev.Result`
// defaults to "ok" when empty.
func (b *Bus) Publish(ctx context.Context, ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	} else {
		ev.Timestamp = ev.Timestamp.UTC()
	}
	if ev.Result == "" {
		ev.Result = "ok"
	}

	// Append to ring.
	b.mu.Lock()
	b.ring[b.head] = ev
	b.head = (b.head + 1) % len(b.ring)
	if b.count < len(b.ring) {
		b.count++
	}
	// Snapshot subscribers under the same lock so concurrent unsub
	// during the loop doesn't race.
	subs := make([]chan Event, 0, len(b.subscribers))
	for _, ch := range b.subscribers {
		subs = append(subs, ch)
	}
	pub := b.publisher
	b.mu.Unlock()

	// Fan out (best-effort).
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// Subscriber is slow — drop the event for them. The
			// alternative (block) would let one slow client wedge
			// the entire bus.
		}
	}

	// Forward to NATS.
	if pub != nil {
		// Best-effort — log nothing here (no logger in this package),
		// the handler caller logs on the rbac_assign.go side using
		// h.log when needed. Production main.go can wrap pub with a
		// logging adapter if desired.
		_ = pub.Publish(ctx, ev)
	}
}

// List returns up to `limit` most-recent events whose AuditType passes
// `filter` and whose SovereignID matches `sovereignID` (when set). The
// result is sorted newest-first. If `filter` is nil, every event in
// the ring is considered.
//
// Pure read; safe to call concurrently with Publish.
func (b *Bus) List(sovereignID string, filter func(string) bool, limit int) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.count == 0 {
		return nil
	}
	if limit <= 0 {
		limit = b.count
	}

	out := make([]Event, 0, b.count)
	// Walk the ring newest-first. The newest event sits at index
	// (head-1) % cap; iterate backward `count` times.
	cap := len(b.ring)
	for i := 0; i < b.count; i++ {
		idx := (b.head - 1 - i + cap) % cap
		ev := b.ring[idx]
		if filter != nil && !filter(ev.AuditType) {
			continue
		}
		if sovereignID != "" && ev.SovereignID != sovereignID {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	// Already newest-first by construction; defensive sort in case a
	// future caller passes events with offset clocks.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out
}

// Subscribe returns a channel + unsub func for SSE consumers. The
// channel buffer is sized per BusConfig.SubscriberBuffer (default
// 64). The caller MUST call unsub when done — leaving subscribers
// stranded leaks a channel for the bus's lifetime.
//
// `sovereignID` filters at fan-out time; pass "" for all events.
// `filter` is a per-event audit-type predicate (nil = no filter).
func (b *Bus) Subscribe(sovereignID string, filter func(string) bool) (<-chan Event, func()) {
	bufSize := DefaultSubscriberBuffer
	in := make(chan Event, bufSize)
	out := make(chan Event, bufSize)

	b.mu.Lock()
	b.subID++
	id := b.subID
	b.subscribers[id] = in
	b.mu.Unlock()

	// Translator goroutine: applies sovereign + audit-type filter
	// between the bus fan-out and the consumer channel. Lets the
	// bus stay generic while consumers see only matching events.
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				if sovereignID != "" && ev.SovereignID != sovereignID {
					continue
				}
				if filter != nil && !filter(ev.AuditType) {
					continue
				}
				select {
				case out <- ev:
				default:
					// Consumer is slow — drop. Same as bus-level rule.
				}
			}
		}
	}()

	unsub := func() {
		b.mu.Lock()
		ch, ok := b.subscribers[id]
		if ok {
			delete(b.subscribers, id)
		}
		b.mu.Unlock()
		// Close the bus-side channel to signal the translator to
		// drain + exit. Done in a goroutine to avoid blocking
		// callers if the channel is currently in a fan-out write.
		if ok {
			close(done)
			// Drain `ch` to free any pending writes; the channel
			// stays referenced by the translator until done is
			// observed, then GC reclaims.
			go func() {
				for range ch {
				}
			}()
		}
	}
	return out, unsub
}

// ── Helpers ──────────────────────────────────────────────────────────

// MarshalJSONLine returns ev as a single-line JSON document suitable
// for SSE `data:` framing. Reusable for tests and for the SSE handler.
func MarshalJSONLine(ev Event) ([]byte, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	// Defensive — strip newlines so a SSE `data:` frame stays
	// single-line. JSON encoders never emit a literal newline inside
	// an object so the cost is essentially zero.
	return []byte(strings.ReplaceAll(string(b), "\n", " ")), nil
}
