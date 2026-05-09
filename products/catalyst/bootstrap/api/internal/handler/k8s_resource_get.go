// Package handler — k8s_resource_get.go: EPIC-4 Slice R1 (#1099).
//
// Single-resource GET against the Sovereign cluster's apiserver, going
// through the k8scache.Factory's per-cluster dynamic client (NEVER
// poking the apiserver from a fresh REST config — see ADR-0001 §5).
//
// REST surface added by this slice:
//
//	GET /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}      — namespaced resource
//	GET /api/v1/sovereigns/{id}/k8s/{kind}/_/{name}         — cluster-scoped (ns="_")
//
// Response body: the unstructured K8s object (redacted via the
// k8scache.Factory's redactor for Sensitive kinds — Secret/ConfigMap
// data fields are stripped).
//
// Architecture rules:
//
//   - ADR-0001 §5: read live CRs via the per-cluster dynamic client owned
//     by k8scache.Factory; never invent a parallel kubeclient pool.
//   - INVIOLABLE-PRINCIPLES.md #4 (never hardcode): the kind catalogue is
//     the live registry, not a switch statement.
//   - INVIOLABLE-PRINCIPLES.md #5 (least privilege): GET is gated by the
//     existing SAR cache so a viewer-tier user only sees resources they
//     have `get` rights on. The slice R6 actions handler enforces a
//     stricter tier-admin gate on writes.
package handler

import (
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HandleK8sResourceGet — GET
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}
//
// `ns` may be the literal `_` for cluster-scoped resources (so chi's
// path-segment matcher doesn't choke on an empty segment). Returns the
// live unstructured CR or 404. Sensitive kinds (Secret, ConfigMap) have
// `.data` / `.stringData` scrubbed by the k8scache redactor before the
// payload leaves catalyst-api.
func (h *Handler) HandleK8sResourceGet(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}

	// Authorization: SAR gate (same shape as HandleK8sList). Per-resource
	// `get` permission keeps the surface in lockstep with the list view.
	user := h.k8sUser(r)
	if user != "" && h.sarCache != nil {
		if !h.sarCache.Allowed(r.Context(), h.k8sCache, user, clusterID, kindName, ns, "get") {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": fmt.Sprintf("user %q lacks get on %s in namespace %q", user, kindName, ns),
			})
			return
		}
	}

	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}
	if kind.Namespaced && ns == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "namespace-required",
			"detail": fmt.Sprintf("kind %q is namespace-scoped; ns must be supplied", kindName),
		})
		return
	}

	var fetched *unstructured.Unstructured
	var fetchErr error
	if kind.Namespaced {
		fetched, fetchErr = dyn.Resource(kind.GVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	} else {
		fetched, fetchErr = dyn.Resource(kind.GVR).Get(r.Context(), name, metav1.GetOptions{})
	}
	if fetchErr != nil {
		if apierrors.IsNotFound(fetchErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "resource-not-found",
				"detail": fmt.Sprintf("%s %q not found", kindName, qualifiedName(ns, name)),
			})
			return
		}
		if apierrors.IsForbidden(fetchErr) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "apiserver-forbidden",
				"detail": fetchErr.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "resource-get-failed",
			"detail": fetchErr.Error(),
		})
		return
	}
	// Apply redactor so Secret/ConfigMap data never leaves the process.
	redacted := h.k8sCache.RedactForKind(kind, fetched)
	writeJSON(w, http.StatusOK, redacted)
}

// qualifiedName joins the namespace and name in a printable form.
// Cluster-scoped resources render as bare names; namespaced ones render
// `<ns>/<name>`.
func qualifiedName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}
