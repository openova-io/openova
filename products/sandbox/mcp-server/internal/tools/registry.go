// Package tools is the MCP tool catalogue for openova-sandbox-mcp.
//
// Each tool is registered with a fully-qualified name (matching the
// namespaces enumerated in products/sandbox/docs/architecture.md §3)
// plus a short description. Wave 2 shipped stubs only — every Call()
// returned {"status":"not_implemented"}. Wave 8 (this file's current
// shape) replaces the stubs for:
//
//   - gitea.repo.list / gitea.repo.get
//   - gitea.pr.list / gitea.pr.get
//   - k8s.read.get / k8s.read.list / k8s.read.watch
//   - sandbox.session.whoami / sandbox.session.info
//
// All other namespaces (sandbox.db.*, sandbox.auth.*, sandbox.stripe.*,
// sandbox.preview.*, k8s.write.*) remain stubbed and continue to return
// not_implemented until Wave 8+ ships their backends.
//
// Wire model recap (architecture.md §3):
//
//   - The agent process (claude / cursor / qwen / aider / opencode)
//     speaks MCP JSON-RPC over stdio to this server.
//   - The server speaks HTTPS to the Sovereign control plane using the
//     per-Sandbox PAT injected into the pod env as SANDBOX_TOKEN.
//   - Every tool call is authz'd against the bearer's Claims
//     (core/services/shared/auth) before reaching the backend.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// Tool is one MCP tool the server advertises over stdio JSON-RPC.
type Tool struct {
	// Name is the MCP tool name (the catalogue ID). Dotted; lowercased.
	Name string `json:"name"`

	// Description is the one-line summary the agent sees during
	// MCP `tools/list`.
	Description string `json:"description"`

	// InputSchema is a JSON Schema fragment for the tool's arguments.
	InputSchema map[string]any `json:"inputSchema"`

	// Handler runs the tool. Stubs (sandbox.db.*, sandbox.auth.*, etc.)
	// have Handler==nil; Registry.Call returns notImplemented for them.
	Handler HandlerFunc `json:"-"`

	// RequiredCapability — when non-empty, Registry.Call rejects any
	// invocation whose Claims do not carry this capability string in
	// their Capabilities list (architecture.md §3 RBAC). Empty means
	// any authenticated bearer may invoke (read-only tools).
	RequiredCapability string `json:"-"`
}

// HandlerFunc is the shape every real tool implementation conforms to.
// Receives the carry-context (which holds Claims + Env via the keys in
// this package) and the raw arguments JSON. Returns any JSON-marshalable
// value or an error.
type HandlerFunc func(ctx context.Context, args json.RawMessage) (any, error)

// Registry is the in-process catalogue. Safe for concurrent reads.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	// env is the per-process server environment (token-signing secret,
	// org_id, gitea URL/token, kubeconfig, etc.) all tool handlers read
	// at call time. Stored on Registry instead of context because every
	// handler needs it and threading it through arguments was noisier.
	env *Env
}

// Env carries the per-process configuration every tool handler reads.
// Populated from the pod's environment in main.go via NewEnvFromOS().
type Env struct {
	// OrgID — the operator's Organization slug (`acme`, `bankdhofar`).
	// Matched against Claims.OrgID on every tool call; mismatch → 403.
	OrgID string

	// SandboxID — the Sandbox CR name (`emrah`). Surfaced via
	// sandbox.session.info.
	SandboxID string

	// SandboxNamespace — k8s namespace housing the Sandbox pod
	// + its resources inside the Org vcluster (sandbox-<owner-uid>).
	SandboxNamespace string

	// SovereignFQDN — the Sovereign's primary FQDN (`acme.openova.io`).
	SovereignFQDN string

	// SandboxRepos — comma-separated list of `<org>/<repo>` entries
	// the Sandbox has cloned. Surfaced via sandbox.session.info.
	SandboxRepos []string

	// JWTSecret — HS256 secret used to validate per-call bearer tokens.
	// Sourced from the Sandbox-mounted Secret `sandbox-jwt-secret`.
	// Empty → accept tokens unsigned (test mode); a real deployment
	// MUST set this.
	JWTSecret []byte

	// SandboxToken — the long-lived PAT minted by the sandbox-controller
	// at pod create time. Used as the fall-back bearer when a tool call
	// arrives without an explicit _auth.token argument.
	SandboxToken string

	// GiteaBaseURL — the Sovereign's Gitea root (no /api/v1 suffix).
	GiteaBaseURL string

	// GiteaToken — the per-Sandbox Gitea PAT (NOT the user's; minted by
	// sandbox-controller from a Gitea machine account). Used by the
	// gitea.* tool handlers.
	GiteaToken string

	// KubeconfigPath — path to a kubeconfig pointing at the Org vcluster.
	// Empty → use in-cluster config (the Sandbox pod's SA).
	KubeconfigPath string
}

