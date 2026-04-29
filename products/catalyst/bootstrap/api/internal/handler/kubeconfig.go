// GET + PUT /api/v1/deployments/{id}/kubeconfig — the cloud-init
// kubeconfig postback contract (issue #183, Option D).
//
// Producer of the bytes: the new Sovereign's cloud-init runs k3s
// install, waits for /healthz, rewrites /etc/rancher/k3s/k3s.yaml's
// `https://127.0.0.1:6443` to the load-balancer's public IP, then
// PUTs the rewritten YAML to this endpoint with an
// `Authorization: Bearer <token>` header. The token was templated
// into cloud-init by the OpenTofu module from the
// `kubeconfig_bearer_token` tfvars key the catalyst-api stamped onto
// the provisioner.Request at CreateDeployment time.
//
// Bearer verification:
//
//   - The handler reads the bearer from `Authorization: Bearer ...`
//     (case-insensitive), computes SHA-256, hex-encodes, and uses
//     `subtle.ConstantTimeCompare` against the persisted
//     `Deployment.kubeconfigBearerHash`. A mismatch returns 403 with
//     `{"error":"invalid-bearer"}`.
//   - The bearer is single-use: once `Result.KubeconfigPath` is set
//     a subsequent PUT returns 403 with `{"error":"already-set"}`.
//     This defends against a replay where an attacker captured the
//     bearer (e.g. by reading user_data) and tries to swap in a
//     hostile kubeconfig later.
//
// File handling:
//
//   - Plaintext is written to `<kubeconfigsDir>/<id>.yaml` with
//     mode 0600. The directory is pre-created at handler startup
//     (mode 0700). The plaintext NEVER lands in the JSON record on
//     disk — the record carries only the path pointer.
//   - After the file write, `Result.KubeconfigPath` is populated and
//     persistDeployment is called so a Pod restart between PUT and
//     the helmwatch goroutine launching still finds the path on
//     disk.
//   - The helmwatch goroutine is then kicked off via
//     `runPhase1Watch` so the per-component SSE events start flowing
//     to the wizard. The phase1Started guard on Deployment ensures
//     runProvisioning's later (synchronous) call is a no-op so we
//     don't run two informers.
//
// Failure modes:
//
//   - 401 — Authorization header missing or not `Bearer ...`
//   - 403 — bearer hash mismatch OR kubeconfig already set OR no
//     bearer hash on record
//   - 404 — deployment id unknown
//   - 422 — body empty or > 1 MiB (a valid k3s kubeconfig is ~3 KB)
//   - 503 — kubeconfigs directory unwritable (PVC unmounted)
//
// GET semantics (unchanged contract for operator break-glass /
// wizard "Download kubeconfig"):
//
//   - 200 application/yaml when KubeconfigPath is set and readable
//   - 409 {"error":"not-implemented"} when the postback hasn't
//     happened yet — preserves the StepSuccess.test.tsx fallback.
//   - 409 {"error":"kubeconfig-file-missing"} when the path pointer
//     is set but the file is gone (PVC drift).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): the
// catalyst-api never logs the kubeconfig bytes, never logs the
// bearer token, never logs the bearer hash. It logs deployment id,
// byte length, and outcome class.
//
// The GET endpoint remains intentionally NOT authenticated at the
// catalyst-api edge — it inherits whatever auth the franchise
// console attaches. PUT is bearer-protected because the cloud-init
// caller has no SSO context. Adding edge-auth to GET is out of
// scope for this issue (separate ticket if needed).
package handler

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// maxKubeconfigBytes — upper bound on a PUT body. A real k3s
// kubeconfig is roughly 3 KB; capping at 1 MiB preserves headroom
// for hand-edited configs while making oversize-bomb attacks
// against the PVC harmless.
const maxKubeconfigBytes = 1 << 20

// GetKubeconfig — GET /api/v1/deployments/{id}/kubeconfig.
//
// Returns the kubeconfig as application/yaml by reading the file
// at Result.KubeconfigPath. An absent or unreadable path yields
// HTTP 409 with body {"error": "not-implemented"} so the wizard's
// existing StepSuccess.test.tsx fallback path keeps working.
func (h *Handler) GetKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	dep.mu.Lock()
	var path string
	if dep.Result != nil {
		path = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()

	if path == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-implemented",
			"detail": "kubeconfig has not been captured for this deployment yet. Operator can fetch it via SSH per docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH; programmatic capture happens when the new Sovereign's cloud-init PUTs to this endpoint.",
		})
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		// File pointer present but file gone — the "kubeconfig file
		// lost" case. Surface 409 so the wizard renders the SSH-
		// fetch fallback rather than a server error.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "kubeconfig-file-missing",
			"detail": "deployment record points at a kubeconfig file that no longer exists on disk: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+dep.Request.SovereignFQDN+`-kubeconfig.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)

	h.log.Info("kubeconfig served",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"bytes", len(raw),
	)
}

