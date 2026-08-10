// org_tenant_org_cr_delete_denial_test.go — #5426 second half.
//
// The sibling file org_tenant_org_cr_delete_test.go proves the handler
// CALLS Delete. That is not the property that failed live on hw293: the
// call was made, the apiserver REFUSED it, and the handler returned 204
// anyway. Measured on hw293 2026-08-10:
//
//	DELETE /api/v1/organizations/{id}  ->  204 No Content
//	catalyst-api log: organizations.orgs.openova.io is forbidden:
//	  User "system:serviceaccount:catalyst:catalyst-api-cutover-driver"
//	  cannot delete resource "organizations" in API group
//	  "orgs.openova.io" at the cluster scope
//	Organization CR:  deletionTimestamp: null, finalizer still attached,
//	                  resourceVersion frozen
//	T+138s:           namespace + vcluster StatefulSet BACK under a new uid
//	                  (the surviving CR's Flux sources rebuilt them)
//
// A permission error reported as success is a lie the caller cannot
// detect — the console, a script and a User all read 204 as "deleted".
// That is what let the defect sit undiscovered. These guards assert the
// property the previous suite could not fail on: WHEN THE APISERVER
// DENIES THE DELETE, DOES THE CALLER LEARN.
//
// The fake dynamic client in the sibling harness has no RBAC, so its
// Delete can never be refused — these tests install a reactor that
// returns the real apierrors.NewForbidden the apiserver returns.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// forbidOrganizationDelete makes the fake apiserver refuse `delete
// organizations` exactly the way the live one did — same GroupResource,
// same verb, so err is apierrors.IsForbidden and the message names the
// resource and the ServiceAccount.
func forbidOrganizationDelete(dyn *dynamicfake.FakeDynamicClient) {
	dyn.PrependReactor("delete", "organizations", func(action k8stesting.Action) (bool, runtime.Object, error) {
		name := ""
		if da, ok := action.(k8stesting.DeleteAction); ok {
			name = da.GetName()
		}
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "orgs.openova.io", Resource: "organizations"},
			name,
			// Verbatim shape of the live denial (hw293, catalyst-api log).
			errRBACDeniedLiveShape,
		)
	})
}

var errRBACDeniedLiveShape = &forbiddenDetail{}

type forbiddenDetail struct{}

func (e *forbiddenDetail) Error() string {
	return `User "system:serviceaccount:catalyst:catalyst-api-cutover-driver" ` +
		`cannot delete resource "organizations" in API group "orgs.openova.io" ` +
		`at the cluster scope`
}

// TestDeleteOrganization_ApiserverDenialIsNotReportedAsSuccess is the
// decisive guard. A 403 from the apiserver MUST NOT become a 204.
func TestDeleteOrganization_ApiserverDenialIsNotReportedAsSuccess(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)
	rec := seedOrgRecordAndCR(t, h, dyn, "denyprobe")
	forbidOrganizationDelete(dyn)

	w := deleteOrgViaHandler(t, h, rec.OrganizationID)

	if w.Code == http.StatusNoContent {
		t.Fatalf("apiserver DENIED the Organization CR delete and the handler still answered "+
			"204 No Content — the caller is told the Organization was deleted when nothing was "+
			"deleted (#5426, measured live on hw293). body=%q", w.Body.String())
	}
	if w.Code < 500 || w.Code > 599 {
		t.Fatalf("want a 5xx on an upstream apiserver denial, got %d body=%q", w.Code, w.Body.String())
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 Bad Gateway (the upstream apiserver refused), got %d body=%q", w.Code, w.Body.String())
	}
}

// TestDeleteOrganization_DenialBodyNamesWhatWasDenied — the status code
// alone tells a script something failed; the body must tell an operator
// WHAT, or the next session repeats the hw293 investigation from zero.
func TestDeleteOrganization_DenialBodyNamesWhatWasDenied(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)
	rec := seedOrgRecordAndCR(t, h, dyn, "denybody")
	forbidOrganizationDelete(dyn)

	w := deleteOrgViaHandler(t, h, rec.OrganizationID)

	// map[string]any, not map[string]string: writeJSON also stamps a
	// NUMERIC `status` alongside the string fields.
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("denial response body is not JSON: %v (raw=%q)", err, w.Body.String())
	}
	str := func(k string) string {
		s, _ := body[k].(string)
		return s
	}
	if str("error") == "" {
		t.Fatalf("denial response carries no `error` key: %v", body)
	}
	// The detail must name the resource the apiserver refused, so an
	// operator can go straight to the ClusterRole.
	joined := strings.ToLower(str("error") + " " + str("detail"))
	if !strings.Contains(joined, "organization") {
		t.Fatalf("denial body never names the denied resource: %v", body)
	}
	if !strings.Contains(joined, "forbidden") && !strings.Contains(joined, "cannot delete") {
		t.Fatalf("denial body never says the delete was refused: %v", body)
	}
}

// TestDeleteOrganization_DenialLeavesRecordUndeleted — the store must not
// be stamped STSDeleted when the CR survives. A "deleted" audit row on a
// live Organization is the same lie one layer down: the console list drops
// the Org while its namespace, vCluster and Keycloak realm keep running.
func TestDeleteOrganization_DenialLeavesRecordUndeleted(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)
	rec := seedOrgRecordAndCR(t, h, dyn, "denystate")
	forbidOrganizationDelete(dyn)

	_ = deleteOrgViaHandler(t, h, rec.OrganizationID)

	got, ok := h.orgTenantDeps.Store.Get(rec.OrganizationID)
	if !ok {
		t.Fatalf("record vanished from the store after a FAILED delete")
	}
	if got.State == store.STSDeleted {
		t.Fatalf("record stamped STSDeleted while the Organization CR is still live — "+
			"the console will hide an Organization whose namespace/vCluster/realm keep running "+
			"(state=%s)", got.State)
	}

	// And the CR really is still there — the control on this assertion.
	if _, err := dyn.Resource(organizationGVR()).Get(context.Background(), "denystate", metav1.GetOptions{}); err != nil {
		t.Fatalf("precondition broken: the reactor should have BLOCKED the delete, CR get: %v", err)
	}
}

// TestDeleteOrganization_SuccessStillReturns204 is the control: with the
// apiserver allowing the delete (no reactor), the honest path is unchanged.
// Without this a "fix" that always 502s would pass the three guards above.
func TestDeleteOrganization_SuccessStillReturns204(t *testing.T) {
	h, dyn, _, _ := newOrgDeleteCascadeHarness(t)
	rec := seedOrgRecordAndCR(t, h, dyn, "allowprobe")

	w := deleteOrgViaHandler(t, h, rec.OrganizationID)

	if w.Code != http.StatusNoContent {
		t.Fatalf("permitted delete: want 204 got %d body=%q", w.Code, w.Body.String())
	}
	got, ok := h.orgTenantDeps.Store.Get(rec.OrganizationID)
	if !ok || got.State != store.STSDeleted {
		t.Fatalf("permitted delete must stamp STSDeleted, got ok=%v state=%v", ok, got.State)
	}
}
