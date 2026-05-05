// deployment_handover_export.go — mother-side cutover data transfer.
//
// At handover (fireHandover), the mother POSTs the full deployment
// record (events, jobs history, HRs, cloud topology, kubeconfig
// metadata) to the freshly-provisioned child's catalyst-api at
//
//   POST https://api.<sovereign-fqdn>/api/v1/internal/deployments/import
//
// The receiving Sovereign persists it to its local store (see
// deployment_handover_import.go) so its operator-facing endpoints
// answer with byte-byte-identical data. Closes the data half of the
// mother→child contract.
package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"time"
)

// exportDeploymentToChild ships the deployment record to the child's
// catalyst-api. Called as a goroutine from fireHandover so it never
// blocks the SSE emit.
func (h *Handler) exportDeploymentToChild(dep *Deployment, fqdn string) {
	if h.store == nil {
		h.log.Warn("deployment-export: no store; cannot export record",
			"id", dep.ID,
		)
		return
	}
	dep.mu.Lock()
	rec := dep.toRecord()
	depID := dep.ID
	dep.mu.Unlock()

	body, err := json.Marshal(rec)
	if err != nil {
		h.log.Error("deployment-export: marshal failed",
			"id", depID,
			"err", err,
		)
		return
	}

	url := "https://api." + fqdn + "/api/v1/internal/deployments/import"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		h.log.Error("deployment-export: NewRequest failed",
			"id", depID,
			"url", url,
			"err", err,
		)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child's LE cert may be seconds behind handover; operator browsers always see the validated cert
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		h.log.Error("deployment-export: POST failed",
			"id", depID,
			"url", url,
			"err", err,
		)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		h.log.Error("deployment-export: child rejected import",
			"id", depID,
			"url", url,
			"status", resp.StatusCode,
		)
		return
	}
	h.log.Info("deployment-export: shipped to child",
		"id", depID,
		"url", url,
		"events", len(rec.Events),
	)
}
