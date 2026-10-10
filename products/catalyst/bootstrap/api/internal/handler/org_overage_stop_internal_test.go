// org_overage_stop_internal_test.go — the ServiceAccount-authenticated
// spend-limit stop routes (products/chargeback DESIGN.md §22.11, #6971):
//
//  1. no bearer → 401, CR untouched; a foreign ServiceAccount → 403;
//  2. the chargeback ServiceAccount → 200, spec.overageStoppedUntil (UTC) +
//     spec.overageStopReason stamped, the actor annotated, the rest of the
//     spec intact; resume REMOVES both fields;
//  3. a missing or unparseable until → 400, CR untouched;
//  4. an Organization that does not exist → 404; no client → 503; GET → 405.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func callInternalOverageStop(t *testing.T, h *Handler, method, id, action, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/internal/organizations/"+id+"/"+action, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	if action == "overage-stop" {
		h.HandleInternalOverageStopOrganization(rec, req)
	} else {
		h.HandleInternalOverageResumeOrganization(rec, req)
	}
	return rec
}

func overageStopOnCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug string) (until string, untilFound bool, reason string, reasonFound bool, actor string) {
	t.Helper()
	cr := readOrgCR(t, dyn, slug)
	until, untilFound, _ = unstructured.NestedString(cr.Object, "spec", "overageStoppedUntil")
	reason, reasonFound, _ = unstructured.NestedString(cr.Object, "spec", "overageStopReason")
	return until, untilFound, reason, reasonFound, cr.GetAnnotations()[overageStopActorAnnotation]
}

func TestInternalOverageStop_Auth(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, chargebackSAUsername)
	if w := callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-stop", "", `{"until":"2026-11-01T00:00:00Z"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	installTokenReviewReactor(t, core, "system:serviceaccount:default:default")
	w := callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-stop", "foreign", `{"until":"2026-11-01T00:00:00Z"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "unauthorized-sa") {
		t.Fatalf("foreign SA = %d %s, want 403 unauthorized-sa", w.Code, w.Body.String())
	}
	if _, found, _, _, _ := overageStopOnCR(t, dyn, "acme"); found {
		t.Fatal("an unauthorised call stamped the CR")
	}
}

func TestInternalOverageStop_ChargebackSAStampsAndClears(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, chargebackSAUsername)

	// An offset instant is normalised to UTC on the CR.
	w := callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-stop", "projected-sa-token",
		`{"until":"2026-11-01T04:00:00+04:00","reason":"spend limit 3.000 OMR reached for 2026-10"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("overage-stop = %d %s", w.Code, w.Body.String())
	}
	var out orgOverageStopResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if out.Slug != "acme" || out.OverageStoppedUntil != "2026-11-01T00:00:00Z" || out.Actor != chargebackSAUsername {
		t.Fatalf("response = %+v", out)
	}
	until, _, reason, _, actor := overageStopOnCR(t, dyn, "acme")
	if until != "2026-11-01T00:00:00Z" || !strings.Contains(reason, "3.000 OMR") || actor != chargebackSAUsername {
		t.Fatalf("CR: until=%q reason=%q actor=%q", until, reason, actor)
	}
	if slug, _, _ := unstructured.NestedString(readOrgCR(t, dyn, "acme").Object, "spec", "slug"); slug != "acme" {
		t.Fatal("the merge patch must leave the rest of the spec intact")
	}

	// Resume removes both fields (null in the merge patch), not "".
	w = callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-resume", "projected-sa-token", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("overage-resume = %d %s", w.Code, w.Body.String())
	}
	if _, uf, _, rf, actor := overageStopOnCR(t, dyn, "acme"); uf || rf || actor != chargebackSAUsername {
		t.Fatalf("resume must remove the fields: until present=%v reason present=%v actor=%q", uf, rf, actor)
	}
	if !strings.Contains(w.Body.String(), `"overage_stopped_until":""`) {
		t.Fatalf("resume body = %s, want an empty overage_stopped_until", w.Body.String())
	}

	if w := callInternalOverageStop(t, h, http.MethodPost, "ghost", "overage-stop", "projected-sa-token", `{"until":"2026-11-01T00:00:00Z"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing CR = %d %s", w.Code, w.Body.String())
	}
}

func TestInternalOverageStop_BadUntilReturns400(t *testing.T) {
	h, core, dyn := newInternalOrgSuspendHarness(t)
	installTokenReviewReactor(t, core, chargebackSAUsername)
	for _, body := range []string{`{}`, `{"until":"next month"}`, `{"until":"2026-11-01"}`, `not json`} {
		w := callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-stop", "projected-sa-token", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if _, found, _, _, _ := overageStopOnCR(t, dyn, "acme"); found {
		t.Fatal("a refused stop stamped the CR")
	}
}

func TestInternalOverageStop_NoClientAndWrongMethod(t *testing.T) {
	h, _, _ := newInternalOrgSuspendHarness(t)
	if w := callInternalOverageStop(t, h, http.MethodGet, "acme", "overage-stop", "t", ""); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatalf("GET = %d Allow=%q, want 405 POST", w.Code, w.Header().Get("Allow"))
	}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) { return nil, io.ErrUnexpectedEOF })
	if w := callInternalOverageStop(t, h, http.MethodPost, "acme", "overage-stop", "t", `{"until":"2026-11-01T00:00:00Z"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no client = %d, want 503", w.Code)
	}
}
