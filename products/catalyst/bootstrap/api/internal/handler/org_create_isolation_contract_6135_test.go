package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// org_create_isolation_contract_6135_test.go — #6135 (UAT row G7), re-based on
// the every-plan boundary (founder direction 2026-09-10, Refs #4292 #4539).
//
// MEASURED on hw293.omantel.biz (dep a0077ba47e3720e5), while a plan-keyed
// tier gate still existed: the free-subdomain signup door returned HTTP 202
// acknowledging `isolation: vcluster`, and no vCluster was ever created. The
// signup form carries no plan picker, the plan normalised to `s`, and the gate
// that authored the backing put plan `s` on the host `<slug>` namespace. The
// door had let a declared `isolation` WIN over that gate in resolveOrgShape.
//
// #6135 made the declaration a CONSTRAINT ASSERTION adjudicated at the door
// (declaredIsolationConflict): accepted only when it agrees with the boundary
// actually authored, refused with 422 otherwise. That contract is unchanged.
// What changed underneath it is the boundary: every Organization on every
// plan is now backed by a dedicated vCluster (orgIsolation), so the exact hw293
// body — `vcluster` declared, no plan — is ACCEPTED, because the vCluster it
// asserts IS authored for plan `s`. The undeliverable declaration is now
// `namespace`, on every plan.
//
// WHAT THESE TESTS PIN. A declared `namespace` is refused with 422 at the door
// instead of accepted and silently substituted; every declaring body that says
// `vcluster` returns 202 echoing it, including the plan-less hw293 body; the
// marketplace funnel's own body, which declares nothing, is byte-unchanged and
// reports the vCluster it gets; and the resolver has exactly one producer for
// the boundary.

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
// the load-bearing case: a declared `namespace` on the plan-less signup body.
// No catalog plan delivers a host-namespace boundary, so the door must refuse
// it naming the conflict rather than mint an Organization whose label
// contradicts the vCluster the org-controller will author.
func TestCreateOrganization_DeclaredIsolationThePlanCannotDeliverIsRefused_6135(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)

	// No plan_slug — exactly what the signup door sends, since the form has no
	// plan picker. The handler normalises it to "s".
	code, body := postCreateOrgRaw(t, h, `{
		"subdomain":   "g7conflict",
		"admin_email": "admin@g7conflict.test",
		"domain_mode": "free-subdomain",
		"isolation":   "namespace"
	}`)

	if code == http.StatusAccepted {
		t.Fatalf("the door ACCEPTED a declared isolation it does not honour: 202 with isolation=%v. "+
			"Every Organization is backed by a dedicated vCluster, so this 202 promises a host "+
			"namespace that is never authored (#6135 class)", body["isolation"])
	}
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status: want 422 got %d body=%v", code, body)
	}
	if got, _ := body["error"].(string); got != "isolation-plan-conflict" {
		t.Errorf("error code = %q, want %q", got, "isolation-plan-conflict")
	}

	// Assert on the VALUE of the refusal, not that a `detail` key exists: an
	// empty or generic message leaves the caller in exactly the position the
	// silent downgrade left them. It must name what was asked, the plan the
	// create would have used, the boundary every plan delivers, and the way
	// out.
	detail, _ := body["detail"].(string)
	for _, want := range []string{"namespace", "\"s\"", "vcluster", "dedicated vCluster", "No catalog plan delivers", "Omit `isolation`"} {
		if !strings.Contains(detail, want) {
			t.Errorf("422 detail %q does not name %q — the caller cannot tell what they asked for, "+
				"what every plan gives, or how to proceed", detail, want)
		}
	}
	// It must NOT point at a plan that would deliver the request: none does,
	// and the list is computed from the constant rather than transcribed.
	if alt := plansDeliveringIsolation("namespace"); len(alt) != 0 {
		t.Errorf("plansDeliveringIsolation(\"namespace\") = %v, want none", alt)
	}
	if strings.Contains(detail, "Plans that deliver") {
		t.Errorf("422 detail %q steers the caller to a plan that delivers namespace; no plan does", detail)
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
// refusal above is a constraint on the UNDELIVERABLE value rather than a
// blanket rejection of the field. The first case is the exact hw293 body:
// `vcluster` declared, no plan. It is accepted now because the vCluster it
// asserts is authored for plan `s` like for every other plan.
func TestCreateOrganization_DeclaredIsolationThatAgreesIsAccepted_6135(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan string // "" = omit plan_slug, the signup door's body
	}{
		{"row G7: vcluster declared, no plan (the hw293 body)", ""},
		{"vcluster declared on plan s", "s"},
		{"vcluster declared on plan m", "m"},
		{"vcluster declared on plan flexi", "flexi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newOrgPipelineHandlerWithCRs(t)
			planLine := ""
			if tc.plan != "" {
				planLine = `"plan_slug": "` + tc.plan + `",`
			}
			code, body := postCreateOrgRaw(t, h, `{
				"subdomain":   "g7control",
				"admin_email": "admin@g7control.test",
				"domain_mode": "free-subdomain",
				`+planLine+`
				"isolation":   "vcluster"
			}`)
			if code != http.StatusAccepted {
				t.Fatalf("declared isolation vcluster AGREES with the boundary every plan delivers and must be "+
					"accepted (plan %q); got %d body=%v", tc.plan, code, body)
			}
			// The echoed value must equal what the org-controller will author —
			// that equality is the whole contract this row is about.
			if got, _ := body["isolation"].(string); got != orgIsolation {
				t.Errorf("accepted body isolation = %q, want %q", got, orgIsolation)
			}
			// And the vCluster it asserts is named, with the bare slug the
			// org-controller stamps (#5501) — a 202 that says vcluster and
			// names no vCluster is the hw293 shape again.
			if got, _ := body["vcluster_name"].(string); got != "g7control" {
				t.Errorf("accepted body vcluster_name = %q, want %q", got, "g7control")
			}
		})
	}
}

