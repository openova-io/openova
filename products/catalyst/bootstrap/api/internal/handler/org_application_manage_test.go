package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/appmanage"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// A customer manages their OWN Application: before these routes the
// Organization session could install (POST /api/v1/org/applications) but the
// detail, status, upgrade and uninstall routes existed only under
// /api/v1/sovereigns/{id}/applications/..., which OrgScopeGuard refuses for
// an Organization session (org-scoped-forbidden).

const acmeOrgNS = "org-acme-uuid"

// registerOrgApplicationManageRoutes wires the routes exactly as
// cmd/api/routes_org.go does.
func registerOrgApplicationManageRoutes(r chi.Router, h *Handler) {
	own := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		tenant, ok := h.ResolveOrganization(w, r)
		if !ok {
			return "", "", false
		}
		return tenant.OrganizationNamespace, h.SelfDeploymentID(r), true
	}
	appmanage.RegisterOrgRoutes(r, own, appmanage.Routes{
		Get: h.HandleApplicationGet, Status: h.HandleApplicationStatus,
		Update: h.HandleApplicationUpdate, Delete: h.HandleApplicationDelete,
		UpgradePreview: h.HandleApplicationUpgradePreview, TopologyPreview: h.HandleApplicationTopologyPreview,
	})
}

// keepWhileFinalized makes the fake dynamic client honour finalizers the way
// the apiserver does: a delete of an object that still carries a finalizer
// only marks it (deletionTimestamp) and leaves it in place. The plain fake
// tracker removes it at once, which is exactly the shape that let the delete
// handler answer "deleted" without ever looking.
func keepWhileFinalized(client *dynamicfake.FakeDynamicClient) {
	client.PrependReactor("delete", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		da := a.(k8stesting.DeleteAction)
		obj, err := client.Tracker().Get(da.GetResource(), da.GetNamespace(), da.GetName())
		if err != nil {
			return false, nil, nil
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok || len(u.GetFinalizers()) == 0 {
			return false, nil, nil
		}
		u = u.DeepCopy()
		now := metav1.Now()
		u.SetDeletionTimestamp(&now)
		return true, nil, client.Tracker().Update(da.GetResource(), u, da.GetNamespace())
	})
}

func withAppFinalizer(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetFinalizers([]string{"apps.openova.io/application-controller"})
	return u
}

type orgManageFixture struct {
	h      *Handler
	client *dynamicfake.FakeDynamicClient
}

func newOrgManageFixture(t *testing.T, seed ...runtime.Object) orgManageFixture {
	t.Helper()
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(seed...)
	keepWhileFinalized(client)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	h.SetTenantRegistry(orgAppTestRegistry(t))
	dep := installUserAccessDeployment(t, h, "dep-org-manage")
	// The local Sovereign deployment of an Organization request; a
	// SOVEREIGN_FQDN other than the deployment's keeps the client on the
	// test factory instead of the in-cluster config.
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", dep.ID)
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	return orgManageFixture{h: h, client: client}
}

