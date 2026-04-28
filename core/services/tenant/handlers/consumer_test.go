package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
)

// These tests guard issue #73 — prior to the fix, all four callers of
// json.Unmarshal on event.Data silently discarded the error and then
// proceeded to mutate the store with zero-value identifiers. That wedged the
// console UI (ClearAppState(tenant, "") is a no-op) AND left operators blind.
//
// The fix is: log at ERROR, increment the malformed-payload counter, and
// return early BEFORE touching the store. A successful short-circuit is
// observable by (a) the counter incrementing and (b) the nil Store pointer
// not panicking (because we never call it).

func mkEvent(t *testing.T, typ string, raw json.RawMessage) *events.Event {
	t.Helper()
	return &events.Event{
		ID:       "evt-" + typ,
		Type:     typ,
		TenantID: "tenant-test",
		Data:     raw,
	}
}

// malformed payload: not an object, schema drifted completely away.
var malformedData = json.RawMessage(`"just a string, not an appEventPayload"`)

// TestOnAppReady_MalformedPayload_DoesNotCallStore: with a nil store, if the
// malformed payload were not short-circuited, we'd panic. The test passing
// proves the fix is in place.
func TestOnAppReady_MalformedPayload_DoesNotCallStore(t *testing.T) {
	c := &ConsumerHandler{Store: nil}
	before := c.MalformedPayloadCount()
	if err := c.onAppReady(context.Background(), mkEvent(t, "provision.app_ready", malformedData)); err != nil {
		t.Fatalf("expected nil error (we commit past poison pills), got %v", err)
	}
	if got := c.MalformedPayloadCount(); got != before+1 {
		t.Fatalf("counter did not increment: before=%d after=%d", before, got)
	}
}

func TestOnAppRemoved_MalformedPayload_DoesNotCallStore(t *testing.T) {
	c := &ConsumerHandler{Store: nil}
	before := c.MalformedPayloadCount()
	if err := c.onAppRemoved(context.Background(), mkEvent(t, "provision.app_removed", malformedData)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.MalformedPayloadCount(); got != before+1 {
		t.Fatalf("counter did not increment: before=%d after=%d", before, got)
	}
}

func TestOnAppFailed_MalformedPayload_DoesNotCallStore(t *testing.T) {
	c := &ConsumerHandler{Store: nil}
	before := c.MalformedPayloadCount()
	if err := c.onAppFailed(context.Background(), mkEvent(t, "provision.app_failed", malformedData)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.MalformedPayloadCount(); got != before+1 {
		t.Fatalf("counter did not increment: before=%d after=%d", before, got)
	}
}

// TestOnAppReady_EmptyTenantID_NoOp: the early-return path for missing tenant
// id is preserved by the refactor.
func TestOnAppReady_EmptyTenantID_NoOp(t *testing.T) {
	c := &ConsumerHandler{Store: nil}
	evt := &events.Event{Type: "provision.app_ready", Data: json.RawMessage(`{}`)}
	if err := c.onAppReady(context.Background(), evt); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestUnmarshalPayload_Success: well-formed payload must decode cleanly and
// must NOT increment the counter.
func TestUnmarshalPayload_Success(t *testing.T) {
	c := &ConsumerHandler{}
	var p appEventPayload
	evt := mkEvent(t, "provision.app_ready", json.RawMessage(`{"app_slug":"ghost","app_id":"id-1"}`))
	before := c.MalformedPayloadCount()
	if !c.unmarshalPayload(evt, "test", &p) {
		t.Fatalf("expected unmarshalPayload to succeed")
	}
	if p.AppSlug != "ghost" || p.AppID != "id-1" {
		t.Fatalf("decoded fields mismatch: %+v", p)
	}
	if got := c.MalformedPayloadCount(); got != before {
		t.Fatalf("counter should not increment on success: before=%d after=%d", before, got)
	}
}

// TestPayloadHead: sanity on truncation.
func TestPayloadHead(t *testing.T) {
	short := payloadHead([]byte("abc"), 10)
	if short != "abc" {
		t.Fatalf("short payload should pass through, got %q", short)
	}
	long := payloadHead([]byte("0123456789abcdef"), 4)
	if long != "0123…" {
		t.Fatalf("long payload not truncated correctly, got %q", long)
	}
}
