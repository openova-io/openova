// Package catalystapi is the thin HTTP client the MCP tool handlers use
// to reach the LIVE catalyst-api. It is the facade boundary mandated by
// #3988 §3: the MCP reimplements NOTHING — every tool forwards the
// caller's bearer to the SAME REST endpoint the console UI calls, so the
// endpoint's own authz (RequireSession + the per-handler tier check) is
// the final word. The MCP holds no privileged token of its own.
//
// Endpoints used by the first slice (from the real catalyst-api route
// table in products/catalyst/bootstrap/api/cmd/api/main.go):
//
//   - GET  /api/v1/sovereigns/{id}/applications              (HandleApplicationList)
//   - GET  /api/v1/sovereigns/{id}/applications/{name}       (HandleApplicationGet)
//   - GET  /api/v1/organizations                             (HandleListOrganizations)
//   - POST /api/v1/sovereigns/{id}/applications              (HandleApplicationInstall)
//   - GET  /api/v1/org/applications                          (HandleOrgApplications)
//   - POST /api/v1/org/applications                          (HandleOrgApplicationInstall)
//
// The `/api/v1/sovereigns/{id}/…` seam is DEPLOYMENT-ADDRESSED and
// Sovereign-wide; the catalyst-api OrgScopeGuard 403s an Org-scoped session
// on it (it is NOT in orgSafePathPrefixes). The `/api/v1/org/…` pair is the
// own-org seam an Org-scoped session must use — it IS allowlisted, and it
// resolves the caller's own Org namespace SERVER-SIDE from X-Tenant-Host.
// See the ListApplicationsOrg doc comment for the full #5516 reasoning.
//
// The POST is the FIRST write tool (#3988 — create_application, UAT rows
// 221-223). It reuses the SAME UI create seam (HandleApplicationInstall)
// the console Install button posts to — the MCP reimplements no
// namespace/CR logic; it forwards the caller's bearer + the identical
// install-request body, so ensureOrgNamespace + the Application CR write
// happen exactly once, in the catalyst-api handler.
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

	// tenantHost is the Organization console host (e.g.
	// console.demo.omani.homes) the org-scoped install path passes as
	// X-Tenant-Host so the catalyst-api resolves the caller's OWN Org
	// namespace from the tenant registry (#4116). Empty = the org-create
	// path omits the header (catalyst-api 400s tenant-required).
	tenantHost string
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

// WithTenantHost sets the Organization console host the org-scoped install
// path passes as X-Tenant-Host (#4116). The catalyst-api uses it to resolve
// the caller's own Org namespace; the #4110 binding ensures an org session
// can only ever resolve its OWN Org. Returns the receiver for chaining.
func (c *Client) WithTenantHost(host string) *Client {
	c.tenantHost = strings.TrimSpace(host)
	return c
}

// TenantHost returns the configured Organization console host (may be empty).
func (c *Client) TenantHost() string { return c.tenantHost }

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
	return c.getWithHeaders(ctx, path, bearer, nil, out)
}

