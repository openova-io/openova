package handlers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/provisioning/gitguard"
	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for provisioning HTTP handlers.
type Handler struct {
	Store *store.Store
	// Producer is the canonical event-bus surface. In production this
	// is an events.MultiPublisher (NATS + optional Redpanda); a plain
	// *events.Producer also satisfies the interface so the legacy
	// Catalyst-Zero path keeps working without a wiring change.
	Producer     events.BrokerPublisher
	Generator    *gitops.ManifestGenerator
	GitHubClient *ghclient.Client
	CatalogURL   string // internal URL to catalog service

	// GitBasePath + SovereignFQDN are read at startup from env and used
	// to re-validate every commit before it lands (issue #944 cross-
	// cluster pollution guard, defence in depth on top of the startup
	// validation in main.go). Empty SovereignFQDN means Catalyst-Zero
	// (contabo) — see ValidateGitBasePath in main.go.
	GitBasePath   string
	SovereignFQDN string

	// GitBranch is the branch name commits target. Defaults to "main"
	// (matching both upstream github.com defaults and bp-gitea's seeded
	// repo on Sovereigns). Operator-overridable via GITHUB_BRANCH env.
	GitBranch string

	// TenantParentDomain is the org-pool parent zone the provisioning
	// service stamps onto Organization.spec.tenantPublic when a tenant's
	// product becomes Ready. Sourced from TENANT_PARENT_DOMAIN env on
	// the Sovereign's provisioning Deployment (e.g. "omani.homes"). Empty
	// disables the patch entirely — legacy tenants without an Organization
	// CR keep working through the Sovereign-wide tenant-wildcard route.
	// See handlers/tenant_public_patch.go for the patch path. Per
	// docs/INVIOLABLE-PRINCIPLES.md #4 this knob flows through env, never
	// hardcoded — every Sovereign picks its own pool zone.
	TenantParentDomain string

	// AppsParentDomain is the EFFECTIVE org-pool parent zone the per-Org
	// apps-HTTPRoute generator renders product hosts under (e.g.
	// openclaw.<slug>.<this>). Unlike TenantParentDomain (empty = "feature
	// disabled" for the day-2 patch), this is the RESOLVED value the apps
	// generator actually uses — gitops.ResolveParentDomain(TENANT_PARENT_DOMAIN),
	// which falls back to omani.homes when the env is unset. main.go resolves
	// it ONCE and hands the SAME value to both the generator and this Handler,
	// so the per-Org DNS-writer pool (Org.spec.tenantPublic.parentDomain) the
	// createOrganizationCR handler stamps can never diverge from the pool the
	// apps render under — the #4421 fix. Without it, a Sovereign with no
	// TENANT_PARENT_DOMAIN minted apps on the omani.homes default but wrote the
	// per-Org A-record under the customer's funnel pick (or none), so the app
	// host fell through to a stale apex `*.omani.homes` wildcard → dead IP.
	AppsParentDomain string

	// PoolDomains is the served org-pool TLD set (#4999) — env
	// TENANT_POOL_DOMAINS, defaulting to the canonical four .omani.X zones when
	// empty (see pool_domains.go). resolveOrgParentDomain honors a customer's
	// funnel pick only when it is in this set (else it falls back to
	// AppsParentDomain), so a 2nd Org can provision under a DIFFERENT served TLD.
	// The apps generator follows the SAME resolved zone (applyTenantChangePerOrg
	// scoped clone), keeping console==apps under the honored TLD.
	PoolDomains []string

	// PerOrgGitops enables the Sovereign per-Org commit target (#4384). When
	// true, the day-2 cart install commits the customer's purchased
	// Applications into the per-Org `<slug>/catalyst-tenant` repo's
	// `vcluster/apps/` tree (the one the org-controller bootstrapped + wired a
	// Flux Kustomization for) instead of the globally-configured catalog repo
	// (GITHUB_OWNER/GITHUB_REPO = openova/openova on a Sovereign). The global
	// repo is the WRONG target for per-Org apps on a Sovereign and 404'd on the
	// empty-SHA tree path. Sourced from TENANT_GITOPS_PER_ORG env; defaults ON
	// when SOVEREIGN_FQDN is set (the same signal that flips the chart's git
	// coordinates to the local Gitea). Off (legacy contabo per-tenant overlay
	// path) when empty.
	PerOrgGitops bool

	// PerOrgRepoName is the per-Org Gitea repo the org-controller bootstraps
	// and the funnel cart install targets when PerOrgGitops is true. Matches
	// the org-controller's `catalyst-tenant` constant (organization_controller.go).
	// Operator-overridable via TENANT_GITOPS_REPO env; defaults "catalyst-tenant".
	PerOrgRepoName string

	// PerOrgBranch is the branch the per-Org `<slug>/catalyst-tenant` repo
	// commits land on. CRITICAL: this is NOT the global GitBranch — on a
	// Sovereign GitBranch is `org-tenants` (the cutover-mirror-protected branch
	// of the global openova/openova catalog repo), but the per-Org repo the
	// org-controller bootstrapped tracks branch `main` (per_org_flux.go's
	// GitRepository ref.branch + the org-controller's own PutFile branch). A
	// commit to the wrong branch would land on a ref no Flux GitRepository
	// watches. Operator-overridable via TENANT_GITOPS_BRANCH env; defaults "main".
	PerOrgBranch string

	// day2Cancels tracks in-flight day-2 job wait contexts so tenant.deleted
	// can preempt them (issue #99). Zero value is ready to use.
	day2Cancels day2CancelRegistry

	// pendingInstalls holds day-2 cart installs whose step-0 commit could not
	// land yet because the per-Org Gitea org/repo did not exist after the
	// in-line retry budget (#4404). StartPendingInstallReconciler drains them
	// once the org-controller finishes creating the repo, so a slow per-Org
	// create never drops the purchased app. Zero value is ready to use.
	pendingInstalls pendingInstallRegistry

	// perOrgCommits serialises this process's commits to a single per-Org
	// gitops branch so the funnel's own N concurrent cart-app installs cannot
	// contend for one branch head (#5387). Zero value is ready to use.
	perOrgCommits perOrgCommitGate
}

