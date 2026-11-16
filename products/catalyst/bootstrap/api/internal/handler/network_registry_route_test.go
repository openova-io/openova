package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	registryOwner = "owner@example.org"
	registryDep   = "dddd000000000005"
)

// postNetworkRegistry sends POST /api/v1/deployments/{id}/network-registry
// as the session email (none when "") and decodes the body.
func postNetworkRegistry(t *testing.T, h *Handler, id, email string) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/v1/deployments/{id}/network-registry", h.HandleNetworkRegistry)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/"+id+"/network-registry", nil)
	if email != "" {
		req.Header.Set("X-User-Email", email)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d, body %q: %v", rec.Code, rec.Body.String(), err)
	}
	return rec.Code, body
}

// registryRouteHandler is a parent holding one deployment of registryOwner,
// in status, with its OpenTofu state, and a cluster cache over regions.
func registryRouteHandler(t *testing.T, status string, regions map[string]*dynamicfake.FakeDynamicClient) *Handler {
	t.Helper()
	t.Setenv("SOVEREIGN_FQDN", "")
	workDir := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", workDir)
	writeRegistryState(t, workDir, registryDep, registryDoc)
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.deployments.Store(registryDep, &Deployment{ID: registryDep, Status: status, OwnerEmail: registryOwner})
	// Wired directly, not through SetK8sCache, so the start pass does not
	// race the request under test.
	h.k8sCache = registryCache(t, regions)
	return h
}

// TestNetworkRegistryRouteRefusals: an unknown id and another user's session
// answer 404, a deployment that is not ready 409, a deployment without state
// 422, a parent without its cluster cache 503 — and none makes a call.
func TestNetworkRegistryRouteRefusals(t *testing.T) {
	region := registryRegion()
	regions := map[string]*dynamicfake.FakeDynamicClient{registryDep: region}

	h := registryRouteHandler(t, "ready", regions)
	if code, body := postNetworkRegistry(t, h, "eeee000000000006", registryOwner); code != http.StatusNotFound || body["error"] != "deployment not found" {
		t.Errorf("unknown id: %d %v", code, body)
	}
	if code, body := postNetworkRegistry(t, h, registryDep, "someone@example.org"); code != http.StatusNotFound || body["error"] != "deployment not found" {
		t.Errorf("another user's session: %d %v", code, body)
	}

	for _, status := range []string{"phase1-watching", "provisioning", "failed", "wiped"} {
		h := registryRouteHandler(t, status, regions)
		if code, body := postNetworkRegistry(t, h, registryDep, registryOwner); code != http.StatusConflict ||
			body["error"] != "deployment-not-ready" || body["status"] != status {
			t.Errorf("status %s: %d %v", status, code, body)
		}
	}

	h = registryRouteHandler(t, "ready", regions)
	t.Setenv("CATALYST_TOFU_WORKDIR", t.TempDir())
	if code, body := postNetworkRegistry(t, h, registryDep, registryOwner); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body["detail"].(string), "no OpenTofu state") {
		t.Errorf("no state: %d %v", code, body)
	}

	h = registryRouteHandler(t, "ready", map[string]*dynamicfake.FakeDynamicClient{"ffff000000000009": registryRegion()})
	if code, body := postNetworkRegistry(t, h, registryDep, registryOwner); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body["detail"].(string), "no cluster of deployment "+registryDep) {
		t.Errorf("no cluster of the deployment: %d %v", code, body)
	}

	h = registryRouteHandler(t, "ready", regions)
	h.k8sCache = nil
	if code, body := postNetworkRegistry(t, h, registryDep, registryOwner); code != http.StatusServiceUnavailable || body["error"] != "cluster-cache-not-wired" {
		t.Errorf("no cache: %d %v", code, body)
	}

	if got := regionCalls(region); len(got) != 0 {
		t.Fatalf("a refused request made calls %q", got)
	}
}

