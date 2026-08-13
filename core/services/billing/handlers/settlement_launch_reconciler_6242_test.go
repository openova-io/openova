// settlement_launch_reconciler_6242_test.go — #6242.
//
// The guard here is deliberately driven through the REAL settlement call site
// (dispatchOrderPlaced) and the REAL sweep (SettlementLaunchReconciler.Sweep)
// wired to the REAL launch leg (*Handler.launchTenant). Testing a stand-in
// launcher would pin a helper while leaving the call site — the thing that
// actually strands paid Organizations — unpinned, which is how #4956's happy
// path came to be the only thing covered.
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/services/billing/store"
)

// flakyTenantService is a stand-in for the tenant service that can be taken
// down and brought back up mid-test — the failure this whole mechanism exists
// to survive. It records every launch path it is asked for.
type flakyTenantService struct {
	mu      sync.Mutex
	up      bool
	touched []string
}

func newFlakyTenantService(up bool) (*flakyTenantService, *httptest.Server) {
	f := &flakyTenantService{up: up}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.up {
			// The shape of a tenant service mid-roll: reachable socket,
			// refusing work. Chosen over closing the listener because it is
			// the harder case — the request completes, so only the STATUS
			// distinguishes a lost launch from a delivered one.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.touched = append(f.touched, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"t","launched":true,"status":"provisioning"}`))
	}))
	return f, srv
}

func (f *flakyTenantService) setUp(up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up = up
}

func (f *flakyTenantService) launches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.touched))
	copy(out, f.touched)
	return out
}

// fakeAwaitingLaunchStore feeds the reconciler a fixed order set.
type fakeAwaitingLaunchStore struct {
	orders []store.Order
	err    error
	calls  int
}

func (f *fakeAwaitingLaunchStore) ListOrdersAwaitingLaunch(context.Context, time.Duration, int) ([]store.Order, error) {
	f.calls++
	return f.orders, f.err
}

// TestSettlementLaunch_RecoversAnOrgStrandedByALostLaunchCall is the whole
// point of #6242, walked in the order it happens in production:
//
//  1. the checkout settles while the tenant service is refusing work — the
//     launch call is LOST and the paid Org is stranded at pending_payment;
//  2. the tenant service comes back;
//  3. the sweep re-offers the launch and it lands.
//
// Step 1 is asserted, not assumed: if the settlement path somehow delivered
// the launch while the service was down, step 3 would prove nothing.
func TestSettlementLaunch_RecoversAnOrgStrandedByALostLaunchCall(t *testing.T) {
	tenantSvc, srv := newFlakyTenantService(false) // down at settlement time
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	order := &store.Order{ID: "ord-1", TenantID: "tnt-stranded", Status: "completed"}

	// 1. Settlement happens with the tenant service down. The real call site.
	h.dispatchOrderPlaced(order.TenantID, order)
	if got := tenantSvc.launches(); len(got) != 0 {
		t.Fatalf("precondition broken: launch was delivered while the tenant service was down: %v", got)
	}

	// 2. The tenant service recovers.
	tenantSvc.setUp(true)

	// 3. The sweep must find the settled order and re-offer the launch.
	rec := &SettlementLaunchReconciler{
		Store:    &fakeAwaitingLaunchStore{orders: []store.Order{*order}},
		Launcher: h,
	}
	delivered := rec.Sweep(context.Background())

	if delivered != 1 {
		t.Errorf("Sweep delivered %d launches, want 1", delivered)
	}
	got := tenantSvc.launches()
	want := "POST /tenant/internal/tenants/tnt-stranded/launch"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("stranded Org was not recovered: tenant service saw %v, want exactly [%q]", got, want)
	}
}

// TestSettlementLaunch_SweepDeduplicatesByTenant is the CONTROL that a sweep
// which simply POSTs for every row cannot pass.
//
// Three settled orders share one Organization (a plan plus two add-on
// purchases) and a fourth belongs to a different one. Correct behaviour is two
// calls, one per Organization — not four.
func TestSettlementLaunch_SweepDeduplicatesByTenant(t *testing.T) {
	tenantSvc, srv := newFlakyTenantService(true)
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	rec := &SettlementLaunchReconciler{
		Store: &fakeAwaitingLaunchStore{orders: []store.Order{
			{ID: "ord-1", TenantID: "tnt-a", Status: "completed"},
			{ID: "ord-2", TenantID: "tnt-a", Status: "completed"},
			{ID: "ord-3", TenantID: "tnt-b", Status: "completed"},
			{ID: "ord-4", TenantID: "tnt-a", Status: "completed"},
		}},
		Launcher: h,
	}

	if delivered := rec.Sweep(context.Background()); delivered != 2 {
		t.Errorf("Sweep delivered %d launches for 2 distinct Organizations, want 2", delivered)
	}
	got := tenantSvc.launches()
	if len(got) != 2 {
		t.Fatalf("tenant service saw %d launch calls for 2 Organizations, want 2: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Fatalf("duplicate launch call %q — the sweep does not de-duplicate by tenant: %v", g, got)
		}
		seen[g] = true
	}
	for _, want := range []string{
		"POST /tenant/internal/tenants/tnt-a/launch",
		"POST /tenant/internal/tenants/tnt-b/launch",
	} {
		if !seen[want] {
			t.Errorf("missing launch call %q: %v", want, got)
		}
	}
}

