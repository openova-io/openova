// Tests for the retry calls of a deployment (gate check FND-G02, Zero touch;
// internal/retrycall): every retry route appends exactly one entry when it
// accepts a call, the entry survives a reload of the record, and the list
// travels to the Sovereign with the record and is never dropped there.
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/retrycall"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// retryCallStore gives h a deployment store of its own.
func retryCallStore(t *testing.T, h *Handler) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h.store = st
	return st
}

// retryCallsOf returns a copy of dep's retry calls.
func retryCallsOf(dep *Deployment) []store.RetryCall { return dep.retries.List() }

// reloadedRetryCalls reads the record of id back from the store the way a
// restart of the engine does (restoreFromStore → fromRecord) and returns the
// list GET /api/v1/deployments/{id} would serve, and whether it serves one.
func reloadedRetryCalls(t *testing.T, st *store.Store, id string) ([]store.RetryCall, bool) {
	t.Helper()
	rec, err := st.Load(id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	served, ok := fromRecord(rec).State()["retryCalls"]
	if !ok {
		return nil, false
	}
	return served.([]store.RetryCall), true
}

// assertOneRetryCall: dep holds exactly one entry, of route and naming
// target, and the record reloaded from st serves that same entry.
func assertOneRetryCall(t *testing.T, st *store.Store, dep *Deployment, route, target string) {
	t.Helper()
	calls := retryCallsOf(dep)
	if len(calls) != 1 || calls[0].Route != route || !strings.Contains(calls[0].Target, target) || calls[0].At.IsZero() || calls[0].By == "" {
		t.Fatalf("want one retry call of %q naming %q, got %+v", route, target, calls)
	}
	got, ok := reloadedRetryCalls(t, st, dep.ID)
	if !ok || len(got) != 1 || !got[0].At.Equal(calls[0].At) || got[0].Route != calls[0].Route ||
		got[0].Target != calls[0].Target || got[0].By != calls[0].By {
		t.Fatalf("after a reload of the record: want %+v, got %+v (served=%v)", calls, got, ok)
	}
}

func TestRetryCalls_PhaseRetries(t *testing.T) {
	for _, phase := range []string{"tofu-apply", "cilium"} { // phase 0, phase 1
		h := NewWithPDM(silentLogger(), &fakePDM{})
		st := retryCallStore(t, h)
		dep := makeTimedOutDeployment(t, h, "retry-"+phase, true)
		dep.retries.Start()
		// The Phase-0 retry stops at the request's validation, before any
		// OpenTofu run: the branch name is not a valid one.
		dep.Request.GitopsBranch = "not a branch!"

		rr, _ := postRetryPhase(t, h, dep.ID, phase)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d %s", phase, rr.Code, rr.Body.String())
		}
		dep.mu.Lock()
		done := dep.done
		dep.mu.Unlock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the retry did not finish", phase)
		}
		if phase == "tofu-apply" { // its last write is the failed record
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if rec, err := st.Load(dep.ID); err == nil && rec.Status == "failed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the Phase-0 retry never saved its failed record")
				}
			}
		}
		assertOneRetryCall(t, st, dep, retrycall.RoutePhase, "phase "+phase)
	}
}

func TestRetryCalls_Phase1Rewatch(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	st := retryCallStore(t, h)
	h.dynamicFactory = fakeDynamicFactoryFromObjects(makeReadyHR("bp-cilium"))
	h.phase1WatchTimeout = 5 * time.Second
	h.phase1WatchWG = &sync.WaitGroup{}
	h.suppressPostHandoverHooks = true

	// A refused re-watch (a watch is attached) records nothing.
	busy := makeTimedOutDeployment(t, h, "rewatch-busy", true)
	busy.Status = "phase1-watching"
	busy.retries.Start()
	if rr, _ := postRetryPhase(t, h, busy.ID, "phase1-watch"); rr.Code != http.StatusConflict || len(retryCallsOf(busy)) != 0 {
		t.Fatalf("refused re-watch: want 409 and no entry, got %d %+v", rr.Code, retryCallsOf(busy))
	}

	dep := makeTimedOutDeployment(t, h, "rewatch-accepted", true)
	dep.retries.Start()
	if rr, _ := postRetryPhase(t, h, dep.ID, "phase1-watch"); rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	waitCh := make(chan struct{})
	go func() { h.phase1WatchWG.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(20 * time.Second):
		t.Fatalf("re-watch did not terminate within 20s")
	}
	assertOneRetryCall(t, st, dep, retrycall.RoutePhase, "phase phase1-watch")
}

