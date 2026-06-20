package events

// Per ADR-0001 §6, the OpenOva canonical event spine is NATS JetStream with
// subject convention `catalyst.<domain>.<event>`. Historically the Organization
// services (tenant, billing, notification, provisioning) emitted to
// Redpanda topics under the legacy `org.<producer>.events` shape because
// the bus was Redpanda on contabo. On Sovereigns there IS no Redpanda —
// only NATS at nats-jetstream.nats-system.svc.cluster.local:4222 — so
// every event the tenant + billing services published was silently lost,
// blocking convergence (provisioning never received tenant.created /
// order.placed, downstream subscribers never saw tenant.deleted).
//
// This file introduces a thin BrokerPublisher abstraction so the existing
// HTTP handlers can keep calling a single Publish method:
//
//   - On Sovereigns NATS_URL is set and Publish writes to JetStream
//     subjects (catalyst.<event.Type>) per the canonical convention.
//   - On legacy contabo the REDPANDA_BROKERS env is non-empty (and may
//     point at a real Redpanda) — Publish ALSO writes to the legacy
//     org.<producer>.events topic so any not-yet-migrated consumers
//     keep receiving events.
//   - Both can run together. NATS wins for new subscribers; Redpanda
//     keeps legacy consumers alive during the migration window.
//
// Subject derivation: each call site already knows the legacy Kafka topic
// it wants to write to AND the event.Type (e.g. "tenant.created"). The
// canonical NATS subject is `catalyst.<event.Type>`. The bridge handles
// both publishes inside one method so call sites don't grow conditional
// branches everywhere.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// BrokerPublisher is the unified publish surface every Organization service uses
// for outbound domain events. Implementations:
//
//   - MultiPublisher (this file): fans out to NATS + optional Redpanda.
//   - Producer (events.go): legacy Kafka-only path retained for tests
//     and the contabo-only fallback.
//
// The interface intentionally matches Producer.Publish so call sites can
// migrate by changing the field type without touching every Publish call.
type BrokerPublisher interface {
	// Publish writes the event to BOTH the canonical NATS subject
	// (derived from event.Type as catalyst.<event.Type>) AND, if the
	// implementation was configured with Kafka brokers, the legacy
	// kafkaTopic. Errors are best-effort — NATS failures bubble up so
	// the caller can log/observe, Kafka failures are logged but do not
	// fail the call (the legacy bus is non-authoritative on Sovereigns).
	Publish(ctx context.Context, kafkaTopic string, event *Event) error
	// Close releases the underlying connections.
	Close()
}

// MultiPublisher fans an event out to NATS (canonical) + Redpanda (legacy).
// Either side may be nil:
//
//   - nats == nil: pure-Kafka publisher (matches legacy contabo behaviour
//     when NATS_URL is empty).
//   - kafka == nil: pure-NATS publisher (matches the canonical Sovereign
//     wiring where REDPANDA_BROKERS is intentionally empty).
//
// At construction time NewMultiPublisher refuses to return a publisher
// with both sides nil — that would silently drop every event and is the
// exact failure mode that blocked convergence in the first place.
type MultiPublisher struct {
	nats   *NATSConn
	kafka  *Producer
}

// NewMultiPublisher constructs a publisher wired to the supplied NATS
// connection and/or Kafka producer. Pass nil for either side to disable
// that transport.
//
// Returns an error if BOTH transports are nil — a publisher with no
// outputs is always a configuration bug (the events would be silently
// dropped, which is what got us into this mess).
func NewMultiPublisher(nc *NATSConn, kp *Producer) (*MultiPublisher, error) {
	if nc == nil && kp == nil {
		return nil, errors.New("events: NewMultiPublisher requires at least one of NATS or Kafka")
	}
	return &MultiPublisher{nats: nc, kafka: kp}, nil
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

// Publish writes the event to NATS (canonical) and Kafka (legacy) if
// either transport is configured. NATS publish errors are returned;
// Kafka publish errors are logged but swallowed so a stale legacy
// broker cannot break a healthy Sovereign.
func (m *MultiPublisher) Publish(ctx context.Context, kafkaTopic string, event *Event) error {
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

	if m.kafka != nil && kafkaTopic != "" {
		if err := m.kafka.Publish(ctx, kafkaTopic, event); err != nil {
			// Legacy bus failures should NEVER fail a Sovereign-side
			// call — log + continue. Returning would block requests on
			// a Redpanda outage that is irrelevant to Sovereign flows.
			slog.Warn("events: legacy kafka publish failed",
				"topic", kafkaTopic, "type", event.Type, "error", err)
		}
	}

	return natsErr
}

// Close releases NATS + Kafka connections (whichever are non-nil).
func (m *MultiPublisher) Close() {
	if m.nats != nil {
		m.nats.Close()
	}
	if m.kafka != nil {
		m.kafka.Close()
	}
}
