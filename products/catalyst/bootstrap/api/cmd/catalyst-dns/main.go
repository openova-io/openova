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

// runArgs is the resolved configuration for a single catalyst-dns run.
// Pulled out of main() so tests can drive the same logic without setenv
// gymnastics — the binary's main() reads env into a runArgs and hands it
// to run().
type runArgs struct {
	APIKey    string
	APISecret string
	Domain    string
	Subdomain string
	LBIP      string
}

// run does the actual work: validates the args, builds a dynadot client,
// writes the canonical 6-record set. Returns the success message (so callers
// can choose where to print it) or an error.
//
// The client is built via newClient — tests substitute a client whose HTTP
// transport is rewritten to a httptest.Server, so we never hit the real
// Dynadot endpoint.
func run(ctx context.Context, args runArgs, newClient func(apiKey, apiSecret string) *dynadot.Client) (string, error) {
	if args.APIKey == "" || args.APISecret == "" {
		return "", fmt.Errorf("DYNADOT_API_KEY and DYNADOT_API_SECRET must be set")
	}
	if args.Domain == "" {
		return "", fmt.Errorf("DOMAIN must be set (e.g. omani.works)")
	}
	if args.Subdomain == "" {
		return "", fmt.Errorf("SUBDOMAIN must be set (e.g. omantel)")
	}
	if args.LBIP == "" {
		return "", fmt.Errorf("LB_IP must be set (the Hetzner load balancer IPv4)")
	}
	if !dynadot.IsManagedDomain(args.Domain) {
		return "", fmt.Errorf("DOMAIN %q is not in the managed-domain allowlist; refusing to write records", args.Domain)
	}

	client := newClient(args.APIKey, args.APISecret)
	if err := client.AddSovereignRecords(ctx, args.Domain, args.Subdomain, args.LBIP); err != nil {
		return "", fmt.Errorf("write DNS: %w", err)
	}
	return fmt.Sprintf("✓ Wrote 6 A records for *.%s.%s → %s via Dynadot\n", args.Subdomain, args.Domain, args.LBIP), nil
}

// runFromEnv is the production entry point — reads env vars into a runArgs
// and invokes run() with the real Dynadot client constructor.
func runFromEnv(ctx context.Context, stdout, stderr io.Writer) int {
	args := runArgs{
		APIKey:    os.Getenv("DYNADOT_API_KEY"),
		APISecret: os.Getenv("DYNADOT_API_SECRET"),
		Domain:    os.Getenv("DOMAIN"),
		Subdomain: os.Getenv("SUBDOMAIN"),
		LBIP:      os.Getenv("LB_IP"),
	}
	msg, err := run(ctx, args, dynadot.New)
	if err != nil {
		fmt.Fprintf(stderr, "catalyst-dns: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, msg)
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	os.Exit(runFromEnv(ctx, os.Stdout, os.Stderr))
}
