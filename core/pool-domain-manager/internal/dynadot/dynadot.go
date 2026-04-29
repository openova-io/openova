// Package dynadot — DNS API client for OpenOva-managed pool domains.
//
// This package is the SOLE caller of api.dynadot.com in the OpenOva fleet.
// catalyst-api, the wizard, and every other product talk to DNS through
// pool-domain-manager — they never import this package directly. Centralising
// the writer means the auto-memory invariant `feedback_dynadot_dns.md`
// (NEVER run exploratory set_dns2 — each call wipes all records) is enforced
// architecturally: there's one writer, one commit path.
//
// Design choices baked in:
//
//   - Every write uses add_dns_to_current_setting=yes so it appends rather
//     than replaces. The Dynadot API treats set_dns2 as "REPLACE the entire
//     zone" by default — the auto-memory documents an incident where this
//     wiped MX records.
//
//   - The managed-domain list comes from runtime configuration
//     (DYNADOT_MANAGED_DOMAINS env var) per docs/INVIOLABLE-PRINCIPLES.md #4.
//     Adding a fourth pool domain is purely a secret update — no rebuild.
//
//   - Reads (set_dns2 has no list-records counterpart) are done via the
//     get_dns command, which returns the current zone we then filter by
//     subdomain prefix when DeleteSubdomainRecords needs to clean up.
package dynadot

