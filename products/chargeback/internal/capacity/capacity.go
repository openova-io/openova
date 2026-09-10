// Package capacity is the pure part of capacity management (DESIGN.md §11,
// EPIC #6867, founder requirement 2026-09-11): the resource families a
// region's availability zones are measured in, and how much of each family
// one unit of a SKU consumes — its FOOTPRINT — derived from the SKU name
// where the name encodes it.
//
// It has no database and no HTTP. The store keeps the regions, zones, pools
// and the explicit footprint rows and computes consumption; the API turns a
// request into store calls. Everything here is deterministic on its input,
// so it is pinned by plain unit tests.
package capacity

import (
	"sort"
	"strconv"
	"strings"
)

// Family is one kind of pooled capacity an availability zone holds.
type Family struct {
	Key   string `json:"family"`
	Label string `json:"label"`
	// Unit is what a pool total and a footprint amount count in.
	Unit string `json:"unit"`
}

// The seven families. Totals are entered per (zone, family) by the operator
// until a capacity collector fills them; consumption is derived from
// metering through the footprints.
const (
	FamilyVCPU      = "vcpu"
	FamilyMemoryGiB = "memory_gib"
	FamilyBlockSSD  = "block_ssd_gib"
	FamilyBlockHDD  = "block_hdd_gib"
	FamilyObject    = "object_gib"
	FamilyEIP       = "eip_addresses"
	FamilyBandwidth = "bandwidth_mbps"
)

// Families lists every family in a stable, display order. The set is also
// the CHECK constraint on capacity_pools.family and sku_footprints.family
// (store.capacityMigrationSQL is generated from it).
var Families = []Family{
	{Key: FamilyVCPU, Label: "vCPU", Unit: "vCPU"},
	{Key: FamilyMemoryGiB, Label: "Memory", Unit: "GiB"},
	{Key: FamilyBlockSSD, Label: "Block SSD", Unit: "GiB"},
	{Key: FamilyBlockHDD, Label: "Block HDD", Unit: "GiB"},
	{Key: FamilyObject, Label: "Object storage", Unit: "GiB"},
	{Key: FamilyEIP, Label: "Elastic IPs", Unit: "addresses"},
	{Key: FamilyBandwidth, Label: "Bandwidth", Unit: "Mbps"},
}

// ValidFamily reports whether key is one of Families.
func ValidFamily(key string) bool {
	for _, f := range Families {
		if f.Key == key {
			return true
		}
	}
	return false
}

// FamilyKeys is Families as keys, in the same order.
func FamilyKeys() []string {
	out := make([]string, len(Families))
	for i, f := range Families {
		out[i] = f.Key
	}
	return out
}

// Footprint is how much of each family ONE unit of a SKU consumes, as exact
// decimal text keyed by family (store.Decimal is a string too, so the store
// converts without arithmetic). An ECS instance-hour of m7n.2xlarge.8 is
// {vcpu: 8, memory_gib: 64}; a GB-hour of evs.ssd.gb is {block_ssd_gib: 1}.
type Footprint map[string]string

// Sources a footprint can come from, reported on the wire so the page can
// say which rows an operator typed and which the name implied.
const (
	// SourceManual is a row the operator wrote or edited.
	SourceManual = "manual"
	// SourceSeed is a row the migration seeded from the National Cloud list.
	SourceSeed = "seed"
	// SourceDerived is not a row at all: the footprint the SKU name implies,
	// computed at read time for a metered SKU that has no row.
	SourceDerived = "derived"
)

// ecsSizes is the vCPU count each Huawei ECS size token stands for. The
// flavour name is <family><generation>[<variant>].<size>.<ratio>:
// c7n.large.2 is 2 vCPU with a 1:2 vCPU:GiB ratio, so 4 GiB; m7n.2xlarge.8
// is 8 vCPU at 1:8, so 64 GiB. The seeding descriptions in
// internal/synth/rates.go ("m7n.xlarge.8 — 4 vCPU 32 GB") and the flavour
// vcpus/ram_mb the ECS lister records agree with this table.
var ecsSizes = map[string]int{
	"small":  1,
	"medium": 1,
	"large":  2,
	"xlarge": 4,
}

// ParseECSFlavor reads vCPU and memory out of a Huawei ECS flavour name.
// ok is false for any name the convention does not cover — the caller then
// reports the SKU as "no footprint" rather than guessing.
func ParseECSFlavor(name string) (vcpus, memoryGiB int, ok bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(name)), ".")
	if len(parts) != 3 || parts[0] == "" {
		return 0, 0, false
	}
	size := parts[1]
	if n, found := ecsSizes[size]; found {
		vcpus = n
	} else if mult, rest, found := strings.Cut(size, "xlarge"); found && rest == "" {
		m, err := strconv.Atoi(mult)
		if err != nil || m < 2 {
			return 0, 0, false
		}
		vcpus = 4 * m
	} else {
		return 0, 0, false
	}
	ratio, err := strconv.Atoi(parts[2])
	if err != nil || ratio < 1 {
		return 0, 0, false
	}
	return vcpus, vcpus * ratio, true
}

