// purge_test.go — regression sentinel for issue #392.
//
// The bug: purge.go filtered Hetzner resources by `catalyst-deployment-id=<id>`
// while OpenTofu (`infra/hetzner/main.tf`) emits `catalyst.openova.io/sovereign=<fqdn>`.
// The mismatch made `wipe.go`'s force-purge silently no-op for every failed
// deployment since the bug landed.
//
// These tests pin both halves of the contract: the label key constant and
// the wire-format produced by FilterByLabel. If either side drifts (Tofu
// stops emitting the canonical label, or purge.go stops looking for it),
// the test fails. The OpenTofu side is asserted by reading the canonical
// module file directly so a single PR that desyncs the two sides is
// caught here, not in production.
package hetzner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPurgeLabelKey_MatchesTofuEmit(t *testing.T) {
	// The label key the package filters by.
	if PurgeLabelKey != "catalyst.openova.io/sovereign" {
		t.Fatalf("PurgeLabelKey drifted from contract: got %q, want %q",
			PurgeLabelKey, "catalyst.openova.io/sovereign")
	}

	// And: the OpenTofu module at infra/hetzner/main.tf MUST emit the same
	// key on every taggable resource. Walk up to the repo root, then read
	// the module file and assert the constant appears.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root not found (test running outside repo? %v)", err)
	}
	tfPath := filepath.Join(repoRoot, "infra", "hetzner", "main.tf")
	bytes, err := os.ReadFile(tfPath)
	if err != nil {
		t.Skipf("Tofu module not readable at %s: %v", tfPath, err)
	}
	if !strings.Contains(string(bytes), `"`+PurgeLabelKey+`"`) {
		t.Fatalf("Tofu module %s does not emit label %q — purge.go and Tofu have drifted",
			tfPath, PurgeLabelKey)
	}
}

func TestFilterByLabel_WireFormat(t *testing.T) {
	got := FilterByLabel("catalyst.openova.io/sovereign", "omantel.omani.works")
	want := "catalyst.openova.io/sovereign=omantel.omani.works"
	if got != want {
		t.Fatalf("FilterByLabel wire format drift: got %q, want %q", got, want)
	}
}

