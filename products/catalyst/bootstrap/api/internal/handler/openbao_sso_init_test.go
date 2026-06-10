// openbao_sso_init_test.go — unit coverage for the #3226 server-side
// zero-click silent-SSO shim that gives OpenBao grafana/harbor parity.
//
// OpenBao's UI is a client-side SPA, so a static ssoInitPath cannot
// auto-redirect — it can only pre-select the OIDC method and still needs
// a click. The shim replicates server-side what grafana / harbor do
// natively: it asks Vault for the OIDC auth_url (the Keycloak authorize
// URL, already carrying kc_idp_hint=catalyst-pin via the realm IDP
// binding) and 302-redirects the browser straight to it.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// ssoInitRouter mounts only the shim route so the tests are isolated.
func ssoInitRouter(h *Handler) *chi.Mux {
	r := chi.NewMux()
	r.Get("/catalyst/v1/apps/{id}/openbao-sso-init", h.HandleOpenBaoSSOInit)
	return r
}

// newSSOInitHandler builds a Handler with the openbao endpoint wired into
// a fake catalog plus the given Vault address. The blueprint mirrors the
// live platform/openbao/blueprint.yaml shape (bao.<fqdn> host, ssoEnabled,
// the ?with=oidc deep-link fallback).
func newSSOInitHandler(t *testing.T, vaultAddr string) *Handler {
	t.Helper()
	h := NewWithPDM(slog.New(slog.NewTextHandler(io_discard{}, nil)), nil)
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		SovereignFQDN: "t01.omani.works",
	})
	h.SetCatalogClient(fakeBlueprintInCatalog("openbao",
		[]map[string]interface{}{
			{
				"name":             "api",
				"hostnameTemplate": "bao.{SovereignFQDN}",
				"tls":              true,
				"ssoEnabled":       true,
				"launchDefault":    true,
				"ssoInitPath":      "/ui/vault/auth?with=oidc",
			},
		}, false, []string{"singleton"}))
	if vaultAddr != "" {
		h.SetOpenBao(openbao.New(vaultAddr, "shim-token"))
	}
	return h
}

// TestOpenBaoSSOInit_RedirectsToVaultAuthURL — the happy path: with a
// reachable Vault that returns an auth_url, the shim 302-redirects the
// browser to that EXACT auth_url (this is what gives zero-click parity).
func TestOpenBaoSSOInit_RedirectsToVaultAuthURL(t *testing.T) {
	const authURL = "https://auth.t01.omani.works/realms/sovereign/protocol/openid-connect/auth" +
		"?client_id=openbao&kc_idp_hint=catalyst-pin&redirect_uri=https%3A%2F%2Fbao.t01.omani.works" +
		"%2Fui%2Fvault%2Fauth%2Foidc%2Foidc%2Fcallback&response_type=code&scope=openid&state=st&nonce=nc"

	var gotReq struct {
		Path   string
		Role   string
		RdrURI string
	}
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq.Path = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotReq.Role, _ = body["role"].(string)
		gotReq.RdrURI, _ = body["redirect_uri"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"auth_url": authURL},
		})
	}))
	defer vault.Close()

	h := newSSOInitHandler(t, vault.URL)
	r := ssoInitRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/openbao/openbao-sso-init", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != authURL {
		t.Fatalf("Location must be the Vault auth_url verbatim:\n got %q\nwant %q", loc, authURL)
	}
	// The shim must hit the Vault OIDC auth_url API with the Vault UI
	// callback as redirect_uri (Vault appends /oidc/oidc/callback).
	if gotReq.Path != "/v1/auth/oidc/oidc/auth_url" {
		t.Errorf("wrong Vault path; got %q", gotReq.Path)
	}
	if gotReq.Role != "operator" {
		t.Errorf("expected role=operator (the bootstrap-kit OIDC role); got %q", gotReq.Role)
	}
	if !strings.HasPrefix(gotReq.RdrURI, "https://bao.t01.omani.works/ui/vault/auth/oidc/oidc/callback") {
		t.Errorf("redirect_uri must target the Vault UI callback; got %q", gotReq.RdrURI)
	}
}

// TestOpenBaoSSOInit_FallsBackToDeepLinkOnVaultError — when the auth_url
// POST fails (Vault sealed / oidc method not mounted / 500), the shim
// must NOT 500 the Open button. It 302-redirects to the app's deep-link
// (ssoInitPath) so the operator at least lands on the pre-selected OIDC
// method instead of an error page.
func TestOpenBaoSSOInit_FallsBackToDeepLinkOnVaultError(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":["vault is sealed"]}`))
	}))
	defer vault.Close()

	h := newSSOInitHandler(t, vault.URL)
	r := ssoInitRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/openbao/openbao-sso-init", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 fallback (never 500), got %d (body=%s)", rec.Code, rec.Body.String())
	}
	want := "https://bao.t01.omani.works/ui/vault/auth?with=oidc"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Fatalf("fallback Location must be the deep-link:\n got %q\nwant %q", loc, want)
	}
}

// TestOpenBaoSSOInit_FallsBackWhenOpenBaoUnwired — Catalyst-Zero (or any
// catalyst-api without CATALYST_OPENBAO_ADDR) has no Vault client. The
// shim must still 302 to the deep-link, never 500.
func TestOpenBaoSSOInit_FallsBackWhenOpenBaoUnwired(t *testing.T) {
	h := newSSOInitHandler(t, "") // no openbao client wired
	r := ssoInitRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/openbao/openbao-sso-init", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 fallback when openbao unwired, got %d", rec.Code)
	}
	want := "https://bao.t01.omani.works/ui/vault/auth?with=oidc"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Fatalf("fallback Location must be the deep-link; got %q", loc)
	}
}

// TestOpenBaoSSOInit_UnknownAppIs404 — addressing an id with no resolvable
// SSO-enabled endpoint must 404, not silently redirect somewhere bogus.
func TestOpenBaoSSOInit_UnknownAppIs404(t *testing.T) {
	h := NewWithPDM(slog.New(slog.NewTextHandler(io_discard{}, nil)), nil)
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{SovereignFQDN: "t01.omani.works"})
	h.SetCatalogClient(newFakeCatalog()) // empty catalog
	r := ssoInitRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/nope/openbao-sso-init", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestComputeVaultUICallback — the redirect_uri the shim sends to Vault is
// the Vault-UI OIDC callback. Vault's web UI uses
// https://<host>/ui/vault/auth/<mount>/oidc/callback. We mirror that here
// so the round-trip lands back in the UI.
func TestComputeVaultUICallback(t *testing.T) {
	got := computeVaultUICallback("https", "bao.t01.omani.works", "oidc")
	want := "https://bao.t01.omani.works/ui/vault/auth/oidc/oidc/callback"
	if got != want {
		t.Fatalf("callback mismatch:\n got %q\nwant %q", got, want)
	}
}

var _ = context.Background
