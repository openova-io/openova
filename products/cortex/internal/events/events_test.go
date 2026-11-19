package events

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// source fails its first LISTEN, then reports two jobs and an unknown one.
type source struct{ listens atomic.Int32 }

func (s *source) Listen(ctx context.Context, fn func(string)) error {
	if s.listens.Add(1) == 1 {
		return errors.New("the connection was reset")
	}
	for _, id := range []string{"", "j1", "nope", "j2"} {
		fn(id)
	}
	<-ctx.Done()
	return nil
}

func (s *source) Job(_ context.Context, id string) (store.Job, error) {
	if id == "nope" {
		return store.Job{}, store.ErrNotFound
	}
	return store.Job{ID: id, Kind: "index", SourceID: "hr", DocumentID: "d-" + id, State: "queued", UpdatedAt: time.Unix(1, 0)}, nil
}

func TestHubPublishesEveryJobToEverySubscriber(t *testing.T) {
	h := NewHub()
	a, stopA := h.Subscribe()
	defer stopA()
	b, stopB := h.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx, &source{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 10*time.Millisecond)
	for _, ch := range []<-chan Event{a, b} {
		for _, want := range []string{"j1", "j2"} {
			select {
			case e := <-ch:
				if e != (Event{JobID: want, SourceID: "hr", DocumentID: "d-" + want, Kind: "index", State: "queued", At: time.Unix(1, 0)}) {
					t.Fatalf("got %+v, want job %s", e, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no event %s", want)
			}
		}
	}
	stopB()
	h.Publish(Event{JobID: "j3"})
	if e := <-a; e.JobID != "j3" {
		t.Fatalf("got %+v after the other subscription ended", e)
	}
}

func TestASlowSubscriberHoldsNothingUp(t *testing.T) {
	h := NewHub()
	slow, stopSlow := h.Subscribe()
	defer stopSlow()
	fast, stopFast := h.Subscribe()
	defer stopFast()
	done := make(chan struct{})
	go func() {
		for i := 0; i < Buffer*3; i++ {
			h.Publish(Event{JobID: "j"})
			<-fast
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish waited for a subscriber that does not read")
	}
	if n := len(slow); n != Buffer {
		t.Fatalf("the slow subscriber holds %d events, want the %d that fit", n, Buffer)
	}
}
