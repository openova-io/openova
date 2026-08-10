// org_scope_binding_failclosed_test.go — the #4110 cross-Org binding must
// FAIL CLOSED when the Org identity cannot be established (#5516).
//
// The binding in resolveOrganization used to read:
//
//	if sessionOrg != "" && resolvedSlug != "" && sessionOrg != resolvedSlug { 403 }
//
// so an EMPTY value on either side fell through as "no mismatch". An absent
// value silently read as an absent constraint — the same defect class, in the
// same chain, as a missing `deployment_id` claim reading as "no deployment".
//
// Both empty cases were reachable:
//
//   - `orgSlugFromHost` returns "" for any registered tenant host that is not
//     `<console|api|marketplace|auth>.<slug>.<zone>` — a 2-label vanity host,
//     or one fronted by a different service label. resolveOrgScope already
//     KNEW this, which is why it falls back to the tenant id when the slug is
//     empty; resolveOrganization had no such fallback and simply skipped the
//     check.
//   - an Org-scoped session (tier=org-admin) carrying an empty `org` claim
//     skipped the binding outright.
//
// Why it matters here rather than as a generic hardening item: this is the
// binding HandleOrgApplicationInstall relies on to force the Application CR
// into the caller's own namespace. Skipping it means an Org session can WRITE
// into a sibling Organization — the precise cross-Org denial UAT row 223
// asserts the agentic path enforces.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// failClosedRegistry registers two Org tenants whose hosts carry NO derivable
// slug (`orgSlugFromHost` returns "" for both — the leading label is not one
// of console/api/marketplace/auth), plus one ordinary console-host Org used as
// the control.
func failClosedRegistry(t *testing.T) *store.TenantRegistry {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, r := range []store.TenantRegistration{
		{
			Host:                  "portal.demo.omani.homes", // leading label not in the slug switch → slug ""
			TenantID:              "demo",
			TenantKind:            store.TenantKindOrg,
			OrganizationNamespace: "org-demo-uuid",
		},
		{
			Host:                  "portal.acme.omani.homes", // the sibling Org a demo session must never reach
			TenantID:              "acme",
			TenantKind:            store.TenantKindOrg,
			OrganizationNamespace: "org-acme-uuid",
		},
		{
			Host:                  "console.demo.omani.homes", // ordinary shape → slug "demo"
			TenantID:              "demo",
			TenantKind:            store.TenantKindOrg,
			OrganizationNamespace: "org-demo-uuid",
		},
	} {
		if err := reg.Put(r); err != nil {
			t.Fatalf("put %s: %v", r.Host, err)
		}
	}
	return reg
}

func failClosedInstall(t *testing.T, host string, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "version": "1.2.3", "name": "leak"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/applications", strings.NewReader(string(raw)))
	req.Header.Set("X-Tenant-Host", host)
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	h := &Handler{log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	h.SetTenantRegistry(failClosedRegistry(t))
	rec := httptest.NewRecorder()
	h.HandleOrgApplicationInstall(rec, req)
	return rec
}

// Test_ResolveOrganization_CrossOrgForgedHostWithNoDerivableSlug — THE HOLE.
// A demo-scoped session forges the sibling Org's host. Because that host
// yields no slug, the old binding skipped its comparison entirely and let the
// request through to the install core, which would then force the Application
// CR into ACME's namespace.
//
// WITHOUT THE FIX this fails: the response is the install core's
// catalog-not-wired envelope instead of 403 org-scope-mismatch.
func Test_ResolveOrganization_CrossOrgForgedHostWithNoDerivableSlug(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := failClosedInstall(t, "portal.acme.omani.homes", claims)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "org-scope-mismatch") {
		t.Fatalf("a forged sibling-Org host must be 403 org-scope-mismatch even when the host carries no derivable slug; got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// Test_ResolveOrganization_OwnHostWithNoDerivableSlugStillPasses — THE
// CONTROL. The same session, the same unusual host shape, but its OWN Org:
// it must still be admitted, binding on the tenant id exactly as
// resolveOrgScope stamps it. Without this the test above could be satisfied
// by simply 403-ing every slug-less host, which would break every such Org.
func Test_ResolveOrganization_OwnHostWithNoDerivableSlugStillPasses(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := failClosedInstall(t, "portal.demo.omani.homes", claims)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("an Org session on its OWN tenant host must be admitted, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "catalog-not-wired") {
		t.Fatalf("expected the install core to be reached (catalog-not-wired), got %d: %s", rec.Code, rec.Body.String())
	}
}

// Test_ResolveOrganization_OrgScopedSessionWithEmptyOrgClaimIsDenied — the
// second empty case. A tier=org-admin session carrying no `org` claim has no
// Organization to be confined to, so it must be denied rather than admitted
// to whichever Org its X-Tenant-Host names.
func Test_ResolveOrganization_OrgScopedSessionWithEmptyOrgClaimIsDenied(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier} // org-scoped tier, no Org
	rec := failClosedInstall(t, "console.demo.omani.homes", claims)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "org-scope-mismatch") {
		t.Fatalf("an Org-scoped session with no org claim must be denied, not admitted to the named tenant; got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// Test_ResolveOrganization_SovereignAdminSessionIsUnaffected — the blast-radius
// control. The binding applies ONLY to Org-scoped sessions; a sovereign-admin
// (no org claim, non-org tier) legitimately operates any Organization and must
// keep reaching the install core on any host. If the fail-closed change had
// leaked past claimsAreOrgScoped, this is what would catch it.
func Test_ResolveOrganization_SovereignAdminSessionIsUnaffected(t *testing.T) {
	claims := &auth.Claims{Tier: "owner"}
	rec := failClosedInstall(t, "portal.acme.omani.homes", claims)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("a sovereign-admin session must not be caught by the Org binding, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "catalog-not-wired") {
		t.Fatalf("expected the install core to be reached (catalog-not-wired), got %d: %s", rec.Code, rec.Body.String())
	}
}
