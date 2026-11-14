package events

// Sovereign consumer wiring: the subscribe side of the event bus.
//
// The Organization services publish `tenant.created`, `tenant.deleted`,
// `tenant.app_install_requested`, `tenant.app_uninstall_requested`,
// `order.placed` and their other events to NATS JetStream (subject
// `catalyst.<event.Type>` per ADR-0001 §6) via events.MultiPublisher.
// NATS is the only event bus, so every consumer of these services reads
// from NATS JetStream too.
//
// This file mirrors bridge.go on the subscribe side. It defines:
//
//   - BrokerSubscriber: the single Subscribe surface every Organization service
//     consumer uses.
//   - MultiSubscriber: fans in from NATS JetStream durable consumers
//     (one per canonical subject). The handler receives parsed *Event
//     values.
//
// Subject derivation mirrors bridge.go's CanonicalSubject — callers
// supply the set of event.Type values they react to, and the subscriber
// maps those to canonical NATS subjects (`catalyst.<event.Type>`) at
// construction time.
//
// At construction time NewMultiSubscriber refuses to return a subscriber
// with nothing to consume — that would silently drop every event and is
// the exact failure mode that motivated this file in the first place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// BrokerSubscriber is the unified subscribe surface every Organization service
// consumer (provisioning, notification, domain, billing tenant-events)
// uses. MultiSubscriber (this file) is the one implementation: it fans
// in from NATS JetStream durable consumers.
//
// Subscribe blocks until ctx is cancelled OR the underlying broker
// transport returns an unrecoverable error.
type BrokerSubscriber interface {
	// Subscribe registers handler against every NATS subject the
	// implementation was configured for. The handler is invoked once
	// per envelope. Returning a non-nil error from the handler signals
	// "do not ack" — NATS will redeliver, so the same envelope arrives
	// again.
	Subscribe(ctx context.Context, handler func(*Event) error) error
	// Close releases the underlying transport handles.
	Close()
}

// MultiSubscriber fans events in from NATS JetStream.
//
// MultiSubscriber NEVER swallows configuration errors — if a caller
// requests a subject but the NATS Stream does not exist, NewMultiSubscriber
// returns an error so the operator sees the misconfiguration at pod
// start instead of "every tenant.created event is silently dropped".
type MultiSubscriber struct {
	nats   *NATSConn
	group  string
	stream string
	// subjects holds the canonical NATS subjects the subscriber will
	// register durable consumers on. Filled at construction time from
	// the event types passed by the caller.
	subjects []string

	// maxDeliver bounds JetStream redelivery of a persistently-failing
	// envelope (#6464 residual). Resolved once at construction from
	// EVENTS_NATS_MAX_DELIVER (default defaultNATSMaxDeliver). MUST be a
	// positive, finite integer — a value of -1 (JetStream's "infinite")
	// is exactly the unbounded hot loop that held the noreply@ 25/hour
	// send budget at zero and killed PIN sign-in fleet-wide; see
	// resolveMaxDeliver + runNATS.
	maxDeliver int

	mu     sync.Mutex
	closed bool
	// consumeCtxs tracks the JetStream ConsumeContexts so Close can stop
	// them deterministically. Populated by Subscribe.
	consumeCtxs []jetstream.ConsumeContext
}

// MultiSubscriberConfig captures the construction inputs of a
// MultiSubscriber. Subjects are derived from EventTypes via
// CanonicalSubject.
type MultiSubscriberConfig struct {
	// NATS is the JetStream connection. NewMultiSubscriber returns an
	// error when it is nil.
	NATS *NATSConn
	// Group is the durable-consumer name suffix on the NATS side AND
	// the operator-visible group tag in slog.
	Group string
	// EventTypes lists the bare event types the subscriber filters
	// for on the NATS side (e.g. "tenant.created", "order.placed").
	// Each is mapped to a canonical subject via CanonicalSubject and
	// becomes its own JetStream durable consumer with a FilterSubject
	// scoped to that subject. NewMultiSubscriber returns an error when
	// it is empty — without subjects there is nothing to consume.
	EventTypes []string
	// StreamName is the JetStream Stream the subjects live on. Defaults
	// to StreamCatalystOrg when empty.
	StreamName string
	// StreamSubjects are the subject filters the bound Stream must carry.
	// Defaults to []string{"catalyst.>"}: a single Stream backs every
	// catalyst.<domain>.<event> subject.
	StreamSubjects []string
}

// StreamCatalystOrg is the canonical JetStream Stream backing every
// Organization convergence event (catalyst.tenant.*, catalyst.billing.*,
// catalyst.domain.*, catalyst.provision.*). The chart bp-nats-jetstream
// declares it (platform/nats-jetstream/chart/templates/streams.yaml) and
// the JetStream controller of that chart creates it. No Go code creates
// or changes it: a service binds to it at start and fails while it is
// absent.
const StreamCatalystOrg = "CATALYST_ORG"

