package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openova-io/openova/core/services/shared/respond"
)

// BackingServiceStatus is the per-service view returned to callers. Fields
// reflect the actual manifests the installer writes — do not invent values.
type BackingServiceStatus struct {
	Slug          string `json:"slug"`            // "postgres" | "mysql" | "redis"
	PodStatus     string `json:"pod_status"`      // "Running" | "Pending" | "Failed" | "unknown" | "not_found"
	ReadyReplicas int    `json:"ready_replicas"`  // count of pods in Ready state
	TotalReplicas int    `json:"total_replicas"`  // count of pods seen
	Image         string `json:"image,omitempty"` // container image reference (e.g. "postgres:16-alpine")
}

// GetTenantBackingServices returns live pod status for the backing services
// (postgres/mysql/redis) that run inside a tenant's vCluster. vCluster syncs
// pods up to the host namespace `tenant-<slug>` with a name pattern of
// `<pod>-x-apps-x-vcluster`, so a simple kube-API list on that ns is enough —
// no vCluster auth needed.
//
// Querystring:
//
//	slug     — tenant subdomain (required; resolves to `tenant-<slug>`)
//	services — comma-separated list of service slugs to report on (required).
//	           Unknown slugs are skipped; callers typically pass the set they
//	           already know is installed per the catalog.
//
// Response:
//
//	200 { services: [ { slug, pod_status, ready_replicas, total_replicas, image } ] }
//	400 missing slug/services
//
// This endpoint has no persistent state — the tenant service proxies it so
// the per-tenant "Backing services" view can show runtime status without
// giving the tenant service its own kube-API credentials.
func (h *Handler) GetTenantBackingServices(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	servicesQ := strings.TrimSpace(r.URL.Query().Get("services"))
	if slug == "" {
		respond.Error(w, http.StatusBadRequest, "slug is required")
		return
	}
	if servicesQ == "" {
		respond.Error(w, http.StatusBadRequest, "services is required")
		return
	}

	wanted := make(map[string]bool)
	for _, s := range strings.Split(servicesQ, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		wanted[s] = true
	}
	if len(wanted) == 0 {
		respond.Error(w, http.StatusBadRequest, "services is empty")
		return
	}

	// #4290: org-controller-owned `<slug>` host namespace (single boundary);
	// the vCluster syncs backing-service pods up into it.
	hostNS := slug
	body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", hostNS))
	if err != nil {
		// Namespace-gone / tenant never provisioned / kube unreachable — be
		// explicit but don't 500: the UI can render "unknown" rows.
		out := buildUnknownStatuses(wanted)
		respond.OK(w, map[string]any{"services": out})
		return
	}

	var podList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					Image string `json:"image"`
				} `json:"containerStatuses"`
			} `json:"status"`
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &podList); err != nil {
		out := buildUnknownStatuses(wanted)
		respond.OK(w, map[string]any{"services": out})
		return
	}

	// Aggregate pods per service slug. vCluster syncs inner pods up to the host
	// namespace as `<pod>-x-<inner-namespace>-x-vcluster`.
	//
	// The inner namespace is NOT a constant. This used to hardcode
	// `-x-apps-x-vcluster`, but since #4290 the inner namespace is the Org slug,
	// so real pods look like `umami-7c4df67dc6-vdwb7-x-theta-corp-x-vcluster`.
	// Verified live on hw290: ZERO pods across the whole cluster carried the
	// hardcoded `-x-apps-x-vcluster` form, so this matched nothing and every
	// service reported "not_found" — a silent, total miss that looked like an
	// answer.
	type agg struct {
		phase string
		ready int
		total int
		image string
	}
	out := map[string]*agg{}
	for name := range wanted {
		out[name] = &agg{}
	}

	for _, pod := range podList.Items {
		podName, ok := vclusterInnerPodName(pod.Metadata.Name)
		if !ok {
			continue
		}
		// Match against the requested slugs directly rather than splitting on
		// the first `-`. Slugs contain dashes: splitting "uptime-kuma-8c8...-x-"
		// on the first dash yields "uptime", which matches no requested slug.
		prefix := matchWantedSlug(podName, wanted)
		if prefix == "" {
			continue
		}
		a, ok := out[prefix]
		if !ok {
			continue
		}
		a.total++
		// Phase precedence: Failed > Pending > Running. If any pod is Failed
		// the service is failed; any Pending downgrades a Running service.
		switch pod.Status.Phase {
		case "Failed":
			a.phase = "Failed"
		case "Pending":
			if a.phase != "Failed" {
				a.phase = "Pending"
			}
		case "Running":
			if a.phase == "" {
				a.phase = "Running"
			}
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				a.ready++
				break
			}
		}
		if a.image == "" {
			// containerStatuses has the running image (resolved digest);
			// fall back to spec.containers if status isn't populated yet.
			if len(pod.Status.ContainerStatuses) > 0 {
				a.image = pod.Status.ContainerStatuses[0].Image
			} else if len(pod.Spec.Containers) > 0 {
				a.image = pod.Spec.Containers[0].Image
			}
		}
	}

	services := make([]BackingServiceStatus, 0, len(out))
	for slug, a := range out {
		status := a.phase
		if a.total == 0 {
			status = "not_found"
		}
		services = append(services, BackingServiceStatus{
			Slug:          slug,
			PodStatus:     status,
			ReadyReplicas: a.ready,
			TotalReplicas: a.total,
			Image:         a.image,
		})
	}
	respond.OK(w, map[string]any{"services": services})
}

// vclusterInnerPodName recovers the inner pod name from a vCluster-synced host
// pod name of the form `<pod>-x-<inner-namespace>-x-vcluster`, for ANY inner
// namespace. Returns false for pods that are not vCluster-synced.
func vclusterInnerPodName(hostName string) (string, bool) {
	const tail = "-x-vcluster"
	if !strings.HasSuffix(hostName, tail) {
		return "", false
	}
	core := strings.TrimSuffix(hostName, tail)
	// What remains is `<pod>-x-<inner-namespace>`. The inner namespace cannot
	// itself contain `-x-`, so the LAST occurrence is the true separator even
	// when the pod name contains one.
	i := strings.LastIndex(core, "-x-")
	if i <= 0 {
		return "", false
	}
	return core[:i], true
}

// matchWantedSlug returns the requested slug that owns this pod, or "" if none
// does. A pod belongs to a slug when its name is that slug or begins with
// `<slug>-` (the replica-set/hash tail that Deployments append).
//
// The LONGEST match wins, so a slug like "uptime" can never shadow
// "uptime-kuma" when both are requested.
func matchWantedSlug(podName string, wanted map[string]bool) string {
	best := ""
	for slug := range wanted {
		if podName != slug && !strings.HasPrefix(podName, slug+"-") {
			continue
		}
		if len(slug) > len(best) {
			best = slug
		}
	}
	return best
}

func buildUnknownStatuses(wanted map[string]bool) []BackingServiceStatus {
	out := make([]BackingServiceStatus, 0, len(wanted))
	for s := range wanted {
		out = append(out, BackingServiceStatus{Slug: s, PodStatus: "unknown"})
	}
	return out
}
