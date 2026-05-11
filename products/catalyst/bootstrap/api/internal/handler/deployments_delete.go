// deployments_delete.go — record-only delete for the operator's
// deployments admin list (issue #178).
//
// Two delete modes are exposed to operators on the /sovereign/deployments
// page:
//
//   - "Delete record only" — calls DELETE /api/v1/deployments/{id} (this
//     file). Removes the deployment from the in-memory map + on-disk
//     store + the kubeconfig file on the catalyst-api Pod. Does NOT
//     touch Hetzner. The Sovereign that was provisioned by this
//     deployment KEEPS RUNNING in Hetzner — the operator has chosen to
//     orphan it. Useful when the customer Sovereign has been handed
//     over but the breadcrumb row is no longer wanted in the admin UI,
//     or when a stuck record persists after a manual cleanup.
//
//   - "Delete record AND wipe Sovereign" — the UI calls POST
//     /api/v1/deployments/{id}/wipe FIRST (existing wipe.go handler,
//     which already destroys Hetzner + deletes the on-disk record on
//     success). The "deep delete" toggle on the modal simply chooses
//     which endpoint to POST against — both paths funnel back to
//     /sovereign/deployments with a refreshed list afterwards.
//
// Refusal rules — identical posture to ReleaseSubdomain (wipe.go) so an
// operator can't surprise themselves into orphaning a still-converging
// Sovereign or stripping the breadcrumb of a customer-adopted one:
//
//	200/204 — record deleted (or already absent)
//	404     — unknown deployment id (also returned on ownership mismatch
//	          per the issue #689 anti-enumeration posture)
//	409     — deployment is still in-flight; refuse so the
//	          runProvisioning goroutine can't try to Commit a row that
//	          no longer exists
//	422     — deployment has been adopted by a customer; refuse so the
//	          handover breadcrumb stays intact

package handler

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

// deleteDeploymentResponse is the wire shape of DELETE
// /api/v1/deployments/{id}.
type deleteDeploymentResponse struct {
	DeploymentID  string `json:"deploymentId"`
	SovereignFQDN string `json:"sovereignFQDN,omitempty"`
	StoreDeleted  bool   `json:"storeDeleted"`
	LocalCleaned  bool   `json:"localCleaned"`
	Mode          string `json:"mode"`
	Note          string `json:"note,omitempty"`
}

// DeleteDeployment handles DELETE /api/v1/deployments/{id}.
//
// This is the "record-only" delete: the deployment row is removed from
// catalyst-api but NO cloud resources are touched. For the destructive
// "kill the kid" path, the UI POSTs /wipe first; this handler is then
// not called (wipe.go already deletes the record on its way out).
func (h *Handler) DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		// Per the issue #689 posture: don't differentiate "not found"
		// from "exists but not yours". Returning 404 here also makes
		// the endpoint idempotent — a second DELETE after a successful
		// first one is a clean 404 (matches HTTP DELETE semantics).
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	// Ownership check FIRST so a hostile probe walking ids can't tell
	// "exists but not yours" from "doesn't exist" via timing or body
	// differences. checkOwnership writes the 404 response for us.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	dep.mu.Lock()
	status := dep.Status
	fqdn := dep.Request.SovereignFQDN
	adopted := dep.AdoptedAt != nil
	dep.mu.Unlock()

	// Adopted deployments are customer-owned Sovereigns. Don't allow
	// the operator to strip the handover breadcrumb — they should use
	// the customer Sovereign's own admin surface to remove the record
	// on the other side of handover.
	if adopted {
		writeJSON(w, http.StatusUnprocessableEntity, deleteDeploymentResponse{
			DeploymentID:  id,
			SovereignFQDN: fqdn,
			Mode:          "record-only",
			Note:          "deployment has been adopted by a customer; the breadcrumb record is protected and cannot be deleted here",
		})
		return
	}

	// Refuse to delete a deployment that is still in-flight — the
	// runProvisioning goroutine may still call Commit on a row that
	// would suddenly be missing from the in-memory map.
	if isInFlightStatus(status) {
		writeJSON(w, http.StatusConflict, deleteDeploymentResponse{
			DeploymentID:  id,
			SovereignFQDN: fqdn,
			Mode:          "record-only",
			Note:          "deployment is still in-flight (status=" + status + "); wait for terminal state or use POST /wipe to destroy + delete in one shot",
		})
		return
	}

	out := deleteDeploymentResponse{
		DeploymentID:  id,
		SovereignFQDN: fqdn,
		Mode:          "record-only",
	}

	// Step 1 — on-disk record delete. Best-effort; surface a "note" but
	// don't fail the response if the underlying file is already missing.
	if h.store != nil {
		if err := h.store.Delete(id); err != nil {
			// IsNotFound at the store layer is treated as success
			// (idempotent). Other errors are surfaced as a note but
			// the in-memory map delete still runs so the admin UI
			// reflects the operator's intent immediately.
			if !os.IsNotExist(err) {
				h.log.Warn("delete-deployment: store delete failed",
					"id", id, "err", err)
				out.Note = "store delete: " + err.Error()
			} else {
				out.StoreDeleted = true
			}
		} else {
			out.StoreDeleted = true
		}
	} else {
		out.StoreDeleted = true
	}

	// Step 2 — kubeconfig file on disk. Same mode-0600 file the wipe
	// path removes. Best-effort; absence is success.
	if h.kubeconfigsDir != "" {
		kcPath := filepath.Join(h.kubeconfigsDir, id+".yaml")
		if err := os.Remove(kcPath); err != nil && !os.IsNotExist(err) {
			h.log.Warn("delete-deployment: kubeconfig delete failed",
				"id", id, "path", kcPath, "err", err)
		} else {
			out.LocalCleaned = true
		}
	} else {
		out.LocalCleaned = true
	}

	// Step 3 — drop the in-memory row. The wizard's polling loop will
	// then see the deployment disappear from GET /api/v1/deployments
	// on its next tick (~30s).
	h.deployments.Delete(id)

	h.log.Info("delete-deployment: record-only delete complete",
		"id", id,
		"sovereignFQDN", fqdn,
		"priorStatus", status,
		"storeDeleted", out.StoreDeleted,
		"localCleaned", out.LocalCleaned,
	)

	writeJSON(w, http.StatusOK, out)
}
