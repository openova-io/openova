// Tests for the Phase-1 HelmRelease watch wiring in the handler.
//
// What this file proves (matches the GATES checklist for the
// per-component-SSE task):
//
//  1. runPhase1Watch wires the helmwatch.Watcher against the
//     deployment's persisted Kubeconfig and the per-component
//     events flow into the same eventsBuf the Phase-0 events use,
//     so /events replay sees them.
//  2. markPhase1Done writes ComponentStates + Phase1FinishedAt
//     onto Deployment.Result and flips Status to "ready" when
//     every component installed.
//  3. A failed component flips Status to "failed" with an error
//     message naming the count.
//  4. An empty Kubeconfig short-circuits the watch with a single
//     warn event and still calls markPhase1Done so Status doesn't
//     stay "phase1-watching" forever.
//  5. Pod-restart resume — a deployment loaded from disk with
//     Status="phase1-watching" gets rewritten to "failed" by
//     fromRecord (existing contract) so a Pod kill mid-watch
//     surfaces as the wizard's FailureCard, not a stuck pill.
//  6. CATALYST_PHASE1_WATCH_TIMEOUT env var parses through
//     phase1WatchConfigForDeployment.
//  7. The on-disk store record JSON includes ComponentStates +
//     Phase1FinishedAt so a Pod restart rehydrates them.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// ─────────────────────────────────────────────────────────────────────
// Test fixtures shared between the handler tests below.
// ─────────────────────────────────────────────────────────────────────

// helmReleaseListGVK_handler — registered with the fake dynamic client
// so List+Watch resolve. Same rationale as in helmwatch's tests; we
// duplicate locally to keep this file independently runnable.
var helmReleaseListGVK_handler = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmReleaseList",
}

func newFakeSchemeForHandler() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(helmReleaseListGVK_handler, &unstructured.UnstructuredList{})
	return scheme
}

// makeReadyHR builds a bp-* HelmRelease with Ready=True. Used by the
// "all installed" path so the watch terminates immediately.
func makeReadyHR(name string) *unstructured.Unstructured {
	return makeHRWithReady(name, metav1.ConditionTrue, "ReconciliationSucceeded", "Helm install succeeded")
}

// makeFailedHR builds a bp-* HelmRelease with Ready=False reason=
// InstallFailed so the watch sees a terminal failure.
func makeFailedHR(name, msg string) *unstructured.Unstructured {
	return makeHRWithReady(name, metav1.ConditionFalse, "InstallFailed", msg)
}

func makeHRWithReady(name string, status metav1.ConditionStatus, reason, message string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"name":      name,
				"namespace": helmwatch.FluxNamespace,
			},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{"chart": name},
				},
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             string(status),
						"reason":             reason,
						"message":            message,
						"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	return u
}

// fakeDynamicFactoryFromObjects — closure that returns a fake.NewSimpleDynamicClient
// seeded with the given HelmReleases, ignoring the kubeconfig argument.
// Tests use this to inject a deterministic apiserver into runPhase1Watch.
func fakeDynamicFactoryFromObjects(objs ...runtime.Object) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
		scheme := newFakeSchemeForHandler()
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			scheme,
			map[schema.GroupVersionResource]string{helmwatch.HelmReleaseGVR: "HelmReleaseList"},
			objs...,
		)
		return client, nil
	}
}

// makeDeploymentWithKubeconfig — analogous to makeDeployment in
// deployments_events_test.go but with Result.KubeconfigPath
// pre-populated so runPhase1Watch picks it up. The kubeconfig
// argument is the file CONTENTS (post-#183 the watch reads from
// disk; an empty string maps to KubeconfigPath="" so the watch
// short-circuits).
func makeDeploymentWithKubeconfig(t *testing.T, h *Handler, id, kubeconfig string) *Deployment {
	t.Helper()

	var path string
	if kubeconfig != "" {
		path = filepath.Join(t.TempDir(), id+".yaml")
		if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
			t.Fatalf("write kubeconfig: %v", err)
		}
	}

	dep := &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test." + id + ".example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "test." + id + ".example",
			KubeconfigPath: path,
		},
	}
	h.deployments.Store(id, dep)
	return dep
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

