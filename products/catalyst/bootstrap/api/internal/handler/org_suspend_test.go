// org_suspend_test.go — the billing-enforcement routes (products/chargeback
// DESIGN.md §9.7) stamp spec.suspended on the canonical Organization CR and
// answer with the apiserver's truth: 200 with the stamped state, 404 for an
// Organization that does not exist, 503 without an in-cluster client.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func newOrgSuspendHarness(t *testing.T) (*Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{Store: tenantStore, OTECHFQDN: "otech.example"})
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{organizationGVR(): "OrganizationList"})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) { return &sovereignDeps{dyn: dyn}, nil })
	return h, dyn
}

func callOrgSuspend(t *testing.T, h *Handler, id, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations/"+id+"/"+action, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	if action == "suspend" {
		h.HandleSuspendOrganization(rec, req)
	} else {
		h.HandleResumeOrganization(rec, req)
	}
	return rec
}

func TestOrgSuspend_StampsAndClearsSpecSuspendedOnTheCR(t *testing.T) {
	h, dyn := newOrgSuspendHarness(t)
	rec := store.OrganizationProvisionRecord{OrganizationID: "t-acme", Subdomain: "acme", AdminEmail: "owner@acme.omani.homes", CompanyName: "Acme", DomainMode: store.OrganizationDomainFreeSubdomain, State: store.STSDone}
	if err := h.orgTenantDeps.Store.Save(&rec); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrganizationCR(context.Background(), dyn, rec, "otech.example"); err != nil {
		t.Fatal(err)
	}
	// Suspend by the record id — the slug is resolved from the record.
	w := callOrgSuspend(t, h, "t-acme", "suspend", `{"reason":"invoice INV-2026-00007 is 45 days overdue"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", w.Code, w.Body.String())
	}
	var out orgSuspendResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Slug != "acme" || !out.Suspended || !strings.Contains(out.SuspendReason, "45 days overdue") {
		t.Fatalf("suspend response = %+v", out)
	}
	cr := readOrgCR(t, dyn, "acme")
	if s, _, _ := unstructured.NestedBool(cr.Object, "spec", "suspended"); !s {
		t.Fatalf("spec.suspended not stamped: %v", cr.Object["spec"])
	}
	// The rest of the spec survived the merge patch.
	if slug, _, _ := unstructured.NestedString(cr.Object, "spec", "slug"); slug != "acme" {
		t.Fatalf("merge patch must leave the spec intact: %v", cr.Object["spec"])
	}
	// Idempotent.
	if w := callOrgSuspend(t, h, "acme", "suspend", `{"reason":"still overdue"}`); w.Code != http.StatusOK {
		t.Fatalf("re-suspend = %d", w.Code)
	}
	// Resume by slug, with no body.
	w = callOrgSuspend(t, h, "acme", "resume", "")
	if w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	cr = readOrgCR(t, dyn, "acme")
	s, _, _ := unstructured.NestedBool(cr.Object, "spec", "suspended")
	why, _, _ := unstructured.NestedString(cr.Object, "spec", "suspendReason")
	if s || why != "" || out.Suspended {
		t.Fatalf("resume must clear the flag and the reason: suspended=%v reason=%q out=%+v", s, why, out)
	}
}

func TestOrgSuspend_AnswersTheApiserversTruth(t *testing.T) {
	h, _ := newOrgSuspendHarness(t)
	// No such Organization: 404, never a 200 over nothing.
	if w := callOrgSuspend(t, h, "ghost", "suspend", `{"reason":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing CR = %d %s", w.Code, w.Body.String())
	}
	// An id that is not a slug: 404 too.
	if w := callOrgSuspend(t, h, "9abc", "suspend", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("bad slug = %d", w.Code)
	}
	// A malformed body is refused before the apiserver is touched.
	if w := callOrgSuspend(t, h, "acme", "suspend", `{"reason":`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d", w.Code)
	}
	// No in-cluster client: 503, named.
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) { return nil, io.ErrUnexpectedEOF })
	if w := callOrgSuspend(t, h, "acme", "suspend", `{"reason":"x"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no client = %d %s", w.Code, w.Body.String())
	}
}

func readOrgCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug string) *unstructured.Unstructured {
	t.Helper()
	obj, err := dyn.Resource(organizationGVR()).Get(context.Background(), slug, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read CR %s: %v", slug, err)
	}
	return obj
}
