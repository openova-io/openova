// org_membership_test.go — an Organization's console admits only that
// Organization's members, with the role they hold (org_membership.go).
//
// Every refusal below passed on main: the sign-in doors minted org-admin for
// the host's Organization without reading its membership, the guard admitted
// any session on an Organization host, and X-Tenant-Host bound only org-admin
// sessions. Each case names the person (a member of A, a stranger, a viewer of
// B) and asserts both the status and that the protected handler never ran.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// withOwners sets spec.owners of obj from email, role pairs.
func withOwners(obj *unstructured.Unstructured, pairs ...string) *unstructured.Unstructured {
	owners := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		owners = append(owners, map[string]any{"email": pairs[i], "role": pairs[i+1]})
	}
	if err := unstructured.SetNestedSlice(obj.Object, owners, "spec", "owners"); err != nil {
		panic(err)
	}
	return obj
}

// twoOrgsHandler serves two Organizations on one Sovereign: alice owns A; bob
// owns B, dev is B's developer and vera B's viewer. The Sovereign's own
// console is console.omantel.biz.
func twoOrgsHandler(t *testing.T) *Handler {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, r := range []store.TenantRegistration{
		{Host: "console.a.omani.homes", TenantID: "tnt-a", TenantKind: store.TenantKindOrg},
		{Host: "console.b.omani.homes", TenantID: "tnt-b", TenantKind: store.TenantKindOrg},
	} {
		if err := reg.Put(r); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	h := &Handler{log: quietLog(), tenantRegistry: reg}
	withOrgRecords(h,
		withOwners(orgCR("a", "omani.homes", "", "tnt-a"), "alice@a.example", "owner"),
		withOwners(orgCR("b", "omani.homes", "", "tnt-b"),
			"bob@b.example", "owner", "dev@b.example", "developer", "vera@b.example", "viewer"),
	)
	return h
}

func claimsReq(method, path, fwdHost string, c *auth.Claims) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if fwdHost != "" {
		req.Header.Set("X-Forwarded-Host", fwdHost)
	}
	return req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, c))
}

func TestOrgScopeGuard_AdmitsOnlyMembersWithTheirRole(t *testing.T) {
	h := twoOrgsHandler(t)
	aliceOfA := &auth.Claims{Email: "alice@a.example", EmailVerified: true, Tier: orgScopedTier, Org: "a"}
	stranger := &auth.Claims{Email: "eve@elsewhere.example", EmailVerified: true, Tier: "viewer"}
	bob := &auth.Claims{Email: "bob@b.example", EmailVerified: true, Tier: orgScopedTier, Org: "b"}
	dev := &auth.Claims{Email: "dev@b.example", EmailVerified: true, Tier: orgScopedTier, Org: "b"}
	vera := &auth.Claims{Email: "vera@b.example", EmailVerified: true, Tier: orgScopedTier, Org: "b"}
	unverified := &auth.Claims{Email: "bob@b.example", Tier: "viewer"}
	support := &auth.Claims{Email: "support+op@b.omani.homes", EmailVerified: true, Tier: "owner",
		SupportSession: true, ImpersonatedOrg: "b"}

	cases := []struct {
		name         string
		claims       *auth.Claims
		method, path string
		host         string
		want         int
	}{
		// A member of A on B's console (or naming B's host by X-Forwarded-Host).
		{"member of A installs into B", aliceOfA, http.MethodPost, "/catalyst/v1/apps/instances", "console.b.omani.homes", http.StatusForbidden},
		{"member of A lists B's users", aliceOfA, http.MethodGet, "/api/v1/org/users", "console.b.omani.homes", http.StatusForbidden},
		{"member of A, own console", aliceOfA, http.MethodPost, "/api/v1/org/applications", "console.a.omani.homes", http.StatusOK},
		// A stranger's Sovereign PIN session with a forged forwarded host.
		{"stranger installs into B", stranger, http.MethodPost, "/api/v1/org/applications", "console.b.omani.homes", http.StatusForbidden},
		{"stranger reads B", stranger, http.MethodGet, "/api/v1/whoami", "console.b.omani.homes", http.StatusForbidden},
		{"unverified email of B's owner", unverified, http.MethodGet, "/api/v1/whoami", "console.b.omani.homes", http.StatusForbidden},
		// B's members, each with the role B gives them.
		{"owner adds a user", bob, http.MethodPost, "/api/v1/org/users", "console.b.omani.homes", http.StatusOK},
		{"developer installs", dev, http.MethodPost, "/api/v1/org/applications", "console.b.omani.homes", http.StatusOK},
		{"developer adds a user", dev, http.MethodPost, "/api/v1/org/users", "console.b.omani.homes", http.StatusForbidden},
		{"viewer reads users", vera, http.MethodGet, "/api/v1/org/users", "console.b.omani.homes", http.StatusOK},
		{"viewer installs", vera, http.MethodPost, "/catalyst/v1/apps/instances", "console.b.omani.homes", http.StatusForbidden},
		{"viewer signs out", vera, http.MethodPost, "/api/v1/auth/logout", "console.b.omani.homes", http.StatusOK},
		// The sovereign-admin's support entry acts on the one Organization it entered.
		{"support session in B", support, http.MethodPost, "/api/v1/org/users", "console.b.omani.homes", http.StatusOK},
		{"support session of B in A", support, http.MethodGet, "/api/v1/org/users", "console.a.omani.homes", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, claimsReq(c.method, c.path, c.host, c.claims))
			if rec.Code != c.want {
				t.Fatalf("%s %s on %s: want %d got %d body=%s", c.method, c.path, c.host, c.want, rec.Code, rec.Body.String())
			}
			if reached != (c.want == http.StatusOK) {
				t.Fatalf("%s %s on %s: handler reached=%v, want %v", c.method, c.path, c.host, reached, c.want == http.StatusOK)
			}
		})
	}
}

