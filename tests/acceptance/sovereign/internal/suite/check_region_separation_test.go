package suite

import (
	"encoding/json"
	"strings"
	"testing"
)

// regionSeparationZones is the registry of two regions in two zones of one
// cloud region; edit changes it further.
func regionSeparationZones(edit func(doc obj)) obj {
	return networkRegistry(func(doc obj) {
		doc["regionSeparation"] = "zone"
		doc["regions"].([]any)[1].(map[string]any)["az"] = "region-1b"
		if edit != nil {
			edit(doc)
		}
	})
}

// regionSeparationSovereign is a Sovereign object as its writer leaves it:
// status.network, status.regionSeparation and the condition RegionSeparation.
// An empty value leaves the field out; an empty status leaves the condition out.
func regionSeparationSovereign(network obj, value, status, reason string) obj {
	st := obj{"network": network}
	if value != "" {
		st["regionSeparation"] = value
	}
	if status != "" {
		st["conditions"] = []obj{{"type": "Ready", "status": "True"}, {"type": "RegionSeparation", "status": status, "reason": reason}}
	}
	return obj{"metadata": obj{"name": "sovereign-self"}, "spec": obj{"fqdn": "t99.example.test"}, "status": st}
}

func init() {
	none := kubeList(regionSeparationSovereign(networkRegistry(nil), "none", "False", "SameFailureDomain"))
	zone := kubeList(regionSeparationSovereign(regionSeparationZones(nil), "zone", "True", "SeparateZones"))
	site := kubeList(regionSeparationSovereign(networkRegistry(func(doc obj) {
		doc["regionSeparation"] = "site"
		doc["regions"].([]any)[1].(map[string]any)["cloudRegion"] = "region-2"
	}), "site", "True", "SeparateSites"))
	both := func(a, b string) map[string]string {
		return map[string]string{"a sovereigns.catalyst.openova.io": a, "b sovereigns.catalyst.openova.io": b}
	}
	fakeAnswers["region-separation-none"] = both(none, none)
	fakeAnswers["region-separation-zone"] = both(zone, zone)
	fakeAnswers["region-separation-site"] = both(site, site)
	fakeAnswers["region-separation-differ"] = both(none, zone)
	fakeAnswers["region-separation-two-objects"] = both(none, kubeList(
		regionSeparationSovereign(networkRegistry(nil), "none", "False", "SameFailureDomain"),
		regionSeparationSovereign(networkRegistry(nil), "none", "False", "SameFailureDomain")))
	fakeAnswers["region-separation-no-network"] = both(none, kubeList(regionSeparationSovereign(nil, "none", "False", "SameFailureDomain")))
	fakeAnswers["region-separation-not-stated"] = both(kubeList(regionSeparationSovereign(networkRegistry(nil), "", "False", "SameFailureDomain")), none)
	fakeAnswers["region-separation-wrong-value"] = both(none, kubeList(regionSeparationSovereign(networkRegistry(nil), "zone", "True", "SeparateZones")))
	fakeAnswers["region-separation-registry-differs"] = both(kubeList(regionSeparationSovereign(regionSeparationZones(func(doc obj) {
		doc["regionSeparation"] = "none"
	}), "zone", "True", "SeparateZones")), zone)
	fakeAnswers["region-separation-no-condition"] = both(none, kubeList(regionSeparationSovereign(networkRegistry(nil), "none", "", "")))
	fakeAnswers["region-separation-condition-true"] = both(kubeList(regionSeparationSovereign(networkRegistry(nil), "none", "True", "SeparateZones")), none)
	fakeAnswers["region-separation-condition-reason"] = both(zone, kubeList(regionSeparationSovereign(regionSeparationZones(nil), "zone", "True", "SeparateSites")))
}

