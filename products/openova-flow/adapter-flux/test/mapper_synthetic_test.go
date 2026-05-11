// mapper_synthetic_test.go — covers the synthetic phase + region
// nodes the adapter emits per Agent #6 brief:
//
//   - 1 region root with meta.layout=lane-vertical
//   - 4 phase nodes (phase-0..phase-3) with meta.layout=lane-horizontal
//     and meta.sortKey=0..3
//   - 3 finish-to-start edges chaining phases (0→1, 1→2, 2→3)
//   - Per HR: TWO `contains` edges (one to region, one to phase)
//   - Phase derivation: slot-label → phase-1; component=cutover → phase-2;
//     bp-catalyst-platform → phase-3
package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/informer"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func mustParse(t *testing.T, raw string) *unstructured.Unstructured {
	t.Helper()
	js, err := yaml.YAMLToJSON([]byte(strings.TrimSpace(raw)))
	if err != nil {
		t.Fatalf("yaml->json: %v", err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(js); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return u
}

func TestSynthetic_PhaseNodes_PerRegion(t *testing.T) {
	nodes := informer.BuildPhaseNodes("fsn1")
	if len(nodes) != 4 {
		t.Fatalf("phase nodes: %d want 4", len(nodes))
	}

	wantIDs := []string{"fsn1/phase-0", "fsn1/phase-1", "fsn1/phase-2", "fsn1/phase-3"}
	for i, n := range nodes {
		if n.ID != wantIDs[i] {
			t.Fatalf("nodes[%d].id = %s want %s", i, n.ID, wantIDs[i])
		}
		if n.Family == nil || *n.Family != "phase" {
			t.Fatalf("nodes[%d].family = %+v want phase", i, n.Family)
		}
		if n.Region == nil || *n.Region != "fsn1" {
			t.Fatalf("nodes[%d].region = %+v want fsn1", i, n.Region)
		}
		if n.Meta["layout"] != "lane-horizontal" {
			t.Fatalf("nodes[%d].meta.layout = %+v want lane-horizontal", i, n.Meta["layout"])
		}
		if n.Meta["isGroup"] != true {
			t.Fatalf("nodes[%d].meta.isGroup = %+v want true", i, n.Meta["isGroup"])
		}
		if n.Meta["sortKey"] != i {
			t.Fatalf("nodes[%d].meta.sortKey = %+v want %d", i, n.Meta["sortKey"], i)
		}
	}

	// Phase 0 is special — adapter sees HRs only after Phase 0 done.
	if nodes[0].Status != "succeeded" {
		t.Fatalf("phase-0 status = %s want succeeded", nodes[0].Status)
	}
	for i := 1; i < 4; i++ {
		if nodes[i].Status != "pending" {
			t.Fatalf("phase-%d status = %s want pending", i, nodes[i].Status)
		}
	}
}

func TestSynthetic_PhaseEdges_PerRegion(t *testing.T) {
	edges := informer.BuildPhaseEdges("hel1-1")
	if len(edges) != 3 {
		t.Fatalf("phase edges: %d want 3", len(edges))
	}
	want := [][2]string{
		{"hel1-1/phase-0", "hel1-1/phase-1"},
		{"hel1-1/phase-1", "hel1-1/phase-2"},
		{"hel1-1/phase-2", "hel1-1/phase-3"},
	}
	for i, e := range edges {
		if e.FromID != want[i][0] || e.ToID != want[i][1] {
			t.Fatalf("edges[%d] = (%s→%s) want (%s→%s)", i, e.FromID, e.ToID, want[i][0], want[i][1])
		}
		if e.Type != "finish-to-start" {
			t.Fatalf("edges[%d].type = %s want finish-to-start", i, e.Type)
		}
		if e.Condition != "on-success" {
			t.Fatalf("edges[%d].condition = %s want on-success", i, e.Condition)
		}
	}
}

func TestSynthetic_HR_Slot_GoesToPhase1(t *testing.T) {
	hr := mustParse(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cilium
  labels:
    catalyst.openova.io/slot: "01"
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, ok := informer.BuildFromHR(hr, "fsn1")
	if !ok {
		t.Fatal("BuildFromHR returned not-ok")
	}
	if res.PhaseID != "fsn1/phase-1" {
		t.Fatalf("phaseID = %s want fsn1/phase-1", res.PhaseID)
	}
	wantContains := map[string]bool{
		"fsn1->fsn1/bp-cilium":         false,
		"fsn1/phase-1->fsn1/bp-cilium": false,
	}
	for _, r := range res.Relationships {
		if r.Type == "contains" {
			key := fmt.Sprintf("%s->%s", r.FromID, r.ToID)
			if _, ok := wantContains[key]; ok {
				wantContains[key] = true
			}
		}
	}
	for k, v := range wantContains {
		if !v {
			t.Fatalf("missing contains edge: %s (rels=%+v)", k, res.Relationships)
		}
	}
}

func TestSynthetic_HR_CutoverComponent_GoesToPhase2(t *testing.T) {
	hr := mustParse(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-self-sovereign-cutover
  labels:
    catalyst.openova.io/component: cutover
    catalyst.openova.io/slot: "06a"
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.PhaseID != "fsn1/phase-2" {
		t.Fatalf("phaseID = %s want fsn1/phase-2 (component=cutover must win over slot)", res.PhaseID)
	}
}

func TestSynthetic_HR_CutoverNameFallback(t *testing.T) {
	// No component label set — HR-name fallback kicks in.
	hr := mustParse(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-self-sovereign-cutover
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.PhaseID != "fsn1/phase-2" {
		t.Fatalf("phaseID = %s want fsn1/phase-2 (name fallback)", res.PhaseID)
	}
}

func TestSynthetic_HR_CatalystPlatform_GoesToPhase3(t *testing.T) {
	hr := mustParse(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-catalyst-platform
  labels:
    catalyst.openova.io/slot: "13"
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.PhaseID != "fsn1/phase-3" {
		t.Fatalf("phaseID = %s want fsn1/phase-3", res.PhaseID)
	}
}

func TestSynthetic_HR_NoLabels_DefaultsToPhase1(t *testing.T) {
	hr := mustParse(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-grafana
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.PhaseID != "fsn1/phase-1" {
		t.Fatalf("phaseID = %s want fsn1/phase-1 (default)", res.PhaseID)
	}
}

// TestSynthetic_FortyThreeMockHRs — the brief's headline acceptance:
// given 43 mock HRs across slots, the synthetic structure emits the
// expected counts.
func TestSynthetic_FortyThreeMockHRs(t *testing.T) {
	// Slots 01..57 with gaps that match clusters/_template/bootstrap-kit
	// (43 real slots in current bootstrap-kit).
	slots := []string{
		"01", "01a", "02", "03", "04", "05", "05a", "06a", "07", "08",
		"09", "10", "11", "12", "13", "14", "15", "15a", "16", "17",
		"18", "19", "20", "21", "22", "23", "24", "25", "27", "28",
		"29", "30", "31", "32", "33", "34", "35", "49", "50", "51",
		"52", "55", "56",
	}
	if len(slots) != 43 {
		t.Fatalf("setup error: %d slots, expected 43", len(slots))
	}

	region := "fsn1"
	regionRels := 0
	phase1Rels := 0
	phase2Rels := 0
	phase3Rels := 0
	for _, s := range slots {
		// Mock HR — name + slot label. Mark slot 06a as cutover
		// (component label) + slot 13 as catalyst-platform via name.
		name := fmt.Sprintf("bp-mock-%s", s)
		extraLabel := ""
		if s == "06a" {
			name = "bp-self-sovereign-cutover"
			extraLabel = "    catalyst.openova.io/component: cutover\n"
		}
		if s == "13" {
			name = "bp-catalyst-platform"
		}
		yamlStr := fmt.Sprintf(`
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %s
  labels:
    catalyst.openova.io/slot: "%s"
%sstatus:
  conditions:
    - type: Ready
      status: "True"
`, name, s, extraLabel)
		hr := mustParse(t, yamlStr)
		res, ok := informer.BuildFromHR(hr, region)
		if !ok {
			t.Fatalf("slot %s: BuildFromHR failed", s)
		}
		for _, r := range res.Relationships {
			if r.Type != "contains" {
				continue
			}
			switch r.FromID {
			case region:
				regionRels++
			case region + "/phase-1":
				phase1Rels++
			case region + "/phase-2":
				phase2Rels++
			case region + "/phase-3":
				phase3Rels++
			}
		}
	}

	if regionRels != 43 {
		t.Fatalf("region contains: %d want 43", regionRels)
	}
	if phase2Rels != 1 {
		t.Fatalf("phase-2 contains: %d want 1 (cutover)", phase2Rels)
	}
	if phase3Rels != 1 {
		t.Fatalf("phase-3 contains: %d want 1 (catalyst-platform)", phase3Rels)
	}
	if phase1Rels != 41 {
		t.Fatalf("phase-1 contains: %d want 41 (43 - 1 cutover - 1 platform)", phase1Rels)
	}
	if total := phase1Rels + phase2Rels + phase3Rels; total != 43 {
		t.Fatalf("phase contains total: %d want 43", total)
	}

	// Synthetic skeleton: 1 region + 4 phase nodes + 3 FS edges.
	region2 := informer.BuildRegionNode(region)
	if region2.ID != region {
		t.Fatalf("region node id: %s", region2.ID)
	}
	phaseNodes := informer.BuildPhaseNodes(region)
	if len(phaseNodes) != 4 {
		t.Fatalf("phase nodes: %d want 4", len(phaseNodes))
	}
	phaseEdges := informer.BuildPhaseEdges(region)
	if len(phaseEdges) != 3 {
		t.Fatalf("phase edges: %d want 3", len(phaseEdges))
	}
}

// TestSynthetic_RegionScopedIDs — multi-region: phase IDs MUST be
// scoped under the region key so two adapters in two regions don't
// collide.
func TestSynthetic_RegionScopedIDs(t *testing.T) {
	for _, region := range []string{"fsn1", "hel1-1", "default"} {
		t.Run(region, func(t *testing.T) {
			nodes := informer.BuildPhaseNodes(region)
			for _, n := range nodes {
				if !strings.HasPrefix(n.ID, region+"/phase-") {
					t.Fatalf("node id %s not scoped under region %s", n.ID, region)
				}
			}
			edges := informer.BuildPhaseEdges(region)
			for _, e := range edges {
				if !strings.HasPrefix(e.FromID, region+"/phase-") || !strings.HasPrefix(e.ToID, region+"/phase-") {
					t.Fatalf("edge (%s→%s) not scoped under region %s", e.FromID, e.ToID, region)
				}
			}
		})
	}
}

// TestSynthetic_DefaultRegionFallback — empty region key defaults to
// "default" (mirrors BuildFromHR behaviour).
func TestSynthetic_DefaultRegionFallback(t *testing.T) {
	nodes := informer.BuildPhaseNodes("")
	if nodes[0].ID != "default/phase-0" {
		t.Fatalf("default region id: %s", nodes[0].ID)
	}
	edges := informer.BuildPhaseEdges("")
	if edges[0].FromID != "default/phase-0" {
		t.Fatalf("default region edge: %+v", edges[0])
	}
}
