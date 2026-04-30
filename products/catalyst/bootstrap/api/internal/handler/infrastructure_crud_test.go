// infrastructure_crud_test.go — coverage for the Day-2 mutation
// endpoints (POST/PATCH/DELETE) that write Crossplane XRCs against
// the Sovereign cluster's dynamic client.
//
// The fake dynamic client is seeded with the right list-kinds for
// each XRC kind so the catalyst-api's create call returns a typed
// success without hitting a real apiserver. Tests assert:
//
//   - 202 happy path: response carries jobId + xrcKind + xrcName + status
//   - 404 unknown deployment
//   - 409 conflict when same XRC name already exists
//   - 503 when the sovereign cluster is unreachable (no kubeconfig)
//   - DELETE returns the cascade preview
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/infrastructure"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// xrcListKinds — every Composite Resource Claim kind the CRUD
// handlers can write. Mirrors the Kind* constants in
// internal/infrastructure/xrc.go. Tests register the matching
// list-kind names so the fake dynamic client's List+Create paths
// behave correctly.
func xrcListKinds() map[schema.GroupVersionResource]string {
	mk := func(plural string) schema.GroupVersionResource {
		return schema.GroupVersionResource{
			Group:    infrastructure.XRCAPIGroup,
			Version:  infrastructure.XRCAPIVersion,
			Resource: plural,
		}
	}
	out := map[schema.GroupVersionResource]string{
		mk("regionclaims"):       "RegionClaimList",
		mk("clusterclaims"):      "ClusterClaimList",
		mk("vclusterclaims"):     "VClusterClaimList",
		mk("nodepoolclaims"):     "NodePoolClaimList",
		mk("loadbalancerclaims"): "LoadBalancerClaimList",
		mk("peeringclaims"):      "PeeringClaimList",
		mk("firewallruleclaims"): "FirewallRuleClaimList",
		mk("nodeactionclaims"):   "NodeActionClaimList",
	}
	// The DELETE handler calls infrastructure.Load to compute the
	// cascade preview, which queries vcluster.io/v1alpha1/vclusters
	// and core/v1/persistentvolumeclaims. Register those kinds with
	// the fake client so List doesn't panic on "unregistered list
	// kind". Production hits a real apiserver that either has the
	// CRD or returns 404 — both code paths return gracefully.
	out[schema.GroupVersionResource{Group: "vcluster.io", Version: "v1alpha1", Resource: "vclusters"}] = "VClusterList"
	out[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}] = "PersistentVolumeClaimList"
	return out
}

// fakeXRCDynamicFactory — closure factory the handler reads via
// h.dynamicFactory. Returns a single fake client seeded with the
// xrcListKinds map; tests can append additional unstructured
// objects to simulate pre-existing claims (for the 409 conflict
// path).
func fakeXRCDynamicFactory(seed ...runtime.Object) func(string) (dynamic.Interface, error) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, xrcListKinds(), seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}
}

// installCRUDDeployment — like installInfraDeployment but with a
// Result.KubeconfigPath pointing at a temp file so
// sovereignDynamicClient resolves to the injected fake. Each test
// gets its own deployment id so concurrent tests don't share a
// fake apiserver.
func installCRUDDeployment(t *testing.T, h *Handler, id string) *Deployment {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".yaml")
	// Kubeconfig contents are ignored by the fake factory, but the
	// file must exist + be readable. Per
	// docs/INVIOLABLE-PRINCIPLES.md #10 the fake content carries no
	// real credentials.
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	dep := &Deployment{
		ID:     id,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN:    "omantel.omani.works",
			Region:           "fsn1",
			ControlPlaneSize: "cpx21",
			WorkerSize:       "cpx41",
			WorkerCount:      2,
			HetznerProjectID: "test-project",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "omantel.omani.works",
			ControlPlaneIP: "5.6.7.8",
			LoadBalancerIP: "203.0.113.10",
			KubeconfigPath: path,
		},
		mu: sync.Mutex{},
	}
	h.deployments.Store(id, dep)
	return dep
}

