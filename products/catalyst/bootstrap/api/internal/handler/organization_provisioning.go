// Package handler — organization_provisioning.go: Organization tenant provisioning pipeline
// orchestrator (issue #804).
//
// This is the back-end for the marketplace's "Sign up an Organization tenant"
// flow. The full epic is #795 — tenancy is K8s-native (per Inviolable
// Principle 7) so a tenant is materialised by:
//
//  1. A vCluster inside the OTECH cluster (the Organization's logical cluster).
//  2. A namespace `org-<tenant-id>` in the OTECH cluster (Secret-as-
//     truth for per-user NewAPI keys + per-host TLS Certificates).
//  3. The 4 sister bp-* charts installed inside the Organization vcluster
//     (bp-keycloak per-organization, bp-cnpg, bp-wordpress-tenant,
//     bp-openclaw, bp-stalwart-tenant).
//  4. DNS records (free-subdomain via PowerDNS API) or BYO-CNAME
//     validation (the customer's own DNS).
//  5. cert-manager Certificate (per-host HTTP-01 for BYO; the
//     wildcard `*.<otech-fqdn>` already covers free-subdomain).
//  6. OIDC clients pre-created in the Organization vcluster Keycloak
//     (WordPress, OpenClaw, Stalwart, unified-RBAC Organization-tier) with
//     group templates `org-admin` + `org-user`.
//  7. A row in the host → tenant registry (consumed by the
//     public `/api/v1/tenant/discover` endpoint per #802) so the
//     SPA's first hit on `console.<org-host>` resolves to the new
//     tenant.
//
// State machine: see store.OrganizationProvisionState. Each step is
// independently idempotent; the orchestrator persists the row at every
// state transition so a Pod restart never strands a half-provisioned
// tenant. The reconciler is event-driven (NATS subject
// `org.tenant.reconcile-pending`) per Inviolable Principle 1 and
// ADR-0001 §6 — never a Kubernetes CronJob, never a goroutine
// `time.Tick`.
//
// HTTP surface:
//
//	POST   /api/v1/org/tenants            — create + start pipeline
//	GET    /api/v1/org/tenants            — list tenants
//	GET    /api/v1/org/tenants/{id}       — read one
//	POST   /api/v1/org/tenants/{id}/reconcile — operator-triggered
//	                                        re-run from current state
//	DELETE /api/v1/org/tenants/{id}       — inverse pipeline
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

// OrganizationDNSProvisioner provisions DNS for the Organization's
// `console.<host>` either via PowerDNS (free-subdomain) or by
// validating an operator-supplied CNAME (BYO).
//
// The free-subdomain method takes a `parentZone` parameter so the
// multi-domain Sovereign (epic #825) can write records under any of
// its role:org-pool zones; on a single-domain Sovereign the wired
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
	//
	// #4732(3): consoleIPv4 targets the Org console record at the
	// DEDICATED console gateway/ELB front door (#4053/#4718) — the same
	// door `console.<sovereign-fqdn>` serves; the app hosts + per-Org
	// wildcard stay on ingressIPv4 (the shared gateway). Empty
	// consoleIPv4 falls back to ingressIPv4.
	ProvisionFreeSubdomain(ctx context.Context, subdomain, parentZone, ingressIPv4, consoleIPv4 string) error
	// DeprovisionFreeSubdomain removes the per-Org pool A-records that
	// ProvisionFreeSubdomain wrote (console + app sister hosts under
	// <subdomain>.<parentZone>). Called on Organization delete so a stale
	// record does not survive the Org and point a later same-slug re-prov at
	// a DEAD console-ELB IP — the #4459 Console-000 poisoning. Idempotent:
	// deleting an already-absent rrset is a 2xx no-op on PowerDNS, so this is
	// safe to call even when the Org never provisioned DNS.
	DeprovisionFreeSubdomain(ctx context.Context, subdomain, parentZone string) error
	// ValidateBYOCNAME resolves `console.<byo_domain>` and confirms
	// it CNAMEs to one of acceptedTargets (or, when nil/empty, to
	// the supplied legacyTarget). Returns a structured error when the
	// lookup fails or the target doesn't match — the orchestrator
	// surfaces those in the wizard UI so the customer can fix their
	// own DNS before the pipeline can advance.
	ValidateBYOCNAME(ctx context.Context, byoDomain, legacyTarget string, acceptedTargets ...string) error
}

// OrganizationKeycloakClientProvisioner pre-creates OIDC clients +
// group templates in the Organization vcluster Keycloak realm. Stubbed in
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
// brought at signup (epic #825 / MD-1 #826) that is offered to Organizations.
// One Sovereign typically holds several: a `primary` parent (the one
// hosting `console.<sovereign>`) plus zero-or-more `org-pool` parents
// the Organization tenant pipeline (this file) writes free-subdomains under.
//
// Per Inviolable Principle 4 the pool is fully data-driven; #828
// neither hardcodes nor caps the count.
type OrganizationParentDomain struct {
	// Name — the FQDN itself, e.g. "omani.trade".
	Name string `json:"name"`
	// Role — "primary" | "org-pool". The Organization tenant create endpoint
	// only accepts entries with role=org-pool.
	Role string `json:"role"`
	// NSFlipReady — true once the registrar's NS records point at
	// the Sovereign's PowerDNS (set by the Sovereign provisioning
	// pipeline / MD-1). The Organization create endpoint refuses to write a
	// free-subdomain into a parent that isn't NS-flip-ready yet.
	NSFlipReady bool `json:"ns_flip_ready"`
}

