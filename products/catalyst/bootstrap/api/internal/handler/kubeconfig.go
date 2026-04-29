// GET /api/v1/deployments/{id}/kubeconfig — returns the new
// Sovereign cluster's k3s kubeconfig as application/yaml.
//
// Producer of the bytes: Phase 0 OpenTofu finishes, an out-of-band
// fetch reads /etc/rancher/k3s/k3s.yaml from the control-plane node
// (rewriting the server URL from 127.0.0.1 to the load-balancer's
// public IP) and writes it onto Deployment.Result.Kubeconfig before
// runProvisioning persists the deployment. The HelmRelease watch
// loop reads from the same field at Phase-1 start.
//
// Consumers:
//
//   - The wizard's StepSuccess.tsx "Download kubeconfig" button hits
//     /api/v1/deployments/<id>/kubeconfig and triggers a browser
//     download named `<sovereignFQDN>-kubeconfig.yaml`.
//   - Operators running `kubectl --kubeconfig=$(curl .../kubeconfig)`
//     ad-hoc against a fresh Sovereign.
//   - The Sovereign Admin shell uses the same endpoint to give the
//     operator a one-click download for break-glass access.
//
// Failure modes:
//
//   - 404 — deployment not found
//   - 409 — deployment exists but no kubeconfig has been captured
//     yet (Phase 0 still in flight, or Phase 0 failed before the
//     fetch step). Body is a JSON envelope so the wizard can
//     surface the not-implemented / not-yet states distinctly.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): the
// kubeconfig contains a long-lived k3s service-account token until
// Phase 2 swaps it for a SPIFFE-issued identity. The endpoint is
// intentionally NOT authenticated at the catalyst-api edge — it
// inherits whatever auth the franchise console attaches. The
// Sovereign Admin shell sits behind the franchise SSO; ad-hoc
// curl-from-the-command-line MUST go through the same SSO. The
// catalyst-api never logs the kubeconfig bytes; it only logs the
// deployment id and the byte length.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// GetKubeconfig — GET /api/v1/deployments/{id}/kubeconfig.
//
// Returns the kubeconfig as application/yaml. Per the not-yet-
// implemented contract that the wizard's StepSuccess.tsx already
// handles, an absent kubeconfig yields HTTP 409 with body
// {"error": "not-implemented"} so the UI can render the SSH-fetch
// runbook fallback rather than a generic error.
func (h *Handler) GetKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	dep.mu.Lock()
	var kubeconfig string
	if dep.Result != nil {
		kubeconfig = dep.Result.Kubeconfig
	}
	dep.mu.Unlock()

	if kubeconfig == "" {
		// 409 keeps the wizard's existing "not-implemented" branch
		// working unchanged (StepSuccess.test.tsx asserts a 409
		// triggers the SSH-fetch runbook fallback card).
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-implemented",
			"detail": "kubeconfig has not been captured for this deployment yet. Operator can fetch it via SSH per docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH; programmatic capture lands in a follow-on ticket.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+dep.Request.SovereignFQDN+`-kubeconfig.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(kubeconfig))

	h.log.Info("kubeconfig served",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"bytes", len(kubeconfig),
	)
}
