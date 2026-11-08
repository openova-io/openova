package controller

// ADR-0015 D6 at the reconcile level: an Application whose placement spans
// regions while its Blueprint declares an unreplicated spec.regionLocalVolumes
// entry is refused BEFORE anything is rendered (Ready=False,
// reason=RegionLocalVolumeNotReplicated, the message names the volume) — the
// gate for a placement edited in Git or with kubectl, which never passes
// through catalyst-api's 422.

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func withRegionLocalVolumes(bp *unstructured.Unstructured, vols ...[2]string) *unstructured.Unstructured {
	items := make([]interface{}, 0, len(vols))
	for _, v := range vols {
		items = append(items, map[string]interface{}{"name": v[0], "replication": v[1]})
	}
	_ = unstructured.SetNestedSlice(bp.Object, items, "spec", "regionLocalVolumes")
	return bp
}

func readyCondition(t *testing.T, app *unstructured.Unstructured) (status, reason, message string) {
	t.Helper()
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		if m["type"] == "Ready" {
			s, _ := m["status"].(string)
			r, _ := m["reason"].(string)
			msg, _ := m["message"].(string)
			return s, r, msg
		}
	}
	return "", "", ""
}

func TestReconcile_D6_UnreplicatedRegionLocalVolume_MultiRegion_Refused(t *testing.T) {
	bp := withRegionLocalVolumes(makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby"),
		[2]string{"uploads", "s3-replicated"}, [2]string{"wp-content", "none"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, hotStandbyApp("mgmt-A", "mgmt-B"), makeMultiRegionEnv("acme-prod", "acme", "prod"), makeOrg("acme"), bp)

	reconcileFromCluster(t, r, "acme", "obs")

	status, reason, msg := readyCondition(t, readApp(t, r, "acme", "obs"))
	if status != "False" || reason != ReasonRegionLocalVolumeNotReplicated {
		t.Fatalf("Ready = %q reason %q (%s), want False/%s", status, reason, msg, ReasonRegionLocalVolumeNotReplicated)
	}
	if !strings.Contains(msg, `"wp-content"`) || strings.Contains(msg, `"uploads"`) {
		t.Fatalf("message must name wp-content and only it: %s", msg)
	}
	if objs := regionFileObjs(t, fg, "acme", testRegionA, "obs"); len(objs) != 0 {
		t.Fatalf("a refused placement rendered %d object(s) into region A", len(objs))
	}
	if objs := regionFileObjs(t, fg, "acme", testRegionB, "obs"); len(objs) != 0 {
		t.Fatalf("a refused placement rendered %d object(s) into region B", len(objs))
	}
}

func TestReconcile_D6_ReplicatedRegionLocalVolumes_MultiRegion_Delivered(t *testing.T) {
	bp := withRegionLocalVolumes(makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby"),
		[2]string{"uploads", "s3-replicated"}, [2]string{"wp-content", "none-needed"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, hotStandbyApp("mgmt-A", "mgmt-B"), makeMultiRegionEnv("acme-prod", "acme", "prod"), makeOrg("acme"), bp)

	reconcileFromCluster(t, r, "acme", "obs")

	if _, reason, msg := readyCondition(t, readApp(t, r, "acme", "obs")); reason == ReasonRegionLocalVolumeNotReplicated {
		t.Fatalf("replicated volumes refused: %s", msg)
	}
	if len(regionFileObjs(t, fg, "acme", testRegionA, "obs")) != 1 || len(regionFileObjs(t, fg, "acme", testRegionB, "obs")) != 1 {
		t.Fatal("an admitted placement must render one file per region")
	}
}
