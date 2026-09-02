// Tests for the `phase1-watch` re-watch retry (#6795 / #6799).
//
// The scenario: Phase 1 timed out (status=failed, phase1Outcome=timeout)
// but the Sovereign converged on its own. The Phase-0 retry re-runs
// `tofu apply` (not idempotent on kom4dc after the NAT-EIP rotation) and
// the Phase-1 advisory branch prints an event and stops. `phase1-watch`
// re-attaches ONLY the helmwatch observer, and on OutcomeReady walks the
// same terminal path as a first-launch watch (ready + handover).
//
// The watcher runs for real against a fake dynamic client (the same
// seam phase1_watch_test.go uses), so the assertions cover the whole
// chain from the HTTP request to the terminal status — not only the
// handler's state flip up to the goroutine.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// postRetryPhase drives h.RetryPhase the way the chi router does.
func postRetryPhase(t *testing.T, h *Handler, id, phase string) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	rctx.URLParams.Add("phase", phase)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/"+id+"/phases/"+phase+"/retry", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.RetryPhase(rr, req)
	// writeJSON stamps a numeric `status` onto error envelopes, so decode
	// loosely and keep the string-valued fields the contract names.
	body := map[string]string{}
	if strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "{") {
		raw := map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode body %q: %v", rr.Body.String(), err)
		}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				body[k] = s
			}
		}
	}
	return rr, body
}

// makeTimedOutDeployment builds the hw307 shape: a terminal failed record
// whose Phase 1 timed out, with Result + a kubeconfig file on disk (the
// watcher's two inputs). Channels are CLOSED, as they are after the first
// run's runProvisioning returned.
func makeTimedOutDeployment(t *testing.T, h *Handler, id string, withKubeconfig bool) *Deployment {
	t.Helper()
	path := ""
	if withKubeconfig {
		path = filepath.Join(t.TempDir(), id+".yaml")
		if err := os.WriteFile(path, []byte("fake-kubeconfig: yaml"), 0o600); err != nil {
			t.Fatalf("write kubeconfig: %v", err)
		}
	}
	finished := time.Now().Add(-time.Hour)
	evCh := make(chan provisioner.Event)
	close(evCh)
	doneCh := make(chan struct{})
	close(doneCh)
	dep := &Deployment{
		ID:         id,
		Status:     "failed",
		Error:      "Phase 1 watch timed out before convergence: 66 component(s) observed, none hard-failed",
		StartedAt:  time.Now().Add(-3 * time.Hour),
		FinishedAt: finished,
		eventsCh:   evCh,
		done:       doneCh,
		Request: provisioner.Request{
			SovereignFQDN: "hw307." + id + ".example",
			Region:        "ae-ad-1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:    "hw307." + id + ".example",
			KubeconfigPath:   path,
			Phase1Outcome:    helmwatch.OutcomeTimeout,
			Phase1FinishedAt: &finished,
		},
	}
	h.deployments.Store(id, dep)
	return dep
}

func TestRetryPhase_UnknownPhaseStill400(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	makeTimedOutDeployment(t, h, "rewatch-unknown", true)

	rr, body := postRetryPhase(t, h, "rewatch-unknown", "phase2-watch")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown phase: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(body["error"], "unknown phase") {
		t.Fatalf("unknown phase: error should name it, got %q", body["error"])
	}
	// The re-watch phase is deliberately NOT in the Flux-advisory set.
	if phase1Phases["phase1-watch"] || phase0Phases["phase1-watch"] {
		t.Fatalf("phase1-watch must live only in rewatchPhases")
	}
	if err := validatePhaseID("phase1-watch"); err != nil {
		t.Fatalf("validatePhaseID(phase1-watch): %v", err)
	}
}

func TestRetryPhase_Phase1Rewatch_InFlight409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	for _, status := range []string{"provisioning", "tofu-applying", "phase1-watching"} {
		id := "rewatch-inflight-" + status
		dep := makeTimedOutDeployment(t, h, id, true)
		dep.mu.Lock()
		dep.Status = status
		dep.mu.Unlock()

		rr, body := postRetryPhase(t, h, id, "phase1-watch")
		if rr.Code != http.StatusConflict {
			t.Fatalf("status=%s: want 409, got %d body=%s", status, rr.Code, rr.Body.String())
		}
		if body["error"] == "" {
			t.Fatalf("status=%s: 409 must carry an error detail", status)
		}
		dep.mu.Lock()
		if dep.Status != status {
			t.Fatalf("status=%s: a refused re-watch must not touch the record, got %q", status, dep.Status)
		}
		dep.mu.Unlock()
	}
}

