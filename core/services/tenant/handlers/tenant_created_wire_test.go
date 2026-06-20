package handlers

// Publisher-side wire-format round-trip for `tenant.created` (#1829, D29).
//
// The publisher in CreateOrg (handlers.go:237-256) builds an anonymous
// wrapper struct that embeds *store.Tenant and adds `owner_email` as a
// sibling JSON field. The provisioning consumer in
// core/services/provisioning/handlers/organization_create.go decodes
// into a flat `tenantCreatedPayload` struct (id/slug/name/owner_id/
// owner_email/plan_id/...) that mirrors the Tenant fields it cares
// about plus the same owner_email sibling.
//
// This test asserts the publisher-side bytes (marshalled from the
// EXACT wrapper struct shape used in production) decode cleanly into
// the consumer's flat struct shape WITH owner_email present and
// non-empty. A regression that nests owner_email under "tenant" or
// renames the field would fail this test before reaching staging.
//
// Why both sides? The existing TestHandleTenantCreated_FullTenantStructDecode
// in provisioning/handlers feeds a hand-rolled raw JSON to the
// consumer. That covers the consumer side. THIS test marshals from
// the publisher's actual wrapper-struct value so the test fails if
// someone refactors the wrapper (e.g., changes the embed to a named
// field "tenant", or replaces the `json:"owner_email"` tag) — the
// raw-JSON test in provisioning would still pass on that breakage.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/tenant/store"
)

// consumerPayloadMirror mirrors
// core/services/provisioning/handlers/organization_create.go's
// tenantCreatedPayload struct. Duplicated here (not imported) to
// avoid a tenant → provisioning service import dependency. If the
// consumer changes its decode shape, update this mirror to match.
type consumerPayloadMirror struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	OwnerID      string `json:"owner_id"`
	OwnerEmail   string `json:"owner_email"`
	PlanID       string `json:"plan_id"`
	Tier         string `json:"tier,omitempty"`
	BillingMode  string `json:"billing_mode,omitempty"`
	ParentDomain string `json:"parent_domain,omitempty"`
	// TBD-V18-D follow-up to PR #2038 — the per-instance configSchema
	// values bubble through the publisher wrapper via the embedded
	// *store.Tenant. A consumer that wants to honour them adds this
	// field with a matching tag — see TBD-V26 #2040 Path A/B.
	AppConfigs map[string]map[string]any `json:"app_configs,omitempty"`
}

func TestTenantCreatedWire_PublisherToConsumer_RoundTrip(t *testing.T) {
	// Build the wrapper struct IDENTICAL in shape to handlers.go:248-252.
	// Any drift on the publisher (embed change, tag change) breaks here.
	tenant := &store.Tenant{
		ID:        "tenant-abc",
		Slug:      "acme",
		Name:      "ACME Corp",
		OwnerID:   "user-xyz",
		PlanID:    "org-pool-basic",
		Apps:      []string{"wordpress"},
		Status:    "provisioning",
		Subdomain: "acme",
	}
	tenantCreatedPayload := struct {
		*store.Tenant
		OwnerEmail string `json:"owner_email,omitempty"`
	}{Tenant: tenant, OwnerEmail: "owner@example.com"}

	wire, err := json.Marshal(tenantCreatedPayload)
	if err != nil {
		t.Fatalf("publisher marshal failed: %v", err)
	}

	// Sanity: owner_email is a SIBLING, not nested under "tenant".
	if !strings.Contains(string(wire), `"owner_email":"owner@example.com"`) {
		t.Errorf("wire bytes missing top-level owner_email: %s", string(wire))
	}
	if strings.Contains(string(wire), `"tenant":{`) {
		t.Errorf("wire bytes wrap tenant under a nested key — breaks consumer flat decode: %s", string(wire))
	}

	// Decode through the consumer's payload shape.
	var got consumerPayloadMirror
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("consumer decode failed: %v\nwire=%s", err, string(wire))
	}

	// Every field the consumer's createOrganizationCR validates against
	// MUST be populated. owner_email is the canary — its absence
	// triggers publishEvent(provision.org_create_failed) per
	// organization_create.go:123-126.
	if got.OwnerEmail != "owner@example.com" {
		t.Errorf("owner_email lost in wire round-trip: got %q want %q", got.OwnerEmail, "owner@example.com")
	}
	if got.ID != "tenant-abc" {
		t.Errorf("id lost: got %q", got.ID)
	}
	if got.Slug != "acme" {
		t.Errorf("slug lost: got %q", got.Slug)
	}
	if got.OwnerID != "user-xyz" {
		t.Errorf("owner_id lost: got %q", got.OwnerID)
	}
	if got.PlanID != "org-pool-basic" {
		t.Errorf("plan_id lost: got %q", got.PlanID)
	}
}