// callCRUDInfra fires a request through a freshly-built chi router
// that knows the depId + nested path params. Returns the recorder.
func callCRUDInfra(t *testing.T, h *Handler, method, suffix string, depID string, body any, register func(r chi.Router, h *Handler)) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r, h)
	var buf *bytes.Buffer
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/deployments/"+depID+"/infrastructure/"+suffix, buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func mustDecodeMutation(t *testing.T, rec *httptest.ResponseRecorder) infrastructure.MutationResponse {
	t.Helper()
	var out infrastructure.MutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode mutation: %v body=%s", err, rec.Body.String())
	}
	return out
}

/* ── POST /infrastructure/regions ────────────────────────────── */

func TestCreateRegion_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-region-happy")

	body := map[string]any{
		"region":      "hel1",
		"skuCP":       "cpx21",
		"skuWorker":   "cpx41",
		"workerCount": 2,
	}
	rec := callCRUDInfra(t, h, http.MethodPost, "regions", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindRegionClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindRegionClaim)
	}
	if !strings.Contains(out.XRCName, "region-hel1") {
		t.Fatalf("xrcName must contain 'region-hel1': got %q", out.XRCName)
	}
	if out.JobID == "" {
		t.Fatalf("jobId must be set")
	}
	if out.Status != "submitted-pending-composition" {
		t.Fatalf("status: got %q want submitted-pending-composition", out.Status)
	}
}

func TestCreateRegion_NotFound(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	rec := callCRUDInfra(t, h, http.MethodPost, "regions", "ghost",
		map[string]any{"region": "hel1", "skuCP": "cpx21"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRegion_503WhenKubeconfigMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	// Build a deployment WITHOUT a kubeconfig path so the
	// sovereignDynamicClient short-circuits with 503.
	dep := &Deployment{
		ID:     "dep-no-kubeconfig",
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN:    "x.example",
			Region:           "fsn1",
			ControlPlaneSize: "cpx21",
		},
		Result: &provisioner.Result{
			// Intentionally empty KubeconfigPath
		},
		mu: sync.Mutex{},
	}
	h.deployments.Store(dep.ID, dep)

	rec := callCRUDInfra(t, h, http.MethodPost, "regions", dep.ID,
		map[string]any{"region": "hel1", "skuCP": "cpx21"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sovereign-cluster-unreachable") {
		t.Fatalf("expected sovereign-cluster-unreachable body; got %s", rec.Body.String())
	}
}

func TestCreateRegion_409Conflict(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	// Pre-seed the fake dynamic client with the EXACT same XRC the
	// handler will compute from (depID, "region", "hel1"). The
	// second create() then returns AlreadyExists which the helper
	// surfaces as ErrXRCNameConflict → HTTP 409.
	depID := "dep-region-conflict"
	wantName := infrastructure.XRCName(depID, "region", "hel1")
	existing := newUnstructuredXRC(infrastructure.KindRegionClaim, wantName)
	h.dynamicFactory = fakeXRCDynamicFactory(existing)

	dep := installCRUDDeployment(t, h, depID)

	rec := callCRUDInfra(t, h, http.MethodPost, "regions", dep.ID,
		map[string]any{"region": "hel1", "skuCP": "cpx21"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "xrc-name-conflict") {
		t.Fatalf("expected xrc-name-conflict; got %s", rec.Body.String())
	}
}

/* ── POST /infrastructure/regions/{id}/clusters ──────────────── */

func TestCreateCluster_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-cluster-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "regions/region-fsn1/clusters", dep.ID,
		map[string]any{"name": "edge-1", "version": "v1.30", "ha": false},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/regions/{id}/clusters", h.CreateInfrastructureCluster)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindClusterClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindClusterClaim)
	}
}

/* ── POST /infrastructure/clusters/{id}/vclusters ────────────── */

func TestCreateVCluster_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-vcluster-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "clusters/cluster-x/vclusters", dep.ID,
		map[string]any{"name": "dmz", "namespace": "dmz", "role": "dmz"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/vclusters", h.CreateInfrastructureVCluster)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindVClusterClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindVClusterClaim)
	}
}

