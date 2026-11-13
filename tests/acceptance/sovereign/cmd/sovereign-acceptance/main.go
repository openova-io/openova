// sovereign-acceptance — ADR-0015 D9: function is the acceptance criterion.
//
//	sovereign-acceptance run      [flags]   drive J1..J5 against a live Sovereign
//	sovereign-acceptance discover [flags]   read-only: resolve Org/Env/Regions + probe every endpoint the run uses
//	sovereign-acceptance mint-session --key <pem|-> --issuer <url> [--email e]
//
// See tests/acceptance/sovereign/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/session"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/suite"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mint-session":
		os.Exit(mint(os.Args[2:]))
	case "run", "discover":
		os.Exit(run(os.Args[1], os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sovereign-acceptance run|discover|mint-session [flags]  (see README.md)")
}

func mint(args []string) int {
	fs := flag.NewFlagSet("mint-session", flag.ExitOnError)
	keyPath := fs.String("key", "", "PEM private key of the Sovereign's handover signer ('-' = stdin)")
	issuer := fs.String("issuer", "", "session issuer, = the Sovereign's CATALYST_PIN_ISSUER (https://console.<fqdn>)")
	email := fs.String("email", "emrah.baysal@openova.io", "owner email")
	ttl := fs.Duration("ttl", 3*time.Hour, "session lifetime")
	_ = fs.Parse(args)
	var pemBytes []byte
	var err error
	if *keyPath == "-" {
		pemBytes, err = io.ReadAll(os.Stdin)
	} else {
		pemBytes, err = os.ReadFile(*keyPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mint-session:", err)
		return 2
	}
	k, err := session.ParseKey(pemBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mint-session:", err)
		return 2
	}
	tok, err := session.Mint(k, session.Claims{Issuer: *issuer, Email: *email, TTL: *ttl})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mint-session:", err)
		return 2
	}
	fmt.Print(tok)
	return 0
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func run(mode string, args []string) int {
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	cfg := suite.Config{}
	fs.StringVar(&cfg.Env, "env", env("ACCEPT_ENV", ""), "environment label as named in the UAT header, e.g. hw307")
	fs.StringVar(&cfg.Console, "console", env("ACCEPT_CONSOLE", ""), "console base URL (default https://console.<env>.omani.works)")
	fs.StringVar(&cfg.DeploymentID, "deployment", env("ACCEPT_DEPLOYMENT_ID", ""), "deployment id (default: GET /api/v1/sovereign/self)")
	fs.StringVar(&cfg.KubeconfigA, "kubeconfig-a", env("ACCEPT_KUBECONFIG_A", ""), "kubeconfig of the region that starts as Primary")
	fs.StringVar(&cfg.KubeconfigB, "kubeconfig-b", env("ACCEPT_KUBECONFIG_B", ""), "kubeconfig of the region that starts as Standby")
	fs.StringVar(&cfg.RegionA, "region-a", env("ACCEPT_REGION_A", ""), "region id A (default: first region of the topology)")
	fs.StringVar(&cfg.RegionB, "region-b", env("ACCEPT_REGION_B", ""), "region id B (default: second region of the topology)")
	fs.StringVar(&cfg.Org, "org", env("ACCEPT_ORG", ""), "Organization (default: the Sovereign's own internal Organization)")
	fs.StringVar(&cfg.Environment, "environment", env("ACCEPT_ENVIRONMENT", ""), "Environment (default: the Org's only multi-region Environment)")
	fs.StringVar(&cfg.Blueprint, "blueprint", env("ACCEPT_BLUEPRINT", "bp-wordpress-tenant"), "Blueprint under test")
	fs.StringVar(&cfg.AppName, "name", env("ACCEPT_APP_NAME", ""), "Application name (default accept-<unix-ts>)")
	fs.StringVar(&cfg.RegionIPs, "region-ips", env("ACCEPT_REGION_IPS", ""), "region→public IPs map for the DNS assertion: '<regionA>=ip,ip;<regionB>=ip'")
	fs.StringVar(&cfg.Journeys, "journeys", env("ACCEPT_JOURNEYS", "J1,J2,J3,J4,J5"), "comma list of journeys to run")
	fs.DurationVar(&cfg.InstallTimeout, "install-timeout", envDur("ACCEPT_INSTALL_TIMEOUT", 25*time.Minute), "J1 convergence timeout")
	fs.DurationVar(&cfg.MoveTimeout, "move-timeout", envDur("ACCEPT_MOVE_TIMEOUT", 20*time.Minute), "J3/J4 convergence timeout")
	fs.DurationVar(&cfg.DeleteTimeout, "delete-timeout", envDur("ACCEPT_DELETE_TIMEOUT", 10*time.Minute), "J5 removal timeout")
	fs.DurationVar(&cfg.CommitBudget, "commit-budget", envDur("ACCEPT_COMMIT_BUDGET", 5*time.Second), "max wall time of one WordPress write (ADR-0004 sync commit must not block)")
	fs.BoolVar(&cfg.InsecureAppTLS, "insecure-app-tls", envBool("ACCEPT_INSECURE_APP_TLS"), "skip TLS verification for the app FQDN (staging certificates)")
	fs.StringVar(&cfg.WPTablePrefix, "wp-table-prefix", env("ACCEPT_WP_TABLE_PREFIX", "wp_"), "WordPress table prefix")
	fs.StringVar(&cfg.DNSServer, "dns-server", env("ACCEPT_DNS_SERVER", ""), "resolver for the DNS assertion, host[:port] (default: system)")
	fs.StringVar(&cfg.RunIn, "run-in", env("ACCEPT_RUN_IN", "cloud"), "where the suite runs: local or cloud; it selects the gate checks by their runs_in list")
	fs.StringVar(&cfg.Manifest, "manifest", env("ACCEPT_MANIFEST", ""), "gate manifest as JSON (scripts/acceptance-gate.py export); empty: no gate check runs")
	fs.StringVar(&cfg.CandidateID, "candidate-id", env("ACCEPT_CANDIDATE_ID", ""), "candidate this run certifies (empty: the record certifies nothing)")
	fs.StringVar(&cfg.RepoRoot, "repo-root", env("ACCEPT_REPO_ROOT", "."), "checkout of the commit under test; its HEAD is the record's candidate.commit")
	fs.StringVar(&cfg.Capabilities, "capabilities", env("ACCEPT_CAPABILITIES", ""), "comma list of capability flags this run declares, e.g. multi-region")
	fs.StringVar(&cfg.NodesRequested, "nodes-requested", env("ACCEPT_NODES_REQUESTED", ""), "nodes the deployment request asked for, per region id: '<regionA>=6,<regionB>=6' (empty: check FND-G03 fails)")
	fs.StringVar(&cfg.PreviousDeploymentID, "previous-deployment-id", env("ACCEPT_PREVIOUS_DEPLOYMENT_ID", ""), "the deployment whose wipe this fire follows; give it with --previous-wipe-orphans (check FND-G13)")
	fs.StringVar(&cfg.PreviousWipeOrphans, "previous-wipe-orphans", env("ACCEPT_PREVIOUS_WIPE_ORPHANS", ""), "resources of that deployment that a listing of the cloud account still found after the wipe (check FND-G13 passes on 0)")
	fs.BoolVar(&cfg.Record, "record", false, "write docs/ledger/acceptance/<env>-<runid>.json")
	fs.StringVar(&cfg.RecordDir, "record-dir", env("ACCEPT_RECORD_DIR", "docs/ledger/acceptance"), "where --record writes")
	fs.BoolVar(&cfg.KeepOnFail, "keep-on-fail", envBool("ACCEPT_KEEP_ON_FAIL"), "skip J5 cleanup when an earlier journey failed (leave the app for debugging)")
	_ = fs.Parse(args)
	cfg.Session = os.Getenv("CATALYST_SESSION")
	cfg.ReadOnly = mode == "discover"
	if cfg.RunIn != string(suite.RunInLocal) && cfg.RunIn != string(suite.RunInCloud) {
		fmt.Fprintln(os.Stderr, "--run-in must be local or cloud")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := suite.Execute(ctx, cfg, os.Stdout)
	if errors.Is(err, suite.ErrManifest) {
		fmt.Fprintln(os.Stderr, "FATAL:", err)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FATAL:", err)
		return 3
	}
	if res.Result != "PASS" {
		return 1
	}
	return 0
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v == "1" || v == "true" || v == "yes"
}
