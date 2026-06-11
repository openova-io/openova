// sovereign_dns_records.go — write per-Sovereign A records into the
// parent zone after Phase-0 tofu output captures the primary
// load-balancer IP.
//
// Why this exists:
//   The wizard provisions a Sovereign with a FQDN like
//   "t111.omani.works" on a parent zone "omani.works". Browser users
//   hit "console.t111.omani.works" which must resolve to the primary
//   region's Hetzner load-balancer public IP. Pre-#1505 nothing in
//   catalyst-api wrote those records into the parent zone — the
//   architecture had a stub pdmCreatePowerDNSZone (parent_domains.go:1096
//   "stub: no powerdns client wired") and a separate PDM commit path
//   that only fires for POOL-allocated subdomains (otech<N>.<pool>).
//   BYO-style FQDNs (operator-owned parent + arbitrary Sovereign name)
//   left the parent zone untouched, so console.<fqdn> returned NXDOMAIN
//   (or worse: hit a stale wildcard pointing at an orphan IP).
//
//   Caught on prov t110.omani.works (fe09897a1b6b3c1d, 2026-05-15):
//   primary LB at 77.42.11.95 was healthy + serving the LE PROD cert,
//   but `dig console.t110.omani.works` returned 49.12.16.160 — an
//   orphan IP from a wiped earlier prov (since reassigned to a
//   different Hetzner customer). Traffic was being routed to a third
//   party.
//
// What this writes:
//   For every CanonicalSovereignSubdomain (console / auth / gitea /
//   harbor / bao / grafana / hubble / pdns / pdns-admin / openova-flow /
//   marketplace / api / registry / guacamole / newapi / sandbox) an A record
//     <sub>.<sovereign-fqdn>. → <primary-lb-ipv4>
//   in the parent zone. PATCH REPLACE — idempotent re-runs are safe.

package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// CanonicalSovereignSubdomains — the public hostnames every Sovereign
// exposes through its Cilium Gateway. Must stay aligned with the
// HTTPRoutes shipped by:
//   - clusters/_template/sovereign-tls/cilium-gateway-cert.yaml (SAN list)
//   - platform/catalyst-platform/chart/templates/*.yaml
//   - platform/bp-keycloak / bp-gitea / bp-harbor / bp-openbao / bp-grafana /
//     bp-pdns / bp-openova-flow-server / bp-guacamole / bp-newapi (HTTPRoute hostnames)
//
// Operator-overridable via CATALYST_SOVEREIGN_SUBDOMAINS (comma-separated)
// for the rare case where the Sovereign exposes a custom subset.
var CanonicalSovereignSubdomains = []string{
	"console",
	"auth",
	"gitea",
	"harbor",
	"registry",
	"bao",
	"grafana",
	"hubble",
	"pdns",
	"openova-flow",
	"marketplace",
	"api",
	"guacamole",
	// newapi — public URL for the multi-tenant LLM marketplace gateway
	// (bp-newapi, bootstrap-kit slot 80). The chart renders an HTTPRoute
	// at newapi.<sov-fqdn> (platform/newapi/chart/templates/httproute.yaml,
	// host newapi.${SOVEREIGN_FQDN} per clusters/_template/bootstrap-kit/
	// 80-newapi.yaml) and Sandbox runtimes hit it via the controller-minted
	// NEWAPI_BASE_URL=https://newapi.<sov-fqdn>/v1. Without this entry the
	// parent zone returns NXDOMAIN — every Sandbox LLM call + the operator's
	// newapi.<fqdn> browser walk fail even though the Gateway listener +
	// HTTPRoute are Accepted (Refs #3263 #2737). Caught live on hw126:
	// `dig newapi.hw126.omantel.biz` → empty while every sibling resolved.
	"newapi",
	// pdns-admin — public URL for the PowerDNS-Admin web UI (bp-powerdns-admin,
	// bootstrap-kit slot 11a, host pdns-admin.${SOVEREIGN_FQDN} per
	// clusters/_template/bootstrap-kit/11a-bp-powerdns-admin.yaml). The bare
	// pdns.<fqdn> API host IS in this list; the human-facing UI host
	// pdns-admin.<fqdn> was not, so operators hit NXDOMAIN on the UI even
	// after #3227 redirected pdns.<fqdn>/ → pdns-admin.<fqdn>/ (the redirect
	// target itself never resolved). Refs #3225 #3263.
	"pdns-admin",
	// sandbox — public URL for the Sandbox product's per-Sandbox
	// pty-server StatefulSet (PR #1641 rendered the per-Sandbox
	// HTTPRoute at sandbox.<sov-fqdn>/sessions/<owner-uid>/*; without
	// this entry the parent zone returns NXDOMAIN and browsers cannot
	// reach the URL even though the Gateway listener + HTTPRoute exist).
	// Matches the cilium-gateway-cert.yaml SAN list (sandbox.<sov-fqdn>).
	"sandbox",
}

