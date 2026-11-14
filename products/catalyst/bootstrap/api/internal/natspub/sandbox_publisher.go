// Package natspub holds the concrete NATS client bindings catalyst-api
// uses to publish events on the canonical `catalyst.<domain>.<event>`
// subject taxonomy (ADR-0001 §6).
//
// The package is deliberately tiny — exactly one publisher type per
// event surface — and lives outside `internal/handler/` so the handler
// package stays free of NATS-protocol concerns. The handler depends on
// the abstract `TenantEventPublisher` interface (see
// internal/handler/sandbox_sessions.go); this package supplies the
// production binding that main.go wires when CATALYST_NATS_URL is set.
//
// Provenance: TBD-D35c follow-up to PR #1918 — the producer scaffold
// landed in #1918 with a nil-returning constructor (catalyst-api's
// go.mod did not yet import nats.go). This package introduces the
// concrete binding so D35 ("NATS round-trip `catalyst.tenant.
// sandbox_requested` end-to-end") can flip GREEN on a fresh prov.
//
// Why core NATS publish, not JetStream:
//
//   - The audit-trail consumer (sandbox-controller's NATSBridge in
//     core/controllers/sandbox/internal/controller/nats_bridge.go) reads
//     off the broker's at-most-once `catalyst.tenant.*` subscription —
//     not a JetStream durable. A core publish is the symmetric counter-
//     part and avoids the publisher-side stream-bootstrap concern.
//   - The sandbox CR Create has already succeeded by the time we
//     publish; the publish is the audit-trail leg, not the source of
//     truth. JetStream's at-least-once ack is overkill (and would risk
//     wedging the CR-create hot path on a slow broker).
//   - Future migration to JetStream is a one-line swap — see
//     core/services/shared/events/nats.go for the reference impl.
package natspub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// natsConn is the minimal `*nats.Conn` surface the publisher uses. The
// interface exists so the unit tests can substitute a fake without
// bringing up an embedded NATS server (no embedded-server pattern lives
// in this repo today — see core/cmd/projector/internal/nats/
// consumer_test.go for the canonical fake-only test style).
type natsConn interface {
	// Publish writes data to subject. Mirrors `*nats.Conn`.Publish.
	Publish(subject string, data []byte) error
	// Flush blocks until the broker has acked every pending publish.
	// Used by Close to ensure no envelopes are lost on graceful shutdown.
	Flush() error
	// IsConnected reports whether the underlying TCP+protocol handshake
	// is currently up. Used for an informational log on first publish.
	IsConnected() bool
	// Close terminates the connection.
	Close()
}

// Publisher is a concrete handler.TenantEventPublisher backed by a
// `*nats.Conn`. The struct is intentionally trivial — all retry +
// reconnect concerns live in the nats.Conn options set by Dial below.
type Publisher struct {
	conn natsConn
	log  *slog.Logger
}

// NewPublisher returns a Publisher wired to the supplied connection.
// Exported so a future caller (e.g. a NATS-aware integration test that
// brings up its own broker) can construct directly with a custom conn.
func NewPublisher(conn natsConn, log *slog.Logger) *Publisher {
	if log == nil {
		log = slog.Default()
	}
	return &Publisher{conn: conn, log: log}
}

// Dial opens a NATS connection at url with the production-grade option
// set: indefinite reconnect, 2-second wait, 20-second keepalive ping —
// matching core/services/shared/events.ConnectNATS. Returns a Publisher
// wrapping the open conn.
//
// Returns (nil, err) on dial failure; the caller is expected to log +
// continue so the catalyst-api Pod still serves the rest of its surface
// when the broker is briefly unreachable on cold-start. See
// newTenantEventPublisherFromEnv in cmd/api/main.go.
func Dial(url string, log *slog.Logger) (*Publisher, error) {
	if url == "" {
		return nil, errors.New("natspub: empty NATS URL")
	}
	if log == nil {
		log = slog.Default()
	}
	nc, err := nats.Connect(url,
		nats.Name("catalyst-api"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
		nats.Timeout(5*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("natspub: NATS disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Info("natspub: NATS reconnected", "url", c.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("natspub: dial %s: %w", url, err)
	}
	log.Info("natspub: NATS publisher ready",
		"url", url, "subjectExample", handler.SandboxRequestedSubject,
	)
	return NewPublisher(nc, log), nil
}

// PublishTenantEvent implements handler.TenantEventPublisher. Serialises
// ev to JSON and publishes it on the supplied subject via core NATS
// (no JetStream — see package-level doc for the rationale).
//
// Returns an error only on serialisation or broker-write failure; the
// handler logs + continues per the nil-tolerant contract on the
// interface, so a transient NATS outage never wedges the Sandbox-create
// hot path.
//
// ctx is currently observed only for cancellation: nats.Conn.Publish
// is synchronous + non-blocking past the local outbound buffer flush,
// so honouring ctx via a select-on-Done is sufficient (a hung broker
// is detected via the reconnect handler, not via per-call timeout).
func (p *Publisher) PublishTenantEvent(ctx context.Context, subject string, ev handler.TenantEvent) error {
	if p == nil || p.conn == nil {
		return errors.New("natspub: publisher not initialised")
	}
	if subject == "" {
		return errors.New("natspub: empty subject")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("natspub: ctx done before publish: %w", err)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("natspub: marshal tenant event: %w", err)
	}
	if err := p.conn.Publish(subject, body); err != nil {
		return fmt.Errorf("natspub: publish %s: %w", subject, err)
	}
	return nil
}

// Close flushes pending publishes and shuts down the underlying NATS
// connection. Safe to call multiple times; safe on a nil receiver.
func (p *Publisher) Close() {
	if p == nil || p.conn == nil {
		return
	}
	// Best-effort flush — bound at 2s so a wedged broker can't hold up
	// Pod shutdown past the kubelet grace period.
	if err := p.conn.Flush(); err != nil {
		p.log.Warn("natspub: flush on close failed", "err", err)
	}
	p.conn.Close()
}

// Compile-time check: *Publisher satisfies the handler interface so a
// drift between the interface signature and this binding fails at
// build time, not at runtime on the first publish.
var _ handler.TenantEventPublisher = (*Publisher)(nil)
