package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// recorder is a Publisher that keeps what it gets, or fails.
type recorder struct {
	events []Event
	err    error
}

func (r *recorder) Publish(_ context.Context, ev Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	return nil
}

// logLines returns the lines a JSON logger wrote, decoded.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestBusStampsAnIdentifier(t *testing.T) {
	pub := &recorder{}
	b := NewBus(BusConfig{RingCapacity: 4, Publisher: pub})
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated})
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated, ID: "given"})
	got := b.List("", nil, 10)
	if len(got) != 2 || got[0].ID != "given" {
		t.Fatalf("the ring holds %+v", got)
	}
	if len(got[1].ID) != 26 {
		t.Errorf("the Bus gave the id %q, want a ULID of 26 characters", got[1].ID)
	}
	if len(pub.events) != 2 || pub.events[0].ID != got[1].ID || pub.events[1].ID != "given" {
		t.Errorf("the publisher got other ids than the ring: %+v", pub.events)
	}
}

func TestEventJSONCarriesIDAndPhase(t *testing.T) {
	plain, _ := json.Marshal(Event{AuditType: "x", ID: "01A"})
	if !strings.Contains(string(plain), `"id":"01A"`) || strings.Contains(string(plain), "phase") {
		t.Errorf("an event without a phase is %s", plain)
	}
	phased, _ := json.Marshal(Event{AuditType: "x", ID: "01A", Phase: PhaseIntent})
	if !strings.Contains(string(phased), `"phase":"intent"`) {
		t.Errorf("an event with a phase is %s", phased)
	}
	if PhaseIntent != "intent" || PhaseResult != "result" {
		t.Errorf("phases are %q and %q", PhaseIntent, PhaseResult)
	}
}

func TestBusTransport(t *testing.T) {
	if got := NewBus(BusConfig{}).Transport(); got != "in-process" {
		t.Errorf("without a publisher the transport is %q, want in-process", got)
	}
	if got := NewBus(BusConfig{Publisher: &recorder{}}).Transport(); got != "jetstream:CATALYST_ORG" {
		t.Errorf("with a publisher the transport is %q, want jetstream:CATALYST_ORG", got)
	}
}

