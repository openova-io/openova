package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// idpTestClient mirrors adminTestClient (admin_test.go) so each IdP
// test stays focused on the assertion rather than the HTTP boilerplate.
func idpTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})
}

// idpFixture returns a typical Azure-SSO-flavoured IdentityProvider
// representation — used as the "desired state" across happy-path /
// idempotency / drift tests.
func idpFixture() IdentityProvider {
	return IdentityProvider{
		Alias:                     "azure-sso-acme",
		DisplayName:               "ACME Azure AD",
		ProviderID:                "oidc",
		Enabled:                   true,
		FirstBrokerLoginFlowAlias: "first broker login",
		Config: map[string]string{
			"clientId":         "00000000-0000-0000-0000-aaaaaaaaaaaa",
			"clientSecret":     "kept-in-memory-only",
			"authorizationUrl": "https://login.microsoftonline.com/T/oauth2/v2.0/authorize",
			"tokenUrl":         "https://login.microsoftonline.com/T/oauth2/v2.0/token",
			"jwksUrl":          "https://login.microsoftonline.com/T/discovery/v2.0/keys",
			"defaultScope":     "openid profile email",
			"validateSignature": "true",
			"useJwksUrl":       "true",
		},
	}
}

// mapperFixtureOIDtoExternalID returns the canonical Azure-AD
// `oid` → `openova.io/external-id` claim mapper.
func mapperFixtureOIDtoExternalID() IdentityProviderMapper {
	return IdentityProviderMapper{
		Name:                   "oid-to-external-id",
		IdentityProviderAlias:  "azure-sso-acme",
		IdentityProviderMapper: "oidc-user-attribute-idp-mapper",
		Config: map[string]string{
			"claim":          "oid",
			"user.attribute": "openova.io/external-id",
			"syncMode":       "INHERIT",
		},
	}
}

