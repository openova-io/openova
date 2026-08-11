package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// org_create_isolation_contract_6135_test.go — #6135 (UAT row G7).
//
// MEASURED on hw293.omantel.biz (dep a0077ba47e3720e5): the free-subdomain
// signup door returned HTTP 202 acknowledging `isolation: vcluster`, and no
// vCluster was ever created. The Organization was backed by the host `<slug>`
// namespace.
//
// MECHANISM, at the line. `resolveOrgShape` let a valid explicit `isolation`
// win over the tier gate:
//
//	isolation := strings.ToLower(strings.TrimSpace(req.Isolation))
//	switch isolation {
//	case "namespace", "vcluster":   // request value WINS
//	default:
//	        isolation = isolationForTier(planSlug)
//	}
//
// while the plan normalised to "s" a few lines above (the signup form carries
// no plan picker) and the gate that AUTHORS the backing —
// core/controllers/organization/internal/gitops/manifests.go
// `boundaryIsVcluster(planSlug)` — takes the plan and nothing else. No non-test
// reader of the accepted `isolation` exists in the provisioning gitops
// renderer, the provisioning consumer, or any core/controllers reconciler. So
// the field could only ever agree with the plan or lie about it.
//
// The control on the same Sovereign was `hw293walkone`: same declared
// isolation, plan `m`, a real vCluster 1/1. The variable was the plan.
//
// WHAT THESE TESTS PIN. The undeliverable combination is refused with 422 at
// the door instead of accepted and silently substituted; every other declaring
// body — the ones that AGREE with their plan — still returns 202 with the
// declared value; and the marketplace funnel's own body, which declares
// nothing, is byte-unchanged.

// postCreateOrgRaw drives the create door and returns the recorder plus the
// decoded generic body. Unlike postCreateOrg it does NOT assume the success
// envelope, so an error response is readable field-by-field.
func postCreateOrgRaw(t *testing.T, h *Handler, body string) (int, map[string]any) {
	t.Helper()
	w, _ := postCreateOrg(t, h, body)
	var generic map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode create response: %v body=%s", err, w.Body.String())
	}
	return w.Code, generic
}

// TestCreateOrganization_DeclaredIsolationThePlanCannotDeliverIsRefused_6135 is
// the load-bearing case: the exact hw293 body. Pre-fix it returned 202 with
// `"isolation":"vcluster"`; post-fix it returns 422 naming the conflict.
func TestCreateOrganization_DeclaredIsolationThePlanCannotDeliverIsRefused_6135(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)

	// No plan_slug — exactly what the signup door sends, since the form has no
	// plan picker. The handler normalises it to "s", the one tier the gate
	// backs with a host namespace.
	code, body := postCreateOrgRaw(t, h, `{
		"subdomain":   "g7conflict",
		"admin_email": "admin@g7conflict.test",
		"domain_mode": "free-subdomain",
		"isolation":   "vcluster"
	}`)

	if code == http.StatusAccepted {
		t.Fatalf("the door ACCEPTED a declared isolation it does not honour: 202 with isolation=%v. "+
			"The org-controller keys the boundary off planSlug alone (BoundaryIsVcluster(\"s\") == false), "+
			"so this 202 promises a vCluster that is never authored (#6135, hw293 dep a0077ba47e3720e5)",
			body["isolation"])
	}
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status: want 422 got %d body=%v", code, body)
	}
	if got, _ := body["error"].(string); got != "isolation-plan-conflict" {
		t.Errorf("error code = %q, want %q", got, "isolation-plan-conflict")
	}

	// Assert on the VALUE of the refusal, not that a `detail` key exists: an
	// empty or generic message leaves the caller in exactly the position the
	// silent downgrade left them.
	detail, _ := body["detail"].(string)
	for _, want := range []string{"vcluster", "\"s\"", "namespace", "Omit `isolation`"} {
		if !strings.Contains(detail, want) {
			t.Errorf("422 detail %q does not name %q — the caller cannot tell what they asked for, "+
				"what the plan gives, or how to get what they wanted", detail, want)
		}
	}
	// It must also point at a plan that WOULD deliver the request, computed
	// from the gate rather than transcribed.
	for _, p := range plansDeliveringIsolation("vcluster") {
		if !strings.Contains(detail, p) {
			t.Errorf("422 detail %q omits plan %q, which the tier gate does back with a vCluster", detail, p)
		}
	}

	// Nothing may have been persisted: a refusal that still mints the record is
	// the same divergence with a different status code.
	if _, ok := body["org_tenant_id"]; ok {
		t.Errorf("refused create still returned an Organization id (%v) — the record was minted anyway", body["org_tenant_id"])
	}
}

