// Tests for the issue #914 minimum-life wipe guard.
//
// Mid-provision wipe at T+25m destroyed a still-converging Sovereign
// (otech106.omani.works, 2026-05-05). The fix is a server-side
// minimum-life guard: when status is `phase1-watching` AND the
// deployment is younger than wipeMinLifeProtection (default 30m), POST
// /wipe returns 409 with retryAfterSec instead of running the
// destructive purge sequence.
//
// Operator override: `?force=true` query param. The pure decision
// function shouldRefuseWipe is the test seam — the four-input contract
// (status, startedAt, now, threshold, force) lets every branch be
// exercised without standing up a Handler.
//
// The HTTP-level 409-on-still-converging test is the integration
// proof: the guard short-circuits BEFORE any tofu / hetzner / pdm
// call, so the test runs without hitting external APIs and verifies
// the wire shape the wizard's banner reads (retryAfterSec,
// minLifeSec, hint).
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// callWipeDeployment wires a chi router so chi.URLParam("id") resolves
// to the supplied id and returns the recorded response.
func callWipeDeployment(h *Handler, id, query, bodyJSON string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/api/v1/deployments/{id}/wipe", h.WipeDeployment)
	w := httptest.NewRecorder()
	url := "/api/v1/deployments/" + id + "/wipe"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// ── shouldRefuseWipe pure-function tests ────────────────────────────

// TestShouldRefuseWipe_StillConvergingTooYoung — the headline case.
// status=phase1-watching, age below threshold, no force → REFUSE.
func TestShouldRefuseWipe_StillConvergingTooYoung(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-25 * time.Minute) // age = 25m
	threshold := 30 * time.Minute

	if !shouldRefuseWipe("phase1-watching", startedAt, now, threshold, false) {
		t.Errorf("status=phase1-watching age=25m threshold=30m force=false → expected REFUSE, got allow")
	}
}

// TestShouldRefuseWipe_StillConvergingOldEnoughAccept — the watcher
// has had time to converge or fail; refusing further wipes is no
// longer the right behaviour. Age ≥ threshold → ALLOW.
func TestShouldRefuseWipe_StillConvergingOldEnoughAccept(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-31 * time.Minute) // age = 31m
	threshold := 30 * time.Minute

	if shouldRefuseWipe("phase1-watching", startedAt, now, threshold, false) {
		t.Errorf("status=phase1-watching age=31m threshold=30m → expected ALLOW (past min-life), got refuse")
	}
}

// TestShouldRefuseWipe_FinishedAccept — status=ready (terminal). The
// guard MUST allow wipe of a finished deployment regardless of age.
func TestShouldRefuseWipe_FinishedAccept(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-1 * time.Minute) // very young
	threshold := 30 * time.Minute

	if shouldRefuseWipe("ready", startedAt, now, threshold, false) {
		t.Errorf("status=ready → expected ALLOW (terminal), got refuse")
	}
}

// TestShouldRefuseWipe_FailedAccept — status=failed. Wipe is the
// canonical recovery path for a failed deployment; refusing it would
// strand the operator.
func TestShouldRefuseWipe_FailedAccept(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-2 * time.Minute) // very young
	threshold := 30 * time.Minute

	if shouldRefuseWipe("failed", startedAt, now, threshold, false) {
		t.Errorf("status=failed → expected ALLOW (terminal), got refuse")
	}
}

// TestShouldRefuseWipe_ForceFlagAlwaysAccepts — the explicit operator
// override. Even when the deployment is still converging and young,
// ?force=true must let the wipe proceed.
func TestShouldRefuseWipe_ForceFlagAlwaysAccepts(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-5 * time.Minute) // young
	threshold := 30 * time.Minute

	if shouldRefuseWipe("phase1-watching", startedAt, now, threshold, true) {
		t.Errorf("status=phase1-watching age=5m force=true → expected ALLOW (force override), got refuse")
	}
}

// TestShouldRefuseWipe_NonConvergingStatusAccept — table-driven check
// that every non-`phase1-watching` status falls through to allow,
// even while age < threshold. The guard ONLY protects mid-converge
// deployments; everything else (pending, provisioning, tofu-applying,
// flux-bootstrapping, wiping, wiped) is a state where the operator's
// wipe intent is not in question.
func TestShouldRefuseWipe_NonConvergingStatusAccept(t *testing.T) {
	now := time.Now()
	startedAt := now.Add(-5 * time.Minute) // young
	threshold := 30 * time.Minute

	statuses := []string{
		"pending",
		"provisioning",
		"tofu-applying",
		"flux-bootstrapping",
		"ready",
		"failed",
		"wiping",
		"wiped",
		"adopted",
	}
	for _, s := range statuses {
		if shouldRefuseWipe(s, startedAt, now, threshold, false) {
			t.Errorf("status=%q young → expected ALLOW (only phase1-watching is protected), got refuse", s)
		}
	}
}

