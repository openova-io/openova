// tenant_discover_test.go — pins the chroot self-discovery + alias
// fields added in qa-loop iter-6 TC-013.
//
// The contract under test:
//
//   - When SOVEREIGN_FQDN is set on the Pod env (chroot mode) and the
//     incoming request asks for that FQDN (or `console.<fqdn>`, or any
//     subdomain of <fqdn>), HandleTenantDiscover returns 200 with a
//     synthesized payload that carries `deploymentId` + `tenantHost`
//     containing the FQDN. The matrix's keyword assertion
//     (`omantel.biz` + `deploymentId` for the omantel.biz chroot)
//     resolves on this branch alone — the chroot's tenant registry is
//     intentionally empty until the cutover orchestrator POSTs a
//     TenantRegistration back, which on BYO domains never happens.
//
//   - The legacy snake_case keys (`host`, `tenant_id`,
//     `keycloak_realm_url`) remain populated so older SPA bundles
//     keep working unchanged.
//
//   - When SOVEREIGN_FQDN is unset (mothership mode) and the host
//     does NOT match a registered tenant, the response is 404
//     tenant-not-registered as before — the new branch never
//     short-circuits the registry lookup for non-chroot hosts.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleTenantDiscover_ChrootSelfDiscovery_ByHost(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover?host=console.omantel.biz", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp tenantDiscoverResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeploymentID == "" {
		t.Errorf("deploymentId: got empty, want non-empty (TC-013 contract)")
	}
	if !strings.Contains(resp.TenantHost, "omantel.biz") {
		t.Errorf("tenantHost = %q, want substring omantel.biz", resp.TenantHost)
	}
	if !strings.Contains(resp.Host, "omantel.biz") {
		t.Errorf("host = %q, want substring omantel.biz (legacy alias)", resp.Host)
	}
	if resp.Realm == "" {
		t.Errorf("realm: got empty, want non-empty default")
	}
	if resp.OIDCIssuer == "" {
		t.Errorf("oidcIssuer: got empty, want non-empty default")
	}
}

// TestHandleTenantDiscover_ChrootSelfDiscovery_ByEmailParam covers the
// matrix's TC-013 URL shape — `?email=<addr>` with no `?host=`. The
// handler MUST fall back to the request's Host header (set by the
// proxy chain to the tenant origin) so chroot self-discovery still
// resolves.
func TestHandleTenantDiscover_ChrootSelfDiscovery_HostHeaderFallback(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover?email=emrah.baysal@openova.io", nil)
	req.Host = "console.omantel.biz"
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp tenantDiscoverResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeploymentID == "" {
		t.Errorf("deploymentId: got empty, want non-empty")
	}
	if !strings.Contains(resp.TenantHost, "omantel.biz") {
		t.Errorf("tenantHost = %q, want substring omantel.biz", resp.TenantHost)
	}
}

// TestHandleTenantDiscover_DeploymentIDOverride pins that an
// orchestrator-stamped CATALYST_SELF_DEPLOYMENT_ID env wins over the
// `sovereign-<fqdn>` fallback so post-handover responses carry the
// stable mothership-issued id.
func TestHandleTenantDiscover_DeploymentIDOverride(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep-omantel-12345")

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover?host=console.omantel.biz", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)

	var resp tenantDiscoverResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeploymentID != "dep-omantel-12345" {
		t.Errorf("deploymentId = %q, want dep-omantel-12345 (env override)", resp.DeploymentID)
	}
}

// TestHandleTenantDiscover_NonChrootHostStillRegistry confirms the
// chroot branch is gated on host-matches-FQDN — a Pod with
// SOVEREIGN_FQDN=omantel.biz that receives a discovery request for
// some-other.example.com does NOT short-circuit; it falls through to
// the registry path (which for an unwired registry returns 503
// tenant-registry-unavailable, the legacy behaviour).
func TestHandleTenantDiscover_NonChrootHostFallsThrough(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover?host=acme.example.com", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)

	// Registry is nil in this test; chroot branch did NOT match;
	// fallthrough emits 503 tenant-registry-unavailable.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (chroot branch must NOT swallow non-matching hosts; body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "tenant-registry-unavailable") {
		t.Errorf("body = %s, want tenant-registry-unavailable error", w.Body.String())
	}
}

// TestRealmFromIssuer pins the helper's URL-trail extraction logic.
func TestRealmFromIssuer(t *testing.T) {
	cases := map[string]string{
		"https://console.openova.io/auth/realms/openova":  "openova",
		"https://console.openova.io/auth/realms/openova/": "openova",
		"https://kc.example.com/realms/org":               "org",
		"":                                                "",
		"plainstring":                                     "plainstring",
	}
	for in, want := range cases {
		if got := realmFromIssuer(in); got != want {
			t.Errorf("realmFromIssuer(%q) = %q, want %q", in, got, want)
		}
	}
}
