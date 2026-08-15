package handler

// applications_placement_declared_roles_unit_6347_test.go — the half of the
// #6347 contract the endpoint table cannot see. It lives in its own file
// because it names API that does not exist on the pre-fix tree the table test
// is watched RED against.
//
// Two things are pinned here that a passing endpoint test would NOT catch:
//
//  1. the region fold actually folds (a normalizer that returned its input
//     verbatim would leave the endpoint table green ONLY because the fixtures
//     happen to line up — so both directions and the pass-through cases are
//     asserted directly);
//  2. the declaration stays SILENT everywhere it has no standing — singleton,
//     active-active, one distinct region, a region it never names, and a CR
//     that declares nothing at all. Silence is what keeps every component
//     without an Application CR on its pre-#6347 projection.

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func TestNormalizePlacementRegion_6347(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{"hw-me-east-215-a-rtz-prod", "me-east-215-a", "the openova.io/region node label folds to the cloud region the CR declares"},
		{"hw-me-east-215-b-rtz-prod", "me-east-215-b", "same, region B"},
		{"hz-fsn-rtz-prod", "fsn", "the 4-segment CLUSTER shape: provider first, building-block + env_type last"},
		{"hz-hel-mgmt-staging", "hel", "every token set member, not just the rtz/prod pair"},
		{"me-east-215-a", "me-east-215-a", "IDEMPOTENT: an already-bare region is returned unchanged"},
		{"fsn1", "fsn1", "short bare region, fewer than 4 segments"},
		{"platform-bootstrap-owned-host", "platform-bootstrap-owned-host", "the host placeholder is not a cluster name — surfaced verbatim, never mangled"},
		{"primary", "primary", "the seed door's literal — visibly unrecognised beats silently folded"},
		{"aws-eu-west-1-dmz-uat", "eu-west-1", "provider/bb/env tokens outside the Huawei pair"},
		{"me-east-215-a-rtz-prod", "me-east-215-a-rtz-prod", "first segment is not a provider ⇒ not a cluster name ⇒ verbatim"},
		{"hw-me-east-215-a-rtz-qa", "hw-me-east-215-a-rtz-qa", "unknown env_type ⇒ verbatim, the closed set is the anchor"},
		{"  hw-me-east-215-a-rtz-prod  ", "me-east-215-a", "trimmed before matching"},
		{"", "", "empty stays empty and never compares equal to anything"},
	}
	for _, c := range cases {
		if got := normalizePlacementRegion(c.in); got != c.want {
			t.Errorf("normalizePlacementRegion(%q) = %q want %q — %s", c.in, got, c.want, c.why)
		}
	}

	// The comparison built on it: two spellings of one place are equal, two
	// places are not, and empty never matches (an empty region must never
	// silently satisfy a "same region" test).
	if !samePlacementRegion("hw-me-east-215-b-rtz-prod", "me-east-215-b") {
		t.Error("samePlacementRegion: the node label and the declared region name ONE place and must compare equal — this is the comparison that failed on hw298")
	}
	if samePlacementRegion("hw-me-east-215-a-rtz-prod", "me-east-215-b") {
		t.Error("samePlacementRegion: region A must not match region B")
	}
	if samePlacementRegion("", "") {
		t.Error("samePlacementRegion(\"\",\"\") must be false — an unset region is not a place")
	}
}

