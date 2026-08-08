package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5910 / UAT row 95 — resolveAppSlugs must make the UNRESOLVABLE case visible.
//
// The pass-through itself is correct and stays: callers may hand this function
// values that are already slugs. The defect was that a value resolving to
// NOTHING was byte-identical downstream to a legitimate slug, while the returned
// slice length (and therefore the cart count) matched either way. That is how a
// purchased app reached the generator, rendered as a null-image /
// containerPort-0 Deployment, and failed the whole per-Org apply with nothing
// anywhere reporting it.
//
// These tests assert on the WARNING, because the return value is deliberately
// unchanged — the observability IS the fix. Each has a control so it cannot pass
// for the wrong reason.

func catalogStub(t *testing.T, apps []map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apps)
	}))
}

// captureLogs swaps the default slog handler for the duration of fn.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestResolveAppSlugs_UnresolvableIDIsReported_5910(t *testing.T) {
	const (
		realID      = "11111111-2222-3333-4444-555555555555"
		unresolved  = "0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"
		realSlugOut = "wordpress"
	)
	srv := catalogStub(t, []map[string]string{{"id": realID, "slug": realSlugOut}})
	defer srv.Close()

	h := &Handler{CatalogURL: srv.URL}

	// CONTROL 1 — a resolvable catalog id must NOT warn. Without this, the
	// assertion below would also pass on an implementation that warns about
	// everything, which would be noise rather than a signal.
	ctlOut := captureLogs(t, func() {
		got := h.resolveAppSlugs(context.Background(), []string{realID})
		if len(got) != 1 || got[0] != realSlugOut {
			t.Errorf("control: expected %q to resolve to %q, got %v", realID, realSlugOut, got)
		}
	})
	if strings.Contains(ctlOut, "#5910") {
		t.Errorf("control FAILED: a resolvable catalog id produced the unresolved warning:\n%s", ctlOut)
	}

	// CONTROL 2 — a value that is already a KNOWN app slug is a legitimate
	// pass-through and must NOT warn either. This is the case the fallback was
	// written for, and breaking it would be a regression.
	ctl2 := captureLogs(t, func() {
		got := h.resolveAppSlugs(context.Background(), []string{"wordpress"})
		if len(got) != 1 || got[0] != "wordpress" {
			t.Errorf("control 2: expected a known slug to pass through, got %v", got)
		}
	})
	if strings.Contains(ctl2, "#5910") {
		t.Errorf("control 2 FAILED: a legitimate known-slug pass-through was reported as unresolved:\n%s", ctl2)
	}

	// THE CASE THAT COST ROW 95 — resolves to neither a catalog id nor a known
	// app spec. Return value is unchanged (by design); the warning is the fix.
	out := captureLogs(t, func() {
		got := h.resolveAppSlugs(context.Background(), []string{unresolved})
		if len(got) != 1 || got[0] != unresolved {
			t.Errorf("expected the unresolvable id to pass through unchanged (dropping it would "+
				"trade one silent failure for another), got %v", got)
		}
	})
	if !strings.Contains(out, "#5910") || !strings.Contains(out, unresolved) {
		t.Errorf("#5910: an id resolving to NOTHING must be reported — this is the only point in the "+
			"chain where the miss is still observable. logs:\n%s", out)
	}
}

func TestResolveAppSlugs_CatalogUnreachableReportsUnknownIDs_5910(t *testing.T) {
	// Catalog down: the function returns appIDs wholesale. If those are UUIDs,
	// every one is about to render as a husk — and the count still matches.
	h := &Handler{CatalogURL: "http://127.0.0.1:1"} // nothing listening

	out := captureLogs(t, func() {
		got := h.resolveAppSlugs(context.Background(),
			[]string{"0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"})
		if len(got) != 1 {
			t.Errorf("expected wholesale pass-through to preserve length, got %v", got)
		}
	})
	if !strings.Contains(out, "#5910") {
		t.Errorf("#5910: catalog-unreachable + unknown ids must be reported, not passed through "+
			"in silence. logs:\n%s", out)
	}

	// CONTROL — with the catalog down but a KNOWN slug supplied, there is
	// nothing wrong and nothing to report.
	ctl := captureLogs(t, func() {
		_ = h.resolveAppSlugs(context.Background(), []string{"wordpress"})
	})
	if strings.Contains(ctl, "#5910") {
		t.Errorf("control FAILED: a known slug with the catalog down is fine and must not warn:\n%s", ctl)
	}
}
