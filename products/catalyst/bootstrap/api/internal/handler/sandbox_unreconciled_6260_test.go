package handler

// sandbox_unreconciled_6260_test.go — #6260.
//
// The subject under test is the WIRE PAYLOAD of the two read endpoints
// the Sandbox front end polls, not the projection helper. Every case
// below drives `GET /api/v1/sandbox/sessions` and
// `GET /api/v1/sandbox/sessions/{id}` through the real chi router via
// callSandbox, because the defect being fixed was only ever observable
// there: `unstructuredToSandboxItem` was returning a status the handler
// then served to a browser that spun on it forever.
//
// Each assertion is paired with a CONTROL that shares the property the
// verdict keys on, so a change that fires too broadly cannot pass:
//
//	AgedEmptyStatus     — aged AND empty  → failed + NoReconciler
//	  control ReadyIsUntouched      — aged, NOT empty   → running, no condition
//	  control YoungIsPending        — empty, NOT aged   → pending, no condition
//	  control NoTimestampIsPending  — empty, age UNKNOWN → pending, no condition
//
// The last control is the one that matters most: an unmeasurable age
// must never be read as a stale one.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mkAgedSandboxCR builds a Sandbox CR whose creationTimestamp is
// exactly `age` in the past. Unlike mkSandboxCR it takes the age
// explicitly so a test can sit on either side of the grace window.
func mkAgedSandboxCR(name, ns, agent string, age time.Duration, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "sandbox.openova.io",
		Version: "v1",
		Kind:    "Sandbox",
	})
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-age).UTC().Truncate(time.Second)))
	_ = unstructured.SetNestedSlice(u.Object, []any{agent}, "spec", "agentCatalogue")
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

// getSandboxItem drives the real GET-by-id handler and decodes the
// wire payload.
func getSandboxItem(t *testing.T, h *Handler, id string) sandboxItem {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/"+id, nil)
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")
	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s", id, rec.Code, rec.Body.String())
	}
	var item sandboxItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	return item
}

// findSandboxCondition returns the first condition of the given type, or
// false when absent.
func findSandboxCondition(item sandboxItem, condType string) (sandboxCondition, bool) {
	for _, c := range item.Conditions {
		if c.Type == condType {
			return c, true
		}
	}
	return sandboxCondition{}, false
}

// TestSandboxSessions_AgedEmptyStatusIsNotPending — THE defect.
//
// A Sandbox CR that has carried an empty `.status` for half an hour is
// not "about to be observed". Since 9ed9619e1 retired bootstrap-kit
// slot 19a there is no reconciler installed to observe it at all, so
// the wire payload must say so rather than keep the FE spinner alive.
func TestSandboxSessions_AgedEmptyStatusIsNotPending(t *testing.T) {
	cr := mkAgedSandboxCR("claude-code-aged", "acme", "claude-code", 30*time.Minute, nil)
	h := newSandboxHandler(t, cr)

	item := getSandboxItem(t, h, "claude-code-aged")

	if item.Status != "failed" {
		t.Errorf("Status = %q, want failed — an empty .status 30m after create is terminal, not pending", item.Status)
	}
	// The raw phase stays verbatim: the projection reports what it
	// concluded, it does not fabricate a controller write.
	if item.Phase != "" {
		t.Errorf("Phase = %q, want \"\" — the CR genuinely has no .status.phase", item.Phase)
	}

	c, ok := findSandboxCondition(item, sandboxReconciledConditionType)
	if !ok {
		t.Fatalf("no %q condition on the wire payload; conditions = %+v", sandboxReconciledConditionType, item.Conditions)
	}
	// SandboxProvisioningPanel.tsx:201-208 renders a condition only
	// when status is literally "False" AND reason is non-empty. Assert
	// the exact shape that contract needs, not merely "a condition".
	if c.Status != "False" {
		t.Errorf("condition Status = %q, want False — the FE's isSandboxFailed keys on it", c.Status)
	}
	if c.Reason != sandboxNoReconcilerReason {
		t.Errorf("condition Reason = %q, want %q — an empty reason is filtered out by the FE", c.Reason, sandboxNoReconcilerReason)
	}
	if c.Message == "" {
		t.Error("condition Message is empty — the operator is left with a red pill and no explanation")
	}
}

