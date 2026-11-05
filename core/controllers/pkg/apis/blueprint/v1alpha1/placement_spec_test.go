package v1alpha1

import (
	"reflect"
	"testing"
)

// The three stored shapes the field has ever had, decoded through the ONE
// reader (#3969 — one canonical representation).
func TestDecodeApplicationPlacement_AllStoredShapes(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		got, err := DecodeApplicationPlacement(nil)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Absent || got.Legacy || got.Mode != "" {
			t.Fatalf("absent placement decoded as %+v", got)
		}
	})

	t.Run("legacy string is decoded, canonicalised and flagged", func(t *testing.T) {
		got, err := DecodeApplicationPlacement("active-hotstandby")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Legacy {
			t.Fatal("a bare string must be reported as the legacy spelling")
		}
		if got.Mode != "active-hot-standby" {
			t.Fatalf("mode = %q, want the canonical token", got.Mode)
		}
	})

	t.Run("object carries every key", func(t *testing.T) {
		got, err := DecodeApplicationPlacement(map[string]interface{}{
			"mode":     "single-region",
			"vcluster": "host",
			"clusters": []interface{}{"mgmt-A"},
			"regions":  []interface{}{"me-east-215-a", "me-east-215-b"},
			"targets": []interface{}{
				map[string]interface{}{"region": "me-east-215-a", "cluster": "mgmt-A", "role": "Primary"},
				map[string]interface{}{"region": "me-east-215-b", "cluster": "mgmt-B", "role": "Standby", "standbyType": "Hot"},
			},
			"ownedDependencies": []interface{}{
				map[string]interface{}{"name": "keycloak-pg", "follow": false},
				map[string]interface{}{"name": "keycloak-cache"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Legacy || got.Absent {
			t.Fatalf("object decoded as legacy/absent: %+v", got)
		}
		if got.Mode != "singleton" {
			t.Errorf("mode = %q, want singleton (folded from single-region)", got.Mode)
		}
		if !got.VClusterSet || got.VCluster != "host" {
			t.Errorf("vcluster = (%q, set=%v), want (host, true)", got.VCluster, got.VClusterSet)
		}
		if !reflect.DeepEqual(got.Clusters, []string{"mgmt-A"}) {
			t.Errorf("clusters = %v", got.Clusters)
		}
		if !reflect.DeepEqual(got.Regions, []string{"me-east-215-a", "me-east-215-b"}) {
			t.Errorf("regions = %v", got.Regions)
		}
		if len(got.Targets) != 2 || got.Targets[1].StandbyType != StandbyHot || got.Targets[0].Role != DataRolePrimary {
			t.Errorf("targets = %+v", got.Targets)
		}
		if len(got.OwnedDependencies) != 2 || got.OwnedDependencies[0].Follow || !got.OwnedDependencies[1].Follow {
			t.Errorf("ownedDependencies = %+v (follow defaults true, explicit false decouples)", got.OwnedDependencies)
		}
	})

	t.Run("malformed values are refused, not dropped", func(t *testing.T) {
		for _, raw := range []interface{}{
			42,
			[]interface{}{"singleton"},
			map[string]interface{}{"mode": 7},
			map[string]interface{}{"targets": "Primary"},
			map[string]interface{}{"regions": []interface{}{1}},
		} {
			if _, err := DecodeApplicationPlacement(raw); err == nil {
				t.Errorf("DecodeApplicationPlacement(%#v) accepted a malformed value", raw)
			}
		}
	})
}

// Canonical() is the ONE shape every writer stores: an object, `mode` always
// present when known, every other key only when it carries a value.
func TestPlacementSpec_Canonical(t *testing.T) {
	t.Parallel()

	t.Run("posture only is {mode}", func(t *testing.T) {
		got := ApplicationPlacement{Mode: "active-hotstandby"}.Canonical()
		want := map[string]interface{}{"mode": "active-hot-standby"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Canonical() = %#v, want %#v", got, want)
		}
	})

	t.Run("explicit host vcluster survives as a choice", func(t *testing.T) {
		got := ApplicationPlacement{Mode: "singleton", VCluster: "", VClusterSet: true}.Canonical()
		if v, ok := got["vcluster"]; !ok || v != "" {
			t.Fatalf("an explicit host choice must be stored, got %#v", got)
		}
	})

	t.Run("targets omit undeclared keys", func(t *testing.T) {
		got := ApplicationPlacement{
			Mode: "active-hot-standby",
			Targets: []PlacementTarget{
				{Region: "a", Cluster: "mgmt-A", Role: DataRolePrimary},
				{Region: "b", Cluster: "mgmt-B", Role: DataRoleStandby, StandbyType: StandbyHot},
			},
		}.Canonical()
		targets := got["targets"].([]interface{})
		primary := targets[0].(map[string]interface{})
		if _, present := primary["standbyType"]; present {
			t.Fatalf("Primary carries standbyType — the CRD CEL forbids it: %#v", primary)
		}
		if _, present := primary["vcluster"]; present {
			t.Fatalf("an undeclared vcluster must be omitted, not stored as an explicit host: %#v", primary)
		}
		standby := targets[1].(map[string]interface{})
		if standby["standbyType"] != "Hot" || standby["role"] != "Standby" {
			t.Fatalf("standby = %#v", standby)
		}
	})

	t.Run("round-trips through Decode", func(t *testing.T) {
		in := ApplicationPlacement{
			Mode:     "active-passive",
			Regions:  []string{"a", "b"},
			Clusters: []string{"c1"},
			Targets: []PlacementTarget{
				{Region: "a", Cluster: "c1", Role: DataRolePrimary},
				{Region: "b", Cluster: "c2", Role: DataRoleStandby, StandbyType: StandbyCold},
			},
			OwnedDependencies: []OwnedDependencyOverride{{Name: "pg", Follow: false}},
		}
		back, err := DecodeApplicationPlacement(in.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		if back.Legacy || back.Absent {
			t.Fatal("the canonical object must never read back as legacy/absent")
		}
		if !reflect.DeepEqual(back.Targets, in.Targets) || !reflect.DeepEqual(back.OwnedDependencies, in.OwnedDependencies) ||
			!reflect.DeepEqual(back.Regions, in.Regions) || !reflect.DeepEqual(back.Clusters, in.Clusters) || back.Mode != in.Mode {
			t.Fatalf("round-trip lost data:\n in=%+v\nout=%+v", in, back)
		}
	})
}

// CanonicalPlacementValue is what the application-controller's one-shot
// migration calls: a string becomes {mode}, a legacy spelling inside an
// object is folded, a canonical object is untouched, an absent placement is
// never invented.
func TestCanonicalPlacementValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		raw     interface{}
		want    map[string]interface{}
		changed bool
	}{
		{"absent stays absent", nil, nil, false},
		{"string becomes {mode}", "singleton", map[string]interface{}{"mode": "singleton"}, true},
		{"legacy string spelling folds", "active-hotstandby", map[string]interface{}{"mode": "active-hot-standby"}, true},
		{"canonical object is untouched", map[string]interface{}{"mode": "active-passive", "clusters": []interface{}{"x"}},
			map[string]interface{}{"mode": "active-passive", "clusters": []interface{}{"x"}}, false},
		{"object with legacy mode folds and keeps the other keys",
			map[string]interface{}{"mode": "single-region", "vcluster": "rtz", "targets": []interface{}{map[string]interface{}{"region": "a", "role": "Primary"}}},
			map[string]interface{}{"mode": "singleton", "vcluster": "rtz", "targets": []interface{}{map[string]interface{}{"region": "a", "role": "Primary"}}}, true},
		{"object with no mode is left alone", map[string]interface{}{"vcluster": "host"}, map[string]interface{}{"vcluster": "host"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := CanonicalPlacementValue(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v (got %#v)", changed, tc.changed, got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("value = %#v, want %#v", got, tc.want)
			}
		})
	}

	if _, _, err := CanonicalPlacementValue([]interface{}{"x"}); err == nil {
		t.Fatal("a malformed value must surface as an error, never be migrated over")
	}
}

func TestCanonicalMode_FoldsEverySpelling(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"singleton": "singleton", "single-region": "singleton", "single_region": "singleton",
		"active-active": "active-active", "active_active": "active-active",
		"active-hot-standby": "active-hot-standby", "active-hotstandby": "active-hot-standby", "active_hot_standby": "active-hot-standby",
		"active-passive": "active-passive", "active_passive": "active-passive", " Active-Passive ": "active-passive",
		"": "", "bogus-mode": "bogus-mode",
	}
	for in, want := range cases {
		if got := CanonicalMode(in); got != want {
			t.Errorf("CanonicalMode(%q) = %q, want %q", in, got, want)
		}
	}
}
