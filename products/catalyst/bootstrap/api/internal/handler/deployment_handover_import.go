// deployment_handover_import.go — POST /api/v1/internal/deployments/import
//
// Sovereign-side endpoint that receives the full deployment record from
// the contabo mother at handover time. Mother's fireHandover() POSTs the
// record here AFTER mint+persist on its side; the receiving Sovereign
// stores it locally so its `/api/v1/deployments/{id}/...` endpoints
// answer with byte-byte-identical data.
//
// This closes the data half of the mother→child contract. Combined with
// PR #976's URL routing (clean /dashboard, /apps, /jobs etc on child),
// the operator's Sovereign Console renders pixel-byte-byte identical to
// the mother view at console.openova.io/sovereign/provision/<id>/<page>.
//
// Auth: NOT session-gated. The endpoint validates the request body by
// checking that the deployment record's SovereignFQDN matches the
// receiving cluster's own CATALYST_OTECH_FQDN env. A request to import
// a record claiming a different FQDN is silently rejected — protects
// against cross-cluster data poisoning.
//
// Idempotent: if a record with the same id already exists in the local
// store, the import OVERWRITES it (intentional — mother is the source
// of truth for events / job history during provisioning, and a re-fire
// of handover is a legitimate retry).
package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// HandleDeploymentImport receives the full deployment record from the
// mother and persists it to this cluster's local catalyst-api store.
func (h *Handler) HandleDeploymentImport(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "store-unavailable",
			"detail": "catalyst-api has no persistence layer; cannot import deployment record",
		})
		return
	}

	var rec store.Record
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "bad-record",
			"detail": err.Error(),
		})
		return
	}

	// FQDN check — protect against cross-cluster data poisoning.
	expectedFQDN := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if expectedFQDN == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api is not running on a Sovereign cluster (CATALYST_OTECH_FQDN unset) — refusing to accept deployment-record import",
		})
		return
	}
	if !strings.EqualFold(rec.Request.SovereignFQDN, expectedFQDN) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "fqdn-mismatch",
			"detail": "deployment record's SovereignFQDN does not match this cluster's CATALYST_OTECH_FQDN — refusing to import a foreign record",
		})
		return
	}
	if strings.TrimSpace(rec.ID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-id",
			"detail": "deployment record carries no id",
		})
		return
	}

	// D30 PR I (2026-05-17 t140 /parent-domains regression): mark imported
	// deployment as Adopted at import time. The chroot IS the owner of
	// this Sovereign (FQDN-match guard above verifies it) and the record
	// arrives ONLY after handover-fire on the mothership, so the chroot's
	// view of "this is my deployment" should not wait for a separate
	// wizard adoption step (which doesn't exist on the chroot side).
	//
	// Without this, h.activeDeployment() returns nil because it filters
	// to AdoptedAt!=nil → ListParentDomains returns only the synth primary
	// → operator visits /parent-domains and sees ONE row instead of the
	// pool. Founder caught on t140 (b#6).
	if rec.AdoptedAt == nil {
		now := time.Now().UTC()
		rec.AdoptedAt = &now
	}

	if err := h.store.Save(rec); err != nil {
		h.log.Error("deployment-import: store.Save failed",
			"id", rec.ID,
			"err", err,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "save-failed",
			"detail": err.Error(),
		})
		return
	}

	// Load into the in-memory map so /deployments/{id}/* endpoints
	// (jobs, infrastructure, events, …) answer immediately without
	// waiting for the next pod restart's restoreFromStore. fromRecord
	// also rewrites in-flight Status fields to "failed" — irrelevant
	// here since the mother only exports terminal-state records.
	dep := fromRecord(rec)
	h.deployments.Store(rec.ID, dep)

	// #1907 — bake-time top-up of the canonical .omani.X org-pool.
	// The mother only stamps the single operator-selected pool entry,
	// but the marketplace UI's /addons subdomain picker offers all
	// four. Ensure the Sovereign-side pool matches the picker so a
	// customer never sees 422 invalid-parent-domain after a successful
	// subdomain pick. See chroot_parent_domains_seed.go for the full
	// rationale.
	h.chrootEnsureOrgPoolSeed(dep)

	h.log.Info("deployment-import: persisted",
		"id", rec.ID,
		"fqdn", rec.Request.SovereignFQDN,
		"events", len(rec.Events),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"deploymentId": rec.ID,
	})
}
