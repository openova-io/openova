// purge_name_prefix_test.go — regression sentinel for issue #732.
//
// The bug: production Sovereigns observed (otech83, 2026-05-04) leaked
// LB / network / firewall / SSH key after a wipe even though the label
// sweep ran. Root cause was that those resources existed in the
// Hetzner project WITHOUT the canonical
// `catalyst.openova.io/sovereign=<fqdn>` label — typically because of
// a partial `tofu apply`, an out-of-band edit, or a fresh PVC that
// lost the tfstate so tofu destroy had nothing to remove.
//
// These tests pin the fallback contract:
//
//   1. TestPurge_NamePrefixFallback_DeletesUnlabeled — every resource
//      kind that is missing the label but matches the
//      `catalyst-<fqdn-with-dashes>` name prefix is deleted in the
//      second pass. Asserts the report names them and the fake's
//      DELETE counter saw all five kinds.
//
//   2. TestPurge_NamePrefixFallback_DoesNotTouchOtherCustomers —
//      the prefix scan uses HasPrefix, so a "catalyst-otech8-…"
//      Sovereign's wipe MUST NOT touch a "catalyst-otech80-…"
//      Sovereign's resources. Pins the boundary; without this guard
//      a numeric extension would silently delete a neighbouring
//      production tenant's infra. CRITICAL safety regression.
//
//   3. TestPurge_NamePrefixFallback_NoDoubleCount — when the label
//      pass already deleted a resource, the prefix pass MUST NOT add
//      it to the report a second time.
//
//   4. TestNamePrefixForSovereign_MatchesTofuEmit — the prefix
//      contract is pinned against the OpenTofu module's name template
//      so a future PR that changes either side fails this test.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHetznerNamePrefix is a Hetzner Cloud API stub that:
//
//   - Returns NO resources when queried with a label_selector (simulating
//     the production case where the resources were created without the
//     canonical label)
//   - Returns a configurable list of resources when queried WITHOUT a
//     label_selector (the name-prefix fallback path)
//   - Records every DELETE so the test asserts each kind was touched
type fakeHetznerNamePrefix struct {
	t *testing.T

	// unlabeled holds the per-kind resources the no-selector listing
	// returns. The labelled list always returns empty.
	unlabeled map[string][]map[string]any

	mu       sync.Mutex
	deletes  map[string][]int64
	dupGuard map[string]struct{} // path → seen
}

func newFakeHetznerNamePrefix(t *testing.T) *fakeHetznerNamePrefix {
	return &fakeHetznerNamePrefix{
		t:         t,
		unlabeled: map[string][]map[string]any{},
		deletes:   map[string][]int64{},
		dupGuard:  map[string]struct{}{},
	}
}

func (f *fakeHetznerNamePrefix) handler() http.Handler {
	mux := http.NewServeMux()
	for _, kind := range []string{"servers", "load_balancers", "firewalls", "networks", "ssh_keys"} {
		kind := kind
		path := "/v1/" + kind
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			selector := r.URL.Query().Get("label_selector")
			var entries []map[string]any
			if selector == "" {
				entries = f.unlabeled[kind]
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta":  map[string]any{"pagination": map[string]any{"next_page": nil}},
				kind:    entries,
			})
		})
		mux.HandleFunc(path+"/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			tail := strings.TrimPrefix(r.URL.Path, path+"/")
			var id int64
			fmt.Sscanf(tail, "%d", &id)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deletes[kind] = append(f.deletes[kind], id)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}

func namePrefixTestSetup(t *testing.T, fake *fakeHetznerNamePrefix) func() {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("parse test server url: %v", err)
	}
	origClient := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport},
		Timeout:   5 * time.Second,
	}
	origBackoff := firewallRetryInitialBackoff
	firewallRetryInitialBackoff = 1 * time.Millisecond
	return func() {
		srv.Close()
		purgeHTTPClient = origClient
		firewallRetryInitialBackoff = origBackoff
	}
}

