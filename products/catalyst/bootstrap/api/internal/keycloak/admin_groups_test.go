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

func groupTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})
}

func TestListGroups_HappyPath(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/groups":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"id":"g1","name":"acme","path":"/acme","attributes":{"org":["acme"],"tier":["admin"]}},
				{"id":"g2","name":"bank","path":"/bank","attributes":{"org":["bank"]}}
			]`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got))
	}
	if got[0].Attributes["tier"][0] != "admin" {
		t.Fatalf("tier attribute lost: %+v", got[0])
	}
}

func TestGetGroup_NotFound(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/groups/missing":
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, err := client.GetGroup(context.Background(), "missing")
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("expected ErrGroupNotFound, got %v", err)
	}
}

func TestFindGroupByPath_Found(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/group-by-path/acme":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"g1","name":"acme","path":"/acme"}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.FindGroupByPath(context.Background(), "/acme")
	if err != nil {
		t.Fatalf("FindGroupByPath: %v", err)
	}
	if got.ID != "g1" {
		t.Fatalf("expected g1, got %q", got.ID)
	}
}

func TestFindGroupByPath_LeadingSlashOptional(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/group-by-path/acme":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"g1","name":"acme","path":"/acme"}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	// Caller may pass either "acme" or "/acme" — both must work.
	got, err := client.FindGroupByPath(context.Background(), "acme")
	if err != nil {
		t.Fatalf("FindGroupByPath: %v", err)
	}
	if got.ID != "g1" {
		t.Fatalf("expected g1, got %q", got.ID)
	}
}

func TestFindGroupByPath_404IsEmptyResult(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	got, err := client.FindGroupByPath(context.Background(), "/missing")
	if err != nil {
		t.Fatalf("FindGroupByPath should not error on 404, got %v", err)
	}
	if got.ID != "" {
		t.Fatalf("expected empty group, got %+v", got)
	}
}

func TestCreateGroup_201Location(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/groups":
			w.Header().Set("Location", "http://kc.local/admin/realms/test-realm/groups/new-uuid")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	uuid, err := client.CreateGroup(context.Background(), Group{Name: "acme"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if uuid != "new-uuid" {
		t.Fatalf("expected new-uuid, got %q", uuid)
	}
}

func TestCreateSubGroup_201Location(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/groups/parent-uuid/children":
			w.Header().Set("Location", "http://kc/admin/realms/test-realm/groups/child-uuid")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	uuid, err := client.CreateSubGroup(context.Background(), "parent-uuid", Group{Name: "admins"})
	if err != nil {
		t.Fatalf("CreateSubGroup: %v", err)
	}
	if uuid != "child-uuid" {
		t.Fatalf("expected child-uuid, got %q", uuid)
	}
}

func TestEnsureGroup_FindFirstReturnsExisting(t *testing.T) {
	var posted bool
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/group-by-path/acme":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"existing","name":"acme","path":"/acme","attributes":{"org":["acme"]}}`))
		case r.Method == http.MethodPost:
			posted = true
		}
	})
	got, err := client.EnsureGroup(context.Background(), "/acme", map[string][]string{"org": {"acme"}})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if got.ID != "existing" {
		t.Fatalf("expected existing UUID, got %q", got.ID)
	}
	if posted {
		t.Fatal("EnsureGroup must not POST when path resolves and attrs match")
	}
}

func TestEnsureGroup_DriftTriggersUpdate(t *testing.T) {
	var puts int
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/group-by-path/acme":
			// Existing has tier=viewer; caller wants tier=admin → drift.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"g-existing","name":"acme","path":"/acme","attributes":{"tier":["viewer"]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/admin/realms/test-realm/groups/g-existing":
			puts++
			w.WriteHeader(http.StatusNoContent)
		}
	})
	got, err := client.EnsureGroup(context.Background(), "/acme", map[string][]string{"tier": {"admin"}})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if got.ID != "g-existing" {
		t.Fatalf("expected g-existing, got %q", got.ID)
	}
	if puts != 1 {
		t.Fatalf("expected 1 PUT to update attrs, got %d", puts)
	}
}