func TestRetryPhase_Phase1Rewatch_MissingInputs409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	// No Result at all — Phase 0 never produced tofu outputs.
	noResult := makeTimedOutDeployment(t, h, "rewatch-no-result", true)
	noResult.mu.Lock()
	noResult.Result = nil
	noResult.mu.Unlock()
	rr, body := postRetryPhase(t, h, "rewatch-no-result", "phase1-watch")
	if rr.Code != http.StatusConflict || !strings.Contains(body["error"], "Phase 0 never produced a result") {
		t.Fatalf("nil Result: want 409 naming the missing result, got %d %s", rr.Code, rr.Body.String())
	}
	noResult.mu.Lock()
	if noResult.Status != "failed" {
		t.Fatalf("nil Result: record must stay failed, got %q", noResult.Status)
	}
	noResult.mu.Unlock()

	// Result present but no kubeconfig was ever PUT (no stamp, no file).
	noKube := makeTimedOutDeployment(t, h, "rewatch-no-kubeconfig", false)
	rr, body = postRetryPhase(t, h, "rewatch-no-kubeconfig", "phase1-watch")
	if rr.Code != http.StatusConflict || !strings.Contains(body["error"], "no kubeconfig was ever posted") {
		t.Fatalf("no kubeconfig: want 409 naming the missing kubeconfig, got %d %s", rr.Code, rr.Body.String())
	}
	noKube.mu.Lock()
	if noKube.Status != "failed" || noKube.Result.Phase1Outcome != helmwatch.OutcomeTimeout {
		t.Fatalf("no kubeconfig: record must keep its terminal markers, got status=%q outcome=%q", noKube.Status, noKube.Result.Phase1Outcome)
	}
	noKube.mu.Unlock()
}

// TestRetryPhase_Phase1Rewatch_TimedOutThenConvergedReachesReady is the
// hw307 case end to end: failed/timeout record, cluster now reporting
// every HelmRelease Ready → 200 + phase1-watching + banner event, and the
// re-attached watcher flips the record to ready with OutcomeReady.
func TestRetryPhase_Phase1Rewatch_TimedOutThenConvergedReachesReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
		makeReadyHR("bp-flux"),
	)
	h.phase1WatchTimeout = 5 * time.Second
	h.phase1WatchWG = &sync.WaitGroup{}

	dep := makeTimedOutDeployment(t, h, "rewatch-converged", true)

	rr, body := postRetryPhase(t, h, "rewatch-converged", "phase1-watch")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if body["status"] != "phase1-watching" || body["phase"] != "phase1-watch" || body["id"] != "rewatch-converged" {
		t.Fatalf("response contract: got %+v", body)
	}
	if body["streamURL"] != "/api/v1/deployments/rewatch-converged/logs" {
		t.Fatalf("streamURL: got %q", body["streamURL"])
	}

	// The handler's own state flip is visible before the goroutine ends.
	dep.mu.Lock()
	foundBanner := false
	for _, ev := range dep.eventsBuf {
		if ev.Phase == "phase1-watch" && strings.Contains(ev.Message, "Phase-1 re-watch initiated — no tofu, no cloud writes") {
			foundBanner = true
		}
	}
	dep.mu.Unlock()
	if !foundBanner {
		t.Fatalf("re-watch banner event not recorded in eventsBuf")
	}

	// Await the re-attached watcher (same WaitGroup resumePhase1Watch uses).
	waitCh := make(chan struct{})
	go func() { h.phase1WatchWG.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(20 * time.Second):
		t.Fatalf("re-watch did not terminate within 20s")
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" {
		t.Fatalf("after convergence: want status ready, got %q (err=%q)", dep.Status, dep.Error)
	}
	if dep.Error != "" {
		t.Fatalf("after convergence: Error must be cleared, got %q", dep.Error)
	}
	if dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		t.Fatalf("after convergence: want OutcomeReady, got %q", dep.Result.Phase1Outcome)
	}
	if dep.Result.Phase1FinishedAt == nil || dep.Result.Phase1FinishedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("after convergence: Phase1FinishedAt must be re-stamped by this watch, got %v", dep.Result.Phase1FinishedAt)
	}
	if len(dep.Result.ComponentStates) != 3 {
		t.Fatalf("after convergence: want 3 component states, got %d", len(dep.Result.ComponentStates))
	}
	if !dep.isDone() {
		t.Fatalf("after convergence: done channel must be closed so SSE consumers release")
	}
}

// TestRetryPhase_Phase1Rewatch_NonReadyOutcomeStaysFailed proves the
// re-watch cannot fabricate a ready verdict: with no HelmRelease ever
// observed the watcher terminates non-ready and the record is failed
// with the outcome named.
func TestRetryPhase_Phase1Rewatch_NonReadyOutcomeStaysFailed(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects()
	h.phase1WatchTimeout = 2 * time.Second
	h.phase1FirstSeenTimeout = 500 * time.Millisecond
	h.phase1WatchWG = &sync.WaitGroup{}

	dep := makeTimedOutDeployment(t, h, "rewatch-empty", true)

	rr, _ := postRetryPhase(t, h, "rewatch-empty", "phase1-watch")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	waitCh := make(chan struct{})
	go func() { h.phase1WatchWG.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("re-watch did not terminate within 30s")
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" {
		t.Fatalf("empty cluster: want failed, got %q", dep.Status)
	}
	if dep.Result.Phase1Outcome == "" || dep.Result.Phase1Outcome == helmwatch.OutcomeReady {
		t.Fatalf("empty cluster: outcome must be a named non-ready outcome, got %q", dep.Result.Phase1Outcome)
	}
	if dep.Error == "" {
		t.Fatalf("empty cluster: Error must name the outcome")
	}
}
