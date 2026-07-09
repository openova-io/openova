package main

import (
	"strings"
	"testing"
)

// TestSMTPHostDefaultIsNotStalwart guards the #4919 regression: marketplace-api
// used to default SMTP_HOST to the in-cluster Stalwart Service
// (stalwart-mail.stalwart.svc.cluster.local), which does not exist on a fresh
// Sovereign, so the signup PIN silently black-holed. The compiled-in default
// must be empty (safe "no send" posture) and must never reference Stalwart.
func TestSMTPHostDefaultIsNotStalwart(t *testing.T) {
	if defaultSMTPHost != "" {
		t.Fatalf("defaultSMTPHost must be empty (safe no-send default), got %q", defaultSMTPHost)
	}
	if strings.Contains(strings.ToLower(defaultSMTPHost), "stalwart") {
		t.Fatalf("defaultSMTPHost must not reference the in-cluster Stalwart svc, got %q", defaultSMTPHost)
	}
}

// TestSMTPHostResolvesFromSeed proves SMTP_HOST resolves from the injected env
// value — the durable catalyst-system/sovereign-smtp-credentials seed
// (#4748/#4749 → mail.openova.io) that the chart wires in via secretKeyRef —
// and wins over the compiled-in default rather than the hardcoded stalwart host.
func TestSMTPHostResolvesFromSeed(t *testing.T) {
	const seedHost = "mail.openova.io"
	t.Setenv("SMTP_HOST", seedHost)

	got := getEnv("SMTP_HOST", defaultSMTPHost)
	if got != seedHost {
		t.Fatalf("SMTP_HOST should resolve from the seed-injected env value, got %q want %q", got, seedHost)
	}
	if got == "stalwart-mail.stalwart.svc.cluster.local" {
		t.Fatalf("SMTP_HOST resolved to the removed hardcoded stalwart svc")
	}
}

// TestSMTPHostFallsBackWhenUnset confirms that with no env injected (a Sovereign
// whose sovereign-smtp-credentials seed has not landed yet, so the optional
// secretKeyRef leaves SMTP_HOST unset) the resolver yields the empty safe
// default — never a dial against a dead host.
func TestSMTPHostFallsBackWhenUnset(t *testing.T) {
	t.Setenv("SMTP_HOST", "")

	if got := getEnv("SMTP_HOST", defaultSMTPHost); got != "" {
		t.Fatalf("with SMTP_HOST unset the resolver must yield the empty safe default, got %q", got)
	}
}
