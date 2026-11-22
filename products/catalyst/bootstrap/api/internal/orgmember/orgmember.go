// Package orgmember decides which Organization a session may act on, and
// with which role.
//
// THE RULE. The Organization a request acts on, and the role it holds there,
// come from the VERIFIED SESSION'S MEMBERSHIP of that Organization, never from
// the request host, a header or an address. The host (console.<org>.<pool>,
// or the client's X-Tenant-Host) only says which Organization the request is
// ABOUT; this package decides whether the session may act on it. A session
// that holds no role in that Organization is refused.
//
// Before this package, catalyst-api decided "Organization admin" from the
// host alone: PIN sign-in on console.<org>.<pool> (handler/auth.go), the
// marketplace hand-off /auth/org-handover (handler/org_handover.go) and every
// request that arrived on an Organization console host (OrgScopeGuard,
// handler/org_scope.go). Anyone who could prove a mailbox became the admin of
// whichever Organization's console they opened, and a viewer session of the
// Sovereign console reached any Organization's users with the header
// X-Tenant-Host (handler/org_users.go resolveOrganization).
//
// THE MEMBERSHIP RECORD is spec.owners of the Organization object (cluster-
// scoped, named by its slug): one entry per person, {email, role} with role
// owner|admin|developer|viewer. Both doors that create an Organization write
// the first entry, the Organization's admin email as owner
// (handler/org_tenant_org_cr.go ensureOrganizationCR,
// core/services/provisioning/handlers/organization_create.go); the
// organization-controller turns each entry into a UserAccess grant; and the
// agent-token issuer reads the same list (agenttoken.roleIn). It is the one
// membership record the platform keeps, so this package reads it and writes
// nothing.
package orgmember

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// The roles of Organization.spec.owners[].role.
const (
	Owner     = "owner"
	Admin     = "admin"
	Developer = "developer"
	Viewer    = "viewer"
)

// TTL is how long one answer of the membership record is reused. The guard
// asks on every request of an Organization console and the SPA polls, so a
// short reuse keeps the apiserver load flat, while a removed member loses
// access within this window.
const TTL = 15 * time.Second

// organizations is the Organization resource (orgs.openova.io/v1), the same
// one handler.organizationGVR and agenttoken read.
var organizations = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}

// Members answers membership questions for one API server, reading
// Organization objects through dyn and keeping each answer for TTL. Errors
// are never kept.
type Members struct {
	dyn   func() (dynamic.Interface, error)
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	role    string
	expires time.Time
}

// registry holds one Members per owner (one per handler.Handler), so tests
// that build their own handler and fake cluster never share answers.
var registry sync.Map

// For returns the Members of owner, creating it on first use with dyn as the
// client of Organization objects. dyn is called on every read, so a client
// that is wired later is picked up.
func For(owner any, dyn func() (dynamic.Interface, error)) *Members {
	if m, ok := registry.Load(owner); ok {
		return m.(*Members)
	}
	m, _ := registry.LoadOrStore(owner, &Members{dyn: dyn, now: time.Now})
	return m.(*Members)
}

// RoleOf returns the role email holds in Organization org, read from
// spec.owners: "" when the Organization does not list that person or does
// not exist. org may be the object's name (the slug) or, when no object has
// that name, its spec.tenantPublic.subdomain (the console host label). An
// error means the record could not be read; callers refuse on it.
func (m *Members) RoleOf(ctx context.Context, org, email string) (string, error) {
	org = norm(org)
	email = norm(email)
	if org == "" || email == "" {
		return "", nil
	}
	key := org + "\x00" + email
	now := m.now()
	m.mu.Lock()
	if e, ok := m.cache[key]; ok && now.Before(e.expires) {
		m.mu.Unlock()
		return e.role, nil
	}
	m.mu.Unlock()

	dyn, err := m.dyn()
	if err != nil || dyn == nil {
		return "", fmt.Errorf("no client reads Organization objects: %v", err)
	}
	res := dyn.Resource(organizations)
	obj, err := res.Get(ctx, org, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj, err = bySubdomain(ctx, res, org)
	}
	if err != nil {
		return "", fmt.Errorf("the Organization %s could not be read: %v", org, err)
	}
	role := ""
	if obj != nil {
		role = roleIn(obj, email)
	}
	m.mu.Lock()
	if m.cache == nil {
		m.cache = map[string]entry{}
	}
	m.cache[key] = entry{role: role, expires: now.Add(TTL)}
	m.mu.Unlock()
	return role, nil
}

