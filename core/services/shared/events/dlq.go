package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// DefaultMaxRetries is the in-memory retry budget per event ID before a
// record is shipped to the DLQ. Three matches the brief for #72.
const DefaultMaxRetries = 3

// DLQEnvelope is the payload format written to TopicDLQ. It carries enough
// metadata for an operator to triage and replay without reading the
// original broker offsets directly.
type DLQEnvelope struct {
	// OriginalTopic is where the message was first published.
	OriginalTopic string `json:"original_topic"`
	// OriginalPartition is the kgo.Record.Partition of the poison record.
	OriginalPartition int32 `json:"original_partition"`
	// OriginalOffset is the kgo.Record.Offset of the poison record.
	OriginalOffset int64 `json:"original_offset"`
	// ConsumerGroup identifies the consumer that gave up on this record.
	ConsumerGroup string `json:"consumer_group"`
	// EventID is the parsed Event.ID if the payload was valid JSON, else "".
	EventID string `json:"event_id,omitempty"`
	// EventType is the parsed Event.Type if the payload was valid, else "".
	EventType string `json:"event_type,omitempty"`
	// TenantID is the parsed Event.TenantID if available.
	TenantID string `json:"tenant_id,omitempty"`
	// Error is the handler error that exhausted the retry budget.
	Error string `json:"error"`
	// Attempts is how many handler invocations failed before giving up.
	Attempts int `json:"attempts"`
	// FailedAt is when the DLQ publish was dispatched (UTC).
	FailedAt time.Time `json:"failed_at"`
	// Payload is the exact original record bytes (base64 is unnecessary;
	// json.RawMessage preserves the bytes if they parse, else they are sent
	// wrapped as a string).
	Payload json.RawMessage `json:"payload"`
	// RawPayload is present when Payload could not be unmarshalled as JSON;
	// it carries the original bytes verbatim as a string.
	RawPayload string `json:"raw_payload,omitempty"`
}

// DLQSubscriber wraps a Consumer with retry + dead-letter semantics.
//
// Behaviour per record (compare with the non-DLQ Consumer.Subscribe
// documented in events.go):
//   - Malformed JSON: ship to DLQ and commit (no retry — the payload will
//     never parse).
//   - Handler returns nil: commit.
//   - Handler returns err: bump the in-memory retry counter for the event
//     ID (falling back to topic+partition+offset when the event is
//     unparseable). Retries < MaxRetries are re-delivered on the next poll
//     (counter survives across polls but not across pod restarts — the
//     consumer group offset re-delivers after restart anyway). When the
//     counter reaches MaxRetries, ship to DLQ and commit so the partition
//     is unblocked.
//
// The DLQ publish is best-effort: if it fails, we log at ERROR but still
// commit the record. Leaving the partition blocked on a DLQ broker outage
// would multiply the original incident's blast radius.
type DLQSubscriber struct {
	Consumer    *Consumer
	Producer    *Producer
	Group       string
	MaxRetries  int
	DLQTopic    string
	attempts    map[string]int
	attemptsMux sync.Mutex
}

// NewDLQSubscriber wires a Consumer to a Producer for poison-record
// handling. maxRetries <= 0 falls back to DefaultMaxRetries. dlqTopic ==
// "" falls back to TopicDLQ.
func NewDLQSubscriber(consumer *Consumer, producer *Producer, group string, maxRetries int, dlqTopic string) *DLQSubscriber {
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	if dlqTopic == "" {
		dlqTopic = TopicDLQ
	}
	return &DLQSubscriber{
		Consumer:   consumer,
		Producer:   producer,
		Group:      group,
		MaxRetries: maxRetries,
		DLQTopic:   dlqTopic,
		attempts:   make(map[string]int),
	}
}

// RetryBackoff is the sleep between in-loop handler retries. Kept small
// so transient downstream blips (e.g. momentary tenant-svc 503) clear
// quickly; the outer retry budget is the real safety net.
var RetryBackoff = 500 * time.Millisecond

