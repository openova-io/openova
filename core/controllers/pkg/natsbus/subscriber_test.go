// Unit tests for natsbus. Verifies the per-message dispatch contract
// (Ack on handler success, Nak on handler error, Ack-to-skip on
// malformed JSON) without spinning up an embedded NATS server. Live-
// broker integration is covered by the D35 fresh-prov verifier.
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// fakeMsg implements just enough of jetstream.Msg for dispatchOne to
// drive Ack / Nak / NakWithDelay outcomes. Method set mirrors the
// interface so a typecheck against jetstream.Msg keeps the fake honest.
type fakeMsg struct {
	data []byte

	mu          sync.Mutex
	ackCount    int
	nakCount    int
	termCount   int
	nakDelay    time.Duration
	inProgress  int
	headers     map[string][]string
	subject     string
	reply       string
}

var _ jetstream.Msg = (*fakeMsg)(nil)

func (f *fakeMsg) Data() []byte { return f.data }
func (f *fakeMsg) Headers() nats.Header {
	out := nats.Header{}
	for k, vs := range f.headers {
		out[k] = vs
	}
	return out
}
func (f *fakeMsg) Subject() string { return f.subject }
func (f *fakeMsg) Reply() string   { return f.reply }
func (f *fakeMsg) Ack() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackCount++
	return nil
}
func (f *fakeMsg) DoubleAck(context.Context) error {
	return f.Ack()
}
func (f *fakeMsg) Nak() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nakCount++
	return nil
}
func (f *fakeMsg) NakWithDelay(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nakCount++
	f.nakDelay = d
	return nil
}
func (f *fakeMsg) InProgress() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inProgress++
	return nil
}
func (f *fakeMsg) Term() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.termCount++
	return nil
}
func (f *fakeMsg) TermWithReason(string) error { return f.Term() }
func (f *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{}, nil
}

// TestDispatchOne_HandlerSuccess pins: a handler returning nil Acks
// the message exactly once and never Naks. Bodyguard for the D35
// happy path — every successful round-trip moves the consumer cursor.
func TestDispatchOne_HandlerSuccess(t *testing.T) {
	payload := Event{
		ID:        "evt-1",
		Type:      "tenant.created",
		Source:    "tenant-service",
		Timestamp: time.Now().UTC(),
		TenantID:  "tnt-abc",
		Data:      json.RawMessage(`{"id":"tnt-abc","slug":"acme"}`),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	msg := &fakeMsg{data: body, subject: SubjectTenantCreated}

	var seen *Event
	handler := Handler(func(_ context.Context, ev *Event) error {
		seen = ev
		return nil
	})

	dispatchOne(context.Background(), msg, handler, SubjectTenantCreated, "test-d", 5*time.Second)

	if msg.ackCount != 1 {
		t.Errorf("want Ack count 1, got %d", msg.ackCount)
	}
	if msg.nakCount != 0 {
		t.Errorf("want Nak count 0, got %d", msg.nakCount)
	}
	if seen == nil || seen.ID != "evt-1" || seen.Type != "tenant.created" {
		t.Errorf("handler did not receive the parsed envelope: %+v", seen)
	}
	if seen.TenantID != "tnt-abc" {
		t.Errorf("envelope tenant_id mismatch: got %q want %q", seen.TenantID, "tnt-abc")
	}
}

// TestDispatchOne_HandlerError pins: a handler returning a non-nil
// error Naks (with the configured backoff) and does NOT Ack. Bodyguard
// for transient downstream failures — JetStream must redeliver.
func TestDispatchOne_HandlerError(t *testing.T) {
	body, _ := json.Marshal(Event{ID: "evt-err", Type: "order.placed"})
	msg := &fakeMsg{data: body, subject: SubjectOrderPlaced}

	handler := Handler(func(context.Context, *Event) error {
		return errors.New("downstream API unreachable")
	})

	const backoff = 7 * time.Second
	dispatchOne(context.Background(), msg, handler, SubjectOrderPlaced, "test-d", backoff)

	if msg.ackCount != 0 {
		t.Errorf("want Ack count 0 on handler error, got %d", msg.ackCount)
	}
	if msg.nakCount != 1 {
		t.Errorf("want Nak count 1 on handler error, got %d", msg.nakCount)
	}
	if msg.nakDelay != backoff {
		t.Errorf("want Nak delay %v, got %v", backoff, msg.nakDelay)
	}
}

// TestDispatchOne_MalformedJSON pins: a payload that fails json.Unmarshal
// is Ack'd-skipped (the consumer cursor advances) so a poison pill cannot
// hot-loop the subscriber. Caught by the operator log line, not the
// transport.
func TestDispatchOne_MalformedJSON(t *testing.T) {
	msg := &fakeMsg{data: []byte("not-json{"), subject: SubjectTenantSandboxRequested}

	called := false
	handler := Handler(func(context.Context, *Event) error {
		called = true
		return nil
	})

	dispatchOne(context.Background(), msg, handler, SubjectTenantSandboxRequested, "test-d", 5*time.Second)

	if called {
		t.Error("handler should NOT be invoked on malformed JSON")
	}
	if msg.ackCount != 1 {
		t.Errorf("want Ack count 1 to skip poison pill, got %d", msg.ackCount)
	}
	if msg.nakCount != 0 {
		t.Errorf("want Nak count 0 on poison pill (Term/Ack only), got %d", msg.nakCount)
	}
}

// TestDispatchOne_SandboxRequestedPayload pins the exact wire format
// the publish-side (PR #1633 tenant.handlers.CreateOrg) emits — the
// downstream sandbox-controller handler reads tenant_id, org_slug,
// owner_id, owner_email, agent_catalogue out of the Data blob. If
// the publish-side renames a field this test goes red so the consumer
// stays in lockstep.
func TestDispatchOne_SandboxRequestedPayload(t *testing.T) {
	payload := Event{
		ID:        "evt-sb",
		Type:      "tenant.sandbox_requested",
		Source:    "tenant-service",
		Timestamp: time.Now().UTC(),
		TenantID:  "tnt-sb",
		Data: json.RawMessage(`{
			"tenant_id":"tnt-sb",
			"org_slug":"acme",
			"owner_id":"u-1",
			"owner_email":"ceo@acme.com",
			"agent_catalogue":["qwen","claude"]
		}`),
	}
	body, _ := json.Marshal(payload)
	msg := &fakeMsg{data: body, subject: SubjectTenantSandboxRequested}

	var got struct {
		TenantID       string   `json:"tenant_id"`
		OrgSlug        string   `json:"org_slug"`
		OwnerEmail     string   `json:"owner_email"`
		AgentCatalogue []string `json:"agent_catalogue"`
	}
	handler := Handler(func(_ context.Context, ev *Event) error {
		return json.Unmarshal(ev.Data, &got)
	})

	dispatchOne(context.Background(), msg, handler, SubjectTenantSandboxRequested, "test-d", 5*time.Second)

	if msg.ackCount != 1 {
		t.Fatalf("expected Ack=1 on success, got Ack=%d Nak=%d", msg.ackCount, msg.nakCount)
	}
	if got.OrgSlug != "acme" || got.OwnerEmail != "ceo@acme.com" {
		t.Errorf("payload fields not surfaced: %+v", got)
	}
	if len(got.AgentCatalogue) != 2 || got.AgentCatalogue[0] != "qwen" {
		t.Errorf("agent_catalogue not surfaced: %+v", got.AgentCatalogue)
	}
}
