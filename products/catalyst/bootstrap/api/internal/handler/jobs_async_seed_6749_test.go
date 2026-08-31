package handler

// #6749 — the /jobs read (and the ?inventory=full treemap read that shares the
// handler) must NOT block on the jobs-store seed's multi-region live-cluster
// LISTs. On a converged Sovereign whose cutover was churning a region those
// LISTs hung tens of seconds each and every /jobs call measured 73-98s. The fix
// serves the durable store immediately when it is WARM and refreshes it in a
// background goroutine; only a COLD store still seeds synchronously so the
// first-ever read returns a populated page.
//
// The decisive guard below wires a dynamic client whose `list` verb BLOCKS
// forever, then asserts a warm-store ListJobs still returns promptly with the
// already-seeded rows. Under the old synchronous seed this ServeHTTP would
// deadlock on the blocked LIST and the test would time out — so it fails on the
// regression and passes on the fix.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// blockingListDynamicFactory serves the same list kinds the seed touches, but
// every `list` blocks on `block` until the test releases it. `listStarted` is
// closed (once) the first time a list is attempted, so the test can confirm the
// BACKGROUND refresh really did start (it's parked in List) rather than never
// having been scheduled.
func blockingListDynamicFactory(block <-chan struct{}, listStarted chan<- struct{}, objs ...runtime.Object) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
		scheme := runtime.NewScheme()
		for _, lk := range []schema.GroupVersionKind{
			{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"},
			{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList"},
			{Group: "batch", Version: "v1", Kind: "CronJobList"},
			{Group: "batch", Version: "v1", Kind: "JobList"},
			{Group: "apps", Version: "v1", Kind: "DeploymentList"},
		} {
			scheme.AddKnownTypeWithName(lk, &unstructured.UnstructuredList{})
		}
		c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
			map[schema.GroupVersionResource]string{
				helmwatch.HelmReleaseGVR: "HelmReleaseList",
				{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList",
				{Group: "batch", Version: "v1", Resource: "cronjobs"}:                             "CronJobList",
				{Group: "batch", Version: "v1", Resource: "jobs"}:                                 "JobList",
				{Group: "apps", Version: "v1", Resource: "deployments"}:                           "DeploymentList",
			}, objs...)
		var once bool
		c.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
			if !once {
				once = true
				close(listStarted)
			}
			<-block // park here until the test releases the background refresh
			return false, nil, nil
		})
		return c, nil
	}
}

func TestListJobs_WarmStore_ServesWithoutBlockingOnSeed(t *testing.T) {
	r, _, h := newBackfillRouter(t)
	depID := "dwarm6749"
	dep := makeDeploymentForBackfill(t, h, depID, "apiVersion: v1\nkind: Config\n")

	// 1. Warm the store with a FAST fake cluster so the store-first path is the
	//    one exercised below. bp-cilium becomes a real install row.
	h.dynamicFactory = perOrgDynamicFactory(makeReadyHR("bp-cilium"))
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)
	if !h.jobsStoreHasRows(depID) {
		t.Fatalf("precondition: warm seed produced no rows for %s", depID)
	}

	// 2. Swap in a factory whose LIST blocks forever. A synchronous seed would
	//    now hang ListJobs; the fix must not call it on a warm store.
	block := make(chan struct{})
	listStarted := make(chan struct{})
	h.dynamicFactory = blockingListDynamicFactory(block, listStarted, makeReadyHR("bp-cilium"))
	t.Cleanup(func() { close(block) }) // always release the parked goroutine

	// 3. ?inventory=full is the treemap read (the founder's second surface) AND
	//    it skips FilterFiniteJobs so the install row is visible to assert on.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/v1/deployments/"+depID+"/jobs?inventory=full", nil))
		done <- w
	}()

	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ListJobs blocked >5s on a WARM store — the seed is running on the " +
			"request path instead of in the background (#6749 regression)")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("warm ListJobs: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /jobs: %v", err)
	}
	var sawCilium bool
	names := make([]string, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		n, _ := j["jobName"].(string)
		names = append(names, n)
		if strings.Contains(n, "cilium") {
			sawCilium = true
		}
	}
	if !sawCilium {
		t.Fatalf("warm ListJobs served %d rows but none was the pre-seeded bp-cilium install — "+
			"the store-first read did not return the durable rows. rows=%v", len(body.Jobs), names)
	}

	// 4. The background refresh really was scheduled — it is parked in the
	//    blocked LIST. (Best-effort: the goroutine may still be spinning up.)
	select {
	case <-listStarted:
	case <-time.After(2 * time.Second):
	}
}

func TestKickJobsSeedAsync_SingleflightCoalesces(t *testing.T) {
	_, _, h := newBackfillRouter(t)
	depID := "dsingleflight6749"
	dep := makeDeploymentForBackfill(t, h, depID, "apiVersion: v1\nkind: Config\n")

	// Pre-mark a refresh in flight for this dep. A second kick must be a no-op —
	// no new goroutine, no seed — so a burst of 5s polls can never stack N slow
	// scans. The guard is cleared by whoever holds the slot; here nobody does,
	// so the kick simply returns.
	h.jobsSeedInFlight.Store(dep.ID, struct{}{})
	before := h.jobsStoreHasRows(depID)
	h.kickJobsSeedAsync(dep) // must LoadOrStore-see the slot busy and return
	if got := h.jobsStoreHasRows(depID); got != before {
		t.Fatalf("kickJobsSeedAsync mutated the store while a refresh was already in flight")
	}
	if _, busy := h.jobsSeedInFlight.Load(dep.ID); !busy {
		t.Fatalf("kickJobsSeedAsync cleared another refresh's singleflight slot")
	}
}