func TestDeclaredPlacementRoleForRegion_6347(t *testing.T) {
	ahs := declaredPlacement{mode: "active-hot-standby", regions: []string{"me-east-215-a", "me-east-215-b"}}
	ap := declaredPlacement{mode: "active-passive", regions: []string{"me-east-215-a", "me-east-215-b"}}

	cases := []struct {
		name        string
		d           declaredPlacement
		region      string
		wantRole    bpv1.DataRole
		wantStandby bpv1.StandbyType
		wantOK      bool
		why         string
	}{
		{"regions[0] is the primary", ahs, "hw-me-east-215-a-rtz-prod", bpv1.DataRolePrimary, "", true,
			"the rule placement.Resolve and placementRegionCountError both state"},
		{"regions[1..] are the standbys", ahs, "hw-me-east-215-b-rtz-prod", bpv1.DataRoleStandby, bpv1.StandbyHot, true,
			"a leg in a declared standby region is a follower, not a second writer"},
		{"declared spelling matches too", ahs, "me-east-215-b", bpv1.DataRoleStandby, bpv1.StandbyHot, true,
			"either vocabulary resolves — the fold works in both directions"},
		{"active-passive standbys are Cold", ap, "hw-me-east-215-b-rtz-prod", bpv1.DataRoleStandby, bpv1.StandbyCold, true,
			"Hot asserts a streaming replica nothing here observed; mirror the declared type, never the stronger claim"},
		{"a region the declaration never names", ahs, "hz-fsn-rtz-prod", "", "", false,
			"no standing ⇒ the runtime reading stands"},
		{"singleton declares no follower", declaredPlacement{mode: "singleton", regions: []string{"me-east-215-a"}}, "hw-me-east-215-a-rtz-prod", "", "", false,
			"a one-region posture has no role split to impose"},
		{"active-active is multi-primary BY DEFINITION", declaredPlacement{mode: "active-active", regions: []string{"me-east-215-a", "me-east-215-b"}}, "hw-me-east-215-b-rtz-prod", "", "", false,
			"both regions serve traffic — demoting one would be the mirror-image defect"},
		{"one distinct region under an asymmetric mode", declaredPlacement{mode: "active-hot-standby", regions: []string{"me-east-215-a", "me-east-215-a"}}, "hw-me-east-215-a-rtz-prod", "", "", false,
			"`[a,a]` is ONE place (placementRegionCountError) and cannot say which leg follows"},
		{"no declaration at all", declaredPlacement{}, "hw-me-east-215-a-rtz-prod", "", "", false,
			"a bootstrap-kit HelmRelease with no Application CR keeps its pre-#6347 projection"},
		{"empty region", ahs, "", "", "", false, "an unresolvable region gets no role"},
	}
	for _, c := range cases {
		role, standby, ok := c.d.roleForRegion(c.region)
		if ok != c.wantOK || role != c.wantRole || standby != c.wantStandby {
			t.Errorf("%s: roleForRegion(%q) = (%q, %q, %v) want (%q, %q, %v) — %s",
				c.name, c.region, role, standby, ok, c.wantRole, c.wantStandby, c.wantOK, c.why)
		}
	}
}

func TestDeclaredPlacementFromCR_6347(t *testing.T) {
	// The dual form the CRD accepts, both read through placementFromSpec: the
	// legacy bare string (what post_handover_spine_apps.go stamps) and the
	// #3373 object whose posture rides in `mode`.
	stringForm := spineApplicationFixtureCRWithPlacement("spine-gitea", "bp-gitea", "flux-system",
		"active-hot-standby", []string{"me-east-215-a", "me-east-215-b"})
	if d := declaredPlacementFromCR(stringForm); d.mode != "active-hot-standby" || len(d.regions) != 2 || d.regions[0] != "me-east-215-a" {
		t.Fatalf("string form: %+v want mode active-hot-standby over [me-east-215-a me-east-215-b] IN ORDER", d)
	}

	objForm := spineApplicationFixtureCR("spine-openbao", "bp-openbao", "flux-system")
	_ = unstructured.SetNestedMap(objForm.Object, map[string]any{
		"mode":    "active_passive",
		"regions": []any{"me-east-215-a", "me-east-215-b"},
	}, "spec", "placement")
	d := declaredPlacementFromCR(objForm)
	if d.mode != "active-passive" {
		t.Fatalf("object form: mode %q want the CANONICAL active-passive — one vocabulary regardless of stored spelling", d.mode)
	}
	if len(d.regions) != 2 || d.regions[1] != "me-east-215-b" {
		t.Fatalf("object form: regions %v want the placement.regions fallback when spec.regions is absent", d.regions)
	}

	// spec.regions[] is the slice placement.Resolve is actually called with, so
	// it wins when both are present.
	both := spineApplicationFixtureCRWithPlacement("spine-gitea", "bp-gitea", "flux-system",
		"active-hot-standby", []string{"me-east-215-a", "me-east-215-b"})
	_ = unstructured.SetNestedSlice(both.Object, []any{"decoy-region"}, "spec", "placement", "regions")
	if got := declaredPlacementFromCR(both); len(got.regions) != 2 || got.regions[0] != "me-east-215-a" {
		t.Fatalf("both present: regions %v want spec.regions — the list placement.Resolve reads", got.regions)
	}

	if got := declaredPlacementFromCR(nil); got.mode != "" || len(got.regions) != 0 {
		t.Fatalf("nil CR: %+v want the zero value — no CR, no opinion", got)
	}
	bare := spineApplicationFixtureCR("spine-harbor", "bp-harbor", "flux-system")
	if got := declaredPlacementFromCR(bare); got.asymmetric() {
		t.Fatalf("CR with no placement: %+v must not claim an asymmetric posture", got)
	}
}
