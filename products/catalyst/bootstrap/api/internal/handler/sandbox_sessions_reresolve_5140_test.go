// Package handler — sandbox_sessions_reresolve_5140_test.go pins the
// #5140 part-B recovery contract: after a region-kill / DR failover the
// Path-1 (k8sCache Factory) sandbox client can keep dialing a dead or
// stale endpoint forever — the Factory builds it once at AddCluster
// registration and DynamicClientFor never invalidates it. Pre-fix the
// handler degraded every such request to 503 and NEVER recovered, even
// once the DR survivor was serving. sandboxCall now re-resolves a fresh
// in-cluster client (sovereignDepsFor — the local host apiserver, where
// the Sandbox CRD + RBAC are always valid) and retries once, so the
// first request after the survivor takes over returns 200/201.
//
// Wiring mirrors production Path 1: a k8scache.Factory carrying one
// registered cluster whose pre-built dynamic client fails with the
// connection-level class observed live on hw261, resolved through
// resolveChrootClusterID via SOVEREIGN_FQDN — while the re-resolve seam
// (SetSovereignDepsFactory) serves the healthy survivor.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// sandboxFakeDynamicClient builds a fake dynamic client with the
// sandbox list-kind registered, seeded via the same Create-based path
// newSandboxHandler uses (the tracker's objects-arg needs GVK
// heuristics that don't align for the sandbox CRD).
func sandboxFakeDynamicClient(t *testing.T, seed ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		SandboxGVR(): "SandboxList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	for _, obj := range seed {
		if _, err := dyn.Resource(SandboxGVR()).Namespace(obj.GetNamespace()).Create(
			context.Background(), obj, metav1.CreateOptions{},
		); err != nil {
			t.Fatalf("seed Sandbox %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return dyn
}

// deadEndpointDynamicClient returns a fake dynamic client whose every
// verb fails with the connection-level error a cached client dialing a
// dead (post-region-kill) apiserver endpoint produces.
func deadEndpointDynamicClient(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := sandboxFakeDynamicClient(t)
	dyn.PrependReactor("*", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(`Get "https://100.64.0.10:6443/apis/sandbox.openova.io/v1": dial tcp 100.64.0.10:6443: connect: connection refused`)
	})
	return dyn
}

// factoryWithPrimary registers exactly one cluster in a
// k8scache.Factory backed by the supplied pre-built dynamic client —
// the shape of a chroot whose registration-time primary client the
// sandbox handler resolves via Path 1. The empty Registry keeps
// AddCluster from spawning informers; Start is never called, matching
// the handler-side contract that DynamicClientFor works regardless.
func factoryWithPrimary(t *testing.T, clusterID string, dyn *dynamicfake.FakeDynamicClient) *k8scache.Factory {
	t.Helper()
	cfg := k8scache.Config{
		Logger:   silentLogger(),
		Registry: k8scache.NewRegistry(),
		Clusters: []k8scache.ClusterRef{
			{
				ID:            clusterID,
				DynamicClient: dyn,
				CoreClient:    fakek8s.NewSimpleClientset(),
			},
		},
	}
	f, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Stop)
	return f
}

// TestSandboxReResolve_ListRecoversFromDeadPrimary — the part-B
// contract itself: the Path-1 client dials the dead pre-failover
// endpoint, the re-resolve retry lands on the survivor, and the FIRST
// request after the failover returns 200 with the survivor's rows —
// not the pre-fix permanent 503.
func TestSandboxReResolve_ListRecoversFromDeadPrimary(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetK8sCache(factoryWithPrimary(t, "sovereign-t99-omani-works", deadEndpointDynamicClient(t)), k8scache.NewSARCache(), "")

	// The survivor: a healthy apiserver already carrying one Sandbox.
	survivor := sandboxFakeDynamicClient(t,
		mkSandboxCR("sandbox-survivor", "acme", "claude-code", map[string]any{"phase": "Ready"}),
	)
	core := fakek8s.NewSimpleClientset()
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: survivor}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	req = withSandboxClaims(req, "user-sub-dr", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list after failover: status = %d, want 200 (re-resolved survivor); body = %s", rec.Code, rec.Body.String())
	}
	var resp sandboxListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sandboxes) != 1 || resp.Sandboxes[0].ID != "sandbox-survivor" {
		t.Fatalf("sandboxes = %+v, want the survivor's single row", resp.Sandboxes)
	}
	if resp.Sandboxes[0].Status != "running" {
		t.Errorf("status = %q, want running (Ready phase projected)", resp.Sandboxes[0].Status)
	}
}

// TestSandboxReResolve_CreateRecoversFromDeadPrimary — the mutating
// path (the hw261 walk's failing click): POST against the dead primary
// re-resolves and materialises the CR on the survivor, returning 201.
func TestSandboxReResolve_CreateRecoversFromDeadPrimary(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetK8sCache(factoryWithPrimary(t, "sovereign-t99-omani-works", deadEndpointDynamicClient(t)), k8scache.NewSARCache(), "")

	survivor := sandboxFakeDynamicClient(t)
	core := fakek8s.NewSimpleClientset()
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: survivor}, nil
	})

	body, _ := json.Marshal(sandboxCreateRequest{Agent: "aider", Name: "post-failover"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-dr", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create after failover: status = %d, want 201 (re-resolved survivor); body = %s", rec.Code, rec.Body.String())
	}

	// The CR must exist on the SURVIVOR's apiserver.
	got, err := survivor.Resource(SandboxGVR()).Namespace("acme").Get(
		context.Background(), "post-failover", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("survivor Get after create: %v", err)
	}
	if got.GetName() != "post-failover" {
		t.Fatalf("survivor CR name = %q, want post-failover", got.GetName())
	}
}

// TestSandboxReResolve_BothPathsDeadStays503 — when the re-resolved
// client is ALSO unreachable (survivor not serving yet), the surface
// keeps the honest part-A contract: 503 sandbox-backend-unavailable,
// never a 500 and never a hang.
func TestSandboxReResolve_BothPathsDeadStays503(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetK8sCache(factoryWithPrimary(t, "sovereign-t99-omani-works", deadEndpointDynamicClient(t)), k8scache.NewSARCache(), "")

	core := fakek8s.NewSimpleClientset()
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: deadEndpointDynamicClient(t)}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	req = withSandboxClaims(req, "user-sub-dr", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("both paths dead: status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "sandbox-backend-unavailable" {
		t.Errorf("error = %v, want sandbox-backend-unavailable", resp["error"])
	}
}

// TestSandboxReResolve_NoRetryOnObjectNotFound — a genuine per-object
// 404 from a HEALTHY primary maps straight to 404 without paying a
// re-resolve call: the retry class is connection-level only, so
// ordinary misses keep single-call latency.
func TestSandboxReResolve_NoRetryOnObjectNotFound(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetK8sCache(factoryWithPrimary(t, "sovereign-t99-omani-works", sandboxFakeDynamicClient(t)), k8scache.NewSARCache(), "")

	reResolves := 0
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		reResolves++
		return &sovereignDeps{core: fakek8s.NewSimpleClientset(), dyn: sandboxFakeDynamicClient(t)}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/no-such-sandbox", nil)
	req = withSandboxClaims(req, "user-sub-dr", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("healthy-primary object miss: status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	if reResolves != 0 {
		t.Errorf("re-resolve invoked %d times on a genuine object 404, want 0", reResolves)
	}
}
