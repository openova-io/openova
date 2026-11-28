package capabilityapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
)

// The fixture catalogue: object-storage has contracts v1 and v2 current and v3 deprecated, so its current
// contract is v2; queue names a default implementation that no Blueprint object provides.
const testIndex = `{"comment": "test", "capabilities": [
  {"name": "object-storage", "title": "Object storage", "defaultImplementation": "bp-object-bucket",
   "contracts": [
     {"version": "v1", "status": "current", "probe": "p", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}], "contextFields": []},
     {"version": "v2", "status": "current", "probe": "p", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}], "contextFields": []},
     {"version": "v3", "status": "deprecated", "probe": "p", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}], "contextFields": []}],
   "providers": []},
  {"name": "queue", "title": "Queue", "defaultImplementation": "bp-queue",
   "contracts": [{"version": "v1", "status": "current", "probe": "p", "keys": [{"name": "url", "required": true, "secret": false, "description": "d"}], "contextFields": []}],
   "providers": []}]}`

// provider is a Blueprint object that provides object-storage v1.
func provider(name, implementation, lifecycleValue, title string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"provides": []interface{}{map[string]interface{}{"capability": "object-storage", "contract": "v1", "implementation": implementation}},
	}
	if lifecycleValue != "" {
		spec["lifecycle"] = lifecycleValue
	}
	if title != "" {
		spec["card"] = map[string]interface{}{"title": title}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1", "kind": "Blueprint",
		"metadata": map[string]interface{}{"name": name}, "spec": spec,
	}}
}

// fixtureBlueprints: an in-cluster, a cloud-native and a deprecated in-cluster provider, and a Blueprint
// that provides nothing.
func fixtureBlueprints() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		provider("bp-object-bucket", "in-cluster", "", "Object storage (in-cluster)"),
		provider("bp-object-bucket-managed", "cloud-native", "experimental", ""),
		provider("bp-object-bucket-old", "in-cluster", "deprecated", ""),
		{Object: map[string]interface{}{"apiVersion": "catalyst.openova.io/v1", "kind": "Blueprint",
			"metadata": map[string]interface{}{"name": "bp-postgres"}, "spec": map[string]interface{}{}}},
	}
}

type fakeCache struct {
	clusters []string
	synced   map[string]map[string]bool
	objects  map[string][]*unstructured.Unstructured // cluster → Blueprint objects
	err      error
}

func (f *fakeCache) Clusters() []string                 { return f.clusters }
func (f *fakeCache) Synced() map[string]map[string]bool { return f.synced }
func (f *fakeCache) List(cluster, kind string, _ labels.Selector) ([]*unstructured.Unstructured, time.Duration, error) {
	if kind != BlueprintKind {
		return nil, 0, errors.New("unexpected kind " + kind)
	}
	return f.objects[cluster], 0, f.err
}

// cloudOffers answers yes for every capability.
type cloudOffers struct{}

func (cloudOffers) Available(string) (bool, string) { return true, "" }

// newRoutes: a Sovereign whose cache holds a secondary cluster and the home cluster
// sovereign-t01.omani.works; only the home cluster has the fixture Blueprints.
func newRoutes(t *testing.T, cache lister) *Routes {
	t.Helper()
	idx, err := capability.LoadIndex([]byte(testIndex))
	if err != nil {
		t.Fatal(err)
	}
	return &Routes{
		cache:       func() lister { return cache },
		index:       func() (capability.Index, error) { return idx, nil },
		avail:       capability.NoCloudProfile{},
		admin:       lifecycle.SovereignAdmin,
		onSovereign: func() bool { return true },
	}
}

func syncedCache() *fakeCache {
	return &fakeCache{
		clusters: []string{"sovereign-t01.omani.works", "a-secondary"},
		synced:   map[string]map[string]bool{"sovereign-t01.omani.works": {BlueprintKind: true}, "a-secondary": {BlueprintKind: true}},
		objects: map[string][]*unstructured.Unstructured{
			"sovereign-t01.omani.works": fixtureBlueprints(),
			"a-secondary":               {provider("bp-elsewhere", "in-cluster", "", "")},
		},
	}
}

// serve runs one GET through a router that registers the two routes as catalyst-api does.
func serve(rt *Routes, path string, claims *auth.Claims) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/v1/catalog/capabilities", rt.List)
	r.Get("/api/v1/catalog/capabilities/{name}", rt.Get)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