// claimsCtxKey is the unexported context key under which the
// per-call bearer's Claims live. main.go stuffs them on the context
// after validating the bearer; tool handlers retrieve via ClaimsFrom().
type claimsCtxKey struct{}

// ClaimsFrom returns the per-call Claims if present on ctx, or
// (nil, false) when the request was unauthenticated.
func ClaimsFrom(ctx context.Context) (*sharedauth.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey{}).(*sharedauth.Claims)
	return c, ok && c != nil
}

// WithClaims returns a new context carrying claims. The MCP server's
// dispatch path uses this once per `tools/call` after validating the
// bearer.
func WithClaims(ctx context.Context, claims *sharedauth.Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, claims)
}

// EnvFrom returns the Registry's Env via the supplied ctx for the
// handler chain. Provided as a context-helper so unit tests can swap
// envs without touching the Registry singleton.
func EnvFrom(ctx context.Context) *Env {
	if e, ok := ctx.Value(envCtxKey{}).(*Env); ok {
		return e
	}
	return nil
}

// WithEnv attaches env to ctx; tool dispatch installs the Registry's
// env on every call.
func WithEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envCtxKey{}, env)
}

type envCtxKey struct{}

// NewRegistry returns a registry pre-populated with every catalogue
// stub from architecture.md §3. Wave 8 entries (gitea.* / k8s.read.* /
// sandbox.session.*) carry real Handler funcs; the rest remain stubs.
//
// Pass nil for env in unit tests that exercise only the registry shape
// (List + stub Call); real binaries always pass a populated Env.
func NewRegistry(env *Env) *Registry {
	if env == nil {
		env = &Env{}
	}
	r := &Registry{tools: make(map[string]Tool), env: env}
	for _, t := range defaultCatalogue(env) {
		r.tools[t.Name] = t
	}
	return r
}

// Env returns the registry's Env. Used by tests + the MCP server's
// outer dispatcher when it wants to thread a copy onto a request ctx.
func (r *Registry) Env() *Env { return r.env }

// Register adds (or replaces) a tool by name. Used by tests + future
// waves to swap stubs for real handlers.
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

// CallOpts threads per-call inputs (the bearer's parsed Claims) to the
// handler chain without leaking them into the tool's argument schema.
type CallOpts struct {
	// Claims is the validated bearer claims (validated by main.go's
	// dispatch loop). May be nil for tools whose RequiredCapability is
	// empty AND env.JWTSecret is also empty (test mode).
	Claims *sharedauth.Claims
}

// Call invokes the named tool with the supplied argument blob.
//
// Authorisation: when env.JWTSecret is non-empty, the caller MUST pass
// opts.Claims; nil → 401. When the tool has a RequiredCapability, that
// string MUST be present in claims.Capabilities; absent → 403.
//
// Org scoping: every tool that touches the Org's data (gitea.*,
// k8s.read.*) checks claims.OrgID == env.OrgID and rejects on mismatch.
// session.* skip the org check (the operator always sees their own
// session).
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage, opts CallOpts) (any, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tools/call: unknown tool %q", name)
	}
	if t.Handler == nil {
		return notImplemented(name), nil
	}

	// Auth gate: only enforced when the binary was started with a JWT
	// secret. With no secret, the deployment is test/dev mode and
	// callers can omit Claims (the gitea + k8s tools still surface
	// "not configured" if their backends weren't wired).
	//
	// When Claims ARE supplied — production OR a test that mints
	// them — we always enforce the capability + org-scope checks so
	// the test surface matches production semantics.
	if len(r.env.JWTSecret) > 0 && opts.Claims == nil {
		return nil, errors.New("tools/call: unauthenticated (no bearer claims)")
	}
	if opts.Claims != nil {
		if t.RequiredCapability != "" && !opts.Claims.HasCapability(t.RequiredCapability) {
			return nil, fmt.Errorf("tools/call: forbidden (missing capability %q)", t.RequiredCapability)
		}
		// Org-scope: gitea.* + k8s.read.* must match the pod's OrgID.
		// session.* is exempt (the operator's session is the operator's
		// regardless of which Org slug their claim carries).
		if r.env.OrgID != "" && opts.Claims.OrgID != "" && opts.Claims.OrgID != r.env.OrgID && !exemptFromOrgScope(name) {
			return nil, fmt.Errorf("tools/call: forbidden (org_id mismatch: claim=%q env=%q)", opts.Claims.OrgID, r.env.OrgID)
		}
	}

	ctx = WithClaims(ctx, opts.Claims)
	ctx = WithEnv(ctx, r.env)
	return t.Handler(ctx, args)
}

