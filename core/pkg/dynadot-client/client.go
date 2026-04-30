// Dynadot HTTP transport + command builders. See doc.go for the rationale
// behind hosting this client at core/pkg/.
package dynadot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production Dynadot api3.json endpoint. Override
// via Client.BaseURL in tests.
const DefaultBaseURL = "https://api.dynadot.com/api3.json"

// Errors surfaced by the client. Callers can errors.Is / errors.As against
// these to distinguish auth failures from transport failures from
// "domain not in your account" without parsing strings.
var (
	// ErrInvalidToken — the Dynadot API rejected the (key, secret) pair.
	ErrInvalidToken = errors.New("dynadot: invalid api key/secret")
	// ErrRateLimited — Dynadot returned 429 or a rate-limit error code.
	ErrRateLimited = errors.New("dynadot: rate limited")
	// ErrAPIUnavailable — the API endpoint is not reachable or returned 5xx.
	ErrAPIUnavailable = errors.New("dynadot: api unavailable")
	// ErrDomainNotInAccount — the provided domain is not registered with
	// the calling account. Frequently means the operator pointed the
	// webhook at the wrong domain (typo) or rotated credentials to a
	// different account.
	ErrDomainNotInAccount = errors.New("dynadot: domain not in account")
)

// Client wraps the Dynadot api3.json endpoint. Construct via New and
// reuse — every method is safe for concurrent use.
type Client struct {
	// APIKey + APISecret authenticate every call. Both are required.
	APIKey    string
	APISecret string
	// BaseURL is api.dynadot.com/api3.json by default. Tests override
	// with an httptest.Server URL.
	BaseURL string
	// HTTP is the underlying transport. Replaced by tests with a client
	// pointing at an httptest fixture; in production a 30s-timeout
	// http.Client is used so a stuck Dynadot socket cannot block a
	// webhook reply for the full kube-apiserver request budget.
	HTTP *http.Client
}

