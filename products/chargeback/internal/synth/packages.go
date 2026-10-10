package synth

// The showcase PACKAGE LADDER (DESIGN.md §7, §22; founder direction
// 2026-10-10, numbers from the pricing workbook sent to the National Cloud
// team — NC-OO-Pricing.xlsx, sheets Pricing + Packages + Inputs — every one
// of them real, none invented here). The seeding command writes it onto the
// "OpenOva plans" book through the product's own API, idempotently, and
// --purge removes it again.
//
// What the sheet says, and where each number below comes from:
//
//   - the four PACKAGES at the sheet's Target column — S 2.490 · M 4.490 ·
//     L 7.990 · XL 13.990 OMR a month — with the Tier's vCPU / RAM / Storage
//     as the shape and the guaranteed floors as headline ÷ the sheet's
//     overcommit (CPU 6×, RAM 3×). The sheet has no taglines and no annual
//     term rule, so those stay empty;
//   - the nine features the sheet marks Included on every package are the
//     FLOOR: listed once under the packages, never priced, no cell;
//   - two QUANTITY rows: bandwidth and disk, hard-capped on S and M and
//     metered on L and XL at the National Cloud list rate for the meter;
//   - the sheet's five Optional rows as ADD-ONS. Four of them are priced so
//     that the STEP-UP RULE holds on L → XL (gap 6.000): backup 1.500 + AI
//     SEO 2.000 + AI builder 2.000 + domain 0.500 = 6.000. Those four prices
//     are DERIVED, not taken from a cell of the sheet, and every such cell
//     carries the note that says so. The dedicated IP is the sheet's own rate
//     card: an Elastic IP at 250 OMR a year, discount 0 — cost-based,
//     flagged on the cell until the discount is decided;
//   - the non-numeric tiers (access doors, managed operations, the DR
//     topology level) carry no price and come from the approved ladder.

// Feature kinds, groups, states and overage policies, restated here so this
// package imports nothing from the store (the seeder speaks to the product
// over HTTP).
const (
	FeatureBoolean  = "boolean"
	FeatureQuantity = "quantity"
	FeatureLevel    = "level"
	FeatureAccess   = "access"

	Included   = "included"
	Optional   = "optional"
	NotOffered = "not_offered"

	OverageMetered   = "metered"
	OverageHardCap   = "hard_cap"
	OverageUnlimited = "unlimited"

	GroupFloor      = "floor"
	GroupCapacity   = "capacity"
	GroupFeatures   = "features"
	GroupAccess     = "access"
	GroupOps        = "ops"
	GroupScope      = "scope"
	GroupResilience = "resilience"
	GroupService    = "service"
)

// Cell is one feature on one package as the ladder states it.
type Cell struct {
	State string
	// Quantity and Overage on a quantity feature.
	Quantity    float64
	HasQuantity bool
	Overage     string
	// Level on a level feature.
	Level int
	// Note is published under the cell.
	Note string
}

// Feature is one row of the ladder.
type Feature struct {
	Key      string
	Name     string
	Blurb    string
	Kind     string
	Group    string
	Unit     string
	AddonSKU string
	Levels   []string
	Teaser   bool
	// Cells is the cell per plan slug (s, m, l, xl); a floor item has none.
	Cells map[string]Cell
}

// Package is one of the four packages: its price per month (the sheet's
// Target), its shape (the Tier's vCPU / RAM / Storage) and the guaranteed
// floors (headline ÷ overcommit).
type Package struct {
	Slug, Name, Tagline string
	Monthly             float64
	Recommended         bool
	AnnualMonthsFree    int
	VCPU, MemoryGB      float64
	VCPUGuaranteed      float64
	MemoryGBGuaranteed  float64
	DiskGB              float64
}

