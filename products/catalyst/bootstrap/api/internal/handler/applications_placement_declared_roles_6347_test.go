package handler

// applications_placement_declared_roles_6347_test.go — UAT row 60 (#3375), the
// hw298 reproduction taken AFTER #6345 landed.
//
// WHAT WAS MEASURED. Mothership rolled to `catalyst-api:8c41df2`, same endpoint,
// same deployment (2540d866403f1f7c), same pass:
//
//	shared-pg      active-hot-standby → 2 targets  Primary(cluster set) + Standby(cluster set)
//	spine-gitea    active-hot-standby → 3 targets  Primary(set) + Primary(set) + Standby(cluster "")
//	spine-keycloak active-hot-standby → 3 targets  Primary(set) + Primary(set) + Standby(cluster "")
//
// #6345 made the Primary resolve; what it exposed is that NOTHING downstream
// turns two occupied regions into one Primary and one Standby. `shared-pg` is
// again the CONTROL and again escapes for a reason that is not a property of
// the app: CNPG stamps `openova.io/cnpg-role` on each half, so the roles come
// from positive per-leg evidence. gitea and keycloak carry no such marker, and
// their bootstrap-kit HelmReleases carry no `catalyst.openova.io/region-role`
// gate — "no gate ⇒ every region" — so they genuinely run in BOTH region
// clusters and the stateless arm calls each one a Primary.
//
// THE REGION VOCABULARY IS PART OF THE REPRODUCTION, not decoration. A target's
// region comes from the `openova.io/region` node label — the full cluster name
// `hw-me-east-215-a-rtz-prod` — while a spine Application's `spec.regions[]` and
// its per-app `dr-<app>` Continuum carry the bare cloud region `me-east-215-a`
// (post_handover_spine_apps.go:619). #5482 recorded all three divergent
// spellings on one Application. The fixtures below use BOTH spellings on
// purpose: a fix that compares the two raw resolves nothing and leaves the wire
// exactly as the walk found it.

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

const (
	// The deployment's declared regions (provisioner RegionSpec.CloudRegion) —
	// what a spine Application CR's spec.regions[] holds.
	declRegionA = "me-east-215-a"
	declRegionB = "me-east-215-b"
	// The canonical `openova.io/region` node/pod label — what a placement
	// target's Region actually carries on the wire.
	nodeRegionA = "hw-me-east-215-a-rtz-prod"
	nodeRegionB = "hw-me-east-215-b-rtz-prod"
)

// spineApplicationFixtureCRWithPlacement is spineApplicationFixtureCR plus the
// DECLARATION the live spine CRs carry: the canonical mode
// (spinePlacementMode) and the ordered region list placement.Resolve reads,
// where regions[0] is the primary and regions[1..] are the standbys.
func spineApplicationFixtureCRWithPlacement(name, hrName, hrNS, mode string, regions []string) *unstructured.Unstructured {
	u := spineApplicationFixtureCR(name, hrName, hrNS)
	_ = unstructured.SetNestedField(u.Object, mode, "spec", "placement")
	rgs := make([]any, 0, len(regions))
	for _, r := range regions {
		rgs = append(rgs, r)
	}
	_ = unstructured.SetNestedSlice(u.Object, rgs, "spec", "regions")
	return u
}

