// Package events publishes Continuum audit events to NATS JetStream
// subject `catalyst.audit` (per ADR-0001 §3 — NATS is the canonical
// audit transport, never a separate audit DB).
//
// K-Cont-1 reserved the audit-event-types in a doc-only placeholder;
// K-Cont-2 (this file) ships the real Publisher interface, the
// JetStream implementation (using github.com/nats-io/nats.go), and
// the in-memory recorder used by unit tests.
//
// Subscribers (chiefly the U-DR-1 UI's switchover-history panel)
// filter on `catalyst.audit` with a header selector
// `audit-type=continuum-*`. The 9 reserved audit-types are exposed
// as constants below — adding a new one is a CRD-shape-level change
// (UI subscribes to the union).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4, the NATS URL + stream name
// are runtime config. Tests use the in-memory Recorder instead.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Subject for every Continuum audit event. ADR-0001 §3:
// JetStream `catalyst.audit` is the canonical audit subject.
const Subject = "catalyst.audit"

// Audit-type constants — the 9 reserved values from K-Cont-2 plus 3
// added by slice F (#1101 F-1) for full state-transition coverage.
//
// K-Cont-1 documented the originals in placeholder; K-Cont-2 shipped
// them as exported constants. F-1 extends the set with three audit
// emits the K-Cont-2 reconciler did NOT cover:
//
//   - TypeCRCreated        — CR Reconcile-on-Create (per-CR goroutine spin-up)
//   - TypeConfigChanged    — CR Update where switchover-relevant fields changed
//   - TypeLeaseCollision   — Acquire returned ErrLeaseHeldByAnother during
//     opportunistic re-acquire (the rare two-region
//     lease race that is split-brain warning material)
//
// Schemas for the new types are documented in
// products/continuum/DESIGN.md §"Slice F audit-type schemas".
//
// #5125 Defect-2 adds one more: TypeRejoinRepair — step-8's
// re-clone-on-divergence outcome (attempted / bounded-fail-loud).
const (
	TypeSwitchover        = "continuum-switchover"
	TypeFailbackPending   = "continuum-failback-pending"
	TypeFailbackCompleted = "continuum-failback-completed"
	TypeLeaseLost         = "continuum-lease-lost"
	TypeLeaseAcquired     = "continuum-lease-acquired"
	TypeCNPGLagBreach     = "continuum-cnpg-lag-breach"
	TypeCNPGPromotable    = "continuum-cnpg-promotable"
	TypeError             = "continuum-error"
	TypeReconcileSuccess  = "continuum-reconcile-success"

	// F-1 additions (#1101).
	TypeCRCreated      = "continuum-cr-created"
	TypeConfigChanged  = "continuum-config-changed"
	TypeLeaseCollision = "continuum-lease-collision"

	// #5125 Defect-2 addition — step-8 rejoin-repair outcome.
	TypeRejoinRepair = "continuum-rejoin-repair"
)

// AuditTypes is the canonical list — used by tests + by U-DR-1's
// audit-subscription startup wiring (filter validation).
//
// Order is K-Cont-2's original 9 first, then F-1's 3 — additions
// MUST go at the end so existing index-pinned tests keep working.
var AuditTypes = []string{
	TypeSwitchover,
	TypeFailbackPending,
	TypeFailbackCompleted,
	TypeLeaseLost,
	TypeLeaseAcquired,
	TypeCNPGLagBreach,
	TypeCNPGPromotable,
	TypeError,
	TypeReconcileSuccess,
	TypeCRCreated,
	TypeConfigChanged,
	TypeLeaseCollision,
	TypeRejoinRepair,
}

// IsValidType reports whether `t` is one of the 9 reserved values.
func IsValidType(t string) bool {
	for _, v := range AuditTypes {
		if v == t {
			return true
		}
	}
	return false
}

