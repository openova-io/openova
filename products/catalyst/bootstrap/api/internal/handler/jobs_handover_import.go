// jobs_handover_import.go — POST /api/v1/internal/jobs/import
// (Refs #3263 #3277).
//
// Sovereign-side endpoint that receives the mother's jobs snapshot at
// handover time (see jobs_handover_export.go for the mother half). The
// chroot's jobs store is seeded only by its OWN bootstrap watch
// (primary region); the secondary regions' install rows live solely in
// the mothership's store. This endpoint upserts those rows into the
// local store so the chroot's /jobs surface renders BOTH regions and
// the Region column/filter (#3278 #3352) auto-appears.
//
// Auth: NOT session-gated — cross-cluster ingress at handover happens
// before any operator session exists on the child, exactly like
// /api/v1/internal/deployments/import. Validation mirrors
// HandleDeploymentImport: the payload's sovereignFqdn must match the
// receiving cluster's CATALYST_OTECH_FQDN env (cross-cluster data
// poisoning guard), and the deploymentId must pass the safe-id regex
// the secondary-kubeconfig receiver uses.
//
// Idempotent: jobs.Store.ImportSnapshot merges under mergeJob
// semantics — a re-POST of the same snapshot is a no-op, a terminal
// local row is never regressed, and rows the chroot wrote itself
// (region-a) are preserved/merged, never deleted.
package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// HandleJobsImport receives the mother's jobs snapshot and upserts it
// into this cluster's local jobs store.
func (h *Handler) HandleJobsImport(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "jobs-store-unavailable",
			"detail": "catalyst-api has no jobs persistence layer; cannot import job rows",
		})
		return
	}

	var payload jobsImportPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "bad-payload",
			"detail": err.Error(),
		})
		return
	}

	// FQDN check — protect against cross-cluster data poisoning. Same
	// guard as HandleDeploymentImport.
	expectedFQDN := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if expectedFQDN == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api is not running on a Sovereign cluster (CATALYST_OTECH_FQDN unset) — refusing to accept jobs import",
		})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(payload.SovereignFQDN), expectedFQDN) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "fqdn-mismatch",
			"detail": "jobs payload's sovereignFqdn does not match this cluster's CATALYST_OTECH_FQDN — refusing to import foreign job rows",
		})
		return
	}

	payload.DeploymentID = strings.TrimSpace(payload.DeploymentID)
	if !safeIDPattern.MatchString(payload.DeploymentID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-deploymentId",
			"detail": "must match ^[a-z0-9][a-z0-9-]{0,62}$",
		})
		return
	}

	jobsApplied, execsApplied, err := h.jobs.ImportSnapshot(
		payload.DeploymentID, payload.Jobs, payload.Executions,
	)
	if err != nil {
		h.log.Error("jobs-import: ImportSnapshot failed",
			"id", payload.DeploymentID,
			"err", err,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "import-failed",
			"detail": err.Error(),
		})
		return
	}

	h.log.Info("jobs-import: persisted",
		"id", payload.DeploymentID,
		"jobsReceived", len(payload.Jobs),
		"jobsApplied", jobsApplied,
		"executionsReceived", len(payload.Executions),
		"executionsApplied", execsApplied,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"deploymentId":      payload.DeploymentID,
		"jobsApplied":       jobsApplied,
		"executionsApplied": execsApplied,
	})
}
