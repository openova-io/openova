// Package tools is the MCP tool catalogue for openova-sandbox-mcp.
//
// Each tool is registered with a fully-qualified name (matching the
// namespaces enumerated in products/sandbox/docs/architecture.md §3)
// plus a short description. Wave 2 ships stubs only — every Call()
// returns {"status":"not_implemented"} so the agent can list, dispatch,
// and reason about the surface end-to-end before any backend is wired.
//
// Real implementations land in Wave 3+ and replace the Handler func on
// each Tool record without touching the registry shape.
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Tool is one MCP tool the server advertises over stdio JSON-RPC.
type Tool struct {
	// Name is the MCP tool name (the catalogue ID). Dotted; lowercased.
	Name string `json:"name"`

	// Description is the one-line summary the agent sees during
	// MCP `tools/list`.
	Description string `json:"description"`

	// InputSchema is a JSON Schema fragment for the tool's arguments.
	// Wave 2 uses {} (any) for stubs; Wave 3 tightens.
	InputSchema map[string]any `json:"inputSchema"`

	// Handler runs the tool. Wave 2 stubs return not_implemented.
	Handler func(args json.RawMessage) (any, error) `json:"-"`
}

// Registry is the in-process catalogue. Safe for concurrent reads.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns a registry pre-populated with every namespace
// stub from architecture.md §3.
func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	for _, t := range defaultCatalogue() {
		r.tools[t.Name] = t
	}
	return r
}

// Register adds (or replaces) a tool by name. Used by tests + Wave 3
// to swap stubs for real handlers.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
}

// List returns every tool, sorted by name (stable for MCP `tools/list`).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call invokes the named tool with the supplied argument blob.
func (r *Registry) Call(name string, args json.RawMessage) (any, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tools/call: unknown tool %q", name)
	}
	if t.Handler == nil {
		return notImplemented(name), nil
	}
	return t.Handler(args)
}

// notImplemented is the canonical Wave 2 stub response.
func notImplemented(name string) map[string]any {
	return map[string]any{
		"status": "not_implemented",
		"tool":   name,
		"wave":   2,
		"note":   "scaffold; real handler lands in Wave 3+",
	}
}

// defaultCatalogue enumerates the four namespaces required for Wave 2
// (gitea, k8s.read, sandbox.db, sandbox.auth) plus a thin
// sandbox.session.* for the MCP server's own session metadata. Adding
// the remaining namespaces (sandbox.deploy, marketplace, flux, rag,
// etc.) is a stub list extension only — Wave 3 work.
func defaultCatalogue() []Tool {
	any := map[string]any{"type": "object", "additionalProperties": true}
	return []Tool{
		// gitea.*
		{Name: "gitea.repos.list", Description: "List repos in the Org's Gitea Org.", InputSchema: any},
		{Name: "gitea.repos.get", Description: "Get a single repo by owner/name.", InputSchema: any},
		{Name: "gitea.pr.list", Description: "List PRs on a repo.", InputSchema: any},
		{Name: "gitea.pr.create", Description: "Open a PR (branch -> base) with title and body.", InputSchema: any},
		{Name: "gitea.pr.merge", Description: "Merge a PR by number (admin or org-admin only).", InputSchema: any},
		{Name: "gitea.issue.list", Description: "List issues on a repo.", InputSchema: any},
		{Name: "gitea.issue.create", Description: "Create an issue with title and body.", InputSchema: any},
		{Name: "gitea.release.list", Description: "List releases on a repo.", InputSchema: any},

		// k8s.read.* (Org vcluster scope — never host)
		{Name: "k8s.read.get", Description: "GET a single object by GVK + namespace + name.", InputSchema: any},
		{Name: "k8s.read.list", Description: "LIST objects by GVK + namespace (label selector optional).", InputSchema: any},
		{Name: "k8s.read.watch", Description: "WATCH a kind for live updates (returns a subscription id).", InputSchema: any},
		{Name: "k8s.read.logs", Description: "Fetch container logs for a pod (read-only).", InputSchema: any},

		// sandbox.db.*
		{Name: "sandbox.db.provision", Description: "Provision a CNPG cluster (size + version). Returns Cluster CR ref.", InputSchema: any},
		{Name: "sandbox.db.list", Description: "List CNPG clusters owned by this Sandbox.", InputSchema: any},
		{Name: "sandbox.db.dump", Description: "Trigger a pg_dump-backed backup; returns object-store URL.", InputSchema: any},
		{Name: "sandbox.db.drop", Description: "Drop a CNPG cluster owned by this Sandbox.", InputSchema: any},

		// sandbox.auth.* (Keycloak realm + client management within Sandbox scope)
		{Name: "sandbox.auth.provisionRealm", Description: "Provision a Keycloak realm for an Application under this Sandbox.", InputSchema: any},
		{Name: "sandbox.auth.listClients", Description: "List Keycloak clients in the Sandbox's realm.", InputSchema: any},
		{Name: "sandbox.auth.registerClient", Description: "Register a new Keycloak client (id, redirect URIs).", InputSchema: any},

		// sandbox.session.* (this MCP server's own metadata)
		{Name: "sandbox.session.whoami", Description: "Return the claims (sub, org_id, sandbox_id, role) the server sees on its token.", InputSchema: any},
		{Name: "sandbox.session.info", Description: "Return the current Sandbox name, namespace, attached repos.", InputSchema: any},
	}
}
