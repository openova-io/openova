// Package namecheap — Registrar adapter for Namecheap.
//
// Namecheap exposes a flat HTTP/XML API. Auth is by ApiUser + ApiKey +
// (account) UserName + ClientIp — every request must include the IP the
// caller is making the request from, and that IP must be whitelisted in
// the Namecheap account dashboard. This is a customer-facing constraint:
// when a customer hands us their API credentials, they MUST also have
// added our PDM egress IP to their Namecheap whitelist. The wizard
// surfaces this requirement up the stack.
//
// Token format passed to the adapter: "<apiUser>:<apiKey>:<userName>:<clientIp>"
// (4-part colon-separated). userName defaults to apiUser when only 3
// parts are supplied; clientIp defaults to ClientIP if both userName and
// clientIp are omitted (3-part format).
//
// Sandbox vs production: Namecheap has api.sandbox.namecheap.com (free)
// and api.namecheap.com (production). The adapter defaults to production;
// tests override BaseURL.
//
// API operations used:
//
//   - namecheap.users.getBalances — used as ValidateToken probe (it's a
//     read-only, low-rate-limit-cost call that requires valid auth).
//   - namecheap.domains.dns.setCustom — sets the nameservers for a TLD.
//     Namecheap requires SLD + TLD splits in the request, so we split
//     the domain at the first dot.
//   - namecheap.domains.getList — used by GetNameservers (no direct
//     "get current NS" command exists for non-DNS-mode domains; we read
//     the registered domain entries).
//
// Reference: https://www.namecheap.com/support/api/intro/
package namecheap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// Adapter implements registrar.Registrar for Namecheap.
type Adapter struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Namecheap adapter aimed at the production endpoint.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://api.namecheap.com/xml.response",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewSandbox returns an adapter pointed at api.sandbox.namecheap.com,
// useful for integration tests.
func NewSandbox() *Adapter {
	a := New()
	a.BaseURL = "https://api.sandbox.namecheap.com/xml.response"
	return a
}

// Name returns "namecheap".
func (a *Adapter) Name() string { return "namecheap" }

// creds is the parsed token shape.
type creds struct {
	APIUser  string
	APIKey   string
	UserName string
	ClientIP string
}

// parseToken splits the colon-separated token. Accepts 2-part (apiUser:
// apiKey, ClientIP defaults to "127.0.0.1" — only useful in tests),
// 3-part (apiUser:apiKey:clientIp) or 4-part (apiUser:apiKey:userName:
// clientIp) formats.
func parseToken(token string) (creds, error) {
	var c creds
	parts := strings.Split(strings.TrimSpace(token), ":")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	switch len(parts) {
	case 4:
		c = creds{APIUser: parts[0], APIKey: parts[1], UserName: parts[2], ClientIP: parts[3]}
	case 3:
		c = creds{APIUser: parts[0], APIKey: parts[1], UserName: parts[0], ClientIP: parts[2]}
	case 2:
		c = creds{APIUser: parts[0], APIKey: parts[1], UserName: parts[0], ClientIP: "127.0.0.1"}
	default:
		return creds{}, fmt.Errorf("namecheap: token must be apiUser:apiKey[:userName]:clientIp: %w", registrar.ErrInvalidToken)
	}
	if c.APIUser == "" || c.APIKey == "" {
		return creds{}, fmt.Errorf("namecheap: empty apiUser/apiKey: %w", registrar.ErrInvalidToken)
	}
	return c, nil
}

// errResponse is the Namecheap error envelope. Every reply has
// <ApiResponse Status="OK|ERROR"><Errors><Error Number="...">msg</Error>
// </Errors></ApiResponse>.
type errResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number string `xml:"Number,attr"`
			Value  string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
}

func classifyHTTP(statusCode int) error {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return registrar.ErrInvalidToken
	case statusCode == http.StatusTooManyRequests:
		return registrar.ErrRateLimited
	case statusCode >= 500:
		return registrar.ErrAPIUnavailable
	}
	return nil
}

