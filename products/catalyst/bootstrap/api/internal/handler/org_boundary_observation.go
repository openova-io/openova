// org_boundary_observation.go — #5501. The Organization provisioning
// pipeline (organization_provisioning.go) is a chain of SUBMITS: it commits
// a GitOps overlay, writes DNS rrsets, creates Keycloak clients, writes a
// registry row and POSTs the canonical Organization CR. Not one of those
// steps observes that the Organization's boundary — the per-Org namespace or
// the dedicated vCluster — actually came up, because the boundary is authored
// downstream by the org-controller reconciling the CR the pipeline mints.
//
// Before this file the pipeline nevertheless marched to the terminal STSDone
// in the same request, so `POST /api/v1/organizations` answered
// `state:"done"` with six `"done"` steps in ZERO seconds while the walked
// Sovereign had no namespace, no vCluster and an empty `status.phase`
// (hw291, 2026-07-29). The truth was already on the cluster: the CR the
// pipeline had just minted said `status.vcluster.phase: Pending`.
//
// This file is the read-back seam. It resolves the SAME dynamic client the
// CR mint uses (sovereignDepsFor — test-overridable via
// SetSovereignDepsFactory) and reports the observed boundary phase, which
// the pipeline stamps onto the record, gates STSDone on, and the wire
// response derives its substrate-side steps from.
//
// Deliberately fail-CLOSED: "no in-cluster client" / "the read errored" is
// reported as UNOBSERVED (empty phase, ready=false), never as ready. An
// Organization whose substrate nobody could look at is not a provisioned
// Organization, and publishing terminal success for it is the exact defect
// this file exists to remove. Nothing is stranded by that choice — the
// record stays at the highest non-terminal state and the NATS-driven
// reconciler (ReconcileAllPending, which walks every non-terminal row)
// re-runs the pipeline until the boundary is observed Ready.

package handler

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Boundary phases. These mirror the org-controller's ladder
// (core/controllers/organization/internal/controller/organization_controller.go
// vclusterReadiness): Pending → Provisioning → Ready, plus Failed.
const (
	boundaryPhaseReady        = "Ready"
	boundaryPhaseProvisioning = "Provisioning"
	boundaryPhasePending      = "Pending"
	boundaryPhaseFailed       = "Failed"
)

// boundaryPhaseFromCR derives the observed boundary phase from an
// Organization CR. Both tiers are covered by the SAME read, because the
// org-controller reports them differently (#5489):
//
//   - vcluster tier (plan m/l/xl/flexi): `status.vcluster.phase` carries the
//     ladder — the controller only stamps that block when it actually
//     authors a vCluster.
//   - host-namespace tier (internal, plan ""/s/free): NO vcluster block is
//     ever written, so readiness is the top-level Ready condition the
//     controller stamps once the `<slug>` namespace exists.
//
// Returns "" ONLY for an object that carries no status at all — an honest
// "the controller has not observed this object yet", which is exactly what
// the walked hw291 Org looked like seconds after create.
func boundaryPhaseFromCR(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "vcluster", "phase"); strings.TrimSpace(phase) != "" {
		switch strings.ToLower(strings.TrimSpace(phase)) {
		case "ready":
			return boundaryPhaseReady
		case "failed":
			return boundaryPhaseFailed
		case "provisioning":
			return boundaryPhaseProvisioning
		default:
			return boundaryPhasePending
		}
	}
	// Host-namespace tier: the Ready condition is the boundary signal.
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		if !strings.EqualFold(ctype, "Ready") {
			continue
		}
		cstatus, _ := cm["status"].(string)
		if strings.EqualFold(cstatus, "True") {
			return boundaryPhaseReady
		}
		// An explicit Ready=False means the controller HAS observed the
		// object and is still working on it.
		return boundaryPhaseProvisioning
	}
	return ""
}

// observeOrgBoundary reads back the canonical Organization CR for rec and
// reports (phase, ready).
//
//   - ready is true ONLY for an observed Ready boundary.
//   - phase is "" whenever the substrate could not be observed at all (no
//     in-cluster dynamic client — CI / out-of-cluster catalyst-api — or the
//     Get errored). An absent CR is a real observation, not an unknown: the
//     pipeline mints the CR before calling this, so a missing object means
//     the mint did not land, and the boundary is reported Pending.
func (h *Handler) observeOrgBoundary(ctx context.Context, rec store.OrganizationProvisionRecord) (string, bool) {
	slug := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	if slug == "" {
		return "", false
	}
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Unobservable — NOT "ready". See the fail-closed note in the file
		// header.
		h.log.Info("org-tenant: boundary unobserved — no in-cluster dynamic client; holding non-terminal",
			"slug", slug, "org_tenant_id", rec.OrganizationID, "err", err)
		return "", false
	}
	obj, err := deps.dyn.Resource(organizationGVR()).Get(ctx, slug, metav1.GetOptions{})
	if err != nil || obj == nil {
		h.log.Warn("org-tenant: boundary unobserved — Organization CR read failed; holding non-terminal",
			"slug", slug, "org_tenant_id", rec.OrganizationID, "err", err)
		return "", false
	}
	phase := boundaryPhaseFromCR(obj)
	return phase, phase == boundaryPhaseReady
}

// boundaryStepFor maps an observed boundary phase onto the wire step value
// the SPA renders. An UNOBSERVED boundary ("") is "pending" — the honest
// rendering of "this has not been seen to exist", never "done".
func boundaryStepFor(phase string) string {
	switch phase {
	case boundaryPhaseReady:
		return "done"
	case boundaryPhaseFailed:
		return "failed"
	default:
		return "pending"
	}
}
