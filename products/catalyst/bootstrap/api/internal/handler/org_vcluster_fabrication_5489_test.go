package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Tests for #5489 — the bootstrap API synthesized a vCluster for
// namespace-backed Organizations on two seams. (Every Organization created
// now is vCluster-backed — orgIsolation, founder direction 2026-09-10 — so the
// namespace-backed subject is an Organization the org-controller reconciled
// BEFORE that and is still observed as such; the contract below is about
// reporting a measured absence honestly, not about any plan.)
//
//  1. `vcluster_name: "vc-<slug>"` was stamped unconditionally
//     (org_list_from_cr.go + the create path), so the payload asserted a
//     vCluster right next to `isolation: "namespace"`. Latent — the UI
//     declares the field and never binds it — but a lie in the wire shape.
//  2. `steps.vcluster: "done"` was emitted for every Org, so the
//     post-create timeline painted a completed vCluster step for a tier
//     that never provisions one (proven live on hw291, dep w/ zero
//     vclusters.vcluster.com resources).
//
// Anti-theater: each case is proven in BOTH directions — the namespace-backed
// assertions fail against the pre-fix code, and the vCluster-backed control
// assertions pin that the honest value still renders for Orgs that DO have
// a vCluster (a fix that blanked the field everywhere would satisfy the
// first half while breaking the directory for every real Organization).

func TestVClusterNameFor(t *testing.T) {
	t.Parallel()
	// #5501 — the reported name is the BARE SLUG, the name the org-controller
	// (the object's only producer) stamps at status.vcluster.name. The former
	// `vc-<slug>` synthesis disagreed with the CR on a walked Sovereign.
	if got := vclusterNameFor("vcluster", "acme"); got != "acme" {
		t.Errorf("vcluster tier: got %q want acme (the CR-authoritative name)", got)
	}
	if got := vclusterNameFor("namespace", "acme"); got != "" {
		t.Errorf("an observed namespace-backed Org must not synthesize a vCluster name, got %q", got)
	}
	if got := vclusterNameFor("", "acme"); got != "" {
		t.Errorf("unknown isolation must not synthesize a vCluster name, got %q", got)
	}
}

// TestOrgResponseFromCR_ObservedNamespaceBacked_NoVClusterFields — a CR the
// org-controller reconciled with an EMPTY status.vcluster block (the shape a
// controller that predates the every-plan boundary wrote for a host-namespace
// Org; the walked hw291/hw293 objects) must carry NO vcluster_name and NO
// steps.vcluster in the wire payload. The raw JSON is asserted (not just the
// struct) because the step omission rides the omitempty tag — a struct-only
// check could pass while the key still shipped.
//
// The fixture is orgCRHostNamespace, not orgReadyCR: orgReadyCR now mints the
// vCluster block every plan gets, so it can no longer stand in for this case.
func TestOrgResponseFromCR_ObservedNamespaceBacked_NoVClusterFields(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgCRHostNamespace(t, "omantel", "s"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}

	var typed struct {
		Items []orgTenantResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &typed); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if len(typed.Items) != 1 {
		t.Fatalf("items: want 1 got %d: %s", len(typed.Items), w.Body.String())
	}
	row := typed.Items[0]
	if row.Isolation != "namespace" {
		t.Fatalf("fixture must be OBSERVED namespace-backed (reconciled, empty status.vcluster), got %q", row.Isolation)
	}
	if row.VClusterName != "" {
		t.Errorf("namespace-backed row must not name a vCluster, got vcluster_name=%q", row.VClusterName)
	}
	if row.Steps.VCluster != "" {
		t.Errorf("namespace-backed row must not carry a vCluster step, got steps.vcluster=%q", row.Steps.VCluster)
	}

	// Raw-JSON check: the steps object must OMIT the key entirely, and the
	// remaining timeline must still be present (vacuity control — an empty
	// steps object would also pass the absence check while breaking the
	// timeline).
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	var steps map[string]string
	if err := json.Unmarshal(raw.Items[0]["steps"], &steps); err != nil {
		t.Fatalf("decode steps: %v", err)
	}
	if _, present := steps["vcluster"]; present {
		t.Errorf("steps JSON must omit the vcluster key for a namespace-backed Org, got %v", steps)
	}
	if steps["bp_charts"] != "done" || steps["registry"] != "done" {
		t.Errorf("the rest of the Ready timeline must survive the omission, got %v", steps)
	}
}

