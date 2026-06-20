// Package handler — organization_provisioning.go: SME tenant provisioning pipeline
// orchestrator (issue #804).
//
// This is the back-end for the marketplace's "Sign up an SME tenant"
// flow. The full epic is #795 — tenancy is K8s-native (per Inviolable
// Principle 7) so a tenant is materialised by:
//
//  1. A vCluster inside the OTECH cluster (the SME's logical cluster).
//  2. A namespace `sme-<tenant-id>` in the OTECH cluster (Secret-as-
//     truth for per-user NewAPI keys + per-host TLS Certificates).
//  3. The 4 sister bp-* charts installed inside the SME vcluster
//     (bp-keycloak per-organization, bp-cnpg, bp-wordpress-tenant,
//     bp-openclaw, bp-stalwart-tenant).
//  4. DNS records (free-subdomain via PowerDNS API) or BYO-CNAME
//     validation (the customer's own DNS).
//  5. cert-manager Certificate (per-host HTTP-01 for BYO; the
//     wildcard `*.<otech-fqdn>` already covers free-subdomain).
//  6. OIDC clients pre-created in the SME-vcluster Keycloak
//     (WordPress, OpenClaw, Stalwart, unified-RBAC SME-tier) with
//     group templates `sme-admin` + `sme-user`.
//  7. A row in the host → tenant registry (consumed by the
//     public `/api/v1/tenant/discover` endpoint per #802) so the
//     SPA's first hit on `console.<sme-host>` resolves to the new
//     tenant.
//
// State machine: see store.OrganizationProvisionState. Each step is
// independently idempotent; the orchestrator persists the row at every
// state transition so a Pod restart never strands a half-provisioned
// tenant. The reconciler is event-driven (NATS subject
// `sme.tenant.reconcile-pending`) per Inviolable Principle 1 and
// ADR-0001 §6 — never a Kubernetes CronJob, never a goroutine
// `time.Tick`.
//
// HTTP surface:
//
//	POST   /api/v1/sme/tenants            — create + start pipeline
//	GET    /api/v1/sme/tenants            — list tenants
//	GET    /api/v1/sme/tenants/{id}       — read one
//	POST   /api/v1/sme/tenants/{id}/reconcile — operator-triggered
//	                                        re-run from current state
//	DELETE /api/v1/sme/tenants/{id}       — inverse pipeline
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the orchestrator NEVER calls
// `kubectl apply`. Manifests are committed to a per-tenant overlay
// path in the GitOps repo (see organization_gitops.go); Flux on the
// OTECH cluster reconciles them. Crossplane XR claims for the
// vCluster MAY be used when the openova-io vcluster Composition is
// shipped (#322) — until then, the overlay HelmRelease is the canonical
// seam.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL / chart version /
// image ref is configurable at runtime via env or operator-supplied
// request fields — the orchestrator never inlines them.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// OrganizationGitOpsWriter is the seam through which the orchestrator
// authors per-tenant overlay manifests. The default implementation
// (organization_gitops.go) clones the GitOps repo, generates the per-
// tenant Kustomize overlay from the in-package template, commits, and
// pushes. Tests inject a stub that records the (rec, action) pair so
// the state machine can be exercised end-to-end without a real
// GitOps repo.
type OrganizationGitOpsWriter interface {
	// WriteTenantOverlay generates + commits the per-tenant overlay
	// for the supplied record. Returns the commit SHA on success.
	WriteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error)
	// DeleteTenantOverlay removes the per-tenant overlay path for the
	// supplied record. Idempotent; returns the commit SHA on success.
	DeleteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error)
}

// OrganizationDNSProvisioner provisions DNS for the SME's
// `console.<host>` either via PowerDNS (free-subdomain) or by
// validating an operator-supplied CNAME (BYO).
//
// The free-subdomain method takes a `parentZone` parameter so the
// multi-domain Sovereign (epic #825) can write records under any of
// its role:sme-pool zones; on a single-domain Sovereign the wired
// caller supplies OTECHFQDN, preserving #804 behaviour.
//
// The BYO method takes an `acceptedTargets` slice so the validator
// accepts a CNAME pointing at ANY parent in the pool, not just the
// primary OTECHFQDN. The slice MUST be non-empty; nil/empty falls
// back to the legacy single-target path for backward compat.
type OrganizationDNSProvisioner interface {
	// ProvisionFreeSubdomain creates A/CNAME records for
	// `console.<subdomain>.<parentZone>` plus the per-app sister
	// hostnames. Idempotent; returns nil on "record already exists
	// with the same RDATA" outcomes.
	ProvisionFreeSubdomain(ctx context.Context, subdomain, parentZone, ingressIPv4 string) error
	// ValidateBYOCNAME resolves `console.<byo_domain>` and confirms
	// it CNAMEs to one of acceptedTargets (or, when nil/empty, to
	// the supplied legacyTarget). Returns a structured error when the
	// lookup fails or the target doesn't match — the orchestrator
	// surfaces those in the wizard UI so the customer can fix their
	// own DNS before the pipeline can advance.
	ValidateBYOCNAME(ctx context.Context, byoDomain, legacyTarget string, acceptedTargets ...string) error
}

// OrganizationKeycloakClientProvisioner pre-creates OIDC clients +
// group templates in the SME-vcluster Keycloak realm. Stubbed in
// tests; the production wiring is the in-cluster admin API per
// platform/keycloak chart values.
type OrganizationKeycloakClientProvisioner interface {
	ProvisionOrganizationClients(ctx context.Context, rec store.OrganizationProvisionRecord) error
}