// TestEnsureIdentityProvider_CleanRealm_Creates verifies the slice F1
// creation path: GET 404 → POST. No drift, no extra writes.
func TestEnsureIdentityProvider_CleanRealm_Creates(t *testing.T) {
	var posts, gets atomic.Int32
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme":
			if r.Method == http.MethodGet {
				gets.Add(1)
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			t.Errorf("unexpected method on alias path: %s", r.Method)
		case "/admin/realms/test-realm/identity-provider/instances":
			if r.Method == http.MethodPost {
				posts.Add(1)
				body, _ := io.ReadAll(r.Body)
				var got IdentityProvider
				if err := json.Unmarshal(body, &got); err != nil {
					t.Errorf("decode body: %v", err)
				}
				if got.Alias != "azure-sso-acme" {
					t.Errorf("alias mismatch: %q", got.Alias)
				}
				if got.Config["clientSecret"] != "kept-in-memory-only" {
					t.Errorf("clientSecret missing on POST: %q", got.Config["clientSecret"])
				}
				w.WriteHeader(http.StatusCreated)
				return
			}
			t.Errorf("unexpected method on instances path: %s", r.Method)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProvider(context.Background(), idpFixture()); err != nil {
		t.Fatalf("EnsureIdentityProvider: %v", err)
	}
	if gets.Load() != 1 {
		t.Errorf("expected 1 GET, got %d", gets.Load())
	}
	if posts.Load() != 1 {
		t.Errorf("expected 1 POST, got %d", posts.Load())
	}
}

// TestEnsureIdentityProvider_SteadyState_NoWrites — re-running on a
// populated realm with byte-equal desired state produces 0 writes.
// The idempotency anchor for the F1 contract.
func TestEnsureIdentityProvider_SteadyState_NoWrites(t *testing.T) {
	var posts, puts atomic.Int32
	desired := idpFixture()
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme":
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(desired)
			case http.MethodPut:
				puts.Add(1)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method: %s", r.Method)
			}
		case "/admin/realms/test-realm/identity-provider/instances":
			if r.Method == http.MethodPost {
				posts.Add(1)
				w.WriteHeader(http.StatusCreated)
				return
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProvider(context.Background(), desired); err != nil {
		t.Fatalf("EnsureIdentityProvider: %v", err)
	}
	if posts.Load() != 0 {
		t.Errorf("steady state expected 0 POSTs, got %d", posts.Load())
	}
	if puts.Load() != 0 {
		t.Errorf("steady state expected 0 PUTs, got %d", puts.Load())
	}
}

// TestEnsureIdentityProvider_DriftedConfig_PUTOnce verifies that a
// drifted Config map (e.g. operator rotated the client secret in spec)
// causes EXACTLY ONE PUT. No POST.
func TestEnsureIdentityProvider_DriftedConfig_PUTOnce(t *testing.T) {
	var posts, puts atomic.Int32
	desired := idpFixture()
	server := idpFixture()
	server.Config = map[string]string{
		// drifted: clientId equal, clientSecret rotated, authorizationUrl missing
		"clientId":     desired.Config["clientId"],
		"clientSecret": "old-secret",
	}

	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme":
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(server)
			case http.MethodPut:
				puts.Add(1)
				body, _ := io.ReadAll(r.Body)
				var got IdentityProvider
				if err := json.Unmarshal(body, &got); err != nil {
					t.Errorf("decode body: %v", err)
				}
				if got.Config["clientSecret"] != "kept-in-memory-only" {
					t.Errorf("PUT did not carry the new clientSecret: %q", got.Config["clientSecret"])
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method: %s", r.Method)
			}
		case "/admin/realms/test-realm/identity-provider/instances":
			if r.Method == http.MethodPost {
				posts.Add(1)
				w.WriteHeader(http.StatusCreated)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProvider(context.Background(), desired); err != nil {
		t.Fatalf("EnsureIdentityProvider: %v", err)
	}
	if posts.Load() != 0 {
		t.Errorf("drift expected 0 POSTs, got %d", posts.Load())
	}
	if puts.Load() != 1 {
		t.Errorf("drift expected 1 PUT, got %d", puts.Load())
	}
}

// TestEnsureIdentityProvider_409Race_RefindsAndUpdates — 409 on POST
// means a sibling caller created the alias between our GET and POST.
// The Ensure must re-GET and reconcile from there (re-find then
// no-op-or-PUT depending on representation match).
func TestEnsureIdentityProvider_409Race_RefindsAndUpdates(t *testing.T) {
	var stage atomic.Int32 // 0 = first GET (404), 1 = race-create-on-server, 2 = re-GET (200)
	var puts atomic.Int32
	desired := idpFixture()

	// Sibling caller created an exactly-equal IdP (no drift) — we
	// expect 0 PUTs after re-GET.
	siblingState := desired
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme":
			switch r.Method {
			case http.MethodGet:
				if stage.Load() < 2 {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(siblingState)
			case http.MethodPut:
				puts.Add(1)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method: %s", r.Method)
			}
		case "/admin/realms/test-realm/identity-provider/instances":
			if r.Method == http.MethodPost {
				// Simulate the race: between our GET and POST the sibling
				// caller created the alias. Server responds 409.
				stage.Store(2)
				http.Error(w, "exists", http.StatusConflict)
				return
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProvider(context.Background(), desired); err != nil {
		t.Fatalf("EnsureIdentityProvider: %v", err)
	}
	if puts.Load() != 0 {
		t.Errorf("409 race with equal sibling state should not PUT, got %d PUTs", puts.Load())
	}
}

// TestDeleteIdentityProvider_NotFound_Sentinel verifies absent-as-error
// surfaces ErrIdentityProviderNotFound (which the F2 finalizer treats
// as best-effort success).
func TestDeleteIdentityProvider_NotFound_Sentinel(t *testing.T) {
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/missing":
			if r.Method == http.MethodDelete {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	err := client.DeleteIdentityProvider(context.Background(), "missing")
	if !errors.Is(err, ErrIdentityProviderNotFound) {
		t.Fatalf("expected ErrIdentityProviderNotFound, got %v", err)
	}
}

// TestDeleteIdentityProvider_Found_NoContent — happy path.
func TestDeleteIdentityProvider_Found_NoContent(t *testing.T) {
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.DeleteIdentityProvider(context.Background(), "azure-sso-acme"); err != nil {
		t.Fatalf("DeleteIdentityProvider: %v", err)
	}
}

// TestListIdentityProviders_HappyPath verifies the access-matrix UI
// path.
func TestListIdentityProviders_HappyPath(t *testing.T) {
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/identity-provider/instances":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"alias":"azure-sso-acme","providerId":"oidc","enabled":true,"config":{"clientId":"a"}},
				{"alias":"okta-beta","providerId":"oidc","enabled":false,"config":{"clientId":"b"}}
			]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	got, err := client.ListIdentityProviders(context.Background())
	if err != nil {
		t.Fatalf("ListIdentityProviders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 IdPs, got %d", len(got))
	}
	if got[0].Alias != "azure-sso-acme" || got[1].Alias != "okta-beta" {
		t.Errorf("aliases mismatch: %+v", got)
	}
}

// TestEnsureIdentityProviderMapper_NotPresent_POSTs — the mapper-create
// path: list returns empty, expect POST.
func TestEnsureIdentityProviderMapper_NotPresent_POSTs(t *testing.T) {
	var posts, puts atomic.Int32
	mapper := mapperFixtureOIDtoExternalID()

	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers":
			posts.Add(1)
			body, _ := io.ReadAll(r.Body)
			var got IdentityProviderMapper
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if got.Name != "oid-to-external-id" {
				t.Errorf("mapper name mismatch: %q", got.Name)
			}
			if got.Config["claim"] != "oid" {
				t.Errorf("mapper config.claim mismatch: %q", got.Config["claim"])
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers/"):
			puts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProviderMapper(context.Background(), "azure-sso-acme", mapper); err != nil {
		t.Fatalf("EnsureIdentityProviderMapper: %v", err)
	}
	if posts.Load() != 1 {
		t.Errorf("expected 1 POST, got %d", posts.Load())
	}
	if puts.Load() != 0 {
		t.Errorf("expected 0 PUTs, got %d", puts.Load())
	}
}

// TestEnsureIdentityProviderMapper_PresentEqual_NoOp — mapper exists
// with byte-equal Config. Re-Ensure must produce 0 writes.
func TestEnsureIdentityProviderMapper_PresentEqual_NoOp(t *testing.T) {
	var posts, puts atomic.Int32
	mapper := mapperFixtureOIDtoExternalID()
	server := mapper
	server.ID = "mapper-uuid-abc"

	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]IdentityProviderMapper{server})
		case r.Method == http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut:
			puts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProviderMapper(context.Background(), "azure-sso-acme", mapper); err != nil {
		t.Fatalf("EnsureIdentityProviderMapper: %v", err)
	}
	if posts.Load() != 0 || puts.Load() != 0 {
		t.Errorf("equal mapper should be no-op, got POSTs=%d PUTs=%d", posts.Load(), puts.Load())
	}
}

// TestEnsureIdentityProviderMapper_PresentDrift_PUTOnce — mapper exists
// with different Config; expect 1 PUT, 0 POST.
func TestEnsureIdentityProviderMapper_PresentDrift_PUTOnce(t *testing.T) {
	var posts, puts atomic.Int32
	desired := mapperFixtureOIDtoExternalID()
	server := mapperFixtureOIDtoExternalID()
	server.ID = "mapper-uuid-abc"
	server.Config = map[string]string{
		"claim":          "oid",
		"user.attribute": "openova.io/external-id",
		"syncMode":       "FORCE", // drifted from desired's INHERIT
	}

	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]IdentityProviderMapper{server})
		case r.Method == http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/admin/realms/test-realm/identity-provider/instances/azure-sso-acme/mappers/mapper-uuid-abc":
			puts.Add(1)
			body, _ := io.ReadAll(r.Body)
			var got IdentityProviderMapper
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if got.Config["syncMode"] != "INHERIT" {
				t.Errorf("PUT did not carry desired syncMode: %q", got.Config["syncMode"])
			}
			if got.ID != "mapper-uuid-abc" {
				t.Errorf("PUT body must carry the existing ID, got %q", got.ID)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.EnsureIdentityProviderMapper(context.Background(), "azure-sso-acme", desired); err != nil {
		t.Fatalf("EnsureIdentityProviderMapper: %v", err)
	}
	if posts.Load() != 0 {
		t.Errorf("drift expected 0 POSTs, got %d", posts.Load())
	}
	if puts.Load() != 1 {
		t.Errorf("drift expected 1 PUT, got %d", puts.Load())
	}
}

// TestEnsureIdentityProviderMapper_AliasMismatch_Errors — caller-set
// IdentityProviderAlias must match the URL alias. Defensive guard.
func TestEnsureIdentityProviderMapper_AliasMismatch_Errors(t *testing.T) {
	client := idpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s (no HTTP should fire on guard error)", r.Method, r.URL.Path)
	})
	mapper := mapperFixtureOIDtoExternalID()
	mapper.IdentityProviderAlias = "wrong-alias"
	err := client.EnsureIdentityProviderMapper(context.Background(), "azure-sso-acme", mapper)
	if err == nil || !strings.Contains(err.Error(), "wrong-alias") {
		t.Fatalf("expected alias-mismatch error, got %v", err)
	}
}