// TestShouldRefuseWipe_ZeroStartedAtAccept — legacy records persisted
// before StartedAt was stamped lack the anchor needed to compute age.
// In that shape the guard CANNOT enforce a threshold and falls
// through to allow — the operator's intent is clearer than the
// guard's heuristic.
func TestShouldRefuseWipe_ZeroStartedAtAccept(t *testing.T) {
	now := time.Now()
	threshold := 30 * time.Minute

	if shouldRefuseWipe("phase1-watching", time.Time{}, now, threshold, false) {
		t.Errorf("status=phase1-watching startedAt=zero → expected ALLOW (no anchor), got refuse")
	}
}

// TestShouldRefuseWipe_ExactlyAtThresholdAccept — boundary. Age ==
// threshold means "the protected window has just ended"; the wipe
// must proceed.
func TestShouldRefuseWipe_ExactlyAtThresholdAccept(t *testing.T) {
	now := time.Now()
	threshold := 30 * time.Minute
	startedAt := now.Add(-threshold) // age = threshold exactly

	if shouldRefuseWipe("phase1-watching", startedAt, now, threshold, false) {
		t.Errorf("status=phase1-watching age==threshold → expected ALLOW (boundary), got refuse")
	}
}

// ── compileWipeMinLifeProtection helper tests ───────────────────────

func TestCompileWipeMinLifeProtection_DefaultOnEmpty(t *testing.T) {
	if got := compileWipeMinLifeProtection(""); got != DefaultWipeMinLifeProtection {
		t.Errorf("empty input → expected %v, got %v", DefaultWipeMinLifeProtection, got)
	}
}

func TestCompileWipeMinLifeProtection_DefaultOnGarbage(t *testing.T) {
	if got := compileWipeMinLifeProtection("not-a-duration"); got != DefaultWipeMinLifeProtection {
		t.Errorf("garbage input → expected default %v, got %v", DefaultWipeMinLifeProtection, got)
	}
}

func TestCompileWipeMinLifeProtection_DefaultOnNonPositive(t *testing.T) {
	for _, raw := range []string{"0s", "-5m", "0"} {
		if got := compileWipeMinLifeProtection(raw); got != DefaultWipeMinLifeProtection {
			t.Errorf("input %q → expected default %v, got %v", raw, DefaultWipeMinLifeProtection, got)
		}
	}
}

func TestCompileWipeMinLifeProtection_ParsesValidDuration(t *testing.T) {
	if got := compileWipeMinLifeProtection("45m"); got != 45*time.Minute {
		t.Errorf("45m → expected %v, got %v", 45*time.Minute, got)
	}
	if got := compileWipeMinLifeProtection("1h"); got != 1*time.Hour {
		t.Errorf("1h → expected %v, got %v", 1*time.Hour, got)
	}
}

// ── HTTP-level integration tests ────────────────────────────────────

