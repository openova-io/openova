// Package handler — sandbox_sessions_nats_test.go covers TBD-D35b /
// #1776: every successful Sandbox CR Create MUST publish a
// `catalyst.tenant.sandbox_requested` NATS event so the audit-trail UI
// (and cross-component consumers like sandbox-controller's bridge) can
// correlate FE-requested sandboxes with reconciler-observed CRs without
// scraping the apiserver.
//
// The tests use a recording TenantEventPublisher (recordingTenantEventPub
// below) wired via SetTenantEventPublisher. The fake captures every
// Publish call so the assertions can pin both the subject AND the
// payload shape (tenant_id, sandbox_id, requested_by, timestamp,
// spec_hash) — drift in any of these would silently break downstream
// consumers, which is exactly the failure mode the issue called out.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingTenantEventPub is a TenantEventPublisher that captures every
// Publish call. Safe for concurrent use so a handler that publishes
// from a goroutine (future variant) can still be asserted reliably.
type recordingTenantEventPub struct {
	mu       sync.Mutex
	calls    []recordedTenantPub
	returnErr error
}

type recordedTenantPub struct {
	subject string
	event   TenantEvent
}

func (r *recordingTenantEventPub) PublishTenantEvent(_ context.Context, subject string, ev TenantEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedTenantPub{subject: subject, event: ev})
	return r.returnErr
}

func (r *recordingTenantEventPub) Calls() []recordedTenantPub {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedTenantPub, len(r.calls))
	copy(out, r.calls)
	return out
}

// TestSandboxCreate_PublishesSandboxRequested — the canonical happy-path
// assertion: a successful Sandbox CR Create triggers exactly one
// publish on the canonical subject with all required fields populated.
func TestSandboxCreate_PublishesSandboxRequested(t *testing.T) {
	h := newSandboxHandler(t)
	pub := &recordingTenantEventPub{}
	h.SetTenantEventPublisher(pub)

	body := sandboxCreateRequest{
		Agent: "claude-code",
		Name:  "my-sandbox",
		Repo:  "acme/site",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	calls := pub.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 publish, got %d: %+v", len(calls), calls)
	}
	c := calls[0]

	if c.subject != SandboxRequestedSubject {
		t.Errorf("subject: want %q, got %q", SandboxRequestedSubject, c.subject)
	}
	if c.subject != "catalyst.tenant.sandbox_requested" {
		t.Errorf("subject constant drifted from canonical taxonomy: %q", c.subject)
	}

	ev := c.event
	if ev.TenantID != "acme" {
		t.Errorf("tenant_id: want %q (claims.Org), got %q", "acme", ev.TenantID)
	}
	if ev.SandboxID == "" {
		t.Errorf("sandbox_id must be populated (CR name)")
	}
	// The handler derives the CR name from the supplied Name field.
	// The exact transform is covered by deriveSandboxName tests; here
	// we only assert the publish observed the same id.
	var created sandboxItem
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if ev.SandboxID != created.ID {
		t.Errorf("sandbox_id: event=%q, response=%q (must match)", ev.SandboxID, created.ID)
	}
	if ev.RequestedBy != "operator@acme.com" {
		t.Errorf("requested_by: want %q (claims.Email), got %q", "operator@acme.com", ev.RequestedBy)
	}
	if ev.Timestamp.IsZero() {
		t.Errorf("timestamp must be set")
	}
	if ev.SpecHash == "" {
		t.Errorf("spec_hash must be set")
	}
	if len(ev.SpecHash) != 64 {
		t.Errorf("spec_hash must be hex-encoded SHA-256 (64 chars), got %d: %q",
			len(ev.SpecHash), ev.SpecHash)
	}
	// hex-only
	for _, r := range ev.SpecHash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("spec_hash contains non-hex char: %q", ev.SpecHash)
			break
		}
	}
}

