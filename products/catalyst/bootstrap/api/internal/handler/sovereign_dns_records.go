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
//   marketplace / api / registry / guacamole / newapi / sandbox / mcp) an A record
//     <sub>.<sovereign-fqdn>. → <primary-lb-ipv4>
//   in the parent zone. PATCH REPLACE — idempotent re-runs are safe.

package handler

import (
	"context"
	"fmt"
	"os"
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
//   - products/openova-mcp/chart/templates/httproute.yaml (bp-openova-mcp,
//     sovereign-mode host mcp.<sov-fqdn> — #5206)
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
	// mcp — public URL for the Sovereign-mode OpenOva MCP server
	// (bp-openova-mcp, bootstrap-kit slot 13d, host mcp.${SOVEREIGN_FQDN}
	// per clusters/_template/bootstrap-kit/13d-bp-openova-mcp.yaml). The
	// chart renders an Accepted=True HTTPRoute on the shared cilium-gateway
	// (products/openova-mcp/chart/templates/httproute.yaml) but — same
	// #3263/#3225 gap class — that alone never resolves publicly: only the
	// prefixes in THIS allowlist get an A record in the parent zone. Caught
	// live on hw270 (2026-07-18): `dig mcp.hw270.omantel.biz` returned empty
	// for 3h+ while every sibling (registry/gitea/console) resolved, even
	// though the HTTPRoute was Accepted and the backend healthy — blocking
	// the entire Pillar-4 Org→MCP north star (#5206). Wildcard TLS
	// (*.<fqdn>) already covers it; no cert/SAN change needed.
	"mcp",
}

// ConsoleGatewaySubdomains — the subset of CanonicalSovereignSubdomains whose
// HTTPRoutes parent the DEDICATED console gateway (`cilium-gateway-console`,
// #4053 / #4054) rather than the shared `cilium-gateway`. Their A-records MUST
// point at the console load-balancer IP (the console ELB / dedicated console LB)
// — the shared gateway carries no listener/route for these hosts, so resolving
// them at the shared LB IP returns 404.
//
// Source of truth — this MUST stay in lockstep with the chart's console-gateway
// membership, which is the single `ingress.gateway.parentRef.name:
// cilium-gateway-console` default applied to exactly these HTTPRoutes:
//   - products/catalyst/chart/templates/httproute.yaml          (catalyst-ui → console.<fqdn>, catalyst-api → api.<fqdn>)
//   - products/catalyst/chart/templates/services/catalog/httproute.yaml (catalyst-catalog → api.<fqdn>)
//   - products/catalyst/chart/templates/org-services/marketplace-routes.yaml (marketplace → marketplace.<fqdn>)
//
// Verified live on the permanent omantel.biz Sovereign (dep 4635277cae4ffed9,
// 2026-06-22): `kubectl get httproute -A` showed catalyst-ui/catalyst-api/
// catalyst-catalog/marketplace parenting cilium-gateway-console (EIP …33) while
// every other route parented cilium-gateway (EIP …31). console/api/marketplace
// served 404 on …31 and 200 on …33 (Refs #4069). The #4054 commit message
// claimed marketplace stays on the shared gateway — the live cluster contradicts
// it because the marketplace route inherits the same parentRef default.
//
// Operator-overridable via CATALYST_CONSOLE_GATEWAY_SUBDOMAINS (comma-separated)
// for the rare topology that re-parents a different subset. Any entry here that
// is NOT also in CanonicalSovereignSubdomains is still written (so an operator
// can add a console-gateway-only host), but the canonical set already contains
// all three so no record is dropped.
var ConsoleGatewaySubdomains = []string{
	"console",
	"api",
	"marketplace",
}

// consoleGatewaySubdomainSet returns ConsoleGatewaySubdomains as a lookup set,
// honouring the CATALYST_CONSOLE_GATEWAY_SUBDOMAINS override.
func consoleGatewaySubdomainSet() map[string]struct{} {
	subs := ConsoleGatewaySubdomains
	if raw := strings.TrimSpace(os.Getenv("CATALYST_CONSOLE_GATEWAY_SUBDOMAINS")); raw != "" {
		subs = nil
		for _, tok := range strings.Split(raw, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				subs = append(subs, tok)
			}
		}
	}
	out := make(map[string]struct{}, len(subs))
	for _, s := range subs {
		out[strings.ToLower(s)] = struct{}{}
	}
	return out
}

// recordTargetIP picks the A-record target for a given canonical subdomain:
// the console LB IP for console-gateway hosts (when a dedicated console LB
// exists), otherwise the shared/primary LB IP. consoleLBIP == "" (module
// pre-#4053, or no console-gateway isolation) collapses to lbIP for every host
// — byte-identical to the pre-#4069 behaviour.
func recordTargetIP(subdomain, lbIP, consoleLBIP string, consoleSet map[string]struct{}) string {
	if consoleLBIP == "" {
		return lbIP
	}
	if _, ok := consoleSet[strings.ToLower(subdomain)]; ok {
		return consoleLBIP
	}
	return lbIP
}

