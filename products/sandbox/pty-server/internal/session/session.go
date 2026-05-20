package session

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// DefaultRingBytes is the per-session replay-buffer capacity used when a
// caller leaves [Spec.RingBytes] zero. The default is sized so a freshly-
// attaching client (the canonical "close laptop, open phone" multi-device
// handoff path documented in products/sandbox/docs/user-journey.md
// Scene 6) replays a meaningful slice of the recent terminal context
// rather than just the last screen.
//
// History: pre-TBD-V22 (#1986 F1, 2026-05-20) this was a hardcoded
// 256 KiB literal, which on a real Plan-mode / file-listing / multi-turn
// agent session rolls in well under a minute — the multi-device replay
// claim in user-journey.md was unbacked. The default is now 1 MiB
// (1_048_576) — enough for several minutes of typical agent output, and
// trivially small against the per-Sandbox pty-server Pod memory budget
// (one ring per live PTY; ten concurrent sessions consume 10 MiB).
//
// Operators may override via the SANDBOX_RING_BUFFER_BYTES env var read
// in [LoadDefaultRingBytesFromEnv] at pty-server startup. The package
// also exposes the package-level variable directly so tests can set it
// without going through env. The HARD upper bound (16 MiB) exists to
// stop a misconfigured operator from making a single ring dominate the
// pty-server Pod's memory; values above the bound are clamped + logged
// (see [LoadDefaultRingBytesFromEnv]).
var DefaultRingBytes = 1 << 20 // 1 MiB

// MaxRingBytes is the hard ceiling enforced by [LoadDefaultRingBytesFromEnv].
// Per-Pod memory budget rationale: at 16 MiB × 10 concurrent sessions = 160
// MiB worst-case, still well under typical Sandbox Pod memory limits
// (architecture.md §1 sizing). Operators wanting larger replay windows
// should instead persist conversation history via the agent's own
// `--continue` flag (user-journey.md Scene 6 "Pod restart" row), not
// inflate the in-memory ring.
const MaxRingBytes = 16 << 20 // 16 MiB

// LoadDefaultRingBytesFromEnv reads SANDBOX_RING_BUFFER_BYTES and, when
// set to a positive integer, updates [DefaultRingBytes]. Empty / non-
// integer / non-positive values leave the default unchanged. Values
// above [MaxRingBytes] are clamped (and the clamp is announced to the
// caller via the returned (effective, clamped) tuple so the pty-server
// can log the decision at startup).
//
// Called once from cmd/pty-server/main.go on process start; safe to
// call again from tests via t.Setenv + this function.
func LoadDefaultRingBytesFromEnv() (effective int, clamped bool) {
	raw := strings.TrimSpace(os.Getenv("SANDBOX_RING_BUFFER_BYTES"))
	if raw == "" {
		return DefaultRingBytes, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultRingBytes, false
	}
	if n > MaxRingBytes {
		DefaultRingBytes = MaxRingBytes
		return MaxRingBytes, true
	}
	DefaultRingBytes = n
	return n, false
}

// ErrClosed indicates the Session has already exited (graceful or
// forced) and no further writes / signals / resizes will succeed.
var ErrClosed = errors.New("session: closed")

// Subscriber receives a copy of every PTY stdout chunk after attach.
// The Session writes the chunk to Ch; if Ch is full the Session drops
// the chunk for THAT subscriber only (slow consumers do not stall the
// PTY read loop or the other consumers). The Done channel is closed
// when the Session exits, signalling subscribers to wind down.
type Subscriber struct {
	Ch   chan []byte
	Done chan struct{}
}

// Session is one PTY + one child process + N concurrent subscribers.
//
// Lifecycle:
//
//	NewSession -> Start -> (Subscribe / Write / Resize / Signal) ... -> Close
//
// Subscribe replays the ring buffer to the new subscriber, then joins
// it to the live fan-out. Close sends SIGTERM, waits gracefully for
// up to 5 s, then SIGKILLs (per architecture.md §2 "graceful stop,
// then SIGKILL").
type Session struct {
	ID        string
	CreatedAt time.Time

	cmd     *exec.Cmd
	ptyFile *os.File
	ring    *RingBuffer

	mu          sync.Mutex
	subscribers map[*Subscriber]struct{}
	closed      bool
	done        chan struct{}
	exitErr     error
}

// Spec describes how to spawn the agent process inside the PTY.
type Spec struct {
	// Command is argv. Command[0] is the binary, the rest are args.
	Command []string
	// Env is the full environment for the child. nil = inherit
	// pty-server's os.Environ().
	Env []string
	// Cwd is the child's working directory. "" = inherit.
	Cwd string
	// Rows / Cols seed the initial PTY size; the browser sends
	// SIGWINCH-triggering Resize() calls once it knows its viewport.
	Rows uint16
	Cols uint16
	// RingBytes is the replay buffer size in bytes. Zero ⇒
	// [DefaultRingBytes] (1 MiB, overridable via
	// SANDBOX_RING_BUFFER_BYTES at pty-server startup). The buffer
	// holds the trailing tail of PTY stdout for replay on new client
	// attach — see products/sandbox/docs/user-journey.md Scene 6
	// "Mobile handoff".
	RingBytes int
}

