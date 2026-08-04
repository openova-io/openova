// Package store — organization_provisioning.go: persisted state for the Organization tenant
// provisioning pipeline (issue #804).
//
// On a marketplace tenant-create event the catalyst-api orchestrator
// must drive a multi-step pipeline:
//
//	pending
//	   → vcluster_created
//	      → bp_charts_installed
//	         → dns_provisioned
//	            → certs_issued
//	               → keycloak_clients_provisioned
//	                  → tenant_registered
//	                     → done
//	   ↓ (5 transient failures)
//	failed
//
// Each step is independently idempotent: the orchestrator commits a
// per-tenant Kustomize overlay to the GitOps repo (see
// internal/handler/organization_gitops.go), Flux reconciles the
// resulting HelmReleases, the orchestrator polls Flux's reported
// state, and the state machine advances when each gate passes.
//
// The reconciler is event-driven (NATS subject
// `org.tenant.reconcile-pending` per Inviolable Principle 1 / ADR-0001
// §6) — never a Kubernetes CronJob, never a goroutine `time.Tick`.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3, this implementation uses the
// same flat-file convention the deployment store + user-provision
// store already follow. When the unified-rbac slice is split out as
// its own deployable unit the schema migrates verbatim to Postgres.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// OrganizationProvisionState is one of the canonical states the pipeline
// transitions through. Strings (not int constants) so the on-disk
// record is human-readable.
type OrganizationProvisionState string

const (
	// STSPending — the orchestrator persisted the row but no side
	// effects have been performed yet. The reconciler picks up
	// pending rows and re-runs from step 1.
	STSPending OrganizationProvisionState = "pending"
	// STSVClusterCreated — the per-tenant Kustomize overlay
	// containing the vcluster HelmRelease has been committed and
	// pushed to the GitOps repo. Flux reconciles within ~1 min.
	STSVClusterCreated OrganizationProvisionState = "vcluster_created"
	// STSBPChartsInstalled — the same overlay also installs the bp-*
	// charts (bp-keycloak, bp-wordpress-tenant, bp-openclaw,
	// bp-stalwart-tenant) inside the Organization vcluster. (Postgres is
	// reconciled by the cluster-wide platform cnpg-system operator; the per-Org
	// bp-cnpg operator was removed in #4920.) The orchestrator
	// advances to this state once the parent overlay's Kustomization
	// is Ready=True.
	STSBPChartsInstalled OrganizationProvisionState = "bp_charts_installed"
	// STSDNSProvisioned — A/CNAME records for `console.<org-host>`
	// (free-subdomain) or BYO CNAME validation has succeeded.
	STSDNSProvisioned OrganizationProvisionState = "dns_provisioned"
	// STSCertsIssued — wildcard cert (free-subdomain) or per-host
	// HTTP-01 (BYO) is Ready=True. For free-subdomain mode the
	// wildcard already covers the Organization's *.<otech-fqdn> subtree so
	// the orchestrator advances immediately.
	STSCertsIssued OrganizationProvisionState = "certs_issued"
	// STSKeycloakClientsProvisioned — OIDC clients for WordPress,
	// OpenClaw, Stalwart, unified-RBAC Organization-tier have been pre-created
	// in the Organization vcluster Keycloak. Group templates `org-admin` /
	// `org-user` are seeded.
	STSKeycloakClientsProvisioned OrganizationProvisionState = "keycloak_clients_provisioned"
	// STSTenantRegistered — the row in the host → tenant registry has
	// been written so the SPA's `/api/v1/tenant/discover` lookup
	// returns the new tenant. This is the gate that opens the SPA's
	// front door for the Organization admin's first login.
	STSTenantRegistered OrganizationProvisionState = "tenant_registered"
	// STSDone — terminal happy state. Subscribers on
	// `org.tenant.events` have been notified.
	STSDone OrganizationProvisionState = "done"
	// STSFailed — terminal sad state. LastError carries a structured
	// `<step>:<class>:<detail>` string per ADR-0003 §3.8.
	STSFailed OrganizationProvisionState = "failed"
	// STSDeleted — audit row preserved after a tenant tear-down. The
	// inverse pipeline (see ADR-0003 §3.7) marks rows here so the
	// reconciler doesn't pick them up again.
	STSDeleted OrganizationProvisionState = "deleted"
)

