package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// helpers to keep Unstructured ergonomic in this test file.
func unstructuredCluster() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	return u
}

func setNested(o map[string]interface{}, value interface{}, fields ...string) error {
	return unstructured.SetNestedField(o, value, fields...)
}

func setNestedSlice(o map[string]interface{}, value []interface{}, fields ...string) error {
	return unstructured.SetNestedSlice(o, value, fields...)
}

// fakeProvider builds a Sequencer + plan for tests.
type fakeProvider struct {
	missing bool
	err     error
}

func (f *fakeProvider) SequencerFor(ns, name string) (*switchover.Sequencer, switchover.SwitchoverPlan, error) {
	if f.err != nil {
		return nil, switchover.SwitchoverPlan{}, f.err
	}
	if f.missing {
		return nil, switchover.SwitchoverPlan{}, ErrCRNotFound
	}
	store := witness.NewInMemoryStore()
	w := store.Client(ns + "/" + name)
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		return nil, switchover.SwitchoverPlan{}, err
	}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{cnpg.ClusterGVR: "ClusterList"},
		newPair(ns)...,
	)
	seq := &switchover.Sequencer{
		CNPG:    cnpg.NewReader(dyn),
		Witness: w,
		Audit:   events.NewRecorder(),
		PDMCommit: func(ctx context.Context, _ []dns.Record) error {
			return nil
		},
		Sleep: func(time.Duration) {},
	}
	plan := switchover.SwitchoverPlan{
		ContinuumName:   ns + "/" + name,
		ApplicationName: ns + "/demo-app",
		FromRegion:      "fsn",
		CNPGPair:        "demo",
		CNPGNamespace:   ns,
		PDMZone:         "example.com",
		SynthParams: dns.SynthParams{
			RegionToIPs: map[string][]string{
				"fsn": {"5.1.2.3"},
				"hel": {"5.5.6.7"},
			},
			Hostnames: []string{"a.example.com"},
		},
	}
	return seq, plan, nil
}

func newPair(ns string) []runtime.Object {
	primary := newCluster(ns, "demo-primary", cnpg.RolePrimary, false, true)
	replica := newCluster(ns, "demo-replica", cnpg.RoleReplica, true, true)
	return []runtime.Object{primary, replica}
}

func newCluster(ns, name, role string, replicaEnabled, ready bool) runtime.Object {
	cl := unstructuredCluster()
	cl.SetNamespace(ns)
	cl.SetName(name)
	cl.SetLabels(map[string]string{cnpg.PairLabel: "demo", cnpg.PairRoleLabel: role})
	if replicaEnabled {
		_ = setNested(cl.Object, true, "spec", "replica", "enabled")
	} else {
		_ = setNested(cl.Object, false, "spec", "replica", "enabled")
	}
	if ready {
		_ = setNestedSlice(cl.Object, []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True"},
		}, "status", "conditions")
	}
	return cl
}

func TestDryRun_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider: &fakeProvider{},
	}).Handler())
	defer srv.Close()

	body := bytes.NewBufferString(`{"toRegion":"hel","reason":"unit-test"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run", body)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
	var rep switchover.DryRunReport
	if err := json.NewDecoder(res.Body).Decode(&rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rep.Steps) != 7 {
		t.Errorf("steps = %d want 7", len(rep.Steps))
	}
	if rep.PlanFingerprint == "" {
		t.Error("PlanFingerprint empty")
	}
}

func TestDryRun_MissingTierHeader_401(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{}}).Handler())
	defer srv.Close()
	res, err := http.Post(srv.URL+"/v1/continuums/demo/cr1/dry-run", "application/json",
		strings.NewReader(`{"toRegion":"hel"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("status = %d want 401", res.StatusCode)
	}
}

func TestDryRun_BearerTokenEnforced(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider:  &fakeProvider{},
		AuthToken: "secret-token",
	}).Handler())
	defer srv.Close()

	// Wrong token → 401.
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run",
		strings.NewReader(`{"toRegion":"hel"}`))
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	req.Header.Set("Authorization", "Bearer wrong")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Errorf("wrong-token status = %d want 401", res.StatusCode)
	}

	// Correct token → 200.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run",
		strings.NewReader(`{"toRegion":"hel"}`))
	req2.Header.Set("X-Catalyst-Owner-Tier", "true")
	req2.Header.Set("Authorization", "Bearer secret-token")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("correct-token Do: %v", err)
	}
	if res2.StatusCode != 200 {
		t.Errorf("correct-token status = %d want 200", res2.StatusCode)
	}
}

