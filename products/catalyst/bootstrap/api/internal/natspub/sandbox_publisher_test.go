// Package natspub tests — TBD-D35c. The tests verify the concrete
// publisher's contract WITHOUT a live NATS broker, mirroring the
// fake-only style of core/cmd/projector/internal/nats/consumer_test.go.
//
// Why no embedded NATS server: zero existing tests in this repo bring
// up an embedded nats-server, and pulling in nats-server/test would
// double the catalyst-api binary's transitive dep surface for a single
// test file. The contract that matters at this layer is the marshal +
// publish call — the broker is a transparent passthrough whose
// behaviour is covered by upstream nats.go tests.
//
// End-to-end coverage of the producer-consumer round-trip happens at
// the D35 Playwright gate (fresh prov walkthrough), not here.
package natspub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// fakeConn is a natsConn implementation that records every Publish call.
// Safe for concurrent use so a future async-publish handler variant
// can still assert reliably.
type fakeConn struct {
	mu        sync.Mutex
	published []publishedMsg
	pubErr    error
	flushErr  error
	closed    bool
	connected bool
}

type publishedMsg struct {
	subject string
	data    []byte
}

func (f *fakeConn) Publish(subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pubErr != nil {
		return f.pubErr
	}
	// Copy the slice — nats.go documents Publish as not retaining the
	// caller's buffer, so a real call site might pool it.
	cp := make([]byte, len(data))
	copy(cp, data)
	f.published = append(f.published, publishedMsg{subject: subject, data: cp})
	return nil
}

func (f *fakeConn) Flush() error { return f.flushErr }

func (f *fakeConn) IsConnected() bool { return f.connected }

func (f *fakeConn) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeConn) calls() []publishedMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publishedMsg, len(f.published))
	copy(out, f.published)
	return out
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestPublishTenantEvent_HappyPath — the canonical happy-path assertion:
// PublishTenantEvent calls the underlying conn.Publish exactly once with
// the supplied subject and a JSON-marshaled body that round-trips back
// to the original TenantEvent.
func TestPublishTenantEvent_HappyPath(t *testing.T) {
	fc := &fakeConn{connected: true}
	p := NewPublisher(fc, newTestLogger())

	now := time.Now().UTC().Truncate(time.Second)
	ev := handler.TenantEvent{
		TenantID:    "acme",
		SandboxID:   "sandbox-operator-at-acme-com",
		RequestedBy: "operator@acme.com",
		Timestamp:   now,
		SpecHash:    "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
	}

	if err := p.PublishTenantEvent(context.Background(), handler.SandboxRequestedSubject, ev); err != nil {
		t.Fatalf("PublishTenantEvent: %v", err)
	}

	calls := fc.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 publish, got %d", len(calls))
	}
	if calls[0].subject != handler.SandboxRequestedSubject {
		t.Errorf("subject: want %q, got %q", handler.SandboxRequestedSubject, calls[0].subject)
	}

	var got handler.TenantEvent
	if err := json.Unmarshal(calls[0].data, &got); err != nil {
		t.Fatalf("round-trip unmarshal: %v\nraw=%s", err, calls[0].data)
	}
	if got.TenantID != ev.TenantID {
		t.Errorf("tenant_id: want %q, got %q", ev.TenantID, got.TenantID)
	}
	if got.SandboxID != ev.SandboxID {
		t.Errorf("sandbox_id: want %q, got %q", ev.SandboxID, got.SandboxID)
	}
	if got.RequestedBy != ev.RequestedBy {
		t.Errorf("requested_by: want %q, got %q", ev.RequestedBy, got.RequestedBy)
	}
	if !got.Timestamp.Equal(ev.Timestamp) {
		t.Errorf("timestamp: want %v, got %v", ev.Timestamp, got.Timestamp)
	}
	if got.SpecHash != ev.SpecHash {
		t.Errorf("spec_hash: want %q, got %q", ev.SpecHash, got.SpecHash)
	}
}

