// Producer-side tests for #5639 — the per-Org bp-postgres install door must
// emit `topology.primary.region` whenever it emits an active-hot-standby
// `topology.mode`. Mode and region are ONE contract: the chart pins a REQUIRED
// nodeAffinity on the region, so a mode without a region renders
// `openova.io/region In [""]`, which no node can ever satisfy.
//
// Live evidence these tests encode (hw292, 2026-08-03): the HelmRelease values
// for the per-Org Cluster `hw292-omani-works/postgres` were exactly
// {"topology":{"mode":"active-hot-standby"}} — mode present, region absent. The
// Cluster sat at phase="Setting up primary" for 7+ hours with
// `FailedScheduling: 0/4 nodes are available: 4 node(s) didn't match Pod's node
// affinity/selector` while every node carried
// openova.io/region=hw-me-east-215-a-rtz-prod. The HelmRelease reported
// `install succeeded` throughout.
//
// ANTI-VACUITY. Every assertion here was run against the UNFIXED producer first:
//   - StampsPrimaryRegion / SeedDoor / InstallDoor / CrossRegionBool / Completes-
//     ExplicitMode all FAIL on unfixed code with the region key simply absent
//     (`topology.primary` missing), which is the defect itself.
//   - SingletonStampsNoRegion and ExplicitRegionNeverClobbered PASS on unfixed
//     code — they are the no-regression side of the pair and are here so the
//     fix cannot be "stamp a region everywhere".
//   - NoEnvStampsNothing PASSES on unfixed code for the same reason; it pins the
//     deliberate fail-closed handoff to the chart's `required`.
// A one-sided suite would go green on a producer that stamped the region into
// every install, including singleton ones that must not carry it.
//
// Domains: the region strings here are canonical openova.io/region NODE LABELS
// (hz-fsn-rtz-prod / hw-me-east-215-a-rtz-prod), not hostnames.

package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// postgresTopologyOf pulls spec.parameters.topology as a map, failing loudly
// rather than panicking on a producer that emitted a different shape.
func postgresTopologyOf(t *testing.T, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	topo, ok := params["topology"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters carry no topology object: %#v", params)
	}
	return topo
}

// postgresPrimaryRegionOf returns topology.primary.region, or "" when the
// producer emitted no primary block at all (the #5639 defect shape).
func postgresPrimaryRegionOf(t *testing.T, params map[string]interface{}) string {
	t.Helper()
	topo := postgresTopologyOf(t, params)
	primary, ok := topo["primary"].(map[string]interface{})
	if !ok {
		return ""
	}
	region, _ := primary["region"].(string)
	return region
}

// ── the defect: mode emitted without its region ─────────────────────────

// FAILS on unfixed code (topology.primary absent) — this is #5639 verbatim.
func TestDefaultedParameters_PostgresActiveHotStandby_StampsPrimaryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hw-me-east-215-a-rtz-prod")

	got := defaultedParameters("bp-postgres", "active-hot-standby", "", "", "", nil)
	topo := postgresTopologyOf(t, got)

	if topo["mode"] != "active-hot-standby" {
		t.Fatalf("topology.mode = %v, want active-hot-standby", topo["mode"])
	}
	if region := postgresPrimaryRegionOf(t, got); region != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("topology.primary.region = %q, want hw-me-east-215-a-rtz-prod — "+
			"a mode without a region renders `openova.io/region In [\"\"]` and the "+
			"primary is unschedulable forever (#5639)", region)
	}

	// Mode and region MUST live in the SAME topology object: the
	// application-controller merges Blueprint manifests.values with
	// spec.parameters SHALLOWLY, so a region emitted under a separate
	// top-level key would be dropped before it reaches HelmRelease.spec.values.
	if _, stray := got["primary"]; stray {
		t.Fatalf("region was emitted as a TOP-LEVEL `primary` key; it must sit "+
			"inside `topology` or the shallow mergeMaps drops it: %#v", got)
	}
}

