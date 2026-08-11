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
func (h *Handler) orgResponsesFromCRs(ctx context.Context) ([]orgTenantResponse, observedIsolationIndex, error) {
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: no apiserver to read CRs from. The caller
		// falls back to the local provision store.
		return nil, nil, nil
	}
	list, err := deps.dyn.Resource(organizationGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		// CRD not installed on an older Sovereign, transient apiserver
		// blip, RBAC gap — degrade to store-only rather than erroring the
		// directory. The caller logs + falls back.
		return nil, nil, err
	}
	out := make([]orgTenantResponse, 0, len(list.Items))
	observed := make(observedIsolationIndex, len(list.Items)*2)
	for i := range list.Items {
		resp := orgCRToResponse(&list.Items[i], h.orgTenantDeps.OTECHFQDN)
		out = append(out, resp)
		observed.record(&list.Items[i], resp)
	}
	return out, observed, nil
}

// observedIsolationIndex maps each merge key of a CR-derived row (see
// orgMergeKeys) to the boundary primitive the org-controller was OBSERVED to
// have authored for it. Entries exist ONLY for Organizations whose CR carries
// that observation — an unreconciled CR contributes nothing, so a caller can
// never mistake "not looked at yet" for a measurement (#6145).
type observedIsolationIndex map[string]string

func (idx observedIsolationIndex) record(obj *unstructured.Unstructured, resp orgTenantResponse) {
	observed := observedIsolationFromCR(obj)
	if observed == "" {
		return
	}
	for _, k := range orgMergeKeys(resp) {
		idx[k] = observed
	}
}

