// install_refs_api.go — ADR-0015 D4: the console's install-dialog option
// lists, read from the objects a create request must name.
//
//	GET /api/v1/refs/organizations                     → Organization CRs
//	GET /api/v1/refs/organizations/{org}/environments  → that Org's Environment CRs
//	GET /api/v1/refs/regions                           → the region registry
//
// Every value the dialog sends back is a value from one of these lists,
// verbatim, so a dialog submit can only ever name an existing object. An
// Org-scoped session (per-Org console) sees only its own Organization.
package handler

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/regions"
)

type refOrganization struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Namespace   string `json:"namespace"`
	Ready       bool   `json:"ready"`
	// Kind — Organization.spec.kind (customer / internal …), verbatim.
	Kind string `json:"kind"`
	// Platform — the Sovereign's own control-plane Organization (label
	// openova.io/scope=platform). The console never pre-selects it.
	Platform bool `json:"platform"`
}

type refEnvironment struct {
	Name            string `json:"name"`
	EnvType         string `json:"envType"`
	OrganizationRef string `json:"organizationRef"`
}

// HandleListRefOrganizations — GET /api/v1/refs/organizations.
func (h *Handler) HandleListRefOrganizations(w http.ResponseWriter, r *http.Request) {
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "cluster-unavailable", "message": err.Error()})
		return
	}
	list, err := client.Resource(organizationGVR()).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "reference-lookup-failed", "message": err.Error()})
		return
	}
	ownOrg, scoped := h.orgScopeForRequest(r)
	out := make([]refOrganization, 0, len(list.Items))
	for i := range list.Items {
		o := &list.Items[i]
		if scoped && !strings.EqualFold(o.GetName(), ownOrg) {
			continue
		}
		display, _, _ := unstructured.NestedString(o.Object, "spec", "displayName")
		kind, _, _ := unstructured.NestedString(o.Object, "spec", "kind")
		out = append(out, refOrganization{
			Name:        o.GetName(),
			DisplayName: display,
			Namespace:   o.GetName(),
			Ready:       refConditionTrue(o, "Ready"),
			Kind:        kind,
			Platform:    o.GetLabels()["openova.io/scope"] == "platform",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"organizations": out})
}

// HandleListRefEnvironments — GET /api/v1/refs/organizations/{org}/environments.
func (h *Handler) HandleListRefEnvironments(w http.ResponseWriter, r *http.Request) {
	org := strings.TrimSpace(chi.URLParam(r, "org"))
	if ownOrg, scoped := h.orgScopeForRequest(r); scoped && !strings.EqualFold(org, ownOrg) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden-cross-org",
			"message": "an Organization-scoped session may list only its own Environments"})
		return
	}
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "cluster-unavailable", "message": err.Error()})
		return
	}
	if _, err := client.Resource(organizationGVR()).Get(r.Context(), org, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"code": "organization-not-found", "field": "org",
				"message": "Organization " + org + " does not exist on this Sovereign"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "reference-lookup-failed", "message": err.Error()})
		return
	}
	list, err := client.Resource(EnvironmentGVR()).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "reference-lookup-failed", "message": err.Error()})
		return
	}
	out := make([]refEnvironment, 0)
	for i := range list.Items {
		e := &list.Items[i]
		ref, _, _ := unstructured.NestedString(e.Object, "spec", "organizationRef")
		if ref != org {
			continue
		}
		envType, _, _ := unstructured.NestedString(e.Object, "spec", "envType")
		out = append(out, refEnvironment{Name: e.GetName(), EnvType: envType, OrganizationRef: ref})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"environments": out})
}

// HandleListRefRegions — GET /api/v1/refs/regions.
func (h *Handler) HandleListRefRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"regions": regions.Registry()})
}

// refConditionTrue reports status.conditions[type=t].status == "True".
func refConditionTrue(o *unstructured.Unstructured, t string) bool {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == t {
			return m["status"] == "True"
		}
	}
	return false
}
