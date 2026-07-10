package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/tenant/catalog"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// tenantSlugRE mirrors the guard in services/provisioning/handlers/consumer.go
// so bad slugs are rejected at the tenant-service input boundary (CreateOrg)
// rather than only downstream in provisioning. Security-critical: the slug
// becomes a filesystem path component in clusters/.../tenants/<slug>/ so
// anything but [a-z0-9-] opens a path-traversal vector. Issue #105 (extended).
var tenantSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// validTenantSlug returns true iff s is a safe tenant slug.
func validTenantSlug(s string) bool {
	return tenantSlugRE.MatchString(s)
}

// Handler holds dependencies for tenant HTTP handlers.
type Handler struct {
	Store *store.Store
	// Producer is the broker publisher used to emit tenant lifecycle
	// events (tenant.created, tenant.deleted, tenant.app_install_requested,
	// tenant.app_uninstall_requested). Type is the BrokerPublisher
	// interface so main.go can wire a MultiPublisher (NATS + Redpanda)
	// per ADR-0001 §6 — see core/services/shared/events/bridge.go for
	// why the legacy Redpanda-only Producer was insufficient on
	// Sovereigns (no Redpanda exists there).
	Producer events.BrokerPublisher
	// Catalog is optional; when unset the day-2 app install/uninstall
	// endpoints return 501. Provisioning-time creation does not need it
	// because the marketplace already validated capacity at checkout.
	Catalog *catalog.Client
	// ProvisioningURL is the internal base URL for provisioning-service
	// (e.g. http://provisioning.org-services.svc.cluster.local:8084). Tenant calls
	// it directly for day-2 install/uninstall so the pipeline works even
	// when the event bus is unavailable.
	ProvisioningURL string

	// AppsParentDomain is the Sovereign's EFFECTIVE org-pool apps zone
	// (env TENANT_PARENT_DOMAIN, e.g. "omani.homes") — the SAME value the
	// provisioning service + org-controller key off. #4821 Finding-2: when
	// non-empty it WINS over the funnel-selected `parent_domain`, so the
	// server-authoritative `console_host` this service returns (and the
	// `parent_domain` it persists + emits on `tenant.created`) matches the pool
	// the org-controller actually provisions the per-Org DNS / TLS / HTTPRoute
	// under. See resolveOrgParentDomain below + the identical #4421 apps-pool-wins
	// invariant on the provisioning door (core/services/provisioning/handlers/
	// organization_create.go::resolveOrgParentDomain, which the org-controller CR
	// then honours). Empty preserves the legacy behaviour: honour the funnel pick
	// verbatim (single-domain / degenerate Sovereigns with no apps pool wired).
	AppsParentDomain string

	// DayTwoLocks serializes day-2 install/uninstall on a given tenant so
	// concurrent callers see consistent tenant.Apps reads. Issue #110.
	// Callers MUST pre-populate via NewTenantLocks(); nil is not safe.
	DayTwoLocks *tenantLocks
}

// NewTenantLocks returns a fresh tenantLocks for Handler.DayTwoLocks.
// Exposed so main.go can wire it at construction.
func NewTenantLocks() *tenantLocks { return newTenantLocks() }

// requireMembership checks that the calling user is a member of the tenant
// and returns the role. Returns empty string and writes an error response if
// the user is not a member.
func (h *Handler) requireMembership(w http.ResponseWriter, r *http.Request, tenantID string) (string, bool) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return "", false
	}
	role, err := h.Store.GetMemberRole(r.Context(), tenantID, userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check membership")
		return "", false
	}
	if role == "" {
		respond.Error(w, http.StatusForbidden, "not a member of this organization")
		return "", false
	}
	return role, true
}

// requireOwnerOrAdmin checks that the calling user has owner or admin role in the tenant.
func (h *Handler) requireOwnerOrAdmin(w http.ResponseWriter, r *http.Request, tenantID string) (string, bool) {
	role, ok := h.requireMembership(w, r, tenantID)
	if !ok {
		return "", false
	}
	if role != "owner" && role != "admin" {
		respond.Error(w, http.StatusForbidden, "owner or admin role required")
		return "", false
	}
	return role, true
}

// requireOwner checks that the calling user has the owner role in the tenant.
func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	role, ok := h.requireMembership(w, r, tenantID)
	if !ok {
		return false
	}
	if role != "owner" {
		respond.Error(w, http.StatusForbidden, "owner role required")
		return false
	}
	return true
}

// requireSuperadmin checks that the request was made by a superadmin.
func requireSuperadmin(r *http.Request) bool {
	return middleware.RoleFromContext(r.Context()) == "superadmin"
}

