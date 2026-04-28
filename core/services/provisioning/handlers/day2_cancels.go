package handlers

import (
	"context"
	"sync"
)

// day2CancelRegistry tracks in-flight day-2 job wait contexts keyed by
// tenant slug + job ID so handleTenantDeleted can preempt them.
//
// Why: before issue #99 the provisioning consumer blocked serially on a
// 10-minute pod-ready wait per day-2 install. When tenant.deleted arrived
// behind a never-ready install, the teardown stalled for 10 min — the
// "leaked tenant" class of bug. With day-2 waits now async, we still want
// an in-flight wait for a doomed tenant to stop immediately rather than
// poll a terminating namespace for the full timeout.
//
// Zero value is usable (sync.Map-ish). Registration creates the tenant map
// lazily; CancelAllFor and Unregister are safe on missing entries.
type day2CancelRegistry struct {
	mu       sync.Mutex
	byTenant map[string]map[string]context.CancelFunc
}

// Register returns a cancellable context derived from parent and stores
// its cancel function under (tenantSlug, jobID). Callers MUST pair this
// with Unregister(tenantSlug, jobID) in a defer to avoid leaks.
func (r *day2CancelRegistry) Register(parent context.Context, tenantSlug, jobID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.byTenant == nil {
		r.byTenant = make(map[string]map[string]context.CancelFunc)
	}
	jobs, ok := r.byTenant[tenantSlug]
	if !ok {
		jobs = make(map[string]context.CancelFunc)
		r.byTenant[tenantSlug] = jobs
	}
	jobs[jobID] = cancel
	r.mu.Unlock()
	return ctx, cancel
}

// Unregister drops the entry for (tenantSlug, jobID). Idempotent; safe to
// call after CancelAllFor already evicted the slug.
func (r *day2CancelRegistry) Unregister(tenantSlug, jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs, ok := r.byTenant[tenantSlug]
	if !ok {
		return
	}
	delete(jobs, jobID)
	if len(jobs) == 0 {
		delete(r.byTenant, tenantSlug)
	}
}

// CancelAllFor cancels every registered day-2 context for tenantSlug and
// drops the entries. Returns the count canceled so handleTenantDeleted can
// log it. Safe to call when nothing is registered.
func (r *day2CancelRegistry) CancelAllFor(tenantSlug string) int {
	r.mu.Lock()
	jobs := r.byTenant[tenantSlug]
	cancels := make([]context.CancelFunc, 0, len(jobs))
	for _, c := range jobs {
		cancels = append(cancels, c)
	}
	delete(r.byTenant, tenantSlug)
	r.mu.Unlock()
	// Fire cancels outside the lock so a cancel handler can't re-enter us.
	for _, c := range cancels {
		c()
	}
	return len(cancels)
}
