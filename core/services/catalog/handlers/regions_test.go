package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestParseConfiguredRegions guards the CATALYST_CONFIGURED_REGIONS parse
// that powers GET /catalog/regions (#4525). The keys MUST pass through
// verbatim (the gitops layer consumes them as-is, e.g. `me-east-215-a`),
// trailing-comma / whitespace ghosts must be skipped (mirrors fleet.go::
// regionsFromEnv), duplicates collapse, and an empty/unset env yields a
// non-nil empty slice so the JSON renders `[]` not `null` (the frontend
// treats `[]` as "keep the static fallback").
func TestParseConfiguredRegions(t *testing.T) {
	t.Run("empty env yields empty non-nil slice", func(t *testing.T) {
		got := parseConfiguredRegions("")
		if got == nil {
			t.Fatal("got nil, want non-nil empty slice (JSON must render [] not null)")
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	t.Run("huawei keys pass through verbatim", func(t *testing.T) {
		got := parseConfiguredRegions("me-east-215-a,me-east-215-b")
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
		}
		if got[0].Key != "me-east-215-a" || got[1].Key != "me-east-215-b" {
			t.Fatalf("keys = %q,%q, want me-east-215-a,me-east-215-b", got[0].Key, got[1].Key)
		}
		// A bare key with no -rtz-prod suffix labels verbatim.
		if got[0].Label != "me-east-215-a" {
			t.Fatalf("label = %q, want me-east-215-a", got[0].Label)
		}
	})

	t.Run("whitespace and trailing-comma ghosts are skipped", func(t *testing.T) {
		got := parseConfiguredRegions("fsn1, hz-hel-rtz-prod ,")
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
		}
		if got[0].Key != "fsn1" || got[1].Key != "hz-hel-rtz-prod" {
			t.Fatalf("keys = %q,%q, want fsn1,hz-hel-rtz-prod", got[0].Key, got[1].Key)
		}
		// The -rtz-prod Catalyst cluster suffix is stripped for the label.
		if got[1].Label != "hz-hel" {
			t.Fatalf("label = %q, want hz-hel", got[1].Label)
		}
	})

	t.Run("explicit key=label pair is honored", func(t *testing.T) {
		got := parseConfiguredRegions("me-east-215-a=Muscat A,me-east-215-b=Muscat B")
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
		}
		if got[0].Key != "me-east-215-a" || got[0].Label != "Muscat A" {
			t.Fatalf("entry[0] = %+v, want {me-east-215-a, Muscat A}", got[0])
		}
	})

	t.Run("duplicate keys collapse to first", func(t *testing.T) {
		got := parseConfiguredRegions("me-east-215-a,me-east-215-a=Ignored")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 (dedup)", len(got))
		}
		if got[0].Label != "me-east-215-a" {
			t.Fatalf("label = %q, want first occurrence's derived label", got[0].Label)
		}
	})
}

// TestListRegionsHandler exercises the HTTP surface end-to-end: the env
// is read, the response is the [{key,label}] array, and the cache header
// is set. Empty env must still return `[]` with HTTP 200 (never null,
// never an error) so the picker degrades to its static fallback cleanly.
func TestListRegionsHandler(t *testing.T) {
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "me-east-215-a,me-east-215-b")
	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/catalog/regions", nil)
	h.ListRegions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q, want public, max-age=300", cc)
	}
	var regions []Region
	if err := json.Unmarshal(rec.Body.Bytes(), &regions); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if len(regions) != 2 || regions[0].Key != "me-east-215-a" || regions[1].Key != "me-east-215-b" {
		t.Fatalf("regions = %+v, want me-east-215-a + me-east-215-b", regions)
	}

	// Empty env → [] with 200, never null/500.
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "")
	rec2 := httptest.NewRecorder()
	h.ListRegions(rec2, httptest.NewRequest(http.MethodGet, "/catalog/regions", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("empty-env status = %d, want 200", rec2.Code)
	}
	if body := rec2.Body.String(); body != "[]\n" && body != "[]" {
		t.Fatalf("empty-env body = %q, want []", body)
	}
}