// OrganizationEventEmitter publishes lifecycle events on the canonical
// `org.tenant.events` topic (see core/services/shared/events/topics.go).
type OrganizationEventEmitter interface {
	EmitOrganizationCreated(ctx context.Context, rec store.OrganizationProvisionRecord) error
	EmitOrganizationStateChanged(ctx context.Context, rec store.OrganizationProvisionRecord) error
	EmitOrganizationDeleted(ctx context.Context, rec store.OrganizationProvisionRecord) error
}

// OrganizationParentDomain describes one parent domain the Sovereign
// brought at signup (epic #825 / MD-1 #826) that is offered to SMEs.
// One Sovereign typically holds several: a `primary` parent (the one
// hosting `console.<sovereign>`) plus zero-or-more `sme-pool` parents
// the SME tenant pipeline (this file) writes free-subdomains under.
//
// Per Inviolable Principle 4 the pool is fully data-driven; #828
// neither hardcodes nor caps the count.
type OrganizationParentDomain struct {
	// Name — the FQDN itself, e.g. "omani.trade".
	Name string `json:"name"`
	// Role — "primary" | "sme-pool". The SME tenant create endpoint
	// only accepts entries with role=sme-pool.
	Role string `json:"role"`
	// NSFlipReady — true once the registrar's NS records point at
	// the Sovereign's PowerDNS (set by the Sovereign provisioning
	// pipeline / MD-1). The SME create endpoint refuses to write a
	// free-subdomain into a parent that isn't NS-flip-ready yet.
	NSFlipReady bool `json:"ns_flip_ready"`
}

// OrganizationDeps bundles the dependencies the SME tenant handlers
// need. Wired at startup; nil values turn the corresponding gate
// into a no-op (see runOrganizationPipeline below) so the handler
// degrades gracefully in CI / Sovereign-side without these wired.
type OrganizationDeps struct {
	Store            *store.OrganizationProvisionStore
	GitOps           OrganizationGitOpsWriter
	DNS              OrganizationDNSProvisioner
	KeycloakClients  OrganizationKeycloakClientProvisioner
	Events           OrganizationEventEmitter
	TenantRegistry   *store.TenantRegistry
	OTECHFQDN        string
	OTECHIngressIPv4 string
	// ParentDomains — the multi-domain pool config (epic #825 / MD-1
	// #826). Wired at startup from MD-1's data-model output (or, while
	// MD-1 is in flight, from CATALYST_ORG_POOL_DOMAINS env stub).
	// Empty/nil means "single-domain Sovereign": the only parent is
	// OTECHFQDN itself with role=sme-pool, ns_flip_ready=true.
	ParentDomains []OrganizationParentDomain
	// MaxRetryCount — promoted to STSFailed at this many transient
	// failures of the same step. Per ADR-0003 §3.8 = 5.
	MaxRetryCount int
}

// PoolDomains returns the subset of ParentDomains with role=sme-pool.
// Includes the implicit OTECHFQDN entry when the wired list is empty
// (single-domain Sovereign — backward-compat with #804).
func (d OrganizationDeps) PoolDomains() []OrganizationParentDomain {
	if len(d.ParentDomains) == 0 {
		if strings.TrimSpace(d.OTECHFQDN) == "" {
			return nil
		}
		return []OrganizationParentDomain{
			{Name: d.OTECHFQDN, Role: "sme-pool", NSFlipReady: true},
		}
	}
	out := make([]OrganizationParentDomain, 0, len(d.ParentDomains))
	for _, p := range d.ParentDomains {
		if strings.EqualFold(p.Role, "sme-pool") {
			out = append(out, p)
		}
	}
	return out
}

// FindParentDomain looks up a parent by name (case-insensitive) in the
// wired pool. Returns the matching entry + true on hit. Used by the
// create handler to validate operator-supplied parent_domain values.
func (d OrganizationDeps) FindParentDomain(name string) (OrganizationParentDomain, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range d.PoolDomains() {
		if strings.ToLower(p.Name) == name {
			return p, true
		}
	}
	return OrganizationParentDomain{}, false
}

// SetOrganizationDeps wires the SME-tenant pipeline dependencies. Called
// by main.go at startup; tests pass a struct with stub clients.
func (h *Handler) SetOrganizationDeps(deps OrganizationDeps) {
	if deps.MaxRetryCount == 0 {
		deps.MaxRetryCount = 5
	}
	h.orgTenantDeps = deps
}

/* ── wire shapes ─────────────────────────────────────────────────── */

