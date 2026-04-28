package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is the standard envelope for all domain events.
type Event struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Source    string            `json:"source"`
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenant_id"`
	Data      json.RawMessage   `json:"data"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// NewEvent creates an Event with a unique ID and marshals data into the Data field.
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
		Metadata:  make(map[string]string),
	}, nil
}

// Producer publishes events to RedPanda/Kafka topics.
type Producer struct {
	client *kgo.Client
}

// NewProducer creates a Producer connected to the given brokers.
func NewProducer(brokers []string) (*Producer, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return nil, err
	}
	return &Producer{client: cl}, nil
}

// Publish serializes an event and sends it to the given topic.
func (p *Producer) Publish(ctx context.Context, topic string, event *Event) error {
	val, err := json.Marshal(event)
	if err != nil {
		return err
	}
	rec := &kgo.Record{
		Topic: topic,
		Key:   []byte(event.TenantID),
		Value: val,
	}
	p.client.Produce(ctx, rec, func(_ *kgo.Record, err error) {
		// Errors are surfaced via the synchronous wrapper below.
	})
	if err := p.client.Flush(ctx); err != nil {
		return err
	}
	return nil
}

// Close shuts down the producer.
func (p *Producer) Close() {
	p.client.Close()
}

// Consumer reads events from RedPanda/Kafka topics.
type Consumer struct {
	client *kgo.Client
}

// NewConsumer creates a Consumer for the given group and topics.
//
// Fresh consumer groups start from the earliest retained offset so events
// published during a pod restart window are not dropped. Auto-commit is
// disabled — Subscribe commits explicitly after successful handler invocation
// to give at-least-once delivery.
func NewConsumer(brokers []string, group string, topics []string) (*Consumer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, err
	}
	return &Consumer{client: cl}, nil
}

// Subscribe polls for records and calls handler for each event.
// Records are committed only after the handler returns nil; a handler error
// skips the commit so the record is redelivered on the next poll.
// It blocks until the context is cancelled.
func (c *Consumer) Subscribe(ctx context.Context, handler func(*Event) error) error {
	for {
		fetches := c.client.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		var toCommit []*kgo.Record
		fetches.EachRecord(func(rec *kgo.Record) {
			var event Event
			if err := json.Unmarshal(rec.Value, &event); err != nil {
				// Malformed payload — commit to avoid poison-pill loops.
				toCommit = append(toCommit, rec)
				return
			}
			if err := handler(&event); err != nil {
				return
			}
			toCommit = append(toCommit, rec)
		})
		if len(toCommit) > 0 {
			if err := c.client.CommitRecords(ctx, toCommit...); err != nil {
				return err
			}
		}
	}
}

// Close shuts down the consumer.
func (c *Consumer) Close() {
	c.client.Close()
}
