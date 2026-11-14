package events

// Per ADR-0001 §6, the OpenOva canonical event spine is NATS JetStream with
// subject convention `catalyst.<domain>.<event>`. NATS is the only event
// bus of the Organization services (tenant, billing, notification,
// provisioning, domain). On a Sovereign it runs at
// nats-jetstream.nats-system.svc.cluster.local:4222.
//
// This file holds the thin BrokerPublisher abstraction, so the HTTP
// handlers call a single Publish method.
//
// Subject derivation: each call site knows the event.Type (e.g.
// "tenant.created"). The canonical NATS subject is `catalyst.<event.Type>`.
// Publish derives it, so call sites carry no subject names.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// BrokerPublisher is the unified publish surface every Organization service uses
// for outbound domain events. MultiPublisher (this file) is the one
// implementation; the services' tests substitute fakes.
type BrokerPublisher interface {
	// Publish writes the event to the canonical NATS subject, derived
	// from event.Type as catalyst.<event.Type>. A NATS failure is
	// returned so the caller can log/observe it.
	//
	// topic is not used. It was the topic name on a second bus that no
	// longer exists; the argument stays so that the call sites and the
	// test fakes keep one signature.
	Publish(ctx context.Context, topic string, event *Event) error
	// Close releases the underlying connection.
	Close()
}

// MultiPublisher publishes an event to NATS JetStream.
//
// NewMultiPublisher refuses to return a publisher without a NATS
// connection — that would silently drop every event and is the exact
// failure mode that blocked convergence in the first place.
type MultiPublisher struct {
	nats *NATSConn
}

// NewMultiPublisher constructs a publisher wired to the supplied NATS
// connection.
//
// Returns an error if the connection is nil — a publisher with no
// output is always a configuration bug (the events would be silently
// dropped).
func NewMultiPublisher(nc *NATSConn) (*MultiPublisher, error) {
	if nc == nil {
		return nil, errors.New("events: NewMultiPublisher requires a NATS connection")
	}
	return &MultiPublisher{nats: nc}, nil
}

// CanonicalSubject returns the canonical NATS JetStream subject for an
// event.Type per ADR-0001 §6 (`catalyst.<event.Type>`). Exported so
// tests + alternate publishers can derive the same name without
// duplicating the convention.
//
// Examples:
//
//	"tenant.created"               → "catalyst.tenant.created"
//	"order.placed"                 → "catalyst.billing.order.placed"
//	"tenant.app_install_requested" → "catalyst.tenant.app_install_requested"
//
// The `order.placed` → `catalyst.billing.order.placed` mapping mirrors
// the rule the user spelled out: billing's order events live under the
// `billing` domain even though the event.Type itself does not contain
// "billing" (the producer-side domain is implied by the service name).
func CanonicalSubject(eventType string) string {
	// Empty or already-canonical event types are passed through as-is.
	if eventType == "" {
		return ""
	}
	if strings.HasPrefix(eventType, "catalyst.") {
		return eventType
	}
	// Billing's order events sit under the billing domain by convention.
	// Without this special-case `order.placed` would become
	// `catalyst.order.placed` which mixes billing concerns with the
	// `order` domain that other services may later own (refunds,
	// disputes). Naming bug we deliberately don't ship.
	if strings.HasPrefix(eventType, "order.") {
		return "catalyst.billing." + eventType
	}
	return "catalyst." + eventType
}

// Publish writes the event to its canonical NATS subject and returns a
// NATS publish error. The topic argument is not used (see
// BrokerPublisher).
func (m *MultiPublisher) Publish(ctx context.Context, _ string, event *Event) error {
	if event == nil {
		return errors.New("events: Publish nil event")
	}
	subject := CanonicalSubject(event.Type)

	var natsErr error
	if m.nats != nil && subject != "" {
		natsErr = m.nats.PublishEvent(ctx, subject, event)
		if natsErr != nil {
			// Surface so the HTTP handler's slog.Error captures it.
			natsErr = fmt.Errorf("nats publish %s: %w", subject, natsErr)
		}
	}
	return natsErr
}

// Close releases the NATS connection.
func (m *MultiPublisher) Close() {
	if m.nats != nil {
		m.nats.Close()
	}
}
