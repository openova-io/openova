// capabilities.go — plan→MCP-capabilities map for the Sandbox CR.
//
// The sandbox-controller stamps the resolved capability allowlist onto
// every minted NewAPI token; the MCP server's tools.Registry gates each
// tool call against the bearer's Claims.Capabilities via
// HasCapability (architecture.md §3 RBAC).
//
// This file is the source of truth for the plan-to-capability shape.
// `core/services/tenant/handlers/sandbox_consumer.go` carries an exact
// duplicate (`capabilitiesForPlan`) so the tenant-service has no
// compile-time dep on the controllers module (same isolation pattern
// `quotaForPlan` already uses). If either side changes, the other MUST
// change in the same PR.
//
// Plan ladder (matches catalog seed `expectedSandboxPlans`):
//
//   - sandbox-free: read-only k8s + gitea read + session/rag/skills.
//   - sandbox-pro:  Free + sandbox.db.* + sandbox.storage.* +
//     sandbox.preview.* + sandbox.auth.* + sandbox.secrets.* +
//     marketplace.* + flux.status.
//   - sandbox-ent:  Pro + sandbox.deploy.{staging,production} +
//     sandbox.stripe.* + flux.{reconcile,suspend,resume} +
//     gitea.pr.{create,merge} + gitea.issue.*.
//
// An unknown / empty plan slug falls through to the Free shape so a
// misconfigured CR never silently inherits Pro/Ent grants.
//
// Wildcards (`sandbox.db.*`) are honoured by the MCP server's
// HasCapability matcher (products/sandbox/mcp-server/internal/tools/
// registry.go via Claims.HasCapability extension). The plan map can
// therefore carry coarse grants without enumerating every per-tool
// capability string.

package sandboxapi

import "strings"

// Plan slugs (mirrors core/services/catalog/handlers/seed.go).
const (
	PlanSandboxFree = "sandbox-free"
	PlanSandboxPro  = "sandbox-pro"
	PlanSandboxEnt  = "sandbox-ent"
)

// freePlanCapabilities is the read-only-by-default surface every
// Sandbox owner gets. Sized to let the agent discover the namespace,
// browse repos + PRs + issues, pull logs/objects, and ask the
// session-introspection tools who they are. NO write surface; NO
// per-Sandbox provisioning (db / storage / preview / deploy / stripe).
var freePlanCapabilities = []string{
	// gitea read surface.
	"gitea.repo.list",
	"gitea.repo.get",
	"gitea.pr.list",
	"gitea.pr.get",

	// k8s read surface (Org vcluster only — the MCP server's tool
	// implementations reject any GVK/namespace outside the Sandbox's
	// vcluster regardless of this grant).
	"k8s.read.get",
	"k8s.read.list",

	// Session introspection — the MCP server's exempt-from-org-scope
	// allowlist also covers these tools but the capability gate runs
	// FIRST, so we still need an explicit grant.
	"sandbox.session.*",

	// RAG + skills catalogue (read-only).
	"rag.search",
	"skills.list",
	"skills.get",
}

// proPlanCapabilities extends Free with the per-Sandbox provisioning
// surface (db, storage, preview, auth, secrets) + marketplace domain
// reservation + Flux read-only status.
var proPlanCapabilities = func() []string {
	out := make([]string, 0, len(freePlanCapabilities)+16)
	out = append(out, freePlanCapabilities...)
	out = append(out,
		// Per-Sandbox provisioning (mutating in the Sandbox's own
		// vcluster namespace only — the MCP server scopes every call
		// to env.SandboxNamespace).
		"sandbox.db.*",
		"sandbox.storage.*",
		"sandbox.preview.*",
		"sandbox.auth.*",
		"sandbox.secrets.*",

		// Marketplace domain reservation (proxied to core/services/domain;
		// the canonical auth + WHOIS + uniqueness checks live there).
		"marketplace.*",

		// Flux: read-only status only at Pro. Reconcile/suspend/resume
		// are mutating ops on tenant HRs — Ent-tier.
		"flux.status",
	)
	return out
}()

// entPlanCapabilities extends Pro with the staging+production deploy
// surface, Stripe binding, Flux mutating ops, and the gitea write
// surface (PR create/merge + issue create/comment).
var entPlanCapabilities = func() []string {
	out := make([]string, 0, len(proPlanCapabilities)+12)
	out = append(out, proPlanCapabilities...)
	out = append(out,
		// Staging + production deploys via Flux HelmReleases written
		// to the tenant Gitea repo. The MCP server's deploy.production
		// handler ALSO gates on a second capability
		// `sandbox.deploy.production` so Ent operators can issue staging
		// PATs scoped to staging-only by dropping the production grant.
		"sandbox.deploy.staging",
		"sandbox.deploy.production",
		"sandbox.deploy.status",
		"sandbox.deploy.rollback",

		// Stripe — bound to the Sandbox's own Secret store; per-call
		// outbound to api.stripe.com.
		"sandbox.stripe.*",

		// Flux mutating ops on tenant-scoped HRs / Kustomizations. The
		// MCP server's dynamic client RBAC also gates which namespaces
		// the SA can patch.
		"flux.reconcile",
		"flux.suspend",
		"flux.resume",

		// Gitea write surface — PR open/merge + issue lifecycle.
		"gitea.pr.create",
		"gitea.pr.merge",
		"gitea.issue.*",
	)
	return out
}()

// CapabilitiesForPlan returns the MCP capability allowlist a freshly
// minted Sandbox token should carry given the customer-picked plan
// slug. Unknown / empty plan slug falls through to the Free shape so
// every Sandbox always carries at least the read-only surface.
//
// The returned slice is a fresh copy on every call — callers may
// append/mutate without affecting the package-level templates.
func CapabilitiesForPlan(planID string) []string {
	switch strings.TrimSpace(planID) {
	case PlanSandboxEnt:
		return append([]string(nil), entPlanCapabilities...)
	case PlanSandboxPro:
		return append([]string(nil), proPlanCapabilities...)
	case PlanSandboxFree:
		return append([]string(nil), freePlanCapabilities...)
	default:
		return append([]string(nil), freePlanCapabilities...)
	}
}

// ResolveCapabilities returns the effective capability allowlist for a
// Sandbox CR — the explicit spec.capabilities when non-empty,
// otherwise the plan-derived default via CapabilitiesForPlan. The
// returned slice is always a fresh copy.
func ResolveCapabilities(spec *SandboxSpec) []string {
	if spec == nil {
		return CapabilitiesForPlan("")
	}
	if len(spec.Capabilities) > 0 {
		out := make([]string, 0, len(spec.Capabilities))
		for _, c := range spec.Capabilities {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			out = append(out, c)
		}
		if len(out) > 0 {
			return out
		}
	}
	return CapabilitiesForPlan(spec.PlanID)
}
