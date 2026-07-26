// publisher.go adds the minimal PUBLISH leg to natsbus (#5364).
//
// natsbus was subscriber-only: the Group-C controllers CONSUMED
// catalyst.* envelopes but never emitted any. The organization-controller
// now needs to PUBLISH a `tenant.deleted` envelope on Org-CR finalizer
// teardown so the provisioning service's tenant.deleted consumer runs the
// SECOND half of Org teardown (prune the `org-tenants` gitops dir). See
// SubjectTenantDeleted in subscriber.go for the split-teardown rationale.
//
// The surface deliberately mirrors Subscriber's connect/config style
// (MaxReconnects=-1, 2s ReconnectWait, 20s PingInterval) and stays narrow:
//
//   - NewPublisher(url) → *Publisher, Close() teardown.
//   - Publisher.Publish(ctx, subject, ev) JSON-marshals ev and publishes it
//     to subject on the CATALYST_ORG JetStream stream, waiting for the
//     broker PubAck (bounded by ctx — callers pass a short timeout so a
//     broker stall cannot wedge them).
//   - NewEvent(...) mirrors core/services/shared/events.NewEvent so the JSON
//     wire format is identical (id / type / source / timestamp / tenant_id /
//     data / metadata). It is re-implemented here (rather than imported) for
//     the same reason Event is: to keep the controllers module free of a
//     dependency on core/services/shared/events (which drags in franz-go +
//     Kafka transports the controllers never touch).
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Publisher holds an open NATS+JetStream connection for PUBLISHING domain
// events. Construct via NewPublisher; close via Close. Publish is safe for
// concurrent use (nats.Conn + jetstream.JetStream are goroutine-safe).
type Publisher struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// NewPublisher opens a NATS connection at url and binds a JetStream client
// for publishing. Empty url falls back to nats.DefaultURL so unit tests can
// exercise the package against a local nats-server without env wiring.
// Connection options mirror Connect (the subscriber leg) exactly.
//
// Returns an error if the broker is unreachable; the caller (main.go) is
// expected to log + continue (the emit-on-finalizer is best-effort — a
// missing publisher degrades the Org-delete path to its pre-#5364 behavior,
// it never wedges the controller).
func NewPublisher(url string) (*Publisher, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	nc, err := nats.Connect(url,
		nats.Name("catalyst-controllers-publisher"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
	)
	if err != nil {
		return nil, fmt.Errorf("natsbus: publisher connect %s: %w", url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("natsbus: publisher jetstream init: %w", err)
	}
	return &Publisher{nc: nc, js: js}, nil
}

// Publish JSON-marshals ev and publishes it to subject on the JetStream
// stream that captures it (CATALYST_ORG for catalyst.* subjects), waiting for
// the broker PubAck. The wait is bounded by ctx: callers pass a short timeout
// so a broker stall surfaces as a ctx-deadline error the caller can log +
// swallow rather than blocking indefinitely.
func (p *Publisher) Publish(ctx context.Context, subject string, ev *Event) error {
	if p == nil || p.js == nil {
		return errors.New("natsbus: publisher not initialised")
	}
	if subject == "" {
		return errors.New("natsbus: Publish requires subject")
	}
	if ev == nil {
		return errors.New("natsbus: Publish requires an event")
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("natsbus: marshal event: %w", err)
	}
	if _, err := p.js.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("natsbus: publish %s: %w", subject, err)
	}
	return nil
}

// Close drains the underlying NATS connection. Idempotent.
func (p *Publisher) Close() {
	if p == nil || p.nc == nil {
		return
	}
	_ = p.nc.Drain()
}

// NewEvent creates an Event with a unique ID and marshals data into the Data
// field. It mirrors core/services/shared/events.NewEvent so an envelope this
// controller publishes is byte-compatible with one the tenant-service emits —
// the provisioning consumer unmarshals both identically.
func NewEvent(eventType, source, tenantID string, data any) (*Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &Event{
		ID:        uuid.New().String(),
		Type:      eventType,
		Source:    source,
		Timestamp: time.Now().UTC(),
		TenantID:  tenantID,
		Data:      raw,
		Metadata:  map[string]string{},
	}, nil
}
