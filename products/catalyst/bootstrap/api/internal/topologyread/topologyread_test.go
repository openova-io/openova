package topologyread

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/regions"
)

// The informer cache of the process is a lister.
var _ lister = (*k8scache.Factory)(nil)

type fakeCache struct {
	clusters []string
	synced   map[string]map[string]bool
	objs     map[string][]*unstructured.Unstructured
	listErr  error
	listed   []string
}

func (f *fakeCache) Clusters() []string                 { return f.clusters }
func (f *fakeCache) Synced() map[string]map[string]bool { return f.synced }
func (f *fakeCache) List(cluster, kind string, _ labels.Selector) ([]*unstructured.Unstructured, time.Duration, error) {
	f.listed = append(f.listed, cluster+"/"+kind)
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.objs[cluster], 0, nil
}

func env(name, org, placement string, regions ...string) *unstructured.Unstructured {
	spec := map[string]any{"organizationRef": org, "envType": "prod", "placement": placement}
	if len(regions) > 0 {
		var regs []any
		for _, r := range regions {
			regs = append(regs, map[string]any{"provider": "huawei", "region": r, "buildingBlock": "rtz"})
		}
		spec["regions"] = regs
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalyst.openova.io/v1", "kind": "Environment",
		"metadata": map[string]any{"name": name}, "spec": spec,
	}}
	return u
}

func routes(c lister, sovereign bool, reg []regions.Region) *Routes {
	return &Routes{
		cache:       func() lister { return c },
		registry:    func() []regions.Region { return reg },
		onSovereign: func() bool { return sovereign },
	}
}

// serve sends GET path through a chi router, so {id} is set as in the API.
func serve(t *testing.T, rt *Routes, path string) (int, []byte) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/regions", rt.Regions)
	r.Get("/api/v1/sovereigns/{id}/environments", rt.Environments)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q, want application/json", path, ct)
	}
	return rec.Code, rec.Body.Bytes()
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("refusal is not JSON: %v: %s", err, body)
	}
	if e.Detail == "" {
		t.Errorf("refusal %q has no detail", e.Error)
	}
	return e.Error
}

const regionsPath = "/api/v1/sovereigns/dep-1/regions"
const envsPath = "/api/v1/sovereigns/dep-1/environments"

func TestRegionsServesTheRegistryInOrder(t *testing.T) {
	reg := []regions.Region{
		{ID: "hw-me-east-215-a-rtz-prod", Label: "hw-me-east-215-a-rtz-prod", Primary: true, Provider: "huawei"},
		{ID: "hw-me-east-215-b-rtz-prod", Label: "hw-me-east-215-b-rtz-prod", Provider: "huawei"},
	}
	code, body := serve(t, routes(nil, true, reg), regionsPath)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", code, body)
	}
	var got struct {
		Regions []regions.Region `json:"regions"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Regions, reg) {
		t.Errorf("regions %+v, want %+v", got.Regions, reg)
	}
	// The suite reads {"regions": [{"id"}]}: each id is there, verbatim.
	var suite struct {
		Regions []struct {
			ID string `json:"id"`
		} `json:"regions"`
	}
	_ = json.Unmarshal(body, &suite)
	var ids []string
	for _, x := range suite.Regions {
		ids = append(ids, x.ID)
	}
	if !reflect.DeepEqual(ids, []string{"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod"}) {
		t.Errorf("the suite's shape reads ids %v", ids)
	}
}

func TestRegionsEmptyRegistryIsAnEmptyList(t *testing.T) {
	code, body := serve(t, routes(nil, true, nil), regionsPath)
	if code != http.StatusOK || string(body) != "{\"regions\":[]}\n" {
		t.Errorf("status %d body %q, want 200 {\"regions\":[]}", code, body)
	}
}

func TestNotASovereign(t *testing.T) {
	c := &fakeCache{clusters: []string{"dep-1"}, synced: map[string]map[string]bool{"dep-1": {EnvironmentKind: true}}}
	rt := routes(c, false, []regions.Region{{ID: "a"}})
	for _, path := range []string{regionsPath, envsPath} {
		code, body := serve(t, rt, path)
		if code != http.StatusNotFound || errorCode(t, body) != "not-a-sovereign" {
			t.Errorf("%s: status %d body %s, want 404 not-a-sovereign", path, code, body)
		}
	}
	if len(c.listed) != 0 {
		t.Errorf("the cache was read off a Sovereign: %v", c.listed)
	}
}

func TestEnvironmentsServesTheHomeClusterSorted(t *testing.T) {
	c := &fakeCache{
		clusters: []string{"dep-1", "dep-1-hw-me-east-215-b-rtz-prod"},
		synced: map[string]map[string]bool{
			"dep-1":                           {EnvironmentKind: true},
			"dep-1-hw-me-east-215-b-rtz-prod": {EnvironmentKind: true},
		},
		objs: map[string][]*unstructured.Unstructured{
			"dep-1": {
				env("org-b-prod", "org-b", "single-region", "mea"),
				env("org-a-cp", "org-a", "multi-region", "mea", "meb"),
				env("org-a-dev", "org-a", ""),
			},
			"dep-1-hw-me-east-215-b-rtz-prod": {env("only-in-b", "org-a", "multi-region", "mea", "meb")},
		},
	}
	code, body := serve(t, routes(c, true, nil), envsPath)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", code, body)
	}
	var got EnvironmentList
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Items) != 3 {
		t.Fatalf("total %d items %d, want 3 and 3: %s", got.Total, len(got.Items), body)
	}
	names := []string{got.Items[0].Name, got.Items[1].Name, got.Items[2].Name}
	if !reflect.DeepEqual(names, []string{"org-a-cp", "org-a-dev", "org-b-prod"}) {
		t.Errorf("names %v, want sorted org-a-cp, org-a-dev, org-b-prod", names)
	}
	cp := got.Items[0]
	if cp.OrganizationRef != "org-a" || cp.Placement != "multi-region" || cp.EnvType != "prod" || len(cp.Regions) != 2 {
		t.Errorf("org-a-cp read as %+v", cp)
	}
	if r0, _ := cp.Regions[0].(map[string]any); r0["region"] != "mea" || r0["buildingBlock"] != "rtz" || r0["provider"] != "huawei" {
		t.Errorf("spec.regions not verbatim: %+v", cp.Regions)
	}
	if got.Items[1].Regions == nil || len(got.Items[1].Regions) != 0 {
		t.Errorf("absent spec.regions must be an empty list, got %#v", got.Items[1].Regions)
	}
	if !reflect.DeepEqual(c.listed, []string{"dep-1/" + EnvironmentKind}) {
		t.Errorf("listed %v, want only the home cluster dep-1", c.listed)
	}
}

// The suite parses the list with this shape; an Organization's two-region
// Environment must come out with its Organization and two regions.
func TestEnvironmentsInTheSuitesShape(t *testing.T) {
	c := &fakeCache{
		clusters: []string{"sovereign-x.example"},
		synced:   map[string]map[string]bool{"sovereign-x.example": {EnvironmentKind: true}},
		objs:     map[string][]*unstructured.Unstructured{"sovereign-x.example": {env("x-cp", "x", "multi-region", "mea", "meb")}},
	}
	_, body := serve(t, routes(c, true, nil), envsPath)
	var suite struct {
		Items []struct {
			Name         string   `json:"name"`
			Organization string   `json:"organization"`
			OrgRef       string   `json:"organizationRef"`
			Placement    string   `json:"placement"`
			Regions      []any    `json:"regions"`
			RegionIDs    []string `json:"regionIds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &suite); err != nil {
		t.Fatal(err)
	}
	if len(suite.Items) != 1 || suite.Items[0].Name != "x-cp" || suite.Items[0].OrgRef != "x" ||
		suite.Items[0].Placement != "multi-region" || len(suite.Items[0].Regions) != 2 {
		t.Errorf("the suite reads %+v", suite.Items)
	}
}