// TestWipeDeployment_StillConvergingReturns409 — headline integration
// test. A deployment in phase1-watching, 5 minutes old (well under the
// 30m default threshold), receives a wipe request and the handler
// returns 409 with the structured body the wizard banner reads. The
// Hetzner / tofu / PDM call paths are NOT invoked — the guard
// short-circuits BEFORE any of them.
//
// This is exactly the otech106 incident shape: external POST /wipe at
// T+24m on a Sovereign with status=phase1-watching, 28/40 components
// installed. With this guard, that wipe is refused.
func TestWipeDeployment_StillConvergingReturns409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-otech106",
		Status:    "phase1-watching",
		StartedAt: time.Now().Add(-5 * time.Minute), // 5m old, well under 30m
		Request: provisioner.Request{
			SovereignFQDN:       "otech106.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 — body=%s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v — raw=%s", err, w.Body.String())
	}

	if body["deploymentId"] != "dep-otech106" {
		t.Errorf("deploymentId=%v, want dep-otech106", body["deploymentId"])
	}
	if body["sovereignFQDN"] != "otech106.omani.works" {
		t.Errorf("sovereignFQDN=%v, want otech106.omani.works", body["sovereignFQDN"])
	}
	if body["status"] != "phase1-watching" {
		t.Errorf("status=%v, want phase1-watching", body["status"])
	}
	// retryAfterSec must be present and positive — the wizard banner
	// reads this for the countdown.
	retry, ok := body["retryAfterSec"].(float64)
	if !ok {
		t.Errorf("retryAfterSec missing or wrong type: %v", body["retryAfterSec"])
	} else if retry < 1 {
		t.Errorf("retryAfterSec=%v, want positive", retry)
	}
	if _, ok := body["minLifeSec"]; !ok {
		t.Errorf("minLifeSec missing from response — wizard banner needs it")
	}
	if _, ok := body["hint"]; !ok {
		t.Errorf("hint missing from response — operator-guidance text required")
	}

	// Critical invariant: the deployment status MUST NOT have flipped
	// to "wiping". The guard short-circuits BEFORE the single-flight
	// status flip, so the still-converging deployment continues to be
	// observed by the Phase-1 watcher.
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "phase1-watching" {
		t.Errorf("dep.Status=%q after refused wipe, want phase1-watching (guard must not mutate state)", dep.Status)
	}
}

// TestWipeDeployment_ForceFlagBypassesGuard — operator override path.
// `?force=true` skips the minimum-life guard and proceeds to the
// destructive path. We can't run the full destroy in unit-tests
// without a Hetzner mock, but we CAN verify the guard doesn't return
// 409: a non-409 response (any status) proves the guard was bypassed.
//
// In practice the destructive path will fail later (no real Hetzner
// token, no real workdir) and surface in the response body's Errors,
// but that's noise for this assertion — we're proving the guard
// allowed the flow to proceed.
//
// The test injects a near-zero wipeMinLifeProtection so even without
// force the deployment WOULD be allowed; we then verify the same
// outcome holds with force=true on a young deployment to prove the
// flag works.
func TestWipeDeployment_ForceFlagBypassesGuard(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Set protection to 30m default; deployment is 5m old so guard
	// would normally fire.
	h.wipeMinLifeProtection = 30 * time.Minute

	dep := &Deployment{
		ID:        "dep-force-young",
		Status:    "phase1-watching",
		StartedAt: time.Now().Add(-5 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-force.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "force=true", `{"hetznerToken":"fake-hetzner-token"}`)

	// The guard must NOT have returned 409 with the still-converging
	// body. The downstream destructive path may set its own status
	// codes (likely 200 with errors after tofu/hetzner stubs noop) —
	// the only thing we're proving here is "guard didn't refuse".
	if w.Code == http.StatusConflict {
		// Decode and check it isn't OUR 409 — there's also the
		// "wipe already in progress" 409 from the single-flight
		// guard, but that fires AFTER ours and only when status is
		// already "wiping" (which it isn't here).
		body := w.Body.String()
		if bytes.Contains([]byte(body), []byte("still converging")) {
			t.Errorf("force=true did NOT bypass minimum-life guard: status=409 body=%s", body)
		}
	}

	// Critical invariant: with force=true on a young still-converging
	// deployment, the handler must have moved past the guard (i.e.
	// status flipped to "wiping" by the single-flight guard, even if
	// downstream errors followed).
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status == "phase1-watching" {
		t.Errorf("dep.Status=%q after force=true wipe, expected status to have moved past the guard", dep.Status)
	}
}

// TestWipeDeployment_MissingTokenIs400 — defensive cross-check that
// the guard does NOT fire before token validation. A wipe call
// without a hetznerToken must still return 400, not 409, even on a
// still-converging deployment.
func TestWipeDeployment_MissingTokenIs400(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-no-token",
		Status:    "phase1-watching",
		StartedAt: time.Now().Add(-2 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-no-tok.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{}`) // empty body, no token

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 (missing token must surface BEFORE the min-life guard) — body=%s", w.Code, w.Body.String())
	}
}

// TestWipeDeployment_UnknownDeploymentIs404 — defensive cross-check.
// Unknown id must return 404 regardless of any guard configuration.
func TestWipeDeployment_UnknownDeploymentIs404(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	w := callWipeDeployment(h, "no-such-deployment", "", `{"hetznerToken":"x"}`)

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 — body=%s", w.Code, w.Body.String())
	}
}