// upsertSovereignParentZoneRecords PATCHes the parent zone with A
// records that route every CanonicalSovereignSubdomain at the
// Sovereign's primary load-balancer IP — except the ConsoleGatewaySubdomains,
// which point at consoleLBIP (the dedicated console LB) when one exists (#4069).
//
// Inputs come from the Phase-0 tofu output:
//   - sovereignFQDN: e.g. "t111.omani.works"
//   - parentZone:    e.g. "omani.works"  (parent of sovereignFQDN)
//   - lbIP:          e.g. "77.42.11.95"  (primary/shared region LB)
//   - consoleLBIP:   optional (variadic so existing callers compile). #4069.
//     When non-empty, the ConsoleGatewaySubdomains (console/api/marketplace —
//     the hosts whose HTTPRoutes parent the dedicated cilium-gateway-console)
//     point HERE instead of lbIP, so a fresh prov writes the console front
//     doors at the console LB. Empty/omitted → every record points at lbIP
//     (byte-identical to pre-#4069 behaviour for module-pre-#4053 Sovereigns).
//
// Idempotent: PowerDNS PATCH REPLACE rewrites the rrset on every call.
//
// Errors are wrapped with context. Caller decides whether to bail the
// provision or log + continue (today we log + continue so the Sovereign
// still reaches phase1-watching even if PowerDNS is briefly unavailable;
// the operator can re-trigger via the parent-domain refresh endpoint).
func (h *Handler) upsertSovereignParentZoneRecords(ctx context.Context, sovereignFQDN, parentZone, lbIP string, consoleLBIP ...string) error {
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

	// #4069 — resolve the console front-door IP (defaults to lbIP when the
	// console gateway is not isolated, i.e. consoleLBIP is omitted/empty).
	consoleIP := ""
	if len(consoleLBIP) > 0 {
		consoleIP = strings.TrimSpace(consoleLBIP[0])
	}
	consoleSet := consoleGatewaySubdomainSet()

	subdomains := CanonicalSovereignSubdomains
	rrsets := make([]powerdns.RRSet, 0, len(subdomains)+1)
	for _, sub := range subdomains {
		target := recordTargetIP(sub, lbIP, consoleIP, consoleSet)
		rrsets = append(rrsets, powerdns.RRSet{
			Name:       sub + "." + sovereignFQDN + ".",
			Type:       "A",
			TTL:        60,
			ChangeType: "REPLACE",
			Records:    []powerdns.Record{{Content: target, Disabled: false}},
		})
	}
	// Also stamp the bare FQDN apex (e.g. t111.omani.works. itself) for
	// dashboard "Open Sovereign Console →" deep-links and for ACME
	// clients that probe the apex first. The apex is NOT a console-gateway
	// host (it has no isolated HTTPRoute) so it stays on the shared lbIP.
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
		"consoleLBIP", consoleIP,
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
		err := h.upsertSovereignParentZoneRecords(ctx, result.SovereignFQDN, parent, result.LoadBalancerIP, result.ConsoleLoadBalancerIP)
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
				if err2 := h.upsertSovereignParentZoneRecords(ctx, result.SovereignFQDN, derivedParent, result.LoadBalancerIP, result.ConsoleLoadBalancerIP); err2 == nil {
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

// sovereignParentZoneRecordNames returns the fully-qualified, dot-terminated
// rrset names a Sovereign owns in its parent zone: one per
// CanonicalSovereignSubdomain (`<sub>.<fqdn>.`) plus the bare apex (`<fqdn>.`).
//
// This is the single source of truth for the record-SELECTION contract shared
// by the write side (upsertSovereignParentZoneRecords) and the wipe teardown
// (deleteSovereignParentZoneRecords) — keeping them symmetric so a wiped
// Sovereign can never leave behind a record the upsert created (the #4764
// dangling-`console.<fqdn>` leak). Pure + allocation-only so the selection can
// be unit-tested without a PowerDNS client. FQDN is lowercased + trimmed of a
// trailing dot (DNS names are case-insensitive; PowerDNS canonicalises on
// write) so a blank/whitespace FQDN yields NO names rather than a bare-dot `.`
// rrset that could touch the zone apex.
func sovereignParentZoneRecordNames(sovereignFQDN string) []string {
	fqdn := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(sovereignFQDN)), ".")
	if fqdn == "" {
		return nil
	}
	names := make([]string, 0, len(CanonicalSovereignSubdomains)+1)
	for _, sub := range CanonicalSovereignSubdomains {
		names = append(names, sub+"."+fqdn+".")
	}
	names = append(names, fqdn+".") // apex
	return names
}