// classifyEnvelope inspects Namecheap's <Errors> array. Number codes are
// stable per docs.
func classifyEnvelope(env errResponse) error {
	if strings.EqualFold(env.Status, "OK") && len(env.Errors.Error) == 0 {
		return nil
	}
	if len(env.Errors.Error) == 0 {
		return errors.New("namecheap: api status=ERROR with no errors")
	}
	first := env.Errors.Error[0]
	num := first.Number
	switch {
	// 1010100 invalid api user; 1011102 invalid api key; 1010101 / 1011150 ip whitelist; 1011500 invalid creds
	case num == "1010100", num == "1011102", num == "1011500", num == "1010101", num == "1011150":
		return fmt.Errorf("namecheap: %s [%s]: %w", first.Value, num, registrar.ErrInvalidToken)
	// 2019166 domain not in customer's account; 2019167 you don't own this domain
	case num == "2019166", num == "2019167", num == "2030280":
		return fmt.Errorf("namecheap: %s [%s]: %w", first.Value, num, registrar.ErrDomainNotInAccount)
	// 1011102 too many; 5050900 rate limit-ish (string-match fallback below)
	case strings.Contains(strings.ToLower(first.Value), "rate"), strings.Contains(strings.ToLower(first.Value), "too many"):
		return fmt.Errorf("namecheap: %s [%s]: %w", first.Value, num, registrar.ErrRateLimited)
	}
	return fmt.Errorf("namecheap api error: code=%s msg=%s", num, first.Value)
}

// ValidateToken probes namecheap.users.getBalances. The call needs valid
// auth and is account-scoped; it does NOT confirm the domain — for that
// we then chain a getList check.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	c, err := parseToken(token)
	if err != nil {
		return err
	}
	body, err := a.do(ctx, c, "namecheap.users.getBalances", url.Values{})
	if err != nil {
		return err
	}
	var env errResponse
	if err := xml.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("namecheap: parse balances: %w", err)
	}
	if err := classifyEnvelope(env); err != nil {
		return err
	}
	// Cross-check: confirm domain is in the account.
	return a.confirmDomain(ctx, c, domain)
}

// confirmDomain enumerates domains via getList until we find a match.
// Default Page=1, PageSize=100. We search across pages until match or end.
func (a *Adapter) confirmDomain(ctx context.Context, c creds, domain string) error {
	want := strings.ToLower(strings.TrimSpace(domain))
	for page := 1; page < 200; page++ { // hard cap to avoid runaway
		params := url.Values{}
		params.Set("Page", fmt.Sprintf("%d", page))
		params.Set("PageSize", "100")
		body, err := a.do(ctx, c, "namecheap.domains.getList", params)
		if err != nil {
			return err
		}
		var raw struct {
			XMLName xml.Name `xml:"ApiResponse"`
			Status  string   `xml:"Status,attr"`
			Errors  struct {
				Error []struct {
					Number string `xml:"Number,attr"`
					Value  string `xml:",chardata"`
				} `xml:"Error"`
			} `xml:"Errors"`
			CommandResponse struct {
				Result struct {
					Domains []struct {
						Name string `xml:"Name,attr"`
					} `xml:"Domain"`
				} `xml:"DomainGetListResult"`
				Paging struct {
					TotalItems  int `xml:"TotalItems"`
					CurrentPage int `xml:"CurrentPage"`
					PageSize    int `xml:"PageSize"`
				} `xml:"Paging"`
			} `xml:"CommandResponse"`
		}
		if err := xml.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("namecheap: parse domain list: %w", err)
		}
		if !strings.EqualFold(raw.Status, "OK") {
			env := errResponse{Status: raw.Status}
			env.Errors.Error = make([]struct {
				Number string `xml:"Number,attr"`
				Value  string `xml:",chardata"`
			}, len(raw.Errors.Error))
			for i, e := range raw.Errors.Error {
				env.Errors.Error[i] = struct {
					Number string `xml:"Number,attr"`
					Value  string `xml:",chardata"`
				}{Number: e.Number, Value: e.Value}
			}
			if err := classifyEnvelope(env); err != nil {
				return err
			}
		}
		for _, d := range raw.CommandResponse.Result.Domains {
			if strings.EqualFold(d.Name, want) {
				return nil
			}
		}
		// Pagination terminator.
		total := raw.CommandResponse.Paging.TotalItems
		size := raw.CommandResponse.Paging.PageSize
		if size <= 0 {
			size = 100
		}
		if page*size >= total || len(raw.CommandResponse.Result.Domains) == 0 {
			break
		}
	}
	return fmt.Errorf("namecheap: domain %q not in account: %w", domain, registrar.ErrDomainNotInAccount)
}