var (
	owner  = &auth.Claims{Tier: "owner"}
	viewer = &auth.Claims{Tier: "viewer"}
)

// The answers of the two routes, written out by hand for the fixture catalogue.
const (
	objectStorageAdmin = `{"name":"object-storage","title":"Object storage","contract":"v2","default":"bp-object-bucket","organizationChoice":"allowed","implementations":[` +
		`{"blueprint":"bp-object-bucket","title":"Object storage (in-cluster)","implementation":"in-cluster","lifecycle":"experimental","offered":true,"available":true,"unavailableReason":""},` +
		`{"blueprint":"bp-object-bucket-managed","title":"bp-object-bucket-managed","implementation":"cloud-native","lifecycle":"experimental","offered":false,"available":false,"unavailableReason":"cloud profile not available"},` +
		`{"blueprint":"bp-object-bucket-old","title":"bp-object-bucket-old","implementation":"in-cluster","lifecycle":"deprecated","offered":false,"available":true,"unavailableReason":""}]}`
	objectStorageMember = `{"name":"object-storage","title":"Object storage","contract":"v2","default":"bp-object-bucket","organizationChoice":"allowed","implementations":[` +
		`{"blueprint":"bp-object-bucket","title":"Object storage (in-cluster)","implementation":"in-cluster","lifecycle":"experimental","offered":true,"available":true,"unavailableReason":""}]}`
	queue = `{"name":"queue","title":"Queue","contract":"v1","default":"","organizationChoice":"allowed","implementations":[]}`
)

func assertBody(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(want)); err != nil {
		t.Fatalf("the expected body is not JSON: %v", err)
	}
	if got := bytes.TrimSpace(rec.Body.Bytes()); !bytes.Equal(got, compact.Bytes()) {
		t.Errorf("body\n got %s\nwant %s", got, compact.Bytes())
	}
}

func TestListForASovereignAdmin(t *testing.T) {
	rec := serve(newRoutes(t, syncedCache()), "/api/v1/catalog/capabilities", owner)
	assertBody(t, rec, http.StatusOK, `{"items":[`+objectStorageAdmin+`,`+queue+`]}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
}

func TestListForAnotherCallerShowsOnlyTheOffered(t *testing.T) {
	for _, c := range []struct {
		name   string
		claims *auth.Claims
	}{{"tier viewer", viewer}, {"no claims", nil}} {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(newRoutes(t, syncedCache()), "/api/v1/catalog/capabilities", c.claims)
			assertBody(t, rec, http.StatusOK, `{"items":[`+objectStorageMember+`,`+queue+`]}`)
		})
	}
}

func TestGetOne(t *testing.T) {
	rt := newRoutes(t, syncedCache())
	assertBody(t, serve(rt, "/api/v1/catalog/capabilities/object-storage", owner), http.StatusOK, objectStorageAdmin)
	assertBody(t, serve(rt, "/api/v1/catalog/capabilities/object-storage", viewer), http.StatusOK, objectStorageMember)
	assertBody(t, serve(rt, "/api/v1/catalog/capabilities/queue", viewer), http.StatusOK, queue)
}

// With a cloud that offers the composite kind, both implementations are offered and available.
func TestGetTwoOffered(t *testing.T) {
	rt := newRoutes(t, syncedCache())
	rt.avail = cloudOffers{}
	assertBody(t, serve(rt, "/api/v1/catalog/capabilities/object-storage", viewer), http.StatusOK,
		`{"name":"object-storage","title":"Object storage","contract":"v2","default":"bp-object-bucket","organizationChoice":"allowed","implementations":[`+
			`{"blueprint":"bp-object-bucket","title":"Object storage (in-cluster)","implementation":"in-cluster","lifecycle":"experimental","offered":true,"available":true,"unavailableReason":""},`+
			`{"blueprint":"bp-object-bucket-managed","title":"bp-object-bucket-managed","implementation":"cloud-native","lifecycle":"experimental","offered":true,"available":true,"unavailableReason":""}]}`)
}

func TestRefusals(t *testing.T) {
	notSynced := syncedCache()
	notSynced.synced["sovereign-t01.omani.works"][BlueprintKind] = false
	listFails := syncedCache()
	listFails.err = errors.New("informer stopped")
	for _, c := range []struct {
		name, path string
		routes     func(*Routes)
		cache      lister
		status     int
		body       string
	}{
		{"an unknown capability", "/api/v1/catalog/capabilities/nosuch", nil, syncedCache(), http.StatusNotFound,
			`{"error":{"code":"capability-not-found","message":"no capability \"nosuch\" in the capability index"}}`},
		{"not a Sovereign", "/api/v1/catalog/capabilities", func(rt *Routes) { rt.onSovereign = func() bool { return false } }, syncedCache(),
			http.StatusNotFound, `{"error":{"code":"not-a-sovereign","message":"this catalyst-api does not run on a Sovereign"}}`},
		{"no informer cache", "/api/v1/catalog/capabilities", nil, nil, http.StatusServiceUnavailable,
			`{"error":{"code":"cache-unavailable","message":"the informer cache is not set up"}}`},
		{"a cache with no cluster", "/api/v1/catalog/capabilities/object-storage", nil, &fakeCache{}, http.StatusServiceUnavailable,
			`{"error":{"code":"cache-unavailable","message":"the informer cache holds no cluster"}}`},
		{"the home cluster's informer has not synced", "/api/v1/catalog/capabilities", nil, notSynced, http.StatusServiceUnavailable,
			`{"error":{"code":"cache-not-synced","message":"the Blueprint informer of cluster sovereign-t01.omani.works has not finished its first list"}}`},
		{"the cache cannot list", "/api/v1/catalog/capabilities/object-storage", nil, listFails, http.StatusServiceUnavailable,
			`{"error":{"code":"cache-unavailable","message":"informer stopped"}}`},
		{"the index cannot be read", "/api/v1/catalog/capabilities", func(rt *Routes) {
			rt.index = func() (capability.Index, error) { return nil, errors.New("bad index") }
		}, syncedCache(), http.StatusInternalServerError, `{"error":{"code":"capability-index-invalid","message":"bad index"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var rt *Routes
			if c.cache == nil {
				rt = newRoutes(t, nil)
				rt.cache = func() lister { return nil }
			} else {
				rt = newRoutes(t, c.cache)
			}
			if c.routes != nil {
				c.routes(rt)
			}
			assertBody(t, serve(rt, c.path, owner), c.status, c.body)
		})
	}
}

