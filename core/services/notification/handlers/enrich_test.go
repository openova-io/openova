package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWorkspaceURL_ConsolePrefix_PerSovereignParentZone is the TBD-A67
// regression guard for issue #1990. The pure-function `workspaceURL`
// helper MUST emit `https://console.<subdomain>.<parentZone>` for every
// non-empty subdomain + parent zone pair, and MUST NEVER reach for the
// hardcoded `.openova.io` host the previous implementation appended.
// The canonical shape mirrors:
//
//   - products/catalyst/bootstrap/api/internal/handler/
//     org_tenant_gitops.go:536 (chart-side host derivation)
//   - core/controllers/organization/internal/controller/
//     tenant_route.go:113 (per-Org HTTPRoute hostname)
//
// Drift between any of the three would surface as "email points at a
// host that does not 200" — a class of bug we cannot afford for
// onboarding-day-1 outbound mail.
func TestWorkspaceURL_ConsolePrefix_PerSovereignParentZone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		subdomain  string
		parentZone string
		want       string
	}{
		{
			name:       "omani.homes sovereign",
			subdomain:  "acme",
			parentZone: "omani.homes",
			want:       "https://console.acme.omani.homes",
		},
		{
			name:       "talents.scope sovereign — proves zero coupling to openova.io",
			subdomain:  "delta",
			parentZone: "talents.scope",
			want:       "https://console.delta.talents.scope",
		},
		{
			name:       "whitespace is trimmed on both sides",
			subdomain:  " acme ",
			parentZone: " omani.homes ",
			want:       "https://console.acme.omani.homes",
		},
		{
			name:       "empty parent zone yields empty URL (no openova.io fallback)",
			subdomain:  "acme",
			parentZone: "",
			want:       "",
		},
		{
			name:       "empty subdomain yields empty URL",
			subdomain:  "",
			parentZone: "omani.homes",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workspaceURL(tc.subdomain, tc.parentZone)
			if got != tc.want {
				t.Errorf("workspaceURL(%q, %q) = %q, want %q",
					tc.subdomain, tc.parentZone, got, tc.want)
			}
			// Hard regression check: ANY non-empty output must contain
			// the canonical `console.` infix immediately after the
			// scheme. Without this, a future refactor that drops the
			// prefix produces a valid-looking URL that 404s on the
			// Cilium Gateway.
			if got != "" && !strings.HasPrefix(got, "https://console.") {
				t.Errorf("rendered URL %q missing required `console.` infix per CLAUDE.md §0", got)
			}
			// TBD-A67 regression guard: the platform marketing domain
			// `openova.io` must NEVER appear in a per-tenant
			// WorkspaceURL. The old enrich.go:81 hardcode leaked it
			// onto every non-openova.io Sovereign.
			if strings.Contains(got, "openova.io") {
				t.Errorf("workspaceURL leaked .openova.io into output %q — must use per-Sovereign parent zone only", got)
			}
		})
	}
}

// TestEnricher_Lookup_WorkspaceURL_UsesParentZone covers the end-to-end
// path inside Lookup: when the enricher is constructed with a per-
// Sovereign parent zone, the returned TenantInfo.WorkspaceURL MUST
// render `https://console.<subdomain>.<parentZone>` from the tenant's
// subdomain field. The fake tenant + auth servers stub the upstream
// admin endpoints so we exercise only the enrichment seam.
func TestEnricher_Lookup_WorkspaceURL_UsesParentZone(t *testing.T) {
	t.Parallel()

	tenantSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tenant/admin/tenants/") {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/tenant/admin/tenants/")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        id,
			"name":      "Acme Inc",
			"owner_id":  "user-1",
			"subdomain": "acme",
		})
	}))
	defer tenantSvc.Close()
	authSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/auth/admin/users/") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{
				"id":    "user-1",
				"email": "owner@acme.example",
				"name":  "Alice Owner",
			},
		})
	}))
	defer authSvc.Close()

	e := NewEnricher(tenantSvc.URL, authSvc.URL, "omani.homes", []byte("test-secret"))
	info, err := e.Lookup(context.Background(), "tenant-abc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if info == nil {
		t.Fatal("Lookup returned nil TenantInfo")
	}
	if got, want := info.WorkspaceURL, "https://console.acme.omani.homes"; got != want {
		t.Errorf("WorkspaceURL: got %q, want %q", got, want)
	}
	// TBD-A67 regression guard: rendered body MUST NOT contain the
	// platform marketing host. The previous implementation appended
	// `.openova.io` unconditionally.
	if strings.Contains(info.WorkspaceURL, "openova.io") {
		t.Errorf("WorkspaceURL leaked .openova.io into per-tenant URL %q", info.WorkspaceURL)
	}
}

// TestEnricher_Lookup_WorkspaceURL_EmptyParentZone covers the no-op
// degrade path: when the operator hasn't wired TENANT_PARENT_DOMAIN
// (or set it empty), WorkspaceURL renders to the empty string rather
// than fall back to a hardcoded host. The email template treats empty
// as "omit the URL line" instead of emit a wrong link.
func TestEnricher_Lookup_WorkspaceURL_EmptyParentZone(t *testing.T) {
	t.Parallel()

	tenantSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/tenant/admin/tenants/")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        id,
			"name":      "Acme Inc",
			"owner_id":  "user-1",
			"subdomain": "acme",
		})
	}))
	defer tenantSvc.Close()
	authSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{
				"id":    "user-1",
				"email": "owner@acme.example",
				"name":  "Alice Owner",
			},
		})
	}))
	defer authSvc.Close()

	e := NewEnricher(tenantSvc.URL, authSvc.URL, "", []byte("test-secret"))
	info, err := e.Lookup(context.Background(), "tenant-abc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if info.WorkspaceURL != "" {
		t.Errorf("WorkspaceURL with empty parent zone: got %q, want empty string", info.WorkspaceURL)
	}
}