func TestPurge_NamePrefixFallback_DeletesUnlabeled(t *testing.T) {
	const fqdn = "otech83.omani.works"
	const prefix = "catalyst-otech83-omani-works"

	fake := newFakeHetznerNamePrefix(t)
	// Production-shape resources: every kind, named off the canonical
	// prefix that infra/hetzner/main.tf renders. None carry the
	// catalyst.openova.io/sovereign label.
	fake.unlabeled["servers"] = []map[string]any{
		{"id": 1001, "name": prefix + "-cp1"},
	}
	fake.unlabeled["load_balancers"] = []map[string]any{
		{"id": 2002, "name": prefix + "-lb"},
	}
	fake.unlabeled["firewalls"] = []map[string]any{
		{"id": 3003, "name": prefix + "-fw"},
	}
	fake.unlabeled["networks"] = []map[string]any{
		{"id": 4004, "name": prefix + "-net"},
	}
	fake.unlabeled["ssh_keys"] = []map[string]any{
		{"id": 5005, "name": prefix},
	}
	cleanup := namePrefixTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// Every kind should have been deleted via the prefix fallback.
	wantNames := map[string]string{
		"servers":        prefix + "-cp1",
		"load_balancers": prefix + "-lb",
		"firewalls":      prefix + "-fw",
		"networks":       prefix + "-net",
		"ssh_keys":       prefix,
	}
	gotInReport := map[string][]string{
		"servers":        report.Servers,
		"load_balancers": report.LoadBalancers,
		"firewalls":      report.Firewalls,
		"networks":       report.Networks,
		"ssh_keys":       report.SSHKeys,
	}
	for kind, wantName := range wantNames {
		if len(gotInReport[kind]) != 1 {
			t.Errorf("%s: expected 1 entry in report, got %v", kind, gotInReport[kind])
			continue
		}
		if gotInReport[kind][0] != wantName {
			t.Errorf("%s: report name %q, want %q", kind, gotInReport[kind][0], wantName)
		}
	}

	// And the fake recorded a DELETE for every kind.
	wantDeletes := map[string]int64{
		"servers":        1001,
		"load_balancers": 2002,
		"firewalls":      3003,
		"networks":       4004,
		"ssh_keys":       5005,
	}
	for kind, wantID := range wantDeletes {
		ids := fake.deletes[kind]
		if len(ids) != 1 {
			t.Errorf("%s: expected 1 DELETE call against fake, got %v", kind, ids)
			continue
		}
		if ids[0] != wantID {
			t.Errorf("%s: DELETE id %d, want %d", kind, ids[0], wantID)
		}
	}

	if got := report.Total(); got != 5 {
		t.Errorf("report.Total() = %d, want 5", got)
	}
}

// TestPurge_NamePrefixFallback_DoesNotTouchOtherCustomers — CRITICAL
// safety guard. The prefix scan uses HasPrefix, so wiping otech8 must
// NOT touch otech80's resources. Without a boundary check, a numeric
// extension would silently destroy a neighbouring production tenant.
//
// Failure here would be a P0 — would delete another customer's infra.
func TestPurge_NamePrefixFallback_DoesNotTouchOtherCustomers(t *testing.T) {
	const fqdn = "otech8.omani.works"
	const prefix = "catalyst-otech8-omani-works"
	// Neighbouring tenant — its name has the SAME prefix string but with
	// extra characters before the next dash. HasPrefix would falsely
	// match if we didn't pin the prefix to end with the per-resource
	// suffix dashes the Tofu module emits.
	const otherPrefix = "catalyst-otech80-omani-works"

	fake := newFakeHetznerNamePrefix(t)
	fake.unlabeled["servers"] = []map[string]any{
		{"id": 9999, "name": otherPrefix + "-cp1"},
	}
	fake.unlabeled["load_balancers"] = []map[string]any{
		{"id": 9998, "name": otherPrefix + "-lb"},
	}
	fake.unlabeled["firewalls"] = []map[string]any{
		{"id": 9997, "name": otherPrefix + "-fw"},
	}
	fake.unlabeled["networks"] = []map[string]any{
		{"id": 9996, "name": otherPrefix + "-net"},
	}
	fake.unlabeled["ssh_keys"] = []map[string]any{
		{"id": 9995, "name": otherPrefix},
	}
	cleanup := namePrefixTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// CRITICAL: we expect ZERO deletes. The other tenant must be untouched.
	if len(report.Servers)+len(report.LoadBalancers)+len(report.Firewalls)+len(report.Networks)+len(report.SSHKeys) != 0 {
		t.Fatalf("PURGE TOUCHED ANOTHER TENANT — this is a P0 safety regression. report=%+v", report)
	}
	for kind, ids := range fake.deletes {
		if len(ids) > 0 {
			t.Fatalf("%s: fake recorded DELETE %v against another tenant — P0 safety regression", kind, ids)
		}
	}
	_ = prefix // documentation
}

