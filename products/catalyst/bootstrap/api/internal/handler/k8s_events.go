// k8s_events.go — events-for-resource handler (G89 #2636, 2026-05-31).
//
// Route:
//
//	GET /api/v1/sovereigns/{id}/k8s/events-for/{kind}/{ns}/{name}
//	    ?limit=100
//
// Returns JSON `{"events":[...], "count":N, "fieldSelector":"..."}`.
//
// Why bypass k8scache?
//
// Per TBD-V50 #2125: events are unbounded; caching them via informers
// explodes memory. EventsPanel previously read from the SSE snapshot
// of `event` kind which was NEVER registered, so the panel rendered
// empty across every Deployment / Pod / Service detail page.
//
// This handler hits the apiserver directly with a tightly-scoped
// FieldSelector on `regarding.kind|name|namespace` (events.k8s.io/v1
// supports the indexed FieldSelector path for sub-100ms List on
// thousand-event clusters). Limit defaults to 100; cap at 500.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const evtsApiserverTimeout = 8 * time.Second

// kindToK8sKindCase maps the canonical lowercase kind name used in
// catalyst URLs to the PascalCase K8s Kind value Events' `regarding.kind`
// field carries.
var kindToK8sKindCase = map[string]string{
	"pod":                     "Pod",
	"replicaset":              "ReplicaSet",
	"deployment":              "Deployment",
	"statefulset":             "StatefulSet",
	"daemonset":               "DaemonSet",
	"job":                     "Job",
	"cronjob":                 "CronJob",
	"service":                 "Service",
	"ingress":                 "Ingress",
	"gateway":                 "Gateway",
	"httproute":               "HTTPRoute",
	"endpoints":               "Endpoints",
	"endpointslice":           "EndpointSlice",
	"configmap":               "ConfigMap",
	"secret":                  "Secret",
	"persistentvolume":        "PersistentVolume",
	"persistentvolumeclaim":   "PersistentVolumeClaim",
	"namespace":               "Namespace",
	"node":                    "Node",
	"serviceaccount":          "ServiceAccount",
	"horizontalpodautoscaler": "HorizontalPodAutoscaler",
	"verticalpodautoscaler":   "VerticalPodAutoscaler",
	"poddisruptionbudget":     "PodDisruptionBudget",
	"helmrelease":             "HelmRelease",
	"helmrepository":          "HelmRepository",
	"helmchart":               "HelmChart",
	"gitrepository":           "GitRepository",
	"ocirepository":           "OCIRepository",
	"kustomization":           "Kustomization",
	"certificate":             "Certificate",
	"certificaterequest":      "CertificateRequest",
	"cluster":                 "Cluster",
	"continuum":               "Continuum",
	"cnpgpair":                "CnpgPair",
	"application":             "Application",
}

// HandleK8sEventsForResource lists Events keyed to a focused resource.
// Bypasses k8scache (per TBD-V50) — direct apiserver List with indexed
// FieldSelector. Falls back to core/v1 Events if events.k8s.io returns
// nothing (kubelet still emits to core/v1 in many K8s versions).
func (h *Handler) HandleK8sEventsForResource(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "k8scache-disabled",
		})
		return
	}
	clusterID := chi.URLParam(r, "id")
	kindLower := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	ns := chi.URLParam(r, "ns")
	name := chi.URLParam(r, "name")
	if clusterID == "" || kindLower == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-path-params",
			"detail": "id, kind, ns, name are all required",
		})
		return
	}
	// `_` is the chi convention for cluster-scoped resources (matches
	// resource.api.ts:nsSegment encoding).
	if ns == "_" {
		ns = ""
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	limit := int64(100)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			limit = n
			if limit > 500 {
				limit = 500
			}
		}
	}

	core := h.k8sCache.CoreClient(clusterID)
	if core == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "sovereign-not-registered",
			"detail": fmt.Sprintf("sovereign %q not registered with k8sCache", clusterID),
		})
		return
	}

	kindCase, ok := kindToK8sKindCase[kindLower]
	if !ok && kindLower != "" {
		kindCase = strings.ToUpper(kindLower[:1]) + kindLower[1:]
	}

	listNS := ns
	if listNS == "" {
		listNS = metav1.NamespaceAll
	}

	ctx, cancel := context.WithTimeout(r.Context(), evtsApiserverTimeout)
	defer cancel()

	all := listEventsForResource(ctx, core, listNS, kindCase, name, ns, limit)
	sort.SliceStable(all, func(i, j int) bool {
		ti, _ := all[i]["eventTime"].(string)
		tj, _ := all[j]["eventTime"].(string)
		return ti > tj
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"events": all,
		"count":  len(all),
		"kind":   kindCase,
		"name":   name,
		"ns":     ns,
	})
}