// VerifyCommitTargetSafe re-runs the issue #944 cross-cluster pollution
// guard before every git commit. Callers MUST invoke it as the first
// statement of any code path that reaches GitHubClient.CommitFiles* —
// even though startup already validated, a runtime mutation (env-var
// flip via kubectl exec, ConfigMap update without Pod restart, or a
// mis-resolved manifest path computed from rec.OTECHFQDN that doesn't
// share the prefix) could still slip a foreign-cluster path past the
// startup check.
//
// The function is intentionally a thin wrapper around the package-level
// validator (sub-package gitguard) so the policy lives in exactly one
// place.
func (h *Handler) VerifyCommitTargetSafe(targetPath string) error {
	if err := gitguard.ValidateBasePath(h.GitBasePath, h.SovereignFQDN); err != nil {
		return fmt.Errorf("base path: %w", err)
	}
	if targetPath == "" {
		return nil
	}
	clean := strings.TrimSuffix(targetPath, "/")
	if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return fmt.Errorf("commit target path must be repo-relative without traversal, got %q", targetPath)
	}
	base := strings.TrimSuffix(h.GitBasePath, "/")
	if base != "" && !strings.HasPrefix(clean, base+"/") && clean != base {
		return fmt.Errorf("commit target path %q escapes configured GIT_BASE_PATH %q (issue #944 cross-cluster pollution guard)", targetPath, h.GitBasePath)
	}
	return nil
}

// startRequest is the JSON body for manually starting a provision.
type startRequest struct {
	TenantID  string   `json:"tenant_id"`
	OrderID   string   `json:"order_id"`
	PlanID    string   `json:"plan_id"`
	Apps      []string `json:"apps"`
	Subdomain string   `json:"subdomain"`
}

// GetStatus returns the provision status by ID.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.Store.GetProvision(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get provision")
		return
	}
	if p == nil {
		respond.Error(w, http.StatusNotFound, "provision not found")
		return
	}
	respond.OK(w, p)
}

// GetByTenant returns the provision status for a given tenant.
func (h *Handler) GetByTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	p, err := h.Store.GetProvisionByTenant(r.Context(), tenantID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get provision")
		return
	}
	if p == nil {
		respond.Error(w, http.StatusNotFound, "provision not found for tenant")
		return
	}
	respond.OK(w, p)
}

// Start manually triggers provisioning (admin endpoint).
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.TenantID == "" || req.OrderID == "" || req.PlanID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id, order_id, and plan_id are required")
		return
	}

	// HTTP /provisioning/start (admin manual trigger) doesn't carry
	// app_configs — the customer-chosen configSchema values lookup is
	// scoped to the marketplace order.placed path only. Passing nil
	// here keeps generator defaults; if an admin wants to override they
	// can extend startRequest to carry the map.
	provision, err := h.startProvisioning(r.Context(), req.TenantID, req.OrderID, req.PlanID, req.Apps, req.Subdomain, nil)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to start provisioning")
		return
	}

	respond.JSON(w, http.StatusCreated, provision)
}