// Every canonical HA placement token folds to the chart's active-hot-standby
// mode, so every one of them must also carry the region.
func TestDefaultedParameters_PostgresEveryHAToken_StampsPrimaryRegion(t *testing.T) {
	for _, placement := range []string{
		"active-hot-standby", "active-hotstandby", "active-active", "active-passive",
	} {
		t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")
		got := defaultedParameters("postgres", placement, "", "", "", nil)
		if region := postgresPrimaryRegionOf(t, got); region != "hz-fsn-rtz-prod" {
			t.Errorf("placement %q → topology.primary.region = %q, want hz-fsn-rtz-prod",
				placement, region)
		}
	}
}

// The chart ALSO activates the region-pinned pair on the boolean
// topology.crossRegion (the bootstrap-kit slot signal, because envsubst cannot
// produce the mode STRING). A caller riding that seam needs the region just as
// much. FAILS on unfixed code.
func TestDefaultedParameters_PostgresCrossRegionBool_StampsPrimaryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")

	explicit := map[string]interface{}{
		"topology": map[string]interface{}{"mode": "singleton", "crossRegion": true},
	}
	got := defaultedParameters("bp-postgres", "singleton", "", "", "", explicit)

	if region := postgresPrimaryRegionOf(t, got); region != "hz-fsn-rtz-prod" {
		t.Fatalf("crossRegion=true → topology.primary.region = %q, want hz-fsn-rtz-prod — "+
			"crossRegion renders the SAME region-pinned Cluster as mode=active-hot-standby", region)
	}
}

// A caller that supplies its own active-hot-standby mode goes through the same
// chart code path and hits the same empty selector, so the region is completed
// there too. FAILS on unfixed code (which returned explicit values verbatim and
// stamped nothing).
func TestDefaultedParameters_PostgresExplicitMode_RegionCompleted(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")

	explicit := map[string]interface{}{
		"topology":  map[string]interface{}{"mode": "active-hot-standby"},
		"databases": []interface{}{map[string]interface{}{"name": "wp", "owner": "wp"}},
	}
	got := defaultedParameters("bp-postgres", "singleton", "", "", "", explicit)

	if postgresTopologyOf(t, got)["mode"] != "active-hot-standby" {
		t.Fatalf("explicit topology.mode must survive: %#v", got["topology"])
	}
	if _, ok := got["databases"]; !ok {
		t.Fatalf("explicit databases must survive: %#v", got)
	}
	if region := postgresPrimaryRegionOf(t, got); region != "hz-fsn-rtz-prod" {
		t.Fatalf("explicit-mode install got topology.primary.region = %q, want hz-fsn-rtz-prod", region)
	}
}

// ── the other direction: no region where none belongs ───────────────────

// PASSES on unfixed code. Present so the fix cannot be "always stamp a region":
// the singleton render consumes no region and must stay byte-identical.
func TestDefaultedParameters_PostgresSingleton_StampsNoRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")

	got := defaultedParameters("bp-postgres", "singleton", "", "", "", nil)
	topo := postgresTopologyOf(t, got)

	if topo["mode"] != "singleton" {
		t.Fatalf("topology.mode = %v, want singleton", topo["mode"])
	}
	if _, ok := topo["primary"]; ok {
		t.Fatalf("singleton must NOT carry topology.primary — the singleton Cluster "+
			"renders no nodeAffinity and pinning it would change a shape that works: %#v", topo)
	}
}

// PASSES on unfixed code. An operator who pinned a region explicitly owns it.
func TestDefaultedParameters_PostgresExplicitRegion_NeverClobbered(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")

	explicit := map[string]interface{}{
		"topology": map[string]interface{}{
			"mode":    "active-hot-standby",
			"primary": map[string]interface{}{"region": "hz-hel-rtz-prod"},
		},
	}
	got := defaultedParameters("bp-postgres", "active-hot-standby", "", "", "", explicit)

	if region := postgresPrimaryRegionOf(t, got); region != "hz-hel-rtz-prod" {
		t.Fatalf("explicit topology.primary.region was overwritten: got %q, want hz-hel-rtz-prod", region)
	}
}

