package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// memQueue hands out its jobs in order and records how each ended. Listen reports LISTEN in
// place, then one notification for each job added later.
type memQueue struct {
	mu     sync.Mutex
	due    []store.Job
	ended  []string
	added  chan string
	claims int
}

func (q *memQueue) Claim(context.Context, time.Duration) (*store.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if len(q.due) == 0 {
		return nil, nil
	}
	j := q.due[0]
	q.due = q.due[1:]
	return &j, nil
}
func (q *memQueue) NextDue(context.Context) (time.Duration, bool, error) { return 0, false, nil }
func (q *memQueue) Finish(_ context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ended = append(q.ended, id+" done")
	return nil
}
func (q *memQueue) Fail(_ context.Context, id, _ string, retry time.Duration, dead bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ended = append(q.ended, fmt.Sprintf("%s retry=%v dead=%v", id, retry, dead))
	return nil
}
func (q *memQueue) Listen(ctx context.Context, fn func(string)) error {
	fn("")
	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-q.added:
			fn(id)
		}
	}
}
func (q *memQueue) add(j store.Job) {
	q.mu.Lock()
	q.due = append(q.due, j)
	q.mu.Unlock()
	q.added <- j.ID
}
func (q *memQueue) endings() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.ended...)
}

func TestWorkerRecordsHowEachJobEnds(t *testing.T) {
	q := &memQueue{added: make(chan string)}
	var handled []string
	w := &Worker{Queue: q, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Handle: func(_ context.Context, j store.Job) error {
		handled = append(handled, j.ID)
		switch j.Kind {
		case "fails":
			return errors.New("the gateway is away")
		case "permanent":
			return fmt.Errorf("%w: no parser", ErrPermanent)
		}
		return nil
	}}
	for _, j := range []store.Job{{ID: "ok", Attempts: 1}, {ID: "first", Kind: "fails", Attempts: 1}, {ID: "third", Kind: "fails", Attempts: 3},
		{ID: "fourth", Kind: "fails", Attempts: 4}, {ID: "perm", Kind: "permanent", Attempts: 1}, {ID: "leased", Attempts: 5}} {
		w.run(context.Background(), j)
	}
	want := []string{"ok done", "first retry=1m0s dead=false", "third retry=25m0s dead=false", "fourth retry=0s dead=true",
		"perm retry=0s dead=true", "leased retry=0s dead=true"}
	if got := q.endings(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if fmt.Sprint(handled) != "[ok first third fourth perm]" {
		t.Fatalf("handled %v: a job whose last lease ran out must not run", handled)
	}
}

func TestWorkerWakesOnNotification(t *testing.T) {
	q := &memQueue{added: make(chan string), due: []store.Job{{ID: "before", Attempts: 1}}}
	ran := make(chan string, 4)
	w := &Worker{Queue: q, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Handle: func(_ context.Context, j store.Job) error { ran <- j.ID; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	for _, want := range []string{"before", "after"} {
		if want == "after" {
			q.add(store.Job{ID: "after", Attempts: 1})
		}
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("ran %s, want %s", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not run", want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
