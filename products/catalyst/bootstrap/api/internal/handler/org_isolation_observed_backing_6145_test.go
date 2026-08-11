package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// org_isolation_observed_backing_6145_test.go — #6145 (UAT row 101).
//
// MEASURED on hw293.omantel.biz (dep a0077ba47e3720e5), 2026-08-11:
//
//	Organization  plan  real backing                     console said
//	g7freea       s     NO vcluster StatefulSet;         Isolation: Vcluster
//	                    bp-keycloak/bp-agenity run in
//	                    the host `g7freea` namespace
//	hw293walkone  m     statefulset/vcluster 1/1         Isolation: Vcluster
//
// Two Organizations with materially different backings reported the SAME
// isolation. That is the row-101 assertion failing: the field is not derived
// from the backing.
//
// MECHANISM, at the line. The persisted provision record on the Sovereign
// carried the DECLARED value, not a derived one:
//
//	/var/lib/catalyst/deployments/org-tenant/84f0cb06-….json
//	  "subdomain": "g7freea", "plan_slug": "s", "isolation": "vcluster"
//
// organization_provisioning.go orgTenantRecordToResponse copies that field
// verbatim (`Isolation: rec.Isolation`), and mergeOrgResponses gives the local
// store row priority over the CR-derived row on a slug/id collision — so the
// declared value wins over the tier-derived one the CR read path already
// computes correctly. The console was NOT defaulting: `isolation` was present
// on the wire with the value "vcluster".
//
// WHY OBSERVED AND NOT DECLARED. `isolation` renders on the Organization
// identity card as a statement about infrastructure. The only component that
// AUTHORS the boundary is the org-controller, and it records what it authored:
// `status.vcluster{name,phase}` is stamped only for a vCluster-backed Org
// (#5489 vclusterStatusFor). A read path that republishes a declared field is
// reporting an intention as a fact. So the read path now prefers the
// controller's own observation and falls back to the tier gate only while the
// Organization has not been reconciled yet.
//
// WHAT THESE TESTS PIN.
//   - RED case: a store record that declares `vcluster` beside plan `s`, with a
//     reconciled CR that authored no vCluster, must report `namespace`.
//   - CONTROL: an Organization with the SAME declared `vcluster` and a CR that
//     really did author one must still report `vcluster` — the fix is not
//     "everything is namespace".
//   - VACUITY: the observation helper must be able to return each value, and
//     must not answer from key PRESENCE (the live g7freea CR carries
//     `status.vcluster: {}` — an empty block).

// orgCRWithVCluster builds an Organization CR whose status carries the block
// the org-controller stamps ONLY for a vCluster-backed Org.
func orgCRWithVCluster(t *testing.T, slug, planSlug string) *unstructured.Unstructured {
	t.Helper()
	cr := orgReadyCR(slug, slug, "omani.homes", "owner@"+slug+".test", "")
	spec, _, _ := unstructured.NestedMap(cr.Object, "spec")
	spec["planSlug"] = planSlug
	cr.Object["spec"] = spec
	cr.Object["status"] = map[string]any{
		"observedGeneration": int64(2),
		"vcluster": map[string]any{
			"name":        slug,
			"hostCluster": "hw293.omantel.biz",
			"phase":       "Ready",
		},
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Reconciled"},
		},
	}
	return cr
}

// orgCRHostNamespace builds an Organization CR shaped EXACTLY like the walked
// g7freea: reconciled (observedGeneration 2, Ready=True) with an EMPTY
// status.vcluster block, which is how the org-controller records "no vCluster
// was authored for this Organization".
func orgCRHostNamespace(t *testing.T, slug, planSlug string) *unstructured.Unstructured {
	t.Helper()
	cr := orgReadyCR(slug, slug, "omani.homes", "owner@"+slug+".test", "")
	spec, _, _ := unstructured.NestedMap(cr.Object, "spec")
	spec["planSlug"] = planSlug
	cr.Object["spec"] = spec
	cr.Object["status"] = map[string]any{
		"observedGeneration": int64(2),
		// The live CR carries the key with an empty value — see the header.
		"vcluster": map[string]any{},
		"conditions": []any{
			map[string]any{
				"type": "Ready", "status": "True", "reason": "Reconciled",
				"message": "host namespace Active + Keycloak group + Gitea Org reconciled (namespace-isolated tier — no vCluster authored)",
			},
		},
	}
	return cr
}

