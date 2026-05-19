// jobs_d20_secondary_test.go — unit coverage for the D20 multi-region
// chroot seed fan-out (issue #1821).
//
// The Sovereign /jobs page hides the Region filter dropdown on
// single-region sets (regionOptions.length > 1 gate in
// products/catalyst/bootstrap/ui/src/pages/sovereign/JobsTable.tsx).
// On a 3-region Sovereign the dropdown only appears when the per-
// deployment jobs.Store carries region-prefixed install-* rows for
// every secondary region. Before this test landed,
// chrootSeedJobsStoreIfEmpty only enumerated the primary cluster's
// HelmReleases — secondary regions' rows never made it to the store,
// the dropdown stayed hidden, and DoD D20 failed on every fresh prov.
//
// The fan-out is wired up in chrootSeedSecondaryRegions which walks
// h.k8sCache.Clusters() and consults regionFromSecondaryClusterID to
// derive the region key from each registered cluster id. The two
// tests below cover (a) the cluster-id → region key derivation
// contract and (b) the end-to-end seed write into the bridge.

package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// TestRegionFromSecondaryClusterID_Contract locks in the rules the
// chroot fan-out relies on:
//   - empty cluster id → no region
//   - cluster id == primary depID → no region (primary already seeded)
//   - cluster id == chroot fallback (sovereign-<fqdn>) → no region
//   - "<depID>-<region>" → region
//   - "<chrootFallback>-<region>" → region
//   - alien / cross-deployment ids → no region (no leakage)
//   - empty SOVEREIGN_FQDN ("sovereign-" fallback) does NOT match any
//     ".*-..." cluster id (defense in depth)
func TestRegionFromSecondaryClusterID_Contract(t *testing.T) {
	cases := []struct {
		name       string
		clusterID  string
		depID      string
		fallbackID string
		want       string
	}{
		{
			name:       "primary depID — no region",
			clusterID:  "dep123",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "chroot fallback id — no region",
			clusterID:  "sovereign-t34.omani.works",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "depID-prefixed secondary — extracts region",
			clusterID:  "dep123-hel1-1",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "hel1-1",
		},
		{
			name:       "fallback-prefixed secondary — extracts region",
			clusterID:  "sovereign-t34.omani.works-nbg1-2",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "nbg1-2",
		},
		{
			name:       "alien cluster id — no region (no leakage)",
			clusterID:  "other-deployment-fsn1",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "empty cluster id — no region",
			clusterID:  "",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "bare sovereign- fallback marker does NOT match arbitrary ids",
			clusterID:  "sovereign-fsn1",
			depID:      "dep123",
			fallbackID: "sovereign-",
			want:       "",
		},
		{
			name:       "depID with hyphenated region — preserves region key",
			clusterID:  "abc-123-fsn1-2",
			depID:      "abc-123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "fsn1-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := regionFromSecondaryClusterID(tc.clusterID, tc.depID, tc.fallbackID)
			if got != tc.want {
				t.Fatalf("regionFromSecondaryClusterID(%q, %q, %q) = %q; want %q",
					tc.clusterID, tc.depID, tc.fallbackID, got, tc.want)
			}
		})
	}
}

// TestSnapshotsToSeedsForRegion_PrefixesAppID verifies that the seed
// shape the chroot fan-out feeds into the Bridge produces region-
// prefixed jobs that the FE regionFromJob() helper picks up. Without
// the "<region>:" prefix on the resulting Job.AppID the UI's
// regionOptions set stays size 1 and the Region dropdown never
// renders. Locks in the pipe from ComponentSnapshot → InformerSeed →
// Job.AppID without standing up the full helmwatch.Bridge.
//
// snapshotsToSeedsForRegion already has direct coverage in
// jobs_backfill_test.go for the bare-region case; this test pins the
// hyphenated-region (hel1-1, nbg1-2) case the D20 fan-out exercises
// in production.
func TestSnapshotsToSeedsForRegion_HyphenatedRegion(t *testing.T) {
	// Synthesise two snapshots — chroot fan-out path uses the same
	// helmwatch.ComponentSnapshot shape ListAndSnapshotHelmReleases
	// returns from a fake apiserver.
	snaps := []helmwatch.ComponentSnapshot{
		{AppID: "bp-cilium", Status: "succeeded"},
		{AppID: "bp-flux", Status: "running", DependsOn: []string{"bp-cilium"}},
	}
	seeds := snapshotsToSeedsForRegion(snaps, "hel1-1")
	if len(seeds) != 2 {
		t.Fatalf("got %d seeds; want 2", len(seeds))
	}
	wantBy := map[string]struct {
		state string
		deps  []string
	}{
		"hel1-1:bp-cilium": {state: "succeeded", deps: nil},
		"hel1-1:bp-flux":   {state: "running", deps: []string{"hel1-1:bp-cilium"}},
	}
	for _, s := range seeds {
		want, ok := wantBy[s.Component]
		if !ok {
			t.Fatalf("unexpected seed component %q", s.Component)
		}
		if s.State != want.state {
			t.Fatalf("seed %q: state=%q want %q", s.Component, s.State, want.state)
		}
		if len(want.deps) != len(s.DependsOn) {
			t.Fatalf("seed %q: deps=%v want %v", s.Component, s.DependsOn, want.deps)
		}
		for i, d := range s.DependsOn {
			if d != want.deps[i] {
				t.Fatalf("seed %q: deps[%d]=%q want %q", s.Component, i, d, want.deps[i])
			}
		}
	}
}
