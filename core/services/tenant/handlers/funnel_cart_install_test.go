package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// recordingProducer captures every event Publish receives so a test can assert
// exactly which app-install dispatches the funnel cart emitted.
type recordingProducer struct {
	published []*events.Event
}

func (p *recordingProducer) Publish(_ context.Context, _ string, e *events.Event) error {
	p.published = append(p.published, e)
	return nil
}
func (p *recordingProducer) Close() {}

// appSlugsFromInstallEvents pulls the app_slug out of every
// tenant.app_install_requested event the producer recorded.
func appSlugsFromInstallEvents(t *testing.T, evs []*events.Event) []string {
	t.Helper()
	var out []string
	for _, e := range evs {
		if e.Type != "tenant.app_install_requested" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatalf("decode app_install_requested data: %v", err)
		}
		if s, ok := data["app_slug"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestDispatchFunnelCartInstall_DispatchesDeployableAppsSkipsSandbox proves the
// funnel cart-placement gap fix (#4360): a funnel Org created with cart apps
// emits a tenant.app_install_requested per cart app so the provisioning service
// renders the Applications into the per-Org gitops tree — the funnel analogue
// of the BSS door's Step-6. `sandbox` is excluded (it has its own
// tenant.sandbox_requested dispatch). With the Catalog unwired the helper
// dispatches the raw cart slugs (capacity was validated at checkout).
func TestDispatchFunnelCartInstall_DispatchesDeployableAppsSkipsSandbox(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{
		Producer: prod,
		// Catalog nil → raw-slug dispatch path; ProvisioningURL empty → the
		// HTTP leg errors (logged, non-fatal) and the event leg still fires.
	}

	tenant := &store.Tenant{
		ID:        "tid-1",
		Slug:      "acme",
		Subdomain: "acme",
		PlanID:    "plan-s",
		Apps:      []string{"wordpress", "sandbox", "umami"},
	}

	h.dispatchFunnelCartInstall(context.Background(), tenant)

	got := appSlugsFromInstallEvents(t, prod.published)
	want := map[string]bool{"wordpress": true, "umami": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d install dispatches, got %d (%v)", len(want), len(got), got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected dispatch for %q (sandbox must be excluded; only cart apps dispatched)", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("missing dispatch for cart app %q", s)
	}
}

// TestDispatchFunnelCartInstall_EmptyCartNoOp proves an Org created with no cart
// apps emits nothing (the funnel still mints the Org shell via tenant.created;
// this helper is purely additive for cart placement).
func TestDispatchFunnelCartInstall_EmptyCartNoOp(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}

	h.dispatchFunnelCartInstall(context.Background(), &store.Tenant{ID: "tid", Subdomain: "acme"})
	if len(prod.published) != 0 {
		t.Fatalf("expected zero events for empty cart, got %d", len(prod.published))
	}

	// nil tenant must not panic.
	h.dispatchFunnelCartInstall(context.Background(), nil)
}
