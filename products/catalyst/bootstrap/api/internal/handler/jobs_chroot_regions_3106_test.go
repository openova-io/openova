// jobs_chroot_regions_3106_test.go — unit coverage for the #3106
// cloud-graph "Region 1/1" fix.
//
// On a 2-region hot-standby Sovereign the chroot catalyst-api may have
// an empty `regionsJson` ConfigMap key (SOVEREIGN_REGIONS_JSON=[]) even
// though the per-Sovereign overlay populates the discrete
// primaryRegion / replicaRegion / enableHotStandby keys. Before this
// fix chrootRegionsFromEnv returned nil in that case, buildTopology
// dropped into the single-cluster live-Nodes path, and
// `/cloud?view=graph` rendered only the in-cluster primary region
// ("Region 1/1") despite both clusters genuinely existing. These tests
// lock in: N regions of env input → N regions reconstructed, so the
// topology loader emits one Region per region for the graph view.
//
// Reproduces hw101 (e19b083c6db41bb0, 2026-06-08): regionsJson=[],
// primaryRegion=hw-me-east-215-a-rtz-prod,
// replicaRegion=hw-me-east-215-b-rtz-prod, enableHotStandby=true.

package handler

import (
	"io"
	"log/slog"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestChrootRegionsFromEnv_PrimaryReplicaFallback is the core #3106
// table: each row sets the env the chroot sees and asserts the
// reconstructed CloudRegion list. The "regionsJson empty + 2-region
// hot-standby" row is the exact hw101 shape that produced "Region
// 1/1".
func TestChrootRegionsFromEnv_PrimaryReplicaFallback(t *testing.T) {
	cases := []struct {
		name        string
		regionsJSON string
		primary     string
		replica     string
		hotStandby  string
		wantRegions []string
	}{
		{
			name:        "hw101 shape — empty regionsJson, 2-region hot-standby → BOTH regions",
			regionsJSON: "[]",
			primary:     "hw-me-east-215-a-rtz-prod",
			replica:     "hw-me-east-215-b-rtz-prod",
			hotStandby:  "true",
			wantRegions: []string{"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod"},
		},
		{
			name:        "unset regionsJson, 2-region hot-standby → BOTH regions",
			regionsJSON: "",
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "true",
			wantRegions: []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"},
		},
		{
			name:        "hot-standby disabled → primary ONLY (no replica row)",
			regionsJSON: "[]",
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "false",
			wantRegions: []string{"hz-fsn-rtz-prod"},
		},
		{
			name:        "hot-standby unset → primary ONLY",
			regionsJSON: "",
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "",
			wantRegions: []string{"hz-fsn-rtz-prod"},
		},
		{
			name:        "single-region prov (no replica) → primary ONLY",
			regionsJSON: "[]",
			primary:     "hz-fsn-rtz-prod",
			replica:     "",
			hotStandby:  "true",
			wantRegions: []string{"hz-fsn-rtz-prod"},
		},
		{
			name:        "replica == primary (degenerate) → deduped to ONE",
			regionsJSON: "",
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-fsn-rtz-prod",
			hotStandby:  "true",
			wantRegions: []string{"hz-fsn-rtz-prod"},
		},
		{
			name:        "no primary, no regionsJson → nil (live-Nodes fallback preserved)",
			regionsJSON: "",
			primary:     "",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "true",
			wantRegions: nil,
		},
		{
			name:        "populated regionsJson WINS over primary/replica env",
			regionsJSON: `[{"cloudRegion":"region-x","workerCount":3},{"cloudRegion":"region-y","workerCount":3},{"cloudRegion":"region-z","workerCount":3}]`,
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "true",
			wantRegions: []string{"region-x", "region-y", "region-z"},
		},
		{
			name:        "malformed regionsJson falls THROUGH to primary/replica fallback",
			regionsJSON: "{not-json",
			primary:     "hz-fsn-rtz-prod",
			replica:     "hz-hel-rtz-prod",
			hotStandby:  "1",
			wantRegions: []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_REGIONS_JSON", tc.regionsJSON)
			t.Setenv("SOVEREIGN_PRIMARY_REGION", tc.primary)
			t.Setenv("SOVEREIGN_REPLICA_REGION", tc.replica)
			t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", tc.hotStandby)

			got := chrootRegionsFromEnv()
			if len(got) != len(tc.wantRegions) {
				t.Fatalf("region count = %d (%v); want %d (%v)",
					len(got), regionCodes(got), len(tc.wantRegions), tc.wantRegions)
			}
			for i, want := range tc.wantRegions {
				if got[i].CloudRegion != want {
					t.Fatalf("region[%d].CloudRegion = %q; want %q (full: %v)",
						i, got[i].CloudRegion, want, regionCodes(got))
				}
			}
		})
	}
}

