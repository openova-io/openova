package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/shared/middleware"
)

// createOrgAs posts body to CreateOrg as the signed-in user u-1. The Handler
// has no store, so a request that reaches the slug-uniqueness check panics: a
// test that passes proves the refusal comes before the store is asked.
func createOrgAs(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tenant/orgs", strings.NewReader(body))
	req = req.WithContext(middleware.WithClaims(context.Background(), map[string]any{"sub": "u-1"}))
	rec := httptest.NewRecorder()
	(&Handler{}).CreateOrg(rec, req)
	return rec
}

func assertReserved(t *testing.T, slug string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("slug %q: status %d, want %d; body %s", slug, rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("slug %q: body is not JSON: %v: %s", slug, err, rec.Body.String())
	}
	if got["error"] != "slug is reserved" {
		t.Fatalf("slug %q: error %q, want %q", slug, got["error"], "slug is reserved")
	}
}

// TestCreateOrg_SlugMgmtIsRefused: the slug mgmt is answered with HTTP 422
// and the message "slug is reserved".
func TestCreateOrg_SlugMgmtIsRefused(t *testing.T) {
	assertReserved(t, "mgmt", createOrgAs(t, `{"slug":"mgmt","name":"Mgmt"}`))
}

// TestCreateOrg_RefusesEveryReservedSlug: every word of reservedSlugs is
// answered with HTTP 422, before the store is asked.
func TestCreateOrg_RefusesEveryReservedSlug(t *testing.T) {
	if len(reservedSlugs) < 60 {
		t.Fatalf("reservedSlugs holds %d words, want at least 60", len(reservedSlugs))
	}
	for slug := range reservedSlugs {
		b, err := json.Marshal(map[string]string{"slug": slug, "name": "Reserved"})
		if err != nil {
			t.Fatal(err)
		}
		assertReserved(t, slug, createOrgAs(t, string(b)))
	}
}

// TestValidTenantSlug_RefusesReserved: validTenantSlug is false for every
// reserved word and stays true for an ordinary slug.
func TestValidTenantSlug_RefusesReserved(t *testing.T) {
	for slug := range reservedSlugs {
		if validTenantSlug(slug) {
			t.Errorf("validTenantSlug(%q) = true for a reserved slug", slug)
		}
	}
	for _, slug := range []string{"acme-co", "bank-engineering", "mgmt-team"} {
		if !validTenantSlug(slug) {
			t.Errorf("validTenantSlug(%q) = false, want true", slug)
		}
	}
}
