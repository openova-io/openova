// Package godaddy — Registrar adapter for GoDaddy.
//
// GoDaddy v1 API uses a "sso-key <key>:<secret>" Authorization header.
// The token shape we accept is "<apiKey>:<apiSecret>" (colon separated)
// matching how AWS-style two-part credentials are typically conveyed.
//
// Endpoints used:
//
//   - GET  /v1/domains              — list of domains visible to the
//                                     credential. Used for ValidateToken
//                                     (auth check) AND domain-in-account
//                                     check (search the response).
//   - PATCH /v1/domains/{domain}    — body {"nameServers":[...]} — sets
//                                     the nameserver list on the named
//                                     domain. PATCH semantics: only the
//                                     supplied fields are updated.
//   - GET  /v1/domains/{domain}     — read the domain record; the
//                                     response contains nameServers[].
//
// Reference: https://developer.godaddy.com/doc/endpoint/domains
package godaddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// Adapter implements registrar.Registrar for GoDaddy.
type Adapter struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a GoDaddy adapter.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://api.godaddy.com",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewOTE returns an adapter aimed at api.ote-godaddy.com for sandbox
// testing.
func NewOTE() *Adapter {
	a := New()
	a.BaseURL = "https://api.ote-godaddy.com"
	return a
}

// Name returns "godaddy".
func (a *Adapter) Name() string { return "godaddy" }

// authHeader builds the GoDaddy auth header from a colon-separated
// token. apiKey:apiSecret.
func authHeader(token string) (string, error) {
	parts := strings.SplitN(strings.TrimSpace(token), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("godaddy: token must be 'apiKey:apiSecret': %w", registrar.ErrInvalidToken)
	}
	return "sso-key " + parts[0] + ":" + parts[1], nil
}

// godaddyError is the standard error envelope.
type godaddyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Name    string `json:"name"`
}

// classifyHTTP is the first-pass HTTP→typed-error map.
func classifyHTTP(statusCode int) error {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return registrar.ErrInvalidToken
	case statusCode == http.StatusTooManyRequests:
		return registrar.ErrRateLimited
	case statusCode == http.StatusNotFound:
		return registrar.ErrDomainNotInAccount
	case statusCode >= 500:
		return registrar.ErrAPIUnavailable
	}
	return nil
}

// ValidateToken: GET /v1/domains. 401/403 → invalid token. We then look
// for the supplied domain in the listing. GoDaddy's /v1/domains supports
// pagination via marker; default returns up to 1000 entries which is
// enough for almost all customers.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	auth, err := authHeader(token)
	if err != nil {
		return err
	}
	body, err := a.do(ctx, http.MethodGet, "/v1/domains?statuses=ACTIVE&limit=1000", auth, nil)
	if err != nil {
		return err
	}
	var domains []struct {
		Domain string `json:"domain"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &domains); err != nil {
		return fmt.Errorf("godaddy: parse domains list: %w", err)
	}
	want := strings.ToLower(strings.TrimSpace(domain))
	for _, d := range domains {
		if strings.EqualFold(d.Domain, want) {
			return nil
		}
	}
	return fmt.Errorf("godaddy: %q not in account: %w", domain, registrar.ErrDomainNotInAccount)
}

// SetNameservers PATCH /v1/domains/{domain} with nameServers body.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("godaddy: nameservers list is empty")
	}
	auth, err := authHeader(token)
	if err != nil {
		return err
	}
	payload := map[string]any{"nameServers": ns}
	_, err = a.do(ctx, http.MethodPatch, "/v1/domains/"+domain, auth, payload)
	return err
}

// GetNameservers GET /v1/domains/{domain}.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	auth, err := authHeader(token)
	if err != nil {
		return nil, err
	}
	body, err := a.do(ctx, http.MethodGet, "/v1/domains/"+domain, auth, nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Domain      string   `json:"domain"`
		NameServers []string `json:"nameServers"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("godaddy: parse domain detail: %w", err)
	}
	return d.NameServers, nil
}

// do is the shared HTTP transport. PATCH/POST send JSON; GET ignores body.
// On non-2xx the body is parsed as godaddyError and surfaced via the
// typed error vocabulary.
func (a *Adapter) do(ctx context.Context, method, path, auth string, payload any) ([]byte, error) {
	var rdr io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("godaddy: marshal payload: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("godaddy: build request: %w", err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("godaddy: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		// Try to extract richer detail from the envelope.
		var gerr godaddyError
		_ = json.Unmarshal(body, &gerr)
		if gerr.Message != "" {
			return nil, fmt.Errorf("godaddy api status %d (%s): %w", resp.StatusCode, gerr.Message, e)
		}
		return nil, fmt.Errorf("godaddy api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("godaddy api unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

var _ registrar.Registrar = (*Adapter)(nil)
