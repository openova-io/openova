// org_list_from_cr.go — #4479 (Refs #4179 #4290 #3687). The console
// org-list + org-detail read path used to source ONLY from the local
// directory-backed OrganizationProvisionStore (deps.Store) — the
// provision-record store written by THIS catalyst-api's own create
// pipeline (the BSS door, HandleCreateOrganization). A funnel-created
// Organization (core/services/provisioning consumer → createOrganizationCR)
// mints the canonical orgs.openova.io CR directly but never writes a
// local provision record, so it was invisible: GET /api/v1/organizations
// returned {"items":[]} and GET /api/v1/organizations/{slug} 404'd
// org-tenant-not-found — for every funnel Org AND the parent Sovereign Org.
//
// This file aligns the read path with the #4290 single-producer model:
// every Organization — funnel OR BSS — is an orgs.openova.io CR the
// org-controller reconciles, so the console reads from THAT source of
// truth. The local store is still consulted (and wins on collision) so
// in-flight provisioning detail (the 7-step timeline, last_error,
// commit_sha) keeps rendering for rows the BSS door authored before the
// CR lands. The merge is deduped by slug.
//
// CR-read is nil-tolerant by construction: out-of-cluster / CI (no
// in-cluster dynamic client) leaves orgResponsesFromCRs returning
// (nil, nil) and the handlers fall back to the local store unchanged —
// preserving the existing store-only unit tests.

package handler

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgResponsesFromCRs lists every orgs.openova.io Organization CR via the
// in-cluster dynamic client and maps each to an orgTenantResponse. Returns
// (nil, nil) when no in-cluster dynamic client is available (CI /
// out-of-cluster catalyst-api) so callers transparently fall back to the
// local store. A list error is surfaced so the caller can decide whether to
// degrade to store-only (it does) rather than 5xx the whole console.
func (h *Handler) orgResponsesFromCRs(ctx context.Context) ([]orgTenantResponse, error) {
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: no apiserver to read CRs from. The caller
		// falls back to the local provision store.
		return nil, nil
	}
	list, err := deps.dyn.Resource(organizationGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		// CRD not installed on an older Sovereign, transient apiserver
		// blip, RBAC gap — degrade to store-only rather than erroring the
		// directory. The caller logs + falls back.
		return nil, err
	}
	out := make([]orgTenantResponse, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, orgCRToResponse(&list.Items[i], h.orgTenantDeps.OTECHFQDN))
	}
	return out, nil
}

// orgCRFromSlug fetches a single Organization CR by name (the slug). Returns
// (nil, false) when no dynamic client is available or the CR is absent — the
// caller then falls back to the local store / 404.
func (h *Handler) orgCRFromSlug(ctx context.Context, slug string) (*orgTenantResponse, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil, false
	}
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		return nil, false
	}
	obj, err := deps.dyn.Resource(organizationGVR()).Get(ctx, slug, metav1.GetOptions{})
	if err != nil || obj == nil {
		return nil, false
	}
	resp := orgCRToResponse(obj, h.orgTenantDeps.OTECHFQDN)
	return &resp, true
}