func TestRetryCalls_JobRetry(t *testing.T) {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetName("bp-velero")
	hr.SetNamespace("flux-system")
	r, h, depID := installRetryHarness(t, "owner@t99.omani.works", "install-velero", jobs.StatusFailed, hr)
	st := retryCallStore(t, h)
	val, _ := h.deployments.Load(depID)
	dep := val.(*Deployment)
	dep.retries.Start()

	// A refused retry (another tenant's viewer) records nothing.
	t.Setenv("OPERATOR_EMAIL", "")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-velero", &auth.Claims{Email: "viewer@example.org", Tier: "viewer"}))
	if rec.Code == http.StatusOK || len(retryCallsOf(dep)) != 0 {
		t.Fatalf("refused retry: got %d and entries %+v", rec.Code, retryCallsOf(dep))
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-velero", &auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	assertOneRetryCall(t, st, dep, retrycall.RouteJob, "job install-velero: annotated HelmRelease bp-velero")
	if got := retryCallsOf(dep)[0].By; got != "owner@t99.omani.works" {
		t.Errorf("the entry names %q, want the session's email", got)
	}
}

func TestRetryCalls_ReconcilerReconcileAndResume(t *testing.T) {
	r, h, depID := manageHarness(t, "", helmReleaseObj("bp-keycloak", false, false, "0.4.1"))
	st := retryCallStore(t, h)
	val, _ := h.deployments.Load(depID)
	dep := val.(*Deployment)
	dep.retries.Start()

	for _, action := range []string{"suspend", "reconcile"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", action, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d %s", action, rec.Code, rec.Body.String())
		}
	}
	// suspend runs nothing again; reconcile is one entry.
	assertOneRetryCall(t, st, dep, retrycall.RouteReconciler, "reconcile HelmRelease flux-system/bp-keycloak")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "resume", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if calls := retryCallsOf(dep); len(calls) != 2 || calls[1].Target != "resume HelmRelease flux-system/bp-keycloak" {
		t.Fatalf("resume must be the second entry, got %+v", calls)
	}
}

func TestRetryCalls_RepublishDNS(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	st := retryCallStore(t, h)
	dep := makeDepForRepublish("c986326a77d391d4", "hw126.omantel.biz", "1.2.3.4", []string{"omantel.biz"})
	dep.retries.Start()
	h.deployments.Store(dep.ID, dep)
	h.powerdnsZoneClient = &stubPowerDNSClient{}

	if rr := doRepublish(h, dep.ID); rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	assertOneRetryCall(t, st, dep, retrycall.RouteRepublishDNS, "DNS records of hw126.omantel.biz")
}

func TestRetryCalls_CutoverStart(t *testing.T) {
	const fqdn = "t99.omani.works"
	t.Setenv("SOVEREIGN_FQDN", fqdn)
	h, client := fakeHandlerWithCutover(t,
		makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data:       map[string]string{"cutoverComplete": "false"},
		},
	)
	installJobReactor(t, client, batchv1.JobComplete)
	st := retryCallStore(t, h)
	dep := &Deployment{ID: "dep-cutover", Status: "ready", Request: provisioner.Request{SovereignFQDN: fqdn}}
	dep.retries.Start()
	h.deployments.Store(dep.ID, dep)

	rec := httptest.NewRecorder()
	h.HandleCutoverStart(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assertOneRetryCall(t, st, dep, retrycall.RouteCutoverStart, "cutover chain")
}