type orgTenantCreateRequest struct {
	// Subdomain — the SME slug (e.g. "acme"). Required for both
	// free-subdomain and BYO modes (used in resource names + the
	// vCluster name `vc-<subdomain>`).
	Subdomain string `json:"subdomain"`
	// DomainMode — "free-subdomain" (default) or "byo".
	DomainMode string `json:"domain_mode,omitempty"`
	// BYODomain — required when DomainMode == "byo". The orchestrator
	// derives the host as `console.<byo_domain>`.
	BYODomain string `json:"byo_domain,omitempty"`
	// ParentDomain — required when DomainMode == "free-subdomain"
	// and the Sovereign has more than one entry in its sme-pool. When
	// omitted with a multi-entry pool the orchestrator defaults to the
	// first NS-flip-ready entry. Must match (case-insensitive) one of
	// the entries returned by GET /api/v1/sovereign/parent-domains
	// with role=sme-pool. Per epic #825 the resulting host becomes
	// `console.<subdomain>.<parent_domain>` — never inferred from
	// OTECHFQDN.
	ParentDomain string `json:"parent_domain,omitempty"`
	// AdminEmail — the SME's first user (chart admin email + welcome
	// recipient). Required.
	AdminEmail string `json:"admin_email"`
	// CompanyName — branding metadata; optional.
	CompanyName string `json:"company_name,omitempty"`

	// ── Organizations internal door (issue #3378 B1) ──
	//
	// These map onto OrganizationSpec Kind/Tier/BillingMode + the
	// derived Isolation. When omitted (the marketplace funnel = the
	// customer door) the handler stamps the customer default shape so
	// the funnel is byte-unchanged. kind="internal" (this menu's Create
	// = the internal door) stamps the department shape (showback +
	// namespace) and skips the voucher dependency — no voucher step for
	// an internal org. The handler resolves billing_mode + isolation
	// from kind when those two are omitted (the kind-derived default;
	// the advanced-view override sends them explicitly).
	Kind        string `json:"kind,omitempty"`
	Tier        string `json:"tier,omitempty"`
	BillingMode string `json:"billing_mode,omitempty"`
	Isolation   string `json:"isolation,omitempty"`
}

// orgShape is the resolved Organizations-model shape (issue #3378 B1)
// the create handler stamps onto the provision record.
type orgShape struct {
	Kind        string
	Tier        string
	BillingMode string
	Isolation   string
}

// resolveOrgShape applies the §2.1/§2.3 model: kind defaults to
// "customer" (the marketplace door); kind-derived billingMode +
// isolation defaults (internal → showback + namespace; customer → real +
// vcluster) fill in when the advanced override didn't send them; tier
// defaults to "sme". Unknown enum values fall back to the kind default
// so a malformed body can never stamp a nonsense shape.
func resolveOrgShape(req orgTenantCreateRequest) orgShape {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "internal" && kind != "customer" {
		kind = "customer"
	}

	// kind-derived defaults (§2.3).
	defBilling, defIsolation := "real", "vcluster"
	if kind == "internal" {
		defBilling, defIsolation = "showback", "namespace"
	}

	billing := strings.ToLower(strings.TrimSpace(req.BillingMode))
	switch billing {
	case "real", "chargeback", "showback":
	default:
		billing = defBilling
	}

	isolation := strings.ToLower(strings.TrimSpace(req.Isolation))
	switch isolation {
	case "namespace", "vcluster":
	default:
		isolation = defIsolation
	}

	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	switch tier {
	case "sme", "corporate":
	default:
		tier = "sme"
	}

	return orgShape{Kind: kind, Tier: tier, BillingMode: billing, Isolation: isolation}
}

