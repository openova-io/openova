package events

import (
	"strings"
	"testing"
)

// TestNewMultiSubscriberRequiresTransport pins the safety check —
// silently constructing a no-op subscriber is the convergence-blocking
// failure mode this file exists to catch.
func TestNewMultiSubscriberRequiresTransport(t *testing.T) {
	if _, err := NewMultiSubscriber(MultiSubscriberConfig{Group: "g"}); err == nil {
		t.Fatal("NewMultiSubscriber(nil, nil) should return an error")
	}
}

// TestNewMultiSubscriberRequiresGroup ensures we never construct a
// subscriber whose durable consumers would collide with a sibling
// service's anonymous durable.
func TestNewMultiSubscriberRequiresGroup(t *testing.T) {
	if _, err := NewMultiSubscriber(MultiSubscriberConfig{
		EventTypes: []string{"tenant.created"},
	}); err == nil {
		t.Fatal("NewMultiSubscriber without Group should error")
	}
}

// TestNewMultiSubscriberNATSWithoutEventTypes — passing a NATS conn
// without any event types is not subscribable; without Kafka either
// the constructor refuses (would silently drop everything).
func TestNewMultiSubscriberNATSWithoutEventTypes(t *testing.T) {
	if _, err := NewMultiSubscriber(MultiSubscriberConfig{
		Group: "g",
		NATS:  &NATSConn{}, // js nil — but the empty EventTypes check fires first
	}); err == nil {
		t.Fatal("NewMultiSubscriber with NATS but no EventTypes should error")
	}
}

// TestMultiSubscriberDurableNameSanitises ensures the JetStream durable
// name conforms to the allowed character set (letters, digits, dash,
// underscore) even though canonical NATS subjects use dots.
func TestMultiSubscriberDurableNameSanitises(t *testing.T) {
	m := &MultiSubscriber{group: "provisioning"}
	cases := []struct{ subject, want string }{
		{"catalyst.tenant.created", "provisioning-catalyst-tenant-created"},
		{"catalyst.billing.order.placed", "provisioning-catalyst-billing-order-placed"},
		{"catalyst.tenant.app_install_requested", "provisioning-catalyst-tenant-app_install_requested"},
	}
	for _, c := range cases {
		got := m.durableName(c.subject)
		if got != c.want {
			t.Errorf("durableName(%q) = %q, want %q", c.subject, got, c.want)
		}
		// Double-check no dots survive.
		if strings.Contains(got, ".") {
			t.Errorf("durableName(%q) contains a dot: %q", c.subject, got)
		}
	}
}

// TestNilMultiSubscriberSubscribe guards the nil-receiver path so a
// caller that forgot to construct via NewMultiSubscriber doesn't panic
// on .Subscribe().
func TestNilMultiSubscriberSubscribe(t *testing.T) {
	var m *MultiSubscriber
	if err := m.Subscribe(nil, nil); err == nil {
		t.Fatal("nil MultiSubscriber.Subscribe should error")
	}
}
