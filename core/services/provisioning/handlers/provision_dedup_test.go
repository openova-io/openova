package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// fakeDedupStore models the partial-unique-index dedup semantics of the real
// Mongo-backed store WITHOUT a live Mongo, so the #3744 double-fire →
// single-provision contract is deterministically testable. The first
// CreateProvisionIfAbsent for a tenant in an in-flight state wins; every
// subsequent one for the same tenant collides with store.ErrProvisionExists,
// exactly as the real partial unique index on (tenant_id, in-flight status)
// behaves. GetInFlightProvisionByTenant surfaces the winner to the loser.
type fakeDedupStore struct {
	mu sync.Mutex
	// inflight maps tenantID -> the provision that won the create race.
	inflight map[string]*store.Provision
	// createCalls counts every CreateProvisionIfAbsent attempt (winners + losers).
	createCalls int32
	// getInFlightErr, when set, is returned by GetInFlightProvisionByTenant to
	// exercise the lookup-failure branch.
	getInFlightErr error
	// dropInFlight, when true, makes GetInFlightProvisionByTenant return
	// (nil, nil) to model the "row terminated between collision and lookup" race.
	dropInFlight bool
}

func newFakeDedupStore() *fakeDedupStore {
	return &fakeDedupStore{inflight: map[string]*store.Provision{}}
}

