package capacity

import (
	"reflect"
	"testing"
)

func TestNormClasses(t *testing.T) {
	got, err := NormClasses([]string{" Spot", "guaranteed", "spot"})
	if err != nil || !reflect.DeepEqual(got, []string{ClassGuaranteed, ClassSpot}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := NormClasses(nil); err == nil {
		t.Error("an empty class set must be refused: a pool that enforces nothing sells nothing")
	}
	if _, err := NormClasses([]string{"reserved"}); err == nil {
		t.Error("an unknown class must be refused")
	}
	if HasClass(DefaultPoolClasses, ClassBurstable) {
		t.Error("burstable is opted into, never the default: a resold cloud flavour cannot throttle")
	}
}

func TestResolveClass(t *testing.T) {
	all := []string{ClassGuaranteed, ClassBurstable, ClassSpot}
	cases := []struct {
		name          string
		override, tag string
		placed        []string
		want          ClassResolution
		ok            bool
	}{
		{"untagged counts conservative", "", "", all, ClassResolution{Class: ClassGuaranteed, Source: ClassFromDefault}, true},
		{"tag", "", "Spot", all, ClassResolution{Class: ClassSpot, Source: ClassFromTag}, true},
		{"override beats tag", "burstable", "spot", all, ClassResolution{Class: ClassBurstable, Source: ClassFromOverride}, true},
		{"default is the most conservative PLACED class", "", "", []string{ClassSpot, ClassBurstable}, ClassResolution{Class: ClassBurstable, Source: ClassFromDefault}, true},
		{"a tag naming an unplaced class still counts, and says so", "", "spot", []string{ClassGuaranteed}, ClassResolution{Class: ClassGuaranteed, Source: ClassFromDefault, Asked: ClassSpot}, true},
		{"an unplaced override falls through to a placed tag", "burstable", "spot", []string{ClassGuaranteed, ClassSpot}, ClassResolution{Class: ClassSpot, Source: ClassFromTag}, true},
		{"a tag that is not a class is ignored", "", "production", all, ClassResolution{Class: ClassGuaranteed, Source: ClassFromDefault}, true},
		{"placed nowhere", "spot", "spot", nil, ClassResolution{}, false},
	}
	for _, c := range cases {
		got, ok := ResolveClass(c.override, c.tag, c.placed)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestFamilies(t *testing.T) {
	if !IsFamily("ecs.m7n.*") || IsFamily("ecs.m7n.xlarge.8") {
		t.Fatal("IsFamily")
	}
	for _, bad := range []string{"", "*", ".*", "ecs*", "ecs.*.large", "ecs m7n.*"} {
		if ValidPlacementSKU(bad) {
			t.Errorf("%q must not be a valid placement SKU", bad)
		}
	}
	for _, good := range []string{"eip", "ecs.m7n.xlarge.8", "ecs.*", "ecs.m7n.*"} {
		if !ValidPlacementSKU(good) {
			t.Errorf("%q must be a valid placement SKU", good)
		}
	}
	if !MatchFamily("ecs.m7n.*", "ECS.m7n.2xlarge.8") || MatchFamily("ecs.m7n.*", "ecs.m7nx.large.2") || MatchFamily("ecs.*", "ecs") {
		t.Error("MatchFamily matches on the dotted prefix only")
	}

	placed := []string{"ecs.*", "ecs.m7n.*", "ecs.m7n.xlarge.8", "evs.*"}
	for sku, want := range map[string]string{
		"ecs.m7n.xlarge.8":  "ecs.m7n.xlarge.8", // exact wins
		"ecs.m7n.2xlarge.8": "ecs.m7n.*",        // longest family wins
		"ecs.s7n.large.2":   "ecs.*",
		"evs.ssd.gb":        "evs.*",
		"eip":               "",
	} {
		if got := BestMatches(placed, sku); got != want {
			t.Errorf("BestMatches(%s) = %q, want %q", sku, got, want)
		}
	}

	got := Families([]string{"ecs.m7n.xlarge.8", "ecs.m7n.2xlarge.8", "ecs.s7n.2xlarge.2", "evs.ssd.gb", "eip", "eip.bandwidth_mbps"})
	want := []Family{{"ecs.*", 3}, {"ecs.m7n.*", 2}, {"eip.*", 1}, {"evs.*", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Families = %+v, want %+v", got, want)
	}
}
