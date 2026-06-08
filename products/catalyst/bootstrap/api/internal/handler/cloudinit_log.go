// PUT + GET /api/v1/deployments/{id}/cloudinit-log — the cloud-init
// diagnostic-log capture contract (#3132, founder directive 2026-06-08).
//
// WHY THIS EXISTS: a Phase-1 cloud-init failure ("the new Sovereign
// cluster did not PUT its kubeconfig") leaves the control-plane VM
// running but the cluster never converges. To fix it we need the
// cloud-init log — but on kom4dc HCS that log CANNOT be PULLED:
//   - the ECS SDK exposes no os-getConsoleOutput (only interactive
//     VNC/serial consoles), and
//   - the control-plane has no reachable sshd (Connection refused even
//     after opening :22 in the security group — verified live on
//     hw106, 2026-06-08).
//
// So the log must be PUSHED. The control-plane cloud-init uploads
// /var/log/cloud-init-output.log to this endpoint on a 30s loop
// (cloudinit-control-plane.tftpl), so the latest snapshot is always on
// the mothership PVC even when cloud-init aborts before the kubeconfig
// PUT. This is the ONLY diagnosis path for the recurring Phase-1
// failure (#3129) AND the mechanical "capture-before-wipe" guard the
// founder asked for — the log survives the wipe regardless of operator
// discipline.
//
// AUTH: the same bearer as the kubeconfig PUT (the cloud-init already
// carries kubeconfig_bearer_token). Verified by SHA-256 +
// ConstantTimeCompare against Deployment.kubeconfigBearerHash, exactly
// like PutKubeconfig.
//
// NOT single-use: unlike the kubeconfig, the log is uploaded
// repeatedly and each PUT OVERWRITES the prior snapshot (latest wins).
// It never triggers Phase-1 watch / SMTP seed — it is pure diagnostics.
//
// Credential hygiene (INVIOLABLE-PRINCIPLES #10): logs only id + byte
// length + outcome, never the body or the bearer.
package handler

import (
	"crypto/subtle"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

// maxCloudInitLogBytes — upper bound on a cloud-init log PUT. A real
// /var/log/cloud-init-output.log is ~100-500 KB; 4 MiB leaves generous
// headroom while keeping a runaway upload from filling the PVC.
const maxCloudInitLogBytes = 4 << 20

// PutCloudInitLog — PUT /api/v1/deployments/{id}/cloudinit-log.
//
// Bearer-protected (same token + hash as PutKubeconfig). Idempotent
// overwrite — the cloud-init loop re-uploads the growing log; the
// latest snapshot wins. No single-use guard, no Phase-1/SMTP launch.
func (h *Handler) PutCloudInitLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <token> header is required",
		})
		return
	}
	dep.mu.Lock()
	persistedHash := dep.kubeconfigBearerHash
	dep.mu.Unlock()
	if persistedHash == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "no-bearer-hash",
			"detail": "this deployment has no bearer hash on record; refusing the upload",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(hashBearerToken(bearer)), []byte(persistedHash)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "invalid-bearer",
			"detail": "bearer token does not match the deployment's expected hash",
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCloudInitLogBytes))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "body-too-large",
			"detail": "cloud-init log exceeds the size cap",
		})
		return
	}
	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured (CATALYST_KUBECONFIGS_DIR)",
		})
		return
	}
	if err := os.MkdirAll(h.kubeconfigsDir, 0o700); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unwritable",
			"detail": "catalyst-api cannot create the diagnostics directory: " + err.Error(),
		})
		return
	}
	target := filepath.Join(h.kubeconfigsDir, id+"-cloudinit.log")
	if err := writeFileAtomic0600(target, body); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "write-failed",
			"detail": "cloud-init log could not be persisted: " + err.Error(),
		})
		return
	}
	h.log.Info("cloud-init log received from control-plane",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"bytes", len(body),
	)
	w.WriteHeader(http.StatusNoContent)
}

// GetCloudInitLog — GET /api/v1/deployments/{id}/cloudinit-log.
//
// Returns the last-uploaded cloud-init log as text/plain for
// post-mortem diagnosis of a Phase-1 failure. Ownership-checked like
// GetKubeconfig. 404 when no log has been uploaded yet.
func (h *Handler) GetCloudInitLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	if !h.checkOwnership(w, r, dep) {
		return
	}
	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured",
		})
		return
	}
	target := filepath.Join(h.kubeconfigsDir, id+"-cloudinit.log")
	raw, err := os.ReadFile(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "no-cloudinit-log",
			"detail": "no cloud-init log has been uploaded for this deployment yet",
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
