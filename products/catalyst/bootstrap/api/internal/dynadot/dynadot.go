// Package dynadot — DNS API client for omani.works (and other OpenOva pool
// domains registered in the same Dynadot account).
//
// The Dynadot API is account-scoped: a single API key + secret pair can
// manage all domains owned by that account. The K8s secret
// `dynadot-api-credentials` in the `openova-system` namespace stores the
// credentials and the managed-domain list. Per docs/PROVISIONING-PLAN.md §3
// the secret's `domains` field is a comma-separated list — this client
// takes a domain per call so the same client instance handles openova.io,
// omani.works, and any future pool domain (e.g. acme.io) without code
// changes; only the secret's `domains` value needs to grow.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode"): the managed
// domain list is read at runtime from DYNADOT_MANAGED_DOMAINS env var
// (mounted from the secret's `domains` key), with the legacy single-value
// `domain` key honoured as a fallback for backward compatibility. Adding a
// third pool domain is purely a secret update — no code change.
//
// See `~/.claude/.../memory/feedback_dynadot_dns.md`: NEVER run exploratory
// set_dns2 calls — each one wipes all records. We always use
// `add_dns_to_current_setting=yes` to append rather than replace.
package dynadot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Client wraps the Dynadot REST API. Construct once and reuse.
type Client struct {
	APIKey    string
	APISecret string
	HTTP      *http.Client
}