func TestRetryCalls_RecordRetryCallByID(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	st := retryCallStore(t, h)
	dep := &Deployment{ID: "dep-registry", Status: "ready"}
	dep.retries.Start()
	h.deployments.Store(dep.ID, dep)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/dep-registry/network-registry", nil)
	req.Header.Set("X-User-Email", "owner@t99.omani.works")

	RecordRetryCall(h, nil, "unknown-id", retrycall.RouteNetworkRegistry, "network registry of the hand-over", req)
	RecordRetryCall(h, nil, dep.ID, retrycall.RouteNetworkRegistry, "network registry of the hand-over", req)
	RecordRetryCall(h, dep, "", retrycall.RouteReconciler, retrycall.ReconcilerTarget("suspend", "HelmRelease", "flux-system", "bp-x"), req)
	assertOneRetryCall(t, st, dep, retrycall.RouteNetworkRegistry, "network registry of the hand-over")
	if got := retryCallsOf(dep)[0].By; got != "owner@t99.omani.works" {
		t.Errorf("the entry names %q, want the session's email", got)
	}
}

// TestRetryCalls_EmptyAndAbsentSurviveAReload: an empty list stays an empty
// list across a save and a load, and a record that never recorded retry calls
// serves no list at all, so absence can never read as "none".
func TestRetryCalls_EmptyAndAbsentSurviveAReload(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	st := retryCallStore(t, h)
	empty := &Deployment{ID: "dep-empty", Status: "ready"}
	empty.retries.Start()
	h.persistDeployment(empty)
	h.persistDeployment(&Deployment{ID: "dep-old", Status: "ready"})

	if got, ok := reloadedRetryCalls(t, st, "dep-empty"); !ok || got == nil || len(got) != 0 {
		t.Fatalf("an empty list must reload as an empty list, got %#v served=%v", got, ok)
	}
	if got, ok := reloadedRetryCalls(t, st, "dep-old"); !ok || got != nil {
		t.Fatalf("a record without a list must serve null, got %#v served=%v", got, ok)
	}
	if raw, _ := json.Marshal(fromRecord(mustLoad(t, st, "dep-old")).State()["retryCalls"]); string(raw) != "null" {
		t.Fatalf("a record without a list must serve retryCalls null, got %s", raw)
	}
}