// PASSES on unfixed code. FAIL-CLOSED handoff: with no Sovereign region declared
// (the mothership / Catalyst-Zero case) the producer guesses NOTHING. The chart's
// `required` then refuses to render and names the key — an install error a human
// reads, rather than a green badge over a Pending Pod. Stamping a fabricated
// region here would be worse than stamping none: a near-miss region is exactly as
// unschedulable as an empty one.
func TestDefaultedParameters_PostgresActiveHotStandby_NoEnv_StampsNothing(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "")

	got := defaultedParameters("bp-postgres", "active-hot-standby", "", "", "", nil)
	topo := postgresTopologyOf(t, got)

	if topo["mode"] != "active-hot-standby" {
		t.Fatalf("topology.mode = %v, want active-hot-standby", topo["mode"])
	}
	if _, ok := topo["primary"]; ok {
		t.Fatalf("with no SOVEREIGN_PRIMARY_REGION the producer must stamp NOTHING and "+
			"let the chart fail closed; got %#v", topo)
	}
}

// A non-postgres Blueprint never grows a topology block from this path.
func TestDefaultedParameters_NonPostgres_NoRegionStamp(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hz-fsn-rtz-prod")

	got := defaultedParameters("bp-grafana", "active-hot-standby", "", "", "", nil)
	if len(got) != 0 {
		t.Fatalf("non-postgres parameters must stay empty {}, got %#v", got)
	}
}

// ── both real doors, end to end onto the Application CR ─────────────────

// The create-instance seed door. FAILS on unfixed code.
func TestNewApplicationCRFromSeed_PostgresAHS_CarriesPrimaryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hw-me-east-215-a-rtz-prod")

	seed := instances.ApplicationSeed{
		Name:      "postgres",
		Namespace: "hw292-omani-works",
		Blueprint: "bp-postgres",
		Topology:  "active-hot-standby",
	}
	obj := newApplicationCRFromSeed(seed)

	params, found, err := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if err != nil {
		t.Fatalf("read spec.parameters: %v", err)
	}
	if !found || params == nil {
		t.Fatal("spec.parameters absent/nil")
	}
	if region := postgresPrimaryRegionOf(t, params); region != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("seed door emitted topology.primary.region = %q, want hw-me-east-215-a-rtz-prod — "+
			"this is the exact CR shape that produced the hw292 hang", region)
	}

	rep, vErr := validate.Parameters(bpPostgresConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("region-bearing parameters must still satisfy the bp-postgres configSchema: %v", rep.Errors)
	}
}

// The POST /applications install door. FAILS on unfixed code.
func TestNewApplicationUnstructured_PostgresAHS_CarriesPrimaryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hw-me-east-215-a-rtz-prod")

	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-postgres", Version: "0.2.17"},
		Name:            "postgres",
		OrganizationRef: "hw292-omani-works",
		EnvironmentRef:  "hw292-omani-works-prod",
		Placement: applicationPlacement{
			Mode:    "active-hot-standby",
			Regions: []string{"me-east-215-a", "me-east-215-b"},
		},
	}
	obj := newApplicationUnstructured(req, "", "")

	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatal("spec.parameters absent/nil")
	}

	region := postgresPrimaryRegionOf(t, params)
	if region != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("install door emitted topology.primary.region = %q, want the NODE LABEL "+
			"hw-me-east-215-a-rtz-prod", region)
	}
	// The node label is NOT the placement region. #5482 recorded all three
	// spellings on one hw291 Application (me-east-215-a in the CR,
	// hw-me-east-215-a-rtz-prod on the node, platform-bootstrap-owned-host in
	// the Overview). A near-miss region is exactly as unschedulable as an empty
	// one, so this pins that we did not reach for Placement.Regions[0].
	if region == req.Placement.Regions[0] {
		t.Fatalf("the region was taken from Placement.Regions[0] (%q) — that is the CLOUD "+
			"region, not the openova.io/region node label (#5482)", req.Placement.Regions[0])
	}

	rep, vErr := validate.Parameters(bpPostgresConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("region-bearing parameters must still satisfy the bp-postgres configSchema: %v", rep.Errors)
	}
}
