// appconfigs_test.go — TBD-V27 (#2042) regression locks.
//
// Asserts that customer-chosen configSchema values
// (Tenant.AppConfigs keyed by app SLUG, persisted by PR #2043 and
// dispatched on order.placed by the billing handler) actually
// materialize in the rendered manifest. Before TBD-V27 the values
// reached the Tenant store + the NATS event but were silently dropped
// at the manifest renderer — postgres always rendered replicas:1 +
// 2Gi storage regardless of the customer's picks. These tests are
// the regression seat-belt against the gap re-opening.

package gitops

import (
	"strings"
	"testing"
)

// TestPostgres_AppConfigs_RendersCustomerValues covers the canonical
// Postgres-backed app (the 10-step deterministic walk's step-2
// bundle). With AppConfigs={"postgres":{"replicas":3,"disk_gb":20,
// "backups_enabled":true}} the rendered db-postgres.yaml MUST show
// `replicas: 3`, `storage: 20Gi`, and a `backups-enabled: "true"`
// annotation. Anything less is theater.
func TestPostgres_AppConfigs_RendersCustomerValues(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	// JSON unmarshal would land integers as float64 inside the opaque
	// map; the renderer must accept either. Test both shapes here to
	// document the contract.
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"}, // umami needs postgres → db-postgres.yaml gets rendered
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"replicas":        float64(3), // JSON-decoded shape
				"disk_gb":         int(20),    // direct int shape
				"backups_enabled": true,
			},
		},
	)

	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-postgres.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("db-postgres.yaml not generated")
	}

	must := []string{
		"replicas: 3",
		"storage: 20Gi",
		`openova.io/backups-enabled: "true"`,
	}
	for _, want := range must {
		if !strings.Contains(manifest, want) {
			t.Errorf("db-postgres.yaml missing %q — full manifest:\n%s", want, manifest)
		}
	}
	// The default-path strings MUST NOT appear when the customer chose
	// something different — guards against the renderer ignoring the
	// map and falling back to defaults.
	mustNot := []string{
		"replicas: 1\n",
		"storage: 2Gi",
		`openova.io/backups-enabled: "false"`,
	}
	for _, banned := range mustNot {
		if strings.Contains(manifest, banned) {
			t.Errorf("db-postgres.yaml unexpectedly contains default value %q — appConfigs were dropped", banned)
		}
	}
}

// TestPostgres_AppConfigs_NilUsesDefaults — the legacy code path
// (handlers that don't carry AppConfigs) MUST keep working unchanged.
// Nil/empty map → defaults (replicas:1, 2Gi, backups disabled).
func TestPostgres_AppConfigs_NilUsesDefaults(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"},
		"deadbeef",
		nil,
	)
	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-postgres.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("db-postgres.yaml not generated")
	}
	want := []string{
		"replicas: 1\n",
		"storage: 5Gi", // the default disk_gb from seed.go = 5
		`openova.io/backups-enabled: "false"`,
	}
	for _, w := range want {
		if !strings.Contains(manifest, w) {
			t.Errorf("nil-appConfigs manifest missing default %q — full manifest:\n%s", w, manifest)
		}
	}
}

// TestPostgres_AppConfigs_OutOfRangeFallsBack — the renderer MUST
// clamp / reject out-of-range values. configSchema for `replicas` is
// min=1, max=5; passing 99 must fall back to default (1), not render
// `replicas: 99` and let the user smuggle past the schema.
func TestPostgres_AppConfigs_OutOfRangeFallsBack(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"},
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"replicas": float64(99), // out of [1,5]
				"disk_gb":  float64(999), // out of [1,500]
			},
		},
	)
	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-postgres.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("db-postgres.yaml not generated")
	}
	banned := []string{
		"replicas: 99",
		"storage: 999Gi",
	}
	for _, b := range banned {
		if strings.Contains(manifest, b) {
			t.Errorf("out-of-range value %q smuggled past configSchema — full manifest:\n%s", b, manifest)
		}
	}
	want := []string{
		"replicas: 1\n", // fell back to default
		"storage: 5Gi",  // fell back to default
	}
	for _, w := range want {
		if !strings.Contains(manifest, w) {
			t.Errorf("out-of-range fallback missing default %q — full manifest:\n%s", w, manifest)
		}
	}
}

