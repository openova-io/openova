package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// #5276 / row 214: the operator console migrated its member / app /
// backing-service sub-resource calls off the deprecated `/tenant/orgs/{id}/...`
// alias onto the canonical `/organizations/{id}/...` routes (banned-term
// eradication — "tenant" → Organization). This test locks in that the tenant
// service actually registers the canonical routes AND keeps the legacy aliases
// resolving for one-release deprecation, so the console fix can never silently
// regress to a 404.
func TestOrganizationSubResourceRoutes_CanonicalAndLegacyAliases(t *testing.T) {
	mux, ok := (&Handler{}).Routes().(*http.ServeMux)
	if !ok {
		t.Fatalf("Routes() concrete type is not *http.ServeMux")
	}

	cases := []struct {
		name        string
		method      string
		path        string
		wantPattern string
	}{
		// Canonical `/organizations/{id}/...` sub-resources (the console now
		// calls these).
		{"canonical list members", "GET", "/organizations/o1/members", "GET /organizations/{id}/members"},
		{"canonical invite member", "POST", "/organizations/o1/members", "POST /organizations/{id}/members"},
		{"canonical remove member", "DELETE", "/organizations/o1/members/u1", "DELETE /organizations/{id}/members/{userId}"},
		{"canonical install app", "POST", "/organizations/o1/apps", "POST /organizations/{id}/apps"},
		{"canonical uninstall app", "DELETE", "/organizations/o1/apps/wordpress", "DELETE /organizations/{id}/apps/{slug}"},
		{"canonical uninstall preview", "GET", "/organizations/o1/apps/wordpress/uninstall-preview", "GET /organizations/{id}/apps/{slug}/uninstall-preview"},
		{"canonical backing services", "GET", "/organizations/o1/backing-services", "GET /organizations/{id}/backing-services"},

		// Legacy `/tenant/orgs/{id}/...` aliases still resolve (kept for one
		// release so any pinned consumer keeps working during rolling deploy).
		{"legacy list members", "GET", "/tenant/orgs/o1/members", "GET /tenant/orgs/{id}/members"},
		{"legacy invite member", "POST", "/tenant/orgs/o1/members", "POST /tenant/orgs/{id}/members"},
		{"legacy remove member", "DELETE", "/tenant/orgs/o1/members/u1", "DELETE /tenant/orgs/{id}/members/{userId}"},
		{"legacy install app", "POST", "/tenant/orgs/o1/apps", "POST /tenant/orgs/{id}/apps"},
		{"legacy uninstall app", "DELETE", "/tenant/orgs/o1/apps/wordpress", "DELETE /tenant/orgs/{id}/apps/{slug}"},
		{"legacy uninstall preview", "GET", "/tenant/orgs/o1/apps/wordpress/uninstall-preview", "GET /tenant/orgs/{id}/apps/{slug}/uninstall-preview"},
		{"legacy backing services", "GET", "/tenant/orgs/o1/backing-services", "GET /tenant/orgs/{id}/backing-services"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			_, pattern := mux.Handler(req)
			if pattern != tc.wantPattern {
				t.Errorf("%s %s resolved to pattern %q, want %q", tc.method, tc.path, pattern, tc.wantPattern)
			}
		})
	}
}

// deprecatedAlias must carry the RFC-8594 Deprecation/Sunset signalling and a
// successor-version Link pointing at the canonical `/organizations` path, so
// consumers still on the legacy alias are told where to migrate.
func TestDeprecatedAlias_EmitsRFC8594Headers(t *testing.T) {
	called := false
	h := deprecatedAlias("/api/organizations/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/tenant/orgs/o1/members", nil))

	if !called {
		t.Fatal("deprecatedAlias did not invoke the wrapped handler")
	}
	if got := rec.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation header = %q, want %q", got, "true")
	}
	if got := rec.Header().Get("Sunset"); got == "" {
		t.Error("Sunset header missing")
	}
	if got := rec.Header().Get("Link"); got != `</api/organizations/{id}/members>; rel="successor-version"` {
		t.Errorf("Link header = %q, want successor-version pointing at the canonical path", got)
	}
}
