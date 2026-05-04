// Package handler — sovereign_parent_domains.go: env-stub seam +
// Sovereign-data-model adapter for the parent-domain pool consumed by
// the SME tenant create pipeline (epic #825 / MD-3 #828).
//
// The HTTP surface (`GET /api/v1/sovereign/parent-domains`) is owned
// by `parent_domains.go` (issue #829) — admin "add another parent
// domain" + DNS propagation status panel. This file does NOT register
// a duplicate route; instead it provides:
//
//  1. LoadSMETenantParentDomainsFromEnv — startup wiring helper that
//     seeds SMETenantDeps.ParentDomains from
//     CATALYST_SME_POOL_DOMAINS env (stub for #826's data model).
//
//  2. ParentDomainsForSMECreate — runtime adapter the SME tenant
//     create handler uses to validate the operator-supplied
//     parent_domain. Reads from the same global parentDomainStore
//     that ListParentDomains surfaces, so what the operator sees in
//     the dropdown == what the create handler accepts.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the pool is fully data-driven.
// While MD-1 (#826) is in flight the env stub answers; once MD-1 lands
// the orchestrator's source-of-truth is Deployment.parentDomains[]
// (the wire shape is forward-compatible — same JSON keys).
package handler

import (
	"os"
	"strings"
)

// LoadSMETenantParentDomainsFromEnv returns the env-derived
// SME-pool seed. Wired to CATALYST_SME_POOL_DOMAINS (comma-separated
// FQDNs; primary role marker is `<fqdn>:primary`, default role is
// sme-pool). When the env knob is unset and CATALYST_OTECH_FQDN is
// set, returns the otech FQDN as the implicit primary entry plus the
// canonical-but-stub `omani.works` and `omani.trade` sme-pool entries
// — covering the #828 constraint "stub returning a 2-domain hardcoded
// list with TODO" while MD-1 (#826) is in flight.
//
// Forward-compat: when MD-1 lands the catalyst-api startup wiring
// switches to read from the Sovereign's deployment record. The
// SMETenantDeps consumer (sme_tenant.go) doesn't change.
func LoadSMETenantParentDomainsFromEnv() []SMETenantParentDomain {
	raw := strings.TrimSpace(os.Getenv("CATALYST_SME_POOL_DOMAINS"))
	otech := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if raw == "" {
		// Hardcoded stub fallback. TODO(#826): remove once the data
		// model is live.
		out := []SMETenantParentDomain{}
		if otech != "" {
			out = append(out, SMETenantParentDomain{
				Name: strings.ToLower(otech), Role: "primary", NSFlipReady: true,
			})
		}
		out = append(out,
			SMETenantParentDomain{Name: "omani.works", Role: "sme-pool", NSFlipReady: true},
			SMETenantParentDomain{Name: "omani.trade", Role: "sme-pool", NSFlipReady: true},
		)
		return out
	}
	out := []SMETenantParentDomain{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		role := "sme-pool"
		name := entry
		if strings.Contains(entry, ":") {
			parts := strings.SplitN(entry, ":", 2)
			name = strings.TrimSpace(parts[0])
			r := strings.ToLower(strings.TrimSpace(parts[1]))
			if r == "primary" || r == "sme-pool" {
				role = r
			}
		}
		out = append(out, SMETenantParentDomain{
			Name: strings.ToLower(name), Role: role, NSFlipReady: true,
		})
	}
	return out
}

// ParentDomainsForSMECreate composes the live parent-domain pool the
// SME tenant create handler validates against. The runtime source of
// truth is the global parentDomainStore (admin "add a domain" entries
// from issue #829) merged with the implicit primary domain
// (lookupPrimaryDomain).
//
// Returned entries are normalised to SMETenantParentDomain so the
// create handler's existing FindParentDomain / PoolDomains paths work
// uniformly across sources.
//
// NOTE: this method intentionally does NOT fold the env stub into its
// output. The env stub seed is wired exactly once at startup (via
// LoadSMETenantParentDomainsFromEnv → SMETenantDeps.ParentDomains) so
// SMETenantDeps remains the single startup-time seed. The runtime
// adapter only adds entries the operator has changed *after* startup
// (admin store + adopted primary). This preserves the back-compat
// behaviour from #804 where a single-domain Sovereign with no admin
// entries falls back to OTECHFQDN as the implicit sme-pool parent.
func (h *Handler) ParentDomainsForSMECreate() []SMETenantParentDomain {
	live := globalParentDomainStore.list()
	out := make([]SMETenantParentDomain, 0, len(live)+1)
	seen := map[string]struct{}{}
	for _, p := range live {
		// Map the admin-store FlipStatus into SMETenant's narrower
		// boolean flag. Anything past `flipped` (zone created + cert
		// issued) is "ready"; pre-flip states are not yet bookable.
		ready := p.FlipStatus == FlipStatusReady ||
			p.FlipStatus == FlipStatusFlipped
		out = append(out, SMETenantParentDomain{
			Name:        strings.ToLower(p.Name),
			Role:        string(p.Role),
			NSFlipReady: ready,
		})
		seen[strings.ToLower(p.Name)] = struct{}{}
	}
	if primary := h.lookupPrimaryDomain(); primary != "" {
		key := strings.ToLower(primary)
		if _, ok := seen[key]; !ok {
			out = append(out, SMETenantParentDomain{
				Name: key, Role: "primary", NSFlipReady: true,
			})
		}
	}
	return out
}
