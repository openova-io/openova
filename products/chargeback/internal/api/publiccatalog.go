package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The public catalog's taxonomy (DESIGN.md §12.5). A prospect chooses a
// SERVICE inside a PRODUCT FAMILY — Compute → Elastic Cloud Server — and a
// configurator turns friendly choices ("4 vCPU · 16 GB", "always on") into
// the SKU lines the estimate prices. Which family and service a SKU belongs
// to, and what shape it is, is decided HERE, once, from the SKU name the
// price book carries; the page never parses a SKU. A prefix nobody has
// classified lands in "Other services" with the SKU as its name — nothing
// the book prices is ever dropped from the catalog.
//
// The Huawei naming the classifier reads:
//
//	ecs.<flavor>.<size>.<ratio>              s7n general · c7n compute · m7n memory
//	rds.mysql|pg.<flavor>.<size>.<ratio>.<single|ha>
//	dds.mongodb.<flavor>.<size>.<ratio>.<single|ha>
//	gaussdb.<N>-vcpus-<M>-gb-mem-…<.single|.ha>
//	<engine>.storage.<single|ha>.gb           the engine's storage
//	evs.<ssd|hdd>.gb · cbr.gb · ims.gb
//	eip · eip.bandwidth_mbps · elb · nat.<1..4> · vpc · vpcep · dns · waf · as
//	cce.…-<nodes>                             50 / 100 / 200 nodes
//	k8s.vcpu · k8s.mem_gb · k8s.pvc_gb        pay-per-use capacity
//	plan.<s|m|l|xl>                           the catalog plans
//
// A size is large = 2 vCPU, xlarge = 4, <N>xlarge = 4N (2xlarge = 8 …
// 24xlarge = 96), and the trailing ratio is memory per vCPU, so
// memory = vCPU × ratio; small and medium are 1 vCPU.

// skuFacts is what the catalog says about one SKU beyond its price.
type skuFacts struct {
	// Family and Service are stable keys the page groups and configures by;
	// the *Name fields are the words a prospect reads.
	Family      string `json:"family"`
	FamilyName  string `json:"family_name"`
	Service     string `json:"service"`
	ServiceName string `json:"service_name"`
	// DisplayName is the option label inside a configurator: "General
	// purpose · 4 vCPU · 16 GB", "SSD", "NAT gateway · Large".
	DisplayName string `json:"display_name"`
	// Variant tells SKUs of one service apart when a configurator offers
	// them as a first choice: the ECS class (general / compute / memory),
	// the EVS media (ssd / hdd), address vs bandwidth on an EIP, the k8s
	// meter, the GaussDB topology.
	Variant     string `json:"variant,omitempty"`
	VariantName string `json:"variant_name,omitempty"`
	// The shape of an instance SKU, when the name carries one.
	VCPU     int `json:"vcpu,omitempty"`
	MemoryGB int `json:"memory_gb,omitempty"`
	// Deployment is single or ha on a database SKU (instance or storage).
	Deployment string `json:"deployment,omitempty"`
	// Size is the ordinal of a sized service: the NAT gateway spec 1..4,
	// the node count of a CCE cluster.
	Size int `json:"size,omitempty"`
}

// The families, in the order the page lists them.
const (
	familyCompute    = "compute"
	familyStorage    = "storage"
	familyNetworking = "networking"
	familyDatabases  = "databases"
	familyContainers = "containers"
	familyPlans      = "plans"
	familyOther      = "other"
)

var familyNames = map[string]string{
	familyCompute:    "Compute",
	familyStorage:    "Storage",
	familyNetworking: "Networking",
	familyDatabases:  "Databases",
	familyContainers: "Containers",
	familyPlans:      "Platform plans",
	familyOther:      "Other services",
}

// service is one row of the catalog's service list.
type service struct {
	family, name string
}

// services maps the classifier's service keys to their family and name.
// The key is what the page's configurators are keyed on; a new prefix is
// added here and in classifySKU, nowhere else.
var services = map[string]service{
	"ecs":             {familyCompute, "Elastic Cloud Server"},
	"as":              {familyCompute, "Auto Scaling"},
	"evs":             {familyStorage, "Block storage"},
	"cbr":             {familyStorage, "Backup"},
	"ims":             {familyStorage, "Images"},
	"eip":             {familyNetworking, "Elastic IP"},
	"elb":             {familyNetworking, "Load balancer"},
	"nat":             {familyNetworking, "NAT gateway"},
	"vpc":             {familyNetworking, "Virtual private cloud"},
	"vpcep":           {familyNetworking, "VPC endpoint"},
	"dns":             {familyNetworking, "DNS"},
	"waf":             {familyNetworking, "Web application firewall"},
	"rds-mysql":       {familyDatabases, "RDS for MySQL"},
	"rds-postgresql":  {familyDatabases, "RDS for PostgreSQL"},
	"rds-storage":     {familyDatabases, "RDS storage"},
	"dds":             {familyDatabases, "MongoDB (DDS)"},
	"dds-storage":     {familyDatabases, "MongoDB storage"},
	"gaussdb":         {familyDatabases, "GaussDB"},
	"gaussdb-storage": {familyDatabases, "GaussDB storage"},
	"cce":             {familyContainers, "CCE cluster"},
	"k8s":             {familyContainers, "Kubernetes capacity"},
	store.PlanKind:    {familyPlans, "Platform plans"},
}

