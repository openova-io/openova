package events

// Convergence fix 2026-05-18 — Sovereign consumer wiring (mirrors PR #1626).
//
// PR #1626 fixed the PUBLISH side: tenant + billing now write
// `tenant.created`, `tenant.deleted`, `tenant.app_install_requested`,
// `tenant.app_uninstall_requested`, `order.placed` to BOTH NATS JetStream
// (subject `catalyst.<event.Type>` per ADR-0001 §6) AND the legacy
// Redpanda topic, via events.MultiPublisher.
//
// The CONSUME side was still Kafka-only — provisioning, notification,
// domain, and billing's tenant-events cascade all called
// events.NewConsumer(redpandaBrokers, …). On Sovereigns REDPANDA_BROKERS
// is empty by design (no Redpanda exists; NATS is the canonical bus),
// so those consumers either never started OR dialed `localhost:9092` in
// a hot crash loop. Either way, every `tenant.created` envelope tenant
// published to NATS landed in /dev/null on the subscriber side — no
// Organization CR ever spawned, no vCluster, no convergence.
//
// This file mirrors bridge.go on the subscribe side. It defines:
//
//   - BrokerSubscriber: the single Subscribe surface every Organization service
//     consumer is migrating to. Matches the existing
//     events.Consumer.Subscribe + events.DLQSubscriber.Subscribe shape
//     so call sites only need a type swap, not a handler rewrite.
//   - MultiSubscriber: fans in from NATS JetStream durable consumers
//     (one per canonical subject) AND, when configured, a legacy Kafka
//     Consumer. The handler receives parsed *Event values from either
//     transport; the caller does not need to know which leg delivered
//     each envelope.
//
// Subject derivation mirrors bridge.go's CanonicalSubject — callers
// supply the legacy Kafka topic + the set of event.Type values they
// react to, and the subscriber maps those to canonical NATS subjects
// (`catalyst.<event.Type>`) at construction time.
//
// At construction time NewMultiSubscriber refuses to return a subscriber
// with both legs nil — that would silently drop every event and is the
// exact failure mode that motivated this file in the first place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// BrokerSubscriber is the unified subscribe surface every Organization service
// consumer (provisioning, notification, domain, billing tenant-events)
// uses. Implementations:
//
//   - MultiSubscriber (this file): fans in from NATS JetStream durable
//     consumers + an optional legacy Kafka Consumer.
//   - *Consumer (events.go): legacy Kafka-only path retained for
//     Catalyst-Zero / unit tests / the contabo-only fallback.
//   - *DLQSubscriber (dlq.go): legacy Kafka-only path with retry + DLQ.
//
// Subscribe blocks until ctx is cancelled OR the underlying broker
// transport returns an unrecoverable error.
type BrokerSubscriber interface {
	// Subscribe registers handler against every NATS subject AND every
	// Kafka topic the implementation was configured for. The handler is
	// invoked once per envelope regardless of which leg delivered it.
	// Returning a non-nil error from the handler signals "do not ack" —
	// the underlying transport will redeliver (NATS) or skip the commit
	// (Kafka) so the same envelope arrives again.
	Subscribe(ctx context.Context, handler func(*Event) error) error
	// Close releases the underlying transport handles.
	Close()
}

// MultiSubscriber fans events in from NATS JetStream + a legacy Kafka
// Consumer. Either side may be nil at construction time:
//
//   - nats == nil + kafka != nil: legacy Catalyst-Zero wiring (matches
//     pre-#1626 behaviour on contabo where REDPANDA_BROKERS is set and
//     NATS_URL is empty).
//   - nats != nil + kafka == nil: canonical Sovereign wiring (NATS-only,
//     REDPANDA_BROKERS intentionally empty).
//   - nats != nil + kafka != nil: dual-mode during the migration window
//     so consumers running against a hybrid environment receive
//     envelopes regardless of which transport the publisher used.
//
// MultiSubscriber NEVER swallows configuration errors — if a caller
// requests a subject but the NATS Stream cannot be created, Subscribe
// returns an error so the operator sees the misconfiguration at pod
// start instead of "every tenant.created event is silently dropped".
type MultiSubscriber struct {
	nats   *NATSConn
	kafka  *Consumer
	group  string
	stream string
	// subjects holds the canonical NATS subjects the subscriber will
	// register durable consumers on. Filled at construction time from
	// the kafka topic list + event types passed by the caller, so the
	// constructor surface mirrors what the existing call sites
	// (events.NewConsumer(brokers, group, topics)) already know.
	subjects []string

	mu     sync.Mutex
	closed bool
	// consumeCtxs tracks the JetStream ConsumeContexts so Close can stop
	// them deterministically. Populated by Subscribe.
	consumeCtxs []jetstream.ConsumeContext
}