// mustLoad reads the record of id from st.
func mustLoad(t *testing.T, st *store.Store, id string) store.Record {
	t.Helper()
	rec, err := st.Load(id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return rec
}

// importRecord posts rec to h's import route the way the parent does.
func importRecord(t *testing.T, h *Handler, rec store.Record, query string) {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr := httptest.NewRecorder()
	h.HandleDeploymentImport(rr, httptest.NewRequest(http.MethodPost, "/api/v1/internal/deployments/import"+query, bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("import%s: want 200, got %d %s", query, rr.Code, rr.Body.String())
	}
}

// TestRetryCalls_TravelWithTheHandOver: the record the hand-over exports
// carries the parent's list to the Sovereign; the Sovereign appends its own
// calls; a second import never drops one; a forwarded call merges into the
// held record and leaves its other fields alone.
func TestRetryCalls_TravelWithTheHandOver(t *testing.T) {
	const fqdn = "t99.omani.works"
	t.Setenv("CATALYST_OTECH_FQDN", fqdn)
	at := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	parent := &Deployment{ID: "dep-handover", Status: "ready", Request: provisioner.Request{SovereignFQDN: fqdn}}
	parent.retries.Set([]store.RetryCall{{At: at, Route: retrycall.RoutePhase, Target: "phase phase1-watch", By: "owner@t99.omani.works"}})

	child := NewWithPDM(silentLogger(), &fakePDM{})
	childSt := retryCallStore(t, child)
	importRecord(t, child, parent.toRecord(), "") // what exportDeploymentToChild sends
	held := func() *Deployment {
		val, ok := child.deployments.Load(parent.ID)
		if !ok {
			t.Fatalf("the Sovereign does not hold the record")
		}
		return val.(*Deployment)
	}
	if calls := retryCallsOf(held()); len(calls) != 1 || calls[0].Target != "phase phase1-watch" {
		t.Fatalf("the hand-over did not carry the list: %+v", calls)
	}

	// A retry call on the Sovereign after the hand-over.
	RecordRetryCall(child, held(), "", retrycall.RouteJob, "job install-velero: annotated HelmRelease bp-velero for reconcile", httptest.NewRequest(http.MethodPost, "/", nil))
	// The hand-over fires again with the parent's record: nothing is dropped.
	importRecord(t, child, parent.toRecord(), "")
	if calls := retryCallsOf(held()); len(calls) != 2 {
		t.Fatalf("a second import dropped an entry: %+v", calls)
	}

	// The parent accepts a call after the hand-over and forwards it.
	held().mu.Lock()
	held().Error = "set on the Sovereign"
	held().mu.Unlock()
	parent.retries.Set(append(parent.retries.List(), store.RetryCall{At: at.Add(time.Hour), Route: retrycall.RouteRepublishDNS, Target: "DNS records of " + fqdn, By: "owner@t99.omani.works"}))
	importRecord(t, child, parent.toRecord(), "?"+retrycall.OnlyParam+"=true")
	calls := retryCallsOf(held())
	if len(calls) != 3 || calls[2].Route != retrycall.RouteRepublishDNS {
		t.Fatalf("the forwarded call did not merge: %+v", calls)
	}
	if held().Error != "set on the Sovereign" {
		t.Fatalf("a retryCallsOnly import changed another field of the held record")
	}
	if got, ok := reloadedRetryCalls(t, childSt, parent.ID); !ok || len(got) != 3 {
		t.Fatalf("after a reload of the Sovereign's record: %+v served=%v", got, ok)
	}
}

// TestRetryCalls_ParentForwardsAfterTheHandOver: a call the parent accepts
// after the hand-over fired reaches the Sovereign's record.
func TestRetryCalls_ParentForwardsAfterTheHandOver(t *testing.T) {
	const fqdn = "t99.omani.works"
	t.Setenv("SOVEREIGN_FQDN", "") // this engine is the parent
	t.Setenv("CATALYST_OTECH_FQDN", fqdn)
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "10")
	child := NewWithPDM(silentLogger(), &fakePDM{})
	retryCallStore(t, child)
	srv := httptest.NewServer(http.HandlerFunc(child.HandleDeploymentImport))
	defer srv.Close()
	saved := retrycall.ChildImportURL
	retrycall.ChildImportURL = func(string) string { return srv.URL + "/api/v1/internal/deployments/import" }
	defer func() { retrycall.ChildImportURL = saved }()

	fired := time.Now().UTC()
	parent := NewWithPDM(silentLogger(), &fakePDM{})
	retryCallStore(t, parent)
	dep := &Deployment{ID: "dep-forward", Status: "ready", Request: provisioner.Request{SovereignFQDN: fqdn},
		Result: &provisioner.Result{HandoverFiredAt: &fired}}
	dep.retries.Start()
	parent.deployments.Store(dep.ID, dep)
	RecordRetryCall(parent, dep, "", retrycall.RouteRepublishDNS, "DNS records of "+fqdn, httptest.NewRequest(http.MethodPost, "/", nil))

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if val, ok := child.deployments.Load(dep.ID); ok {
			if calls := retryCallsOf(val.(*Deployment)); len(calls) == 1 && calls[0].Route == retrycall.RouteRepublishDNS {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the Sovereign's record never received the parent's retry call")
}
