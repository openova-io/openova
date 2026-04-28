// Package dynadot — DNS API client for omani.works (and other OpenOva pool
// domains registered in the same Dynadot account).
//
// The Dynadot API is account-scoped: a single API key + secret pair can
// manage all domains owned by that account. The K8s secret
// `dynadot-api-credentials` in the `openova-system` namespace stores the
// credentials. Per docs/PROVISIONING-PLAN.md §3 the secret's `domain` field
// can be a list — this client takes a domain per call so the same client
// instance handles openova.io, omani.works, and any future pool domain.
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

// builtinDefaultManagedDomains is the last-resort allowlist used when neither
// DYNADOT_MANAGED_DOMAINS nor DYNADOT_DOMAIN is set in the environment. This
// keeps unit tests / dev runs working without any K8s secret wiring while
// still giving production a configurable knob.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #4 ("never hardcode") this list
// is ONLY a fallback — production picks the real list out of the
// dynadot-api-credentials K8s secret via env. Adding a new pool domain to
// production does NOT require a code change; it requires a secret update.
var builtinDefaultManagedDomains = []string{"openova.io", "omani.works"}

var (
	managedDomainsOnce  sync.Once
	managedDomainsCache []string
)

// ManagedDomains returns the canonical list of pool domains whose DNS we
// manage via the Dynadot API. The list resolution order is:
//
//  1. DYNADOT_MANAGED_DOMAINS — comma-separated canonical list. This is the
//     production wiring (set on the catalyst-api Deployment as an env var
//     sourced from the dynadot-api-credentials K8s secret's
//     `managed-domains` field). New pool domains are added to production by
//     editing the secret, NOT by editing this code.
//  2. DYNADOT_DOMAIN — legacy single-domain env var. The first iteration of
//     the secret only carried one domain; this fallback ensures the binary
//     keeps working during a rolling secret upgrade where some pods see the
//     new key and others see the old key.
//  3. Built-in defaults — used when neither env var is set (dev workstations
//     and unit tests). Production MUST set #1.
//
// Each domain is normalised: lowercase, trimmed of whitespace. Duplicates
// are collapsed. The result is cached after the first call so callers can
// invoke this in hot paths without re-parsing the env each time. Tests that
// need to mutate the env mid-process can call resetManagedDomainsCache().
func ManagedDomains() []string {
	managedDomainsOnce.Do(func() {
		managedDomainsCache = loadManagedDomains()
	})
	return managedDomainsCache
}

// loadManagedDomains is the unmemoised implementation — exposed (lowercase,
// package-internal) so tests can drive it deterministically without fighting
// the sync.Once cache.
func loadManagedDomains() []string {
	if raw := strings.TrimSpace(os.Getenv("DYNADOT_MANAGED_DOMAINS")); raw != "" {
		return canonicaliseDomainList(strings.Split(raw, ","))
	}
	if single := strings.TrimSpace(os.Getenv("DYNADOT_DOMAIN")); single != "" {
		// Legacy key — single domain, no separator. Still run it through the
		// canonicalisation pipeline so case and whitespace get normalised.
		return canonicaliseDomainList([]string{single})
	}
	out := make([]string, len(builtinDefaultManagedDomains))
	copy(out, builtinDefaultManagedDomains)
	return out
}

// canonicaliseDomainList lowercases each entry, trims whitespace, drops
// empties, and dedupes while preserving first-seen order.
func canonicaliseDomainList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

// resetManagedDomainsCache clears the memoised result so a unit test can
// re-set DYNADOT_MANAGED_DOMAINS / DYNADOT_DOMAIN and re-resolve. Production
// callers never need this — the env is read once at startup.
func resetManagedDomainsCache() {
	managedDomainsOnce = sync.Once{}
	managedDomainsCache = nil
}

// IsManagedDomain returns true if the given domain is one whose DNS Dynadot
// manages on behalf of OpenOva. The decision is delegated to ManagedDomains()
// so it always reflects the same allowlist (single source of truth) and
// honours the DYNADOT_MANAGED_DOMAINS / DYNADOT_DOMAIN env var configuration.
//
// Match is case-insensitive and whitespace-tolerant.
func IsManagedDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	for _, d := range ManagedDomains() {
		if d == domain {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
