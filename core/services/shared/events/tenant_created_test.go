package events

import (
	"encoding/json"
	"testing"
)

// TestTenantCreatedPayload_WireCompat locks the JSON shape so adopting the
// shared struct on the producer + consumer is wire-compatible with the
// historical `tenant.created` envelope (#3687 fold #3690/#3673). The tags
// MUST stay snake_case so an in-flight event published by the old producer
// still decodes, and vice-versa.
func TestTenantCreatedPayload_WireCompat(t *testing.T) {
	p := NewTenantCreatedPayload(
		"tid-1", "acme", "ACME Corp", "owner-uuid", "ceo@acme.com",
		"plan-pro", "sme", "real", "omani.homes")

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Decode into a map to assert the on-wire key names.
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for k, want := range map[string]string{
		"id":            "tid-1",
		"slug":          "acme",
		"name":          "ACME Corp",
		"owner_id":      "owner-uuid",
		"owner_email":   "ceo@acme.com",
		"plan_id":       "plan-pro",
		"tier":          "sme",
		"billing_mode":  "real",
		"parent_domain": "omani.homes",
	} {
		if got, _ := m[k].(string); got != want {
			t.Errorf("wire key %q = %q, want %q", k, got, want)
		}
	}

	// Round-trip back into the struct.
	var back TenantCreatedPayload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if back != p {
		t.Errorf("round-trip mismatch: got %+v want %+v", back, p)
	}
}

// TestNewTenantCreatedPayload_TrimsWhitespace ensures the constructor
// normalizes inputs so a slug with stray whitespace never reaches the
// org-controller's path-component validator.
func TestNewTenantCreatedPayload_TrimsWhitespace(t *testing.T) {
	p := NewTenantCreatedPayload("  tid  ", "  acme ", " ACME ", " uid ", " a@b.c ", " plan ", " sme ", " real ", " omani.homes ")
	if p.Slug != "acme" || p.ID != "tid" || p.OwnerEmail != "a@b.c" || p.Tier != "sme" {
		t.Errorf("constructor must trim whitespace, got %+v", p)
	}
}

// TestTenantCreatedPayload_OptionalFieldsOmitEmpty proves tier/billing/
// parent omit when empty (the SME-pool default path), so the consumer's
// canonical defaulting applies rather than an explicit empty string.
func TestTenantCreatedPayload_OptionalFieldsOmitEmpty(t *testing.T) {
	p := NewTenantCreatedPayload("tid", "acme", "ACME", "uid", "a@b.c", "plan", "", "", "")
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"tier", "billing_mode", "parent_domain"} {
		if _, present := m[k]; present {
			t.Errorf("empty optional field %q must be omitted from the wire", k)
		}
	}
	// Required identity fields are always present.
	for _, k := range []string{"id", "slug", "owner_email"} {
		if _, present := m[k]; !present {
			t.Errorf("required field %q must always be on the wire", k)
		}
	}
}
