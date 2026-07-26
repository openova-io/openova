// Package natsbus is a minimal NATS JetStream subscriber for the
// in-cluster Group-C controllers (organization-controller,
// sandbox-controller). It exists to close the consume-leg of DoD D35
// (`catalyst.tenant.created` / `catalyst.order.placed` /
// `catalyst.tenant.sandbox_requested` round-trip end-to-end) when the
// publish-leg PR #1626 wired into core/services/shared/events landed.
//
// The package is deliberately self-contained inside core/controllers/
// (separate Go module from core/services/shared/events) — copying the
// ~80 LOC of JetStream connect + durable-consumer attach is cheaper
// than dragging the entire core/services/shared/events package
// (BrokerPublisher / MultiSubscriber / Kafka transport) into a
// controller binary that only needs to subscribe.
//
// What the controllers do with each envelope is up to the caller —
// the package surface is intentionally narrow:
//
//   - Connect(url) → *Subscriber, Close() teardown.
//   - Subscriber.Subscribe(ctx, subject, durable, handler) starts a
//     durable JetStream consumer and dispatches every envelope to
//     handler. Handler returning nil → Ack; non-nil → Nak (5s
//     backoff so transient downstream blips don't hot-loop).
//   - Event mirrors core/services/shared/events.Event so the JSON
//     wire format is identical (id / type / source / timestamp /
//     tenant_id / data / metadata).
//
// The expected operational pattern is:
//
//	r.Reconcile is the canonical path for steady-state convergence.
//	NATS subscribers in main.go observe domain events as they fire
//	and enqueue the corresponding CR for reconcile (so the controller
//	responds within ~50ms instead of waiting up to 30s for the next
//	requeue). The 30s RequeueAfter inside r.Reconcile remains the
//	fallback — NATS message loss never strands a CR; the next
//	informer-driven Reconcile picks it up.
//
// Connection options mirror core/cmd/projector/internal/nats:
// MaxReconnects=-1 (retry forever — JetStream rollouts shouldn't
// crash-loop the controller), 2s ReconnectWait, 20s PingInterval.
//
// Per Inviolable Principle #4 every knob is env-driven; this package
// reads nothing from os.Getenv directly — main.go owns the env
// surface and passes URL + StreamName + DurableName into Connect /
// Subscribe.
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// StreamCatalystOrg is the canonical JetStream Stream backing every
// Organization convergence event (catalyst.tenant.*, catalyst.billing.*,
// catalyst.domain.*, catalyst.provision.*). Mirrors
// core/services/shared/events.StreamCatalystOrg — kept in sync by the
// constant lifting up to the chart / per-Sovereign overlay.
const StreamCatalystOrg = "CATALYST_ORG"

// Canonical subjects the Group-C controllers consume. Each constant
// matches the publish-side subject derived by
// core/services/shared/events.CanonicalSubject — kept in sync by the
// D35 test plan.
const (
	// SubjectTenantCreated fires when tenant-service finalises a new
	// tenant (publish-side PR #1626). organization-controller subscribes
	// so the corresponding Organization CR converges within ~50ms of
	// the marketplace checkout instead of waiting on the 30s fallback.
	SubjectTenantCreated = "catalyst.tenant.created"

	// SubjectOrderPlaced fires when billing-service records a paid
	// order (publish-side PR #1626). organization-controller subscribes
	// to observe the round-trip and trigger reconcile of the per-tenant
	// Organization CR (so day-1 app installs in the basket land on the
	// per-Org Gitea repo without the operator polling catalyst-api).
	SubjectOrderPlaced = "catalyst.billing.order.placed"

	// SubjectTenantSandboxRequested fires when the marketplace cart
	// contained the sandbox product (publish-side tenant-service in
	// PR #1633). sandbox-controller subscribes so the per-Sandbox
	// reconcile loop runs immediately on cart completion.
	SubjectTenantSandboxRequested = "catalyst.tenant.sandbox_requested"

	// SubjectTenantDeleted is the canonical NATS subject the
	// organization-controller PUBLISHES on when an Organization CR is being
	// finalized (#5364). It mirrors the subject core/services/shared/events
	// derives for the `tenant.deleted` event type (CanonicalSubject) so the
	// provisioning service's `tenant.deleted` consumer — subscribed to the same
	// CATALYST_ORG stream — runs the SECOND half of Org teardown (prune the
	// `org-tenants` gitops dir: the <slug> Namespace manifest + tenant
	// HelmReleases). The org-controller's own finalizer runs only the FIRST half
	// (per-Org vCluster Flux + tenant-networking); before #5364 a raw
	// `kubectl delete organization` never emitted this event (only the
	// tenant-service's DELETE /api/organizations/{id} route did), so the
	// org-tenants Kustomization perpetually recreated the ns + HRs.
	SubjectTenantDeleted = "catalyst.tenant.deleted"
)