/* ── POST /infrastructure/clusters/{id}/pools ────────────────── */

func TestCreatePool_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-pool-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "clusters/cluster-x/pools", dep.ID,
		map[string]any{"name": "gpu-1", "role": "worker", "sku": "cpx51", "region": "fsn1", "desiredSize": 3},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/pools", h.CreateInfrastructurePool)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindNodePoolClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindNodePoolClaim)
	}
}

/* ── PATCH /infrastructure/pools/{id} ─────────────────────────── */

func TestPatchPool_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-pool-patch")

	size := 5
	rec := callCRUDInfra(t, h, http.MethodPatch, "pools/gpu-1", dep.ID,
		map[string]any{"desiredSize": size},
		func(r chi.Router, h *Handler) {
			r.Patch("/api/v1/deployments/{depId}/infrastructure/pools/{id}", h.PatchInfrastructurePool)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindNodePoolClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindNodePoolClaim)
	}
}

func TestPatchPool_ConflictBecomes202(t *testing.T) {
	// PATCH semantics: an existing claim with the same name is the
	// expected case (PATCH targets convergence). Handler must NOT
	// surface 409 for the patch path — it's 202.
	h := NewWithPDM(silentLogger(), &fakePDM{})
	depID := "dep-pool-patch-conflict"
	xrcName := infrastructure.XRCName(depID, "pool", "gpu-1")
	existing := newUnstructuredXRC(infrastructure.KindNodePoolClaim, xrcName)
	h.dynamicFactory = fakeXRCDynamicFactory(existing)
	dep := installCRUDDeployment(t, h, depID)

	size := 5
	rec := callCRUDInfra(t, h, http.MethodPatch, "pools/gpu-1", dep.ID,
		map[string]any{"desiredSize": size},
		func(r chi.Router, h *Handler) {
			r.Patch("/api/v1/deployments/{depId}/infrastructure/pools/{id}", h.PatchInfrastructurePool)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST /infrastructure/loadbalancers ──────────────────────── */

func TestCreateLB_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-lb-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "loadbalancers", dep.ID,
		map[string]any{"name": "edge-lb", "region": "hel1", "ports": "443"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/loadbalancers", h.CreateInfrastructureLoadBalancer)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST /infrastructure/peerings ────────────────────────────── */

func TestCreatePeering_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-peering-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "peerings", dep.ID,
		map[string]any{"name": "fsn1-hel1", "vpcFrom": "vpc-fsn1", "vpcTo": "vpc-hel1", "subnets": "10.0.0.0/16<>10.1.0.0/16"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/peerings", h.CreateInfrastructurePeering)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST /infrastructure/firewalls/{id}/rules ───────────────── */

func TestCreateFirewallRule_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-fw-happy")

	rec := callCRUDInfra(t, h, http.MethodPost, "firewalls/fw-1/rules", dep.ID,
		map[string]any{"direction": "in", "protocol": "tcp", "port": "443", "sources": "0.0.0.0/0", "action": "accept"},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/firewalls/{id}/rules", h.CreateInfrastructureFirewallRule)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST /infrastructure/nodes/{id}/{action} ─────────────────── */

func TestCreateNodeAction_HappyDrain(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-node-drain")

	rec := callCRUDInfra(t, h, http.MethodPost, "nodes/node-w-0/drain", dep.ID, nil,
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/nodes/{id}/{action}", h.CreateInfrastructureNodeAction)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindNodeActionClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindNodeActionClaim)
	}
}

func TestCreateNodeAction_BadAction(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-node-bad")

	rec := callCRUDInfra(t, h, http.MethodPost, "nodes/node-w-0/yeet", dep.ID, nil,
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/nodes/{id}/{action}", h.CreateInfrastructureNodeAction)
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

/* ── DELETE /infrastructure/{kind}/{id} ───────────────────────── */

func TestDeleteResource_HappyWithCascade(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})

	// Pre-seed an XRC the DELETE call can find. The CascadeFor
	// helper composes the cascade rows from the LIVE topology
	// (which today doesn't include the XRC's children — it pulls
	// from the deployment record), so the cascade always emits at
	// least one descriptor row.
	depID := "dep-region-delete"
	xrcName := infrastructure.XRCName(depID, "region", "fsn1")
	existing := newUnstructuredXRC(infrastructure.KindRegionClaim, xrcName)
	h.dynamicFactory = fakeXRCDynamicFactory(existing)

	dep := installCRUDDeployment(t, h, depID)

	rec := callCRUDInfra(t, h, http.MethodDelete, "regions/fsn1", dep.ID, nil,
		func(r chi.Router, h *Handler) {
			r.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindRegionClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindRegionClaim)
	}
	if len(out.Cascade) == 0 {
		t.Fatalf("expected non-empty cascade preview; got %v", out.Cascade)
	}
}

func TestDeleteResource_UnknownKind(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-bad-kind")

	rec := callCRUDInfra(t, h, http.MethodDelete, "widgets/foo", dep.ID, nil,
		func(r chi.Router, h *Handler) {
			r.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteResource_AlreadyAbsent(t *testing.T) {
	// Delete returning NotFound is treated as "already gone" — the
	// audit Job is still committed and the response is 202 with
	// status="already-absent" so the FE can re-render.
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-region-already-gone")

	rec := callCRUDInfra(t, h, http.MethodDelete, "regions/fsn1", dep.ID, nil,
		func(r chi.Router, h *Handler) {
			r.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.Status != "already-absent" {
		t.Fatalf("status: got %q want already-absent", out.Status)
	}
}

/* ── Audit-trail end-to-end: mutation Job materialised ───────── */

// TestCreateRegion_AuditJobMaterialised — after a successful create,
// the catalyst-api MUST have committed a Job + Execution + LogLines
// to the jobs Store under the current deployment id. This is the
// audit-trail invariant: every Day-2 mutation is observable via the
// existing /jobs surface.
func TestCreateRegion_AuditJobMaterialised(t *testing.T) {
	dir := t.TempDir()
	js, err := jobs.NewStore(dir)
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h := NewWithJobsStore(silentLogger(), js)
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-audit-region")

	rec := callCRUDInfra(t, h, http.MethodPost, "regions", dep.ID,
		map[string]any{"region": "hel1", "skuCP": "cpx21", "workerCount": 2},
		func(r chi.Router, h *Handler) {
			r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}

	jobsList, err := js.ListJobs(dep.ID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobsList) == 0 {
		t.Fatalf("expected at least one mutation Job committed; got none")
	}
	found := false
	for _, j := range jobsList {
		if strings.HasPrefix(j.JobName, jobs.MutationJobNamePrefix) && j.BatchID == jobs.BatchDay2Mutations {
			found = true
			if j.Status != jobs.StatusSucceeded {
				t.Fatalf("mutation Job status: got %q want %q", j.Status, jobs.StatusSucceeded)
			}
		}
	}
	if !found {
		t.Fatalf("expected a Job with name prefix %q and batch %q; got %+v", jobs.MutationJobNamePrefix, jobs.BatchDay2Mutations, jobsList)
	}
}

/* ── Helpers ──────────────────────────────────────────────────── */

// newUnstructuredXRC builds a bare XRC unstructured suitable for
// pre-seeding the fake dynamic client. Only metadata.name +
// apiVersion + kind are populated; tests don't assert on .spec
// content.
func newUnstructuredXRC(kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(infrastructure.XRCAPIGroup + "/" + infrastructure.XRCAPIVersion)
	u.SetKind(kind)
	u.SetNamespace(infrastructure.XRCNamespace)
	u.SetName(name)
	return u
}

// silenceUnused — keep the metav1 + context imports anchored even
// when the test file's surface evolves.
var (
	_ = metav1.ObjectMeta{}
	_ = context.Background
)