// TestSandboxSessions_AgedReadyIsUntouched — CONTROL sharing the AGE
// property.
//
// This CR is just as old as the one above. If the verdict keyed on age
// alone it would be marked failed here too, which would break every
// long-lived healthy Sandbox on a Sovereign that DOES run a
// reconciler. A controller-written phase is the truth and wins.
func TestSandboxSessions_AgedReadyIsUntouched(t *testing.T) {
	cr := mkAgedSandboxCR("claude-code-ready", "acme", "claude-code", 30*time.Minute, map[string]any{
		"phase": "Ready",
	})
	h := newSandboxHandler(t, cr)

	item := getSandboxItem(t, h, "claude-code-ready")

	if item.Status != "running" {
		t.Errorf("Status = %q, want running — a controller wrote Ready, age is irrelevant", item.Status)
	}
	if _, ok := findSandboxCondition(item, sandboxReconciledConditionType); ok {
		t.Errorf("unexpected %q condition on a reconciled Sandbox: %+v", sandboxReconciledConditionType, item.Conditions)
	}
}

// TestSandboxSessions_YoungEmptyStatusIsPending — CONTROL sharing the
// EMPTY-STATUS property.
//
// A CR created two seconds ago has an empty `.status` for the same
// reason it always did, and on a Sovereign with a reconciler it will
// be observed shortly. Calling that failed would make the create hot
// path report failure on every single click.
func TestSandboxSessions_YoungEmptyStatusIsPending(t *testing.T) {
	cr := mkAgedSandboxCR("claude-code-young", "acme", "claude-code", 2*time.Second, nil)
	h := newSandboxHandler(t, cr)

	item := getSandboxItem(t, h, "claude-code-young")

	if item.Status != "pending" {
		t.Errorf("Status = %q, want pending — a 2s-old CR is still inside the grace window", item.Status)
	}
	if _, ok := findSandboxCondition(item, sandboxReconciledConditionType); ok {
		t.Errorf("unexpected %q condition on a young Sandbox: %+v", sandboxReconciledConditionType, item.Conditions)
	}
}

// TestSandboxSessions_NoCreationTimestampIsPending — CONTROL for the
// UNMEASURABLE case.
//
// A CR with no creationTimestamp has an age of "unknown", and a zero
// time is the epoch — trivially older than any grace window. Reading
// that as stale would assert a fact the payload does not contain. This
// is also the shape of every hand-built fixture in
// sandbox_sessions_test.go, so the guard doubles as a regression pin
// on those.
func TestSandboxSessions_NoCreationTimestampIsPending(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{"name": "sandbox-x"},
			"status":   map[string]any{"phase": ""},
		},
	}
	item := unstructuredToSandboxItem(u)

	if item.Status != "pending" {
		t.Errorf("Status = %q, want pending — an unmeasurable age must not be reported as a stale one", item.Status)
	}
	if _, ok := findSandboxCondition(item, sandboxReconciledConditionType); ok {
		t.Errorf("unexpected %q condition on a CR with no creationTimestamp: %+v", sandboxReconciledConditionType, item.Conditions)
	}
}

// TestSandboxSessions_FutureCreationTimestampIsPending — CONTROL for
// clock skew between this process and the apiserver. A negative age is
// not evidence of anything.
func TestSandboxSessions_FutureCreationTimestampIsPending(t *testing.T) {
	cr := mkAgedSandboxCR("claude-code-skew", "acme", "claude-code", -10*time.Minute, nil)
	h := newSandboxHandler(t, cr)

	item := getSandboxItem(t, h, "claude-code-skew")

	if item.Status != "pending" {
		t.Errorf("Status = %q, want pending — a creationTimestamp in the future is skew, not staleness", item.Status)
	}
}