// callProvisioning POSTs a JSON payload to the provisioning service. Used by
// day-2 app install/uninstall so the pipeline works when RedPanda is down.
// Returns nil on any 2xx; logs and returns an error otherwise. A 5s timeout
// keeps the tenant API responsive if provisioning is slow.
func (h *Handler) callProvisioning(ctx context.Context, path string, payload any) error {
	if h.ProvisioningURL == "" {
		return fmt.Errorf("provisioning URL not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.ProvisioningURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("provisioning %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tenant CRUD
// ---------------------------------------------------------------------------

// statusPendingPayment is the state a DEFERRED-launch funnel Org sits in
// between CreateOrg (shell persisted) and billing settlement (which calls the
// internal launch endpoint). No provisioning triggers fire while a tenant is in
// this state, so a checkout that 400s / is abandoned never leaves a provisioned
// Org behind (#4956). The launch endpoint transitions it to "provisioning".
const statusPendingPayment = "pending_payment"

// CreateOrg creates a new organization for the authenticated user.
func (h *Handler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}

	var body struct {
		Slug     string   `json:"slug"`
		Name     string   `json:"name"`
		OrgType  string   `json:"org_type"`
		Industry string   `json:"industry"`
		PlanID   string   `json:"plan_id"`
		Apps     []string `json:"apps"`
		AddOns   []string `json:"addons"`
		// ParentDomain — the org-pool parent apex the customer chose at
		// the /addons step (e.g. "omani.works"). #4176/#4179: the per-Org
		// console lives at `console.<slug>.<parent_domain>`. On a Sovereign
		// whose marketplace runs on the Sovereign domain (omantel.biz) while
		// Orgs provision on a SEPARATE pool domain (omani.works), this is the
		// ONLY signal that carries the chosen pool apex — without it the
		// console_host is mis-derived to console.<slug>.omantel.biz, an
		// unreachable host that breaks EVERY org-create redirect. Tolerated
		// empty (legacy / single-domain Sovereigns fall back to the
		// Sovereign FQDN in deriveConsoleHost).
		ParentDomain string `json:"parent_domain"`
		// Wave 4 Sandbox — coding-agent picks from the marketplace
		// detail page. Only acted on when `Apps` contains "sandbox":
		// CreateOrg publishes an extra `tenant.sandbox_requested`
		// event the sandbox-controller consumes to mint a Sandbox CR
		// with `spec.agentCatalogue` = these slugs. Tolerated empty.
		Agents []string `json:"agents"`
		// TBD-V18-D follow-up to PR #2038 — per-app configSchema
		// values, keyed by app SLUG. Each inner map is `ConfigField.Key`
		// → field-typed primitive (int / string / bool). Persisted on
		// `store.Tenant.AppConfigs`; round-trips on the `tenant.created`
		// event payload via the *store.Tenant embed (no separate
		// wrapper field needed). The downstream HelmRelease-values
		// binding is gated on TBD-V26 (#2040) Path A/B; this field
		// threads the SHAPE end-to-end so flipping the binding switch
		// works without a second upstream change. Tolerated empty.
		AppConfigs map[string]map[string]any `json:"app_configs"`
		// DeferLaunch (#4956) — when true, CreateOrg persists the Org shell
		// but does NOT fire the provisioning triggers (tenant.created →
		// Organization CR, funnel cart-install, sandbox request). The Org
		// stays `pending_payment` until billing settlement calls the internal
		// launch endpoint. The marketplace funnel sets this so a checkout that
		// 400s (bad/unseeded voucher, payment declined) can NEVER leave a
		// provisioned Org behind — the integrity gap from the hw235 walk. All
		// other callers (BSS door, direct API) omit it and keep launching
		// immediately, so this change is inert for every non-funnel path.
		DeferLaunch bool `json:"defer_launch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.Slug == "" || body.Name == "" {
		respond.Error(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	// Slug becomes a DNS subdomain AND a filesystem path component in
	// clusters/.../tenants/<slug>/ — it MUST match a tight regex or we
	// open a path-traversal vector (slug="../etc/passwd" would have the
	// provisioning consumer write outside the tenants directory). Same
	// regex the provisioning-side guard in #105 enforces. Security fix
	// caught by dod-chaos scenario1_apiBoundaries test 1b.
	if !validTenantSlug(body.Slug) {
		respond.Error(w, http.StatusBadRequest,
			"slug must be 3-31 chars, lowercase alphanumeric with hyphens, starting with a letter (e.g. 'acme-co')")
		return
	}

	// Check slug uniqueness.
	available, err := h.Store.CheckSlugAvailable(r.Context(), body.Slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check slug availability")
		return
	}
	if !available {
		respond.Error(w, http.StatusConflict, "slug is already taken")
		return
	}

	// #4176/#4179: normalize the chosen org-pool parent apex. Strip any
	// leading dot the marketplace TLD <select> may carry (".omani.works")
	// and lowercase so deriveConsoleHost composes a clean RFC-1123 host.
	funnelParentDomain := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(body.ParentDomain, ".")))

	// #4821 Finding-2: the Sovereign's apps pool WINS over the funnel pick.
	// The org-controller stamps spec.tenantPublic.parentDomain via the SAME
	// apps-pool-wins rule (organization_create.go::resolveOrgParentDomain, #4421)
	// and writes the per-Org DNS / TLS / HTTPRoute under THAT zone. If this
	// service persisted (and returned as console_host) the raw funnel pick
	// instead, a Sovereign that serves a single apps pool (`omani.homes`) but
	// whose funnel offered `omani.rest` would return console.<slug>.omani.rest —
	// an NXDOMAIN host the org-controller never provisions — so the post-Launch
	// redirect 404s (the reported #4821 Finding-2). Resolving to the apps pool
	// here keeps the returned/persisted/emitted host in lockstep with what the
	// org-controller actually provisions. Empty AppsParentDomain (legacy /
	// single-domain Sovereign) falls back to the funnel pick, unchanged.
	parentDomain := resolveOrgParentDomain(h.AppsParentDomain, funnelParentDomain)
	if funnelParentDomain != "" && parentDomain != funnelParentDomain {
		slog.Warn("CreateOrg: funnel-selected parent_domain overridden by the Sovereign apps pool to keep console_host aligned with the org-controller's provisioned pool (#4821 Finding-2 / #4421)",
			"slug", body.Slug,
			"funnel_parent_domain", funnelParentDomain,
			"apps_parent_domain", parentDomain)
	}

	// Owner email is derived from the caller's JWT claim and persisted on the
	// tenant so the DEFERRED-launch path (#4956) can emit tenant.created with a
	// non-empty owner_email once billing settles — the launch is triggered
	// server-to-server by billing, long after these request claims are gone.
	claims, _ := middleware.ClaimsFromContext(r.Context())
	ownerEmail, _ := claims["email"].(string)

	// #4956 — a deferred (funnel) Org is parked at `pending_payment`; the
	// provisioning triggers only fire once billing settlement calls the internal
	// launch endpoint. Every non-funnel caller keeps the immediate
	// "provisioning" status + inline launch below.
	initialStatus := "provisioning"
	if body.DeferLaunch {
		initialStatus = statusPendingPayment
	}

	tenant := &store.Tenant{
		Slug:         body.Slug,
		Name:         body.Name,
		OrgType:      body.OrgType,
		Industry:     body.Industry,
		OwnerID:      userID,
		OwnerEmail:   ownerEmail,
		PlanID:       body.PlanID,
		Apps:         body.Apps,
		Agents:       body.Agents,
		AddOns:       body.AddOns,
		AppConfigs:   body.AppConfigs,
		Subdomain:    body.Slug,
		ParentDomain: parentDomain,
		Status:       initialStatus,
	}

	if err := h.Store.CreateTenant(r.Context(), tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	// Add the creator as owner member.
	member := &store.Member{
		TenantID: tenant.ID,
		UserID:   userID,
		Role:     "owner",
		JoinedAt: time.Now().UTC(),
	}
	if err := h.Store.AddMember(r.Context(), member); err != nil {
		slog.Error("failed to add owner as member", "tenant_id", tenant.ID, "error", err)
		// Tenant was created; don't fail the response, but log the error.
	}

	// #4956 — fire the provisioning triggers (tenant.created → Organization CR,
	// funnel cart-install, sandbox request) INLINE only when the caller did NOT
	// request a deferred launch. The marketplace funnel sets defer_launch=true
	// so nothing provisions until billing settlement calls the internal launch
	// endpoint; every non-funnel caller (BSS door, direct API) leaves it false
	// and launches immediately, exactly as before.
	if !body.DeferLaunch {
		h.launchTenant(r.Context(), tenant)
	} else {
		slog.Info("CreateOrg: deferred launch — Org parked at pending_payment until billing settles (#4956)",
			"tenant_id", tenant.ID, "slug", tenant.Slug, "apps", tenant.Apps)
	}

	// #4176/#4179: return the server-authoritative customer console host so
	// the marketplace redirects to `console.<slug>.<parent_domain>` (the
	// chosen pool apex) instead of re-deriving it from the marketplace host
	// (which on marketplace.omantel.biz yields the unreachable
	// console.<slug>.omantel.biz). We embed the full Tenant so every existing
	// client field is unchanged and add `console_host` as a sibling. When the
	// caller omitted parent_domain (single-domain Sovereign back-compat),
	// console_host is empty and the client keeps its host-splice fallback.
	respond.JSON(w, http.StatusCreated, tenantWithConsoleHost{
		Tenant:      tenant,
		ConsoleHost: deriveTenantConsoleHost(tenant),
	})
}

// dispatchFunnelCartInstall renders the customer's selected cart apps into the
// per-Org gitops tree at signup time (#4360, Refs #4272 #4307 #4322 #4179).
//
// The marketplace funnel (CreateOrg) persists the chosen `Apps` on the Tenant
// and emits `tenant.created`, which mints ONLY the Organization CR — the
// org-controller then renders the boundary namespace + vCluster + network
// policies, but NOT the cart Applications. This helper closes that gap by
// dispatching the SAME day-2 install path InstallApp uses for each deployable
// cart app, so the provisioning service commits the Applications into the
// per-Org `vcluster/apps/` tree (the applyTenantChange → GenerateAllWithPassword
// path Flux reconciles into the Org boundary once it is up). It is the funnel
// analogue of the BSS door's Step-6 cart placement.
//
// Best-effort + non-fatal: a catalog miss, a non-deployable slug, or a publish
// failure is logged loud but never fails Org creation (the shell is already
// minted; the day-2 page can re-install). `sandbox` is excluded — it has its
// own `tenant.sandbox_requested` dispatch. When the Catalog client is unwired
// (capacity was validated at checkout, the consumer resolves slugs itself) the
// helper still dispatches the raw cart slugs so provisioning can render them.
func (h *Handler) dispatchFunnelCartInstall(ctx context.Context, t *store.Tenant) {
	if t == nil || len(t.Apps) == 0 {
		return
	}

	// Resolve the catalog so we can (a) filter to deployable apps and (b) map
	// slug↔ID. When the Catalog is unwired we cannot filter, so we fall back to
	// dispatching the raw cart entries (minus sandbox) and let the provisioning
	// consumer resolve + skip non-deployable slugs.
	var bySlug map[string]*catalog.App
	var byID map[string]*catalog.App
	if h.Catalog != nil {
		if apps, err := h.Catalog.ListApps(ctx); err == nil {
			bySlug = make(map[string]*catalog.App, len(apps))
			byID = make(map[string]*catalog.App, len(apps))
			for i := range apps {
				bySlug[apps[i].Slug] = &apps[i]
				byID[apps[i].ID] = &apps[i]
			}
		} else {
			slog.Warn("funnel cart-install: catalog list failed — dispatching raw cart slugs",
				"tenant_id", t.ID, "slug", t.Subdomain, "error", err)
		}
	}

	for _, entry := range t.Apps {
		// Cart entries may be catalog IDs or slugs depending on the marketplace
		// build. Normalize to (slug, id) via whichever index hits.
		slug, id := entry, ""
		if byID != nil {
			if a, ok := byID[entry]; ok {
				slug, id = a.Slug, a.ID
			} else if a, ok := bySlug[entry]; ok {
				slug, id = a.Slug, a.ID
			}
		}
		// Sandbox has its own dispatch (tenant.sandbox_requested) — never push it
		// through the app-install path (it has no provisioning Deployment template).
		if slug == "sandbox" {
			continue
		}
		// Filter to deployable apps when the catalog is available. A catalog entry
		// that is listed-but-not-deployable (e.g. openclaw/stalwart-mail, which
		// need HelmRelease overlays the one-Deployment generator can't emit) is
		// skipped with a loud log rather than dispatched to fail downstream.
		if bySlug != nil {
			a, ok := bySlug[slug]
			if !ok {
				slog.Warn("funnel cart-install: cart app not in catalog — skipping",
					"tenant_id", t.ID, "slug", t.Subdomain, "app", slug)
				continue
			}
			if !a.Deployable {
				slog.Warn("funnel cart-install: cart app not deployable yet — skipping (day-2 page can retry once the template ships)",
					"tenant_id", t.ID, "slug", t.Subdomain, "app", slug)
				continue
			}
		}

		// Mirror InstallApp's dispatch shape exactly so the provisioning service
		// dedups + renders identically across the funnel and day-2 doors.
		idempotencyKey := t.ID + ":cart:" + slug
		payload := map[string]any{
			"tenant_id":       t.ID,
			"tenant_slug":     t.Subdomain,
			"plan_id":         t.PlanID,
			"app_slug":        slug,
			"app_id":          id,
			"idempotency_key": idempotencyKey,
			"deploy_ids":      []string{},
			"deploy_slugs":    []string{slug},
			"apps":            t.Apps,
		}
		if err := h.callProvisioning(ctx, "/provisioning/apps/install", payload); err != nil {
			slog.Error("funnel cart-install: provisioning HTTP call failed (event fallback still fires)",
				"tenant_id", t.ID, "slug", t.Subdomain, "app", slug, "error", err)
		}
		if evt, err := events.NewEvent("tenant.app_install_requested", "tenant-service", t.ID, payload); err == nil {
			pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if pubErr := h.Producer.Publish(pubCtx, "org.tenant.events", evt); pubErr != nil {
				slog.Debug("funnel cart-install: event publish (best-effort)",
					"tenant_id", t.ID, "app", slug, "error", pubErr)
			}
			cancel()
		}
		slog.Info("funnel cart-install: dispatched cart app",
			"tenant_id", t.ID, "slug", t.Subdomain, "app", slug)
	}
}

// launchTenant fires the provisioning triggers for a tenant: the
// `tenant.created` event (→ provisioning mints the Organization CR → the
// org-controller renders the boundary ns + vCluster + network policies), the
// funnel cart-install (→ the customer's purchased Applications land in the
// per-Org gitops tree), and the Sandbox request (when the cart holds sandbox).
//
// #4956 — this is the SINGLE place the funnel Org is provisioned. On the
// immediate path (defer_launch=false) CreateOrg calls it inline; on the
// deferred path it is called by InternalLaunchTenant ONLY after billing
// settlement, so a checkout that 400s / is abandoned can never leave a
// provisioned Org behind. owner_email + agents are read off the persisted
// tenant (not the request), so the deferred call — which runs server-to-server
// with no user context — still emits a complete tenant.created / sandbox
// request. Best-effort + non-blocking: broker outages log loud but never fail
// the caller (the Org shell already exists; the day-2 page / redelivery
// recover).
func (h *Handler) launchTenant(ctx context.Context, t *store.Tenant) {
	if t == nil {
		return
	}
	// #3687 (fold #3690/#3673): emit the ONE canonical
	// events.TenantCreatedPayload — the same struct the provisioning consumer
	// decodes into and the bootstrap-API funnel maps onto. Tier / BillingMode
	// stay empty here (the Organization-pool wizard default); the consumer
	// applies the canonical defaults (tier→"org", billing→"real").
	//
	// #4176/#4179: ParentDomain is carried through so the provisioning consumer
	// can create the per-Org `console.<slug>.<parent_domain>` record + HTTPRoute.
	tenantCreatedPayload := events.NewTenantCreatedPayload(
		t.ID, t.Slug, t.Name, t.OwnerID, t.OwnerEmail,
		t.PlanID, "", "", t.ParentDomain)
	if evt, err := events.NewEvent("tenant.created", "tenant-service", t.ID, tenantCreatedPayload); err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if pubErr := h.Producer.Publish(pubCtx, "org.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.created event", "tenant_id", t.ID, "error", pubErr)
		}
		pubCancel()
	}

	// Funnel cart-placement (#4360): render each deployable cart app into the
	// per-Org gitops tree. Absent here, the customer's purchased apps never land
	// (the vcluster/apps tree carried only networkpolicy.yaml).
	h.dispatchFunnelCartInstall(ctx, t)

	// Wave 4 — Sandbox: when the cart contains the sandbox product, emit
	// `tenant.sandbox_requested` so the sandbox-orchestrator can mint a Sandbox
	// CR with `spec.agentCatalogue` matching the persisted picks (t.Agents).
	if containsSlug(t.Apps, "sandbox") {
		sandboxPayload := map[string]any{
			"tenant_id":    t.ID,
			"org_slug":     t.Slug,
			"owner_id":     t.OwnerID,
			"agents":       t.Agents,
			"sovereign":    "", // populated by the consumer from its env / cluster context
			"plan_id":      t.PlanID,
			"requested_at": time.Now().UTC().Format(time.RFC3339),
		}
		if sbEvt, sbErr := events.NewEvent("tenant.sandbox_requested", "tenant-service", t.ID, sandboxPayload); sbErr == nil {
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if pubErr := h.Producer.Publish(pubCtx, "org.tenant.events", sbEvt); pubErr != nil {
				slog.Error("failed to publish tenant.sandbox_requested event", "tenant_id", t.ID, "error", pubErr)
			}
			pubCancel()
		}
	}
}

// InternalLaunchTenant transitions a DEFERRED (pending_payment) Org to
// provisioning and fires its launch — the settlement gate for #4956. It is
// called SERVER-TO-SERVER by the billing service from dispatchOrderPlaced once a
// checkout settles (credit-only OR Stripe webhook), so an Org launches iff its
// order was actually placed.
//
// Security: this lives under `/tenant/internal/*`, which both edge gateways 401
// externally (see main.go's JWT-bypass note) — it is reachable only by
// in-cluster callers, never the browser. So the customer CANNOT self-launch to
// skip payment; only billing (post-settlement) can.
//
// Idempotent + race-safe: the pending_payment→provisioning transition is a
// conditional atomic store update, so repeated settlement dispatches (credit +
// Stripe retry) launch exactly once. A tenant already past pending_payment
// (immediate-launch caller, or a second settlement) is a benign 200 no-op. The
// downstream tenant.created (409 AlreadyExists) + cart-install (idempotency-key)
// dedups are the defence-in-depth backstop.
func (h *Handler) InternalLaunchTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	t, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch tenant")
		return
	}
	if t == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Only a pending_payment Org is launchable here. Win the atomic transition
	// before firing the triggers so a concurrent settlement dispatch can't
	// double-launch.
	won, err := h.Store.TryTransitionTenantStatus(r.Context(), id, statusPendingPayment, "provisioning")
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to transition tenant status")
		return
	}
	if !won {
		// Already launched (immediate caller or a prior settlement) — benign.
		slog.Info("internal launch: tenant not in pending_payment — no-op",
			"tenant_id", id, "status", t.Status)
		respond.JSON(w, http.StatusOK, map[string]any{
			"id": id, "launched": false, "status": t.Status,
		})
		return
	}

	t.Status = "provisioning"
	slog.Info("internal launch: billing settlement — launching deferred Org (#4956)",
		"tenant_id", id, "slug", t.Slug, "apps", t.Apps)
	h.launchTenant(r.Context(), t)
	respond.JSON(w, http.StatusOK, map[string]any{
		"id": id, "launched": true, "status": "provisioning",
	})
}

// resolveOrgParentDomain picks the org-pool parent zone the Tenant's
// ParentDomain (→ console_host + tenant.created payload) is stamped with.
// #4821 Finding-2: it MIRRORS the provisioning door's resolver of the same name
// (core/services/provisioning/handlers/organization_create.go) so BOTH doors —
// and the org-controller CR they mint — agree on the pool.
//
// THE INVARIANT (#4421): the pool this service returns as the customer's
// console_host MUST equal the pool the org-controller writes the per-Org DNS
// A-record + console TLS cert + HTTPRoute under. appsParentDomain is the
// Sovereign's EFFECTIVE apps pool (env TENANT_PARENT_DOMAIN); it is non-empty on
// any real Sovereign, so it WINS. The per-customer funnel pick is honoured only
// as a last-resort fallback on a degenerate Sovereign with no apps pool wired
// (which then matches the org-controller's own empty-appsPool fallback).
// Returns lowercased/trimmed.
func resolveOrgParentDomain(appsParentDomain, funnelParentDomain string) string {
	if pd := strings.ToLower(strings.TrimSpace(appsParentDomain)); pd != "" {
		return pd
	}
	return strings.ToLower(strings.TrimSpace(funnelParentDomain))
}

// deriveTenantConsoleHost composes the per-Org customer console host:
//
//	console.<subdomain>.<parent_domain>   (org-pool Sovereign — the funnel)
//
// Returns "" when the parent apex is unknown (legacy / single-domain
// Sovereigns), in which case the marketplace client falls back to splicing
// the slug onto the marketplace host. Mirrors the bootstrap-API
// organization_provisioning.go::deriveConsoleHost contract so both org-create
// doors emit the identical host. #4176/#4179.
func deriveTenantConsoleHost(t *store.Tenant) string {
	if t == nil {
		return ""
	}
	sub := strings.ToLower(strings.TrimSpace(t.Subdomain))
	parent := strings.ToLower(strings.TrimSpace(t.ParentDomain))
	if sub == "" || parent == "" {
		return ""
	}
	return "console." + sub + "." + parent
}

// containsSlug returns true iff slug appears in slugs. Used to gate the
// Sandbox-specific `tenant.sandbox_requested` event emission so the
// CreateOrg hot path doesn't pay an event marshal for tenants that
// never picked the Sandbox product.
func containsSlug(slugs []string, slug string) bool {
	for _, s := range slugs {
		if s == slug {
			return true
		}
	}
	return false
}

// ListOrgs returns all organizations where the authenticated user is a member.
func (h *Handler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}

	tenants, err := h.Store.ListTenantsByOwner(r.Context(), userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list organizations")
		return
	}
	// #4176/#4179: enrich each Org with its server-derived console_host so
	// the marketplace "returning visitor / cache" redirect path (CheckoutStep
	// matches an existing Org from getMyOrgs) lands on the same correct
	// `console.<slug>.<parent_domain>` host as a fresh create.
	respond.OK(w, withConsoleHost(tenants))
}

// tenantWithConsoleHost embeds a Tenant and adds the server-authoritative
// `console_host` sibling field (empty for legacy / single-domain Orgs).
// #4176/#4179.
type tenantWithConsoleHost struct {
	*store.Tenant
	ConsoleHost string `json:"console_host,omitempty"`
}

// withConsoleHost wraps a slice of Tenants, attaching each Org's derived
// console host. Used by ListOrgs so the response shape matches CreateOrg.
func withConsoleHost(tenants []store.Tenant) []tenantWithConsoleHost {
	out := make([]tenantWithConsoleHost, 0, len(tenants))
	for i := range tenants {
		t := tenants[i]
		out = append(out, tenantWithConsoleHost{Tenant: &t, ConsoleHost: deriveTenantConsoleHost(&t)})
	}
	return out
}

// GetOrg returns a single organization by ID (membership required).
func (h *Handler) GetOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireMembership(w, r, id); !ok {
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}
	// #4176/#4179: same console_host enrichment as ListOrgs/CreateOrg.
	respond.OK(w, tenantWithConsoleHost{Tenant: tenant, ConsoleHost: deriveTenantConsoleHost(tenant)})
}

// UpdateOrg updates an organization (owner/admin only).
func (h *Handler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	existing, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if existing == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	var body struct {
		Name          *string  `json:"name"`
		OrgType       *string  `json:"org_type"`
		Industry      *string  `json:"industry"`
		PlanID        *string  `json:"plan_id"`
		Apps          []string `json:"apps"`
		AddOns        []string `json:"addons"`
		Subdomain     *string  `json:"subdomain"`
		CustomDomains []string `json:"custom_domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Apply partial updates.
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.OrgType != nil {
		existing.OrgType = *body.OrgType
	}
	if body.Industry != nil {
		existing.Industry = *body.Industry
	}
	if body.PlanID != nil {
		existing.PlanID = *body.PlanID
	}
	if body.Apps != nil {
		existing.Apps = body.Apps
	}
	if body.AddOns != nil {
		existing.AddOns = body.AddOns
	}
	if body.Subdomain != nil {
		existing.Subdomain = *body.Subdomain
	}
	if body.CustomDomains != nil {
		existing.CustomDomains = body.CustomDomains
	}

	if err := h.Store.UpdateTenant(r.Context(), id, existing); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update organization")
		return
	}
	respond.OK(w, existing)
}

// DeleteOrg soft-deletes an organization (owner only).
func (h *Handler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !h.requireOwner(w, r, id) {
		return
	}

	existing, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if existing == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	// Soft delete: set status to "deleted".
	existing.Status = "deleted"
	if err := h.Store.UpdateTenant(r.Context(), id, existing); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete organization")
		return
	}

	// Publish tenant.deleted event (non-blocking). Slug is required by the
	// provisioning consumer so it can locate the tenant's GitOps directory.
	evt, err := events.NewEvent("tenant.deleted", "tenant-service", id, map[string]string{
		"id":   id,
		"slug": existing.Subdomain,
	})
	if err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer pubCancel()
		if pubErr := h.Producer.Publish(pubCtx, "org.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.deleted event", "tenant_id", id, "error", pubErr)
		}
	}

	respond.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

// ListMembers returns all members of an organization (membership required).
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireMembership(w, r, id); !ok {
		return
	}

	members, err := h.Store.ListMembers(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list members")
		return
	}
	respond.OK(w, members)
}

// InviteMember adds a new member to an organization (owner/admin only).
func (h *Handler) InviteMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	var body struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.Email == "" {
		respond.Error(w, http.StatusBadRequest, "email is required")
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	// Prevent adding a second owner.
	if body.Role == "owner" {
		respond.Error(w, http.StatusBadRequest, "cannot assign owner role via invitation")
		return
	}
	if body.Role != "admin" && body.Role != "member" && body.Role != "viewer" {
		respond.Error(w, http.StatusBadRequest, "role must be admin, member, or viewer")
		return
	}

	member := &store.Member{
		TenantID: id,
		UserID:   body.UserID,
		Email:    body.Email,
		Role:     body.Role,
		JoinedAt: time.Now().UTC(),
	}
	if err := h.Store.AddMember(r.Context(), member); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	respond.JSON(w, http.StatusCreated, member)
}

// RemoveMember removes a member from an organization (owner/admin only, can't remove owner).
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	targetUserID := r.PathValue("userId")

	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	// Check the target's role — cannot remove the owner.
	targetRole, err := h.Store.GetMemberRole(r.Context(), id, targetUserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check member role")
		return
	}
	if targetRole == "" {
		respond.Error(w, http.StatusNotFound, "member not found")
		return
	}
	if targetRole == "owner" {
		respond.Error(w, http.StatusForbidden, "cannot remove the owner")
		return
	}

	if err := h.Store.RemoveMember(r.Context(), id, targetUserID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to remove member")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ---------------------------------------------------------------------------
// Slug check (public)
// ---------------------------------------------------------------------------

// CheckSlug returns whether a slug is available.
func (h *Handler) CheckSlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		respond.Error(w, http.StatusBadRequest, "slug is required")
		return
	}
	available, err := h.Store.CheckSlugAvailable(r.Context(), slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check slug")
		return
	}
	respond.OK(w, map[string]bool{"available": available})
}

// ---------------------------------------------------------------------------
// Admin endpoints
// ---------------------------------------------------------------------------

// AdminListTenants returns a paginated list of all tenants (superadmin only).
func (h *Handler) AdminListTenants(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	q := r.URL.Query().Get("q")
	if q != "" {
		tenants, err := h.Store.SearchTenants(r.Context(), q)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to search tenants")
			return
		}
		respond.OK(w, map[string]any{"tenants": tenants, "total": len(tenants)})
		return
	}

	tenants, total, err := h.Store.ListAllTenants(r.Context(), offset, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	respond.OK(w, map[string]any{"tenants": tenants, "total": total, "offset": offset, "limit": limit})
}

// AdminGetTenant returns any tenant by ID (superadmin only).
func (h *Handler) AdminGetTenant(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.OK(w, tenant)
}

// AdminUpdateStatus changes a tenant's status (superadmin only).
func (h *Handler) AdminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	validStatuses := map[string]bool{"active": true, "suspended": true, "provisioning": true, "deleted": true}
	if !validStatuses[body.Status] {
		respond.Error(w, http.StatusBadRequest, "status must be active, suspended, provisioning, or deleted")
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	tenant.Status = body.Status
	if err := h.Store.UpdateTenant(r.Context(), id, tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update tenant status")
		return
	}
	respond.OK(w, tenant)
}

// AdminDeleteTenant soft-deletes any tenant and publishes tenant.deleted
// (superadmin only, no membership check).
func (h *Handler) AdminDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	// Already soft-deleted — a subsequent DELETE request should 404, not
	// return 'deleted' status again. Caught by dod-chaos scenario8_negativeOps
	// on 2026-04-20: a second admin delete was quietly returning 200 which
	// would cause duplicate tenant.deleted events to fire and confuse audit
	// trails.
	if tenant.Status == "deleted" {
		respond.Error(w, http.StatusNotFound, "tenant already deleted")
		return
	}

	tenant.Status = "deleted"
	if err := h.Store.UpdateTenant(r.Context(), id, tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}

	evt, err := events.NewEvent("tenant.deleted", "tenant-service", id, map[string]string{
		"id":   id,
		"slug": tenant.Subdomain,
	})
	if err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer pubCancel()
		if pubErr := h.Producer.Publish(pubCtx, "org.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.deleted event", "tenant_id", id, "error", pubErr)
		}
	}

	respond.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// InternalGetSubdomain returns the tenant's subdomain by ID. No auth — this
// route is only registered at the cluster-internal service IP and is used
// by billing to enrich order.placed events with the subdomain that
// store.Order doesn't carry. Returning just id+subdomain (no other
// sensitive fields) keeps the blast radius small even if the path were
// ever exposed at a gateway by accident. Issue #105.
func (h *Handler) InternalGetSubdomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	t, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch tenant")
		return
	}
	if t == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{
		"id":        t.ID,
		"subdomain": t.Subdomain,
	})
}

// InternalGetAppConfigs returns the tenant's per-app configSchema values
// (Tenant.AppConfigs) keyed by app slug. No auth — same security model as
// InternalGetSubdomain: cluster-internal only. Billing calls this when
// dispatching order.placed so the provisioning consumer can thread the
// customer-chosen values (replicas, disk_gb, backups_enabled) into the
// rendered manifests for postgres/mysql/redis backing services.
//
// TBD-V27 (#2042) — closes the 10-step deterministic walk step that was
// dropping customer-picked configSchema values between the Tenant store
// (PR #2043 persisted them) and the materialised app manifest.
//
// Response shape: {"id": "<tid>", "app_configs": {"<slug>": {<key>: <val>}}}.
// Empty map (not null) when the tenant has no AppConfigs — keeps the
// caller's null-vs-{} branch simple.
func (h *Handler) InternalGetAppConfigs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	t, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch tenant")
		return
	}
	if t == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	cfg := t.AppConfigs
	if cfg == nil {
		cfg = map[string]map[string]any{}
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"id":          t.ID,
		"app_configs": cfg,
	})
}
