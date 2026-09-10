package capacity

import (
	"reflect"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The Huawei flavour convention: <family>.<size>.<ratio>, size in vCPU
// (small/medium 1, large 2, xlarge 4, Nxlarge 4N), memory = vCPU × ratio.
// The three ECS SKUs of the National Cloud list are the anchor: their
// descriptions in internal/synth/rates.go state the vCPU and GB.
func TestParseECSFlavor(t *testing.T) {
	cases := []struct {
		name       string
		vcpus, mem int
		ok         bool
	}{
		{"m7n.xlarge.8", 4, 32, true},
		{"m7n.2xlarge.8", 8, 64, true},
		{"s7n.2xlarge.2", 8, 16, true},
		{"s6.small.1", 1, 1, true},
		{"s6.medium.2", 1, 2, true},
		{"c7n.large.2", 2, 4, true},
		{"c7.4xlarge.2", 16, 32, true},
		{"kc1.16xlarge.4", 64, 256, true},
		{"unknown", 0, 0, false},
		{"cpu_util", 0, 0, false},
		{"m7n.2xlarge", 0, 0, false},         // no ratio
		{"m7n.huge.8", 0, 0, false},          // unknown size token
		{"m7n.1xlarge.8", 0, 0, false},       // 1xlarge is not a Huawei size
		{"m7n.2xlarge.8.linux", 0, 0, false}, // four parts: not the convention
		{"m7n.2xlarge.x", 0, 0, false},       // ratio not a number
		{"", 0, 0, false},
	}
	for _, c := range cases {
		v, m, ok := ParseECSFlavor(c.name)
		if ok != c.ok || v != c.vcpus || m != c.mem {
			t.Errorf("ParseECSFlavor(%q) = %d, %d, %v; want %d, %d, %v", c.name, v, m, ok, c.vcpus, c.mem, c.ok)
		}
	}
}

// Derive covers exactly the unambiguous shapes; a platform meter and a
// storage SKU whose class is not in its name derive nothing (the control).
func TestDerive(t *testing.T) {
	cases := map[string]Footprint{
		"ecs.m7n.2xlarge.8":  {FamilyVCPU: "8", FamilyMemoryGiB: "64"},
		"ECS.S6.Large.2":     {FamilyVCPU: "2", FamilyMemoryGiB: "4"},
		"evs.ssd.gb":         {FamilyBlockSSD: "1"},
		"evs.hdd.gb":         {FamilyBlockHDD: "1"},
		"eip":                {FamilyEIP: "1"},
		"eip.bandwidth_mbps": {FamilyBandwidth: "1"},
		"ecs.unknown":        nil,
		"ecs.cpu_util":       nil,
		"k8s.vcpu":           nil, // platform meter: runs on instances the ecs.* SKUs already count
		"k8s.mem_gb":         nil,
		"plan.m":             nil,
		"rds.storage.ha.gb":  nil, // storage class not in the name
		"cbr.gb":             nil,
		"elb":                nil,
		"nat.1":              nil,
		"vpc":                nil,
		"":                   nil,
	}
	for sku, want := range cases {
		got := Derive(sku)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Derive(%q) = %v, want %v", sku, got, want)
		}
	}
}

// The seed list IS the National Cloud list: the same SKUs, none missing,
// none extra, so a rate added to the list is either seeded here or shows up
// as unseeded on purpose.
func TestSeedCoversNationalCloudList(t *testing.T) {
	want := make([]string, 0, len(synth.NationalCloudRates))
	for _, r := range synth.NationalCloudRates {
		want = append(want, r.SKU)
	}
	if !reflect.DeepEqual(SeedSKUs, want) {
		t.Fatalf("SeedSKUs = %v, want the National Cloud list %v", SeedSKUs, want)
	}
	rows := Seed()
	// 3 ECS × 2 families + evs.ssd.gb + eip + eip.bandwidth_mbps = 9 rows.
	if len(rows) != 9 {
		t.Fatalf("Seed() = %d rows, want 9: %+v", len(rows), rows)
	}
	if rows[0] != (SeedRow{SKU: "ecs.m7n.xlarge.8", Family: FamilyVCPU, Amount: "4"}) || rows[1] != (SeedRow{SKU: "ecs.m7n.xlarge.8", Family: FamilyMemoryGiB, Amount: "32"}) {
		t.Fatalf("first rows = %+v", rows[:2])
	}
	for _, r := range rows {
		if !ValidFamily(r.Family) {
			t.Fatalf("seed row %+v has an unknown family", r)
		}
	}
	// 6 of 9 SKUs mapped; elb, nat.1 and vpc have no per-unit footprint.
	if got := Unseeded(); !reflect.DeepEqual(got, []string{"elb", "nat.1", "vpc"}) {
		t.Fatalf("Unseeded() = %v", got)
	}
}

func TestFamiliesAndStatus(t *testing.T) {
	if len(Families) != 7 || len(FamilyKeys()) != 7 {
		t.Fatalf("Families = %v", Families)
	}
	for _, f := range Families {
		if !ValidFamily(f.Key) || f.Label == "" || f.Unit == "" {
			t.Fatalf("family %+v", f)
		}
	}
	if ValidFamily("gpu") {
		t.Fatal("gpu is not a family")
	}
	cases := []struct {
		pct      float64
		hasTotal bool
		want     string
	}{
		{0, false, StatusUnset},
		{99, false, StatusUnset},
		{0, true, StatusOK},
		{69.9, true, StatusOK},
		{70, true, StatusWarn},
		{84.9, true, StatusWarn},
		{85, true, StatusCritical},
		{140, true, StatusCritical},
	}
	for _, c := range cases {
		if got := Status(c.pct, c.hasTotal); got != c.want {
			t.Errorf("Status(%v, %v) = %s, want %s", c.pct, c.hasTotal, got, c.want)
		}
	}
}
