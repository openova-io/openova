package synth

// The showcase PACKAGE MATRIX (DESIGN.md §7, §22; founder direction
// 2026-10-10). The baseline matrix is the one in the pricing workbook sent to
// the National Cloud team: fifteen features across S / M / L / XL, each
// included, optional or not offered, and the five add-ons priced per month.
// The seeding command writes it onto the "OpenOva plans" book through the
// product's own API, idempotently, and --purge removes it again.

// Feature kinds and states, restated here so this package imports nothing
// from the store (the seeder speaks to the product over HTTP).
const (
	FeatureBoolean  = "boolean"
	FeatureQuantity = "quantity"

	Included   = "included"
	Optional   = "optional"
	NotOffered = "not_offered"
)

// Feature is one row of the showcase matrix.
type Feature struct {
	Key      string
	Name     string
	Blurb    string
	Kind     string
	Unit     string
	AddonSKU string
	// Cells is the state per plan slug (s, m, l, xl); a quantity feature's
	// included cells carry the quantity in Quantities.
	Cells      map[string]string
	Quantities map[string]float64
}

// AddonRate is one add-on SKU and its price per month in OMR; the seeder
// prices it in the plans book per plan-hour exactly as the plans are priced
// (annual = monthly × 12, unit = annual ÷ 8760 = monthly ÷ 730).
type AddonRate struct {
	SKU     string
	Name    string
	Monthly float64
}

// AddonRates are the five add-ons of the baseline matrix.
var AddonRates = []AddonRate{
	{SKU: "addon.backup", Name: "Backup", Monthly: 1.5},
	{SKU: "addon.dedicated_ip", Name: "Dedicated IP address", Monthly: 2},
	{SKU: "addon.ai_seo", Name: "AI SEO ready", Monthly: 3},
	{SKU: "addon.ai_builder", Name: "AI website builder", Monthly: 4},
	{SKU: "addon.domain", Name: "Domain", Monthly: 1},
}

// BandwidthSKU is the metered SKU the bandwidth quantity feature is an
// allowance on; its excess is billed at the plans book's rate for it.
const BandwidthSKU = "eip.bandwidth_mbps"

// BandwidthRate prices the excess bandwidth in the plans book: the National
// Cloud list rate for a Mbps-hour, borrowed rather than minted (§7.2).
var BandwidthRate = Rate{SKU: BandwidthSKU, Unit: "mbps-hour", Annual: 150.38, Description: "EIP bandwidth per Mbps above what the package includes (National Cloud list rate)"}

func all(state string) map[string]string {
	return map[string]string{"s": state, "m": state, "l": state, "xl": state}
}

func optionalUntilXL() map[string]string {
	return map[string]string{"s": Optional, "m": Optional, "l": Optional, "xl": Included}
}

// Features is the baseline matrix, in the workbook's order.
var Features = []Feature{
	{Key: "applications", Name: "Applications", Blurb: "Install any application from the catalog", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "databases", Name: "Databases", Blurb: "Managed databases for your applications", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "mail", Name: "Mail server (unlimited accounts)", Blurb: "Your own mail server, as many mailboxes as you need", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "backup", Name: "Backup", Blurb: "Daily backups of your sites and databases, kept 30 days", Kind: FeatureBoolean, AddonSKU: "addon.backup", Cells: optionalUntilXL()},
	{Key: "dedicated_ip", Name: "Dedicated IP address", Blurb: "A public address of your own", Kind: FeatureBoolean, AddonSKU: "addon.dedicated_ip", Cells: all(Optional)},
	{Key: "ssl", Name: "Unlimited free SSL", Blurb: "Certificates for every site, renewed for you", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "ai_seo", Name: "AI SEO ready", Blurb: "Search-engine readiness checked and tuned by AI", Kind: FeatureBoolean, AddonSKU: "addon.ai_seo", Cells: optionalUntilXL()},
	{Key: "ai_builder", Name: "AI website builder", Blurb: "Build and edit your site with an AI assistant", Kind: FeatureBoolean, AddonSKU: "addon.ai_builder", Cells: optionalUntilXL()},
	{Key: "domain", Name: "Domain", Blurb: "A domain name registered for you", Kind: FeatureBoolean, AddonSKU: "addon.domain", Cells: optionalUntilXL()},
	{Key: "sso", Name: "SSO", Blurb: "One sign-in for every application", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "ddos", Name: "Standard DDoS protection", Blurb: "Volumetric attacks absorbed at the edge", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "malware_scanner", Name: "Malware scanner", Blurb: "Your sites scanned for malware", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "waf", Name: "Web application firewall", Blurb: "Common web attacks blocked before they reach you", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "support", Name: "24/7 customer support", Blurb: "Someone to talk to, any hour", Kind: FeatureBoolean, Cells: all(Included)},
	{Key: "bandwidth", Name: "Bandwidth", Blurb: "Included bandwidth; more is billed per Mbps", Kind: FeatureQuantity, Unit: "Mbps", AddonSKU: BandwidthSKU,
		Cells: all(Included), Quantities: map[string]float64{"s": 50, "m": 100, "l": 250, "xl": 1000}},
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
