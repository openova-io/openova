// org_suspend_internal_test.go — the ServiceAccount-authenticated billing-
// enforcement routes (products/chargeback DESIGN.md §9, EPIC #6867).
//
// Coverage gates, in the shape of cutover_internal_test.go:
//
//  1. no Authorization header → 401 missing-bearer, CR untouched;
//  2. TokenReview rejects the token → 502 token-review-failed;
//  3. an authenticated but foreign ServiceAccount → 403 unauthorized-sa,
//     CR untouched;
//  4. the chargeback ServiceAccount → 200, spec.suspended + reason stamped
//     on the CR, the SA username recorded as the actor; resume clears both;
//  5. the plural env override admits another ServiceAccount;
//  6. no in-cluster client → 503;
//  7. wrong method → 405.
//
// The TokenReview round-trip is mocked with the same reactor the cutover
// tests use (installTokenReviewReactor); the CR lives in a fake dynamic
// client.
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

const chargebackSAUsername = "system:serviceaccount:chargeback:chargeback"

// newInternalOrgSuspendHarness returns a Handler whose sovereign deps carry
// BOTH a fake clientset (for the TokenReview) and a fake dynamic client
// (for the CR), with the Organization "acme" already provisioned.
func newInternalOrgSuspendHarness(t *testing.T) (*Handler, *fakek8s.Clientset, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	orgStore, err := store.NewOrganizationProvisionStore(t.TempDir())
	if err != nil {
		t.Fatalf("organization store: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{Store: orgStore, OTECHFQDN: "otech.example"})
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{organizationGVR(): "OrganizationList"})
	core := fakek8s.NewSimpleClientset()
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) { return &sovereignDeps{core: core, dyn: dyn}, nil })

	rec := store.OrganizationProvisionRecord{OrganizationID: "t-acme", Subdomain: "acme", AdminEmail: "owner@acme.omani.homes", CompanyName: "Acme", DomainMode: store.OrganizationDomainFreeSubdomain, State: store.STSDone}
	if err := orgStore.Save(&rec); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrganizationCR(context.Background(), dyn, rec, "otech.example"); err != nil {
		t.Fatal(err)
	}
	return h, core, dyn
}

// callInternalOrgSuspend issues the request with an optional bearer.
func callInternalOrgSuspend(t *testing.T, h *Handler, method, id, action, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/internal/organizations/"+id+"/"+action, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	if action == "suspend" {
		h.HandleInternalSuspendOrganization(rec, req)
	} else {
		h.HandleInternalResumeOrganization(rec, req)
	}
	return rec
}

func orgSuspendedOnCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug string) (bool, string, string) {
	t.Helper()
	cr := readOrgCR(t, dyn, slug)
	s, _, _ := unstructured.NestedBool(cr.Object, "spec", "suspended")
	why, _, _ := unstructured.NestedString(cr.Object, "spec", "suspendReason")
	return s, why, cr.GetAnnotations()[suspendActorAnnotation]
}

func TestInternalOrgSuspend_MissingBearerReturns401(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, chargebackSAUsername)
	w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "", `{"reason":"overdue"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if s, _, _ := orgSuspendedOnCR(t, dyn, "acme"); s {
		t.Fatal("an unauthenticated call stamped the CR")
	}
}

func TestInternalOrgSuspend_TokenReviewRejectsReturns502(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, "") // empty username = reject
	w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "expired-token", `{"reason":"overdue"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if s, _, _ := orgSuspendedOnCR(t, dyn, "acme"); s {
		t.Fatal("a rejected token stamped the CR")
	}
}

