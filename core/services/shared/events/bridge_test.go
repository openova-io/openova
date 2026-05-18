package events

import "testing"

// TestCanonicalSubject pins ADR-0001 §6 (`catalyst.<domain>.<event>`)
// — every existing call site relies on the mapping below so the
// fan-out publisher writes to the correct JetStream subject.
func TestCanonicalSubject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Tenant lifecycle events the tenant service emits.
		{"tenant.created", "catalyst.tenant.created"},
		{"tenant.deleted", "catalyst.tenant.deleted"},
		{"tenant.app_install_requested", "catalyst.tenant.app_install_requested"},
		{"tenant.app_uninstall_requested", "catalyst.tenant.app_uninstall_requested"},
		// Billing event — the only `order.*` producer today is billing,
		// so the canonical subject puts it under the billing domain
		// (see CanonicalSubject for the rationale).
		{"order.placed", "catalyst.billing.order.placed"},
		// Already-canonical subjects pass through.
		{"catalyst.usage.recorded", "catalyst.usage.recorded"},
		// Empty in → empty out (NATS leg skipped).
		{"", ""},
		// Generic platform event.
		{"domain.verified", "catalyst.domain.verified"},
	}
	for _, c := range cases {
		got := CanonicalSubject(c.in)
		if got != c.want {
			t.Errorf("CanonicalSubject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNewMultiPublisherRequiresAtLeastOne pins the safety check —
// silently constructing a no-op publisher is the exact failure mode
// that blocked Sovereign convergence (events were dropped on the floor
// when REDPANDA_BROKERS pointed at a non-existent broker and NATS_URL
// was unset).
func TestNewMultiPublisherRequiresAtLeastOne(t *testing.T) {
	if _, err := NewMultiPublisher(nil, nil); err == nil {
		t.Fatal("NewMultiPublisher(nil, nil) should return an error")
	}
}