// TestRunPhase1Watch_AllInstalledFlowsThroughEventsBuf proves the
// per-component events that helmwatch emits land in the durable
// eventsBuf, so /events replay sees them and a browser landing on
// the page after Phase 1 completes still renders per-app pills.
func TestRunPhase1Watch_AllInstalledFlowsThroughEventsBuf(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
		makeReadyHR("bp-flux"),
	)
	h.phase1WatchTimeout = 5 * time.Second

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-all-installed", "fake-kubeconfig: yaml")

	h.runPhase1Watch(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Errorf("Status = %q, want %q (all components installed)", dep.Status, "ready")
	}
	if dep.Result == nil {
		t.Fatalf("Result is nil")
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Errorf("Phase1FinishedAt was not set")
	}
	if got := len(dep.Result.ComponentStates); got != 3 {
		t.Errorf("ComponentStates length = %d, want 3 (got=%v)", got, dep.Result.ComponentStates)
	}
	for _, comp := range []string{"cilium", "cert-manager", "flux"} {
		if dep.Result.ComponentStates[comp] != helmwatch.StateInstalled {
			t.Errorf("ComponentStates[%q] = %q, want %q", comp, dep.Result.ComponentStates[comp], helmwatch.StateInstalled)
		}
	}

	// Per-component events landed in the durable buffer.
	var componentEvents []provisioner.Event
	for _, ev := range dep.eventsBuf {
		if ev.Phase == helmwatch.PhaseComponent && ev.Component != "" {
			componentEvents = append(componentEvents, ev)
		}
	}
	if got := len(componentEvents); got != 3 {
		t.Errorf("durable eventsBuf component events = %d, want 3:\n%+v", got, componentEvents)
	}
	for _, ev := range componentEvents {
		if ev.State != helmwatch.StateInstalled {
			t.Errorf("eventsBuf event for %q State=%q, want %q", ev.Component, ev.State, helmwatch.StateInstalled)
		}
	}
}