// upsertSovereignParentZoneRecords PATCHes the parent zone with A
// records that route every CanonicalSovereignSubdomain at the
// Sovereign's primary load-balancer IP.
//
// Inputs come from the Phase-0 tofu output:
//   - sovereignFQDN: e.g. "t111.omani.works"
//   - parentZone:    e.g. "omani.works"  (parent of sovereignFQDN)
//   - lbIP:          e.g. "77.42.11.95"  (primary region Hetzner LB)
//
// Idempotent: PowerDNS PATCH REPLACE rewrites the rrset on every call.
//
// Errors are wrapped with context. Caller decides whether to bail the
// provision or log + continue (today we log + continue so the Sovereign
// still reaches phase1-watching even if PowerDNS is briefly unavailable;
// the operator can re-trigger via the parent-domain refresh endpoint).
func (h *Handler) upsertSovereignParentZoneRecords(ctx context.Context, sovereignFQDN, parentZone, lbIP string) error {
	if h.powerdnsZoneClient == nil {
		h.log.Info("sovereign-dns-records: skipping (no powerdns client wired)",
			"sovereignFQDN", sovereignFQDN,
		)
		return nil
	}
	if sovereignFQDN == "" || lbIP == "" {
		return fmt.Errorf("sovereign-dns-records: sovereignFQDN and lbIP are required (got %q / %q)", sovereignFQDN, lbIP)
	}
	if parentZone == "" {
		// Best-effort default: the parent zone is the FQDN minus its
		// first label. e.g. "t111.omani.works" → "omani.works". For
		// single-label-deep Sovereigns ("acme.com") the parent IS the
		// FQDN itself — caller should pass parentZone explicitly in
		// that case.
		parts := strings.SplitN(sovereignFQDN, ".", 2)
		if len(parts) == 2 {
			parentZone = parts[1]
		} else {
			parentZone = sovereignFQDN
		}
	}

	subdomains := CanonicalSovereignSubdomains
	rrsets := make([]powerdns.RRSet, 0, len(subdomains)+1)
	for _, sub := range subdomains {
		rrsets = append(rrsets, powerdns.RRSet{
			Name:       sub + "." + sovereignFQDN + ".",
			Type:       "A",
			TTL:        60,
			ChangeType: "REPLACE",
			Records:    []powerdns.Record{{Content: lbIP, Disabled: false}},
		})
	}
	// Also stamp the bare FQDN apex (e.g. t111.omani.works. itself) for
	// dashboard "Open Sovereign Console →" deep-links and for ACME
	// clients that probe the apex first.
	rrsets = append(rrsets, powerdns.RRSet{
		Name:       sovereignFQDN + ".",
		Type:       "A",
		TTL:        60,
		ChangeType: "REPLACE",
		Records:    []powerdns.Record{{Content: lbIP, Disabled: false}},
	})

	if err := h.powerdnsZoneClient.PatchRRSets(ctx, parentZone, rrsets); err != nil {
		return fmt.Errorf("sovereign-dns-records: patch parent zone %q: %w", parentZone, err)
	}
	h.log.Info("sovereign-dns-records: parent zone PATCHed with per-Sovereign A records",
		"sovereignFQDN", sovereignFQDN,
		"parentZone", parentZone,
		"lbIP", lbIP,
		"recordCount", len(rrsets),
	)
	return nil
}

