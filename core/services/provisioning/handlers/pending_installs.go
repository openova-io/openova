package handlers

import (
	"sync"
	"time"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// pendingInstallRegistry holds day-2 cart installs whose step-0 commit could
// NOT be committed yet because the per-Org Gitea org/repo did not exist after
// the in-line retry budget was exhausted (#4404). The funnel cart dispatches
// the install the instant the Org CR is created, racing the
// organization-controller's per-Org Gitea-org create; on a Sovereign whose
// Gitea-org-creation latency exceeds the in-line budget, the install would
// otherwise be dropped FOREVER (the failed attempt poisons the shared
// idempotency Job so the sibling transport is suppressed as a duplicate).
//
// This registry is the durable side of the self-heal: a record parked here is
// drained by StartPendingInstallReconciler, which re-attempts the commit on a
// cadence until the Gitea org/repo finally exists and the commit lands —
// zero-touch, regardless of how long the per-Org repo takes to appear. A
// permanent (non-race) commit error drops the record and fails the Job.
//
// In-memory by design: the same provisioning pod that took the cart install
// owns the retry. A pod restart that loses the registry is itself covered —
// the install's Job row is left non-terminal, and the funnel re-dispatches
// over the surviving transport on the next reconcile of the Org. The registry
// keeps steady-state cost at one map per parked install. Zero value is usable.
//
// Keyed by idempotency key (falls back to tenantSlug+appSlug when the dispatch
// carried no key) so a re-dispatch of the SAME install coalesces onto one
// pending record instead of stacking duplicates.
type pendingInstallRegistry struct {
	mu    sync.Mutex
	byKey map[string]*pendingInstall
}

// pendingInstall is one parked day-2 install awaiting its per-Org Gitea repo.
type pendingInstall struct {
	data       appChangeData
	job        *store.Job
	enqueuedAt time.Time
	attempts   int
	lastErr    string
}

// pendingInstallKey derives the coalescing key for a parked install.
func pendingInstallKey(data appChangeData) string {
	if data.IdempotencyKey != "" {
		return data.IdempotencyKey
	}
	return data.TenantSlug + "|" + data.AppSlug
}

// Enqueue parks (or refreshes) a pending install. Coalesces on the key so a
// re-dispatch of the same install does not stack a second record; the original
// enqueuedAt is preserved so the age-out budget measures from the first miss.
func (r *pendingInstallRegistry) Enqueue(data appChangeData, job *store.Job) {
	key := pendingInstallKey(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = make(map[string]*pendingInstall)
	}
	if existing, ok := r.byKey[key]; ok {
		// Refresh the carried data/job (a re-dispatch may carry a fresher app
		// list) but keep the original enqueue time + attempt count.
		existing.data = data
		if job != nil {
			existing.job = job
		}
		return
	}
	r.byKey[key] = &pendingInstall{
		data:       data,
		job:        job,
		enqueuedAt: time.Now().UTC(),
	}
}

// Snapshot returns a copy of the parked installs so the reconciler can walk
// them without holding the lock across the (slow) commit re-attempts.
func (r *pendingInstallRegistry) Snapshot() []*pendingInstall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*pendingInstall, 0, len(r.byKey))
	for _, p := range r.byKey {
		// Shallow copy is enough — the reconciler only reads data/job and
		// writes back attempt/err via MarkAttempt/Remove under the lock.
		cp := *p
		out = append(out, &cp)
	}
	return out
}

// MarkAttempt records that the reconciler tried (and failed, still transient)
// to commit a parked install, so logs/age-out can see progress. No-op if the
// record was already drained.
func (r *pendingInstallRegistry) MarkAttempt(data appChangeData, errMsg string) {
	key := pendingInstallKey(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byKey[key]; ok {
		p.attempts++
		p.lastErr = errMsg
	}
}

// Remove drops a parked install (committed successfully, aged out, or failed
// permanently). Idempotent.
func (r *pendingInstallRegistry) Remove(data appChangeData) {
	key := pendingInstallKey(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byKey, key)
}

// RemoveAllFor drops every parked install for a tenant slug (called on
// tenant.deleted so a doomed Org's parked installs stop being re-attempted).
// Returns the count removed.
func (r *pendingInstallRegistry) RemoveAllFor(tenantSlug string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for key, p := range r.byKey {
		if p.data.TenantSlug == tenantSlug {
			delete(r.byKey, key)
			removed++
		}
	}
	return removed
}

// Len returns the number of parked installs (test/observability helper).
func (r *pendingInstallRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byKey)
}
