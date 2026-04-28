package handlers

import "sync"

// tenantLocks serializes day-2 install/uninstall on a single tenant so
// read-modify-write sequences (tenant fetch → capacity check → atomic
// append) can't interleave. The $addToSet storage-layer fix handles
// basic concurrent appends, but capacity math and the "already installed"
// short-circuit also read tenant.Apps, and FerretDB 1.24's operator
// semantics aren't guaranteed under heavy concurrency. An in-process
// RWMutex-per-tenant costs near-zero and makes the behaviour trivially
// correct — each install/uninstall on a given tenant sees a consistent
// snapshot. Issue #110.
//
// The map grows by one entry per active tenant; an idle tenant's entry
// is released after its last handler returns by purgeIfIdle. The pod
// has one tenantLocks instance on the Handler; crashing-then-restart
// re-creates the map cleanly.
type tenantLocks struct {
	mu sync.Mutex
	m  map[string]*lockEntry
}

type lockEntry struct {
	mu       sync.Mutex
	refCount int
}

func newTenantLocks() *tenantLocks {
	return &tenantLocks{m: map[string]*lockEntry{}}
}

// acquire returns a function that releases the lock. Callers should use
// it as `release := locks.acquire(id); defer release()`.
func (t *tenantLocks) acquire(tenantID string) func() {
	if tenantID == "" {
		return func() {}
	}
	t.mu.Lock()
	e, ok := t.m[tenantID]
	if !ok {
		e = &lockEntry{}
		t.m[tenantID] = e
	}
	e.refCount++
	t.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		t.mu.Lock()
		e.refCount--
		if e.refCount == 0 {
			delete(t.m, tenantID)
		}
		t.mu.Unlock()
	}
}