// New builds a Client with the production endpoint and a sane HTTP
// timeout. Panics if either credential is empty — that is a programmer
// error and would otherwise surface as a confusing 401 on the first
// call.
func New(apiKey, apiSecret string) *Client {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(apiSecret) == "" {
		panic("dynadot.New: APIKey and APISecret must be non-empty")
	}
	return &Client{
		APIKey:    apiKey,
		APISecret: apiSecret,
		BaseURL:   DefaultBaseURL,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Record is one DNS record on a Dynadot-managed domain.
type Record struct {
	// Subdomain — empty/"@" means apex. Examples: "*.omantel",
	// "_acme-challenge.console.omantel".
	Subdomain string
	// Type — A, AAAA, CNAME, TXT, MX, NS. Uppercase.
	Type string
	// Value — A:IPv4, CNAME:FQDN, TXT:content (no quotes), MX:"prio host".
	Value string
	// TTL in seconds. Dynadot snaps to a fixed ladder
	// (60, 300, 1800, 3600, 7200, 14400, 28800, 43200, 86400). We default
	// to 60 for ACME challenges so the DNS-01 propagation wait is short.
	TTL int
}

// AddRecord appends a single record using set_dns2 with
// add_dns_to_current_setting=yes. This is the SAFE append path —
// existing records are preserved.
//
// Idempotency: Dynadot dedupes by (subdomain, type, value), so re-running
// with the same record is a no-op. Use AddRecord for the simple cases
// where you only need to add — for read-modify-write semantics (e.g.
// removing a TXT record) use FullSync.
func (c *Client) AddRecord(ctx context.Context, domain string, rec Record) error {
	if rec.TTL == 0 {
		rec.TTL = 60
	}

	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "set_dns2")
	params.Set("domain", domain)
	params.Set("add_dns_to_current_setting", "yes")

	if rec.Subdomain == "" || rec.Subdomain == "@" {
		params.Set("main_record_type0", strings.ToUpper(rec.Type))
		params.Set("main_record0", rec.Value)
		params.Set("main_recordx0", fmt.Sprintf("%d", rec.TTL))
	} else {
		params.Set("subdomain0", rec.Subdomain)
		params.Set("sub_record_type0", strings.ToUpper(rec.Type))
		params.Set("sub_record0", rec.Value)
		params.Set("sub_recordx0", fmt.Sprintf("%d", rec.TTL))
	}

	body, err := c.do(ctx, params)
	if err != nil {
		return err
	}

	var raw struct {
		SetDNS2Response struct {
			ResponseHeader respHeader `json:"ResponseHeader"`
		} `json:"SetDns2Response"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse set_dns2: %w (body=%s)", err, truncate(string(body), 256))
	}
	return classifyDynadotError(raw.SetDNS2Response.ResponseHeader)
}

// DomainInfo is the parsed result of a `domain_info` call. Only the
// subset of fields the cert-manager webhook needs is populated — apex
// records, sub-records, and nameserver list. Adding fields here is
// non-breaking.
type DomainInfo struct {
	NameServers []string
	MainRecords []Record
	SubRecords  []Record
}

// GetDomainInfo reads the current DNS configuration for `domain` via
// `domain_info`. The returned DomainInfo is a faithful snapshot of what
// Dynadot will return on the next ACME challenge — tests assert against
// this directly so the SetFullDNS round-trip can be verified.
func (c *Client) GetDomainInfo(ctx context.Context, domain string) (*DomainInfo, error) {
	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := c.do(ctx, params)
	if err != nil {
		return nil, err
	}

	var raw struct {
		DomainInfoResponse struct {
			ResponseHeader respHeader `json:"ResponseHeader"`
			DomainInfo     struct {
				NameServerSettings struct {
					NameServers []struct {
						ServerName string `json:"ServerName"`
					} `json:"NameServers"`
					MainDomains []struct {
						RecordType string `json:"RecordType"`
						Value      string `json:"Value"`
						TTL        int    `json:"TTL"`
					} `json:"MainDomains"`
					SubDomains []struct {
						Subhost    string `json:"Subhost"`
						RecordType string `json:"RecordType"`
						Value      string `json:"Value"`
						TTL        int    `json:"TTL"`
					} `json:"SubDomains"`
				} `json:"NameServerSettings"`
			} `json:"DomainInfo"`
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("dynadot: parse domain_info: %w (body=%s)", err, truncate(string(body), 256))
	}
	if err := classifyDynadotError(raw.DomainInfoResponse.ResponseHeader); err != nil {
		return nil, err
	}

	out := &DomainInfo{}
	for _, ns := range raw.DomainInfoResponse.DomainInfo.NameServerSettings.NameServers {
		if ns.ServerName != "" {
			out.NameServers = append(out.NameServers, ns.ServerName)
		}
	}
	for _, m := range raw.DomainInfoResponse.DomainInfo.NameServerSettings.MainDomains {
		out.MainRecords = append(out.MainRecords, Record{
			Type:  m.RecordType,
			Value: m.Value,
			TTL:   m.TTL,
		})
	}
	for _, s := range raw.DomainInfoResponse.DomainInfo.NameServerSettings.SubDomains {
		out.SubRecords = append(out.SubRecords, Record{
			Subdomain: s.Subhost,
			Type:      s.RecordType,
			Value:     s.Value,
			TTL:       s.TTL,
		})
	}
	return out, nil
}

// SetFullDNS replaces the entire DNS configuration for `domain` with the
// provided main + sub record lists. THIS WIPES ANY RECORD NOT IN THE
// SUPPLIED LISTS — it is the destructive variant of set_dns2 and must
// only be used as the second half of a read-modify-write that started
// with GetDomainInfo. Direct callers in production should use
// AddRecord (append) or RemoveSubRecord (read-modify-write) instead.
//
// The function is exported so the cert-manager-dynadot-webhook can
// remove a specific TXT record at CleanUp time. Per ~/.claude/.../memory/
// feedback_dynadot_dns.md, exploratory calls without a prior read are a
// known incident and have wiped pool-domain DNS in the past — do not
// add new direct callers.
func (c *Client) SetFullDNS(ctx context.Context, domain string, mains, subs []Record) error {
	params := url.Values{}
	params.Set("key", c.APIKey)
	params.Set("secret", c.APISecret)
	params.Set("command", "set_dns2")
	params.Set("domain", domain)
	// NB: no add_dns_to_current_setting — this is the full-replace path.

	for i, m := range mains {
		ttl := m.TTL
		if ttl == 0 {
			ttl = 60
		}
		params.Set(fmt.Sprintf("main_record_type%d", i), strings.ToUpper(m.Type))
		params.Set(fmt.Sprintf("main_record%d", i), m.Value)
		params.Set(fmt.Sprintf("main_recordx%d", i), fmt.Sprintf("%d", ttl))
	}
	for i, s := range subs {
		ttl := s.TTL
		if ttl == 0 {
			ttl = 60
		}
		params.Set(fmt.Sprintf("subdomain%d", i), s.Subdomain)
		params.Set(fmt.Sprintf("sub_record_type%d", i), strings.ToUpper(s.Type))
		params.Set(fmt.Sprintf("sub_record%d", i), s.Value)
		params.Set(fmt.Sprintf("sub_recordx%d", i), fmt.Sprintf("%d", ttl))
	}

	body, err := c.do(ctx, params)
	if err != nil {
		return err
	}

	var raw struct {
		SetDNS2Response struct {
			ResponseHeader respHeader `json:"ResponseHeader"`
		} `json:"SetDns2Response"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse set_dns2: %w (body=%s)", err, truncate(string(body), 256))
	}
	return classifyDynadotError(raw.SetDNS2Response.ResponseHeader)
}

// RemoveSubRecord performs a safe read-modify-write that removes any
// sub-records whose (Subdomain, Type, Value) tuple matches `match`.
// All other records (main + remaining subs) are preserved verbatim.
//
// Used by the cert-manager DNS-01 webhook's CleanUp path: after Let's
// Encrypt validates a challenge, the TXT record at
// `_acme-challenge.<host>` must be removed so the zone is clean for
// the next renewal.
//
// If no record matches, RemoveSubRecord returns nil — that is the
// expected outcome when CleanUp is retried (idempotent).
func (c *Client) RemoveSubRecord(ctx context.Context, domain string, match Record) error {
	info, err := c.GetDomainInfo(ctx, domain)
	if err != nil {
		return fmt.Errorf("dynadot: read domain_info before delete: %w", err)
	}
	wantSub := strings.ToLower(strings.TrimSpace(match.Subdomain))
	wantType := strings.ToUpper(strings.TrimSpace(match.Type))
	wantValue := strings.TrimSpace(match.Value)

	kept := make([]Record, 0, len(info.SubRecords))
	removed := false
	for _, r := range info.SubRecords {
		if strings.EqualFold(strings.TrimSpace(r.Subdomain), wantSub) &&
			strings.EqualFold(strings.TrimSpace(r.Type), wantType) &&
			(wantValue == "" || strings.TrimSpace(r.Value) == wantValue) {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		// Nothing to do — idempotent CleanUp.
		return nil
	}
	return c.SetFullDNS(ctx, domain, info.MainRecords, kept)
}

// respHeader matches the Dynadot envelope's ResponseHeader on every
// command. `ResponseCode` is 0 on success, non-zero on failure;
// `Error` is human-readable.
type respHeader struct {
	ResponseCode string `json:"ResponseCode"`
	Status       string `json:"Status"`
	Error        string `json:"Error"`
}

// classifyHTTP turns a transport-level outcome into a typed sentinel
// error so callers can errors.Is(err, ErrInvalidToken) etc.
func classifyHTTP(statusCode int) error {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return ErrInvalidToken
	case statusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case statusCode >= 500:
		return ErrAPIUnavailable
	}
	return nil
}

// classifyDynadotError inspects an api3.json envelope and surfaces a
// typed sentinel for the categories that matter to the webhook. The
// raw error string is preserved via error wrapping for operator logs.
func classifyDynadotError(h respHeader) error {
	if strings.EqualFold(h.Status, "success") || h.ResponseCode == "0" {
		return nil
	}
	msg := strings.ToLower(h.Error)
	switch {
	case strings.Contains(msg, "invalid api"),
		strings.Contains(msg, "invalid key"),
		strings.Contains(msg, "invalid secret"),
		strings.Contains(msg, "auth"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, ErrInvalidToken)
	case strings.Contains(msg, "not found"),
		strings.Contains(msg, "not in your account"),
		strings.Contains(msg, "not own"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, ErrDomainNotInAccount)
	case strings.Contains(msg, "rate"), strings.Contains(msg, "too many"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, ErrRateLimited)
	}
	return fmt.Errorf("dynadot api error: code=%s status=%s err=%s", h.ResponseCode, h.Status, h.Error)
}

// do issues the GET, classifies HTTP-level errors, and returns the raw
// response body for command-specific JSON decoding upstream.
func (c *Client) do(ctx context.Context, params url.Values) ([]byte, error) {
	endpoint := c.BaseURL
	if endpoint == "" {
		endpoint = DefaultBaseURL
	}
	if strings.Contains(endpoint, "?") {
		endpoint += "&" + params.Encode()
	} else {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dynadot: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dynadot: %s: %w", err.Error(), ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		return nil, fmt.Errorf("dynadot api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dynadot api unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
