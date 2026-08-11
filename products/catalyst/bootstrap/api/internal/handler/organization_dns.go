// Package handler — organization_dns.go: DNS provisioner for the Organization
// tenant pipeline (issue #804).
//
// Two flows:
//
//   - Free-subdomain mode (default): the orchestrator calls PowerDNS
//     directly via the otech's in-cluster PowerDNS service to add an
//     A record for `console.<subdomain>.<otech-fqdn>` (and optional
//     wildcard CNAME for sister hosts). Idempotent — PowerDNS PATCH
//     is naturally re-runnable.
//
//   - BYO mode: the orchestrator does NO writes; instead it resolves
//     `console.<byo_domain>` and confirms the CNAME target is the
//     otech ingress hostname. A failed lookup or mismatched target is
//     surfaced as a structured error so the wizard UI can render
//     "your CNAME doesn't point here yet" without a chat-with-support
//     loop.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the PowerDNS endpoint + API
// key are env-configured (CATALYST_POWERDNS_API_URL,
// CATALYST_POWERDNS_API_KEY). A nil writer (env unset) returns a
// graceful error rather than a panic; the orchestrator surfaces that
// as a transient `dns:transient:powerdns not wired` until the
// catalyst-api is restarted with the env populated.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// PowerDNSWriter is a minimal PowerDNS API client matching the subset
// the Organization pipeline needs: PATCH zones/<zone> with one or more
// RRsets. Kept narrow on purpose — the orchestrator never lists,
// creates, or deletes whole zones (the otech zone already exists).
type PowerDNSWriter struct {
	BaseURL    string
	ServerID   string // PowerDNS server identifier; "localhost" by default
	APIKey     string
	HTTPClient *http.Client
}

