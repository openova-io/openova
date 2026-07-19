// wipe_async_creds_rewipe_test.go — #5193 regression coverage.
//
// #5193 root-cause chain (hw268/hw269, 2026-07-18): (1) the console wipe
// ran `tofu destroy` synchronously inside the HTTP request, so nginx's
// 60s proxy-read-timeout closing the client connection cancelled
// r.Context() and SIGKILLed the destroy mid-teardown; (2) the wipe path
// lacked the huawei-operator-creds env fallback the janitor's
// discoverHuaweiCreds already has, so a wipe whose per-deployment
// tfvars were already destroyed (and whose in-memory dep.Request was
// lost to a Pod roll) 400'd "credentials required" forever; (3) the
// resulting stranded `wiping` record could not be re-fired because the
// single-flight guard only checked the status string.
//
// This file exercises all three fixes:
//   - async destroy kickoff (WipeDeployment responds before the purge
//     finishes, and the purge survives cancellation of the ORIGINAL
//     request context)
//   - the operator-creds env fallback (huaweiOperatorCredsFromEnv +
//     the HTTP-level 400 guard)
//   - idempotent re-wipe of a stranded `wiping` record
package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ── async destroy kickoff ────────────────────────────────────────────

// TestWipeDeployment_RespondsBeforePurgeCompletes proves the primary
// #5193 fix: WipeDeployment acks 202 (with dep.wipeInFlight already
// true and dep.Status already "wiping") synchronously, WITHOUT waiting
// for the purge sequence — tofu destroy + orphan sweep + DNS/PDM/local
// cleanup — to finish. Pre-#5193 the entire sequence ran inline inside
// the HTTP handler, so this assertion would have required the (real,
// network-bound) purge to complete before any response came back.
func TestWipeDeployment_RespondsBeforePurgeCompletes(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-async-kickoff",
		Status:    "failed",
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-async.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	start := time.Now()
	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)
	elapsed := time.Since(start)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}
	// The real purge (network-bound tofu/hetzner calls) takes several
	// seconds in this test environment (see
	// TestWipeDeployment_NoS3CredsAnywhereSurfacesError, ~4.5s). A
	// synchronous implementation would make this response equally
	// slow; the async one must return near-instantly.
	if elapsed > 500*time.Millisecond {
		t.Errorf("WipeDeployment took %v to respond — expected a near-instant 202 (the purge must run in a detached goroutine, not inline)", elapsed)
	}

	dep.mu.Lock()
	inFlight := dep.wipeInFlight
	status := dep.Status
	dep.mu.Unlock()
	if !inFlight {
		t.Errorf("dep.wipeInFlight=false immediately after the 202 response — the purge goroutine was not launched synchronously with the ack")
	}
	if status != "wiping" {
		t.Errorf("dep.Status=%q immediately after the 202 response, want wiping", status)
	}

	// Drain the goroutine before returning so it can't leak into a
	// later test in this same process. 60s ceiling (#5255): a loaded
	// shared CI runner finished the purge at ~15.01s, tripping the old
	// 15s deadline. waitForWipeDone polls every 10ms and returns the
	// moment the purge lands, so the generous ceiling costs nothing on
	// healthy runs.
	waitForWipeDone(t, dep, 60*time.Second)
}

// TestWipeDeployment_PurgeSurvivesRequestContextCancellation is the
// direct regression test for the #5193 root cause: nginx's 60s
// proxy-read-timeout closing the client-facing connection surfaces to
// catalyst-api as the inbound request's context being cancelled. The
// OLD code rooted the purge's tofuCtx in r.Context(), so that
// cancellation propagated into every downstream network call
// (`tofu destroy`'s exec.CommandContext, the Hetzner/Huawei orphan-
// sweep HTTP client) and killed them mid-flight — region-a destroyed,
// region-b/EIPs/VPCs/S3 left behind (the hw268 incident).
//
// This test cancels the ORIGINAL request's context immediately after
// WipeDeployment acks, then asserts the detached purge goroutine still
// ran its real network call to completion — i.e. its error is the
// normal (several-second) auth-rejection shape, NOT an instant
// "context canceled" that a request-bound context would have produced.
func TestWipeDeployment_PurgeSurvivesRequestContextCancellation(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-detached-ctx",
		Status:    "failed",
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "otech-detached.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	h.deployments.Store(dep.ID, dep)

	router := chi.NewRouter()
	router.Post("/api/v1/deployments/{id}/wipe", h.WipeDeployment)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/"+dep.ID+"/wipe", bytes.NewBufferString(`{"hetznerToken":"fake-hetzner-token"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}

	// Simulate nginx (or the wizard's browser tab) dropping the
	// connection the instant the ack is received — exactly what a 60s
	// proxy-timeout does to the caller's side of the request.
	cancelledAt := time.Now()
	cancel()

	waitForWipeDone(t, dep, 60*time.Second)
	purgeElapsed := time.Since(cancelledAt)

	dep.mu.Lock()
	status := dep.Status
	report := dep.lastWipeReport
	dep.mu.Unlock()

	if status != "wiped" {
		t.Errorf("dep.Status=%q after request-context cancellation, want wiped", status)
	}
	if report == nil {
		t.Fatalf("dep.lastWipeReport is nil after purge completed")
	}
	for _, e := range report.Errors {
		if strings.Contains(e, "context canceled") || strings.Contains(e, "context deadline exceeded") {
			t.Errorf("purge error carries a context-cancellation shape (%q) — the purge is still tied to the (now-cancelled) request context, the #5193 bug is NOT fixed", e)
		}
	}
	// A request-bound context would have failed every downstream call
	// instantly (sub-millisecond). The real network round trip this
	// test environment observes takes on the order of seconds — assert
	// the purge actually took that long, proving it ran independently
	// of the cancelled request context rather than aborting instantly.
	if purgeElapsed < 200*time.Millisecond {
		t.Errorf("purge completed %v after the request context was cancelled — too fast for a real network round trip, suggests the purge was aborted by the cancellation instead of running detached", purgeElapsed)
	}
}

