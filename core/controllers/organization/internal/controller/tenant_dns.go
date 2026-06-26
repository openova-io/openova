// tenant_dns.go — per-Organization pool-DNS A-record reconciler (issue #4236).
//
// THE GAP this file closes (the #4179 final layer):
//
// A fresh marketplace signup walks the funnel
//   marketplace → tenant-service POST /api/tenant/orgs → tenant.created event
//   → provisioning-service createOrganizationCR → Organization CR (orgs.openova.io/v1)
// and the org-controller reconciles that CR into the vCluster + Keycloak group
// + Gitea org + per-pool console TLS (tenant_console_tls.go) + per-Org console
// HTTPRoute (tenant_route.go). All of that landed. But NOTHING on the funnel
// path ever wrote the central-PowerDNS A-record for the customer's console host
// `console.<slug>.<parentDomain>`, so the (correct, #4184-derived) redirect host
// stayed DNS_PROBE_FINISHED_NXDOMAIN — the customer never reached their console.
//
// #4222 wired a pool-DNS writer (DefaultOrganizationDNSProvisioner.PoolWriter)
// but ONLY into the catalyst-api BSS pipeline (organization_provisioning.go),
// a door the marketplace funnel never runs. The org-controller, by contrast,
// reconciles EVERY Organization CR regardless of which door minted it (BSS funnel
// via org_tenant_org_cr.go OR marketplace funnel via provisioning-service), so
// writing the A-record HERE covers both doors with one change — the single
// source of truth the #4236 ticket calls the "reconciler-correct home".
//
// WHAT IT WRITES (mirrors the catalyst-api PoolWriter shape #4218/#4222 exactly):
//   - `console.<slug>.<parentDomain>` A → console-ELB IP  (the customer's console)
//   - `*.<slug>.<parentDomain>`       A → console-ELB IP  (so every per-Org host
//     under the subtree — agenity.<slug>.<parent>, etc — resolves and is not
//     shadowed by a stale apex `*.<parentDomain>` wildcard from a prior env,
//     the #4075 failure mode)
// Both as PATCH changetype=REPLACE — an unconditional idempotent upsert, so a
// re-reconcile is a no-op and a stale same-name record is overwritten.
//
// TARGET ZONE + AUTH: the omani.* subdomain pool is authoritative on the CENTRAL
// PowerDNS at https://pdns.openova.io (NOT the Sovereign-local powerdns, which
// has no pool zone → PATCH 404). The central key already lives on every Sovereign
// as `cert-manager/powerdns-api-credentials` and #4218 reflects it into
// catalyst-system as `pool-powerdns-api-credentials`. The org-controller runs in
// catalyst-system too, so it reads the SAME bridged secret — no new bridge.
//
// TARGET IP: the console host must resolve to the dedicated console-ELB EIP
// (#4053 blast-radius isolation; e.g. 212.72.24.33), NOT the shared/primary LB.
// That value lives only in the catalyst-api deployment record (tofu output
// console_load_balancer_ip), so it is threaded to the controller via env
// (CATALYST_TENANT_CONSOLE_LB_IPV4, sourced from the sovereign-fqdn ConfigMap
// `consoleLBIP` key the chart now renders from global.sovereignConsoleLBIP).
// When that env is empty the reconciler falls back to the primary LB env
// (CATALYST_OTECH_INGRESS_IPV4) so a single-LB / pre-#4053 Sovereign still
// writes a resolving record. With NEITHER configured the step is a logged no-op
// (the apex wildcard handles legacy single-domain Sovereigns) — never a hard
// failure that would wedge the Org reconcile.
//
// FAILURE MODE: best-effort + transient. A PowerDNS hiccup is logged and the
// caller requeues (the cert/route/vCluster steps already converged); it never
// fails the whole Org reconcile. Per docs/INVIOLABLE-PRINCIPLES.md #4 every
// operationally-meaningful value (pdns URL/key, console IP) flows through the
// Reconciler's env-configured fields — no hardcoded endpoint or IP.

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// poolDNSTTL is the TTL stamped on the per-Org pool A-records. 300s matches the
// catalyst-api PoolWriter (organization_dns.go) so a re-prov of the same slug
// re-resolves quickly.
const poolDNSTTL = 300

// poolDNSRRSet / poolDNSRecord are the PowerDNS REST API shapes the reconciler
// PATCHes. Kept narrow on purpose — the controller only ever REPLACEs the two
// per-Org rrsets, never lists/creates/deletes whole zones (the pool zone already
// exists on the central server). Mirrors catalyst-api's pdnsRRSet/pdnsRecord.
type poolDNSRRSet struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	TTL        int             `json:"ttl"`
	ChangeType string          `json:"changetype"`
	Records    []poolDNSRecord `json:"records"`
}

type poolDNSRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// reconcileTenantDNS writes (or upserts) the per-Org pool A-records for
// `console.<slug>.<parentDomain>` + `*.<slug>.<parentDomain>` to the central
// PowerDNS, so the customer's funnel-derived console host resolves to the
// console-ELB EIP. Returns (changed, err). No-op (false, nil) when the Org has
// no pool parentDomain (legacy single-domain Org → apex wildcard covers it) or
// when the writer is unconfigured (no pdns URL/key, or no console IP) — those
// degrade to a logged skip, never a hard error.
//
// Idempotent: PowerDNS PATCH changetype=REPLACE rewrites the rrset on every
// call, so a steady-state re-reconcile produces an identical record (no churn,
// no error on an existing rrset — the #4236 idempotency requirement).
func (r *Reconciler) reconcileTenantDNS(ctx context.Context, org *orgapi.Organization) (bool, error) {
	tp := org.Spec.TenantPublic
	parentDomain := strings.TrimSpace(tp.ParentDomain)
	if parentDomain == "" {
		// Feature disabled — Org has no pool-parent public hostname. The
		// Sovereign-wide `*.<sovFQDN>` apex wildcard covers single-domain Orgs.
		return false, nil
	}

	subdomain := strings.TrimSpace(tp.Subdomain)
	if subdomain == "" {
		subdomain = org.Spec.Slug
	}

	baseURL := strings.TrimSpace(r.PoolPowerDNSURL)
	apiKey := strings.TrimSpace(r.PoolPowerDNSAPIKey)
	if baseURL == "" || apiKey == "" {
		// No central-pdns writer wired (CATALYST_POOL_POWERDNS_API_URL/_KEY
		// unset, or the reflector hasn't filled the bridged secret yet). Skip
		// loud so an operator sees the pool record won't be written; the
		// next reconcile retries once the env/secret lands. NOT an error —
		// matching the catalyst-api no-op DNS provisioner degrade path.
		r.Log.Info("tenant-dns: skipped — central pool PowerDNS not wired",
			"organization", org.Name,
			"have_url", baseURL != "", "have_key", apiKey != "")
		return false, nil
	}

	consoleIP := r.tenantConsoleLBIPv4()
	if consoleIP == "" {
		// No console-ELB IP available (CATALYST_TENANT_CONSOLE_LB_IPV4 +
		// CATALYST_OTECH_INGRESS_IPV4 both empty). Writing an A-record with no
		// content would 422; skip loud and retry on the next pass once the
		// chart/overlay stamps the IP. NOT a hard failure.
		r.Log.Info("tenant-dns: skipped — no console-ELB IPv4 configured (CATALYST_TENANT_CONSOLE_LB_IPV4 / CATALYST_OTECH_INGRESS_IPV4 both empty)",
			"organization", org.Name, "parent_domain", parentDomain)
		return false, nil
	}

	// Build the two per-Org rrsets. The `*` wildcard is REQUIRED, not a nicety
	// (see organization_dns.go #4075 rationale): without an explicit
	// `*.<subdomain>.<parentZone>` record, every per-Org host not in a fixed
	// prefix list falls through to any stale apex `*.<parentZone>` wildcard left
	// by a prior env and resolves to a DEAD IP. REPLACE shadows that apex
	// wildcard for this Org's entire subtree and pins it to THIS Sovereign's
	// current console-ELB IP.
	rrsets := make([]poolDNSRRSet, 0, 2)
	for _, prefix := range []string{"console", "*"} {
		fqdn := fmt.Sprintf("%s.%s.%s.", prefix, subdomain, parentDomain)
		rrsets = append(rrsets, poolDNSRRSet{
			Name:       fqdn,
			Type:       "A",
			TTL:        poolDNSTTL,
			ChangeType: "REPLACE",
			Records:    []poolDNSRecord{{Content: consoleIP, Disabled: false}},
		})
	}

	if err := r.patchPoolZone(ctx, baseURL, apiKey, parentDomain, rrsets); err != nil {
		return false, fmt.Errorf("patch pool zone %q for %q: %w", parentDomain, subdomain, err)
	}
	r.Log.Info("tenant-dns: wrote per-Org pool A-records",
		"organization", org.Name,
		"console_host", fmt.Sprintf("console.%s.%s", subdomain, parentDomain),
		"target", consoleIP,
		"zone", parentDomain)
	return true, nil
}

