// Package jobs runs the job table's claim loop in the index process.
//
// A job's life: queued, then running, then done; or failed, due again after 1, 5 and 25
// minutes, and dead when it fails once more after its third retry. A job whose handler returns
// an error that wraps ErrPermanent is dead at once. A claim holds a running job for a lease;
// a job whose process ended while it ran is due again when the lease runs out, and that counts
// as an attempt. The claim takes the oldest due job with FOR UPDATE SKIP LOCKED, so two index
// processes never run one job together.
//
// There is no polling loop. The worker runs one claim pass when it starts and whenever LISTEN
// cortex_job delivers a notification (every insert and state change of a job sends one), and
// otherwise sleeps until the next job is due.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// ErrPermanent is wrapped by a handler's error that no retry can mend.
var ErrPermanent = errors.New("jobs: permanent")

// Retries are the delays before the retries of a failed job.
var Retries = []time.Duration{time.Minute, 5 * time.Minute, 25 * time.Minute}

// Lease is how long a claim holds a running job; Reconnect is the delay before LISTEN, or a
// read of the next due job, is tried again after it failed.
const (
	Lease     = 30 * time.Minute
	Reconnect = 5 * time.Second
)

// Queue is the part of the store the worker uses; *store.Postgres is one.
type Queue interface {
	Claim(ctx context.Context, lease time.Duration) (*store.Job, error)
	NextDue(ctx context.Context) (time.Duration, bool, error)
	Finish(ctx context.Context, id string) error
	Fail(ctx context.Context, id, message string, retry time.Duration, dead bool) error
	Listen(ctx context.Context, fn func(jobID string)) error
}

// Worker claims jobs and hands each to Handle.
type Worker struct {
	Queue  Queue
	Handle func(ctx context.Context, j store.Job) error
	Log    *slog.Logger
}

// Run claims and runs jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)
	poke := func(string) {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go func() {
		for ctx.Err() == nil {
			if err := w.Queue.Listen(ctx, poke); err != nil {
				w.Log.Warn("jobs: LISTEN cortex_job failed; it is tried again", "error", err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(Reconnect):
			}
		}
	}()
	for {
		w.pass(ctx)
		// The pass ended on a claim that found nothing, so a job that is due now is held by
		// another claim for a moment: wait a little for it rather than spin.
		var due <-chan time.Time
		if in, ok, err := w.Queue.NextDue(ctx); err != nil {
			w.Log.Warn("jobs: the next due job cannot be read", "error", err.Error())
			due = time.After(Reconnect)
		} else if ok {
			due = time.After(max(in, 50*time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-due:
		}
	}
}

// pass runs due jobs until none is due.
func (w *Worker) pass(ctx context.Context) {
	for ctx.Err() == nil {
		j, err := w.Queue.Claim(ctx, Lease)
		if err != nil {
			w.Log.Warn("jobs: claim failed", "error", err.Error())
			return
		}
		if j == nil {
			return
		}
		w.run(ctx, *j)
	}
}

// run runs one claimed job and records how it ended.
func (w *Worker) run(ctx context.Context, j store.Job) {
	var err error
	if j.Attempts > len(Retries)+1 {
		err = fmt.Errorf("%w: the lease of its last attempt ran out", ErrPermanent)
	} else {
		err = w.Handle(ctx, j)
	}
	log := w.Log.With("job", j.ID, "kind", j.Kind, "attempt", j.Attempts)
	if err == nil {
		if err := w.Queue.Finish(ctx, j.ID); err != nil {
			log.Warn("jobs: done, but not recorded; it runs again when the lease runs out", "error", err.Error())
		}
		return
	}
	dead := errors.Is(err, ErrPermanent) || j.Attempts > len(Retries)
	var retry time.Duration
	if !dead {
		retry = Retries[j.Attempts-1]
	}
	log.Warn("jobs: failed", "error", err.Error(), "dead", dead, "retry", retry.String())
	if err := w.Queue.Fail(context.WithoutCancel(ctx), j.ID, err.Error(), retry, dead); err != nil {
		log.Warn("jobs: the failure was not recorded; it runs again when the lease runs out", "error", err.Error())
	}
}