// Packages are the four packages at the sheet's prices and shapes.
var Packages = []Package{
	{Slug: "s", Name: "S", Monthly: 2.49, VCPU: 1, MemoryGB: 2, VCPUGuaranteed: 0.17, MemoryGBGuaranteed: 0.67, DiskGB: 25},
	{Slug: "m", Name: "M", Monthly: 4.49, Recommended: true, VCPU: 2, MemoryGB: 4, VCPUGuaranteed: 0.33, MemoryGBGuaranteed: 1.33, DiskGB: 50},
	{Slug: "l", Name: "L", Monthly: 7.99, VCPU: 4, MemoryGB: 8, VCPUGuaranteed: 0.67, MemoryGBGuaranteed: 2.67, DiskGB: 100},
	{Slug: "xl", Name: "XL", Monthly: 13.99, VCPU: 8, MemoryGB: 16, VCPUGuaranteed: 1.33, MemoryGBGuaranteed: 5.33, DiskGB: 250},
}

// AddonRate is one add-on SKU and its price per month in OMR; the seeder
// prices it in the plans book per plan-hour exactly as the plans are priced
// (annual = monthly × 12, unit = annual ÷ 8760 = monthly ÷ 730). An Annual
// set directly wins over Monthly × 12 — the dedicated IP is the sheet's rate
// card figure per year, 250, which is 20.833 a month and not a round number.
type AddonRate struct {
	SKU     string
	Name    string
	Monthly float64
	Annual  float64
}

// AnnualPrice is what the seeder prices the add-on at per year.
func (a AddonRate) AnnualPrice() float64 {
	if a.Annual > 0 {
		return a.Annual
	}
	return a.Monthly * 12
}

// The note on every cell whose price is derived from the step-up rule rather
// than read from the sheet, and on the cost-based dedicated IP.
const (
	DerivedNote     = "derived from the step-up rule; confirm"
	DedicatedIPNote = "EIP list price; discount to be decided"
)

// AddonRates are the five add-ons of the sheet's Optional rows.
var AddonRates = []AddonRate{
	{SKU: "addon.backup", Name: "Backup", Monthly: 1.5},
	{SKU: "addon.ai_seo", Name: "AI SEO ready", Monthly: 2},
	{SKU: "addon.ai_builder", Name: "AI website builder", Monthly: 2},
	{SKU: "addon.domain", Name: "Domain", Monthly: 0.5},
	// The sheet's rate card: Elastic IP list 250 OMR/yr, discount 0.
	{SKU: "addon.dedicated_ip", Name: "Dedicated IP address", Annual: 250},
}

// BandwidthSKU is the metered SKU the bandwidth quantity feature is an
// allowance on; its excess is billed at the plans book's rate for it where
// the package meters it.
const BandwidthSKU = "eip.bandwidth_mbps"

// DiskSKU is the platform meter the disk quantity feature is an allowance
// on: the PVC capacity the platform collector reports per GB-hour.
const DiskSKU = "k8s.pvc_gb"

// BandwidthRate prices the excess bandwidth in the plans book: the National
// Cloud list rate for a Mbps-hour, borrowed rather than minted (§7.2).
var BandwidthRate = Rate{SKU: BandwidthSKU, Unit: "mbps-hour", Annual: 150.38, Description: "EIP bandwidth per Mbps above what the package includes (National Cloud list rate)"}

// DiskRate prices the excess disk in the plans book: the National Cloud list
// rate for EVS SSD block storage per GB, borrowed like the bandwidth. The
// plans book otherwise prices no k8s.* meter — under a plan they are the
// allocation basis — and pricing this one is what lets the L and XL
// packages meter disk above what they include (DESIGN.md §22.2).
var DiskRate = Rate{SKU: DiskSKU, Unit: "gb-hour", Annual: 2.00, Description: "Disk per GB above what the package includes (National Cloud list rate for EVS SSD)"}

// MeterRates are the two meters the seeder prices in the plans book.
var MeterRates = []Rate{BandwidthRate, DiskRate}