func (f *fakeDedupStore) CreateProvisionIfAbsent(_ context.Context, p *store.Provision) error {
	atomic.AddInt32(&f.createCalls, 1)
	if p.TenantID == "" {
		return store.ErrProvisionExists // not reached in these tests; mirror real guard intent
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.inflight[p.TenantID]; exists {
		return store.ErrProvisionExists
	}
	if p.ID == "" {
		p.ID = "prov-" + p.TenantID
	}
	// Store a copy so the caller mutating its local pointer can't change what
	// the loser observes via GetInFlightProvisionByTenant.
	cp := *p
	f.inflight[p.TenantID] = &cp
	return nil
}

func (f *fakeDedupStore) GetInFlightProvisionByTenant(_ context.Context, tenantID string) (*store.Provision, error) {
	if f.getInFlightErr != nil {
		return nil, f.getInFlightErr
	}
	if f.dropInFlight {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.inflight[tenantID]; ok {
		return p, nil
	}
	return nil, nil
}

// TestDedupProvisionCreate_DoubleFire_SingleProvision is the direct #3744
// acceptance test: the credit-covered checkout fires the provision-create
// entrypoint TWICE (NATS order.placed event + POST /provisioning/start). The
// second call MUST NOT fork a second workflow — it must return the in-flight
// provision the first call inserted. Exactly one fork means exactly one Gitea
// commit, so the self-race that marked stranger redeems "failed" can't happen.
func TestDedupProvisionCreate_DoubleFire_SingleProvision(t *testing.T) {
	st := newFakeDedupStore()
	ctx := context.Background()
	const tenant = "walk-stranger-co"

	// First trigger (e.g. the NATS order.placed event).
	first := &store.Provision{TenantID: tenant, Status: "provisioning"}
	d1, err := dedupProvisionCreate(ctx, st, first, tenant)
	if err != nil {
		t.Fatalf("first dedupProvisionCreate errored: %v", err)
	}
	if !d1.fork {
		t.Fatal("first trigger must fork exactly one workflow, got fork=false")
	}
	if d1.provision != first {
		t.Fatalf("first trigger must return the freshly-inserted provision, got %+v", d1.provision)
	}

	// Second trigger (e.g. the belt-and-suspenders POST /provisioning/start),
	// same tenant — the double-fire the bug is about.
	second := &store.Provision{TenantID: tenant, Status: "provisioning"}
	d2, err := dedupProvisionCreate(ctx, st, second, tenant)
	if err != nil {
		t.Fatalf("second dedupProvisionCreate errored: %v", err)
	}
	if d2.fork {
		t.Fatal("second trigger MUST NOT fork a second workflow (the #3744 self-race), got fork=true")
	}
	// The loser must surface the WINNER's provision, not its own throwaway.
	if d2.provision == nil || d2.provision.ID != first.ID {
		t.Fatalf("second trigger must return the in-flight (winner) provision %q, got %+v", first.ID, d2.provision)
	}
}

// TestDedupProvisionCreate_ConcurrentDoubleFire_ExactlyOneFork proves the guard
// holds when both triggers land near-simultaneously — the actual race that
// produced two concurrent sme-tenants commits on hw158. Across N concurrent
// calls for one tenant, exactly ONE may return fork=true.
func TestDedupProvisionCreate_ConcurrentDoubleFire_ExactlyOneFork(t *testing.T) {
	st := newFakeDedupStore()
	ctx := context.Background()
	const tenant = "walk-stranger-co"
	const n = 8

	var forks int32
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start // line everyone up to maximize the race window
			d, err := dedupProvisionCreate(ctx, st, &store.Provision{TenantID: tenant, Status: "provisioning"}, tenant)
			if err != nil {
				t.Errorf("concurrent dedupProvisionCreate errored: %v", err)
				return
			}
			if d.fork {
				atomic.AddInt32(&forks, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&forks); got != 1 {
		t.Fatalf("exactly one of %d concurrent triggers may fork a workflow, got %d", n, got)
	}
	if got := atomic.LoadInt32(&st.createCalls); got != n {
		t.Fatalf("all %d triggers must attempt the insert, got %d", n, got)
	}
}

// TestDedupProvisionCreate_TerminatedBetweenCollisionAndLookup covers the benign
// race where the winning provision reaches a terminal state between the loser's
// collision and its in-flight lookup: the loser must still NOT fork (the prior
// workflow owns the outcome), returning its own built provision rather than nil.
func TestDedupProvisionCreate_TerminatedBetweenCollisionAndLookup(t *testing.T) {
	st := newFakeDedupStore()
	ctx := context.Background()
	const tenant = "walk-stranger-co"

	// Seed a winner so the next create collides...
	if _, err := dedupProvisionCreate(ctx, st, &store.Provision{TenantID: tenant, Status: "provisioning"}, tenant); err != nil {
		t.Fatalf("seed provision errored: %v", err)
	}
	// ...but make the lookup find no in-flight row (it just terminated).
	st.dropInFlight = true

	own := &store.Provision{ID: "loser-built", TenantID: tenant, Status: "provisioning"}
	d, err := dedupProvisionCreate(ctx, st, own, tenant)
	if err != nil {
		t.Fatalf("dedupProvisionCreate errored: %v", err)
	}
	if d.fork {
		t.Fatal("must NOT fork when a prior provision already terminated, got fork=true")
	}
	if d.provision != own {
		t.Fatalf("on a terminated-row race the loser returns its own built provision, got %+v", d.provision)
	}
}

// TestDedupProvisionCreate_LookupErrorPropagates ensures a failed in-flight
// lookup is surfaced (not swallowed) so the caller can fail loudly rather than
// silently fork or drop the request.
func TestDedupProvisionCreate_LookupErrorPropagates(t *testing.T) {
	st := newFakeDedupStore()
	ctx := context.Background()
	const tenant = "walk-stranger-co"
	if _, err := dedupProvisionCreate(ctx, st, &store.Provision{TenantID: tenant, Status: "provisioning"}, tenant); err != nil {
		t.Fatalf("seed provision errored: %v", err)
	}
	wantErr := context.DeadlineExceeded
	st.getInFlightErr = wantErr

	_, err := dedupProvisionCreate(ctx, st, &store.Provision{TenantID: tenant, Status: "provisioning"}, tenant)
	if err != wantErr {
		t.Fatalf("lookup error must propagate, got %v want %v", err, wantErr)
	}
}
