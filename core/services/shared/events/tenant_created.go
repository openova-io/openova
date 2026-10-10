package events

// tenant_created.go — #3687 (fold #3690/#3673). ONE shared wire shape for
// the `tenant.created` event, so the producer (tenant-service CreateOrg),
// the consumer (provisioning-service handleTenantCreated → Organization
// CR), and the bootstrap-API funnel all agree on a single struct instead
// of three divergent shapes:
//
//   - core/services/tenant/handlers/handlers.go    — published a flat
//     anonymous struct embedding *store.Tenant + owner_email.
//   - core/services/provisioning/handlers/organization_create.go — decoded
//     a local `tenantCreatedPayload` (slug/owner_email/tier/…).
//   - products/catalyst/bootstrap/api .../org_tenant.go — built its own
//     `orgShape` (Tier/BillingMode/ParentDomain).
//
// #3687 §2 (the law): "One unified primitive, no per-path special-case."
// A single canonical type here is the seam the producer-side
// unification rides — the Organization CR is minted from THIS shape, the
// same one every door emits. Lives in the shared module both the
// tenant-service and provisioning-service already import (alongside
// UsageRecordedPayload).
//
// Only the fields the Organization CR needs are modelled; unused
// store.Tenant fields (Apps, AddOns, CustomDomains, …) are intentionally
// omitted so schema drift on them is non-blocking. The producer maps its
// store.Tenant onto this via NewTenantCreatedPayload; the consumer decodes
// straight into it.

import "strings"

// TenantCreatedPayload is the canonical JSON shape of the `tenant.created`
// event. Field tags match the historical wire shape both the producer
// emitted and the consumer decoded, so adopting this struct is wire-
// compatible (no event-format migration required).
type TenantCreatedPayload struct {
	// ID is the tenant's stable identifier (store.Tenant.ID). Carried so
	// the minted Organization CR can label-back to the projection row.
	ID string `json:"id"`
	// Slug is the DNS-/namespace-/repo-safe Organization identifier — the
	// canonical name the org-controller keys every artifact off.
	Slug string `json:"slug"`
	// Name is the human display name (defaults to Slug when empty).
	Name string `json:"name"`
	// OwnerID is the creator's Keycloak user UUID.
	OwnerID string `json:"owner_id"`
	// OwnerEmail seeds the Organization's owner roster — required to mint
	// a non-half-populated Organization CR.
	OwnerEmail string `json:"owner_email"`
	// PlanID is the selected plan (informational; tier drives RBAC).
	PlanID string `json:"plan_id"`
	// Tier is the Organization tier (defaults to "org" when empty — the
	// only tier the Organization-pool wizard issues vouchers for today).
	Tier string `json:"tier,omitempty"`
	// BillingMode defaults to "real" when empty.
	BillingMode string `json:"billing_mode,omitempty"`
	// ParentDomain optionally overrides the Sovereign-wide default apex
	// zone per tenant (omani.homes vs omani.rest vs omani.trade). Empty
	// inherits the Sovereign default.
	ParentDomain string `json:"parent_domain,omitempty"`

	// Commerce — #6971 item 8. What was bought, so the minted Organization
	// CR carries `spec.commerce` and the chargeback OpenOva adapter can
	// attach the Organization's platform Source to its package and add-on
	// lines. The billing order is the source: core/services/tenant persists
	// these on launch (from the storefront's POST /tenant/orgs and then from
	// billing's settlement launch body, which wins) and emits them here. All
	// four are optional and `omitempty`, so a legacy producer that never sets
	// them emits the exact pre-#6971 wire bytes and the consumer mints no
	// `spec.commerce` block.
	//
	// PackageSKU is the BSS package sku ("plan.m"). Addons carries BSS
	// add-on SKUs ONLY ("addon.backup") — never the catalog add-on ids the
	// storefront mixes into the same cart list; producers run the list
	// through BSSAddonSKUs. PriceSource is "catalog" or
	// "bss:<price_book>@<prices_as_of>". OrderID is the billing `orders.id`.
	PackageSKU  string   `json:"package_sku,omitempty"`
	Addons      []string `json:"addons,omitempty"`
	PriceSource string   `json:"price_source,omitempty"`
	OrderID     string   `json:"order_id,omitempty"`
}

// TenantCommerce is the purchase a producer attaches to a
// TenantCreatedPayload via WithCommerce. Zero value = no purchase recorded
// (the sovereign-admin door, a direct API create, a legacy record).
type TenantCommerce struct {
	PackageSKU  string
	Addons      []string
	PriceSource string
	OrderID     string
}

// BSSAddonSKUPrefix is what distinguishes a Catalyst BSS add-on SKU
// ("addon.backup") from a catalog add-on id ("waf", a UUID) in the ONE
// cart list the storefront sends (core/marketplace/src/lib/cart.ts).
const BSSAddonSKUPrefix = "addon."

// BSSAddonSKUs returns the BSS add-on SKUs in `in`, in first-seen order:
// trimmed, lower-cased, de-duplicated, and ONLY the entries carrying
// BSSAddonSKUPrefix. Catalog add-on ids are dropped — the Organization CR's
// spec.commerce.addons is read by the chargeback adapter as BSS SKUs, and a
// catalog id there would attach to nothing. nil in → nil out; a list with
// no SKU → nil, so `omitempty` keeps the wire bytes legacy-identical.
func BSSAddonSKUs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if !strings.HasPrefix(s, BSSAddonSKUPrefix) || len(s) == len(BSSAddonSKUPrefix) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// WithCommerce stamps the purchase onto the payload. Addons is normalised
// through BSSAddonSKUs; scalars are trimmed. An all-empty TenantCommerce is
// a no-op, so callers pass whatever the record holds without pre-checking.
func (p TenantCreatedPayload) WithCommerce(c TenantCommerce) TenantCreatedPayload {
	p.PackageSKU = strings.ToLower(strings.TrimSpace(c.PackageSKU))
	p.Addons = BSSAddonSKUs(c.Addons)
	p.PriceSource = strings.TrimSpace(c.PriceSource)
	p.OrderID = strings.TrimSpace(c.OrderID)
	return p
}

// HasCommerce reports whether any purchase field is set — the consumer's
// "mint a spec.commerce block or not" test.
func (p TenantCreatedPayload) HasCommerce() bool {
	return p.PackageSKU != "" || len(p.Addons) > 0 || p.PriceSource != "" || p.OrderID != ""
}

// NewTenantCreatedPayload builds a canonical payload from the raw fields a
// producer has (the tenant-service maps its store.Tenant onto this). It
// trims whitespace; downstream defaulting (tier→"org", billing→"real",
// name→slug) stays in the consumer so every door defaults identically.
func NewTenantCreatedPayload(id, slug, name, ownerID, ownerEmail, planID, tier, billingMode, parentDomain string) TenantCreatedPayload {
	return TenantCreatedPayload{
		ID:           strings.TrimSpace(id),
		Slug:         strings.TrimSpace(slug),
		Name:         strings.TrimSpace(name),
		OwnerID:      strings.TrimSpace(ownerID),
		OwnerEmail:   strings.TrimSpace(ownerEmail),
		PlanID:       strings.TrimSpace(planID),
		Tier:         strings.TrimSpace(tier),
		BillingMode:  strings.TrimSpace(billingMode),
		ParentDomain: strings.TrimSpace(parentDomain),
	}
}
