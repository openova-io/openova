package controller

// ADR-0015 D1/D2/D3 at the reconcile level: spec.placement is translated into
// one Git file per region, each with the D2 values contract; the controller
// applies nothing; a pre-ADR-0015 copy of another region's leg is retired;
// moving the Primary is a Git change only.

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

func hotStandbyApp(primaryCluster, standbyCluster string) *unstructured.Unstructured {
	region := map[string]string{"mgmt-A": testRegionA, "mgmt-B": testRegionB}
	return makeAppWithTargets("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{testRegionA, testRegionB},
		[]map[string]interface{}{
			{"region": region[primaryCluster], "cluster": primaryCluster, "vcluster": "mgmt", "role": "Primary"},
			{"region": region[standbyCluster], "cluster": standbyCluster, "vcluster": "mgmt", "role": "Standby", "standbyType": "Hot"},
		},
	)
}

func topologyIn(t *testing.T, objs []*unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	var hrs []*unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == "HelmRelease" {
			hrs = append(hrs, o)
		}
	}
	if len(hrs) != 1 {
		t.Fatalf("want 1 HelmRelease in the region file, got %d", len(hrs))
	}
	topo, ok, _ := unstructured.NestedMap(hrs[0].Object, "spec", "values", "openova", "topology")
	if !ok {
		t.Fatalf("%s carries no openova.topology", hrs[0].GetName())
	}
	return topo
}

// continuumIn returns the Continuum document of a region file (ADR-0015 D8:
// a governed Application's Continuum is delivered from Git in every region).
func continuumIn(t *testing.T, objs []*unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	for _, o := range objs {
		if o.GetKind() == "Continuum" {
			return o
		}
	}
	t.Fatalf("region file carries no Continuum document")
	return nil
}

func TestReconcile_ADR0015_PlacementBecomesOneFilePerRegion(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	// A pre-ADR-0015 controller upserted region B's leg INTO region A.
	stray := makeSeedFanoutHR("mgmt", "obs-mgmt-b", "obs", "uid-obs", "mgmt-B")
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, hotStandbyApp("mgmt-A", "mgmt-B"), env, org, bp, stray)

	reconcileFromCluster(t, r, "acme", "obs")

	a := topologyIn(t, regionFileObjs(t, fg, "acme", testRegionA, "obs"))
	b := topologyIn(t, regionFileObjs(t, fg, "acme", testRegionB, "obs"))
	if a["self"] != testRegionA || a["primary"] != testRegionA || b["self"] != testRegionB || b["primary"] != testRegionA {
		t.Fatalf("contracts: A=%v B=%v", a, b)
	}
	if a["mode"] != "active-hot-standby" {
		t.Errorf("mode = %v", a["mode"])
	}

	// Region A must no longer hold region B's leg.
	if _, err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace("mgmt").
		Get(context.Background(), "obs-mgmt-b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the stray region-B leg in region A was not retired: %v", err)
	}
	// And the controller applied nothing itself.
	list, _ := r.Dynamic.Resource(FluxHelmReleaseGVR).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatalf("the controller applied %d HelmReleases; delivery is Git only", len(list.Items))
	}

	// Status rows name their region; only region A's leg is observed here.
	got := readApp(t, r, "acme", "obs")
	rows, _, _ := unstructured.NestedSlice(got.Object, "status", "perCluster")
	seen := map[string]bool{}
	for _, row := range rows {
		m := row.(map[string]interface{})
		seen[m["region"].(string)] = true
	}
	if !seen[testRegionA] || !seen[testRegionB] {
		t.Fatalf("perCluster rows must carry both regions: %v", rows)
	}
}

