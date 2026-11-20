package cutoverrecord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// runWithin runs w.Run and fails the test when it does not return within limit.
func runWithin(ctx context.Context, t *testing.T, w Wait, try func() (int, error), limit time.Duration) (int, time.Duration, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	start := time.Now()
	done := make(chan result, 1)
	go func() {
		n, err := w.Run(ctx, try)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		return r.n, time.Since(start), r.err
	case <-time.After(limit):
		t.Fatalf("Wait.Run did not return within %s", limit)
		return 0, 0, nil
	}
}

func notArrived() error { return Shortfall{Expected: 1, Dir: "/k", SecretRef: "ns/s"}.Err() }

// The record arrives while the pre-step waits: the next try runs on the
// import route's signal, long before the bound.
func TestWait_RecordArrives_TryRunsAgainOnTheSignal(t *testing.T) {
	var sig Signal
	calls, waited := 0, 0
	w := Wait{ImportBudget: time.Hour, Arrived: sig.C(), Waiting: func(string) {
		waited++
		sig.Announce() // the import lands while the pre-step waits
	}}
	try := func() (int, error) {
		calls++
		if calls == 1 {
			return 0, notArrived()
		}
		return 1, nil
	}
	n, _, err := runWithin(context.Background(), t, w, try, 10*time.Second)
	if err != nil || n != 1 || calls != 2 || waited != 1 {
		t.Fatalf("n=%d err=%v calls=%d waited=%d, want 1 <nil> 2 1", n, err, calls, waited)
	}
}

// The record never arrives: Run fails once the bound has passed, naming the
// import, and never earlier.
func TestWait_RecordNeverArrives_FailsAtTheBoundNamingTheImport(t *testing.T) {
	const bound = 300 * time.Millisecond
	var sig Signal
	w := Wait{ImportBudget: bound, Arrived: sig.C()}
	_, elapsed, err := runWithin(context.Background(), t, w, func() (int, error) { return 0, notArrived() }, 10*time.Second)
	if !errors.Is(err, ErrNotArrived) {
		t.Fatalf("err=%v, want ErrNotArrived", err)
	}
	if elapsed < bound {
		t.Fatalf("failed after %s, before the bound of %s", elapsed, bound)
	}
	for _, want := range []string{"POST /api/v1/internal/deployments/import", "waited until", "has not landed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %q: %v", want, err)
		}
	}
}

// A later attempt counts from the cutover's first start: past the bound it
// fails at once and does not announce a wait.
func TestWait_LaterAttemptPastTheBound_FailsAtOnce(t *testing.T) {
	var sig Signal
	waited := false
	w := Wait{StartedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339), ImportBudget: time.Hour,
		Arrived: sig.C(), Waiting: func(string) { waited = true }}
	_, _, err := runWithin(context.Background(), t, w, func() (int, error) { return 0, notArrived() }, 10*time.Second)
	if !errors.Is(err, ErrNotArrived) || !strings.Contains(err.Error(), "waited until") || waited {
		t.Fatalf("err=%v waited=%v, want the bound failure at once and no wait", err, waited)
	}
}

// Any other shortfall, including a record that arrived with an empty id, is
// returned at once: it is the failed step it always was.
func TestWait_OtherFailureReturnsAtOnce(t *testing.T) {
	var sig Signal
	calls, waited := 0, false
	w := Wait{ImportBudget: time.Hour, Arrived: sig.C(), Waiting: func(string) { waited = true }}
	degraded := Shortfall{Arrived: true, Expected: 1, Dir: "/k", SecretRef: "ns/s"}.Err()
	_, _, err := runWithin(context.Background(), t, w, func() (int, error) { calls++; return 0, degraded }, 10*time.Second)
	if err != degraded || calls != 1 || waited {
		t.Fatalf("err=%v calls=%d waited=%v, want the degraded failure after one try and no wait", err, calls, waited)
	}
	if errors.Is(degraded, ErrNotArrived) || !strings.Contains(degraded.Error(), "EMPTY deployment id") {
		t.Fatalf("an arrived record with an empty id must keep the #5488 failure: %v", degraded)
	}
}

// The wait ends with its context.
func TestWait_ContextEndsTheWait(t *testing.T) {
	var sig Signal
	ctx, cancel := context.WithCancel(context.Background())
	w := Wait{ImportBudget: time.Hour, Arrived: sig.C(), Waiting: func(string) { cancel() }}
	_, _, err := runWithin(ctx, t, w, func() (int, error) { return 0, notArrived() }, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestDeadline_CountsFromTheFirstStart(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 21, 53, 0, time.UTC)
	if got := Deadline("", now, 20*time.Minute); !got.Equal(now.Add(20 * time.Minute)) {
		t.Fatalf("first run: %s, want now + budget", got)
	}
	if got := Deadline("2026-11-20T15:00:00Z", now, 20*time.Minute); !got.Equal(time.Date(2026, 11, 20, 15, 20, 0, 0, time.UTC)) {
		t.Fatalf("later run: %s, want cutoverStartedAt + budget", got)
	}
}

func TestShortfall_NotArrivedNamesTheImportAndKeepsTheRefusal(t *testing.T) {
	err := Shortfall{Expected: 1, Dir: "/k", SecretRef: "ns/s", Ambiguous: []string{"a", "b"}, Refusal: "refused: foreign"}.Err()
	for _, want := range []string{"POST /api/v1/internal/deployments/import", "2 deployment prefixes", "#5359", "refused: foreign"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %q: %v", want, err)
		}
	}
	if !errors.Is(err, ErrNotArrived) {
		t.Fatalf("err=%v, want ErrNotArrived", err)
	}
}
