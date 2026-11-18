// sku.go — which Hetzner server types can be ordered in which location
// (issue #916). Moved here from the engine (CLD-103).
//
// Hetzner lists every server type in its global catalog but sells some of
// them only in some locations: POST /v1/servers with cpx32 in ash returns
// "unsupported location for server type", which broke a `tofu apply` 41
// seconds in, after the control plane, network, load balancer and firewall
// were created. The wizard hides such types (providerSizes.ts,
// availableRegions); this check refuses them at the API boundary for a stale
// wizard or a direct caller. Keep both sides in step.

package hetzner

import (
	"fmt"
	"strings"
)

// skuRegionAvailability maps a server type to the locations where it can be
// ordered:
//   - no entry: every location;
//   - empty list: no location (listed in /v1/server_types, refused by
//     POST /v1/servers: cpx21, cpx31, issue #752);
//   - a list: exactly those locations.
var skuRegionAvailability = map[string][]string{
	"cpx21": {},
	"cpx31": {},
	"cpx32": {"fsn1", "nbg1", "hel1"},
}

// skuAvailableInRegion reports whether sku can be ordered in region. An
// empty sku or region is not judged here.
func skuAvailableInRegion(sku, region string) bool {
	sku = strings.TrimSpace(sku)
	region = strings.ToLower(strings.TrimSpace(region))
	if sku == "" || region == "" {
		return true
	}
	allowed, ok := skuRegionAvailability[sku]
	if !ok {
		return true
	}
	for _, r := range allowed {
		if r == region {
			return true
		}
	}
	return false
}

// skuRegionError is the message for a server type that cannot be ordered in
// region; role names the request field.
func skuRegionError(role, sku, region string) string {
	regions := skuRegionAvailability[strings.TrimSpace(sku)]
	if len(regions) == 0 {
		return fmt.Sprintf("%s %q is no longer orderable for new servers in any region of provider %q — pick a different SKU", role, sku, Name)
	}
	return fmt.Sprintf("%s %q is not orderable in region %q for provider %q — try one of: %s", role, sku, region, Name, strings.Join(regions, ", "))
}
