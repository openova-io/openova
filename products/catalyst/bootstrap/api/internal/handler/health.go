package handler

import (
	"encoding/json"
	"net/http"
)

// healthResponse — wire shape of /readyz (and the JSON path of
// /healthz, kept for backward compatibility). The legacy plain-text
// "ok" path is preserved for curl-based probes — clients that send
// `Accept: application/json` receive the structured body, every other
// request receives the 2-byte "ok".
//
// The structured body lets an operator see at a glance:
//   - Are at least Pod + Deployment informers synced on the primary
//     cluster? (the Ready bool)
//   - Per-cluster, per-kind sync map.
//   - Registered Sovereigns (so a missing kubeconfig file shows up
//     here as an empty list).
type healthResponse struct {
	Ready      bool                       `json:"ready"`
	Sovereigns []string                   `json:"sovereigns"`
	Synced     map[string]map[string]bool `json:"synced"`
}

// Health handles GET /healthz — liveness.
//
// Liveness MUST return 200 whenever the catalyst-api process is up
// and the HTTP server is serving. It MUST NOT depend on any
// per-Sovereign state (kubeconfig presence, informer sync, store
// hydration, etc.) — those are normal parts of the provisioning
// lifecycle, NOT signs that the process has crashed.
//
// The previous implementation gated /healthz on informer sync of the
// "primary" Sovereign's Pod + Deployment informers. Once a deployment
// was registered (via POST /api/v1/deployments) but BEFORE cloud-init
// PUT the kubeconfig back, the registered cluster had no kubeconfig
// on disk — informers could not start, sync was false forever,
// /healthz returned 503, kubelet killed the Pod on the failed
// liveness probe, the new Pod restored the deployment from PVC and
// re-entered the same state. This crashloop made the Service have no
// ready endpoints, which made nginx return 502 to cloud-init's
// kubeconfig PUT, which prevented the kubeconfig from ever arriving.
// Vicious cycle. See issue #530 for the full incident.
//
// The fix: /healthz is liveness now — always 200 if we can answer the
// request. The strict informer-sync check moved to /readyz (used by
// the readinessProbe). Failing readiness pulls the Pod out of the
// Service rotation but does NOT restart it; that is exactly the
// behaviour we want during provisioning.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Ready handles GET /readyz — readiness.
//
// Returns 200 whenever the catalyst-api process can serve traffic,
// EVEN IF some registered Sovereign's informers have not yet synced.
// Per-Sovereign sync state is surfaced in the JSON body for operator
// observability (Accept: application/json) but is NOT a gating
// condition on the HTTP status code.
//
// Why /readyz is NOT strict on per-Sovereign sync (issue #530):
//
// catalyst-api is single-replica with strategy: Recreate. The
// readinessProbe gates Service-endpoint membership. If a stale
// kubeconfig on the PVC points at a destroyed Sovereign cluster, its
// informer can NEVER sync (apiserver unreachable), so a strict
// /readyz would return 503 forever. nginx then can't route ANY
// traffic to catalyst-api — including the cloud-init kubeconfig PUT
// from a fresh deployment — locking the wizard in a permanent dead
// state with no operator path out.
//
// The right model is: liveness = process is alive (kubelet uses to
// kill/restart), readiness = process can serve traffic at all (Service
// uses to gate endpoint membership). NEITHER probe is the right place
// for "all my watch subscriptions are healthy" — that's a per-request
// concern owned by the K8s data-plane handlers (`HandleK8sList`,
// `HandleK8sStream`), which already return 503 when the queried
// Sovereign's Indexer is not synced. The wizard renders a "Catching
// up…" state when it sees those 503s; it does NOT need the Pod
// removed from the Service.
//
// What `Ready: false` in the JSON body means: at least one registered
// Sovereign's informers haven't completed their initial LIST. This is
// useful telemetry, not a kill condition.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		// k8scache disabled (e.g. test environment without
		// kubeconfigs). Always ready; nothing to wait for.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	clusters := h.k8sCache.Clusters()
	synced := h.k8sCache.Synced()

	// "ready" here is informational only — every registered Sovereign
	// has at least Pod + Deployment informers synced. Drives the JSON
	// `ready` field; does NOT drive the HTTP status code.
	ready := true
	for _, c := range clusters {
		s := synced[c]
		if !s["pod"] || !s["deployment"] {
			ready = false
			break
		}
	}

	if r.Header.Get("Accept") != "application/json" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	resp := healthResponse{
		Ready:      ready,
		Sovereigns: clusters,
		Synced:     synced,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
