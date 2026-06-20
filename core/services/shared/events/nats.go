package events

// Per #795 [Q-mine-3] + #798 + ADR-0001 §6, NATS JetStream is the canonical
// event bus going forward. The legacy RedPanda producer/consumer in
// events.go remains for backward compatibility with org.tenant.events,
// org.order.events, org.user.events (consumed by services that have not
// yet migrated). NEW topics — starting with `catalyst.usage.recorded` —
// flow through the JetStream surface defined here.
//
// Why a thin abstraction over nats.go and jetstream:
//
//   - Tests substitute a fake (NATSPublisher / NATSSubscription
//     interfaces) instead of bringing up an embedded NATS server.
//   - The publisher's connection options are centralised so retry +
//     reconnect semantics are uniform across services.
//   - The subscription helper hides the JetStream durable-consumer
//     bootstrap so call sites stay short and readable.
//
// The package intentionally does NOT wrap every JetStream concept; it
// only exposes what billing's metering subscriber, the NewAPI metering
// sidecar, and the future unified-rbac NATS publishers need.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATS canonical subjects. Canonical convention from ADR-0001 §6:
//
//	catalyst.<domain>.<event>            // platform / control-plane events
//	sme.<producer>.events                // legacy SME bus (RedPanda topics)
//
// New subjects MUST follow the catalyst.<domain>.<event> shape.
const (
	// SubjectUsageRecorded carries one metered LLM-request envelope per
	// completed customer call. Produced by the NewAPI metering sidecar
	// (#798 §A); consumed by sme-billing's metering_consumer (#798 §B).
	// Payload shape: see UsageRecordedPayload below.
	SubjectUsageRecorded = "catalyst.usage.recorded"

	// StreamCatalystUsage is the JetStream Stream that retains envelopes
	// on SubjectUsageRecorded. The Stream is created idempotently by
	// EnsureUsageStream during sme-billing startup (and by any other
	// service that publishes/consumes it on cold start). 14-day retention
	// gives ample replay window for billing reconciliation; storage stays
	// bounded via MaxBytes set in the JetStream chart.
	StreamCatalystUsage = "CATALYST_USAGE"

	// ConsumerOrgBillingMetering is the durable consumer name on
	// StreamCatalystUsage that sme-billing reads from. Durable name lives
	// here so the publisher (sidecar) and the consumer (billing) cannot
	// drift on naming.
	ConsumerOrgBillingMetering = "org-billing-metering"
)

// UsageRecordedPayload is the JSON shape published on
// SubjectUsageRecorded. Field names match the issue spec verbatim so the
// NewAPI metering sidecar (Go) and any future non-Go publisher emit the
// same envelope.
type UsageRecordedPayload struct {
	// CustomerID is the SME-vcluster Keycloak user UUID propagated from
	// NewAPI's `external_id` field (ADR-0003 §3.2). When the upstream
	// request did not carry an authenticated user (admin probe, health
	// check), this MUST be empty and the subscriber will skip the row.
	CustomerID string `json:"customer_id"`

	// AmountOMR is the signed OMR amount for human-readable observability
	// + back-compat with [Q-mine-3]'s example payload. Negative = usage
	// spend, positive = top-up (top-ups should never come over this
	// subject, but the schema keeps the door open). The authoritative
	// integer-precision value is AmountMicroOMR.
	AmountOMR float64 `json:"amount_omr"`

	// AmountMicroOMR is the canonical signed amount in micro-OMR
	// (1 OMR = 1,000,000 micro-OMR). The sidecar SHOULD set this; if it
	// is zero AND AmountOMR is non-zero, the subscriber computes
	// AmountMicroOMR = round(AmountOMR * 1e6).
	AmountMicroOMR int64 `json:"amount_micro_omr,omitempty"`

	// Reason follows the convention `usage:newapi:<model>` — billing
	// rolls usage up by `LIKE 'usage:newapi:%'` for per-model breakdowns.
	Reason string `json:"reason"`

	// Metadata is the raw envelope (model, tokens_used, request_id,
	// tenant_id, latency_ms, completed_at) preserved for analytics.
	// The subscriber pulls request_id out for idempotency dedup.
	Metadata UsageRecordedMetadata `json:"metadata"`
}