// ensureOrgStream binds to the declared JetStream Stream name (default
// StreamCatalystOrg) and checks that it carries every subject filter in
// subjects (default `catalyst.>`). It never creates or changes a Stream.
// It returns an error when the Stream is absent (wrapping
// jetstream.ErrStreamNotFound) or lacks one of the subjects, so a service
// that starts before the Stream exists fails at start and is restarted,
// instead of creating a second, undeclared configuration.
func (c *NATSConn) ensureOrgStream(ctx context.Context, name string, subjects []string) error {
	if c == nil || c.js == nil {
		return errors.New("events: nats publisher not initialised")
	}
	if name == "" {
		name = StreamCatalystOrg
	}
	if len(subjects) == 0 {
		subjects = []string{"catalyst.>"}
	}
	stream, err := c.js.Stream(ctx, name)
	if err != nil {
		return fmt.Errorf("events: bind Organization stream %s (declared by the chart bp-nats-jetstream): %w", name, err)
	}
	var have []string
	if info := stream.CachedInfo(); info != nil {
		have = info.Config.Subjects
	}
	for _, want := range subjects {
		if !slices.Contains(have, want) {
			return fmt.Errorf("events: stream %s does not carry subject %s (it carries %v)", name, want, have)
		}
	}
	return nil
}

// NewMultiSubscriber wires a subscriber against the supplied NATS
// connection. Returns an error if there is nothing to consume (no NATS
// connection or no event types); that combination would silently drop
// every event and is the configuration bug this file exists to catch.
//
// The Stream is bound at construction time; the per-subject durable
// consumers are created on the first Subscribe call. Failures there bubble
// up as an error from Subscribe so the caller (typically a service's
// main.go goroutine) logs the actual misconfiguration.
func NewMultiSubscriber(cfg MultiSubscriberConfig) (*MultiSubscriber, error) {
	if cfg.NATS == nil || len(cfg.EventTypes) == 0 {
		return nil, errors.New("events: NewMultiSubscriber requires NATS (with EventTypes)")
	}
	if cfg.StreamName == "" {
		cfg.StreamName = StreamCatalystOrg
	}
	if len(cfg.StreamSubjects) == 0 {
		cfg.StreamSubjects = []string{"catalyst.>"}
	}
	if cfg.Group == "" {
		return nil, errors.New("events: NewMultiSubscriber requires Group")
	}

	subs := make([]string, 0, len(cfg.EventTypes))
	for _, et := range cfg.EventTypes {
		subj := CanonicalSubject(et)
		if subj == "" {
			continue
		}
		subs = append(subs, subj)
	}
	if len(subs) == 0 {
		return nil, errors.New("events: NewMultiSubscriber refused — no transport actually subscribable")
	}

	m := &MultiSubscriber{
		nats:       cfg.NATS,
		group:      cfg.Group,
		stream:     cfg.StreamName,
		subjects:   subs,
		maxDeliver: resolveMaxDeliver(),
	}
	// Bind to the declared Stream at construction time so a missing
	// Stream or a misconfigured per-Sovereign overlay (wrong Stream
	// name, no JetStream domain enabled) surfaces at pod start instead
	// of the first envelope.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cfg.NATS.ensureOrgStream(ctx, cfg.StreamName, cfg.StreamSubjects); err != nil {
		return nil, fmt.Errorf("events: ensure Organization stream: %w", err)
	}
	return m, nil
}