type orgTenantResponse struct {
	OrganizationID     string                        `json:"sme_tenant_id"`
	State           store.OrganizationProvisionState `json:"state"`
	Subdomain       string                        `json:"subdomain"`
	DomainMode      store.OrganizationDomainMode           `json:"domain_mode"`
	BYODomain       string                        `json:"byo_domain,omitempty"`
	ParentDomain    string                        `json:"parent_domain,omitempty"`
	AdminEmail      string                        `json:"admin_email"`
	CompanyName     string                        `json:"company_name,omitempty"`
	OTECHFQDN       string                        `json:"otech_fqdn"`
	VClusterName    string                        `json:"vcluster_name"`
	TenantNamespace string                        `json:"tenant_namespace"`
	ConsoleHost     string                        `json:"console_host"`
	// Organizations model (issue #3378 B1) — surfaced so the directory
	// can badge the org by kind/tier/billingMode/isolation.
	Kind        string         `json:"kind,omitempty"`
	Tier        string         `json:"tier,omitempty"`
	BillingMode string         `json:"billing_mode,omitempty"`
	Isolation   string         `json:"isolation,omitempty"`
	CommitSHA   string         `json:"commit_sha,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	Steps       orgTenantSteps `json:"steps"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// orgTenantSteps surfaces the 7-state machine to the SPA so it can
// render a progress timeline.
type orgTenantSteps struct {
	VCluster        string `json:"vcluster"`
	BPCharts        string `json:"bp_charts"`
	DNS             string `json:"dns"`
	Certs           string `json:"certs"`
	KeycloakClients string `json:"keycloak_clients"`
	Registry        string `json:"registry"`
}

// stepsForState surfaces "pending"|"done"|"failed" for each step the
// SPA renders. The state machine is linear — every state higher than
// X implies X is done.
func stepsForState(state store.OrganizationProvisionState, lastError string) orgTenantSteps {
	steps := orgTenantSteps{
		VCluster:        "pending",
		BPCharts:        "pending",
		DNS:             "pending",
		Certs:           "pending",
		KeycloakClients: "pending",
		Registry:        "pending",
	}
	switch state {
	case store.STSDone:
		steps.VCluster = "done"
		steps.BPCharts = "done"
		steps.DNS = "done"
		steps.Certs = "done"
		steps.KeycloakClients = "done"
		steps.Registry = "done"
	case store.STSTenantRegistered:
		steps.VCluster = "done"
		steps.BPCharts = "done"
		steps.DNS = "done"
		steps.Certs = "done"
		steps.KeycloakClients = "done"
		steps.Registry = "done"
	case store.STSKeycloakClientsProvisioned:
		steps.VCluster = "done"
		steps.BPCharts = "done"
		steps.DNS = "done"
		steps.Certs = "done"
		steps.KeycloakClients = "done"
	case store.STSCertsIssued:
		steps.VCluster = "done"
		steps.BPCharts = "done"
		steps.DNS = "done"
		steps.Certs = "done"
	case store.STSDNSProvisioned:
		steps.VCluster = "done"
		steps.BPCharts = "done"
		steps.DNS = "done"
	case store.STSBPChartsInstalled:
		steps.VCluster = "done"
		steps.BPCharts = "done"
	case store.STSVClusterCreated:
		steps.VCluster = "done"
	case store.STSFailed:
		// Mark whichever step failed. lastError carries the
		// `<step>:<class>:<detail>` triplet from ADR-0003 §3.8.
		switch {
		case strings.HasPrefix(lastError, "registry:"):
			steps.VCluster, steps.BPCharts, steps.DNS = "done", "done", "done"
			steps.Certs, steps.KeycloakClients = "done", "done"
			steps.Registry = "failed"
		case strings.HasPrefix(lastError, "keycloak_clients:"):
			steps.VCluster, steps.BPCharts, steps.DNS = "done", "done", "done"
			steps.Certs = "done"
			steps.KeycloakClients = "failed"
		case strings.HasPrefix(lastError, "certs:"):
			steps.VCluster, steps.BPCharts, steps.DNS = "done", "done", "done"
			steps.Certs = "failed"
		case strings.HasPrefix(lastError, "dns:"):
			steps.VCluster, steps.BPCharts = "done", "done"
			steps.DNS = "failed"
		case strings.HasPrefix(lastError, "bp_charts:"):
			steps.VCluster = "done"
			steps.BPCharts = "failed"
		case strings.HasPrefix(lastError, "vcluster:"):
			steps.VCluster = "failed"
		default:
			steps.VCluster = "failed"
		}
	}
	return steps
}

func orgTenantRecordToResponse(rec store.OrganizationProvisionRecord) orgTenantResponse {
	return orgTenantResponse{
		OrganizationID:     rec.OrganizationID,
		State:           rec.State,
		Subdomain:       rec.Subdomain,
		DomainMode:      rec.DomainMode,
		BYODomain:       rec.BYODomain,
		ParentDomain:    rec.ParentDomain,
		AdminEmail:      rec.AdminEmail,
		CompanyName:     rec.CompanyName,
		OTECHFQDN:       rec.OTECHFQDN,
		VClusterName:    rec.VClusterName,
		TenantNamespace: rec.TenantNamespace,
		ConsoleHost:     deriveConsoleHost(rec),
		Kind:            rec.Kind,
		Tier:            rec.Tier,
		BillingMode:     rec.BillingMode,
		Isolation:       rec.Isolation,
		CommitSHA:       rec.CommitSHA,
		LastError:       rec.LastError,
		Steps:           stepsForState(rec.State, rec.LastError),
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
	}
}

// deriveConsoleHost returns the SPA-facing host:
//   - free-subdomain (multi-domain Sovereign):
//     console.<subdomain>.<parent_domain>
//   - free-subdomain (single-domain back-compat, ParentDomain empty):
//     console.<subdomain>.<otech-fqdn>
//   - byo: console.<byo_domain>
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the leading `console.` prefix
// is conventional but configurable via the gitops template; the
// runtime SPA reads its tenant from the registry, not from string
// parsing. Per epic #825 the parent zone is data-driven — never
// hardcoded to OTECHFQDN.
func deriveConsoleHost(rec store.OrganizationProvisionRecord) string {
	switch rec.DomainMode {
	case store.OrganizationDomainBYO:
		if strings.TrimSpace(rec.BYODomain) == "" {
			return ""
		}
		return "console." + strings.TrimSpace(rec.BYODomain)
	case store.OrganizationDomainFreeSubdomain:
		fallthrough
	default:
		if strings.TrimSpace(rec.Subdomain) == "" {
			return ""
		}
		parent := strings.TrimSpace(rec.ParentDomain)
		if parent == "" {
			parent = strings.TrimSpace(rec.OTECHFQDN)
		}
		if parent == "" {
			return ""
		}
		return "console." + strings.TrimSpace(rec.Subdomain) + "." + parent
	}
}

// validSubdomain matches RFC 1123 label rules: 1-63 chars, lowercase
// alphanumerics + hyphens, can't start/end with hyphen.
var validSubdomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// validBYODomain is intentionally lax — the orchestrator accepts any
// shape that smells like a hostname so the operator can experiment.
// CNAME validation runs at the DNS step and reports structured errors.
var validBYODomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

/* ── HTTP handlers ───────────────────────────────────────────────── */

// HandleCreateOrganization — POST /api/v1/sme/tenants.
//
// The marketplace signup form POSTs here. The orchestrator persists
// the pending row, fires the pipeline synchronously, and returns the
// current (likely partial) state immediately — the SPA renders a
// progress timeline against the steps[] field while the reconciler
// runs in the background. Returns 202 (not 201) because the resource
// is "accepted, materialising" rather than "created".
func (h *Handler) HandleCreateOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-tenant-store-unavailable",
			"detail": "catalyst-api was started without an SME-tenant store",
		})
		return
	}

	var body orgTenantCreateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	subdomain := strings.ToLower(strings.TrimSpace(body.Subdomain))
	email := strings.TrimSpace(body.AdminEmail)
	mode := strings.TrimSpace(strings.ToLower(body.DomainMode))
	byo := strings.ToLower(strings.TrimSpace(body.BYODomain))
	parent := strings.ToLower(strings.TrimSpace(body.ParentDomain))

	if subdomain == "" {
		writeBadRequest(w, "subdomain-required", "subdomain is required")
		return
	}
	if !validSubdomain.MatchString(subdomain) {
		writeBadRequest(w, "subdomain-invalid",
			"subdomain must match RFC 1123 label rules (lowercase, alphanumerics + hyphens, 1-63 chars)")
		return
	}
	if email == "" || !strings.Contains(email, "@") {
		writeBadRequest(w, "admin-email-invalid", "admin_email must be a valid email")
		return
	}

	// Organizations model (issue #3378 B1): resolve the kind/tier/
	// billingMode/isolation shape. kind defaults to "customer" (the
	// marketplace funnel door) so the funnel is byte-unchanged; the
	// internal door sends kind="internal" → showback + namespace, no
	// voucher step. The resolved shape is stamped on the record below.
	shape := resolveOrgShape(body)

	if mode == "" {
		mode = string(store.OrganizationDomainFreeSubdomain)
	}
	if mode != string(store.OrganizationDomainFreeSubdomain) && mode != string(store.OrganizationDomainBYO) {
		writeBadRequest(w, "domain-mode-invalid",
			"domain_mode must be 'free-subdomain' or 'byo'")
		return
	}
	if mode == string(store.OrganizationDomainBYO) {
		if byo == "" || !validBYODomain.MatchString(byo) {
			writeBadRequest(w, "byo-domain-invalid",
				"byo_domain must be a valid hostname when domain_mode=byo")
			return
		}
	}

	otech := strings.TrimSpace(deps.OTECHFQDN)
	if otech == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "otech-fqdn-unconfigured",
			"detail": "CATALYST_OTECH_FQDN is not set on this catalyst-api Pod",
		})
		return
	}

	// Multi-domain Sovereign (epic #825 / MD-3 #828): for free-subdomain
	// mode the operator may pick which sme-pool parent domain hosts the
	// new tenant. When omitted with a non-empty pool we default to the
	// first NS-flip-ready entry (or, on a single-domain Sovereign, the
	// implicit OTECHFQDN entry — see Handler.ParentDomainsForSMECreate).
	//
	// The pool is composed live (admin store from #829 + implicit
	// primary + env stub) so an operator who adds a new sme-pool entry
	// via the #829 admin surface can bind tenants under it immediately,
	// without restarting catalyst-api. The same composition is what
	// the front-end's GET /api/v1/sovereign/parent-domains shows.
	if mode == string(store.OrganizationDomainFreeSubdomain) {
		pool := h.poolDomainsForSMECreate(deps)
		if len(pool) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sme-pool-empty",
				"detail": "this Sovereign has no role:sme-pool parent domains; ask the operator to add one via the admin console or configure CATALYST_ORG_POOL_DOMAINS",
			})
			return
		}
		if parent == "" {
			// Default to the first NS-flip-ready entry; otherwise the
			// first entry (the orchestrator surfaces a retry-after below
			// when the chosen entry isn't ready).
			chosen := pool[0]
			for _, p := range pool {
				if p.NSFlipReady {
					chosen = p
					break
				}
			}
			parent = strings.ToLower(chosen.Name)
		}
		match, ok := findParentInPool(parent, pool)
		if !ok {
			writeBadRequest(w, "parent-domain-invalid",
				"parent_domain must be one of this Sovereign's role:sme-pool parent domains")
			return
		}
		if !match.NSFlipReady {
			// Per #828 §C: NS-flip incomplete → return retry-after.
			w.Header().Set("Retry-After", "300")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "parent-domain-ns-flip-pending",
				"detail": "the chosen parent_domain is not yet NS-flip-ready; retry once the Sovereign provisioning state machine reports the flip complete",
			})
			return
		}
		parent = strings.ToLower(match.Name)
	} else {
		// BYO mode — no parent_domain is captured on the record (the
		// BYO domain is the canonical key), but we still validate the
		// CNAME against any parent in the pool below at the DNS step.
		parent = ""
	}

	orgTenantID := uuid.New().String()
	rec := store.OrganizationProvisionRecord{
		OrganizationID:     orgTenantID,
		State:           store.STSPending,
		Subdomain:       subdomain,
		DomainMode:      store.OrganizationDomainMode(mode),
		BYODomain:       byo,
		ParentDomain:    parent,
		AdminEmail:      email,
		CompanyName:     strings.TrimSpace(body.CompanyName),
		OTECHFQDN:       otech,
		VClusterName:    "vc-" + subdomain,
		TenantNamespace: "sme-" + orgTenantID,
		// Organizations model (issue #3378 B1) — stamp the resolved
		// kind/tier/billingMode/isolation so the directory badges the
		// org correctly and the controller can later read the spec
		// shape (namespace-mode reconcile is the placement/org-controller
		// follow-on per #3378 §9; this records the desired-state fields).
		Kind:        shape.Kind,
		Tier:        shape.Tier,
		BillingMode: shape.BillingMode,
		Isolation:   shape.Isolation,
	}
	if err := deps.Store.Put(rec); err != nil {
		h.log.Error("sme-tenant: persist pending failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "persist-failed",
			"detail": err.Error(),
		})
		return
	}

	if deps.Events != nil {
		if err := deps.Events.EmitOrganizationCreated(r.Context(), rec); err != nil {
			h.log.Warn("sme-tenant: nats emit created failed", "err", err)
		}
	}

	final := h.runOrganizationPipeline(r.Context(), rec)
	writeJSON(w, http.StatusAccepted, orgTenantRecordToResponse(final))
}

