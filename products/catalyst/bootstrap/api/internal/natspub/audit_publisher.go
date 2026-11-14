// Package natspub — audit_publisher.go: the audit.Publisher of catalyst-api.
//
// Every audit event is published as a CloudEvent (contract C3,
// docs/contracts/C3-event-subjects.yaml) on the subject
// catalyst.audit.recorded.v1 of the JetStream stream CATALYST_ORG. The
// envelope's id is the event's id, and it is sent as the Nats-Msg-Id header,
// so the stream keeps one copy of an event that is published twice. Publish
// returns only after the stream acknowledged the message.
//
// Replay reads the newest events back from the stream. NewAuditBus, which
// main.go calls once at start, loads the result into the audit Bus, so the
// listing routes show after a restart what they showed before it.
//
// This file creates no stream: the chart of bp-nats-jetstream declares
// CATALYST_ORG. While the stream is absent, Publish and Replay return an
// error.
package natspub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
)

const (
	// AuditSubject is the NATS subject of every audit event.
	AuditSubject = "catalyst.audit.recorded.v1"

	// auditPublishTimeout bounds the wait for the stream's acknowledgement
	// of one event.
	auditPublishTimeout = 5 * time.Second

	// auditReplayBatch is how many messages Replay asks for in one request.
	auditReplayBatch = 256

	// AuditReplayLimit is how many of the newest audit events NewAuditBus
	// reads back from the stream.
	AuditReplayLimit = 1000

	// auditReplayTimeout bounds that read.
	auditReplayTimeout = 30 * time.Second
)

// AuditPublisher implements audit.Publisher on a JetStream stream. Construct
// it with NewAuditPublisher. Publish is safe for concurrent use.
type AuditPublisher struct {
	url    string
	source string
	pub    *natsbus.Publisher
	log    *slog.Logger
}

// Compile-time assertion that the type satisfies the audit interface.
var _ audit.Publisher = (*AuditPublisher)(nil)

// NewAuditPublisher connects to NATS at url. source is the `source`
// attribute of every envelope it publishes: catalyst-api/<sovereign
// FQDN>/<region id>. It returns an error when url or source is empty or the
// server cannot be reached; the caller then runs without a publisher.
func NewAuditPublisher(url, source string, log *slog.Logger) (*AuditPublisher, error) {
	if url == "" {
		return nil, errors.New("natspub: CATALYST_NATS_URL is empty")
	}
	if source == "" {
		return nil, errors.New("natspub: the audit publisher needs a source")
	}
	if log == nil {
		log = slog.Default()
	}
	pub, err := natsbus.NewPublisher(url)
	if err != nil {
		return nil, fmt.Errorf("natspub: audit publisher: %w", err)
	}
	return &AuditPublisher{
		url:    url,
		source: source,
		pub:    pub,
		log:    log.With("component", "audit-publisher", "subject", AuditSubject),
	}, nil
}

// Publish wraps ev in a CloudEvent whose id is ev.ID and whose time is
// ev.Timestamp, publishes it on AuditSubject and waits for the stream's
// acknowledgement, at most auditPublishTimeout. The wait does not end when
// the caller's request is cancelled: an audit event of a change that has
// happened must still be recorded.
func (p *AuditPublisher) Publish(ctx context.Context, ev audit.Event) error {
	if p == nil || p.pub == nil {
		return errors.New("natspub: audit publisher not initialised")
	}
	if ev.ID == "" {
		return errors.New("natspub: an audit event needs an id")
	}
	ce, err := natsbus.NewCloudEvent(AuditSubject, p.source, "", ev)
	if err != nil {
		return err
	}
	ce.ID = ev.ID
	if !ev.Timestamp.IsZero() {
		ce.Time = ev.Timestamp.UTC()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditPublishTimeout)
	defer cancel()
	return p.pub.SendCloudEvent(ctx, ce)
}

