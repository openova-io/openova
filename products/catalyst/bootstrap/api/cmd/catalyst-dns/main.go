// catalyst-dns — small Go binary the OpenTofu module's null_resource.dns_pool
// invokes via local-exec when domain_mode=pool.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #3: cloud APIs are NOT called
// from bespoke Go in the catalyst-api process. The narrow exception is this
// binary, which is invoked by OpenTofu (the canonical IaC) as if it were a
// terraform-dynadot provider — the Dynadot terraform provider does not
// exist on the registry, so we ship our own helper. The contract here is
// the same as a terraform provider would expose: receive inputs via env
// vars, write the records, exit 0 on success.
//
// Inputs (env vars):
//   DYNADOT_API_KEY     — Dynadot account API key (account-scoped, covers
//                         every domain owned by the account)
//   DYNADOT_API_SECRET  — Dynadot account API secret
//   DOMAIN              — Pool domain (e.g. omani.works)
//   SUBDOMAIN           — Sovereign subdomain (e.g. omantel)
//   LB_IP               — Hetzner load-balancer IPv4 the records point at
//
// Output: writes the canonical 6-record set per dynadot.AddSovereignRecords:
//   *.<SUBDOMAIN>.<DOMAIN>          A → <LB_IP>
//   console.<SUBDOMAIN>.<DOMAIN>    A → <LB_IP>
//   gitea.<SUBDOMAIN>.<DOMAIN>      A → <LB_IP>
//   harbor.<SUBDOMAIN>.<DOMAIN>     A → <LB_IP>
//   admin.<SUBDOMAIN>.<DOMAIN>      A → <LB_IP>
//   api.<SUBDOMAIN>.<DOMAIN>        A → <LB_IP>
//
// Idempotent: re-running with the same inputs writes the same records again
// (Dynadot dedupes by (subdomain, type) under add_dns_to_current_setting=yes).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

// inputs captures the env-var contract documented at the top of this file.
// Splitting it out keeps main() small and lets tests construct a fixture
// without touching os.Environ().
type inputs struct {
	APIKey    string
	APISecret string
	Domain    string
	Subdomain string
	LBIP      string
}

// readInputsFromEnv returns the inputs struct populated from the canonical
// DYNADOT_* / DOMAIN / SUBDOMAIN / LB_IP env vars.
func readInputsFromEnv() inputs {
	return inputs{
		APIKey:    os.Getenv("DYNADOT_API_KEY"),
		APISecret: os.Getenv("DYNADOT_API_SECRET"),
		Domain:    os.Getenv("DOMAIN"),
		Subdomain: os.Getenv("SUBDOMAIN"),
		LBIP:      os.Getenv("LB_IP"),
	}
}

// validate enforces the input contract. Returned error is intended to be
// surfaced verbatim to the operator (matches the original messages so the
// existing OpenTofu error-handling continues to work).
func (in inputs) validate() error {
	if in.APIKey == "" || in.APISecret == "" {
		return fmt.Errorf("DYNADOT_API_KEY and DYNADOT_API_SECRET must be set")
	}
	if in.Domain == "" {
		return fmt.Errorf("DOMAIN must be set (e.g. omani.works)")
	}
	if in.Subdomain == "" {
		return fmt.Errorf("SUBDOMAIN must be set (e.g. omantel)")
	}
	if in.LBIP == "" {
		return fmt.Errorf("LB_IP must be set (the Hetzner load balancer IPv4)")
	}
	if !dynadot.IsManagedDomain(in.Domain) {
		return fmt.Errorf("DOMAIN %q is not in the managed-domain allowlist; refusing to write records", in.Domain)
	}
	return nil
}

// run is the testable core. It accepts an already-constructed Dynadot client
// so tests can inject a transport that rewrites requests at a httptest.Server,
// avoiding any real api.dynadot.com traffic.
func run(ctx context.Context, client *dynadot.Client, in inputs, stdout io.Writer) error {
	if err := in.validate(); err != nil {
		return err
	}
	if err := client.AddSovereignRecords(ctx, in.Domain, in.Subdomain, in.LBIP); err != nil {
		return fmt.Errorf("write DNS: %w", err)
	}
	fmt.Fprintf(stdout, "✓ Wrote 6 A records for *.%s.%s → %s via Dynadot\n", in.Subdomain, in.Domain, in.LBIP)
	return nil
}

func main() {
	in := readInputsFromEnv()
	// Validate first so we don't construct a client for a no-op run.
	if err := in.validate(); err != nil {
		fail(err.Error())
	}
	client := dynadot.New(in.APIKey, in.APISecret)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, client, in, os.Stdout); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintf(os.Stderr, "catalyst-dns: %s\n", msg)
	os.Exit(1)
}
