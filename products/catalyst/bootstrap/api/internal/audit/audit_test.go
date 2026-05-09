package audit

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRBACAuditType(t *testing.T) {
	cases := map[string]bool{
		AuditTypeRBACGrantCreated: true,
		AuditTypeRBACGrantUpdated: true,
		AuditTypeRBACGrantDeleted: true,
		AuditTypeRBACTierChanged:  true,
		"continuum-switchover":    false,
		"":                        false,
		"rbac-other":              false,
	}
	for in, want := range cases {
		if got := IsRBACAuditType(in); got != want {
			t.Errorf("IsRBACAuditType(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestBus_Publish_AppendsAndFanout(t *testing.T) {
	b := NewBus(BusConfig{RingCapacity: 10})
	ch, unsub := b.Subscribe("", nil)
	defer unsub()

	ev := Event{AuditType: AuditTypeRBACGrantCreated, SovereignID: "sov1", Actor: "alice"}
	b.Publish(context.Background(), ev)

	select {
	case got := <-ch:
		if got.AuditType != AuditTypeRBACGrantCreated {
			t.Errorf("got AuditType=%q; want %q", got.AuditType, AuditTypeRBACGrantCreated)
		}
		if got.Result != "ok" {
			t.Errorf("default Result want ok; got %q", got.Result)
		}
		if got.Timestamp.IsZero() {
			t.Errorf("Timestamp must be set on Publish")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the event")
	}

	list := b.List("sov1", nil, 10)
	if len(list) != 1 {
		t.Fatalf("ring size want 1; got %d", len(list))
	}
	if list[0].Actor != "alice" {
		t.Errorf("actor want alice; got %q", list[0].Actor)
	}
}

func TestBus_List_FilterAndOrder(t *testing.T) {
	b := NewBus(BusConfig{RingCapacity: 10})

	// Pre-set timestamps so ordering is deterministic.
	now := time.Now().UTC()
	for i, typ := range []string{
		AuditTypeRBACGrantCreated,
		"continuum-switchover",
		AuditTypeRBACTierChanged,
	} {
		b.Publish(context.Background(), Event{
			AuditType:   typ,
			SovereignID: "sov1",
			Timestamp:   now.Add(time.Duration(i) * time.Second),
		})
	}
	// One event under a different SovereignID.
	b.Publish(context.Background(), Event{
		AuditType:   AuditTypeRBACGrantDeleted,
		SovereignID: "sov2",
	})

	rbac := b.List("sov1", IsRBACAuditType, 10)
	if len(rbac) != 2 {
		t.Fatalf("rbac filter want 2 events; got %d", len(rbac))
	}
	// Newest-first: tier-changed (i=2) before grant-created (i=0).
	if rbac[0].AuditType != AuditTypeRBACTierChanged {
		t.Errorf("newest-first order broken: %q", rbac[0].AuditType)
	}
}

func TestBus_RingEvictsOldest(t *testing.T) {
	b := NewBus(BusConfig{RingCapacity: 3})
	for i := 0; i < 5; i++ {
		b.Publish(context.Background(), Event{
			AuditType:   AuditTypeRBACGrantCreated,
			SovereignID: "sov",
			Detail:      "ev-" + string(rune('0'+i)),
		})
	}
	out := b.List("sov", nil, 10)
	if len(out) != 3 {
		t.Fatalf("ring should evict to capacity 3; got %d", len(out))
	}
	if out[0].Detail != "ev-4" {
		t.Errorf("newest first; want ev-4 got %q", out[0].Detail)
	}
	if out[2].Detail != "ev-2" {
		t.Errorf("oldest third; want ev-2 got %q", out[2].Detail)
	}
}

func TestBus_Subscribe_FilterByAuditType(t *testing.T) {
	b := NewBus(BusConfig{RingCapacity: 10})
	ch, unsub := b.Subscribe("sov", IsRBACAuditType)
	defer unsub()

	b.Publish(context.Background(), Event{AuditType: "continuum-foo", SovereignID: "sov"})
	b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated, SovereignID: "sov"})

	select {
	case got := <-ch:
		if got.AuditType != AuditTypeRBACGrantCreated {
			t.Errorf("filter let through non-RBAC: %q", got.AuditType)
		}
	case <-time.After(time.Second):
		t.Fatal("RBAC event never delivered")
	}
}

func TestBus_PublisherForwarded(t *testing.T) {
	rec := &recorderPublisher{}
	b := NewBus(BusConfig{RingCapacity: 5, Publisher: rec})

	for i := 0; i < 3; i++ {
		b.Publish(context.Background(), Event{
			AuditType:   AuditTypeRBACGrantCreated,
			SovereignID: "sov",
		})
	}
	// Wait briefly for forwarder.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&rec.count) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&rec.count); got != 3 {
		t.Errorf("publisher Publish() invocations want 3; got %d", got)
	}
}

func TestMarshalJSONLine_StripsNewlines(t *testing.T) {
	ev := Event{
		AuditType: AuditTypeRBACGrantCreated,
		Detail:    "line1\nline2",
		Timestamp: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
	}
	b, err := MarshalJSONLine(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "\n") {
		t.Errorf("JSON line must not contain newline: %s", b)
	}
	// Must still be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Errorf("parse roundtrip: %v", err)
	}
}

func TestBus_DropsOnSlowSubscriber(t *testing.T) {
	// Subscriber buffer is 64; publish 200 without draining ⇒ no panic, no block.
	b := NewBus(BusConfig{})
	ch, unsub := b.Subscribe("", nil)
	defer unsub()

	for i := 0; i < 200; i++ {
		b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated})
	}
	// Drain a few; expect we get something but not necessarily 200.
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	got := 0
loop:
	for {
		select {
		case <-ch:
			got++
		case <-deadline.C:
			break loop
		}
		if got >= 64 {
			break
		}
	}
	if got == 0 {
		t.Errorf("no events delivered at all; SSE fan-out broken")
	}
}

func TestBus_ConcurrentPublishSafe(t *testing.T) {
	b := NewBus(BusConfig{RingCapacity: 256})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Publish(context.Background(), Event{AuditType: AuditTypeRBACGrantCreated, SovereignID: "sov"})
			}
		}()
	}
	wg.Wait()
	out := b.List("sov", nil, 1000)
	if len(out) == 0 {
		t.Errorf("expected events after concurrent publish; got 0")
	}
}

// ── helpers ──────────────────────────────────────────────────────────

type recorderPublisher struct {
	count int32
}

func (r *recorderPublisher) Publish(_ context.Context, _ Event) error {
	atomic.AddInt32(&r.count, 1)
	return nil
}
