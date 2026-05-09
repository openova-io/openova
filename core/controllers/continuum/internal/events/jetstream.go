// JetStreamPublisher is the production Publisher wired into the
// continuum-controller binary. Implements Publisher via the
// github.com/nats-io/nats.go JetStream client.
//
// Per ADR-0001 §3, NATS JetStream is the canonical audit transport.
// We require a durable acknowledgement on every Publish (no
// fire-and-forget) so a transient broker outage surfaces as an
// emit-side error rather than a silent loss.
//
// Stream config:
//
//	subject:   catalyst.audit
//	retention: limits     (size + age caps; 7d default)
//	storage:   file       (durable across restarts)
//
// Stream creation is OUT OF SCOPE for K-Cont-2: the bp-nats
// blueprint owns the StreamConfig manifest. This publisher only
// PUBLISHES; it neither creates nor configures the stream.
package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamPublisher is the production Publisher.
type JetStreamPublisher struct {
	js jetstream.JetStream

	// nc is retained so Close() can unwind both layers.
	nc *nats.Conn
}

// NewJetStreamPublisher dials NATS at `url` and returns a Publisher
// that emits onto subject `catalyst.audit`. The caller owns calling
// Close on shutdown.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is runtime config —
// the controller's main.go reads NATS_URL env var. Dev clusters use
// `nats://nats.openova-system.svc.cluster.local:4222`.
//
// On a connect failure the function returns the underlying error.
// On a JetStream-context-init failure (e.g. JetStream not enabled)
// we close the underlying connection before returning.
func NewJetStreamPublisher(ctx context.Context, url string, opts ...nats.Option) (*JetStreamPublisher, error) {
	if url == "" {
		return nil, errors.New("events: NATS URL is required")
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("events: nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("events: jetstream init: %w", err)
	}
	return &JetStreamPublisher{js: js, nc: nc}, nil
}

// NewJetStreamPublisherWith wires an already-built JetStream client.
// Used by tests that mock the JetStream interface.
func NewJetStreamPublisherWith(js jetstream.JetStream) *JetStreamPublisher {
	return &JetStreamPublisher{js: js}
}

// Close closes the underlying NATS connection (best-effort).
func (p *JetStreamPublisher) Close() {
	if p.nc != nil {
		p.nc.Close()
	}
}

// Publish implements Publisher. Stamps `audit-type` as a NATS message
// header so subscribers can filter without decoding the body. Awaits
// the JetStream Ack before returning.
func (p *JetStreamPublisher) Publish(ctx context.Context, e Event) error {
	if p == nil || p.js == nil {
		return errors.New("events: JetStreamPublisher not initialised")
	}
	e = FillTimestamp(e, nil)
	if err := e.Validate(); err != nil {
		return err
	}
	body, err := MarshalEvent(e)
	if err != nil {
		return fmt.Errorf("events: marshal: %w", err)
	}
	msg := &nats.Msg{
		Subject: Subject,
		Data:    body,
		Header: nats.Header{
			"audit-type":       []string{e.Type},
			"continuum-name":   []string{e.ContinuumName},
			"application-name": []string{e.ApplicationName},
		},
	}
	if _, err := p.js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("events: publish: %w", err)
	}
	return nil
}

// Compile-time assertion.
var _ Publisher = (*JetStreamPublisher)(nil)
