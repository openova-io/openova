package handlers

import (
	"context"
	"sync"
)

// perOrgCommitGate serialises this process's commits to ONE per-Org gitops
// branch, keyed by `<owner>/<repo>@<branch>`.
//
// #5387 (hw290) — the funnel dispatches a cart with N Applications as N
// SEPARATE `/provisioning/apps/install` calls (tenant service: one POST + one
// `tenant.app_install_requested` event PER cart entry), and ApplyAppInstall
// runs each in its own detached goroutine. Every one of those goroutines then
// re-renders the WHOLE cart and commits it to the SAME
// `<slug>/catalyst-tenant@main` branch. So the per-Org gitops writer was
// literally racing itself: N concurrent Gitea ChangeFiles batches contending
// for one branch head, of which at most one can fast-forward. The losers came
// back as Gitea `PushOutOfDate` (see isGiteaRefRaceError) and burned the
// funnel's finite CAS-retry budget fighting siblings that were pushing
// byte-identical content.
//
// The gate removes that entire class of contention at the source: the cart's N
// commits queue behind each other and land sequentially, each rebuilt against
// the head its predecessor just wrote. The out-of-process racer (the
// org-controller's own PutFile burst on the same branch) is NOT covered by a
// process-local gate — that one is handled by the CAS retry in
// CommitFilesWithPruneAndRebuild, which now has the whole retry budget
// available for it instead of spending it on self-inflicted collisions.
//
// It is a gate, not a dedup: every queued commit still runs, still re-reads the
// head, and still reports its own errors. Nothing is swallowed.
//
// Zero value is usable. Entries are refcounted and dropped when the last
// waiter leaves, so a long-lived process that serves many Organizations does
// not accumulate them.
type perOrgCommitGate struct {
	mu    sync.Mutex
	slots map[string]*commitGateSlot
}

// commitGateSlot is one branch's mutual-exclusion slot. `ch` is a 1-buffered
// channel used as a ctx-cancellable mutex; `waiters` refcounts the goroutines
// holding or queued on it so the map entry can be reclaimed.
type commitGateSlot struct {
	ch      chan struct{}
	waiters int
}

// acquire blocks until the caller owns the gate for key, then returns the
// release func. It returns ctx.Err() (without acquiring) if ctx is cancelled
// while queued, so a tenant-deleted preempt or a shutting-down consumer never
// parks forever behind another Org's commit.
//
// Callers MUST invoke the returned release exactly once — `defer release()` is
// the idiom. Release is itself idempotent.
func (g *perOrgCommitGate) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g.mu.Lock()
	if g.slots == nil {
		g.slots = make(map[string]*commitGateSlot)
	}
	slot, ok := g.slots[key]
	if !ok {
		slot = &commitGateSlot{ch: make(chan struct{}, 1)}
		g.slots[key] = slot
	}
	slot.waiters++
	g.mu.Unlock()

	select {
	case slot.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-slot.ch
				g.drop(key)
			})
		}, nil
	case <-ctx.Done():
		g.drop(key)
		return nil, ctx.Err()
	}
}

// drop decrements the slot's refcount and removes the map entry once nobody
// holds or waits on it.
func (g *perOrgCommitGate) drop(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	slot, ok := g.slots[key]
	if !ok {
		return
	}
	slot.waiters--
	if slot.waiters <= 0 {
		delete(g.slots, key)
	}
}
