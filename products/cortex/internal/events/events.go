// Package events carries the changes of jobs to the callers of GET /v1/events in the query
// process. A Hub listens on cortex_job (every insert and state change of a job notifies it),
// reads the job, and hands one Event to every subscriber. A subscriber that does not keep up
// loses events and never holds up the hub or another subscriber: an event is a hint, and the
// client reads the current state again. Which subscriber may see an event is decided by the
// subscriber, with its own principals.
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// Event is the data of one frame of the stream.
type Event struct {
	JobID      string    `json:"jobId"`
	SourceID   string    `json:"sourceId"`
	DocumentID string    `json:"documentId"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
}

// Source is where the hub learns of jobs; *store.Postgres is one.
type Source interface {
	Listen(ctx context.Context, fn func(jobID string)) error
	Job(ctx context.Context, id string) (store.Job, error)
}

// Buffer is the number of events a subscriber may hold before the hub drops its events.
const Buffer = 64

// Hub hands events to subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewHub returns a hub without subscribers.
func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

// Subscribe returns the events from now on, and the function that ends the subscription.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, Buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Publish hands e to every subscriber that has room for it, without waiting.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Run listens on src and publishes each job it is told of, until ctx ends. After the listening
// connection fails it waits reconnect and listens again.
func (h *Hub) Run(ctx context.Context, src Source, log *slog.Logger, reconnect time.Duration) {
	for ctx.Err() == nil {
		err := src.Listen(ctx, func(id string) {
			if id == "" {
				return
			}
			j, err := src.Job(ctx, id)
			if err != nil {
				log.Warn("events: a job cannot be read", "job", id, "error", err.Error())
				return
			}
			h.Publish(Event{JobID: j.ID, SourceID: j.SourceID, DocumentID: j.DocumentID, Kind: j.Kind, State: j.State, At: j.UpdatedAt})
		})
		if err != nil {
			log.Warn("events: LISTEN cortex_job failed; it is tried again", "error", err.Error())
		}
		select {
		case <-ctx.Done():
		case <-time.After(reconnect):
		}
	}
}