// MultiSubscriberConfig captures the construction inputs of a
// MultiSubscriber. Subjects are derived from EventTypes via
// CanonicalSubject; Topics is the legacy Kafka topic list passed
// through to events.NewConsumer when the Kafka leg is enabled.
type MultiSubscriberConfig struct {
	// NATS is the (optional) JetStream connection. Pass nil to disable
	// the NATS leg.
	NATS *NATSConn
	// Kafka is the (optional) legacy Kafka Consumer. Pass nil to
	// disable the Kafka leg. The Consumer's group + topics are already
	// baked in via events.NewConsumer — MultiSubscriber does not
	// re-create the Consumer.
	Kafka *Consumer
	// Group is the durable-consumer name suffix on the NATS side AND
	// the operator-visible group tag in slog. Should match the Kafka
	// consumer group when both legs are wired so observability stays
	// symmetric.
	Group string
	// EventTypes lists the bare event types the subscriber filters
	// for on the NATS side (e.g. "tenant.created", "order.placed").
	// Each is mapped to a canonical subject via CanonicalSubject and
	// becomes its own JetStream durable consumer with a FilterSubject
	// scoped to that subject. Empty disables the NATS leg even if
	// NATS != nil — without subjects there is nothing to consume.
	EventTypes []string
	// StreamName is the JetStream Stream the subjects live on. Defaults
	// to StreamCatalystSME when empty.
	StreamName string
	// StreamSubjects is the Stream's subject filter when MultiSubscriber
	// is the lifecycle owner. Defaults to []string{"catalyst.>"} so a
	// single Stream backs every catalyst.<domain>.<event> subject.
	StreamSubjects []string
}

// StreamCatalystSME is the canonical JetStream Stream backing every
// Organization convergence event (catalyst.tenant.*, catalyst.billing.*,
// catalyst.domain.*, catalyst.provision.*). Created idempotently by
// MultiSubscriber if it does not already exist — first consumer to
// start on a fresh Sovereign owns lifecycle.
const StreamCatalystSME = "CATALYST_SME"

// ensureSMEStream creates an Organization-side JetStream Stream listening on
// the supplied subject filter (defaulting to `catalyst.>`). Idempotent
// — calling it on a Sovereign whose Stream was provisioned in a prior
// startup is a no-op. Lives here (rather than in nats.go) because the
// MultiSubscriber is its sole caller; per-Sovereign overlays may also
// own lifecycle out of band but the first consumer to start on a
// fresh cluster bootstraps the Stream so cold-start is one-shot.
//
// File storage + 14-day retention mirror EnsureUsageStream —
// convergence events (tenant.created, order.placed, …) deserve the
// same replay window as metering envelopes.
func (c *NATSConn) ensureSMEStream(ctx context.Context, name string, subjects []string) error {
	if c == nil || c.js == nil {
		return errors.New("events: nats publisher not initialised")
	}
	if name == "" {
		name = StreamCatalystSME
	}
	if len(subjects) == 0 {
		subjects = []string{"catalyst.>"}
	}
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        name,
		Description: "Catalyst Organization convergence events (catalyst.<domain>.<event>).",
		Subjects:    subjects,
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      14 * 24 * time.Hour,
		Replicas:    1,
	})
	if err != nil {
		return fmt.Errorf("events: ensure Organization stream %s: %w", name, err)
	}
	return nil
}

