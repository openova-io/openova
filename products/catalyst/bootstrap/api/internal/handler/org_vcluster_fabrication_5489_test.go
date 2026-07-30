package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Tests for #5489 — the bootstrap API synthesized a vCluster for
// namespace-isolated Organizations on two seams:
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
// Anti-theater: each case is proven in BOTH directions — the namespace-tier
// assertions fail against the pre-fix code, and the vcluster-tier control
// assertions pin that the honest value still renders for Orgs that DO have
// a vCluster (a fix that blanked the field everywhere would satisfy the
// first half while breaking the directory for real vcluster-tier Orgs).

func TestVClusterNameFor(t *testing.T) {
	t.Parallel()
	if got := vclusterNameFor("vcluster", "acme"); got != "vc-acme" {
		t.Errorf("vcluster tier: got %q want vc-acme", got)
	}
	if got := vclusterNameFor("namespace", "acme"); got != "" {
		t.Errorf("namespace tier must not synthesize a vCluster name, got %q", got)
	}
	if got := vclusterNameFor("", "acme"); got != "" {
		t.Errorf("unknown isolation must not synthesize a vCluster name, got %q", got)
	}
}

// TestOrgResponseFromCR_NamespaceTier_NoVClusterFields — a namespace-tier CR
// (customer + plan s, the same shape orgReadyCR mints and the same tier live
// on hw291) must carry NO vcluster_name and NO steps.vcluster in the wire
// payload. The raw JSON is asserted (not just the struct) because the step
// omission rides the omitempty tag — a struct-only check could pass while
// the key still shipped.
func TestOrgResponseFromCR_NamespaceTier_NoVClusterFields(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("omantel", "Omantel", "", "admin@omantel.biz", "Ready"),
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
		t.Fatalf("fixture must derive namespace isolation (customer + plan s), got %q", row.Isolation)
	}
	if row.VClusterName != "" {
		t.Errorf("namespace-tier row must not name a vCluster, got vcluster_name=%q", row.VClusterName)
	}
	if row.Steps.VCluster != "" {
		t.Errorf("namespace-tier row must not carry a vCluster step, got steps.vcluster=%q", row.Steps.VCluster)
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
		t.Errorf("steps JSON must omit the vcluster key for a namespace-tier Org, got %v", steps)
	}
	if steps["bp_charts"] != "done" || steps["registry"] != "done" {
		t.Errorf("the rest of the Ready timeline must survive the omission, got %v", steps)
	}
}

// TestOrgResponseFromCR_VclusterTier_KeepsVClusterFields is the control
// direction: a vcluster-tier CR (customer + plan m) keeps the exact pre-fix
// shape — vc-<slug> name + a rendered vCluster step.
func TestOrgResponseFromCR_VclusterTier_KeepsVClusterFields(t *testing.T) {
	cr := orgReadyCR("acme", "ACME Corp", "", "ceo@acme.com", "Ready")
	if err := unstructured.SetNestedField(cr.Object, "m", "spec", "planSlug"); err != nil {
		t.Fatalf("set planSlug: %v", err)
	}
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
	if row.Isolation != "vcluster" {
		t.Fatalf("fixture must derive vcluster isolation (customer + plan m), got %q", row.Isolation)
	}
	if row.VClusterName != "vc-acme" {
		t.Errorf("vcluster-tier row keeps its vCluster name, got %q want vc-acme", row.VClusterName)
	}
	if row.Steps.VCluster != "done" {
		t.Errorf("vcluster-tier Ready row keeps steps.vcluster=done, got %q", row.Steps.VCluster)
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
