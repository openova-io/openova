// Tests for the catalyst-api auto-fire of the handover JWT mint after the
// Phase-1 watch terminates with OutcomeReady (issues #764 + #768).
//
// What this file proves:
//
//  1. markPhase1Done's terminal Ready transition triggers fireHandover
//     when h.handoverSigner is wired — Result.HandoverFiredAt and
//     Result.HandoverURL are populated, and the typed
//     `handover-ready` SSE event lands in the durable buffer.
//  2. A non-Ready terminal outcome (failed, kubeconfig-missing, …)
//     does NOT fire the handover — the URL + timestamp stay nil.
//  3. The auto-fire is idempotent: a second markPhase1Done call (e.g.
//     informer reattach + re-emit) does NOT mint a second JWT or emit
//     a duplicate SSE event.
//  4. fireHandover is a no-op when h.handoverSigner is nil — the
//     existing tests that build Handler{} without a Signer continue
//     to flip Status to "ready" without crashing.
//  5. StreamLogs renders a buffered handover-ready event as the typed
//     SSE shape `event: handover-ready, data: {handoverURL, expiresAt}`
//     — the wizard's addEventListener('handover-ready', …) consumes
//     this contract.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// loadTestSigner returns a real handoverjwt.Signer wired with a freshly
// generated RSA-2048 keypair. Each test gets its own pair — the
// generation cost (~50 ms on a developer laptop, sub-millisecond after
// process warm-up) is acceptable for a per-test fixture and keeps the
// keys off disk.
func loadTestSigner(t *testing.T) *handoverjwt.Signer {
	t.Helper()
	privPEM, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	signer, err := handoverjwt.New(privPEM, "https://console.test", time.Minute*5)
	if err != nil {
		t.Fatalf("handoverjwt.New: %v", err)
	}
	return signer
}

// TestMarkPhase1Done_AutoFiresHandoverOnReady proves that when Phase-1
// terminates with OutcomeReady, markPhase1Done invokes fireHandover,
// which:
//   - mints a JWT via the wired Signer,
//   - persists Result.HandoverFiredAt + Result.HandoverURL,
//   - emits a typed `handover-ready` event onto the durable buffer
//     with a JSON payload containing handoverURL + expiresAt.
//
// This is the load-bearing assertion for issue #768's DoD.
func TestMarkPhase1Done_AutoFiresHandoverOnReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-handover-ready",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-test.example.com",
			OrgEmail:      "operator@test.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-test.example.com",
		},
		OwnerEmail: "operator@test.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{
		"cilium":       helmwatch.StateInstalled,
		"cert-manager": helmwatch.StateInstalled,
		"flux":         helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want %q", dep.Status, "ready")
	}
	if dep.Result == nil {
		t.Fatalf("Result is nil")
	}
	if dep.Result.HandoverFiredAt == nil {
		t.Errorf("HandoverFiredAt was not set after Ready transition")
	}
	if dep.Result.HandoverURL == "" {
		t.Errorf("HandoverURL was not set after Ready transition")
	}
	if !strings.HasPrefix(dep.Result.HandoverURL, "https://console.otech-test.example.com/auth/handover?token=") {
		t.Errorf("HandoverURL has unexpected shape: %q", dep.Result.HandoverURL)
	}

	// Find the handover-ready event in the durable buffer and verify
	// the payload shape (issue #768 wire contract).
	var ready *provisioner.Event
	for i := range dep.eventsBuf {
		if dep.eventsBuf[i].Phase == PhaseHandoverReady {
			ready = &dep.eventsBuf[i]
			break
		}
	}
	if ready == nil {
		t.Fatalf("no handover-ready event in eventsBuf; got=%+v", dep.eventsBuf)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(ready.Message), &payload); err != nil {
		t.Fatalf("decode handover-ready payload %q: %v", ready.Message, err)
	}
	if payload["handoverURL"] != dep.Result.HandoverURL {
		t.Errorf("handover-ready payload handoverURL = %q, want %q",
			payload["handoverURL"], dep.Result.HandoverURL)
	}
	if payload["expiresAt"] == "" {
		t.Errorf("handover-ready payload expiresAt is empty")
	}
	if _, err := time.Parse(time.RFC3339, payload["expiresAt"]); err != nil {
		t.Errorf("handover-ready payload expiresAt %q is not RFC3339: %v",
			payload["expiresAt"], err)
	}
}

