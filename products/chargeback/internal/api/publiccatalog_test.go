package api

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// Every family the page lists, one SKU of each shape the naming takes, and
// what the classifier must read out of it. A wrong family here is a service
// card in the wrong column; a wrong shape is a size dropdown that lies.
func TestClassifySKUEveryFamily(t *testing.T) {
	cases := []struct {
		sku  string
		want skuFacts
	}{
		// Compute
		{"ecs.s7n.xlarge.4", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "General purpose · 4 vCPU · 16 GB", Variant: "general", VariantName: "General purpose", VCPU: 4, MemoryGB: 16}},
		{"ecs.c7n.2xlarge.2", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "Compute-optimised · 8 vCPU · 16 GB", Variant: "compute", VariantName: "Compute-optimised", VCPU: 8, MemoryGB: 16}},
		{"ecs.m7n.large.8", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "Memory-optimised · 2 vCPU · 16 GB", Variant: "memory", VariantName: "Memory-optimised", VCPU: 2, MemoryGB: 16}},
		{"ecs.s7n.small.1", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "General purpose · 1 vCPU · 1 GB", Variant: "general", VariantName: "General purpose", VCPU: 1, MemoryGB: 1}},
		{"ecs.s7n.medium.4", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "General purpose · 1 vCPU · 4 GB", Variant: "general", VariantName: "General purpose", VCPU: 1, MemoryGB: 4}},
		{"ecs.c7n.24xlarge.4", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "Compute-optimised · 96 vCPU · 384 GB", Variant: "compute", VariantName: "Compute-optimised", VCPU: 96, MemoryGB: 384}},
		{"ecs.s6.large.2", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "General purpose · 2 vCPU · 4 GB", Variant: "general", VariantName: "General purpose", VCPU: 2, MemoryGB: 4}},
		{"as", skuFacts{Family: "compute", FamilyName: "Compute", Service: "as", ServiceName: "Auto Scaling", DisplayName: "Auto Scaling group"}},
		// Storage
		{"evs.ssd.gb", skuFacts{Family: "storage", FamilyName: "Storage", Service: "evs", ServiceName: "Block storage", DisplayName: "SSD", Variant: "ssd", VariantName: "SSD"}},
		{"evs.hdd.gb", skuFacts{Family: "storage", FamilyName: "Storage", Service: "evs", ServiceName: "Block storage", DisplayName: "HDD", Variant: "hdd", VariantName: "HDD"}},
		{"cbr.gb", skuFacts{Family: "storage", FamilyName: "Storage", Service: "cbr", ServiceName: "Backup", DisplayName: "Backup storage"}},
		{"ims.gb", skuFacts{Family: "storage", FamilyName: "Storage", Service: "ims", ServiceName: "Images", DisplayName: "Private image storage"}},
		// Networking
		{"eip", skuFacts{Family: "networking", FamilyName: "Networking", Service: "eip", ServiceName: "Elastic IP", DisplayName: "Elastic IP address", Variant: "address", VariantName: "Address"}},
		{"eip.bandwidth_mbps", skuFacts{Family: "networking", FamilyName: "Networking", Service: "eip", ServiceName: "Elastic IP", DisplayName: "Bandwidth per Mbps", Variant: "bandwidth", VariantName: "Bandwidth"}},
		{"elb", skuFacts{Family: "networking", FamilyName: "Networking", Service: "elb", ServiceName: "Load balancer", DisplayName: "Load balancer"}},
		{"nat.1", skuFacts{Family: "networking", FamilyName: "Networking", Service: "nat", ServiceName: "NAT gateway", DisplayName: "Small", Size: 1}},
		{"nat.4", skuFacts{Family: "networking", FamilyName: "Networking", Service: "nat", ServiceName: "NAT gateway", DisplayName: "Extra large", Size: 4}},
		{"vpc", skuFacts{Family: "networking", FamilyName: "Networking", Service: "vpc", ServiceName: "Virtual private cloud", DisplayName: "Virtual private cloud"}},
		{"vpcep", skuFacts{Family: "networking", FamilyName: "Networking", Service: "vpcep", ServiceName: "VPC endpoint", DisplayName: "VPC endpoint"}},
		{"dns", skuFacts{Family: "networking", FamilyName: "Networking", Service: "dns", ServiceName: "DNS", DisplayName: "DNS zone"}},
		{"waf", skuFacts{Family: "networking", FamilyName: "Networking", Service: "waf", ServiceName: "Web application firewall", DisplayName: "Web application firewall"}},
		// Databases
		{"rds.mysql.c7.xlarge.4.ha", skuFacts{Family: "databases", FamilyName: "Databases", Service: "rds-mysql", ServiceName: "RDS for MySQL", DisplayName: "4 vCPU · 16 GB · Primary + standby", VCPU: 4, MemoryGB: 16, Deployment: "ha"}},
		{"rds.pg.c7.16xlarge.2.single", skuFacts{Family: "databases", FamilyName: "Databases", Service: "rds-postgresql", ServiceName: "RDS for PostgreSQL", DisplayName: "64 vCPU · 128 GB · Single node", VCPU: 64, MemoryGB: 128, Deployment: "single"}},
		{"rds.storage.ha.gb", skuFacts{Family: "databases", FamilyName: "Databases", Service: "rds-storage", ServiceName: "RDS storage", DisplayName: "Primary + standby", Deployment: "ha"}},
		{"dds.mongodb.c7.2xlarge.4.single", skuFacts{Family: "databases", FamilyName: "Databases", Service: "dds", ServiceName: "MongoDB (DDS)", DisplayName: "8 vCPU · 32 GB · Single node", VCPU: 8, MemoryGB: 32, Deployment: "single"}},
		{"dds.storage.single.gb", skuFacts{Family: "databases", FamilyName: "Databases", Service: "dds-storage", ServiceName: "MongoDB storage", DisplayName: "Single node", Deployment: "single"}},
		{"gaussdb.16-vcpus-128-gb-mem-3-replicas-3-shards-3-co.ha", skuFacts{Family: "databases", FamilyName: "Databases", Service: "gaussdb", ServiceName: "GaussDB", DisplayName: "16 vCPU · 128 GB · Distributed · Primary + standby", Variant: "distributed", VariantName: "Distributed", VCPU: 16, MemoryGB: 128, Deployment: "ha"}},
		{"gaussdb.16vcpu-128gb-mem-1-primary-2-standby-enterpr.ha", skuFacts{Family: "databases", FamilyName: "Databases", Service: "gaussdb", ServiceName: "GaussDB", DisplayName: "16 vCPU · 128 GB · Centralized · Primary + standby", Variant: "centralized", VariantName: "Centralized", VCPU: 16, MemoryGB: 128, Deployment: "ha"}},
		{"gaussdb.8-vcpu-64gb-mem-3-replicas-3-shards-3-coordi.ha", skuFacts{Family: "databases", FamilyName: "Databases", Service: "gaussdb", ServiceName: "GaussDB", DisplayName: "8 vCPU · 64 GB · Distributed · Primary + standby", Variant: "distributed", VariantName: "Distributed", VCPU: 8, MemoryGB: 64, Deployment: "ha"}},
		{"gaussdb.32-vcpus-256-gb-mem-distributed-combined-bas.single", skuFacts{Family: "databases", FamilyName: "Databases", Service: "gaussdb", ServiceName: "GaussDB", DisplayName: "32 vCPU · 256 GB · Distributed · Single node", Variant: "distributed", VariantName: "Distributed", VCPU: 32, MemoryGB: 256, Deployment: "single"}},
		{"gaussdb.storage.single.gb", skuFacts{Family: "databases", FamilyName: "Databases", Service: "gaussdb-storage", ServiceName: "GaussDB storage", DisplayName: "Single node", Deployment: "single"}},
		// Containers
		{"cce.cloud-container-engine-cce-excluding-vms-50-", skuFacts{Family: "containers", FamilyName: "Containers", Service: "cce", ServiceName: "CCE cluster", DisplayName: "Up to 50 nodes", Size: 50}},
		{"cce.cloud-container-engine-cce-excluding-vms-200", skuFacts{Family: "containers", FamilyName: "Containers", Service: "cce", ServiceName: "CCE cluster", DisplayName: "Up to 200 nodes", Size: 200}},
		{"k8s.vcpu", skuFacts{Family: "containers", FamilyName: "Containers", Service: "k8s", ServiceName: "Kubernetes capacity", DisplayName: "vCPU", Variant: "vcpu", VariantName: "vCPU"}},
		{"k8s.mem_gb", skuFacts{Family: "containers", FamilyName: "Containers", Service: "k8s", ServiceName: "Kubernetes capacity", DisplayName: "Memory per GiB", Variant: "memory", VariantName: "Memory"}},
		{"k8s.pvc_gb", skuFacts{Family: "containers", FamilyName: "Containers", Service: "k8s", ServiceName: "Kubernetes capacity", DisplayName: "Persistent storage per GB", Variant: "storage", VariantName: "Persistent storage"}},
		// Platform plans
		{"plan.m", skuFacts{Family: "plans", FamilyName: "Platform plans", Service: "plan", ServiceName: "Platform plans", DisplayName: "M plan · 4 vCPU · 8 GB", Variant: "m", VariantName: "M", VCPU: 4, MemoryGB: 8}},
		{"plan.xl", skuFacts{Family: "plans", FamilyName: "Platform plans", Service: "plan", ServiceName: "Platform plans", DisplayName: "XL plan · 16 vCPU · 32 GB", Variant: "xl", VariantName: "XL", VCPU: 16, MemoryGB: 32}},
		// Unknown prefixes: kept, under Other services, grouped by first token.
		{"obs.standard.gb", skuFacts{Family: "other", FamilyName: "Other services", Service: "obs", ServiceName: "OBS", DisplayName: "obs.standard.gb"}},
		{"sfs-turbo.gb", skuFacts{Family: "other", FamilyName: "Other services", Service: "sfs", ServiceName: "SFS", DisplayName: "sfs-turbo.gb"}},
		// A known head with a shape the naming does not promise still lands
		// in its service, unshaped, rather than being dropped.
		{"ecs.gpu-special", skuFacts{Family: "compute", FamilyName: "Compute", Service: "ecs", ServiceName: "Elastic Cloud Server", DisplayName: "ecs.gpu-special"}},
		{"rds.unknown.thing", skuFacts{Family: "other", FamilyName: "Other services", Service: "rds", ServiceName: "RDS", DisplayName: "rds.unknown.thing"}},
	}
	for _, tc := range cases {
		got := classifySKU(tc.sku)
		if got != tc.want {
			t.Errorf("classifySKU(%q)\n got  %+v\n want %+v", tc.sku, got, tc.want)
		}
	}
	// Case and whitespace never matter, and serviceOf is the same reading.
	if classifySKU("  ECS.S7N.XLARGE.4 ") != classifySKU("ecs.s7n.xlarge.4") || serviceOf("RDS.PG.c7.large.2.ha") != "rds-postgresql" {
		t.Fatal("classifySKU must normalise case and whitespace")
	}
}

