package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// UAT row 218 (#6081) — the per-Org console's install wizard could not name the
// Organization it is scoped to.
//
// `listOrganizations()` (ui/src/lib/organizations.api.ts) composes the PARENT
// row from GET /api/v1/sovereign/self with the SUB-ORG rows from
// GET /api/v1/organizations. Only the first of those is on the Org-scoped
// allowlist, so OrgScopeGuard 403'd the second, `listOrgRecords` swallowed the
// non-2xx into `[]` (bss.api.ts:826-828), and the wizard's org select rendered
// with exactly one option: the Sovereign self-org. #5823's pre-select then
// filtered that lone parent row out BY DESIGN, leaving zero candidates and an
// unselected control — the honest outcome for the list it was handed.
//
// The pre-select was never the defect. The candidate list was.
//
// These tests pin BOTH halves of the fix, because either half alone is a new
// defect: opening the route without confining the handler hands a customer the
// whole Sovereign's Organization directory, and confining the handler without
// opening the route leaves the list empty.

// orgScopedClaims builds the session an Org console mints (tier=org-admin +
// the Org slug), which is what claimsAreOrgScoped/orgScopeForRequest read.
func orgScopedClaims(slug string) *auth.Claims {
	return &auth.Claims{Email: "customer@" + slug, Tier: orgScopedTier, Org: slug}
}

func withDirClaims(r *http.Request, c *auth.Claims) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, c))
}

