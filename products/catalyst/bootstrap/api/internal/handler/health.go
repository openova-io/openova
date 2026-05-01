package handler

import (
	"encoding/json"
	"net/http"
)

// healthResponse — wire shape of /healthz. The legacy plain-text "ok"
// path is preserved for backward compatibility with curl-based
// liveness probes — clients that send `Accept: application/json`
// receive the structured body, every other request receives the
// 9-byte "ok\n".
//
// The structured body lets an operator see at a glance:
//   - Are at least Pod + Deployment informers synced on the primary
//     cluster? (the Ready bool)
//   - Per-cluster, per-kind sync map.
//   - Registered Sovereigns (so a missing kubeconfig file shows up
//     here as an empty list).
type healthResponse struct {
	Ready     bool                     `json:"ready"`
	Sovereigns []string                `json:"sovereigns"`
	Synced    map[string]map[string]bool `json:"synced"`
}

// Health handles GET /healthz.
//
// Per the issue spec the handler returns 200 once at least Pod +
// Deployment informers for the primary cluster are synced. "Primary
// cluster" is the lexically-first registered Sovereign id; with no
// clusters registered (cold catalyst-api) the handler returns 200 +
// Ready=true (the data-plane is not the only thing this Pod does;
// the wizard surfaces continue to work).
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		// k8scache disabled (e.g. test environment without
		// kubeconfigs). Preserve the legacy plain-text contract.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
		return
	}

	clusters := h.k8sCache.Clusters()
	synced := h.k8sCache.Synced()

	ready := true
	if len(clusters) > 0 {
		// Primary == lexically-first registered Sovereign.
		primary := clusters[0]
		s := synced[primary]
		// Per spec — Pod + Deployment informers must be synced.
		ready = s["pod"] && s["deployment"]
	}

	if r.Header.Get("Accept") != "application/json" {
		// Plain-text path — kept identical to the original
		// implementation so the existing readinessProbe contract is
		// preserved. Always 200 unless the data-plane is
		// catastrophically wedged.
		w.Header().Set("Content-Type", "text/plain")
		if ready {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok")) //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("syncing")) //nolint:errcheck
		}
		return
	}

	resp := healthResponse{
		Ready:      ready,
		Sovereigns: clusters,
		Synced:     synced,
	}
	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}