var (
	// gaussdbShape reads "16-vcpus-128-gb-mem", "16vcpu-128gb-mem" and
	// "8-vcpu-64gb-mem" alike.
	gaussdbShape = regexp.MustCompile(`^(\d+)-?vcpus?-(\d+)-?gb`)
	// trailingNumber is the node count at the end of a CCE SKU, with or
	// without the dash the list's truncation leaves ("…-vms-50-").
	trailingNumber = regexp.MustCompile(`-(\d+)-?$`)
)

// sizeVCPU is the vCPU count a Huawei size token names; ok is false for a
// token that is not a size.
func sizeVCPU(size string) (int, bool) {
	switch size {
	case "small", "medium":
		return 1, true
	case "large":
		return 2, true
	case "xlarge":
		return 4, true
	}
	if n, ok := strings.CutSuffix(size, "xlarge"); ok {
		if k, err := strconv.Atoi(n); err == nil && k > 0 {
			return 4 * k, true
		}
	}
	return 0, false
}

// shapeOf parses "<size>.<ratio>" into vCPU and memory; ok is false when
// either token is not what the naming promises.
func shapeOf(size, ratio string) (vcpu, memGB int, ok bool) {
	v, ok := sizeVCPU(size)
	if !ok {
		return 0, 0, false
	}
	r, err := strconv.Atoi(ratio)
	if err != nil || r <= 0 {
		return 0, 0, false
	}
	return v, v * r, true
}

// shapeLabel is "4 vCPU · 16 GB".
func shapeLabel(vcpu, memGB int) string {
	return fmt.Sprintf("%d vCPU · %d GB", vcpu, memGB)
}

// deploymentName is the word for single / ha.
func deploymentName(d string) string {
	switch d {
	case "ha":
		return "Primary + standby"
	case "single":
		return "Single node"
	}
	return d
}

var ecsClasses = map[byte][2]string{
	's': {"general", "General purpose"},
	'c': {"compute", "Compute-optimised"},
	'm': {"memory", "Memory-optimised"},
}

var natSizes = map[int]string{1: "Small", 2: "Medium", 3: "Large", 4: "Extra large"}

// classifySKU is the one place a SKU name becomes a family, a service and a
// shape. It never fails: what it cannot read stays a SKU in "Other
// services", so a newly imported list is priceable before anyone has
// taught the classifier its prefix.
func classifySKU(sku string) skuFacts {
	s := strings.ToLower(strings.TrimSpace(sku))
	parts := strings.Split(s, ".")
	head := parts[0]
	f := skuFacts{DisplayName: s}

	switch head {
	case "ecs":
		f.setService("ecs")
		if len(parts) == 4 && parts[1] != "" {
			flavor := parts[1]
			if class, ok := ecsClasses[flavor[0]]; ok {
				f.Variant, f.VariantName = class[0], class[1]
			} else {
				f.Variant, f.VariantName = flavor, strings.ToUpper(flavor)
			}
			if v, m, ok := shapeOf(parts[2], parts[3]); ok {
				f.VCPU, f.MemoryGB = v, m
				f.DisplayName = f.VariantName + " · " + shapeLabel(v, m)
			}
		}
	case "as":
		f.setService("as")
		f.DisplayName = "Auto Scaling group"
	case "evs":
		f.setService("evs")
		if len(parts) == 3 && parts[2] == "gb" {
			f.Variant = parts[1]
			f.VariantName = strings.ToUpper(parts[1])
			f.DisplayName = f.VariantName
		}
	case "cbr":
		f.setService("cbr")
		f.DisplayName = "Backup storage"
	case "ims":
		f.setService("ims")
		f.DisplayName = "Private image storage"
	case "eip":
		f.setService("eip")
		if len(parts) == 1 {
			f.Variant, f.VariantName, f.DisplayName = "address", "Address", "Elastic IP address"
		} else if parts[1] == "bandwidth_mbps" {
			f.Variant, f.VariantName, f.DisplayName = "bandwidth", "Bandwidth", "Bandwidth per Mbps"
		}
	case "elb":
		f.setService("elb")
		f.DisplayName = "Load balancer"
	case "nat":
		f.setService("nat")
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
				f.Size = n
				if name, ok := natSizes[n]; ok {
					f.DisplayName = name
				} else {
					f.DisplayName = "Size " + parts[1]
				}
			}
		}
	case "vpc":
		f.setService("vpc")
		f.DisplayName = "Virtual private cloud"
	case "vpcep":
		f.setService("vpcep")
		f.DisplayName = "VPC endpoint"
	case "dns":
		f.setService("dns")
		f.DisplayName = "DNS zone"
	case "waf":
		f.setService("waf")
		f.DisplayName = "Web application firewall"
	case "rds":
		classifyEngine(&f, parts, map[string]string{"mysql": "rds-mysql", "pg": "rds-postgresql"}, "rds-storage")
	case "dds":
		classifyEngine(&f, parts, map[string]string{"mongodb": "dds"}, "dds-storage")
	case "gaussdb":
		classifyGaussDB(&f, parts)
	case "cce":
		f.setService("cce")
		if len(parts) >= 2 {
			if m := trailingNumber.FindStringSubmatch(parts[len(parts)-1]); m != nil {
				f.Size, _ = strconv.Atoi(m[1])
				f.DisplayName = fmt.Sprintf("Up to %d nodes", f.Size)
			}
		}
	case "k8s":
		f.setService("k8s")
		if len(parts) == 2 {
			switch parts[1] {
			case "vcpu":
				f.Variant, f.VariantName, f.DisplayName = "vcpu", "vCPU", "vCPU"
			case "mem_gb":
				f.Variant, f.VariantName, f.DisplayName = "memory", "Memory", "Memory per GiB"
			case "pvc_gb":
				f.Variant, f.VariantName, f.DisplayName = "storage", "Persistent storage", "Persistent storage per GB"
			}
		}
	case store.PlanKind:
		f.setService(store.PlanKind)
		if len(parts) == 2 {
			slug := parts[1]
			f.Variant, f.VariantName = slug, store.PlanName(slug)
			f.DisplayName = store.PlanName(slug) + " plan"
			if v, m, ok := store.PlanShape(slug); ok {
				f.VCPU, f.MemoryGB = v, m
				f.DisplayName += " · " + shapeLabel(v, m)
			}
		}
	default:
		// Unclassified: its own service under "Other services", named by
		// the first token so a book of obs.* rows still reads as one group.
		f.Family, f.FamilyName = familyOther, familyNames[familyOther]
		key := head
		if i := strings.IndexAny(key, "-_/:"); i > 0 {
			key = key[:i]
		}
		if key == "" {
			key = s
		}
		f.Service, f.ServiceName = key, strings.ToUpper(key)
	}
	return f
}