// TestSandboxSessions_ListAgreesWithGet — the landing page polls LIST,
// the session page polls GET. A verdict that only reached one of them
// would leave the landing grid showing a healthy-looking row next to a
// session page reporting failure.
func TestSandboxSessions_ListAgreesWithGet(t *testing.T) {
	h := newSandboxHandler(t,
		mkAgedSandboxCR("claude-code-aged", "acme", "claude-code", 30*time.Minute, nil),
		mkAgedSandboxCR("claude-code-ready", "acme", "claude-code", 30*time.Minute, map[string]any{"phase": "Ready"}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")
	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Sandboxes []sandboxItem `json:"sandboxes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode LIST body: %v", err)
	}
	if len(resp.Sandboxes) != 2 {
		t.Fatalf("LIST returned %d rows, want 2: %+v", len(resp.Sandboxes), resp.Sandboxes)
	}

	byID := map[string]sandboxItem{}
	for _, s := range resp.Sandboxes {
		byID[s.ID] = s
	}
	if got := byID["claude-code-aged"].Status; got != "failed" {
		t.Errorf("LIST claude-code-aged Status = %q, want failed", got)
	}
	if got := byID["claude-code-ready"].Status; got != "running" {
		t.Errorf("LIST claude-code-ready Status = %q, want running", got)
	}
}

// TestSandboxSessions_CreateResponseIsPending — the create hot path.
//
// POST projects the object the apiserver just returned. Whatever the
// fake sets for creationTimestamp, a just-created Sandbox must never
// come back as failed: that would put a red "cannot provision" pill on
// screen in the same paint as the click.
func TestSandboxSessions_CreateResponseIsPending(t *testing.T) {
	h := newSandboxHandler(t)

	raw, _ := json.Marshal(sandboxCreateRequest{Agent: "claude-code", Name: "fresh"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-abcdef12", "operator@acme.com", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created sandboxItem
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if created.Status != "pending" {
		t.Errorf("POST Status = %q, want pending on a just-created Sandbox", created.Status)
	}
}

// ── grace-window knob ───────────────────────────────────────────────

// TestSandboxUnreconciledAfter_EnvOverride — the window is operator
// tunable, and garbage cannot silently disable the honest verdict.
func TestSandboxUnreconciledAfter_EnvOverride(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset", "", defaultSandboxUnreconciledAfter},
		{"valid", "90s", 90 * time.Second},
		{"valid-minutes", "10m", 10 * time.Minute},
		{"unparseable", "soon", defaultSandboxUnreconciledAfter},
		{"zero", "0s", defaultSandboxUnreconciledAfter},
		{"negative", "-5m", defaultSandboxUnreconciledAfter},
		{"whitespace", "   ", defaultSandboxUnreconciledAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATALYST_SANDBOX_UNRECONCILED_AFTER", tc.env)
			if got := sandboxUnreconciledAfter(); got != tc.want {
				t.Errorf("env=%q → %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestSandboxSessions_GraceWindowIsHonouredEndToEnd — the knob must
// reach the wire payload, not just the helper. A CR aged 30s reads
// pending under the default window and failed under a 10s one, with
// nothing else about the CR changing.
func TestSandboxSessions_GraceWindowIsHonouredEndToEnd(t *testing.T) {
	t.Run("default-window-30s-old-is-pending", func(t *testing.T) {
		h := newSandboxHandler(t, mkAgedSandboxCR("cr", "acme", "claude-code", 30*time.Second, nil))
		if got := getSandboxItem(t, h, "cr").Status; got != "pending" {
			t.Errorf("Status = %q, want pending under the 2m default", got)
		}
	})
	t.Run("tightened-window-30s-old-is-failed", func(t *testing.T) {
		t.Setenv("CATALYST_SANDBOX_UNRECONCILED_AFTER", "10s")
		h := newSandboxHandler(t, mkAgedSandboxCR("cr", "acme", "claude-code", 30*time.Second, nil))
		if got := getSandboxItem(t, h, "cr").Status; got != "failed" {
			t.Errorf("Status = %q, want failed under a 10s window", got)
		}
	})
}