func TestPublishStrictRecordsAfterTheAcknowledgement(t *testing.T) {
	pub := &recorder{}
	b := NewBus(BusConfig{Publisher: pub})
	ch, unsub := b.Subscribe("", nil)
	defer unsub()
	if err := b.PublishStrict(context.Background(), Event{AuditType: "break-glass", Phase: PhaseIntent}); err != nil {
		t.Fatal(err)
	}
	got := b.List("", nil, 10)
	if len(got) != 1 || got[0].Phase != "intent" || got[0].Result != "ok" || got[0].Timestamp.IsZero() || len(got[0].ID) != 26 {
		t.Fatalf("the ring holds %+v", got)
	}
	if len(pub.events) != 1 || pub.events[0].ID != got[0].ID {
		t.Errorf("the publisher got %+v", pub.events)
	}
	select {
	case ev := <-ch:
		if ev.ID != got[0].ID {
			t.Errorf("the subscriber got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("the subscriber got no event")
	}
}

func TestPublishStrictReturnsThePublishersError(t *testing.T) {
	down := errors.New("stream down")
	b := NewBus(BusConfig{Publisher: &recorder{err: down}})
	err := b.PublishStrict(context.Background(), Event{AuditType: "break-glass"})
	if !errors.Is(err, down) {
		t.Fatalf("PublishStrict returned %v, want the publisher's error", err)
	}
	if got := b.List("", nil, 10); len(got) != 0 {
		t.Errorf("an event that was not recorded is in the ring: %+v", got)
	}
}

func TestPublishStrictNeedsAPublisher(t *testing.T) {
	b := NewBus(BusConfig{})
	if err := b.PublishStrict(context.Background(), Event{AuditType: "break-glass"}); !errors.Is(err, ErrNoPublisher) {
		t.Fatalf("PublishStrict without a publisher returned %v, want ErrNoPublisher", err)
	}
	if got := b.List("", nil, 10); len(got) != 0 {
		t.Errorf("the ring holds %+v", got)
	}
}

func TestPublishKeepsTheEventWhenThePublisherFails(t *testing.T) {
	var buf bytes.Buffer
	b := NewBus(BusConfig{Publisher: &recorder{err: errors.New("stream down")}, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated, Actor: "alice"})
	if got := b.List("", nil, 10); len(got) != 1 || got[0].Actor != "alice" {
		t.Fatalf("the ring holds %+v", got)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["audit"] != true || lines[0]["level"] != "ERROR" || lines[0]["publishError"] != "stream down" {
		t.Errorf("the log holds %v", lines)
	}
}

func TestEveryEventIsOneLogLine(t *testing.T) {
	var buf bytes.Buffer
	b := NewBus(BusConfig{Publisher: &recorder{}, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated, Actor: "alice"})
	if err := b.PublishStrict(context.Background(), Event{AuditType: "break-glass", Phase: PhaseResult}); err != nil {
		t.Fatal(err)
	}
	lines := logLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("the log holds %d lines, want 2: %s", len(lines), buf.String())
	}
	ring := b.List("", nil, 10) // newest first
	for i, line := range lines {
		ev, _ := line["event"].(map[string]any)
		if line["audit"] != true || line["level"] != "INFO" || ev == nil || ev["id"] != ring[1-i].ID || ev["auditType"] != ring[1-i].AuditType {
			t.Errorf("log line %d is %v", i, line)
		}
		if _, has := line["publishError"]; has {
			t.Errorf("log line %d has a publishError: %v", i, line)
		}
	}
	// PublishStrict writes the line when it fails too, and no line without a logger.
	buf.Reset()
	failing := NewBus(BusConfig{Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	_ = failing.PublishStrict(context.Background(), Event{AuditType: "break-glass"})
	if lines := logLines(t, &buf); len(lines) != 1 || lines[0]["audit"] != true || lines[0]["publishError"] != ErrNoPublisher.Error() {
		t.Errorf("the log of a failed PublishStrict holds %v", lines)
	}
	NewBus(BusConfig{}).Publish(context.Background(), Event{AuditType: "x"}) // must not panic
}

func TestLoadFillsTheRingWithoutPublishing(t *testing.T) {
	pub := &recorder{}
	var buf bytes.Buffer
	b := NewBus(BusConfig{RingCapacity: 3, Publisher: pub, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	ch, unsub := b.Subscribe("", nil)
	defer unsub()
	base := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	events := []Event{{ID: "skipped: no audit type"}}
	for i, id := range []string{"a", "b", "c", "d"} {
		events = append(events, Event{ID: id, AuditType: AuditTypeRBACGrantCreated, SovereignID: "dep", Timestamp: base.Add(time.Duration(i) * time.Minute)})
	}
	if n := b.Load(events); n != 4 {
		t.Fatalf("Load appended %d events, want 4", n)
	}
	got := b.List("dep", IsRBACAuditType, 10)
	if len(got) != 3 || got[0].ID != "d" || got[1].ID != "c" || got[2].ID != "b" {
		t.Errorf("a ring of 3 holds %+v, want d, c, b", got)
	}
	if len(pub.events) != 0 || buf.Len() != 0 {
		t.Errorf("Load published %d events and logged %q", len(pub.events), buf.String())
	}
	select {
	case ev := <-ch:
		t.Errorf("Load fanned out %+v", ev)
	default:
	}
	// An event published after the load is the newest.
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantDeleted, SovereignID: "dep", Timestamp: base.Add(time.Hour)})
	if got := b.List("dep", nil, 1); len(got) != 1 || got[0].AuditType != AuditTypeRBACGrantDeleted {
		t.Errorf("the newest event is %+v", got)
	}
}
