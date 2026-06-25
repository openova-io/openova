package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #4293 MAJOR-3 — resolvePlanSlug silently returned "s" on ANY transient
// catalog failure, so a day-2 install for a paid (M+) Org could re-route apps
// to the wrong boundary on a momentary catalog blip. The fix splits the lookup
// so the day-2 path can DISTINGUISH a confirmed answer from an
// unreachable-catalog guess (lookupPlanSlug → reachable bool) and reads the
// authoritative spec.planSlug off the Organization CR first
// (resolveTenantPlanSlug). These tests lock the new fail-closed signal.

// catalogPlansStub stands up an httptest server serving /catalog/plans with the
// given status + body. Returns the base URL to set as Handler.CatalogURL.
func catalogPlansStub(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/plans" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLookupPlanSlug_ConfirmedHit(t *testing.T) {
	url := catalogPlansStub(t, http.StatusOK, `[{"id":"plan-m-uuid","slug":"m"},{"id":"plan-s-uuid","slug":"s"}]`)
	h := &Handler{CatalogURL: url}
	slug, reachable := h.lookupPlanSlug(context.Background(), "plan-m-uuid")
	if !reachable {
		t.Fatalf("a 200 catalog response must be reachable=true")
	}
	if slug != "m" {
		t.Errorf("slug = %q, want m", slug)
	}
}

func TestLookupPlanSlug_GenuineMissIsReachable(t *testing.T) {
	// Catalog answered, plan genuinely absent → "s" is a REAL answer (reachable).
	url := catalogPlansStub(t, http.StatusOK, `[{"id":"other","slug":"l"}]`)
	h := &Handler{CatalogURL: url}
	slug, reachable := h.lookupPlanSlug(context.Background(), "unknown-uuid")
	if !reachable {
		t.Errorf("a genuine plan-miss on a reachable catalog must be reachable=true")
	}
	if slug != "s" {
		t.Errorf("slug = %q, want s (default for a real miss)", slug)
	}
}

func TestLookupPlanSlug_TransientFailuresAreUnreachable(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		url := catalogPlansStub(t, http.StatusInternalServerError, `boom`)
		h := &Handler{CatalogURL: url}
		if slug, reachable := h.lookupPlanSlug(context.Background(), "plan-m-uuid"); reachable {
			t.Errorf("a 500 must be reachable=false (transient), got slug=%q reachable=true", slug)
		}
	})
	t.Run("decode-error", func(t *testing.T) {
		url := catalogPlansStub(t, http.StatusOK, `{not-json`)
		h := &Handler{CatalogURL: url}
		if _, reachable := h.lookupPlanSlug(context.Background(), "plan-m-uuid"); reachable {
			t.Errorf("a decode error must be reachable=false (transient)")
		}
	})
	t.Run("no-catalog-url", func(t *testing.T) {
		h := &Handler{CatalogURL: ""}
		if _, reachable := h.lookupPlanSlug(context.Background(), "plan-m-uuid"); reachable {
			t.Errorf("an empty CatalogURL must be reachable=false")
		}
	})
}

// TestResolveTenantPlanSlug_FailsClosedOnTransient is the headline MAJOR-3 lock.
// With no in-cluster env (Org CR read fails) AND a transient catalog failure,
// resolveTenantPlanSlug must return authoritative=false so the day-2 caller
// ABORTS rather than silently downgrading a paid Org to host tier.
func TestResolveTenantPlanSlug_FailsClosedOnTransient(t *testing.T) {
	clearK8sEnv(t) // Org CR read returns "not running in cluster"
	url := catalogPlansStub(t, http.StatusInternalServerError, `boom`)
	h := &Handler{CatalogURL: url}
	slug, authoritative := h.resolveTenantPlanSlug(context.Background(), "acme", "plan-m-uuid")
	if authoritative {
		t.Fatalf("catalog transient + no CR planSlug MUST be authoritative=false (fail-closed), got slug=%q authoritative=true", slug)
	}
}

// TestResolveTenantPlanSlug_CatalogFallbackAuthoritative — when the Org CR read
// fails (no in-cluster env) but the catalog is reachable, the confirmed catalog
// hit is authoritative (the fresh-prov / catalog-up day-2 path keeps working).
func TestResolveTenantPlanSlug_CatalogFallbackAuthoritative(t *testing.T) {
	clearK8sEnv(t)
	url := catalogPlansStub(t, http.StatusOK, `[{"id":"plan-m-uuid","slug":"m"}]`)
	h := &Handler{CatalogURL: url}
	slug, authoritative := h.resolveTenantPlanSlug(context.Background(), "acme", "plan-m-uuid")
	if !authoritative {
		t.Fatalf("a reachable-catalog confirmed hit must be authoritative=true")
	}
	if slug != "m" {
		t.Errorf("slug = %q, want m", slug)
	}
}
