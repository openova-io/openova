// Package provisioner — SKU/region availability matrix (issue #916).
//
// Cloud providers list every SKU in their global catalog but only sell
// some of them in some DCs. Hetzner, for example, lists `cpx32` with a
// global price but POST /v1/servers in `ash` (Ashburn) returns
// `{"error":{"code":"invalid_input","message":"unsupported location for
// server type"}}` — exactly the failure that broke otech109's
// `tofu apply` 41 seconds in, after Phase 0 had already created the
// CP + network + LB + firewall.
//
// This file is the GO-SIDE MIRROR of the wizard's
// `products/catalyst/bootstrap/ui/src/shared/constants/providerSizes.ts`
// `availableRegions` field. Two-sided enforcement is intentional — the
// wizard hides unavailable SKUs from its dropdown, but a stale wizard
// build OR a direct API caller bypassing the UI MUST still hit this
// gate at the catalyst-api boundary. See `Request.Validate` in
// provisioner.go.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the matrix is
// runtime-configurable: refreshing it is a single PR + image bump, not
// a redesign. When Hetzner adds back orderability for a SKU/region pair
// (or a hyperscaler deprecates one), update this file AND
// providerSizes.ts in the same commit so the two sides stay in step.

package provisioner

import (
	"fmt"
	"strings"
)

// skuRegionAvailability maps "<provider>:<sku>" to the list of
// orderable regions, mirroring `NodeSize.availableRegions` in
// `providerSizes.ts`.
//
// Semantics match the wizard's `isSkuAvailableInRegion`:
//   - missing entry  → no constraint (orderable in every region the
//     provider lists)
//   - empty slice    → orderable nowhere new (Hetzner-deprecated SKU
//     that's still listed under /v1/server_types but rejected by
//     POST /v1/servers — cpx21, cpx31)
//   - non-empty list → orderable exactly in those region ids
//
// Region ids are the SAME identifiers the wizard's PROVIDER_REGIONS
// catalog uses: Hetzner = fsn1/nbg1/hel1/ash/hil; AWS = us-east-1/
// eu-west-1/...; Azure = westeurope/eastus/...; OCI = eu-frankfurt-1/
// us-ashburn-1/...; Huawei = eu-west-101/eu-west-204/...
var skuRegionAvailability = map[string][]string{
	// Hetzner — only the constrained SKUs listed; everything else is
	// "no constraint" (= every region the provider lists).
	"hetzner:cpx21": {}, // listed in /v1/server_types but POST /v1/servers rejects everywhere (issue #752)
	"hetzner:cpx31": {}, // same as cpx21 — Hetzner deprecated the cpx{1,2,3,4}1 family for new orders
	"hetzner:cpx32": {"fsn1", "nbg1", "hel1"}, // EU only — issue #916 root cause (otech109 in `ash`)
}

// IsSkuAvailableInRegion reports whether (provider, sku, region) is
// orderable per the matrix. Mirrors the wizard's
// `isSkuAvailableInRegion(provider, skuId, region)` predicate so the
// two sides agree byte-for-byte.
//
// Returns true when:
//   - sku is unknown (skip — let downstream report any error)
//   - sku has no availability entry (= every region)
//   - sku's availability list contains region
//
// Returns false only when an explicit list exists and region is NOT in
// it (including the empty-list case).
//
// `region` is matched case-INSENSITIVELY against the registered ids
// because Hetzner's region tokens are lowercase but operators
// occasionally type "FSN1" — easier to handle here than to thread a
// normalisation layer through every caller.
func IsSkuAvailableInRegion(provider, sku, region string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	sku = strings.TrimSpace(sku)
	region = strings.ToLower(strings.TrimSpace(region))
	if provider == "" || sku == "" || region == "" {
		// Empty inputs are not this validator's concern — the
		// surrounding Validate() already enforces non-empty
		// provider/region/SKU, and an empty value here would falsely
		// trigger an availability error that drowns out the more
		// specific "X is required" message.
		return true
	}
	key := provider + ":" + sku
	allowed, ok := skuRegionAvailability[key]
	if !ok {
		// No constraint registered → orderable in every region.
		return true
	}
	for _, r := range allowed {
		if strings.ToLower(r) == region {
			return true
		}
	}
	return false
}

// AvailableRegionsForSku returns the orderable region list for a SKU,
// or `nil` when the SKU is unconstrained. Used by the wizard's error
// surface (via /api/v1/regions/sku-availability or similar) and by
// `Validate()` to compose human-readable rejection messages — exposing
// "orderable in fsn1, nbg1, hel1" rather than the raw rejection.
//
// A nil return means "no constraint", which the caller MUST distinguish
// from the empty-slice "orderable nowhere new" semantics. Use
// `_, constrained := AvailableRegionsForSku(...)` when the difference
// matters.
func AvailableRegionsForSku(provider, sku string) ([]string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	sku = strings.TrimSpace(sku)
	key := provider + ":" + sku
	regions, ok := skuRegionAvailability[key]
	return regions, ok
}

// formatSkuRegionError builds the rejection message used by Validate()
// when a SKU/region combination is unavailable. Mirrors the
// human-readable shape the wizard would surface in its inline-error UI
// when the StepProvider catches the same condition client-side, so the
// operator sees the same message either path.
//
// `role` is "controlPlaneSize" or "workerSize" (matches the JSON
// payload field names — the operator can act on the message without a
// translation step).
func formatSkuRegionError(role, provider, sku, region string) string {
	regions, constrained := AvailableRegionsForSku(provider, sku)
	if !constrained {
		// Defensive — Validate() only calls this when
		// IsSkuAvailableInRegion returned false, which can only
		// happen for a constrained SKU. Reaching this branch means
		// the matrix lookup raced with a runtime modification; report
		// the bare facts.
		return fmt.Sprintf(
			"%s %q is not orderable in region %q for provider %q (no fallback regions registered)",
			role, sku, region, provider,
		)
	}
	if len(regions) == 0 {
		return fmt.Sprintf(
			"%s %q is no longer orderable for new servers in any region of provider %q — pick a different SKU",
			role, sku, provider,
		)
	}
	return fmt.Sprintf(
		"%s %q is not orderable in region %q for provider %q — try one of: %s",
		role, sku, region, provider, strings.Join(regions, ", "),
	)
}