// lookup returns the observed isolation for a row, matching on either of the
// row's merge keys (Organization id / subdomain).
func (idx observedIsolationIndex) lookup(r orgTenantResponse) string {
	for _, k := range orgMergeKeys(r) {
		if v, ok := idx[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

// applyObservedIsolation replaces a row's DECLARED boundary with the one the
// org-controller was observed to have authored, and re-derives the two other
// fields that are claims about the SAME object — the vCluster's name and the
// vCluster timeline step. Re-deriving them here is what keeps the payload
// internally consistent: `isolation: "namespace"` beside `vcluster_name:
// "g7freea"` is the shape #5489/#5501 already removed once, and overwriting
// only the label would reintroduce it from the store side.
//
// A no-op when nothing was observed, or when the observation agrees with the
// row — so a correctly recorded Organization is byte-unchanged.
func applyObservedIsolation(r orgTenantResponse, observed string) orgTenantResponse {
	if observed == "" || observed == r.Isolation {
		return r
	}
	r.Isolation = observed
	if observed == "namespace" {
		// Nothing was authored, so there is no vCluster to name and no
		// vCluster step to report.
		r.VClusterName = ""
		r.Steps.VCluster = ""
		return r
	}
	// vcluster: name it after the same slug the org-controller reports
	// (status.vcluster.name == the slug, #5501). A row with no slug to derive
	// from keeps whatever name it already carried rather than losing it.
	if name := vclusterNameFor(observed, r.Subdomain); name != "" {
		r.VClusterName = name
	}
	return r
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

// observedIsolationForSlug reads the canonical Organization CR for slug and
// reports the boundary the org-controller was observed to have authored, or ""
// when there is no in-cluster client, no such CR, or the controller has not
// reconciled it. Same fail-open-to-unknown contract as observeOrgBoundary: an
// unreadable substrate yields no measurement rather than a wrong one.
func (h *Handler) observedIsolationForSlug(ctx context.Context, slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ""
	}
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		return ""
	}
	obj, err := deps.dyn.Resource(organizationGVR()).Get(ctx, slug, metav1.GetOptions{})
	if err != nil || obj == nil {
		return ""
	}
	return observedIsolationFromCR(obj)
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
	// gitops.boundaryIsVcluster): free/S/empty → namespace (host-ns);
	// m/l/xl/flexi → vcluster. Deriving from kind ALONE (the pre-#4539
	// behavior) mislabeled an S-plan Org "vcluster" while it correctly backs a
	// host namespace (UAT rows 9-12, dep 91dc05917e44d1c1); deriving from kind
	// AT ALL (the #4539 `internal → namespace` short-circuit) mislabeled a
	// paid-plan internal Org "namespace" while the org-controller rendered it a
	// real vCluster (UAT row 100). `kind` is a billing dimension; it is not read
	// here.
	//
	// #6145 (UAT row 101) — the tier gate is now the FALLBACK, not the answer.
	// It is a second copy of the switch that decides what to author, so it
	// reports intent; `status.vcluster` reports what the org-controller
	// actually authored. Prefer the measurement and fall back to the gate only
	// while the Organization has not been reconciled yet, so a CR minted
	// seconds ago still badges its plan's boundary instead of blanking.
	isolation := isolationForTier(planSlug)
	if observed := observedIsolationFromCR(obj); observed != "" {
		isolation = observed
	}

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
		OrganizationID: tenantID,
		State:          state,
		Subdomain:      slug,
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   parentDomain,
		AdminEmail:     adminEmail,
		CompanyName:    displayName,
		OTECHFQDN:      strings.TrimSpace(otechFQDN),
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
		// #5501 — carry the OBSERVED boundary phase straight off the CR, so a
		// CR-derived row's substrate-side steps report what the org-controller
		// reports instead of whatever the state ladder implies.
		BoundaryPhase: boundaryPhaseFromCR(obj),
		CreatedAt:     obj.GetCreationTimestamp().Time,
		UpdatedAt:     obj.GetCreationTimestamp().Time,
	}
	return orgTenantRecordToResponse(rec)
}

// observedIsolationFromCR reports the boundary primitive the org-controller
// was OBSERVED to have authored for this Organization — "vcluster",
// "namespace", or "" when the controller has not reconciled the object yet.
//
// #6145 (UAT row 101). `isolation` renders on the Organization identity card
// as a statement about infrastructure, so it has to be MEASURED. Until this
// function existed every producer of the field answered from intent: the BSS
// door persisted the request's declaration (organization_provisioning.go
// resolveOrgShape) and the CR read path mirrored the tier gate
// (isolationForTier). On hw293 that produced `Isolation: Vcluster` for
// `g7freea`, an Organization whose bp-keycloak/bp-agenity StatefulSets ran in
// the host namespace with no vCluster anywhere on the cluster.
//
// The org-controller is the only component that authors the boundary, and it
// records what it authored: `vclusterStatusFor`
// (core/controllers/organization/internal/controller/organization_controller.go)
// stamps `status.vcluster{name,hostCluster,phase}` for a vCluster-backed Org
// and the ZERO VALUE for a host-namespace one (#5489). So:
//
//   - a non-empty name OR phase is POSITIVE evidence a vCluster was authored;
//   - their absence is evidence of a host namespace ONLY once the controller
//     has actually processed the object, which `status.observedGeneration`
//     reports. An untouched CR is byte-identical to a namespace-backed one,
//     and answering "namespace" for it would report every freshly created
//     M-plan Organization as host-namespace-backed. That case returns "" and
//     the caller falls back to the tier gate.
//
// The read is on the VALUE, never the key. The walked g7freea CR carries
// `status.vcluster: {}` — an EMPTY block — so `NestedMap(...)` finding the key
// says nothing at all, and a presence test would report "vcluster" for exactly
// the Organization this row is about.
func observedIsolationFromCR(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if vc, found, _ := unstructured.NestedMap(obj.Object, "status", "vcluster"); found {
		name, _ := vc["name"].(string)
		phase, _ := vc["phase"].(string)
		if strings.TrimSpace(name) != "" || strings.TrimSpace(phase) != "" {
			return "vcluster"
		}
	}
	if gen, found, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration"); found && gen > 0 {
		return "namespace"
	}
	return ""
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
//
// #6145 (UAT row 101) — "local wins" is right for the provisioning TIMELINE
// and wrong for the boundary. The store record's `isolation` is whatever the
// create door persisted at t=0; the CR's status is what the org-controller
// authored. On hw293 the g7freea record said `isolation:"vcluster"` beside
// `plan_slug:"s"` and won the collision, so the console reported a vCluster
// for an Organization backed by the host namespace. A local row that collides
// with an OBSERVED CR therefore adopts that observation before it wins.
func mergeOrgResponses(local, fromCR []orgTenantResponse, observed observedIsolationIndex) []orgTenantResponse {
	seen := make(map[string]struct{}, len(local)*2)
	out := make([]orgTenantResponse, 0, len(local)+len(fromCR))

	add := func(r orgTenantResponse) {
		keys := orgMergeKeys(r)
		if len(keys) == 0 {
			// Unidentifiable on BOTH axes. Keep it — dropping a row the
			// operator's own store authored would be worse than showing it —
			// but it cannot participate in dedupe, so say so rather than
			// pretend it was deduped.
			out = append(out, r)
			return
		}
		for _, k := range keys {
			if _, dup := seen[k]; dup {
				return
			}
		}
		for _, k := range keys {
			seen[k] = struct{}{}
		}
		out = append(out, r)
	}

	// Local first: it wins on collision. The BSS door authored the in-flight
	// provisioning detail (7-step timeline, last_error, commit_sha) that the
	// CR does not carry — but NOT the boundary, which is measured (#6145).
	for _, r := range local {
		add(applyObservedIsolation(r, observed.lookup(r)))
	}
	for _, r := range fromCR {
		add(r)
	}
	return out
}

func orgMergeKeys(r orgTenantResponse) []string {
	out := make([]string, 0, 2)
	if id := strings.ToLower(strings.TrimSpace(r.OrganizationID)); id != "" {
		out = append(out, "id:"+id)
	}
	if sub := strings.ToLower(strings.TrimSpace(r.Subdomain)); sub != "" {
		out = append(out, "sub:"+sub)
	}
	return out
}