// NewMultiSubscriber wires a subscriber against the supplied transports.
// Returns an error if both legs are nil-or-empty (no NATS connection +
// no event types AND no Kafka consumer); that combination would silently
// drop every event and is the configuration bug this file exists to
// catch.
//
// NATS-side bootstrap is lazy: the Stream + per-subject durable
// consumers are created on first Subscribe call. Failures there bubble
// up as an error from Subscribe so the caller (typically a service's
// main.go goroutine) logs the actual misconfiguration.
func NewMultiSubscriber(cfg MultiSubscriberConfig) (*MultiSubscriber, error) {
	natsEnabled := cfg.NATS != nil && len(cfg.EventTypes) > 0
	kafkaEnabled := cfg.Kafka != nil
	if !natsEnabled && !kafkaEnabled {
		return nil, errors.New("events: NewMultiSubscriber requires NATS (with EventTypes) or Kafka")
	}
	if cfg.StreamName == "" {
		cfg.StreamName = StreamCatalystSME
	}
	if len(cfg.StreamSubjects) == 0 {
		cfg.StreamSubjects = []string{"catalyst.>"}
	}
	if cfg.Group == "" {
		return nil, errors.New("events: NewMultiSubscriber requires Group")
	}

	subs := make([]string, 0, len(cfg.EventTypes))
	if natsEnabled {
		for _, et := range cfg.EventTypes {
			subj := CanonicalSubject(et)
			if subj == "" {
				continue
			}
			subs = append(subs, subj)
		}
		if len(subs) == 0 {
			natsEnabled = false
		}
	}
	if !natsEnabled && !kafkaEnabled {
		return nil, errors.New("events: NewMultiSubscriber refused — no transport actually subscribable")
	}

	m := &MultiSubscriber{
		group:    cfg.Group,
		stream:   cfg.StreamName,
		subjects: subs,
	}
	if natsEnabled {
		m.nats = cfg.NATS
		// Ensure the Stream exists at construction time so a
		// misconfigured per-Sovereign overlay (wrong Stream name, no
		// JetStream domain enabled) surfaces at pod start instead of
		// the first envelope.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := cfg.NATS.ensureSMEStream(ctx, cfg.StreamName, cfg.StreamSubjects); err != nil {
			return nil, fmt.Errorf("events: ensure Organization stream: %w", err)
		}
	}
	if kafkaEnabled {
		m.kafka = cfg.Kafka
	}
	return m, nil
}

// Subscribe blocks until ctx is cancelled, dispatching every envelope
// arriving on the configured NATS subjects + Kafka topics to handler.
// On NATS-side errors during Stream/Consumer bootstrap, returns the
// underlying error. On Kafka-side polling errors, returns the
// underlying error from the legacy Consumer.
//
// When BOTH transports are configured the two legs run in parallel
// (separate goroutines) and the first one returning an error wins —
// the other is cancelled. This matches the migration-window contract:
// if NATS goes away we still drain Kafka, and vice versa.
func (m *MultiSubscriber) Subscribe(ctx context.Context, handler func(*Event) error) error {
	if m == nil {
		return errors.New("events: nil MultiSubscriber")
	}
	if handler == nil {
		return errors.New("events: nil handler")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("events: MultiSubscriber closed")
	}
	m.mu.Unlock()

	natsEnabled := m.nats != nil && len(m.subjects) > 0
	kafkaEnabled := m.kafka != nil

	if !natsEnabled && !kafkaEnabled {
		return errors.New("events: MultiSubscriber has no transport")
	}

	slog.Info("multi-subscriber starting",
		"group", m.group,
		"nats_subjects", m.subjects,
		"kafka_enabled", kafkaEnabled,
	)

	// Single-leg fast path.
	if natsEnabled && !kafkaEnabled {
		return m.runNATS(ctx, handler)
	}
	if kafkaEnabled && !natsEnabled {
		return m.kafka.Subscribe(ctx, handler)
	}

	// Dual-leg path: run both in parallel, fail on first non-nil error.
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() {
		errCh <- m.runNATS(subCtx, handler)
	}()
	go func() {
		errCh <- m.kafka.Subscribe(subCtx, handler)
	}()

	// First error (including ctx cancellation) cancels the sibling.
	first := <-errCh
	cancel()
	// Drain the second so the goroutine doesn't leak past return.
	<-errCh
	return first
}

