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
	// SQLUsagePredicate selects showcase usage records and inventory rows
	// (usage_records.labels / resource_inventory.attrs).
	SQLUsagePredicate = "labels->>'synthetic' = 'true'"
	// SQLInventoryPredicate is SQLUsagePredicate over resource_inventory.
	SQLInventoryPredicate = "attrs->>'synthetic' = 'true'"
)
