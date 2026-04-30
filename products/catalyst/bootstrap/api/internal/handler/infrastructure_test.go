// infrastructure_test.go — coverage for the Sovereign Infrastructure
// REST surface.
//
// The unified GET .../infrastructure/topology emits the hierarchical
// TopologyResponse shape (cloud → topology.regions[*] → clusters →
// vclusters | pools | nodes | LBs + storage). The legacy flat
// /compute, /storage, /network endpoints remain wired with their
// pre-existing shapes until the FE migrates to the unified topology.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/infrastructure"
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
			HetznerProjectID: "test-project",
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

// TestInfrastructureTopology_OKShape pins the unified hierarchical
// shape: cloud, topology.regions[*].clusters[*].nodes[*]/pools[*]/LBs,
// storage. The legacy flat nodes/edges shape is no longer emitted.
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
	var out infrastructure.TopologyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Cloud — exactly one tenant per provider.
	if len(out.Cloud) != 1 {
		t.Fatalf("expected 1 cloud tenant; got %d", len(out.Cloud))
	}
	if out.Cloud[0].Provider != "hetzner" {
		t.Fatalf("cloud provider: got %q want hetzner", out.Cloud[0].Provider)
	}
	if out.Cloud[0].ProjectID != "test-project" {
		t.Fatalf("cloud projectID: got %q want test-project", out.Cloud[0].ProjectID)
	}

	// Topology — pattern + 1 region (legacy singular path).
	if out.Topology.Pattern == "" {
		t.Fatalf("topology pattern is required")
	}
	if len(out.Topology.Regions) != 1 {
		t.Fatalf("expected 1 region; got %d", len(out.Topology.Regions))
	}
	rg := out.Topology.Regions[0]
	if rg.ProviderRegion != "fsn1" {
		t.Fatalf("region.providerRegion: got %q want fsn1", rg.ProviderRegion)
	}
	if rg.SkuCP != "cpx21" {
		t.Fatalf("region.skuCP: got %q want cpx21", rg.SkuCP)
	}
	if len(rg.Clusters) != 1 {
		t.Fatalf("expected 1 cluster per region; got %d", len(rg.Clusters))
	}
	c := rg.Clusters[0]
	if c.Name != "omantel.omani.works" {
		t.Fatalf("cluster.name: got %q want omantel.omani.works", c.Name)
	}
	// 1 cp + 2 workers
	if len(c.Nodes) != 3 {
		t.Fatalf("expected 3 nodes (1 cp + 2 workers); got %d", len(c.Nodes))
	}
	if len(c.LoadBalancers) != 1 {
		t.Fatalf("expected 1 LB when LoadBalancerIP set; got %d", len(c.LoadBalancers))
	}
	if c.LoadBalancers[0].PublicIP != "203.0.113.10" {
		t.Fatalf("lb publicIP: got %q want 203.0.113.10", c.LoadBalancers[0].PublicIP)
	}
	// node pools: 1 cp pool + 1 worker pool
	if len(c.NodePools) != 2 {
		t.Fatalf("expected 2 node pools (cp + worker); got %d", len(c.NodePools))
	}
}

// TestInfrastructureTopology_NoLBWhenAbsent — pre-LB-reconcile
// deployment must not surface a synthesised LB row.
func TestInfrastructureTopology_NoLBWhenAbsent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "provisioning")
	rec := callInfra(t, h, http.MethodGet, "topology", id, h.GetInfrastructureTopology)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out infrastructure.TopologyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Topology.Regions) != 1 {
		t.Fatalf("expected 1 region; got %d", len(out.Topology.Regions))
	}
	if len(out.Topology.Regions[0].Clusters) == 0 {
		t.Fatal("expected 1 cluster")
	}
	if len(out.Topology.Regions[0].Clusters[0].LoadBalancers) != 0 {
		t.Fatalf("expected no LBs before LoadBalancerIP reported; got %+v", out.Topology.Regions[0].Clusters[0].LoadBalancers)
	}
}

// TestInfrastructureTopology_StorageEmptyFallback — storage arrays
// MUST serialise as `[]` (never null) so the FE can iterate them.
func TestInfrastructureTopology_StorageEmptyFallback(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "ready")
	rec := callInfra(t, h, http.MethodGet, "topology", id, h.GetInfrastructureTopology)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"pvcs":[]`) {
		t.Fatalf("storage.pvcs must serialise as []; body=%s", body)
	}
	if !strings.Contains(body, `"buckets":[]`) {
		t.Fatalf("storage.buckets must serialise as []; body=%s", body)
	}
	if !strings.Contains(body, `"volumes":[]`) {
		t.Fatalf("storage.volumes must serialise as []; body=%s", body)
	}
}

// TestInfrastructureTopology_PeeringsEmptyByDefault — when no
// PeeringClaim XRCs exist, the loader emits an empty peerings array.
func TestInfrastructureTopology_PeeringsEmptyByDefault(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	_, id := installInfraDeployment(t, h, "ready")
	rec := callInfra(t, h, http.MethodGet, "topology", id, h.GetInfrastructureTopology)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	var out infrastructure.TopologyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, rg := range out.Topology.Regions {
		for _, n := range rg.Networks {
			if n.Peerings == nil {
				t.Fatalf("network.peerings must be [] not null")
			}
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