// TestPlacementDeclaredRoles_Row60_6347 — one table so the CONTROL and the
// DEFECT are answered by the same code path in the same run.
func TestPlacementDeclaredRoles_Row60_6347(t *testing.T) {
	cases := []struct {
		name    string
		depID   string
		appName string
		queryNS string
		objsA   []runtime.Object
		objsB   []runtime.Object
		crs     []runtime.Object

		wantTargets      int
		wantPrimaries    int
		wantStandbys     int
		wantDerived      bool
		wantUnresolvedPr bool
		wantPrimaryRegn  string
		wantStandbyRegn  string
		wantStandbyType  bpv1.StandbyType
		wantStandbyClstr bool // the Standby names the cluster it was observed in
		wantPattern      bpv1.Pattern
		why              string
	}{
		{
			// THE DEFECT — hw298 post-#6345. Pods in BOTH region clusters, no
			// role marker on either, an active-hot-standby declaration naming
			// which region is which.
			//
			// PRE-FIX this returns exactly what the walk recorded: THREE
			// targets — Primary(hw-…-a) + Primary(hw-…-b) + a third Standby
			// carrying `cluster: ""` that augmentWithContinuumStandby appends
			// because its region comparison never matches across the two
			// spellings. DerivePattern reads that list as `active-active`.
			name:    "spine-gitea shape: occupied in BOTH regions, declared active-hot-standby",
			depID:   "dep-6347-gitea",
			appName: "spine-gitea",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				identityFixturePod("gitea", "gitea-75d9f486fb-g8hsr", "gitea", nodeRegionA, ""),
				spineApplicationFixtureCRWithPlacement("spine-gitea", "bp-gitea", "flux-system",
					"active-hot-standby", []string{declRegionA, declRegionB}),
				helmReleaseFixture("bp-gitea", "flux-system", "gitea", "gitea"),
			},
			objsB: []runtime.Object{
				identityFixturePod("gitea", "gitea-75d9f486fb-qm4kt", "gitea", nodeRegionB, ""),
			},
			crs: []runtime.Object{
				newContinuumUnstructured("dr-spine-gitea", "catalyst", "spine-gitea", declRegionA, []string{declRegionB}),
			},
			wantTargets:      2,
			wantPrimaries:    1,
			wantStandbys:     1,
			wantDerived:      true,
			wantPrimaryRegn:  nodeRegionA,
			wantStandbyRegn:  nodeRegionB,
			wantStandbyType:  bpv1.StandbyHot,
			wantStandbyClstr: true,
			wantPattern:      bpv1.PatternActiveHotStandby,
			why:              "row 60's clause: ONE region-a primary + ONE region-b replica — not two primaries and a phantom third leg",
		},
		{
			// THE CONTROL — the row that was already correct on the wire and
			// must not move. Its roles come from positive per-leg CNPG
			// evidence, and the declaration agrees with them.
			name:    "shared-pg shape: CNPG role labels carry the split",
			depID:   "dep-6347-sharedpg",
			appName: "shared-pg",
			queryNS: "shared-data",
			objsA: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-1", "shared-pg", nodeRegionA, cnpgRolePrimary),
				spineApplicationFixtureCRWithPlacement("shared-pg", "bp-postgres-shared", "flux-system",
					"active-hot-standby", []string{declRegionA, declRegionB}),
				helmReleaseFixture("bp-postgres-shared", "flux-system", "shared-pg", "shared-data"),
			},
			objsB: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-r-1", "shared-pg", nodeRegionB, cnpgRoleReplica),
			},
			wantTargets:      2,
			wantPrimaries:    1,
			wantStandbys:     1,
			wantDerived:      true,
			wantPrimaryRegn:  nodeRegionA,
			wantStandbyRegn:  nodeRegionB,
			wantStandbyType:  bpv1.StandbyHot,
			wantStandbyClstr: true,
			wantPattern:      bpv1.PatternActiveHotStandby,
			why:              "the control proves the projection works; if this row moves, the fix broke what already worked",
		},
		{
			// THE ANTI-REGRESSION that decides whether the declaration is
			// allowed to overrule an OBSERVATION. After a switchover the live
			// primary is in region B while spec.regions[0] still names region
			// A. The cnpg-role labels are positive evidence of which half
			// serves writes; a declaration is not evidence and must never
			// overwrite one. A projection that tracked config here would tell
			// an operator the write path is in the region that no longer holds
			// it — worse than the defect being fixed.
			name:    "failed-over CNPG pair: observed role beats the stale declaration",
			depID:   "dep-6347-failedover",
			appName: "shared-pg",
			queryNS: "shared-data",
			objsA: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-1", "shared-pg", nodeRegionA, cnpgRoleReplica),
				spineApplicationFixtureCRWithPlacement("shared-pg", "bp-postgres-shared", "flux-system",
					"active-hot-standby", []string{declRegionA, declRegionB}),
				helmReleaseFixture("bp-postgres-shared", "flux-system", "shared-pg", "shared-data"),
			},
			objsB: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-r-1", "shared-pg", nodeRegionB, cnpgRolePrimary),
			},
			wantTargets:      2,
			wantPrimaries:    1,
			wantStandbys:     1,
			wantDerived:      true,
			wantPrimaryRegn:  nodeRegionB,
			wantStandbyRegn:  nodeRegionA,
			wantStandbyType:  bpv1.StandbyHot,
			wantStandbyClstr: true,
			wantPattern:      bpv1.PatternActiveHotStandby,
			why:              "runtime role evidence is the authority; the declaration only speaks where no evidence exists",
		},
		{
			// A GENUINELY SINGLE-REGION APP. One declared region, one occupied
			// region, no Continuum. Nothing may invent a second leg: the honest
			// answer is one Primary and the `singleton` pattern.
			name:    "single-region app: no fabricated standby",
			depID:   "dep-6347-singleton",
			appName: "spine-powerdns",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				identityFixturePod("powerdns", "powerdns-0", "powerdns", nodeRegionA, ""),
				spineApplicationFixtureCRWithPlacement("spine-powerdns", "bp-powerdns", "flux-system",
					"singleton", []string{declRegionA}),
				helmReleaseFixture("bp-powerdns", "flux-system", "powerdns", "powerdns"),
			},
			objsB:           []runtime.Object{},
			wantTargets:     1,
			wantPrimaries:   1,
			wantStandbys:    0,
			wantDerived:     true, // both of the DEPLOYMENT's clusters were listed, so the #6015 coverage gate is satisfied; the app simply occupies one of them
			wantPrimaryRegn: nodeRegionA,
			wantPattern:     bpv1.PatternSingleton,
			why:             "a one-region app must stay one Primary — the declaration names no standby region to promote",
		},
		{
			// #4551 MUST SURVIVE. Region B is genuinely empty here, so the
			// declaration-derived Standby is the ONLY thing that can tell the
			// Topology tab a standby region exists. Suppressing it whenever a
			// runtime Primary is present would trade this defect for the one
			// #4551 closed. Its `cluster` stays empty precisely because nothing
			// was observed there.
			name:    "standby region genuinely empty: the Continuum-derived leg is still emitted",
			depID:   "dep-6347-declonly",
			appName: "spine-keycloak",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				identityFixturePod("keycloak", "keycloak-0", "keycloak", nodeRegionA, ""),
				spineApplicationFixtureCRWithPlacement("spine-keycloak", "bp-keycloak", "flux-system",
					"active-hot-standby", []string{declRegionA, declRegionB}),
				helmReleaseFixture("bp-keycloak", "flux-system", "keycloak", "keycloak"),
			},
			objsB: []runtime.Object{},
			crs: []runtime.Object{
				newContinuumUnstructured("dr-spine-keycloak", "catalyst", "spine-keycloak", declRegionA, []string{declRegionB}),
			},
			wantTargets:      2,
			wantPrimaries:    1,
			wantStandbys:     1,
			wantDerived:      true,
			wantPrimaryRegn:  nodeRegionA,
			wantStandbyRegn:  declRegionB,
			wantStandbyType:  bpv1.StandbyHot,
			wantStandbyClstr: false,
			wantPattern:      bpv1.PatternActiveHotStandby,
			why:              "no leg was observed in region B, so the declaration is the only standby signal there — never suppress it",
		},
		{
			// OCCUPIED ONLY IN THE DECLARED STANDBY REGION. Pre-fix this
			// answers Primary(hw-…-b) plus an appended `cluster: ""` Standby
			// naming me-east-215-b — the SAME region twice, spelled two ways,
			// rendered as a cross-region pair. The honest answer is a half-pair:
			// the region that serves writes was not observed, so #6268's
			// refusal must fire rather than promote the standby leg to writer.
			name:    "occupied only in the declared STANDBY region: honest half-pair, not a false primary",
			depID:   "dep-6347-standbyonly",
			appName: "spine-gitea",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				spineApplicationFixtureCRWithPlacement("spine-gitea", "bp-gitea", "flux-system",
					"active-hot-standby", []string{declRegionA, declRegionB}),
				helmReleaseFixture("bp-gitea", "flux-system", "gitea", "gitea"),
			},
			objsB: []runtime.Object{
				identityFixturePod("gitea", "gitea-75d9f486fb-qm4kt", "gitea", nodeRegionB, ""),
			},
			crs: []runtime.Object{
				newContinuumUnstructured("dr-spine-gitea", "catalyst", "spine-gitea", declRegionA, []string{declRegionB}),
			},
			wantTargets:      1,
			wantPrimaries:    0,
			wantStandbys:     1,
			wantDerived:      false,
			wantUnresolvedPr: true,
			wantStandbyRegn:  nodeRegionB,
			wantStandbyType:  bpv1.StandbyHot,
			wantStandbyClstr: true,
			wantPattern:      bpv1.PatternNotReported,
			why:              "one leg in the declared standby region is not a placement — never two legs spelling one region",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdentityPlacementHandler(t, tc.depID, declRegionA, declRegionB, tc.objsA, tc.objsB, tc.crs)
			resp := callPlacementNS(t, h, tc.depID, tc.appName, tc.queryNS)

			primaries, standbys := 0, 0
			for _, tgt := range resp.Targets {
				switch tgt.Role {
				case bpv1.DataRolePrimary:
					primaries++
				case bpv1.DataRoleStandby:
					standbys++
				}
			}
			if len(resp.Targets) != tc.wantTargets || primaries != tc.wantPrimaries || standbys != tc.wantStandbys {
				t.Fatalf("#6347 %s: got %d target(s) (%d Primary, %d Standby) want %d (%d Primary, %d Standby) — %s\ntargets=%+v derivedFromRuntime=%v unresolvedPrimary=%v",
					tc.appName, len(resp.Targets), primaries, standbys,
					tc.wantTargets, tc.wantPrimaries, tc.wantStandbys, tc.why,
					resp.Targets, resp.DerivedFromRuntime, resp.UnresolvedPrimary)
			}
			if resp.DerivedFromRuntime != tc.wantDerived {
				t.Fatalf("#6347 %s: derivedFromRuntime=%v want %v (targets=%+v)",
					tc.appName, resp.DerivedFromRuntime, tc.wantDerived, resp.Targets)
			}
			if resp.UnresolvedPrimary != tc.wantUnresolvedPr {
				t.Fatalf("#6347 %s: unresolvedPrimary=%v want %v — a Standby with no Primary renders identically to an honest single-region app (targets=%+v)",
					tc.appName, resp.UnresolvedPrimary, tc.wantUnresolvedPr, resp.Targets)
			}

			primary := targetByRole(resp.Targets, bpv1.DataRolePrimary)
			standby := targetByRole(resp.Targets, bpv1.DataRoleStandby)
			if tc.wantPrimaryRegn != "" {
				if primary == nil || primary.Region != tc.wantPrimaryRegn {
					t.Fatalf("#6347 %s: Primary region %+v want %q", tc.appName, primary, tc.wantPrimaryRegn)
				}
				if primary.Cluster == "" {
					t.Fatalf("#6347 %s: Primary carries an EMPTY cluster (%+v) — an observed leg always names the cluster it ran on", tc.appName, *primary)
				}
				if primary.StandbyType != "" {
					t.Fatalf("#6347 %s: Primary carries standbyType %q — ValidatePlacement rejects that outright (PrimaryHasStandbyType)", tc.appName, primary.StandbyType)
				}
			}
			if tc.wantStandbyRegn != "" {
				if standby == nil || standby.Region != tc.wantStandbyRegn {
					t.Fatalf("#6347 %s: Standby region %+v want %q", tc.appName, standby, tc.wantStandbyRegn)
				}
				if standby.StandbyType != tc.wantStandbyType {
					t.Fatalf("#6347 %s: Standby type %q want %q", tc.appName, standby.StandbyType, tc.wantStandbyType)
				}
				if gotCluster := standby.Cluster != ""; gotCluster != tc.wantStandbyClstr {
					t.Fatalf("#6347 %s: Standby cluster=%q (set=%v) want set=%v — an OBSERVED leg names its cluster, a declaration-derived one cannot (%+v)",
						tc.appName, standby.Cluster, gotCluster, tc.wantStandbyClstr, *standby)
				}
			}
			if tc.wantPattern != "" {
				if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != tc.wantPattern {
					t.Fatalf("#6347 %s: pattern %q want %q (targets=%+v)", tc.appName, got, tc.wantPattern, resp.Targets)
				}
			}
			// The whole point of the row: an app whose Blueprint capability is
			// the default primary+standby must project a placement its OWN
			// validator accepts. Two Primaries is MultiPrimaryNotSupported.
			if len(resp.Targets) > 0 && tc.wantPattern != bpv1.PatternNotReported {
				if err := bpv1.ValidatePlacement(bpv1.Placement{Targets: resp.Targets}, bpv1.CapabilityPrimaryStandby); err != nil {
					t.Fatalf("#6347 %s: the projected placement fails its own validator: %v (targets=%+v)", tc.appName, err, resp.Targets)
				}
			}
		})
	}
}