func TestDryRun_MissingToRegion_400(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{}}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run",
		strings.NewReader(`{}`))
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 400 {
		t.Errorf("status = %d want 400", res.StatusCode)
	}
}

func TestDryRun_BadJSON_400(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{}}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run",
		strings.NewReader(`{not json`))
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 400 {
		t.Errorf("status = %d want 400", res.StatusCode)
	}
}

func TestDryRun_CRNotFound_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{missing: true}}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/ghost/dry-run",
		strings.NewReader(`{"toRegion":"hel"}`))
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 404 {
		t.Errorf("status = %d want 404", res.StatusCode)
	}
}

func TestHealth_CacheHit(t *testing.T) {
	t.Parallel()
	cache := NewInMemoryHealthCache()
	cache.Put("demo", "cr1", switchover.HealthReport{
		NewPrimaryRegion: "hel",
		OverallHealthy:   true,
		Checks: []switchover.HealthCheck{
			{Name: switchover.CheckReplicasHealthy, Passed: true},
		},
	})
	srv := httptest.NewServer((&Server{
		Provider:    &fakeProvider{},
		HealthCache: cache,
	}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/continuums/demo/cr1/health", nil)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
	var rep switchover.HealthReport
	if err := json.NewDecoder(res.Body).Decode(&rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.NewPrimaryRegion != "hel" {
		t.Errorf("NewPrimaryRegion = %q want hel", rep.NewPrimaryRegion)
	}
	if !rep.OverallHealthy {
		t.Errorf("OverallHealthy = false")
	}
}

func TestHealth_CacheMiss_ComputesOnDemand(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider:    &fakeProvider{},
		HealthCache: NewInMemoryHealthCache(),
		// No HealthOpts → audit + dns checks deferred. Replicas
		// check still runs (against the fake CNPG).
	}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/continuums/demo/cr1/health", nil)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
	var rep switchover.HealthReport
	if err := json.NewDecoder(res.Body).Decode(&rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rep.Checks) != 4 {
		t.Errorf("checks = %d want 4", len(rep.Checks))
	}
}

func TestHealth_NoCache_ComputesEachTime(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider: &fakeProvider{},
	}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/continuums/demo/cr1/health", nil)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Errorf("status = %d want 200", res.StatusCode)
	}
}

func TestHealth_MissingCR_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider: &fakeProvider{missing: true},
	}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/continuums/demo/ghost/health", nil)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 404 {
		t.Errorf("status = %d want 404", res.StatusCode)
	}
}

func TestRoute_BadShape_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{}}).Handler())
	defer srv.Close()
	cases := []string{
		"/v1/continuums/onlyone",
		"/v1/continuums/ns/name", // missing verb
	}
	for _, p := range cases {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.Header.Set("X-Catalyst-Owner-Tier", "true")
		res, _ := http.DefaultClient.Do(req)
		if res.StatusCode != 404 {
			t.Errorf("path %q status = %d want 404", p, res.StatusCode)
		}
	}
}

func TestRoute_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{Provider: &fakeProvider{}}).Handler())
	defer srv.Close()
	// GET on dry-run → 405.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/continuums/demo/cr1/dry-run", nil)
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 405 {
		t.Errorf("GET on dry-run status = %d want 405", res.StatusCode)
	}
	// POST on health → 405.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/health", nil)
	req2.Header.Set("X-Catalyst-Owner-Tier", "true")
	res2, _ := http.DefaultClient.Do(req2)
	if res2.StatusCode != 405 {
		t.Errorf("POST on health status = %d want 405", res2.StatusCode)
	}
}

func TestRoute_ProviderError_500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{
		Provider: &fakeProvider{err: errors.New("simulated provider failure")},
	}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/continuums/demo/cr1/dry-run",
		strings.NewReader(`{"toRegion":"hel"}`))
	req.Header.Set("X-Catalyst-Owner-Tier", "true")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 500 {
		t.Errorf("status = %d want 500", res.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d want 200", res.StatusCode)
	}
}

func TestInMemoryHealthCache_Concurrent(t *testing.T) {
	t.Parallel()
	c := NewInMemoryHealthCache()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.Put("ns", "cr", switchover.HealthReport{NewPrimaryRegion: "hel"})
		}()
		go func() {
			defer wg.Done()
			_, _ = c.Get("ns", "cr")
		}()
	}
	wg.Wait()
}
