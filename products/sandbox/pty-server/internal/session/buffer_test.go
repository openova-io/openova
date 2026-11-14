// buffer_test.go — coverage for the replay ring buffer + the
// SANDBOX_RING_BUFFER_BYTES env-driven default (TBD-V22 #1986 F1,
// 2026-05-20). The Session lifecycle proper (PTY + child process)
// needs /bin/sh + a TTY and is exercised end-to-end in integration
// tests; this file isolates the ring + env-loader so they're
// reachable from `go test` in any container.
package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRingBuffer_WriteShortAndSnapshot(t *testing.T) {
	t.Parallel()
	r := NewRingBuffer(16)
	n, err := r.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("write short: n=%d err=%v", n, err)
	}
	if got := r.Len(); got != 5 {
		t.Fatalf("len after short write: got %d want 5", got)
	}
	if got := r.Snapshot(); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("snapshot short: got %q", got)
	}
}

func TestRingBuffer_WrapPreservesChronologicalOrder(t *testing.T) {
	t.Parallel()
	r := NewRingBuffer(8)
	// Write 13 bytes into an 8-byte ring — last 8 must survive in
	// chronological order.
	r.Write([]byte("ABCDEFGHIJKLM"))
	if got := r.Len(); got != 8 {
		t.Fatalf("len after wrap: got %d want 8", got)
	}
	want := []byte("FGHIJKLM")
	if got := r.Snapshot(); !bytes.Equal(got, want) {
		t.Fatalf("snapshot after wrap: got %q want %q", got, want)
	}
}

func TestRingBuffer_RejectsZeroCapacity(t *testing.T) {
	t.Parallel()
	// NewRingBuffer(<=0) must NOT panic on make([]byte, n) — it
	// promotes to capacity 1 so the ring is at least usable for a
	// degenerate single-byte stream.
	r := NewRingBuffer(0)
	if r == nil {
		t.Fatal("NewRingBuffer(0) returned nil")
	}
	r.Write([]byte("xyz"))
	if got := r.Len(); got != 1 {
		t.Fatalf("zero-cap promoted len: got %d want 1", got)
	}
	if got := r.Snapshot(); !bytes.Equal(got, []byte("z")) {
		t.Fatalf("zero-cap promoted snapshot: got %q want %q", got, "z")
	}
}

func TestDefaultRingBytes_OneMiB(t *testing.T) {
	// Pre-TBD-V22 the default was 256 KiB which on a real agent
	// session rolled in well under a minute. The new default is
	// 1 MiB — large enough to cover several minutes of typical
	// agent output, small enough to be trivial against per-Pod
	// memory budgets. This test is a guardrail against an accidental
	// downward revert.
	if DefaultRingBytes != 1<<20 {
		t.Fatalf("DefaultRingBytes: got %d want %d (1 MiB)", DefaultRingBytes, 1<<20)
	}
}

func TestLoadDefaultRingBytesFromEnv_Unset(t *testing.T) {
	t.Setenv("SANDBOX_RING_BUFFER_BYTES", "")
	// Reset to known-good before the test so a prior parallel test
	// hasn't moved DefaultRingBytes.
	prev := DefaultRingBytes
	DefaultRingBytes = 1 << 20
	t.Cleanup(func() { DefaultRingBytes = prev })

	got, clamped := LoadDefaultRingBytesFromEnv()
	if got != 1<<20 || clamped {
		t.Fatalf("unset env: got (%d,%v) want (%d,false)", got, clamped, 1<<20)
	}
}

func TestLoadDefaultRingBytesFromEnv_HonorsValidValue(t *testing.T) {
	prev := DefaultRingBytes
	t.Cleanup(func() { DefaultRingBytes = prev })

	t.Setenv("SANDBOX_RING_BUFFER_BYTES", "524288")
	got, clamped := LoadDefaultRingBytesFromEnv()
	if got != 524288 || clamped {
		t.Fatalf("valid env: got (%d,%v) want (524288,false)", got, clamped)
	}
	if DefaultRingBytes != 524288 {
		t.Fatalf("DefaultRingBytes after env: got %d want 524288", DefaultRingBytes)
	}
}

func TestLoadDefaultRingBytesFromEnv_ClampsAboveCeiling(t *testing.T) {
	prev := DefaultRingBytes
	t.Cleanup(func() { DefaultRingBytes = prev })

	t.Setenv("SANDBOX_RING_BUFFER_BYTES", "999999999") // ~1 GiB
	got, clamped := LoadDefaultRingBytesFromEnv()
	if got != MaxRingBytes || !clamped {
		t.Fatalf("ceiling clamp: got (%d,%v) want (%d,true)", got, clamped, MaxRingBytes)
	}
	if DefaultRingBytes != MaxRingBytes {
		t.Fatalf("DefaultRingBytes after clamp: got %d want %d", DefaultRingBytes, MaxRingBytes)
	}
}

func TestLoadDefaultRingBytesFromEnv_RejectsGarbage(t *testing.T) {
	prev := DefaultRingBytes
	DefaultRingBytes = 1 << 20
	t.Cleanup(func() { DefaultRingBytes = prev })

	for _, raw := range []string{"abc", "-100", "0", " "} {
		t.Run(strings.ReplaceAll(raw, " ", "_space_"), func(t *testing.T) {
			t.Setenv("SANDBOX_RING_BUFFER_BYTES", raw)
			got, clamped := LoadDefaultRingBytesFromEnv()
			if got != 1<<20 || clamped {
				t.Fatalf("garbage %q: got (%d,%v) want (%d,false)", raw, got, clamped, 1<<20)
			}
		})
	}
}

func TestMaxRingBytes_SixteenMiB(t *testing.T) {
	if MaxRingBytes != 16<<20 {
		t.Fatalf("MaxRingBytes: got %d want %d (16 MiB)", MaxRingBytes, 16<<20)
	}
}
