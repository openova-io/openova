// jobs_handover_export.go — mother-side handover jobs transfer
// (Refs #3263 #3277).
//
// FOUNDER-DoD gap (hw126, re-verified hw130): the Sovereign console's
// /jobs page "shows only half of the regions". Root cause: the chroot's
// jobs store is seeded only by its OWN bootstrap watch (region-a); the
// secondary regions' rows ("install-<region>:<chart>") are written
// exclusively by the MOTHERSHIP's secondary helmwatch bridges and never
// leave the mothership PVC. The Region column/filter (#3278 #3352) is
// already shipped and auto-appears once 2+ region buckets exist — the
// chroot just never gets the second bucket.
//
// The wire: at handover (exportDeploymentToChild) the mother POSTs a
// bounded snapshot of the deployment's Job rows + each Job's latest
// Execution summary (NO log lines) to
//
//	POST https://api.<sovereign-fqdn>/api/v1/internal/jobs/import
//
// The chroot upserts the rows idempotently (jobs.Store.ImportSnapshot —
// never regresses a terminal row, preserves its own region-a rows), so
// the local /jobs surface carries both regions exactly like the mother
// view. Auth + retry follow exportDeploymentToChild verbatim: no
// session exists on the child at handover time, the child validates by
// FQDN match against CATALYST_OTECH_FQDN; transient EOF/refused during
// the child's gateway programming window is absorbed by the 5s→60s
// backoff with a 5-minute budget.
//
// Recovery path: the export ALSO re-fires from MintHandoverToken (the
// operator's "Open Sovereign console" re-mint), mirroring the D16
// secondary-kubeconfig re-fire — an env whose handover predates this
// wire (hw130) receives its region-b rows on the next re-mint without
// a re-prov.
package handler

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// jobsImportPayload is the wire shape POSTed to the chroot's
// /api/v1/internal/jobs/import endpoint. SovereignFQDN lets the child
// apply the same cross-cluster poisoning guard HandleDeploymentImport
// uses (match against CATALYST_OTECH_FQDN). Jobs come from the
// mother's ListJobs derived view; Executions are filtered to each
// Job's LatestExecutionID so the payload stays bounded — log lines are
// intentionally NOT shipped.
type jobsImportPayload struct {
	DeploymentID  string           `json:"deploymentId"`
	SovereignFQDN string           `json:"sovereignFqdn"`
	Jobs          []jobs.Job       `json:"jobs"`
	Executions    []jobs.Execution `json:"executions"`
}

// buildJobsExportPayload serialises the deployment's Job rows + latest
// Execution summaries into the jobsImportPayload wire shape. Split out
// of exportJobsToChild so the marshal shape is unit-testable without a
// network half. Returns the marshalled body plus the job/execution
// counts for logging.
func (h *Handler) buildJobsExportPayload(depID, fqdn string) ([]byte, int, int, error) {
	jobRows, err := h.jobs.ListJobs(depID)
	if err != nil {
		return nil, 0, 0, err
	}

	// Bound the payload: ChildIDs is derived at read time on BOTH sides
	// (the chroot's persistIndex strips it anyway) and each Job's
	// latest Execution is the only attempt the operator's table view
	// deep-links to — older retry attempts stay mother-side.
	latest := make(map[string]bool, len(jobRows))
	for i := range jobRows {
		jobRows[i].ChildIDs = nil
		if id := jobRows[i].LatestExecutionID; id != "" {
			latest[id] = true
		}
	}
	allExecs, err := h.jobs.ListExecutions(depID)
	if err != nil {
		return nil, 0, 0, err
	}
	execRows := make([]jobs.Execution, 0, len(latest))
	for _, e := range allExecs {
		if latest[e.ID] {
			execRows = append(execRows, e)
		}
	}

	body, err := json.Marshal(jobsImportPayload{
		DeploymentID:  depID,
		SovereignFQDN: fqdn,
		Jobs:          jobRows,
		Executions:    execRows,
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return body, len(jobRows), len(execRows), nil
}

// exportJobsToChild ships the deployment's jobs snapshot to the
// child's catalyst-api. Called as a goroutine from
// exportDeploymentToChild (auto-handover + converged-late rescue, both
// route through fireHandover) and from MintHandoverToken (operator
// re-mint recovery). Fire-and-forget; failures are logged loudly per
// docs/PRINCIPLES.md — the mother stays the source of truth and a
// re-mint retries the export.
func (h *Handler) exportJobsToChild(depID, fqdn string) {
	if h.jobs == nil {
		h.log.Warn("jobs-export: no jobs store; cannot export rows",
			"id", depID,
		)
		return
	}
	body, nJobs, nExecs, err := h.buildJobsExportPayload(depID, fqdn)
	if err != nil {
		h.log.Error("jobs-export: build payload failed",
			"id", depID, "err", err,
		)
		return
	}
	if nJobs == 0 {
		h.log.Info("jobs-export: no job rows for deployment; nothing to ship",
			"id", depID,
		)
		return
	}

	url := "https://api." + fqdn + "/api/v1/internal/jobs/import"
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child's LE cert may be seconds behind handover, same rationale as exportDeploymentToChild
		},
	}
	h.log.Info("jobs-export: shipping snapshot to child",
		"id", depID, "url", url, "jobs", nJobs, "executions", nExecs,
	)
	h.postJobsExportWithRetry(client, url, body, depID)
}

// postJobsExportWithRetry POSTs body to url via the shared handover-export
// retry policy (#3747): backoff doubles from 5s to the ceiling, total budget
// defaults to 20m (was a fixed 5m), retries connection errors + 5xx while the
// child backend warms up, gives up on a 4xx. Split out so the network half is
// unit-testable with an injected client.
func (h *Handler) postJobsExportWithRetry(client *http.Client, url string, body []byte, depID string) {
	outcome := postHandoverExportWithRetry(client, url, body, func(attempt int, kind string, status int, err error) {
		switch kind {
		case "request-error":
			h.log.Error("jobs-export: NewRequest failed",
				"id", depID, "url", url, "err", err,
			)
		case "conn-error":
			h.log.Warn("jobs-export: POST failed (will retry)",
				"id", depID, "url", url, "attempt", attempt, "err", err,
			)
		case "5xx":
			h.log.Warn("jobs-export: child 5xx (will retry)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "4xx":
			h.log.Error("jobs-export: child 4xx (giving up)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "ok":
			h.log.Info("jobs-export: snapshot shipped to child",
				"id", depID, "url", url, "attempt", attempt,
			)
		}
	})
	if outcome == handoverExportGaveUpBudget {
		h.log.Error("jobs-export: gave up after export budget exhausted (backend never reachable — operator can re-mint via /mint-handover-token)",
			"id", depID, "url", url, "budget", handoverExportBudget().String(),
		)
	}
}