// ── operator-creds env fallback ──────────────────────────────────────

// TestHuaweiOperatorCredsFromEnv_AllPresent — the happy path: all four
// CATALYST_HUAWEI_* env vars set, region included verbatim.
func TestHuaweiOperatorCredsFromEnv_AllPresent(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "ak-operator")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "sk-operator")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "proj-operator")
	t.Setenv("CATALYST_HUAWEI_REGION", "me-east-215")

	hw, ok := huaweiOperatorCredsFromEnv()
	if !ok {
		t.Fatal("expected ok=true with all four env vars set")
	}
	if hw.AccessKey != "ak-operator" || hw.SecretKey != "sk-operator" || hw.ProjectID != "proj-operator" || hw.Region != "me-east-215" {
		t.Fatalf("unexpected creds: %+v", hw)
	}
}

// TestHuaweiOperatorCredsFromEnv_DefaultsRegion — empty region defaults
// to me-east-215, mirroring the janitor's discoverHuaweiCreds fallback
// default (janitor.go).
func TestHuaweiOperatorCredsFromEnv_DefaultsRegion(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "ak-operator")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "sk-operator")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "proj-operator")
	t.Setenv("CATALYST_HUAWEI_REGION", "")

	hw, ok := huaweiOperatorCredsFromEnv()
	if !ok {
		t.Fatal("expected ok=true with access/secret/project set and region empty")
	}
	if hw.Region != "me-east-215" {
		t.Errorf("empty region must default to me-east-215, got %q", hw.Region)
	}
}

// TestHuaweiOperatorCredsFromEnv_MissingAnyReturnsFalse — access_key,
// secret_key, and project_id are each individually required; region is
// the only optional field (defaulted).
func TestHuaweiOperatorCredsFromEnv_MissingAnyReturnsFalse(t *testing.T) {
	cases := []struct {
		name              string
		ak, sk, projectID string
	}{
		{"missing access_key", "", "sk", "proj"},
		{"missing secret_key", "ak", "", "proj"},
		{"missing project_id", "ak", "sk", ""},
		{"all missing", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", tc.ak)
			t.Setenv("CATALYST_HUAWEI_SECRET_KEY", tc.sk)
			t.Setenv("CATALYST_HUAWEI_PROJECT_ID", tc.projectID)
			t.Setenv("CATALYST_HUAWEI_REGION", "")

			if _, ok := huaweiOperatorCredsFromEnv(); ok {
				t.Errorf("%s: expected ok=false", tc.name)
			}
		})
	}
}

// TestWipeDeployment_HuaweiOperatorCredsEnvFallbackAvoids400 is the
// HTTP-level integration proof for #5193 fix 2. A Huawei deployment
// whose body, in-memory Request, AND per-deployment PVC tfvars are ALL
// empty of credentials (the exact hw268 shape: a partial destroy wiped
// the tfvars, a Pod roll wiped dep.Request, the operator re-fires with
// an empty body) must NOT 400 "credentials required" when the
// huawei-operator-creds env is present on catalyst-api — it must fall
// back to it, exactly like the janitor's discoverHuaweiCreds already
// does for the orphan sweep.
func TestWipeDeployment_HuaweiOperatorCredsEnvFallbackAvoids400(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "ak-operator-secret")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "sk-operator-secret")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "proj-operator-secret")
	t.Setenv("CATALYST_HUAWEI_REGION", "me-east-215")
	// No per-deployment tfvars on disk — points at an empty workdir root
	// so loadHuaweiCredsFromTfvars honestly misses (simulating the
	// partial-destroy-already-deleted-tfvars shape).
	t.Setenv("CATALYST_TOFU_WORKDIR", t.TempDir())

	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-hw268-stranded",
		Status:    "failed",
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "hw268.omani.works",
			SovereignDomainMode: "pool",
			Provider:            "huawei",
			Regions:             []provisioner.RegionSpec{{Provider: "huawei", CloudRegion: "me-east-215"}},
			// Huawei* fields intentionally EMPTY — simulates the
			// post-Pod-roll state where they were GC'd from memory.
		},
	}
	h.deployments.Store(dep.ID, dep)

	// Empty body — the operator re-fires the canonical bare wipe, same
	// as the manual recovery this issue documents having to bypass via
	// port-forward + explicit creds. With the fix, the empty body alone
	// must be enough because the env fallback fills it in.
	w := callWipeDeployment(h, dep.ID, "", `{}`)

	if w.Code == http.StatusBadRequest {
		t.Fatalf("wipe 400'd despite huawei-operator-creds env being set — the env fallback did not fire: body=%s", w.Body.String())
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}

	waitForWipeDone(t, dep, 60*time.Second)
}

