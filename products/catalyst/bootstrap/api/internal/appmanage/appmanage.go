// Package appmanage is what a caller may see and do with one Application
// after it is installed: the Organization-confined routes through which a
// customer reads, upgrades and uninstalls their OWN Application, the honest
// answer of a delete, and the card status of the Organization's Apps grid.
//
// Before it, an Organization session (console.<org>.<pool>) could install an
// Application (POST /api/v1/org/applications) and then neither read its
// status nor upgrade nor uninstall it: every other Application route lives
// under /api/v1/sovereigns/{id}/applications/..., which OrgScopeGuard refuses
// for an Organization session (org-scoped-forbidden). The console offered the
// buttons; the API answered 403.
package appmanage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// OwnOrg resolves the caller's own Organization namespace and the local
// Sovereign deployment id of the request. It writes the error answer itself
// (tenant not registered, forged sibling host: 403 org-scope-mismatch, …) and
// then returns ok=false.
type OwnOrg func(w http.ResponseWriter, r *http.Request) (namespace, deploymentID string, ok bool)

// Routes are the Sovereign Application handlers the Organization routes run:
// the same code, the same body, the same answer.
type Routes struct {
	Get, Status, Update, Delete, UpgradePreview, TopologyPreview http.HandlerFunc
}

// RegisterOrgRoutes registers the customer's routes for one Application of
// their own Organization. They sit under /api/v1/org/applications, which is
// already on the OrgScopeGuard allowlist, so no surface is widened.
func RegisterOrgRoutes(r chi.Router, own OwnOrg, h Routes) {
	r.Get("/api/v1/org/applications/{name}", Confine(own, h.Get))
	r.Get("/api/v1/org/applications/{name}/status", Confine(own, h.Status))
	r.Put("/api/v1/org/applications/{name}", Confine(own, h.Update))
	r.Delete("/api/v1/org/applications/{name}", Confine(own, h.Delete))
	r.Post("/api/v1/org/applications/{name}/upgrade/preview", Confine(own, h.UpgradePreview))
	r.Post("/api/v1/org/applications/{name}/topology/preview", Confine(own, h.TopologyPreview))
}

// Confine runs next, a Sovereign Application handler, for the caller's own
// Organization only. The namespace is FORCED to that Organization's
// namespace (a ?namespace= the caller sent is replaced), the deployment id is
// the local Sovereign's. Nothing outside that namespace is reachable.
//
// An Organization session (auth.ClaimsAreOrgScoped) is marked confined
// (auth.WithOrgConfined), which is what lets its mutation pass the handler's
// tier gate: own already bound that session to its own Organization, and
// that binding is the authorization, exactly as on the install route. Any
// other session is NOT marked: a Sovereign session that names an
// Organization with X-Tenant-Host meets the same tier gate as on
// /api/v1/sovereigns/{id}/applications/{name}, so a viewer, developer or
// operator cannot upgrade or uninstall an Organization's Application here
// either.
func Confine(own OwnOrg, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns, depID, ok := own(w, r)
		if !ok {
			return
		}
		ns = strings.TrimSpace(ns)
		if ns == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "org-namespace-unresolved",
				"detail": "tenant registry has no org_tenant_namespace for this Organization",
			})
			return
		}
		if strings.TrimSpace(depID) == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "deployment-unresolved",
				"detail": "could not resolve the local Sovereign deployment for an org-scoped request",
			})
			return
		}
		ctx := r.Context()
		if auth.ClaimsAreOrgScoped(auth.ClaimsFromContext(ctx)) {
			ctx = auth.WithOrgConfined(ctx)
		}
		confined := r.Clone(ctx)
		q := confined.URL.Query()
		q.Set("namespace", ns)
		confined.URL.RawQuery = q.Encode()
		if rctx := chi.RouteContext(confined.Context()); rctx != nil {
			rctx.URLParams.Add("id", depID)
		}
		next(w, confined)
	}
}

// DeleteAnswer is the answer of DELETE .../applications/{name}. Status is the
// canonical token programmatic consumers branch on (the TC-080 matrix asserts
// the "deleted" substring of the last two):
//
//	"uninstalling"     202, deleted:false — the object is still there, marked
//	                   for deletion, while the application-controller removes
//	                   it from every region; it releases the object last
//	"deleted"          200, deleted:true — the object is gone
//	"already-deleted"  200, deleted:true — it was gone before the call
//
// Kind/HTTPStatus/Deleted are the stable anchors of Fix #177 (qa-loop
// iter-17). The status is read back from the cluster after the delete, never
// assumed from the delete call being accepted.
type DeleteAnswer struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	HTTPStatus string `json:"httpStatus"`
	Deleted    bool   `json:"deleted"`
	// Phase and Finalizers describe the object still present after an
	// accepted delete ("uninstalling"); both are empty otherwise.
	Phase      string   `json:"phase,omitempty"`
	Finalizers []string `json:"finalizers,omitempty"`
	Message    string   `json:"message"`
}

// DeleteOutcome is the answer of an ACCEPTED delete of Application name in
// namespace ns, given the read-back of the object after it. The
// application-controller holds a finalizer on every Application it
// delivered, so the object normally stays, marked for deletion, until the
// controller has removed it from every region: that is "uninstalling". Only
// an object that is gone when it is read back is "deleted". A read-back that
// fails proves nothing either way and says so.
func DeleteOutcome(name, ns string, left *unstructured.Unstructured, readErr error) (int, DeleteAnswer) {
	a := DeleteAnswer{Kind: "Application", Name: name, Namespace: ns}
	switch {
	case readErr == nil && left != nil:
		a.Status, a.HTTPStatus = "uninstalling", "202"
		a.Phase, _, _ = unstructured.NestedString(left.Object, "status", "phase")
		a.Finalizers = left.GetFinalizers()
		a.Message = fmt.Sprintf("HTTP 202 Accepted — Application %q in namespace %q is uninstalling: the application-controller removes it from every region and releases the object last; read its status for progress", name, ns)
		return http.StatusAccepted, a
	case readErr != nil && !apierrors.IsNotFound(readErr):
		a.Status, a.HTTPStatus = "uninstalling", "202"
		a.Message = fmt.Sprintf("HTTP 202 Accepted — the delete of Application %q in namespace %q was accepted, but reading it back failed (%v); read its status for progress", name, ns, readErr)
		return http.StatusAccepted, a
	}
	a.Status, a.HTTPStatus, a.Deleted = "deleted", "200", true
	a.Message = fmt.Sprintf("HTTP 200 OK — Application %q deleted in namespace %q; status: deleted", name, ns)
	return http.StatusOK, a
}

// CardStatus maps an Application's status.phase onto the card status of the
// Organization's Apps grid. Only a phase on the way to Ready (Pending,
// Provisioning, or none yet) is "installing"; an Application the controller
// gave up on, that degraded (a plan-quota refusal, a missing chart) or that
// is being removed says so, with the reason and message in the row's
// objectStatus. Before, every phase other than Ready read "installing".
func CardStatus(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "ready":
		return "installed"
	case "failed":
		return "failed"
	case "degraded":
		return "degraded"
	case "uninstalling":
		return "uninstalling"
	default:
		return "installing"
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
