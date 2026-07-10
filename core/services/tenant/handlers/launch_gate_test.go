package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// eventsByType returns every recorded event of the given type.
func eventsByType(evs []*events.Event, typ string) []*events.Event {
	var out []*events.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// TestLaunchTenant_FiresAllProvisioningTriggersFromPersistedFields is the
// core #4956 guard. It proves that launchTenant — the SINGLE place a funnel Org
// is provisioned, now invoked ONLY after billing settlement on the deferred
// path — emits every provisioning trigger using values read off the PERSISTED
// tenant (owner_email + agents), NOT the original HTTP request. The deferred
// launch runs server-to-server from billing with no user context, so if these
// fields weren't persisted the Organization CR would mint without an owner and
// the sandbox catalogue would be lost. This is what makes "a customer who
// orders WordPress gets WordPress" hold on the settled path.
func TestLaunchTenant_FiresAllProvisioningTriggersFromPersistedFields(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{
		Producer: prod,
		// Catalog nil → raw-slug cart dispatch; ProvisioningURL empty → the HTTP
		// install leg errors (logged, non-fatal) and the event leg still fires.
	}

	tenant := &store.Tenant{
		ID:           "tid-235",
		Slug:         "acme235",
		Name:         "ACME 235",
		OwnerID:      "user-abc",
		OwnerEmail:   "owner@acme235.example",
		PlanID:       "plan-m",
		ParentDomain: "omani.homes",
		Apps:         []string{"wordpress", "sandbox", "umami"},
		Agents:       []string{"claude-code", "qwen-code"},
	}

	h.launchTenant(context.Background(), tenant)

	// 1. Exactly one tenant.created, carrying the PERSISTED owner_email + apex.
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	var payload map[string]any
	if err := json.Unmarshal(created[0].Data, &payload); err != nil {
		t.Fatalf("decode tenant.created: %v", err)
	}
	if got := payload["owner_email"]; got != "owner@acme235.example" {
		t.Errorf("tenant.created owner_email: got %v, want the persisted owner email (deferred launch has no request claims)", got)
	}
	if got := payload["parent_domain"]; got != "omani.homes" {
		t.Errorf("tenant.created parent_domain: got %v, want omani.homes", got)
	}

	// 2. The purchased app stack is attached — one install per deployable cart
	//    app (sandbox excluded, it has its own dispatch).
	installs := appSlugsFromInstallEvents(t, prod.published)
	want := map[string]bool{"wordpress": true, "umami": true}
	if len(installs) != len(want) {
		t.Fatalf("expected %d cart installs, got %d (%v)", len(want), len(installs), installs)
	}
	for _, s := range installs {
		if !want[s] {
			t.Errorf("unexpected cart install %q (sandbox must be excluded)", s)
		}
	}

	// 3. Sandbox request fired with the PERSISTED agent catalogue.
	sb := eventsByType(prod.published, "tenant.sandbox_requested")
	if len(sb) != 1 {
		t.Fatalf("expected 1 tenant.sandbox_requested, got %d", len(sb))
	}
	var sbData map[string]any
	if err := json.Unmarshal(sb[0].Data, &sbData); err != nil {
		t.Fatalf("decode sandbox payload: %v", err)
	}
	agents, _ := sbData["agents"].([]any)
	if len(agents) != 2 {
		t.Errorf("sandbox agents: got %v, want the 2 persisted picks (agents lost if not persisted for deferred launch)", sbData["agents"])
	}
}

// TestLaunchTenant_NilTenantNoPanic guards the defensive nil path.
func TestLaunchTenant_NilTenantNoPanic(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), nil)
	if len(prod.published) != 0 {
		t.Fatalf("nil tenant must emit nothing, got %d events", len(prod.published))
	}
}

// TestLaunchTenant_NoSandboxWhenNotInCart proves the sandbox request is only
// emitted when the cart holds the sandbox product — a WordPress-only Org must
// not spuriously request a Sandbox.
func TestLaunchTenant_NoSandboxWhenNotInCart(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme",
		OwnerEmail: "o@x.example", Apps: []string{"wordpress"},
	})
	if n := len(eventsByType(prod.published, "tenant.sandbox_requested")); n != 0 {
		t.Fatalf("expected no sandbox request for a sandbox-free cart, got %d", n)
	}
	if n := len(eventsByType(prod.published, "tenant.created")); n != 1 {
		t.Fatalf("expected the Org shell to still launch (1 tenant.created), got %d", n)
	}
}