// bySubdomain finds the Organization whose spec.tenantPublic.subdomain is
// sub; (nil, nil) when none is.
func bySubdomain(ctx context.Context, res dynamic.NamespaceableResourceInterface, sub string) (*unstructured.Unstructured, error) {
	list, err := res.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		s, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "tenantPublic", "subdomain")
		if norm(s) == sub {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// roleIn returns the role of email's entry in spec.owners of obj, or "" when
// there is none. An entry with an empty or unknown role holds no role.
func roleIn(obj *unstructured.Unstructured, email string) string {
	owners, _, _ := unstructured.NestedSlice(obj.Object, "spec", "owners")
	for _, o := range owners {
		e, _ := o.(map[string]any)
		addr, _ := e["email"].(string)
		if norm(addr) != email {
			continue
		}
		role, _ := e["role"].(string)
		switch r := norm(role); r {
		case Owner, Admin, Developer, Viewer:
			return r
		}
		return ""
	}
	return ""
}

// scopedToOne reports whether a session was minted for exactly one
// Organization: a session of an Organization console (tier org-admin) or an
// agent token (typ agent). Mirrors handler.claimsAreOrgScoped.
func scopedToOne(c *auth.Claims) bool {
	return strings.EqualFold(strings.TrimSpace(c.Tier), auth.OrgScopedTier) || isAgent(c)
}

// isAgent reports whether c is an agent token (claim typ agent,
// internal/agenttoken).
func isAgent(c *auth.Claims) bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.Typ), "agent")
}

// RoleFor returns the role the session of c holds in Organization org, or ""
// when it holds none there:
//
//   - an agent token holds its own tier (viewer or developer) in the
//     Organization of its org claim only; its issuer checked the person
//     behind it (agenttoken.roleIn);
//   - a sovereign-admin's support session (support_session) holds admin in
//     the one Organization it was entered on (impersonated_org) and in no
//     other;
//   - a session minted for one Organization never acts on another, and holds
//     in its own the role its email holds there now;
//   - every other session (a PIN session of the Sovereign console, a Keycloak
//     token) holds the role its verified email holds in spec.owners.
func (m *Members) RoleFor(ctx context.Context, c *auth.Claims, org string) (string, error) {
	org = norm(org)
	if c == nil || org == "" {
		return "", nil
	}
	own := norm(c.Org)
	switch {
	case isAgent(c):
		if t := norm(c.Tier); own == org && (t == Viewer || t == Developer) {
			return t, nil
		}
		return "", nil
	case c.SupportSession:
		if norm(c.ImpersonatedOrg) == org && (own == "" || own == org) {
			return Admin, nil
		}
		return "", nil
	case scopedToOne(c) && own != org:
		return "", nil
	case !c.EmailVerified:
		return "", nil
	}
	return m.RoleOf(ctx, org, c.Email)
}

// installPaths are the Organization's own surfaces that create or change
// Applications: a developer may write them, a viewer may not.
var installPaths = []string{
	"/api/v1/org/applications",
	"/api/v1/org/apps",
	"/catalyst/v1/apps/instances",
}

// MayDo reports whether role may make a request of method on path in its
// Organization. Every role reads; every role may sign itself in and out
// (/api/v1/auth/); a developer may also install Applications; owner and admin
// may do everything the Organization console offers.
func MayDo(role, method, path string) bool {
	if role == "" {
		return false
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if strings.HasPrefix(path, "/api/v1/auth/") {
		return true
	}
	switch role {
	case Owner, Admin:
		return true
	case Developer:
		for _, p := range installPaths {
			if path == p || strings.HasPrefix(path, p+"/") {
				return true
			}
		}
	}
	return false
}

// Target returns the Organization a request is about: the Organization of the
// console host when the request arrived on one, else the session's own org
// claim.
func Target(c *auth.Claims, hostOrg string, onOrgHost bool) string {
	if onOrgHost || c == nil {
		return norm(hostOrg)
	}
	return norm(c.Org)
}

// Gate answers whether the session of c may make request r on Organization
// org. When it may not, Gate writes the refusal and returns false: 503
// org-membership-unavailable when the record cannot be read (fail closed),
// 403 not-an-organization-member when the session holds no role there, 403
// org-role-forbidden when its role does not permit the request.
func (m *Members) Gate(w http.ResponseWriter, r *http.Request, c *auth.Claims, org string) bool {
	role, err := m.RoleFor(r.Context(), c, org)
	switch {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, "org-membership-unavailable", "the membership record of this Organization could not be read")
	case role == "":
		refuse(w, http.StatusForbidden, "not-an-organization-member", "this session holds no role in this Organization")
	case !isAgent(c) && !MayDo(role, r.Method, r.URL.Path):
		// An agent token's writes are decided by agenttoken.Restrict, which
		// runs next in the session route group: a viewer token that acts for
		// an admin (acting_person) may install (MutationsAllowed). Here it is
		// held to its own Organization only.
		refuse(w, http.StatusForbidden, "org-role-forbidden", "the role "+role+" in this Organization does not permit this request")
	default:
		return true
	}
	return false
}

// Refusal answers whether the person with the verified address email may be
// given a session on Organization org's console: (0, "") when they hold a
// role there; otherwise the status and reason a sign-in door answers with,
// 503 when the record cannot be read and 403 when they are not a member.
func (m *Members) Refusal(ctx context.Context, org, email string) (int, string) {
	role, err := m.RoleOf(ctx, org, email)
	switch {
	case err != nil:
		return http.StatusServiceUnavailable, "org-membership-unavailable"
	case role == "":
		return http.StatusForbidden, "not-an-organization-member"
	}
	return 0, ""
}

func refuse(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": detail})
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
