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
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

// noteAppend joins a new note fragment onto an existing one with "; ".
// Refs #3728 — the record-only delete can now surface both a store-delete
// note and a pdm-release note in the same response.
func noteAppend(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// deleteDeploymentResponse is the wire shape of DELETE
// /api/v1/deployments/{id}.
type deleteDeploymentResponse struct {
	DeploymentID  string `json:"deploymentId"`
	SovereignFQDN string `json:"sovereignFQDN,omitempty"`
	StoreDeleted  bool   `json:"storeDeleted"`
	LocalCleaned  bool   `json:"localCleaned"`
	PDMReleased   bool   `json:"pdmReleased,omitempty"`
	Mode          string `json:"mode"`
	Note          string `json:"note,omitempty"`
}

// destroyedTerminalStatus reports whether a deployment's infra is already
// gone (wiped or failed-before-materialising). Refs #3728 — the
// record-only delete releases the pool subdomain ONLY in these states, so
// a deliberately-orphaned but LIVE Sovereign keeps its DNS records intact
// (releasing would drop its PowerDNS child zone out from under it).
func destroyedTerminalStatus(s string) bool {
	return s == "wiped" || s == "failed"
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
	poolDomain := dep.pdmPoolDomain
	subdomain := dep.pdmSubdomain
	domainMode := dep.Request.SovereignDomainMode
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

	// Step 2b (Refs #3728) — release the pool subdomain ONLY when the
	// Sovereign's infra is already destroyed (wiped/failed). This closes
	// the leak where a record-only delete of an already-wiped breadcrumb
	// left the `active` pool_allocations row orphaned → permanent 409 on
	// re-fire of the same subdomain. We deliberately do NOT release for a
	// LIVE deployment being orphaned (status ready/converged/…): that
	// Sovereign still serves DNS from the pool child zone, and dropping it
	// would break the running cluster. Best-effort + ErrNotFound-as-success
	// on a background context (independent of the inbound request).
	if domainMode == "pool" && (poolDomain == "" || subdomain == "") {
		if idx := strings.IndexByte(fqdn, '.'); idx > 0 {
			subdomain = fqdn[:idx]
			poolDomain = fqdn[idx+1:]
		}
	}
	if domainMode == "pool" && destroyedTerminalStatus(status) && poolDomain != "" && subdomain != "" {
		if h.pdm == nil {
			out.Note = noteAppend(out.Note, "pool release skipped: pool-domain-manager client not configured")
		} else {
			relCtx, relCancel := context.WithTimeout(context.Background(), 90*time.Second)
			if err := h.pdm.ReleaseWithRetry(relCtx, poolDomain, subdomain, pdm.CommitRetryConfig{}); err != nil && !errors.Is(err, pdm.ErrNotFound) {
				h.log.Warn("delete-deployment: pdm release failed",
					"id", id, "poolDomain", poolDomain, "subdomain", subdomain, "err", err)
				out.Note = noteAppend(out.Note, "pdm release: "+err.Error())
			} else {
				out.PDMReleased = true
			}
			relCancel()
		}
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
