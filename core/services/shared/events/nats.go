package events

// Per #795 [Q-mine-3] + #798 + ADR-0001 §6, NATS JetStream is the canonical
// event bus. Every event of the Organization services — the domain
// events of bridge.go and the `catalyst.usage.recorded` metering
// envelopes — flows through the JetStream surface defined here.
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
//
// New subjects MUST follow the catalyst.<domain>.<event> shape.
const (
	// SubjectUsageRecorded carries one metered LLM-request envelope per
	// completed customer call. Produced by the NewAPI metering sidecar
	// (#798 §A); consumed by org-billing's metering_consumer (#798 §B).
	// Payload shape: see UsageRecordedPayload below.
	SubjectUsageRecorded = "catalyst.usage.recorded"

	// ConsumerOrgBillingMetering is the durable consumer name on
	// StreamCatalystOrg that org-billing reads from. Durable name lives
	// here so the publisher (sidecar) and the consumer (billing) cannot
	// drift on naming.
	ConsumerOrgBillingMetering = "org-billing-metering"
)

// UsageRecordedPayload is the JSON shape published on
// SubjectUsageRecorded. Field names match the issue spec verbatim so the
// NewAPI metering sidecar (Go) and any future non-Go publisher emit the
// same envelope.
type UsageRecordedPayload struct {
	// CustomerID is the Organization-vcluster Keycloak user UUID propagated from
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

// NATSPublisher is the interface org-billing's HTTP handler and the
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
// No Go code creates a Stream: the chart bp-nats-jetstream declares
// CATALYST_ORG and a service binds to it (see EnsureCatalystOrgStream).
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

// EnsureCatalystOrgStream binds to the canonical CATALYST_ORG Stream
// (subjects `catalyst.>`) and returns an error while it is absent. It
// never creates the Stream: the chart bp-nats-jetstream declares it
// (platform/nats-jetstream/chart/templates/streams.yaml) and its JetStream
// controller creates it.
//
// CATALYST_ORG backs every `catalyst.<domain>.<event>` subject on a
// Sovereign — tenant events, billing events, domain events, AND the
// `catalyst.usage.recorded` metering envelopes. A second Stream on that
// subject cannot exist: JetStream refuses two Streams with overlapping
// subject filters (NATS error 10065).
//
// This is the public wrapper around the package-private ensureOrgStream
// helper used by NewMultiSubscriber. org-billing calls it at startup and
// exits while the Stream is absent, so it starts only once the metering
// sidecar (which deliberately does NOT call any Ensure* — see
// services/metering-sidecar/main.go) can publish.
func (c *NATSConn) EnsureCatalystOrgStream(ctx context.Context) error {
	return c.ensureOrgStream(ctx, StreamCatalystOrg, []string{"catalyst.>"})
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
// route the tenant.created / order.placed / tenant.deleted events onto
// the canonical `catalyst.<event.Type>` NATS subjects per ADR-0001 §6.
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

// SubscribeUsageRecordedOnOrg creates (or attaches to) the durable
// consumer ConsumerOrgBillingMetering on the canonical CATALYST_ORG
// Stream and returns a NATSSubscription ready to Consume. The consumer
// uses FilterSubject = `catalyst.usage.recorded` so it ONLY receives
// metering envelopes even though CATALYST_ORG also carries tenant,
// billing, domain, and provisioning events.
//
// Stream ownership: the chart bp-nats-jetstream declares CATALYST_ORG.
// EnsureCatalystOrgStream (which org-billing calls at startup) and
// NewMultiSubscriber (which every other Organization service's
// tenant-events consumer calls) only bind to it.
func (c *NATSConn) SubscribeUsageRecordedOnOrg(ctx context.Context) (NATSSubscription, error) {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamCatalystOrg, jetstream.ConsumerConfig{
		Durable:       ConsumerOrgBillingMetering,
		Description:   "org-billing metering consumer (#798) — CATALYST_ORG filter scope.",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		FilterSubject: SubjectUsageRecorded,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    -1,
	})
	if err != nil {
		return nil, fmt.Errorf("events: subscribe usage on CATALYST_ORG: %w", err)
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
