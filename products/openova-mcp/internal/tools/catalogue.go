// catalogue.go — the OpenOva MCP tool catalogue.
//
// The READ tools (whoami, list_organizations, list_environments,
// list_applications, get_application) are Org-scoped (RequiredContext is
// empty so they are offered in both Org + Sovereign contexts, since a
// sovereign-admin can read any Org).
//
// The FIRST WRITE tool — create_application (#3988, UAT rows 221-223) —
// ships alongside them: it reuses the SAME UI create seam
// (HandleApplicationInstall, POST /api/v1/sovereigns/{id}/applications) the
// console Install button posts to, with the SAME Org-scope enforcement the
// read side uses — an Org-scoped token may create only in its own Org; a
// sovereign-admin may create in any Org. A cross-Org create returns
// ErrForbidden → MCP 403 (exact parity with the read-side cross-Org get).
// The remaining write/mutating + Sovereign-only tools (deployments.*,
// vouchers.*, cutover.*, placement.*) stay DEFERRED to follow-ups per
// #3988 §5.
//
// Org scoping is enforced at the handler boundary: an Org-context caller
// only ever sees/touches data for their own OrgID (the catalyst-api
// endpoint is addressed by the caller's bound deployment; Application items
// are filtered to the caller's Org namespace on read, and the target
// organizationRef is gated to the caller's Org on create). A sovereign-
// admin sees/acts on the full set unfiltered.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// catalogue enumerates every tool the first slice advertises.
func catalogue() []Tool {
	emptyObj := map[string]any{"type": "object", "additionalProperties": false}

	return []Tool{
		{
			Name:        "whoami",
			Description: "Return the resolved caller identity the MCP server sees: realm context (organization|sovereign), tier, Org scope, deployment, email. Mirrors the console's /auth/me.",
			InputSchema: emptyObj,
			MinTier:     identity.TierViewer,
			Handler:     handleWhoami,
		},
		{
			Name:        "list_organizations",
			Description: "List the Organizations the caller can see. In Org context this is the caller's own Organization only; a sovereign-admin sees every Organization on the Sovereign. Thin facade over GET /api/v1/organizations.",
			InputSchema: emptyObj,
			MinTier:     identity.TierViewer,
			Handler:     handleListOrganizations,
		},
		{
			Name:        "list_environments",
			Description: "List the Environments (env-type partitions: dev/staging/prod) visible to the caller, derived from the Applications running in the caller's Organization. Scoped to the caller's Org in Org context.",
			InputSchema: emptyObj,
			MinTier:     identity.TierViewer,
			Handler:     handleListEnvironments,
		},
		{
			Name:        "list_applications",
			Description: "List the Applications in the caller's Organization, with blueprint, version, namespace, and phase — exactly what the console Applications page shows for that user. Thin facade over GET /api/v1/sovereigns/{id}/applications, Org-scoped.",
			InputSchema: emptyObj,
			MinTier:     identity.TierViewer,
			Handler:     handleListApplications,
		},
		{
			Name:        "get_application",
			Description: "Get a single Application by name in the caller's Organization (full status + spec the console detail view renders). Org-scoped: a name outside the caller's Org is not returned.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Application name (metadata.name)."},
				},
			},
			MinTier: identity.TierViewer,
			Handler: handleGetApplication,
		},
		{
			Name:        "create_application",
			Description: "Install (create) an Application in the caller's Organization from a Blueprint — the SAME action as the console's Install button. Reuses POST /api/v1/sovereigns/{id}/applications (the shared create seam), so the Org namespace is ensured and the Application CR is written by the catalyst-api exactly once. RBAC-scoped: an Org-scoped caller may create ONLY in their own Organization (a cross-Org organizationRef is forbidden); a sovereign-admin may create in any Organization. Requires tier-admin or higher (mirrors the console Install gate).",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"blueprint", "name"},
				"properties": map[string]any{
					"blueprint":      map[string]any{"type": "string", "description": "Blueprint to install, e.g. bp-wordpress (must start with bp-)."},
					"version":        map[string]any{"type": "string", "description": "Blueprint version to pin, e.g. 1.2.3. Required by the catalyst-api install validator."},
					"name":           map[string]any{"type": "string", "description": "Application name (metadata.name): RFC-1123 lowercase alphanumeric + hyphens, 1-63 chars. Doubles as the app's purpose."},
					"organization":   map[string]any{"type": "string", "description": "Target Organization (slug or FQDN, e.g. acme or hw178.omani.works). OPTIONAL in Org context — defaults to the caller's own Org; a value naming a DIFFERENT Org is forbidden for an Org-scoped caller. REQUIRED for a sovereign-admin (no implicit Org)."},
					"environment":    map[string]any{"type": "string", "description": "Target Environment ref, e.g. acme-prod. Optional — defaults to <organization>-prod."},
					"placement_mode": map[string]any{"type": "string", "description": "Topology mode: singleton (default), active-active, active-hot-standby, active-passive."},
					"regions":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Region IDs the instance targets. Optional — defaults to the Sovereign's primary region."},
					"parameters":     map[string]any{"type": "object", "description": "Blueprint parameters (validated against the Blueprint's configSchema by the catalyst-api).", "additionalProperties": true},
				},
			},
			MinTier: identity.TierAdmin,
			Handler: handleCreateApplication,
		},
	}
}

