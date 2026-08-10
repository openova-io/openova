// parent_domains_org_pool_divergence_test.go — UAT row 8 regression lock.
//
// Row 8 walks the sovereign-console "Create Organization" form and picks an
// org-pool parent domain. On hw293 the select rendered DISABLED with a single
// empty option: GET /api/v1/sovereign/parent-domains answered with the
// `primary` row and ZERO `role: "org-pool"` rows, while org-pool parents were
// live on Organization records.
//
// The divergence is between two functions that the file comment on
// sovereign_parent_domains.go says cannot diverge:
//
//	"Reads from the same global parentDomainStore that ListParentDomains
//	 surfaces, so what the operator sees in the dropdown == what the create
//	 handler accepts."
//
//   - the CREATE path validates against poolDomainsForOrgCreate(deps), whose
//     first rung is OrganizationDeps.ParentDomains — seeded at startup from
//     LoadOrganizationParentDomainsFromEnv (CATALYST_ORG_POOL_DOMAINS, or the
//     four canonical .omani.X entries).
//   - the LIST path (ListParentDomains) reads ONLY the adopted Deployment's
//     Request.ParentDomains plus the synthesised primary. A post-handover
//     Sovereign persists no Deployment record at all (handover is JWT-only —
//     see lookupPrimaryDomain's own SOVEREIGN_FQDN fallback comment), so that
//     slice is empty and the endpoint has nothing but the primary to report.
//
// So the operator cannot select a parent the server would have accepted. The
// assertions below are on the VALUES in the response, never on the presence of
// the `items` key — an empty items array carries an `items` key too.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// listPoolRoles issues the real GET and returns the names bucketed by role.
func listPoolRoles(t *testing.T, h *Handler) (primary []string, orgPool []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	for _, it := range resp.Items {
		switch it.Role {
		case RolePrimary:
			primary = append(primary, it.Name)
		case RoleOrgPool:
			orgPool = append(orgPool, it.Name)
		}
	}
	return primary, orgPool
}

// The hw293 shape: a post-handover Sovereign. No adopted Deployment record
// exists (handover is JWT-only), the primary comes from SOVEREIGN_FQDN, and
// the org-pool the create handler validates against lives in
// OrganizationDeps.ParentDomains.
func newPostHandoverSovereign(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	h := &Handler{log: slog.Default()}
	h.SetOrganizationDeps(OrganizationDeps{
		ParentDomains: []OrganizationParentDomain{
			{Name: "omani.homes", Role: "org-pool", NSFlipReady: true},
			{Name: "omani.rest", Role: "org-pool", NSFlipReady: true},
			{Name: "omani.trade", Role: "org-pool", NSFlipReady: true},
		},
	})
	return h
}

func TestListParentDomains_SurfacesTheOrgPoolTheCreateHandlerAccepts(t *testing.T) {
	h := newPostHandoverSovereign(t)

	// What the CREATE path would accept — the contract the dropdown must match.
	accepted := h.poolDomainsForOrgCreate(h.orgTenantDeps)
	if len(accepted) == 0 {
		t.Fatalf("fixture is inert: the create path accepts no org-pool parent, so this test could not fail")
	}

	primary, orgPool := listPoolRoles(t, h)

	if len(primary) != 1 || primary[0] != "hw293.omantel.biz" {
		t.Fatalf("primary row: want [hw293.omantel.biz], got %v", primary)
	}
	// The actual row-8 assertion: every parent the create handler accepts is
	// offered by the list endpoint. Asserted on the NAMES, not on a count.
	offered := map[string]struct{}{}
	for _, n := range orgPool {
		offered[n] = struct{}{}
	}
	for _, a := range accepted {
		if _, ok := offered[a.Name]; !ok {
			t.Errorf("create handler accepts parent_domain=%q but GET /sovereign/parent-domains never offers it (org-pool rows returned: %v)", a.Name, orgPool)
		}
	}
}

// CONTROL 1 — the persistent record still wins where it exists. An adopted
// Deployment carrying admin-added org-pool rows must keep surfacing them, and
// must not be displaced by the startup seed (#837).
func TestListParentDomains_PersistedAdminRowsStillSurface(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_FQDN", "")
	h := &Handler{log: slog.Default()}
	seedActiveDeployment(t, h, "hw293.omantel.biz", "acquired-portfolio.example")
	h.SetOrganizationDeps(OrganizationDeps{
		ParentDomains: []OrganizationParentDomain{
			{Name: "omani.homes", Role: "org-pool", NSFlipReady: true},
		},
	})

	_, orgPool := listPoolRoles(t, h)
	found := false
	for _, n := range orgPool {
		if n == "acquired-portfolio.example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("persisted admin-added parent domain disappeared from the list: %v", orgPool)
	}
}

// CONTROL 2 — the endpoint must not invent a pool. With no deps seed and no
// deployment record, the org-pool half stays EMPTY. This is what stops the fix
// from being "always print the four canonical .omani.X names".
func TestListParentDomains_NoSeedNoDeployment_StillReportsNoOrgPool(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	h := &Handler{log: slog.Default()}

	primary, orgPool := listPoolRoles(t, h)
	if len(primary) != 1 {
		t.Fatalf("primary row: want exactly 1, got %v", primary)
	}
	if len(orgPool) != 0 {
		t.Fatalf("no pool is wired anywhere, yet the endpoint reported org-pool rows: %v", orgPool)
	}
}

// CONTROL 3 — a `primary` entry in the deps seed is NOT promoted into the
// org-pool half. Epic #825: primary domains are not bookable by Organizations,
// and poolDomainsForOrgCreate filters them out, so the list must too.
func TestListParentDomains_PrimaryRoleInSeedIsNotOfferedAsOrgPool(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	h := &Handler{log: slog.Default()}
	h.SetOrganizationDeps(OrganizationDeps{
		ParentDomains: []OrganizationParentDomain{
			{Name: "hw293.omantel.biz", Role: "primary", NSFlipReady: true},
			{Name: "omani.homes", Role: "org-pool", NSFlipReady: true},
		},
	})

	_, orgPool := listPoolRoles(t, h)
	for _, n := range orgPool {
		if n == "hw293.omantel.biz" {
			t.Fatalf("the Sovereign primary was offered as a bookable org-pool parent: %v", orgPool)
		}
	}
	if len(orgPool) != 1 || orgPool[0] != "omani.homes" {
		t.Fatalf("org-pool rows: want [omani.homes], got %v", orgPool)
	}
}

// CONTROL 4 — flipStatus must reflect the seed's NSFlipReady, not a blanket
// "ready". The console's isParentDomainReady() gates pre-selection on it
// (org.api.ts), so a not-yet-flipped parent must not present as ready.
func TestListParentDomains_SeedNotFlipReadySurfacesAsNotReady(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	h := &Handler{log: slog.Default()}
	h.SetOrganizationDeps(OrganizationDeps{
		ParentDomains: []OrganizationParentDomain{
			{Name: "omani.homes", Role: "org-pool", NSFlipReady: true},
			{Name: "not-flipped.example", Role: "org-pool", NSFlipReady: false},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	var resp struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]FlipStatus{}
	for _, it := range resp.Items {
		got[it.Name] = it.FlipStatus
	}
	if got["omani.homes"] != FlipStatusReady {
		t.Errorf("omani.homes flipStatus: want %q, got %q", FlipStatusReady, got["omani.homes"])
	}
	if got["not-flipped.example"] == FlipStatusReady {
		t.Errorf("not-flipped.example was reported ready despite NSFlipReady=false")
	}
}