// TestOrgResponseFromCR_VClusterBacked_KeepsVClusterFields is the control
// direction: a CR whose status carries the vCluster block keeps the honest
// shape — the bare-slug name (#5501) + a rendered vCluster step. It runs on
// plan `s` AND plan `m`: plan `s` is the discriminating half, because a read
// path that still keyed the boundary off the plan would blank these fields
// for `s` while the org-controller had authored a real vCluster.
func TestOrgResponseFromCR_VClusterBacked_KeepsVClusterFields(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		t.Run("plan "+plan, func(t *testing.T) {
			// #6145 — built on the plan UP FRONT rather than patched afterwards,
			// so the fixture's status is the one the org-controller stamps (a
			// real status.vcluster block). Patching the plan after the status
			// was written produced a CR whose plan and status disagreed.
			cr := orgReadyCRWithPlan("acme", "ACME Corp", "", "ceo@acme.com", "Ready", plan)
			h, _ := newOrgHandlerWithSeededCRs(t, cr)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
			w := httptest.NewRecorder()
			h.HandleListOrganizations(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
			}
			var typed struct {
				Items []orgTenantResponse `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &typed); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(typed.Items) != 1 {
				t.Fatalf("items: want 1 got %d", len(typed.Items))
			}
			row := typed.Items[0]
			if row.PlanSlug != plan {
				t.Fatalf("plan_slug = %q, want %q", row.PlanSlug, plan)
			}
			if row.Isolation != "vcluster" {
				t.Fatalf("plan %s: isolation = %q, want vcluster — the CR carries the observed vCluster block", plan, row.Isolation)
			}
			// #5501 — named with the CR-authoritative bare slug.
			if row.VClusterName != "acme" {
				t.Errorf("plan %s: vCluster-backed row keeps its vCluster name, got %q want acme", plan, row.VClusterName)
			}
			if row.Steps.VCluster != "done" {
				t.Errorf("plan %s: vCluster-backed Ready row keeps steps.vcluster=done, got %q", plan, row.Steps.VCluster)
			}
		})
	}
}

// TestOrgTenantRecordToResponse_StepOmission_TierMatrix pins the store-record
// mapper directly: explicit namespace blanks the step, explicit vcluster
// keeps it, and a LEGACY record with empty isolation keeps the full timeline
// (there is nothing to derive from — guessing either way would be the same
// fabrication class this fix removes).
func TestOrgTenantRecordToResponse_StepOmission_TierMatrix(t *testing.T) {
	t.Parallel()
	base := store.OrganizationProvisionRecord{
		OrganizationID: "tid-x",
		State:          store.STSDone,
		Subdomain:      "x",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	ns := base
	ns.Isolation = "namespace"
	if got := orgTenantRecordToResponse(ns).Steps.VCluster; got != "" {
		t.Errorf("isolation=namespace: steps.vcluster must be omitted, got %q", got)
	}

	vc := base
	vc.Isolation = "vcluster"
	if got := orgTenantRecordToResponse(vc).Steps.VCluster; got != "done" {
		t.Errorf("isolation=vcluster: steps.vcluster must stay done, got %q", got)
	}

	legacy := base // Isolation == "" (pre-#3378 record)
	if got := orgTenantRecordToResponse(legacy).Steps.VCluster; got != "done" {
		t.Errorf("legacy record (no isolation): timeline must be unchanged, got %q", got)
	}
}