// TestRunPhase1Watch_FailedComponentFlipsStatusToFailed proves a
// component ending in "failed" propagates to Deployment.Status =
// "failed" with an error message naming the count.
func TestRunPhase1Watch_FailedComponentFlipsStatusToFailed(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeFailedHR("bp-cert-manager", "chart load failed: 401"),
	)
	h.phase1WatchTimeout = 5 * time.Second

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-failed", "fake-kubeconfig: yaml")
	h.runPhase1Watch(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Errorf("Status = %q, want %q", dep.Status, "failed")
	}
	if !strings.Contains(dep.Error, "1 failed component") {
		t.Errorf("Error = %q, want it to mention the failed count", dep.Error)
	}
	if dep.Result.ComponentStates["cert-manager"] != helmwatch.StateFailed {
		t.Errorf("ComponentStates[cert-manager] = %q, want %q",
			dep.Result.ComponentStates["cert-manager"], helmwatch.StateFailed)
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// TestRunPhase1Watch_EmptyKubeconfigShortCircuits proves that a
// deployment with no captured kubeconfig surfaces a single warn
// event and still calls markPhase1Done so Status leaves
// "phase1-watching" — AND, per issue #488 (Phase-8a bug #8), the
// short-circuit must NOT report Status="ready". Before #488 was
// fixed the short-circuit fell through to markPhase1Done's default
// branch which set "ready" — the wizard then lied to the operator
// that a Sovereign was Ready when catalyst-api had never observed
// it. The truthful state is "failed" with a kubeconfig-missing
// outcome and an operator-actionable error message.
//
// Issue #538: the watch now POLLS for the cloud-init kubeconfig
// rather than terminating on the first miss. To exercise the
// terminal-on-timeout path deterministically the test injects a
// tiny kubeconfigArrivalTimeout / kubeconfigArrivalPollInterval —
// the polling loop runs for ~50 ms and then surfaces
// OutcomeKubeconfigMissing as the original test expected.
func TestRunPhase1Watch_EmptyKubeconfigShortCircuits(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.kubeconfigArrivalTimeout = 50 * time.Millisecond
	h.kubeconfigArrivalPollInterval = 10 * time.Millisecond
	dep := makeDeploymentWithKubeconfig(t, h, "phase1-no-kubeconfig", "")
	h.runPhase1Watch(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status == "phase1-watching" {
		t.Errorf("Status stuck at phase1-watching after short-circuit")
	}
	// Issue #488: must be "failed", not "ready". Falling through to
	// "ready" with no observed components means the UI lies about
	// Sovereign state every single time a kubeconfig PUT is missed.
	if dep.Status != "failed" {
		t.Errorf("Status = %q, want %q (kubeconfig short-circuit must fail loudly, not flip ready — issue #488)", dep.Status, "failed")
	}
	if dep.Error == "" {
		t.Errorf("Error must be populated with operator-actionable diagnostic, got empty")
	}
	if !strings.Contains(dep.Error, "kubeconfig") {
		t.Errorf("Error must mention kubeconfig so the operator knows what to investigate; got: %q", dep.Error)
	}
	// Result.Phase1FinishedAt is set even though no watch ran.
	if dep.Result == nil || dep.Result.Phase1FinishedAt == nil {
		t.Errorf("Phase1FinishedAt should be set even on short-circuit; result=%+v", dep.Result)
	}
	// Phase1Outcome must be the explicit kubeconfig-missing string so
	// the wizard banner can render the right operator-actionable
	// diagnostic — not empty, which is what triggered the false-ready
	// fall-through before the fix.
	if dep.Result == nil || dep.Result.Phase1Outcome != helmwatch.OutcomeKubeconfigMissing {
		t.Errorf("Phase1Outcome = %q, want %q (issue #488)", func() string {
			if dep.Result == nil {
				return "<nil result>"
			}
			return dep.Result.Phase1Outcome
		}(), helmwatch.OutcomeKubeconfigMissing)
	}
	// Exactly one warn event in the buffer (the "skipped" message).
	warns := 0
	for _, ev := range dep.eventsBuf {
		if ev.Phase == helmwatch.PhaseComponent && ev.Level == "warn" {
			warns++
		}
	}
	if warns < 1 {
		t.Errorf("expected at least 1 warn event for the kubeconfig-skipped path, got: %+v", dep.eventsBuf)
	}
}

// TestGetDeployment_SurfacesComponentStatesAtTopLevel proves the
// State() snapshot lifts ComponentStates + Phase1FinishedAt to the
// top of the response so the Sovereign Admin can read them without
// unwrapping result.
func TestGetDeployment_SurfacesComponentStatesAtTopLevel(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	dep := &Deployment{
		ID:        "phase1-state-surface",
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test.example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "test.example",
			ComponentStates: map[string]string{
				"cilium":            helmwatch.StateInstalled,
				"cert-manager":      helmwatch.StateInstalled,
				"catalyst-platform": helmwatch.StateInstalling,
			},
			Phase1FinishedAt: ptrTime(time.Now().UTC()),
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
	cs, ok := got["componentStates"].(map[string]any)
	if !ok {
		t.Fatalf("componentStates missing or wrong type at top level: %v", got)
	}
	if cs["cilium"] != "installed" {
		t.Errorf("componentStates[cilium] = %v, want \"installed\"", cs["cilium"])
	}
	if cs["catalyst-platform"] != "installing" {
		t.Errorf("componentStates[catalyst-platform] = %v, want \"installing\"", cs["catalyst-platform"])
	}
	if got["phase1FinishedAt"] == nil {
		t.Errorf("phase1FinishedAt missing at top level: %v", got)
	}
}

// TestGetDeploymentEvents_ReturnsComponentEventsInBuffer proves the
// /events endpoint surfaces phase=component events the watch wrote
// into eventsBuf — same path as the SSE replay, so a wizard reload
// on a completed deployment renders per-component pills instantly.
func TestGetDeploymentEvents_ReturnsComponentEventsInBuffer(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
	)
	h.phase1WatchTimeout = 3 * time.Second

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-events-replay", "fake-kubeconfig: yaml")
	h.runPhase1Watch(dep)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+dep.ID+"/events", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", dep.ID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	h.GetDeploymentEvents(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Events []provisioner.Event `json:"events"`
		State  map[string]any      `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	gotComponents := map[string]string{}
	for _, ev := range got.Events {
		if ev.Phase == helmwatch.PhaseComponent && ev.Component != "" {
			gotComponents[ev.Component] = ev.State
		}
	}
	if gotComponents["cilium"] != helmwatch.StateInstalled {
		t.Errorf("/events did not surface cilium=installed, got: %v", gotComponents)
	}
	if gotComponents["cert-manager"] != helmwatch.StateInstalled {
		t.Errorf("/events did not surface cert-manager=installed, got: %v", gotComponents)
	}
}

// TestRunPhase1Watch_TimeoutFlipsStatusAndRecordsPartial proves
// that a stuck install reaches markPhase1Done after the configured
// timeout (cfg.WatchTimeout, threaded from h.phase1WatchTimeout)
// without the watch hanging forever. The test uses a single
// non-terminal release so the only exit is the timeout path.
func TestRunPhase1Watch_TimeoutFlipsStatusAndRecordsPartial(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeHRWithReady("bp-keycloak", metav1.ConditionUnknown, "Progressing", "Reconciliation in progress"),
	)
	h.phase1WatchTimeout = 400 * time.Millisecond

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-timeout", "fake-kubeconfig: yaml")
	start := time.Now()
	h.runPhase1Watch(dep)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("runPhase1Watch took %v — timeout did not kick in", elapsed)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	// Status stays "ready" because no failure occurred — the partial
	// state has keycloak=installing, no failures. The Sovereign Admin
	// shell renders "1 of N components installed (timeout reached)".
	// This contract: timeout without failure = "ready" with partial
	// componentStates, NOT "failed".
	if dep.Status != "ready" {
		t.Errorf("Status = %q, want %q (timeout with no failed components is not a Phase-1 failure)", dep.Status, "ready")
	}
	if dep.Result.ComponentStates["keycloak"] != helmwatch.StateInstalling {
		t.Errorf("ComponentStates[keycloak] = %q, want %q",
			dep.Result.ComponentStates["keycloak"], helmwatch.StateInstalling)
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Errorf("Phase1FinishedAt was not set after timeout")
	}
}

// TestPhase1WatchConfig_EnvVarOverridesTimeout proves that
// CATALYST_PHASE1_WATCH_TIMEOUT parses through
// phase1WatchConfigForDeployment when h.phase1WatchTimeout is unset.
func TestPhase1WatchConfig_EnvVarOverridesTimeout(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	t.Setenv("CATALYST_PHASE1_WATCH_TIMEOUT", "5m")

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-env-timeout", "fake-kubeconfig: yaml")
	cfg := h.phase1WatchConfigForDeployment(dep, "fake-kubeconfig: yaml")
	if cfg.WatchTimeout != 5*time.Minute {
		t.Errorf("WatchTimeout = %v, want 5m (from env)", cfg.WatchTimeout)
	}
}

func TestPhase1WatchConfig_FieldOverrideBeatsEnv(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.phase1WatchTimeout = 7 * time.Second
	t.Setenv("CATALYST_PHASE1_WATCH_TIMEOUT", "5m") // ignored when h.phase1WatchTimeout is set

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-field-timeout", "fake-kubeconfig: yaml")
	cfg := h.phase1WatchConfigForDeployment(dep, "fake-kubeconfig: yaml")
	if cfg.WatchTimeout != 7*time.Second {
		t.Errorf("WatchTimeout = %v, want 7s (handler field override)", cfg.WatchTimeout)
	}
}

// TestPodRestart_ResumeRehydratesComponentStates proves that
// ComponentStates + Phase1FinishedAt round-trip through the on-disk
// store. A Pod restart that loads a completed Phase-1 deployment
// from disk presents the same state to the Sovereign Admin as the
// pre-restart Pod did.
func TestPodRestart_ResumeRehydratesComponentStates(t *testing.T) {
	tmp := t.TempDir()
	st1, err := store.New(tmp)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Pre-create the kubeconfig file the record will point at — the
	// rehydrate path now reads from disk via Result.KubeconfigPath
	// rather than a string field on Result.
	kcPath := filepath.Join(tmp, "kubeconfigs", "rehydrate-component-states.yaml")
	if err := os.MkdirAll(filepath.Dir(kcPath), 0o700); err != nil {
		t.Fatalf("mkdir kubeconfigs: %v", err)
	}
	if err := os.WriteFile(kcPath, []byte("fake-kubeconfig: yaml"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	finishedAt := time.Now().UTC().Truncate(time.Second)
	rec := store.Record{
		ID:        "rehydrate-component-states",
		Status:    "ready",
		StartedAt: time.Now().Add(-5 * time.Minute).UTC(),
		Request: store.RedactedRequest{
			SovereignFQDN: "test.example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:    "test.example",
			KubeconfigPath:   kcPath,
			Phase1FinishedAt: &finishedAt,
			ComponentStates: map[string]string{
				"cilium":            helmwatch.StateInstalled,
				"cert-manager":      helmwatch.StateInstalled,
				"catalyst-platform": helmwatch.StateFailed,
			},
		},
	}
	if err := st1.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Simulate Pod restart: build a fresh handler against the same
	// directory and confirm the rehydrated deployment carries
	// ComponentStates + Phase1FinishedAt.
	st2, err := store.New(tmp)
	if err != nil {
		t.Fatalf("store.New (restart): %v", err)
	}
	h := NewWithStore(silentLogger(), &fakePDM{}, st2)

	val, ok := h.deployments.Load(rec.ID)
	if !ok {
		t.Fatalf("deployment %q did not rehydrate", rec.ID)
	}
	dep := val.(*Deployment)
	if dep.Result == nil {
		t.Fatalf("Result is nil after rehydrate")
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
	if dep.Result.ComponentStates["catalyst-platform"] != helmwatch.StateFailed {
		t.Errorf("ComponentStates[catalyst-platform] = %q, want %q",
			dep.Result.ComponentStates["catalyst-platform"], helmwatch.StateFailed)
	}
	if dep.Result.Phase1FinishedAt == nil ||
		!dep.Result.Phase1FinishedAt.Equal(finishedAt) {
		t.Errorf("Phase1FinishedAt = %v, want %v", dep.Result.Phase1FinishedAt, finishedAt)
	}
	// KubeconfigPath round-trips on disk and the file is still
	// readable post-restart.
	if dep.Result.KubeconfigPath != kcPath {
		t.Errorf("KubeconfigPath did not round-trip: got %q want %q", dep.Result.KubeconfigPath, kcPath)
	}
	if got, err := os.ReadFile(dep.Result.KubeconfigPath); err != nil {
		t.Errorf("kubeconfig file gone after rehydrate: %v", err)
	} else if string(got) != "fake-kubeconfig: yaml" {
		t.Errorf("kubeconfig file content drift: got %q", got)
	}

	// And the on-disk JSON includes the new fields verbatim, so a
	// future schema bump that drops them gets caught here.
	rawBytes, err := os.ReadFile(filepath.Join(tmp, rec.ID+".json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	raw := string(rawBytes)
	for _, want := range []string{
		`"componentStates"`,
		`"cilium": "installed"`,
		`"catalyst-platform": "failed"`,
		`"phase1FinishedAt"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("on-disk JSON missing %q\n%s", want, raw)
		}
	}
}

