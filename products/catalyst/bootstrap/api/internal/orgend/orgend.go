// Package orgend lets the platform's own DELETE /api/v1/organizations/{id}
// end an Organization the marketplace door created.
//
// The marketplace door creates the Organization CR in the provisioning service
// (core/services/provisioning/handlers/organization_create.go, label
// openova.io/source=tenant-created-event); catalyst-api never holds a provision
// record for it. The store handler (internal/handler HandleDeleteOrganization)
// looks only in that record store, so for such an Organization it answered
// 404 org-tenant-not-found and removed nothing (measured on the two-region
// environment on 22 November 2026, for both cancelled test Organizations).
// Deleting the CR is the one trigger of the organization-controller's removal
// in every region and of the chargeback customer's close, so that is what this
// route does for it. Every other id goes to the store handler unchanged.
//
// SELECTOR, so it never deletes another Organization: a CR labelled
// openova.io/source=tenant-created-event AND openova.io/tenant-id=<id> (the id
// the directory lists as org_tenant_id), else the CR named <id> (the slug)
// that carries the same source label. A CR of the sovereign-admin door
// (source org-tenant-funnel, which has a store record) is never matched. Two
// CRs with one tenant id answer 409 and nothing is deleted. The delete carries
// a UID precondition, so a CR re-created between the read and the delete is
// refused by the apiserver.
//
// SAFE TO RUN TWICE: a CR already marked for deletion (its removal is
// running) answers 204 and is not deleted again; once the CR is gone the id
// names nothing and the store handler answers 404.
package orgend

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	sourceLabel       = "openova.io/source"
	marketplaceSource = "tenant-created-event"
	tenantIDLabel     = "openova.io/tenant-id"
)

// OrganizationGVR is the cluster-scoped Organization kind.
var OrganizationGVR = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}

// errAmbiguous: more than one marketplace CR carries the tenant id.
var errAmbiguous = errors.New("more than one Organization carries this tenant id")

// Client returns the dynamic client of the cluster catalyst-api runs in. An
// error or a nil client means there is none (CI, out of cluster): every
// request then goes to the store handler.
type Client func() (dynamic.Interface, error)

// InCluster is the Client of a catalyst-api Pod.
func InCluster() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// Delete wraps the store handler of DELETE /api/v1/organizations/{id}: an id
// that names a marketplace Organization ends that Organization by deleting its
// CR; any other id is served by next.
func Delete(next http.HandlerFunc, client Client, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		dyn, err := client()
		if id == "" || err != nil || dyn == nil {
			next(w, r)
			return
		}
		org, err := find(r.Context(), dyn, id)
		switch {
		case errors.Is(err, errAmbiguous):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "organization-ambiguous", "detail": err.Error()})
			return
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "organization-cr-read-failed", "detail": truncate(err.Error())})
			return
		case org == nil:
			next(w, r)
			return
		}
		status, body := end(r.Context(), dyn, org)
		if status != http.StatusNoContent {
			log.Error("orgend: the Organization was NOT ended; it stays in every region and stays metered until this DELETE succeeds",
				"organization", org.GetName(), "tenant_id", org.GetLabels()[tenantIDLabel], "status", status, "detail", body["detail"])
			writeJSON(w, status, body)
			return
		}
		log.Info("orgend: Organization CR deleted; the organization-controller removes it in every region and chargeback closes its customer",
			"organization", org.GetName(), "tenant_id", org.GetLabels()[tenantIDLabel])
		w.WriteHeader(http.StatusNoContent)
	}
}

// find returns the marketplace Organization CR id names, nil when it names
// none.
func find(ctx context.Context, dyn dynamic.Interface, id string) (*unstructured.Unstructured, error) {
	res := dyn.Resource(OrganizationGVR)
	if len(validation.IsValidLabelValue(id)) == 0 {
		sel := labels.Set{sourceLabel: marketplaceSource, tenantIDLabel: id}.AsSelector().String()
		list, err := res.List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil // no Organization kind on this cluster
			}
			return nil, err
		}
		switch n := len(list.Items); {
		case n == 1:
			return &list.Items[0], nil
		case n > 1:
			return nil, errAmbiguous
		}
	}
	name := strings.ToLower(id)
	if len(validation.IsDNS1123Label(name)) != 0 {
		return nil, nil
	}
	obj, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if obj.GetLabels()[sourceLabel] != marketplaceSource {
		return nil, nil
	}
	return obj, nil
}

// end deletes the CR, with a UID precondition. It answers 204 when the CR is
// marked for deletion or gone.
func end(ctx context.Context, dyn dynamic.Interface, org *unstructured.Unstructured) (int, map[string]string) {
	if org.GetDeletionTimestamp() != nil {
		return http.StatusNoContent, nil
	}
	opts := metav1.DeleteOptions{}
	if uid := org.GetUID(); uid != "" {
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	err := dyn.Resource(OrganizationGVR).Delete(ctx, org.GetName(), opts)
	switch {
	case err == nil, apierrors.IsNotFound(err):
		return http.StatusNoContent, nil
	case apierrors.IsConflict(err):
		return http.StatusConflict, map[string]string{"error": "organization-changed",
			"detail": "the Organization was re-created while it was being deleted; nothing was deleted: " + truncate(err.Error())}
	}
	return http.StatusBadGateway, map[string]string{
		"error":  "organization-cr-delete-failed",
		"detail": truncate(err.Error()),
		"hint": "the Organization CR survives, so its removal never started and it is still live and metered; " +
			"this DELETE is idempotent: re-issue it once the apiserver permits the call",
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func truncate(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
