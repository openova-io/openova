// pool_domains.go — the Sovereign's served org-pool TLD set (#4999, Refs #3376).
//
// Mirrors core/services/tenant/handlers/pool_domains.go (the two services are
// separate Go modules, so the tiny served-pool helper is duplicated the same way
// resolveOrgParentDomain already is). resolveOrgParentDomain uses this to HONOR
// the customer's funnel-selected pool TLD when the Sovereign serves that zone,
// instead of the pre-#4999 #4421 band-aid that dropped every non-primary pick.
package handlers

import (
	"os"
	"strings"
)

// defaultPoolTLDs is the canonical OpenOva-managed org-pool free-subdomain set —
// matches core/services/domain/store.AllowedTLDs, the marketplace /addons picker,
// and catalyst-api's CATALYST_ORG_POOL_DOMAINS seed. All four are
// PowerDNS-delegated + DNS-01-wired on every Sovereign, so provisioning a
// customer Org under any of them is safe. Used as the served set when
// TENANT_POOL_DOMAINS is unset, so a fresh Sovereign honors the funnel TLD choice
// with no extra config.
var defaultPoolTLDs = []string{"omani.rest", "omani.works", "omani.trade", "omani.homes"}

// PoolDomainsFromEnv reads the operator-overridable served-pool set from
// TENANT_POOL_DOMAINS (comma-separated FQDNs). Empty → nil (resolveOrgParentDomain
// falls back to defaultPoolTLDs).
func PoolDomainsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("TENANT_POOL_DOMAINS"))
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if z := normalizePoolZone(p); z != "" {
			out = append(out, z)
		}
	}
	return out
}

// normalizePoolZone lowercases, trims, and strips any leading dot.
func normalizePoolZone(z string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(z), "."))
}

// isServedPoolDomain reports whether tld is a pool zone this Sovereign serves.
// An empty configured set falls back to the canonical defaultPoolTLDs.
func isServedPoolDomain(tld string, configured []string) bool {
	tld = normalizePoolZone(tld)
	if tld == "" {
		return false
	}
	set := configured
	if len(set) == 0 {
		set = defaultPoolTLDs
	}
	for _, z := range set {
		if normalizePoolZone(z) == tld {
			return true
		}
	}
	return false
}