// OrganizationDeps bundles the dependencies the Organization tenant handlers
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
	// OTECHFQDN itself with role=org-pool, ns_flip_ready=true.
	ParentDomains []OrganizationParentDomain
	// MaxRetryCount — promoted to STSFailed at this many transient
	// failures of the same step. Per ADR-0003 §3.8 = 5.
	MaxRetryCount int
}

// PoolDomains returns the subset of ParentDomains with role=org-pool.
// Includes the implicit OTECHFQDN entry when the wired list is empty
// (single-domain Sovereign — backward-compat with #804).
func (d OrganizationDeps) PoolDomains() []OrganizationParentDomain {
	if len(d.ParentDomains) == 0 {
		if strings.TrimSpace(d.OTECHFQDN) == "" {
			return nil
		}
		return []OrganizationParentDomain{
			{Name: d.OTECHFQDN, Role: "org-pool", NSFlipReady: true},
		}
	}
	out := make([]OrganizationParentDomain, 0, len(d.ParentDomains))
	for _, p := range d.ParentDomains {
		if strings.EqualFold(p.Role, "org-pool") {
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

// SetOrganizationDeps wires the Organization pipeline dependencies. Called
// by main.go at startup; tests pass a struct with stub clients.
func (h *Handler) SetOrganizationDeps(deps OrganizationDeps) {
	if deps.MaxRetryCount == 0 {
		deps.MaxRetryCount = 5
	}
	h.orgTenantDeps = deps
}

/* ── wire shapes ─────────────────────────────────────────────────── */

type orgTenantCreateRequest struct {
	// Subdomain — the Organization slug (e.g. "acme"). Required for both
	// free-subdomain and BYO modes (used in resource names + the
	// vCluster name `vc-<subdomain>`).
	Subdomain string `json:"subdomain"`
	// DomainMode — "free-subdomain" (default) or "byo".
	DomainMode string `json:"domain_mode,omitempty"`
	// BYODomain — required when DomainMode == "byo". The orchestrator
	// derives the host as `console.<byo_domain>`.
	BYODomain string `json:"byo_domain,omitempty"`
	// ParentDomain — required when DomainMode == "free-subdomain"
	// and the Sovereign has more than one entry in its org-pool. When
	// omitted with a multi-entry pool the orchestrator defaults to the
	// first NS-flip-ready entry. Must match (case-insensitive) one of
	// the entries returned by GET /api/v1/sovereign/parent-domains
	// with role=org-pool. Per epic #825 the resulting host becomes
	// `console.<subdomain>.<parent_domain>` — never inferred from
	// OTECHFQDN.
	ParentDomain string `json:"parent_domain,omitempty"`
	// AdminEmail — the Organization's first user (chart admin email + welcome
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

	// PlanSlug — purchased catalog plan slug (s|m|l|xl|flexi). Carried onto
	// the Organization CR so the org-controller materializes the matching
	// ResourceQuota + LimitRange (Workstream B, #4292). Empty defaults to
	// "s" (the smallest paid tier) so the BSS door never mints an uncapped
	// Org.
	PlanSlug string `json:"plan_slug,omitempty"`
}

// orgShape is the resolved Organizations-model shape (issue #3378 B1)
// the create handler stamps onto the provision record.
type orgShape struct {
	Kind        string
	Tier        string
	BillingMode string
	Isolation   string
	// PlanSlug — resolved catalog plan slug (#4292). Drives the org-
	// controller's plan-templated quota.
	PlanSlug string
}

// allTiersVcluster mirrors the controller-side single switch (issue #4292
// boundaryIsVcluster in core/controllers/organization/internal/gitops/
// manifests.go): set it true to put EVERY tier (incl. free/S) on a dedicated
// vCluster. It is duplicated here the same way gitops.BoundaryIsVcluster
// duplicates the controller gate — the two MUST flip together so the displayed
// `isolation` label never diverges from the actual backing.
const allTiersVcluster = false

// isolationForTier returns the Org's actual boundary primitive ("namespace"
// for a host-ns Org, "vcluster" for a dedicated Org-vCluster), derived from the
// SAME #4292 TIER GATE the org-controller uses to author the backing. Keeping
// this in lockstep with that gate is what makes the displayed `isolation` value
// ACCURATE rather than a static kind-derived guess:
//
//   - internal kind → always "namespace" (a department shares the host ns; no
//     dedicated vCluster regardless of plan).
//   - customer kind → the plan tier gate decides: free/S (or empty) share the
//     host `<slug>` namespace → "namespace"; m/l/xl/flexi get a dedicated
//     Org-vCluster → "vcluster".
//
// Before this fix the label was hardcoded customer→vcluster, so an S-plan Org
// that correctly backs a host namespace was mislabeled "vcluster" (UAT rows
// 9-12, dep 91dc05917e44d1c1). The BACKING was always right — only the label
// ignored the tier.
func isolationForTier(kind, planSlug string) string {
	if strings.ToLower(strings.TrimSpace(kind)) == "internal" {
		return "namespace"
	}
	if allTiersVcluster {
		return "vcluster"
	}
	switch strings.ToLower(strings.TrimSpace(planSlug)) {
	case "", "s", "free":
		return "namespace"
	default:
		return "vcluster"
	}
}

// resolveOrgShape applies the §2.1/§2.3 model: kind defaults to
// "customer" (the marketplace door); billingMode defaults from kind
// (internal → showback; customer → real); isolation is DERIVED from the
// #4292 tier gate (isolationForTier) so the displayed boundary matches the
// actual backing — free/S → namespace, M+ → vcluster — not a static
// kind-only guess; tier defaults to "org". Unknown enum values fall back to
// the derived default so a malformed body can never stamp a nonsense shape.
// An explicit valid isolation in the request still overrides (the advanced
// operator view).
func resolveOrgShape(req orgTenantCreateRequest) orgShape {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "internal" && kind != "customer" {
		kind = "customer"
	}

	// kind-derived billing default (§2.3).
	defBilling := "real"
	if kind == "internal" {
		defBilling = "showback"
	}

	billing := strings.ToLower(strings.TrimSpace(req.BillingMode))
	switch billing {
	case "real", "chargeback", "showback":
	default:
		billing = defBilling
	}

	planSlug := strings.ToLower(strings.TrimSpace(req.PlanSlug))
	switch planSlug {
	case "s", "m", "l", "xl", "flexi":
	default:
		planSlug = "s"
	}

	// Isolation is DERIVED from the #4292 tier gate (host-ns for free/S,
	// vcluster for M+; internal is always host-ns) so the label reflects the
	// real backing. An explicit valid request override still wins.
	isolation := strings.ToLower(strings.TrimSpace(req.Isolation))
	switch isolation {
	case "namespace", "vcluster":
	default:
		isolation = isolationForTier(kind, planSlug)
	}

	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	switch tier {
	case "org", "corporate":
	default:
		tier = "org"
	}

	return orgShape{Kind: kind, Tier: tier, BillingMode: billing, Isolation: isolation, PlanSlug: planSlug}
}

type orgTenantResponse struct {
	OrganizationID  string                           `json:"org_tenant_id"`
	State           store.OrganizationProvisionState `json:"state"`
	Subdomain       string                           `json:"subdomain"`
	DomainMode      store.OrganizationDomainMode     `json:"domain_mode"`
	BYODomain       string                           `json:"byo_domain,omitempty"`
	ParentDomain    string                           `json:"parent_domain,omitempty"`
	AdminEmail      string                           `json:"admin_email"`
	CompanyName     string                           `json:"company_name,omitempty"`
	OTECHFQDN string `json:"otech_fqdn"`
	// VClusterName — omitempty (#5501): a host-namespace Org authors no
	// vCluster, and an empty string in the payload still reads as "there is
	// a vcluster_name field for this Org" to anything that binds it. An
	// absent key is the honest shape.
	VClusterName    string `json:"vcluster_name,omitempty"`
	TenantNamespace string `json:"tenant_namespace"`
	ConsoleHost     string `json:"console_host"`
	// Organizations model (issue #3378 B1) — surfaced so the directory
	// can badge the org by kind/tier/billingMode/isolation.
	Kind        string         `json:"kind,omitempty"`
	Tier        string         `json:"tier,omitempty"`
	BillingMode string         `json:"billing_mode,omitempty"`
	Isolation   string         `json:"isolation,omitempty"`
	CommitSHA   string         `json:"commit_sha,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	Steps       orgTenantSteps `json:"steps"`
	// BoundaryPhase — the observed phase of the Org's boundary (#5501),
	// read back from the canonical Organization CR. Absent when the
	// substrate has never been observed; the SPA can distinguish
	// "unobserved" from "Pending" because one omits the key and the other
	// names a real controller phase.
	BoundaryPhase string `json:"boundary_phase,omitempty"`
	// CreatedAt / UpdatedAt — omitzero (#5501). A Go zero time serialized
	// as `0001-01-01T00:00:00Z` is a FALSE MEASUREMENT: it reads as a real
	// RFC-3339 instant to every consumer. When the timestamp is genuinely
	// unknown the key is omitted instead — an absent field is honest, a
	// zero timestamp is not. Same class as #5477.
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// vclusterNameFor returns the name of a vcluster-tier Org's vCluster and ""
// for anything else (#5489): only the vcluster tier has a vCluster to name.
// The legacy unconditional `vc-<slug>` put a `vcluster_name` in the payload
// right beside `isolation: "namespace"` — latent (the UI declares the field
// and never binds it), but it would assert an object that does not exist the
// moment anyone rendered it. Since #4188 no overlay template consumes the
// value either, so an empty name is inert on the provisioning pipeline.
//
// #5501 — the name is the BARE SLUG, not the synthesized `vc-<slug>`. The
// org-controller is the only producer of the object and it reports
// `status.vcluster.name = <slug>` (vclusterStatusFor in
// core/controllers/organization/internal/controller/organization_controller.go),
// which is what a walked Sovereign returns from `kubectl get organization
// <slug> -o yaml`. The API used to answer `vc-uatcorp` for a CR that said
// `uatcorp`: two names for one object, and the one the API published named
// nothing that exists. Synthesizing a SECOND name for a resource another
// component owns is the defect — this function now mirrors that owner.
func vclusterNameFor(isolation, slug string) string {
	if isolation == "vcluster" {
		return strings.ToLower(strings.TrimSpace(slug))
	}
	return ""
}

// orgTenantSteps surfaces the 7-state machine to the SPA so it can
// render a progress timeline.
//
// #5489 — `vcluster` is omitempty: a namespace-isolated Org never
// provisions a vCluster, so its timeline must not carry a `vcluster:
// "done"` step over an unauthored object. orgTenantRecordToResponse
// blanks the step for records that explicitly say isolation=namespace;
// the SPA (CreateOrganizationPage ProvisionSteps) renders only the steps
// the payload carries.
type orgTenantSteps struct {
	VCluster        string `json:"vcluster,omitempty"`
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
	steps := stepsForState(rec.State, rec.LastError)

	// #5501 — the two SUBSTRATE-side steps report the OBSERVED boundary,
	// not the orchestrator's position on its own ladder.
	//
	// stepsForState is right about what it models: the linear state machine
	// records which SUBMITS the orchestrator has completed. But `vcluster`
	// and `bp_charts` are claims about things that exist on the cluster —
	// the Org's boundary, and the bp-* HelmReleases that install INTO it —
	// and the orchestrator only ever committed manifests for those. So the
	// ladder alone reported `vcluster:"done"` + `bp_charts:"done"` zero
	// seconds after create, against a CR that said `phase: Pending` and a
	// cluster with no namespace at all (hw291, #5501).
	//
	// rec.BoundaryPhase is the read-back of that CR (org_boundary_observation.go).
	// The inference runs in ONE direction only: a boundary that is not up
	// cannot have charts installed in it, so both steps degrade to the
	// observed phase. It never promotes a step — an unobserved boundary
	// reads "pending", never "done" (boundaryStepFor).
	//
	// Terminal records are left alone: STSDone is now itself gated on an
	// observed-Ready boundary (runOrganizationPipeline step 7), and a
	// STSFailed record's per-step failure detail comes from last_error.
	if rec.State != store.STSDone && rec.State != store.STSFailed {
		observed := boundaryStepFor(rec.BoundaryPhase)
		if observed != "done" {
			if steps.VCluster == "done" {
				steps.VCluster = observed
			}
			if steps.BPCharts == "done" {
				steps.BPCharts = observed
			}
		}
	}

	// #5489 — a namespace-isolated Org has no vCluster step to report.
	// Blank it (the field is omitempty) only when the record EXPLICITLY
	// says namespace; legacy rows with an empty isolation keep the full
	// timeline — there is nothing to derive from, and guessing is the
	// exact fabrication this fix removes. A failed boundary still
	// surfaces via state=failed + last_error.
	if rec.Isolation == "namespace" {
		steps.VCluster = ""
	}
	return orgTenantResponse{
		OrganizationID:  rec.OrganizationID,
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
		Steps:           steps,
		BoundaryPhase:   rec.BoundaryPhase,
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

// HandleCreateOrganization — POST /api/v1/org/tenants.
//
// The marketplace signup form POSTs here. The orchestrator persists
// the pending row, fires the pipeline synchronously, and returns the
// current (necessarily partial) state immediately — the SPA renders a
// progress timeline against the steps[] field while the reconciler
// runs in the background. Returns 202 (not 201) because the resource
// is "accepted, materialising" rather than "created".
//
// #5501 — 202 was always right; the BODY was the lie. A fresh create can
// NEVER be `state:"done"`: the Org's boundary is authored downstream by the
// org-controller and takes minutes, so the pipeline holds the record at the
// highest non-terminal state until it observes that boundary Ready (see the
// terminal gate in runOrganizationPipeline step 7). The response now reports
// that honestly instead of claiming six completed steps in zero seconds.
func (h *Handler) HandleCreateOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "org-tenant-store-unavailable",
			"detail": "catalyst-api was started without an Organization store",
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
	// mode the operator may pick which org-pool parent domain hosts the
	// new tenant. When omitted with a non-empty pool we default to the
	// first NS-flip-ready entry (or, on a single-domain Sovereign, the
	// implicit OTECHFQDN entry — see Handler.ParentDomainsForOrgCreate).
	//
	// The pool is composed live (admin store from #829 + implicit
	// primary + env stub) so an operator who adds a new org-pool entry
	// via the #829 admin surface can bind tenants under it immediately,
	// without restarting catalyst-api. The same composition is what
	// the front-end's GET /api/v1/sovereign/parent-domains shows.
	if mode == string(store.OrganizationDomainFreeSubdomain) {
		pool := h.poolDomainsForOrgCreate(deps)
		if len(pool) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "org-pool-empty",
				"detail": "this Sovereign has no role:org-pool parent domains; ask the operator to add one via the admin console or configure CATALYST_ORG_POOL_DOMAINS",
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
				"parent_domain must be one of this Sovereign's role:org-pool parent domains")
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
		OrganizationID: orgTenantID,
		State:          store.STSPending,
		Subdomain:      subdomain,
		DomainMode:     store.OrganizationDomainMode(mode),
		BYODomain:      byo,
		ParentDomain:   parent,
		AdminEmail:     email,
		CompanyName:    strings.TrimSpace(body.CompanyName),
		OTECHFQDN:      otech,
		// #5489 — only a vcluster-tier Org gets a vCluster name; a
		// namespace-tier record stays empty rather than naming an object
		// the platform never authors. Inert on the pipeline: since #4188
		// no overlay template renders VClusterName.
		VClusterName: vclusterNameFor(shape.Isolation, subdomain),
		// Workstream A (#4290 / EPIC #4293) — the per-Organization host
		// namespace is the org-controller-owned `<slug>`, NOT a stray
		// `org-<uuid>`. The org-controller (core/controllers/organization/
		// internal/gitops/manifests.go) is the SINGLE boundary producer: it
		// renders `<slug>` namespace + the `vcluster` HelmRelease from the
		// Organization CR. This BSS door MINTS the CR (createOrgOrganizationCR)
		// and co-renders its bp-* charts INTO that same `<slug>` namespace, so
		// no second boundary is built. The prior `org-<uuid>` value produced a
		// duplicate, never-referenced namespace (the #4179 stray) that diverged
		// from the org-controller's DNS/TLS/registry. The Org CR slug == the
		// subdomain (orgSlugRE), and the org-controller stamps `<slug>` as the
		// namespace name, so the two paths now resolve to ONE namespace.
		TenantNamespace: subdomain,
		// Organizations model (issue #3378 B1) — stamp the resolved
		// kind/tier/billingMode/isolation so the directory badges the
		// org correctly and the controller can later read the spec
		// shape (namespace-mode reconcile is the placement/org-controller
		// follow-on per #3378 §9; this records the desired-state fields).
		Kind:        shape.Kind,
		Tier:        shape.Tier,
		BillingMode: shape.BillingMode,
		Isolation:   shape.Isolation,
		// #4292 — the purchased plan slug the org-controller caps the
		// boundary namespace at (ResourceQuota + LimitRange).
		PlanSlug: shape.PlanSlug,
	}
	// #5501 — Save (not Put) so the CreatedAt/UpdatedAt the store stamps
	// land on THIS record. Put persists a by-value copy, so the record the
	// handler goes on to serialize kept Go zero timestamps and the create
	// response published `0001-01-01T00:00:00Z` as a measurement.
	if err := deps.Store.Save(&rec); err != nil {
		h.log.Error("org-tenant: persist pending failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "persist-failed",
			"detail": err.Error(),
		})
		return
	}

	if deps.Events != nil {
		if err := deps.Events.EmitOrganizationCreated(r.Context(), rec); err != nil {
			h.log.Warn("org-tenant: nats emit created failed", "err", err)
		}
	}

	final := h.runOrganizationPipeline(r.Context(), rec)
	writeJSON(w, http.StatusAccepted, orgTenantRecordToResponse(final))
}

// HandleListOrganizations — GET /api/v1/org/tenants.
//
// #4479 (Refs #4179 #4290 #3687): the directory unions the local
// provision-record store (the BSS door's in-flight timeline detail) with
// the canonical orgs.openova.io Organization CRs (the truth BOTH doors
// write). Without the CR read every funnel-created Org — and the parent
// Sovereign Org — was invisible because the funnel never writes a local
// record. The CR read is nil-tolerant (orgResponsesFromCRs returns
// (nil,nil) out-of-cluster/CI), so the store-only path is preserved when
// no in-cluster dynamic client exists. Local rows win on slug collision
// (richer provisioning timeline); CR-only rows are appended.
func (h *Handler) HandleListOrganizations(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	local := make([]orgTenantResponse, 0)
	if deps.Store != nil {
		for _, rec := range deps.Store.List() {
			local = append(local, orgTenantRecordToResponse(rec))
		}
	}

	fromCR, err := h.orgResponsesFromCRs(r.Context())
	if err != nil {
		// CRD absent / apiserver blip / RBAC gap — degrade to store-only
		// rather than 5xx-ing the directory. Loud log, soft fall-through.
		h.log.Warn("org-tenant: list Organization CRs failed — directory degrades to local store only", "err", err)
	}

	out := mergeOrgResponses(local, fromCR)
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// HandleGetOrganization — GET /api/v1/org/tenants/{id}.
//
// #4479 (Refs #4179 #4290 #3687): org-detail resolves the path param
// against the local provision store FIRST (the param is the BSS door's
// UUID), then falls back to an orgs.openova.io Organization CR lookup by
// slug. The console + openova-MCP address org-detail by slug
// (console.<slug>...), and a funnel-created Org has no local record — only
// the CR — so without the CR fallback BOTH the customer Org and the parent
// Sovereign Org 404'd org-tenant-not-found. The CR read is nil-tolerant
// (no dynamic client → fall through to 404), preserving the store-only
// path in CI / out-of-cluster.
func (h *Handler) HandleGetOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	id := chi.URLParam(r, "id")

	// 1) Local provision store keyed by the BSS-door UUID.
	if deps.Store != nil {
		if rec, ok := deps.Store.Get(id); ok {
			writeJSON(w, http.StatusOK, orgTenantRecordToResponse(rec))
			return
		}
	}

	// 2) Canonical Organization CR keyed by slug (the funnel-Org path + the
	//    parent Sovereign Org). `id` doubles as the slug for slug-addressed
	//    callers (console / openova-MCP).
	if resp, ok := h.orgCRFromSlug(r.Context(), id); ok {
		writeJSON(w, http.StatusOK, *resp)
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": "org-tenant-not-found",
	})
}

// HandleReconcileOrganization — POST /api/v1/org/tenants/{id}/reconcile.
//
// Operator-triggered re-run of the pipeline from the current state.
// Idempotent: a record already in STSDone is a no-op; one in STSFailed
// resets retry_count and re-runs from the failed step.
func (h *Handler) HandleReconcileOrganization(w http.ResponseWriter, r *http.Request) {
	deps := h.orgTenantDeps
	if deps.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "org-tenant-store-unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok := deps.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "org-tenant-not-found",
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
	// #4732(1): the step-7 finalisation pair (Organization CR + console
	// TLS trio) is best-effort and non-gating, so it can fail silently
	// while the record still reaches done — and the pipeline never
	// revisits a done record, leaving the Org permanently without a
	// console front door (the nstar failure: no cert, no listener, no
	// route → pool-wildcard cert + 404). Re-ensure both on EVERY
	// operator-triggered reconcile; each is idempotent (AlreadyExists /
	// SSA-merge = no-op), so a healthy Org is untouched.
	if final.State == store.STSDone {
		h.createOrgOrganizationCR(r.Context(), final)
		h.provisionOrgConsoleTLS(r.Context(), final)
	}
	writeJSON(w, http.StatusOK, orgTenantRecordToResponse(final))
}

// HandleDeleteOrganization — DELETE /api/v1/org/tenants/{id}.
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
			"error": "org-tenant-store-unavailable",
		})
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok := deps.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "org-tenant-not-found",
		})
		return
	}

	if deps.GitOps != nil {
		if sha, err := deps.GitOps.DeleteTenantOverlay(r.Context(), rec); err != nil {
			h.log.Warn("org-tenant: delete overlay best-effort failed", "err", err)
		} else {
			rec.CommitSHA = sha
		}
	}
	if deps.TenantRegistry != nil {
		host := deriveConsoleHost(rec)
		if host != "" {
			if err := deps.TenantRegistry.Delete(host); err != nil {
				h.log.Warn("org-tenant: registry delete best-effort failed", "err", err)
			}
		}
	}
	// #4459 — remove the per-Org pool DNS records so a later same-slug re-prov
	// does not inherit a stale console/app A-record pointing at a DEAD ELB IP
	// (Console 000). Best-effort + idempotent (delete of an absent rrset is a
	// 2xx no-op), mirroring the overlay/registry teardowns above. Free-subdomain
	// mode only: a BYO-domain Org's records live in the customer's own zone.
	if deps.DNS != nil && rec.DomainMode == store.OrganizationDomainFreeSubdomain {
		parentZone := strings.TrimSpace(rec.ParentDomain)
		if parentZone == "" {
			parentZone = rec.OTECHFQDN
		}
		if err := deps.DNS.DeprovisionFreeSubdomain(r.Context(), rec.Subdomain, parentZone); err != nil {
			h.log.Warn("org-tenant: DNS deprovision best-effort failed", "err", err, "subdomain", rec.Subdomain)
		}
	}

	rec.State = store.STSDeleted
	rec.LastError = ""
	if err := deps.Store.Put(rec); err != nil {
		h.log.Warn("org-tenant: persist deleted failed", "err", err)
	}
	if deps.Events != nil {
		if err := deps.Events.EmitOrganizationDeleted(r.Context(), rec); err != nil {
			h.log.Warn("org-tenant: nats emit deleted failed", "err", err)
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
		// #5501 — Save writes the stamped CreatedAt/UpdatedAt back onto
		// `updated`, so every record this pipeline returns (and every
		// record the response mapper serializes) carries the same
		// timestamps the persisted row does, instead of Go zero values.
		if err := deps.Store.Save(&updated); err != nil {
			h.log.Warn("org-tenant: persist failed during pipeline", "err", err)
		}
		if deps.Events != nil {
			if err := deps.Events.EmitOrganizationStateChanged(ctx, updated); err != nil {
				h.log.Warn("org-tenant: nats emit state-changed failed", "err", err)
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

		// #4277 — seed the Sovereign's OpenBao
		// `secret/catalyst/anthropic/token` so this Org's (and every
		// Org's — the path is cluster-shared) bp-agenity ExternalSecret
		// resolves and the spawned claude-code authenticates zero-touch.
		// The READ side (chart init container + ExternalSecret) was already
		// wired (#4111); this is the missing producer. Deliberately NOT
		// gated on success: a credential gap or transient OpenBao blip must
		// not fail the Org pipeline (the agenity HR still installs; only its
		// chat-runtime stays offline until the path is seeded). seedAnthropicToken
		// surfaces the outcome loudly via the catalyst-api log and never
		// returns an error. Idempotent (PutKVv2 overwrites) so re-running on
		// every Org-create both makes new Orgs converge and keeps the
		// short-lived OAuth blob fresh.
		_ = h.seedAnthropicToken(ctx)

		// #4276 (hop 7/7b) — seed the per-Org OpenBao
		// `secret/catalyst/agenity/<slug>/mcp-bearer` with an Org-scoped
		// Catalyst session bearer + the RS256 verify pubkey (PEM) so this
		// Org's bp-agenity MCP authenticates create_application against
		// catalyst-api. Without it the spawned claude-code agent reaches
		// the openova MCP with NO bearer → -32001 unauthenticated, even
		// after the Anthropic key (#4277) is seeded (that only authenticates
		// claude-code to ANTHROPIC, not to CATALYST). Same posture as
		// seedAnthropicToken: idempotent (PutKVv2 overwrites — refreshes the
		// bearer's expiry every reconcile) and NEVER fails the pipeline (a
		// signer/OpenBao gap only leaves the MCP create path unauthenticated;
		// the agenity dashboard still serves). rec.Subdomain is the Org slug
		// the bearer is scoped to; rec.AdminEmail is the owner subject.
		_ = h.seedMCPBearer(ctx, rec.Subdomain, rec.AdminEmail)

		// #4477 (secondary; ADR-0003 §3.2/§6) — propagate NewAPI's bridge
		// ADMIN_SECRET (the bearer the sandbox-bridge validates with
		// subtle.ConstantTimeCompare) into the Sovereign's OpenBao
		// `secret/catalyst/newapi/admin-token` so the cluster-shared
		// `catalyst-newapi-admin-token` ExternalSecret resolves and
		// unified-rbac's per-user-key issuance against NewAPI's admin API
		// authenticates. Without it that ExternalSecret stays
		// SecretSyncedError (the live fault on 91dc0591) and admin-API calls
		// 401. The path is cluster-shared, so the FIRST Org-create converges
		// the whole Sovereign. Same posture as the two seeds above:
		// idempotent (PutKVv2 overwrites) and NEVER fails the pipeline (a
		// missing bridge Secret only leaves unified-rbac's admin path
		// unauthenticated until NewAPI's token-signing-key Secret renders).
		_ = h.seedNewapiAdminToken(ctx)
	}

	// Step 2 — bp_charts_installed.
	//
	// The same overlay we committed in step 1 also enumerates the bp-*
	// HelmReleases (bp-keycloak per-organization, bp-cnpg, bp-
	// wordpress-tenant, bp-openclaw, bp-stalwart-tenant). Flux on the
	// OTECH cluster reconciles them inside the Organization vcluster via the
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
				// #4732(3): the Org console record must target the
				// DEDICATED console gateway/ELB, not the shared
				// gateway. The authoritative source that exists on
				// EVERY Sovereign with zero extra config is the
				// Sovereign's own console A-record
				// (`console.<OTECHFQDN>` → console ELB EIP, written
				// at prov time). Resolve it here; empty on failure
				// falls back to the shared ingress IP inside
				// ProvisionFreeSubdomain (prior behaviour).
				consoleIPv4 := resolveSovereignConsoleIPv4(ctx, rec.OTECHFQDN)
				if err := deps.DNS.ProvisionFreeSubdomain(ctx, rec.Subdomain, parentZone, deps.OTECHIngressIPv4, consoleIPv4); err != nil {
					return failTransient(rec, "dns", err)
				}
			case store.OrganizationDomainBYO:
				// BYO mode (#828 §C generalisation): the operator's
				// CNAME may point at ANY parent in the pool. Build the
				// accepted-targets list from the live pool composition.
				accepted := []string{rec.OTECHFQDN}
				for _, p := range h.poolDomainsForOrgCreate(deps) {
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
	// covers every Organization's `console.<sub>.<otech>` and the orchestrator
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
				rec.Subdomain, realmZone, "org-"+rec.Subdomain)
			reg := store.TenantRegistration{
				Host:                  host,
				TenantID:              rec.OrganizationID,
				TenantKind:            store.TenantKindOrg,
				KeycloakRealmURL:      realmURL,
				KeycloakClientID:      "catalyst-ui",
				OrganizationNamespace: rec.TenantNamespace,
				OrgKeycloakAdminURL:   fmt.Sprintf("http://keycloak-%s.%s.svc:8080", rec.Subdomain, rec.TenantNamespace),
				OrgKeycloakRealmName:  "org-" + rec.Subdomain,
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
		h.createOrgOrganizationCR(ctx, rec)

		// #4075 — make the Org's console host (console.<slug>.<parent>) serve
		// TLS. A free-subdomain Org on a role=org-pool parent (omani.homes/
		// rest/trade/works) needs three resources the substrate above never
		// provisioned: a wildcard Certificate for *.<slug>.<parent>, a
		// listener pair on cilium-gateway-console binding it, and the console
		// HTTPRoute → catalyst-ui/catalyst-api. Best-effort + idempotent +
		// non-gating (mirrors createOrgOrganizationCR): a transient apiserver
		// failure logs loud but does NOT fail the provision (the substrate is
		// valid), and a later reconcile / HandleReconcileOrganization pass
		// re-applies the trio. BYO Orgs are skipped (own cert + CNAME).
		h.provisionOrgConsoleTLS(ctx, rec)

		// #5501 — THE TERMINAL GATE. Everything above this line is a
		// SUBMIT: an overlay committed to Git, DNS rrsets written, Keycloak
		// clients created, a registry row persisted, the Organization CR
		// POSTed. None of it observes that the Org's boundary exists — the
		// boundary is authored downstream by the org-controller reconciling
		// the CR this step just minted, which takes minutes. Stamping
		// STSDone here is what made `POST /api/v1/organizations` answer
		// `state:"done"` with six "done" steps in ZERO seconds over a
		// Sovereign with no namespace and no vCluster (hw291, 2026-07-29).
		//
		// So: read the CR back and promote ONLY on an observed-Ready
		// boundary. Anything else — Pending, Provisioning, Failed, or
		// unobservable — holds the record at STSTenantRegistered, the
		// highest NON-terminal state, meaning exactly "every side effect
		// the orchestrator owns is committed; the substrate has not been
		// confirmed". Nothing is stranded: ListPending returns every
		// non-terminal row, so the NATS-driven reconciler
		// (ReconcileAllPending) re-runs this step until the boundary comes
		// up, and both finalisers above are idempotent so they are simply
		// re-ensured on each pass.
		phase, ready := h.observeOrgBoundary(ctx, rec)
		rec.BoundaryPhase = phase
		if !ready {
			h.log.Info("org-tenant: holding non-terminal — boundary not observed Ready",
				"slug", rec.Subdomain, "org_tenant_id", rec.OrganizationID,
				"boundary_phase", phase, "state", string(rec.State))
			rec.LastError = ""
			return persist(rec)
		}

		rec.State = store.STSDone
		rec.LastError = ""
		rec = persist(rec)
	}

	return rec
}

/* ── Reconciler hook (NATS-driven) ─────────────────────────────── */

// ReconcileAllPending walks every non-terminal record and re-runs the
// pipeline. Called from the NATS subscriber main.go wires on the
// `org.tenant.reconcile-pending` subject (see ADR-0003 §3.5 for the
// architectural pattern — heartbeat-to-self instead of CronJob).
//
// The reconciler is NOT a goroutine `time.Tick` and NOT a Kubernetes
// CronJob — both violate Inviolable Principle 1. The catalyst-api's
// main.go publishes a heartbeat envelope on
// `org.tenant.reconcile-pending` every 30s; the consumer (in the
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

// poolDomainsForOrgCreate returns the live org-pool list used to
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
// Returns org-pool entries only — primary domains are not bookable
// for Organization tenants per epic #825.
func (h *Handler) poolDomainsForOrgCreate(deps OrganizationDeps) []OrganizationParentDomain {
	merged := make([]OrganizationParentDomain, 0)
	seen := map[string]struct{}{}
	addIf := func(p OrganizationParentDomain) {
		key := strings.ToLower(p.Name)
		if _, ok := seen[key]; ok {
			return
		}
		if !strings.EqualFold(p.Role, "org-pool") {
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
	for _, p := range h.ParentDomainsForOrgCreate() {
		addIf(p)
	}
	if len(merged) > 0 {
		return merged
	}
	// (3) + (4) — single-domain back-compat.
	if strings.TrimSpace(deps.OTECHFQDN) != "" {
		merged = append(merged, OrganizationParentDomain{
			Name: strings.ToLower(deps.OTECHFQDN), Role: "org-pool", NSFlipReady: true,
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
