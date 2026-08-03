// org_tenant_org_cr_delete_test.go — #5426 guard. DELETE
// /api/v1/organizations/{id} MUST delete the canonical Organization CR:
// that delete is the ONLY trigger for the org-controller finalizer
// cascade (publishTenantDeleted → teardownPerOrgFlux →
// teardownTenantNetworking → iac-bootstrap → per-org-realm), which is
// the only complete teardown of an Organization's namespace / vCluster /
// Keycloak realm / per-Org Flux sources / gateway listeners. Before the
// fix the handler reaped the GitOps overlay + registry + DNS and returned
// 204 while the CR kept an EMPTY deletionTimestamp — observed live on
// hw292 (r17probe: listeners + GitRepository + cert all survived the
// DELETE) and hw290 (gamma-corp: 5 orphaned HelmReleases with no owning
// CR, un-reapable because a finalizer cannot run on a CR that no longer
// exists).
//
// Also guards the #4459/R17 ordering: the overlay reap must commit
// BEFORE the CR delete, otherwise Flux recreates the namespace the
// cascade just tore down (observed at T+5m07s on hw290).
package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orderRecordingGitOps wraps fakeGitOps and appends to a shared step log
// on every overlay delete, so tests can assert the reap-before-CR-delete
// order against the dynamic client's reactor writing to the same log.
type orderRecordingGitOps struct {
	fakeGitOps
	mu    *sync.Mutex
	steps *[]string
}

func (g *orderRecordingGitOps) DeleteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error) {
	g.mu.Lock()
	*g.steps = append(*g.steps, "overlay-reap")
	g.mu.Unlock()
	return g.fakeGitOps.DeleteTenantOverlay(ctx, rec)
}

// newOrgDeleteCascadeHarness wires the Organization pipeline deps + a fake
// dynamic client registering the Organization GVR (mirroring
// newOrgTenantHandlerWithDynamic) plus the shared ordering log.
func newOrgDeleteCascadeHarness(t *testing.T) (*Handler, *dynamicfake.FakeDynamicClient, *sync.Mutex, *[]string) {
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
	var mu sync.Mutex
	steps := []string{}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{
		Store:            tenantStore,
		GitOps:           &orderRecordingGitOps{mu: &mu, steps: &steps},
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
	dyn.PrependReactor("delete", "organizations", func(action k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		steps = append(steps, "cr-delete")
		mu.Unlock()
		return false, nil, nil // fall through to the default delete
	})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h, dyn, &mu, &steps
}

// seedOrgRecordAndCR seeds a converged Organization directly: provision
// record in the store + the canonical Organization CR in the fake
// apiserver — the state a real create leaves behind (the mint contract
// itself is owned by TestCreateOrgTenant_MintsOrganizationCR). Seeding
// directly keeps these tests off the create pipeline's polling waits.
func seedOrgRecordAndCR(t *testing.T, h *Handler, dyn *dynamicfake.FakeDynamicClient, subdomain string) store.OrganizationProvisionRecord {
	t.Helper()
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-" + subdomain,
		Subdomain:      subdomain,
		AdminEmail:     "owner@" + subdomain + ".omani.homes",
		CompanyName:    "Probe Corp",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		State:          store.STSDone,
	}
	if err := h.orgTenantDeps.Store.Save(&rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if err := ensureOrganizationCR(context.Background(), dyn, rec, "otech.example"); err != nil {
		t.Fatalf("seed Organization CR: %v", err)
	}
	return rec
}

func deleteOrgViaHandler(t *testing.T, h *Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/organizations/"+id, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleDeleteOrganization(w, req)
	return w
}

// TestDeleteOrganization_DeletesOrganizationCR is the #5426 binary proof:
// the console DELETE path issues the apiserver delete on the Organization
// CR. On a real cluster that delete stamps deletionTimestamp, which is
// exactly what fires the `orgs.openova.io/tenant-networking` finalizer
// cascade in the org-controller — the r17probe walk showed the identical
// object fully reaped in ~2s once the CR delete was issued (kubectl-side),
// and untouched (deletionTimestamp empty) when it was not.
func TestDeleteOrganization_DeletesOrganizationCR(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)

	rec := seedOrgRecordAndCR(t, h, dyn, "r17probe")

	// Precondition: the CR exists (the state the #3687 create mint leaves).
	if _, err := dyn.Resource(organizationGVR()).Get(context.Background(), "r17probe", metav1.GetOptions{}); err != nil {
		t.Fatalf("precondition: Organization CR `r17probe` missing after seed: %v", err)
	}

	delW := deleteOrgViaHandler(t, h, rec.OrganizationID)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204 got %d body=%s", delW.Code, delW.Body.String())
	}

	// The CR must be GONE — i.e. the handler issued the apiserver delete
	// that triggers the finalizer cascade. Before the fix this Get
	// succeeded with an empty deletionTimestamp (the hw292 r17probe state).
	_, err := dyn.Resource(organizationGVR()).Get(context.Background(), "r17probe", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("Organization CR `r17probe` survived DELETE — the finalizer cascade was never triggered (#5426)")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound after delete, got: %v", err)
	}
}

// TestDeleteOrganization_OverlayReapBeforeCRDelete guards the #4459/R17
// order: the GitOps source reap must land BEFORE the CR delete, or Flux
// recreates the namespace the cascade tears down (hw290, T+5m07s).
func TestDeleteOrganization_OverlayReapBeforeCRDelete(t *testing.T) {
	h, dyn, mu, steps := newOrgDeleteCascadeHarness(t)

	rec := seedOrgRecordAndCR(t, h, dyn, "orderprobe")

	delW := deleteOrgViaHandler(t, h, rec.OrganizationID)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204 got %d", delW.Code)
	}

	mu.Lock()
	got := append([]string(nil), *steps...)
	mu.Unlock()
	want := []string{"overlay-reap", "cr-delete"}
	if len(got) != len(want) {
		t.Fatalf("teardown steps: want %v got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("teardown order: want %v got %v — CR delete before the source reap is the R17 recreate race", want, got)
		}
	}
}

// TestDeleteOrganization_CRAlreadyGoneIsIdempotent — a kubectl-side delete
// (or a prior DELETE run) already removed the CR; the handler must treat
// NotFound as success and still return 204 so the remaining teardown steps
// (registry, DNS, audit row) complete.
func TestDeleteOrganization_CRAlreadyGoneIsIdempotent(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)

	rec := seedOrgRecordAndCR(t, h, dyn, "goneprobe")

	if err := dyn.Resource(organizationGVR()).Delete(context.Background(), "goneprobe", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("pre-delete CR: %v", err)
	}

	delW := deleteOrgViaHandler(t, h, rec.OrganizationID)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete with CR already gone: want 204 got %d body=%s", delW.Code, delW.Body.String())
	}
}
