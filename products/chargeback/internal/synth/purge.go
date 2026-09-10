package synth

import "strings"

// Purge selectors. The seeding command deletes exactly the rows these match
// and nothing else; TestPurgeSelectorsMatchOnlySynthetic pins the SQL
// predicates to the Go predicates so they cannot drift apart.

// IsSyntheticSlug reports whether a customer or org slug is a showcase slug:
// the demo- prefix followed by at least one character.
func IsSyntheticSlug(slug string) bool {
	return strings.HasPrefix(slug, SlugPrefix) && len(slug) > len(SlugPrefix)
}

// IsSyntheticName reports whether a discount, campaign or budget name is a
// showcase name.
func IsSyntheticName(name string) bool {
	return strings.HasPrefix(name, NamePrefix) && len(name) > len(NamePrefix)
}

// IsSyntheticSourceName reports whether a source's project_id/name is one
// this tool made. It is the selector the LANDLORD backfill needs: that
// source hangs off a REAL customer whose slug carries no mark, so the
// customer-slug selector above would never reach it and a purge would strand
// three months of synthetic rows on a live ledger.
//
// A real source's project_id is a Huawei project id, an Organization slug or
// a file-import name — never demo-prefixed.
func IsSyntheticSourceName(name string) bool {
	return strings.HasPrefix(name, SlugPrefix) && len(name) > len(SlugPrefix)
}

// IsSyntheticLabels reports whether a usage record's labels carry the mark.
func IsSyntheticLabels(labels map[string]string) bool {
	return labels[LabelKey] == LabelValue
}

// SQL predicates over the product's tables, one per selector above. They
// take no parameters so a reader of the command sees the exact rows it can
// touch. LIKE treats '-' and ' ' literally; neither prefix contains '_' or
// '%'.
const (
	// SQLCustomerPredicate selects showcase customers (customers.slug).
	SQLCustomerPredicate = "slug LIKE 'demo-_%'"
	// SQLNamePredicate selects showcase discounts and budgets (name).
	SQLNamePredicate = "name LIKE 'demo: _%'"
	// SQLSourcePredicate selects sources this tool made (cost_sources.
	// project_id), including the landlord backfill's source on a real
	// customer. It is paired with `NOT internal` at the call site so the
	// Sovereign's own platform source can never be reached.
	SQLSourcePredicate = "project_id LIKE 'demo-_%'"
	// SQLUsagePredicate selects showcase usage records and inventory rows
	// (usage_records.labels / resource_inventory.attrs).
	SQLUsagePredicate = "labels->>'synthetic' = 'true'"
	// SQLInventoryPredicate is SQLUsagePredicate over resource_inventory.
	SQLInventoryPredicate = "attrs->>'synthetic' = 'true'"
)
