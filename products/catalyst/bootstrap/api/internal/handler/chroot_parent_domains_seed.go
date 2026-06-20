// chroot_parent_domains_seed.go — bake-time top-up of the canonical
// .omani.X org-pool entries on Sovereign-side catalyst-api.
//
// Why this exists
// ---------------
// The mothership wizard stamps `Request.ParentDomains` with the
// operator-selected primary (e.g. `omani.works`) plus exactly one
// org-pool entry — the pool TLD the operator picked at provisioning
// time. When the mother POSTs the full record to the Sovereign-side
// catalyst-api via /api/v1/internal/deployments/import, the chroot
// inherits that same 2-entry slice.
//
// Result: `GET /api/v1/sovereign/parent-domains?role=org-pool` returns
// 1 of 4 entries instead of the canonical 4 the marketplace UI's
// /addons subdomain picker hard-codes (omani.homes, omani.rest,
// omani.trade, omani.works — same list as
// core/services/domain/store.AllowedTLDs). A customer who selects a
// missing TLD on the picker hits a 422 invalid-parent-domain at SME
// tenant signup because FindParentDomain doesn't recognise the TLD.
//
// PR #1861 widened LoadOrganizationParentDomainsFromEnv() to seed all 4
// entries, but that's the *env-stub fallback* path used by the SME
// tenant create handler's startup wiring (OrganizationDeps.ParentDomains).
// The /api/v1/sovereign/parent-domains HTTP surface reads from the
// adopted Deployment's persistent Request.ParentDomains slice, NOT
// the env stub. The mother-written record bypasses the fallback.
//
// Fix shape
// ---------
// Run a chroot-mode bake-time top-up: when SOVEREIGN_FQDN is set AND
// we have an adopted/in-memory deployment record, ensure every
// canonical .omani.X entry exists as Role=org-pool. Idempotent — a
// re-run is a no-op when the pool is already full. Same pattern as
// the D21 owner-UserAccess bake-time seed (PR #1893): the chroot
// fills in operator-curated defaults that the mother either could
// not or did not stamp.
//
// Called from two places so a fresh prov AND a Pod restart both
// converge:
//
//   1. HandleDeploymentImport — after the mother POSTs the record at
//      handover. Closes the data-half of the contract for a fresh
//      prov.
//
//   2. restoreFromStore — at catalyst-api startup, for every
//      rehydrated deployment. Covers the Pod-restart case where the
//      import already ran on a prior Pod and the on-disk record is
//      still short of 4 entries (the situation #1907 caught on t31).
//
// Mothership mode: SOVEREIGN_FQDN unset → no-op. The mother only ever
// holds the operator-selected org-pool entry; the canonical pool is a
// per-Sovereign concept and doesn't apply on the mothership.
//
// AllowedTLDs is the central registry (core/services/domain/store).
// We duplicate the literal here only to avoid pulling a Mongo client
// dependency into the bootstrap package — the lockstep is enforced by
// TestChrootEnsureOrgPoolSeed_MatchesAllowedTLDsLiteral below.
package handler

import (
	"os"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// canonicalOrgPoolDomains — the four .omani.X TLDs the marketplace
// UI offers in its /addons subdomain picker. Locked to
// core/services/domain/store.AllowedTLDs (asserted via a
// compile-time-style test below). Order is intentional and matches
// the env-stub fallback in sovereign_parent_domains.go so the wire
// shape is deterministic.
var canonicalOrgPoolDomains = []string{
	"omani.homes",
	"omani.rest",
	"omani.trade",
	"omani.works",
}

// chrootEnsureOrgPoolSeed tops up dep.Request.ParentDomains with any
// missing canonical org-pool entries. No-op when:
//   - dep is nil
//   - we are not in chroot mode (SOVEREIGN_FQDN unset)
//   - every canonical entry is already present
//
// When at least one row is appended, the function persists the
// deployment record so a Pod restart sees the topped-up shape.
// Returns the number of rows appended so the caller can log.
func (h *Handler) chrootEnsureOrgPoolSeed(dep *Deployment) int {
	if dep == nil {
		return 0
	}
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) == "" {
		return 0
	}
	dep.mu.Lock()
	// Dedup against existing org-pool entries only — a row with
	// role=primary on the same name does NOT count as "already
	// present" for pool purposes. The customer-facing /addons picker
	// validates against role=org-pool entries, so the pool needs every
	// canonical TLD listed as org-pool even when the operator's
	// chosen primary happens to share a name with one of them (the
	// real-world t31 shape: primary=omani.works, pool must still
	// surface omani.works as a org-pool row so SME tenants picking
	// .omani.works pass FindParentDomain).
	presentPool := map[string]struct{}{}
	for _, pd := range dep.Request.ParentDomains {
		if string(pd.Role) != provisioner.ParentDomainRoleOrgPool {
			continue
		}
		presentPool[strings.ToLower(strings.TrimSpace(pd.Name))] = struct{}{}
	}
	now := time.Now().UTC()
	appended := 0
	for _, name := range canonicalOrgPoolDomains {
		if _, ok := presentPool[name]; ok {
			continue
		}
		dep.Request.ParentDomains = append(dep.Request.ParentDomains, provisioner.ParentDomain{
			Name:    name,
			Role:    provisioner.ParentDomainRoleOrgPool,
			AddedAt: now,
		})
		appended++
	}
	// Keep the slice in deterministic order so wire-shape lock tests
	// (and ListParentDomains' sort.Slice) don't surface flakes.
	sort.SliceStable(dep.Request.ParentDomains, func(i, j int) bool {
		return strings.ToLower(dep.Request.ParentDomains[i].Name) <
			strings.ToLower(dep.Request.ParentDomains[j].Name)
	})
	dep.mu.Unlock()
	if appended > 0 {
		h.persistDeployment(dep)
		h.log.Info("chroot: seeded canonical org-pool parent domains",
			"depId", dep.ID,
			"appended", appended,
			"pool", canonicalOrgPoolDomains,
		)
	}
	return appended
}