// New spawns the command in a fresh PTY and returns a started
// Session. The PTY read loop runs in its own goroutine; subscribers
// can be added immediately.
func New(id string, spec Spec) (*Session, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("session: empty Command")
	}
	if spec.Rows == 0 {
		spec.Rows = 24
	}
	if spec.Cols == 0 {
		spec.Cols = 80
	}
	if spec.RingBytes == 0 {
		spec.RingBytes = DefaultRingBytes
	}

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	if spec.Env != nil {
		cmd.Env = spec.Env
	} else {
		cmd.Env = os.Environ()
	}
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: spec.Rows, Cols: spec.Cols})
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:          id,
		CreatedAt:   time.Now().UTC(),
		cmd:         cmd,
		ptyFile:     f,
		ring:        NewRingBuffer(spec.RingBytes),
		subscribers: make(map[*Subscriber]struct{}),
		done:        make(chan struct{}),
	}

	go s.readLoop()
	go s.waitLoop()

	return s, nil
}

// readLoop drains the PTY master fd, mirrors every chunk into the
// ring buffer, and fans the chunk out to every live subscriber. It
// exits when the PTY is closed (typically: child exited and Wait()
// returned).
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptyFile.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			_, _ = s.ring.Write(chunk)
			s.fanout(chunk)
		}
		if err != nil {
			return
		}
	}
}

// waitLoop reaps the child process. When the child exits we close
// the PTY (unblocks readLoop), mark the Session closed, and signal
// all subscribers via Done.
func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.exitErr = err
		_ = s.ptyFile.Close()
		close(s.done)
		for sub := range s.subscribers {
			close(sub.Done)
		}
	}
	s.mu.Unlock()
}

func (s *Session) fanout(chunk []byte) {
	s.mu.Lock()
	subs := make([]*Subscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.Ch <- chunk:
		default:
			// Slow consumer: drop for this subscriber only;
			// the PTY-side and other consumers are unaffected.
		}
	}
}

// Subscribe registers a new fan-out consumer. The returned Subscriber
// receives every PTY stdout chunk that arrives AFTER subscribe time;
// the second return value is a snapshot of the ring (call replay
// first, then loop on Ch).
func (s *Session) Subscribe(bufferDepth int) (*Subscriber, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	if bufferDepth <= 0 {
		bufferDepth = 64
	}
	sub := &Subscriber{
		Ch:   make(chan []byte, bufferDepth),
		Done: make(chan struct{}),
	}
	s.subscribers[sub] = struct{}{}
	return sub, s.ring.Snapshot(), nil
}

// Unsubscribe removes the subscriber from the fan-out set. Safe to
// call even after the Session has exited.
func (s *Session) Unsubscribe(sub *Subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers, sub)
}

// Write forwards user keystrokes (raw bytes from the WS) to PTY stdin.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	f := s.ptyFile
	s.mu.Unlock()
	return f.Write(p)
}

// Resize triggers SIGWINCH on the child by re-setting the PTY winsize
// (per architecture.md §1 / §2 "SIGWINCH on browser resize").
func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	f := s.ptyFile
	s.mu.Unlock()
	return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
}

// Signal delivers a UNIX signal to the child process group. The
// pty-server API accepts named INT / QUIT / TERM / KILL (the canonical
// "user-driven abort" set from architecture.md §2).
func (s *Session) Signal(sig syscall.Signal) error {
	s.mu.Lock()
	if s.closed || s.cmd == nil || s.cmd.Process == nil {
		s.mu.Unlock()
		return ErrClosed
	}
	pid := s.cmd.Process.Pid
	s.mu.Unlock()
	// Negative pid → send to the whole process group, so children
	// spawned by the agent (e.g. shell tools) also receive the
	// signal. The PTY allocated by creack/pty is the controlling
	// terminal, so pid == pgid.
	return syscall.Kill(-pid, sig)
}

// Close gracefully stops the child: SIGTERM, wait up to 5 s, SIGKILL.
// Idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	already := s.closed
	s.mu.Unlock()
	if already {
		return nil
	}
	_ = s.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
		return nil
	case <-time.After(5 * time.Second):
	}
	_ = s.Signal(syscall.SIGKILL)
	<-s.done
	return nil
}

// Done returns a channel closed when the session has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitError is the process exit error after Wait, or nil if the
// session has not exited / exited successfully.
func (s *Session) ExitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return nil
	}
	return s.exitErr
}

// Compile-time assertion that *Session implements io.Writer for any
// future use that wants to push raw bytes (e.g. test fakes).
var _ io.Writer = (*Session)(nil)