// upsertSovereignParentZoneRecordsFromResult is the deployment-flow
// wrapper. Called from the Phase-0 success branch in
// startProvisioningAsync — alongside commitPDMWithRetry — so the
// parent zone has the right A records the moment Phase-1 watch
// starts (and certainly before cert-manager runs DNS-01 against the
// Sovereign FQDNs).
//
// On a multi-domain Sovereign (#827), the loop covers EVERY parent in
// dep.Request.ParentDomains — each parent zone gets its own copy of
// the canonical subdomain records pointing at the SAME primary LB IP.
// PowerDNS treats them as separate zones so the writes are independent.
func (h *Handler) upsertSovereignParentZoneRecordsFromResult(ctx context.Context, dep *Deployment, result *provisioner.Result) {
	if dep == nil || result == nil {
		return
	}
	if result.SovereignFQDN == "" || result.LoadBalancerIP == "" {
		h.log.Warn("sovereign-dns-records: missing fqdn or lb-ip in tofu result; skipping",
			"id", dep.ID,
			"sovereignFQDN", result.SovereignFQDN,
			"loadBalancerIP", result.LoadBalancerIP,
		)
		return
	}

	dep.mu.Lock()
	parents := make([]string, 0, len(dep.Request.ParentDomains))
	for _, pd := range dep.Request.ParentDomains {
		if pd.Name != "" {
			parents = append(parents, pd.Name)
		}
	}
	dep.mu.Unlock()

	// Backstop: if the prov body didn't supply ParentDomains, derive
	// the parent zone from the FQDN's tail labels. Keeps zero-touch
	// provisioning working for the legacy single-parent shape.
	if len(parents) == 0 {
		parts := strings.SplitN(result.SovereignFQDN, ".", 2)
		if len(parts) == 2 {
			parents = []string{parts[1]}
		}
	}

	for _, parent := range parents {
		err := h.upsertSovereignParentZoneRecords(ctx, result.SovereignFQDN, parent, result.LoadBalancerIP)
		if err == nil {
			continue
		}
		// Per-instance 404 fallback: the synthesized ParentDomain may
		// equal SovereignFQDN itself (Validate's back-compat synthesis
		// stamps r.ParentDomains[0].Name = SovereignFQDN when neither
		// SovereignPoolDomain nor an explicit parentDomains slice was
		// supplied). For a SUB-zone FQDN like "t126.omani.works", the
		// authoritative PowerDNS zone is "omani.works" — so PATCH zone
		// "t126.omani.works." returns 404 and no A records ever land.
		// Retry against parent-of-FQDN before giving up. Caught on t126
		// (84c0848406dd6fdd, 2026-05-16): handover fired but D1/D2
		// stayed red because every console hostname resolved NXDOMAIN
		// despite the wildcard cert being valid + LB IP assigned.
		if strings.Contains(err.Error(), "status 404") && parent == result.SovereignFQDN {
			if i := strings.Index(result.SovereignFQDN, "."); i > 0 {
				derivedParent := result.SovereignFQDN[i+1:]
				h.log.Info("sovereign-dns-records: parent zone 404 — retrying with derived sub-zone parent",
					"id", dep.ID,
					"originalParent", parent,
					"derivedParent", derivedParent,
				)
				if err2 := h.upsertSovereignParentZoneRecords(ctx, result.SovereignFQDN, derivedParent, result.LoadBalancerIP); err2 == nil {
					continue
				} else {
					err = fmt.Errorf("original=%w, derived-fallback=%v", err, err2)
				}
			}
		}
		// Best-effort: log + continue. The Sovereign still
		// reaches phase1-watching; the operator can hit the
		// parent-domain refresh endpoint to retry later.
		h.log.Warn("sovereign-dns-records: write failed (continuing)",
			"id", dep.ID,
			"parent", parent,
			"err", err,
		)
	}
}
