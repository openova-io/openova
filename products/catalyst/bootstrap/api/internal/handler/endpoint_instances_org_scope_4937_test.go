// endpoint_instances_org_scope_4937_test.go — #4937 authz coverage for the
// customer self-service app-instance seam.
//
// The bug (live on hw234, Org acme): a customer signed into their OWN Org
// console (console.acme.omani.homes, marketplace→console handover, an Org-
// scoped RS256 session tier=org-admin) got `getApplicationInstances HTTP 403`
// and could not self-install. Root cause: the app-instance endpoints
// (GET /catalyst/v1/catalog/{bp}/instances, POST /catalyst/v1/apps/instances)
// were NOT on the OrgScopeGuard org-safe allowlist, and the create path's
// tier gate (applicationInstallCallerAuthorized) rejected tier=org-admin.
//
// These tests pin the authz DECISION at the handler level:
//   - own-Org session  → 200 on the instances list, confined to its own Org.
//   - cross-Org query   → 403 forbidden-cross-org.
//   - own-Org session  → 201 on install, forced into its own Org namespace,
//     Sovereign-tier gate skipped (RBAC parity with the Org console UI).
//   - a cross-Org install body is FORCED to the caller's own Org (never leaks).
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgConsoleHost is the customer's own Org console host (tenant_kind=org).
const orgConsoleHost4937 = "console.acme.omani.homes"

// seedAppWithOrgLabel builds an Application in namespace `ns` whose
// `catalyst.openova.io/organization` label is `orgLabel` — used to simulate
// the /org/applications install path, which labels CRs with the real
// `org-<uuid>` namespace rather than the slug. #4937's namespace-anchored
// confinement must still return it for the owning Org.
func seedAppWithOrgLabel(uid, name, ns, orgLabel, blueprint string) *unstructured.Unstructured {
	app := seedApp(uid, name, ns, blueprint)
	labels := app.GetLabels()
	labels["catalyst.openova.io/organization"] = orgLabel
	app.SetLabels(labels)
	return app
}

// withOrgConsole wires an Org-scoped customer session onto a request: the
// host anchor (X-Forwarded-Host, the gateway-set real browser host that an
// Org-console browser cannot forge) PLUS the org-admin session claims the
// marketplace→console handover mints.
func withOrgConsole(req *http.Request, host, org string) *http.Request {
	req.Header.Set("X-Forwarded-Host", host)
	return withTestClaims(req, &testClaimsSpec{
		Email: "member@" + org + ".example",
		Tier:  orgScopedTier, // "org-admin"
		Org:   org,
	})
}

// orgRegistry returns a tenant registry with `host` registered as a
// tenant_kind=org console whose Kubernetes namespace is `ns`.
func orgRegistry(t *testing.T, host, ns string) *store.TenantRegistry {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:                  host,
		TenantID:              "tenant-org-acme",
		TenantKind:            store.TenantKindOrg,
		OrganizationNamespace: ns,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	return reg
}

// TestListBlueprintInstances_OrgScoped_ConfinedToOwnOrg — the reproduced
// 403 becomes a 200 scoped to the caller's own Org. The listing is namespace-
// anchored, so it includes an instance labelled with the real `org-<uuid>`
// (the /org/applications install convention) and EXCLUDES another Org's
// instance in a different namespace.
func TestListBlueprintInstances_OrgScoped_ConfinedToOwnOrg(t *testing.T) {
	own := seedApp("uid-4937-1", "wp-own", "acme", "wordpress")
	// Same Org, different label convention (real org-<uuid> namespace).
	ownAltLabel := seedAppWithOrgLabel("uid-4937-2", "wp-own-2", "acme", "org-abc123", "wordpress")
	other := seedApp("uid-4937-3", "wp-other", "bigcorp", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, own, ownAltLabel, other)
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	req := httptest.NewRequest("GET", "/catalyst/v1/catalog/wordpress/instances", nil)
	req = withOrgConsole(req, orgConsoleHost4937, "acme")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("own-Org session must get 200 (not the #4937 403), got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp listInstancesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 own-Org instances (namespace-confined), got %d: %+v", len(resp.Items), resp.Items)
	}
	for _, it := range resp.Items {
		if it.Name == "wp-other" {
			t.Fatalf("cross-Org instance wp-other leaked into the own-Org listing: %+v", resp.Items)
		}
	}
}

