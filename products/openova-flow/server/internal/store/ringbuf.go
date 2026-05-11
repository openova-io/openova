// Package store — in-memory state per flowId. No persistence.
package store

import (
	"sync"

	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// RingBuffer is a fixed-capacity FIFO ring of FlowMessage envelopes.
// On overflow the oldest entry is dropped — emitters are expected to
// re-emit a snapshot on reconnect, so durability per-event is not the
// invariant; the invariant is "the buffer holds the most recent N
// events, in order, for SSE catch-up".
//
// Concurrency: safe for concurrent Append + Snapshot + Subscribe.
type RingBuffer struct {
	mu    sync.RWMutex
	cap   int
	data  []*types.FlowMessage
	head  int // next write index
	count int

	// monotonic counter so SSE subscribers can request "everything
	// after sequence N" — the cursor sits between calls.
	seq uint64
}

// NewRingBuffer constructs a buffer with the supplied capacity (must
// be > 0).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		cap:  capacity,
		data: make([]*types.FlowMessage, capacity),
	}
}

// Append pushes one envelope onto the buffer. Returns the assigned
// monotonic sequence number; the SSE handler emits this in the SSE
// `id:` field so reconnecting clients can resume from Last-Event-ID.
func (rb *RingBuffer) Append(m *types.FlowMessage) uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.seq++
	rb.data[rb.head] = m
	rb.head = (rb.head + 1) % rb.cap
	if rb.count < rb.cap {
		rb.count++
	}
	return rb.seq
}

// Slice returns a copy of the buffered envelopes in insertion order
// (oldest first). The returned slice is safe to mutate by the caller.
func (rb *RingBuffer) Slice() []*types.FlowMessage {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	out := make([]*types.FlowMessage, 0, rb.count)
	if rb.count < rb.cap {
		// data[0..count) is the entire ring contiguous from index 0.
		for i := 0; i < rb.count; i++ {
			out = append(out, rb.data[i])
		}
		return out
	}
	// Full ring: oldest is at head.
	for i := 0; i < rb.cap; i++ {
		idx := (rb.head + i) % rb.cap
		out = append(out, rb.data[idx])
	}
	return out
}

// Seq returns the latest assigned sequence number.
func (rb *RingBuffer) Seq() uint64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.seq
}

// Len returns the number of currently buffered envelopes.
func (rb *RingBuffer) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}