// seedDeclaredRecord writes the provision record the BSS/free-subdomain door
// persisted on hw293: plan `s`, isolation DECLARED as `vcluster`.
func seedDeclaredRecord(t *testing.T, st *store.OrganizationProvisionStore, id, slug, planSlug, declared string) {
	t.Helper()
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  id,
		State:           store.STSTenantRegistered,
		Subdomain:       slug,
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		ParentDomain:    "omani.homes",
		AdminEmail:      "owner@" + slug + ".test",
		CompanyName:     slug,
		OTECHFQDN:       "otech.example",
		VClusterName:    slug,
		TenantNamespace: slug,
		Kind:            "customer",
		Tier:            "org",
		BillingMode:     "real",
		Isolation:       declared,
		PlanSlug:        planSlug,
	}
	if err := st.Save(&rec); err != nil {
		t.Fatalf("seed record %s: %v", slug, err)
	}
}

// listOrgsBySlug drives GET /api/v1/organizations and indexes the rows.
func listOrgsBySlug(t *testing.T, h *Handler) map[string]orgTenantResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []orgTenantResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	out := make(map[string]orgTenantResponse, len(got.Items))
	for _, it := range got.Items {
		out[it.Subdomain] = it
	}
	return out
}

// TestOrgDirectory_IsolationComesFromObservedBacking_6145 is the row-101
// measurement, both arms in ONE directory read so the two rows are produced by
// the same code on the same request — which is what makes "they reported the
// same value" a defect rather than a coincidence.
func TestOrgDirectory_IsolationComesFromObservedBacking_6145(t *testing.T) {
	h, st := newOrgHandlerWithSeededCRs(t,
		orgCRHostNamespace(t, "g7freea", "s"),
		orgCRWithVCluster(t, "hw293walkone", "m"),
	)
	// BOTH records declare `vcluster` — that is the suspect property the
	// control shares. On hw293 both rows rendered `Isolation: Vcluster`.
	seedDeclaredRecord(t, st, "84f0cb06-1e9b-43a6-8c4e-1c7a0540779d", "g7freea", "s", "vcluster")
	seedDeclaredRecord(t, st, "tid-hw293walkone", "hw293walkone", "m", "vcluster")

	rows := listOrgsBySlug(t, h)

	free, ok := rows["g7freea"]
	if !ok {
		t.Fatalf("g7freea missing from the directory: %+v", rows)
	}
	walk, ok := rows["hw293walkone"]
	if !ok {
		t.Fatalf("hw293walkone missing from the directory: %+v", rows)
	}

	// RED before the fix: the declared value survives the read path.
	if free.Isolation != "namespace" {
		t.Errorf("g7freea isolation = %q, want %q — the org-controller authored NO vCluster for it "+
			"(status.vcluster empty, Ready message \"no vCluster authored\"), and on hw293 the host "+
			"namespace really did run bp-keycloak/bp-agenity directly. Reporting %q republishes the "+
			"DECLARED field from the provision record as though it were a fact about the cluster (#6145)",
			free.Isolation, "namespace", free.Isolation)
	}
	// CONTROL: shares the suspect property (declared `vcluster`, same door,
	// same mapper) and must stay green.
	if walk.Isolation != "vcluster" {
		t.Errorf("hw293walkone isolation = %q, want %q — this Organization HAS a vCluster "+
			"(status.vcluster.name/phase stamped, statefulset/vcluster 1/1 on hw293). A fix that "+
			"answers \"namespace\" here has over-corrected into reporting every Org as namespace",
			walk.Isolation, "vcluster")
	}
	// The two must DIFFER — that difference is the whole row.
	if free.Isolation == walk.Isolation {
		t.Errorf("both Organizations report isolation=%q despite materially different backings; "+
			"the field is still not derived from the backing", free.Isolation)
	}

	// A namespace-backed Org must not carry a vcluster_name either: the name
	// would assert an object nobody authored (#5489/#5501 contract).
	if free.VClusterName != "" {
		t.Errorf("g7freea vcluster_name = %q, want empty — no vCluster exists to name", free.VClusterName)
	}
	if walk.VClusterName != "hw293walkone" {
		t.Errorf("hw293walkone vcluster_name = %q, want %q", walk.VClusterName, "hw293walkone")
	}
	// The vcluster timeline step is a claim about the same object.
	if free.Steps.VCluster != "" {
		t.Errorf("g7freea steps.vcluster = %q, want empty — a host-namespace Org has no vCluster step",
			free.Steps.VCluster)
	}
}