// TestCreateOrganization_DeclaredIsolationThatAgreesIsAccepted_6135 is the
// CONTROL. It shares the suspect property — a non-empty declared `isolation`
// on the same door, through the same adjudicator — and stays green, so the
// diff above is a new constraint on the UNDELIVERABLE combination rather than
// a blanket refusal of the field.
func TestCreateOrganization_DeclaredIsolationThatAgreesIsAccepted_6135(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan string
		want string
	}{
		{"vcluster declared on plan m", "m", "vcluster"},
		{"vcluster declared on plan flexi", "flexi", "vcluster"},
		{"namespace declared on plan s", "s", "namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newOrgPipelineHandlerWithCRs(t)
			code, body := postCreateOrgRaw(t, h, `{
				"subdomain":   "g7control",
				"admin_email": "admin@g7control.test",
				"domain_mode": "free-subdomain",
				"plan_slug":   "`+tc.plan+`",
				"isolation":   "`+tc.want+`"
			}`)
			if code != http.StatusAccepted {
				t.Fatalf("declared isolation %q AGREES with plan %q and must still be accepted; got %d body=%v",
					tc.want, tc.plan, code, body)
			}
			if got, _ := body["isolation"].(string); got != tc.want {
				t.Errorf("accepted body isolation = %q, want %q", got, tc.want)
			}
			// The echoed value must equal what the authoring gate will do —
			// that equality is the whole contract this row is about.
			if got, _ := body["isolation"].(string); got != isolationForTier(tc.plan) {
				t.Errorf("echoed isolation %q diverges from the tier gate's answer %q for plan %q",
					got, isolationForTier(tc.plan), tc.plan)
			}
		})
	}
}

// TestCreateOrganization_FunnelBodyWithNoDeclarationIsUnchanged_6135 is the
// second CONTROL: the marketplace funnel declares no isolation at all, so the
// door must behave exactly as before — 202, with the value DERIVED from the
// resolved plan.
func TestCreateOrganization_FunnelBodyWithNoDeclarationIsUnchanged_6135(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)

	code, body := postCreateOrgRaw(t, h, `{
		"subdomain":    "g7funnel",
		"admin_email":  "admin@g7funnel.test",
		"company_name": "G7 Funnel",
		"domain_mode":  "free-subdomain"
	}`)

	if code != http.StatusAccepted {
		t.Fatalf("the funnel body declares no isolation and must stay accepted; got %d body=%v", code, body)
	}
	if got, _ := body["isolation"].(string); got != "namespace" {
		t.Errorf("derived isolation = %q, want %q — the funnel's plan resolves to \"s\" and the gate "+
			"backs that with the host namespace", got, "namespace")
	}
	// A host-namespace Organization authors no vCluster, so the payload must
	// carry no vcluster_name at all (#5501 omitempty contract). An empty string
	// still reads as "this Org has a vCluster field".
	if _, present := body["vcluster_name"]; present {
		t.Errorf("namespace-backed Organization carries vcluster_name=%v — the response still "+
			"advertises a boundary that was never authored", body["vcluster_name"])
	}
}

// TestResolveOrgShape_IsolationHasExactlyOneProducer_6135 is the VACUITY CHECK
// for the fix's central claim: the boundary label is produced by the tier gate
// and by nothing else. It sweeps every catalog plan against every declarable
// isolation value, INCLUDING the ones that contradict the plan, and requires
// the resolver to answer isolationForTier(plan) every single time.
//
// It cannot pass on a stub: restore the old override branch and the
// contradicting half of the sweep goes red immediately, and a resolver that
// returned a constant fails the other half.
func TestResolveOrgShape_IsolationHasExactlyOneProducer_6135(t *testing.T) {
	t.Parallel()
	declarations := []string{"", "namespace", "vcluster", "VCLUSTER", "  namespace  ", "bogus"}
	sawAgreeing, sawContradicting := false, false

	for _, plan := range catalogPlanSlugs {
		want := isolationForTier(plan)
		for _, decl := range declarations {
			got := resolveOrgShape(orgTenantCreateRequest{
				Kind: "customer", PlanSlug: plan, Isolation: decl,
			})
			if got.Isolation != want {
				t.Errorf("resolveOrgShape(plan=%q, isolation=%q).Isolation = %q, want %q — "+
					"a second input steered the boundary label away from the tier gate",
					plan, decl, got.Isolation, want)
			}
			switch strings.ToLower(strings.TrimSpace(decl)) {
			case "":
			case want:
				sawAgreeing = true
			default:
				sawContradicting = true
			}
		}
	}

	// Vacuity: the sweep is only meaningful if it actually exercised both a
	// declaration that matches the plan and one that contradicts it.
	if !sawAgreeing {
		t.Fatal("sweep never exercised a declaration that AGREES with its plan — the guard proves nothing")
	}
	if !sawContradicting {
		t.Fatal("sweep never exercised a declaration that CONTRADICTS its plan — that is the whole defect")
	}
}