// Event is the JSON envelope on every catalyst.* subject. The shape
// mirrors core/services/shared/events.Event exactly — fields are
// duplicated here (rather than imported) to keep the controllers
// module free of a dependency on core/services/shared, which pulls
// in franz-go + Kafka transports the controllers never touch.
type Event struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Source    string            `json:"source"`
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenant_id"`
	Data      json.RawMessage   `json:"data"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Subscriber holds an open NATS+JetStream connection. Construct via
// Connect; close via Close. Subscribe may be called from multiple
// goroutines — each call attaches an independent durable consumer.
type Subscriber struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Connect opens a NATS connection at url and binds a JetStream client
// on top. Empty url falls back to nats.DefaultURL so unit tests can
// exercise the package against a local nats-server without env wiring.
//
// Returns an error if the broker is unreachable; the caller (main.go)
// is expected to either bail out (NATS is canonical on Sovereigns) or
// log and continue (Catalyst-Zero / contabo, where REDPANDA_BROKERS
// is the authoritative bus).
func Connect(url string) (*Subscriber, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	nc, err := nats.Connect(url,
		nats.Name("catalyst-controllers"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
	)
	if err != nil {
		return nil, fmt.Errorf("natsbus: connect %s: %w", url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("natsbus: jetstream init: %w", err)
	}
	return &Subscriber{nc: nc, js: js}, nil
}

// Handler is the per-message callback. Returning a nil error Acks the
// message (JetStream advances the consumer cursor); returning a
// non-nil error Naks with a 5s backoff so transient handler failures
// redeliver instead of stranding the envelope.
//
// Implementations SHOULD be idempotent — JetStream guarantees
// at-least-once delivery, so the same Event.ID may arrive twice on a
// broker rebalance.
type Handler func(ctx context.Context, ev *Event) error

// SubscribeOptions tunes Subscribe behaviour. Zero values yield sane
// production defaults (Stream=CATALYST_ORG, AckWait=30s, no MaxDeliver
// cap so a permanently-failing handler does NOT silently drop events
// — operator-visible nak loops are the right failure mode).
type SubscribeOptions struct {
	// Stream is the JetStream Stream the FilterSubject lives on.
	// Defaults to StreamCatalystOrg.
	Stream string
	// AckWait bounds how long JetStream waits for Ack before redeliver.
	// Defaults to 30 seconds.
	AckWait time.Duration
	// NakBackoff is the backoff inserted before redelivery when the
	// Handler returns a non-nil error. Defaults to 5 seconds.
	NakBackoff time.Duration
}

// Subscribe attaches a durable consumer to subject on options.Stream
// (default StreamCatalystOrg) under the supplied durable name, and
// dispatches every envelope to handler.
//
// Subscribe is non-blocking: it returns once the consumer has been
// created AND the underlying Consume loop has been started. The
// loop runs until ctx is cancelled. To stop, cancel ctx — the
// underlying JetStream ConsumeContext is then stopped automatically.
//
// Durable names are stable across pod restarts so JetStream resumes
// from the committed sequence after a controller-manager rollout.
// MaxDeliver=-1 (retry forever) matches the
// core/services/shared/events.MultiSubscriber convention: operator-
// visible nak loops are the right failure mode for unrecoverable
// handler errors, not silent drops.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	subject, durable string,
	handler Handler,
	opts SubscribeOptions,
) error {
	if s == nil || s.js == nil {
		return errors.New("natsbus: subscriber not initialised")
	}
	if subject == "" {
		return errors.New("natsbus: Subscribe requires subject")
	}
	if durable == "" {
		return errors.New("natsbus: Subscribe requires durable name")
	}
	if handler == nil {
		return errors.New("natsbus: Subscribe requires handler")
	}
	stream := opts.Stream
	if stream == "" {
		stream = StreamCatalystOrg
	}
	ackWait := opts.AckWait
	if ackWait <= 0 {
		ackWait = 30 * time.Second
	}
	nakBackoff := opts.NakBackoff
	if nakBackoff <= 0 {
		nakBackoff = 5 * time.Second
	}

	cons, err := s.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		Description:   fmt.Sprintf("controllers natsbus %s on %s", durable, subject),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    -1,
	})
	if err != nil {
		return fmt.Errorf("natsbus: create consumer %s on %s/%s: %w", durable, stream, subject, err)
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		dispatchOne(ctx, msg, handler, subject, durable, nakBackoff)
	})
	if err != nil {
		return fmt.Errorf("natsbus: start consume %s: %w", durable, err)
	}

	slog.Info("natsbus: subscribed",
		"subject", subject, "durable", durable, "stream", stream)

	// Stop the JetStream consume context when ctx is cancelled. We do
	// this in a goroutine so Subscribe returns immediately — the caller
	// (main.go) wires the same ctx to manager.Start so SIGTERM unwinds
	// every Subscribe at once.
	go func() {
		<-ctx.Done()
		cc.Stop()
		slog.Info("natsbus: stopped", "subject", subject, "durable", durable)
	}()
	return nil
}

// dispatchOne parses one JetStream message into an Event, invokes the
// handler with a per-message timeout, and Acks / Naks per the result.
// Malformed JSON is Ack'd-skipped (with an error log) so a poison-pill
// envelope cannot hot-loop the consumer.
func dispatchOne(
	parent context.Context,
	msg jetstream.Msg,
	handler Handler,
	subject, durable string,
	nakBackoff time.Duration,
) {
	var ev Event
	if err := json.Unmarshal(msg.Data(), &ev); err != nil {
		slog.Error("natsbus: malformed envelope — ack to skip",
			"subject", subject, "durable", durable,
			"err", err, "body_size", len(msg.Data()))
		_ = msg.Ack()
		return
	}

	// Per-message timeout — 25s leaves 5s of slack inside the 30s
	// default AckWait so the broker does not redeliver while the handler
	// is still running.
	hctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()

	if err := handler(hctx, &ev); err != nil {
		slog.Warn("natsbus: handler error — nak for retry",
			"subject", subject, "durable", durable,
			"event_id", ev.ID, "event_type", ev.Type, "err", err)
		if nakErr := msg.NakWithDelay(nakBackoff); nakErr != nil {
			slog.Error("natsbus: nak failed",
				"subject", subject, "durable", durable, "err", nakErr)
		}
		return
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("natsbus: ack failed",
			"subject", subject, "durable", durable,
			"event_id", ev.ID, "err", ackErr)
	}
}

// Close drains the underlying NATS connection. Idempotent.
func (s *Subscriber) Close() {
	if s == nil || s.nc == nil {
		return
	}
	_ = s.nc.Drain()
}