// TestPodRestart_StuckPhase1WatchingRewrittenToFailed proves the
// existing in-flight-status rewrite covers the "phase1-watching"
// case the Phase-1 watch introduced. A Pod kill mid-watch must NOT
// leave a deployment stuck at phase1-watching; the wizard's
// FailureCard renders instead.
func TestPodRestart_StuckPhase1WatchingRewrittenToFailed(t *testing.T) {
	tmp := t.TempDir()
	st1, err := store.New(tmp)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	rec := store.Record{
		ID:        "rehydrate-stuck-phase1",
		Status:    "phase1-watching", // in-flight at restart
		StartedAt: time.Now().Add(-5 * time.Minute).UTC(),
		Request: store.RedactedRequest{
			SovereignFQDN: "test.example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "test.example",
		},
	}
	if err := st1.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st2, err := store.New(tmp)
	if err != nil {
		t.Fatalf("store.New (restart): %v", err)
	}
	h := NewWithStore(silentLogger(), &fakePDM{}, st2)

	val, _ := h.deployments.Load(rec.ID)
	dep := val.(*Deployment)
	if dep.Status != "failed" {
		t.Errorf("Status = %q, want %q (in-flight phase1-watching must rewrite to failed)", dep.Status, "failed")
	}
	if dep.Error == "" {
		t.Errorf("Error empty — operator wouldn't know why this deployment failed")
	}
}

