// handover_export_retry.go — shared retry policy for the mother→child
// handover exports (#3747).
//
// ROOT CAUSE (hw158, ab2135d4cf2d01e4, walked live 2026-06-17):
// Each handover export (tofu-archive, deployment-record, jobs,
// secondary-kubeconfig) was a one-shot fire-and-forget goroutine with a
// FIXED 5-minute total retry budget. fireHandover (phase1_watch.go) spawns
// them exactly once when Phase-1 reaches status=ready, and is guarded
// against re-running by dep.Result.HandoverFiredAt != nil — so once the
// budget expires the exports give up PERMANENTLY and nothing re-attempts
// them (the steady-state heal loop only re-reconciles ClusterMesh).
//
// But at the instant Phase-1 reports ready (all HRs Installed on the
// PRIMARY CP), the Sovereign's `api.<fqdn>` data path — cilium-gateway
// HTTPRoute programming + the LE wildcard cert's DNS-01 challenge + the
// catalyst-api Pod becoming Ready + the LoadBalancer health-checking its
// backend — can still be warming up. During that window the exports hit
// `connection reset by peer` (LB answers, backend not yet ready). If the
// warm-up runs longer than 5 minutes the exports exhaust the budget and
// give up. The tofu-archive export is the one that seals
// secret/catalyst/tofu-phase0-archive on the child, which is the gate that
// ARMS the Self-Sovereignty Cutover (ReceiveTofuArchive →
// fireCutoverEngine, handover.go). So a lost export race => cutover never
// arms => NS5 / Pillar-5 can never be walked zero-touch on a fresh prov.
//
// On hw158 the symptom was exactly this: handoverFiredAt=03:38:08 but
// cutoverStartedAt empty + cutoverComplete=false hours later; a manual
// POST /mint-handover-token (which re-fires the exports) sealed the archive
// on attempt 1 and fired the cutover the moment the backend was reachable.
//
// THE FIX: distinguish the warm-up signature (a connection-level error or a
// 5xx from the child — the backend is still coming up) from a genuine 4xx
// (a real defect in the payload/auth that retrying can never fix). A 4xx
// still gives up immediately. A connection-error / 5xx keeps retrying up to
// a GENEROUS absolute cap (default 20m, configurable) instead of the old
// 5m, so the export survives an `api.<fqdn>` backend that takes longer than
// 5 minutes to become reachable. The child's receivers are all idempotent
// (PutKVv2 overwrite / record upsert / AddCluster no-op), so a longer retry
// is safe.
//
// Per docs/PRINCIPLES.md #4 the cap + backoff ceiling are runtime knobs:
//
//	CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS — total retry budget (default 1200s = 20m)
//	CATALYST_HANDOVER_EXPORT_MAX_BACKOFF_SECONDS — backoff ceiling (default 60s)
package handler

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"time"
)

// defaultHandoverExportBudget is the total wall-clock time a handover
// export keeps retrying transient failures (connection errors + 5xx)
// before giving up. Raised from the historical 5m to 20m (#3747) so a slow
// `api.<fqdn>` backend warm-up — gateway programming + LE cert issuance +
// pod readiness + LB health-check, which can collectively exceed 5m on a
// fresh prov — never strands the cutover-arming tofu-archive seal. The
// 10-minute sovereign-wildcard-tls wait (DefaultHandoverCertWaitTimeout,
// phase1_watch.go) plus headroom for the gateway/LB settle motivates 20m.
var defaultHandoverExportBudget = 20 * time.Minute

// defaultHandoverExportMaxBackoff caps the exponential backoff between
// retries. Backoff starts at 5s and doubles to this ceiling.
var defaultHandoverExportMaxBackoff = 60 * time.Second

// handoverExportBudget returns the configured total retry budget, honouring
// the CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS override (tests set a short
// value so the give-up path is exercised quickly).
func handoverExportBudget() time.Duration {
	if v := os.Getenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return defaultHandoverExportBudget
}