// TestNetworkRegistryRouteWritesEveryRegion: for a ready deployment the route
// writes the registry into every region where it is absent or another, only
// reads it where it is current, leaves the regions of another deployment
// alone, and a second call writes nothing.
func TestNetworkRegistryRouteWritesEveryRegion(t *testing.T) {
	rev := registryRevision(registryDoc)
	regions := map[string]*dynamicfake.FakeDynamicClient{
		registryDep:               registryRegion(),                                        // absent
		registryDep + "-region-b": registryRegion(registryConfigMap(`{"schema":"older"}`)), // another revision
		registryDep + "-region-c": registryRegion(registryConfigMap(registryDoc)),          // current
		"ffff000000000009":        registryRegion(),                                        // another deployment
	}
	h := registryRouteHandler(t, "ready", regions)

	code, body := postNetworkRegistry(t, h, registryDep, registryOwner)
	if code != http.StatusOK || body["deploymentId"] != registryDep || body["revision"] != rev {
		t.Fatalf("%d %v", code, body)
	}
	want := []any{
		map[string]any{"cluster": registryDep, "written": true},
		map[string]any{"cluster": registryDep + "-region-b", "written": true},
		map[string]any{"cluster": registryDep + "-region-c", "written": false},
	}
	if !reflect.DeepEqual(body["regions"], want) {
		t.Fatalf("regions %v", body["regions"])
	}
	for id, calls := range map[string][]string{
		registryDep:               {registryGet, registryPtch},
		registryDep + "-region-b": {registryGet, registryPtch},
		registryDep + "-region-c": {registryGet},
		"ffff000000000009":        nil,
	} {
		if got := regionCalls(regions[id]); !reflect.DeepEqual(got, calls) {
			t.Errorf("%s: calls %q, want %q", id, got, calls)
		}
	}
	for _, id := range []string{registryDep, registryDep + "-region-b", registryDep + "-region-c"} {
		if got := heldRevision(regions[id]); got != rev {
			t.Errorf("%s holds %q", id, got)
		}
		regions[id].ClearActions()
	}

	// The second call finds every region current.
	code, body = postNetworkRegistry(t, h, registryDep, registryOwner)
	for _, r := range body["regions"].([]any) {
		if r.(map[string]any)["written"] != false {
			t.Errorf("second call wrote: %v", r)
		}
	}
	if code != http.StatusOK {
		t.Fatalf("second call: %d %v", code, body)
	}
	for _, id := range []string{registryDep, registryDep + "-region-b", registryDep + "-region-c"} {
		if got := regionCalls(regions[id]); !reflect.DeepEqual(got, []string{registryGet}) {
			t.Errorf("second call, %s: calls %q", id, got)
		}
	}
}

// TestNetworkRegistryRouteReportsAFailingRegion: a region that cannot be read
// makes the answer 502 and names it; the other regions are still written.
func TestNetworkRegistryRouteReportsAFailingRegion(t *testing.T) {
	failing := registryRegion()
	failing.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	ok := registryRegion()
	h := registryRouteHandler(t, "ready", map[string]*dynamicfake.FakeDynamicClient{registryDep: ok, registryDep + "-region-b": failing})
	code, body := postNetworkRegistry(t, h, registryDep, registryOwner)
	want := []any{
		map[string]any{"cluster": registryDep, "written": true},
		map[string]any{"cluster": registryDep + "-region-b", "written": false, "error": "connection refused"},
	}
	if code != http.StatusBadGateway || !reflect.DeepEqual(body["regions"], want) {
		t.Fatalf("%d %v", code, body)
	}
	if got := regionCalls(failing); !reflect.DeepEqual(got, []string{registryGet}) {
		t.Errorf("failing region: calls %q", got)
	}
}

// TestNetworkRegistryStartPassOnASovereign: a Sovereign's own catalyst-api
// (SOVEREIGN_FQDN set) is not the writer and holds no OpenTofu state; its
// start pass judges no record and makes no call.
func TestNetworkRegistryStartPassOnASovereign(t *testing.T) {
	region := registryRegion()
	h := registryRouteHandler(t, "ready", map[string]*dynamicfake.FakeDynamicClient{registryDep: region})
	if ids := h.reconcileNetworkRegistries(context.Background(), h.k8sCache); len(ids) != 1 || ids[0] != registryDep {
		t.Fatalf("mothership: judged %q", ids)
	}
	region.ClearActions()
	t.Setenv("SOVEREIGN_FQDN", "alpha.example.org")
	if ids := h.reconcileNetworkRegistries(context.Background(), h.k8sCache); len(ids) != 0 {
		t.Fatalf("sovereign: judged %q", ids)
	}
	if got := regionCalls(region); len(got) != 0 {
		t.Fatalf("sovereign: calls %q", got)
	}
}
