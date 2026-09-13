package store

import (
	"fmt"
	"log/slog"

	"github.com/lib/pq"
)

// Nothing the Postgres driver says reaches a caller.
//
// A constraint violation arrives from lib/pq carrying Detail, Constraint and
// Table. Detail is driver text — for a duplicate cost-centre code it reads
//
//	Key (customer_id, code)=(9692021e-ce13-4bbd-9429-292e5f9218cd, ENG) already exists.
//
// and handing that to the API put it on the screen verbatim, because a 409
// answers with the error's own text (api/respond.go). Two things were wrong
// with that at once. It names columns, a table's key and an internal UUID, so
// the schema and another row's identifier reached whoever tripped it —
// including a customer-scoped principal with no business seeing either. And
// it is unreadable to the person who did nothing worse than type a code that
// was already taken.
//
// So mapErr still CLASSIFIES — the sentinels are load-bearing, and callers all
// the way up to the HTTP status switch on them — and only the MESSAGE changes:
// a sentence per constraint the console can actually trip, a deliberately
// unspecific one for the rest, and the driver's own text logged server-side so
// an engineer still has the diagnostic.
//
// The map is keyed by the name Postgres reports in pq.Error.Constraint, which
// is the constraint name for a table constraint and the index name for a
// partial unique index. Both live in the same namespace, so one map is enough.
// TestConstraintMessagesNameLiveConstraints holds every key to a name the
// migrated schema really has, so a rename cannot leave a dead entry behind
// that silently falls back.
var constraintMessages = map[string]string{
	// cost centres (DESIGN.md §19)
	"cost_centres_customer_id_code_key":                   "that cost-centre code is already used by this customer",
	"cost_centre_rules_customer_id_tag_key_tag_value_key": "a rule for that tag key and value already names a cost centre for this customer",
	"cost_centre_resources_pkey":                          "that resource already has a cost-centre override",
	"cost_centres_code_check":                             "a cost-centre code must match " + CostCentreCodeRule,
	"cost_centre_rules_tag_key_check":                     "a tag key must match " + TagKeyRule,
	"cost_centre_rules_priority_check":                    "a rule priority must be between 0 and 9999",

	// capacity
	"capacity_pools_zone_name_uniq":                  "a capacity pool with that name already exists in this zone",
	"capacity_regions_code_key":                      "a region with that code already exists",
	"capacity_zones_region_id_code_key":              "a zone with that code already exists in this region",
	"capacity_zones_default_uniq":                    "this region already has a default zone",
	"capacity_resource_kinds_pkey":                   "that resource kind is already defined",
	"capacity_pool_resources_pkey":                   "that resource is already sized on this pool",
	"capacity_placements_pkey":                       "that SKU is already placed on this pool",
	"sku_footprints_pkey":                            "that SKU already has a shape for this resource",
	"capacity_pools_machines_check":                  "the machine count cannot be negative",
	"capacity_pools_lead_time_days_check":            "the lead time cannot be negative",
	"capacity_pools_name_check":                      "a capacity pool needs a name",
	"capacity_pool_resources_overcommit_ratio_check": "an overcommit ratio must be greater than zero",
	"capacity_pool_resources_per_machine_check":      "a per-machine figure cannot be negative",
	"capacity_pool_resources_reserve_check":          "a reserve cannot be negative",
	"capacity_regions_code_check":                    "a region code must be lower-case and cannot be empty",
	"capacity_zones_code_check":                      "a zone code must be lower-case and cannot be empty",
	"sku_footprints_amount_check":                    "a SKU footprint must be greater than zero",

	// price books and their items
	"price_books_name_key":             "a price book with that name already exists",
	"price_books_one_public_idx":       "another price book is already the public one; withdraw it first",
	"price_books_derived_uniq":         "this partner already has a book derived from that price book",
	"price_items_pkey":                 "that SKU is already priced in this book",
	"price_books_annual_divisor_check": "the annual divisor must be greater than zero",
	"price_books_bill_stopped_check":   "stopped resources must be billed as compute, storage-only or none",
	"price_books_scope_check":          "a price book covers either the cloud layer or the platform layer",
	"price_items_allowance_check":      "an allowance cannot be negative",
	"price_items_tier_mode_check":      "a tiered price is either graduated or all-units",

	// customers, partners and their commercial terms
	"customers_slug_key":                 "a customer with that short name already exists",
	"partners_slug_key":                  "a partner with that short name already exists",
	"partner_tiers_name_key":             "a partner tier with that name already exists",
	"partner_retail_rules_pkey":          "this partner already has a retail rule",
	"customers_payment_terms_days_check": "payment terms must be between 0 and 365 days",
	"customers_tax_rate_check":           "a tax rate must be between 0 and 1",
	"customers_tax_country_check":        "a tax country must be a two-letter country code",
	"partners_commission_pct_check":      "a commission must be between 0 and 100 percent",
	"partner_tiers_name_check":           "a partner tier needs a name",

	// cost sources
	"cost_sources_customer_id_kind_region_project_id_key":      "this customer already has a cost source for that kind, region and project",
	"cost_sources_internal_idx":                                "an internal cost source for that kind, region and project already exists",
	"usage_records_source_id_resource_id_sku_window_start_key": "that usage record has already been ingested",

	// tax
	"tax_rules_key_idx":             "a tax rule for that country, region, category and start date already exists",
	"tax_categories_pkey":           "that SKU already has a tax category",
	"tax_rules_country_check":       "a country must be a two-letter country code",
	"tax_rules_rate_check":          "a tax rate must be between 0 and 1",
	"tax_rules_validity_check":      "a tax rule cannot stop before it starts",
	"tax_categories_category_check": "a tax category needs a name",

	// access
	"role_bindings_customer_uniq":          "that person already holds this role here",
	"role_bindings_sovereign_uniq":         "that person already holds this role here",
	"role_bindings_partner_uniq":           "that person already holds this role here",
	"group_role_mappings_customer_uniq":    "that group is already mapped to this role here",
	"group_role_mappings_sovereign_uniq":   "that group is already mapped to this role here",
	"group_role_mappings_partner_uniq":     "that group is already mapped to this role here",
	"role_bindings_subject_email_check":    "a role is bound to a lower-case email address",
	"group_role_mappings_group_name_check": "a group mapping needs a group name",

	// invoicing, collections and the ledger
	"statements_customer_id_period_start_key":          "this customer already has a statement for that period",
	"statements_invoice_number_idx":                    "that invoice number is already in use",
	"statements_external_ref_idx":                      "that external invoice reference is already recorded",
	"credit_notes_number_key":                          "that credit-note number is already in use",
	"payments_reference_idx":                           "this customer already has a payment with that reference",
	"commercial_outbox_doc_type_idempotency_key_key":   "that document is already queued for delivery",
	"collection_reminders_statement_id_kind_stage_key": "that reminder stage was already recorded for this invoice",
	"account_entries_invoice_idx":                      "this invoice is already on the account ledger",
	"account_entries_payment_idx":                      "this payment is already on the account ledger",
	"account_entries_credit_note_idx":                  "this credit note is already on the account ledger",
	"einvoice_documents_pkey":                          "this invoice already has an e-invoicing document",
	"finance_periods_pkey":                             "that accounting period is already recorded",
	"account_mappings_pkey":                            "an account mapping with that key already exists",
	"finance_periods_period_check":                     "an accounting period is written as YYYY-MM",
	"account_mappings_key_check":                       "an account key is lower-case letters, digits, dot, dash or underscore",
	"credit_notes_total_check":                         "a credit note must be for more than zero",
	"payments_amount_check":                            "a payment must be for more than zero",
	"invoice_allocations_amount_check":                 "an allocation must be for more than zero",

	// contracts, budgets, discounts and saved views
	"contract_items_contract_id_kind_sku_key":      "that SKU is already on this contract",
	"budget_alerts_budget_id_period_threshold_key": "that budget alert was already raised for this period",
	"saved_views_owner_email_page_name_key":        "you already have a saved view with that name on this page",
	"contracts_check":                              "a contract cannot end before it starts",
	"contracts_term_months_check":                  "a contract term must be at least one month",
	"contracts_renewal_notice_days_check":          "a renewal notice cannot be negative",
	"contracts_minimum_commitment_check":           "a minimum commitment cannot be negative",
	"contracts_name_check":                         "a contract needs a name",
	"contract_items_quantity_check":                "a contract quantity cannot be negative",
	"contract_items_discount_pct_check":            "a contract discount must be between 0 and 100 percent",
	"contract_items_committed_price_check":         "a committed price cannot be negative",
	"budgets_amount_check":                         "a budget cannot be negative",
	"discounts_check":                              "a discount cannot end before it starts",
	"discounts_value_check":                        "a discount cannot be negative",
	"discounts_tier_scope_check":                   "a tier discount is a percentage and belongs to the tier, not to one customer",

	// self-service and settings
	"statement_disputes_open_idx":               "this invoice already has an open dispute",
	"statement_disputes_reason_check":           "a dispute needs a reason",
	"customer_payment_methods_token_idx":        "that payment method is already saved for this customer",
	"currency_rates_pkey":                       "that currency already has a rate",
	"currency_rates_code_check":                 "a currency code is three capital letters",
	"currency_rates_per_base_check":             "an exchange rate must be greater than zero",
	"billing_settings_invoice_prefix_check":     "an invoice prefix is 1 to 12 capital letters, digits or dashes",
	"billing_settings_credit_note_prefix_check": "a credit-note prefix is 1 to 12 capital letters, digits or dashes",
	"billing_settings_tax_rate_check":           "a tax rate must be between 0 and 1",
	"billing_settings_tax_country_check":        "a tax country must be a two-letter country code",
	"billing_settings_escalation_days_check":    "escalation days must be between 0 and 3650",
	"report_schedules_hour_utc_check":           "the hour must be between 0 and 23",
	"report_schedules_day_of_week_check":        "the day of the week must be between 0 and 6",
	"report_schedules_day_of_month_check":       "the day of the month must be between 1 and 28",
	"notification_preferences_key_idx":          "that address already has a preference for this event",
}

// The sentences used when the constraint is one nothing above names. They say
// only what the class of violation actually establishes: a duplicate, a
// missing reference, a refused value. Anything more would be a guess, and a
// guess dressed as an explanation is worse than an honest general sentence.
const (
	conflictFallback   = "a record with those values already exists"
	referenceFallback  = "something this record refers to does not exist"
	checkValueFallback = "one of the values is not allowed for this record"
)

// constraintErr logs what the driver said and returns sentinel carrying a
// sentence written for a person. The raw Detail — the only place the offending
// values appear — goes to the server log and nowhere else.
func constraintErr(sentinel error, pqe *pq.Error, fallback string) error {
	slog.Warn("database refused a write",
		"code", string(pqe.Code),
		"class", pqe.Code.Name(),
		"table", pqe.Table,
		"constraint", pqe.Constraint,
		"detail", pqe.Detail)
	msg := fallback
	if m, ok := constraintMessages[pqe.Constraint]; ok {
		msg = m
	}
	return fmt.Errorf("%w: %s", sentinel, msg)
}