// Subscribe polls the underlying consumer and invokes handler for each
// record. It never returns until ctx is cancelled (or the broker
// connection fails in a way the consumer cannot recover from).
//
// Retries happen INLINE within the same poll — franz-go advances the
// client cursor on PollFetches, so "skip commit and wait for next poll"
// does not redeliver. Instead we retry up to MaxRetries times with a
// short backoff before either committing (success) or publishing to the
// DLQ and committing (exhausted). The record is always committed after
// the retry loop so the partition never stalls on a poison record.
func (s *DLQSubscriber) Subscribe(ctx context.Context, handler func(*Event) error) error {
	client := s.Consumer.client
	for {
		fetches := client.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		var toCommit []*kgo.Record
		fetches.EachRecord(func(rec *kgo.Record) {
			var event Event
			if err := json.Unmarshal(rec.Value, &event); err != nil {
				// Malformed payload — send to DLQ, commit to unblock partition.
				s.publishDLQ(ctx, rec, nil, err, 1, true)
				toCommit = append(toCommit, rec)
				return
			}

			key := s.attemptKey(rec, &event)
			var lastErr error
			for attempt := 1; attempt <= s.MaxRetries; attempt++ {
				if err := ctx.Err(); err != nil {
					return
				}
				lastErr = handler(&event)
				if lastErr == nil {
					// Success — drop any tracked attempts and commit.
					s.attemptsMux.Lock()
					delete(s.attempts, key)
					s.attemptsMux.Unlock()
					toCommit = append(toCommit, rec)
					return
				}
				s.attemptsMux.Lock()
				s.attempts[key] = attempt
				s.attemptsMux.Unlock()
				slog.Warn("handler failed, will retry",
					"group", s.Group,
					"topic", rec.Topic,
					"event_id", event.ID,
					"event_type", event.Type,
					"tenant_id", event.TenantID,
					"attempt", attempt,
					"max_retries", s.MaxRetries,
					"error", lastErr,
				)
				if attempt < s.MaxRetries {
					select {
					case <-ctx.Done():
						return
					case <-time.After(RetryBackoff):
					}
				}
			}

			// Retry budget exhausted — ship to DLQ and commit.
			s.publishDLQ(ctx, rec, &event, lastErr, s.MaxRetries, false)
			s.attemptsMux.Lock()
			delete(s.attempts, key)
			s.attemptsMux.Unlock()
			slog.Error("handler exhausted retries, record sent to DLQ",
				"group", s.Group,
				"topic", rec.Topic,
				"event_id", event.ID,
				"event_type", event.Type,
				"tenant_id", event.TenantID,
				"attempts", s.MaxRetries,
				"error", lastErr,
			)
			toCommit = append(toCommit, rec)
		})
		if len(toCommit) > 0 {
			if err := client.CommitRecords(ctx, toCommit...); err != nil {
				return err
			}
		}
	}
}

// attemptKey produces a stable retry-counter key. Prefers Event.ID when
// available (cross-partition rebalance safe) and falls back to the broker
// coordinates when the payload never parsed.
func (s *DLQSubscriber) attemptKey(rec *kgo.Record, event *Event) string {
	if event != nil && event.ID != "" {
		return event.ID
	}
	return rec.Topic + ":" + itoa(int64(rec.Partition)) + ":" + itoa(rec.Offset)
}

// publishDLQ writes a DLQEnvelope to s.DLQTopic. Failures are logged but
// never returned — DLQ publish is best-effort per the comment on
// DLQSubscriber. malformed == true means rec.Value was not valid JSON,
// so we send it as RawPayload.
func (s *DLQSubscriber) publishDLQ(ctx context.Context, rec *kgo.Record, event *Event, handlerErr error, attempts int, malformed bool) {
	if s.Producer == nil {
		slog.Error("DLQ publish skipped: no producer configured",
			"topic", rec.Topic, "error", handlerErr)
		return
	}

	env := DLQEnvelope{
		OriginalTopic:     rec.Topic,
		OriginalPartition: rec.Partition,
		OriginalOffset:    rec.Offset,
		ConsumerGroup:     s.Group,
		Error:             handlerErr.Error(),
		Attempts:          attempts,
		FailedAt:          time.Now().UTC(),
	}
	if event != nil {
		env.EventID = event.ID
		env.EventType = event.Type
		env.TenantID = event.TenantID
	}
	if malformed {
		env.RawPayload = string(rec.Value)
	} else {
		env.Payload = json.RawMessage(rec.Value)
	}

	// Reuse the shared Event envelope so operators/DLQ drain tools can
	// decode everything with the same types.
	dlqEvt, err := NewEvent("event.dead_letter", s.Group, env.TenantID, env)
	if err != nil {
		slog.Error("DLQ envelope build failed", "topic", rec.Topic, "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Producer.Publish(pubCtx, s.DLQTopic, dlqEvt); err != nil {
		slog.Error("DLQ publish failed",
			"topic", rec.Topic,
			"dlq_topic", s.DLQTopic,
			"event_id", env.EventID,
			"error", err,
		)
	}
}

// itoa is a dependency-free int64 formatter to keep this file usable from
// any service without pulling strconv into the subscribe hot path (purely
// cosmetic — the compiler already inlines strconv.Itoa).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
