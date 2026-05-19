package handlers

// Tests for the route registration table in routes.go. Focused on the
// `POST /billing/purchase` alias added by TBD-C15 (#1750) — we don't
// re-exercise the full Checkout business logic here (that's covered by
// checkout_test.go) but assert that the alias resolves to the same
// handler shape, so the catalyst-api proxy on console.<sov-fqdn>
// stops 404'ing during the marketplace customer-journey re-walk.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutes_PurchaseAliasResolves — the alias MUST resolve to a
// registered handler. We don't care about the response body here; only
// that the mux does not 404. A status >= 400 is fine (no body / no
// auth context) — what is NOT fine is `404 page not found` (which is
// the symptom #1750 was filed for).
func TestRoutes_PurchaseAliasResolves(t *testing.T) {
	h := &Handler{}
	mux := h.Routes()

	req := httptest.NewRequest(http.MethodPost, "/billing/purchase", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("/billing/purchase MUST be registered (TBD-C15 #1750); got 404")
	}
	// We expect SOME non-404 — typically 500 because Handler{} has nil
	// DB / catalog deps; that's fine, the route exists and dispatches.
}

// TestRoutes_CheckoutCanonicalStillWorks — the canonical
// `/billing/checkout` route MUST keep resolving to the same handler.
// Guards against an accidental rename / removal.
func TestRoutes_CheckoutCanonicalStillWorks(t *testing.T) {
	h := &Handler{}
	mux := h.Routes()

	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("/billing/checkout MUST remain registered; got 404")
	}
}
