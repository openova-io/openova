package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder captures the call order across both collaborators. Order is the
// contract drainOnSignal exists to enforce (#5767), so it is what we assert.
type recorder struct {
	mu    sync.Mutex
	calls []string
	// shutdownErr is returned by Shutdown; blockFor delays it so we can
	// prove the join really waits for the drain rather than merely being
	// written after it.
	shutdownErr error
	blockFor    time.Duration
}

func (r *recorder) Shutdown(ctx context.Context) error {
	if r.blockFor > 0 {
		select {
		case <-time.After(r.blockFor):
		case <-ctx.Done():
			r.record("shutdown:ctx-expired")
			return ctx.Err()
		}
	}
	r.record("shutdown")
	return r.shutdownErr
}

func (r *recorder) WaitOrphanReleases() { r.record("join") }

func (r *recorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// TestDrainOnSignal_ShutsDownBeforeJoiningOrphanReleases pins the ordering.
//
// Joining before draining would race: a request landing mid-join spawns an
// orphan-release goroutine the join has already passed, and the process exits
// while that goroutine is still writing to the store — the #489 subdomain lock
// this whole path exists to prevent.
func TestDrainOnSignal_ShutsDownBeforeJoiningOrphanReleases(t *testing.T) {
	rec := &recorder{}
	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainOnSignal(sigCh, rec, rec, time.Second, quietLogger())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainOnSignal did not return after SIGTERM")
	}

	got := rec.snapshot()
	want := []string{"shutdown", "join"}
	if len(got) != len(want) {
		t.Fatalf("call sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call sequence = %v, want %v", got, want)
		}
	}
}

// TestDrainOnSignal_JoinsEvenWhenDrainOverrunsBudget is the case that matters
// operationally: a stuck long-poll must not cost us the orphan-release join.
// An overrunning drain leaves requests cut, which is survivable; a goroutine
// killed mid-persistDeployment leaves a subdomain locked, which is not.
func TestDrainOnSignal_JoinsEvenWhenDrainOverrunsBudget(t *testing.T) {
	rec := &recorder{blockFor: time.Hour} // never finishes inside the budget
	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainOnSignal(sigCh, rec, rec, 50*time.Millisecond, quietLogger())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainOnSignal hung when the drain overran its budget")
	}

	got := rec.snapshot()
	if len(got) != 2 || got[0] != "shutdown:ctx-expired" || got[1] != "join" {
		t.Fatalf("call sequence = %v, want [shutdown:ctx-expired join] — the join must survive a blown drain budget", got)
	}
}

// TestDrainOnSignal_BlocksUntilSignalled proves the drain is signal-driven and
// not fired at startup. Without this, a bug that ran the drain immediately
// would still satisfy the ordering test above.
func TestDrainOnSignal_BlocksUntilSignalled(t *testing.T) {
	rec := &recorder{}
	sigCh := make(chan os.Signal, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainOnSignal(sigCh, rec, rec, time.Second, quietLogger())
	}()

	select {
	case <-done:
		t.Fatal("drainOnSignal returned before any signal was delivered")
	case <-time.After(100 * time.Millisecond):
	}
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("collaborators called before signal: %v", calls)
	}

	sigCh <- syscall.SIGINT
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainOnSignal did not return after SIGINT")
	}
}

// TestDefaultDrainBudget_FitsKubernetesGracePeriod guards the constant against
// drifting past the grace period it is sized for. Overrunning 30s converts a
// clean exit back into the SIGKILL this change removes.
func TestDefaultDrainBudget_FitsKubernetesGracePeriod(t *testing.T) {
	const k8sDefaultGrace = 30 * time.Second
	if defaultDrainBudget >= k8sDefaultGrace {
		t.Fatalf("defaultDrainBudget=%s must stay under the default terminationGracePeriodSeconds (%s), "+
			"leaving headroom for the orphan-release join", defaultDrainBudget, k8sDefaultGrace)
	}
}