// TestPurge_NamePrefixFallback_NoDoubleCount — when the label pass
// already deleted a resource, the prefix pass MUST NOT add it to the
// report a second time. Production scenario: most resources carry the
// label (deleted by the first pass) and a few unlabeled stragglers
// remain (deleted by the second pass). The report's totals must
// reflect actual unique resources, not double-counted.
func TestPurge_NamePrefixFallback_NoDoubleCount(t *testing.T) {
	const fqdn = "otech-mixed.omani.works"
	const prefix = "catalyst-otech-mixed-omani-works"

	mux := http.NewServeMux()
	deletedFW := map[int64]bool{}
	deletedSrv := map[int64]bool{}

	mux.HandleFunc("/v1/servers", func(w http.ResponseWriter, r *http.Request) {
		selector := r.URL.Query().Get("label_selector")
		body := map[string]any{
			"meta":    map[string]any{"pagination": map[string]any{"next_page": nil}},
			"servers": []any{},
		}
		if selector != "" {
			// Labelled pass returns the server.
			body["servers"] = []map[string]any{{"id": 1001, "name": prefix + "-cp1"}}
		} else {
			// Unlabeled pass: the server has been deleted at API level
			// already, so the prefix scan returns empty.
			body["servers"] = []any{}
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/v1/servers/", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/v1/servers/"), "%d", &id)
		deletedSrv[id] = true
		w.WriteHeader(http.StatusNoContent)
	})
	for _, kind := range []string{"load_balancers", "networks", "ssh_keys"} {
		kind := kind
		path := "/v1/" + kind
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"pagination": map[string]any{"next_page": nil}},
				kind:   []any{},
			})
		})
	}
	mux.HandleFunc("/v1/firewalls", func(w http.ResponseWriter, r *http.Request) {
		selector := r.URL.Query().Get("label_selector")
		body := map[string]any{
			"meta":      map[string]any{"pagination": map[string]any{"next_page": nil}},
			"firewalls": []any{},
		}
		if selector != "" {
			body["firewalls"] = []map[string]any{{"id": 3003, "name": prefix + "-fw"}}
		} else {
			body["firewalls"] = []any{}
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/v1/firewalls/", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/v1/firewalls/"), "%d", &id)
		deletedFW[id] = true
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)
	orig := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport},
		Timeout:   5 * time.Second,
	}
	defer func() { purgeHTTPClient = orig }()

	origBackoff := firewallRetryInitialBackoff
	firewallRetryInitialBackoff = 1 * time.Millisecond
	defer func() { firewallRetryInitialBackoff = origBackoff }()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if len(report.Servers) != 1 || report.Servers[0] != prefix+"-cp1" {
		t.Errorf("Servers = %v, want exactly [%s] (no double-count)", report.Servers, prefix+"-cp1")
	}
	if len(report.Firewalls) != 1 || report.Firewalls[0] != prefix+"-fw" {
		t.Errorf("Firewalls = %v, want exactly [%s] (no double-count)", report.Firewalls, prefix+"-fw")
	}
}

func TestNamePrefixForSovereign_MatchesTofuEmit(t *testing.T) {
	// The prefix the Go side emits.
	got := NamePrefixForSovereign("omantel.omani.works")
	want := "catalyst-omantel-omani-works"
	if got != want {
		t.Fatalf("NamePrefixForSovereign drift: got %q, want %q", got, want)
	}

	// And: the OpenTofu module at infra/hetzner/main.tf MUST use the same
	// `catalyst-${replace(var.sovereign_fqdn, ".", "-")}-…` template on
	// every resource it creates. Walk up to the repo root, then read the
	// module file and assert the canonical pattern is present.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root not found (test running outside repo? %v)", err)
	}
	tfPath := filepath.Join(repoRoot, "infra", "providers", "hetzner", "main.tf")
	bytes, err := os.ReadFile(tfPath)
	if err != nil {
		t.Skipf("Tofu module not readable at %s: %v", tfPath, err)
	}
	canonical := `"catalyst-${replace(var.sovereign_fqdn, ".", "-")}`
	if !strings.Contains(string(bytes), canonical) {
		t.Fatalf("Tofu module %s does not use the canonical name template %q — "+
			"NamePrefixForSovereign and Tofu have drifted",
			tfPath, canonical)
	}
}