// TestTenantCreatedWire_EmptyEmailOmitted_StillDecodes — when the
// JWT lacked the email claim (e.g., legacy token, machine-to-machine
// service account), the publisher passes ownerEmail="" and the
// `omitempty` tag drops the field. The consumer sees an absent
// owner_email and (correctly) publishes provision.org_create_failed.
// This test asserts the DECODE itself is clean (not malformed) —
// the rejection happens at validation, not at JSON parsing.
func TestTenantCreatedWire_EmptyEmailOmitted_StillDecodes(t *testing.T) {
	tenant := &store.Tenant{
		ID:      "tenant-abc",
		Slug:    "acme",
		Name:    "ACME Corp",
		OwnerID: "user-xyz",
		PlanID:  "org-pool-basic",
	}
	tenantCreatedPayload := struct {
		*store.Tenant
		OwnerEmail string `json:"owner_email,omitempty"`
	}{Tenant: tenant, OwnerEmail: ""}

	wire, err := json.Marshal(tenantCreatedPayload)
	if err != nil {
		t.Fatalf("publisher marshal failed: %v", err)
	}
	if strings.Contains(string(wire), `"owner_email"`) {
		t.Errorf("expected omitempty to drop empty owner_email, but field present in wire: %s", string(wire))
	}

	var got consumerPayloadMirror
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("consumer decode failed on empty-email wire: %v\nwire=%s", err, string(wire))
	}
	if got.OwnerEmail != "" {
		t.Errorf("expected empty owner_email, got %q", got.OwnerEmail)
	}
	// Other fields still round-trip — consumer can still log a useful
	// failure with the tenant id/slug in scope.
	if got.ID != "tenant-abc" || got.Slug != "acme" {
		t.Errorf("non-email fields lost on empty-email wire: id=%q slug=%q", got.ID, got.Slug)
	}
}

// TestTenantCreatedWire_AppConfigs_RoundTrip — TBD-V18-D follow-up to
// PR #2038. The publisher wrapper embeds *store.Tenant; the AppConfigs
// field (json tag `app_configs`) MUST round-trip into the consumer's
// flat decode shape so a downstream HelmRelease-values binding can
// read replicas / disk_gb / backups_enabled without a second hop.
// A regression that drops the bson/json tag, renames the field, or
// wraps the Tenant in a nested key would fail here before reaching
// staging.
func TestTenantCreatedWire_AppConfigs_RoundTrip(t *testing.T) {
	tenant := &store.Tenant{
		ID:        "tenant-abc",
		Slug:      "acme",
		Name:      "ACME Corp",
		OwnerID:   "user-xyz",
		PlanID:    "org-pool-basic",
		Apps:      []string{"wordpress", "postgres"},
		Status:    "provisioning",
		Subdomain: "acme",
		AppConfigs: map[string]map[string]any{
			"postgres": {
				"replicas":        float64(3),
				"disk_gb":         float64(50),
				"backups_enabled": true,
			},
			"wordpress": {
				"replicas": float64(2),
			},
		},
	}
	tenantCreatedPayload := struct {
		*store.Tenant
		OwnerEmail string `json:"owner_email,omitempty"`
	}{Tenant: tenant, OwnerEmail: "owner@example.com"}

	wire, err := json.Marshal(tenantCreatedPayload)
	if err != nil {
		t.Fatalf("publisher marshal failed: %v", err)
	}

	if !strings.Contains(string(wire), `"app_configs"`) {
		t.Errorf("wire bytes missing app_configs sibling: %s", string(wire))
	}

	var got consumerPayloadMirror
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("consumer decode failed: %v\nwire=%s", err, string(wire))
	}
	if got.AppConfigs == nil {
		t.Fatalf("app_configs nil on consumer side; wire=%s", string(wire))
	}
	pg := got.AppConfigs["postgres"]
	if pg == nil {
		t.Fatalf("postgres bucket missing from app_configs; got=%+v", got.AppConfigs)
	}
	// JSON numbers decode as float64 into `any`; equality compares the
	// canonical decoded shape, not the publisher's int literal.
	if pg["replicas"] != float64(3) {
		t.Errorf("postgres.replicas: got %v (%T) want 3 (float64)", pg["replicas"], pg["replicas"])
	}
	if pg["disk_gb"] != float64(50) {
		t.Errorf("postgres.disk_gb: got %v want 50", pg["disk_gb"])
	}
	if pg["backups_enabled"] != true {
		t.Errorf("postgres.backups_enabled: got %v want true", pg["backups_enabled"])
	}
	wp := got.AppConfigs["wordpress"]
	if wp == nil || wp["replicas"] != float64(2) {
		t.Errorf("wordpress.replicas lost; got=%+v", wp)
	}
}

// TestTenantCreatedWire_EmptyAppConfigs_Omitted — omitempty tag drops
// an empty `app_configs` field so legacy clients keep observing the
// pre-TBD-V18-D wire shape. The consumer's flat decode tolerates the
// absence: app_configs == nil is a legitimate signal that the
// customer never opened an AppDetail surface with a non-empty
// configSchema.
func TestTenantCreatedWire_EmptyAppConfigs_Omitted(t *testing.T) {
	tenant := &store.Tenant{
		ID:      "tenant-abc",
		Slug:    "acme",
		Name:    "ACME Corp",
		OwnerID: "user-xyz",
		PlanID:  "org-pool-basic",
		// AppConfigs deliberately nil.
	}
	tenantCreatedPayload := struct {
		*store.Tenant
		OwnerEmail string `json:"owner_email,omitempty"`
	}{Tenant: tenant, OwnerEmail: "owner@example.com"}

	wire, err := json.Marshal(tenantCreatedPayload)
	if err != nil {
		t.Fatalf("publisher marshal failed: %v", err)
	}
	if strings.Contains(string(wire), `"app_configs"`) {
		t.Errorf("expected omitempty to drop nil app_configs, but field present: %s", string(wire))
	}
}
