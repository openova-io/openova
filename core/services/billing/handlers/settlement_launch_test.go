package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// capturingProducer records every published event so a test can assert the
// order.placed settlement event fired.
type capturingProducer struct {
	mu        sync.Mutex
	published []*events.Event
}

func (p *capturingProducer) Publish(_ context.Context, _ string, e *events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, e)
	return nil
}
func (p *capturingProducer) Close() {}

func (p *capturingProducer) types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.published {
		out = append(out, e.Type)
	}
	return out
}

// TestDispatchOrderPlaced_LaunchesDeferredOrgOnSettlement is the billing half
// of the #4956 settlement gate. dispatchOrderPlaced is the SINGLE point both
// settlement paths (credit-only checkout AND the Stripe checkout.session.
// completed webhook) converge on, so it must POST the tenant-service internal
// launch endpoint — the ONLY caller that can launch a deferred (pending_payment)
// funnel Org. If this call were missing, a paid funnel Org would never
// provision (on a Sovereign the order.placed event alone is a no-op observer).
//
// A checkout that 400s never reaches this function, so the Org stays parked —
// that is the integrity guarantee this test locks in from the billing side.
func TestDispatchOrderPlaced_LaunchesDeferredOrgOnSettlement(t *testing.T) {
	const tenantID = "tid-235"

	var (
		mu         sync.Mutex
		launchHits int
		launchPath string
		launchVerb string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/subdomain"):
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","subdomain":"acme235"}`))
		case strings.HasSuffix(r.URL.Path, "/app-configs"):
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","app_configs":{}}`))
		case strings.HasSuffix(r.URL.Path, "/launch"):
			mu.Lock()
			launchHits++
			launchPath = r.URL.Path
			launchVerb = r.Method
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","launched":true,"status":"provisioning"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	prod := &capturingProducer{}
	h := &Handler{Producer: prod, TenantURL: srv.URL}

	order := &store.Order{
		ID: "order-1", CustomerID: "cust-1", TenantID: tenantID,
		PlanID: "plan-m", AmountOMR: 0, Status: "completed",
	}

	h.dispatchOrderPlaced(tenantID, order)

	// The settlement event fired…
	var sawOrderPlaced bool
	for _, ty := range prod.types() {
		if ty == "order.placed" {
			sawOrderPlaced = true
		}
	}
	if !sawOrderPlaced {
		t.Errorf("expected an order.placed event to be published on settlement, got %v", prod.types())
	}

	// …AND the deferred Org was launched via the internal endpoint.
	mu.Lock()
	defer mu.Unlock()
	if launchHits != 1 {
		t.Fatalf("expected exactly 1 launch POST on settlement, got %d", launchHits)
	}
	if launchVerb != http.MethodPost {
		t.Errorf("launch must be a POST, got %s", launchVerb)
	}
	wantPath := "/tenant/internal/tenants/" + tenantID + "/launch"
	if launchPath != wantPath {
		t.Errorf("launch path: got %q, want %q", launchPath, wantPath)
	}
}

// TestLaunchTenant_NoopWhenTenantURLUnset proves the settlement launch call is
// inert when the tenant URL isn't wired (Catalyst-Zero / tests) — it must never
// panic or block, mirroring the existing lookupTenantSubdomain guard.
func TestLaunchTenant_NoopWhenTenantURLUnset(t *testing.T) {
	h := &Handler{}
	h.launchTenant("tid") // TenantURL empty → no-op, must not panic.
}
