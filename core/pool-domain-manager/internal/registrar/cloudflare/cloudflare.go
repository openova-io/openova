// Package cloudflare — Registrar adapter for Cloudflare.
//
// Speaks Cloudflare API v4 using a Bearer token (the customer's API
// token, scoped to Zone:Edit + Zone:Read on the target zone). The
// adapter performs three operations:
//
//   - ValidateToken — `GET /user/tokens/verify` proves the token is
//     active. We then `GET /zones?name=<domain>` to confirm the domain
//     is visible to that token (Cloudflare returns an empty list if it
//     isn't, distinct from a 401).
//
//   - SetNameservers — Cloudflare's "DNS-as-registrar" customers (the
//     zone is registered through Cloudflare Registrar) edit the
//     nameservers via `PATCH /zones/{id}` with `name_servers` body. For
//     non-registrar Cloudflare zones the nameserver list is read-only,
//     and our adapter surfaces the registrar-specific 1006/1007 errors
//     as ErrDomainNotInAccount so the wizard can flag the customer.
//
//   - GetNameservers — `GET /zones/{id}` reads the current `name_servers`.
//
// Reference: https://developers.cloudflare.com/api/resources/zones/
package cloudflare

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

// Adapter implements registrar.Registrar for Cloudflare.
type Adapter struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Cloudflare adapter pointed at the production API.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://api.cloudflare.com/client/v4",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Name returns "cloudflare".
func (a *Adapter) Name() string { return "cloudflare" }

// apiResp is Cloudflare's standard envelope shape: every endpoint wraps
// its specific result in {success, errors[], messages[], result}.
type apiResp struct {
	Success bool              `json:"success"`
	Errors  []apiErr          `json:"errors"`
	Result  json.RawMessage   `json:"result"`
	Info    map[string]any    `json:"result_info,omitempty"`
}

type apiErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// classifyHTTP turns HTTP-level outcomes into typed registrar errors.
// Cloudflare also signals rate-limiting via code 10013/10000 in the body
// at HTTP 200 sometimes; classifyEnvelope handles that path.
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

// classifyEnvelope inspects Cloudflare's "errors[]" array on a non-success
// response. Codes are stable per Cloudflare's docs.
func classifyEnvelope(env apiResp) error {
	if env.Success {
		return nil
	}
	if len(env.Errors) == 0 {
		return errors.New("cloudflare: api returned success=false with no errors")
	}
	first := env.Errors[0]
	switch {
	case first.Code == 10000 || first.Code == 6003 || first.Code == 6111:
		// 10000 invalid creds; 6003 unable to authenticate; 6111 invalid auth header
		return fmt.Errorf("cloudflare: %s: %w", first.Message, registrar.ErrInvalidToken)
	case first.Code == 9103 || first.Code == 9109 || first.Code == 1001:
		// 9103/9109 unauthorised for resource; 1001 zone not found
		return fmt.Errorf("cloudflare: %s: %w", first.Message, registrar.ErrDomainNotInAccount)
	case first.Code == 10013:
		return fmt.Errorf("cloudflare: %s: %w", first.Message, registrar.ErrRateLimited)
	}
	return fmt.Errorf("cloudflare api error: code=%d msg=%s", first.Code, first.Message)
}

// ValidateToken: verify the token is live AND the domain is visible.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("cloudflare: empty token: %w", registrar.ErrInvalidToken)
	}
	// Step 1 — token verify.
	body, err := a.do(ctx, http.MethodGet, "/user/tokens/verify", token, nil)
	if err != nil {
		return err
	}
	var env apiResp
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("cloudflare: parse verify: %w", err)
	}
	if err := classifyEnvelope(env); err != nil {
		return err
	}
	// Step 2 — confirm zone is visible.
	_, err = a.zoneID(ctx, token, domain)
	return err
}

// SetNameservers patches the zone's name_servers.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("cloudflare: nameservers list is empty")
	}
	zoneID, err := a.zoneID(ctx, token, domain)
	if err != nil {
		return err
	}
	payload := map[string]any{"name_servers": ns}
	body, err := a.do(ctx, http.MethodPatch, "/zones/"+zoneID, token, payload)
	if err != nil {
		return err
	}
	var env apiResp
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("cloudflare: parse zone patch: %w", err)
	}
	return classifyEnvelope(env)
}

// GetNameservers reads the zone's name_servers.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	zoneID, err := a.zoneID(ctx, token, domain)
	if err != nil {
		return nil, err
	}
	body, err := a.do(ctx, http.MethodGet, "/zones/"+zoneID, token, nil)
	if err != nil {
		return nil, err
	}
	var env apiResp
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("cloudflare: parse zone get: %w", err)
	}
	if err := classifyEnvelope(env); err != nil {
		return nil, err
	}
	var z struct {
		NameServers []string `json:"name_servers"`
	}
	if err := json.Unmarshal(env.Result, &z); err != nil {
		return nil, fmt.Errorf("cloudflare: parse zone result: %w", err)
	}
	return z.NameServers, nil
}

// zoneID looks up the Cloudflare zone ID by exact name. Returns
// ErrDomainNotInAccount if no zone matches.
func (a *Adapter) zoneID(ctx context.Context, token, domain string) (string, error) {
	body, err := a.do(ctx, http.MethodGet, "/zones?name="+domain, token, nil)
	if err != nil {
		return "", err
	}
	var env apiResp
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("cloudflare: parse zones list: %w", err)
	}
	if err := classifyEnvelope(env); err != nil {
		return "", err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil {
		return "", fmt.Errorf("cloudflare: parse zones array: %w", err)
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, domain) {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: zone %q not found in account: %w", domain, registrar.ErrDomainNotInAccount)
}

// do is the shared HTTP transport.
func (a *Adapter) do(ctx context.Context, method, path, token string, payload any) ([]byte, error) {
	var rdr io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: marshal payload: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		return nil, fmt.Errorf("cloudflare api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudflare api unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

var _ registrar.Registrar = (*Adapter)(nil)