// decodeOrgSlugs returns the SUBDOMAIN (slug) of every row in a directory
// response. Asserting on the slugs rather than on the presence of the `items`
// key is deliberate: an empty array carries that key too, so a key-presence
// assertion passes on exactly the failure this row recorded.
func decodeOrgSlugs(t *testing.T, body []byte) []string {
	t.Helper()
	var got struct {
		Items []orgTenantResponse `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode directory: %v (body=%s)", err, string(body))
	}
	out := make([]string, 0, len(got.Items))
	for _, it := range got.Items {
		out = append(out, it.Subdomain)
	}
	return out
}

func containsSlug(slugs []string, want string) bool {
	for _, s := range slugs {
		if s == want {
			return true
		}
	}
	return false
}

// TestListOrganizations_OrgScopedSessionSeesItsOwnOrg is the subject.
//
// The expectation is DERIVED from orgScopeForRequest — the same acceptance
// function HandleCreateInstance uses to force an Org-scoped install into the
// caller's own Org (#4937). Deriving it here rather than hardcoding "uatwalk91"
// is what stops the two sides drifting again: if the scope seam ever resolves a
// different Org than the directory serves, this fails instead of quietly
// agreeing with a literal.
func TestListOrganizations_OrgScopedSessionSeesItsOwnOrg(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("uatwalk91", "UAT Walk 91", "omani.homes", "owner@uatwalk91.test", "Ready"),
		orgReadyCR("otherorg", "Someone Else", "omani.homes", "owner@otherorg.test", "Ready"),
		orgReadyCR("omantel", "Omantel", "", "admin@omantel.biz", "Ready"),
	)

	req := withDirClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil),
		orgScopedClaims("uatwalk91"),
	)

	wantSlug, scoped := h.orgScopeForRequest(req)
	if !scoped || wantSlug == "" {
		t.Fatalf("fixture is inert: orgScopeForRequest reported scoped=%v slug=%q — "+
			"the request this test drives is not Org-scoped at all, so it could not "+
			"discriminate the fix", scoped, wantSlug)
	}

	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	slugs := decodeOrgSlugs(t, w.Body.Bytes())

	if !containsSlug(slugs, wantSlug) {
		t.Errorf("the Org-scoped console cannot see its OWN Organization %q — "+
			"directory returned %v. This is UAT row 218: the install wizard's org "+
			"select has no candidate to pre-select.", wantSlug, slugs)
	}
	if len(slugs) != 1 {
		t.Errorf("an Org-scoped session must see EXACTLY its own Organization, got %v "+
			"(%d rows) — anything more is a cross-Org directory leak", slugs, len(slugs))
	}
	for _, s := range slugs {
		if s != wantSlug {
			t.Errorf("cross-Org leak: Org-scoped session for %q was served row %q", wantSlug, s)
		}
	}
}

// TestOrgScopeGuard_OrgScoped_ReadsOrganizationsDirectory — the route half. The
// handler above can be perfectly confined and the console still sees nothing if
// the guard never lets the request through.
func TestOrgScopeGuard_OrgScoped_ReadsOrganizationsDirectory(t *testing.T) {
	h := &Handler{log: quietLog()}

	reached := false
	guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withDirClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil),
		orgScopedClaims("acme"),
	)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("the Org console's own directory read was 403'd (status %d) — "+
			"listOrgRecords turns that into an empty array and the install wizard "+
			"renders no candidate Organization (UAT row 218)", rec.Code)
	}
}

// TestOrgScopeGuard_OrgScoped_StillCannotWriteOrganizations is the CONTROL that
// holds the fix to opening a READ rather than a path.
//
// POST /api/v1/organizations (create) and DELETE /api/v1/organizations/{id}
// share the read's path prefix, and pathIsOrgSafe is method-blind — so a
// prefix entry on the existing allowlist would hand every customer session
// org-create AND org-delete. org_scope_test.go's wipe test states the rule this
// control enforces from the other direction: the guard "must not become
// method-sensitive in a way that lets a write through on a path whose reads are
// denied." Admitting a read on a path whose writes stay denied is the inverse,
// and this test is what proves it stayed the inverse.
func TestOrgScopeGuard_OrgScoped_StillCannotWriteOrganizations(t *testing.T) {
	cases := []struct {
		method string
		path   string
		what   string
	}{
		{http.MethodPost, "/api/v1/organizations", "create an Organization"},
		{http.MethodDelete, "/api/v1/organizations/uuid-1", "delete an Organization"},
		{http.MethodPost, "/api/v1/organizations/uuid-1/reconcile", "re-run another Org's pipeline"},
		{http.MethodPut, "/api/v1/organizations", "overwrite the Organization directory"},
		{http.MethodPatch, "/api/v1/organizations/uuid-1", "patch an Organization"},
	}
	for _, c := range cases {
		h := &Handler{log: quietLog()}
		reached := false
		guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))

		req := withDirClaims(httptest.NewRequest(c.method, c.path, nil), orgScopedClaims("acme"))
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)

		if reached {
			t.Errorf("%s %s: an Org-scoped customer session REACHED the handler and could %s",
				c.method, c.path, c.what)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: want 403 for an Org-scoped session, got %d", c.method, c.path, rec.Code)
		}
	}
}

// TestOrgScopeGuard_OrgScoped_SubPathReadsStayDenied is the second CONTROL on
// the route half: only the directory COLLECTION is opened, never the
// per-Organization detail route, which is addressed by another Org's id.
func TestOrgScopeGuard_OrgScoped_SubPathReadsStayDenied(t *testing.T) {
	h := &Handler{log: quietLog()}
	reached := false
	guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withDirClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/organizations/uuid-of-another-org", nil),
		orgScopedClaims("acme"),
	)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if reached {
		t.Fatalf("an Org-scoped session reached another Organization's detail route")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 on the org-detail route, got %d", rec.Code)
	}
}

// TestListOrganizations_SovereignAdminStillSeesEveryOrg is the CONTROL that
// keeps the handler-side confinement from being a blanket filter. The operator
// directory is the whole point of GET /api/v1/organizations; a fix that made
// this test fail would have "closed" row 218 by emptying the Sovereign console.
func TestListOrganizations_SovereignAdminStillSeesEveryOrg(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("uatwalk91", "UAT Walk 91", "omani.homes", "owner@uatwalk91.test", "Ready"),
		orgReadyCR("otherorg", "Someone Else", "omani.homes", "owner@otherorg.test", "Ready"),
		orgReadyCR("omantel", "Omantel", "", "admin@omantel.biz", "Ready"),
	)

	req := withDirClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil),
		&auth.Claims{Email: "operator@openova.io", Tier: "owner"},
	)
	if _, scoped := h.orgScopeForRequest(req); scoped {
		t.Fatalf("control is inert: the operator request resolved as Org-scoped")
	}

	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)

	slugs := decodeOrgSlugs(t, w.Body.Bytes())
	for _, want := range []string{"uatwalk91", "otherorg", "omantel"} {
		if !containsSlug(slugs, want) {
			t.Errorf("Sovereign-admin directory lost %q — got %v", want, slugs)
		}
	}
}

// TestListOrganizations_OrgScopedSessionWithNoMatchingRowStaysEmpty is the
// CONTROL against inventing a row. A confinement that SYNTHESISED the caller's
// Org when the directory holds none would make the subject test pass while
// telling the console an Organization exists that no CR backs.
func TestListOrganizations_OrgScopedSessionWithNoMatchingRowStaysEmpty(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("otherorg", "Someone Else", "omani.homes", "owner@otherorg.test", "Ready"),
	)

	req := withDirClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil),
		orgScopedClaims("ghostorg"),
	)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)

	slugs := decodeOrgSlugs(t, w.Body.Bytes())
	if len(slugs) != 0 {
		t.Errorf("an Org-scoped session whose Org has no record must get an EMPTY "+
			"directory, not an invented row or someone else's: got %v", slugs)
	}
}