// TestMarkPhase1Done_DoesNotFireHandoverOnFailure proves a failed
// Phase-1 outcome does NOT mint a handover JWT. The wizard renders
// the FailureCard + retry/wipe affordances; the operator never sees
// a redirect button on a failed deployment.
func TestMarkPhase1Done_DoesNotFireHandoverOnFailure(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-handover-failed",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-fail.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-fail.example.com",
		},
		OwnerEmail: "operator@fail.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateFailed,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeFailed)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want %q", dep.Status, "failed")
	}
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt unexpectedly set on failed terminal outcome")
	}
	if dep.Result.HandoverURL != "" {
		t.Errorf("HandoverURL unexpectedly set on failed terminal outcome: %q",
			dep.Result.HandoverURL)
	}
	for _, ev := range dep.eventsBuf {
		if ev.Phase == PhaseHandoverReady {
			t.Errorf("handover-ready event leaked on failed terminal outcome: %+v", ev)
		}
	}
}

// TestFireHandover_IdempotentOnDoubleFire proves a second invocation
// (e.g. an informer reattach causing markPhase1Done to fire twice) is
// a no-op: HandoverFiredAt + HandoverURL are unchanged and no second
// SSE event lands in the buffer.
func TestFireHandover_IdempotentOnDoubleFire(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-handover-double",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-double.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-double.example.com",
		},
		OwnerEmail: "operator@double.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	h.fireHandover(dep)
	dep.mu.Lock()
	firstFiredAt := dep.Result.HandoverFiredAt
	firstURL := dep.Result.HandoverURL
	firstEventCount := 0
	for _, ev := range dep.eventsBuf {
		if ev.Phase == PhaseHandoverReady {
			firstEventCount++
		}
	}
	dep.mu.Unlock()

	if firstFiredAt == nil || firstURL == "" {
		t.Fatalf("first fireHandover did not populate URL/timestamp; URL=%q firedAt=%v",
			firstURL, firstFiredAt)
	}
	if firstEventCount != 1 {
		t.Fatalf("first fireHandover should have emitted 1 handover-ready event; got %d",
			firstEventCount)
	}

	// Second invocation — must be a no-op.
	h.fireHandover(dep)
	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result.HandoverFiredAt != firstFiredAt {
		t.Errorf("second fireHandover replaced HandoverFiredAt; first=%v second=%v",
			firstFiredAt, dep.Result.HandoverFiredAt)
	}
	if dep.Result.HandoverURL != firstURL {
		t.Errorf("second fireHandover replaced HandoverURL; first=%q second=%q",
			firstURL, dep.Result.HandoverURL)
	}
	secondEventCount := 0
	for _, ev := range dep.eventsBuf {
		if ev.Phase == PhaseHandoverReady {
			secondEventCount++
		}
	}
	if secondEventCount != 1 {
		t.Errorf("second fireHandover emitted a duplicate handover-ready event; total=%d",
			secondEventCount)
	}
}

// TestFireHandover_NoSignerIsNoOp proves that a Handler with no
// handoverSigner wired (the test-only / Sovereign-side path) does
// NOT crash on the auto-fire and silently leaves the deployment in
// the legacy "status=ready, no redirect URL" state. The wizard
// falls back to the manual mint-handover-token endpoint via the
// AdminPage button (issue #605) — this branch keeps existing tests
// that build a Handler{} without a Signer working unchanged.
func TestFireHandover_NoSignerIsNoOp(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// h.handoverSigner intentionally left nil.

	dep := &Deployment{
		ID:        "phase1-handover-no-signer",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-nosigner.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-nosigner.example.com",
		},
	}
	h.deployments.Store(dep.ID, dep)

	// Should not panic.
	h.fireHandover(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt unexpectedly set without a Signer")
	}
	if dep.Result.HandoverURL != "" {
		t.Errorf("HandoverURL unexpectedly set without a Signer: %q",
			dep.Result.HandoverURL)
	}
}