// TestOrgScopeGuard_MembershipUnreadable_FailsClosed: an unreadable record is
// never read as membership.
func TestOrgScopeGuard_MembershipUnreadable_FailsClosed(t *testing.T) {
	h := twoOrgsHandler(t)
	h.endpointDeps.DynamicClient = func() (dynamic.Interface, error) { return nil, context.DeadlineExceeded }
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, claimsReq(http.MethodGet, "/api/v1/whoami", "console.b.omani.homes",
		&auth.Claims{Email: "bob@b.example", EmailVerified: true, Tier: orgScopedTier, Org: "b"}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when spec.owners cannot be read, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestResolveOrganization_TenantHeaderGrantsNothing: on the Sovereign's own
// host the client-supplied X-Tenant-Host names the Organization; the session
// must still hold a role there. Sovereign staff keep their reach.
func TestResolveOrganization_TenantHeaderGrantsNothing(t *testing.T) {
	h := twoOrgsHandler(t)
	cases := []struct {
		name   string
		claims *auth.Claims
		method string
		want   int // 0 = resolved
	}{
		{"viewer PIN session, stranger", &auth.Claims{Email: "eve@elsewhere.example", EmailVerified: true, Tier: "viewer"}, http.MethodGet, http.StatusForbidden},
		{"viewer PIN session, member of A", &auth.Claims{Email: "alice@a.example", EmailVerified: true, Tier: "viewer"}, http.MethodPost, http.StatusForbidden},
		{"viewer of B writes", &auth.Claims{Email: "vera@b.example", EmailVerified: true, Tier: "viewer"}, http.MethodPost, http.StatusForbidden},
		{"viewer of B reads", &auth.Claims{Email: "vera@b.example", EmailVerified: true, Tier: "viewer"}, http.MethodGet, 0},
		{"sovereign admin", &auth.Claims{Email: "ops@sovereign.example", EmailVerified: true, Tier: "admin"}, http.MethodPost, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := claimsReq(c.method, "/api/v1/org/users", "console.omantel.biz", c.claims)
			req.Header.Set("X-Tenant-Host", "console.b.omani.homes")
			rec := httptest.NewRecorder()
			_, ok := h.resolveOrganization(rec, req)
			if c.want == 0 {
				if !ok {
					t.Fatalf("want resolved, got %d body=%s", rec.Code, rec.Body.String())
				}
				return
			}
			if ok || rec.Code != c.want {
				t.Fatalf("want refused %d, got ok=%v status=%d body=%s", c.want, ok, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuthOrgHandover_NonMemberRefused: a valid marketplace token of someone
// who is not on B's spec.owners mints no session on B's console.
func TestAuthOrgHandover_NonMemberRefused(t *testing.T) {
	h, secret := orgHandoverTestSetup(t)
	for _, email := range []string{"alice@a.example", "stranger@elsewhere.example"} {
		token := mintMemberToken(t, secret, email, time.Now().Add(15*time.Minute))
		rec := httptest.NewRecorder()
		h.AuthOrgHandover(rec, orgHandoverRequest(token))
		if rec.Code == http.StatusFound {
			t.Fatalf("%s is not a member of demo and was handed a session (Location %q)", email, rec.Header().Get("Location"))
		}
		if c := findCookie(rec.Result().Cookies(), "catalyst_session"); c != nil && c.Value != "" {
			t.Fatalf("%s: a non-member was issued catalyst_session", email)
		}
		if !strings.Contains(rec.Body.String(), "not-an-organization-member") {
			t.Fatalf("%s: want the not-a-member refusal, got %d body=%s", email, rec.Code, rec.Body.String())
		}
	}
}

// TestPinVerify_OrgConsole_NonMemberRefused: proving a mailbox on B's console
// is not membership of B.
func TestPinVerify_OrgConsole_NonMemberRefused(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")
	h := testPinSetup(t)
	h.tenantRegistry = orgRegistry(t, "console.acme.omantel.biz", "acme")
	withOrgRecords(h, withOwners(orgCR("acme", "", "", ""), "owner@acme.test", "owner"))
	h.pinStore.put("stranger@elsewhere.test", "123456", "req-1")

	req := httptest.NewRequest(http.MethodPost,
		"http://console.acme.omantel.biz/api/v1/auth/pin/verify",
		strings.NewReader(`{"email":"stranger@elsewhere.test","pin":"123456","requestId":"req-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.acme.omantel.biz")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("a stranger's PIN on acme's console: want 403 got %d body=%s", w.Code, w.Body.String())
	}
	if c := findCookie(w.Result().Cookies(), "catalyst_session"); c != nil && c.Value != "" {
		t.Fatal("a stranger was issued catalyst_session on acme's console")
	}
}

// byoHandler serves two Organizations whose consoles share the host label
// "acme": alice owns acme (pool console console.acme.omani.homes), carol owns
// acme-corp, whose BYO console is console.acme.com (deriveConsoleHost). Both
// registrations are shaped as the Organization pipeline writes them
// (TenantID = the Organization's openova.io/tenant-id label, namespace = its
// slug).
func byoHandler(t *testing.T) *Handler {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, r := range []store.TenantRegistration{
		{Host: "console.acme.omani.homes", TenantID: "tnt-acme", TenantKind: store.TenantKindOrg, OrganizationNamespace: "acme"},
		{Host: "console.acme.com", TenantID: "tnt-corp", TenantKind: store.TenantKindOrg, OrganizationNamespace: "acme-corp"},
	} {
		if err := reg.Put(r); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	h := testPinSetup(t)
	h.tenantRegistry = reg
	withOrgRecords(h,
		withOwners(orgCR("acme", "omani.homes", "", "tnt-acme"), "alice@acme.example", "owner"),
		withOwners(orgCR("acme-corp", "", "", "tnt-corp"), "carol@corp.example", "owner"),
	)
	return h
}

// TestBYOConsole_MembershipIsTheRegisteredOrganizations: on a BYO console the
// membership read is the Organization the host is registered to, not the one
// its label names. Before the repair carol was refused a session on her own
// console and alice was admitted to it.
func TestBYOConsole_MembershipIsTheRegisteredOrganizations(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")
	h := byoHandler(t)

	pin := func(email string) *httptest.ResponseRecorder {
		h.pinStore.put(email, "123456", "req-"+email)
		req := httptest.NewRequest(http.MethodPost, "http://console.acme.com/api/v1/auth/pin/verify",
			strings.NewReader(`{"email":"`+email+`","pin":"123456","requestId":"req-`+email+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Host", "console.acme.com")
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.HandlePinVerify(w, req)
		return w
	}
	if w := pin("carol@corp.example"); w.Code != http.StatusOK || findCookie(w.Result().Cookies(), "catalyst_session") == nil {
		t.Fatalf("the owner of acme-corp signing in on its console: want 200 and a session, got %d body=%s", w.Code, w.Body.String())
	}
	if w := pin("alice@acme.example"); w.Code != http.StatusForbidden {
		t.Fatalf("the owner of acme signing in on acme-corp's console: want 403, got %d body=%s", w.Code, w.Body.String())
	}

	// Sessions minted on console.acme.com carry its host label as org claim.
	carol := &auth.Claims{Email: "carol@corp.example", EmailVerified: true, Tier: orgScopedTier, Org: "acme"}
	alice := &auth.Claims{Email: "alice@acme.example", EmailVerified: true, Tier: orgScopedTier, Org: "acme"}
	for _, c := range []struct {
		name   string
		claims *auth.Claims
		host   string
		want   int
	}{
		{"owner of acme-corp on its console", carol, "console.acme.com", http.StatusOK},
		{"owner of acme on acme-corp's console", alice, "console.acme.com", http.StatusForbidden},
		{"owner of acme on its own console", alice, "console.acme.omani.homes", http.StatusOK},
	} {
		reached := false
		guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, claimsReq(http.MethodPost, "/api/v1/org/users", c.host, c.claims))
		if rec.Code != c.want || reached != (c.want == http.StatusOK) {
			t.Fatalf("%s: want %d, got %d (handler reached=%v) body=%s", c.name, c.want, rec.Code, reached, rec.Body.String())
		}
	}

	// carol's session names acme's console in X-Tenant-Host: the label
	// matches her claim, the registration does not.
	for _, c := range []struct {
		tenantHost string
		ok         bool
	}{{"console.acme.com", true}, {"console.acme.omani.homes", false}} {
		req := claimsReq(http.MethodPost, "/api/v1/org/users", "console.acme.com", carol)
		req.Header.Set("X-Tenant-Host", c.tenantHost)
		rec := httptest.NewRecorder()
		if _, ok := h.resolveOrganization(rec, req); ok != c.ok || (!ok && rec.Code != http.StatusForbidden) {
			t.Fatalf("carol on console.acme.com, X-Tenant-Host %s: want resolved=%v, got %v status=%d body=%s", c.tenantHost, c.ok, ok, rec.Code, rec.Body.String())
		}
	}
}

// withOrgRecords wires the client orgmember reads Organization objects
// through (Handler.dynamicClientOrFallback) to a fake cluster holding orgs.
func withOrgRecords(h *Handler, orgs ...*unstructured.Unstructured) {
	f := orgCRDepsFactory(orgs...)
	h.endpointDeps.DynamicClient = func() (dynamic.Interface, error) {
		d, err := f()
		if err != nil {
			return nil, err
		}
		return d.dyn, nil
	}
}