// ApplyAppInstall is the HTTP equivalent of the tenant.app_install_requested
// event. The tenant service calls this directly after persisting tenant.Apps,
// which keeps day-2 working when RedPanda is offline. Returns 202 and runs
// the apply/wait flow in a goroutine so the tenant service isn't blocked.
// The async worker drives the shared Job lifecycle so the Jobs page renders
// the same shape regardless of which transport (HTTP / event-bus) was used.
func (h *Handler) ApplyAppInstall(w http.ResponseWriter, r *http.Request) {
	var data appChangeData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if data.TenantID == "" || data.TenantSlug == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id and tenant_slug are required")
		return
	}
	// #4404 — runInstallJob now retries the step-0 commit while the per-Org
	// Gitea repo is still being created (the funnel cart races the
	// organization-controller), so a transient race no longer drops the
	// purchased app. A logged error here is therefore a genuine, exhausted
	// failure, not the race. context.Background() (no deadline) lets the
	// retry loop run to completion in the detached goroutine.
	go func() {
		if err := h.runInstallJob(context.Background(), data); err != nil {
			slog.Error("day-2 install (http)", "tenant", data.TenantSlug, "error", err)
		}
	}()
	respond.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ApplyAppUninstall is the HTTP twin of ApplyAppInstall for uninstall.
func (h *Handler) ApplyAppUninstall(w http.ResponseWriter, r *http.Request) {
	var data appChangeData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if data.TenantID == "" || data.TenantSlug == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id and tenant_slug are required")
		return
	}
	go func() {
		if err := h.runUninstallJob(context.Background(), data); err != nil {
			slog.Error("day-2 uninstall (http)", "tenant", data.TenantSlug, "error", err)
		}
	}()
	respond.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// List returns all provisions with pagination (admin endpoint).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	limit := 50

	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		}
	}
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	provisions, err := h.Store.ListProvisions(r.Context(), offset, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list provisions")
		return
	}
	respond.OK(w, provisions)
}

// --- catalog resolution helpers ---

// catalogAppResp mirrors the /catalog/apps response shape we care about.
// DependencyIDs is the resolved canonical-ID view of Dependencies that the
// catalog service computes once per request (see #89). Keying provisioning
// logic by ID lets us drop the slug↔ID translation maps that used to live
// here and in computePurgeRetention — the two services now agree on a single
// identifier kind.
type catalogAppResp struct {
	ID            string   `json:"id"`
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	Dependencies  []string `json:"dependencies"`   // slugs (admin-friendly)
	DependencyIDs []string `json:"dependency_ids"` // canonical UUIDs — preferred
}

// fetchCatalogApps is the single place we call GET /catalog/apps. Every
// translation helper (name lookup, slug lookup, dependency walk) derives
// from the same response so we don't fan out N requests on a single event.
// Returns (nil, false) on any non-success so callers can fall back cleanly
// without duplicating the error-handling boilerplate.
func (h *Handler) fetchCatalogApps(ctx context.Context) ([]catalogAppResp, bool) {
	if h.CatalogURL == "" {
		return nil, false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.CatalogURL+"/catalog/apps", nil)
	if err != nil {
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, false
	}
	var apps []catalogAppResp
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, false
	}
	return apps, true
}

// resolveAppNames fetches app names from the catalog service, keyed by ID.
func (h *Handler) resolveAppNames(ctx context.Context) map[string]string {
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return nil
	}
	m := make(map[string]string, len(apps))
	for _, a := range apps {
		m[a.ID] = a.Name
	}
	return m
}

// resolveAppSlugs resolves app UUIDs to slugs via the catalog.
func (h *Handler) resolveAppSlugs(ctx context.Context, appIDs []string) []string {
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return appIDs
	}
	idToSlug := make(map[string]string, len(apps))
	for _, a := range apps {
		idToSlug[a.ID] = a.Slug
	}
	slugs := make([]string, len(appIDs))
	for i, id := range appIDs {
		if slug, ok := idToSlug[id]; ok {
			slugs[i] = slug
		} else {
			slugs[i] = id // fallback to ID
		}
	}
	return slugs
}

// resolveAppDependencies returns the catalog-defined dependency slugs for the
// given app slugs. These are installed alongside the user-selected apps
// (e.g. WordPress → mysql). The existing NeedsDB mechanism in gitops still
// handles DB creation; this surface is here so the UI can show what's being
// installed and future non-DB deps can use the same path.
//
// #89: uses the shared fetchCatalogApps helper instead of re-implementing
// the request + decode. Output keyed by slug because the caller
// (startProvisioning) names provisioning steps by slug.
func (h *Handler) resolveAppDependencies(ctx context.Context, appSlugs []string) map[string][]string {
	deps := make(map[string][]string, len(appSlugs))
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return deps
	}
	bySlug := make(map[string][]string, len(apps))
	for _, a := range apps {
		bySlug[a.Slug] = a.Dependencies
	}
	for _, slug := range appSlugs {
		if d := bySlug[slug]; len(d) > 0 {
			deps[slug] = d
		}
	}
	return deps
}