// OrganizationDomainMode discriminates the two domain-onboarding flows the
// pipeline supports per epic #795 [Q-mine-1]. Free-subdomain mode is
// the turnkey default; BYO mode requires the customer to point a CNAME
// at the otech ingress before the pipeline can complete.
type OrganizationDomainMode string

const (
	// OrganizationDomainFreeSubdomain — `<subdomain>.<otech-fqdn>`. The
	// orchestrator authoritatively owns the DNS records via
	// PowerDNS; cert issuance is covered by the otech-wide wildcard.
	OrganizationDomainFreeSubdomain OrganizationDomainMode = "free-subdomain"
	// OrganizationDomainBYO — customer-owned domain (`acme.com`). The customer
	// must CNAME `console.acme.com` → otech ingress before the
	// orchestrator can verify ownership; per-host HTTP-01 ACME runs
	// on the otech ingress to mint a per-host certificate.
	OrganizationDomainBYO OrganizationDomainMode = "byo"
)

// OrganizationProvisionRecord is the per-tenant state row.
//
// OrganizationID is the primary key — a UUID minted by the orchestrator
// on first POST. It is opaque to humans; the Organization admin's mental model
// uses Subdomain ("acme") instead.
type OrganizationProvisionRecord struct {
	OrganizationID string                  `json:"org_tenant_id"`
	State       OrganizationProvisionState `json:"state"`
	// Subdomain — the operator-supplied Organization slug. In free-subdomain
	// mode the resulting console URL is `console.<subdomain>.<otech>`;
	// in BYO mode the slug is metadata only (the BYO host is the
	// canonical key).
	Subdomain string `json:"subdomain"`
	// DomainMode — see OrganizationDomainMode.
	DomainMode OrganizationDomainMode `json:"domain_mode"`
	// BYODomain — populated when DomainMode == OrganizationDomainBYO. The
	// orchestrator builds the host as `console.<byo_domain>`.
	BYODomain string `json:"byo_domain,omitempty"`
	// AdminEmail — the Organization's first user. The orchestrator wires this
	// into the bp-wordpress-tenant chart values (admin email) and into
	// the welcome-email subject of the `org.tenant.events` envelope.
	AdminEmail string `json:"admin_email"`
	// CompanyName — branding metadata (chart titles, welcome email
	// salutation). Optional — empty falls back to the subdomain slug.
	CompanyName string `json:"company_name,omitempty"`
	// OTECHFQDN — captured at create time so the reconciler doesn't
	// have to re-read env. Used to build derived hostnames + the cert
	// issuer ClusterRef and to scope the GitOps overlay path.
	OTECHFQDN string `json:"otech_fqdn"`
	// ParentDomain — the parent zone the Organization's free-subdomain hangs
	// under (multi-domain Sovereign per epic #825). For
	// free-subdomain mode this is one of the Sovereign's
	// `role: org-pool` parent domains (e.g. "omani.trade") and the
	// SPA host becomes `console.<subdomain>.<parent_domain>`. For
	// BYO mode this is empty (the BYO domain is the canonical key).
	// Per Inviolable Principle 4 the value is operator-supplied at
	// create time, never inferred from OTECHFQDN.
	ParentDomain string `json:"parent_domain,omitempty"`
	// VClusterName — `vc-<subdomain>` for a vcluster-tier Org, "" for a
	// namespace-tier one (#5489: only the vcluster tier has a vCluster to
	// name). Captured at create so the orchestrator can reference it in
	// GitOps manifest paths without re-deriving on every reconcile —
	// though since #4188 no overlay template renders it.
	VClusterName string `json:"vcluster_name"`
	// TenantNamespace — `org-<org_tenant_id>` per #804 scope §1. The
	// bp-wordpress-tenant / bp-openclaw / bp-stalwart-tenant charts
	// install into this namespace.
	TenantNamespace string `json:"tenant_namespace"`
	// CommitSHA — sha returned by the GitOps push that committed the
	// per-tenant overlay; surfaced in the orchestrator API response so
	// the operator can deep-link the audit trail.
	CommitSHA string `json:"commit_sha,omitempty"`
	// RetryCount — bumped on every transient failure of the same
	// step. Promoted to STSFailed at 5.
	RetryCount int `json:"retry_count"`
	// LastError — structured `<step>:<class>:<detail>` per ADR-0003
	// §3.8. Truncated to 256 bytes.
	LastError string `json:"last_error,omitempty"`

	// ── Organizations model (issue #3378 B1 — the internal door) ──
	//
	// These four fields map onto OrganizationSpec
	// (core/controllers/organization/internal/orgapi/types.go:54-79):
	// Kind/Tier/BillingMode + the derived Isolation. The marketplace
	// funnel (the customer door) omits them and the create handler
	// stamps the customer default shape (kind=customer, tier=org,
	// billingMode=real, isolation=vcluster) so the funnel is byte-
	// unchanged. The internal door (kind=internal) stamps the
	// department shape (showback + namespace) and skips the voucher
	// dependency. Empty (legacy records) reads as the customer default.
	Kind        string `json:"kind,omitempty"`
	Tier        string `json:"tier,omitempty"`
	BillingMode string `json:"billing_mode,omitempty"`
	Isolation   string `json:"isolation,omitempty"`
	// PlanSlug — purchased catalog plan slug (s|m|l|xl|flexi), #4292. Carried
	// onto the Organization CR spec.planSlug so the org-controller materializes
	// the matching ResourceQuota + LimitRange on the boundary namespace. Empty
	// (legacy records) reads as "s" at the renderer.
	PlanSlug string `json:"plan_slug,omitempty"`

	// BoundaryPhase — the LAST OBSERVED phase of the Org's boundary
	// (#5501), read back from the canonical Organization CR the pipeline
	// mints: `status.vcluster.phase` for a vcluster-tier Org, the
	// top-level Ready condition for a host-namespace one. One of
	// "Ready" | "Provisioning" | "Pending" | "Failed"; EMPTY means
	// NEVER OBSERVED (no in-cluster client, or the read failed) — it is
	// deliberately NOT defaulted to a phase, because "we could not look"
	// and "we looked and it is up" are different facts.
	//
	// This is the only substrate truth the API has: every step of the
	// provisioning pipeline is a SUBMIT (commit an overlay, write a DNS
	// rrset, POST a CR), so the state machine alone can never tell a
	// caller whether the Organization actually exists. The pipeline
	// stamps this field, gates the terminal STSDone promotion on it, and
	// the wire response derives its substrate-side steps from it.
	BoundaryPhase string `json:"boundary_phase,omitempty"`

	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// orgTenantDir is the on-disk subdirectory under <store>/org-tenant.
// One file per tenant (org_tenant_id.json); a directory walk is fine
// at the cardinality this layer targets (hundreds, not millions).
const orgTenantDir = "org-tenant"

// OrganizationProvisionStore is the directory-backed implementation.
//
// Concurrency: a single mutex guards the on-disk write+rename — reads
// load the file on every Get so a parallel writer can't return
// stale state.
type OrganizationProvisionStore struct {
	dir string
	mu  sync.Mutex
}

// NewOrganizationProvisionStore returns a store rooted at
// <dir>/org-tenant. dir must already exist; the subdirectory is
// created at 0o700 perms.
func NewOrganizationProvisionStore(dir string) (*OrganizationProvisionStore, error) {
	if dir == "" {
		return nil, errors.New("org-tenant store: directory is required")
	}
	root := filepath.Join(dir, orgTenantDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("org-tenant store: mkdir %q: %w", root, err)
	}
	return &OrganizationProvisionStore{dir: root}, nil
}

// Put upserts a record. CreatedAt is preserved across upserts; the
// caller is responsible for setting State/Subdomain/etc. before
// calling. UpdatedAt is bumped automatically.
//
// #5501 — Put takes the record BY VALUE, so the CreatedAt/UpdatedAt it
// stamps land on this function's local copy and are invisible to the
// caller. Callers that serialize the record they just persisted (the
// Organization create handler) therefore published Go ZERO timestamps
// (0001-01-01T00:00:00Z) while the on-disk row carried the real ones —
// which is why a GET showed real timestamps and the POST response did
// not. Use Save when the caller keeps the record; Put is retained for
// fire-and-forget writers.
func (s *OrganizationProvisionStore) Put(rec OrganizationProvisionRecord) error {
	return s.Save(&rec)
}

// Save persists rec and writes the stamped CreatedAt/UpdatedAt BACK onto
// the caller's record (#5501), so an in-memory record that has just been
// persisted carries the same timestamps the on-disk row does. Same
// semantics as Put in every other respect.
func (s *OrganizationProvisionStore) Save(rec *OrganizationProvisionRecord) error {
	if rec == nil {
		return errors.New("org-tenant: record is required")
	}
	if strings.TrimSpace(rec.OrganizationID) == "" {
		return errors.New("org-tenant: org_tenant_id is required")
	}
	if rec.State == "" {
		rec.State = STSPending
	}
	now := time.Now().UTC()
	rec.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, sanitizeID(rec.OrganizationID)+".json")
	if rec.CreatedAt.IsZero() {
		if existing, err := readOrganizationRec(path); err == nil && !existing.CreatedAt.IsZero() {
			rec.CreatedAt = existing.CreatedAt
		} else {
			rec.CreatedAt = now
		}
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("org-tenant: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("org-tenant: write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// Get returns the record for the given tenant id.
func (s *OrganizationProvisionStore) Get(orgTenantID string) (OrganizationProvisionRecord, bool) {
	path := filepath.Join(s.dir, sanitizeID(orgTenantID)+".json")
	rec, err := readOrganizationRec(path)
	if err != nil {
		return OrganizationProvisionRecord{}, false
	}
	return rec, true
}

// List returns every persisted record sorted by CreatedAt descending
// (newest first). Used by the orchestrator's reconciler to find rows
// that aren't terminal.
func (s *OrganizationProvisionStore) List() []OrganizationProvisionRecord {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	out := make([]OrganizationProvisionRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		if rec, err := readOrganizationRec(path); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ListPending returns every record whose State is not in
// {STSDone, STSFailed, STSDeleted}. The reconciler uses this to find
// rows that need another pass.
func (s *OrganizationProvisionStore) ListPending() []OrganizationProvisionRecord {
	all := s.List()
	out := make([]OrganizationProvisionRecord, 0, len(all))
	for _, rec := range all {
		switch rec.State {
		case STSDone, STSFailed, STSDeleted:
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Delete removes a record. Idempotent — deleting a missing id returns
// nil. Used by tests; production code marks State=STSDeleted for the
// audit trail rather than removing rows.
func (s *OrganizationProvisionStore) Delete(orgTenantID string) error {
	path := filepath.Join(s.dir, sanitizeID(orgTenantID)+".json")
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("org-tenant: remove: %w", err)
	}
	return nil
}

func readOrganizationRec(path string) (OrganizationProvisionRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OrganizationProvisionRecord{}, err
	}
	var rec OrganizationProvisionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return OrganizationProvisionRecord{}, err
	}
	return rec, nil
}
