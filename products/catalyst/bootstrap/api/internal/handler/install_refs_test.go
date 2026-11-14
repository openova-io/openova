package handler

// ADR-0015 D4 — references, never fabrication. Every create door resolves
// Organization, Environment, Blueprint and regions against live objects
// BEFORE writing; an unknown reference is a 422 naming the field and NO
// namespace is created.

import (
	"bytes"
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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func postInstance(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := newTestRouter(h)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func namespaceNames(t *testing.T, h *Handler) []string {
	t.Helper()
	c, err := h.dynamicClientOrFallback()
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.Resource(namespacesGVR()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, n := range list.Items {
		out = append(out, n.GetName())
	}
	return out
}

func TestCreateInstance_UnknownReferences_422_NoNamespaceCreated(t *testing.T) {
	cases := []struct {
		name, body, code, field string
	}{
		{"unknown org (the old sovereign fallback)",
			`{"blueprint":"wordpress","org":"sovereign","environment":"sovereign-prod","name":"test"}`,
			"organization-not-found", "org"},
		{"unknown environment",
			`{"blueprint":"wordpress","org":"acme","environment":"acme-staging","name":"wp-x"}`,
			"environment-not-found", "environment"},
		{"environment of another org",
			`{"blueprint":"wordpress","org":"acme","environment":"beta-prod","name":"wp-x"}`,
			"environment-organization-mismatch", "environment"},
		{"unknown blueprint (the deleted bp-wordpress alias)",
			`{"blueprint":"bp-wordpress","org":"acme","environment":"acme-prod","name":"wp-x"}`,
			"blueprint-not-found", "blueprint"},
		{"re-spelled region",
			`{"blueprint":"wordpress","org":"acme","environment":"acme-prod","name":"wp-x","placement":{"regions":["me-east-215-a"]}}`,
			"region-unknown", "placement.regions[0]"},
		{"primary sentinel is not a region",
			`{"blueprint":"wordpress","org":"acme","environment":"acme-prod","name":"wp-x","placement":{"regions":["primary"]}}`,
			"region-unknown", "placement.regions[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHandlerWithEndpoint(t,
				refTestOrganization("beta"), refTestEnvironment("beta-prod", "beta", "prod"))
			h.SetCatalogClient(fakeBlueprintInCatalog("wordpress", nil, true, []string{"singleton"}))
			before := namespaceNames(t, h)
			rec := postInstance(t, h, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["code"] != tc.code || resp["field"] != tc.field {
				t.Fatalf("code/field = %v/%v, want %s/%s; body=%s", resp["code"], resp["field"], tc.code, tc.field, rec.Body.String())
			}
			after := namespaceNames(t, h)
			if len(after) != len(before) {
				t.Fatalf("a namespace was created for a rejected request: before=%v after=%v", before, after)
			}
			for _, ns := range after {
				if ns == "sovereign" {
					t.Fatal(`namespace "sovereign" exists — the API fabricated an Organization boundary`)
				}
			}
		})
	}
}

func TestCreateInstance_OrgWithoutNamespace_409NotReady_NothingCreated(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t,
		refTestOrganization("fresh"), refTestEnvironment("fresh-prod", "fresh", "prod"))
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress", nil, true, []string{"singleton"}))
	rec := postInstance(t, h, `{"blueprint":"wordpress","org":"fresh","environment":"fresh-prod","name":"wp-1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "organization-not-ready") {
		t.Fatalf("status = %d body=%s, want 409 organization-not-ready", rec.Code, rec.Body.String())
	}
	for _, ns := range namespaceNames(t, h) {
		if ns == "fresh" {
			t.Fatal("catalyst-api created the Organization's namespace — that is the org-controller's")
		}
	}
	apps, _ := dyn.Resource(ApplicationGVR()).Namespace("fresh").List(context.Background(), metav1.ListOptions{})
	if apps != nil && len(apps.Items) != 0 {
		t.Fatalf("an Application was written for an Org that is not ready: %d", len(apps.Items))
	}
}

func TestCreateInstance_ValidReferences_ApplicationCarriesExactRefs(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t, refTestEnvironment("acme-dev", "acme", "dev"))
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress", nil, true, []string{"singleton", "active-hot-standby"}))
	rec := postInstance(t, h, `{"blueprint":"wordpress","org":"acme","environment":"acme-dev","name":"wp-ha",`+
		`"topology":"active-hot-standby","placement":{"regions":["rgn-b","rgn-a"]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	got, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-ha", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	str := func(path ...string) string { v, _, _ := unstructured.NestedString(got.Object, path...); return v }
	if str("spec", "organizationRef") != "acme" || str("spec", "environmentRef") != "acme-dev" ||
		str("spec", "blueprintRef", "name") != "wordpress" || str("spec", "blueprintRef", "version") != "1.0.0" {
		t.Fatalf("refs = org %q env %q bp %q@%q", str("spec", "organizationRef"), str("spec", "environmentRef"),
			str("spec", "blueprintRef", "name"), str("spec", "blueprintRef", "version"))
	}
	regs, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "regions")
	if len(regs) != 2 || regs[0] != "rgn-b" || regs[1] != "rgn-a" {
		t.Fatalf("spec.regions = %v, want the request's [rgn-b rgn-a] verbatim", regs)
	}
}

func TestCreateInstance_OmittedEnvAndRegions_ResolvedFromObjects(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress", nil, true, []string{"singleton"}))
	rec := postInstance(t, h, `{"blueprint":"wordpress","org":"acme","name":"wp-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-1", metav1.GetOptions{})
	env, _, _ := unstructured.NestedString(got.Object, "spec", "environmentRef")
	regs, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "regions")
	// acme's only Environment of its default type, and the registry primary.
	if env != "acme-prod" || len(regs) != 1 || regs[0] != refTestRegionA {
		t.Fatalf("env=%q regions=%v, want acme-prod [%s]", env, regs, refTestRegionA)
	}
}

func TestCreateInstance_AmbiguousEnvironment_422(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, refTestEnvironment("acme-prod-2", "acme", "prod"))
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress", nil, true, []string{"singleton"}))
	rec := postInstance(t, h, `{"blueprint":"wordpress","org":"acme","name":"wp-1"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "environment-required") {
		t.Fatalf("status = %d body=%s, want 422 environment-required", rec.Code, rec.Body.String())
	}
}

func TestCreateInstance_ModeNotInPlacementSchema_422(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, refTestBlueprint("bp-solo", "single-region"))
	h.SetCatalogClient(fakeBlueprintInCatalog("bp-solo", nil, true, []string{"singleton", "active-active"}))
	rec := postInstance(t, h, `{"blueprint":"bp-solo","org":"acme","environment":"acme-prod","name":"s-1","topology":"active-active",`+
		`"placement":{"regions":["rgn-a","rgn-b"]}}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "placement-mode-not-allowed") {
		t.Fatalf("status = %d body=%s, want 422 placement-mode-not-allowed", rec.Code, rec.Body.String())
	}
}

func TestHandleApplicationInstall_UnknownEnvironment_422_NoNamespace(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-refs")
	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "nosuch",
		EnvironmentRef:  "nosuch-prod",
		Parameters:      map[string]interface{}{"domain": "shop.nosuch.test"},
		Placement:       applicationPlacement{Mode: "singleton", Regions: []string{"hz-fsn-rtz-prod"}},
	}
	rec := callUserAccess(t, h, http.MethodPost, "/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "organization-not-found") {
		t.Fatalf("status = %d body=%s, want 422 organization-not-found", rec.Code, rec.Body.String())
	}
	if _, err := client.Resource(namespacesGVR()).Get(context.Background(), "nosuch", metav1.GetOptions{}); err == nil {
		t.Fatal(`namespace "nosuch" was created for an unknown Organization`)
	}
}

func TestRefsEndpoints_ListObjectsVerbatim(t *testing.T) {
	setRefTestRegistry(t)
	platform := refTestOrganization("hw307-omani-works")
	platform.SetLabels(map[string]string{"openova.io/scope": "platform"})
	h, _, _ := newTestHandlerWithEndpoint(t, platform, refTestEnvironment("beta-prod", "beta", "prod"))
	r := chi.NewMux()
	r.Get("/api/v1/refs/organizations", h.HandleListRefOrganizations)
	r.Get("/api/v1/refs/organizations/{org}/environments", h.HandleListRefEnvironments)
	r.Get("/api/v1/refs/regions", h.HandleListRefRegions)
	get := func(path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, orgs := get("/api/v1/refs/organizations")
	if code != 200 {
		t.Fatalf("orgs status %d", code)
	}
	list := orgs["organizations"].([]any)
	if len(list) != 2 {
		t.Fatalf("organizations = %v", list)
	}
	first := list[1].(map[string]any) // sorted: acme, hw307-omani-works
	if first["name"] != "hw307-omani-works" || first["platform"] != true {
		t.Fatalf("platform org row = %v", first)
	}
	code, envs := get("/api/v1/refs/organizations/acme/environments")
	el := envs["environments"].([]any)
	if code != 200 || len(el) != 1 || el[0].(map[string]any)["name"] != "acme-prod" {
		t.Fatalf("acme environments = %d %v (beta-prod must not leak)", code, envs)
	}
	if code, _ := get("/api/v1/refs/organizations/sovereign/environments"); code != http.StatusNotFound {
		t.Fatalf("unknown org environments status = %d, want 404", code)
	}
	_, regs := get("/api/v1/refs/regions")
	rl := regs["regions"].([]any)
	if len(rl) != 2 || rl[0].(map[string]any)["id"] != refTestRegionA || rl[0].(map[string]any)["primary"] != true {
		t.Fatalf("regions = %v", rl)
	}
}

// ADR-0015 D5 — the funnel's applications are Application CRs from the same
// create function; the overlay carries only the Org substrate.
func TestFunnelApplicationsFromOverlay_ValuesBecomeParameters(t *testing.T) {
	files, err := renderOrganizationOverlay(store.OrganizationProvisionRecord{
		OrganizationID: "t-acme", Subdomain: "acme", ParentDomain: "omani.homes",
		DomainMode: store.OrganizationDomainFreeSubdomain, AdminEmail: "a@acme.test",
		OTECHFQDN: "t01.omani.works",
	}, OrganizationChartVersions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bp-newapi.yaml", "bp-wordpress-tenant.yaml", "bp-openclaw.yaml", "bp-stalwart-tenant.yaml", "bp-agenity.yaml"} {
		if strings.Contains(files["kustomization.yaml"], f) {
			t.Errorf("kustomization still lists %s as a bare HelmRelease", f)
		}
	}
	apps, err := funnelApplicationsFromOverlay(files)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]funnelApplication{}
	for _, a := range apps {
		byName[a.Name] = a
	}
	wp, ok := byName["wordpress-tenant"]
	if !ok || wp.Blueprint != "bp-wordpress-tenant" {
		t.Fatalf("wordpress-tenant funnel app = %+v (apps=%v)", wp, apps)
	}
	if len(wp.Parameters) == 0 {
		t.Fatal("the overlay's Helm values did not become Application parameters")
	}
	if len(apps) != 5 {
		t.Fatalf("funnel apps = %d, want 5", len(apps))
	}
}

func TestInstallFunnelApplications_CreatesApplicationCRs_ThroughSharedCreate(t *testing.T) {
	t.Setenv("TENANT_GITOPS_PER_ORG", "")
	var seed []runtime.Object
	for _, bp := range []string{"bp-newapi", "bp-wordpress-tenant", "bp-openclaw", "bp-stalwart-tenant", "bp-agenity"} {
		seed = append(seed, refTestBlueprint(bp))
	}
	h, _, dyn := newTestHandlerWithEndpoint(t, seed...)
	h.orgTenantDeps.GitOps = DefaultOrganizationGitOpsWriter{}
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-acme", Subdomain: "acme", ParentDomain: "omani.homes",
		DomainMode: store.OrganizationDomainFreeSubdomain, AdminEmail: "a@acme.test",
		OTECHFQDN: "t01.omani.works",
	}
	ready, err := h.installFunnelApplications(context.Background(), rec)
	if err != nil || !ready {
		t.Fatalf("installFunnelApplications = %v, %v", ready, err)
	}
	list, _ := dyn.Resource(ApplicationGVR()).Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 5 {
		t.Fatalf("Application CRs = %d, want 5", len(list.Items))
	}
	for _, a := range list.Items {
		env, _, _ := unstructured.NestedString(a.Object, "spec", "environmentRef")
		org, _, _ := unstructured.NestedString(a.Object, "spec", "organizationRef")
		if env != "acme-prod" || org != "acme" {
			t.Fatalf("%s refs = %s/%s", a.GetName(), org, env)
		}
	}
	// Idempotent re-run.
	if ready, err := h.installFunnelApplications(context.Background(), rec); err != nil || !ready {
		t.Fatalf("re-run = %v, %v", ready, err)
	}
	// An Org whose boundary is not reconciled holds the pipeline.
	rec.Subdomain = "fresh"
	if ready, _ := h.installFunnelApplications(context.Background(), rec); ready {
		t.Fatal("funnel reported ready for an Organization that does not exist")
	}
}
