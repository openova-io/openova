// Package catalystapi is the thin HTTP client the MCP tool handlers use
// to reach the LIVE catalyst-api. It is the facade boundary mandated by
// #3988 §3: the MCP reimplements NOTHING — every tool forwards the
// caller's bearer to the SAME REST endpoint the console UI calls, so the
// endpoint's own authz (RequireSession + the per-handler tier check) is
// the final word. The MCP holds no privileged token of its own.
//
// Endpoints used by the first slice (all read-only, from the real
// catalyst-api route table in
// products/catalyst/bootstrap/api/cmd/api/main.go):
//
//   - GET /api/v1/sovereigns/{id}/applications              (HandleApplicationList)
//   - GET /api/v1/sovereigns/{id}/applications/{name}       (HandleApplicationGet)
//   - GET /api/v1/organizations                             (HandleListOrganizations)
package catalystapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps an HTTP client + the catalyst-api base URL. The caller's
// bearer is supplied per-request (forwarded identity), never stored.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client targeting baseURL (e.g.
// `https://console.openova.io` for the mothership route table, or the
// in-cluster catalyst-api Service URL on a Sovereign). The default 30s
// timeout bounds a wedged upstream.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// WithHTTPClient overrides the underlying http.Client (used by tests to
// inject a RoundTripper that serves canned catalyst-api responses).
func (c *Client) WithHTTPClient(h *http.Client) *Client { c.http = h; return c }

// APIError carries the upstream status + body so the MCP can return the
// SAME status the UI endpoint returned (the parity requirement — #3988
// DoD §4). isError-mapping happens in the dispatch layer.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("catalyst-api: upstream status %d: %s", e.Status, truncate(e.Body, 512))
}

// get issues an authenticated GET and decodes a 2xx JSON body into out.
// Non-2xx → *APIError carrying the upstream status (so 403→403 parity
// holds). bearer is forwarded verbatim as the Authorization header AND as
// the session cookie, covering both the gateway (Authorization) and the
// catalyst-api RequireSession (cookie) read paths.
func (c *Client) get(ctx context.Context, path, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("catalyst-api: build request: %w", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		// catalyst-api RequireSession reads the session cookie; forward
		// the same bearer there too so the facade works against both the
		// gateway and the catalyst-api directly.
		req.Header.Set("Cookie", "catalyst_session="+bearer)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("catalyst-api: %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("catalyst-api: decode %s: %w", path, err)
	}
	return nil
}

// ── Wire shapes (mirror the catalyst-api handler responses exactly) ──────

// ApplicationListResponse mirrors applicationListResponse in
// products/catalyst/bootstrap/api/internal/handler/applications.go.
type ApplicationListResponse struct {
	Kind  string            `json:"kind"`
	Items []ApplicationItem `json:"items"`
	Total int               `json:"total"`
}

// ApplicationItem mirrors applicationListItem.
type ApplicationItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Blueprint string `json:"blueprint,omitempty"`
	Version   string `json:"version,omitempty"`
	Phase     string `json:"phase,omitempty"`
}

// OrganizationListResponse mirrors the {items:[…]} envelope from
// HandleListOrganizations.
type OrganizationListResponse struct {
	Items []map[string]any `json:"items"`
}

// ListApplications calls GET /api/v1/sovereigns/{depID}/applications.
func (c *Client) ListApplications(ctx context.Context, depID, bearer string) (*ApplicationListResponse, error) {
	var out ApplicationListResponse
	if err := c.get(ctx, fmt.Sprintf("/api/v1/sovereigns/%s/applications", depID), bearer, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetApplication calls GET /api/v1/sovereigns/{depID}/applications/{name}.
// The body shape is the full Application object; we return it as a generic
// map so the MCP surfaces exactly what the UI would render without forcing
// a brittle struct on a still-evolving CR shape.
func (c *Client) GetApplication(ctx context.Context, depID, name, bearer string) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, fmt.Sprintf("/api/v1/sovereigns/%s/applications/%s", depID, name), bearer, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListOrganizations calls GET /api/v1/organizations.
func (c *Client) ListOrganizations(ctx context.Context, bearer string) (*OrganizationListResponse, error) {
	var out OrganizationListResponse
	if err := c.get(ctx, "/api/v1/organizations", bearer, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