// TestWipeDeployment_HuaweiNoCredsAnywhereStill400s is the negative
// cross-check: WITHOUT the operator-creds env set (and without body/
// tfvars/in-memory creds), the wipe must still 400 — the fallback must
// not mask a genuinely unconfigured catalyst-api.
func TestWipeDeployment_HuaweiNoCredsAnywhereStill400s(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "")
	t.Setenv("CATALYST_HUAWEI_REGION", "")
	t.Setenv("CATALYST_TOFU_WORKDIR", t.TempDir())

	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-hw268-nocreds",
		Status:    "failed",
		StartedAt: time.Now().Add(-10 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "hw268-nocreds.omani.works",
			SovereignDomainMode: "pool",
			Provider:            "huawei",
			Regions:             []provisioner.RegionSpec{{Provider: "huawei", CloudRegion: "me-east-215"}},
		},
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (no credential source anywhere) — body=%s", w.Code, w.Body.String())
	}
	dep.mu.Lock()
	status := dep.Status
	dep.mu.Unlock()
	if status != "failed" {
		t.Errorf("dep.Status=%q after a 400, want unchanged 'failed' — the guard must short-circuit before the single-flight status flip", status)
	}
}

// ── idempotent re-wipe of a stranded `wiping` record ─────────────────

// TestWipeDeployment_StrandedWipingRecordIsReWipeable is the direct
// regression test for #5193 fix 3. A deployment whose Status is
// "wiping" but whose owning goroutine died with a PRIOR catalyst-api
// process (simulated here by wipeInFlight defaulting to its zero value
// false — exactly what fromRecord produces on Pod-restart rehydration,
// since wipeInFlight is never persisted) must be re-wipeable: a fresh
// POST .../wipe must NOT 409, it must resume/re-run the purge.
func TestWipeDeployment_StrandedWipingRecordIsReWipeable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-stranded-wiping",
		Status:    "wiping", // left behind by a dead process
		StartedAt: time.Now().Add(-45 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "hw268-stranded.omani.works",
			SovereignDomainMode: "pool",
		},
		// wipeInFlight intentionally left at its zero value (false) —
		// this is exactly the in-memory shape a Deployment rehydrated
		// via fromRecord has: the field is never persisted, so a fresh
		// process always starts believing no goroutine owns this wipe.
	}
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)

	if w.Code == http.StatusConflict {
		t.Fatalf("stranded wiping record was refused with 409 — a Status=wiping record with no live owning goroutine must be re-wipeable: body=%s", w.Body.String())
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}

	dep.mu.Lock()
	inFlight := dep.wipeInFlight
	dep.mu.Unlock()
	if !inFlight {
		t.Errorf("dep.wipeInFlight=false immediately after re-firing the wipe — the purge goroutine was not (re)launched")
	}

	waitForWipeDone(t, dep, 60*time.Second)

	dep.mu.Lock()
	status := dep.Status
	dep.mu.Unlock()
	if status != "wiped" {
		t.Errorf("dep.Status=%q after re-wiping a stranded record, want wiped — the resumed destroy must reach completion", status)
	}
}

// TestWipeDeployment_GenuinelyInFlightWipeReturns409 is the paired
// negative case: a Status="wiping" record whose goroutine IS actively
// running in THIS process (wipeInFlight=true) must still be refused
// with 409 — the single-flight guard must not become a no-op.
func TestWipeDeployment_GenuinelyInFlightWipeReturns409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "dep-genuinely-in-flight",
		Status:    "wiping",
		StartedAt: time.Now().Add(-5 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "hw268-inflight.omani.works",
			SovereignDomainMode: "pool",
		},
	}
	dep.mu.Lock()
	dep.wipeInFlight = true // a goroutine in THIS process genuinely owns the purge
	dep.mu.Unlock()
	h.deployments.Store(dep.ID, dep)

	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (a genuinely in-flight purge must refuse a concurrent wipe) — body=%s", w.Code, w.Body.String())
	}
}