// PutKubeconfig — PUT /api/v1/deployments/{id}/kubeconfig.
//
// The cloud-init postback endpoint. See file header for the full
// contract.
func (h *Handler) PutKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	// Extract bearer token. RFC 6750 §2.1 — case-insensitive scheme,
	// single space separator. We trim aggressively to tolerate
	// trailing whitespace from curl variants.
	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <token> header is required",
		})
		return
	}

	// Snapshot the persisted hash + already-set state under the
	// lock so a concurrent retry/double-PUT can't observe the old
	// hash while we write the new file.
	dep.mu.Lock()
	persistedHash := dep.kubeconfigBearerHash
	alreadySet := dep.Result != nil && dep.Result.KubeconfigPath != ""
	dep.mu.Unlock()

	if persistedHash == "" {
		// CreateDeployment always mints a hash, so a missing one
		// means this deployment was created before #183 landed (or
		// some upstream bug). Refuse — accepting a kubeconfig
		// against an unverifiable bearer would silently accept
		// arbitrary YAML.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "no-bearer-hash",
			"detail": "this deployment has no bearer hash on record; refusing to accept a kubeconfig",
		})
		return
	}

	// Constant-time compare on the SHA-256 hex strings. We hash
	// the inbound bearer first so timing differences in the
	// comparison can't leak the prefix of the persisted hash.
	inboundHash := hashBearerToken(bearer)
	if subtle.ConstantTimeCompare([]byte(inboundHash), []byte(persistedHash)) != 1 {
		h.log.Warn("kubeconfig PUT rejected: bearer mismatch",
			"id", id,
			"sovereignFQDN", dep.Request.SovereignFQDN,
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "invalid-bearer",
			"detail": "bearer token does not match the deployment's expected hash",
		})
		return
	}

	if alreadySet {
		// Single-use replay defence — once the kubeconfig has been
		// captured, the bearer is consumed. A second PUT (network
		// retry, attacker replay) is rejected.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "already-set",
			"detail": "kubeconfig has already been captured for this deployment; bearer is single-use",
		})
		return
	}

	// Read the body. http.MaxBytesReader caps at maxKubeconfigBytes
	// so a misbehaving cloud-init can't exhaust the PVC.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxKubeconfigBytes))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "body-too-large",
			"detail": fmt.Sprintf("kubeconfig body exceeds %d bytes", maxKubeconfigBytes),
		})
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "empty-body",
			"detail": "kubeconfig body is empty",
		})
		return
	}

	// Persist to disk. The directory is created in handler.New() at
	// startup; we MkdirAll here defensively so a delete-and-retry
	// flow (operator manually removing the kubeconfig to reissue)
	// recreates the directory automatically.
	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured (CATALYST_KUBECONFIGS_DIR)",
		})
		return
	}
	if err := os.MkdirAll(h.kubeconfigsDir, 0o700); err != nil {
		h.log.Error("kubeconfigs dir create failed", "dir", h.kubeconfigsDir, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unwritable",
			"detail": "catalyst-api cannot create the kubeconfigs directory: " + err.Error(),
		})
		return
	}
	target := filepath.Join(h.kubeconfigsDir, id+".yaml")
	if err := writeFileAtomic0600(target, body); err != nil {
		h.log.Error("kubeconfig file write failed", "id", id, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "write-failed",
			"detail": "kubeconfig could not be persisted: " + err.Error(),
		})
		return
	}

	// Stamp the path on Result + persist. If Result is nil
	// (cloud-init beat `tofu apply` to the finish line — possible
	// when k3s healthz comes up before hcloud_load_balancer_target
	// reconciles READY), allocate a minimal Result with the known
	// SovereignFQDN; the runProvisioning goroutine merges in
	// ControlPlaneIP / LoadBalancerIP / ConsoleURL / GitOpsRepoURL
	// when Phase 0 finishes.
	dep.mu.Lock()
	if dep.Result == nil {
		dep.Result = &provisioner.Result{
			SovereignFQDN: dep.Request.SovereignFQDN,
		}
	}
	dep.Result.KubeconfigPath = target
	dep.mu.Unlock()
	h.persistDeployment(dep)

	h.log.Info("kubeconfig received from cloud-init",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"path", target,
		"bytes", len(body),
	)

	// Launch the helmwatch goroutine in the background. The PUT
	// returns immediately; per-component events flow via the SSE
	// stream the wizard already has open. The phase1Started guard
	// ensures runProvisioning's later (synchronous) call is a
	// no-op so we don't run two informers.
	go h.runPhase1Watch(dep)

	w.WriteHeader(http.StatusNoContent)
}

// extractBearer returns the bearer plaintext from an Authorization
// header value. Empty string indicates "no bearer present" — every
// caller treats that as 401 unauthenticated.
func extractBearer(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}
	const prefix = "bearer "
	if len(authHeader) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(authHeader[len(prefix):])
}

// writeFileAtomic0600 writes data to path atomically with mode 0600.
// Same temp-file + rename pattern store.Save uses for the JSON
// records, applied to the kubeconfig file.
func writeFileAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod 0600: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
