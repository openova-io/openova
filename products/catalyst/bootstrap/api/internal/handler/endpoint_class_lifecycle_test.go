package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Contract C1 — POST /catalyst/v1/apps/instances refuses a platform component
// for a caller who is not a sovereign-admin (403 class-not-installable) and a
// deprecated Blueprint for everyone (409 blueprint-deprecated), before any
// object is written.

// classLifecycleCatalog is the catalogue of one Blueprint "wordpress" whose
// object says the given class and lifecycle.
func classLifecycleCatalog(class, lifecycle string) *fakeCatalogClient {
	c := fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton"})
	for _, bp := range c.byKey {
		spec := bp.Raw["spec"].(map[string]interface{})
		spec["class"], spec["lifecycle"] = class, lifecycle
	}
	return c
}

// createWordpress posts one create; claims nil means an Org-scoped session of
// the Organization acme (tier org-admin, not a sovereign-admin).
func createWordpress(t *testing.T, class, lifecycle string, claims *testClaimsSpec) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	h, dyn := newTestHandlerWithEndpoint(t)
	h.tenantRegistry = orgRegistry(t, orgConsoleHost4937, "acme")
	h.SetCatalogClient(classLifecycleCatalog(class, lifecycle))
	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-c1","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if claims == nil {
		req = withOrgConsole(req, orgConsoleHost4937, "acme")
	} else {
		req.Header.Set("X-Forwarded-Host", "console.t01.omani.works")
		req = withTestClaims(req, claims)
	}
	rec := httptest.NewRecorder()
	newTestRouter(h).ServeHTTP(rec, req)
	_, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-c1", metav1.GetOptions{})
	return rec, err == nil
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if c, ok := b["code"].(string); ok {
		return c
	}
	if e, ok := b["error"].(map[string]interface{}); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func TestCreateInstance_PlatformComponent_403ClassNotInstallable(t *testing.T) {
	rec, created := createWordpress(t, "platform-component", "experimental", nil)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "class-not-installable" {
		t.Fatalf("want 403 class-not-installable, got %d body=%s", rec.Code, rec.Body.String())
	}
	if created {
		t.Fatal("a refused create must write no Application")
	}
}

func TestCreateInstance_PlatformComponent_SovereignAdminInstalls(t *testing.T) {
	rec, created := createWordpress(t, "platform-component", "experimental", &testClaimsSpec{Email: "admin@example.com", Tier: "admin", Org: "acme"})
	if rec.Code != http.StatusCreated || !created {
		t.Fatalf("a sovereign-admin installs a platform component: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateInstance_Deprecated_409BlueprintDeprecated(t *testing.T) {
	rec, created := createWordpress(t, "application", "deprecated", &testClaimsSpec{Email: "admin@example.com", Tier: "admin", Org: "acme"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "blueprint-deprecated" {
		t.Fatalf("want 409 blueprint-deprecated, got %d body=%s", rec.Code, rec.Body.String())
	}
	if created {
		t.Fatal("a refused create must write no Application")
	}
}

func TestCreateInstance_ExperimentalApplication_OrgScopedInstalls(t *testing.T) {
	rec, created := createWordpress(t, "application", "experimental", nil)
	if rec.Code != http.StatusCreated || !created {
		t.Fatalf("an experimental application is installable: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}
