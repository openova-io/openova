// Package audit is the audit trail of catalyst-api: type-tagged Event
// records, an in-process ring buffer that the listing routes read, an SSE
// fan-out for live-tail consumers, and a Publisher interface that main.go
// binds to the JetStream publisher of internal/natspub when
// CATALYST_NATS_URL is set.
//
// The durable record is the subject `catalyst.audit.recorded.v1` of the
// JetStream stream CATALYST_ORG (contract C3,
// docs/contracts/C3-event-subjects.yaml). There is NO audit database. The
// ring buffer is a mirror of the newest events: main.go fills it at start
// from the stream (Bus.Load), so a restart of the Pod loses no event that
// `GET /api/v1/sovereigns/{id}/audit/rbac` listed. Without a Publisher the
// ring is all there is, and Bus.Transport says so.
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
// ── Publish and PublishStrict ─────────────────────────────────────────
//
// Publish records a change that has already happened: the event goes to
// the ring and the subscribers, then to the Publisher, and a Publisher
// error is logged, not returned. A nil Publisher is safe: the ring and
// the SSE fan-out still work.
//
// PublishStrict is for a caller that must not act without a durable
// record: it returns the Publisher's error, or ErrNoPublisher, and then
// the event is not in the ring either. Both write one log line per event
// with the key `audit` set to true.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
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

// Event is the on-the-wire record: the `data` of the CloudEvent published
// to `catalyst.audit.recorded.v1`, and one item of a listing. The
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

	// ID — the identifier of the event: a ULID, and the id of its
	// envelope on the stream. The Bus sets it when it is empty.
	ID string `json:"id"`

	// Phase — PhaseIntent for the event published before an operation
	// runs, PhaseResult for the one published after it. Empty for an
	// event that records one finished change.
	Phase string `json:"phase,omitempty"`
}

// The values of Event.Phase.
const (
	PhaseIntent = "intent"
	PhaseResult = "result"
)

// EventScope is one (key, value) pair in Event.Scopes. Mirrors
// rbacAssignScopeBody so the rbac_assign.go emit-side can pass a
// converted slice without redeclaring the shape in this package.
type EventScope struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ── Publisher interface (NATS adapter binding point) ─────────────────

// Publisher publishes an Event to the subject catalyst.audit.recorded.v1
// of the JetStream stream. Production main.go binds this to
// natspub.AuditPublisher (when CATALYST_NATS_URL is set); tests bind a
// recorder. Nil-tolerant on the consumer side — see audit.Bus.Publish.
type Publisher interface {
	// Publish writes the event to the stream and returns only after the
	// stream acknowledged it. ev.ID is set. MUST NOT panic on error.
	Publish(ctx context.Context, ev Event) error
}

// The values of Bus.Transport.
const (
	// TransportInProcess: no Publisher is set. The ring buffer is the
	// only record, and a restart of the Pod empties it.
	TransportInProcess = "in-process"
	// TransportJetStream: every event goes to the JetStream stream.
	TransportJetStream = "jetstream:" + natsbus.StreamCatalystOrg
)

// ErrNoPublisher is what PublishStrict returns when the Bus has no
// Publisher: the event cannot be recorded durably.
var ErrNoPublisher = errors.New("audit: no publisher is set")

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
//   - an optional Publisher for persistent storage
//     (the subject catalyst.audit.recorded.v1 of the JetStream stream)
//
// Bus.Publish and Bus.PublishStrict are the two entry points — each
// appends to the ring, fans out to subscribers, forwards to the
// Publisher when set and writes the log line. Callers don't have to
// coordinate.
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
	// also forwarded. Never changed after NewBus.
	publisher Publisher

	// log — receives one line per event. Nil: no line is written.
	log *slog.Logger
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

	// Log receives one line per event, with the key `audit` set to true
	// and the event under `event`. Production main.go passes the API's
	// JSON logger. Nil ⇒ no line is written.
	Log *slog.Logger
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
		log:         cfg.Log,
	}
}

// Transport says where the events of this Bus are recorded:
// TransportJetStream when a Publisher is set, TransportInProcess
// otherwise. The listing routes report it.
func (b *Bus) Transport() string {
	if b.publisher != nil {
		return TransportJetStream
	}
	return TransportInProcess
}

// stamp fills what the Bus owns: `ev.Timestamp` is set to
// time.Now().UTC() if zero, `ev.Result` defaults to "ok" when empty,
// and `ev.ID` is a new ULID when empty.
func stamp(ev Event) Event {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	} else {
		ev.Timestamp = ev.Timestamp.UTC()
	}
	if ev.Result == "" {
		ev.Result = "ok"
	}
	if ev.ID == "" {
		ev.ID = natsbus.NewID()
	}
	return ev
}

// Publish appends `ev` to the ring buffer, fans out to all
// subscribers (best-effort, drops to full channels), and forwards to
// the Publisher when configured. A Publisher error is written to the
// log line and not returned: the event stays in the ring.
//
// Use it for a change that has already happened. A caller that must
// not act without a durable record uses PublishStrict.
func (b *Bus) Publish(ctx context.Context, ev Event) {
	ev = stamp(ev)
	b.record(ev)
	var err error
	if b.publisher != nil {
		err = b.publisher.Publish(ctx, ev)
	}
	b.logEvent(ctx, ev, err)
}

// PublishStrict forwards `ev` to the Publisher first and waits for the
// stream's acknowledgement. Only then does it append the event to the
// ring buffer and fan it out. It returns the Publisher's error, or
// ErrNoPublisher when no Publisher is set; the event is then not in
// the ring, because the stream does not hold it. The log line is
// written in both cases.
func (b *Bus) PublishStrict(ctx context.Context, ev Event) error {
	ev = stamp(ev)
	err := ErrNoPublisher
	if b.publisher != nil {
		err = b.publisher.Publish(ctx, ev)
	}
	b.logEvent(ctx, ev, err)
	if err != nil {
		return fmt.Errorf("audit: event %s (%s) is not recorded: %w", ev.ID, ev.AuditType, err)
	}
	b.record(ev)
	return nil
}

// Load appends events that the stream already holds to the ring
// buffer, oldest first, and returns how many it appended. It does not
// fan out, does not forward to the Publisher and writes no log line:
// the events are not new. main.go calls it once at start, before the
// first Publish, with the events natspub.AuditPublisher.Replay read.
// An event without an AuditType is skipped.
func (b *Bus) Load(events []Event) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, ev := range events {
		if ev.AuditType == "" {
			continue
		}
		b.ring[b.head] = ev
		b.head = (b.head + 1) % len(b.ring)
		if b.count < len(b.ring) {
			b.count++
		}
		n++
	}
	return n
}

// logEvent writes the log line of one event: the key `audit` is true,
// `event` holds the event, and `publishError` the Publisher's error
// when there is one (the line is then at level ERROR).
func (b *Bus) logEvent(ctx context.Context, ev Event, publishErr error) {
	if b.log == nil {
		return
	}
	if publishErr != nil {
		b.log.LogAttrs(ctx, slog.LevelError, "audit event", slog.Bool("audit", true),
			slog.Any("event", ev), slog.String("publishError", publishErr.Error()))
		return
	}
	b.log.LogAttrs(ctx, slog.LevelInfo, "audit event", slog.Bool("audit", true), slog.Any("event", ev))
}

// record appends `ev` to the ring buffer and fans it out to all
// subscribers (best-effort, drops to full channels).
func (b *Bus) record(ev Event) {
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