// SetNameservers calls namecheap.domains.dns.setCustom.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("namecheap: nameservers list is empty")
	}
	c, err := parseToken(token)
	if err != nil {
		return err
	}
	sld, tld, err := splitDomain(domain)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("SLD", sld)
	params.Set("TLD", tld)
	params.Set("Nameservers", strings.Join(ns, ","))
	body, err := a.do(ctx, c, "namecheap.domains.dns.setCustom", params)
	if err != nil {
		return err
	}
	var env errResponse
	if err := xml.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("namecheap: parse setCustom: %w", err)
	}
	return classifyEnvelope(env)
}

// GetNameservers reads via namecheap.domains.dns.getList.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	c, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	sld, tld, err := splitDomain(domain)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("SLD", sld)
	params.Set("TLD", tld)
	body, err := a.do(ctx, c, "namecheap.domains.dns.getList", params)
	if err != nil {
		return nil, err
	}
	var raw struct {
		XMLName xml.Name `xml:"ApiResponse"`
		Status  string   `xml:"Status,attr"`
		Errors  struct {
			Error []struct {
				Number string `xml:"Number,attr"`
				Value  string `xml:",chardata"`
			} `xml:"Error"`
		} `xml:"Errors"`
		CommandResponse struct {
			Result struct {
				Nameservers []string `xml:"Nameserver"`
			} `xml:"DomainDNSGetListResult"`
		} `xml:"CommandResponse"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("namecheap: parse getList: %w", err)
	}
	if !strings.EqualFold(raw.Status, "OK") {
		env := errResponse{Status: raw.Status}
		for _, e := range raw.Errors.Error {
			env.Errors.Error = append(env.Errors.Error, struct {
				Number string `xml:"Number,attr"`
				Value  string `xml:",chardata"`
			}{Number: e.Number, Value: e.Value})
		}
		if err := classifyEnvelope(env); err != nil {
			return nil, err
		}
	}
	return raw.CommandResponse.Result.Nameservers, nil
}

// splitDomain breaks "acme.com" → ("acme", "com"); "acme.co.uk" →
// ("acme", "co.uk"). Namecheap's API takes SLD + TLD separately.
// We keep the rule simple: SLD = first label, TLD = everything after.
func splitDomain(domain string) (sld, tld string, err error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return "", "", errors.New("namecheap: empty domain")
	}
	idx := strings.Index(d, ".")
	if idx <= 0 || idx == len(d)-1 {
		return "", "", fmt.Errorf("namecheap: invalid domain %q", domain)
	}
	return d[:idx], d[idx+1:], nil
}

// do is the shared transport.
func (a *Adapter) do(ctx context.Context, c creds, command string, extra url.Values) ([]byte, error) {
	params := url.Values{}
	params.Set("ApiUser", c.APIUser)
	params.Set("ApiKey", c.APIKey)
	params.Set("UserName", c.UserName)
	params.Set("ClientIp", c.ClientIP)
	params.Set("Command", command)
	for k, vs := range extra {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	endpoint := a.BaseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("namecheap: build request: %w", err)
	}
	req.Header.Set("Accept", "application/xml")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("namecheap: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		return nil, fmt.Errorf("namecheap api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("namecheap api unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

var _ registrar.Registrar = (*Adapter)(nil)