// getWithHeaders is get() with caller-supplied extra request headers (e.g.
// X-Tenant-Host for the own-org read path, #5516). The bearer + accept
// headers are set identically to get().
func (c *Client) getWithHeaders(ctx context.Context, path, bearer string, extra map[string]string, out any) error {
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
	for k, v := range extra {
		if k != "" && v != "" {
			req.Header.Set(k, v)
		}
	}

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

// post issues an authenticated POST with a JSON body and decodes a 2xx
// JSON body into out. Non-2xx → *APIError carrying the upstream status (so
// 403→403 parity holds — the write side enforces the SAME tier/Org gate the
// read side does). bearer is forwarded verbatim as the Authorization header
// AND the session cookie, identical to get(), covering both the gateway and
// the catalyst-api RequireSession write paths.
//
// NOTE: HandleApplicationInstall (the create seam) widens several semantic
// errors (bad body / forbidden / conflict) into an HTTP 200 envelope that
// carries an `httpStatus` / `status` token in the body (the qa-loop
// matrix-runner wire-shape contract). That is the upstream endpoint's
// behaviour and the thin facade surfaces it verbatim — so a genuine
// HTTP-403 (e.g. the gateway/RequireSession rejecting the bearer) is
// surfaced as a 403 APIError, while the handler-level "forbidden" envelope
// arrives as a 200 body the tool layer inspects. Both paths are exercised
// by the tool tests.
func (c *Client) post(ctx context.Context, path, bearer string, in, out any) error {
	return c.postWithHeaders(ctx, path, bearer, nil, in, out)
}

// postWithHeaders is post() with caller-supplied extra request headers (e.g.
// X-Tenant-Host for the org-scoped install path, #4116). The bearer +
// content/accept headers are set identically to post().
func (c *Client) postWithHeaders(ctx context.Context, path, bearer string, extra map[string]string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("catalyst-api: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("catalyst-api: build request: %w", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Cookie", "catalyst_session="+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range extra {
		if k != "" && v != "" {
			req.Header.Set(k, v)
		}
	}

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

	// Environment — the Application's Environment ref (`<org>-<env_type>`,
	// e.g. acme-prod). The Sovereign-wide list endpoint does not project it
	// (list_environments derives the partition from Namespace there), but the
	// own-org endpoint DOES (spec.environmentRef verbatim), and it is a
	// strictly better grouping key than the namespace. Empty on the
	// deployment-addressed path.
	Environment string `json:"environment,omitempty"`

	// Topology — the instance's resolved placement chip (singleton,
	// active-active, active-hot-standby, …) as the console renders it. Only
	// projected by the own-org endpoint.
	Topology string `json:"topology,omitempty"`

	// ExternalURL — the instance's front-door launch URL when an HTTPRoute
	// fronts it. Only projected by the own-org endpoint; empty for workloads
	// with no HTTP surface.
	ExternalURL string `json:"externalURL,omitempty"`
}

// orgAppsResponse mirrors the `sovereignAppsResponse` envelope
// HandleOrgApplications returns (products/catalyst/bootstrap/api/internal/
// handler/sovereign.go). It is the per-Org instance-card projection the
// customer console's AppsPage consumes — the SAME wire shape as
// /api/v1/sovereign/apps, restricted server-side to the caller's own Org
// namespace.
type orgAppsResponse struct {
	Apps        []orgAppItem `json:"apps"`
	GeneratedAt string       `json:"generatedAt,omitempty"`
}

// orgAppItem is the subset of `sovereignAppItem` the MCP projects. `ID` is
// the Application CR / HelmRelease name; `Status` is the card status
// vocabulary (installed | installing | available | bootstrap) rather than the
// CR `status.phase` the Sovereign-wide list reports.
type orgAppItem struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Environment string `json:"environment,omitempty"`
	Blueprint   string `json:"blueprint,omitempty"`
	Version     string `json:"version,omitempty"`
	Topology    string `json:"topology,omitempty"`
	ExternalURL string `json:"externalURL,omitempty"`
	Instance    bool   `json:"instance,omitempty"`
}

// orgStatusToPhase maps the own-org endpoint's CARD-status vocabulary onto
// the CR-phase vocabulary the deployment-addressed list reports, so
// `list_applications` speaks one language regardless of which seam served it.
// An unrecognised value is passed through verbatim rather than silently
// coerced — the facade never invents a status it was not told.
func orgStatusToPhase(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "installed", "bootstrap":
		return "Ready"
	case "installing":
		return "Installing"
	case "available":
		return "NotInstalled"
	default:
		return strings.TrimSpace(status)
	}
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

// ListApplicationsOrg calls GET /api/v1/org/applications — the own-org read
// path an Org-scoped session MUST use (#5516).
//
// WHY this exists rather than passing a deployment_id to ListApplications:
// the deployment-addressed seam `/api/v1/sovereigns/{id}/applications` is NOT
// in the catalyst-api's orgSafePathPrefixes allowlist, so OrgScopeGuard 403s
// every Org-scoped session (tier=org-admin) on it — deny-by-default, the
// #4110 privilege-escalation fix. An Org bearer therefore CANNOT read that
// seam no matter what claims it carries: stamping a deployment_id onto the
// bearer only converts the MCP-side "no deployment binding" error into an
// upstream 403 `org-scoped-forbidden`. The own-org route is the seam that is
// actually reachable, exactly as CreateApplicationOrg already is for writes.
//
// The Org namespace is resolved SERVER-SIDE from X-Tenant-Host against the
// tenant registry, with the #4110 binding ensuring an Org session can only
// ever resolve its OWN Org — so the rows come back pre-confined and the
// facade does no client-side namespace filtering on this path.
//
// The response is normalised into the SAME ApplicationListResponse the
// deployment-addressed list returns, so the tool layer is seam-agnostic. Only
// rows the endpoint marked `instance:true` are surfaced — a catalog/blueprint
// row is not a running Application.
func (c *Client) ListApplicationsOrg(ctx context.Context, bearer string) (*ApplicationListResponse, error) {
	var raw orgAppsResponse
	headers := map[string]string{}
	if c.tenantHost != "" {
		headers["X-Tenant-Host"] = c.tenantHost
	}
	if err := c.getWithHeaders(ctx, "/api/v1/org/applications", bearer, headers, &raw); err != nil {
		return nil, err
	}
	out := &ApplicationListResponse{Kind: "ApplicationList", Items: []ApplicationItem{}}
	for _, a := range raw.Apps {
		if !a.Instance {
			continue
		}
		name := strings.TrimSpace(a.ID)
		if name == "" {
			name = strings.TrimSpace(a.Title)
		}
		if name == "" {
			continue
		}
		out.Items = append(out.Items, ApplicationItem{
			Kind:        "Application",
			Name:        name,
			Blueprint:   a.Blueprint,
			Version:     a.Version,
			Phase:       orgStatusToPhase(a.Status),
			Environment: a.Environment,
			Topology:    a.Topology,
			ExternalURL: a.ExternalURL,
		})
	}
	out.Total = len(out.Items)
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

// WhoamiResponse mirrors whoamiResponse in
// products/catalyst/bootstrap/api/internal/handler/auth.go. It is the
// authoritative identity verdict for a session token: catalyst-api verifies
// the token with the SAME key it minted the session with, then reports the
// resolved (mode, tier, org, deployment) triple.
type WhoamiResponse struct {
	Email         string `json:"email"`
	Sub           string `json:"sub"`
	Verified      bool   `json:"verified"`
	DeploymentID  string `json:"deploymentId,omitempty"`
	SovereignFQDN string `json:"sovereignFQDN,omitempty"`
	Mode          string `json:"mode,omitempty"` // "sovereign" | "organization"
	Tier          string `json:"tier,omitempty"` // "owner" | "admin" | "viewer" | …
	OrgScoped     bool   `json:"orgScoped,omitempty"`
	Org           string `json:"org,omitempty"` // Organization slug in Org context
}

// Whoami calls GET /api/v1/whoami — catalyst-api's own identity endpoint.
// The MCP uses it as the authority for SESSION tokens it cannot verify
// locally: the MCP is configured with the mothership HANDOVER public key,
// but catalyst-api mints post-handover session tokens with a DIFFERENT
// (deployment-local) key whose public half the MCP does not hold (#5175).
// Rather than ship that key to every MCP, the facade delegates the verdict
// to catalyst-api — which IS the session-token issuer/authority. A non-2xx
// (e.g. 401 on a bad/expired/tampered token) returns *APIError so the caller
// fails closed.
func (c *Client) Whoami(ctx context.Context, bearer string) (*WhoamiResponse, error) {
	var out WhoamiResponse
	if err := c.get(ctx, "/api/v1/whoami", bearer, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Write: create Application (POST .../applications) ────────────────────

// ApplicationBlueprintRef mirrors `applicationBlueprintRef` in the
// catalyst-api install handler — the {name, version} pin of the Blueprint
// to install.
type ApplicationBlueprintRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ApplicationPlacement mirrors the install handler's `applicationPlacement`
// subset the MCP exposes: the topology mode + regions. The instance-WHERE
// fields (vcluster/clusters) are left to the catalyst-api default-placement
// resolution so the agent's create stays as ergonomic as the console's
// basic Install flow.
type ApplicationPlacement struct {
	Mode    string   `json:"mode,omitempty"`
	Regions []string `json:"regions,omitempty"`
}

// CreateApplicationRequest is the body POSTed to the install endpoint. It
// is the EXACT long-form `applicationInstallRequest` shape the console
// Install button posts — the MCP reuses the same wire contract rather than
// inventing a parallel one. Fields the caller omits are filled by the
// catalyst-api's applicationInstallRequestNormalize (EnvironmentRef
// defaults to "<org>-prod"; Placement defaults to a single-region
// "primary"), so a minimal {blueprintRef, name, organizationRef} body is
// a valid create.
type CreateApplicationRequest struct {
	BlueprintRef    ApplicationBlueprintRef `json:"blueprintRef"`
	Name            string                  `json:"name"`
	OrganizationRef string                  `json:"organizationRef"`
	EnvironmentRef  string                  `json:"environmentRef,omitempty"`
	Parameters      map[string]any          `json:"parameters,omitempty"`
	Placement       *ApplicationPlacement   `json:"placement,omitempty"`
}

// CreateApplication calls POST /api/v1/sovereigns/{depID}/applications. The
// install endpoint (HandleApplicationInstall) ensures the Org namespace and
// writes the Application CR — the MCP reuses that seam verbatim. The
// response is returned as a generic map (the install envelope carries
// kind/name/namespace/uid/status plus the endpoint's httpStatus/applied
// wire-shape fields) so the agent surfaces exactly what the console's
// post-install banner reads, without forcing a brittle struct on a still-
// evolving response shape.
func (c *Client) CreateApplication(ctx context.Context, depID string, req CreateApplicationRequest, bearer string) (map[string]any, error) {
	var out map[string]any
	if err := c.post(ctx, fmt.Sprintf("/api/v1/sovereigns/%s/applications", depID), bearer, req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateApplicationOrg calls POST /api/v1/org/applications — the Org-scoped
// install path (#4116). It is the seam an Org-scoped customer session
// (tier=org-admin) uses: the Sovereign seam /api/v1/sovereigns/{id}/...
// 403s for org sessions (OrgScopeGuard, the #4110/#4112 fix), so the agent's
// forwarded org bearer must use this own-org route instead. The target Org
// namespace is resolved SERVER-SIDE from the X-Tenant-Host header against the
// tenant registry (the catalyst-api forces the CR into the caller's own
// namespace and ignores any organizationRef in the body), with the #4110
// binding ensuring an org session can only ever create in its OWN Org.
//
// req.OrganizationRef is irrelevant here (the server forces the namespace),
// so a minimal {blueprintRef, name, [parameters]} body suffices.
func (c *Client) CreateApplicationOrg(ctx context.Context, req CreateApplicationRequest, bearer string) (map[string]any, error) {
	var out map[string]any
	headers := map[string]string{}
	if c.tenantHost != "" {
		headers["X-Tenant-Host"] = c.tenantHost
	}
	if err := c.postWithHeaders(ctx, "/api/v1/org/applications", bearer, headers, req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
