package provisioner

import (
	"reflect"
	"testing"
)

// installedN returns a component-state map with `installed` entries named
// c0..c(installed-1) and `installing` entries filling up to total — a compact
// way to build a region census of "installed/total" for the table tests.
func installedN(installed, total int) map[string]string {
	if installed > total {
		installed = total
	}
	m := make(map[string]string, total)
	for i := 0; i < total; i++ {
		state := "installing"
		if i < installed {
			state = "installed"
		}
		m[componentName(i)] = state
	}
	return m
}

func componentName(i int) string {
	// stable distinct keys; the exact name is irrelevant to counting.
	return "bp-component-" + string(rune('a'+i%26)) + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestComputeRegionHealth(t *testing.T) {
	tests := []struct {
		name             string
		primaryRegion    string
		primaryStates    map[string]string
		secondaryStates  map[string]map[string]string
		wantRegions      []RegionHealth
		wantSecondaryDeg bool
	}{
		{
			name:          "single-region primary only ready — no secondaries, not degraded",
			primaryRegion: "me-east-215-a",
			primaryStates: installedN(63, 63),
			wantRegions: []RegionHealth{
				{Region: "me-east-215-a", Primary: true, HRReady: 63, HRTotal: 63, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "both regions fully ready — secondary not degraded",
			primaryRegion: "me-east-215-a",
			primaryStates: installedN(63, 63),
			secondaryStates: map[string]map[string]string{
				"me-east-215-b": installedN(63, 63),
			},
			wantRegions: []RegionHealth{
				{Region: "me-east-215-a", Primary: true, HRReady: 63, HRTotal: 63, Degraded: false},
				{Region: "me-east-215-b", Primary: false, HRReady: 63, HRTotal: 63, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "secondary degraded — the hw145 case (region-a 60/63, region-b 48/63)",
			primaryRegion: "me-east-215-a",
			primaryStates: installedN(60, 63),
			secondaryStates: map[string]map[string]string{
				"me-east-215-b": installedN(48, 63),
			},
			wantRegions: []RegionHealth{
				{Region: "me-east-215-a", Primary: true, HRReady: 60, HRTotal: 63, Degraded: false},
				{Region: "me-east-215-b", Primary: false, HRReady: 48, HRTotal: 63, Degraded: true},
			},
			wantSecondaryDeg: true,
		},
		{
			name:          "secondary one HR behind primary — NOT degraded (below absolute floor)",
			primaryRegion: "reg-a",
			primaryStates: installedN(63, 63),
			secondaryStates: map[string]map[string]string{
				"reg-b": installedN(62, 63),
			},
			wantRegions: []RegionHealth{
				{Region: "reg-a", Primary: true, HRReady: 63, HRTotal: 63, Degraded: false},
				{Region: "reg-b", Primary: false, HRReady: 62, HRTotal: 63, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "secondary fully installed but fewer total HRs than primary — NOT degraded",
			primaryRegion: "reg-a",
			primaryStates: installedN(63, 63),
			secondaryStates: map[string]map[string]string{
				// e.g. a region that does not host the marketplace: 55/55
				// installed. shortfall 8 > floor and 55 < 0.9*63=56.7, but
				// the secondary is FULLY installed so it must not be flagged.
				"reg-b": installedN(55, 55),
			},
			wantRegions: []RegionHealth{
				{Region: "reg-a", Primary: true, HRReady: 63, HRTotal: 63, Degraded: false},
				{Region: "reg-b", Primary: false, HRReady: 55, HRTotal: 55, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "secondary watcher attached but zero HRs observed yet vs converged primary — degraded",
			primaryRegion: "reg-a",
			primaryStates: installedN(60, 63),
			secondaryStates: map[string]map[string]string{
				"reg-b": {},
			},
			wantRegions: []RegionHealth{
				{Region: "reg-a", Primary: true, HRReady: 60, HRTotal: 63, Degraded: false},
				{Region: "reg-b", Primary: false, HRReady: 0, HRTotal: 0, Degraded: true},
			},
			wantSecondaryDeg: true,
		},
		{
			name:          "early convergence — both 0/0 — secondary NOT degraded (no meaningful primary baseline)",
			primaryRegion: "reg-a",
			primaryStates: map[string]string{},
			secondaryStates: map[string]map[string]string{
				"reg-b": {},
			},
			wantRegions: []RegionHealth{
				{Region: "reg-a", Primary: true, HRReady: 0, HRTotal: 0, Degraded: false},
				{Region: "reg-b", Primary: false, HRReady: 0, HRTotal: 0, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "empty primary region key falls back to 'primary' label",
			primaryRegion: "",
			primaryStates: installedN(10, 10),
			wantRegions: []RegionHealth{
				{Region: "primary", Primary: true, HRReady: 10, HRTotal: 10, Degraded: false},
			},
			wantSecondaryDeg: false,
		},
		{
			name:          "three regions — one healthy secondary, one degraded; sorted deterministically; roll-up true",
			primaryRegion: "reg-a",
			primaryStates: installedN(60, 63),
			secondaryStates: map[string]map[string]string{
				// intentionally inserted out of sorted order to prove the
				// output is sorted by region key.
				"reg-c": installedN(40, 63), // degraded
				"reg-b": installedN(60, 63), // healthy
			},
			wantRegions: []RegionHealth{
				{Region: "reg-a", Primary: true, HRReady: 60, HRTotal: 63, Degraded: false},
				{Region: "reg-b", Primary: false, HRReady: 60, HRTotal: 63, Degraded: false},
				{Region: "reg-c", Primary: false, HRReady: 40, HRTotal: 63, Degraded: true},
			},
			wantSecondaryDeg: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRegions, gotDeg := ComputeRegionHealth(tc.primaryRegion, tc.primaryStates, tc.secondaryStates)
			if gotDeg != tc.wantSecondaryDeg {
				t.Errorf("secondaryDegraded = %v, want %v", gotDeg, tc.wantSecondaryDeg)
			}
			if !reflect.DeepEqual(gotRegions, tc.wantRegions) {
				t.Errorf("regions mismatch:\n got = %#v\nwant = %#v", gotRegions, tc.wantRegions)
			}
		})
	}
}

func TestComputeRegionHealth_PrimaryAlwaysFirstAndNeverDegraded(t *testing.T) {
	// Even when the primary is itself behind (it failed some HRs), the
	// primary entry is never flagged Degraded — its own health is conveyed
	// via dep.Status / Phase1Outcome, and Degraded is a secondary-only
	// signal in this census.
	regions, deg := ComputeRegionHealth("reg-a", installedN(10, 63), map[string]map[string]string{
		"reg-b": installedN(63, 63),
	})
	if len(regions) != 2 {
		t.Fatalf("want 2 regions, got %d", len(regions))
	}
	if !regions[0].Primary {
		t.Errorf("first region must be the primary; got %+v", regions[0])
	}
	if regions[0].Degraded {
		t.Errorf("primary must never be flagged degraded; got %+v", regions[0])
	}
	// secondary is fully installed → roll-up false even though the primary
	// is the weak one.
	if deg {
		t.Errorf("secondaryDegraded must be false when the only secondary is fully installed; got true")
	}
}

func TestRegionDegraded_Gate(t *testing.T) {
	tests := []struct {
		name                       string
		priReady, secReady, secTot int
		want                       bool
	}{
		{"secondary fully installed never degraded", 63, 55, 55, false},
		{"one behind under floor", 63, 62, 63, false},
		{"two behind under floor", 63, 61, 63, false},
		{"three behind but still >=90% not degraded", 30, 27, 63, false}, // shortfall 3 >= floor, 27 >= 0.9*30=27 → not below ratio
		{"hw145 12 behind and <90% degraded", 60, 48, 63, true},
		{"exactly at 90% ratio not degraded", 100, 90, 120, false}, // 90 < 90? no
		{"just below 90% ratio degraded", 100, 89, 120, true},      // 89 < 90 and shortfall 11
		{"zero observed vs strong primary degraded", 60, 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := regionDegraded(tc.priReady, tc.secReady, tc.secTot); got != tc.want {
				t.Errorf("regionDegraded(%d,%d,%d) = %v, want %v", tc.priReady, tc.secReady, tc.secTot, got, tc.want)
			}
		})
	}
}

func TestCountInstalled(t *testing.T) {
	ready, total := countInstalled(map[string]string{
		"a": "installed",
		"b": "installed",
		"c": "installing",
		"d": "failed",
		"e": "degraded",
		"f": "pending",
	})
	if ready != 2 {
		t.Errorf("ready = %d, want 2 (only 'installed' counts)", ready)
	}
	if total != 6 {
		t.Errorf("total = %d, want 6", total)
	}
	// nil map → 0/0
	if r, tot := countInstalled(nil); r != 0 || tot != 0 {
		t.Errorf("countInstalled(nil) = %d/%d, want 0/0", r, tot)
	}
}
