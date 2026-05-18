// In-flight deployment count — public, unauthenticated.
//
// Endpoint: GET /api/v1/deployments/in-flight-count
//
// Returns the number of deployments currently in a non-terminal Phase-0
// status (pending / provisioning / tofu-applying / flux-bootstrapping)
// across ALL owners. The caller is the CI deploy-bot
// (.github/workflows/catalyst-build.yaml), which uses this to GATE the
// values.yaml image-SHA bump: a non-zero count means rolling the
// catalyst-api Deployment now would kill an active tofu apply
// mid-flight, abandoning the prov and leaking Hetzner resources.
//
// Background — t13/t17/t21 incident, 2026-05-17/18:
//
// catalyst-api is single-replica with strategy: Recreate (Sovereign
// chart) / RollingUpdate maxUnavailable=1 (contabo-mkt). When the
// deploy-bot bumps the image SHA, Flux reconciles the Deployment and
// the OLD Pod gets SIGTERM'd. The OpenTofu workdir lives on a /tmp
// emptyDir that dies with the Pod (provisioner constraint — fresh state
// every run), so any in-flight `tofu apply` is killed mid-resource.
// The on-disk deployment record is rewritten to status=failed on the
// NEW Pod's restoreFromStore path (see deployments.go:413 — issue #530
// follow-up), but the Hetzner resources tagged with the abandoned
// deployment-id remain orphans. Three consecutive provs (t13/t17/t21)
// died this way during 2026-05-17.
//
// Why this is the right shape:
//
//   - Phase-0 in-flight is the only status that cannot survive a Pod
//     restart. Phase-1 (HelmRelease watch) is observational and resumes
//     via resumePhase1Watch on the new Pod, so we do NOT count
//     "phase1-watching" as a blocking state — isPhase0InFlightStatus
//     (deployments.go:440) is the canonical predicate.
//
//   - "Count only" (not a per-deployment list with FQDNs/owners) keeps
//     information disclosure to the bare minimum. Same posture as
//     /api/v1/subdomains/check — pre-auth surface, read-only, smallest
//     answer that still drives the caller's decision.
//
//   - HTTP 200 + count=0 is the green light. The deploy-bot polls until
//     count==0 OR a hard timeout, then proceeds with the bump.
//
//   - No auth gate. The deploy-bot has no session cookie and runs from
//     a GitHub Actions runner. Same precedent as /healthz, /readyz,
//     /api/v1/version, /api/v1/subdomains/check.
//
// Adopted deployments are excluded from the count — once AdoptedAt is
// set, Phase-0 is long-since complete (handover already fired), so a
// stale "phase1-watching" or "ready" status with AdoptedAt set should
// not gate a deploy. We rely on isPhase0InFlightStatus to filter
// terminal statuses anyway, so the AdoptedAt check is belt-and-braces.

package handler

import (
	"encoding/json"
	"net/http"
	"sort"
)

// inFlightCountResponse is the wire shape of the public count endpoint.
//
// Count is the authoritative number the deploy-bot gates on. IDs is a
// best-effort sorted list of deployment IDs in non-terminal Phase-0
// status — useful for CI logs ("waiting on dep-abc123, dep-def456")
// without leaking owner emails or FQDNs.
type inFlightCountResponse struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// InFlightCount handles GET /api/v1/deployments/in-flight-count.
//
// Public + unauthenticated. Returns a count of deployments in any
// Phase-0 in-flight status (see isPhase0InFlightStatus). The deploy-bot
// in .github/workflows/catalyst-build.yaml polls this before bumping
// the values.yaml image SHA — if count > 0, it waits and retries to
// avoid rolling the catalyst-api Pod mid-tofu-apply.
func (h *Handler) InFlightCount(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0)

	h.deployments.Range(func(_, val any) bool {
		dep, ok := val.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		status := dep.Status
		adopted := dep.AdoptedAt != nil
		id := dep.ID
		dep.mu.Unlock()

		if adopted {
			return true
		}
		if isPhase0InFlightStatus(status) {
			ids = append(ids, id)
		}
		return true
	})

	sort.Strings(ids)

	resp := inFlightCountResponse{
		Count: len(ids),
		IDs:   ids,
	}

	w.Header().Set("Content-Type", "application/json")
	// Discourage caching: a deploy-bot retry seconds later needs the
	// fresh state, not whatever an upstream proxy stored.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
