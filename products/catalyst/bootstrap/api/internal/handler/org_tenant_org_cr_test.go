package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// newOrgTenantHandlerWithDynamic wires the Organization pipeline deps AND a
// fake dynamic client (via SetSovereignDepsFactory) registering the
// Organization GVR, so the #3687 Org-CR-creation step at the
// STSTenantRegistered → STSDone transition POSTs into the fake apiserver.
func newOrgTenantHandlerWithDynamic(t *testing.T) (*Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{
		Store:            tenantStore,
		GitOps:           &fakeGitOps{},
		DNS:              &fakeDNS{},
		KeycloakClients:  &fakeKCClients{},
		Events:           &fakeTenantEmitter{},
		TenantRegistry:   registry,
		OTECHFQDN:        "otech.example",
		OTECHIngressIPv4: "192.0.2.10",
		MaxRetryCount:    5,
	})

	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		organizationGVR(): "OrganizationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h, dyn
}

// TestCreateOrgTenant_MintsOrganizationCR is the #3687 binary DoD: a
// completed Organization create POSTs the canonical Organization CR so
// `kubectl get organizations -A` is ≥1. Asserts the CR exists with the
// org-controller's spec shape (slug/displayName/kind/tier/billingMode/
// owners/tenantPublic).
func TestCreateOrgTenant_MintsOrganizationCR(t *testing.T) {
	h, dyn := newOrgTenantHandlerWithDynamic(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":     "acme",
			"admin_email":   "owner@acme.test",
			"company_name":  "Acme Corp",
			"domain_mode":   "free-subdomain",
			"parent_domain": "otech.example"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: want 202 got %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// #5501 — the CR mint is a SUBMIT, not a completion: the org-controller
	// authors the boundary from this CR and has not observed it yet (the CR
	// this request just POSTed carries no status), so the record holds at the
	// highest non-terminal state. This test's subject is the mint below; the
	// terminal-state contract is owned by
	// org_create_fake_green_5501_test.go.
	if got.State == store.STSDone {
		t.Fatalf("state: a create whose boundary was never observed must not report done (#5501), lastError=%s", got.LastError)
	}
	if got.State != store.STSTenantRegistered {
		t.Fatalf("state: want %s got %s (lastError=%s)", store.STSTenantRegistered, got.State, got.LastError)
	}

	// The Organization CR must now exist — the dead-object-model fix.
	org, err := dyn.Resource(organizationGVR()).Get(context.Background(), "acme", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Organization CR `acme` not created — `kubectl get organizations -A` would be 0: %v", err)
	}
	spec, _, _ := unstructuredNestedMap(org.Object, "spec")
	if spec["slug"] != "acme" {
		t.Errorf("spec.slug: want acme got %v", spec["slug"])
	}
	if spec["displayName"] != "Acme Corp" {
		t.Errorf("spec.displayName: want 'Acme Corp' got %v", spec["displayName"])
	}
	if spec["kind"] != "customer" {
		t.Errorf("spec.kind: want customer got %v", spec["kind"])
	}
	if spec["tier"] != "org" {
		t.Errorf("spec.tier: want org got %v", spec["tier"])
	}
	if spec["billingMode"] != "real" {
		t.Errorf("spec.billingMode: want real got %v", spec["billingMode"])
	}
	// #4292: an Organization minted without an explicit plan slug must still
	// carry spec.planSlug (defaulted to "s"), never empty — so the
	// org-controller always materializes a ResourceQuota + LimitRange.
	if spec["planSlug"] != "s" {
		t.Errorf("spec.planSlug: want s (default) got %v", spec["planSlug"])
	}
	if spec["sovereignRef"] != "otech.example" {
		t.Errorf("spec.sovereignRef: want otech.example got %v", spec["sovereignRef"])
	}
	owners, ok := spec["owners"].([]interface{})
	if !ok || len(owners) != 1 {
		t.Fatalf("spec.owners: want 1 owner got %v", spec["owners"])
	}
	owner0, _ := owners[0].(map[string]interface{})
	if owner0["email"] != "owner@acme.test" || owner0["role"] != "owner" {
		t.Errorf("spec.owners[0]: %v", owner0)
	}
	tp, ok := spec["tenantPublic"].(map[string]interface{})
	if !ok || tp["parentDomain"] != "otech.example" || tp["subdomain"] != "acme" {
		t.Errorf("spec.tenantPublic: %v", spec["tenantPublic"])
	}
	// Label back-reference to the Organization projection row.
	labels := org.GetLabels()
	if labels["openova.io/source"] != "org-tenant-funnel" {
		t.Errorf("label openova.io/source: %v", labels)
	}
}

// TestCreateOrgTenant_OrganizationCR_Idempotent re-runs the pipeline against
// the same record and asserts the second create is a benign AlreadyExists
// (no error, still exactly one CR).
func TestCreateOrgTenant_OrganizationCR_Idempotent(t *testing.T) {
	h, dyn := newOrgTenantHandlerWithDynamic(t)

	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "tid-1",
		State:           store.STSTenantRegistered,
		Subdomain:       "acme",
		AdminEmail:      "owner@acme.test",
		CompanyName:     "Acme",
		OTECHFQDN:       "otech.example",
		TenantNamespace: "org-tid-1",
	}
	// First create.
	h.createOrgOrganizationCR(context.Background(), rec)
	// Second create — must not error, must not duplicate.
	h.createOrgOrganizationCR(context.Background(), rec)

	list, err := dyn.Resource(organizationGVR()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list organizations: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("want exactly 1 Organization CR after double-create, got %d", len(list.Items))
	}
}

// TestCreateOrgTenant_OrganizationCR_InvalidSlugSkipped asserts a subdomain
// that is a valid RFC-1123 label but NOT a valid Org slug (digit-leading)
// is skipped without panicking and without creating a CR.
func TestCreateOrgTenant_OrganizationCR_InvalidSlugSkipped(t *testing.T) {
	h, dyn := newOrgTenantHandlerWithDynamic(t)

	rec := store.OrganizationProvisionRecord{
		OrganizationID: "tid-2",
		State:       store.STSTenantRegistered,
		Subdomain:   "9acme", // digit-leading: valid subdomain, invalid Org slug
		AdminEmail:  "owner@acme.test",
		OTECHFQDN:   "otech.example",
	}
	h.createOrgOrganizationCR(context.Background(), rec)

	list, err := dyn.Resource(organizationGVR()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list organizations: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("want 0 Organization CRs for invalid slug, got %d", len(list.Items))
	}
}

// TestCreateOrgTenant_OrganizationCR_NoClientSkipped asserts that when no
// in-cluster dynamic client is wired (CI / out-of-cluster), the create is a
// safe no-op — the Organization pipeline still completes.
func TestCreateOrgTenant_OrganizationCR_NoClientSkipped(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "otech.example"})
	// sovereignDepsFactory not set → sovereignDepsFromEnv runs → no in-cluster
	// config in the test process → error → skip. Must not panic.
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "tid-3",
		State:       store.STSTenantRegistered,
		Subdomain:   "acme",
		AdminEmail:  "owner@acme.test",
		OTECHFQDN:   "otech.example",
	}
	h.createOrgOrganizationCR(context.Background(), rec) // no-op, no panic
}

// unstructuredNestedMap is a thin helper for reading spec out of the
// unstructured CR in assertions.
func unstructuredNestedMap(obj map[string]interface{}, key string) (map[string]interface{}, bool, error) {
	v, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	m, ok := v.(map[string]interface{})
	return m, ok, nil
}
