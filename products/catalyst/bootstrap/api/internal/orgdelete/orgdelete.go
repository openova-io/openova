// Package orgdelete answers DELETE /api/v1/organizations/{id} for an id of
// which the console's provisioning store holds no record: an Organization the
// storefront created.
//
// The storefront's Organizations are created by the provisioning service
// (core/services/provisioning, createOrganizationCR). It writes only the
// Organization CR, named after the slug and labelled openova.io/tenant-id
// with the storefront's tenant id, and the console lists the Organization
// under that id (handler.orgCRToResponse). The route looked the id up in the
// store only, so it answered 404 for every one of them and never deleted the
// CR. Nothing else deletes it: the storefront's own delete ends at the
// provisioning service, which removes the applications and leaves the CR to
// the organization-controller (#4290). The CR's finalizers are what remove
// the Organization's Flux objects in every region and, through them, its
// namespace in every region; so every Organization the storefront created
// stayed, with its namespaces, in both regions (gate check BSS-G01 on hw307,
// 26 and 27 November 2026: "DELETE /api/v1/organizations/<id> answered 404").
//
// Unrecorded finds the CRs that the id names and deletes each through the CR
// delete of the recorded path (handler.deleteOrgOrganizationCR), so this
// package makes no cluster write of its own.
package orgdelete

import (
	"context"
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TenantIDLabel carries the tenant id on an Organization CR; both creators of
// the CR set it (the provisioning service and handler.ensureOrganizationCR).
const TenantIDLabel = "openova.io/tenant-id"

// scopeLabel is "platform" on the Sovereign's own Organization
// (handler.ensureSpineOrganization), which has no tenant id.
const scopeLabel = "openova.io/scope"

// organizations is the resource of the Organization CR.
var organizations = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}

// Names returns, sorted, the names of the Organization CRs in list that id
// names: each CR whose TenantIDLabel equals id, and the CR whose name equals
// id in lower case (a caller that addresses the Organization by its slug, as
// the GET of the same path accepts). Only a CR that a tenant door created is
// ever named: one without a tenant id, or labelled openova.io/scope: platform
// (the Sovereign's own Organization, whose name passes the slug check), is
// never named, by label or by name.
func Names(list []unstructured.Unstructured, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	var names []string
	for i := range list {
		name, labels := list[i].GetName(), list[i].GetLabels()
		tenantID := strings.TrimSpace(labels[TenantIDLabel])
		if tenantID == "" || labels[scopeLabel] == "platform" {
			continue
		}
		if tenantID == id || name == strings.ToLower(id) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Refused is the body of the 502 that the route answers when the apiserver
// refuses the delete of an Organization CR.
func Refused(err error) map[string]string {
	return map[string]string{
		"error": "organization-cr-delete-failed",
		// Verbatim apiserver text: on an RBAC denial it names the
		// ServiceAccount, the verb and the resource, which is the diagnosis.
		"detail": detail(err),
		"hint": "the Organization CR survives, so the org-controller finalizer cascade never ran " +
			"and the namespace / vCluster / Keycloak realm / per-Org Flux sources are still live; " +
			"this DELETE is idempotent — re-issue it once the apiserver permits the call",
	}
}

// detail is the apiserver's text of err, cut at 512 bytes.
func detail(err error) string {
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// Unrecorded answers the route for an id the store has no record of.
//
//   - client gives no in-cluster dynamic client (CI, out of cluster): 404
//     org-tenant-not-found, as the GET of the same path answers; there is no
//     apiserver that could hold a CR.
//   - the apiserver refuses the list of Organization CRs: 502
//     organization-cr-read-failed; a list that could not be read is never
//     answered as an Organization that does not exist.
//   - no CR is named by id: 404 org-tenant-not-found.
//   - del refuses the delete of a named CR: 502 with the body of Refused.
//   - del deleted every named CR (or found it gone): 204. The CR's finalizers
//     then remove the Organization in every region.
//
// del is the CR delete of the recorded path; it is given a record that
// carries the id and the CR name as subdomain. write writes a JSON body.
func Unrecorded(ctx context.Context, w http.ResponseWriter, id string,
	client func() (dynamic.Interface, error),
	del func(context.Context, store.OrganizationProvisionRecord) error,
	write func(http.ResponseWriter, int, any)) {
	notFound := map[string]string{"error": "org-tenant-not-found"}
	dyn, err := client()
	if err != nil || dyn == nil {
		write(w, http.StatusNotFound, notFound)
		return
	}
	list, err := dyn.Resource(organizations).List(ctx, metav1.ListOptions{})
	if err != nil {
		write(w, http.StatusBadGateway, map[string]string{
			"error":  "organization-cr-read-failed",
			"detail": detail(err),
		})
		return
	}
	names := Names(list.Items, id)
	if len(names) == 0 {
		write(w, http.StatusNotFound, notFound)
		return
	}
	for _, name := range names {
		if err := del(ctx, store.OrganizationProvisionRecord{OrganizationID: id, Subdomain: name}); err != nil {
			write(w, http.StatusBadGateway, Refused(err))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