// runNATS attaches a durable consumer to each configured subject and
// dispatches messages to handler. Each subject gets its own durable so
// per-subject lag/lag-metrics stay independent — a slow handler on
// `catalyst.tenant.app_install_requested` does not back-pressure
// `catalyst.tenant.created`.
func (m *MultiSubscriber) runNATS(ctx context.Context, handler func(*Event) error) error {
	if m.nats == nil || m.nats.js == nil {
		return errors.New("events: NATS connection not initialised")
	}

	var startedCtxs []jetstream.ConsumeContext
	for _, subj := range m.subjects {
		durable := m.durableName(subj)
		cons, err := m.nats.js.CreateOrUpdateConsumer(ctx, m.stream, jetstream.ConsumerConfig{
			Durable:       durable,
			Description:   fmt.Sprintf("MultiSubscriber %s on %s", m.group, subj),
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       30 * time.Second,
			FilterSubject: subj,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			MaxDeliver:    -1,
		})
		if err != nil {
			// Stop anything we already started so we don't leak.
			for _, cc := range startedCtxs {
				cc.Stop()
			}
			return fmt.Errorf("events: create consumer %s on %s: %w", durable, subj, err)
		}
		cc, err := cons.Consume(func(msg jetstream.Msg) {
			handleNATSMsg(ctx, msg, handler, m.group, subj)
		})
		if err != nil {
			for _, started := range startedCtxs {
				started.Stop()
			}
			return fmt.Errorf("events: start consume %s: %w", durable, err)
		}
		startedCtxs = append(startedCtxs, cc)
		slog.Info("nats subscriber attached",
			"group", m.group, "subject", subj, "durable", durable)
	}

	m.mu.Lock()
	m.consumeCtxs = append(m.consumeCtxs, startedCtxs...)
	m.mu.Unlock()

	<-ctx.Done()
	for _, cc := range startedCtxs {
		cc.Stop()
	}
	return ctx.Err()
}

// handleNATSMsg parses the JetStream message body as an Event envelope
// and dispatches it. Handler errors trigger Nak with a 5-second backoff
// so transient downstream blips don't hot-loop; success acks. Malformed
// payloads are ack'd-skipped (logged at ERROR) so the consumer never
// hot-loops on a poison pill.
func handleNATSMsg(ctx context.Context, msg jetstream.Msg, handler func(*Event) error, group, subject string) {
	var event Event
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		slog.Error("multi-subscriber: malformed NATS envelope — ack to skip",
			"group", group, "subject", subject,
			"error", err, "body_size", len(msg.Data()))
		_ = msg.Ack()
		return
	}

	hctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	_ = hctx

	if err := handler(&event); err != nil {
		slog.Warn("multi-subscriber: handler returned error — nak for retry",
			"group", group, "subject", subject,
			"event_id", event.ID, "event_type", event.Type,
			"error", err)
		if nakErr := msg.NakWithDelay(5 * time.Second); nakErr != nil {
			slog.Error("multi-subscriber: nak failed",
				"group", group, "subject", subject, "error", nakErr)
		}
		return
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("multi-subscriber: ack failed",
			"group", group, "subject", subject,
			"event_id", event.ID, "error", ackErr)
	}
}

// durableName builds the JetStream durable consumer name for a subject.
// JetStream restricts durable names to ASCII letters, digits, dash, and
// underscore; subjects use dots, so we sanitise. Group is the operator-
// supplied tag (e.g. `provisioning`, `notification`); subject is the
// canonical NATS subject (`catalyst.tenant.created`). The result is
// stable across pod restarts so JetStream resumes from the committed
// position automatically.
func (m *MultiSubscriber) durableName(subject string) string {
	clean := strings.ReplaceAll(subject, ".", "-")
	clean = strings.ReplaceAll(clean, ">", "all")
	clean = strings.ReplaceAll(clean, "*", "any")
	return m.group + "-" + clean
}

// Close stops every JetStream consume context AND closes the underlying
// Kafka Consumer. Idempotent.
func (m *MultiSubscriber) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, cc := range m.consumeCtxs {
		cc.Stop()
	}
	m.consumeCtxs = nil
	if m.kafka != nil {
		m.kafka.Close()
	}
}