// TestPostgres_AppConfigs_UnknownKeysDropped — keys not in the
// canonical configSchema (replicas / disk_gb / backups_enabled) MUST
// NOT appear anywhere in the rendered YAML. The Warn log fires but
// the renderer keeps producing the known-good manifest.
func TestPostgres_AppConfigs_UnknownKeysDropped(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"},
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"replicas":          float64(2),
				"evil_smuggle_yaml": "extraField: pwned",
				"another_unknown":   "value",
			},
		},
	)
	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-postgres.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("db-postgres.yaml not generated")
	}
	// Known key DID render
	if !strings.Contains(manifest, "replicas: 2") {
		t.Errorf("known key replicas:2 missing — full manifest:\n%s", manifest)
	}
	// Unknown keys MUST be absent from the rendered YAML
	banned := []string{
		"evil_smuggle_yaml",
		"extraField",
		"pwned",
		"another_unknown",
	}
	for _, b := range banned {
		if strings.Contains(manifest, b) {
			t.Errorf("unknown configSchema key %q tunnelled into manifest — full manifest:\n%s", b, manifest)
		}
	}
}

// TestMySQL_AppConfigs_ClampsReplicasTo1 — MySQL primary-replica
// replication is not yet wired; replicas>1 must be clamped to 1 with
// a Warn log. Disk size threads through unchanged.
func TestMySQL_AppConfigs_ClampsReplicasTo1(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"wordpress"},
		"deadbeef",
		map[string]map[string]any{
			"mysql": {
				"replicas": float64(3),
				"disk_gb":  float64(50),
			},
		},
	)
	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-mysql.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("db-mysql.yaml not generated")
	}
	if !strings.Contains(manifest, "replicas: 1\n") {
		t.Errorf("MySQL replicas not clamped to 1 — full manifest:\n%s", manifest)
	}
	if strings.Contains(manifest, "replicas: 3") {
		t.Errorf("MySQL accepted replicas:3 — primary-replica not wired, this would break the cluster")
	}
	if !strings.Contains(manifest, "storage: 50Gi") {
		t.Errorf("MySQL disk_gb didn't thread through — full manifest:\n%s", manifest)
	}
}

// TestReadIntCfg_HandlesAllShapes documents the JSON-vs-Go type
// coverage the helper has to absorb. Critical because tenant.created /
// order.placed events arrive via NATS JSON decode (float64 for ints)
// while in-memory test fixtures often pass real ints.
func TestReadIntCfg_HandlesAllShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want int
	}{
		{"int", int(3), 3},
		{"int32", int32(3), 3},
		{"int64", int64(3), 3},
		{"float64", float64(3), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"replicas": tc.raw}
			got := readIntCfg(cfg, "replicas", 1, 1, 5, "test")
			if got != tc.want {
				t.Errorf("got %d want %d for raw=%v (%T)", got, tc.want, tc.raw, tc.raw)
			}
		})
	}
}

// TestReadIntCfg_MistypeFallsBack — strings, booleans, nested maps
// MUST fall back to default with a Warn, not panic and not coerce.
func TestReadIntCfg_MistypeFallsBack(t *testing.T) {
	cfg := map[string]any{
		"replicas": "3", // string, not int
	}
	got := readIntCfg(cfg, "replicas", 1, 1, 5, "test")
	if got != 1 {
		t.Errorf("got %d want 1 (default) on string mistype", got)
	}
}