// TestOrgDetail_IsolationComesFromObservedBacking_6145 pins the SAME contract
// on GET /api/v1/organizations/{id}, the slug/UUID-addressed detail read that
// openova-MCP and the console share. A fix applied only to the list leaves the
// identical lie on the second surface.
func TestOrgDetail_IsolationComesFromObservedBacking_6145(t *testing.T) {
	h, st := newOrgHandlerWithSeededCRs(t, orgCRHostNamespace(t, "g7freea", "s"))
	seedDeclaredRecord(t, st, "84f0cb06-1e9b-43a6-8c4e-1c7a0540779d", "g7freea", "s", "vcluster")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/84f0cb06-1e9b-43a6-8c4e-1c7a0540779d", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "84f0cb06-1e9b-43a6-8c4e-1c7a0540779d")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleGetOrganization(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Isolation != "namespace" {
		t.Errorf("org-detail isolation = %q, want %q — the store record declares vcluster, the "+
			"reconciled CR authored none, and the detail read republished the declaration",
			got.Isolation, "namespace")
	}
	if got.VClusterName != "" {
		t.Errorf("org-detail vcluster_name = %q, want empty", got.VClusterName)
	}
}

// TestObservedIsolationFromCR_VacuityAndValueNotKey_6145 is the VACUITY CHECK
// for the observation helper: it must be able to answer each of the three
// outcomes, and it must read the VALUE of status.vcluster rather than the
// presence of the key. The live g7freea CR carries `status.vcluster: {}`, so a
// helper that tested key presence would report "vcluster" for the very
// Organization this row is about — and every assertion above would pass while
// measuring nothing.
func TestObservedIsolationFromCR_VacuityAndValueNotKey_6145(t *testing.T) {
	t.Parallel()

	statusOf := func(status map[string]any) *unstructured.Unstructured {
		obj := map[string]any{
			"apiVersion": "orgs.openova.io/v1",
			"kind":       "Organization",
			"metadata":   map[string]any{"name": "probe"},
			"spec":       map[string]any{"slug": "probe", "planSlug": "s"},
		}
		if status != nil {
			obj["status"] = status
		}
		return &unstructured.Unstructured{Object: obj}
	}

	cases := []struct {
		name   string
		status map[string]any
		want   string
	}{
		{
			// The exact live shape. Key PRESENT, value EMPTY.
			name: "reconciled with an empty status.vcluster block (the live g7freea shape)",
			status: map[string]any{
				"observedGeneration": int64(2),
				"vcluster":           map[string]any{},
				"conditions":         []any{map[string]any{"type": "Ready", "status": "True"}},
			},
			want: "namespace",
		},
		{
			name: "reconciled with no vcluster key at all",
			status: map[string]any{
				"observedGeneration": int64(1),
				"conditions":         []any{map[string]any{"type": "Ready", "status": "True"}},
			},
			want: "namespace",
		},
		{
			name: "vCluster authored and Ready",
			status: map[string]any{
				"observedGeneration": int64(2),
				"vcluster":           map[string]any{"name": "walkone", "phase": "Ready"},
			},
			want: "vcluster",
		},
		{
			// Mid-provision: the block exists with a phase before the vCluster
			// is up. Still POSITIVE evidence that one was authored.
			name: "vCluster authored, still Pending",
			status: map[string]any{
				"observedGeneration": int64(1),
				"vcluster":           map[string]any{"name": "walkone", "phase": "Pending"},
			},
			want: "vcluster",
		},
		{
			// The over-correction guard: an unreconciled CR must NOT read as
			// namespace. It looks identical to a namespace-backed one, and
			// answering "namespace" here would report every freshly created
			// M-plan Organization as host-namespace-backed.
			name:   "no status at all — unobserved",
			status: nil,
			want:   "",
		},
		{
			name:   "status present but the controller has not reconciled it",
			status: map[string]any{"conditions": []any{}},
			want:   "",
		},
	}

	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := observedIsolationFromCR(statusOf(tc.status)); got != tc.want {
				t.Fatalf("observedIsolationFromCR() = %q, want %q", got, tc.want)
			}
		})
		seen[tc.want] = true
	}

	// Vacuity: the table above is only a guard if it actually exercised all
	// three outcomes. A helper hardwired to any single answer fails here.
	for _, want := range []string{"namespace", "vcluster", ""} {
		if !seen[want] {
			t.Fatalf("the table never exercised outcome %q — the guard cannot distinguish a working "+
				"helper from one that returns a constant", want)
		}
	}
}
