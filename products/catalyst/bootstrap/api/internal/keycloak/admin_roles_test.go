package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// roleTestClient mirrors adminTestClient (in admin_test.go) but is
// duplicated here to keep the role test surface independent — if a
// future refactor moves admin.go elsewhere, this file still builds.
func roleTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})
}

func TestListRealmRoles_HappyPath(t *testing.T) {
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case "/admin/realms/test-realm/roles":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"id":"r1","name":"catalyst-viewer","containerId":"realm"},
				{"id":"r2","name":"catalyst-developer","containerId":"realm","composite":true},
				{"id":"r3","name":"catalyst-admin","containerId":"realm","composite":true}
			]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.ListRealmRoles(context.Background())
	if err != nil {
		t.Fatalf("ListRealmRoles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 roles, got %d", len(got))
	}
	if got[1].Name != "catalyst-developer" || !got[1].Composite {
		t.Fatalf("composite flag missing: %+v", got[1])
	}
}

func TestGetRealmRole_NotFound(t *testing.T) {
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.URL.Path == "/admin/realms/test-realm/roles/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected: %s", r.URL.Path)
		}
	})
	_, err := client.GetRealmRole(context.Background(), "missing")
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("expected ErrRoleNotFound, got %v", err)
	}
}

func TestCreateRealmRole_201Created(t *testing.T) {
	var receivedName string
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/roles":
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			if idx := strings.Index(string(buf[:n]), `"name":"`); idx >= 0 {
				start := idx + len(`"name":"`)
				end := strings.Index(string(buf[start:n]), `"`)
				if end > 0 {
					receivedName = string(buf[start : start+end])
				}
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	err := client.CreateRealmRole(context.Background(), RealmRole{Name: "catalyst-developer"})
	if err != nil {
		t.Fatalf("CreateRealmRole: %v", err)
	}
	if receivedName != "catalyst-developer" {
		t.Fatalf("expected name catalyst-developer in body, got %q", receivedName)
	}
}

func TestCreateRealmRole_409Conflict(t *testing.T) {
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/roles":
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	err := client.CreateRealmRole(context.Background(), RealmRole{Name: "catalyst-developer"})
	if !errors.Is(err, errRoleAlreadyExists) {
		t.Fatalf("expected errRoleAlreadyExists, got %v", err)
	}
}

func TestEnsureRealmRole_FindReturnsExisting(t *testing.T) {
	var posted bool
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/roles/catalyst-admin":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"existing","name":"catalyst-admin","composite":true}`))
		case r.Method == http.MethodPost:
			posted = true
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	got, err := client.EnsureRealmRole(context.Background(), RealmRole{Name: "catalyst-admin"})
	if err != nil {
		t.Fatalf("EnsureRealmRole: %v", err)
	}
	if got.ID != "existing" || !got.Composite {
		t.Fatalf("expected existing/composite, got %+v", got)
	}
	if posted {
		t.Fatal("EnsureRealmRole must not POST when GET succeeds")
	}
}

func TestEnsureRealmRole_CreateOn404(t *testing.T) {
	var getCalls, postCalls int
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/roles/catalyst-admin":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Second GET (after CREATE) returns the row.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"new","name":"catalyst-admin"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/roles":
			postCalls++
			w.WriteHeader(http.StatusCreated)
		}
	})
	got, err := client.EnsureRealmRole(context.Background(), RealmRole{Name: "catalyst-admin"})
	if err != nil {
		t.Fatalf("EnsureRealmRole: %v", err)
	}
	if got.ID != "new" {
		t.Fatalf("expected new ID, got %q", got.ID)
	}
	if getCalls != 2 || postCalls != 1 {
		t.Fatalf("expected 2 GETs + 1 POST, got %d GETs + %d POSTs", getCalls, postCalls)
	}
}

func TestUpdateRealmRole_RequiresName(t *testing.T) {
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("UpdateRealmRole must fail before HTTP when name is empty: %s", r.URL.Path)
	})
	if err := client.UpdateRealmRole(context.Background(), "", RealmRole{}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestDeleteRealmRole_NotFound(t *testing.T) {
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/realms/test-realm/roles/gone":
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := client.DeleteRealmRole(context.Background(), "gone"); !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("expected ErrRoleNotFound, got %v", err)
	}
}

func TestAssignGroupRealmRoles_PostBody(t *testing.T) {
	var bodyLen int
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/groups/g1/role-mappings/realm":
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			bodyLen = n
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	err := client.AssignGroupRealmRoles(context.Background(), "g1", []RealmRole{
		{ID: "r1", Name: "catalyst-developer"},
	})
	if err != nil {
		t.Fatalf("AssignGroupRealmRoles: %v", err)
	}
	if bodyLen == 0 {
		t.Fatal("expected non-empty POST body")
	}
}

func TestAssignGroupRealmRoles_EmptyIsNoOp(t *testing.T) {
	// Empty role list must NOT make any HTTP call.
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty role list must not make any HTTP call; got %s", r.URL.Path)
	})
	if err := client.AssignGroupRealmRoles(context.Background(), "g1", nil); err != nil {
		t.Fatalf("expected nil for empty roles, got %v", err)
	}
}

func TestListUserEffectiveRealmRoles_HitsCompositeEndpoint(t *testing.T) {
	var sawComposite bool
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/users/u1/role-mappings/realm/composite":
			sawComposite = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"r1","name":"catalyst-viewer"},{"id":"r2","name":"catalyst-developer"}]`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.ListUserEffectiveRealmRoles(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListUserEffectiveRealmRoles: %v", err)
	}
	if !sawComposite {
		t.Fatal("expected /composite suffix on URL")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 effective roles, got %d", len(got))
	}
}

func TestListUserRealmRoles_DirectEndpoint(t *testing.T) {
	// Without /composite — should hit the direct endpoint only.
	var sawDirect bool
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/users/u1/role-mappings/realm":
			sawDirect = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"r1","name":"catalyst-viewer"}]`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.ListUserRealmRoles(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListUserRealmRoles: %v", err)
	}
	if !sawDirect {
		t.Fatal("expected direct endpoint")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 direct role, got %d", len(got))
	}
}