// Derive is the footprint a SKU name implies, or nil when the name does not
// say. Only unambiguous shapes derive:
//
//	ecs.<flavour>        vcpu + memory_gib from the flavour name
//	evs.ssd.gb           block_ssd_gib 1
//	evs.hdd.gb           block_hdd_gib 1
//	eip                  eip_addresses 1
//	eip.bandwidth_mbps   bandwidth_mbps 1
//
// Platform meters (k8s.*, plan.*) derive nothing: they run on the cloud's
// instances, which the cloud SKUs already count — deriving them too would
// consume the same vCPU twice. Database storage, backup and image SKUs
// derive nothing either: their storage class is not in the name.
func Derive(sku string) Footprint {
	s := strings.ToLower(strings.TrimSpace(sku))
	switch {
	case s == "eip":
		return Footprint{FamilyEIP: "1"}
	case s == "eip.bandwidth_mbps":
		return Footprint{FamilyBandwidth: "1"}
	case s == "evs.ssd.gb":
		return Footprint{FamilyBlockSSD: "1"}
	case s == "evs.hdd.gb":
		return Footprint{FamilyBlockHDD: "1"}
	case strings.HasPrefix(s, "ecs."):
		v, m, ok := ParseECSFlavor(strings.TrimPrefix(s, "ecs."))
		if !ok {
			return nil
		}
		return Footprint{FamilyVCPU: strconv.Itoa(v), FamilyMemoryGiB: strconv.Itoa(m)}
	}
	return nil
}

// SeedSKUs are the SKUs of the National Cloud list price book
// (internal/synth NationalCloudRates — TestSeedCoversNationalCloudList pins
// the two equal). The migration seeds a footprint row for each one Derive
// covers; the rest (elb, nat.<spec>, vpc) have no per-unit footprint in any
// of the seven families and are reported as such.
var SeedSKUs = []string{
	"ecs.m7n.xlarge.8",
	"ecs.m7n.2xlarge.8",
	"ecs.s7n.2xlarge.2",
	"evs.ssd.gb",
	"eip",
	"eip.bandwidth_mbps",
	"elb",
	"nat.1",
	"vpc",
}

// SeedRow is one (sku, family, amount) the migration writes.
type SeedRow struct {
	SKU    string
	Family string
	Amount string
}

// Seed is every footprint row the migration writes, in a stable order:
// the SeedSKUs Derive covers, one row per family.
func Seed() []SeedRow {
	var out []SeedRow
	for _, sku := range SeedSKUs {
		fp := Derive(sku)
		if fp == nil {
			continue
		}
		for _, fam := range sortedFamilies(fp) {
			out = append(out, SeedRow{SKU: sku, Family: fam, Amount: fp[fam]})
		}
	}
	return out
}

// Unseeded lists the SeedSKUs that derive nothing — what the page shows as
// "no footprint" until the operator writes one.
func Unseeded() []string {
	var out []string
	for _, sku := range SeedSKUs {
		if Derive(sku) == nil {
			out = append(out, sku)
		}
	}
	return out
}

// sortedFamilies orders a footprint's families in Families order.
func sortedFamilies(fp Footprint) []string {
	keys := make([]string, 0, len(fp))
	for k := range fp {
		keys = append(keys, k)
	}
	idx := map[string]int{}
	for i, f := range Families {
		idx[f.Key] = i
	}
	sort.Slice(keys, func(i, j int) bool {
		a, aok := idx[keys[i]]
		b, bok := idx[keys[j]]
		if aok != bok {
			return aok
		}
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	return keys
}

// Thresholds are the utilisation percentages the page colours at:
// warn at 70 %, critical at 85 % (founder requirement 2026-09-11).
const (
	ThresholdWarnPct     = 70
	ThresholdCriticalPct = 85
)

// Pool statuses.
const (
	StatusUnset    = "unset" // total is 0: nothing to measure against
	StatusOK       = "ok"
	StatusWarn     = "warn"
	StatusCritical = "critical"
)

// Status classifies a utilisation percentage; ok=false (StatusUnset) when
// the pool has no total.
func Status(utilisationPct float64, hasTotal bool) string {
	switch {
	case !hasTotal:
		return StatusUnset
	case utilisationPct >= ThresholdCriticalPct:
		return StatusCritical
	case utilisationPct >= ThresholdWarnPct:
		return StatusWarn
	}
	return StatusOK
}