// knownPlanSlugs is the canonical set of plan slugs the platform recognizes,
// matching the boundary/QoS switches in gitops (BoundaryIsVcluster: ""/s/free
// → host tier; m/l/xl/flexi → dedicated vcluster; qosResources: flexi vs the
// rest). It is the single source of truth for "is this string already a slug?"
// so resolvePlanSlug can short-circuit the catalog UUID lookup when the caller
// (the marketplace funnel) already posts the slug directly.
var knownPlanSlugs = map[string]struct{}{
	"s":     {},
	"m":     {},
	"l":     {},
	"xl":    {},
	"flexi": {},
	"free":  {},
}

// isKnownPlanSlug reports whether s is one of the canonical plan slugs
// (case-insensitive, whitespace-trimmed) and, if so, returns its normalized
// (lowercase) form.
func isKnownPlanSlug(s string) (string, bool) {
	norm := strings.ToLower(strings.TrimSpace(s))
	if norm == "" {
		return "", false
	}
	_, ok := knownPlanSlugs[norm]
	return norm, ok
}

// resolvePlanSlug resolves the caller's plan identifier to a canonical plan
// slug (s|m|l|xl|flexi). It accepts BOTH forms the two provisioning doors post:
//
//   - the marketplace FUNNEL door posts the plan SLUG directly ("m"/"l"/"xl"/…);
//     that value is returned as-is (normalized) without a catalog round-trip.
//   - the BSS door posts a plan UUID, which is looked up against /catalog/plans.
//
// Before #4473 this function treated EVERY input as a UUID, so a funnel-posted
// slug matched no catalog row and silently fell through to the "s" default —
// every funnel Org provisioned at the S boundary regardless of the chosen plan
// (Pillar-1 billing/provisioning correctness fault, verified live on prov
// 91dc05917e44d1c1: plan_id:"m" → Org CR spec.planSlug:"s"). The slug fast-path
// closes that gap; the UUID lookup is preserved for the BSS door.
//
// When the input is neither a known slug nor a resolvable UUID, the historical
// "s" default is returned — but now LOGGED, so a silent S-downgrade is never
// invisible again.
func (h *Handler) resolvePlanSlug(ctx context.Context, planID string) string {
	// Fast-path: the funnel posts the slug directly. No catalog round-trip,
	// no dependency on /catalog/plans being reachable at creation time.
	if slug, ok := isKnownPlanSlug(planID); ok {
		return slug
	}

	if h.CatalogURL == "" {
		slog.Warn("resolvePlanSlug: no catalog URL and plan_id is not a known slug — defaulting to 's' (verify the Org is not being silently downgraded)",
			"plan_id", planID)
		return "s" // default
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.CatalogURL+"/catalog/plans", nil)
	if err != nil {
		slog.Warn("resolvePlanSlug: catalog request build failed — defaulting to 's'", "plan_id", planID, "err", err)
		return "s"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("resolvePlanSlug: catalog unreachable — defaulting to 's'", "plan_id", planID, "err", err)
		return "s"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		slog.Warn("resolvePlanSlug: catalog returned non-200 — defaulting to 's'", "plan_id", planID, "status", resp.StatusCode)
		return "s"
	}
	var plans []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&plans); err != nil {
		slog.Warn("resolvePlanSlug: catalog decode failed — defaulting to 's'", "plan_id", planID, "err", err)
		return "s"
	}
	for _, p := range plans {
		if p.ID == planID {
			return p.Slug
		}
	}
	slog.Warn("resolvePlanSlug: plan_id matched neither a known slug nor a catalog plan UUID — defaulting to 's' (an Org may be silently downgraded to the S boundary)",
		"plan_id", planID)
	return "s"
}

