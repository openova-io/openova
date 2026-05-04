// Verify the name-prefix fallback in hetzner.Purge against a REAL
// Hetzner project containing orphan resources whose names match the
// canonical catalyst-<fqdn> prefix but lack the label. This reproduces
// the production otech83 failure mode.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
)

func main() {
	tokenBytes, err := os.ReadFile("/tmp/.hcloud-otech-token")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read token: %v\n", err)
		os.Exit(2)
	}
	token := strings.TrimSpace(string(tokenBytes))
	fqdn := "otech90.omani.works"

	fmt.Printf("=== Running hetzner.Purge for fqdn=%s ===\n", fqdn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	report, err := hetzner.Purge(ctx, token, fqdn, func(msg string) {
		fmt.Printf("  [progress] %s\n", msg)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Purge returned error: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("\n=== PurgeReport ===\n")
	fmt.Printf("  Servers:        %v\n", report.Servers)
	fmt.Printf("  LoadBalancers:  %v\n", report.LoadBalancers)
	fmt.Printf("  Networks:       %v\n", report.Networks)
	fmt.Printf("  Firewalls:      %v\n", report.Firewalls)
	fmt.Printf("  SSHKeys:        %v\n", report.SSHKeys)
	fmt.Printf("  Total:          %d\n", report.Total())
	if len(report.Errors) > 0 {
		fmt.Printf("  Errors:\n")
		for _, e := range report.Errors {
			fmt.Printf("    - %s\n", e)
		}
	}
}