// listEventsForResource queries BOTH events.k8s.io/v1 + core/v1
// (different operators emit to different APIs). Returns the union as
// normalized wire shapes ready for EventsPanel.tsx.
func listEventsForResource(
	ctx context.Context,
	core kubernetes.Interface,
	listNS, kindCase, name, ns string,
	limit int64,
) []map[string]any {
	out := []map[string]any{}

	// events.k8s.io/v1 path
	parts := []string{
		fmt.Sprintf("regarding.kind=%s", kindCase),
		fmt.Sprintf("regarding.name=%s", name),
	}
	if ns != "" {
		parts = append(parts, fmt.Sprintf("regarding.namespace=%s", ns))
	}
	evts, err := core.EventsV1().Events(listNS).List(ctx, metav1.ListOptions{
		FieldSelector: strings.Join(parts, ","),
		Limit:         limit,
	})
	if err == nil && evts != nil {
		for i := range evts.Items {
			out = append(out, normalizeEventsV1(&evts.Items[i]))
		}
	} else if err != nil && !apierrors.IsForbidden(err) && !apierrors.IsNotFound(err) {
		// Swallow — fall through to core/v1.
		_ = err
	}

	// core/v1 path (legacy, still widely used by kubelet+scheduler)
	coreParts := []string{
		fmt.Sprintf("involvedObject.kind=%s", kindCase),
		fmt.Sprintf("involvedObject.name=%s", name),
	}
	if ns != "" {
		coreParts = append(coreParts, fmt.Sprintf("involvedObject.namespace=%s", ns))
	}
	coreEvts, err := core.CoreV1().Events(listNS).List(ctx, metav1.ListOptions{
		FieldSelector: strings.Join(coreParts, ","),
		Limit:         limit,
	})
	if err == nil && coreEvts != nil {
		for i := range coreEvts.Items {
			out = append(out, normalizeCoreV1Event(&coreEvts.Items[i]))
		}
	}

	// De-dup by UID (events.k8s.io and core/v1 mirror the same event
	// in some K8s versions). Preserves the first occurrence (events.
	// k8s.io path) since it ran first.
	seen := map[string]struct{}{}
	dedup := make([]map[string]any, 0, len(out))
	for _, e := range out {
		uid, _ := e["uid"].(string)
		if uid == "" {
			dedup = append(dedup, e)
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		dedup = append(dedup, e)
	}
	return dedup
}

func normalizeEventsV1(e *eventsv1.Event) map[string]any {
	count := int32(1)
	if e.Series != nil && e.Series.Count > 0 {
		count = e.Series.Count
	}
	ts := ""
	if !e.EventTime.IsZero() {
		ts = e.EventTime.UTC().Format("2006-01-02T15:04:05.000Z")
	} else if !e.ObjectMeta.CreationTimestamp.IsZero() {
		ts = e.ObjectMeta.CreationTimestamp.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"uid": string(e.ObjectMeta.UID),
		"regarding": map[string]string{
			"kind":      e.Regarding.Kind,
			"name":      e.Regarding.Name,
			"namespace": e.Regarding.Namespace,
		},
		"type":      e.Type,
		"reason":    e.Reason,
		"note":      e.Note,
		"eventTime": ts,
		"series":    map[string]int32{"count": count},
	}
}

func normalizeCoreV1Event(e *corev1.Event) map[string]any {
	count := e.Count
	if count <= 0 {
		count = 1
	}
	ts := ""
	if !e.LastTimestamp.IsZero() {
		ts = e.LastTimestamp.UTC().Format(time.RFC3339)
	} else if !e.FirstTimestamp.IsZero() {
		ts = e.FirstTimestamp.UTC().Format(time.RFC3339)
	} else if !e.ObjectMeta.CreationTimestamp.IsZero() {
		ts = e.ObjectMeta.CreationTimestamp.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"uid": string(e.ObjectMeta.UID),
		"regarding": map[string]string{
			"kind":      e.InvolvedObject.Kind,
			"name":      e.InvolvedObject.Name,
			"namespace": e.InvolvedObject.Namespace,
		},
		"type":      e.Type,
		"reason":    e.Reason,
		"note":      e.Message,
		"eventTime": ts,
		"series":    map[string]int32{"count": count},
	}
}
