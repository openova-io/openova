// purge_firewall_retry_test.go — regression sentinel for issue #706.
//
// The bug: Hetzner firewall delete fails with 422 resource_in_use while
// the soft-deleted server is still detaching the firewall. Server delete
// is async (returns 200 "action started") so the firewall stays attached
// for 5-30 seconds. The previous wipe code tried firewall delete once,
// swallowed the 422, and reported `0 firewalls deleted`. Verified leak
// on otech50 (2026-05-03).
//
// These tests pin the retry contract:
//
//   1. TestFirewallRetry_Server_Detach_Async — fake hcloud returns 422
//      twice then 204; assert FirewallsRemoved=1 and at least one retry
//      was recorded in PurgeReport.FirewallsRetried.
//
//   2. TestFirewallRetry_Exhausted — fake hcloud always returns 422;
//      assert PurgeReport.Errors contains the FW id, FirewallsRemoved=0,
//      and the retry counter shows we attempted the full window.
//
// Tests run with firewallRetryInitialBackoff shrunk to 1ms so they
// complete under 100ms.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHetznerFirewallRetry is a Hetzner Cloud API stub that lets each
// list endpoint serve one resource and lets the firewall DELETE handler
// switch behaviour after N calls (controlled per-test).
type fakeHetznerFirewallRetry struct {
	t            *testing.T
	wantSelector string

	// firewallDeleteCalls counts the number of DELETE /v1/firewalls/<id>
	// calls received. Used by handlers to switch between 422 and 204.
	firewallDeleteCalls atomic.Int32

	// firewallSucceedAfter — return 422 for the first N attempts, then
	// 204. Set to 0 to always 204. Set to math.MaxInt32 to always 422.
	firewallSucceedAfter int32
}

