// organization_active_hot_standby_test.go — D31 active-hot-standby
// regression tests for the Organization gitops writer.
//
// The render path reads three Pod-env vars at template time:
//
//   SOVEREIGN_ENABLE_HOT_STANDBY  (true/false/empty)
//   SOVEREIGN_PRIMARY_REGION      (canonical openova.io/region label)
//   SOVEREIGN_REPLICA_REGION      (canonical openova.io/region label)
//
// When the toggle is on AND both regions are non-empty AND distinct, the
// rendered bp-wordpress-tenant HelmRelease emits a
// `values.pg.activeHotStandby.{enabled,primaryRegion,replicaRegion}`
// block — bp-wordpress-tenant chart 0.2.0+ then renders a primary +
// replica Cluster.postgresql.cnpg.io pair (templates/cnpg-cluster.yaml).
//
// ADR-0015 D2/D5 (#7067) SUPERSEDES the above: the writer no longer emits
// pg.activeHotStandby at all (bp-wordpress-tenant 0.5.0 removed it); the
// tests below lock that, whatever the legacy env toggle says.
// In every other shape (toggle off, missing region, identical regions)
// the writer falls back to the single-Cluster shape — defence-in-depth
// against malformed inputs that would otherwise break the entire
// tenant overlay reconcile via the chart's
// `validateActiveHotStandbyRegions` helper's hard `fail`.

package handler

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func d31TestRec() store.OrganizationProvisionRecord {
	return store.OrganizationProvisionRecord{
		OrganizationID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
}

// Without SOVEREIGN_ENABLE_HOT_STANDBY set the WordPress HelmRelease
// MUST NOT carry a pg.activeHotStandby block — the chart falls through
// to its values.yaml default (enabled=false → single-Cluster CNPG).
// Backward-compat gate.
func TestRenderOrganizationOverlay_HotStandby_Off_NoBlock(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wp := files["bp-wordpress-tenant.yaml"]
	if strings.Contains(wp, "activeHotStandby") {
		t.Fatalf("expected no activeHotStandby block when toggle is off:\n%s", wp)
	}
}

// ADR-0015 D2/D5 (#7067) — the HA toggle on NEVER emits the keys
// bp-wordpress-tenant 0.5.0 removed. The overlay's values become the funnel
// Application's spec.parameters verbatim; a pg.activeHotStandby.* or
// database.mode key there fails the chart's render (fail closed), so the
// funnel WordPress on every HA Sovereign would never install. Multi-region
// shape comes from the Application placement → openova.topology instead.
// Asserted on every env shape the old writer branched on.
func TestRenderOrganizationOverlay_NeverEmitsRemovedTopologyKeys_ADR0015(t *testing.T) {
	cases := []struct{ name, enable, primary, replica string }{
		{"toggle-off", "", "", ""},
		{"toggle-on-distinct-regions", "true", "hz-fsn-rtz-prod", "hz-hel-rtz-prod"},
		{"toggle-on-no-regions", "true", "", ""},
		{"toggle-on-same-region", "true", "hz-fsn-rtz-prod", "hz-fsn-rtz-prod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", tc.enable)
			t.Setenv("SOVEREIGN_PRIMARY_REGION", tc.primary)
			t.Setenv("SOVEREIGN_REPLICA_REGION", tc.replica)
			files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			wp := files["bp-wordpress-tenant.yaml"]
			if wp == "" {
				t.Fatal("no bp-wordpress-tenant.yaml rendered — the assertions below would be vacuous")
			}
			apps, err := funnelApplicationsFromOverlay(files)
			if err != nil {
				t.Fatalf("funnel apps: %v", err)
			}
			var params map[string]interface{}
			for _, a := range apps {
				if a.Blueprint == "bp-wordpress-tenant" {
					params = a.Parameters
				}
			}
			if params == nil {
				t.Fatal("funnel produced no bp-wordpress-tenant Application")
			}
			if pg, ok := params["pg"].(map[string]interface{}); ok {
				if _, bad := pg["activeHotStandby"]; bad {
					t.Fatalf("funnel WordPress parameters carry pg.activeHotStandby (removed in 0.5.0): %v", pg)
				}
			}
			if db, ok := params["database"].(map[string]interface{}); ok {
				if _, bad := db["mode"]; bad {
					t.Fatalf("funnel WordPress parameters carry database.mode (removed in 0.5.0): %v", db)
				}
			}
			if strings.Contains(wp, "activeHotStandby:") {
				t.Fatalf("overlay still renders an activeHotStandby block:\n%s", wp)
			}
		})
	}
}

// ADR-0015 D5 — the overlay no longer carries a per-Application
// continuum.yaml (#2066), even with active-hot-standby on: the funnel's
// WordPress is an Application CR, and the application-controller derives its
// Continuum from the Application's multi-region placement exactly as it does
// for a console-installed Application. One install path, one DR path.
func TestRenderOrganizationOverlay_HotStandby_On_NoOverlayContinuum_ADR0015(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "true")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "hz-hel-rtz-prod")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, ok := files["continuum.yaml"]; ok {
		t.Fatalf("continuum.yaml rendered into the overlay — the Application controller owns the Continuum now (files=%v)", fileNames(files))
	}
	if strings.Contains(files["kustomization.yaml"], "continuum.yaml") {
		t.Fatalf("kustomization.yaml references continuum.yaml:\n%s", files["kustomization.yaml"])
	}
}

// #2066 — when active-hot-standby is OFF the writer MUST NOT emit a
// Continuum CR file AND MUST NOT reference continuum.yaml in the
// kustomization. Either side of the asymmetry would cause a kustomize
// "missing resource" or stray "cluster doesn't know dr.openova.io"
// reconcile error on single-cluster tenants.
func TestRenderOrganizationOverlay_HotStandby_Off_NoContinuumCR(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	cr := files["continuum.yaml"]
	if strings.TrimSpace(cr) != "" {
		t.Errorf("continuum.yaml MUST be empty when HA is off, got:\n%s", cr)
	}
	kust := files["kustomization.yaml"]
	if strings.Contains(kust, "continuum.yaml") {
		t.Errorf("kustomization.yaml MUST NOT reference continuum.yaml when HA is off:\n%s", kust)
	}
}

// fileNames is a tiny test helper to keep diagnostic Fatalf payloads
// from dumping every overlay file when continuum.yaml is missing.
func fileNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// #5623 companion — a NON-HA overlay must not carry the declaration at all.
// Single-region tenants render no DR pair, so a promotion mechanism there would
// be a claim about a standby that does not exist.
func TestRenderOrganizationOverlay_HotStandby_Off_NoPromotionMechanism(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "false")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if wp := files["bp-wordpress-tenant.yaml"]; strings.Contains(wp, "mechanism:") {
		t.Fatalf("#5623: non-HA overlay leaked a promotion mechanism declaration:\n%s", wp)
	}
}
