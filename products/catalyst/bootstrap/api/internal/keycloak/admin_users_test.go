package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// usersTestClient mirrors the helper in admin_groups_test.go: a
// httptest.Server-backed Keycloak that returns the SA token on
// /protocol/openid-connect/token and delegates everything else to the
// per-test handler.
func usersTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"sa-token"}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-realm", "test-client", "test-secret")
}

func TestSearchUsers_HappyPath(t *testing.T) {
	c := usersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/realms/test-realm/users") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "ali" {
			t.Errorf("search query: got %q want ali", got)
		}
		if got := r.URL.Query().Get("max"); got != "10" {
			t.Errorf("max: got %q want 10", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"u1","username":"alice","email":"alice@acme.com","firstName":"Alice","lastName":"A"},
			{"id":"u2","username":"bob.fed","federationLink":"azure-sso-acme"}
		]`))
	})
	users, err := c.SearchUsers(context.Background(), "ali", 10)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("users: got %d want 2", len(users))
	}
	if users[0].Username != "alice" {
		t.Errorf("user[0] username: got %q want alice", users[0].Username)
	}
	if users[1].FederationLink != "azure-sso-acme" {
		t.Errorf("user[1] federationLink: got %q want azure-sso-acme", users[1].FederationLink)
	}
}

func TestSearchUsers_DefaultLimit(t *testing.T) {
	var seenMax string
	c := usersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seenMax = r.URL.Query().Get("max")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.SearchUsers(context.Background(), "x", 0); err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if seenMax != "20" {
		t.Errorf("default max: got %q want 20", seenMax)
	}
}

func TestListRealmRoleMembers_HappyPath(t *testing.T) {
	c := usersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/roles/catalyst-admin/users") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"u1","username":"alice"}]`))
	})
	users, err := c.ListRealmRoleMembers(context.Background(), "catalyst-admin")
	if err != nil {
		t.Fatalf("ListRealmRoleMembers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users: got %d want 1", len(users))
	}
}

func TestListRealmRoleMembers_404(t *testing.T) {
	c := usersTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.ListRealmRoleMembers(context.Background(), "missing")
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("err: got %v want ErrRoleNotFound", err)
	}
}

func TestListClientRoles_HappyPath(t *testing.T) {
	c := usersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/clients/client-uuid/roles") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"cr1","name":"editor","clientRole":true}]`))
	})
	roles, err := c.ListClientRoles(context.Background(), "client-uuid")
	if err != nil {
		t.Fatalf("ListClientRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("roles: got %d want 1", len(roles))
	}
	if !roles[0].ClientRole {
		t.Errorf("expected ClientRole=true; got %+v", roles[0])
	}
}
