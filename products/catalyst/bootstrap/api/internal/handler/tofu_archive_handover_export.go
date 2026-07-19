// tofu_archive_handover_export.go — mother-side phase0 tofu-archive
// transfer at handover (Refs #3052 #933 #3376).
//
// FUNNEL gap (hw130, walked live 2026-06-13, #3376): a stranger with a
// voucher never lands signed-in in their own Org console because the
// tenant-Org provisioning chain is wedged at exactly ONE unbuilt wire —
// the mother→child phase0 tofu-archive push.
//
// The chain: at handover the mother must POST the deployment's Phase-0
// OpenTofu workdir (terraform.tfstate + *.tf + lock) to the child's
//
//	POST https://api.<sovereign-fqdn>/api/v1/handover/tofu-archive
//
// The child (ReceiveTofuArchive, handover.go) seals it at
// `secret/catalyst/tofu-phase0-archive` in OpenBao, which is the durable
// handover marker the cutover's 425 gate keys on. Sealing it fires the
// Self-Sovereignty Cutover engine (CATALYST_FIRE_CUTOVER_ON_HANDOVER,
// #3127), whose Step 09 mints the real Gitea API token + patches
// `secret/org-services/provisioning-github-token` with the
// `catalyst.openova.io/token-source=self-sovereign-cutover-step-09`
// annotation. The `org-services/provisioning` Pod's `wait-for-cutover-token` init
// container gates on exactly that annotation; until it lands the pod sits
// Init:0/1 forever, the provisioning Service has zero ready endpoints, and
// `provisioning/start` 502s. So this one push un-wedges the whole funnel.
//
// Before #3376 the archive push lived ONLY inside FinaliseHandover
// (handover.go), the SYNCHRONOUS wizard finalise path. Auto-handover
// (fireHandover, phase1_watch.go) and the operator re-mint
// (MintHandoverToken, handover_jwt.go) never pushed it — so an env that
// converged via auto-handover (every fresh prov) never got the archive.
//
// The wire here mirrors the #3263/#3277 jobs export verbatim
// (jobs_handover_export.go): a standalone exportTofuArchiveToChild fired
// as a goroutine from fireHandover (auto-handover) AND from
// MintHandoverToken (operator re-mint recovery — an env whose handover
// predates this wire, like hw130, receives its archive on the next
// re-mint with NO re-prov). Re-mint re-fires the push; the child's seal
// is idempotent (PutKVv2 overwrites). Best-effort with backoff per
// docs/PRINCIPLES.md — the mother stays the source of truth and a
// re-mint retries.
package handler

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// exportTofuArchiveToChild builds the deployment's phase0 tofu archive
// from the mothership workdir and POSTs it to the child's
// /api/v1/handover/tofu-archive with retry. Called as a goroutine from
// fireHandover (auto-handover) and MintHandoverToken (operator re-mint).
// Fire-and-forget; failures are logged loudly.
//
// Unlike FinaliseHandover's synchronous push, this path does NOT delete
// the local workdir on success — the workdir is the mother's only
// recovery source for a re-mint, and FinaliseHandover (when the wizard
// later calls it) owns the slim-and-delete. Keeping the workdir means a
// repeated re-mint stays idempotent.
func (h *Handler) exportTofuArchiveToChild(depID, fqdn string) {
	prov := provisioner.New()
	workdir := filepath.Join(prov.WorkDir, depID)
	archive, err := buildTofuArchive(workdir)
	if err != nil {
		h.log.Error("tofu-archive-export: build archive failed",
			"id", depID, "workdir", workdir, "err", err,
		)
		return
	}
	if len(archive) == 0 {
		// Empty workdir means it was already cleaned (a prior
		// FinaliseHandover ran, or this is a slim post-handover record).
		// Nothing to ship — the archive is presumably already sealed on
		// the child. Not an error; matches buildTofuArchive's empty-map
		// contract.
		h.log.Info("tofu-archive-export: workdir empty; nothing to ship (archive likely already sealed on child)",
			"id", depID, "workdir", workdir,
		)
		return
	}

	url := "https://api." + fqdn + "/api/v1/handover/tofu-archive"
	if h.handoverTargetURL != "" {
		url = h.handoverTargetURL
	}
	client := h.handoverHTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child's LE cert may be seconds behind handover, same rationale as exportDeploymentToChild
			},
		}
	}
	body, err := json.Marshal(tofuArchiveRequest{
		DeploymentID:  depID,
		SovereignFQDN: fqdn,
		Files:         archive,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.log.Error("tofu-archive-export: marshal payload failed",
			"id", depID, "err", err,
		)
		return
	}
	h.log.Info("tofu-archive-export: shipping phase0 archive to child",
		"id", depID, "url", url, "files", len(archive),
	)
	h.postTofuArchiveWithRetry(client, url, body, depID)
}

// postTofuArchiveWithRetry POSTs body to url via the shared handover-export
// retry policy (#3747): backoff doubles from 5s to the ceiling, total budget
// defaults to 20m (was a fixed 5m, which could expire against a not-yet-ready
// `api.<fqdn>` backend and strand the cutover-arming seal forever), 5xx
// retries, 4xx gives up. A 503 from the receiver ("not handover target" —
// OpenBao token not yet wired) is a 5xx and IS retried, because the child's
// catalyst-api k8s-auth login (#3376) may still be settling. Split out so the
// network half is unit-testable with an injected client.
func (h *Handler) postTofuArchiveWithRetry(client *http.Client, url string, body []byte, depID string) {
	outcome := postHandoverExportWithRetry(client, url, body, func(attempt int, kind string, status int, err error) {
		switch kind {
		case "request-error":
			h.log.Error("tofu-archive-export: NewRequest failed",
				"id", depID, "url", url, "err", err,
			)
		case "conn-error":
			h.log.Warn("tofu-archive-export: POST failed (will retry)",
				"id", depID, "url", url, "attempt", attempt, "err", err,
			)
		case "5xx":
			h.log.Warn("tofu-archive-export: child 5xx (will retry — receiver may still be wiring its OpenBao token)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "4xx":
			h.log.Error("tofu-archive-export: child 4xx (giving up)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "ok":
			h.log.Info("tofu-archive-export: phase0 archive sealed on child",
				"id", depID, "url", url, "attempt", attempt,
			)
		}
	})
	if outcome == handoverExportGaveUpBudget {
		h.log.Error("tofu-archive-export: gave up after export budget exhausted (backend never reachable — operator can re-mint via /mint-handover-token)",
			"id", depID, "url", url, "budget", handoverExportBudget().String(),
		)
	}
}