// TestPostgres_AppConfigs_ActiveHotStandby_GenericApp — TBD-V17
// (#2068). The Pillar-3 cluster-pair install path MUST trigger for a
// NON-WordPress postgres-backed marketplace app (this test uses
// `umami`, which is one of the canonical non-WP postgres-backed apps
// in seed.go:60-92). Before #2068 the bp-cnpg-pair Cluster CRs were
// emitted ONLY by bp-wordpress-tenant's inline cnpg-cluster.yaml
// template — every non-WP customer journey through Pillar 3 was
// silently broken, and the audit at /tmp/audit-pillar3-cnpg-2026-05-
// 20.md flagged this gap.
//
// With active_hot_standby=true + distinct primary_region/replica_region,
// the rendered output MUST:
//
//	1. Emit `db-cnpg-pair.yaml` (the bp-cnpg-pair HelmRelease + its
//	   companion HelmRepository + postgres-credentials Secret).
//	2. NOT emit `db-postgres.yaml` (the legacy single-Pod Deployment
//	   would collide on `postgres` Service name and credentials Secret).
//	3. Carry the customer-chosen region pair into the HelmRelease values.
func TestPostgres_AppConfigs_ActiveHotStandby_GenericApp(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"}, // umami → postgres-backed, non-WordPress
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"active_hot_standby": true,
				"primary_region":     "hz-fsn-rtz-prod",
				"replica_region":     "hz-hel-rtz-prod",
				"replicas":           float64(3),
				"disk_gb":            float64(50),
			},
		},
	)

	var pairManifest, legacyManifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-cnpg-pair.yaml") {
			pairManifest = content
		}
		if strings.HasSuffix(path, "db-postgres.yaml") {
			legacyManifest = content
		}
	}
	if pairManifest == "" {
		t.Fatal("db-cnpg-pair.yaml NOT generated when active_hot_standby=true — Pillar 3 install path broken")
	}
	if legacyManifest != "" {
		t.Errorf("legacy db-postgres.yaml ALSO generated alongside cluster-pair — would collide on `postgres` Service")
	}
	must := []string{
		"chart: bp-cnpg-pair",
		"region: hz-fsn-rtz-prod", // primary
		"region: hz-hel-rtz-prod", // replica
		"instances: 3",
		"size: 50Gi",
		"database: db_umami", // per-app database name
	}
	for _, want := range must {
		if !strings.Contains(pairManifest, want) {
			t.Errorf("db-cnpg-pair.yaml missing %q — full manifest:\n%s", want, pairManifest)
		}
	}
}

// TestPostgres_AppConfigs_ActiveHotStandby_OFF — when the customer
// hasn't opted in (default behavior, every pre-#2068 tenant) the
// generic install path MUST NOT trigger. The legacy single-cluster
// generatePostgres() rendering applies unchanged.
func TestPostgres_AppConfigs_ActiveHotStandby_OFF(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"},
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"replicas": float64(2),
				"disk_gb":  float64(10),
				// active_hot_standby absent → defaults to false
			},
		},
	)
	for path := range files {
		if strings.HasSuffix(path, "db-cnpg-pair.yaml") {
			t.Errorf("db-cnpg-pair.yaml emitted with active_hot_standby unset — should default-OFF")
		}
	}
	var legacy string
	for path, content := range files {
		if strings.HasSuffix(path, "db-postgres.yaml") {
			legacy = content
		}
	}
	if legacy == "" {
		t.Fatal("legacy db-postgres.yaml missing — default-OFF path broken")
	}
}