// UsageRecordedMetadata is the shape of UsageRecordedPayload.Metadata.
type UsageRecordedMetadata struct {
	TokensUsed   int    `json:"tokens_used"`
	Model        string `json:"model"`
	RequestID    string `json:"request_id"`
	TenantID     string `json:"tenant_id"`
	LatencyMS    int    `json:"latency_ms,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// NATSPublisher is the interface sme-billing's HTTP handler and the
// metering sidecar use for at-least-once publication. Tests substitute
// a fake; production wires NATSConn.
type NATSPublisher interface {
	// PublishUsage publishes one envelope on SubjectUsageRecorded with
	// JetStream ack. Returns nil only after the broker has acked the
	// message; transient errors bubble up so the caller can persist
	// the envelope for retry.
	PublishUsage(ctx context.Context, payload UsageRecordedPayload) error
	// Close shuts down the publisher's underlying NATS connection.
	Close()
}

// NATSConn is the JetStream-backed NATSPublisher implementation. The
// connection is configured for resilient publishing: max-reconnect set
// to a large bound (so the sidecar does not give up during a Sovereign-
// cluster NATS rollout) and PingInterval keeps the connection healthy
// behind ingress/load-balancer idle timeouts.
type NATSConn struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// ConnectNATS opens a NATS + JetStream connection for publishing. URL
// is the canonical nats://host:port the chart exposes; pass empty to
// fall back to nats.DefaultURL (handy in tests where a URL env var is
// not configured).
//
// On success the returned NATSConn is ready for PublishUsage calls.
// EnsureUsageStream MUST be called once at startup by whichever
// service owns the Stream lifecycle (sme-billing); publishers that do
// not own the Stream may rely on the consumer side to have created it.
func ConnectNATS(url string) (*NATSConn, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	nc, err := nats.Connect(url,
		nats.Name("catalyst-services"),
		nats.MaxReconnects(-1),                // retry forever
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
	)
	if err != nil {
		return nil, fmt.Errorf("events: nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("events: jetstream init: %w", err)
	}
	return &NATSConn{nc: nc, js: js}, nil
}

// EnsureUsageStream is DEPRECATED — kept only as a back-compat shim for
// any caller that has not yet migrated to the canonical CATALYST_SME
// Stream (subjects `catalyst.>`). t20 (2026-05-18) caught the bug this
// deprecation exists to prevent: when CATALYST_SME already exists with
// subject filter `catalyst.>` (created by the tenant / notification /
// provisioning MultiSubscribers — any consumer of `catalyst.tenant.*`),
// creating CATALYST_USAGE with subject `catalyst.usage.recorded` fails
// with NATS error code 10065 "subjects overlap with an existing stream".
// JetStream forbids two Streams from owning overlapping subject filters.
//
// New code MUST NOT call EnsureUsageStream. Billing now consumes
// `catalyst.usage.recorded` directly off CATALYST_SME via
// SubscribeUsageRecordedOnSME, which uses a consumer-side FilterSubject
// to scope reads to the metering envelopes only. The metering sidecar
// publishes to the same subject — JetStream routes the envelope to
// CATALYST_SME on the basis of its `catalyst.>` filter.
//
// Use EnsureCatalystSMEStream instead.
//
// Deprecated: use EnsureCatalystSMEStream + SubscribeUsageRecordedOnSME.
func (c *NATSConn) EnsureUsageStream(ctx context.Context) error {
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamCatalystUsage,
		Description: "Catalyst per-LLM-request metering envelopes (#798).",
		Subjects:    []string{SubjectUsageRecorded},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      14 * 24 * time.Hour,
		Replicas:    1,
	})
	if err != nil {
		return fmt.Errorf("events: ensure usage stream: %w", err)
	}
	return nil
}

// EnsureCatalystSMEStream creates the canonical CATALYST_SME Stream
// (subjects `catalyst.>`) if it does not already exist. Idempotent — safe
// to call on every startup.
//
// CATALYST_SME backs every `catalyst.<domain>.<event>` subject on a
// Sovereign — tenant events, billing events, domain events, AND the
// `catalyst.usage.recorded` metering envelopes that previously lived on
// the now-deprecated CATALYST_USAGE Stream (see EnsureUsageStream
// comment above for the t20 incident).
//
// This is the public wrapper around the package-private ensureSMEStream
// helper used by NewMultiSubscriber. Exposed so services that own a
// publish OR consume relationship to `catalyst.*` subjects can bootstrap
// the Stream at process start — first-writer-wins is fine because the
// underlying CreateOrUpdateStream call is a no-op on second invocation.
//
// Specifically: sme-billing calls this at startup so the metering sidecar
// (which deliberately does NOT call any Ensure* — see
// services/metering-sidecar/main.go) can begin publishing immediately
// after a fresh Sovereign cold-start, with billing-side bootstrap
// guaranteeing the Stream exists.
func (c *NATSConn) EnsureCatalystSMEStream(ctx context.Context) error {
	return c.ensureSMEStream(ctx, StreamCatalystSME, []string{"catalyst.>"})
}

// PublishUsage publishes one usage envelope on SubjectUsageRecorded
// with JetStream Ack. Implements NATSPublisher.
func (c *NATSConn) PublishUsage(ctx context.Context, payload UsageRecordedPayload) error {
	if c.js == nil {
		return errors.New("events: nats publisher not initialised")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("events: marshal usage: %w", err)
	}
	// Use the request_id (when present) as the JetStream Msg-Id so
	// duplicate publishes inside the broker's de-dup window collapse
	// at the broker side too — the subscriber's external_ref dedup
	// is the second line of defence.
	opts := []jetstream.PublishOpt{}
	if payload.Metadata.RequestID != "" {
		opts = append(opts, jetstream.WithMsgID(payload.Metadata.RequestID))
	}
	_, err = c.js.Publish(ctx, SubjectUsageRecorded, body, opts...)
	if err != nil {
		return fmt.Errorf("events: publish usage: %w", err)
	}
	return nil
}

// PublishEvent publishes a generic domain Event envelope (events.go) on
// the supplied JetStream subject. Used by MultiPublisher (bridge.go) to
// route the same tenant.created / order.placed / tenant.deleted events
// that legacy code emitted to Redpanda onto the canonical
// `catalyst.<event.Type>` NATS subjects per ADR-0001 §6.
//
// Event.ID is set as the JetStream Msg-Id so duplicate publishes inside
// the broker's de-dup window collapse at the broker — gives the
// at-least-once HTTP retry path an idempotency guarantee end-to-end.
//
// This deliberately does NOT require any per-subject Stream to exist:
// JetStream brokers configured with the default `catalyst.>` Stream (or
// a per-domain Stream listening on `catalyst.tenant.>` /
// `catalyst.billing.>`) will pick the message up automatically. If no
// matching Stream exists the broker returns "no responders" which
// surfaces as an error here — surfacing the misconfiguration is correct,
// silently dropping events is what got us into this mess.
func (c *NATSConn) PublishEvent(ctx context.Context, subject string, event *Event) error {
	if c == nil || c.js == nil {
		return errors.New("events: nats publisher not initialised")
	}
	if subject == "" {
		return errors.New("events: PublishEvent requires a subject")
	}
	if event == nil {
		return errors.New("events: PublishEvent nil event")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("events: marshal event: %w", err)
	}
	opts := []jetstream.PublishOpt{}
	if event.ID != "" {
		opts = append(opts, jetstream.WithMsgID(event.ID))
	}
	if _, err := c.js.Publish(ctx, subject, body, opts...); err != nil {
		return fmt.Errorf("events: publish %s: %w", subject, err)
	}
	return nil
}

// Close shuts down the underlying NATS connection.
func (c *NATSConn) Close() {
	if c.nc != nil {
		c.nc.Drain()
	}
}

// NATSSubscription is the interface the metering subscriber consumes.
// It exists so tests can drive Consume with a synchronous fake instead
// of a live JetStream consumer.
type NATSSubscription interface {
	// Consume registers the handler on the underlying durable consumer
	// and blocks until the context is cancelled. The handler MUST call
	// msg.Ack() on success and msg.Nak() (with optional delay) on
	// transient failure — failure to ack will cause JetStream to redeliver
	// after the consumer's AckWait expires.
	Consume(ctx context.Context, handler func(jetstream.Msg)) error
	// Close releases the consumer.
	Close()
}

// natsSub is the JetStream-backed NATSSubscription implementation.
type natsSub struct {
	cons jetstream.Consumer
	cc   jetstream.ConsumeContext
}

// SubscribeUsageRecorded is DEPRECATED — kept only as a back-compat
// shim for the legacy CATALYST_USAGE Stream layout. New code MUST call
// SubscribeUsageRecordedOnSME instead. See the comment on
// EnsureUsageStream for the t20 stream-overlap incident this deprecation
// prevents.
//
// Deprecated: use SubscribeUsageRecordedOnSME.
func (c *NATSConn) SubscribeUsageRecorded(ctx context.Context) (NATSSubscription, error) {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamCatalystUsage, jetstream.ConsumerConfig{
		Durable:       ConsumerOrgBillingMetering,
		Description:   "sme-billing metering consumer (#798).",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		FilterSubject: SubjectUsageRecorded,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    -1,
	})
	if err != nil {
		return nil, fmt.Errorf("events: subscribe usage: %w", err)
	}
	return &natsSub{cons: cons}, nil
}

// SubscribeUsageRecordedOnSME creates (or attaches to) the durable
// consumer ConsumerOrgBillingMetering on the canonical CATALYST_SME
// Stream and returns a NATSSubscription ready to Consume. The consumer
// uses FilterSubject = `catalyst.usage.recorded` so it ONLY receives
// metering envelopes even though CATALYST_SME also carries tenant,
// billing, domain, and provisioning events.
//
// AckPolicy + AckWait + MaxDeliver match SubscribeUsageRecorded so the
// observed delivery semantics are identical to the deprecated path.
//
// Stream ownership: CATALYST_SME is bootstrapped by
// EnsureCatalystSMEStream (which sme-billing calls at startup) AND by
// NewMultiSubscriber (which every other SME service's tenant-events
// consumer calls). First-writer-wins is fine because the underlying
// CreateOrUpdateStream call is idempotent on the (`catalyst.>` subject
// filter, file storage, 14d retention) tuple.
func (c *NATSConn) SubscribeUsageRecordedOnSME(ctx context.Context) (NATSSubscription, error) {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamCatalystSME, jetstream.ConsumerConfig{
		Durable:       ConsumerOrgBillingMetering,
		Description:   "sme-billing metering consumer (#798) — CATALYST_SME filter scope.",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		FilterSubject: SubjectUsageRecorded,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    -1,
	})
	if err != nil {
		return nil, fmt.Errorf("events: subscribe usage on CATALYST_SME: %w", err)
	}
	return &natsSub{cons: cons}, nil
}

// Consume implements NATSSubscription.
func (s *natsSub) Consume(ctx context.Context, handler func(jetstream.Msg)) error {
	cc, err := s.cons.Consume(handler)
	if err != nil {
		return fmt.Errorf("events: consume start: %w", err)
	}
	s.cc = cc
	<-ctx.Done()
	cc.Stop()
	return nil
}

// Close releases the JetStream consume context.
func (s *natsSub) Close() {
	if s.cc != nil {
		s.cc.Stop()
	}
}