// New returns a Dynadot client with sensible defaults. The api key/secret
// are typically read from the dynadot-api-credentials K8s secret by the
// caller and passed in here.
func New(apiKey, apiSecret string) *Client {
	return &Client{
		APIKey:    apiKey,
		APISecret: apiSecret,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Record is a single DNS record we want Dynadot to publish.
type Record struct {
	// Subdomain — leave empty for apex. e.g. "console", "*", "@".
	Subdomain string
	// Type — A, AAAA, CNAME, TXT, MX, etc.
	Type string
	// Value — depends on Type. For A: IPv4 string; for CNAME: target FQDN.
	Value string
	// TTL — seconds. Dynadot supports 60, 300, 1800, 3600, 7200, 14400, 28800,
	// 43200, 86400. Defaults to 300 if zero.
	TTL int
}

// AddRecord appends a single record to the domain's existing DNS configuration
// using add_dns_to_current_setting=yes. This is idempotent across re-runs as
// long as the record value is identical (Dynadot dedupes on (subdomain, type)).
func (c *Client) AddRecord(ctx context.Context, domain string, rec Record) error {
	if rec.TTL == 0 {
		rec.TTL = 300
	}

	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "set_dns2")
	params.Set("domain", domain)
	params.Set("add_dns_to_current_setting", "yes")

	if rec.Subdomain == "" || rec.Subdomain == "@" {
		params.Set("main_record_type0", rec.Type)
		params.Set("main_record0", rec.Value)
		params.Set("main_recordx0", fmt.Sprintf("%d", rec.TTL))
	} else {
		params.Set("subdomain0", rec.Subdomain)
		params.Set("sub_record_type0", rec.Type)
		params.Set("sub_record0", rec.Value)
		params.Set("sub_recordx0", fmt.Sprintf("%d", rec.TTL))
	}

	endpoint := "https://api.dynadot.com/api3.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build dynadot request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("dynadot api: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dynadot api status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		SetDNS2Response struct {
			ResponseHeader struct {
				ResponseCode string `json:"ResponseCode"`
				Status       string `json:"Status"`
				Error        string `json:"Error"`
			} `json:"ResponseHeader"`
		} `json:"SetDns2Response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		// Dynadot sometimes returns plaintext for errors; surface raw body.
		return fmt.Errorf("parse dynadot response: %w (body=%s)", err, truncate(string(body), 256))
	}
	hdr := result.SetDNS2Response.ResponseHeader
	if !strings.EqualFold(hdr.Status, "success") && !strings.EqualFold(hdr.ResponseCode, "0") {
		return fmt.Errorf("dynadot api error: code=%s status=%s err=%s", hdr.ResponseCode, hdr.Status, hdr.Error)
	}
	return nil
}

// AddSovereignRecords writes the canonical record set for a new Sovereign:
// wildcard A + per-component A records (console, gitea, harbor, admin, api)
// pointing at the load balancer IP.
//
// This is idempotent — re-running it with the same domain + IP is safe.
// Re-running with a different IP appends additional records (Dynadot's
// add_dns_to_current_setting semantics) so the caller is responsible for
// cleaning up stale records via DeleteRecords if the IP changes.
func (c *Client) AddSovereignRecords(ctx context.Context, domain, subdomain, lbIP string) error {
	// For pool domains the records go on the pool domain (e.g. omani.works)
	// with subdomains like "omantel", "console.omantel", "gitea.omantel", etc.
	// For BYO domains the customer is expected to manage their own DNS — we
	// only attempt Dynadot writes when the domain is one we own.

	prefixes := []string{
		"",         // wildcard apex of the subdomain — *.omantel.omani.works
		"console",  // console.omantel.omani.works
		"gitea",    // gitea.omantel.omani.works
		"harbor",   // harbor.omantel.omani.works
		"admin",    // admin.omantel.omani.works
		"api",      // api.omantel.omani.works
	}

	for _, p := range prefixes {
		var sub string
		if p == "" {
			sub = "*." + subdomain
		} else {
			sub = p + "." + subdomain
		}
		err := c.AddRecord(ctx, domain, Record{
			Subdomain: sub,
			Type:      "A",
			Value:     lbIP,
			TTL:       300,
		})
		if err != nil {
			return fmt.Errorf("add %s record: %w", sub, err)
		}
	}
	return nil
}

// managedDomainsState holds the resolved, lower-cased pool-domain set the
// process treats as "Dynadot-managed" for this run. Resolution order
// (per docs/INVIOLABLE-PRINCIPLES.md #4 — never hardcode):
//
//  1. DYNADOT_MANAGED_DOMAINS env var, comma- or whitespace-separated.
//     This is the canonical multi-domain configuration mounted from the
//     dynadot-api-credentials K8s secret's `domains` key.
//  2. Legacy DYNADOT_DOMAIN single-value env var (mounted from the
//     secret's `domain` key) for backward compatibility with the
//     pre-multi-domain secret shape.
//  3. Last-resort built-in defaults — only used in tests / when the
//     deployment forgot to mount the secret. Mirrors SOVEREIGN_POOL_DOMAINS
//     in the wizard's deployment/model.ts plus openova.io itself.
//
// The set is computed once on first read and cached; tests can call
// ResetManagedDomains to force re-evaluation after mutating env vars.
var managedDomainsState struct {
	once sync.Once
	set  map[string]struct{}
}

func resolveManagedDomains() map[string]struct{} {
	managedDomainsState.once.Do(func() {
		managedDomainsState.set = computeManagedDomains()
	})
	return managedDomainsState.set
}

// computeManagedDomains is split out so ResetManagedDomains can re-evaluate.
func computeManagedDomains() map[string]struct{} {
	out := make(map[string]struct{})

	// 1. Multi-domain env var (canonical). Accepts both commas and
	// whitespace as separators so operators can use either.
	if raw := os.Getenv("DYNADOT_MANAGED_DOMAINS"); strings.TrimSpace(raw) != "" {
		for _, tok := range splitDomainsList(raw) {
			out[tok] = struct{}{}
		}
		if len(out) > 0 {
			return out
		}
	}

	// 2. Legacy single-domain env var.
	if d := strings.ToLower(strings.TrimSpace(os.Getenv("DYNADOT_DOMAIN"))); d != "" {
		out[d] = struct{}{}
		return out
	}

	// 3. Built-in defaults — only the wizard's well-known pool domains.
	// These are the SAME values surfaced in SOVEREIGN_POOL_DOMAINS in the
	// UI; keeping them as defensive defaults means a misconfigured
	// deployment fails closed (refuses BYO DNS writes) rather than open.
	out["openova.io"] = struct{}{}
	out["omani.works"] = struct{}{}
	return out
}

// ResetManagedDomains clears the cached managed-domain set so the next
// IsManagedDomain call re-reads the environment. Tests use this; in
// production the env vars are immutable for the lifetime of the pod.
func ResetManagedDomains() {
	managedDomainsState.once = sync.Once{}
	managedDomainsState.set = nil
}

// ManagedDomains returns a sorted, deduplicated copy of the configured
// managed-domain list. Useful for logging, /healthz exposure, and the
// external-dns sidecar that needs to know which zones to sync.
func ManagedDomains() []string {
	set := resolveManagedDomains()
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	// Sort for stable output (tests + logs).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// splitDomainsList parses a `DYNADOT_MANAGED_DOMAINS`-style string —
// comma- or whitespace-separated, lower-cased, trimmed, deduped.
func splitDomainsList(raw string) []string {
	raw = strings.ToLower(raw)
	// Replace commas with spaces, then split on whitespace.
	raw = strings.ReplaceAll(raw, ",", " ")
	parts := strings.Fields(raw)
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// IsManagedDomain returns true if the given domain is one whose DNS Dynadot
// manages on behalf of OpenOva. The set comes from runtime configuration
// (see resolveManagedDomains) so adding a future pool domain (acme.io,
// etc.) only requires updating the dynadot-api-credentials secret, not
// rebuilding this binary.
func IsManagedDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	_, ok := resolveManagedDomains()[domain]
	return ok
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// AddNSDelegation writes the parent-zone NS delegation for a child Sovereign
// FQDN. Closes ticket #374 — the wizard's post-handover step calls this
// (via PDM in Phase 8; via stub catalyst-api endpoint in Phase 7) so the
// parent zone (e.g. omani.works) starts forwarding queries for the child
// zone (e.g. omantel.omani.works) to the Sovereign's own PowerDNS replicas.
//
// Inputs (deliberately minimal — every parameter has a single source of
// truth in the wizard state):
//
//   - parentZone:    the registered domain held in the Dynadot account,
//                    e.g. "omani.works". MUST be one of the managed
//                    domains (IsManagedDomain returns true) — otherwise
//                    we refuse to call the API.
//   - sovereignFQDN: the child zone, e.g. "omantel.omani.works". MUST end
//                    with "." + parentZone.
//   - lbIP:          the new Sovereign's public load-balancer IP, used as
//                    the glue A-record value for ns1.<sovereignFQDN>. The
//                    Sovereign's PowerDNS chart materialises ns1/ns2/ns3
//                    behind that single IP via the anycast-endpoint
//                    Service (see platform/powerdns/chart/values.yaml).
//   - extraNS:       optional additional NS hostnames to bundle into the
//                    delegation. Empty/nil means the canonical 3-record
//                    set (ns1.<fqdn>, ns2.<fqdn>, ns3.<fqdn>). Callers
//                    pass non-default values when a Sovereign is fronted
//                    by a CDN that mints its own NS hostnames.
//
// Like AddSovereignRecords this method uses the package's
// add_dns_to_current_setting=yes contract — it never replaces the parent
// zone's existing record set. Re-running with identical inputs is safe
// (Dynadot dedupes on (subdomain, type, value) per the legacy helper's
// docstring).
//
// The API key/secret are read from the Client (set by the caller from the
// dynadot-api-credentials K8s secret); per Inviolable-Principle #10 we
// never log or echo them.
func (c *Client) AddNSDelegation(
	ctx context.Context,
	parentZone string,
	sovereignFQDN string,
	lbIP string,
	extraNS []string,
) error {
	parentZone = strings.ToLower(strings.TrimSpace(parentZone))
	sovereignFQDN = strings.ToLower(strings.TrimSpace(sovereignFQDN))
	lbIP = strings.TrimSpace(lbIP)

	if parentZone == "" {
		return fmt.Errorf("parentZone is required")
	}
	if sovereignFQDN == "" {
		return fmt.Errorf("sovereignFQDN is required")
	}
	if lbIP == "" {
		return fmt.Errorf("loadBalancerIP is required")
	}
	if !IsManagedDomain(parentZone) {
		return fmt.Errorf(
			"parent zone %q is not in DYNADOT_MANAGED_DOMAINS — refusing to call the API "+
				"(per docs/INVIOLABLE-PRINCIPLES.md #4: fail closed on unmanaged domains)",
			parentZone,
		)
	}
	if !strings.HasSuffix(sovereignFQDN, "."+parentZone) {
		return fmt.Errorf(
			"sovereignFQDN %q is not a child of parentZone %q",
			sovereignFQDN, parentZone,
		)
	}

	// Sub-label below the parent zone (e.g. "omantel" or "eu.omantel").
	sub := strings.TrimSuffix(sovereignFQDN, "."+parentZone)
	if sub == "" {
		return fmt.Errorf("derived empty sub-label from sovereignFQDN=%q parentZone=%q",
			sovereignFQDN, parentZone)
	}

	// Build the NS hostname list: defaults to ns1/ns2/ns3.<fqdn> when the
	// caller didn't override it. Lower-case for byte-equality with the
	// child zone's PowerDNS records.
	ns := extraNS
	if len(ns) == 0 {
		ns = []string{
			"ns1." + sovereignFQDN,
			"ns2." + sovereignFQDN,
			"ns3." + sovereignFQDN,
		}
	}

	// Each AddRecord is its own set_dns2 call. This is the same per-record
	// pattern AddSovereignRecords uses for the wildcard + sub-component A
	// records, so the rate-limit / failure-mode behaviour is consistent
	// across the two helpers.
	for _, host := range ns {
		if err := c.AddRecord(ctx, parentZone, Record{
			Subdomain: sub,
			Type:      "NS",
			Value:     host,
			TTL:       300,
		}); err != nil {
			return fmt.Errorf("add NS record %s → %s: %w", sub, host, err)
		}
	}

	// Glue A record so the parent zone's resolvers can find ns1.<sub>
	// even before the child zone's PowerDNS answers authoritatively
	// (RFC 1034 §4.2.1 in-bailiwick glue).
	if err := c.AddRecord(ctx, parentZone, Record{
		Subdomain: "ns1." + sub,
		Type:      "A",
		Value:     lbIP,
		TTL:       300,
	}); err != nil {
		return fmt.Errorf("add glue A record ns1.%s → %s: %w", sub, lbIP, err)
	}

	return nil
}

// BuildNSDelegationRunbook returns the human-readable curl runbook the
// wizard's StepNSDelegation renders (and the operator copy-pastes during
// Phase 7 of the omantel handover, before the Phase 8 live-execution path
// is wired through PDM). The function is pure — no API calls, no
// credentials touched — so the wizard's runbook section can call it
// without holding K8s secret material.
//
// The output mirrors the JSX-side helper buildDynadotRunbookCommand in
// products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepNSDelegation.tsx
// so the operator can re-derive the command from either side.
func BuildNSDelegationRunbook(parentZone, sovereignFQDN, lbIP string) string {
	parentZone = strings.ToLower(strings.TrimSpace(parentZone))
	sovereignFQDN = strings.ToLower(strings.TrimSpace(sovereignFQDN))
	lbIP = strings.TrimSpace(lbIP)

	sub := sovereignFQDN
	if strings.HasSuffix(sovereignFQDN, "."+parentZone) {
		sub = strings.TrimSuffix(sovereignFQDN, "."+parentZone)
	}

	// nsLine builds one --data-urlencode flag with consistent escaping.
	// Helpful so the runbook is greppable in the operator's terminal.
	line := func(k, v string) string {
		return fmt.Sprintf("  --data-urlencode %q \\", k+"="+v)
	}

	parts := []string{
		fmt.Sprintf("# Delegate %s → Sovereign-owned PowerDNS", sovereignFQDN),
		fmt.Sprintf("# (parent zone: %s, registrar: Dynadot)", parentZone),
		"# Pre-req: export DYNADOT_API_KEY=<your-key>  # never embedded in this runbook",
		"curl -sS https://api.dynadot.com/api3.json \\",
		line("key", "$DYNADOT_API_KEY"),
		line("command", "set_dns2"),
		line("domain", parentZone),
		line("add_dns_to_current_setting", "yes"),
		line("subdomain0", sub),
		line("sub_record_type0", "NS"),
		line("sub_record0", "ns1."+sovereignFQDN),
		line("subdomain1", sub),
		line("sub_record_type1", "NS"),
		line("sub_record1", "ns2."+sovereignFQDN),
		line("subdomain2", sub),
		line("sub_record_type2", "NS"),
		line("sub_record2", "ns3."+sovereignFQDN),
		line("subdomain3", "ns1."+sub),
		line("sub_record_type3", "A"),
		line("sub_record3", lbIP),
	}
	out := strings.Join(parts, "\n")
	// Strip the trailing backslash on the last line so the curl command is
	// well-formed when copy-pasted.
	return strings.TrimSuffix(out, " \\")
}
