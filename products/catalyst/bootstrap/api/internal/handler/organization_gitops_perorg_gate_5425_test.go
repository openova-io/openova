package handler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// #5425 — guard for the per-Org-GitOps gate on the LEGACY
// clusters/<fqdn>/org-tenants/<id>/ overlay writer.
//
// ── Why the assertions look the way they do ──────────────────────────
//
// The requirement is "an Organization created with per-Org GitOps enabled
// produces ZERO writes under the legacy overlay root". Asserting that by
// grepping for the literal path "org-tenants" would be worthless: renaming
// the directory would silently un-guard the defect. So these tests assert on
// the WRITE PATH itself — whether the writer performs any filesystem work at
// all — which no rename can slip past.
//
// The observation channel is CATALYST_GITOPS_TMPDIR. Both writers' first
// filesystem action, immediately after the (env-only) config load and token
// check, is:
//
//	os.MkdirTemp(envOr("CATALYST_GITOPS_TMPDIR", os.TempDir()), ...)
//
// Point that at a directory that does not exist and the call fails with
// ENOENT the instant the write path is entered — deterministically, offline,
// and regardless of uid (a chmod-based trap would be defeated by tests
// running as root, and a real repo URL would drag a network `git clone` into
// a unit test). So:
//
//	gated   → ("", nil)      the writer returned before touching the disk
//	ungated → ("", non-nil)  the writer entered the write path and tripped
//
// CATALYST_GITOPS_TOKEN is deliberately set to a non-empty dummy in every
// case. Without it WriteTenantOverlay short-circuits on its own
// "gitops token unconfigured" check and the test would pass for the wrong
// reason — it must be the GATE that stops execution, not a missing token.
//
// TestLegacyOverlayWritePathIsReachable_5425 is the non-vacuity proof: it
// exercises the identical harness with the gate OFF and asserts the write
// path IS entered. If that test ever stops failing-to-write, this file has
// stopped testing anything and the gated assertions above are hollow.