func (f orgManageFixture) call(t *testing.T, method, path, host string, claims *auth.Claims, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	registerOrgApplicationManageRoutes(r, f.h)
	var rd *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("X-Tenant-Host", host)
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (f orgManageFixture) get(t *testing.T, ns, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	u, err := f.client.Resource(ApplicationGVR()).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	return u, err == nil
}

var demoClaims = &auth.Claims{Tier: orgScopedTier, Org: "demo"}

// TestOrgApplicationRoutes_AreOnTheOrgAllowlist — the guard's own verdict:
// the Organization routes pass for every method the console sends, while the
// Sovereign twins stay refused (no surface widening).
func TestOrgApplicationRoutes_AreOnTheOrgAllowlist(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/org/applications/shop"},
		{http.MethodGet, "/api/v1/org/applications/shop/status"},
		{http.MethodPut, "/api/v1/org/applications/shop"},
		{http.MethodDelete, "/api/v1/org/applications/shop"},
		{http.MethodPost, "/api/v1/org/applications/shop/upgrade/preview"},
	} {
		if !requestIsOrgSafe(c.method, c.path) {
			t.Errorf("%s %s is refused for an Organization session", c.method, c.path)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if requestIsOrgSafe(m, "/api/v1/sovereigns/dep/applications/shop") {
			t.Errorf("%s on the Sovereign application route must stay refused for an Organization session", m)
		}
	}
}

// TestOrgApplicationStatus_ReadsOwnOrgOnly — the customer reads the status of
// their own Application; ?namespace= naming a sibling Organization is ignored.
func TestOrgApplicationStatus_ReadsOwnOrgOnly(t *testing.T) {
	own := makeAppCR(demoOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"})
	_ = unstructured.SetNestedField(own.Object, "Degraded", "status", "phase")
	other := makeAppCR(acmeOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"})
	_ = unstructured.SetNestedField(other.Object, "Ready", "status", "phase")
	f := newOrgManageFixture(t, own, other)

	rec := f.call(t, http.MethodGet, "/api/v1/org/applications/shop/status?namespace="+acmeOrgNS,
		"console.demo.omani.homes", demoClaims, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status read: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Namespace string `json:"namespace"`
		Phase     string `json:"phase"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if resp.Namespace != demoOrgNS || resp.Phase != "Degraded" {
		t.Fatalf("read namespace/phase = %q/%q, want the caller's own %q/Degraded; body=%s",
			resp.Namespace, resp.Phase, demoOrgNS, rec.Body.String())
	}

	if rec := f.call(t, http.MethodGet, "/api/v1/org/applications/shop", "console.demo.omani.homes", demoClaims, nil); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), demoOrgNS) || strings.Contains(rec.Body.String(), acmeOrgNS) {
		t.Fatalf("detail read: got %d, want 200 on the own Organization only; body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrgApplicationManage_ForgedSiblingHostRefused — a demo session that
// forges a sibling Organization's console host never reaches its Application.
func TestOrgApplicationManage_ForgedSiblingHostRefused(t *testing.T) {
	other := withAppFinalizer(makeAppCR(acmeOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"}))
	f := newOrgManageFixture(t, other)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		path := "/api/v1/org/applications/shop"
		var body any
		if m == http.MethodPut {
			body = map[string]any{"parameters": map[string]any{"domain": "leak.example"}}
		}
		rec := f.call(t, m, path, "console.acme.omani.homes", demoClaims, body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "org-scope-mismatch") {
			t.Fatalf("%s with a forged sibling host: got %d, want 403 org-scope-mismatch; body=%s", m, rec.Code, rec.Body.String())
		}
	}
	got, ok := f.get(t, acmeOrgNS, "shop")
	if !ok || got.GetDeletionTimestamp() != nil {
		t.Fatal("the sibling Organization's Application was touched")
	}
	if dom, _, _ := unstructured.NestedString(got.Object, "spec", "parameters", "domain"); dom != "shop.acme.com" {
		t.Fatalf("the sibling Organization's Application was patched: domain=%q", dom)
	}
}

// TestOrgApplicationUpdate_CustomerUpgradesOwnApplication — the customer's
// edit lands on their own Application and not on the same-named Application
// of another Organization.
func TestOrgApplicationUpdate_CustomerUpgradesOwnApplication(t *testing.T) {
	own := makeAppCR(demoOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"})
	other := makeAppCR(acmeOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"})
	f := newOrgManageFixture(t, own, other)

	rec := f.call(t, http.MethodPut, "/api/v1/org/applications/shop?namespace="+acmeOrgNS,
		"console.demo.omani.homes", demoClaims,
		map[string]any{"parameters": map[string]any{"domain": "shop.demo.example", "replicas": float64(3)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	mine, _ := f.get(t, demoOrgNS, "shop")
	if dom, _, _ := unstructured.NestedString(mine.Object, "spec", "parameters", "domain"); dom != "shop.demo.example" {
		t.Fatalf("own Application not patched: domain=%q", dom)
	}
	theirs, _ := f.get(t, acmeOrgNS, "shop")
	if dom, _, _ := unstructured.NestedString(theirs.Object, "spec", "parameters", "domain"); dom != "shop.acme.com" {
		t.Fatalf("the sibling Organization's Application was patched: domain=%q", dom)
	}
}

// TestOrgApplicationDelete_CustomerUninstallsOwnApplication — the uninstall
// marks the caller's own Application; the answer says "uninstalling" (the
// controller's finalizer still holds it), never "deleted".
func TestOrgApplicationDelete_CustomerUninstallsOwnApplication(t *testing.T) {
	own := withAppFinalizer(makeAppCR(demoOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"}))
	other := withAppFinalizer(makeAppCR(acmeOrgNS, "shop", "1.2.3", "single-region", []string{"fsn1"}))
	f := newOrgManageFixture(t, own, other)

	rec := f.call(t, http.MethodDelete, "/api/v1/org/applications/shop?namespace="+acmeOrgNS,
		"console.demo.omani.homes", demoClaims, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "uninstalling" || resp.Deleted || resp.Namespace != demoOrgNS {
		t.Fatalf("DELETE answer = %+v, want status uninstalling, deleted false, namespace %s", resp, demoOrgNS)
	}
	if mine, ok := f.get(t, demoOrgNS, "shop"); !ok || mine.GetDeletionTimestamp() == nil {
		t.Fatal("the customer's own Application was not marked for deletion")
	}
	if theirs, ok := f.get(t, acmeOrgNS, "shop"); !ok || theirs.GetDeletionTimestamp() != nil {
		t.Fatal("the sibling Organization's Application was marked for deletion")
	}
}

// TestHandleApplicationDelete_AnswersUninstallingWhileTheFinalizerHolds — the
// Sovereign route: an accepted delete of an Application the controller still
// holds is 202 "uninstalling", deleted:false. On main the handler answered
// 200 "status: deleted", deleted:true before anything was removed.
func TestHandleApplicationDelete_AnswersUninstallingWhileTheFinalizerHolds(t *testing.T) {
	cr := withAppFinalizer(makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"fsn1"}))
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	keepWhileFinalized(client)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-delete-held")

	rec := callUserAccess(t, h, http.MethodDelete,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", nil, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "uninstalling" || resp.Deleted || len(resp.Finalizers) == 0 {
		t.Fatalf("DELETE answer = %+v, want uninstalling, deleted false, the holding finalizer named", resp)
	}
}

// TestOrgApplications_GridShowsRealStateAndAdoptsTheLegs — the Organization's
// Apps grid: a Degraded Application is "degraded" with its reason, not
// "installing"; the HelmRelease the controller rendered for it (labelled with
// the Application, named <app>-<cluster>) is its leg, not a second card, and
// gives the Application's card its Open link.
func TestOrgApplications_GridShowsRealStateAndAdoptsTheLegs(t *testing.T) {
	app := makeApplicationFull("shop", demoOrgNS, "bp-wordpress", "Degraded")
	_ = unstructured.SetNestedSlice(app.Object, []interface{}{
		map[string]interface{}{
			"type": "Ready", "status": "False", "reason": "DownstreamHelmReleaseFailed",
			"message": "exceeded quota: plan-quota",
		},
	}, "status", "conditions")
	leg := makeHRWithRelease("shop-rtz-a", demoOrgNS, "True", "bp-wordpress", "shop")
	leg.SetLabels(map[string]string{labelCatalystApp: "shop"})
	route := makeHTTPRouteWithBackend("shop", demoOrgNS, []string{"shop.demo.omani.homes"}, "shop")

	rec := orgAppsGet(t, "console.demo.omani.homes", demoClaims, []runtime.Object{app, leg, route})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			ExternalURL  string `json:"externalURL"`
			ObjectStatus *struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"objectStatus"`
		} `json:"apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Apps) != 1 {
		t.Fatalf("want ONE card (the Application; its leg adopted), got %d: %+v", len(resp.Apps), resp.Apps)
	}
	c := resp.Apps[0]
	if c.ID != "shop" || c.Status != "degraded" {
		t.Fatalf("card id/status = %q/%q, want shop/degraded", c.ID, c.Status)
	}
	if c.ObjectStatus == nil || c.ObjectStatus.Reason != "DownstreamHelmReleaseFailed" || !strings.Contains(c.ObjectStatus.Message, "plan-quota") {
		t.Fatalf("card objectStatus = %+v, want the Ready reason and message", c.ObjectStatus)
	}
	if c.ExternalURL != "https://shop.demo.omani.homes" {
		t.Fatalf("card externalURL = %q, want the Open link of its leg", c.ExternalURL)
	}
}

func TestOrgAppCardStatus(t *testing.T) {
	for phase, want := range map[string]string{
		"Ready": "installed", "Failed": "failed", "Degraded": "degraded",
		"Uninstalling": "uninstalling", "Pending": "installing", "Provisioning": "installing", "": "installing",
	} {
		if got := appmanage.CardStatus(phase); got != want {
			t.Errorf("appmanage.CardStatus(%q) = %q, want %q", phase, got, want)
		}
	}
}
