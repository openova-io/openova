// Package tools is the OpenOva MCP tool catalogue + the two-layer RBAC
// gate (#3988 §4.1).
//
//	Layer 1 — tools/list filtering: List(id) returns ONLY the tools the
//	          caller's resolved (context, tier) authorizes, so the agent's
//	          surface == the user's UI surface. An Org owner never even
//	          SEES the Sovereign-scoped tools.
//	Layer 2 — tools/call re-auth: Call(id, …) independently re-checks the
//	          tool's required (context, tier) + Org pin against the SAME
//	          identity, so a hand-crafted call to a filtered-out tool is
//	          still denied with a forbidden error.
//
// First slice = read-only Org-scoped tools only. Every data tool is a
// THIN facade that forwards the caller's bearer to the real catalyst-api
// (see internal/catalystapi). The registry holds no privileged token.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// ErrForbidden is returned by Call when the caller's identity does not
// satisfy a tool's required (context, tier). The dispatch layer maps it to
// the parity-403 the UI endpoint would return.
var ErrForbidden = errors.New("forbidden")

// ErrUnknownTool is returned by Call for a name not in the catalogue.
var ErrUnknownTool = errors.New("unknown tool")

// HandlerFunc is the shape every tool implementation conforms to. It
// receives the resolved caller Identity, the backend client, and the raw
// arguments JSON; returns any JSON-marshalable value or an error.
type HandlerFunc func(ctx context.Context, id *identity.Identity, api *catalystapi.Client, args json.RawMessage) (any, error)

// Tool is one MCP tool the server advertises.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`

	// RequiredContext — when non-empty, the tool is only visible/callable
	// in that realm context. Empty = visible in BOTH contexts. The
	// Sovereign context is a superset, so a tool requiring
	// ContextOrganization is ALSO offered to a sovereign-admin (who can
	// act on any Org); a tool requiring ContextSovereign is NEVER offered
	// in an Org context.
	RequiredContext identity.Context `json:"-"`

	// MinTier — the lowest tier that may see/call the tool. Read tools
	// default to TierViewer.
	MinTier identity.Tier `json:"-"`

	// Handler runs the tool.
	Handler HandlerFunc `json:"-"`
}

// Registry is the in-process catalogue + backend client.
type Registry struct {
	tools map[string]Tool
	api   *catalystapi.Client
}

// NewRegistry builds the catalogue wired to the supplied catalyst-api
// client. Pass nil api only in unit tests that exercise the gate shape
// without a backend.
func NewRegistry(api *catalystapi.Client) *Registry {
	r := &Registry{tools: map[string]Tool{}, api: api}
	for _, t := range catalogue() {
		r.tools[t.Name] = t
	}
	return r
}

// API exposes the wired catalyst-api client (nil in backend-less unit tests)
// so the server's identity resolver can fall back to catalyst-api's /whoami
// when a session token cannot be verified locally (#5175). Returns nil if the
// registry was built with no backend.
func (r *Registry) API() *catalystapi.Client {
	if r == nil {
		return nil
	}
	return r.api
}

// visible reports whether the tool is visible to the given identity under
// layer-1 (and gate-checked identically under layer-2). A nil identity
// sees nothing.
func (r *Registry) visible(t Tool, id *identity.Identity) bool {
	if id == nil {
		return false
	}
	if !id.Tier.AtLeast(t.MinTier) {
		return false
	}
	if t.RequiredContext == "" {
		return true // visible in both contexts
	}
	switch t.RequiredContext {
	case identity.ContextSovereign:
		// Sovereign-scoped tools are ONLY visible in the Sovereign
		// context. An Org-context caller never sees them.
		return id.Context == identity.ContextSovereign
	case identity.ContextOrganization:
		// Org-scoped tools are visible to an Org caller AND to a
		// sovereign-admin (Sovereign context is a superset).
		return id.Context == identity.ContextOrganization || id.Context == identity.ContextSovereign
	default:
		return false
	}
}

// List returns the tools visible to id, sorted by name (layer 1).
func (r *Registry) List(id *identity.Identity) []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if r.visible(t, id) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call re-authorizes (layer 2) then invokes the named tool. Returns
// ErrUnknownTool for an unknown name and ErrForbidden when id fails the
// gate — even if the name is real, a caller who cannot see it cannot call
// it.
func (r *Registry) Call(ctx context.Context, id *identity.Identity, name string, args json.RawMessage) (any, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	if id == nil {
		return nil, fmt.Errorf("%w: unauthenticated", ErrForbidden)
	}
	if !r.visible(t, id) {
		// Identical gate to layer 1 → identical forbidden semantics the
		// UI endpoint would return for an out-of-scope user.
		return nil, fmt.Errorf("%w: tool %q not authorized for context=%s tier=%s",
			ErrForbidden, name, id.Context, id.Tier)
	}
	if t.Handler == nil {
		return map[string]any{"status": "not_implemented", "tool": name}, nil
	}
	return t.Handler(ctx, id, r.api, args)
}

// Has reports whether the catalogue contains a tool by name (test helper).
func (r *Registry) Has(name string) bool { _, ok := r.tools[name]; return ok }