// Subscribe blocks until ctx is cancelled, dispatching every envelope
// arriving on the configured NATS subjects to handler. On NATS-side
// errors during Stream/Consumer bootstrap, returns the underlying error.
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

	if m.nats == nil || len(m.subjects) == 0 {
		return errors.New("events: MultiSubscriber has no transport")
	}

	slog.Info("multi-subscriber starting",
		"group", m.group,
		"nats_subjects", m.subjects,
	)

	return m.runNATS(ctx, handler)
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
			// #6464 residual — MaxDeliver was -1 (infinite). #6469 slowed the
			// nak cadence (constant 5s -> exponential, capped 5m) but a
			// message that can NEVER succeed still redelivered forever: an
			// "app is ready" notification whose recipient quota is spent naks,
			// backs off to the 5m cap, and retries every 5m for the life of
			// the stream. With MaxDeliver:-1 the drain never stops, and the
			// 5m spacing keeps the mailer's circuit breaker (3 refusals / 90s)
			// from ever tripping, so every redelivery reaches the wire and the
			// noreply@ 25/hour budget stays pinned at zero — PIN sign-in dead
			// fleet-wide. Bounding MaxDeliver caps the total budget a single
			// stuck envelope can burn: after m.maxDeliver attempts JetStream
			// stops redelivering (MAX_DELIVERIES advisory), the drain becomes
			// finite, and the send budget self-heals within the rolling hour.
			MaxDeliver: m.maxDeliver,
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
		// #6464 — a FIXED 5s nak is what turns one refusal into a storm.
		//
		// MEASURED on the mothership 2026-08-18: Stalwart logged
		// smtp.rate-limit-exceeded (id="sender", limit=[25, 3600000ms]) for
		// noreply@openova.io continuously, with send attempts landing every
		// 2-3 seconds. A 25/hour budget is consumed in about a minute and then
		// stays exhausted, so PIN sign-in was dead FLEET-WIDE — no customer
		// could obtain a code on any Sovereign.
		//
		// Mechanism: every mail handler here returns the send error, this nak
		// re-queues at a CONSTANT 5s, and the stream's MaxDeliver is -1
		// (infinite). A message that can never succeed — an "app is ready"
		// notification whose recipient quota is spent — therefore retries
		// forever at a fixed rate, and a handful of them in flight aggregate
		// into the 2-3s cadence observed. The retry rate never decays no
		// matter how long the downstream stays refusing.
		//
		// Exponential backoff off NumDelivered lets a genuinely transient blip
		// still recover fast (first retry still ~5s) while a sustained refusal
		// decays to minutes instead of hammering. Capped so redelivery stays
		// well inside any operator's patience for a real transient.
		delay := nakBackoff(msg)
		slog.Warn("multi-subscriber: handler returned error — nak for retry",
			"group", group, "subject", subject,
			"event_id", event.ID, "event_type", event.Type,
			"nak_delay", delay.String(),
			"error", err)
		if nakErr := msg.NakWithDelay(delay); nakErr != nil {
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

// Close stops every JetStream consume context. Idempotent.
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
}

// maxDeliverEnv overrides the bounded JetStream redelivery ceiling per
// docs/PRINCIPLES.md Inviolable Principle #4 (every knob runtime-
// configurable at the Deployment level, no rebuild). A positive integer
// wins; anything else (unset, empty, non-numeric, or <= 0 — which
// includes JetStream's "-1 = infinite", the very value this fix exists
// to forbid by default) falls back to defaultNATSMaxDeliver.
const maxDeliverEnv = "EVENTS_NATS_MAX_DELIVER"

// defaultNATSMaxDeliver is the bounded redelivery ceiling for a
// persistently-failing envelope (#6464 residual). Paired with nakBackoff
// (5s,10s,20s,40s,80s,160s,300s,300s,...) 10 attempts span ~20 minutes of
// retries before JetStream stops redelivering — ample headroom for a real
// transient (a Stalwart restart is 1-2 minutes) while guaranteeing a
// structurally-doomed notification can burn at most 10 sends, not an
// unbounded stream, of the shared noreply@ budget.
const defaultNATSMaxDeliver = 10

// resolveMaxDeliver reads the bounded redelivery ceiling from
// maxDeliverEnv, defaulting to defaultNATSMaxDeliver. It NEVER returns a
// non-positive value: -1 (infinite) is exactly the unbounded hot loop
// that killed PIN sign-in fleet-wide (#6464), so an operator cannot
// silently reinstate it through a stray env value — an explicit infinite
// retry is not a supported configuration.
func resolveMaxDeliver() int {
	raw := strings.TrimSpace(os.Getenv(maxDeliverEnv))
	if raw == "" {
		return defaultNATSMaxDeliver
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultNATSMaxDeliver
	}
	return n
}

// nakBackoff returns the redelivery delay for a failed handler, growing
// exponentially with JetStream's own delivery counter (#6464).
//
//	attempt 1 -> 5s    2 -> 10s   3 -> 20s   4 -> 40s   5 -> 80s ... capped
//
// WHY THIS IS NOT A CONSTANT. A fixed delay means the retry rate never
// decays, so a downstream that is refusing for a structural reason — a
// spent per-sender mail quota, an expired credential — is hammered at the
// same cadence forever. Combined with MaxDeliver:-1 that is an unbounded
// hot loop with a 5-second period, which is exactly what held the
// noreply@ 25/hour budget at zero and killed PIN sign-in fleet-wide.
//
// The first retry is unchanged at 5s, so a genuine transient blip still
// recovers as quickly as before; only sustained failure decays.
//
// If the metadata cannot be read we fall back to the base delay rather
// than to zero. An unreadable counter must not silently reinstate the
// constant-rate behaviour this function exists to remove — and it must
// never produce a TIGHTER loop than the old code.
func nakBackoff(msg jetstream.Msg) time.Duration {
	const (
		base = 5 * time.Second
		max  = 5 * time.Minute
	)
	md, err := msg.Metadata()
	if err != nil || md == nil || md.NumDelivered < 1 {
		return base
	}
	d := base
	for i := uint64(1); i < md.NumDelivered; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}
