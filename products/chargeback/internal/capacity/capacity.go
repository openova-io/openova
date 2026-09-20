// Package capacity is the pure part of capacity management (DESIGN.md §11,
// EPIC #6867): what a POOL is, what a SKU's SHAPE is, the three CLASSES a
// shape can be sold at, and the arithmetic that turns those into what can
// still be sold and when it runs out (arithmetic.go).
//
// It has no database and no HTTP. The store keeps the pools, their resource
// vectors, the shapes and the placements and derives consumption; the API
// turns a request into store calls. Everything here is deterministic on its
// input, so it is pinned by plain unit tests.
//
// WHAT CHANGED AND WHY (founder direction 2026-09-13). The first cut of this
// module modelled capacity as ONE POOL PER (zone, family) over a fixed
// seven-family enum, with a per-SKU headroom column and a sku_caps table.
// Three things were wrong with that, and all three are why the model below
// looks nothing like it:
//
//  1. A POOL IS A SET OF IDENTICAL MACHINES AND ITS CAPACITY IS A VECTOR,
//     NOT A NUMBER. vCPU and RAM in the same server are not independently
//     sellable; separate vCPU and RAM pools let you "sell" vCPU with no RAM
//     behind it. A pool therefore holds a per-machine vector and a machine
//     count.
//  2. SEVERAL POOLS OF THE SAME RESOURCE KIND MUST COEXIST IN ONE ZONE
//     (m7n-a and m7n-b, different batches or server types). UNIQUE (zone,
//     family) forbade exactly that, which is why the resource kinds here are
//     DATA — a row a pool declares — and never a CHECK constraint.
//  3. PER-SKU HEADROOM IS A WRONG ANSWER, NOT A MISSING FEATURE. "50 large
//     fit" and "200 small fit" side by side are mutually exclusive: each
//     silently assumes the others sell zero. A pool's free room is reported
//     as ONE basket headroom over a named mix, with the binding resource.
package capacity

