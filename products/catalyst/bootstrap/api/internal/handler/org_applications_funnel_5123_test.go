// org_applications_funnel_5123_test.go — #5123 Facet A: the per-Org console
// never listed funnel-purchased apps.
//
// A funnel purchase deploys NO Application CR and NO HelmRelease: the
// provisioning consumer commits Deployment-shaped workloads into the per-Org
// `<slug>/catalyst-tenant` repo (`vcluster/apps`, #4384), which the
// org-controller's Flux Kustomization `catalyst-tenant-<slug>-apps` reconciles
// into the Org boundary. The ONLY host-visible per-app artifact in the Org
// namespace is the funnel-labeled HTTPRoute (`per-org-app-route` for the host
// tier, `per-org-app-hostroute` for the vcluster tier, #4993). The projection
// used those routes solely as a URL index — never as a card source — so a
// customer who purchased WordPress saw "0 apps" (hw256 acme256, UAT row 89:
// GET /api/v1/org/applications returned only the `vcluster` HR card while
// https://wordpress.acme256.omani.homes served 200).
//
// These tests mirror the hw256 estate byte-for-byte and pin the funnel pass:
// route-labeled purchases are projected as instance cards, status honest from
// the `catalyst-tenant-<slug>-apps` Kustomization Ready condition, deduped
// against Application-CR/HR cards, own-Org-scoped only.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// funnelOrgAppsRec drives GET /api/v1/org/applications as an acme256
// Org-scoped session against a handler seeded with the supplied dynamic
// objects, returning the raw recorder.
func funnelOrgAppsRec(t *testing.T, dynObjs []runtime.Object) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("SOVEREIGN_FQDN", "hw256.omani.works")
	h := newSovereignHandler(t, nil, dynObjs)
	h.SetTenantRegistry(funnelOrgTestRegistry(t))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/applications", nil)
	req.Header.Set("X-Tenant-Host", "console.acme256.omani.homes")
	claims := &auth.Claims{Tier: orgScopedTier, Org: "acme256"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	h.HandleOrgApplications(rec, req)
	return rec
}

// funnelOrgTestRegistry registers a funnel-minted Org shaped exactly like the
// hw256 walk's acme256: the tenant-registry reconciler
// (tenantRegistrationFromOrgCR) sets OrganizationNamespace to the SLUG
// namespace — the org-controller boundary ns the apps Kustomization targets —
// not an `org-<uuid>` namespace.
func funnelOrgTestRegistry(t *testing.T) *store.TenantRegistry {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:                  "console.acme256.omani.homes",
		TenantID:              "acme256-tenant-uuid",
		TenantKind:            store.TenantKindOrg,
		KeycloakRealmURL:      "https://keycloak.acme256.omani.homes/realms/org-acme256",
		KeycloakClientID:      "catalyst-ui",
		OrganizationNamespace: "acme256",
		OrgKeycloakRealmName:  "org-acme256",
	}); err != nil {
		t.Fatalf("put acme256: %v", err)
	}
	return reg
}

// makeFunnelAppRoute builds the per-app HTTPRoute the funnel commits for a
// Deployment-shaped purchased app: `app-<slug>-hostroute` (vcluster tier,
// generateHostNativeAppRoute #4993) or `app-<slug>` (host tier,
// generateAppHTTPRoute), labeled app=<slug> + openova.io/tenant=<org-slug> +
// catalyst.openova.io/component=<component>.
func makeFunnelAppRoute(name, ns, appSlug, orgSlug, component, host, backendSvc string) *unstructured.Unstructured {
	u := makeHTTPRouteWithBackend(name, ns, []string{host}, backendSvc)
	u.SetLabels(map[string]string{
		"app":                           appSlug,
		"openova.io/tenant":             orgSlug,
		"catalyst.openova.io/component": component,
	})
	return u
}

// makeAppsKustomization builds the org-controller's per-Org apps Flux
// Kustomization (`catalyst-tenant-<slug>-apps` in flux-system) with the given
// Ready condition status — the delivery unit of every funnel purchase.
func makeAppsKustomization(orgSlug, ready string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	})
	u.SetName("catalyst-tenant-" + orgSlug + "-apps")
	u.SetNamespace("flux-system")
	conds := []interface{}{
		map[string]interface{}{
			"type":   "Ready",
			"status": ready,
			"reason": "ReconciliationSucceeded",
		},
	}
	_ = unstructured.SetNestedSlice(u.Object, conds, "status", "conditions")
	return u
}