// Replay returns the newest events of AuditSubject that the stream holds, at
// most limit of them, oldest first. It asks the stream how many messages the
// subject holds, reads them from the first one with an ordered consumer,
// which the server removes by itself, and keeps the last limit of them; the
// stream's own age limit bounds the read.
// A message that is not a CloudEvent with an audit event as data is skipped
// and counted in the log. Replay opens a connection of its own and closes it.
func (p *AuditPublisher) Replay(ctx context.Context, limit int) ([]audit.Event, error) {
	if p == nil || p.url == "" {
		return nil, errors.New("natspub: audit publisher not initialised")
	}
	if limit <= 0 {
		return nil, nil
	}
	nc, err := nats.Connect(p.url, nats.Name("catalyst-api/audit-replay"), nats.Timeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("natspub: audit replay: connect: %w", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("natspub: audit replay: jetstream: %w", err)
	}
	stream, err := js.Stream(ctx, natsbus.StreamCatalystOrg)
	if err != nil {
		return nil, fmt.Errorf("natspub: audit replay: stream %s: %w", natsbus.StreamCatalystOrg, err)
	}
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(AuditSubject))
	if err != nil {
		return nil, fmt.Errorf("natspub: audit replay: stream info: %w", err)
	}
	left := info.State.Subjects[AuditSubject] // messages of the subject still to read
	if left == 0 {
		return nil, nil
	}
	var skip uint64 // the messages before the newest limit
	if newest := uint64(limit); left > newest {
		skip = left - newest
	}
	cons, err := js.OrderedConsumer(ctx, natsbus.StreamCatalystOrg, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{AuditSubject},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("natspub: audit replay: consumer: %w", err)
	}
	tail := make([][]byte, 0, min(limit, auditReplayBatch))
	for left > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("natspub: audit replay: %w", err)
		}
		size := auditReplayBatch
		if left < auditReplayBatch {
			size = int(left)
		}
		batch, err := cons.Fetch(size, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			return nil, fmt.Errorf("natspub: audit replay: fetch: %w", err)
		}
		var got uint64
		for msg := range batch.Messages() {
			if got++; skip > 0 {
				skip--
				continue
			}
			tail = append(tail, msg.Data())
		}
		if err := batch.Error(); err != nil {
			return nil, fmt.Errorf("natspub: audit replay: fetch: %w", err)
		}
		if got == 0 || got > left {
			return nil, fmt.Errorf("natspub: audit replay: the stream sent %d messages, %d still expected", got, left)
		}
		left -= got
	}
	events, skipped := make([]audit.Event, 0, len(tail)), 0
	for _, body := range tail {
		ce, err := natsbus.DecodeCloudEvent(body)
		var ev audit.Event
		if err == nil {
			err = json.Unmarshal(ce.Data, &ev)
		}
		if err != nil || ev.AuditType == "" {
			skipped++
			continue
		}
		if ev.ID == "" {
			ev.ID = ce.ID
		}
		events = append(events, ev)
	}
	if skipped > 0 {
		p.log.Warn("audit replay: messages that are not audit events were skipped", "skipped", skipped)
	}
	return events, nil
}

// NewAuditBus builds the audit Bus of catalyst-api. ringCapacity is the size
// of the ring buffer (0: the default of package audit); log receives the log
// line of every event.
//
// With a publisher every event goes to the stream, and the ring is filled
// with the newest AuditReplayLimit events that the stream holds, so the
// listing routes show after a restart what they showed before it. A replay
// that fails is logged and leaves the ring empty; events published afterwards
// are still recorded. With a nil publisher the Bus runs in process only.
func NewAuditBus(pub *AuditPublisher, ringCapacity int, log *slog.Logger) *audit.Bus {
	if log == nil {
		log = slog.Default()
	}
	cfg := audit.BusConfig{RingCapacity: ringCapacity, Log: log}
	if pub != nil {
		cfg.Publisher = pub // never a nil pointer inside the interface
	}
	bus := audit.NewBus(cfg)
	replayed := 0
	if pub != nil {
		ctx, cancel := context.WithTimeout(context.Background(), auditReplayTimeout)
		defer cancel()
		events, err := pub.Replay(ctx, AuditReplayLimit)
		if err != nil {
			log.Error("audit: replay from the stream failed; the listing starts empty", "err", err)
		}
		replayed = bus.Load(events)
	}
	log.Info("audit: bus wired", "ringCapacity", ringCapacity, "transport", bus.Transport(), "replayed", replayed)
	return bus
}

// Close drains the publisher's connection. Idempotent.
func (p *AuditPublisher) Close() {
	if p == nil || p.pub == nil {
		return
	}
	p.pub.Close()
}