// HandleListOrganizations — GET /api/v1/sme/tenants.
func (h *Handler) HandleListOrganizations(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []orgTenantResponse{}})
		return
	}
	rows := deps.Store.List()
	out := make([]orgTenantResponse, 0, len(rows))
	for _, rec := range rows {
		out = append(out, orgTenantRecordToResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// HandleGetOrganization — GET /api/v1/sme/tenants/{id}.
func (h *Handler) HandleGetOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sme-tenant-store-unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok := deps.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "sme-tenant-not-found",
		})
		return
	}
	writeJSON(w, http.StatusOK, orgTenantRecordToResponse(rec))
}

// HandleReconcileOrganization — POST /api/v1/sme/tenants/{id}/reconcile.
//
// Operator-triggered re-run of the pipeline from the current state.
// Idempotent: a record already in STSDone is a no-op; one in STSFailed
// resets retry_count and re-runs from the failed step.
func (h *Handler) HandleReconcileOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sme-tenant-store-unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok := deps.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "sme-tenant-not-found",
		})
		return
	}
	if rec.State == store.STSFailed {
		// Reset retry counter so a manual re-run gets a fresh budget.
		rec.RetryCount = 0
		rec.LastError = ""
		// Reset State to the last successful step so the pipeline
		// re-runs from where it failed. The deriveResumeState helper
		// inspects LastError for the failing step prefix.
		rec.State = deriveResumeState(rec)
	}
	final := h.runOrganizationPipeline(r.Context(), rec)
	writeJSON(w, http.StatusOK, orgTenantRecordToResponse(final))
}