// TestPostgres_AppConfigs_ActiveHotStandby_InvalidRegionPair — when
// the operator opts in but doesn't supply distinct primary/replica
// regions (either empty or identical), the renderer MUST fall back to
// the single-cluster shape rather than emit a bp-cnpg-pair HelmRelease
// the chart's `required` template guard would reject at install time.
// Symmetric with the WP-tenant path (org_tenant_gitops.go:560).
func TestPostgres_AppConfigs_ActiveHotStandby_InvalidRegionPair(t *testing.T) {
	cases := []struct {
		name           string
		primary        string
		replica        string
		wantClusterPair bool
	}{
		{"identical regions", "hz-fsn-rtz-prod", "hz-fsn-rtz-prod", false},
		{"empty primary", "", "hz-hel-rtz-prod", false},
		{"empty replica", "hz-fsn-rtz-prod", "", false},
		{"both empty", "", "", false},
		{"valid pair", "hz-fsn-rtz-prod", "hz-hel-rtz-prod", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
			files := g.GenerateAllWithAppConfigs("acme", "flexi",
				[]string{"umami"},
				"deadbeef",
				map[string]map[string]any{
					"postgres": {
						"active_hot_standby": true,
						"primary_region":     tc.primary,
						"replica_region":     tc.replica,
					},
				},
			)
			gotPair := false
			gotLegacy := false
			for path := range files {
				if strings.HasSuffix(path, "db-cnpg-pair.yaml") {
					gotPair = true
				}
				if strings.HasSuffix(path, "db-postgres.yaml") {
					gotLegacy = true
				}
			}
			if gotPair != tc.wantClusterPair {
				t.Errorf("cluster-pair emitted=%v want=%v", gotPair, tc.wantClusterPair)
			}
			if tc.wantClusterPair && gotLegacy {
				t.Errorf("legacy single-cluster ALSO emitted alongside cluster-pair — would collide")
			}
			if !tc.wantClusterPair && !gotLegacy {
				t.Errorf("legacy single-cluster missing on fallback path — graceful degradation broken")
			}
		})
	}
}

// TestPostgres_AppConfigs_ActiveHotStandby_ReplicasClamped — the
// bp-cnpg-pair chart's configSchema floor for instances is 3 (per
// platform/cnpg-pair/blueprint.yaml — active-hotstandby HA needs a
// 3-node quorum per region to survive a Pod loss without read-only
// degradation). When the customer picks replicas=1 or 2 we clamp to 3
// and log loud rather than render a HelmRelease the chart's `minimum`
// guard would reject.
func TestPostgres_AppConfigs_ActiveHotStandby_ReplicasClamped(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"umami"},
		"deadbeef",
		map[string]map[string]any{
			"postgres": {
				"active_hot_standby": true,
				"primary_region":     "hz-fsn-rtz-prod",
				"replica_region":     "hz-hel-rtz-prod",
				"replicas":           float64(1), // below the bp-cnpg-pair floor of 3
			},
		},
	)
	var pairManifest string
	for path, content := range files {
		if strings.HasSuffix(path, "db-cnpg-pair.yaml") {
			pairManifest = content
		}
	}
	if pairManifest == "" {
		t.Fatal("db-cnpg-pair.yaml not generated")
	}
	if !strings.Contains(pairManifest, "instances: 3") {
		t.Errorf("replicas=1 not clamped to instances:3 — full manifest:\n%s", pairManifest)
	}
	if strings.Contains(pairManifest, "instances: 1") {
		t.Errorf("instances:1 leaked into rendered HelmRelease — chart's minimum:3 guard would reject install")
	}
}

// TestReadStringCfg_HandlesNilAndMistype documents the contract of the
// new readStringCfg helper (added by #2068 for the cnpg-pair region
// pair pickups). Mirrors the readIntCfg / readBoolCfg coverage.
func TestReadStringCfg_HandlesNilAndMistype(t *testing.T) {
	if got := readStringCfg(nil, "primary_region", "default", "test"); got != "default" {
		t.Errorf("nil cfg: got %q want default", got)
	}
	cfg := map[string]any{
		"primary_region": float64(42), // wrong type
		"replica_region": "hz-hel-rtz-prod",
		"empty_string":   "",
	}
	if got := readStringCfg(cfg, "primary_region", "fallback", "test"); got != "fallback" {
		t.Errorf("mistype: got %q want fallback", got)
	}
	if got := readStringCfg(cfg, "replica_region", "fallback", "test"); got != "hz-hel-rtz-prod" {
		t.Errorf("valid: got %q want hz-hel-rtz-prod", got)
	}
	if got := readStringCfg(cfg, "missing_key", "dflt", "test"); got != "dflt" {
		t.Errorf("missing: got %q want dflt", got)
	}
	if got := readStringCfg(cfg, "empty_string", "dflt", "test"); got != "" {
		t.Errorf("empty string is a valid value: got %q want empty", got)
	}
}