// resolveTenantPlanSlug is the #4293 MAJOR-3 fail-safe resolver for the DAY-2
// path. The bug it closes: resolvePlanSlug silently returns "s" on ANY transient
// catalog failure (3s timeout, non-200, decode error, plan-not-found). On a
// day-2 install/uninstall for a paid (M+) Org, if the catalog is briefly
// unreachable at that instant, the day-2 manifests would be re-generated as the
// HOST tier (no kubeConfig) and the apps RE-ROUTED out of the vcluster into the
// host `<slug>` ns — orphaning the original in-vcluster copy. A transient blip
// must NEVER silently downgrade a paid Org's boundary.
//
// Fix: read the AUTHORITATIVE persisted plan slug off the Organization CR
// (`spec.planSlug`), which the funnel resolved ONCE at creation
// (organization_create.go) and which the EPIC designates the single
// truth-source for the resource cap + boundary tier. The CR read has no
// dependency on the live catalog service, so it is immune to the transient. We
// only fall back to the live catalog (resolvePlanSlug) when the CR genuinely
// carries no planSlug (legacy tenants created before Workstream B, or a tenant
// with no Organization CR) — and even then we surface ok=false so the caller can
// fail-closed (retry) rather than commit a host-tier downgrade off a guess.
//
// Returns (slug, true) when the slug is authoritative (from the CR, or a
// confirmed live catalog hit); (slug, false) when it could only be guessed
// (catalog unreachable AND no CR planSlug) — the caller MUST treat false as
// retryable for the day-2 boundary decision, never as a confirmed host-tier.
func (h *Handler) resolveTenantPlanSlug(ctx context.Context, tenantSlug, planID string) (string, bool) {
	// 1. Authoritative: the persisted Organization CR's spec.planSlug.
	if tenantSlug != "" {
		body, err := h.k8sGet("/apis/orgs.openova.io/v1/organizations/" + tenantSlug)
		if err == nil {
			var org struct {
				Spec struct {
					PlanSlug string `json:"planSlug"`
				} `json:"spec"`
			}
			if jerr := json.Unmarshal(body, &org); jerr == nil {
				if slug := strings.TrimSpace(org.Spec.PlanSlug); slug != "" {
					return slug, true
				}
			}
		}
		// A 404 (no Organization CR — legacy tenant-service flow) or any read
		// error falls through to the catalog lookup; it is not, by itself, a
		// reason to fail the day-2 change.
	}

	// 2. Fallback: live catalog. Distinguish a CONFIRMED hit from the
	// silent-"s" default so the caller can fail-closed on a transient. We
	// re-run the lookup but inspect whether the catalog was actually reachable
	// + the plan resolved, rather than trusting resolvePlanSlug's "s" sentinel.
	slug, reachable := h.lookupPlanSlug(ctx, planID)
	if reachable {
		return slug, true
	}
	// Catalog unreachable AND no CR planSlug: return the historical "s" default
	// but flag it non-authoritative so day-2 boundary decisions fail-closed.
	return "s", false
}

// lookupPlanSlug is the catalog HTTP plan-slug lookup that DISTINGUISHES a
// confirmed result from an unreachable-catalog transient. It returns
// (slug, reachable): reachable=true means the catalog answered (the plan
// resolved, or is genuinely absent → "s" is then a real answer); reachable=false
// means the catalog could not be consulted (no URL / timeout / non-200 / decode
// error) so the "s" is a GUESS, not a confirmed downgrade. resolvePlanSlug keeps
// its original always-"s"-on-failure contract for the creation path (which is
// self-consistent because it resolves once); the day-2 path uses this richer
// signal via resolveTenantPlanSlug.
func (h *Handler) lookupPlanSlug(ctx context.Context, planID string) (slug string, reachable bool) {
	// #4473: the funnel posts the slug directly; a known slug is an
	// authoritative answer with no catalog dependency.
	if s, ok := isKnownPlanSlug(planID); ok {
		return s, true
	}
	if h.CatalogURL == "" {
		return "s", false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.CatalogURL+"/catalog/plans", nil)
	if err != nil {
		return "s", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "s", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return "s", false
	}
	var plans []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&plans); err != nil {
		return "s", false
	}
	// Catalog answered. A matching plan is authoritative; a genuine miss
	// (unknown/empty planID) is a real "s" answer (reachable=true).
	for _, p := range plans {
		if p.ID == planID {
			return p.Slug, true
		}
	}
	return "s", true
}

// appDisplayName returns a human-readable name for an app.
func appDisplayName(names map[string]string, id string) string {
	if names != nil {
		if n, ok := names[id]; ok {
			return n
		}
	}
	if len(id) > 8 {
		return fmt.Sprintf("app-%s", id[:8])
	}
	return id
}

// --- K8s API helpers for monitoring deployment status ---

