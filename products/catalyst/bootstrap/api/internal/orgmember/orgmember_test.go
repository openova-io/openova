package orgmember

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

func org(name, subdomain string, pairs ...string) *unstructured.Unstructured {
	owners := []any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		owners = append(owners, map[string]any{"email": pairs[i], "role": pairs[i+1]})
	}
	spec := map[string]any{"slug": name, "owners": owners}
	if subdomain != "" {
		spec["tenantPublic"] = map[string]any{"subdomain": subdomain}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1", "kind": "Organization",
		"metadata": map[string]any{"name": name}, "spec": spec,
	}}
}

func members(t *testing.T, objs ...runtime.Object) (*Members, *int) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{organizations: "OrganizationList"}, objs...)
	reads := 0
	m := For(t, func() (dynamic.Interface, error) { reads++; return dyn, nil })
	return m, &reads
}

func TestRoleOf_ReadsSpecOwnersBySlugOrSubdomain(t *testing.T) {
	m, _ := members(t, org("acme", "shop", "Owner@Acme.test", "owner", "v@acme.test", "viewer", "x@acme.test", "root"))
	cases := []struct{ org, email, want string }{
		{"acme", "owner@acme.test", Owner},
		{"shop", "v@acme.test", Viewer}, // the console host label
		{"acme", "x@acme.test", ""},     // unknown role holds no role
		{"acme", "stranger@else.test", ""},
		{"other", "owner@acme.test", ""}, // no such Organization
	}
	for _, c := range cases {
		got, err := m.RoleOf(context.Background(), c.org, c.email)
		if err != nil || got != c.want {
			t.Errorf("RoleOf(%s, %s) = %q, %v; want %q", c.org, c.email, got, err, c.want)
		}
	}
}

func TestRoleOf_CachesForTTLAndNeverCachesErrors(t *testing.T) {
	m, reads := members(t, org("acme", "", "o@acme.test", "owner"))
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if r, _ := m.RoleOf(context.Background(), "acme", "o@acme.test"); r != Owner {
			t.Fatalf("want owner, got %q", r)
		}
	}
	if *reads != 1 {
		t.Fatalf("want one read within TTL, got %d", *reads)
	}
	now = now.Add(TTL + time.Second)
	_, _ = m.RoleOf(context.Background(), "acme", "o@acme.test")
	if *reads != 2 {
		t.Fatalf("want a fresh read after TTL, got %d", *reads)
	}

	broken := For(&struct{ int }{1}, func() (dynamic.Interface, error) { return nil, errors.New("apiserver down") })
	if _, err := broken.RoleOf(context.Background(), "acme", "o@acme.test"); err == nil {
		t.Fatal("an unreadable record must be an error, never a role")
	}
}

func TestRoleFor_SessionKinds(t *testing.T) {
	m, _ := members(t, org("b", "", "bob@b.test", "admin"))
	cases := []struct {
		name string
		c    *auth.Claims
		want string
	}{
		{"verified member", &auth.Claims{Email: "bob@b.test", EmailVerified: true}, Admin},
		{"unverified address", &auth.Claims{Email: "bob@b.test"}, ""},
		{"session of another Organization", &auth.Claims{Email: "bob@b.test", EmailVerified: true, Tier: auth.OrgScopedTier, Org: "a"}, ""},
		{"session of this Organization", &auth.Claims{Email: "bob@b.test", EmailVerified: true, Tier: auth.OrgScopedTier, Org: "b"}, Admin},
		{"agent token of b", &auth.Claims{Typ: "agent", Tier: Developer, Org: "b"}, Developer},
		{"agent token of a", &auth.Claims{Typ: "agent", Tier: Developer, Org: "a"}, ""},
		{"agent token above its tiers", &auth.Claims{Typ: "agent", Tier: Owner, Org: "b"}, ""},
		{"support session of b", &auth.Claims{SupportSession: true, ImpersonatedOrg: "b", Tier: "owner"}, Admin},
		{"support session of a", &auth.Claims{SupportSession: true, ImpersonatedOrg: "a", Tier: "owner"}, ""},
	}
	for _, c := range cases {
		got, err := m.RoleFor(context.Background(), c.c, "b")
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// TestGate_AgentTokenWritesAreRestrictsToDecide: the guard holds an agent
// token to its own Organization and leaves its writes to agenttoken.Restrict
// (a viewer token acting for an admin may install); a person's viewer
// session is refused the same write here.
func TestGate_AgentTokenWritesAreRestrictsToDecide(t *testing.T) {
	m, _ := members(t, org("b", "", "vera@b.test", "viewer"))
	cases := []struct {
		name string
		c    *auth.Claims
		org  string
		want int
	}{
		{"viewer agent token of b", &auth.Claims{Typ: "agent", Tier: Viewer, Org: "b"}, "b", http.StatusOK},
		{"viewer agent token of a on b", &auth.Claims{Typ: "agent", Tier: Viewer, Org: "a"}, "b", http.StatusForbidden},
		{"viewer person of b", &auth.Claims{Email: "vera@b.test", EmailVerified: true, Tier: auth.OrgScopedTier, Org: "b"}, "b", http.StatusForbidden},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/org/applications", nil)
		ok := m.Gate(rec, req, c.c, c.org)
		got := rec.Code
		if ok {
			got = http.StatusOK
		}
		if got != c.want {
			t.Errorf("%s: got %d (ok=%v) want %d body=%s", c.name, got, ok, c.want, rec.Body.String())
		}
	}
}

func TestMayDo(t *testing.T) {
	cases := []struct {
		role, method, path string
		want               bool
	}{
		{Viewer, http.MethodGet, "/api/v1/org/users", true},
		{Viewer, http.MethodPost, "/api/v1/org/users", false},
		{Viewer, http.MethodPost, "/api/v1/auth/logout", true},
		{Developer, http.MethodPost, "/catalyst/v1/apps/instances", true},
		{Developer, http.MethodDelete, "/api/v1/org/applications/shop", true},
		{Developer, http.MethodPost, "/api/v1/org/users", false},
		{Admin, http.MethodPost, "/api/v1/org/users", true},
		{"", http.MethodGet, "/api/v1/whoami", false},
	}
	for _, c := range cases {
		if got := MayDo(c.role, c.method, c.path); got != c.want {
			t.Errorf("MayDo(%q, %s, %s) = %v, want %v", c.role, c.method, c.path, got, c.want)
		}
	}
}