func TestPurge_RejectsEmptyToken(t *testing.T) {
	_, err := Purge(context.Background(), "", "omantel.omani.works", nil)
	if err == nil {
		t.Fatal("expected error on empty token, got nil")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestPurge_RejectsEmptySovereignFQDN(t *testing.T) {
	_, err := Purge(context.Background(), "fake-token", "", nil)
	if err == nil {
		t.Fatal("expected error on empty sovereign fqdn, got nil")
	}
	// The error message names the new parameter so callers don't get
	// misled by stale "deployment id" wording.
	if !strings.Contains(err.Error(), "sovereign") {
		t.Fatalf("expected sovereign-fqdn error, got %v", err)
	}
}

// TestFilterByLabel_PreservesDotsInFQDN_OmantelBiz pins the live wire
// format for the otech133 / omantel.biz incident (Fix #120, Fix #117
// secondary). The bug class: somewhere along the wipe path, the
// dash-converted workdir form (`omantel-biz`) substitutes for the FQDN
// dot form (`omantel.biz`). The Hetzner API then List-returns 0 matches
// because tofu stamps `omantel.biz` (with dot) on every resource. Wipe
// reports "tofuDestroyed:false; 0 orphans purged" and the next provision
// collides with surviving infra.
//
// This test pins the exact selector value Purge would emit for the
// production FQDN that experienced the bug. Any future refactor that
// substitutes NamePrefixForSovereign() (dash form) into the
// label-selector path fails this test in CI before it reaches Hetzner.
func TestFilterByLabel_PreservesDotsInFQDN_OmantelBiz(t *testing.T) {
	got := FilterByLabel(PurgeLabelKey, "omantel.biz")
	want := "catalyst.openova.io/sovereign=omantel.biz"
	if got != want {
		t.Fatalf("Fix #120 regression — selector for omantel.biz drifted: got %q, want %q "+
			"(if got value contains dashes instead of dots, the dash-converted workdir name "+
			"leaked into the label-selector path; orphan-purge would silently no-op against "+
			"Hetzner because tofu emits the dot form)", got, want)
	}
}

// TestPurge_RejectsDashConvertedFQDN is the runtime guard against the
// regression vector caught by Fix #120. validateSovereignFQDNForPurge
// refuses any input that lacks a dot, on the principle that every
// legitimate Sovereign FQDN is fully-qualified (omantel.biz,
// acme.omani.works, tenant.openova.io). A dotless string is necessarily
// the dash-converted workdir name (`omantel-biz`) leaking across a seam
// — and querying Hetzner with that selector returns 0 matches every
// time, since the OpenTofu module stamps the dot form. Refuse loudly so
// the wipe handler surfaces a clear error in the SSE log instead of
// silently reporting "0 resources purged" while ghost servers survive.
func TestPurge_RejectsDashConvertedFQDN(t *testing.T) {
	cases := []struct {
		name string
		fqdn string
	}{
		{"omantel-biz workdir form leaked from sovereignName()", "omantel-biz"},
		{"acme-omani-works workdir form leaked from deploymentSovereignName()", "acme-omani-works"},
		{"single label without dot", "omantel"},
		{"all-dashes single segment", "catalyst-otech133-omantel-biz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Purge(context.Background(), "fake-token", tc.fqdn, nil)
			if err == nil {
				t.Fatalf("Fix #120 — Purge accepted dotless fqdn %q; expected refusal "+
					"to prevent silent no-op orphan sweep", tc.fqdn)
			}
			if !strings.Contains(err.Error(), "fully-qualified") {
				t.Fatalf("expected fully-qualified-domain error, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.fqdn) {
				t.Errorf("error should name the offending value %q for operator clarity, got %v", tc.fqdn, err)
			}
		})
	}
}

// TestPurge_AcceptsCanonicalFQDN_OmantelBiz proves the validation guard
// does NOT reject the production FQDN that triggered Fix #120. We can't
// run the full Purge against a fake Hetzner here (that's covered by
// purge_e2e_test.go) but we can run it against an unreachable host and
// assert the rejection happens at the URL-issue layer, NOT at the
// FQDN-validation layer (which would surface "fully-qualified" in the
// error). If a future change accidentally tightens the validator to
// reject `.biz` (or any other valid TLD), this test fails.
func TestPurge_AcceptsCanonicalFQDN_OmantelBiz(t *testing.T) {
	cases := []string{
		"omantel.biz",
		"acme.omani.works",
		"tenant.openova.io",
		"otech133.omani.works",
		"a.b", // minimal valid form: two labels with one dot
	}
	for _, fqdn := range cases {
		t.Run(fqdn, func(t *testing.T) {
			err := validateSovereignFQDNForPurge(fqdn)
			if err != nil {
				t.Fatalf("Fix #120 — validator rejected canonical FQDN %q: %v", fqdn, err)
			}
		})
	}
}

// TestPurgeSelectorContract_TofuValueRoundTrip cross-checks the value
// half of the purge<->tofu contract. The label KEY is pinned by
// TestPurgeLabelKey_MatchesTofuEmit; this test pins the VALUE shape:
// tofu's `var.sovereign_fqdn` is consumed verbatim (no dot-to-dash
// conversion) as the label value (see infra/hetzner/main.tf, every
// `labels = { "catalyst.openova.io/sovereign" = var.sovereign_fqdn }`
// block). Therefore the FilterByLabel value MUST also be the verbatim
// FQDN. If a future refactor substitutes `NamePrefixForSovereign(fqdn)`
// (which ReplaceAll's dots with dashes) for the value, this test fails.
func TestPurgeSelectorContract_TofuValueRoundTrip(t *testing.T) {
	const fqdn = "omantel.biz"
	selector := FilterByLabel(PurgeLabelKey, fqdn)

	// The selector MUST be exactly the dot form. If it contains the
	// dash-converted workdir name, the regression vector caught by
	// Fix #120 has re-entered the codebase.
	if strings.Contains(selector, NamePrefixForSovereign(fqdn)) {
		t.Fatalf("Fix #120 regression — selector %q contains the dash-converted workdir "+
			"name %q; tofu stamps the dot form on every resource, so the dashed "+
			"selector silently matches 0 resources and the orphan purge no-ops",
			selector, NamePrefixForSovereign(fqdn))
	}
	if !strings.Contains(selector, fqdn) {
		t.Fatalf("selector %q must contain the FQDN %q verbatim (tofu stamps the dot form)",
			selector, fqdn)
	}
}

// findRepoRoot walks upward from the current package directory looking for
// a directory containing both `infra/` and `products/`. Used so the
// regression test runs from anywhere `go test ./...` is invoked.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "infra", "hetzner", "main.tf")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "products", "catalyst")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
