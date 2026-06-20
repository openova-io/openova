package handlers

// renderTemplate unit tests, covering the D28 voucher-issued template
// and surrounding shape. Other templates are exercised by
// consumer_test.go (which runs them end-to-end via the events
// consumer); the voucher-issued template is exclusively reachable via
// the synchronous POST /notification/send path from org-billing, so we
// test it here directly.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderTemplate_VoucherIssued asserts the rendered HTML for the
// D28 voucher-issued template contains the code + credit + a redeem URL
// pointing at the supplied Sovereign FQDN, and that the default subject
// matches the founder-spec'd copy.
func TestRenderTemplate_VoucherIssued(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"code":           "GIFT-50",
		"credit_omr":     50,
		"description":    "Welcome to OpenOva",
		"sovereign_fqdn": "omani.works",
		"validity_hint":  "Use within 30 days",
	})

	body, subject, err := renderTemplate("voucher-issued", "", json.RawMessage(data))
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if subject != "You've been gifted a voucher for OpenOva" {
		t.Errorf("subject: got %q, want default D28 copy", subject)
	}

	// Must include the voucher code prominently.
	if !strings.Contains(body, "GIFT-50") {
		t.Error("rendered body missing voucher code")
	}
	// Must show the credit amount.
	if !strings.Contains(body, "50 OMR") {
		t.Errorf("rendered body missing '50 OMR' credit line: body=%s", body)
	}
	// Must include the description.
	if !strings.Contains(body, "Welcome to OpenOva") {
		t.Error("rendered body missing description")
	}
	// Must include the redeem URL built from the sovereign FQDN —
	// not hardcoded to openova.io.
	wantURL := "https://marketplace.omani.works/redeem/?code=GIFT-50"
	if !strings.Contains(body, wantURL) {
		t.Errorf("rendered body missing redeem URL %q", wantURL)
	}
	// Must NOT hardcode a different sovereign — guards against a
	// regression where someone substitutes openova.io for the per-
	// sovereign domain.
	if strings.Contains(body, "marketplace.openova.io/redeem") {
		t.Error("rendered body must use the per-sovereign FQDN, not marketplace.openova.io")
	}
}

// TestRenderTemplate_VoucherIssued_CustomSubject confirms an explicit
// caller-provided subject wins over the default.
func TestRenderTemplate_VoucherIssued_CustomSubject(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"code":           "ABC",
		"credit_omr":     10,
		"sovereign_fqdn": "example.test",
	})
	_, subject, err := renderTemplate("voucher-issued", "Custom subject", json.RawMessage(data))
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if subject != "Custom subject" {
		t.Errorf("subject: got %q, want %q", subject, "Custom subject")
	}
}

// TestRenderTemplate_VoucherIssued_NoDescription confirms an optional
// description is gracefully omitted — the email still renders cleanly.
func TestRenderTemplate_VoucherIssued_NoDescription(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"code":           "BARE",
		"credit_omr":     5,
		"sovereign_fqdn": "x.test",
	})
	body, _, err := renderTemplate("voucher-issued", "", json.RawMessage(data))
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if !strings.Contains(body, "BARE") {
		t.Error("rendered body missing code when description omitted")
	}
}

// TestRenderTemplate_UnknownTemplate keeps the negative-path coverage
// for renderTemplate alongside the voucher-issued tests.
func TestRenderTemplate_UnknownTemplate(t *testing.T) {
	_, _, err := renderTemplate("does-not-exist", "", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
	if !strings.Contains(err.Error(), "unknown template") {
		t.Errorf("error message should explain unknown template, got %q", err.Error())
	}
}