// TestSettlementLaunch_SweepSkipsOrdersWithNoTenant is the CONTROL for a row
// that is settled — sharing the suspect property — but has no Organization to
// launch. Posting for it would hit `/tenant/internal/tenants//launch`, which is
// a 400 on a real tenant service and a wasted call on any.
func TestSettlementLaunch_SweepSkipsOrdersWithNoTenant(t *testing.T) {
	tenantSvc, srv := newFlakyTenantService(true)
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	rec := &SettlementLaunchReconciler{
		Store: &fakeAwaitingLaunchStore{orders: []store.Order{
			{ID: "ord-orphan", TenantID: "", Status: "completed"},
		}},
		Launcher: h,
	}

	if delivered := rec.Sweep(context.Background()); delivered != 0 {
		t.Errorf("Sweep delivered %d launches for a tenant-less order, want 0", delivered)
	}
	if got := tenantSvc.launches(); len(got) != 0 {
		t.Fatalf("tenant service was called for a tenant-less order: %v", got)
	}
}

// TestSettlementLaunch_SweepReportsNoDeliveryWhenTheServiceIsDown pins that a
// failed pass reports ZERO delivered rather than counting attempts. The count
// is the only signal an operator has that the recovery is working; inflating it
// with attempts would be a verdict from absent evidence.
func TestSettlementLaunch_SweepReportsNoDeliveryWhenTheServiceIsDown(t *testing.T) {
	_, srv := newFlakyTenantService(false)
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	rec := &SettlementLaunchReconciler{
		Store: &fakeAwaitingLaunchStore{orders: []store.Order{
			{ID: "ord-1", TenantID: "tnt-a", Status: "completed"},
		}},
		Launcher: h,
	}
	if delivered := rec.Sweep(context.Background()); delivered != 0 {
		t.Errorf("Sweep reported %d delivered against a down tenant service, want 0", delivered)
	}
}

// TestSettlementLaunch_SweepSurvivesAStoreError pins that an unreadable orders
// table is a bad PASS, not a crash and not a claim. The next tick retries.
func TestSettlementLaunch_SweepSurvivesAStoreError(t *testing.T) {
	tenantSvc, srv := newFlakyTenantService(true)
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	rec := &SettlementLaunchReconciler{
		Store:    &fakeAwaitingLaunchStore{err: context.DeadlineExceeded},
		Launcher: h,
	}
	if delivered := rec.Sweep(context.Background()); delivered != 0 {
		t.Errorf("Sweep reported %d delivered after a store error, want 0", delivered)
	}
	if got := tenantSvc.launches(); len(got) != 0 {
		t.Fatalf("tenant service was called despite the store read failing: %v", got)
	}
}

// TestSettlementLaunch_RunSweepsBeforeItsFirstTick pins that a billing pod
// which restarted mid-settlement does not make the stranded customer wait a
// whole interval. Run must sweep immediately, then tick.
func TestSettlementLaunch_RunSweepsBeforeItsFirstTick(t *testing.T) {
	tenantSvc, srv := newFlakyTenantService(true)
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL}
	rec := &SettlementLaunchReconciler{
		Store: &fakeAwaitingLaunchStore{orders: []store.Order{
			{ID: "ord-1", TenantID: "tnt-a", Status: "completed"},
		}},
		Launcher: h,
		// An interval far longer than the test: any launch observed can only
		// have come from the pre-tick sweep.
		Interval: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rec.Run(ctx)
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		if len(tenantSvc.launches()) > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("Run made no launch call within 3s against a 1h interval — it waits for its first tick before sweeping")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}

// TestSettlementLaunch_RunIsInertWhenUnwired pins that a half-wired reconciler
// returns instead of panicking or spinning — the Catalyst-Zero / dev-loop case.
func TestSettlementLaunch_RunIsInertWhenUnwired(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&SettlementLaunchReconciler{Launcher: &Handler{}}).Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with no Store did not return promptly")
	}
}
