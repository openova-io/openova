// dashboard_org_block_leak_test.go — UAT row 103.
//
// Clause: "A per-Org vCluster block contains ONLY that Organization's workloads
// — no cross-Org leakage into another Org's block."
//
// #5932 made the vCluster treemap layer derive per-Org blocks from the vCluster
// syncer's own `vcluster.loft.sh/managed-by` label, and pinned that two
// Organizations do not collapse into one block
// (TestVclustersFromRuntime_PerOrgBlocksDoNotCollapse). That test's fixtures
// deliberately carry NO `catalyst.openova.io/vcluster-role` label, so it never
// exercised the case this file covers: a namespace that carries BOTH.
//
// buildPodRows used to read the role label FIRST and fall through to the per-Org
// signal only when it was absent. The role label's value space is mgmt / dmz /
// rtz — a ROLE, shared by construction across every namespace that carries it —
// so two Organizations whose namespaces both carry it land in ONE block named
// after the role. That is cross-Org leakage, and it is silent: the layer renders
// a plausible-looking block rather than failing.
//
// This is reachable, not archaeology. The three charts that stamp the label
// still exist and still stamp it:
//
//	platform/bp-mgmt-vcluster/chart/templates/namespace.yaml:28
//	platform/bp-dmz-vcluster/chart/templates/namespace.yaml:27
//	platform/bp-rtz-vcluster/chart/templates/namespace.yaml:23
//
// and clusters/omantel.omani.works/bootstrap-kit/kustomization.yaml:53 still
// lists slot 54-bp-dmz-vcluster.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mkOrgNamespaceWithRole is an Organization's host namespace on an estate that
// ALSO still carries the legacy role label — the mixed state this test exists
// for. It reuses mkOrgNamespace's shape so the only difference from the #5932
// fixture is the extra label.
func mkOrgNamespaceWithRole(name, org, role string) *unstructured.Unstructured {
	ns := mkOrgNamespace(name, org)
	meta := ns.Object["metadata"].(map[string]any)
	meta["labels"].(map[string]any)["catalyst.openova.io/vcluster-role"] = role
	return ns
}

// TestVclusterBlocks_LegacyRoleLabelDoesNotMergeTwoOrganizations is the row-103
// leakage gate.
//
// Pre-fix both Organizations bucket to "mgmt" and the assertions below fail;
// post-fix each buckets to its own Organization slug.
func TestVclusterBlocks_LegacyRoleLabelDoesNotMergeTwoOrganizations(t *testing.T) {
	namespaces := []*unstructured.Unstructured{
		mkOrgNamespaceWithRole("uatco", "uatco", "mgmt"),
		mkOrgNamespaceWithRole("walk-stranger-two", "walk-stranger-two", "mgmt"),
	}
	pods := []*unstructured.Unstructured{
		mkSyncedPod("uatco", "coredns-0", "coredns"),
		mkSyncedPod("walk-stranger-two", "coredns-0", "coredns"),
	}

	// Vacuity guard: if the fixtures lost the legacy label this test would
	// silently degrade into a duplicate of the #5932 collapse test and pass on
	// the pre-fix code.
	for _, ns := range namespaces {
		if ns.GetLabels()["catalyst.openova.io/vcluster-role"] == "" {
			t.Fatalf("fixture %s lost the legacy role label — the test would not "+
				"exercise the precedence bug it exists for", ns.GetName())
		}
		if ns.GetLabels()["openova.io/organization"] == "" {
			t.Fatalf("fixture %s lost its Organization label", ns.GetName())
		}
	}

	rows := buildPodRows(pods, nil, nil, namespaces, nil, nil, "cid", "")
	got := map[string]string{}
	for _, r := range rows {
		id, _ := dimensionKey(r, "vcluster")
		got[r.namespace] = id
	}

	if got["uatco"] == got["walk-stranger-two"] {
		t.Errorf("two Organizations merged into ONE vCluster block %q — a workload "+
			"of walk-stranger-two is rendered inside uatco's block (UAT row 103). "+
			"The legacy catalyst.openova.io/vcluster-role label names a ROLE, "+
			"which is shared across Organizations, so it must not outrank the "+
			"per-Org signal", got["uatco"])
	}
	if got["uatco"] != "uatco" {
		t.Errorf("uatco pod bucketed to %q, want its own Organization block %q",
			got["uatco"], "uatco")
	}
	if got["walk-stranger-two"] != "walk-stranger-two" {
		t.Errorf("walk-stranger-two pod bucketed to %q, want its own Organization block",
			got["walk-stranger-two"])
	}
}

// TestVclusterBlocks_OrganizationLayerNeverLeaks is the companion assertion on
// the `organization` group_by dimension itself: a pod's Organization is its
// NAMESPACE's Organization, never another namespace's, even when the pod carries
// a contradicting pod-level org label.
//
// This one PASSES pre-fix — it pins behaviour that is already correct rather
// than proving the fix. It is here because without it the row-103 claim would
// rest entirely on the vCluster layer, and the treemap also offers grouping by
// organization directly, where a pod-level label winning would leak one Org's
// workload into another Org's cell by a different route.
func TestVclusterBlocks_OrganizationLayerNeverLeaks(t *testing.T) {
	namespaces := []*unstructured.Unstructured{
		mkOrgNamespace("uatco", "uatco"),
	}
	stray := mkSyncedPod("uatco", "stray-0", "stray")
	stray.Object["metadata"].(map[string]any)["labels"].(map[string]any)["catalyst.openova.io/organization"] = "walk-stranger-two"
	pods := []*unstructured.Unstructured{stray}

	rows := buildPodRows(pods, nil, nil, namespaces, nil, nil, "cid", "")
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	id, _ := dimensionKey(rows[0], "organization")
	if id != "uatco" {
		t.Errorf("pod running in uatco's namespace attributed to %q — the host "+
			"namespace's Organization label is the boundary owner and must win "+
			"over a pod-level label (UAT row 103)", id)
	}
}