// TestEnvTruthy locks in the boolean-env parsing the replica gate relies
// on — the chart stamps "true" but defense-in-depth accepts the common
// truthy spellings, and everything else (notably "false" and unset) is
// false.
func TestEnvTruthy(t *testing.T) {
	truthy := []string{"true", "TRUE", "True", "1", "yes", "YES", "on", "  true  "}
	falsy := []string{"", "false", "FALSE", "0", "no", "off", "nope", "2"}
	for _, v := range truthy {
		t.Run("truthy/"+v, func(t *testing.T) {
			t.Setenv("X_TEST_BOOL", v)
			if !envTruthy("X_TEST_BOOL") {
				t.Fatalf("envTruthy(%q) = false; want true", v)
			}
		})
	}
	for _, v := range falsy {
		t.Run("falsy/"+v, func(t *testing.T) {
			t.Setenv("X_TEST_BOOL", v)
			if envTruthy("X_TEST_BOOL") {
				t.Fatalf("envTruthy(%q) = true; want false", v)
			}
		})
	}
}

// TestChrootEnsureDeployment_TwoRegionsFromPrimaryReplica is the
// end-to-end proof: when the chroot synthesises its self-deployment
// record (the path /infrastructure/topology takes on the Sovereign's
// own console), Request.Regions carries BOTH regions. The topology
// loader's buildTopology consumes Request.Regions and emits one Region
// per entry, so this asserts the data the cloud-graph renders has 2
// regions, not 1.
func TestChrootEnsureDeployment_TwoRegionsFromPrimaryReplica(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw101.omantel.biz")
	t.Setenv("SOVEREIGN_REGIONS_JSON", "[]") // the hw101 wedge: empty list
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "hw-me-east-215-a-rtz-prod")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "hw-me-east-215-b-rtz-prod")
	t.Setenv("SOVEREIGN_ENABLE_HOT_STANDBY", "true")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	dep := h.chrootEnsureDeployment("e19b083c6db41bb0")
	if dep == nil {
		t.Fatal("chrootEnsureDeployment returned nil — expected a synthesised record in chroot mode")
	}
	if len(dep.Request.Regions) != 2 {
		t.Fatalf("synthesised Request.Regions count = %d (%v); want 2 — cloud-graph would render Region 1/%d",
			len(dep.Request.Regions), regionCodes(dep.Request.Regions), len(dep.Request.Regions))
	}
	if dep.Request.Regions[0].CloudRegion != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("region[0] = %q; want hw-me-east-215-a-rtz-prod", dep.Request.Regions[0].CloudRegion)
	}
	if dep.Request.Regions[1].CloudRegion != "hw-me-east-215-b-rtz-prod" {
		t.Fatalf("region[1] = %q; want hw-me-east-215-b-rtz-prod", dep.Request.Regions[1].CloudRegion)
	}
	// The legacy singular Region field should anchor to the primary so
	// the Settings page + derivePattern stay consistent.
	if dep.Request.Region != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("legacy Request.Region = %q; want primary hw-me-east-215-a-rtz-prod", dep.Request.Region)
	}
}

func regionCodes(rs []provisioner.RegionSpec) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.CloudRegion)
	}
	return out
}