// gate5425Env pins the switch plus the observation channel. The tmpdir is a
// path under a fresh t.TempDir() that is never created, so os.MkdirTemp
// against it always fails.
func gate5425Env(t *testing.T, perOrgGitops string) {
	t.Helper()
	t.Setenv("TENANT_GITOPS_PER_ORG", perOrgGitops)
	// SOVEREIGN_FQDN is pinned set in EVERY case on purpose: this leg must
	// key off the explicit switch alone. If someone later re-introduces the
	// `SOVEREIGN_FQDN != ""` default that provisioning uses, the ungated
	// tests below start failing instead of silently stripping the per-Org
	// application stack on every Sovereign (see the gate's doc comment).
	t.Setenv("SOVEREIGN_FQDN", "hw290.omani.works")
	// Non-empty so the writer's own token guard cannot be what stops it.
	t.Setenv("CATALYST_GITOPS_TOKEN", "dummy-token-not-a-secret")
	t.Setenv("CATALYST_GITOPS_TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
}

// With per-Org GitOps owning the Organization, creating one MUST NOT write
// anything to the legacy shared overlay root. This is the defect: before
// #5425 WriteTenantOverlay ran unconditionally and committed a 9-resource
// overlay that nothing owned once the Org was gone.
func TestWriteTenantOverlay_PerOrgGitops_WritesNothing_5425(t *testing.T) {
	gate5425Env(t, "true")

	sha, err := DefaultOrganizationGitOpsWriter{}.WriteTenantOverlay(context.Background(), d31TestRec())
	if err != nil {
		t.Fatalf("gated write must be a silent no-op, got error: %v\n"+
			"a non-nil error here means the writer entered the write path "+
			"(it tripped on the unreachable CATALYST_GITOPS_TMPDIR) — i.e. the "+
			"per-Org-GitOps gate did not fire and the legacy org-tenants overlay "+
			"is being committed again (#5425)", err)
	}
	if sha != "" {
		t.Fatalf("gated write must report no commit, got sha=%q", sha)
	}
}

// Teardown is gated too, and for a non-obvious reason: DeleteTenantOverlay
// calls writeParentTenantsIndex unconditionally, which (re)creates the
// parent kustomization.yaml AND helmrepositories.yaml — materialising the
// legacy tree, with its six shared bp-* HelmRepositories, on a Sovereign
// whose invariant is that the tree stays empty.
func TestDeleteTenantOverlay_PerOrgGitops_WritesNothing_5425(t *testing.T) {
	gate5425Env(t, "true")

	sha, err := DefaultOrganizationGitOpsWriter{}.DeleteTenantOverlay(context.Background(), d31TestRec())
	if err != nil {
		t.Fatalf("gated teardown must be a silent no-op, got error: %v\n"+
			"a non-nil error here means the writer entered the write path and "+
			"would have re-materialised the legacy org-tenants parent index (#5425)", err)
	}
	if sha != "" {
		t.Fatalf("gated teardown must report no commit, got sha=%q", sha)
	}
}

// NON-VACUITY PROOF. Same harness, switch UNSET — which is the default
// posture of every deployment today, including Sovereigns. The writer MUST
// enter the write path and trip on the unreachable tmpdir. This test does
// double duty:
//
//  1. If it ever passes with a nil error, the harness has stopped observing
//     anything and the two gated assertions above are hollow.
//  2. It pins the DEFAULT. The legacy overlay is the only emitter of
//     bp-agenity / bp-keycloak / bp-wordpress-tenant / the Continuum CR, so
//     a default that gates them off would strip the per-Org application
//     stack from every Organization. That must fail loudly here, not in
//     production.
func TestLegacyOverlayWritePathIsReachable_5425(t *testing.T) {
	gate5425Env(t, "")

	for _, tc := range []struct {
		name string
		call func() (string, error)
	}{
		{"write", func() (string, error) {
			return DefaultOrganizationGitOpsWriter{}.WriteTenantOverlay(context.Background(), d31TestRec())
		}},
		{"delete", func() (string, error) {
			return DefaultOrganizationGitOpsWriter{}.DeleteTenantOverlay(context.Background(), d31TestRec())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()
			if err == nil {
				t.Fatal("ungated writer must reach the write path — the harness " +
					"is no longer observing anything, so the #5425 gate assertions " +
					"are hollow (see file header)")
			}
			// Pin the observation to the FIRST filesystem action so a future
			// refactor that moves work ahead of it cannot go unnoticed.
			if !strings.Contains(err.Error(), "mktempdir") {
				t.Fatalf("expected the write path to trip at its first filesystem "+
					"action (mktempdir), got: %v", err)
			}
		})
	}
}

// The gate is EXPLICIT opt-in on this leg: only TENANT_GITOPS_PER_ORG=true
// enables it. SOVEREIGN_FQDN must never enable it by inference the way
// provisioning's default does — see the gate's doc comment for the four
// resources that would be dropped.
func TestPerOrgGitopsEnabled_ExplicitOptInOnly_5425(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sovereignFQDN string
		switchVal     string
		want          bool
	}{
		{"unset is off even on a sovereign", "hw290.omani.works", "", false},
		{"unset is off on mothership", "", "", false},
		{"explicit true enables", "hw290.omani.works", "true", true},
		{"explicit true enables on mothership too", "", "true", true},
		{"explicit false disables", "hw290.omani.works", "false", false},
		{"case-insensitive", "", "TRUE", true},
		{"whitespace-tolerant", "", "  true  ", true},
		{"non-true reads as false", "hw290.omani.works", "yes", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_FQDN", tc.sovereignFQDN)
			t.Setenv("TENANT_GITOPS_PER_ORG", tc.switchVal)
			if got := perOrgGitopsEnabled(); got != tc.want {
				t.Fatalf("perOrgGitopsEnabled() = %v, want %v (SOVEREIGN_FQDN=%q TENANT_GITOPS_PER_ORG=%q)",
					got, tc.want, tc.sovereignFQDN, tc.switchVal)
			}
		})
	}
}

// The gate must not change what the legacy path RENDERS. Ungated, the
// overlay inventory is still the full base-create set — gating is a routing
// decision, never a silent drop of resources.
func TestLegacyOverlayInventoryUnchanged_5425(t *testing.T) {
	gate5425Env(t, "")

	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"kustomization.yaml",
		"namespace.yaml",
		"certificate.yaml",
		"bp-agenity.yaml",
		"bp-keycloak.yaml",
		"bp-newapi.yaml",
		"bp-openclaw.yaml",
		"bp-stalwart-tenant.yaml",
		"bp-wordpress-tenant.yaml",
	} {
		if strings.TrimSpace(files[want]) == "" {
			t.Errorf("legacy overlay lost %s — the #5425 gate must route, not drop", want)
		}
	}
}

var _ OrganizationGitOpsWriter = DefaultOrganizationGitOpsWriter{}

var _ = store.OrganizationProvisionRecord{}
