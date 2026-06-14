// sso_init_test.go — unit tests for the #3374 zero-click bare-URL SSO
// landing page handler.
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSOInitHandler_ServesLandingAtRoot(t *testing.T) {
	h := SSOInitHandler(SSOInitConfig{Slug: "sovereign"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	// The page must run NewAPI's own init sequence: mint state, read the
	// provider, redirect to the authorize URL.
	for _, want := range []string{
		"/api/oauth/state",
		"/api/status",
		"custom_oauth_providers",
		"/oauth/\" + p.slug", // redirect_uri construction
		"window.location.replace(url)",
		`"sovereign"`, // the seeded provider slug, embedded as a JS literal
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing %q", want)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestSSOInitHandler_DefaultsSlug(t *testing.T) {
	// Empty slug must default to "sovereign".
	h := SSOInitHandler(SSOInitConfig{Slug: ""})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `"sovereign"`) {
		t.Errorf("empty slug did not default to \"sovereign\"")
	}
}

func TestSSOInitHandler_404sNonRootPath(t *testing.T) {
	// Belt-and-braces: even though the HTTPRoute only sends the bare root
	// here, the handler must NOT serve the landing page for any other path
	// (it would shadow the SPA / API if the route were ever misconfigured).
	h := SSOInitHandler(SSOInitConfig{Slug: "sovereign"})
	for _, p := range []string{"/console", "/api/status", "/oauth/sovereign", "/assets/x.js"} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", p, rec.Code)
		}
	}
}

func TestSSOInitHandler_HeadOK_PostRejected(t *testing.T) {
	h := SSOInitHandler(SSOInitConfig{Slug: "sovereign"})

	recHead := httptest.NewRecorder()
	h(recHead, httptest.NewRequest(http.MethodHead, "/", nil))
	if recHead.Code != http.StatusOK {
		t.Errorf("HEAD / status = %d, want 200", recHead.Code)
	}

	recPost := httptest.NewRecorder()
	h(recPost, httptest.NewRequest(http.MethodPost, "/", nil))
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", recPost.Code)
	}
}

func TestSSOInitHandler_SlugIsJSEscaped(t *testing.T) {
	// A slug with a quote must not break out of the JS string literal.
	h := SSOInitHandler(SSOInitConfig{Slug: `ev"il`})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, `var SLUG = "ev"il"`) {
		t.Errorf("slug was not JS-escaped — XSS/break-out risk: %q", body)
	}
}