// teardownTenantDNS removes the per-Org pool A-records the up-path wrote to
// the central PowerDNS (`console.<slug>.<parentDomain>` +
// `*.<slug>.<parentDomain>`) when the Organization is deleted — otherwise the
// stale records survive the Org and a later re-prov of the same slug points
// at a DEAD console-ELB IP (#4459). Uses PATCH changetype=DELETE, the
// canonical PowerDNS rrset removal (it does NOT need the record content, so
// no console-IP env is required for the delete). Returns (changed, err). No-op
// (false, nil) when the Org had no pool parentDomain or the central-pdns
// writer is unwired. Idempotent: deleting an already-absent rrset is a 2xx
// no-op on PowerDNS.
func (r *Reconciler) teardownTenantDNS(ctx context.Context, org *orgapi.Organization) (bool, error) {
	tp := org.Spec.TenantPublic
	parentDomain := strings.TrimSpace(tp.ParentDomain)
	if parentDomain == "" {
		return false, nil
	}
	subdomain := strings.TrimSpace(tp.Subdomain)
	if subdomain == "" {
		subdomain = org.Spec.Slug
	}
	baseURL := strings.TrimSpace(r.PoolPowerDNSURL)
	apiKey := strings.TrimSpace(r.PoolPowerDNSAPIKey)
	if baseURL == "" || apiKey == "" {
		// No central-pdns writer wired — nothing this controller can delete.
		r.Log.Info("tenant-dns teardown: skipped — central pool PowerDNS not wired",
			"organization", org.Name)
		return false, nil
	}

	rrsets := make([]poolDNSRRSet, 0, 2)
	for _, prefix := range []string{"console", "*"} {
		fqdn := fmt.Sprintf("%s.%s.%s.", prefix, subdomain, parentDomain)
		rrsets = append(rrsets, poolDNSRRSet{
			Name:       fqdn,
			Type:       "A",
			TTL:        poolDNSTTL,
			ChangeType: "DELETE",
		})
	}
	if err := r.patchPoolZone(ctx, baseURL, apiKey, parentDomain, rrsets); err != nil {
		return false, fmt.Errorf("delete pool zone rrsets %q for %q: %w", parentDomain, subdomain, err)
	}
	r.Log.Info("tenant-dns teardown: deleted per-Org pool A-records",
		"organization", org.Name,
		"console_host", fmt.Sprintf("console.%s.%s", subdomain, parentDomain),
		"zone", parentDomain)
	return true, nil
}

// tenantConsoleLBIPv4 resolves the A-record target IP: the dedicated console-ELB
// EIP (CATALYST_TENANT_CONSOLE_LB_IPV4) when set, else the primary/shared LB
// (CATALYST_OTECH_INGRESS_IPV4) for single-LB / pre-#4053 Sovereigns. Empty when
// neither is configured (the caller then skips the write).
func (r *Reconciler) tenantConsoleLBIPv4() string {
	if v := strings.TrimSpace(r.TenantConsoleLBIPv4); v != "" {
		return v
	}
	return strings.TrimSpace(r.TenantPrimaryLBIPv4)
}

// patchPoolZone PATCHes the central PowerDNS pool zone with the supplied rrsets.
// changetype=REPLACE is the canonical idempotent "create or update" — the same
// shape catalyst-api's PowerDNSWriter.PatchRRSets uses. A non-2xx status is an
// error (so the caller requeues); a 2xx is success.
func (r *Reconciler) patchPoolZone(ctx context.Context, baseURL, apiKey, zone string, rrsets []poolDNSRRSet) error {
	body := struct {
		RRSets []poolDNSRRSet `json:"rrsets"`
	}{RRSets: rrsets}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal rrsets: %w", err)
	}

	// PowerDNS API: PATCH /api/v1/servers/<server>/zones/<zone.>. ServerID is
	// "localhost" by convention (matches catalyst-api's PowerDNSWriter default).
	url := fmt.Sprintf("%s/api/v1/servers/localhost/zones/%s",
		strings.TrimRight(baseURL, "/"), poolZoneCanonical(zone))

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := r.PoolPowerDNSHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("powerdns PATCH: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("powerdns PATCH HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// poolZoneCanonical ensures the zone name ends with exactly one trailing dot
// (PowerDNS API convention). Mirrors catalyst-api's zoneCanonical.
func poolZoneCanonical(zone string) string {
	return strings.TrimRight(zone, ".") + "."
}