func TestRegionSeparationCheck(t *testing.T) {
	if _, ok := registry[regionSeparationID]; !ok || regionSeparationID != "NET-G12" {
		t.Fatalf("check %s is not registered", regionSeparationID)
	}
	for name, c := range map[string]struct {
		scenario string
		claim    bool
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"both regions state none":              {"region-separation-none", false, ""},
		"both regions state zone":              {"region-separation-zone", false, ""},
		"both regions state site":              {"region-separation-site", false, ""},
		"a claim with two zones":               {"region-separation-zone", true, ""},
		"a claim with one failure domain":      {"region-separation-none", true, "the run declares disaster-recovery, but its regions share one failure domain: two clusters, one failure domain"},
		"the regions state different values":   {"region-separation-differ", false, "the regions state different region separations: none, zone"},
		"a region has two objects":             {"region-separation-two-objects", false, "region region-b has 2 Sovereign objects, want exactly 1"},
		"a region has no registry":             {"region-separation-no-network", false, "region region-b: the Sovereign object sovereign-self has no status.network"},
		"the value is not stated":              {"region-separation-not-stated", false, `region region-a: status.regionSeparation is "", the regions of status.network give none`},
		"the value contradicts the regions":    {"region-separation-wrong-value", false, `region region-b: status.regionSeparation is "zone", the regions of status.network give none`},
		"the registry contradicts its regions": {"region-separation-registry-differs", false, `region region-a: status.network.regionSeparation is "none", the regions of status.network give zone`},
		"the condition is absent":              {"region-separation-no-condition", false, "region region-b: the condition RegionSeparation is absent"},
		"the condition claims separation":      {"region-separation-condition-true", false, "the condition RegionSeparation is True SeparateZones, want False SameFailureDomain"},
		"the condition has another reason":     {"region-separation-condition-reason", false, "the condition RegionSeparation is True SeparateSites, want True SeparateZones"},
	} {
		cc := &CheckContext{Caps: map[string]bool{}}
		if c.claim {
			cc.Caps["disaster-recovery"] = true
		}
		r := runCheck(t, c.scenario, regionSeparationCheck{}, cc)
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}

	r := runCheck(t, "region-separation-none", regionSeparationCheck{}, &CheckContext{})
	if len(r.run.Steps) != 3 || r.run.Steps[0].ID != "NET-G12.1" || r.run.Steps[2].ID != "NET-G12.3" {
		t.Fatalf("want the steps NET-G12.1 to NET-G12.3, got %+v", r.run.Steps)
	}
	b, err := json.Marshal(r.run.Steps[1].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"stated":"none"`, `"rule":"none"`, `"registry":"none"`, `"regions":2`, `"condition":"False SameFailureDomain"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the evidence of NET-G12.2 lacks %s: %s", want, b)
		}
	}
	r.run.Provision = &Provision{Installation: &Installation{Provider: "huawei", Regions: []ProvisionRegion{}}}
	r.run.Finalize(nil)
	b, err = json.Marshal(r.run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"separation":{"value":"none","summary":"two clusters, one failure domain"}`) {
		t.Fatalf("the record lacks provision.separation: %s", b)
	}
	if _, left := r.run.Steps[2].Evidence[SeparationKey]; left || r.run.Steps[2].Evidence["summary"] != "two clusters, one failure domain" {
		t.Fatalf("the evidence of NET-G12.3 is %v", r.run.Steps[2].Evidence)
	}
}

func TestRegionSeparationSummary(t *testing.T) {
	for _, c := range []struct {
		value   string
		regions int
		want    string
	}{
		{"none", 1, "one cluster, one failure domain"},
		{"none", 2, "two clusters, one failure domain"},
		{"zone", 2, "two clusters in two zones"},
		{"site", 3, "three clusters in three sites"},
		{"none", 7, "7 clusters, one failure domain"},
	} {
		if got := regionSeparationSummary(c.value, c.regions); got != c.want {
			t.Errorf("%s with %d regions: %q, want %q", c.value, c.regions, got, c.want)
		}
	}
}