// HandleDeleteOrganization — DELETE /api/v1/sme/tenants/{id}.
//
// Inverse pipeline: removes the per-tenant overlay from the GitOps
// repo (Flux reconciles → tenant resources GC), unregisters from the
// host registry, and emits the deletion event. Each step is idempotent
// + best-effort; partial failure leaves a STSDeleted audit row so the
// reconciler can finish on the next pass.
func (h *Handler) HandleDeleteOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sme-tenant-store-unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok := deps.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "sme-tenant-not-found",
		})
		return
	}

	if deps.GitOps != nil {
		if sha, err := deps.GitOps.DeleteTenantOverlay(r.Context(), rec); err != nil {
			h.log.Warn("sme-tenant: delete overlay best-effort failed", "err", err)
		} else {
			rec.CommitSHA = sha
		}
	}
	if deps.TenantRegistry != nil {
		host := deriveConsoleHost(rec)
		if host != "" {
			if err := deps.TenantRegistry.Delete(host); err != nil {
				h.log.Warn("sme-tenant: registry delete best-effort failed", "err", err)
			}
		}
	}

	rec.State = store.STSDeleted
	rec.LastError = ""
	if err := deps.Store.Put(rec); err != nil {
		h.log.Warn("sme-tenant: persist deleted failed", "err", err)
	}
	if deps.Events != nil {
		if err := deps.Events.EmitOrganizationDeleted(r.Context(), rec); err != nil {
			h.log.Warn("sme-tenant: nats emit deleted failed", "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

/* ── pipeline ───────────────────────────────────────────────────── */

// deriveResumeState reads LastError to figure out which step failed and
// returns the previous successful state so the pipeline re-runs the
// failing step. Default is STSPending.
func deriveResumeState(rec store.OrganizationProvisionRecord) store.OrganizationProvisionState {
	switch {
	case strings.HasPrefix(rec.LastError, "registry:"):
		return store.STSKeycloakClientsProvisioned
	case strings.HasPrefix(rec.LastError, "keycloak_clients:"):
		return store.STSCertsIssued
	case strings.HasPrefix(rec.LastError, "certs:"):
		return store.STSDNSProvisioned
	case strings.HasPrefix(rec.LastError, "dns:"):
		return store.STSBPChartsInstalled
	case strings.HasPrefix(rec.LastError, "bp_charts:"):
		return store.STSVClusterCreated
	case strings.HasPrefix(rec.LastError, "vcluster:"):
		return store.STSPending
	}
	return store.STSPending
}

// runOrganizationPipeline drives the 7-step state machine. Each step is
// idempotent; the function returns the final persisted record.
//
// Step gating: a missing dep (nil) advances the step as a no-op so the
// orchestrator can be exercised in CI without every external system
// wired. In production main.go wires every dep; a nil at runtime is
// surfaced through the slog warn the orchestrator emits.
func (h *Handler) runOrganizationPipeline(ctx context.Context, rec store.OrganizationProvisionRecord) store.OrganizationProvisionRecord {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		return rec
	}

	persist := func(updated store.OrganizationProvisionRecord) store.OrganizationProvisionRecord {
		if err := deps.Store.Put(updated); err != nil {
			h.log.Warn("sme-tenant: persist failed during pipeline", "err", err)
		}
		if deps.Events != nil {
			if err := deps.Events.EmitOrganizationStateChanged(ctx, updated); err != nil {
				h.log.Warn("sme-tenant: nats emit state-changed failed", "err", err)
			}
		}
		return updated
	}

	failTransient := func(rec store.OrganizationProvisionRecord, step string, err error) store.OrganizationProvisionRecord {
		rec.RetryCount++
		rec.LastError = step + ":transient:" + truncate(err.Error(), 256)
		if rec.RetryCount >= deps.MaxRetryCount {
			rec.State = store.STSFailed
		}
		return persist(rec)
	}
	failTerminal := func(rec store.OrganizationProvisionRecord, step, detail string) store.OrganizationProvisionRecord {
		rec.LastError = step + ":terminal:" + truncate(detail, 256)
		rec.State = store.STSFailed
		return persist(rec)
	}

	// Step 1 — vcluster_created (overlay committed, vcluster
	// HelmRelease present in GitOps repo).
	if rec.State == store.STSPending {
		if deps.GitOps == nil {
			rec = failTerminal(rec, "vcluster", "gitops writer not wired")
			return rec
		}
		sha, err := deps.GitOps.WriteTenantOverlay(ctx, rec)
		if err != nil {
			return failTransient(rec, "vcluster", err)
		}
		rec.CommitSHA = sha
		rec.State = store.STSVClusterCreated
		rec.LastError = ""
		rec.RetryCount = 0
		rec = persist(rec)
	}

	// Step 2 — bp_charts_installed.
	//
	// The same overlay we committed in step 1 also enumerates the bp-*
	// HelmReleases (bp-keycloak per-organization, bp-cnpg, bp-
	// wordpress-tenant, bp-openclaw, bp-stalwart-tenant). Flux on the
	// OTECH cluster reconciles them inside the SME vcluster via the
	// vcluster-syncer; the orchestrator advances optimistically here
	// and the reconciler downgrades to STSFailed if Flux reports
	// HelmRelease.status.conditions[*].type=Ready=False after the
	// per-state retry interval.
	//
	// In CI / unit tests with no live Flux, the optimistic advance is
	// what the test harness exercises.
	if rec.State == store.STSVClusterCreated {
		rec.State = store.STSBPChartsInstalled
		rec.LastError = ""
		rec = persist(rec)
	}

	// Step 3 — dns_provisioned.
	if rec.State == store.STSBPChartsInstalled {
		if deps.DNS != nil {
			switch rec.DomainMode {
			case store.OrganizationDomainFreeSubdomain:
				// Multi-domain Sovereign (#825): use the chosen parent
				// zone, falling back to OTECHFQDN for single-domain
				// back-compat (#804).
				parentZone := strings.TrimSpace(rec.ParentDomain)
				if parentZone == "" {
					parentZone = rec.OTECHFQDN
				}
				if err := deps.DNS.ProvisionFreeSubdomain(ctx, rec.Subdomain, parentZone, deps.OTECHIngressIPv4); err != nil {
					return failTransient(rec, "dns", err)
				}
			case store.OrganizationDomainBYO:
				// BYO mode (#828 §C generalisation): the operator's
				// CNAME may point at ANY parent in the pool. Build the
				// accepted-targets list from the live pool composition.
				accepted := []string{rec.OTECHFQDN}
				for _, p := range h.poolDomainsForSMECreate(deps) {
					accepted = append(accepted, p.Name)
				}
				if err := deps.DNS.ValidateBYOCNAME(ctx, rec.BYODomain, rec.OTECHFQDN, accepted...); err != nil {
					return failTerminal(rec, "dns", "BYO CNAME validation failed: "+err.Error())
				}
			}
		}
		rec.State = store.STSDNSProvisioned
		rec.LastError = ""
		rec.RetryCount = 0
		rec = persist(rec)
	}

	// Step 4 — certs_issued.
	//
	// For free-subdomain mode the wildcard `*.<otech-fqdn>` already
	// covers every SME's `console.<sub>.<otech>` and the orchestrator
	// advances unconditionally. For BYO mode the per-tenant overlay
	// committed in step 1 includes a `Certificate` resource (per-host
	// HTTP-01); Flux reconciles cert-manager and the orchestrator
	// advances optimistically — the reconciler observes Ready=True
	// downstream.
	if rec.State == store.STSDNSProvisioned {
		rec.State = store.STSCertsIssued
		rec.LastError = ""
		rec = persist(rec)
	}

	// Step 5 — keycloak_clients_provisioned.
	if rec.State == store.STSCertsIssued {
		if deps.KeycloakClients != nil {
			if err := deps.KeycloakClients.ProvisionOrganizationClients(ctx, rec); err != nil {
				return failTransient(rec, "keycloak_clients", err)
			}
		}
		rec.State = store.STSKeycloakClientsProvisioned
		rec.LastError = ""
		rec.RetryCount = 0
		rec = persist(rec)
	}

	// Step 6 — tenant_registered.
	if rec.State == store.STSKeycloakClientsProvisioned {
		if deps.TenantRegistry != nil {
			host := deriveConsoleHost(rec)
			if host == "" {
				return failTerminal(rec, "registry", "could not derive console host")
			}
			// Realm URL — uses the same parent zone as the console host
			// (multi-domain Sovereign per #825). Falls back to
			// OTECHFQDN for single-domain back-compat (#804).
			realmZone := strings.TrimSpace(rec.ParentDomain)
			if realmZone == "" {
				realmZone = rec.OTECHFQDN
			}
			realmURL := fmt.Sprintf("https://keycloak.%s.%s/realms/%s",
				rec.Subdomain, realmZone, "sme-"+rec.Subdomain)
			reg := store.TenantRegistration{
				Host:                 host,
				TenantID:             rec.OrganizationID,
				TenantKind:           store.TenantKindSME,
				KeycloakRealmURL:     realmURL,
				KeycloakClientID:     "catalyst-ui",
				OrganizationNamespace:   rec.TenantNamespace,
				SMEKeycloakAdminURL:  fmt.Sprintf("http://keycloak-%s.%s.svc:8080", rec.Subdomain, rec.TenantNamespace),
				SMEKeycloakRealmName: "sme-" + rec.Subdomain,
			}
			if err := deps.TenantRegistry.Put(reg); err != nil {
				return failTransient(rec, "registry", err)
			}
		}
		rec.State = store.STSTenantRegistered
		rec.LastError = ""
		rec.RetryCount = 0
		rec = persist(rec)
	}

	// Step 7 — done.
	if rec.State == store.STSTenantRegistered {
		// #3687 master root — mint the canonical Organization CR before the
		// pipeline reports DONE. Everything above provisioned the per-tenant
		// substrate (vCluster overlay, DNS, Keycloak clients, registry row)
		// but left `kubectl get organizations -A` = 0; the org-controller +
		// every operator day-2 surface (Dashboard / Showback / Users) key off
		// this CR. Best-effort + idempotent (AlreadyExists = success): a
		// transient apiserver failure logs loud but does NOT fail the
		// provision (the substrate is already valid), and the org-controller's
		// level-triggered reconcile + a pipeline re-run land the CR on retry.
		h.createSMEOrganizationCR(ctx, rec)

		rec.State = store.STSDone
		rec.LastError = ""
		rec = persist(rec)
	}

	return rec
}

/* ── Reconciler hook (NATS-driven) ─────────────────────────────── */

// ReconcileAllPending walks every non-terminal record and re-runs the
// pipeline. Called from the NATS subscriber main.go wires on the
// `sme.tenant.reconcile-pending` subject (see ADR-0003 §3.5 for the
// architectural pattern — heartbeat-to-self instead of CronJob).
//
// The reconciler is NOT a goroutine `time.Tick` and NOT a Kubernetes
// CronJob — both violate Inviolable Principle 1. The catalyst-api's
// main.go publishes a heartbeat envelope on
// `sme.tenant.reconcile-pending` every 30s; the consumer (in the
// same process) handles the heartbeat by calling this method. Tests
// invoke it directly to exercise the reconcile path.
func (h *Handler) ReconcileAllPending(ctx context.Context) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		return
	}
	for _, rec := range deps.Store.ListPending() {
		_ = h.runOrganizationPipeline(ctx, rec)
	}
}

