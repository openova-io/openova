// Package newapi is a minimal admin-API client for the in-cluster
// NewAPI service consumed by the ADR-0003 user-create hook (step 2).
//
// Per ADR-0003 §3.2 the client targets the in-cluster Service DNS
// `http://newapi-bp-newapi.newapi.svc.cluster.local:3000` (NOT the
// Ingress hostname). The Service name is `<Release.Name>-<Chart.Name>`
// per bp-newapi.fullname helper (releaseName=newapi against chart
// bp-newapi per bootstrap-kit slot 80 → `newapi-bp-newapi`). Auth is
// a bearer token sourced from the `catalyst-newapi-admin-token`
// ExternalSecret rendered by the bp-newapi blueprint (issue #799).
// Default URL corrected in TBD-V15 / #2021 (sister of TBD-V14 / #2017).
//
// Idempotency:
//
//   - POST /api/v1/admin/users with X-Idempotency-Key=<org_user_uuid>:
//     201 → return new {user_id, api_key}; 409 → look up existing via
//     GET /api/v1/admin/users?external_id=<uuid> and return that.
//     Per ADR-0003 §3.2: NewAPI does NOT rotate api_key on conflict.
package newapi

import (
	"bytes"
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

// Client is a thin wrapper around the NewAPI admin REST API.
type Client struct {
	addr  string
	token string
	http  *http.Client
}

// New returns a Client. addr is the in-cluster Service URL
// (e.g. http://newapi-bp-newapi.newapi.svc.cluster.local:3000);
// token is the admin bearer.
func New(addr, token string) *Client {
	return NewWithHTTP(addr, token, &http.Client{Timeout: 30 * time.Second})
}

// NewWithHTTP returns a Client using the supplied HTTP client (for
// tests injecting a httptest.Server URL).
func NewWithHTTP(addr, token string, hc *http.Client) *Client {
	return &Client{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		http:  hc,
	}
}

// User is the response shape for admin user-create + lookup.
type User struct {
	UserID    string `json:"user_id"`
	APIKey    string `json:"api_key"`
	CreatedAt string `json:"created_at,omitempty"`
}

// CreateUserRequest is the body shape for POST /api/v1/admin/users.
// Fields mirror ADR-0003 §3.2 verbatim.
type CreateUserRequest struct {
	ExternalID string            `json:"external_id"`
	Email      string            `json:"email"`
	TenantID   string            `json:"tenant_id"`
	Tier       string            `json:"tier"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// EnsureUser is the idempotent create-or-fetch operation:
//
//	POST /api/v1/admin/users with X-Idempotency-Key=ExternalID
//	201 → return new {user_id, api_key}
//	409 → GET /api/v1/admin/users?external_id=ExternalID and return
//	      the existing record (NewAPI does NOT rotate the key).
//
// Other status codes (5xx, 4xx other than 409) return an error so
// the caller's reconciliation loop can classify transient vs
// terminal per ADR-0003 §3.8.
func (c *Client) EnsureUser(ctx context.Context, req CreateUserRequest) (*User, error) {
	if strings.TrimSpace(req.ExternalID) == "" {
		return nil, errors.New("newapi: external_id is required")
	}
	if c.addr == "" {
		return nil, errors.New("newapi: client address not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.addr+"/api/v1/admin/users", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.token)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("X-Idempotency-Key", req.ExternalID)

	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("newapi: POST users: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		var u User
		if err := json.Unmarshal(respBody, &u); err != nil {
			return nil, fmt.Errorf("newapi: decode 201 body: %w", err)
		}
		return &u, nil
	case http.StatusConflict:
		// Existing user — fetch by external_id.
		return c.getByExternalID(ctx, req.ExternalID)
	default:
		return nil, fmt.Errorf("newapi: POST users HTTP %d: %s",
			resp.StatusCode, truncate(string(respBody), 256))
	}
}

// getByExternalID is the conflict-recovery lookup for EnsureUser.
func (c *Client) getByExternalID(ctx context.Context, externalID string) (*User, error) {
	q := url.Values{}
	q.Set("external_id", externalID)
	u := c.addr + "/api/v1/admin/users?" + q.Encode()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.token)
	hreq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("newapi: GET users by external_id: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("newapi: GET by external_id HTTP %d: %s",
			resp.StatusCode, truncate(string(respBody), 256))
	}

	// Server may return a single object or a list; tolerate both shapes.
	var asObj User
	if err := json.Unmarshal(respBody, &asObj); err == nil && asObj.UserID != "" {
		return &asObj, nil
	}
	var asList []User
	if err := json.Unmarshal(respBody, &asList); err == nil && len(asList) > 0 {
		u := asList[0]
		return &u, nil
	}
	return nil, errors.New("newapi: GET by external_id returned no usable record")
}

// DisableUser revokes the per-user api_key as part of the ADR-0003
// §3.7 rollback path. Best-effort (returns nil on 404 — already gone).
func (c *Client) DisableUser(ctx context.Context, userID string) error {
	if c.addr == "" {
		return nil
	}
	if strings.TrimSpace(userID) == "" {
		return nil
	}
	u := fmt.Sprintf("%s/api/v1/admin/users/%s/disable", c.addr, url.PathEscape(userID))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(hreq)
	if err != nil {
		return fmt.Errorf("newapi: POST disable: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("newapi: disable HTTP %d", resp.StatusCode)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
