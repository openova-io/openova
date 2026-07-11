// pool_domains.go — the Sovereign's served org-pool TLD set (#4999, Refs #3376).
//
// The marketplace funnel offers a pool-TLD choice on /addons
// (.omani.homes / .omani.rest / .omani.trade / .omani.works). Before #4999 the
// backend DROPPED that choice — resolveOrgParentDomain forced the single
// Sovereign-wide apps pool (TENANT_PARENT_DOMAIN) to win (the #4421 band-aid),
// so "two Orgs on two different TLDs" (Pillar-1) could never hold. This file
// carries the served-pool set that lets resolveOrgParentDomain HONOR the pick
// when the Sovereign actually serves that zone.
package handlers

import (
	"os"
	"strings"
)

// defaultPoolTLDs is the canonical OpenOva-managed org-pool free-subdomain set.
// It matches core/services/domain/store.AllowedTLDs, the marketplace /addons
// picker (core/marketplace/src/components/AddonsStep.svelte), and catalyst-api's
// CATALYST_ORG_POOL_DOMAINS seed (sovereign_parent_domains.go). All four are
// PowerDNS-delegated + DNS-01-wired on every Sovereign, so provisioning a
// customer Org under any of them is safe. Used as the served set when
// TENANT_POOL_DOMAINS is unset, so a fresh Sovereign honors the funnel TLD
// choice with no extra config.
var defaultPoolTLDs = []string{"omani.rest", "omani.works", "omani.trade", "omani.homes"}

// PoolDomainsFromEnv reads the operator-overridable served-pool set from
// TENANT_POOL_DOMAINS (comma-separated FQDNs). Empty → nil (resolveOrgParentDomain
// falls back to defaultPoolTLDs). Wired onto the Handler in main.go so the set is
// data-driven (Inviolable Principle #4) yet needs no config on a canonical
// Sovereign.
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

// normalizePoolZone lowercases, trims, and strips any leading dot so
// ".Omani.Rest" and "omani.rest" compare equal.
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