func inc() Cell                { return Cell{State: Included} }
func incNote(note string) Cell { return Cell{State: Included, Note: note} }
func no() Cell                 { return Cell{State: NotOffered} }
func opt(note string) Cell     { return Cell{State: Optional, Note: note} }
func qty(q float64, overage string) Cell {
	return Cell{State: Included, Quantity: q, HasQuantity: true, Overage: overage}
}
func lvl(n int) Cell { return Cell{State: Included, Level: n} }

func cells(s, m, l, xl Cell) map[string]Cell {
	return map[string]Cell{"s": s, "m": m, "l": l, "xl": xl}
}

// Features is the ladder, in the order the pages show it: the floor first,
// then one group after another.
var Features = []Feature{
	// The FLOOR — the sheet marks these Included on every package.
	{Key: "applications", Name: "Applications", Blurb: "Install any application from the catalog", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "databases", Name: "Databases", Blurb: "Managed databases for your applications", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "mail", Name: "Mail server (unlimited accounts)", Blurb: "Your own mail server, as many mailboxes as you need", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "ssl", Name: "Unlimited free SSL", Blurb: "Certificates for every site, renewed for you", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "sso", Name: "SSO", Blurb: "One sign-in for every application", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "ddos", Name: "Standard DDoS protection", Blurb: "Volumetric attacks absorbed at the edge", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "malware_scanner", Name: "Malware scanner", Blurb: "Your sites scanned for malware", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "waf", Name: "Web application firewall", Blurb: "Common web attacks blocked before they reach you", Kind: FeatureBoolean, Group: GroupFloor},
	{Key: "support", Name: "24/7 customer support", Blurb: "Someone to talk to, any hour", Kind: FeatureBoolean, Group: GroupFloor},

	// CAPACITY — the two quantity rows, hard-capped on S and M, metered above.
	{Key: "bandwidth", Name: "Bandwidth", Blurb: "Included bandwidth; above it the package's overage rule applies", Kind: FeatureQuantity, Group: GroupCapacity, Unit: "Mbps", AddonSKU: BandwidthSKU,
		Cells: cells(qty(50, OverageHardCap), qty(100, OverageHardCap), qty(250, OverageMetered), qty(1000, OverageMetered))},
	{Key: "disk", Name: "Disk", Blurb: "Persistent storage for apps and databases", Kind: FeatureQuantity, Group: GroupCapacity, Unit: "GB", AddonSKU: DiskSKU,
		Cells: cells(qty(25, OverageHardCap), qty(50, OverageHardCap), qty(100, OverageMetered), qty(250, OverageMetered))},

	// FEATURES — the sheet's Optional rows, priced by the step-up rule.
	{Key: "ai_seo", Name: "AI SEO ready", Blurb: "Search-engine readiness checked and tuned by AI", Kind: FeatureBoolean, Group: GroupFeatures, AddonSKU: "addon.ai_seo",
		Cells: cells(opt(DerivedNote), opt(DerivedNote), opt(DerivedNote), inc())},
	{Key: "ai_builder", Name: "AI website builder", Blurb: "Build and edit your site with an AI assistant", Kind: FeatureBoolean, Group: GroupFeatures, AddonSKU: "addon.ai_builder",
		Cells: cells(opt(DerivedNote), opt(DerivedNote), opt(DerivedNote), inc())},

	// ACCESS — the platform doors.
	{Key: "console", Name: "Console click-install", Blurb: "The Catalyst console: install and run applications by clicking", Kind: FeatureAccess, Group: GroupAccess,
		Cells: cells(inc(), inc(), inc(), inc())},
	{Key: "gitea_iac", Name: "Gitea + IaC", Blurb: "Your Organization's Git and the infrastructure-as-code behind every application", Kind: FeatureAccess, Group: GroupAccess,
		Cells: cells(no(), incNote("read"), inc(), inc())},
	{Key: "k8s_ui", Name: "Advanced Kubernetes UI", Blurb: "The cluster behind your applications, in a browser", Kind: FeatureAccess, Group: GroupAccess,
		Cells: cells(no(), no(), inc(), inc())},
	{Key: "kube_api", Name: "Kube API / shell (Guacamole) + PAM", Blurb: "The Kubernetes API and a shell, through the privileged-access gateway", Kind: FeatureAccess, Group: GroupAccess,
		Cells: cells(no(), no(), no(), inc())},

	// OPS — managed operations: console toggles and the actions we take.
	{Key: "vuln_dashboard", Name: "Vulnerability dashboard", Blurb: "Every image and dependency scanned, the findings in one place", Kind: FeatureBoolean, Group: GroupOps, Teaser: true,
		Cells: cells(no(), inc(), inc(), inc())},
	{Key: "compliance", Name: "Compliance / SRE checks", Blurb: "Configuration and reliability checks run for you", Kind: FeatureBoolean, Group: GroupOps, Teaser: true,
		Cells: cells(no(), no(), inc(), inc())},
	{Key: "audit_log", Name: "Audit log · uptime reports", Blurb: "Who did what, and how available you were", Kind: FeatureBoolean, Group: GroupOps,
		Cells: cells(no(), inc(), inc(), inc())},
	{Key: "cost_explorer", Name: "Cost explorer · anomaly alerts", Blurb: "Where the money goes, and a warning when it jumps", Kind: FeatureBoolean, Group: GroupOps,
		Cells: cells(no(), inc(), inc(), inc())},
	{Key: "patching", Name: "Automated patching", Blurb: "Security updates applied for you", Kind: FeatureBoolean, Group: GroupOps,
		Cells: cells(no(), no(), inc(), inc())},
	{Key: "maintenance", Name: "Proactive maintenance · auto-remediation", Blurb: "Problems found and fixed before you notice", Kind: FeatureBoolean, Group: GroupOps,
		Cells: cells(no(), no(), no(), inc())},

	// SCOPE — the domain and the address.
	{Key: "domain", Name: "Domain", Blurb: "A domain name registered for you", Kind: FeatureBoolean, Group: GroupScope, AddonSKU: "addon.domain",
		Cells: cells(opt(DerivedNote), opt(DerivedNote), opt(DerivedNote), inc())},
	{Key: "dedicated_ip", Name: "Dedicated IP address", Blurb: "A public IPv4 reserved for your Organization", Kind: FeatureBoolean, Group: GroupScope, AddonSKU: "addon.dedicated_ip",
		Cells: cells(opt(DedicatedIPNote), opt(DedicatedIPNote), opt(DedicatedIPNote), opt(DedicatedIPNote))},

	// RESILIENCE — backups and the DR topology.
	{Key: "backup", Name: "Backup", Blurb: "Scheduled backups of your sites and databases", Kind: FeatureBoolean, Group: GroupResilience, AddonSKU: "addon.backup",
		Cells: cells(opt(DerivedNote), opt(DerivedNote), opt(DerivedNote), inc())},
	{Key: "dr_topology", Name: "DR topology", Blurb: "Where your applications run, and where they fail over to", Kind: FeatureLevel, Group: GroupResilience, Levels: []string{"single region", "active-passive"},
		Cells: cells(lvl(0), lvl(0), lvl(0), lvl(1))},
}

// FeatureKeys lists the keys of the showcase features, in order — the purge
// selector for the features this tool made.
func FeatureKeys() []string {
	out := make([]string, 0, len(Features))
	for _, f := range Features {
		out = append(out, f.Key)
	}
	return out
}

// AddonSKUs lists the add-on SKUs the seeder prices.
func AddonSKUs() []string {
	out := make([]string, 0, len(AddonRates))
	for _, a := range AddonRates {
		out = append(out, a.SKU)
	}
	return out
}

// MeterSKUs lists the meters the seeder prices in the plans book.
func MeterSKUs() []string { return []string{BandwidthSKU, DiskSKU} }

// PlanSlugs are the four packages, cheapest first.
var PlanSlugs = []string{"s", "m", "l", "xl"}