// handoverExportMaxBackoff returns the configured backoff ceiling.
func handoverExportMaxBackoff() time.Duration {
	if v := os.Getenv("CATALYST_HANDOVER_EXPORT_MAX_BACKOFF_SECONDS"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return defaultHandoverExportMaxBackoff
}

// handoverExportOutcome is the terminal result of postHandoverExportWithRetry.
type handoverExportOutcome int

const (
	// handoverExportSucceeded — the child returned a non-error status (<400).
	handoverExportSucceeded handoverExportOutcome = iota
	// handoverExportGaveUp4xx — the child returned a 4xx; retrying cannot
	// help (genuine payload/auth defect). Caller logs an error.
	handoverExportGaveUp4xx
	// handoverExportGaveUpBudget — the budget expired while the backend was
	// still unreachable / 5xx. Caller logs an error; an operator re-mint
	// (POST /mint-handover-token) re-fires the export.
	handoverExportGaveUpBudget
	// handoverExportFatal — building the request itself failed (never
	// retried; effectively impossible for a well-formed URL).
	handoverExportFatal
)

// handoverExportAttempt is invoked once per HTTP attempt for observability.
// kind is one of "conn-error", "5xx", "4xx", "ok", "request-error"; status
// is the HTTP status (0 on a connection/request error); err is the transport
// error (non-nil only when kind=="conn-error" or "request-error"). Callers
// use it to emit their existing per-export log lines (so log greppability is
// preserved verbatim per export).
type handoverExportAttempt func(attempt int, kind string, status int, err error)

// postHandoverExportWithRetry POSTs body to url, retrying connection errors
// and 5xx responses with exponential backoff (5s→ceiling) until the export
// budget expires, and giving up immediately on a 4xx. This is the single
// retry policy shared by all four handover exports (#3747) — previously each
// had its own copy with a fixed 5-minute budget that could expire against a
// not-yet-ready `api.<fqdn>` backend.
//
// onAttempt fires once per attempt so each caller keeps its own log strings
// (tofu-archive-export / deployment-export / jobs-export / d16-export). It
// may be nil.
func postHandoverExportWithRetry(client *http.Client, url string, body []byte, onAttempt handoverExportAttempt) handoverExportOutcome {
	maxBackoff := handoverExportMaxBackoff()
	// Initial backoff is 5s, clamped to the ceiling so a low
	// CATALYST_HANDOVER_EXPORT_MAX_BACKOFF_SECONDS (tests) yields tight
	// retries. Production keeps the historical 5s start.
	backoff := 5 * time.Second
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	deadline := time.Now().Add(handoverExportBudget())
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			if onAttempt != nil {
				onAttempt(attempt, "request-error", 0, err)
			}
			return handoverExportFatal
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// Connection-level error — the backend is (very likely) still
			// warming up: gateway not programmed, cert issuing, pod not
			// Ready, or LB health-check failing. THIS is the warm-up
			// signature #3747 fixes: keep retrying up to the (generous)
			// budget instead of giving up at 5m.
			if onAttempt != nil {
				onAttempt(attempt, "conn-error", 0, err)
			}
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		status := resp.StatusCode
		resp.Body.Close()
		if status >= 500 {
			// 5xx — receiver reachable but not ready (e.g. its OpenBao
			// k8s-auth login still settling, returns 503 "not handover
			// target"). Retry, same as a connection error.
			if onAttempt != nil {
				onAttempt(attempt, "5xx", status, nil)
			}
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		if status >= 400 {
			// 4xx — a genuine defect (bad body / failed auth). Retrying
			// can never help; give up immediately and let the error
			// surface loudly.
			if onAttempt != nil {
				onAttempt(attempt, "4xx", status, nil)
			}
			return handoverExportGaveUp4xx
		}
		if onAttempt != nil {
			onAttempt(attempt, "ok", status, nil)
		}
		return handoverExportSucceeded
	}
	return handoverExportGaveUpBudget
}
