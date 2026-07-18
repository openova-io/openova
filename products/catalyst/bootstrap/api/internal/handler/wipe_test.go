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

// waitForWipeDone polls dep until the #5193 async purge goroutine has
// cleared wipeInFlight (i.e. runWipePurge has returned and stamped
// dep.lastWipeReport / dep.Error), or fails the test after timeout.
// WipeDeployment now responds 202 before the purge itself runs, so any
// test that needs to inspect the purge's outcome (report.Errors,
// final Status, etc.) must synchronize on completion first — mirrors
// the short-poll pattern already used elsewhere in this package for
// other async goroutines (e.g. cutover_test.go).
func waitForWipeDone(t *testing.T, dep *Deployment, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		inFlight := dep.wipeInFlight
		hasReport := dep.lastWipeReport != nil
		dep.mu.Unlock()
		if !inFlight && hasReport {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wipe purge did not complete within %v", timeout)
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

// ── issue #166 wire-shape + body-supplied S3 creds tests ────────────

// TestWipeRequest_DecodesObjectStorageCredsFromBody — issue #166.
// Confirms the wipeRequest struct deserialises the three new fields
// the wizard MUST POST when re-prompting the operator for S3 creds
// after a catalyst-api Pod restart. The fields are `omitempty` on
// marshal so the dev-tools network tab stays small for the
// common-case wipe-immediately-after-provision flow that doesn't need
// them.
func TestWipeRequest_DecodesObjectStorageCredsFromBody(t *testing.T) {
	raw := `{
		"hetznerToken": "fake-hetzner-token-1234567890",
		"objectStorageAccessKey": "FAKEACCESSKEY1234567",
		"objectStorageSecretKey": "FAKESECRETKEY1234567890123456789012345678",
		"objectStorageRegion": "fsn1"
	}`
	var got wipeRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.HetznerToken != "fake-hetzner-token-1234567890" {
		t.Errorf("HetznerToken=%q, want fake-hetzner-token-1234567890", got.HetznerToken)
	}
	if got.ObjectStorageAccessKey != "FAKEACCESSKEY1234567" {
		t.Errorf("ObjectStorageAccessKey=%q, want FAKEACCESSKEY1234567", got.ObjectStorageAccessKey)
	}
	if got.ObjectStorageSecretKey != "FAKESECRETKEY1234567890123456789012345678" {
		t.Errorf("ObjectStorageSecretKey=%q, want FAKESECRETKEY...", got.ObjectStorageSecretKey)
	}
	if got.ObjectStorageRegion != "fsn1" {
		t.Errorf("ObjectStorageRegion=%q, want fsn1", got.ObjectStorageRegion)
	}
}

// TestWipeRequest_OmitsEmptyObjectStorageFieldsOnMarshal — wire-shape
// hygiene. When the wizard sends a wipe WITHOUT re-prompting for S3
// creds (e.g. immediately after a successful provision), the
// outbound body must NOT carry empty objectStorageAccessKey="" /
// SecretKey="" / Region="" fields — those would muddy the dev-tools
// view and could trip a future strict-decoder. Verify the `,omitempty`
// JSON tag actually drops them.
func TestWipeRequest_OmitsEmptyObjectStorageFieldsOnMarshal(t *testing.T) {
	req := wipeRequest{HetznerToken: "tok"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, field := range []string{"objectStorageAccessKey", "objectStorageSecretKey", "objectStorageRegion"} {
		if bytes.Contains(raw, []byte(field)) {
			t.Errorf("empty %s field leaked into marshalled body: %s", field, got)
		}
	}
	// HetznerToken is NOT omitempty (it's a required field — the
	// handler returns 400 when absent), so it must always appear.
	if !bytes.Contains(raw, []byte(`"hetznerToken":"tok"`)) {
		t.Errorf("hetznerToken missing from marshalled body: %s", got)
	}
}

// TestWipeDeployment_BodyS3CredsBypassPodRestartScrub — the core
// integration proof for issue #166. A deployment in `failed` status
// whose in-memory Request has EMPTY S3 creds (simulating the
// post-Pod-restart state where on-disk records redacted them) is
// wiped with all three S3 fields supplied on the request body. The
// guard short-circuits because status is terminal, so the destructive
// path runs. We cannot assert PurgeBuckets ran without a Hetzner
// mock, but we CAN assert:
//
//   - The handler did NOT return 400 (body decode succeeded with the
//     new fields)
//   - The handler did NOT return 409 (guard didn't fire on terminal)
//   - The deployment status transitioned past `failed` (proves the
//     handler reached the destructive path, where the body-supplied
//     creds are consumed)
//
// In production with real Hetzner creds, this exact wire shape
// successfully purges the orphan buckets observed live on
// omantel.biz (10 catalyst-omantel-biz-* buckets, one per wiped
// provision back to prov #11).
func TestWipeDeployment_BodyS3CredsBypassPodRestartScrub(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-post-restart",
		Status:    "failed", // terminal — guard allows wipe regardless of age
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-restart.omani.works",
			SovereignDomainMode: "pool",
			// Critical: S3 creds are EMPTY here, simulating the
			// post-Pod-restart state. Pre-#166 this is exactly when
			// the bucket purge silently skipped.
			ObjectStorageAccessKey: "",
			ObjectStorageSecretKey: "",
			ObjectStorageRegion:    "",
		},
	}
	h.deployments.Store(dep.ID, dep)

	body := `{
		"hetznerToken": "fake-hetzner-token",
		"objectStorageAccessKey": "BODYACCESSKEY1234567",
		"objectStorageSecretKey": "BODYSECRETKEY1234567890123456789012345678",
		"objectStorageRegion": "fsn1"
	}`
	w := callWipeDeployment(h, dep.ID, "", body)

	// The handler must not have rejected the body (400) or refused via
	// the guard (409). Any other status code is acceptable here — the
	// destructive path may return 200 with errors in the body when
	// tofu/hetzner client calls fail (no real creds in unit tests),
	// but reaching it at all is the proof we need.
	if w.Code == http.StatusBadRequest {
		t.Fatalf("handler rejected body with new S3 fields: status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Code == http.StatusConflict && bytes.Contains(w.Body.Bytes(), []byte("still converging")) {
		t.Fatalf("min-life guard fired on terminal status: status=%d body=%s", w.Code, w.Body.String())
	}

	// Deployment status must have moved past `failed` (the destructive
	// path always flips it to `wiping`, then `wiped` on completion).
	dep.mu.Lock()
	finalStatus := dep.Status
	dep.mu.Unlock()
	if finalStatus == "failed" {
		t.Errorf("dep.Status=%q after wipe with body-supplied S3 creds — handler did not reach destructive path", finalStatus)
	}
}

// TestWipeDeployment_NoS3CredsAnywhereSurfacesError — issue #166
// negative path. When neither body nor in-memory Request carries S3
// creds, the purge report's Errors slice MUST surface a clear message
// telling the operator to re-prompt + retry. Pre-#166 this case
// silently logged a warn and reported "wipe complete" while a bucket
// leaked. We assert the error message names both sources so future
// readers immediately see why their bucket-purge skipped.
//
// #5193: the purge now runs asynchronously (WipeDeployment responds
// 202 immediately), so this test waits for runWipePurge to finish and
// reads the result off dep.lastWipeReport instead of the HTTP body.
//
// Test setup: terminal status (`failed`) so the guard allows the
// wipe, EMPTY S3 creds on the Deployment Request, and a body with
// HetznerToken but NO S3 fields.
func TestWipeDeployment_NoS3CredsAnywhereSurfacesError(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-no-s3-creds",
		Status:    "failed",
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-no-s3.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)

	// #5193 — the handler now acks 202 immediately; the purge (and any
	// errors it surfaces) completes asynchronously.
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}

	waitForWipeDone(t, dep, 10*time.Second)

	dep.mu.Lock()
	report := dep.lastWipeReport
	dep.mu.Unlock()
	if report == nil {
		t.Fatalf("dep.lastWipeReport is nil after purge completed")
	}
	foundS3Err := false
	for _, e := range report.Errors {
		if bytes.Contains([]byte(e), []byte("object-storage credentials not supplied on request body AND not retained on in-memory deployment record")) {
			foundS3Err = true
			break
		}
	}
	if !foundS3Err {
		t.Errorf("expected hard error naming both creds sources in lastWipeReport.errors; got errors=%v", report.Errors)
	}
}
