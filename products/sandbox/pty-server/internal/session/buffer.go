// Package session implements the per-PTY session model used by
// pty-server. A Session owns one PTY file descriptor + the child
// process attached to it, plus a fan-out hub that ships PTY stdout to
// any number of concurrent WebSocket subscribers (browser tabs, mobile
// card-mode clients).
//
// The ring buffer here keeps the last 256 KiB of PTY output so a newly
// attaching client can replay the recent screen before joining the
// live stream — see architecture.md §1 "ring buffer (256 KB) for
// replay" and §2 "Replay last N KB on reconnect".
package session

import "sync"

// RingBuffer is a fixed-capacity byte ring used to retain the recent
// tail of PTY stdout for replay on new client connect.
//
// The buffer is goroutine-safe: Write() is called from the PTY read
// loop, Snapshot() from any number of attaching subscribers.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	cap  int
	full bool
	pos  int // next write position
}

// NewRingBuffer returns a ring of the given capacity in bytes.
// The default Sandbox session uses 256 KiB (262_144).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		buf: make([]byte, capacity),
		cap: capacity,
	}
}

// Write appends p to the ring, overwriting the oldest bytes if needed.
// It never errors and never blocks beyond its own mutex.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		r.buf[r.pos] = b
		r.pos++
		if r.pos >= r.cap {
			r.pos = 0
			r.full = true
		}
	}
	return len(p), nil
}

// Snapshot returns a copy of the ring contents in chronological order
// (oldest byte first). Safe to call while writes are in flight.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, r.cap)
	copy(out, r.buf[r.pos:])
	copy(out[r.cap-r.pos:], r.buf[:r.pos])
	return out
}

// Len returns the number of bytes currently retained (<= cap).
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.cap
	}
	return r.pos
}
