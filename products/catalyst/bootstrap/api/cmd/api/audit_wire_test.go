package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
)

func TestAuditEventSource(t *testing.T) {
	env := map[string]string{"SOVEREIGN_FQDN": " sov.example ", "SOVEREIGN_PRIMARY_REGION": "region-a"}
	if got := auditEventSource(func(k string) string { return env[k] }); got != "catalyst-api/sov.example/region-a" {
		t.Errorf("source %q", got)
	}
	if got := auditEventSource(func(string) string { return "" }); got != "catalyst-api/-/-" {
		t.Errorf("source without the variables %q", got)
	}
}

// Without CATALYST_NATS_URL the Bus has no publisher and says so.
func TestAuditBusWithoutNATSIsInProcess(t *testing.T) {
	t.Setenv("CATALYST_NATS_URL", "")
	t.Setenv("CATALYST_AUDIT_RING_CAPACITY", "2")
	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	if p := newRBACAuditPublisherFromEnv(log); p != nil {
		t.Fatalf("a publisher was built without CATALYST_NATS_URL: %+v", p)
	}
	bus := newAuditBusFromEnv(log)
	if bus.Transport() != audit.TransportInProcess {
		t.Errorf("transport %q, want %q", bus.Transport(), audit.TransportInProcess)
	}
	if err := bus.PublishStrict(context.Background(), audit.Event{AuditType: "break-glass"}); !errors.Is(err, audit.ErrNoPublisher) {
		t.Errorf("PublishStrict returned %v, want ErrNoPublisher", err)
	}
	for i := 0; i < 3; i++ {
		bus.Publish(context.Background(), audit.Event{AuditType: audit.AuditTypeRBACGrantCreated})
	}
	if got := bus.List("", nil, 10); len(got) != 2 {
		t.Errorf("a ring of capacity 2 holds %d events", len(got))
	}
	if !strings.Contains(logged.String(), `"transport":"in-process"`) || !strings.Contains(logged.String(), `"audit":true`) {
		t.Errorf("the log holds neither the transport nor an audit line: %s", logged.String())
	}
}

// A NATS server that does not answer at start must not be reported as wired.
func TestAuditBusWithUnreachableNATSIsInProcess(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALYST_NATS_URL", "nats://"+l.Addr().String())
	_ = l.Close()
	var logged bytes.Buffer
	bus := newAuditBusFromEnv(slog.New(slog.NewJSONHandler(&logged, nil)))
	if bus.Transport() != audit.TransportInProcess {
		t.Errorf("transport %q, want %q", bus.Transport(), audit.TransportInProcess)
	}
	if !strings.Contains(logged.String(), "audit: NATS publisher init failed") || !strings.Contains(logged.String(), `"level":"ERROR"`) {
		t.Errorf("the failure is not logged as an error: %s", logged.String())
	}
}
