package sovereignobject

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestSovereignObjectRegionSeparation: status.regionSeparation follows the
// rule of the provider modules, the condition RegionSeparation states it, and
// its lastTransitionTime stays while its status stays.
func TestSovereignObjectRegionSeparation(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()
	region := func(cloud, az string) map[string]interface{} {
		return map[string]interface{}{"cloudRegion": cloud, "az": az}
	}
	for _, c := range []struct {
		regions         []interface{}
		want, condition string
		reason          string
	}{
		{[]interface{}{region("r1", "r1a")}, "none", "False", "SameFailureDomain"},
		{[]interface{}{region("r1", "r1a"), region("r1", "r1a")}, "none", "False", "SameFailureDomain"},
		{[]interface{}{region("r1", "r1a"), region("r1", "r1b")}, "zone", "True", "SeparateZones"},
		{[]interface{}{region("r1", "none"), region("r2", "none")}, "site", "True", "SeparateSites"},
		{[]interface{}{region("r1", "r1a"), region("r1", "r1b"), region("r2", "r2a")}, "zone", "True", "SeparateZones"},
		{[]interface{}{region("r1", "r1a"), region("r1", "r1a"), region("r2", "r2a")}, "none", "False", "SameFailureDomain"},
	} {
		got := Separation(map[string]interface{}{"regions": c.regions})
		cond := separationCondition(got, len(c.regions), nil)
		if got != c.want || cond["status"] != c.condition || cond["reason"] != c.reason || cond["type"] != ConditionType ||
			cond["lastTransitionTime"] != "2026-11-20T08:00:00Z" {
			t.Errorf("%v: separation %s, condition %v; want %s, %s, %s", c.regions, got, cond, c.want, c.condition, c.reason)
		}
	}

	f := newCluster(t, true)
	f.objects[objPath] = map[string]interface{}{"status": map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": ConditionType, "status": "False", "lastTransitionTime": "2026-11-01T00:00:00Z"}}}}
	if _, err := Write(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	st := f.applied[objPath+"/status"]["status"].(map[string]interface{})
	want := []interface{}{map[string]interface{}{"type": ConditionType, "status": "False", "reason": "SameFailureDomain",
		"message": "Regions share one failure domain. Not a disaster-recovery configuration.", "lastTransitionTime": "2026-11-01T00:00:00Z"}}
	if st["regionSeparation"] != "none" || !reflect.DeepEqual(st["conditions"], want) {
		got, _ := json.Marshal(st)
		t.Fatalf("status %s", got)
	}
	f.objects[objPath]["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["status"] = "True"
	if _, err := Write(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	cond := f.applied[objPath+"/status"]["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})
	if cond["lastTransitionTime"] != "2026-11-20T08:00:00Z" {
		t.Fatalf("a changed status kept its time: %v", cond)
	}

	f = newCluster(t, false)
	if _, err := Write(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if st := f.applied[objPath+"/status"]["status"].(map[string]interface{}); len(st) != 0 {
		t.Fatalf("without the registry the status is %v", st)
	}
}