func TestInternalOrgSuspend_WrongSAReturns403(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	// Authenticated, but some other namespace's default SA — and even the
	// cutover runner, which has its own route and no business here.
	for _, user := range []string{"system:serviceaccount:default:default", "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner"} {
		installTokenReviewReactor(t, core, user)
		w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "foreign-token", `{"reason":"overdue"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403; body=%s", user, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "unauthorized-sa") {
			t.Fatalf("%s: body = %s, want unauthorized-sa", user, w.Body.String())
		}
	}
	if s, _, actor := orgSuspendedOnCR(t, dyn, "acme"); s || actor != "" {
		t.Fatalf("a foreign ServiceAccount stamped the CR: suspended=%v actor=%q", s, actor)
	}
}

func TestInternalOrgSuspend_ChargebackSAStampsAndClearsTheCR(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, chargebackSAUsername)

	w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "projected-sa-token", `{"reason":"invoice INV-2026-00007 is 45 days overdue"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", w.Code, w.Body.String())
	}
	var out orgSuspendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if out.Slug != "acme" || !out.Suspended || !strings.Contains(out.SuspendReason, "45 days overdue") || out.Actor != chargebackSAUsername {
		t.Fatalf("suspend response = %+v", out)
	}
	s, why, actor := orgSuspendedOnCR(t, dyn, "acme")
	if !s || !strings.Contains(why, "45 days overdue") {
		t.Fatalf("spec.suspended not stamped: suspended=%v reason=%q", s, why)
	}
	if actor != chargebackSAUsername {
		t.Fatalf("actor annotation = %q, want the ServiceAccount username", actor)
	}
	// The rest of the spec survived the merge patch.
	cr := readOrgCR(t, dyn, "acme")
	if slug, _, _ := unstructured.NestedString(cr.Object, "spec", "slug"); slug != "acme" {
		t.Fatalf("merge patch must leave the spec intact: %v", cr.Object["spec"])
	}

	// Resume, no body: flag and reason cleared, actor recorded.
	w = callInternalOrgSuspend(t, h, http.MethodPost, "acme", "resume", "projected-sa-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	s, why, actor = orgSuspendedOnCR(t, dyn, "acme")
	if s || why != "" || actor != chargebackSAUsername {
		t.Fatalf("resume must clear the flag and the reason: suspended=%v reason=%q actor=%q", s, why, actor)
	}

	// The apiserver's truth for an Organization that does not exist.
	if w := callInternalOrgSuspend(t, h, http.MethodPost, "ghost", "suspend", "projected-sa-token", `{"reason":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing CR = %d %s", w.Code, w.Body.String())
	}
}

func TestInternalOrgSuspend_EnvOverrideAdmitsAnotherSA(t *testing.T) {
	t.Setenv(envInternalOrgSuspendSAUsernames, "system:serviceaccount:billing:collector, system:serviceaccount:ops:bot")
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, "system:serviceaccount:ops:bot")
	w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "ops-token", `{"reason":"operator decision"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (env override); body=%s", w.Code, w.Body.String())
	}
	if s, _, actor := orgSuspendedOnCR(t, dyn, "acme"); !s || actor != "system:serviceaccount:ops:bot" {
		t.Fatalf("override caller did not stamp: suspended=%v actor=%q", s, actor)
	}
	// The default is still admitted alongside the override.
	if !orgSuspendCallers().admits(chargebackSAUsername) {
		t.Fatal("the env override displaced the chargeback default")
	}
}

func TestInternalOrgSuspend_NoClusterClientReturns503(t *testing.T) {
	h, _, _ := newInternalOrgSuspendHarness(t)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) { return nil, io.ErrUnexpectedEOF })
	w := callInternalOrgSuspend(t, h, http.MethodPost, "acme", "suspend", "projected-sa-token", `{"reason":"x"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestInternalOrgSuspend_WrongMethodReturns405(t *testing.T) {
	h, _, _ := newInternalOrgSuspendHarness(t)
	w := callInternalOrgSuspend(t, h, http.MethodGet, "acme", "suspend", "projected-sa-token", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Allow"); got != "POST" {
		t.Fatalf("Allow header = %q, want POST", got)
	}
}

// The operator routes record the session as the actor — the audit line must
// say WHO, on both doors.
func TestOrgSuspend_OperatorRouteRecordsTheSessionActor(t *testing.T) {
	h, dyn := newOrgSuspendHarness(t)
	rec := store.OrganizationProvisionRecord{OrganizationID: "t-acme", Subdomain: "acme", AdminEmail: "owner@acme.omani.homes", CompanyName: "Acme", DomainMode: store.OrganizationDomainFreeSubdomain, State: store.STSDone}
	if err := h.orgTenantDeps.Store.Save(&rec); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrganizationCR(context.Background(), dyn, rec, "otech.example"); err != nil {
		t.Fatal(err)
	}
	w := callOrgSuspend(t, h, "acme", "suspend", `{"reason":"operator decision"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", w.Code, w.Body.String())
	}
	// No session claims in the test request: the constant actor says a
	// session did it, and it is on the CR.
	if _, _, actor := orgSuspendedOnCR(t, dyn, "acme"); actor != "operator-session" {
		t.Fatalf("actor annotation = %q, want operator-session", actor)
	}
}
