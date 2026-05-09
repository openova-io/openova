// Package nats wires the JetStream subscriber that pumps Events from
// the catalyst.events stream into the Valkey projector.
//
// The consumer is durable (named CATALYST_PROJECTOR_<replica>) so a
// projector replica restart resumes from the last-acked sequence
// rather than replaying the whole stream. AckExplicitPolicy: every
// message is explicitly Ack'd ONLY after the projector successfully
// writes to Valkey. Failed writes -> Nack -> JetStream redelivery
// after a backoff (default 5s). MaxDeliver: 5 — after 5 failed
// attempts the message goes to the DLQ subject for operator
// inspection.
//
// The package is split from the projector so tests can drive the
// projector without spinning up a NATS embedded server. Both
// directions are interface-bounded:
//
//	Subscriber.Consume(ctx, fn func(EnvelopedMessage))
//	Acker.{Ack,Nak,InProgress}
//
// Production wiring uses nats-io/nats.go's jetstream API; tests use
// channel-backed fakes.
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	pvalkey "github.com/openova-io/openova/core/cmd/projector/internal/valkey"
)

const (
	// DefaultSubject is the canonical JetStream subject for K8s
	// resource events. Bumped to a constant here so callers
	// (catalyst-api publisher + this consumer) reference one truth.
	DefaultSubject = "catalyst.events.>"

	// DefaultStream is the JetStream Stream name reserved for K8s
	// events per platform/nats-jetstream/chart/templates/streams.yaml.
	DefaultStream = "catalyst.events"

	// DefaultDurable prefix — the consumer is durable so restarts
	// resume from last-acked sequence. Real consumer name appends
	// the replica id (HOSTNAME) so multiple replicas share-load via
	// JetStream's queue-group semantics.
	DefaultDurablePrefix = "catalyst-projector"
)

// Config drives ConsumerFromEnv. All fields are env-driven per
// INVIOLABLE-PRINCIPLES #4.
type Config struct {
	URL          string        // NATS_URL — e.g. "nats://nats-jetstream.nats-jetstream.svc:4222"
	Stream       string        // catalyst.events
	Subject      string        // catalyst.events.>
	DurableName  string        // unique per replica
	AckWait      time.Duration // how long jetstream waits for ack before redeliver
	MaxDeliver   int           // bound on retries before message → DLQ
	BackoffMin   time.Duration // initial nack backoff
	BackoffMax   time.Duration // capped backoff
	ConsumerLogr *slog.Logger
}

// Consumer subscribes to the JetStream and routes messages onto a
// projector. Construct via Connect; Run blocks until ctx is done.
type Consumer struct {
	cfg  Config
	nc   *nats.Conn
	js   jetstream.JetStream
	cons jetstream.Consumer
	log  *slog.Logger
}

// Connect opens a NATS+JetStream connection + binds (or creates) the
// durable consumer. Returns an initialized Consumer ready for Run.
func Connect(ctx context.Context, cfg Config) (*Consumer, error) {
	if cfg.ConsumerLogr == nil {
		cfg.ConsumerLogr = slog.Default()
	}
	if cfg.Stream == "" {
		cfg.Stream = DefaultStream
	}
	if cfg.Subject == "" {
		cfg.Subject = DefaultSubject
	}
	if cfg.DurableName == "" {
		cfg.DurableName = DefaultDurablePrefix
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = 5
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Second
	}

	nc, err := nats.Connect(cfg.URL,
		nats.Name("catalyst-projector"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, cfg.Stream, jetstream.ConsumerConfig{
		Name:          cfg.DurableName,
		Durable:       cfg.DurableName,
		FilterSubject: cfg.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		BackOff:       []time.Duration{cfg.BackoffMin, cfg.BackoffMax},
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	return &Consumer{
		cfg:  cfg,
		nc:   nc,
		js:   js,
		cons: cons,
		log:  cfg.ConsumerLogr,
	}, nil
}

// Run blocks pulling messages off the stream and applying each one to
// the supplied projector. On Apply error the message is Nak'd so
// JetStream redelivers; on success the message is Ack'd.
//
// Returns nil when ctx is cancelled, otherwise the underlying
// transport error.
func (c *Consumer) Run(ctx context.Context, p *pvalkey.Projector) error {
	cc, err := c.cons.Consume(func(msg jetstream.Msg) {
		c.handleOne(ctx, p, msg)
	})
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	defer cc.Stop()

	<-ctx.Done()
	return nil
}

// handleOne is split out for testability. Decodes the message body
// into a projector.Event, calls Apply, and Acks/Nacks based on the
// outcome.
func (c *Consumer) handleOne(ctx context.Context, p *pvalkey.Projector, msg jetstream.Msg) {
	raw := msg.Data()
	var ev pvalkey.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.log.Warn("projector: unmarshal failed; terminating message", "err", err)
		// Terminate (NOT Nak) — replaying a malformed message
		// produces the same error every time.
		_ = msg.Term()
		return
	}
	ev.RawBytes = raw

	key, err := p.Apply(ctx, ev)
	if err != nil {
		c.log.Warn("projector: apply failed; nacking",
			"err", err, "key", key, "type", ev.Type)
		_ = msg.Nak()
		return
	}
	if err := msg.Ack(); err != nil {
		c.log.Warn("projector: ack failed (will redeliver)", "err", err, "key", key)
		return
	}
}

// Close shuts down the underlying NATS connection.
func (c *Consumer) Close() {
	if c.nc != nil {
		c.nc.Drain()
	}
}