// funnelOrgAppsGet drives GET /api/v1/org/applications for the acme256 funnel
// Org and decodes the card list into an id-keyed map.
func funnelOrgAppsGet(t *testing.T, dynObjs []runtime.Object) map[string]struct {
	slug, bp, status, url string
	instance              bool
} {
	t.Helper()
	rec := funnelOrgAppsRec(t, dynObjs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []struct {
			ID          string `json:"id"`
			Slug        string `json:"slug"`
			Blueprint   string `json:"blueprint"`
			Status      string `json:"status"`
			ExternalURL string `json:"externalURL"`
			Instance    bool   `json:"instance"`
		} `json:"apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		slug, bp, status, url string
		instance              bool
	}{}
	for _, a := range resp.Apps {
		byID[a.ID] = struct {
			slug, bp, status, url string
			instance              bool
		}{a.Slug, a.Blueprint, a.Status, a.ExternalURL, a.Instance}
	}
	return byID
}

// TestOrgApplications_FunnelPurchasedAppListed_5123 — the hw256 acme256
// estate: the Org's vcluster HR + the funnel-purchased WordPress delivered as
// a Deployment inside the vcluster with its #4993 host-native route in the
// Org namespace, apps Kustomization Ready=True. The projection MUST list the
// purchase as an installed card with its working Open URL — pre-fix it
// returned only the vcluster HR card ("0 apps" grid, UAT row 89 ❌).
func TestOrgApplications_FunnelPurchasedAppListed_5123(t *testing.T) {
	dynObjs := []runtime.Object{
		makeHRWithRelease("vcluster", "acme256", "True", "vcluster", ""),
		makeFunnelAppRoute("app-wordpress-hostroute", "acme256",
			"wordpress", "acme256", "per-org-app-hostroute",
			"wordpress.acme256.omani.homes", "wordpress-x-acme256-x-vcluster"),
		makeAppsKustomization("acme256", "True"),
		// FOREIGN funnel purchase in a sibling Org's namespace — must not leak.
		makeFunnelAppRoute("app-ghost-hostroute", "beta256",
			"ghost", "beta256", "per-org-app-hostroute",
			"ghost.beta256.omani.homes", "ghost-x-beta256-x-vcluster"),
	}
	byID := funnelOrgAppsGet(t, dynObjs)

	wp, ok := byID["wordpress"]
	if !ok {
		t.Fatalf("funnel-purchased wordpress missing from org projection (the #5123 bug); got %v", byID)
	}
	if wp.status != "installed" {
		t.Errorf("wordpress status = %q, want installed (apps Kustomization Ready=True)", wp.status)
	}
	if wp.url != "https://wordpress.acme256.omani.homes" {
		t.Errorf("wordpress externalURL = %q, want https://wordpress.acme256.omani.homes", wp.url)
	}
	if wp.bp != "bp-wordpress" {
		t.Errorf("wordpress blueprint = %q, want bp-wordpress", wp.bp)
	}
	if !wp.instance {
		t.Errorf("wordpress card must be an instance row")
	}
	if _, ok := byID["vcluster"]; !ok {
		t.Errorf("vcluster HR card regressed out of the projection; got %v", byID)
	}
	if _, leaked := byID["ghost"]; leaked {
		t.Errorf("FOREIGN funnel purchase leaked into acme256 projection: %v", byID)
	}
}

// TestOrgApplications_FunnelAppInstalling_KustomizationNotReady_5123 — the
// host-tier route shape (`app-<slug>`, per-org-app-route) with the apps
// Kustomization not (yet) Ready projects the purchase honestly as installing,
// never a fabricated installed.
func TestOrgApplications_FunnelAppInstalling_KustomizationNotReady_5123(t *testing.T) {
	dynObjs := []runtime.Object{
		makeFunnelAppRoute("app-wordpress", "acme256",
			"wordpress", "acme256", "per-org-app-route",
			"wordpress.acme256.omani.homes", "wordpress"),
		makeAppsKustomization("acme256", "False"),
	}
	byID := funnelOrgAppsGet(t, dynObjs)
	wp, ok := byID["wordpress"]
	if !ok {
		t.Fatalf("host-tier funnel wordpress missing from org projection; got %v", byID)
	}
	if wp.status != "installing" {
		t.Errorf("wordpress status = %q, want installing (apps Kustomization Ready=False)", wp.status)
	}
}

// TestOrgApplications_FunnelRouteDedupedAgainstApplicationCR_5123 — when an
// Application CR already projects the same id (e.g. a console-installed app
// that also carries a funnel-shaped route), the funnel pass must not emit a
// duplicate card.
func TestOrgApplications_FunnelRouteDedupedAgainstApplicationCR_5123(t *testing.T) {
	dynObjs := []runtime.Object{
		makeApplicationFull("wordpress", "acme256", "bp-wordpress", "Ready"),
		makeFunnelAppRoute("app-wordpress", "acme256",
			"wordpress", "acme256", "per-org-app-route",
			"wordpress.acme256.omani.homes", "wordpress"),
		makeAppsKustomization("acme256", "True"),
	}
	rec := funnelOrgAppsRec(t, dynObjs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []struct {
			ID string `json:"id"`
		} `json:"apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	count := 0
	for _, a := range resp.Apps {
		if a.ID == "wordpress" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("wordpress projected %d times, want exactly 1 (CR card, no funnel duplicate)", count)
	}
}