// TestStreamLogs_HandoverReadyEventTypedSSE proves the StreamLogs
// rendering of a buffered handover-ready event uses the typed SSE
// `event: handover-ready` shape with the JSON payload as `data:`.
// This is the wire contract the wizard's
// EventSource.addEventListener('handover-ready', …) depends on.
//
// We seed a Deployment that already has a handover-ready event in
// its durable buffer + closed channels (so StreamLogs takes the
// replay-then-done path), then assert the SSE bytes contain the
// expected `event: handover-ready` line + a parseable JSON payload.
func TestStreamLogs_HandoverReadyEventTypedSSE(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	closedCh := make(chan provisioner.Event)
	closedDone := make(chan struct{})
	close(closedCh)
	close(closedDone)

	payload := `{"handoverURL":"https://console.otech-stream.example.com/auth/handover?token=AAA.BBB.CCC","expiresAt":"2026-05-04T15:00:00Z"}`
	dep := &Deployment{
		ID:        "phase1-stream-handover",
		Status:    "ready",
		StartedAt: time.Now(),
		Request: provisioner.Request{
			SovereignFQDN: "otech-stream.example.com",
		},
		eventsCh: closedCh,
		done:     closedDone,
		eventsBuf: []provisioner.Event{
			{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   PhaseHandoverReady,
				Level:   "info",
				Message: payload,
			},
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+dep.ID+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", dep.ID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	h.StreamLogs(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "event: handover-ready\n") {
		t.Errorf("StreamLogs body missing typed `event: handover-ready` line; got:\n%s", body)
	}
	if !strings.Contains(body, "data: "+payload) {
		t.Errorf("StreamLogs body missing handover-ready data payload `%s`; got:\n%s", payload, body)
	}
	if !strings.Contains(body, "event: done\n") {
		t.Errorf("StreamLogs body missing terminal `event: done`; got:\n%s", body)
	}
}

// TestGetDeployment_LiftsHandoverFieldsToTopLevel proves the State()
// snapshot lifts HandoverURL + HandoverFiredAt to the top level of
// /deployments/{id} JSON so the wizard's GET-replay path can populate
// `handoverReady` without unwrapping `result`. This is the
// belt-and-braces fallback for the SSE-missed case in #768's spec.
func TestGetDeployment_LiftsHandoverFieldsToTopLevel(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	now := time.Now().UTC()
	dep := &Deployment{
		ID:        "phase1-state-handover",
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN:   "test.example.com",
			HandoverURL:     "https://console.test.example.com/auth/handover?token=STATE.JWT.TOKEN",
			HandoverFiredAt: &now,
		},
	}
	close(dep.eventsCh)
	close(dep.done)
	h.deployments.Store(dep.ID, dep)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+dep.ID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", dep.ID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	h.GetDeployment(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["handoverURL"] != dep.Result.HandoverURL {
		t.Errorf("handoverURL at top level = %v, want %q",
			got["handoverURL"], dep.Result.HandoverURL)
	}
	if got["handoverFiredAt"] == nil {
		t.Errorf("handoverFiredAt missing at top level: %v", got)
	}
}

// TestMarkPhase1Done_TimeoutNeverFlipsReady is the regression lock for
// issue #3018, caught live on hw91 (2026-06-03): a watch that hit its
// WatchTimeout with zero hard-FAILED but N still-converging components
// fell through markPhase1Done's default branch and flipped Status to
// "ready" — the deployment record claimed ready at 39/54 HelmReleases
// while the console was TCP-closed. A timeout is NOT convergence;
// "ready" must be granted ONLY by an explicit OutcomeReady.
func TestMarkPhase1Done_TimeoutNeverFlipsReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "phase1-timeout-not-ready",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-test.example.com",
			OrgEmail:      "operator@test.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-test.example.com",
		},
		OwnerEmail: "operator@test.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	// hw91 shape: components observed, NONE hard-failed, several still
	// mid-install at the moment the watch budget expired.
	finalStates := map[string]string{
		"cilium":               helmwatch.StateInstalled,
		"cert-manager":         helmwatch.StateInstalled,
		"flux":                 helmwatch.StateInstalled,
		"bp-gitea":             helmwatch.StateInstalling,
		"bp-catalyst-platform": helmwatch.StateInstalling,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeTimeout)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status == "ready" {
		t.Fatalf("Status = %q after OutcomeTimeout — a timed-out watch must NEVER claim ready (issue #3018)", dep.Status)
	}
	if dep.Status != "failed" {
		t.Errorf("Status = %q, want %q (truthful timeout classification)", dep.Status, "failed")
	}
	if dep.Error == "" || !strings.Contains(dep.Error, "timed out") {
		t.Errorf("Error must carry an operator-actionable timeout diagnostic; got %q", dep.Error)
	}
	// Handover must NOT have fired.
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt set after timeout — handover must only fire on OutcomeReady")
	}
}

// TestMarkPhase1Done_UnknownOutcomeNeverFlipsReady locks the #3018
// hardening: any future outcome constant without an explicit case in
// markPhase1Done's switch must surface as a loud failure, not silently
// impersonate success through the old default-ready branch.
func TestMarkPhase1Done_UnknownOutcomeNeverFlipsReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "phase1-unknown-outcome",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request:   provisioner.Request{SovereignFQDN: "otech-test.example.com"},
		Result:    &provisioner.Result{SovereignFQDN: "otech-test.example.com"},
	}
	h.deployments.Store(dep.ID, dep)

	h.markPhase1Done(dep, map[string]string{"cilium": helmwatch.StateInstalled}, "some-future-outcome")

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status == "ready" {
		t.Fatalf("Status = %q for unknown outcome — only OutcomeReady may flip ready (issue #3018)", dep.Status)
	}
	if !strings.Contains(dep.Error, "unhandled outcome") {
		t.Errorf("Error should name the unhandled outcome; got %q", dep.Error)
	}
}
