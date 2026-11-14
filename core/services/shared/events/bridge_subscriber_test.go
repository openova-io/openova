package events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
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

// boundJetStream answers the Stream lookup of ensureOrgStream. Every other
// method of the embedded interface is nil and panics when called, so a test
// fails if ensureOrgStream tries to create or change a Stream.
type boundJetStream struct {
	jetstream.JetStream
	stream jetstream.Stream
	err    error
	looked []string
}

func (b *boundJetStream) Stream(_ context.Context, name string) (jetstream.Stream, error) {
	b.looked = append(b.looked, name)
	return b.stream, b.err
}

// boundStream is a declared Stream with the given subject filters.
type boundStream struct {
	jetstream.Stream
	subjects []string
}

func (s boundStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: jetstream.StreamConfig{Name: StreamCatalystOrg, Subjects: s.subjects}}
}

// TestEnsureOrgStreamBindsToTheDeclaredStream pins the contract of
// ensureOrgStream: it looks the declared Stream up by name, accepts it when
// it carries the requested subjects, and fails, without creating anything,
// when the Stream is absent or lacks a subject.
func TestEnsureOrgStreamBindsToTheDeclaredStream(t *testing.T) {
	ctx := context.Background()
	declared := &boundJetStream{stream: boundStream{subjects: []string{"catalyst.>"}}}
	if err := (&NATSConn{js: declared}).ensureOrgStream(ctx, "", nil); err != nil {
		t.Fatalf("declared stream: got %v, want nil", err)
	}
	if len(declared.looked) != 1 || declared.looked[0] != StreamCatalystOrg {
		t.Fatalf("looked up %v, want [%s]", declared.looked, StreamCatalystOrg)
	}

	absent := &boundJetStream{err: jetstream.ErrStreamNotFound}
	err := (&NATSConn{js: absent}).ensureOrgStream(ctx, StreamCatalystOrg, []string{"catalyst.>"})
	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatalf("absent stream: got %v, want an error wrapping jetstream.ErrStreamNotFound", err)
	}

	narrow := &boundJetStream{stream: boundStream{subjects: []string{"catalyst.tenant.>"}}}
	if err := (&NATSConn{js: narrow}).ensureOrgStream(ctx, "", nil); err == nil || !strings.Contains(err.Error(), "does not carry subject catalyst.>") {
		t.Fatalf("stream without the subject: got %v, want a refusal", err)
	}

	if _, err := NewMultiSubscriber(MultiSubscriberConfig{
		Group:      "g",
		NATS:       &NATSConn{js: &boundJetStream{err: jetstream.ErrStreamNotFound}},
		EventTypes: []string{"tenant.created"},
	}); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatalf("NewMultiSubscriber with the stream absent: got %v, want an error wrapping jetstream.ErrStreamNotFound", err)
	}
}
