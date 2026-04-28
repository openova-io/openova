package handlers

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDay2CancelRegistry_ZeroValueUsable proves the zero value works without
// an explicit constructor — Handler embeds it by value and must not panic on
// first Register.
func TestDay2CancelRegistry_ZeroValueUsable(t *testing.T) {
	var r day2CancelRegistry
	ctx, cancel := r.Register(context.Background(), "tenant-a", "job-1")
	t.Cleanup(cancel)
	if ctx == nil {
		t.Fatal("Register returned nil context")
	}
	if ctx.Err() != nil {
		t.Fatalf("fresh ctx should not be canceled, got %v", ctx.Err())
	}
	r.Unregister("tenant-a", "job-1")
}

// TestDay2CancelRegistry_CancelAllForCancelsRegistered verifies the core
// invariant of issue #99: tenant.deleted → every in-flight day-2 wait for
// that tenant observes ctx.Done immediately.
func TestDay2CancelRegistry_CancelAllForCancelsRegistered(t *testing.T) {
	var r day2CancelRegistry
	ctxA, cancelA := r.Register(context.Background(), "tenant-x", "job-1")
	t.Cleanup(cancelA)
	ctxB, cancelB := r.Register(context.Background(), "tenant-x", "job-2")
	t.Cleanup(cancelB)
	ctxOther, cancelOther := r.Register(context.Background(), "tenant-y", "job-3")
	t.Cleanup(cancelOther)

	if n := r.CancelAllFor("tenant-x"); n != 2 {
		t.Fatalf("CancelAllFor returned %d, want 2", n)
	}

	// tenant-x contexts must be canceled within a short wall-clock window.
	select {
	case <-ctxA.Done():
	case <-time.After(time.Second):
		t.Fatal("ctxA not canceled within 1s")
	}
	select {
	case <-ctxB.Done():
	case <-time.After(time.Second):
		t.Fatal("ctxB not canceled within 1s")
	}

	// tenant-y unaffected.
	if ctxOther.Err() != nil {
		t.Fatalf("ctxOther should still be live, got %v", ctxOther.Err())
	}

	// Subsequent CancelAllFor on same slug is a no-op.
	if n := r.CancelAllFor("tenant-x"); n != 0 {
		t.Fatalf("second CancelAllFor returned %d, want 0", n)
	}
}

// TestDay2CancelRegistry_UnregisterIsIdempotent covers the defer-unregister
// path after CancelAllFor has already evicted the entry.
func TestDay2CancelRegistry_UnregisterIsIdempotent(t *testing.T) {
	var r day2CancelRegistry
	_, cancel := r.Register(context.Background(), "tenant-z", "job-1")
	t.Cleanup(cancel)
	r.CancelAllFor("tenant-z")
	// Previously-registered Unregister must not panic.
	r.Unregister("tenant-z", "job-1")
	// And a never-registered pair must also no-op.
	r.Unregister("tenant-nope", "job-nope")
}

// TestDay2CancelRegistry_ConcurrentRegisterCancel exercises the race between
// many goroutines registering waits and CancelAllFor preempting them. No
// panic and every registered context must be observed canceled.
func TestDay2CancelRegistry_ConcurrentRegisterCancel(t *testing.T) {
	var r day2CancelRegistry
	const N = 64
	ctxs := make([]context.Context, N)
	var wgReg sync.WaitGroup
	wgReg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wgReg.Done()
			ctx, cancel := r.Register(context.Background(), "tenant-c", jobID(i))
			_ = cancel
			ctxs[i] = ctx
		}(i)
	}
	wgReg.Wait()

	if n := r.CancelAllFor("tenant-c"); n != N {
		t.Fatalf("CancelAllFor returned %d, want %d", n, N)
	}
	for i, ctx := range ctxs {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatalf("ctx[%d] not canceled within 1s", i)
		}
	}
}

func jobID(i int) string {
	return "job-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := make([]byte, 0, 4)
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	return string(buf)
}
