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
			// "" provider → hetzner default, which gates nothing, so these
			// pre-#4086 cases keep their exact expected census.
			gotRegions, gotDeg := ComputeRegionHealth("", tc.primaryRegion, tc.primaryStates, tc.secondaryStates)
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
	regions, deg := ComputeRegionHealth("", "reg-a", installedN(10, 63), map[string]map[string]string{
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

// TestComputeRegionHealth_ExcludesProviderInapplicableHRs is the #4086
// contract: on a Huawei Sovereign the hcloud control-plane HRs
// (cluster-autoscaler-hcloud + hcloud-ccm) are suspended and never go Ready,
// so they must be EXCLUDED from the health census. A Sovereign whose ONLY
// non-installed HRs are those provider-inapplicable ones must read fully
// converged (no degraded secondary), while a genuinely-failing applicable
// component must still degrade the status.
func TestComputeRegionHealth_ExcludesProviderInapplicableHRs(t *testing.T) {
	// region-a (primary) + region-b (secondary). Both regions have 60
	// applicable HRs all installed, PLUS the two hcloud HRs stuck
	// non-installed (suspended/never-Ready on Huawei). Without the #4086
	// filter the census would count 60/62 in each region; with the filter it
	// is 60/60 in each — fully converged, no degraded badge.
	mkHuaweiRegion := func(applicableInstalled, applicableTotal int) map[string]string {
		m := installedN(applicableInstalled, applicableTotal)
		// the inapplicable hcloud HRs, stuck non-installed forever on Huawei.
		m["cluster-autoscaler-hcloud"] = "installing"
		m["hcloud-ccm"] = "degraded"
		return m
	}

	t.Run("huawei only-hcloud-non-ready -> not degraded, census excludes them", func(t *testing.T) {
		regions, deg := ComputeRegionHealth("huawei", "me-east-215-a",
			mkHuaweiRegion(60, 60),
			map[string]map[string]string{
				"me-east-215-b": mkHuaweiRegion(60, 60),
			})
		if deg {
			t.Fatalf("secondaryDegraded = true, want false (only provider-inapplicable hcloud HRs are non-Ready)")
		}
		if len(regions) != 2 {
			t.Fatalf("want 2 regions, got %d", len(regions))
		}
		// the hcloud HRs must NOT appear in the count — 60/60 not 60/62.
		if regions[0].HRReady != 60 || regions[0].HRTotal != 60 {
			t.Errorf("primary census = %d/%d, want 60/60 (hcloud HRs excluded)", regions[0].HRReady, regions[0].HRTotal)
		}
		if regions[1].HRReady != 60 || regions[1].HRTotal != 60 {
			t.Errorf("secondary census = %d/%d, want 60/60 (hcloud HRs excluded)", regions[1].HRReady, regions[1].HRTotal)
		}
		if regions[1].Degraded {
			t.Errorf("secondary must not be degraded when only inapplicable HRs are non-Ready; got %+v", regions[1])
		}
	})

	t.Run("huawei real applicable app non-Ready -> still degraded", func(t *testing.T) {
		// secondary has a REAL applicable component failing (48/60 applicable
		// installed) on top of the excluded hcloud HRs. The applicable
		// shortfall must still flag the secondary degraded — the filter must
		// not mask genuine failures.
		regions, deg := ComputeRegionHealth("huawei", "me-east-215-a",
			mkHuaweiRegion(60, 60),
			map[string]map[string]string{
				"me-east-215-b": mkHuaweiRegion(48, 60),
			})
		if !deg {
			t.Fatalf("secondaryDegraded = false, want true (a real applicable app is non-Ready)")
		}
		if regions[1].HRReady != 48 || regions[1].HRTotal != 60 {
			t.Errorf("secondary census = %d/%d, want 48/60 (applicable only)", regions[1].HRReady, regions[1].HRTotal)
		}
		if !regions[1].Degraded {
			t.Errorf("secondary should be degraded on a real applicable shortfall; got %+v", regions[1])
		}
	})

	t.Run("hetzner does NOT exclude hcloud HRs — they are applicable there", func(t *testing.T) {
		// On Hetzner the hcloud HRs ARE applicable. A primary with them
		// non-installed genuinely lowers the count (here they are the only
		// gap). The primary is never flagged degraded, but the census must
		// include them: 60/62 not 60/60.
		regions, _ := ComputeRegionHealth("hetzner", "fsn1",
			mkHuaweiRegion(60, 60), // same shape: 60 applicable + 2 hcloud non-installed
			nil)
		if len(regions) != 1 {
			t.Fatalf("want 1 region, got %d", len(regions))
		}
		if regions[0].HRTotal != 62 {
			t.Errorf("hetzner total = %d, want 62 (hcloud HRs counted on hetzner)", regions[0].HRTotal)
		}
		if regions[0].HRReady != 60 {
			t.Errorf("hetzner ready = %d, want 60", regions[0].HRReady)
		}
	})
}

// TestFilterApplicable covers the provider-gating helper directly.
func TestFilterApplicable(t *testing.T) {
	states := map[string]string{
		"cilium":                    "installed",
		"hcloud-ccm":                "installing",
		"cluster-autoscaler-hcloud": "degraded",
		"hcloud-csi":                "installed", // active-but-empty on Huawei — applicable, kept
	}
	t.Run("huawei drops the two suspended hcloud control-plane HRs", func(t *testing.T) {
		got := filterApplicable("huawei", states)
		if _, ok := got["hcloud-ccm"]; ok {
			t.Errorf("hcloud-ccm must be excluded on huawei")
		}
		if _, ok := got["cluster-autoscaler-hcloud"]; ok {
			t.Errorf("cluster-autoscaler-hcloud must be excluded on huawei")
		}
		if _, ok := got["cilium"]; !ok {
			t.Errorf("cilium must be kept")
		}
		if _, ok := got["hcloud-csi"]; !ok {
			t.Errorf("hcloud-csi is active-but-empty (Ready) on huawei — must be kept, not excluded")
		}
		// input not mutated.
		if _, ok := states["hcloud-ccm"]; !ok {
			t.Errorf("filterApplicable must not mutate its input map")
		}
	})
	t.Run("hetzner keeps everything", func(t *testing.T) {
		got := filterApplicable("hetzner", states)
		if len(got) != len(states) {
			t.Errorf("hetzner gates nothing: got %d entries, want %d", len(got), len(states))
		}
	})
	t.Run("empty provider defaults to hetzner via normalizeProvider", func(t *testing.T) {
		if normalizeProvider("") != "hetzner" {
			t.Errorf("empty provider must default to hetzner")
		}
		if normalizeProvider("  HUAWEI ") != "huawei" {
			t.Errorf("provider must be trimmed + lowercased")
		}
	})
	t.Run("nil/empty states returns as-is (0/0 preserved)", func(t *testing.T) {
		if got := filterApplicable("huawei", nil); got != nil {
			t.Errorf("nil input must return nil, got %v", got)
		}
	})
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