// ── handlers ─────────────────────────────────────────────────────────────

func handleWhoami(_ context.Context, id *identity.Identity, _ *catalystapi.Client, _ json.RawMessage) (any, error) {
	return map[string]any{
		"email":           id.Email,
		"context":         string(id.Context),
		"tier":            id.Tier.String(),
		"org_id":          id.OrgID,
		"deployment_id":   id.DeploymentID,
		"sovereign_fqdn":  id.SovereignFQDN,
		"sovereign_admin": id.Tier == identity.TierSovereignAdmin,
	}, nil
}

func handleListOrganizations(ctx context.Context, id *identity.Identity, api *catalystapi.Client, _ json.RawMessage) (any, error) {
	if api == nil {
		return nil, fmt.Errorf("catalyst-api client not configured")
	}
	resp, err := api.ListOrganizations(ctx, bearerOf(id))
	if err != nil {
		return nil, err
	}
	items := resp.Items
	// Org scoping: an Org-context caller sees only their own Organization.
	// A sovereign-admin sees the full list.
	if id.Context == identity.ContextOrganization {
		items = filterOrgs(items, id.OrgID)
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}

func handleListApplications(ctx context.Context, id *identity.Identity, api *catalystapi.Client, _ json.RawMessage) (any, error) {
	if api == nil {
		return nil, fmt.Errorf("catalyst-api client not configured")
	}
	depID, err := requireDeployment(id)
	if err != nil {
		return nil, err
	}
	resp, err := api.ListApplications(ctx, depID, bearerOf(id))
	if err != nil {
		return nil, err
	}
	items := resp.Items
	if id.Context == identity.ContextOrganization {
		items = filterAppsToOrg(items, id.OrgID)
	}
	return map[string]any{"kind": resp.Kind, "items": items, "total": len(items)}, nil
}

func handleGetApplication(ctx context.Context, id *identity.Identity, api *catalystapi.Client, args json.RawMessage) (any, error) {
	if api == nil {
		return nil, fmt.Errorf("catalyst-api client not configured")
	}
	var in struct {
		Name string `json:"name"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("missing required argument: name")
	}
	depID, err := requireDeployment(id)
	if err != nil {
		return nil, err
	}
	obj, err := api.GetApplication(ctx, depID, in.Name, bearerOf(id))
	if err != nil {
		return nil, err
	}
	// Org scoping: reject an app whose namespace is not the caller's Org
	// (defense in depth — the get-by-name endpoint resolves across
	// namespaces and does NOT scope, so the facade MUST: a cross-Org name
	// must not leak another Org's data). The catalyst-api get response
	// carries `namespace` at the top level (HandleApplicationGet wire
	// shape); fall back to metadata.namespace for the raw-CR shape.
	if id.Context == identity.ContextOrganization {
		ns := nestedString(obj, "namespace")
		if ns == "" {
			ns = nestedString(obj, "metadata", "namespace")
		}
		if ns != "" && !namespaceBelongsToOrg(ns, id.OrgID) {
			return nil, fmt.Errorf("%w: application %q is not in organization %q", ErrForbidden, in.Name, id.OrgID)
		}
	}
	return obj, nil
}

// handleCreateApplication installs an Application in the caller's Org by
// forwarding the SAME install-request body the console Install button posts
// to POST /api/v1/sovereigns/{id}/applications (HandleApplicationInstall).
// The MCP reimplements NO namespace/CR logic — the catalyst-api endpoint
// ensures the Org namespace and writes the CR.
//
// RBAC parity with the read side: the target Organization must equal the
// caller's pinned Org scope in Org context (a cross-Org organizationRef is
// ErrForbidden → 403). A sovereign-admin may create in any Org and MUST
// name the target organization explicitly (no implicit Org scope). The
// catalyst-api endpoint's own tier-admin gate is the FINAL word (thin
// facade): a token that passed layer-1 visibility but lacks tier-admin on
// the live endpoint still gets the upstream 403 surfaced verbatim.
func handleCreateApplication(ctx context.Context, id *identity.Identity, api *catalystapi.Client, args json.RawMessage) (any, error) {
	if api == nil {
		return nil, fmt.Errorf("catalyst-api client not configured")
	}
	var in struct {
		Blueprint     string         `json:"blueprint"`
		Version       string         `json:"version"`
		Name          string         `json:"name"`
		Organization  string         `json:"organization"`
		Environment   string         `json:"environment"`
		PlacementMode string         `json:"placement_mode"`
		Regions       []string       `json:"regions"`
		Parameters    map[string]any `json:"parameters"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if strings.TrimSpace(in.Blueprint) == "" {
		return nil, fmt.Errorf("missing required argument: blueprint")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("missing required argument: name")
	}

	// Resolve the target Organization + enforce Org scope — the SAME
	// scoping rule the read side uses (id.OrgID is the caller's pinned Org).
	targetOrg, err := resolveCreateTargetOrg(id, in.Organization)
	if err != nil {
		return nil, err
	}

	depID, err := requireDeployment(id)
	if err != nil {
		return nil, err
	}

	req := catalystapi.CreateApplicationRequest{
		BlueprintRef:    catalystapi.ApplicationBlueprintRef{Name: strings.TrimSpace(in.Blueprint), Version: strings.TrimSpace(in.Version)},
		Name:            strings.TrimSpace(in.Name),
		OrganizationRef: targetOrg,
		EnvironmentRef:  strings.TrimSpace(in.Environment),
		Parameters:      in.Parameters,
	}
	if strings.TrimSpace(in.PlacementMode) != "" || len(in.Regions) > 0 {
		req.Placement = &catalystapi.ApplicationPlacement{
			Mode:    strings.TrimSpace(in.PlacementMode),
			Regions: in.Regions,
		}
	}

	return api.CreateApplication(ctx, depID, req, bearerOf(id))
}

// resolveCreateTargetOrg resolves + Org-scope-checks the target
// Organization for a create. In Org context the caller may omit the
// organization (it defaults to their own Org) or pass their OWN Org
// explicitly; naming a DIFFERENT Org is ErrForbidden — exact parity with
// the read-side cross-Org denial in handleGetApplication. A sovereign-admin
// must name the target Org explicitly (no implicit scope) and may name any
// Org.
func resolveCreateTargetOrg(id *identity.Identity, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if id.Context == identity.ContextOrganization {
		if id.OrgID == "" {
			return "", fmt.Errorf("%w: no organization scope on the caller's token", ErrForbidden)
		}
		if requested == "" {
			return id.OrgID, nil
		}
		if !orgRefMatches(requested, id.OrgID) {
			return "", fmt.Errorf("%w: organization %q is not the caller's organization %q", ErrForbidden, requested, id.OrgID)
		}
		return requested, nil
	}
	// Sovereign context (sovereign-admin): the target Org must be named.
	if requested == "" {
		return "", fmt.Errorf("organization is required for a sovereign-admin create (no implicit Org scope)")
	}
	return requested, nil
}

// orgRefMatches reports whether a requested organization ref names the
// caller's Org. The Org identity may be a bare slug ("acme") or a dotted
// FQDN ("hw178.omani.works"); the namespace is derived from it via the
// catalyst-api orgNamespace() slugging. We treat an exact (case-insensitive)
// match OR a match on the leading DNS label as the same Org, so a token
// scoped to the slug "hw178" accepts the FQDN "hw178.omani.works" and vice
// versa — the same Org the catalyst-api resolves them both to.
func orgRefMatches(requested, orgID string) bool {
	r := strings.ToLower(strings.TrimSpace(requested))
	o := strings.ToLower(strings.TrimSpace(orgID))
	if r == "" || o == "" {
		return false
	}
	if r == o {
		return true
	}
	return firstLabel(r) == firstLabel(o)
}

// firstLabel returns the leading DNS label (everything before the first dot).
func firstLabel(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

// handleListEnvironments derives the distinct Environments from the
// Applications visible to the caller. Catalyst has no standalone
// list-environments REST endpoint today; the console derives the env-type
// partitions from the running Applications, so the MCP does the same —
// staying a faithful facade rather than inventing a new surface.
func handleListEnvironments(ctx context.Context, id *identity.Identity, api *catalystapi.Client, _ json.RawMessage) (any, error) {
	if api == nil {
		return nil, fmt.Errorf("catalyst-api client not configured")
	}
	depID, err := requireDeployment(id)
	if err != nil {
		return nil, err
	}
	resp, err := api.ListApplications(ctx, depID, bearerOf(id))
	if err != nil {
		return nil, err
	}
	items := resp.Items
	if id.Context == identity.ContextOrganization {
		items = filterAppsToOrg(items, id.OrgID)
	}
	// Group apps by their Environment (the namespace = `<org>-<env_type>`
	// per the naming convention in CLAUDE.md §Naming).
	envs := map[string]int{}
	for _, it := range items {
		env := it.Namespace
		if env == "" {
			env = "(unknown)"
		}
		envs[env]++
	}
	out := make([]map[string]any, 0, len(envs))
	for env, n := range envs {
		out = append(out, map[string]any{"name": env, "applications": n})
	}
	return map[string]any{"items": out, "total": len(out)}, nil
}

// ── scoping helpers ──────────────────────────────────────────────────────

// bearerOf returns the raw compact JWT the caller presented, forwarded
// verbatim to the catalyst-api so the endpoint's own authz is the final
// word (thin-facade rule).
func bearerOf(id *identity.Identity) string {
	if id == nil || id.Claims == nil {
		return ""
	}
	// The shared Claims type does not retain the raw token; the dispatch
	// layer stashes it on the identity via SetRawBearer. Fall back to "".
	return id.RawBearer
}

// requireDeployment returns the deployment ID the caller's session is
// bound to (handover JWT deployment_id). The catalyst-api route table is
// addressed by `/api/v1/sovereigns/{id}/…`; without a deployment binding
// the facade cannot resolve which Sovereign to query.
func requireDeployment(id *identity.Identity) (string, error) {
	if id.DeploymentID == "" {
		return "", fmt.Errorf("no deployment binding on the caller's token (deployment_id claim required)")
	}
	return id.DeploymentID, nil
}

// filterOrgs keeps only the Organization whose slug/id matches orgID.
func filterOrgs(items []map[string]any, orgID string) []map[string]any {
	out := make([]map[string]any, 0, 1)
	for _, it := range items {
		if orgMatches(it, orgID) {
			out = append(out, it)
		}
	}
	return out
}

// orgMatches reports whether an org record's slug/id/name matches orgID.
func orgMatches(rec map[string]any, orgID string) bool {
	for _, k := range []string{"slug", "id", "name", "org_id", "organization"} {
		if v, ok := rec[k].(string); ok && strings.EqualFold(strings.TrimSpace(v), orgID) {
			return true
		}
	}
	return false
}

// filterAppsToOrg keeps only Applications whose namespace belongs to the
// caller's Org. The namespace convention is `<org>-<env_type>` (or the
// per-Org vcluster namespace `<org>`), so a prefix match on the slug is
// the scoping rule the console uses.
func filterAppsToOrg(items []catalystapi.ApplicationItem, orgID string) []catalystapi.ApplicationItem {
	out := make([]catalystapi.ApplicationItem, 0, len(items))
	for _, it := range items {
		if namespaceBelongsToOrg(it.Namespace, orgID) {
			out = append(out, it)
		}
	}
	return out
}

// namespaceBelongsToOrg reports whether a namespace belongs to orgID. The
// Organization owns namespaces named `<org>` and `<org>-<env_type>`
// (CLAUDE.md §Naming), so an exact match or a `<org>-` prefix qualifies.
func namespaceBelongsToOrg(ns, orgID string) bool {
	ns = strings.ToLower(strings.TrimSpace(ns))
	org := strings.ToLower(strings.TrimSpace(orgID))
	if org == "" || ns == "" {
		return false
	}
	return ns == org || strings.HasPrefix(ns, org+"-")
}

// nestedString safely reads a string at a nested path in a generic map.
func nestedString(obj map[string]any, path ...string) string {
	cur := any(obj)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}