import (
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// resource kinds — DATA, never an enum
// ---------------------------------------------------------------------------

// ResourceKind is one kind of resource a machine holds, with the label and
// unit the console renders it in. The set is a TABLE the operator can add to
// (capacity_resource_kinds), not a constraint: a CHECK over a fixed list is
// what stopped two vCPU pools coexisting, and nothing here reintroduces one.
// The seeds below are the kinds this product already meters; a pool that
// declares a kind nobody listed gets a row with its key as the label.
type ResourceKind struct {
	Key      string `json:"resource"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	Position int    `json:"position"`
}

// The resource keys the shapes derived from SKU names use. They are ordinary
// strings: a pool may hold any key at all, and these are simply the ones
// Derive and the seed write.
const (
	ResourceVCPU      = "vcpu"
	ResourceMemoryGiB = "memory_gib"
	ResourceBlockSSD  = "block_ssd_gib"
	ResourceBlockHDD  = "block_hdd_gib"
	ResourceObject    = "object_gib"
	ResourceEIP       = "eip_addresses"
	ResourceBandwidth = "bandwidth_mbps"
)

// SeedResourceKinds are the kinds the migration writes so a fresh console
// has labels and units for what this product meters. Position is the display
// order; a kind an operator adds later sorts after them by key.
var SeedResourceKinds = []ResourceKind{
	{Key: ResourceVCPU, Label: "vCPU", Unit: "vCPU", Position: 10},
	{Key: ResourceMemoryGiB, Label: "Memory", Unit: "GiB", Position: 20},
	{Key: ResourceBlockSSD, Label: "Block SSD", Unit: "GiB", Position: 30},
	{Key: ResourceBlockHDD, Label: "Block HDD", Unit: "GiB", Position: 40},
	{Key: ResourceObject, Label: "Object storage", Unit: "GiB", Position: 50},
	{Key: ResourceEIP, Label: "Elastic IPs", Unit: "addresses", Position: 60},
	{Key: ResourceBandwidth, Label: "Bandwidth", Unit: "Mbps", Position: 70},
}

// NormResource lower-cases and trims a resource key. Keys are compared
// case-insensitively so "vCPU" typed into the pool editor is the same
// resource as the one a shape derives.
func NormResource(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// KindOf returns the seeded kind for a key, or a kind carrying the key as
// its label and no unit — what an operator's own resource reads as until
// they name it.
func KindOf(key string) ResourceKind {
	for _, k := range SeedResourceKinds {
		if k.Key == key {
			return k
		}
	}
	return ResourceKind{Key: key, Label: key, Position: 1000}
}

// ---------------------------------------------------------------------------
// classes
// ---------------------------------------------------------------------------

// A PLACEMENT's class. It lives on the placement, NOT on the SKU's shape, and
// ONE SKU MAY BE PLACED ON A POOL AT SEVERAL CLASSES: the same flavour sold
// guaranteed, burstable and spot is three placements of one SKU, three
// prices, and which of them a RUNNING resource counts at is said by the
// resource itself (ResolveClass, classes.go). A pool lists the classes it can
// actually enforce, and a placement may only use one of those.
const (
	// ClassGuaranteed is backed by physical capacity at 1:1. It is admitted
	// only if it fits `usable`, and every unit sold removes ratio × worth of
	// oversubscribed room — that is what makes the guarantee real.
	ClassGuaranteed = "guaranteed"
	// ClassBurstable is sold against the oversubscribed envelope and is
	// throttled when the pool reaches it.
	ClassBurstable = "burstable"
	// ClassSpot holds no reservation. It is EXCLUDED from admission
	// accounting on both sides: a guaranteed or burstable order is never
	// refused on account of spot, and spot is reclaimed when the room it is
	// running in shrinks.
	ClassSpot = "spot"
)

// ClassDef is a class with the words the console explains it in.
type ClassDef struct {
	Key   string `json:"class"`
	Label string `json:"label"`
	Note  string `json:"note"`
	// Requires is what the substrate under a pool must be able to do for
	// this class to be more than a label — the sentence the pool editor
	// shows beside the checkbox.
	Requires string `json:"requires"`
}

// Classes lists the three classes in the order the console shows them.
var Classes = []ClassDef{
	{Key: ClassGuaranteed, Label: "Guaranteed", Note: "physically backed at 1:1; admitted only if it fits usable capacity", Requires: "a fixed allocation — every substrate has it"},
	{Key: ClassBurstable, Label: "Burstable", Note: "sold against the oversubscribed envelope, never the guaranteed floor; throttled at the soft wall", Requires: "the host throttling at runtime — Kubernetes the platform operates; a resold fixed-vCPU cloud flavour cannot"},
	{Key: ClassSpot, Label: "Spot", Note: "no reservation; reclaimed when the room it runs in shrinks, and never refuses another class", Requires: "the right to delete the resource, with notice — the platform creates and deletes what it sells, so every substrate has it"},
}

// ValidClass reports whether s is one of the three classes.
func ValidClass(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ClassGuaranteed, ClassBurstable, ClassSpot:
		return true
	}
	return false
}

// ClassKeys is Classes as keys, in the same order.
func ClassKeys() []string {
	out := make([]string, len(Classes))
	for i, c := range Classes {
		out[i] = c.Key
	}
	return out
}

// ---------------------------------------------------------------------------
// shapes
// ---------------------------------------------------------------------------

// Shape is how much of each resource ONE unit of a SKU consumes, as exact
// decimal text keyed by resource (store.Decimal is a string too, so the
// store converts without arithmetic). An instance-hour of m7n.2xlarge.8 is
// {vcpu: 8, memory_gib: 64}; a GB-hour of evs.ssd.gb is {block_ssd_gib: 1}.
//
// A shape says WHAT a unit costs the hardware. It never says where it runs
// or at which class — that is the placement.
type Shape map[string]string

// Sources a shape can come from, reported on the wire so the page can say
// which rows an operator typed and which the name implied.
const (
	// SourceManual is a row the operator wrote or edited.
	SourceManual = "manual"
	// SourceSeed is a row the migration seeded from the National Cloud list.
	SourceSeed = "seed"
	// SourceDerived is not a row at all: the shape the SKU name implies,
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
// reports the SKU as "no shape" rather than guessing.
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

// Derive is the shape a SKU name implies, or nil when the name does not say.
//
// IT STILL EARNS ITS PLACE under the pool model, for the reason it did under
// the family model and one more. A Sovereign meters whatever flavours its
// customers run, and the operator cannot type a row for every ECS flavour
// before the first of them is billed; without derivation that usage would
// silently count against nothing. Under the pool model the stake is higher,
// not lower: an unshaped SKU cannot be PLACED either, so it would vanish
// from the pool it is genuinely running on. Derivation keeps it visible — as
// an unplaced SKU that HAS a shape, which the console asks the operator to
// place, rather than as nothing at all.
//
// Only unambiguous shapes derive:
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
func Derive(sku string) Shape {
	s := strings.ToLower(strings.TrimSpace(sku))
	switch {
	case s == "eip":
		return Shape{ResourceEIP: "1"}
	case s == "eip.bandwidth_mbps":
		return Shape{ResourceBandwidth: "1"}
	case s == "evs.ssd.gb":
		return Shape{ResourceBlockSSD: "1"}
	case s == "evs.hdd.gb":
		return Shape{ResourceBlockHDD: "1"}
	case strings.HasPrefix(s, "ecs."):
		v, m, ok := ParseECSFlavor(strings.TrimPrefix(s, "ecs."))
		if !ok {
			return nil
		}
		return Shape{ResourceVCPU: strconv.Itoa(v), ResourceMemoryGiB: strconv.Itoa(m)}
	}
	return nil
}

// SeedSKUs are the SKUs of the National Cloud list price book
// (internal/synth NationalCloudRates — TestSeedCoversNationalCloudList pins
// the two equal). The migration seeds a shape row for each one Derive
// covers; the rest (elb, nat.<spec>, vpc) have no per-unit shape in any
// resource and are reported as such.
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

// SeedRow is one (sku, resource, amount) the migration writes.
type SeedRow struct {
	SKU      string
	Resource string
	Amount   string
}

// Seed is every shape row the migration writes, in a stable order: the
// SeedSKUs Derive covers, one row per resource.
func Seed() []SeedRow {
	var out []SeedRow
	for _, sku := range SeedSKUs {
		sh := Derive(sku)
		if sh == nil {
			continue
		}
		for _, res := range SortedResources(sh) {
			out = append(out, SeedRow{SKU: sku, Resource: res, Amount: sh[res]})
		}
	}
	return out
}

// Unseeded lists the SeedSKUs that derive nothing — what the page shows as
// "no shape" until the operator writes one.
func Unseeded() []string {
	var out []string
	for _, sku := range SeedSKUs {
		if Derive(sku) == nil {
			out = append(out, sku)
		}
	}
	return out
}

// SortedResources orders a shape's resources by the seeded display order,
// then by key — the order the console lists a vector in.
func SortedResources(sh Shape) []string {
	keys := make([]string, 0, len(sh))
	for k := range sh {
		keys = append(keys, k)
	}
	SortResources(keys)
	return keys
}

// SortResources sorts resource keys in place by seeded position, then key.
func SortResources(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		a, b := KindOf(keys[i]), KindOf(keys[j])
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return keys[i] < keys[j]
	})
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// Thresholds are the utilisation percentages the console colours at: warn at
// 70 %, critical at 85 % of what is SELLABLE.
const (
	ThresholdWarnPct     = 70
	ThresholdCriticalPct = 85
)

// Pool and resource statuses.
const (
	// StatusUnset is a resource nobody has sized (no machines, or no amount
	// per machine). It NEVER reads ok: a figure read from a field that is
	// structurally empty would render as good news.
	StatusUnset    = "unset"
	StatusOK       = "ok"
	StatusWarn     = "warn"
	StatusCritical = "critical"
)

// Status classifies a utilisation percentage; StatusUnset when the resource
// carries no capacity to measure against.
func Status(utilisationPct float64, sized bool) string {
	switch {
	case !sized:
		return StatusUnset
	case utilisationPct >= ThresholdCriticalPct:
		return StatusCritical
	case utilisationPct >= ThresholdWarnPct:
		return StatusWarn
	}
	return StatusOK
}

// WorstStatus is the status a pool takes from its resources: the most severe
// of them, and unset when none is sized.
func WorstStatus(all []string) string {
	rank := map[string]int{StatusUnset: 0, StatusOK: 1, StatusWarn: 2, StatusCritical: 3}
	best, out := -1, StatusUnset
	for _, s := range all {
		if r, ok := rank[s]; ok && r > best {
			best, out = r, s
		}
	}
	return out
}
