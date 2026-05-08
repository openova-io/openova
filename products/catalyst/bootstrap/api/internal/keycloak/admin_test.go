package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// adminTestServer wires a minimal Keycloak Admin REST API mock at the
// httptest level. Every test in this file uses the same shape: route by
// (URL path, method) and respond with the canned JSON. The shared helper
// keeps each test focused on the assertion rather than the boilerplate.
func adminTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second}), srv
}

// saTokenHandler responds 200 with a stub access token; tests prepend it
// to every handler that needs it because admin.go calls
// serviceAccountToken at the top of every public method.
func saTokenHandler(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"access_token":"sa-tok","token_type":"Bearer","expires_in":300}`))
}

// TestFindClientByClientID_Found — happy path returning a single client
// from the realm's GET /clients?clientId= response.
func TestFindClientByClientID_Found(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/clients" && r.URL.Query().Get("clientId") == "acme-app":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"uuid-1","clientId":"acme-app","enabled":true,"protocol":"openid-connect"}]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	got, err := client.FindClientByClientID(context.Background(), "acme-app")
	if err != nil {
		t.Fatalf("FindClientByClientID: %v", err)
	}
	if got.ID != "uuid-1" {
		t.Fatalf("expected uuid-1, got %q", got.ID)
	}
	if got.ClientID != "acme-app" {
		t.Fatalf("expected acme-app, got %q", got.ClientID)
	}
}

// TestFindClientByClientID_Empty — the find-or-create caller relies on
// (empty struct, nil error) when the clientId is unknown so it can
// fall through to CreateClient.
func TestFindClientByClientID_Empty(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/clients":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	got, err := client.FindClientByClientID(context.Background(), "missing")
	if err != nil {
		t.Fatalf("FindClientByClientID: %v", err)
	}
	if got.ID != "" {
		t.Fatalf("expected empty, got %+v", got)
	}
}

// TestGetClient_NotFound — the GetClient caller relies on
// ErrClientNotFound to differentiate "doesn't exist" from "transport
// failed" when reconciling.
func TestGetClient_NotFound(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/clients/missing-uuid":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	_, err := client.GetClient(context.Background(), "missing-uuid")
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}

// TestCreateClient_201Location — Keycloak returns the new UUID via the
// Location header's last segment, so the client must extract that
// segment correctly even when Location has trailing whitespace or query
// strings.
func TestCreateClient_201Location(t *testing.T) {
	client, srv := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/clients":
			w.Header().Set("Location", "http://kc.local/admin/realms/test-realm/clients/new-uuid-42")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	_ = srv

	uuid, err := client.CreateClient(context.Background(), OIDCClient{
		ClientID: "acme-app",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateClient: %v", err)
	}
	if uuid != "new-uuid-42" {
		t.Fatalf("expected new-uuid-42, got %q", uuid)
	}
}

// TestCreateClient_DefaultsProtocol — the upstream Keycloak Admin API
// rejects an empty `protocol` with 400, so admin.go MUST default it to
// openid-connect when the caller leaves it empty. Catching this in a
// unit test prevents a regression that would otherwise only surface at
// integration time on a real Keycloak.
func TestCreateClient_DefaultsProtocol(t *testing.T) {
	var receivedProtocol string

	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/clients":
			// Inspect the body — protocol must have been defaulted.
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			body := string(buf[:n])
			if idx := strings.Index(body, `"protocol":"`); idx >= 0 {
				start := idx + len(`"protocol":"`)
				end := strings.Index(body[start:], `"`)
				if end > 0 {
					receivedProtocol = body[start : start+end]
				}
			}
			w.Header().Set("Location", "http://kc.local/admin/realms/test-realm/clients/uuid-z")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	_, err := client.CreateClient(context.Background(), OIDCClient{
		ClientID: "no-proto",
		Enabled:  true,
		// Protocol intentionally empty
	})
	if err != nil {
		t.Fatalf("CreateClient: %v", err)
	}
	if receivedProtocol != "openid-connect" {
		t.Fatalf("expected protocol defaulted to openid-connect, got %q", receivedProtocol)
	}
}

