package nats

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	pvalkey "github.com/openova-io/openova/core/cmd/projector/internal/valkey"
)

// fakeMsg is a minimal jetstream.Msg implementation used by handleOne
// tests. It records which terminal verb (Ack / Nak / Term) was called
// so the assertions can verify retry behaviour without touching a
// real JetStream broker.
type fakeMsg struct {
	data    []byte
	subj    string
	acked   atomic.Int32
	naked   atomic.Int32
	termed  atomic.Int32
	progrs  atomic.Int32
	headers nats.Header
}

func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *fakeMsg) Data() []byte                              { return m.data }
func (m *fakeMsg) Headers() nats.Header                      { return m.headers }
func (m *fakeMsg) Subject() string                           { return m.subj }
func (m *fakeMsg) Reply() string                             { return "" }
func (m *fakeMsg) Ack() error                                { m.acked.Add(1); return nil }
func (m *fakeMsg) DoubleAck(_ context.Context) error         { m.acked.Add(1); return nil }
func (m *fakeMsg) Nak() error                                { m.naked.Add(1); return nil }
func (m *fakeMsg) NakWithDelay(_ time.Duration) error        { m.naked.Add(1); return nil }
func (m *fakeMsg) InProgress() error                         { m.progrs.Add(1); return nil }
func (m *fakeMsg) Term() error                               { m.termed.Add(1); return nil }
func (m *fakeMsg) TermWithReason(_ string) error             { m.termed.Add(1); return nil }

func newConsumer(t *testing.T) *Consumer {
	return &Consumer{
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

func TestHandleOne_HappyPath_Acks(t *testing.T) {
	c := newConsumer(t)
	mem := pvalkey.NewMemKV()
	p := pvalkey.NewProjector(mem, time.Hour)

	body := []byte(`{
		"cluster":"omantel",
		"kind":"pod",
		"type":"ADDED",
		"object":{"metadata":{"name":"web-1","namespace":"default"}},
		"at":"2026-01-01T00:00:00Z"
	}`)
	m := &fakeMsg{data: body}
	c.handleOne(context.Background(), p, m)

	if got := m.acked.Load(); got != 1 {
		t.Fatalf("acked=%d, want 1", got)
	}
	if got := m.naked.Load(); got != 0 {
		t.Fatalf("naked=%d, want 0", got)
	}
	got, ok := mem.Get("cluster:omantel:kind:pod:default/web-1")
	if !ok {
		t.Fatal("key absent in valkey")
	}
	// The projector stores the verbatim message body.
	if string(got) != string(body) {
		t.Fatalf("body mismatch:\ngot  %s\nwant %s", got, body)
	}
}

func TestHandleOne_MalformedJSON_Terminates(t *testing.T) {
	c := newConsumer(t)
	p := pvalkey.NewProjector(pvalkey.NewMemKV(), time.Hour)

	m := &fakeMsg{data: []byte(`not json`)}
	c.handleOne(context.Background(), p, m)

	if got := m.termed.Load(); got != 1 {
		t.Fatalf("termed=%d, want 1", got)
	}
	if got := m.acked.Load(); got != 0 {
		t.Fatalf("acked=%d, want 0", got)
	}
	if got := m.naked.Load(); got != 0 {
		t.Fatalf("naked=%d, want 0", got)
	}
}

func TestHandleOne_KVError_Naks(t *testing.T) {
	c := newConsumer(t)
	failingKV := &errKV{err: errors.New("redis down")}
	p := pvalkey.NewProjector(failingKV, time.Hour)

	body := []byte(`{
		"cluster":"omantel","kind":"pod","type":"ADDED",
		"object":{"metadata":{"name":"x","namespace":"y"}},
		"at":"2026-01-01T00:00:00Z"
	}`)
	m := &fakeMsg{data: body}
	c.handleOne(context.Background(), p, m)

	if got := m.naked.Load(); got != 1 {
		t.Fatalf("naked=%d, want 1", got)
	}
	if got := m.acked.Load(); got != 0 {
		t.Fatalf("acked=%d, want 0", got)
	}
}

// errKV implements valkey.KV but returns the configured error on every
// Set / Del so handleOne's nak path is exercised.
type errKV struct{ err error }

func (e *errKV) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return e.err
}
func (e *errKV) Del(_ context.Context, _ string) error { return e.err }
func (e *errKV) Close()                                {}
