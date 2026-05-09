package events

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAuditTypes_Coverage(t *testing.T) {
	t.Parallel()
	if len(AuditTypes) != 9 {
		t.Fatalf("expected 9 audit types, got %d", len(AuditTypes))
	}
	for _, want := range []string{
		TypeSwitchover, TypeFailbackPending, TypeFailbackCompleted,
		TypeLeaseLost, TypeLeaseAcquired, TypeCNPGLagBreach,
		TypeCNPGPromotable, TypeError, TypeReconcileSuccess,
	} {
		if !IsValidType(want) {
			t.Errorf("expected %q to be valid", want)
		}
	}
	if IsValidType("not-a-type") {
		t.Error("expected unknown type to be invalid")
	}
}

func TestEvent_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"happy", Event{Type: TypeSwitchover, ContinuumName: "ns/x", Timestamp: "now"}, true},
		{"bad type", Event{Type: "x", ContinuumName: "ns/x", Timestamp: "now"}, false},
		{"missing continuum", Event{Type: TypeSwitchover, Timestamp: "now"}, false},
		{"missing ts", Event{Type: TypeSwitchover, ContinuumName: "ns/x"}, false},
	}
	for _, c := range cases {
		err := c.e.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: ok=%v err=%v", c.name, c.ok, err)
		}
	}
}

func TestRecorder_PublishHappyPath(t *testing.T) {
	t.Parallel()
	r := NewRecorder()
	if err := r.Publish(context.Background(), Event{
		Type:          TypeSwitchover,
		ContinuumName: "ns/cr",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d want 1", r.Len())
	}
	es := r.Events()
	if es[0].Timestamp == "" {
		t.Error("Timestamp should be auto-stamped")
	}
}

func TestRecorder_PublishBadEvent(t *testing.T) {
	t.Parallel()
	r := NewRecorder()
	if err := r.Publish(context.Background(), Event{Type: "bad"}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRecorder_PublishContextCancel(t *testing.T) {
	t.Parallel()
	r := NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Publish(ctx, Event{Type: TypeSwitchover, ContinuumName: "ns/x"}); err == nil {
		t.Fatal("expected ctx-cancel error")
	}
}

func TestRecorder_EventsByType(t *testing.T) {
	t.Parallel()
	r := NewRecorder()
	for _, ty := range []string{TypeSwitchover, TypeError, TypeSwitchover} {
		if err := r.Publish(context.Background(), Event{Type: ty, ContinuumName: "ns/x"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if got := len(r.EventsByType(TypeSwitchover)); got != 2 {
		t.Errorf("EventsByType(switchover) = %d want 2", got)
	}
	if got := len(r.EventsByType(TypeError)); got != 1 {
		t.Errorf("EventsByType(error) = %d want 1", got)
	}
	if got := len(r.EventsByType(TypeFailbackPending)); got != 0 {
		t.Errorf("EventsByType(empty type) = %d want 0", got)
	}
}

func TestRecorder_Reset(t *testing.T) {
	t.Parallel()
	r := NewRecorder()
	_ = r.Publish(context.Background(), Event{Type: TypeError, ContinuumName: "ns/x"})
	r.Reset()
	if r.Len() != 0 {
		t.Fatalf("Reset: Len = %d want 0", r.Len())
	}
}

func TestFillTimestamp_PreservesCaller(t *testing.T) {
	t.Parallel()
	in := Event{Type: TypeSwitchover, ContinuumName: "ns/x", Timestamp: "ts-already"}
	out := FillTimestamp(in, nil)
	if out.Timestamp != "ts-already" {
		t.Errorf("FillTimestamp clobbered caller-provided ts: %q", out.Timestamp)
	}
}

func TestFillTimestamp_StampsWhenEmpty(t *testing.T) {
	t.Parallel()
	clk := func() time.Time { return time.Date(2026, 5, 9, 1, 2, 3, 0, time.UTC) }
	out := FillTimestamp(Event{}, clk)
	if !strings.HasPrefix(out.Timestamp, "2026-05-09T01:02:03") {
		t.Errorf("Timestamp = %q want prefix 2026-05-09T01:02:03", out.Timestamp)
	}
}

func TestMarshalEvent_Stable(t *testing.T) {
	t.Parallel()
	e := Event{
		Type: TypeSwitchover, ContinuumName: "ns/x", FromPrimary: "fsn", ToPrimary: "hel",
		Step: "3/7", Timestamp: "ts",
	}
	b1, _ := MarshalEvent(e)
	b2, _ := MarshalEvent(e)
	if string(b1) != string(b2) {
		t.Fatalf("MarshalEvent not stable")
	}
	if !strings.Contains(string(b1), `"type":"continuum-switchover"`) {
		t.Fatalf("Marshal output missing type: %s", b1)
	}
}

func TestNewJetStreamPublisher_RequiresURL(t *testing.T) {
	t.Parallel()
	if _, err := NewJetStreamPublisher(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty URL")
	}
}

func TestJetStreamPublisher_NilGuard(t *testing.T) {
	t.Parallel()
	var p *JetStreamPublisher
	if err := p.Publish(context.Background(), Event{Type: TypeSwitchover, ContinuumName: "ns/x"}); err == nil {
		t.Fatal("expected error on nil publisher")
	}
}
