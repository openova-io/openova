package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	routeOwner = "owner@example.org"
	routeDep   = "dddd000000000005"
)

// memRecords is a deployment store in memory.
type memRecords map[string]DeploymentRecord

func (m memRecords) Load(id string) (DeploymentRecord, error) {
	if rec, ok := m[id]; ok {
		return rec, nil
	}
	return DeploymentRecord{}, fmt.Errorf("open %s.json: %w", id, os.ErrNotExist)
}

func (m memRecords) LoadAll() ([]DeploymentRecord, error) {
	out := make([]DeploymentRecord, 0, len(m))
	for _, rec := range m {
		out = append(out, rec)
	}
	return out, nil
}

// testRegistry is a parent with the given records and regions, and the state
// of every record, holding doc, in a work directory of its own.
func testRegistry(t *testing.T, doc string, records memRecords, cache *regions) *NetworkRegistry {
	t.Helper()
	dir := t.TempDir()
	for id := range records {
		if err := os.MkdirAll(filepath.Join(dir, id), 0o750); err != nil {
			t.Fatal(err)
		}
		st := state(t, `"network_registry":{"value":`+quoted(doc)+`,"type":"string"}`)
		if err := os.WriteFile(filepath.Join(dir, id, "terraform.tfstate"), st, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &NetworkRegistry{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Records:  func() (DeploymentRecords, error) { return records, nil },
		Clusters: func() Clusters { return cache },
		StateDir: func() string { return dir },
	}
}

// post sends POST /api/v1/deployments/{id}/network-registry as the session
// email (none when "") and decodes the body.
func post(t *testing.T, n *NetworkRegistry, id, email string) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/v1/deployments/{id}/network-registry", n.Serve)
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

// TestNetworkRegistryRouteRefusals: an unknown id and another user's session
// answer 404, a deployment that is not ready 409, no state or no cluster of
// the deployment 422, a parent without its store or its cache 503 — and none
// makes a call.
func TestNetworkRegistryRouteRefusals(t *testing.T) {
	doc := registry(t)
	region := applyingClient()
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{routeDep: region}}
	ready := memRecords{routeDep: {ID: routeDep, Status: "ready", OwnerEmail: routeOwner}}

	n := testRegistry(t, doc, ready, cache)
	for _, id := range []string{"eeee000000000006", "..", "a%2Fb"} {
		if code, body := post(t, n, id, routeOwner); code != http.StatusNotFound || body["error"] != "deployment not found" {
			t.Errorf("unknown id %q: %d %v", id, code, body)
		}
	}
	if code, body := post(t, n, routeDep, "someone@example.org"); code != http.StatusNotFound || body["error"] != "deployment not found" {
		t.Errorf("another user's session: %d %v", code, body)
	}

	for _, status := range []string{"phase1-watching", "provisioning", "failed", "wiped"} {
		n := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: status, OwnerEmail: routeOwner}}, cache)
		if code, body := post(t, n, routeDep, routeOwner); code != http.StatusConflict ||
			body["error"] != "deployment-not-ready" || body["status"] != status {
			t.Errorf("status %s: %d %v", status, code, body)
		}
	}

	n = testRegistry(t, doc, ready, cache)
	n.StateDir = func() string { return t.TempDir() }
	if code, body := post(t, n, routeDep, routeOwner); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body["detail"].(string), "no OpenTofu state") {
		t.Errorf("no state: %d %v", code, body)
	}

	n = testRegistry(t, `{"mtu":1420}`, ready, cache)
	if err := os.WriteFile(filepath.Join(n.StateDir(), routeDep, "terraform.tfstate"), state(t, ""), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, body := post(t, n, routeDep, routeOwner); code != http.StatusUnprocessableEntity ||
		body["detail"] != ErrNoNetworkRegistry.Error() {
		t.Errorf("no registry in the state: %d %v", code, body)
	}

	other := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{"ffff000000000009": applyingClient()}}
	n = testRegistry(t, doc, ready, other)
	if code, body := post(t, n, routeDep, routeOwner); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body["detail"].(string), "no cluster of deployment "+routeDep) {
		t.Errorf("no cluster of the deployment: %d %v", code, body)
	}

	n = testRegistry(t, doc, ready, cache)
	n.Clusters = func() Clusters { return nil }
	if code, body := post(t, n, routeDep, routeOwner); code != http.StatusServiceUnavailable || body["error"] != "cluster-cache-not-wired" {
		t.Errorf("no cache: %d %v", code, body)
	}

	n = testRegistry(t, doc, ready, cache)
	n.Records = func() (DeploymentRecords, error) { return nil, errors.New("store: write-probe: read-only file system") }
	if code, body := post(t, n, routeDep, routeOwner); code != http.StatusServiceUnavailable || body["error"] != "deployment-store-unavailable" {
		t.Errorf("no store: %d %v", code, body)
	}

	if got := calls(region); len(got) != 0 || len(calls(other.clients["ffff000000000009"])) != 0 {
		t.Fatalf("a refused request made calls %q", got)
	}
}