func (f *fakeHetznerFirewallRetry) handler() http.Handler {
	mux := http.NewServeMux()

	// The five list endpoints Purge walks. Servers/LBs/networks/SSH-keys
	// each return a single resource that DELETEs cleanly so the test
	// only exercises firewall retry behaviour.
	listEndpoints := []struct {
		path string
		key  string
		id   int64
		name string
	}{
		{"/v1/servers", "servers", 1001, "fw-retry-server"},
		{"/v1/load_balancers", "load_balancers", 0, ""}, // empty list
		{"/v1/firewalls", "firewalls", 3003, "fw-retry-fw"},
		{"/v1/networks", "networks", 0, ""},
		{"/v1/ssh_keys", "ssh_keys", 0, ""},
	}
	for _, e := range listEndpoints {
		e := e
		mux.HandleFunc(e.path, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				selector := r.URL.Query().Get("label_selector")
				// Issue #732 — the no-selector pass (name-prefix fallback)
				// sends an empty selector. This fake returns empty in
				// that case so the second pass walks but finds nothing
				// to delete (the firewall-retry tests are about the
				// labelled-pass behaviour, not the prefix fallback).
				if selector == "" {
					body := map[string]any{
						"meta": map[string]any{"pagination": map[string]any{"next_page": nil}},
						e.key:  []any{},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(body)
					return
				}
				if selector != f.wantSelector {
					f.t.Errorf("%s: label_selector got %q, want %q", e.path, selector, f.wantSelector)
					http.Error(w, "wrong label selector", http.StatusBadRequest)
					return
				}
				var entries []map[string]any
				if e.id != 0 {
					entries = []map[string]any{{"id": e.id, "name": e.name}}
				}
				body := map[string]any{
					"meta": map[string]any{
						"pagination": map[string]any{"next_page": nil},
					},
					e.key: entries,
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
		// Per-resource DELETE under /v1/<kind>/.
		mux.HandleFunc(e.path+"/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if e.path == "/v1/firewalls" {
				n := f.firewallDeleteCalls.Add(1)
				if n <= f.firewallSucceedAfter {
					// 422 with the canonical Hetzner error body so the
					// retry path is exercised against realistic shape.
					w.WriteHeader(http.StatusUnprocessableEntity)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]string{
							"code":    "resource_in_use",
							"message": "Firewall still attached to a server, retry after server delete completes.",
						},
					})
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}

// firewallRetryTestSetup wires the package-level purgeHTTPClient at the
// httptest server, shrinks the firewall retry backoff to 1ms so the
// test completes quickly, and returns a cleanup func.
func firewallRetryTestSetup(t *testing.T, fake *fakeHetznerFirewallRetry) func() {
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

func TestFirewallRetry_Server_Detach_Async(t *testing.T) {
	const fqdn = "test-firewall-retry.example.com"
	fake := &fakeHetznerFirewallRetry{
		t:                    t,
		wantSelector:         "catalyst.openova.io/sovereign=" + fqdn,
		firewallSucceedAfter: 2, // 422, 422, then 204 on attempt 3
	}
	cleanup := firewallRetryTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(report.Firewalls) != 1 {
		t.Fatalf("FirewallsRemoved (len Firewalls) = %d, want 1; report=%+v", len(report.Firewalls), report)
	}
	if report.Firewalls[0] != "fw-retry-fw" {
		t.Errorf("Firewalls[0] = %q, want %q", report.Firewalls[0], "fw-retry-fw")
	}
	if report.FirewallsRetried < 1 {
		t.Errorf("FirewallsRetried = %d, want >= 1 (we returned 422 twice before 204)", report.FirewallsRetried)
	}
	for _, e := range report.Errors {
		if strings.Contains(e, "firewall") {
			t.Errorf("unexpected firewall error after eventual success: %q", e)
		}
	}
	if got := fake.firewallDeleteCalls.Load(); got != 3 {
		t.Errorf("firewall DELETE called %d times, want 3 (422, 422, 204)", got)
	}
}

func TestFirewallRetry_Exhausted(t *testing.T) {
	const fqdn = "test-firewall-stuck.example.com"
	fake := &fakeHetznerFirewallRetry{
		t:                    t,
		wantSelector:         "catalyst.openova.io/sovereign=" + fqdn,
		firewallSucceedAfter: 1 << 30, // never succeed
	}
	cleanup := firewallRetryTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge top-level: %v", err)
	}
	if len(report.Firewalls) != 0 {
		t.Errorf("Firewalls = %v, want empty (delete should have failed every attempt)", report.Firewalls)
	}
	// We expect at least one error mentioning firewall id 3003.
	foundFWErr := false
	for _, e := range report.Errors {
		if strings.Contains(e, "firewall") && strings.Contains(e, "3003") {
			foundFWErr = true
			break
		}
	}
	if !foundFWErr {
		t.Fatalf("expected an error mentioning firewall id 3003 in Errors, got %v", report.Errors)
	}
	// And the retry counter MUST show we attempted the full window.
	if report.FirewallsRetried < firewallRetryAttempts-1 {
		t.Errorf("FirewallsRetried = %d, want >= %d (full retry window)", report.FirewallsRetried, firewallRetryAttempts-1)
	}
	// And the fake recorded the full attempt count.
	if got := fake.firewallDeleteCalls.Load(); got != int32(firewallRetryAttempts) {
		t.Errorf("firewall DELETE called %d times, want %d (every attempt 422)", got, firewallRetryAttempts)
	}
}

// TestFirewallRetry_AlreadyGone_404 — if the firewall is already deleted
// (e.g. tofu destroy already removed it), the first DELETE returns 404
// which the retry helper treats as success without retrying.
func TestFirewallRetry_AlreadyGone_404(t *testing.T) {
	const fqdn = "test-firewall-404.example.com"
	mux := http.NewServeMux()
	listed := false
	mux.HandleFunc("/v1/servers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":    map[string]any{"pagination": map[string]any{"next_page": nil}},
			"servers": []any{},
		})
	})
	mux.HandleFunc("/v1/load_balancers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":           map[string]any{"pagination": map[string]any{"next_page": nil}},
			"load_balancers": []any{},
		})
	})
	mux.HandleFunc("/v1/networks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":     map[string]any{"pagination": map[string]any{"next_page": nil}},
			"networks": []any{},
		})
	})
	mux.HandleFunc("/v1/ssh_keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":     map[string]any{"pagination": map[string]any{"next_page": nil}},
			"ssh_keys": []any{},
		})
	})
	mux.HandleFunc("/v1/firewalls", func(w http.ResponseWriter, _ *http.Request) {
		listed = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":      map[string]any{"pagination": map[string]any{"next_page": nil}},
			"firewalls": []map[string]any{{"id": 4242, "name": "fw-already-gone"}},
		})
	})
	mux.HandleFunc("/v1/firewalls/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
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

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if !listed {
		t.Fatalf("expected firewalls list to have been called")
	}
	// 404 = success path; the firewall counts as removed.
	if len(report.Firewalls) != 1 || report.Firewalls[0] != "fw-already-gone" {
		t.Errorf("Firewalls = %v, want [fw-already-gone] (404 should be idempotent success)", report.Firewalls)
	}
	if report.FirewallsRetried != 0 {
		t.Errorf("FirewallsRetried = %d, want 0 (404 should NOT trigger a retry)", report.FirewallsRetried)
	}
	for _, e := range report.Errors {
		if strings.Contains(strings.ToLower(e), "firewall") {
			t.Errorf("unexpected firewall error on 404 path: %q", e)
		}
	}
}

// TestTotalBackoffWindow_HumanReadable confirms the helper used in error
// messages produces a non-zero, sensible duration.
func TestTotalBackoffWindow_HumanReadable(t *testing.T) {
	orig := firewallRetryInitialBackoff
	firewallRetryInitialBackoff = 6 * time.Second
	defer func() { firewallRetryInitialBackoff = orig }()
	got := totalBackoffWindow()
	want := 90 * time.Second // 6 + 12 + 24 + 48 = 90
	if got != want {
		t.Fatalf("totalBackoffWindow with default 6s init = %s, want %s", got, want)
	}
	if !strings.Contains(fmt.Sprintf("%s", got), "1m30s") {
		t.Errorf("expected formatted duration to render '1m30s', got %s", got)
	}
}
