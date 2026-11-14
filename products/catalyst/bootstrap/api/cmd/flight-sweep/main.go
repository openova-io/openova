// Command flight-sweep lists what a wiped deployment left in its cloud
// account, and the DNS names of it that still answer (dns.go). It only reads.
// Usage: flight-sweep -vars <variables document>
//
// The document is the deployment's tofu.auto.tfvars.json. Its keys provider,
// sovereign_fqdn and deployment_id name the deployment; every key with a
// string value goes to the provider as a credential. The report is printed as
// JSON on standard output; its list kinds ends with the entry dns_records, and
// leftoverCount counts the leftovers of every kind. Exit codes: 0 nothing was
// left; 1 leftovers were found; 2 usage error, or the document cannot be read
// or lacks one of the three keys; 3 not measured (the provider is unknown, has
// no leftover lister, a listing failed, or a DNS name could not be asked).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, providers.Get, systemAsker().countDNS))
}

func run(args []string, stdout, stderr io.Writer, get func(string) (providers.CloudProvider, error),
	countDNS func(context.Context, string) (*dnsKind, error)) int {
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "flight-sweep: "+format+"\n", a...)
		return code
	}
	fs := flag.NewFlagSet("flight-sweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	vars := fs.String("vars", "", "the deployment's variables document (tofu.auto.tfvars.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *vars == "" || fs.NArg() != 0 {
		return fail(2, "usage: flight-sweep -vars <variables document>")
	}
	data, err := os.ReadFile(*vars)
	if err != nil {
		return fail(2, "%v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fail(2, "%s is not a JSON object: %v", *vars, err)
	}
	raw := map[string]string{}
	for k, v := range doc {
		if s, ok := v.(string); ok {
			raw[k] = s
		}
	}
	for _, k := range []string{"provider", "sovereign_fqdn", "deployment_id"} {
		if strings.TrimSpace(raw[k]) == "" {
			return fail(2, "the document has no %s", k)
		}
	}
	p, err := get(raw["provider"])
	if err != nil {
		return fail(3, "not measured: %v", err)
	}
	lister, ok := p.(providers.LeftoverLister)
	if !ok {
		return fail(3, "not measured: provider %q has no leftover lister", raw["provider"])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := lister.ListLeftovers(ctx, providers.LeftoverSpec{
		DeploymentID: raw["deployment_id"], SovereignFQDN: raw["sovereign_fqdn"], Creds: providers.ProviderCreds{Raw: raw},
	})
	if err != nil {
		return fail(3, "not measured: %v", err)
	}
	dns, err := countDNS(ctx, raw["sovereign_fqdn"])
	if err != nil {
		return fail(3, "not measured: dns_records: %v", err)
	}
	kinds := make([]any, 0, len(rep.Kinds)+1)
	for _, k := range rep.Kinds {
		kinds = append(kinds, k)
	}
	kinds = append(kinds, dns)
	left := rep.LeftoverCount() + len(dns.Leftovers)
	out, _ := json.MarshalIndent(map[string]any{
		"provider": raw["provider"], "namePrefix": rep.NamePrefix, "leftoverCount": left, "kinds": kinds,
	}, "", "  ")
	_, _ = fmt.Fprintf(stdout, "%s\n", out)
	if left > 0 {
		return 1
	}
	return 0
}