// TestNetworkRegistryRouteWritesEveryRegion: for a ready deployment the route
// writes the registry into every region where it is absent or another, only
// reads it where it is current, leaves the regions of another deployment
// alone, and a second call writes nothing. A record without an owner, or a
// request without a session email, passes the ownership rule as on the
// neighbouring routes.
func TestNetworkRegistryRouteWritesEveryRegion(t *testing.T) {
	doc := registry(t)
	want := NetworkRegistryConfigMap(doc)
	rev := want.Object["data"].(map[string]any)[NetworkRegistryRevisionKey].(string)
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{
		routeDep:               applyingClient(),                                         // absent
		routeDep + "-region-b": applyingClient(NetworkRegistryConfigMap(`{"mtu":1400}`)), // another document
		routeDep + "-region-c": applyingClient(want.DeepCopy()),                          // this document
		"ffff000000000009":     applyingClient(),                                         // another deployment
	}}
	n := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: "ready", OwnerEmail: routeOwner}}, cache)

	code, body := post(t, n, routeDep, "OWNER@example.org")
	if code != http.StatusOK || body["deploymentId"] != routeDep || body["revision"] != rev {
		t.Fatalf("%d %v", code, body)
	}
	wantRegions := []any{
		map[string]any{"cluster": routeDep, "written": true},
		map[string]any{"cluster": routeDep + "-region-b", "written": true},
		map[string]any{"cluster": routeDep + "-region-c", "written": false},
	}
	if !reflect.DeepEqual(body["regions"], wantRegions) {
		t.Fatalf("regions %v", body["regions"])
	}
	for id, wantCalls := range map[string][]string{
		routeDep:               {getCall, patchCall},
		routeDep + "-region-b": {getCall, patchCall},
		routeDep + "-region-c": {getCall},
		"ffff000000000009":     nil,
	} {
		if got := calls(cache.clients[id]); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("%s: calls %q, want %q", id, got, wantCalls)
		}
	}
	for _, id := range []string{routeDep, routeDep + "-region-b", routeDep + "-region-c"} {
		if d, r := held(t, cache.clients[id]); d != doc || r != rev {
			t.Errorf("%s holds revision %q", id, r)
		}
		cache.clients[id].ClearActions()
	}

	// The second call finds every region current; no session email passes.
	code, body = post(t, n, routeDep, "")
	if code != http.StatusOK {
		t.Fatalf("second call: %d %v", code, body)
	}
	for _, r := range body["regions"].([]any) {
		if r.(map[string]any)["written"] != false {
			t.Errorf("second call wrote: %v", r)
		}
	}
	for _, id := range []string{routeDep, routeDep + "-region-b", routeDep + "-region-c"} {
		if got := calls(cache.clients[id]); !reflect.DeepEqual(got, []string{getCall}) {
			t.Errorf("second call, %s: calls %q", id, got)
		}
	}

	// A legacy record without an owner passes any session.
	legacy := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: "ready"}}, cache)
	if code, body := post(t, legacy, routeDep, "someone@example.org"); code != http.StatusOK {
		t.Errorf("record without an owner: %d %v", code, body)
	}
}

// TestNetworkRegistryRouteReportsAFailingRegion: a region that cannot be read
// makes the answer 502 and names it; the other region is still written.
func TestNetworkRegistryRouteReportsAFailingRegion(t *testing.T) {
	failing := applyingClient()
	failing.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{routeDep: applyingClient(), routeDep + "-region-b": failing}}
	n := testRegistry(t, registry(t), memRecords{routeDep: {ID: routeDep, Status: "ready", OwnerEmail: routeOwner}}, cache)
	code, body := post(t, n, routeDep, routeOwner)
	want := []any{
		map[string]any{"cluster": routeDep, "written": true},
		map[string]any{"cluster": routeDep + "-region-b", "written": false, "error": "connection refused"},
	}
	if code != http.StatusBadGateway || !reflect.DeepEqual(body["regions"], want) {
		t.Fatalf("%d %v", code, body)
	}
	if got := calls(failing); !reflect.DeepEqual(got, []string{getCall}) {
		t.Errorf("failing region: calls %q", got)
	}
}

// TestNetworkRegistryStartPass: the start pass judges every ready record and
// no other. Absent → written, another revision → rewritten, this revision →
// read only, in every region of each record; a record that is not ready, and
// a cluster of no record, get no call. On a Sovereign it judges nothing.
func TestNetworkRegistryStartPass(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	doc := registry(t)
	want := NetworkRegistryConfigMap(doc)
	rev := want.Object["data"].(map[string]any)[NetworkRegistryRevisionKey].(string)
	const current, stale, notReady = "aaaa000000000001", "bbbb000000000002", "0000000000000003"
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{
		current:               applyingClient(want.DeepCopy()),
		stale:                 applyingClient(),
		stale + "-region-b-1": applyingClient(NetworkRegistryConfigMap(`{"mtu":1400}`)),
		notReady:              applyingClient(),
		"ffff000000000009":    applyingClient(),
	}}
	n := testRegistry(t, doc, memRecords{
		current:  {ID: current, Status: "ready"},
		stale:    {ID: stale, Status: "ready"},
		notReady: {ID: notReady, Status: "phase1-watching"},
	}, cache)

	if ids := n.ReconcileReady(context.Background()); !reflect.DeepEqual(ids, []string{current, stale}) {
		t.Fatalf("judged %q", ids)
	}
	for id, wantCalls := range map[string][]string{
		current:               {getCall},
		stale:                 {getCall, patchCall},
		stale + "-region-b-1": {getCall, patchCall},
		notReady:              nil,
		"ffff000000000009":    nil,
	} {
		if got := calls(cache.clients[id]); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("%s: calls %q, want %q", id, got, wantCalls)
		}
	}
	for _, id := range []string{current, stale, stale + "-region-b-1"} {
		if _, r := held(t, cache.clients[id]); r != rev {
			t.Errorf("%s holds revision %q", id, r)
		}
		cache.clients[id].ClearActions()
	}

	t.Setenv("SOVEREIGN_FQDN", "alpha.example.org")
	if ids := n.ReconcileReady(context.Background()); len(ids) != 0 {
		t.Fatalf("on a Sovereign: judged %q", ids)
	}
	for id, c := range cache.clients {
		if got := calls(c); len(got) != 0 {
			t.Errorf("on a Sovereign, %s: calls %q", id, got)
		}
	}
}
