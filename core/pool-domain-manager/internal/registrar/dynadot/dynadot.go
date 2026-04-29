// Package dynadot — Registrar adapter for Dynadot.
//
// This adapter is the NS-flip-only side of Dynadot integration; the DNS
// record writer at core/pool-domain-manager/internal/dynadot/ is a
// separate concern (record management on OpenOva-managed pool domains).
// They are kept distinct because:
//
//   - Adding/removing records targets pool domains we own (openova.io,
//     omani.works, ...).
//   - Flipping nameservers via SetNameservers targets a customer-owned
//     domain that happens to be registered at Dynadot — the customer's
//     token, not our pool credential, authenticates the call.
//
// Dynadot's HTTP/JSON API (api3.json) supports a `set_ns` command that
// replaces the nameserver list for a domain. We use it via the customer-
// supplied (apiKey, apiSecret) pair. Token format: "<apiKey>:<apiSecret>".
//
// Reference: https://www.dynadot.com/domain/api3.html#set_ns
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

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// Adapter implements registrar.Registrar by speaking Dynadot api3.json.
type Adapter struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Dynadot registrar adapter pointing at the production
// api3.json endpoint. Override BaseURL after construction in tests.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://api.dynadot.com/api3.json",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Name returns "dynadot".
func (a *Adapter) Name() string { return "dynadot" }

// parseToken splits the customer credential into apiKey and apiSecret.
// Format: "apiKey:apiSecret".
func parseToken(token string) (key, secret string, err error) {
	parts := strings.SplitN(strings.TrimSpace(token), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("dynadot: token must be 'apiKey:apiSecret': %w", registrar.ErrInvalidToken)
	}
	return parts[0], parts[1], nil
}

// call invokes one Dynadot API3 command and returns the parsed envelope's
// ResponseHeader so the caller can inspect Status/Error. The command's
// content (NameServerSettings, etc.) is left in raw and decoded by the
// caller into its specific shape.
type respHeader struct {
	ResponseCode string `json:"ResponseCode"`
	Status       string `json:"Status"`
	Error        string `json:"Error"`
}

// classifyHTTP turns an HTTP-level outcome into a typed registrar error.
// Used by every command path so 401/429/5xx map consistently.
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

// classifyDynadotError inspects an api3.json ResponseHeader and maps to
// a typed error. Dynadot's auth-failure messages are not rigidly coded;
// we match the substring patterns the public docs document.
func classifyDynadotError(h respHeader) error {
	if strings.EqualFold(h.Status, "success") || h.ResponseCode == "0" {
		return nil
	}
	msg := strings.ToLower(h.Error)
	switch {
	case strings.Contains(msg, "invalid api"), strings.Contains(msg, "key"), strings.Contains(msg, "secret"), strings.Contains(msg, "auth"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrInvalidToken)
	case strings.Contains(msg, "not found"), strings.Contains(msg, "not in your account"), strings.Contains(msg, "not own"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrDomainNotInAccount)
	case strings.Contains(msg, "rate"), strings.Contains(msg, "too many"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrRateLimited)
	}
	return fmt.Errorf("dynadot api error: code=%s status=%s err=%s", h.ResponseCode, h.Status, h.Error)
}

// ValidateToken probes the registrar with `domain_info` for the named
// domain. Success means: the credentials authenticate AND the domain is
// in the customer's account.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	var raw struct {
		DomainInfoResponse struct {
			ResponseHeader respHeader `json:"ResponseHeader"`
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse domain_info: %w", err)
	}
	return classifyDynadotError(raw.DomainInfoResponse.ResponseHeader)
}

// SetNameservers replaces the domain's nameserver list via set_ns.
// Dynadot accepts up to 13 nameservers; the API uses indexed params
// ns0, ns1, ... — same as the DNS records writer.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("dynadot: nameservers list is empty")
	}
	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "set_ns")
	params.Set("domain", domain)
	for i, n := range ns {
		params.Set(fmt.Sprintf("ns%d", i), n)
	}

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	var raw struct {
		SetNsResponse struct {
			ResponseHeader respHeader `json:"ResponseHeader"`
		} `json:"SetNsResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse set_ns: %w", err)
	}
	return classifyDynadotError(raw.SetNsResponse.ResponseHeader)
}

// GetNameservers reads the current nameserver list via domain_info.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	key, secret, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := a.do(ctx, params)
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
				} `json:"NameServerSettings"`
			} `json:"DomainInfo"`
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("dynadot: parse domain_info: %w", err)
	}
	if err := classifyDynadotError(raw.DomainInfoResponse.ResponseHeader); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw.DomainInfoResponse.DomainInfo.NameServerSettings.NameServers))
	for _, ns := range raw.DomainInfoResponse.DomainInfo.NameServerSettings.NameServers {
		if ns.ServerName != "" {
			out = append(out, ns.ServerName)
		}
	}
	return out, nil
}

// do is the shared HTTP transport: encode params, GET BaseURL, classify
// HTTP-level errors, return the raw body for command-specific decode.
func (a *Adapter) do(ctx context.Context, params url.Values) ([]byte, error) {
	endpoint := a.BaseURL
	if strings.Contains(endpoint, "?") {
		endpoint += "&" + params.Encode()
	} else {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dynadot: build request: %w", err)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dynadot: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
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

// compile-time guard.
var _ registrar.Registrar = (*Adapter)(nil)