func TestEnsureGroup_CreateOnMiss(t *testing.T) {
	var pathCalls int
	var posted bool
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/group-by-path/acme":
			pathCalls++
			if pathCalls == 1 {
				// First lookup: not found → caller branches to create.
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Second lookup (after create): the new row.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"g-new","name":"acme","path":"/acme","attributes":{"tier":["developer"]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/groups":
			posted = true
			w.Header().Set("Location", "http://kc/admin/realms/test-realm/groups/g-new")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.EnsureGroup(context.Background(), "/acme",
		map[string][]string{"tier": {"developer"}})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if got.ID != "g-new" {
		t.Fatalf("expected g-new, got %q", got.ID)
	}
	if !posted {
		t.Fatal("EnsureGroup must POST when find returns 404")
	}
	if pathCalls != 2 {
		t.Fatalf("expected 2 path lookups, got %d", pathCalls)
	}
}

func TestSetGroupAttributes_ReplacesAll(t *testing.T) {
	var putBody string
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/groups/g1":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"g1","name":"acme","attributes":{"old":["value"]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/admin/realms/test-realm/groups/g1":
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			putBody = string(buf[:n])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	err := client.SetGroupAttributes(context.Background(), "g1",
		map[string][]string{"tier": {"admin"}})
	if err != nil {
		t.Fatalf("SetGroupAttributes: %v", err)
	}
	// The PUT body must include the new tier=admin and exclude the old=value
	// because attributes is full-replace per the Keycloak API.
	if !strings.Contains(putBody, `"tier":["admin"]`) {
		t.Fatalf("expected tier=admin in PUT body, got %q", putBody)
	}
	if strings.Contains(putBody, `"old":["value"]`) {
		t.Fatalf("old attribute should have been replaced, got %q", putBody)
	}
}

func TestUpdateGroup_RequiresUUID(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("UpdateGroup must fail before HTTP when ID is empty: %s", r.URL.Path)
	})
	if err := client.UpdateGroup(context.Background(), Group{Name: "no-id"}); err == nil {
		t.Fatal("expected error for empty UUID")
	}
}

func TestDeleteGroup_NotFound(t *testing.T) {
	client := groupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/realms/test-realm/groups/gone":
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := client.DeleteGroup(context.Background(), "gone"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("expected ErrGroupNotFound, got %v", err)
	}
}

func TestAttributesEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string][]string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", map[string][]string{}, map[string][]string{}, true},
		{"identical single", map[string][]string{"k": {"v"}}, map[string][]string{"k": {"v"}}, true},
		{"identical multi", map[string][]string{"k": {"a", "b"}}, map[string][]string{"k": {"a", "b"}}, true},
		{"different value", map[string][]string{"k": {"v1"}}, map[string][]string{"k": {"v2"}}, false},
		{"different length", map[string][]string{"k": {"v"}}, map[string][]string{"k": {"v"}, "k2": {"v2"}}, false},
		{"missing key", map[string][]string{"k": {"v"}}, map[string][]string{"j": {"v"}}, false},
		{"order matters in slice", map[string][]string{"k": {"a", "b"}}, map[string][]string{"k": {"b", "a"}}, false},
	}
	for _, c := range cases {
		if got := attributesEqual(c.a, c.b); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLastSlashIndex(t *testing.T) {
	cases := map[string]int{
		"/acme":      0,
		"/acme/sub":  5,
		"acme":       -1,
		"":           -1,
		"/":          0,
		"a/b/c":      3,
	}
	for in, want := range cases {
		if got := lastSlashIndex(in); got != want {
			t.Errorf("lastSlashIndex(%q) = %d, want %d", in, got, want)
		}
	}
}