// deleteSovereignParentZoneRecords is the wipe-side twin of
// upsertSovereignParentZoneRecords (#4732 item 7). It DELETEs the same
// rrset names the upsert wrote — every CanonicalSovereignSubdomain plus
// the bare apex — from the given parent zone, so a wiped Sovereign's
// records do not linger and route third parties' traffic (or a later
// same-name re-prov) at a released EIP. Verified live: a wiped env's
// `console.<fqdn>` still resolved a full day after the wipe.
//
// Idempotent: PowerDNS changetype=DELETE on an absent rrset is a 2xx
// no-op. Best-effort by design — the caller logs and continues so a DNS
// hiccup never blocks the wipe's cloud purge or record cleanup.
//
// NOTE: this targets exactly the `parentZone` it is given. When the wipe is
// handed a `parentZone` equal to the FQDN itself (the BYO back-compat synthesis
// that stamps ParentDomains[0].Name = SovereignFQDN), that sub-FQDN is NOT an
// authoritative PowerDNS zone and the PATCH 404s — the records actually live in
// the FQDN's tail zone. deleteSovereignParentZoneRecordsWithFallback mirrors the
// write-side 404 retry so callers get the tail-zone cleanup for free (#4764).
func (h *Handler) deleteSovereignParentZoneRecords(ctx context.Context, sovereignFQDN, parentZone string) error {
	if h.powerdnsZoneClient == nil {
		h.log.Info("sovereign-dns-records: delete skipped (no powerdns client wired)",
			"sovereignFQDN", sovereignFQDN,
		)
		return nil
	}
	if sovereignFQDN == "" {
		return fmt.Errorf("sovereign-dns-records: sovereignFQDN required for delete")
	}
	if parentZone == "" {
		parts := strings.SplitN(sovereignFQDN, ".", 2)
		if len(parts) == 2 {
			parentZone = parts[1]
		} else {
			parentZone = sovereignFQDN
		}
	}
	names := sovereignParentZoneRecordNames(sovereignFQDN)
	rrsets := make([]powerdns.RRSet, 0, len(names))
	for _, name := range names {
		rrsets = append(rrsets, powerdns.RRSet{
			Name:       name,
			Type:       "A",
			ChangeType: "DELETE",
		})
	}
	if err := h.powerdnsZoneClient.PatchRRSets(ctx, parentZone, rrsets); err != nil {
		return fmt.Errorf("sovereign-dns-records: delete from parent zone %q: %w", parentZone, err)
	}
	h.log.Info("sovereign-dns-records: parent zone records deleted for wiped Sovereign",
		"sovereignFQDN", sovereignFQDN,
		"parentZone", parentZone,
		"recordCount", len(rrsets),
	)
	return nil
}

// deleteSovereignParentZoneRecordsWithFallback is the wipe entry point. It
// deletes the Sovereign's parent-zone records and — crucially — mirrors the
// write-side 404 retry in upsertSovereignParentZoneRecordsFromResult so the
// teardown is symmetric with the write.
//
// Root cause of #4764: Request.Validate() synthesises ParentDomains[0].Name =
// SovereignFQDN for BYO Sovereigns (no SovereignPoolDomain). The wipe hands that
// verbatim value as `parentZone`, but for a sub-FQDN like `t126.omani.works` the
// authoritative PowerDNS zone is the tail `omani.works` — the write side already
// learned this (the sub-FQDN PATCH 404s) and retried against the derived tail,
// so the A records — including `console.<fqdn>` → the console EIP — landed in
// `omani.works`. The wipe did NOT retry, so it PATCH-DELETEd the non-existent
// sub-FQDN zone (404), and every record it should have removed survived. A wiped
// env's `console.<fqdn>` then kept resolving a released EIP.
//
// The fallback fires ONLY when the first attempt 404s AND the parent equals the
// FQDN itself — the exact condition the write side uses. The retry target is
// strictly the FQDN's own tail label (this deployment's registered parent
// domain), so it never touches a sibling Sovereign's records or a shared /
// mothership zone. Idempotent: DELETE on absent rrsets is a PowerDNS no-op.
func (h *Handler) deleteSovereignParentZoneRecordsWithFallback(ctx context.Context, sovereignFQDN, parentZone string) error {
	err := h.deleteSovereignParentZoneRecords(ctx, sovereignFQDN, parentZone)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "status 404") && parentZone == sovereignFQDN {
		if i := strings.IndexByte(sovereignFQDN, '.'); i > 0 {
			derivedParent := sovereignFQDN[i+1:]
			if derivedParent != parentZone {
				h.log.Info("sovereign-dns-records: parent zone 404 on delete — retrying with derived tail parent",
					"sovereignFQDN", sovereignFQDN,
					"originalParent", parentZone,
					"derivedParent", derivedParent,
				)
				if err2 := h.deleteSovereignParentZoneRecords(ctx, sovereignFQDN, derivedParent); err2 == nil {
					return nil
				} else {
					return fmt.Errorf("original=%w, derived-fallback=%v", err, err2)
				}
			}
		}
	}
	return err
}