// A capability whose contracts are all deprecated has no current contract: the index is wrong, never an
// item with an empty contract.
func TestNoCurrentContract(t *testing.T) {
	idx, err := capability.LoadIndex([]byte(`{"comment": "test", "capabilities": [{"name": "old", "title": "Old", "defaultImplementation": "bp-old",
  "contracts": [{"version": "v1", "status": "deprecated", "probe": "p", "keys": [{"name": "k", "required": true, "secret": false, "description": "d"}], "contextFields": []}],
  "providers": []}]}`))
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutes(t, syncedCache())
	rt.index = func() (capability.Index, error) { return idx, nil }
	assertBody(t, serve(rt, "/api/v1/catalog/capabilities", owner), http.StatusInternalServerError,
		`{"error":{"code":"capability-index-invalid","message":"capability old has no contract with status current"}}`)
}

func TestHomeCluster(t *testing.T) {
	for _, c := range []struct {
		clusters []string
		want     string
		ok       bool
	}{
		{nil, "", false},
		{[]string{"only"}, "only", true},
		{[]string{"b-secondary", "sovereign-x", "a-secondary"}, "sovereign-x", true},
		{[]string{"c", "a", "b"}, "a", true},
	} {
		got, ok := homeCluster(c.clusters)
		if got != c.want || ok != c.ok {
			t.Errorf("homeCluster(%v) = %q, %v; want %q, %v", c.clusters, got, ok, c.want, c.ok)
		}
	}
}

// The embedded index of the binary is the one the routes read in production, and it names object-storage.
func TestNewReadsTheEmbeddedIndex(t *testing.T) {
	rt := New(func() *k8scache.Factory { return nil })
	idx, err := rt.index()
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.DefaultImplementation("object-storage"); got != "bp-object-bucket" {
		t.Errorf("embedded index: default implementation of object-storage %q, want bp-object-bucket", got)
	}
	if rt.cache() != nil {
		t.Error("a nil factory must be a nil lister, not a typed nil")
	}
}
