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
	cases := map[string]Shape{
		"ecs.m7n.2xlarge.8":  {ResourceVCPU: "8", ResourceMemoryGiB: "64"},
		"ECS.S6.Large.2":     {ResourceVCPU: "2", ResourceMemoryGiB: "4"},
		"evs.ssd.gb":         {ResourceBlockSSD: "1"},
		"evs.hdd.gb":         {ResourceBlockHDD: "1"},
		"eip":                {ResourceEIP: "1"},
		"eip.bandwidth_mbps": {ResourceBandwidth: "1"},
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
	// 3 ECS × 2 resources + evs.ssd.gb + eip + eip.bandwidth_mbps = 9 rows.
	if len(rows) != 9 {
		t.Fatalf("Seed() = %d rows, want 9: %+v", len(rows), rows)
	}
	if rows[0] != (SeedRow{SKU: "ecs.m7n.xlarge.8", Resource: ResourceVCPU, Amount: "4"}) || rows[1] != (SeedRow{SKU: "ecs.m7n.xlarge.8", Resource: ResourceMemoryGiB, Amount: "32"}) {
		t.Fatalf("first rows = %+v", rows[:2])
	}
	// 6 of 9 SKUs mapped; elb, nat.1 and vpc have no per-unit shape.
	if got := Unseeded(); !reflect.DeepEqual(got, []string{"elb", "nat.1", "vpc"}) {
		t.Fatalf("Unseeded() = %v", got)
	}
}

// Resource kinds are DATA: the seeds carry labels and units, and a key
// nobody seeded is still a resource — it simply reads as its own name. There
// is no ValidResource, by design: a fixed list is what stopped two vCPU pools
// coexisting in one zone.
func TestResourceKindsAreDataNotAnEnum(t *testing.T) {
	if len(SeedResourceKinds) != 7 {
		t.Fatalf("SeedResourceKinds = %+v", SeedResourceKinds)
	}
	for _, k := range SeedResourceKinds {
		if k.Key == "" || k.Label == "" || k.Unit == "" || k.Position == 0 {
			t.Fatalf("seeded kind %+v is incomplete", k)
		}
	}
	gpu := KindOf("gpu_cards")
	if gpu.Key != "gpu_cards" || gpu.Label != "gpu_cards" || gpu.Position != 1000 {
		t.Fatalf("an unseeded kind must still be a kind: %+v", gpu)
	}
	// Ordering: seeded kinds in their order, everything else after, by key.
	keys := []string{"gpu_cards", "memory_gib", "accel", "vcpu"}
	SortResources(keys)
	if !reflect.DeepEqual(keys, []string{"vcpu", "memory_gib", "accel", "gpu_cards"}) {
		t.Fatalf("SortResources = %v", keys)
	}
	if NormResource("  VCPU ") != "vcpu" {
		t.Fatal("resource keys are compared lower-cased and trimmed")
	}
}

func TestClassesAndStatus(t *testing.T) {
	if len(Classes) != 3 || !reflect.DeepEqual(ClassKeys(), []string{ClassGuaranteed, ClassBurstable, ClassSpot}) {
		t.Fatalf("Classes = %+v", Classes)
	}
	for _, c := range Classes {
		if !ValidClass(c.Key) || c.Label == "" || c.Note == "" {
			t.Fatalf("class %+v", c)
		}
	}
	if ValidClass("reserved") || ValidClass("") {
		t.Fatal("only the three classes are classes")
	}
	cases := []struct {
		pct   float64
		sized bool
		want  string
	}{
		// An UNSIZED resource never reads ok, at any percentage: a figure
		// read from a structurally empty field would render as good news.
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
		if got := Status(c.pct, c.sized); got != c.want {
			t.Errorf("Status(%v, %v) = %s, want %s", c.pct, c.sized, got, c.want)
		}
	}
	if WorstStatus(nil) != StatusUnset {
		t.Fatal("a pool with no resource is unset")
	}
	if got := WorstStatus([]string{StatusOK, StatusCritical, StatusWarn, StatusUnset}); got != StatusCritical {
		t.Fatalf("WorstStatus = %s", got)
	}
	if got := WorstStatus([]string{StatusUnset, StatusOK}); got != StatusOK {
		t.Fatalf("WorstStatus = %s", got)
	}
}
