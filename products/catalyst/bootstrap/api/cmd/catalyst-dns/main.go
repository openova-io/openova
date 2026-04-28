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
	"os"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

func main() {
	apiKey := os.Getenv("DYNADOT_API_KEY")
	apiSecret := os.Getenv("DYNADOT_API_SECRET")
	domain := os.Getenv("DOMAIN")
	subdomain := os.Getenv("SUBDOMAIN")
	lbIP := os.Getenv("LB_IP")

	if apiKey == "" || apiSecret == "" {
		fail("DYNADOT_API_KEY and DYNADOT_API_SECRET must be set")
	}
	if domain == "" {
		fail("DOMAIN must be set (e.g. omani.works)")
	}
	if subdomain == "" {
		fail("SUBDOMAIN must be set (e.g. omantel)")
	}
	if lbIP == "" {
		fail("LB_IP must be set (the Hetzner load balancer IPv4)")
	}
	if !dynadot.IsManagedDomain(domain) {
		fail(fmt.Sprintf("DOMAIN %q is not in the managed-domain allowlist; refusing to write records", domain))
	}

	client := dynadot.New(apiKey, apiSecret)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := client.AddSovereignRecords(ctx, domain, subdomain, lbIP); err != nil {
		fail(fmt.Sprintf("write DNS: %v", err))
	}

	fmt.Printf("✓ Wrote 6 A records for *.%s.%s → %s via Dynadot\n", subdomain, domain, lbIP)
}

func fail(msg string) {
	fmt.Fprintf(os.Stderr, "catalyst-dns: %s\n", msg)
	os.Exit(1)
}
