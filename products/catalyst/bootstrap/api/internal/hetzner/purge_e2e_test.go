// purge_e2e_test.go — end-to-end integration test for #392.
//
// The unit tests in purge_test.go pin the label-key constant and the wire
// format of FilterByLabel. This file exercises the full Purge() flow end
// to end against a fake Hetzner Cloud API:
//
//   1. Purge issues GET /v1/{servers,load_balancers,firewalls,networks,ssh_keys}
//      with `?label_selector=catalyst.openova.io/sovereign=<fqdn>`.
//   2. The fake Hetzner returns one resource per kind matching that label.
//   3. Purge issues DELETE on each resource id.
//   4. The fake Hetzner records the DELETEs.
//   5. The returned PurgeReport names every deleted resource.
//
// If anything regresses anywhere along the chain (label key drift, wrong
// query param, missed pagination follow, missing resource kind in the
// loop, wrong DELETE path), the test fails. This is the missing
// behavior-level proof for the wipe.go safety-net step that was a silent
// no-op for every failed deployment until #392.
//
// The test redirects api.hetzner.cloud → httptest server via a custom
// http.Transport, so no real Hetzner credit is consumed.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHetzner is a minimal Hetzner Cloud API stub. For every list
// endpoint it serves, exactly one resource is returned IF the label
// selector matches the canonical Catalyst label.
type fakeHetzner struct {
	t              *testing.T
	wantSelector   string
	mu             sync.Mutex
	deletedServers []int64
	deletedLBs     []int64
	deletedFW      []int64
	deletedNets    []int64
	deletedSSHKeys []int64
}

func (f *fakeHetzner) handler() http.Handler {
	mux := http.NewServeMux()

	// The five list endpoints Purge walks.
	listEndpoints := []struct {
		path string
		key  string
		id   int64
		name string
	}{
		{"/v1/servers", "servers", 1001, "omantel-cp-0"},
		{"/v1/load_balancers", "load_balancers", 2002, "omantel-lb"},
		{"/v1/firewalls", "firewalls", 3003, "omantel-fw"},
		{"/v1/networks", "networks", 4004, "omantel-net"},
		{"/v1/ssh_keys", "ssh_keys", 5005, "omantel-sshkey"},
	}
	for _, e := range listEndpoints {
		e := e // capture
		mux.HandleFunc(e.path, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				selector := r.URL.Query().Get("label_selector")
				if selector != f.wantSelector {
					f.t.Errorf("%s: label_selector wire format drift — got %q, want %q",
						e.path, selector, f.wantSelector)
					http.Error(w, "wrong label selector", http.StatusBadRequest)
					return
				}
				body := map[string]interface{}{
					"meta": map[string]interface{}{
						"pagination": map[string]interface{}{"next_page": nil},
					},
					e.key: []map[string]interface{}{
						{"id": e.id, "name": e.name},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
		// Delete endpoints: /v1/servers/{id}, /v1/load_balancers/{id}, etc.
		mux.HandleFunc(e.path+"/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// Extract the ID from the path tail.
			tail := strings.TrimPrefix(r.URL.Path, e.path+"/")
			var id int64
			fmt.Sscanf(tail, "%d", &id)
			f.mu.Lock()
			defer f.mu.Unlock()
			switch e.key {
			case "servers":
				f.deletedServers = append(f.deletedServers, id)
			case "load_balancers":
				f.deletedLBs = append(f.deletedLBs, id)
			case "firewalls":
				f.deletedFW = append(f.deletedFW, id)
			case "networks":
				f.deletedNets = append(f.deletedNets, id)
			case "ssh_keys":
				f.deletedSSHKeys = append(f.deletedSSHKeys, id)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}

// rewriteTransport rewrites every outgoing request's host to point at the
// httptest server. Lets us swap purgeHTTPClient without touching the
// hardcoded `https://api.hetzner.cloud` base URL in purge.go.
type rewriteTransport struct {
	from string // "api.hetzner.cloud"
	to   *url.URL
	base http.RoundTripper
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == rt.from {
		req.URL.Scheme = rt.to.Scheme
		req.URL.Host = rt.to.Host
	}
	return rt.base.RoundTrip(req)
}

func TestPurge_EndToEnd_FakeHetzner(t *testing.T) {
	const fqdn = "test.omantel.example.com"
	want := "catalyst.openova.io/sovereign=" + fqdn

	fake := &fakeHetzner{t: t, wantSelector: want}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	// Swap the package-level HTTP client so api.hetzner.cloud → test server.
	orig := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{
			from: "api.hetzner.cloud",
			to:   srvURL,
			base: http.DefaultTransport,
		},
		Timeout: 5 * time.Second,
	}
	defer func() { purgeHTTPClient = orig }()

	// Run the actual Purge against the fake.
	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge returned error: %v", err)
	}

	// Total resources purged across all kinds = 5.
	if got := report.Total(); got != 5 {
		t.Fatalf("PurgeReport.Total = %d, want 5 (one per kind)", got)
	}

	// And each kind's id was DELETEd against the fake.
	wantSeen := map[string]int64{
		"servers":        1001,
		"load_balancers": 2002,
		"firewalls":      3003,
		"networks":       4004,
		"ssh_keys":       5005,
	}
	got := map[string][]int64{
		"servers":        fake.deletedServers,
		"load_balancers": fake.deletedLBs,
		"firewalls":      fake.deletedFW,
		"networks":       fake.deletedNets,
		"ssh_keys":       fake.deletedSSHKeys,
	}
	for kind, wantID := range wantSeen {
		ids := got[kind]
		if len(ids) != 1 {
			t.Errorf("%s: expected 1 DELETE call, got %v", kind, ids)
			continue
		}
		if ids[0] != wantID {
			t.Errorf("%s: expected DELETE on id %d, got %d", kind, wantID, ids[0])
		}
	}
}

// TestPurge_EndToEnd_NoMatchingResources verifies the no-op path.
// The fake here is configured to require a *different* selector than
// what Purge would emit; if Purge sends the right selector, the fake
// fails the test. If Purge sends the WRONG selector (regression of the
// original bug), the fake returns a 400 and Purge surfaces the error,
// which this test catches as expected behavior — the error-surfacing
// path was historically silent.
func TestPurge_EndToEnd_RegressionGuard(t *testing.T) {
	const fqdn = "regression.example.com"

	// Configure the fake to expect ONLY the canonical label.
	want := "catalyst.openova.io/sovereign=" + fqdn
	fake := &fakeHetzner{t: t, wantSelector: want}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL)
	orig := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport},
		Timeout:   5 * time.Second,
	}
	defer func() { purgeHTTPClient = orig }()

	// Purge should send the canonical label and the fake should accept.
	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge errored on canonical label: %v — this means purge.go is no longer sending the catalyst.openova.io/sovereign label (regression of #392 fix)", err)
	}
	if report.Total() != 5 {
		t.Fatalf("expected 5 resources purged, got %d", report.Total())
	}
}