// The size table the naming promises: large = 2, xlarge = 4, <N>xlarge = 4N,
// small and medium = 1; memory = vCPU × the trailing ratio.
func TestSizeTable(t *testing.T) {
	for size, want := range map[string]int{"small": 1, "medium": 1, "large": 2, "xlarge": 4, "2xlarge": 8, "3xlarge": 12, "4xlarge": 16, "6xlarge": 24, "8xlarge": 32, "12xlarge": 48, "16xlarge": 64, "24xlarge": 96} {
		if got, ok := sizeVCPU(size); !ok || got != want {
			t.Errorf("sizeVCPU(%q) = %d,%v want %d", size, got, ok, want)
		}
	}
	for _, bad := range []string{"", "huge", "0xlarge", "xxlarge", "large2"} {
		if _, ok := sizeVCPU(bad); ok {
			t.Errorf("sizeVCPU(%q) must not be a size", bad)
		}
	}
	if v, m, ok := shapeOf("4xlarge", "8"); !ok || v != 16 || m != 128 {
		t.Fatalf("shapeOf(4xlarge, 8) = %d %d %v", v, m, ok)
	}
	if _, _, ok := shapeOf("large", "x"); ok {
		t.Fatal("a non-numeric ratio is not a shape")
	}
}

// The facts ride on the catalog wire additively: the fields the page always
// read (sku, service, unit, unit_price, monthly) are still there, and the
// shape fields are omitted when the SKU has none.
func TestCatalogSKUWire(t *testing.T) {
	b, err := json.Marshal(catalogSKU{SKU: "evs.ssd.gb", skuFacts: classifySKU("evs.ssd.gb"), Unit: "gb-hour", UnitPrice: "0.00013699", Monthly: "0.100003"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sku", "service", "service_name", "family", "family_name", "display_name", "variant", "unit", "unit_price", "monthly"} {
		if _, ok := m[k]; !ok {
			t.Errorf("catalog SKU lacks %q: %s", k, b)
		}
	}
	for _, k := range []string{"vcpu", "memory_gb", "deployment", "size"} {
		if _, ok := m[k]; ok {
			t.Errorf("a storage SKU must not carry %q: %s", k, b)
		}
	}
	if m["service"] != "evs" || m["family"] != "storage" {
		t.Fatalf("wire = %s", b)
	}
}

// liveListSKUs is the National Cloud 2026 list as a Sovereign publishes it
// (every distinct SKU of the hw307 book): the classifier must place every
// row in a named family and read a shape wherever the name carries one.
var liveListSKUs = strings.Fields(`
as cbr.gb
cce.cloud-container-engine-cce-excluding-vms-100 cce.cloud-container-engine-cce-excluding-vms-200 cce.cloud-container-engine-cce-excluding-vms-50-
dds.mongodb.c7.16xlarge.4.single dds.mongodb.c7.2xlarge.4.single dds.mongodb.c7.4xlarge.4.single dds.mongodb.c7.8xlarge.4.single dds.mongodb.c7.large.2.single dds.mongodb.c7.xlarge.4.single
dds.storage.ha.gb dds.storage.single.gb dns
ecs.c7n.12xlarge.2 ecs.c7n.12xlarge.4 ecs.c7n.16xlarge.2 ecs.c7n.16xlarge.4 ecs.c7n.24xlarge.2 ecs.c7n.24xlarge.4 ecs.c7n.2xlarge.2 ecs.c7n.2xlarge.4 ecs.c7n.3xlarge.2 ecs.c7n.3xlarge.4
ecs.c7n.4xlarge.2 ecs.c7n.4xlarge.4 ecs.c7n.6xlarge.2 ecs.c7n.6xlarge.4 ecs.c7n.8xlarge.2 ecs.c7n.8xlarge.4 ecs.c7n.large.2 ecs.c7n.large.4 ecs.c7n.xlarge.2 ecs.c7n.xlarge.4
ecs.m7n.12xlarge.8 ecs.m7n.16xlarge.8 ecs.m7n.24xlarge.8 ecs.m7n.2xlarge.8 ecs.m7n.3xlarge.8 ecs.m7n.4xlarge.8 ecs.m7n.6xlarge.8 ecs.m7n.8xlarge.8 ecs.m7n.large.8 ecs.m7n.xlarge.8
ecs.s7n.2xlarge.2 ecs.s7n.2xlarge.4 ecs.s7n.4xlarge.2 ecs.s7n.4xlarge.4 ecs.s7n.large.2 ecs.s7n.large.4 ecs.s7n.medium.2 ecs.s7n.medium.4 ecs.s7n.small.1 ecs.s7n.xlarge.2 ecs.s7n.xlarge.4
eip eip.bandwidth_mbps elb evs.hdd.gb evs.ssd.gb
gaussdb.16-vcpus-128-gb-mem-3-replicas-3-shards-3-co.ha gaussdb.16-vcpus-128-gb-mem-distributed-combined-bas.single gaussdb.16-vcpus-64-gb-mem-distributed-combined-basi.single
gaussdb.16vcpu-128gb-mem-1-primary-2-standby-enterpr.ha gaussdb.32-vcpus-128-gb-mem-distributed-combined-bas.single gaussdb.32-vcpus-256-gb-mem-3-replicas-3-shards-3-co.ha
gaussdb.32-vcpus-256-gb-mem-distributed-combined-bas.single gaussdb.32vcpu-256gb-mem-1-primary-2-standby-enterpr.ha gaussdb.64-vcpus-256-gb-mem-distributed-combined-bas.single
gaussdb.64-vcpus-512-gb-mem-3-replicas-3-shards-3-co.ha gaussdb.64-vcpus-512-gb-mem-distributed-combined-bas.single gaussdb.64vcpu-512gb-mem-1-primary-2-standby-enterpr.ha
gaussdb.8-vcpu-64gb-mem-3-replicas-3-shards-3-coordi.ha gaussdb.8vcpu-64gb-mem-1-primary-2-standby-enterpris.ha gaussdb.storage.single.gb
ims.gb nat.1 nat.2 nat.3 nat.4
rds.mysql.c7.16xlarge.2.ha rds.mysql.c7.16xlarge.2.single rds.mysql.c7.16xlarge.4.ha rds.mysql.c7.16xlarge.4.single rds.mysql.c7.16xlarge.8.ha rds.mysql.c7.2xlarge.2.ha rds.mysql.c7.2xlarge.2.single
rds.mysql.c7.2xlarge.4.ha rds.mysql.c7.2xlarge.4.single rds.mysql.c7.4xlarge.2.ha rds.mysql.c7.4xlarge.2.single rds.mysql.c7.4xlarge.4.ha rds.mysql.c7.4xlarge.4.single rds.mysql.c7.8xlarge.2.ha
rds.mysql.c7.8xlarge.2.single rds.mysql.c7.8xlarge.4.ha rds.mysql.c7.8xlarge.4.single rds.mysql.c7.large.2.ha rds.mysql.c7.large.2.single rds.mysql.c7.large.4.ha rds.mysql.c7.large.4.single
rds.mysql.c7.xlarge.2.ha rds.mysql.c7.xlarge.2.single rds.mysql.c7.xlarge.4.ha rds.mysql.c7.xlarge.4.single
rds.pg.c7.16xlarge.2.ha rds.pg.c7.16xlarge.2.single rds.pg.c7.16xlarge.4.ha rds.pg.c7.16xlarge.4.single rds.pg.c7.2xlarge.2.ha rds.pg.c7.2xlarge.2.single rds.pg.c7.2xlarge.4.ha rds.pg.c7.2xlarge.4.single
rds.pg.c7.4xlarge.2.ha rds.pg.c7.4xlarge.2.single rds.pg.c7.4xlarge.4.ha rds.pg.c7.4xlarge.4.single rds.pg.c7.8xlarge.2.ha rds.pg.c7.8xlarge.2.single rds.pg.c7.8xlarge.4.ha rds.pg.c7.8xlarge.4.single
rds.pg.c7.large.2.ha rds.pg.c7.large.2.single rds.pg.c7.large.4.ha rds.pg.c7.large.4.single rds.pg.c7.xlarge.2.ha rds.pg.c7.xlarge.2.single rds.pg.c7.xlarge.4.ha rds.pg.c7.xlarge.4.single
rds.storage.ha.gb rds.storage.single.gb vpc vpcep waf
plan.s plan.m plan.l plan.xl k8s.vcpu k8s.mem_gb k8s.pvc_gb
`)

// TestClassifyLiveList runs the classifier over the whole published list:
// nothing lands in Other services, every instance SKU has a shape, every
// database SKU a deployment, and run with -v it prints the families and
// services the page will show.
func TestClassifyLiveList(t *testing.T) {
	byService := map[string][]string{}
	familyOf := map[string]string{}
	for _, sku := range liveListSKUs {
		f := classifySKU(sku)
		if f.Family == familyOther {
			t.Errorf("%s landed in Other services", sku)
		}
		if f.DisplayName == "" || f.ServiceName == "" || f.FamilyName == "" {
			t.Errorf("%s has an empty name: %+v", sku, f)
		}
		instance := strings.HasPrefix(sku, "ecs.") || strings.HasPrefix(sku, "rds.mysql") || strings.HasPrefix(sku, "rds.pg") || strings.HasPrefix(sku, "dds.mongodb") || (strings.HasPrefix(sku, "gaussdb.") && !strings.Contains(sku, ".storage."))
		if instance && (f.VCPU == 0 || f.MemoryGB == 0) {
			t.Errorf("%s: no shape read: %+v", sku, f)
		}
		if f.Family == familyDatabases && f.Deployment == "" {
			t.Errorf("%s: no deployment read: %+v", sku, f)
		}
		if strings.HasPrefix(sku, "cce.") && f.Size == 0 {
			t.Errorf("%s: no node count read", sku)
		}
		key := f.FamilyName + " › " + f.ServiceName
		byService[key] = append(byService[key], f.DisplayName)
		familyOf[key] = f.Family
	}
	keys := make([]string, 0, len(byService))
	for k := range byService {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-40s %3d option(s)  e.g. %s", k, len(byService[k]), byService[k][0])
	}
	// 2 compute + 3 storage + 7 networking + 7 databases (three engines and
	// their storage, GaussDB and its storage) + 2 containers + 1 plans.
	if len(keys) != 22 {
		t.Fatalf("the live list yields %d services, want 22: %v", len(keys), keys)
	}
}
