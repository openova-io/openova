// infrastructure_test.go — coverage for the Sovereign Infrastructure
// REST surface. Pins the wire shape every endpoint emits + the 404
// path so the UI's contract stays stable as the data sources evolve
// (today: deployment record only; future: live-cluster kubeconfig).
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// installInfraDeployment seeds a synthetic deployment into the
// handler's in-memory map and returns the id. Tests mutate
// dep.Result fields to exercise the LB / no-LB branches.
func installInfraDeployment(t *testing.T, h *Handler, status string) (*Deployment, string) {
	t.Helper()
	id := "dep-infra-test"
	dep := &Deployment{
		ID:     id,
		Status: status,
		Request: provisioner.Request{
			SovereignFQDN:    "omantel.omani.works",
			Region:           "fsn1",
			ControlPlaneSize: "cpx21",
			WorkerSize:       "cpx41",
			WorkerCount:      2,
		},
		mu: sync.Mutex{},
	}
	h.deployments.Store(id, dep)
	return dep, id
}

// callInfra wires a chi router with the depId path param, executes
// the request through it (so chi.URLParam("depId") resolves), and
// returns the recorder.
func callInfra(t *testing.T, h *Handler, method, suffix, depID string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Method(method, "/api/v1/deployments/{depId}/infrastructure/"+suffix, handler)
	req := httptest.NewRequest(method, "/api/v1/deployments/"+depID+"/infrastructure/"+suffix, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestInfrastructureTopology_NotFound(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callInfra(t, h, http.MethodGet, "topology", "ghost", h.GetInfrastructureTopology)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "deployment-not-found") {
		t.Fatalf("expected deployment-not-found in body; got %s", rec.Body.String())
	}
}

func TestInfrastructureTopology_OKShape(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep, id := installInfraDeployment(t, h, "ready")
	dep.Result = &provisioner.Result{
		SovereignFQDN:  "omantel.omani.works",
		ControlPlaneIP: "5.6.7.8",
		LoadBalancerIP: "203.0.113.10",
	}

	rec := callInfra(t, h, http.MethodGet, "topology", id, h.GetInfrastructureTopology)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraTopologyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Nodes) == 0 {
		t.Fatalf("expected non-empty nodes")
	}
	if len(out.Edges) == 0 {
		t.Fatalf("expected non-empty edges")
	}

	// Cloud + cluster + LB + workers must all surface. Spot-check kinds.
	kinds := map[string]int{}
	for _, n := range out.Nodes {
		kinds[n.Kind]++
	}
	if kinds["cloud"] != 1 {
		t.Fatalf("expected 1 cloud node; got %d", kinds["cloud"])
	}
	if kinds["cluster"] != 1 {
		t.Fatalf("expected 1 cluster node; got %d", kinds["cluster"])
	}
	if kinds["lb"] != 1 {
		t.Fatalf("expected 1 lb node when LoadBalancerIP set; got %d", kinds["lb"])
	}
	// At least the workers + control plane.
	if kinds["node"] < 3 {
		t.Fatalf("expected >=3 node entries (1 cp + 2 workers); got %d", kinds["node"])
	}
}

func TestInfrastructureTopology_NoLBWhenAbsent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "provisioning")
	rec := callInfra(t, h, http.MethodGet, "topology", id, h.GetInfrastructureTopology)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraTopologyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, n := range out.Nodes {
		if n.Kind == "lb" {
			t.Fatalf("expected no lb node before LoadBalancerIP is reported; got %+v", n)
		}
	}
}

func TestInfrastructureCompute_NotFound(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callInfra(t, h, http.MethodGet, "compute", "ghost", h.GetInfrastructureCompute)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestInfrastructureCompute_OK(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "ready")
	rec := callInfra(t, h, http.MethodGet, "compute", id, h.GetInfrastructureCompute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraComputeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Clusters) != 1 {
		t.Fatalf("expected exactly 1 cluster; got %d", len(out.Clusters))
	}
	if len(out.Nodes) != 3 { // 1 cp + 2 workers
		t.Fatalf("expected 3 nodes (cp + 2 workers); got %d", len(out.Nodes))
	}
	c := out.Clusters[0]
	if c.Name != "omantel.omani.works" {
		t.Fatalf("cluster name: got %q want omantel.omani.works", c.Name)
	}
	if c.NodeCount != 3 {
		t.Fatalf("cluster node count: got %d want 3", c.NodeCount)
	}
}

func TestInfrastructureStorage_OKEmpty(t *testing.T) {
	// Storage queries the live cluster, which isn't wired yet. The
	// handler MUST return a well-shaped empty response (not placeholder
	// data) per the file-header contract.
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "ready")
	rec := callInfra(t, h, http.MethodGet, "storage", id, h.GetInfrastructureStorage)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.PVCs) != 0 || len(out.Buckets) != 0 || len(out.Volumes) != 0 {
		t.Fatalf("expected empty arrays for live-cluster sourced data; got %+v", out)
	}
	// JSON arrays MUST be `[]` not `null` so the UI can iterate them.
	body := rec.Body.String()
	if !strings.Contains(body, `"pvcs":[]`) {
		t.Fatalf("pvcs field must serialise as `[]`, got body=%s", body)
	}
}

func TestInfrastructureStorage_NotFound(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callInfra(t, h, http.MethodGet, "storage", "ghost", h.GetInfrastructureStorage)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestInfrastructureNetwork_OKWithLB(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep, id := installInfraDeployment(t, h, "ready")
	dep.Result = &provisioner.Result{LoadBalancerIP: "203.0.113.10"}

	rec := callInfra(t, h, http.MethodGet, "network", id, h.GetInfrastructureNetwork)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraNetworkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.LoadBalancers) != 1 {
		t.Fatalf("expected 1 LB; got %d", len(out.LoadBalancers))
	}
	if out.LoadBalancers[0].PublicIP != "203.0.113.10" {
		t.Fatalf("LB publicIP: got %q want 203.0.113.10", out.LoadBalancers[0].PublicIP)
	}
}

func TestInfrastructureNetwork_OKEmpty(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "provisioning")
	rec := callInfra(t, h, http.MethodGet, "network", id, h.GetInfrastructureNetwork)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infraNetworkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.LoadBalancers) != 0 {
		t.Fatalf("expected 0 LBs before LB IP reported; got %d", len(out.LoadBalancers))
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"loadBalancers":[]`) ||
		!strings.Contains(body, `"drgs":[]`) ||
		!strings.Contains(body, `"peerings":[]`) {
		t.Fatalf("network arrays must serialise as `[]`, got body=%s", body)
	}
}
