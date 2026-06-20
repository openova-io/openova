// Package handler — sovereign_parent_domains.go: env-stub seam +
// Sovereign-data-model adapter for the parent-domain pool consumed by
// the Organization tenant create pipeline (epic #825 / MD-3 #828).
//
// The HTTP surface (`GET /api/v1/sovereign/parent-domains`) is owned
// by `parent_domains.go` (issue #829) — admin "add another parent
// domain" + DNS propagation status panel. This file does NOT register
// a duplicate route; instead it provides:
//
//  1. LoadOrganizationParentDomainsFromEnv — startup wiring helper that
//     seeds OrganizationDeps.ParentDomains from
//     CATALYST_ORG_POOL_DOMAINS env (stub for #826's data model).
//
//  2. ParentDomainsForOrgCreate — runtime adapter the Organization tenant
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

// LoadOrganizationParentDomainsFromEnv returns the env-derived
// org-pool seed. Wired to CATALYST_ORG_POOL_DOMAINS (comma-separated
// FQDNs; primary role marker is `<fqdn>:primary`, default role is
// org-pool). When the env knob is unset and CATALYST_OTECH_FQDN is
// set, returns the otech FQDN as the implicit primary entry plus the
// four canonical org-pool entries (omani.homes, omani.rest,
// omani.trade, omani.works) — the operator-curated free-subdomain
// pool offered to Organization tenants per DoD D30 (issue #1830).
//
// Pool composition note (2026-05-18):
// The four .omani.X TLDs are the canonical OpenOva-managed
// org-pool — they are already delegated to the Sovereign's PowerDNS
// (no Dynadot flip needed; nsAlreadyMatches short-circuit in
// pdmFlipNS covers Day-2 re-adds) and the cert-manager DNS-01
// solver is wired for *.X.<chosen>.<pool>. The marketplace UI
// (core/marketplace/src/components/AddonsStep.svelte) hard-codes
// the same four entries; this seed keeps the backend pool aligned
// with what the customer-facing /addons subdomain picker shows.
// core/services/domain/store.AllowedTLDs has the same authoritative
// list and is the central registry.
//
// Forward-compat: when MD-1 lands the catalyst-api startup wiring
// switches to read from the Sovereign's deployment record. The
// OrganizationDeps consumer (organization_provisioning.go) doesn't change.
func LoadOrganizationParentDomainsFromEnv() []OrganizationParentDomain {
	raw := strings.TrimSpace(os.Getenv("CATALYST_ORG_POOL_DOMAINS"))
	otech := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if raw == "" {
		// Hardcoded stub fallback. TODO(#826): remove once the data
		// model is live.
		out := []OrganizationParentDomain{}
		if otech != "" {
			out = append(out, OrganizationParentDomain{
				Name: strings.ToLower(otech), Role: "primary", NSFlipReady: true,
			})
		}
		// DoD D30 (issue #1830) — the four canonical .omani.X
		// org-pool entries. Match core/services/domain/store.AllowedTLDs
		// and core/marketplace/src/components/AddonsStep.svelte's
		// picker so backend validation accepts every TLD the customer
		// UI offers.
		out = append(out,
			OrganizationParentDomain{Name: "omani.homes", Role: "org-pool", NSFlipReady: true},
			OrganizationParentDomain{Name: "omani.rest", Role: "org-pool", NSFlipReady: true},
			OrganizationParentDomain{Name: "omani.trade", Role: "org-pool", NSFlipReady: true},
			OrganizationParentDomain{Name: "omani.works", Role: "org-pool", NSFlipReady: true},
		)
		return out
	}
	out := []OrganizationParentDomain{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		role := "org-pool"
		name := entry
		if strings.Contains(entry, ":") {
			parts := strings.SplitN(entry, ":", 2)
			name = strings.TrimSpace(parts[0])
			r := strings.ToLower(strings.TrimSpace(parts[1]))
			if r == "primary" || r == "org-pool" {
				role = r
			}
		}
		out = append(out, OrganizationParentDomain{
			Name: strings.ToLower(name), Role: role, NSFlipReady: true,
		})
	}
	return out
}

// ParentDomainsForOrgCreate composes the live parent-domain pool the
// Organization tenant create handler validates against. The runtime source of
// truth is the adopted Deployment's Request.ParentDomains slice
// (issue #837 — replaces the legacy in-memory globalParentDomainStore
// the admin handler used to seed) merged with the implicit primary
// domain (lookupPrimaryDomain).
//
// Returned entries are normalised to OrganizationParentDomain so the
// create handler's existing FindParentDomain / PoolDomains paths work
// uniformly across sources.
//
// NOTE: this method intentionally does NOT fold the env stub into its
// output. The env stub seed is wired exactly once at startup (via
// LoadOrganizationParentDomainsFromEnv → OrganizationDeps.ParentDomains) so
// OrganizationDeps remains the single startup-time seed. The runtime
// adapter only adds entries the operator has changed *after* startup
// (admin-persisted entries on the adopted deployment + adopted
// primary). This preserves the back-compat behaviour from #804 where
// a single-domain Sovereign with no admin entries falls back to
// OTECHFQDN as the implicit org-pool parent.
func (h *Handler) ParentDomainsForOrgCreate() []OrganizationParentDomain {
	live := listParentDomainsFromActive(h.activeDeployment())
	out := make([]OrganizationParentDomain, 0, len(live)+1)
	seen := map[string]struct{}{}
	for _, p := range live {
		// Persisted entries surface as FlipStatusReady from
		// listParentDomainsFromActive (issue #837 — the durable
		// record IS the proof the pipeline succeeded).
		ready := p.FlipStatus == FlipStatusReady ||
			p.FlipStatus == FlipStatusFlipped
		out = append(out, OrganizationParentDomain{
			Name:        strings.ToLower(p.Name),
			Role:        string(p.Role),
			NSFlipReady: ready,
		})
		seen[strings.ToLower(p.Name)] = struct{}{}
	}
	if primary := h.lookupPrimaryDomain(); primary != "" {
		key := strings.ToLower(primary)
		if _, ok := seen[key]; !ok {
			out = append(out, OrganizationParentDomain{
				Name: key, Role: "primary", NSFlipReady: true,
			})
		}
	}
	return out
}
