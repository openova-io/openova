// Package tools is the MCP tool catalogue for openova-sandbox-mcp.
//
// Each tool is registered with a fully-qualified name (matching the
// namespaces enumerated in products/sandbox/docs/architecture.md §3)
// plus a short description. Wave 2 shipped stubs only — every Call()
// returned {"status":"not_implemented"}. Wave 8 replaced the stubs
// for the read surface; Wave 11 (this file's current shape) extends
// to the gitea write surface, k8s.read.logs, and per-Sandbox CNPG
// provisioning so agents can open PRs, file issues, merge, pull
// container logs, and stand up Postgres end-to-end:
//
//   - gitea.repo.list / gitea.repo.get
//   - gitea.pr.list / gitea.pr.get / gitea.pr.create / gitea.pr.merge
//   - gitea.issue.list / gitea.issue.get / gitea.issue.create / gitea.issue.comment
//   - k8s.read.get / k8s.read.list / k8s.read.watch / k8s.read.logs
//   - sandbox.db.provision / sandbox.db.list / sandbox.db.get /
//     sandbox.db.drop / sandbox.db.dump
//   - sandbox.auth.provisionRealm / sandbox.auth.listClients /
//     sandbox.auth.registerClient
//   - sandbox.secrets.read / sandbox.secrets.write
//   - sandbox.session.whoami / sandbox.session.info
//
// Wave 12 extends to sandbox.preview.* + marketplace.domain.* + flux.*:
//
//   - sandbox.preview.create / sandbox.preview.list /
//     sandbox.preview.status / sandbox.preview.teardown /
//     sandbox.preview.rebuild
//   - marketplace.domain.byod / marketplace.domain.subdomain
//   - flux.status / flux.reconcile / flux.suspend / flux.resume
//
// All other namespaces (sandbox.stripe.*, k8s.write.*,
// gitea.release.list) remain stubbed and continue to return
// not_implemented until later waves ship them.
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

	// OwnerUID — the Sandbox's owner UID (`emrah-baysal-at-openova-io`,
	// post-sanitisation of the operator email). The sandbox-controller
	// injects this as SANDBOX_OWNER_UID on the MCP Deployment env (see
	// core/controllers/sandbox/internal/gitops/manifests.go). It is the
	// suffix in the Sandbox namespace name (`sandbox-<owner-uid>`) AND
	// the per-Sandbox Secret-store name (`sandbox-<owner-uid>-secrets`).
	OwnerUID string

	// KeycloakAdminURL — root of the Keycloak Admin REST API
	// (e.g. `http://keycloak.keycloak.svc.cluster.local:8080`). Empty
	// → sandbox.auth.* surfaces a clear "not configured" error.
	KeycloakAdminURL string

	// KeycloakAdminToken — pre-minted Keycloak admin bearer the
	// sandbox-controller drops into the MCP pod env from a mounted
	// Secret. sandbox.auth.* uses it verbatim as the Authorization
	// header for the Admin REST API.
	KeycloakAdminToken string

	// KeycloakParentRealm — the tenant Org's parent realm under which
	// per-Sandbox realms are created (default: `master`). Used as the
	// PARENT (admin auth target) for realm CRUD; the per-Sandbox
	// realm itself is a sibling under the same Keycloak instance.
	KeycloakParentRealm string

	// DomainAPIURL — root URL of the canonical domain service
	// (`core/services/domain`), e.g.
	// `http://domain.sme.svc.cluster.local:8086`. The marketplace.*
	// tool family proxies POST /domain/byod + POST /domain/subdomains
	// here. Empty → marketplace.* surfaces a clear "not configured"
	// error rather than failing on a bad URL.
	//
	// Wired by sandbox-controller as SANDBOX_DOMAIN_API_URL on the
	// MCP Deployment env. The same value flows into the per-Sovereign
	// gateway service as DOMAIN_URL — kept separate here so the MCP
	// server can target the domain service DIRECTLY rather than via
	// the gateway (one fewer hop on a tool call).
	DomainAPIURL string

	// MarketplaceAPIURL — root URL of the marketplace-api service
	// (`core/marketplace-api`), e.g.
	// `http://marketplace-api.marketplace.svc.cluster.local:8082`.
	// Reserved for future marketplace.* tools that drive provisioning
	// (e.g. marketplace.app.deploy). Empty today on every Sandbox; the
	// Wave 12 marketplace.domain.* tools target DomainAPIURL instead.
	MarketplaceAPIURL string

	// TenantID — the tenant identifier the canonical domain service
	// uses to scope BYOD / subdomain registrations
	// (`core/services/domain/handlers/handlers.go` checkTenantMembership).
	// Distinct from OrgID because the SME tenant service identifies
	// orgs by a separate `tenant_id` UUID — sandbox-controller injects
	// it as SANDBOX_TENANT_ID once the chroot Organization controller
	// has minted the tenant row. Empty → marketplace.domain.* surfaces
	// a clear "not configured" error.
	TenantID string
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

		// gitea.* — write surface (Wave 11 wired pr.create/merge + issue.*).
		{
			Name:        "gitea.pr.create",
			Description: "Open a PR (branch -> base) with title and body.",
			InputSchema: schemaGiteaPRCreate(),
			Handler:     giteaPRCreate,
		},
		{
			Name:        "gitea.pr.merge",
			Description: "Merge a PR by number (style=merge|rebase|rebase-merge|squash; default merge).",
			InputSchema: schemaGiteaPRMerge(),
			Handler:     giteaPRMerge,
		},
		{
			Name:        "gitea.issue.list",
			Description: "List issues on a repo (state=open|closed|all, type=issues|pulls).",
			InputSchema: schemaGiteaIssueList(),
			Handler:     giteaIssueList,
		},
		{
			Name:        "gitea.issue.get",
			Description: "Get a single issue by repo + number.",
			InputSchema: schemaGiteaIssueGet(),
			Handler:     giteaIssueGet,
		},
		{
			Name:        "gitea.issue.create",
			Description: "Create an issue with title and body.",
			InputSchema: schemaGiteaIssueCreate(),
			Handler:     giteaIssueCreate,
		},
		{
			Name:        "gitea.issue.comment",
			Description: "Add a comment to an issue (or PR — Gitea conflates).",
			InputSchema: schemaGiteaIssueComment(),
			Handler:     giteaIssueComment,
		},
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
		{
			Name:        "k8s.read.logs",
			Description: "Fetch the recent N log lines for a pod container (bounded; read-only).",
			InputSchema: schemaK8sReadLogs(),
			Handler:     k8sReadLogs,
		},

		// sandbox.db.* — CNPG provisioning (Wave 11 — sandbox_db.go).
		// Every handler addresses env.SandboxNamespace; the agent
		// cannot pass a namespace argument. RequiredCapability gates
		// each call to bearers whose claims carry `sandbox.db`.
		{
			Name:               "sandbox.db.provision",
			Description:        "Provision a CNPG Postgres Cluster CR in this Sandbox's namespace (default plan: 1 instance, 5Gi, postgres 16). Returns the connection envelope.",
			InputSchema:        schemaSandboxDBProvision(),
			Handler:            sandboxDBProvision,
			RequiredCapability: "sandbox.db",
		},
		{
			Name:               "sandbox.db.list",
			Description:        "List CNPG Clusters provisioned by this Sandbox (filtered to openova.io/managed-by=openova-sandbox-mcp).",
			InputSchema:        map[string]any{"type": "object", "additionalProperties": false},
			Handler:            sandboxDBList,
			RequiredCapability: "sandbox.db",
		},
		{
			Name:               "sandbox.db.get",
			Description:        "Get a single Sandbox-managed CNPG Cluster's status + connection envelope by name.",
			InputSchema:        schemaSandboxDBNameOnly(),
			Handler:            sandboxDBGet,
			RequiredCapability: "sandbox.db",
		},
		{
			Name:               "sandbox.db.drop",
			Description:        "Delete a Sandbox-managed CNPG Cluster (CNPG operator cascades PVC/Service/Secret cleanup).",
			InputSchema:        schemaSandboxDBNameOnly(),
			Handler:            sandboxDBDrop,
			RequiredCapability: "sandbox.db",
		},
		{
			Name:               "sandbox.db.dump",
			Description:        "Create a one-shot CNPG Backup CR (barman-cloud or volumeSnapshot per cluster config). Returns the Backup CR ref + configured S3 destinationPath.",
			InputSchema:        schemaSandboxDBNameOnly(),
			Handler:            sandboxDBDump,
			RequiredCapability: "sandbox.db",
		},

		// sandbox.auth.* — Keycloak realm + OIDC client management
		// (Wave 11 — sandbox_auth.go). Every call hits the
		// sandbox-controller-injected admin bearer at
		// KEYCLOAK_ADMIN_URL; the realm scope is derived
		// deterministically from env.OrgID + env.SandboxID for
		// list/register so the agent cannot redirect to another realm.
		{
			Name:               "sandbox.auth.provisionRealm",
			Description:        "Provision a per-Sandbox Keycloak realm under the Sovereign Keycloak instance. Idempotent: returns AlreadyExists on re-call.",
			InputSchema:        schemaSandboxAuthProvisionRealm(),
			Handler:            sandboxAuthProvisionRealm,
			RequiredCapability: "sandbox.auth",
		},
		{
			Name:               "sandbox.auth.listClients",
			Description:        "List OIDC clients in this Sandbox's realm (`sandbox-<org>-<id>`).",
			InputSchema:        map[string]any{"type": "object", "additionalProperties": false},
			Handler:            sandboxAuthListClients,
			RequiredCapability: "sandbox.auth",
		},
		{
			Name:               "sandbox.auth.registerClient",
			Description:        "Register a new OIDC client in this Sandbox's realm (`sandbox-<org>-<id>`). Idempotent: returns AlreadyExists on re-call.",
			InputSchema:        schemaSandboxAuthRegisterClient(),
			Handler:            sandboxAuthRegisterClient,
			RequiredCapability: "sandbox.auth",
		},

		// sandbox.secrets.* — per-Sandbox Secret store
		// (Wave 11 — sandbox_secrets.go). Reads + writes go through
		// a single canonical K8s Secret `sandbox-<owner-uid>-secrets`
		// in env.SandboxNamespace, gated by the
		// openova.io/managed-by=openova-sandbox-mcp label so we never
		// mutate the controller's `sandbox-tokens` Secret.
		{
			Name:               "sandbox.secrets.read",
			Description:        "Read a key from the Sandbox's Secret store (`sandbox-<owner-uid>-secrets`). Returns status=NotFound when the store hasn't been created yet.",
			InputSchema:        schemaSandboxSecretsRead(),
			Handler:            sandboxSecretsRead,
			RequiredCapability: "sandbox.secrets",
		},
		{
			Name:               "sandbox.secrets.write",
			Description:        "Write a key/value into the Sandbox's Secret store (auto-creates the Secret on first write). Encrypted at rest via K8s Secret encryption-at-rest.",
			InputSchema:        schemaSandboxSecretsWrite(),
			Handler:            sandboxSecretsWrite,
			RequiredCapability: "sandbox.secrets",
		},

		// marketplace.domain.* — Wave 12 thin proxy to
		// core/services/domain (marketplace.go). Every call forwards the
		// Sandbox PAT to the domain service which performs the canonical
		// auth + WHOIS + uniqueness checks. RequiredCapability="marketplace"
		// gates the bearer.
		{
			Name:               "marketplace.domain.byod",
			Description:        "Register a Bring-Your-Own-Domain (BYOD) FQDN for the Sandbox's tenant. Returns the CNAME target the operator points at their registrar.",
			InputSchema:        schemaMarketplaceDomainBYOD(),
			Handler:            marketplaceDomainBYOD,
			RequiredCapability: "marketplace",
		},
		{
			Name:               "marketplace.domain.subdomain",
			Description:        "Reserve a `<subdomain>.<parent_zone>` under the Sovereign-managed subdomain pool (parent_zone defaults to the configured pool TLD).",
			InputSchema:        schemaMarketplaceDomainSubdomain(),
			Handler:            marketplaceDomainSubdomain,
			RequiredCapability: "marketplace",
		},

		// flux.* — Wave 12 Flux read/kick surface (flux.go). status is
		// pure-read; reconcile/suspend/resume mutate exactly one
		// well-known annotation or spec field on a tenant-scoped HR /
		// Kustomization. The dynamic client's RBAC gates which
		// namespaces the SA can patch — the MCP server itself does
		// not maintain a separate allow-list. RequiredCapability="flux"
		// gates every call regardless of read vs write.
		{
			Name:               "flux.status",
			Description:        "List HelmReleases + Kustomizations in the Sandbox's tenant namespaces with their Ready condition + last applied revision.",
			InputSchema:        schemaFluxStatus(),
			Handler:            fluxStatus,
			RequiredCapability: "flux",
		},
		{
			Name:               "flux.reconcile",
			Description:        "Force-run a Flux reconcile on a tenant-scoped HelmRelease or Kustomization by setting the `reconcile.fluxcd.io/requestedAt` annotation.",
			InputSchema:        schemaFluxMutation(),
			Handler:            fluxReconcile,
			RequiredCapability: "flux",
		},
		{
			Name:               "flux.suspend",
			Description:        "Set spec.suspend=true on a tenant-scoped HelmRelease or Kustomization so Flux stops reconciling it.",
			InputSchema:        schemaFluxMutation(),
			Handler:            fluxSuspend,
			RequiredCapability: "flux",
		},
		{
			Name:               "flux.resume",
			Description:        "Set spec.suspend=false on a tenant-scoped HelmRelease or Kustomization so Flux resumes reconciling.",
			InputSchema:        schemaFluxMutation(),
			Handler:            fluxResume,
			RequiredCapability: "flux",
		},

		// rag.search — Wave 12 lean text-grep stub over /repo PVC
		// (rag_skills.go). Returns matches[]{path,line,snippet}; flips
		// pendingApi=true when the PVC isn't mounted yet so the agent
		// can branch on "index not ready" vs "no hits".
		{
			Name:               "rag.search",
			Description:        "Text-grep over the Sandbox's mounted /repo PVC. Wave 12 stub for the per-Org hybrid RAG (architecture.md §3).",
			InputSchema:        schemaRagSearch(),
			Handler:            ragSearch,
			RequiredCapability: "rag",
		},

		// skills.* — Wave 12 hardcoded catalogue (rag_skills.go). Real
		// OCI-backed catalogue ships in a later wave; the wire envelope
		// is intentionally stable so the agent's parsing layer carries
		// forward.
		{
			Name:               "skills.list",
			Description:        "List available skill packs. Wave 12 returns a hardcoded catalogue (pendingOCI=true on every entry).",
			InputSchema:        map[string]any{"type": "object", "additionalProperties": false},
			Handler:            skillsList,
			RequiredCapability: "skills",
		},
		{
			Name:               "skills.get",
			Description:        "Fetch a single skill pack's manifest by name. Wave 12 returns a stub manifest; switches to OCI fetch in a later wave.",
			InputSchema:        schemaSkillsGet(),
			Handler:            skillsGet,
			RequiredCapability: "skills",
		},

		// sandbox.preview.* — per-PR preview Deployments
		// (Wave 12 — sandbox_preview.go). Every handler is scoped to
		// env.SandboxNamespace and gated by the
		// openova.io/managed-by=openova-sandbox-mcp +
		// openova.io/preview-pr labels so list/teardown can address the
		// triple by PR number without colliding with operator-managed
		// workloads (pty-server, openova-sandbox-mcp) in the same ns.
		// Hostname pattern: `pr-<num>.<app>.sandbox.<sov-fqdn>` — attaches
		// to the same Cilium Gateway the sandbox-controller already
		// renders for pty-server.
		{
			Name:               "sandbox.preview.create",
			Description:        "Create a per-PR preview triple (Deployment + Service + HTTPRoute) in this Sandbox's namespace. Hostname pattern: `pr-<num>.<app>.sandbox.<sov-fqdn>`.",
			InputSchema:        schemaSandboxPreviewCreate(),
			Handler:            sandboxPreviewCreate,
			RequiredCapability: "sandbox.preview",
		},
		{
			Name:               "sandbox.preview.list",
			Description:        "List active per-PR previews in this Sandbox (filtered to openova.io/managed-by=openova-sandbox-mcp + openova.io/preview-pr label).",
			InputSchema:        map[string]any{"type": "object", "additionalProperties": false},
			Handler:            sandboxPreviewList,
			RequiredCapability: "sandbox.preview",
		},
		{
			Name:               "sandbox.preview.status",
			Description:        "Fetch a per-PR preview's Deployment status + Pod readiness (containerStatuses with waiting reason, restartCount).",
			InputSchema:        schemaSandboxPreviewPRNumber(),
			Handler:            sandboxPreviewStatus,
			RequiredCapability: "sandbox.preview",
		},
		{
			Name:               "sandbox.preview.teardown",
			Description:        "Delete a per-PR preview (Deployment + Service + HTTPRoute). Foreground propagation on the Deployment so Pods + ReplicaSets are fully reaped.",
			InputSchema:        schemaSandboxPreviewPRNumber(),
			Handler:            sandboxPreviewTeardown,
			RequiredCapability: "sandbox.preview",
		},
		{
			Name:               "sandbox.preview.rebuild",
			Description:        "Update a per-PR preview's container image + bump kubectl.kubernetes.io/restartedAt to force a rollout (works even when the image tag is unchanged).",
			InputSchema:        schemaSandboxPreviewRebuild(),
			Handler:            sandboxPreviewRebuild,
			RequiredCapability: "sandbox.preview",
		},

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