// waitForDeployment polls the K8s API until the deployment has at least one
// ready replica or the timeout expires.
func (h *Handler) waitForDeployment(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := h.checkDeploymentReady(namespace, name)
		if err == nil && ready {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("deployment %s/%s not ready after %s", namespace, name, timeout)
}

// waitForAnyPod waits until at least one pod is Running in the namespace.
func (h *Handler) waitForAnyPod(ctx context.Context, namespace string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := h.checkAnyPodRunning(namespace)
		if err == nil && running {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("no running pods in %s after %s", namespace, timeout)
}

// checkDeploymentReady uses the in-cluster K8s API to check deployment readiness.
func (h *Handler) checkDeploymentReady(namespace, name string) (bool, error) {
	body, err := h.k8sGet(fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", namespace, name))
	if err != nil {
		return false, err
	}
	var dep struct {
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &dep); err != nil {
		return false, err
	}
	return dep.Status.ReadyReplicas > 0, nil
}

// checkAnyPodRunning checks if any pod in the namespace is Running.
func (h *Handler) checkAnyPodRunning(namespace string) (bool, error) {
	body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
	if err != nil {
		return false, err
	}
	var podList struct {
		Items []struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &podList); err != nil {
		return false, err
	}
	for _, pod := range podList.Items {
		if pod.Status.Phase == "Running" {
			return true, nil
		}
	}
	return false, nil
}

// waitForVclusterDNSOrKick polls the host NS for the synced kube-dns service
// that vcluster's syncer creates (named kube-dns-x-kube-system-x-vcluster).
// If it doesn't appear within 60s, delete vcluster-0 to force the syncer to
// re-initialize, then poll for another 60s. Returns nil on success, error if
// DNS is still missing after the kick. Issue #103.
//
// Why this matters: without kube-dns synced, pods inside the vcluster stay
// Pending with "waiting for DNS service IP" — every app install that follows
// times out at 10 min. In today's harness run tenant e2e90689b hit this on
// provisioning and only recovered when an operator manually restarted
// vcluster-0. Folding that workaround into the provisioning flow removes the
// operator-in-the-loop requirement.
func (h *Handler) waitForVclusterDNSOrKick(ctx context.Context, hostNS string) error {
	dnsSvc := "/api/v1/namespaces/" + hostNS + "/services/kube-dns-x-kube-system-x-vcluster"
	poll := func(timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if _, err := h.k8sGet(dnsSvc); err == nil {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(5 * time.Second):
			}
		}
		return false
	}

	if poll(60 * time.Second) {
		slog.Info("vcluster dns synced", "ns", hostNS)
		return nil
	}

	slog.Warn("vcluster dns not synced after 60s — kicking vcluster-0", "ns", hostNS)
	// Delete vcluster-0 via the DELETE pod endpoint; the StatefulSet will
	// recreate it and the fresh syncer usually publishes kube-dns within ~12s.
	if err := h.k8sDelete("/api/v1/namespaces/" + hostNS + "/pods/vcluster-0"); err != nil {
		slog.Warn("vcluster dns kick: delete vcluster-0 failed — may still recover",
			"ns", hostNS, "error", err)
	}

	if poll(90 * time.Second) {
		slog.Info("vcluster dns synced after kick", "ns", hostNS)
		return nil
	}
	return fmt.Errorf("kube-dns service still missing in %s after vcluster-0 restart", hostNS)
}

// waitForHelmRelease polls Flux's HelmRelease resource until its Ready condition
// is True or the timeout expires. Used to gate on vCluster being online.
func (h *Handler) waitForHelmRelease(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/apis/helm.toolkit.fluxcd.io/v2/namespaces/%s/helmreleases/%s", namespace, name))
		if err == nil {
			var hr struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
						Reason string `json:"reason"`
					} `json:"conditions"`
				} `json:"status"`
			}
			if jerr := json.Unmarshal(body, &hr); jerr == nil {
				for _, c := range hr.Status.Conditions {
					if c.Type == "Ready" && c.Status == "True" {
						slog.Info("helmrelease ready", "namespace", namespace, "name", name)
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("helmrelease %s/%s not ready after %s", namespace, name, timeout)
}

// waitForVclusterApp waits until an app pod for the given app slug is
// Running+Ready in the host namespace.
//
// #4297 TIER-AWARE: for the VCLUSTER tier, vCluster syncs pods up to the host
// ns under the name pattern <pod>-x-<inner-ns>-x-<vcluster-name> — inner ns is
// "apps", vcluster release is "vcluster", so the synced pod is
// `<appSlug>-...-x-apps-x-vcluster`. For the HOST tier (free/S, no vcluster)
// the apps-sync Kustomization applies the Deployment straight into the host
// `<slug>` ns, so the pod carries its NATIVE name `<appSlug>-...` with NO
// syncer suffix. Callers pass `isVcluster` so the right pod-name shape is
// matched; a host-tier wait that looked for the `-x-apps-x-vcluster` suffix
// would never match and would always time out.
// appPodNameMatches is the #4297 TIER-AWARE pod-name matcher. For the VCLUSTER
// tier the app pod is synced up to the host ns with the
// `<appSlug>-...-x-apps-x-vcluster` shape; for the HOST tier (no vcluster) the
// pod runs natively in the host ns as `<appSlug>-...`. Extracted as a pure
// function so the tier split is unit-testable without a live kube-API.
//
// The host-tier match is intentionally STRICT: it requires the slug-prefixed
// name to NOT carry the vcluster syncer suffix, so a stray synced pod from a
// sibling vcluster Org sharing the host ns can't satisfy a host-tier wait.
func appPodNameMatches(podName, appSlug string, isVcluster bool) bool {
	prefix := appSlug + "-"
	if !strings.HasPrefix(podName, prefix) {
		return false
	}
	const vclusterSuffix = "-x-apps-x-vcluster"
	if isVcluster {
		return strings.HasSuffix(podName, vclusterSuffix)
	}
	// Host tier — native name, MUST NOT be a synced vcluster pod.
	return !strings.HasSuffix(podName, vclusterSuffix)
}

func (h *Handler) waitForVclusterApp(ctx context.Context, namespace, appSlug string, timeout time.Duration, isVcluster bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
		if err == nil {
			var podList struct {
				Items []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Status struct {
						Phase      string `json:"phase"`
						Conditions []struct {
							Type   string `json:"type"`
							Status string `json:"status"`
						} `json:"conditions"`
					} `json:"status"`
				} `json:"items"`
			}
			if jerr := json.Unmarshal(body, &podList); jerr == nil {
				for _, pod := range podList.Items {
					name := pod.Metadata.Name
					if !appPodNameMatches(name, appSlug, isVcluster) {
						continue
					}
					if pod.Status.Phase != "Running" {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == "Ready" && c.Status == "True" {
							slog.Info("vcluster app pod ready", "namespace", namespace, "pod", name)
							return nil
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("app %s not ready in %s after %s", appSlug, namespace, timeout)
}

// waitForCertificate polls cert-manager's Certificate resource until its Ready
// condition is True. Returns nil on ready, error on timeout — callers can
// decide whether a still-issuing cert is fatal.
func (h *Handler) waitForCertificate(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificates/%s", namespace, name))
		if err == nil {
			var cert struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			}
			if jerr := json.Unmarshal(body, &cert); jerr == nil {
				for _, c := range cert.Status.Conditions {
					if c.Type == "Ready" && c.Status == "True" {
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("certificate %s/%s not ready after %s", namespace, name, timeout)
}

// k8sGet makes a GET request to the in-cluster Kubernetes API.
func (h *Handler) k8sGet(path string) ([]byte, error) {
	return h.k8sRequest(http.MethodGet, path, nil)
}

// k8sDelete issues a DELETE against the in-cluster API. Used for tenant
// teardown to explicitly drop Flux Kustomization / HelmRelease CRs so their
// finalizers don't strand the namespace.
func (h *Handler) k8sDelete(path string) error {
	body, err := h.k8sRequest(http.MethodDelete, path, nil)
	if err != nil {
		// 404 on a delete is success (already gone).
		if strings.Contains(err.Error(), "status 404") {
			return nil
		}
		return err
	}
	_ = body
	return nil
}

// k8sPatchRemoveFinalizers strips all .metadata.finalizers from a CR so
// Kubernetes can garbage-collect it. Used as last-resort when a finalizer
// is blocking namespace deletion for longer than the timeout.
func (h *Handler) k8sPatchRemoveFinalizers(path string) error {
	patch := []byte(`{"metadata":{"finalizers":null}}`)
	_, err := h.k8sRequest(http.MethodPatch, path, patch)
	if err != nil && strings.Contains(err.Error(), "status 404") {
		return nil
	}
	return err
}

func (h *Handler) k8sRequest(method, path string, body []byte) ([]byte, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in cluster")
	}

	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}

	url := fmt.Sprintf("https://%s:%s%s", host, port, path)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(tokenBytes))
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("k8s %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// mirrorVClusterKubeconfig copies the `vc-vcluster` Secret from the tenant
// namespace to flux-system as `tenant-<slug>-kubeconfig`. The per-tenant
// Flux Kustomization CR (which lives in flux-system per issue #97) references
// this mirror to reconcile resources into the vCluster. We mirror rather than
// place the CR in the tenant NS because:
//
//  1. Flux Kustomization.spec.kubeConfig.secretRef has no `namespace` field —
//     the secret must live in the CR's own namespace.
//  2. Placing the CR in tenant-<slug> re-introduces the finalizer-blocks-NS-GC
//     defect this whole fix is solving.
//
// Idempotent: if the mirror already exists it's updated in place (handles
// password rotation or kubeconfig CA rotation). Call this after the vcluster
// HelmRelease reaches Ready so the source secret definitely exists.
func (h *Handler) mirrorVClusterKubeconfig(ctx context.Context, tenantSlug string) error {
	// #4290: the vCluster kubeconfig secret `vc-vcluster` is exported by the
	// org-controller's `vcluster` HelmRelease into the `<slug>` namespace (the
	// single boundary), not a `tenant-<slug>` stray. Mirror it from there into
	// flux-system as `tenant-<slug>-kubeconfig`, the name the apps-sync
	// Kustomization (generateAppsSyncKustomization) references.
	srcNS := tenantSlug
	srcName := "vc-vcluster"
	dstName := vclusterKubeconfigSecretName(tenantSlug)

	srcBody, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", srcNS, srcName))
	if err != nil {
		return fmt.Errorf("read source secret %s/%s: %w", srcNS, srcName, err)
	}
	var src struct {
		Data map[string]string `json:"data"`
		Type string            `json:"type"`
	}
	if err := json.Unmarshal(srcBody, &src); err != nil {
		return fmt.Errorf("parse source secret: %w", err)
	}

	// #4785: mirror the kubeconfig into BOTH `flux-system` AND the tenant
	// `<slug>` namespace. `flux-system` is where the apps-sync Kustomization's
	// `kubeConfig.secretRef` resolves (issue #97). But the per-Org application
	// HelmReleases (generateHelmReleaseApp → helmrelease_apps.go) are stamped
	// into the `<slug>` namespace and reference the kubeconfig with NO
	// namespace on the secretRef — Flux resolves that in the HR's OWN
	// namespace. A flux-system-only mirror therefore left every customer app HR
	// stuck `could not get KubeConfig secret '<slug>/tenant-<slug>-kubeconfig':
	// not found` → no customer app ever deployed (proven live hw225 uat225wp:
	// bp-openclaw HR False, WordPress 404). Mirroring into both namespaces is
	// the last mile of the per-Org apps-in-vCluster pillar.
	for _, dstNS := range []string{"flux-system", tenantSlug} {
		// Build the destination secret payload. Copy .data verbatim (base64
		// values survive the round trip) and carry over the type so opaque
		// stays opaque. NB: K8s label values are restricted to [A-Za-z0-9-_.],
		// so the source-ns reference goes in an annotation (unconstrained).
		dst := map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      dstName,
				"namespace": dstNS,
				"labels": map[string]string{
					"openova.io/tenant":     tenantSlug,
					"openova.io/managed-by": "provisioning",
				},
				"annotations": map[string]string{
					"openova.io/mirror-of": srcNS + "/" + srcName,
				},
			},
			"data": src.Data,
			"type": src.Type,
		}
		payload, err := json.Marshal(dst)
		if err != nil {
			return fmt.Errorf("marshal mirror secret (%s): %w", dstNS, err)
		}

		// Try create first; fall back to PUT (full replace) if it exists.
		_, err = h.k8sRequest(http.MethodPost, fmt.Sprintf("/api/v1/namespaces/%s/secrets", dstNS), payload)
		if err == nil {
			slog.Info("mirrored vCluster kubeconfig", "src", srcNS+"/"+srcName, "dst", dstNS+"/"+dstName)
			continue
		}
		// 409 conflict → already exists. Update via PUT to keep data fresh.
		if !strings.Contains(err.Error(), "status 409") {
			return fmt.Errorf("create mirror secret (%s): %w", dstNS, err)
		}
		_, err = h.k8sRequest(http.MethodPut, fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", dstNS, dstName), payload)
		if err != nil {
			return fmt.Errorf("update mirror secret (%s): %w", dstNS, err)
		}
		slog.Info("updated mirrored vCluster kubeconfig", "src", srcNS+"/"+srcName, "dst", dstNS+"/"+dstName)
	}
	return nil
}

// deleteVClusterKubeconfigMirror removes the flux-system mirror secret during
// tenant teardown. 404 is treated as success (already gone).
func (h *Handler) deleteVClusterKubeconfigMirror(ctx context.Context, tenantSlug string) error {
	return h.k8sDelete(fmt.Sprintf(
		"/api/v1/namespaces/flux-system/secrets/%s", vclusterKubeconfigSecretName(tenantSlug)))
}