// TestListBlueprintInstances_OrgScoped_CrossOrgQuery403 — an Org-scoped
// session that explicitly asks for another Org is 403'd.
func TestListBlueprintInstances_OrgScoped_CrossOrgQuery403(t *testing.T) {
	own := seedApp("uid-4937-10", "wp-own", "acme", "wordpress")
	other := seedApp("uid-4937-11", "wp-other", "bigcorp", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, own, other)
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	req := httptest.NewRequest("GET", "/catalyst/v1/catalog/wordpress/instances?org=bigcorp", nil)
	req = withOrgConsole(req, orgConsoleHost4937, "acme")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-Org query must be 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("forbidden-cross-org")) {
		t.Fatalf("expected forbidden-cross-org, got body=%s", rec.Body.String())
	}
}

// TestListBlueprintInstances_Operator_Unchanged — a Sovereign operator (no
// Org scope: not on an Org host, no org-admin claims) keeps the cluster-wide
// listing. Guards against a regression that would confine the operator.
func TestListBlueprintInstances_Operator_Unchanged(t *testing.T) {
	a := seedApp("uid-4937-20", "wp-a", "acme", "wordpress")
	b := seedApp("uid-4937-21", "wp-b", "bigcorp", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, a, b)
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	// Operator on the Sovereign console host (not a tenant_kind=org host),
	// carrying an owner-tier session — must see BOTH Orgs' instances.
	req := httptest.NewRequest("GET", "/catalyst/v1/catalog/wordpress/instances", nil)
	req.Header.Set("X-Forwarded-Host", "console.t01.omani.works")
	req = withTestClaims(req, &testClaimsSpec{Email: "op@openova.io", Tier: "owner"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("operator must get 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp listInstancesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 2 {
		t.Fatalf("operator must see both Orgs' instances (cluster-wide), got %d", len(resp.Items))
	}
}

// TestCreateInstance_OrgScoped_AllowsOwnOrgSkipsTierGate — the customer can
// self-install into their OWN Org. The org-admin tier (which the Sovereign
// gate rejects) is accepted here because the OrgScopeGuard-allowlisted,
// host-anchored own-Org binding IS the authz boundary, and the target is
// FORCED to the caller's own Org namespace regardless of the body's `org`.
func TestCreateInstance_OrgScoped_AllowsOwnOrgSkipsTierGate(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton", "active-hot-standby"}))
	r := newTestRouter(h)

	// The body claims a DIFFERENT Org (an attacker hint) — it must be ignored
	// and the CR forced into the caller's own Org namespace ("acme").
	body := []byte(`{"blueprint":"wordpress","org":"bigcorp","name":"wp-self","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withOrgConsole(req, orgConsoleHost4937, "acme")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("own-Org install must be 201 (tier gate skipped), got %d body=%s", rec.Code, rec.Body.String())
	}
	// CR landed in the caller's own Org namespace, NOT the body's "bigcorp".
	if _, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-self", metav1.GetOptions{}); err != nil {
		t.Fatalf("CR must land in own-Org namespace acme; got %v", err)
	}
	if _, err := dyn.Resource(ApplicationGVR()).Namespace("bigcorp").Get(context.Background(), "wp-self", metav1.GetOptions{}); err == nil {
		t.Fatal("CR must NOT land in the body-specified cross-Org namespace bigcorp")
	}
}

// TestCreateInstance_Operator_TierGateUnchanged — a non-Org-scoped caller
// with an unprivileged tier is still rejected by the Sovereign-tier gate.
// Proves #4937 only relaxes the gate for the confined own-Org path.
func TestCreateInstance_Operator_TierGateUnchanged(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	// No Org host in the registry for this request's host → not Org-scoped.
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-x","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.t01.omani.works") // Sovereign host, not Org-scoped
	// Unprivileged tier, not org-admin → the Sovereign gate must reject.
	req = withTestClaims(req, &testClaimsSpec{Email: "nobody@example.com", Tier: "viewer"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unprivileged non-Org-scoped caller must be 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}