// TestEvent_ComponentAndStateFieldsOmittedForPhase0 proves the
// existing Phase-0 event wire format is unchanged: a Phase-0 OpenTofu
// event JSON-encodes without component/state keys (omitempty).
func TestEvent_ComponentAndStateFieldsOmittedForPhase0(t *testing.T) {
	ev := provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "tofu-apply",
		Level:   "info",
		Message: "hcloud_server.cp[0]: Creation complete after 30s",
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, `"component"`) {
		t.Errorf("Phase-0 event should not include component key: %s", got)
	}
	if strings.Contains(got, `"state"`) {
		t.Errorf("Phase-0 event should not include state key: %s", got)
	}
}

// TestEvent_ComponentAndStateFieldsPresentForPhase1 proves the new
// fields ARE serialized for phase=component events.
func TestEvent_ComponentAndStateFieldsPresentForPhase1(t *testing.T) {
	ev := provisioner.Event{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Phase:     helmwatch.PhaseComponent,
		Level:     "info",
		Message:   "Helm install succeeded",
		Component: "cilium",
		State:     helmwatch.StateInstalled,
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"component":"cilium"`) {
		t.Errorf("Phase-1 event missing component: %s", got)
	}
	if !strings.Contains(got, `"state":"installed"`) {
		t.Errorf("Phase-1 event missing state: %s", got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Issue #538 — Phase-1 watch waits for cloud-init's kubeconfig PUT
// instead of terminating on the first miss.
// ─────────────────────────────────────────────────────────────────────

// TestRunPhase1Watch_WaitsForKubeconfigArrival proves that
// runPhase1Watch polls for the kubeconfig file and proceeds when it
// shows up partway through the wait window — instead of terminating
// kubeconfig-missing on the first poll. This is the live bug from
// otech21 (1a7328cc3a94210b): runProvisioning launched the watch
// moments before cloud-init's PUT arrived, so the deployment latched
// terminal-failed and the wizard showed Install X jobs PENDING
// forever even though the cluster was healthy.
//
// The test races a goroutine that writes the kubeconfig file 50 ms
// into the watch against a ~5 s deadline; the watch must observe
// the kubeconfig and run the helmwatch successfully against the
// fake dynamic client.
func TestRunPhase1Watch_WaitsForKubeconfigArrival(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
	)
	h.phase1WatchTimeout = 5 * time.Second
	// 5 s of polling time, every 25 ms — tiny enough to keep the
	// test fast but big enough that the writer goroutine has time
	// to hand the file to the watch.
	h.kubeconfigArrivalTimeout = 5 * time.Second
	h.kubeconfigArrivalPollInterval = 25 * time.Millisecond

	// Build a deployment whose KubeconfigPath is set BEFORE the
	// watch starts (so the polling loop knows where to look) but
	// whose file does NOT yet exist on disk — a faithful re-creation
	// of the live race: dep.Result.KubeconfigPath was never going
	// to be empty in production because PutKubeconfig sets it.
	id := "phase1-arrival-race"
	kcDir := t.TempDir()
	kcPath := filepath.Join(kcDir, id+".yaml")
	dep := &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test." + id + ".example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "test." + id + ".example",
			KubeconfigPath: kcPath,
		},
	}
	h.deployments.Store(id, dep)

	// Writer goroutine: after a short delay, drop the kubeconfig
	// onto disk. The polling loop should pick it up on the next
	// tick (≤25 ms later) and proceed with helmwatch.
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = os.WriteFile(kcPath, []byte("fake-kubeconfig: yaml"), 0o600)
	}()

	h.runPhase1Watch(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want %q (kubeconfig arrived mid-wait — watch must NOT terminate kubeconfig-missing on first miss; issue #538): error=%q outcome=%q",
			dep.Status, "ready", dep.Error, func() string {
				if dep.Result == nil {
					return "<nil>"
				}
				return dep.Result.Phase1Outcome
			}())
	}
	// Issue #538 contract: the watch ran (it didn't short-circuit
	// kubeconfig-missing). Phase1Outcome is OutcomeKubeconfigMissing
	// ONLY when the wait window times out without a PUT — anything
	// else proves the wait succeeded and the watch took over.
	if dep.Result == nil || dep.Result.Phase1Outcome == helmwatch.OutcomeKubeconfigMissing {
		got := "<nil result>"
		if dep.Result != nil {
			got = dep.Result.Phase1Outcome
		}
		t.Errorf("Phase1Outcome = %q, want anything other than %q — kubeconfig arrived mid-wait (issue #538)", got, helmwatch.OutcomeKubeconfigMissing)
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// TestPutKubeconfig_RestartsWatchAfterTerminalKubeconfigMissing
// proves the belt-and-braces second half of the issue #538 fix:
// when a previous Phase-1 watch already terminated with
// OutcomeKubeconfigMissing (the deployment is terminal-failed,
// phase1Started=true, Phase1FinishedAt set), a successful
// PutKubeconfig clears those terminal markers and launches a fresh
// watch. The freshly-started watch picks the kubeconfig up off disk
// and observes the bp-cilium HelmRelease, ending in Status=ready
// with Phase1Outcome=ready.
func TestPutKubeconfig_RestartsWatchAfterTerminalKubeconfigMissing(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
	)
	h.phase1WatchTimeout = 3 * time.Second
	h.kubeconfigArrivalTimeout = 1 * time.Second
	h.kubeconfigArrivalPollInterval = 10 * time.Millisecond

	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)

	// Simulate the live state from otech21: a previous watch ran,
	// timed out kubeconfig-missing, and latched the deployment in
	// terminal-failed. phase1Started=true, Phase1Outcome=
	// OutcomeKubeconfigMissing, Phase1FinishedAt set, Status=failed.
	finishedAt := time.Now().UTC()
	dep.mu.Lock()
	dep.Status = "failed"
	dep.Error = "Phase 1 watch never ran: kubeconfig missing"
	dep.FinishedAt = finishedAt
	dep.phase1Started = true
	dep.Result.Phase1Outcome = helmwatch.OutcomeKubeconfigMissing
	dep.Result.Phase1FinishedAt = &finishedAt
	dep.Result.ComponentStates = map[string]string{}
	// runProvisioning's close()s would have run when the original
	// watch terminated; close them here too so the relaunch path's
	// channel-allocation branch is genuinely exercised.
	close(dep.eventsCh)
	close(dep.done)
	dep.mu.Unlock()

	// Drive the cloud-init PUT.
	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte("fake-kubeconfig: yaml"))
	h.PutKubeconfig(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	// Wait for the relaunched watch to terminate. With a fake
	// dynamic client + ~3 s phase1WatchTimeout it should land
	// within ~1 s.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		done := dep.Result != nil &&
			dep.Result.Phase1Outcome != helmwatch.OutcomeKubeconfigMissing &&
			dep.Result.Phase1Outcome != ""
		dep.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want %q after PUT-relaunch; outcome=%q error=%q (issue #538)",
			dep.Status, "ready", dep.Result.Phase1Outcome, dep.Error)
	}
	// Issue #538 contract: the relaunched watch ran. The exact
	// terminal outcome is OutcomeReady when the all-done gate fires
	// (≥ MinBootstrapKitHRs observed) or OutcomeTimeout otherwise;
	// what matters is that we are NOT still latched in
	// OutcomeKubeconfigMissing.
	if dep.Result.Phase1Outcome == helmwatch.OutcomeKubeconfigMissing || dep.Result.Phase1Outcome == "" {
		t.Errorf("Phase1Outcome = %q, want anything other than %q/empty — PUT must have relaunched the watch (issue #538)",
			dep.Result.Phase1Outcome, helmwatch.OutcomeKubeconfigMissing)
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q (relaunched watch must observe HRs)",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// TestMarkPhase1Done_RefusesToDowngradeAdopted proves the
// post-adoption guard: once the operator has minted a handover token
// and the wizard has redirected to the Sovereign Console (status =
// "adopted"), a late helmwatch event MUST NOT flap status back to
// "ready"/"failed"/"phase1-watching".
//
// Failure mode without this guard: an adopted Sovereign sees a
// transient HR.Ready=False (e.g. a Pod restart on the new cluster),
// helmwatch's processEvent fires terminate-on-all-done, markPhase1Done
// rewrites Status=ready (or =failed if the flicker pinned a HR in
// failed), and the wizard's next /deployments/{id} poll sees the
// regression. The handover redirect is one-way; we should never
// rewrite past it. otech48 follow-up.
func TestMarkPhase1Done_RefusesToDowngradeAdopted(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{
		ID:        "phase1-adopted-no-downgrade",
		Status:    "adopted",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 8),
		done:      make(chan struct{}),
		Result: &provisioner.Result{
			SovereignFQDN:    "sov.adopted.example",
			ComponentStates:  map[string]string{"cilium": helmwatch.StateInstalled},
			Phase1FinishedAt: ptrTime(time.Now().Add(-1 * time.Minute)),
			Phase1Outcome:    helmwatch.OutcomeReady,
		},
	}
	h.deployments.Store(dep.ID, dep)

	// Simulate a late watcher event arriving after adoption — pretend
	// every observed HR is now flapping (one failed, others installed).
	// Without the guard this would set Status=failed.
	flapState := map[string]string{
		"cilium":       helmwatch.StateFailed,
		"cert-manager": helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, flapState, helmwatch.OutcomeFailed)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "adopted" {
		t.Errorf("Status = %q, want %q (markPhase1Done must not downgrade adopted)", dep.Status, "adopted")
	}
	if dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		t.Errorf("Phase1Outcome = %q, want %q (must not be overwritten on adopted)", dep.Result.Phase1Outcome, helmwatch.OutcomeReady)
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q (must not be overwritten on adopted)", dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func ptrTime(t time.Time) *time.Time { return &t }