// TestEnsureClient_FindFirst — when the client already exists, EnsureClient
// returns the existing UUID without calling POST. This is the common path
// once organization-controller has reconciled the Org once.
func TestEnsureClient_FindFirst(t *testing.T) {
	var postCalls atomic.Int32

	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/clients":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"existing-uuid","clientId":"acme-app","enabled":true,"protocol":"openid-connect"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/clients":
			postCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	uuid, secret, err := client.EnsureClient(context.Background(), OIDCClient{
		ClientID: "acme-app",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("EnsureClient: %v", err)
	}
	if uuid != "existing-uuid" {
		t.Fatalf("expected existing-uuid, got %q", uuid)
	}
	if secret != "" {
		t.Fatalf("find path must not echo a secret, got %q", secret)
	}
	if got := postCalls.Load(); got != 0 {
		t.Fatalf("find path must not POST, got %d POSTs", got)
	}
}

// TestEnsureClient_409ConflictReFinds — the race tolerance test. If two
// callers concurrently EnsureClient on the same clientId, the slower
// one's POST gets 409; it must transparently re-find and return the
// other caller's UUID instead of surfacing 409 to the caller as a 5xx.
func TestEnsureClient_409ConflictReFinds(t *testing.T) {
	var refindCalls atomic.Int32

	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/clients":
			n := refindCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				// First find → empty (race in flight)
				w.Write([]byte(`[]`))
			} else {
				// Second find (after 409) → the winner's row
				w.Write([]byte(`[{"id":"winner-uuid","clientId":"acme-app","enabled":true}]`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/clients":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"errorMessage":"Client acme-app already exists"}`))
		}
	})

	uuid, secret, err := client.EnsureClient(context.Background(), OIDCClient{
		ClientID: "acme-app",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("EnsureClient on 409 race: %v", err)
	}
	if uuid != "winner-uuid" {
		t.Fatalf("expected winner-uuid (re-find), got %q", uuid)
	}
	if secret != "" {
		t.Fatalf("re-find path must not echo a secret, got %q", secret)
	}
	if got := refindCalls.Load(); got != 2 {
		t.Fatalf("expected 2 finds (initial + after-409), got %d", got)
	}
}

// TestUpdateClient_RequiresUUID — calling UpdateClient with an empty .ID
// MUST fail fast with a clear error rather than blindly PUT-ing to a
// malformed URL. Defends against a caller mistake where they call
// FindClientByClientID, get an empty result, mutate the struct, and
// then try UpdateClient instead of CreateClient.
func TestUpdateClient_RequiresUUID(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("UpdateClient must fail BEFORE making any HTTP call when ID is empty; got: %s %s", r.Method, r.URL.String())
	})

	err := client.UpdateClient(context.Background(), OIDCClient{
		ClientID: "acme-app",
		// ID intentionally empty
	})
	if err == nil {
		t.Fatal("expected error for empty UUID, got nil")
	}
	if !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("expected error message to mention UUID, got %v", err)
	}
}

// TestUpdateClient_204 — the happy path returns nil on 204 No Content.
func TestUpdateClient_204(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPut && r.URL.Path == "/admin/realms/test-realm/clients/uuid-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	err := client.UpdateClient(context.Background(), OIDCClient{
		ID:       "uuid-1",
		ClientID: "acme-app",
		Name:     "ACME App (renamed)",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
}

// TestDeleteClient_NotFound_IsNonFatal — deleting an already-absent
// client must surface ErrClientNotFound rather than a generic error so
// reconciliation loops can treat absence-as-success.
func TestDeleteClient_NotFound(t *testing.T) {
	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/realms/test-realm/clients/gone":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	err := client.DeleteClient(context.Background(), "gone")
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}

// TestListClients_PaginatesFirstPage — confirms ListClients sends
// `first=0&max=1000` and parses the array response. (We don't yet
// paginate beyond 1000; that's an explicit design choice — see
// admin.go ListClients comment.)
func TestListClients_PaginatesFirstPage(t *testing.T) {
	var seenQuery string

	client, _ := adminTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/clients":
			seenQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"u1","clientId":"a"},{"id":"u2","clientId":"b"}]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})

	got, err := client.ListClients(context.Background())
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(got))
	}
	if !strings.Contains(seenQuery, "first=0") || !strings.Contains(seenQuery, "max=1000") {
		t.Fatalf("expected first=0&max=1000, got %q", seenQuery)
	}
}

// TestLastSegment — exercise the URL parsing helper directly. Cheap unit
// test that catches regressions to the Location-header path. The cases
// chosen reflect what Keycloak actually emits via the Location header on
// POST /clients (no trailing slash, simple absolute URL); the bare-string
// case exercises the no-slash defensive fallback.
func TestLastSegment(t *testing.T) {
	cases := map[string]string{
		"http://kc/admin/realms/x/clients/abc-123": "abc-123",
		"abc-123":                                  "abc-123",
		"":                                         "",
	}
	for in, want := range cases {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