// orgCRToResponse maps an Organization CR (orgs.openova.io/v1) onto the
// console's orgTenantResponse wire shape. The CR is the canonical desired-
// state both doors write (org_tenant_org_cr.go ensureOrganizationCR +
// core/services/provisioning createOrganizationCR), so the mapping mirrors
// that spec shape. Provisioning-timeline detail (steps/last_error/commit_sha)
// is NOT on the CR — those stay zero-valued here and are filled from the
// local store on the merge when a record exists; for a pure funnel Org the
// timeline is derived from the CR phase (Ready→done) so the directory still
// renders a sane status.
func orgCRToResponse(obj *unstructured.Unstructured, otechFQDN string) orgTenantResponse {
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	getSpec := func(key string) string {
		if spec == nil {
			return ""
		}
		if v, ok := spec[key].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	slug := getSpec("slug")
	if slug == "" {
		slug = strings.TrimSpace(obj.GetName())
	}
	displayName := getSpec("displayName")

	kind := strings.ToLower(getSpec("kind"))
	if kind != "internal" && kind != "customer" {
		kind = "customer"
	}
	tier := strings.ToLower(getSpec("tier"))
	if tier == "" {
		tier = "org"
	}
	billingMode := strings.ToLower(getSpec("billingMode"))
	if billingMode == "" {
		billingMode = "real"
	}
	// planSlug drives the #4292 tier gate below — read it up front so the
	// derived isolation label reflects the actual backing.
	planSlug := strings.ToLower(getSpec("planSlug"))
	// Isolation is derived (not a spec field) from the SAME #4292 tier gate the
	// org-controller uses to author the backing (isolationForTier mirrors
	// gitops.boundaryIsVcluster): internal → namespace; customer free/S →
	// namespace (host-ns); customer M+/flexi → vcluster. Deriving from kind
	// ALONE (the pre-#4539 behavior) mislabeled an S-plan Org "vcluster" while
	// it correctly backs a host namespace (UAT rows 9-12, dep 91dc05917e44d1c1).
	isolation := isolationForTier(kind, planSlug)

	// tenantPublic.parentDomain carries the org-pool apex; subdomain
	// defaults to the slug. Empty parent → single-domain back-compat
	// (deriveConsoleHost falls back to OTECHFQDN).
	parentDomain := ""
	if tp, ok := spec["tenantPublic"].(map[string]any); ok {
		if pd, ok := tp["parentDomain"].(string); ok {
			parentDomain = strings.ToLower(strings.TrimSpace(pd))
		}
	}

	adminEmail := ""
	if owners, ok := spec["owners"].([]any); ok {
		for _, o := range owners {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			if e, ok := om["email"].(string); ok && strings.TrimSpace(e) != "" {
				adminEmail = strings.TrimSpace(e)
				// Prefer the owner role when present; otherwise first email.
				if r, _ := om["role"].(string); strings.EqualFold(r, "owner") {
					break
				}
			}
		}
	}

	// tenant-id label is stamped by both CR emitters (ensureOrganizationCR:
	// openova.io/tenant-id; provisioning createOrganizationCR: same). Fall
	// back to the slug so org-detail is still addressable on legacy CRs.
	tenantID := ""
	if labels := obj.GetLabels(); labels != nil {
		tenantID = strings.TrimSpace(labels["openova.io/tenant-id"])
	}
	if tenantID == "" {
		tenantID = slug
	}

	// Derive the provisioning state from the CR status. The org-controller
	// stamps status.vcluster.phase (Pending|Provisioning|Ready|Failed) and a
	// top-level Ready condition. A Ready Org reads as STSDone so the console
	// timeline shows green; anything else maps to a best-effort in-flight /
	// failed state so the directory badge is honest.
	state := orgStateFromCR(obj)

	// Synthesize a provision record so we reuse deriveConsoleHost + the
	// existing record→response mapper for hostname/steps derivation. This
	// keeps the CR row byte-shaped identically to a store-backed row.
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  tenantID,
		State:           state,
		Subdomain:       slug,
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		ParentDomain:    parentDomain,
		AdminEmail:      adminEmail,
		CompanyName:     displayName,
		OTECHFQDN: strings.TrimSpace(otechFQDN),
		// #5489 — derived from the same isolation the row carries: only a
		// vcluster-tier Org has a vCluster to name. The old unconditional
		// `vc-<slug>` shipped `vcluster_name: "vc-…"` right next to
		// `isolation: "namespace"` — latent (the UI declares the field at
		// pages/org/org.api.ts and never consumes it), but it would assert
		// an object that does not exist the moment anyone bound it.
		VClusterName:    vclusterNameFor(isolation, slug),
		TenantNamespace: slug,
		Kind:            kind,
		Tier:            tier,
		BillingMode:     billingMode,
		Isolation:       isolation,
		PlanSlug:        planSlug,
		CreatedAt:       obj.GetCreationTimestamp().Time,
		UpdatedAt:       obj.GetCreationTimestamp().Time,
	}
	return orgTenantRecordToResponse(rec)
}

// orgStateFromCR maps the Organization CR status to a provision state for the
// console timeline. Ready (vcluster phase Ready OR a Ready=True condition) →
// STSDone; an explicit Failed phase → STSFailed; everything else (Pending /
// Provisioning / no status yet) → STSBPChartsInstalled so the directory shows
// "provisioning" rather than a misleading "pending" for an Org whose substrate
// is already coming up.
func orgStateFromCR(obj *unstructured.Unstructured) store.OrganizationProvisionState {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "vcluster", "phase")
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "ready":
		return store.STSDone
	case "failed":
		return store.STSFailed
	}
	// Top-level Ready condition as a secondary signal.
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		if strings.EqualFold(ctype, "Ready") && strings.EqualFold(cstatus, "True") {
			return store.STSDone
		}
	}
	if strings.TrimSpace(phase) == "" {
		// No status block yet — substrate is being created.
		return store.STSBPChartsInstalled
	}
	return store.STSBPChartsInstalled
}

// mergeOrgResponses unions the local store-backed rows with the CR-derived
// rows, deduped by slug. Local rows win on collision: the BSS door authored
// them and they carry the richer provisioning timeline (steps/last_error/
// commit_sha) the CR lacks. CR-only rows (every funnel Org + the parent
// Sovereign Org) are appended so the directory shows every real Organization.
// Order: local rows first (newest-first as the store returns them), then the
// CR-only remainder.
func mergeOrgResponses(local, fromCR []orgTenantResponse) []orgTenantResponse {
	bySlug := make(map[string]struct{}, len(local))
	out := make([]orgTenantResponse, 0, len(local)+len(fromCR))
	for _, r := range local {
		key := strings.ToLower(strings.TrimSpace(r.Subdomain))
		if key != "" {
			bySlug[key] = struct{}{}
		}
		out = append(out, r)
	}
	for _, r := range fromCR {
		key := strings.ToLower(strings.TrimSpace(r.Subdomain))
		if key != "" {
			if _, seen := bySlug[key]; seen {
				continue
			}
			bySlug[key] = struct{}{}
		}
		out = append(out, r)
	}
	return out
}