// Event is the wire shape of every Continuum audit emit.
//
// Schema is stable and CONTRACT-LEVEL — U-DR-1's switchover-history
// panel decodes against this struct, so a field rename here breaks
// the UI. New fields can be added (omitempty) without breaking
// existing subscribers; renames or removals require a coordinated
// EPIC change.
type Event struct {
	// Type is the audit-type discriminator. Must be one of AuditTypes.
	// JetStream consumers MAY filter via subject token + this field;
	// the JetStream Publisher also stamps it as the message header
	// `audit-type` for header-based filtering.
	Type string `json:"type"`

	// ContinuumName is `<namespace>/<name>` of the Continuum CR that
	// generated the event.
	ContinuumName string `json:"continuumName"`

	// ApplicationName is `<namespace>/<name>` of the Application
	// referenced by the Continuum CR. Empty when the event is about
	// the Continuum CR itself (e.g. continuum-error before app
	// resolution).
	ApplicationName string `json:"applicationName,omitempty"`

	// FromPrimary is the host-cluster name that WAS primary at the
	// start of the event. Empty for first-time-acquired events.
	FromPrimary string `json:"fromPrimary,omitempty"`

	// ToPrimary is the host-cluster name that IS primary after the
	// event. For non-promotion events (LagBreach, LeaseAcquired with
	// no flip) it equals FromPrimary.
	ToPrimary string `json:"toPrimary,omitempty"`

	// LagSeconds is the observed CNPG replication lag at event time.
	// Zero/missing for non-replication events.
	LagSeconds int `json:"lagSeconds,omitempty"`

	// Step (for switchover progression events) is "N/M" — the
	// per-step audit emit lets U-DR-1 render a progress bar.
	Step string `json:"step,omitempty"`

	// Reason is a short machine-readable cause label (e.g.
	// "lag-breach", "operator-requested", "lease-quorum-lost").
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable summary suitable for surfacing in
	// the UI's audit-history row.
	Message string `json:"message,omitempty"`

	// InitiatedBy is the operator email or "auto-failover" /
	// "controller". Surface-on-UI for accountability.
	InitiatedBy string `json:"initiatedBy,omitempty"`

	// Timestamp is the RFC3339 wall-clock at event emit. Set by the
	// publisher; callers SHOULD leave zero so we get the canonical
	// UTC "now" stamp.
	Timestamp string `json:"timestamp"`
}

// Validate returns an error when the event is missing required
// fields. Used both by the publisher's pre-write check and by tests.
func (e Event) Validate() error {
	if !IsValidType(e.Type) {
		return fmt.Errorf("events: invalid Type %q (expected one of %v)", e.Type, AuditTypes)
	}
	if e.ContinuumName == "" {
		return errors.New("events: ContinuumName is required")
	}
	if e.Timestamp == "" {
		return errors.New("events: Timestamp is required")
	}
	return nil
}

// Publisher is the audit emit contract. Production wiring uses
// JetStreamPublisher (this package); tests use Recorder.
type Publisher interface {
	// Publish emits one Event onto subject `catalyst.audit`. The
	// implementation MUST stamp `audit-type` as a NATS message
	// header (not just a body field) so subscribers can filter
	// without decoding the body.
	//
	// Returns nil only after the broker (or in-memory recorder)
	// acknowledges receipt. Per ADR-0001 §3 lossy-fire-and-forget is
	// not acceptable — every Continuum audit MUST be durable.
	Publish(ctx context.Context, e Event) error
}

// FillTimestamp fills Timestamp with UTC RFC3339 when empty. Used by
// every publisher in this package to keep behavior consistent.
func FillTimestamp(e Event, now func() time.Time) Event {
	if e.Timestamp != "" {
		return e
	}
	if now == nil {
		now = time.Now
	}
	e.Timestamp = now().UTC().Format(time.RFC3339Nano)
	return e
}

// Recorder is an in-memory Publisher used by tests. Records every
// Publish call in order; concurrent-safe.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// NewRecorder returns a fresh in-memory recorder.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Publish implements Publisher. Validates the event, stamps a
// timestamp when missing, and appends to the in-memory list.
func (r *Recorder) Publish(ctx context.Context, e Event) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	e = FillTimestamp(e, nil)
	if err := e.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

// Events returns a snapshot copy of the recorded events.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// EventsByType returns the subset of recorded events with the given
// audit-type. Used by tests asserting "the switchover sequence
// emitted exactly these 7 step events".
func (r *Recorder) EventsByType(t string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Event{}
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Len returns the number of recorded events.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// Reset clears the recorder.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// Compile-time assertion.
var _ Publisher = (*Recorder)(nil)

// MarshalEvent returns a deterministic JSON encoding of an Event.
// Used by the JetStream publisher (body) and by tests asserting
// schema stability.
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}