// TestSandboxCreate_NoPublisher_DoesNotFail — nil-tolerant: a Sandbox
// Create on a chroot without CATALYST_NATS_URL must still 201 even
// though no publisher is wired. The publish-side is a no-op so the
// CR-create hot path survives a missing audit transport.
func TestSandboxCreate_NoPublisher_DoesNotFail(t *testing.T) {
	h := newSandboxHandler(t)
	// Intentionally NOT calling SetTenantEventPublisher — leave nil.

	body := sandboxCreateRequest{Agent: "claude-code"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST with nil publisher: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSandboxCreate_PublishError_DoesNotFailRequest — a NATS outage
// returns an error from PublishTenantEvent. The handler must log +
// continue: the CR write has already succeeded by then, so failing the
// HTTP response would leave the apiserver state and the operator's UI
// out of sync (the UI would refresh and see a CR it thinks failed).
func TestSandboxCreate_PublishError_DoesNotFailRequest(t *testing.T) {
	h := newSandboxHandler(t)
	pub := &recordingTenantEventPub{returnErr: errFakeNATSDown}
	h.SetTenantEventPublisher(pub)

	body := sandboxCreateRequest{Agent: "claude-code"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s (publish error MUST NOT fail the request)",
			rec.Code, rec.Body.String())
	}
	if len(pub.Calls()) != 1 {
		t.Fatalf("publish must still have been attempted exactly once: %d", len(pub.Calls()))
	}
}

// TestSandboxCreate_PublishUsesNamespaceWhenOrgEmpty — single-tenant
// chroot fallback: when claims.Org is empty the handler falls back to
// the resolved namespace as the tenant_id. Without this, downstream
// consumers would key by an empty string and conflate every chroot's
// Sandbox events.
func TestSandboxCreate_PublishUsesNamespaceWhenOrgEmpty(t *testing.T) {
	t.Setenv(sandboxDefaultNamespaceEnv, "single-tenant-chroot")

	h := newSandboxHandler(t)
	pub := &recordingTenantEventPub{}
	h.SetTenantEventPublisher(pub)

	body := sandboxCreateRequest{Agent: "claude-code"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	// Claims with empty Org — simulates a chroot Sovereign where the
	// IDP doesn't emit the org_id claim.
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@chroot.example", "")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	calls := pub.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(calls))
	}
	if calls[0].event.TenantID != "single-tenant-chroot" {
		t.Errorf("tenant_id fallback: want namespace %q, got %q",
			"single-tenant-chroot", calls[0].event.TenantID)
	}
}

// TestSandboxCreate_PublishUsesSubWhenEmailEmpty — operator identity
// fallback: when claims.Email is empty the handler falls back to
// claims.Sub (Keycloak subject UUID) so requested_by is never blank.
func TestSandboxCreate_PublishUsesSubWhenEmailEmpty(t *testing.T) {
	h := newSandboxHandler(t)
	pub := &recordingTenantEventPub{}
	h.SetTenantEventPublisher(pub)

	body := sandboxCreateRequest{Agent: "claude-code"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	// Empty Email, non-empty Sub.
	req = withSandboxClaims(req, "user-sub-abcdef12", "", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	calls := pub.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(calls))
	}
	if calls[0].event.RequestedBy != "user-sub-abcdef12" {
		t.Errorf("requested_by Sub fallback: want %q, got %q",
			"user-sub-abcdef12", calls[0].event.RequestedBy)
	}
}

// TestSandboxSpecHash_DeterministicAcrossMapOrder — the spec_hash MUST
// be stable across map-iteration order so two Create calls with the
// same logical spec produce the same hash. Without this, downstream
// drift-detection alerts (built on top of the audit stream) would fire
// spuriously on every event.
func TestSandboxSpecHash_DeterministicAcrossMapOrder(t *testing.T) {
	a := buildSandboxUnstructured("sb-1", "acme", "Sb 1", "claude-code", "acme/site", "operator@acme.com", "acme")
	b := buildSandboxUnstructured("sb-1", "acme", "Sb 1", "claude-code", "acme/site", "operator@acme.com", "acme")

	ha := sandboxSpecHash(a)
	hb := sandboxSpecHash(b)
	if ha == "" {
		t.Fatalf("hash a is empty (spec missing?)")
	}
	if ha != hb {
		t.Errorf("spec_hash not deterministic: a=%q b=%q", ha, hb)
	}

	// Spec change must alter the hash (otherwise drift detection breaks).
	c := buildSandboxUnstructured("sb-1", "acme", "Sb 1", "aider", "acme/site", "operator@acme.com", "acme")
	hc := sandboxSpecHash(c)
	if hc == ha {
		t.Errorf("spec_hash unchanged after agent swap (claude-code → aider): %q", hc)
	}
}

// TestSandboxRequestedSubject_MatchesIssueContract — pins the exact
// subject string the issue (#1776) calls out. A typo here would silently
// pass every other test but ship an unsubscribable event.
func TestSandboxRequestedSubject_MatchesIssueContract(t *testing.T) {
	const wanted = "catalyst.tenant.sandbox_requested"
	if SandboxRequestedSubject != wanted {
		t.Errorf("SandboxRequestedSubject = %q, want %q (per #1776)", SandboxRequestedSubject, wanted)
	}
	// Subject must be a valid NATS-style dotted token (per ADR-0001 §6:
	// `catalyst.<domain>.<event>`).
	parts := strings.Split(SandboxRequestedSubject, ".")
	if len(parts) != 3 || parts[0] != "catalyst" || parts[1] != "tenant" {
		t.Errorf("subject doesn't match catalyst.tenant.<event> taxonomy: %q", SandboxRequestedSubject)
	}
}

// ── helpers ─────────────────────────────────────────────────────────

// errFakeNATSDown is the error a fake publisher returns to simulate a
// transient NATS outage.
var errFakeNATSDown = &natsDownError{}

type natsDownError struct{}

func (e *natsDownError) Error() string { return "fake NATS down" }
