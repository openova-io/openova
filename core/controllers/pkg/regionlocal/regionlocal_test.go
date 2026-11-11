package regionlocal

import (
	"strings"
	"testing"
)

func bp(vols ...map[string]any) map[string]any {
	items := make([]any, 0, len(vols))
	for _, v := range vols {
		items = append(items, v)
	}
	return map[string]any{"spec": map[string]any{"regionLocalVolumes": items}}
}

func vol(name, repl string) map[string]any {
	m := map[string]any{"name": name}
	if repl != "" {
		m["replication"] = repl
	}
	return m
}

func TestRefusal(t *testing.T) {
	cases := []struct {
		name     string
		obj      map[string]any
		mode     string
		regions  int
		refused  bool
		mentions []string
	}{
		{"singleton with none is admitted", bp(vol("wp-content", "none")), "singleton", 1, false, nil},
		{"empty mode one region is admitted", bp(vol("wp-content", "none")), "", 1, false, nil},
		{"active-hot-standby with none is refused", bp(vol("wp-content", "none")), "active-hot-standby", 2, true, []string{`"wp-content"`, "replication: none", "ADR-0015 D6", "bp-x"}},
		{"active-passive with none is refused", bp(vol("wp-content", "none")), "active-passive", 2, true, []string{`"wp-content"`}},
		{"two regions named without a mode is refused", bp(vol("data", "none")), "", 2, true, []string{`"data"`, "2 regions"}},
		{"replication unset is refused", bp(vol("data", "")), "active-active", 2, true, []string{"replication: <unset>"}},
		{"unknown mechanism is refused (fail closed on a typo)", bp(vol("data", "s3-replicatd")), "active-passive", 2, true, []string{"s3-replicatd"}},
		{"only the unreplicated volume is named", bp(vol("uploads", "s3-replicated"), vol("cache", "none")), "active-hot-standby", 2, true, []string{`"cache"`}},
		{"s3-replicated + none-needed is admitted", bp(vol("uploads", "s3-replicated"), vol("wp-content", "none-needed")), "active-hot-standby", 2, false, nil},
		{"volume-replication is admitted", bp(vol("data", "volume-replication")), "active-passive", 2, false, nil},
		{"no declaration is admitted", map[string]any{"spec": map[string]any{}}, "active-hot-standby", 2, false, nil},
		{"non-object entry fails closed", map[string]any{"spec": map[string]any{"regionLocalVolumes": []any{"wp-content"}}}, "active-passive", 2, true, []string{"<unnamed>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Refusal("bp-x", tc.obj, tc.mode, tc.regions)
			if (err != nil) != tc.refused {
				t.Fatalf("refused=%v, want %v (err=%v)", err != nil, tc.refused, err)
			}
			for _, m := range tc.mentions {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("message %q does not mention %q", err.Error(), m)
				}
			}
			if tc.name == "only the unreplicated volume is named" && strings.Contains(err.Error(), `"uploads"`) {
				t.Errorf("message names the replicated volume: %q", err.Error())
			}
		})
	}
}

func TestMultiRegion(t *testing.T) {
	for mode, want := range map[string]bool{"": false, "singleton": false, "single-region": false, "active-passive": true, "active-hot-standby": true, "active-active": true} {
		if got := MultiRegion(mode, 1); got != want {
			t.Errorf("MultiRegion(%q,1)=%v want %v", mode, got, want)
		}
	}
	if !MultiRegion("singleton", 2) {
		t.Error("two regions is multi-region whatever the mode says")
	}
}