func (f *skuFacts) setService(key string) {
	svc := services[key]
	f.Service, f.ServiceName = key, svc.name
	f.Family, f.FamilyName = svc.family, familyNames[svc.family]
}

// classifyEngine reads the two shapes an RDS / DDS SKU takes:
// <head>.<engine>.<flavor>.<size>.<ratio>.<single|ha> and
// <head>.storage.<single|ha>.gb.
func classifyEngine(f *skuFacts, parts []string, engines map[string]string, storageKey string) {
	head := parts[0]
	if len(parts) == 4 && parts[1] == "storage" && parts[3] == "gb" {
		f.setService(storageKey)
		f.Deployment = parts[2]
		f.DisplayName = deploymentName(parts[2])
		return
	}
	if len(parts) == 6 {
		if key, ok := engines[parts[1]]; ok {
			f.setService(key)
			f.Deployment = parts[5]
			if v, m, ok := shapeOf(parts[3], parts[4]); ok {
				f.VCPU, f.MemoryGB = v, m
				f.DisplayName = shapeLabel(v, m) + " · " + deploymentName(parts[5])
			}
			return
		}
	}
	f.Family, f.FamilyName = familyOther, familyNames[familyOther]
	f.Service, f.ServiceName = head, strings.ToUpper(head)
}

// classifyGaussDB reads gaussdb.storage.<single|ha>.gb and
// gaussdb.<N>-vcpus-<M>-gb-mem-<topology…>.<single|ha>, where the topology
// words say whether the engine is centralized (primary + standby) or
// distributed (shards).
func classifyGaussDB(f *skuFacts, parts []string) {
	if len(parts) == 4 && parts[1] == "storage" && parts[3] == "gb" {
		f.setService("gaussdb-storage")
		f.Deployment = parts[2]
		f.DisplayName = deploymentName(parts[2])
		return
	}
	f.setService("gaussdb")
	if len(parts) < 2 {
		return
	}
	slug := parts[1]
	if len(parts) == 3 && (parts[2] == "single" || parts[2] == "ha") {
		f.Deployment = parts[2]
	}
	switch {
	case strings.Contains(slug, "primary") || strings.Contains(slug, "standby"):
		f.Variant, f.VariantName = "centralized", "Centralized"
	case strings.Contains(slug, "shard") || strings.Contains(slug, "distributed") || strings.Contains(slug, "replica"):
		f.Variant, f.VariantName = "distributed", "Distributed"
	}
	if m := gaussdbShape.FindStringSubmatch(slug); m != nil {
		f.VCPU, _ = strconv.Atoi(m[1])
		f.MemoryGB, _ = strconv.Atoi(m[2])
		f.DisplayName = shapeLabel(f.VCPU, f.MemoryGB)
		if f.VariantName != "" {
			f.DisplayName += " · " + f.VariantName
		}
		if f.Deployment != "" {
			f.DisplayName += " · " + deploymentName(f.Deployment)
		}
	}
}

// serviceOf is the service key of a SKU — what the catalog groups by.
func serviceOf(sku string) string { return classifySKU(sku).Service }