// NewPowerDNSWriter returns a configured client. Empty baseURL or
// apiKey returns nil so the orchestrator can fall back to the no-op
// writer below.
func NewPowerDNSWriter(baseURL, apiKey string) *PowerDNSWriter {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return &PowerDNSWriter{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		ServerID:   "localhost",
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// pdnsRRSet is the API shape the orchestrator emits.
type pdnsRRSet struct {
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	TTL        int          `json:"ttl"`
	ChangeType string       `json:"changetype"`
	Records    []pdnsRecord `json:"records"`
}

type pdnsRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// PatchRRSets PATCHes a list of RRsets. PowerDNS REPLACE is the only
// changetype the orchestrator uses — idempotent, re-runnable, and the
// canonical "create or update" shape per the PowerDNS HTTP API.
func (w *PowerDNSWriter) PatchRRSets(ctx context.Context, zone string, rrsets []pdnsRRSet) error {
	body := struct {
		RRSets []pdnsRRSet `json:"rrsets"`
	}{RRSets: rrsets}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	srv := w.ServerID
	if srv == "" {
		srv = "localhost"
	}
	url := fmt.Sprintf("%s/api/v1/servers/%s/zones/%s",
		w.BaseURL, srv, zoneCanonical(zone))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", w.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTPClient.Do(req)
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

// zoneCanonical ensures the zone name ends with a single trailing
// dot (PowerDNS API convention).
func zoneCanonical(zone string) string {
	zone = strings.TrimRight(zone, ".")
	return zone + "."
}

// DefaultOrganizationDNSProvisioner is the production
// OrganizationDNSProvisioner. Wraps a PowerDNSWriter for free-subdomain
// writes and net.LookupCNAME for BYO validation.
type DefaultOrganizationDNSProvisioner struct {
	// Writer — the LOCAL PowerDNS REST client (the Sovereign's own
	// in-cluster powerdns.powerdns.svc), authoritative ONLY for the
	// Sovereign's own FQDN (e.g. omantel.biz). Used for free-subdomain
	// writes when the parent zone is the Sovereign's own domain
	// (single-domain Sovereign / Catalyst-Zero back-compat) and never
	// for the shared omani.* pool zones — those live on a separate
	// authoritative server (see PoolWriter).
	Writer *PowerDNSWriter
	// PoolWriter — the CENTRAL PowerDNS REST client (pdns.openova.io),
	// authoritative for the shared subdomain-pool zones (omani.works,
	// omani.homes, omani.rest, omani.trade). On a production Sovereign
	// the marketplace runs on the Sovereign FQDN while Orgs provision on
	// SEPARATE pool domains; their `console.<slug>.<pool>` A-record MUST
	// be written to the central server, not the Sovereign-local one
	// (which has no pool zone → PATCH 404). When set, ProvisionFreeSubdomain
	// prefers this writer; nil falls back to Writer (the single-PowerDNS
	// Sovereign where the pool IS the local zone). #4218.
	PoolWriter *PowerDNSWriter
	// Resolver — net resolver used for BYO CNAME lookups. Defaults to
	// net.DefaultResolver; tests inject a stub that returns canned
	// responses without hitting the live DNS root.
	Resolver Resolver
}

// Resolver is the narrow interface the BYO validator needs.
// net.DefaultResolver satisfies it.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
}

// ProvisionFreeSubdomain implements OrganizationDNSProvisioner. Writes
// A record for `console.<subdomain>.<parentZone>` plus the standard
// app-host A records (wordpress, openclaw, mail, keycloak) under the
// chosen parent zone so the per-tenant overlay's ingress hostnames
// resolve as soon as Flux finishes reconciling. Per epic #825 the
// parentZone is operator-supplied (one of the Sovereign's
// role:org-pool entries) — never inferred from a hardcoded OTECHFQDN.
//
// #4732(3): the Org console rides the DEDICATED console gateway/ELB
// (#4053/#4718) — the same door `console.<sovereign-fqdn>` serves — so its
// record must carry consoleIPv4. Writing the console record with the
// shared-gateway IP is exactly the nstar failure: the shared gateway has no
// console listener for the Org zone, so the browser got the pool
// `*.<parent>` cert + 404.
//
// UAT rows 90 + 234 — EVERY host in the Org's pool subtree rides that same
// door, not just `console`. This block previously read "App hosts
// (wordpress/openclaw/mail/keycloak + the per-Org wildcard) ride the SHARED
// gateway (ingressIPv4)", and that premise is false on this platform:
//
//   - The ONLY writer of a per-Org wildcard listener is
//     core/controllers/organization/internal/controller/tenant_console_tls.go:307,
//     which builds WildcardHost = "*.<slug>.<parentDomain>" and appends the
//     `console-https-<slug>` / `console-http-<slug>` pair to
//     consoleGatewayName() — `cilium-gateway-console`. Nothing appends a
//     per-Org listener to the shared gateway, and the matching wildcard
//     Certificate is mounted only there. An app host resolving to the shared
//     gateway therefore arrives with an SNI that gateway holds no listener
//     for, and the connection is RESET at the TLS handshake — before any HTTP
//     status, which is why it presents as a dead site rather than a 404.
//
//   - The org-controller already writes the SAME `*` record at
//     core/controllers/organization/internal/controller/tenant_dns.go:182-192
//     and targets consoleIP. Two writers emitting one RRset with different
//     targets is an inconsistency on its face. Because that reconciler
//     re-asserts `*` but never touches the four app prefixes, the app records
//     stayed orphaned at the shared IP — and being EXPLICIT records they then
//     shadow the corrected wildcard, so the fault survived reconciliation.
//
// Measured read-only on hw293 Org `g7freea`: mail/wordpress resolved to the
// shared EIP and failed with curl exit 35 (TLS reset), while forcing the same
// SNI onto the console EIP returned 503 behind a publicly-trusted cert —
// listener, cert and route all present on the console gateway. 503 (route
// matched, no upstream) versus exit 35 (no listener) is what separates
// "wrong front door" from "broken app".
//
// consoleIPv4 == "" falls back to ingressIPv4 (single-gateway Sovereigns and
// older callers keep their prior behaviour).
func (p DefaultOrganizationDNSProvisioner) ProvisionFreeSubdomain(ctx context.Context, subdomain, parentZone, ingressIPv4, consoleIPv4 string) error {
	// #4218: pool-domain console A-records (omani.*) live on the CENTRAL
	// authoritative PowerDNS (pdns.openova.io), not the Sovereign-local
	// one. Prefer PoolWriter when wired; fall back to the local Writer for
	// the single-PowerDNS Sovereign where the pool IS the local zone.
	writer := p.Writer
	if p.PoolWriter != nil {
		writer = p.PoolWriter
	}
	if writer == nil {
		return errors.New("powerdns writer not wired (CATALYST_POWERDNS_API_URL / CATALYST_POWERDNS_API_KEY, or CATALYST_POOL_POWERDNS_API_URL / CATALYST_POOL_POWERDNS_API_KEY)")
	}
	if strings.TrimSpace(ingressIPv4) == "" {
		return errors.New("otech ingress IPv4 unconfigured")
	}
	if strings.TrimSpace(parentZone) == "" {
		return errors.New("parent zone unconfigured (multi-domain Sovereign requires a org-pool parent_domain)")
	}
	zone := parentZone
	rrsets := []pdnsRRSet{}
	// Per-Org host A records. The leading "*" wildcard is REQUIRED, not a
	// nicety: parentZone is a SHARED pool domain (epic #825), and a prior
	// environment that used the same pool commonly leaves a broad apex
	// wildcard (e.g. `*.<parentZone> A <old-ingress-ip>`) behind after a
	// wipe. Without an explicit `*.<subdomain>.<parentZone>` record, every
	// Org host NOT in the fixed prefix list below (and any app subdomain
	// the per-tenant overlay later adds) falls through to that stale apex
	// wildcard and resolves to a DEAD prior-env IP — the #4075 failure
	// ("console unreachable, served 49.12.16.160 from a wiped env"). The
	// explicit per-Org wildcard shadows the apex wildcard for this Org's
	// entire subtree and pins it to THIS Sovereign's current ingress IP.
	//
	// ChangeType=REPLACE makes every write an unconditional upsert, so a
	// stale same-name record (if one ever existed) is overwritten rather
	// than skipped.
	consoleIP := strings.TrimSpace(consoleIPv4)
	if consoleIP == "" {
		consoleIP = ingressIPv4
	}
	for _, prefix := range theFreeSubdomainPrefixes {
		fqdn := fmt.Sprintf("%s.%s.%s.", prefix, subdomain, parentZone)
		// Every prefix in this Org's subtree — the wildcard and the app hosts
		// as much as `console` — is served by the console gateway, because
		// that is the only gateway carrying a `*.<slug>.<parent>` listener and
		// cert (see the rationale above). consoleIP already collapses to
		// ingressIPv4 on a single-gateway Sovereign.
		content := consoleIP
		rrsets = append(rrsets, pdnsRRSet{
			Name:       fqdn,
			Type:       "A",
			TTL:        300,
			ChangeType: "REPLACE",
			Records: []pdnsRecord{
				{Content: content, Disabled: false},
			},
		})
	}
	return writer.PatchRRSets(ctx, zone, rrsets)
}

// theFreeSubdomainPrefixes is the per-Org host prefix set ProvisionFreeSubdomain
// writes and DeprovisionFreeSubdomain deletes — kept in ONE place so the write
// and delete paths can never drift (the #4459 leak class: write N, delete <N,
// the surplus records survive the Org and shadow a re-prov's wildcard with a
// dead IP).
var theFreeSubdomainPrefixes = []string{"*", "console", "wordpress", "openclaw", "mail", "keycloak"}

// lookupHostFn is the seam tests stub to avoid live DNS. Production uses
// net.DefaultResolver.
var lookupHostFn = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// resolveSovereignConsoleIPv4 resolves the Sovereign's own console
// A-record (`console.<sovereignFQDN>`) to discover the DEDICATED console
// gateway/ELB EIP (#4732 item 3). That record is written at prov time on
// every Sovereign and always targets the console front door, so it is the
// zero-config source of truth for where per-Org `console.<slug>.<pool>`
// records must point. Returns "" on any failure (empty FQDN, lookup error,
// no IPv4) — the caller then falls back to the shared ingress IP, which is
// the pre-#4732 behaviour.
func resolveSovereignConsoleIPv4(ctx context.Context, sovereignFQDN string) string {
	fqdn := strings.Trim(strings.TrimSpace(sovereignFQDN), ".")
	if fqdn == "" {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := lookupHostFn(lctx, "console."+fqdn)
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

// DeprovisionFreeSubdomain implements OrganizationDNSProvisioner. DELETEs the
// per-Org pool A-records ProvisionFreeSubdomain wrote (#4459 — a stale record
// surviving an Org delete poisons a later same-slug re-prov with a dead
// console IP → Console 000). No ingress IP is needed: PowerDNS
// changetype=DELETE removes the whole rrset by name+type. Idempotent (deleting
// an absent rrset is a 2xx no-op), so it is safe on an Org that never
// provisioned DNS.
func (p DefaultOrganizationDNSProvisioner) DeprovisionFreeSubdomain(ctx context.Context, subdomain, parentZone string) error {
	writer := p.Writer
	if p.PoolWriter != nil {
		writer = p.PoolWriter
	}
	if writer == nil {
		return errors.New("powerdns writer not wired")
	}
	if strings.TrimSpace(parentZone) == "" {
		return errors.New("parent zone unconfigured")
	}
	rrsets := make([]pdnsRRSet, 0, len(theFreeSubdomainPrefixes))
	for _, prefix := range theFreeSubdomainPrefixes {
		rrsets = append(rrsets, pdnsRRSet{
			Name:       fmt.Sprintf("%s.%s.%s.", prefix, subdomain, parentZone),
			Type:       "A",
			ChangeType: "DELETE",
		})
	}
	return writer.PatchRRSets(ctx, parentZone, rrsets)
}

// ValidateBYOCNAME implements OrganizationDNSProvisioner. Resolves
// `console.<byo_domain>` and confirms its CNAME target ends with one
// of the operator-supplied accepted targets (or, with no targets, the
// legacy single-target path). The multi-target shape backs epic #825
// (multi-domain Sovereign) where any parent in the role:org-pool list
// is a valid CNAME target.
func (p DefaultOrganizationDNSProvisioner) ValidateBYOCNAME(ctx context.Context, byoDomain, legacyTarget string, acceptedTargets ...string) error {
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	host := "console." + strings.Trim(byoDomain, ".")
	target, err := resolver.LookupCNAME(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve CNAME for %s: %w", host, err)
	}
	target = strings.Trim(strings.ToLower(target), ".")
	// Build the candidate list: the supplied accepted targets (multi-
	// domain pool) plus the legacy single target as a fallback.
	candidates := make([]string, 0, len(acceptedTargets)+1)
	for _, c := range acceptedTargets {
		c = strings.Trim(strings.ToLower(c), ".")
		if c != "" {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		legacy := strings.Trim(strings.ToLower(legacyTarget), ".")
		if legacy == "" {
			return errors.New("no accepted CNAME targets supplied")
		}
		candidates = append(candidates, legacy)
	}
	for _, expected := range candidates {
		if strings.HasSuffix(target, expected) {
			return nil
		}
	}
	return fmt.Errorf("%w (got %q, expected suffix from %v)", errBYOCNAMEMismatch, target, candidates)
}

// NoopOrganizationDNSProvisioner satisfies OrganizationDNSProvisioner with
// success-on-call no-ops. Used in CI / test environments without
// PowerDNS or a public DNS resolver. Production main.go wires this
// only when both env knobs (CATALYST_POWERDNS_API_URL,
// CATALYST_POWERDNS_API_KEY) are absent — that's an explicit signal
// from the operator that they're not running the DNS step here.
type NoopOrganizationDNSProvisioner struct{}

// ProvisionFreeSubdomain is a no-op.
func (NoopOrganizationDNSProvisioner) ProvisionFreeSubdomain(_ context.Context, _, _, _, _ string) error {
	return nil
}

// DeprovisionFreeSubdomain is a no-op.
func (NoopOrganizationDNSProvisioner) DeprovisionFreeSubdomain(_ context.Context, _, _ string) error {
	return nil
}

// ValidateBYOCNAME is a no-op (returns nil — accepts any domain).
func (NoopOrganizationDNSProvisioner) ValidateBYOCNAME(_ context.Context, _, _ string, _ ...string) error {
	return nil
}
