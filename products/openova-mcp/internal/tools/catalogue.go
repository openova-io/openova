// catalogue.go — the first-slice OpenOva MCP tool catalogue.
//
// All tools here are READ-ONLY and Org-scoped (RequiredContext is empty so
// they are offered in both Org + Sovereign contexts, since a sovereign-
// admin can read any Org). The write/mutating + Sovereign-only tools
// (deployments.*, vouchers.*, cutover.*, placement.*) are DEFERRED to
// follow-ups per #3988 §5; this slice ships the read facade only.
//
// Org scoping is enforced at the handler boundary: an Org-context caller
// only ever sees data for their own OrgID (the catalyst-api endpoint is
// addressed by the caller's bound deployment, and Application items are
// filtered to the caller's Org namespace). A sovereign-admin sees the full
// set unfiltered.
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
	// (defense in depth — the endpoint already scopes, but the facade
	// double-checks so a cross-Org name leak cannot pass through).
	if id.Context == identity.ContextOrganization {
		if ns := nestedString(obj, "metadata", "namespace"); ns != "" && !namespaceBelongsToOrg(ns, id.OrgID) {
			return nil, fmt.Errorf("%w: application %q is not in organization %q", ErrForbidden, in.Name, id.OrgID)
		}
	}
	return obj, nil
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