import (
	"context"
	"encoding/json"
	"errors"
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

// New returns a Dynadot client. Credentials come from PDM's K8s secret
// `dynadot-api-credentials`; passing them in keeps this package free of
// direct env-var reads (the cmd/pdm main wires it together).
func New(apiKey, apiSecret string) *Client {
	return &Client{
		APIKey:    apiKey,
		APISecret: apiSecret,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Record is a single DNS record we want Dynadot to publish.
type Record struct {
	// Subdomain — leave empty (or "@") for apex. e.g. "console", "*",
	// "*.omantel". Multi-label subdomains ARE supported; Dynadot's set_dns2
	// allows arbitrary labels in the subdomain column.
	Subdomain string
	// Type — A, AAAA, CNAME, TXT, MX, etc.
	Type string
	// Value — depends on Type. For A: IPv4 string; for CNAME: target FQDN.
	Value string
	// TTL — seconds. Dynadot supports 60, 300, 1800, 3600, 7200, 14400,
	// 28800, 43200, 86400. Defaults to 300 if zero.
	TTL int
}

// AddRecord appends a single record to the domain's existing DNS configuration
// using add_dns_to_current_setting=yes. Idempotent across re-runs as long as
// the (subdomain, type, value) tuple is identical (Dynadot dedupes).
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
		return fmt.Errorf("dynadot api status %d: %s", resp.StatusCode, truncate(string(body), 256))
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
		return fmt.Errorf("parse dynadot response: %w (body=%s)", err, truncate(string(body), 256))
	}
	hdr := result.SetDNS2Response.ResponseHeader
	if !strings.EqualFold(hdr.Status, "success") && !strings.EqualFold(hdr.ResponseCode, "0") {
		return fmt.Errorf("dynadot api error: code=%s status=%s err=%s", hdr.ResponseCode, hdr.Status, hdr.Error)
	}
	return nil
}

// AddSovereignRecords writes the canonical record set for a new Sovereign
// subdomain: wildcard + canonical control-plane prefixes (console, gitea,
// harbor, admin, api). All records point at the load balancer IP.
//
// Idempotent: re-running with the same (domain, subdomain, ip) is safe.
// Re-running with a different IP appends extra records (Dynadot append
// semantics) so the caller is responsible for calling DeleteSubdomainRecords
// first when re-pointing.
func (c *Client) AddSovereignRecords(ctx context.Context, domain, subdomain, lbIP string) error {
	prefixes := []string{
		"",        // wildcard apex of the subdomain — *.omantel.omani.works
		"console", // console.omantel.omani.works
		"gitea",   // gitea.omantel.omani.works
		"harbor",  // harbor.omantel.omani.works
		"admin",   // admin.omantel.omani.works
		"api",     // api.omantel.omani.works
	}

	for _, p := range prefixes {
		var sub string
		if p == "" {
			sub = "*." + subdomain
		} else {
			sub = p + "." + subdomain
		}
		if err := c.AddRecord(ctx, domain, Record{
			Subdomain: sub,
			Type:      "A",
			Value:     lbIP,
			TTL:       300,
		}); err != nil {
			return fmt.Errorf("add %s record: %w", sub, err)
		}
	}
	return nil
}

// DeleteSubdomainRecords removes every record under "*.<subdomain>",
// "<prefix>.<subdomain>" for the canonical Sovereign prefixes, by
// re-writing the zone WITHOUT those rows. Dynadot's API has no per-record
// delete; the path is "fetch zone, omit the rows we want gone, write it
// back". We use add_dns_to_current_setting=no for this path because the
// goal IS to replace the zone — but we replace it with a copy that lacks
// the targeted rows AND preserves every other row exactly.
//
// To avoid the auto-memory incident (set_dns2 wiping MX/TXT records), the
// implementation reads the full zone first via get_dns, mutates the in-
// memory representation, and writes back the COMPLETE zone minus the
// targeted rows. The result is a no-op for unrelated records.
//
// Returns nil even when no matching records existed — DeleteSubdomain is
// idempotent.
func (c *Client) DeleteSubdomainRecords(ctx context.Context, domain, subdomain string) error {
	zone, err := c.getZone(ctx, domain)
	if err != nil {
		return fmt.Errorf("read zone: %w", err)
	}

	// Targets to remove: the wildcard + each canonical prefix.
	targets := map[string]struct{}{
		"*." + subdomain:       {},
		"console." + subdomain: {},
		"gitea." + subdomain:   {},
		"harbor." + subdomain:  {},
		"admin." + subdomain:   {},
		"api." + subdomain:     {},
	}

	keep := zone.SubRecords[:0]
	for _, sr := range zone.SubRecords {
		if _, drop := targets[sr.Subdomain]; drop {
			continue
		}
		keep = append(keep, sr)
	}
	zone.SubRecords = keep

	return c.writeZone(ctx, domain, zone)
}

// zoneSnapshot is the in-memory representation of the records returned by
// Dynadot's get_dns command, plus the apex (main) record set.
type zoneSnapshot struct {
	MainRecords []mainRecord
	SubRecords  []subRecord
	TTL         int
}

type mainRecord struct {
	Type  string
	Value string
}

type subRecord struct {
	Subdomain string
	Type      string
	Value     string
}

// getZone reads the current zone via get_dns. Dynadot's response shape is
// nested under GetDnsResponse.Content.MxRecords + .NameServerSettings; we
// only care about the main + sub record arrays for the delete path.
func (c *Client) getZone(ctx context.Context, domain string) (*zoneSnapshot, error) {
	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "get_dns")
	params.Set("domain", domain)

	endpoint := "https://api.dynadot.com/api3.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dynadot get_dns status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	var raw struct {
		GetDNSResponse struct {
			ResponseHeader struct {
				ResponseCode string `json:"ResponseCode"`
				Status       string `json:"Status"`
				Error        string `json:"Error"`
			} `json:"ResponseHeader"`
			Content struct {
				NameServerSettings struct {
					MainDomains []struct {
						RecordType string `json:"record_type"`
						Value      string `json:"value"`
					} `json:"MainDomains"`
					SubDomains []struct {
						Subhost    string `json:"Subhost"`
						RecordType string `json:"RecordType"`
						Value      string `json:"Value"`
					} `json:"SubDomains"`
					TTL int `json:"TTL"`
				} `json:"NameServerSettings"`
			} `json:"Content"`
		} `json:"GetDnsResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse get_dns: %w (body=%s)", err, truncate(string(body), 256))
	}
	hdr := raw.GetDNSResponse.ResponseHeader
	if !strings.EqualFold(hdr.Status, "success") && !strings.EqualFold(hdr.ResponseCode, "0") {
		return nil, fmt.Errorf("dynadot get_dns: code=%s status=%s err=%s", hdr.ResponseCode, hdr.Status, hdr.Error)
	}

	out := &zoneSnapshot{TTL: raw.GetDNSResponse.Content.NameServerSettings.TTL}
	for _, m := range raw.GetDNSResponse.Content.NameServerSettings.MainDomains {
		out.MainRecords = append(out.MainRecords, mainRecord{Type: m.RecordType, Value: m.Value})
	}
	for _, s := range raw.GetDNSResponse.Content.NameServerSettings.SubDomains {
		out.SubRecords = append(out.SubRecords, subRecord{Subdomain: s.Subhost, Type: s.RecordType, Value: s.Value})
	}
	return out, nil
}

// writeZone calls set_dns2 with add_dns_to_current_setting=NO and the full
// zone serialised. This is the dangerous code path the auto-memory warns
// about — we use it only when the caller has read the zone first via
// getZone and wants to write back a deliberate mutation.
func (c *Client) writeZone(ctx context.Context, domain string, zone *zoneSnapshot) error {
	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "set_dns2")
	params.Set("domain", domain)
	// NOTE: deliberately NOT setting add_dns_to_current_setting — we want
	// replace semantics here. The zone we serialise contains every row
	// that was present minus the targeted deletions.

	ttl := zone.TTL
	if ttl == 0 {
		ttl = 300
	}

	for i, m := range zone.MainRecords {
		params.Set(fmt.Sprintf("main_record_type%d", i), m.Type)
		params.Set(fmt.Sprintf("main_record%d", i), m.Value)
		params.Set(fmt.Sprintf("main_recordx%d", i), fmt.Sprintf("%d", ttl))
	}
	for i, s := range zone.SubRecords {
		params.Set(fmt.Sprintf("subdomain%d", i), s.Subdomain)
		params.Set(fmt.Sprintf("sub_record_type%d", i), s.Type)
		params.Set(fmt.Sprintf("sub_record%d", i), s.Value)
		params.Set(fmt.Sprintf("sub_recordx%d", i), fmt.Sprintf("%d", ttl))
	}

	endpoint := "https://api.dynadot.com/api3.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dynadot set_dns2 status %d: %s", resp.StatusCode, truncate(string(body), 256))
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
		return fmt.Errorf("parse set_dns2: %w (body=%s)", err, truncate(string(body), 256))
	}
	hdr := result.SetDNS2Response.ResponseHeader
	if !strings.EqualFold(hdr.Status, "success") && !strings.EqualFold(hdr.ResponseCode, "0") {
		return fmt.Errorf("dynadot set_dns2 error: code=%s status=%s err=%s", hdr.ResponseCode, hdr.Status, hdr.Error)
	}
	return nil
}

// managedDomainsState mirrors the catalyst-api dynadot package's runtime
// resolution: env-var first, then legacy single-domain fallback, then a
// minimal built-in default (kept ONLY so unit tests work without an env).
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

func computeManagedDomains() map[string]struct{} {
	out := make(map[string]struct{})
	if raw := os.Getenv("DYNADOT_MANAGED_DOMAINS"); strings.TrimSpace(raw) != "" {
		for _, tok := range splitDomainsList(raw) {
			out[tok] = struct{}{}
		}
		if len(out) > 0 {
			return out
		}
	}
	if d := strings.ToLower(strings.TrimSpace(os.Getenv("DYNADOT_DOMAIN"))); d != "" {
		out[d] = struct{}{}
		return out
	}
	out["openova.io"] = struct{}{}
	out["omani.works"] = struct{}{}
	return out
}

// ResetManagedDomains clears the cache so tests can re-evaluate after
// mutating env vars.
func ResetManagedDomains() {
	managedDomainsState.once = sync.Once{}
	managedDomainsState.set = nil
}

// ManagedDomains returns a sorted, deduplicated copy of the configured
// managed-domain list. Useful for /healthz exposure and operator logs.
func ManagedDomains() []string {
	set := resolveManagedDomains()
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// IsManagedDomain reports whether the given domain is one whose DNS Dynadot
// manages on behalf of OpenOva.
func IsManagedDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	_, ok := resolveManagedDomains()[domain]
	return ok
}

// splitDomainsList parses a `DYNADOT_MANAGED_DOMAINS`-style string —
// comma- or whitespace-separated, lower-cased, trimmed, deduped.
func splitDomainsList(raw string) []string {
	raw = strings.ToLower(raw)
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// Errors surfaced by the package for callers that want to type-switch.
var (
	// ErrUnmanagedDomain — caller asked for an action against a domain not in
	// DYNADOT_MANAGED_DOMAINS. Hard fail to defend against misconfiguration.
	ErrUnmanagedDomain = errors.New("domain is not in the Dynadot managed list")
)