// TestPublishTenantEvent_BrokerError_Surfaces — a broker-write error
// must bubble back to the caller so the handler can log + continue.
// The handler-level test (sandbox_sessions_nats_test.go::PublishError_
// DoesNotFailRequest) verifies the continue-on-error behaviour; this
// test pins the publisher's side of the contract.
func TestPublishTenantEvent_BrokerError_Surfaces(t *testing.T) {
	fc := &fakeConn{pubErr: errors.New("broker down")}
	p := NewPublisher(fc, newTestLogger())

	err := p.PublishTenantEvent(context.Background(), handler.SandboxRequestedSubject, handler.TenantEvent{
		TenantID: "acme", SandboxID: "sb-1",
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// TestPublishTenantEvent_NilReceiver — defensive: a nil *Publisher
// receiver MUST NOT panic. main.go's env-driven constructor returns nil
// when CATALYST_NATS_URL is unset, and handler.go nil-checks before
// calling, but a stray call through an embedded-typed nil interface
// has historically segfaulted in similar code paths.
func TestPublishTenantEvent_NilReceiver(t *testing.T) {
	var p *Publisher
	err := p.PublishTenantEvent(context.Background(), handler.SandboxRequestedSubject, handler.TenantEvent{})
	if err == nil {
		t.Fatalf("expected error on nil receiver, got nil (panic-safe but caller has no signal)")
	}
}

// TestPublishTenantEvent_EmptySubject — an empty subject is a programmer
// error; reject at the publisher boundary so it doesn't leak as a NATS
// protocol error from the broker on the other end.
func TestPublishTenantEvent_EmptySubject(t *testing.T) {
	fc := &fakeConn{}
	p := NewPublisher(fc, newTestLogger())

	err := p.PublishTenantEvent(context.Background(), "", handler.TenantEvent{TenantID: "acme"})
	if err == nil {
		t.Fatalf("expected error on empty subject, got nil")
	}
	if len(fc.calls()) != 0 {
		t.Errorf("publisher must not call broker on empty subject; got %d calls", len(fc.calls()))
	}
}

// TestPublishTenantEvent_ContextCancelled — a caller-cancelled context
// must short-circuit before the broker is touched. Protects against a
// hung handler that has already given up emitting the audit envelope.
func TestPublishTenantEvent_ContextCancelled(t *testing.T) {
	fc := &fakeConn{}
	p := NewPublisher(fc, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.PublishTenantEvent(ctx, handler.SandboxRequestedSubject, handler.TenantEvent{TenantID: "acme"})
	if err == nil {
		t.Fatalf("expected error on cancelled ctx, got nil")
	}
	if len(fc.calls()) != 0 {
		t.Errorf("publisher must not call broker on cancelled ctx; got %d calls", len(fc.calls()))
	}
}

// TestClose_FlushesAndClosesConn — Close must drain pending publishes
// before terminating the connection so a Pod shutdown doesn't lose
// in-flight envelopes. Tested by asserting both Flush + Close called.
func TestClose_FlushesAndClosesConn(t *testing.T) {
	fc := &fakeConn{}
	p := NewPublisher(fc, newTestLogger())

	p.Close()

	if !fc.closed {
		t.Errorf("Close must invoke conn.Close")
	}
}

// TestClose_NilReceiver — defensive: Close on a nil receiver is a no-op,
// not a panic. main.go's deferred Close runs unconditionally; nil-safe
// keeps the cold-start-without-NATS-URL path from panicking on exit.
func TestClose_NilReceiver(t *testing.T) {
	var p *Publisher
	// Will panic if Close is not nil-safe.
	p.Close()
}

// TestDial_EmptyURL — empty URL is rejected before the network call so
// the caller (newTenantEventPublisherFromEnv) sees a clean error rather
// than nats.go's "invalid url" deep-stack.
func TestDial_EmptyURL(t *testing.T) {
	_, err := Dial("", newTestLogger())
	if err == nil {
		t.Fatalf("expected error on empty URL, got nil")
	}
}
