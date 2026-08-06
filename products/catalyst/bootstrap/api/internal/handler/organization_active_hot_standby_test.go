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

// With the canonical happy-path env triple, the HelmRelease MUST carry
// the chart's pg.activeHotStandby.{enabled,primaryRegion,replicaRegion}
// block so bp-wordpress-tenant chart 0.2.0+ renders the primary +
// replica Cluster CR pair (DoD D31).
func TestRenderOrganizationOverlay_HotStandby_On_EmitsBlock(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "true")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "hz-hel-rtz-prod")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wp := files["bp-wordpress-tenant.yaml"]
	for _, want := range []string{
		"pg:",
		"activeHotStandby:",
		"enabled: true",
		"primaryRegion: hz-fsn-rtz-prod",
		"replicaRegion: hz-hel-rtz-prod",
	} {
		if !strings.Contains(wp, want) {
			t.Errorf("missing %q in HA WordPress overlay:\n%s", want, wp)
		}
	}
}

// #2066 — when active-hot-standby is on AND both regions are distinct,
// renderOrganizationOverlay MUST emit a per-Application Continuum CR
// alongside the bp-wordpress-tenant HelmRelease. Without it the
// bp-continuum controller (Refs #2065) has nothing to reconcile and
// region-kill leaves the tenant unfailed-over. CRD shape per
// products/catalyst/chart/crds/continuum.yaml.
func TestRenderOrganizationOverlay_HotStandby_On_EmitsContinuumCR(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "true")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "hz-hel-rtz-prod")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	cr, ok := files["continuum.yaml"]
	if !ok || strings.TrimSpace(cr) == "" {
		t.Fatalf("expected continuum.yaml in HA overlay, got files=%v\ncontents=%q",
			fileNames(files), cr)
	}
	for _, want := range []string{
		"apiVersion: dr.openova.io/v1",
		"kind: Continuum",
		"name: bp-wordpress-tenant",
		"namespace: org-t-acme",
		"applicationRef: bp-wordpress-tenant",
		"primaryRegion: hz-fsn-rtz-prod",
		"- hz-hel-rtz-prod",
		"rto: 30s",
		"rpo: 5s",
		"kind: dns-quorum",
		"selector: ifurlup",
		"url: https://wordpress.acme.otech.example/healthz",
	} {
		if !strings.Contains(cr, want) {
			t.Errorf("continuum.yaml missing %q\n%s", want, cr)
		}
	}
	// kustomization.yaml MUST also list it under resources so Flux
	// actually applies the CR. Without this, the CR file exists in
	// git but never reconciles into the cluster.
	kust := files["kustomization.yaml"]
	if !strings.Contains(kust, "- continuum.yaml") {
		t.Errorf("kustomization.yaml missing 'continuum.yaml' entry:\n%s", kust)
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

// Defence-in-depth: the writer falls back to single-cluster shape when
// the operator opts in but the region pair is degenerate (empty primary,
// empty replica, primary==replica). The alternative — emitting the
// block anyway — would let the chart's validateActiveHotStandbyRegions
// helper `fail` at template time and gate the entire tenant overlay
// reconcile, which is far worse than degrading silently to non-HA.
func TestRenderOrganizationOverlay_HotStandby_Degenerate_FallsBackToSingle(t *testing.T) {
	cases := []struct {
		name        string
		enable      string
		primary     string
		replica     string
		wantHABlock bool
	}{
		{"toggle-on-no-regions", "true", "", "", false},
		{"toggle-on-empty-primary", "true", "", "hz-hel-rtz-prod", false},
		{"toggle-on-empty-replica", "true", "hz-fsn-rtz-prod", "", false},
		{"toggle-on-same-region", "true", "hz-fsn-rtz-prod", "hz-fsn-rtz-prod", false},
		{"toggle-off-with-regions", "false", "hz-fsn-rtz-prod", "hz-hel-rtz-prod", false},
		{"toggle-on-distinct-regions", "true", "hz-fsn-rtz-prod", "hz-hel-rtz-prod", true},
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
			has := strings.Contains(wp, "activeHotStandby:")
			if has != tc.wantHABlock {
				t.Fatalf("activeHotStandby presence=%v, want %v\noverlay:\n%s",
					has, tc.wantHABlock, wp)
			}
		})
	}
}

// #5623 — the emitted HA block MUST declare what promotes the standby.
//
// On the hw292 G12 region-kill (2026-08-03) bp-cnpg-pair's region-B dr-promoter
// promoted at T0+2m16s with RPO=0, while every DR pair that shipped no
// region-local promoter stayed pg_is_in_recovery()=t for the entire outage.
// bp-wordpress-tenant is in the second group: it renders a cross-region standby
// but ships no promoter, and the per-Application Continuum CR this same writer
// emits is reconciled by continuum-controller — a SINGLE region-A Deployment
// that dies with the region it would fail away from (#5137; verified live on
// hw292 2026-08-06, region-B carries neither the controller nor the Continuum
// CRD) whose spec.autoFailover defaults to false.
//
// bp-wordpress-tenant 0.4.23+ therefore REFUSES to render an active-hot-standby
// pair with no declared mechanism. This test is the lock on the writer side: if
// the declaration is ever dropped from the template, every tenant WordPress
// install on a multi-region Sovereign fails its Helm render instead of silently
// shipping a standby nothing promotes. Asserting the VALUE, not the key — a key
// with an empty value is exactly how #5639 survived two months.
func TestRenderOrganizationOverlay_HotStandby_On_DeclaresPromotionMechanism(t *testing.T) {
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "true")
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "hz-hel-rtz-prod")
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wp := files["bp-wordpress-tenant.yaml"]
	if !strings.Contains(wp, "promotion:") {
		t.Fatalf("#5623: HA overlay carries no pg.activeHotStandby.promotion block — "+
			"bp-wordpress-tenant 0.4.23+ fails the render without it:\n%s", wp)
	}
	if !strings.Contains(wp, "mechanism: manual") {
		t.Fatalf("#5623: HA overlay does not declare mechanism: manual. Only declare a "+
			"mechanism this chart actually ships — \"dr-promoter\" and \"continuum\" fail "+
			"closed in the chart precisely so an undelivered failover cannot be asserted:\n%s", wp)
	}
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