// exemptFromOrgScope reports whether `name` is a tool that the
// org-scope check should skip. Today: just sandbox.session.* — those
// tools return only information the bearer's own token already
// authorised them to see.
func exemptFromOrgScope(name string) bool {
	switch name {
	case "sandbox.session.whoami", "sandbox.session.info":
		return true
	}
	return false
}

// notImplemented is the canonical stub response retained for every
// Wave 8+ tool family still on the to-do list.
func notImplemented(name string) map[string]any {
	return map[string]any{
		"status": "not_implemented",
		"tool":   name,
		"note":   "stub; real handler scheduled for Wave 8+",
	}
}

// defaultCatalogue enumerates every tool the server advertises.
// Wave 8 entries are wired to real handlers; remaining namespaces hold
// place with Handler=nil so the agent can still `tools/list` them.
func defaultCatalogue(env *Env) []Tool {
	anyObj := map[string]any{"type": "object", "additionalProperties": true}

	return []Tool{
		// gitea.* — read surface backed by core/controllers/pkg/gitea.
		{
			Name:        "gitea.repo.list",
			Description: "List repos in the Org's Gitea Org.",
			InputSchema: schemaGiteaRepoList(),
			Handler:     giteaRepoList,
		},
		{
			Name:        "gitea.repo.get",
			Description: "Get a single repo by owner/name.",
			InputSchema: schemaGiteaRepoGet(),
			Handler:     giteaRepoGet,
		},
		{
			Name:        "gitea.pr.list",
			Description: "List PRs on a repo (state=open|closed|all).",
			InputSchema: schemaGiteaPRList(),
			Handler:     giteaPRList,
		},
		{
			Name:        "gitea.pr.get",
			Description: "Get a single PR by repo + number.",
			InputSchema: schemaGiteaPRGet(),
			Handler:     giteaPRGet,
		},

		// gitea.* — write surface remains stubbed (Wave 8+).
		{Name: "gitea.pr.create", Description: "Open a PR (branch -> base) with title and body.", InputSchema: anyObj},
		{Name: "gitea.pr.merge", Description: "Merge a PR by number (admin or org-admin only).", InputSchema: anyObj},
		{Name: "gitea.issue.list", Description: "List issues on a repo.", InputSchema: anyObj},
		{Name: "gitea.issue.create", Description: "Create an issue with title and body.", InputSchema: anyObj},
		{Name: "gitea.release.list", Description: "List releases on a repo.", InputSchema: anyObj},

		// k8s.read.* — Org vcluster scope (NEVER host).
		{
			Name:        "k8s.read.get",
			Description: "GET a single object by GVK + namespace + name (Org vcluster).",
			InputSchema: schemaK8sReadGet(),
			Handler:     k8sReadGet,
		},
		{
			Name:        "k8s.read.list",
			Description: "LIST objects by GVK + namespace (Org vcluster; label selector optional).",
			InputSchema: schemaK8sReadList(),
			Handler:     k8sReadList,
		},
		{
			Name:        "k8s.read.watch",
			Description: "WATCH a kind for live updates (returns a window of events, then closes).",
			InputSchema: schemaK8sReadWatch(),
			Handler:     k8sReadWatch,
		},
		{Name: "k8s.read.logs", Description: "Fetch container logs for a pod (read-only).", InputSchema: anyObj},

		// sandbox.db.* — Wave 8+ (CNPG provisioning).
		{Name: "sandbox.db.provision", Description: "Provision a CNPG cluster (size + version). Returns Cluster CR ref.", InputSchema: anyObj},
		{Name: "sandbox.db.list", Description: "List CNPG clusters owned by this Sandbox.", InputSchema: anyObj},
		{Name: "sandbox.db.dump", Description: "Trigger a pg_dump-backed backup; returns object-store URL.", InputSchema: anyObj},
		{Name: "sandbox.db.drop", Description: "Drop a CNPG cluster owned by this Sandbox.", InputSchema: anyObj},

		// sandbox.auth.* — Wave 8+ (Keycloak management).
		{Name: "sandbox.auth.provisionRealm", Description: "Provision a Keycloak realm for an Application under this Sandbox.", InputSchema: anyObj},
		{Name: "sandbox.auth.listClients", Description: "List Keycloak clients in the Sandbox's realm.", InputSchema: anyObj},
		{Name: "sandbox.auth.registerClient", Description: "Register a new Keycloak client (id, redirect URIs).", InputSchema: anyObj},

		// sandbox.session.* — this MCP server's own metadata (Wave 8).
		{
			Name:        "sandbox.session.whoami",
			Description: "Return the claims (sub, org_id, sandbox_id, role) the server sees on the per-call bearer.",
			InputSchema: anyObj,
			Handler:     sessionWhoami,
		},
		{
			Name:        "sandbox.session.info",
			Description: "Return the Sandbox's name, namespace, attached repos, Sovereign FQDN.",
			InputSchema: anyObj,
			Handler:     sessionInfo,
		},
	}
}
