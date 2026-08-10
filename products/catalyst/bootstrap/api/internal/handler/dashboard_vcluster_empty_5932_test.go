package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mkVClusterControlPlanePod is the loft-sh vCluster StatefulSet pod that runs
// in the Organization's HOST namespace. It is NOT a synced pod: the syncer
// writes `vcluster.loft.sh/managed-by` on the workloads it mirrors DOWN from
// the virtual cluster, never on the control plane that does the mirroring.
//
// The label set here is the one measured live on hw292 region A (read-only,
// `kubectl get pod -n uatco -l app=vcluster -o jsonpath='{...labels}'`):
//
//	{"app":"vcluster","apps.kubernetes.io/pod-index":"0",
//	 "controller-revision-hash":"vcluster-68b879b4db",
//	 "policy.cilium.io/enforced":"true","release":"vcluster",
//	 "statefulset.kubernetes.io/pod-name":"vcluster-0"}
//
// — no managed-by key, which is precisely why it was invisible to #5932.
func mkVClusterControlPlanePod(ns string) *unstructured.Unstructured {
	p := mkDashPod(dashFixturePod{
		Namespace: ns, Name: "vcluster-0", Application: "vcluster", Family: "",
		CPURequest: "100m", Ready: true,
	})
	labels := p.Object["metadata"].(map[string]any)["labels"].(map[string]any)
	labels["app"] = "vcluster"
	labels["release"] = "vcluster"
	labels["statefulset.kubernetes.io/pod-name"] = "vcluster-0"
	return p
}

// UAT row 98 — "LAYER 1 → vCluster renders a `host` block PLUS one block per
// per-Org vCluster, each clickable. VACUITY GUARD: a lone `host` block is a
// FAIL, not an empty environment."
//
// #5932/#5937 made the treemap group on the SYNCER label, which only appears
// once an Organization has deployed something into its vCluster. An
// Organization whose vCluster control plane is Running with nothing inside it
// yet still produced zero map entries, so its pods — the `vcluster-0` control
// plane included — bucketed to "host" and the Org rendered no block at all.
// That is the row-98 lone-`host` FAIL, reached from the freshest state an
// Organization can be in.
//
// The fixtures deliberately carry NEITHER the retired
// `catalyst.openova.io/vcluster-role` label (#4325 deleted its producers) NOR
// any synced pod in the empty Org, so the ONLY thing that can produce a block
// for `walk-stranger-two` is the control-plane signal under test.
func TestVclustersFromRuntime_EmptyVClusterStillRendersItsOwnBlock_UAT98(t *testing.T) {
	namespaces := []*unstructured.Unstructured{
		// Populated Org: control plane + a synced workload.
		mkOrgNamespace("uatco", "uatco"),
		// Empty Org: control plane Running, nothing deployed inside it.
		mkOrgNamespace("walk-stranger-two", "walk-stranger-two"),
		mkDashNamespace("catalyst", "", ""), // host
	}
	pods := []*unstructured.Unstructured{
		mkVClusterControlPlanePod("uatco"),
		mkSyncedPod("uatco", "wordpress-0", "wordpress"),
		mkVClusterControlPlanePod("walk-stranger-two"),
		mkDashPod(dashFixturePod{Namespace: "catalyst", Name: "api-0",
			Application: "catalyst-api", CPURequest: "100m", Ready: true}),
	}

	// Fixture integrity: the retired label must be absent everywhere, and the
	// empty Org must have NO synced pod. Without both, this test could pass
	// on the pre-fix code and prove nothing.
	for _, ns := range namespaces {
		if _, bad := ns.GetLabels()["catalyst.openova.io/vcluster-role"]; bad {
			t.Fatalf("fixture namespace %s carries the retired vcluster-role label; the test would not exercise the fix", ns.GetName())
		}
	}
	for _, p := range pods {
		if p.GetNamespace() != "walk-stranger-two" {
			continue
		}
		if _, synced := p.GetLabels()["vcluster.loft.sh/managed-by"]; synced {
			t.Fatalf("fixture pod %s/%s is syncer-labelled; the empty-vCluster case is not being exercised",
				p.GetNamespace(), p.GetName())
		}
	}

	rows := buildPodRows(pods, nil, nil, namespaces, nil, nil, "cid", "")

	blocks := map[string]int{}
	for _, r := range rows {
		key := r.vcluster
		if key == "" {
			key = "host"
		}
		blocks[key]++
	}

	// The clause's own vacuity guard, asserted first.
	if len(blocks) == 1 {
		if _, lone := blocks["host"]; lone {
			t.Fatalf("LAYER1=vCluster rendered a LONE `host` block holding %d items — "+
				"row 98 declares that a FAIL, not an empty environment", blocks["host"])
		}
	}

	for _, want := range []string{"host", "uatco", "walk-stranger-two"} {
		if blocks[want] == 0 {
			t.Errorf("no vCluster block for %q; got blocks %v. "+
				"An Organization whose vCluster is Running but empty must still render its own block.",
				want, blocks)
		}
	}

	// The empty Org's control-plane pod must be IN its own block, not leaked
	// into host. Counting blocks alone would pass if the pod were double
	// counted or mis-filed.
	for _, r := range rows {
		if r.namespace == "walk-stranger-two" && r.vcluster != "walk-stranger-two" {
			t.Errorf("pod %s/%s filed under vCluster block %q, want %q",
				r.namespace, r.application, r.vcluster, "walk-stranger-two")
		}
	}
}

// CONTROL — the control-plane signal must NOT invent a block for a namespace
// that merely runs a workload whose app label happens to start with the same
// word, and must NOT invent one for an ordinary host namespace. Without this,
// the fix above could be satisfied by "every namespace gets a block", which
// would make the row-98 assertion vacuous in the other direction.
func TestVclustersFromRuntime_ControlPlaneSignalDoesNotOverMatch_UAT98(t *testing.T) {
	namespaces := []*unstructured.Unstructured{
		mkOrgNamespace("uatco", "uatco"),
		mkDashNamespace("catalyst", "", ""),
		mkDashNamespace("vcluster-system", "", ""),
	}

	nearMiss := mkDashPod(dashFixturePod{
		Namespace: "vcluster-system", Name: "vcluster-operator-0",
		Application: "vcluster-operator", CPURequest: "50m", Ready: true,
	})
	// app=vcluster-operator, NOT app=vcluster.
	nearMiss.Object["metadata"].(map[string]any)["labels"].(map[string]any)["app"] = "vcluster-operator"

	pods := []*unstructured.Unstructured{
		mkVClusterControlPlanePod("uatco"),
		nearMiss,
		mkDashPod(dashFixturePod{Namespace: "catalyst", Name: "api-0",
			Application: "catalyst-api", CPURequest: "100m", Ready: true}),
	}

	rows := buildPodRows(pods, nil, nil, namespaces, nil, nil, "cid", "")

	got := map[string]string{}
	for _, r := range rows {
		key := r.vcluster
		if key == "" {
			key = "host"
		}
		got[r.namespace+"/"+r.application] = key
	}

	if v := got["vcluster-system/vcluster-operator"]; v != "host" {
		t.Errorf("app=vcluster-operator minted vCluster block %q; exact-value matching on app=vcluster must not fire on a near-miss label", v)
	}
	if v := got["catalyst/catalyst-api"]; v != "host" {
		t.Errorf("plain host pod filed under vCluster block %q, want host", v)
	}
	if v := got["uatco/vcluster"]; v != "uatco" {
		t.Errorf("the real control plane filed under %q, want uatco — this control is only meaningful if the positive case still fires", v)
	}
}