/* ── error helpers ─────────────────────────────────────────────── */

// errBYOCNAMEMismatch is the canonical error the BYO-CNAME validator
// returns when the customer's CNAME doesn't resolve to the expected
// otech ingress. Surfaced verbatim through the wizard UI so the
// customer can fix their own DNS without contacting support.
var errBYOCNAMEMismatch = errors.New("byo cname does not resolve to otech ingress")

// poolDomainsForSMECreate returns the live sme-pool list used to
// validate the operator-supplied parent_domain. Precedence:
//
//  1. OrganizationDeps.ParentDomains — explicit operator wiring at
//     startup (post-MD-1 Deployment record OR test seed). Always wins
//     when present so tests get deterministic behaviour and the
//     production wiring is the authoritative source.
//  2. Admin store from #829 — entries added via the operator console
//     after handover. Only consulted when (1) is empty.
//  3. Env stub from CATALYST_ORG_POOL_DOMAINS — used while #826's
//     data model is still in flight. Only consulted when (1) and (2)
//     are both empty.
//  4. OTECHFQDN — single-domain back-compat last-resort.
//
// Returns sme-pool entries only — primary domains are not bookable
// for SME tenants per epic #825.
func (h *Handler) poolDomainsForSMECreate(deps OrganizationDeps) []OrganizationParentDomain {
	merged := make([]OrganizationParentDomain, 0)
	seen := map[string]struct{}{}
	addIf := func(p OrganizationParentDomain) {
		key := strings.ToLower(p.Name)
		if _, ok := seen[key]; ok {
			return
		}
		if !strings.EqualFold(p.Role, "sme-pool") {
			return
		}
		p.Name = key
		merged = append(merged, p)
		seen[key] = struct{}{}
	}
	// (1) Explicit deps seed wins.
	for _, p := range deps.ParentDomains {
		addIf(p)
	}
	if len(merged) > 0 {
		return merged
	}
	// (2) Admin store from #829.
	for _, p := range h.ParentDomainsForSMECreate() {
		addIf(p)
	}
	if len(merged) > 0 {
		return merged
	}
	// (3) + (4) — single-domain back-compat.
	if strings.TrimSpace(deps.OTECHFQDN) != "" {
		merged = append(merged, OrganizationParentDomain{
			Name: strings.ToLower(deps.OTECHFQDN), Role: "sme-pool", NSFlipReady: true,
		})
	}
	return merged
}

// findParentInPool looks up `name` (case-insensitive) in `pool`.
// Returns the entry + true on hit.
func findParentInPool(name string, pool []OrganizationParentDomain) (OrganizationParentDomain, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range pool {
		if strings.ToLower(p.Name) == name {
			return p, true
		}
	}
	return OrganizationParentDomain{}, false
}