// TestCreateOrganization_FunnelBodyWithNoDeclarationIsUnchanged_6135 is the
// second CONTROL: the marketplace funnel declares no isolation at all, so the
// door must stay accepted and report the boundary it actually gets — the
// dedicated vCluster, named.
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
	if got, _ := body["isolation"].(string); got != orgIsolation {
		t.Errorf("isolation = %q, want %q — the funnel's plan resolves to \"s\" and every plan is "+
			"backed by a dedicated vCluster", got, orgIsolation)
	}
	// Every Organization authors a vCluster, so the payload names it with the
	// CR-authoritative bare slug (#5501). An omitted key here would be the
	// pre-change host-namespace shape leaking through.
	if got, _ := body["vcluster_name"].(string); got != "g7funnel" {
		t.Errorf("vcluster_name = %v, want %q — the response must name the vCluster it says the "+
			"Organization is backed by", body["vcluster_name"], "g7funnel")
	}
}

// TestResolveOrgShape_IsolationHasExactlyOneProducer_6135 is the VACUITY CHECK
// for the fix's central claim: the boundary label has exactly one producer —
// orgIsolation — and nothing else. It sweeps every catalog plan against every
// declarable isolation value, INCLUDING the ones that contradict the boundary,
// and requires the resolver to answer the constant every single time.
//
// It cannot pass on a stub: restore the old override branch and the
// contradicting half of the sweep goes red immediately.
func TestResolveOrgShape_IsolationHasExactlyOneProducer_6135(t *testing.T) {
	t.Parallel()
	declarations := []string{"", "namespace", "vcluster", "VCLUSTER", "  namespace  ", "bogus"}
	sawAgreeing, sawContradicting := false, false

	for _, plan := range catalogPlanSlugs {
		for _, decl := range declarations {
			got := resolveOrgShape(orgTenantCreateRequest{
				Kind: "customer", PlanSlug: plan, Isolation: decl,
			})
			if got.Isolation != orgIsolation {
				t.Errorf("resolveOrgShape(plan=%q, isolation=%q).Isolation = %q, want %q — "+
					"a second input steered the boundary label away from the constant",
					plan, decl, got.Isolation, orgIsolation)
			}
			switch strings.ToLower(strings.TrimSpace(decl)) {
			case "":
			case orgIsolation:
				sawAgreeing = true
			default:
				sawContradicting = true
			}
		}
	}

	// Vacuity: the sweep is only meaningful if it actually exercised both a
	// declaration that matches the boundary and one that contradicts it.
	if !sawAgreeing {
		t.Fatal("sweep never exercised a declaration that AGREES with the boundary — the guard proves nothing")
	}
	if !sawContradicting {
		t.Fatal("sweep never exercised a declaration that CONTRADICTS the boundary — that is the whole defect")
	}
}