func TestReconcile_ADR0015_MovingPrimaryIsAGitChange(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, hotStandbyApp("mgmt-A", "mgmt-B"), env, org, bp)
	reconcileFromCluster(t, r, "acme", "obs")

	// Swap the Primary to region B.
	cur := readApp(t, r, "acme", "obs")
	moved := hotStandbyApp("mgmt-B", "mgmt-A")
	cur.Object["spec"].(map[string]interface{})["placement"] = moved.Object["spec"].(map[string]interface{})["placement"]
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileFromCluster(t, r, "acme", "obs")

	// ADR-0015 D8: the placement edit moves the DESIRED primary (the
	// Continuum document in every region file); the HelmReleases keep the
	// committed primary, because moving it is Continuum's ordered commit
	// series (old primary first, then the new one with the CNPG token) —
	// never one commit to every region.
	objsA := regionFileObjs(t, fg, "acme", testRegionA, "obs")
	objsB := regionFileObjs(t, fg, "acme", testRegionB, "obs")
	a := topologyIn(t, objsA)
	b := topologyIn(t, objsB)
	if a["primary"] != testRegionA || b["primary"] != testRegionA {
		t.Fatalf("the controller must not move the committed primary itself: A=%v B=%v", a, b)
	}
	for _, objs := range [][]*unstructured.Unstructured{objsA, objsB} {
		c := continuumIn(t, objs)
		if d, _, _ := unstructured.NestedString(c.Object, "spec", "desiredPrimaryRegion"); d != testRegionB {
			t.Fatalf("Continuum desiredPrimaryRegion = %q, want %q", d, testRegionB)
		}
		if owner, _, _ := unstructured.NestedString(c.Object, "spec", "transition", "repo", "owner"); owner != "acme" {
			t.Fatalf("Continuum must name the Org repo it transitions: %v", c.Object["spec"])
		}
	}
	// No Continuum is created through the API any more.
	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").Get(context.Background(), "dr-obs", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Continuum must be delivered from Git, not created by API: %v", err)
	}

	// Continuum commits the transition to region A's file; a later
	// re-render keeps Continuum's fields (compare-and-swap, no revert).
	path := "regions/" + testRegionA + "/apps/obs.yaml"
	raw, _ := fg.get("acme", "catalyst-tenant", "main", path)
	moved2, err := regionfile.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := regionfile.SetTransitionFields(moved2, regionfile.TransitionFields{Primary: testRegionB}); err != nil {
		t.Fatal(err)
	}
	data, _ := regionfile.Marshal(moved2)
	_, _, _ = fg.PutFile(context.Background(), "acme", "catalyst-tenant", "main", path, data, "continuum: traffic-off")
	reconcileFromCluster(t, r, "acme", "obs")
	if a := topologyIn(t, regionFileObjs(t, fg, "acme", testRegionA, "obs")); a["primary"] != testRegionB {
		t.Fatalf("the application-controller reverted Continuum's commit: A=%v", a)
	}
	if b := topologyIn(t, regionFileObjs(t, fg, "acme", testRegionB, "obs")); b["primary"] != testRegionA {
		t.Fatalf("region B must keep its committed primary until Continuum moves it: B=%v", b)
	}
}

func TestReconcile_ADR0015_SingletonOutsideRegionLeavesNoFile(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithTargets("acme", "obs", "acme-prod", "bp-grafana", "1.0.6", "singleton",
		[]string{testRegionB},
		[]map[string]interface{}{{"region": testRegionB, "cluster": "mgmt-B", "vcluster": "mgmt", "role": "Primary"}})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	// A file left from an earlier placement in region A.
	_, _, _ = fg.PutFile(context.Background(), "acme", "catalyst-tenant", "main", "regions/"+testRegionA+"/apps/obs.yaml", []byte("stale"), "seed")
	r := newReconciler(t, fg, app, env, org, bp)
	reconcileFromCluster(t, r, "acme", "obs")

	if objs := regionFileObjs(t, fg, "acme", testRegionA, "obs"); objs != nil {
		t.Fatal("region A is outside the placement: its file must be removed")
	}
	b := topologyIn(t, regionFileObjs(t, fg, "acme", testRegionB, "obs"))
	if b["self"] != testRegionB || len(b["peers"].([]interface{})) != 0 {
		t.Fatalf("singleton contract = %v", b)
	}
}