func TestHomeCluster(t *testing.T) {
	for _, c := range []struct {
		path     string
		clusters []string
		want     string
		ok       bool
	}{
		{"dep-1", nil, "", false},
		{"dep-1", []string{"dep-1-b", "dep-1"}, "dep-1", true},
		{"other", []string{"sovereign-x.example"}, "sovereign-x.example", true},
		{"dep-1", []string{"dep-1-hw-b", "sovereign-x.example"}, "sovereign-x.example", true},
		{"other", []string{"zeta", "alpha-b", "alpha"}, "alpha", true},
	} {
		got, ok := homeCluster(c.path, c.clusters)
		if got != c.want || ok != c.ok {
			t.Errorf("homeCluster(%q, %v) = %q, %v; want %q, %v", c.path, c.clusters, got, ok, c.want, c.ok)
		}
	}
}

func TestEnvironmentsRefusesWhatItCannotRead(t *testing.T) {
	synced := map[string]map[string]bool{"dep-1": {EnvironmentKind: true}}
	for _, c := range []struct {
		name  string
		cache lister
		code  string
	}{
		{"no cache", nil, "cache-unavailable"},
		{"no cluster", &fakeCache{synced: synced}, "cache-unavailable"},
		{"kind not served", &fakeCache{clusters: []string{"dep-1"}, synced: synced, listErr: errors.New(`k8scache: kind "environment" not registered`)}, "cache-unavailable"},
		{"not synced", &fakeCache{clusters: []string{"dep-1"}, synced: map[string]map[string]bool{"dep-1": {EnvironmentKind: false}}}, "cache-not-synced"},
		{"synced map lacks the cluster", &fakeCache{clusters: []string{"dep-1"}, synced: map[string]map[string]bool{}}, "cache-not-synced"},
	} {
		code, body := serve(t, routes(c.cache, true, nil), envsPath)
		if code != http.StatusServiceUnavailable || errorCode(t, body) != c.code {
			t.Errorf("%s: status %d body %s, want 503 %s", c.name, code, body, c.code)
		}
	}
}

// New adapts a nil *k8scache.Factory to no cache, not to a nil value
// inside an interface.
func TestNewWithoutCache(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "x.example")
	rt := New(func() *k8scache.Factory { return nil })
	code, body := serve(t, rt, envsPath)
	if code != http.StatusServiceUnavailable || errorCode(t, body) != "cache-unavailable" {
		t.Errorf("status %d body %s, want 503 cache-unavailable", code, body)
	}
	t.Setenv("SOVEREIGN_FQDN", "")
	if code, _ := serve(t, rt, envsPath); code != http.StatusNotFound {
		t.Errorf("without SOVEREIGN_FQDN: status %d, want 404", code)
	}
}
