// Command cert-manager-dynadot-webhook is the cert-manager external
// DNS-01 webhook for Dynadot.
//
// It implements the cert-manager webhook contract documented at
// https://cert-manager.io/docs/configuration/acme/dns01/webhook/ and
// uses the canonical Dynadot HTTP client at
// github.com/openova-io/openova/core/pkg/dynadot-client to perform the
// underlying record mutations.
//
// Why this binary exists separately from external-dns-dynadot-webhook:
// the external-dns webhook contract is a different protocol (records.list /
// records.add / records.delete RPCs) — see
// platform/cert-manager/chart/templates/clusterissuer-letsencrypt-dns01.yaml
// for the historical context. cert-manager's webhook is an aggregated
// apiserver registered via APIService, served on TCP/443 with mTLS, and
// receives ChallengeRequest objects for Present/CleanUp.
//
// Configuration is environment-variable driven so a Sovereign overlay can
// retune the binary without rebuilding the image (per
// docs/INVIOLABLE-PRINCIPLES.md #4):
//
//	GROUP_NAME                — webhook API group, default
//	                             "acme.dynadot.openova.io". MUST match the
//	                             ClusterIssuer's solvers[].dns01.webhook.groupName.
//	DYNADOT_API_KEY           — Dynadot api3.json API key. REQUIRED.
//	DYNADOT_API_SECRET        — Dynadot api3.json API secret. REQUIRED.
//	DYNADOT_MANAGED_DOMAINS   — comma- or whitespace-separated allowlist
//	                             of pool domains the webhook is permitted
//	                             to mutate (e.g.
//	                             "openova.io,omani.works,omanyx.works").
//	                             REQUIRED for production; allowlist is a
//	                             defence against a misconfigured or stolen
//	                             ClusterIssuer pointing at a third-party
//	                             domain. Single-domain operators may set
//	                             DYNADOT_DOMAIN as a fallback.
//	DYNADOT_DOMAIN            — optional single-domain fallback when
//	                             DYNADOT_MANAGED_DOMAINS is empty. Honoured
//	                             for parity with pool-domain-manager (#108).
//	DYNADOT_BASE_URL          — override for tests; production uses
//	                             https://api.dynadot.com/api3.json.
//
// At Present time the webhook splits the ChallengeRequest's ResolvedFQDN
// into (subdomain, apex) by matching the apex against the managed-domains
// allowlist, then writes a TXT record at `_acme-challenge.<subdomain>`
// using AddRecord (append-only path — never wipes the zone, see
// core/pkg/dynadot-client/doc.go safety contract). At CleanUp it does a
// safe read-modify-write via RemoveSubRecord.
//
// Idempotency: cert-manager retries Present and CleanUp on transient
// errors. AddRecord is idempotent because Dynadot dedupes by
// (subdomain, type, value); RemoveSubRecord returns nil when nothing
// matches. Both behaviours are required by the webhook spec.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	dynadot "github.com/openova-io/openova/core/pkg/dynadot-client"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	"k8s.io/client-go/rest"
)

// defaultGroupName matches the value baked into
// platform/cert-manager/chart/templates/clusterissuer-letsencrypt-dns01.yaml.
// Operators MAY override via the GROUP_NAME env so a Sovereign overlay
// can retune the API group without rebuilding the image.
const defaultGroupName = "acme.dynadot.openova.io"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(logger)

	groupName := strings.TrimSpace(os.Getenv("GROUP_NAME"))
	if groupName == "" {
		groupName = defaultGroupName
	}

	solver, err := newDynadotSolver(loadConfigFromEnv())
	if err != nil {
		logger.Error("solver init failed", "err", err)
		os.Exit(2)
	}

	logger.Info("cert-manager-dynadot-webhook starting",
		"groupName", groupName,
		"managedDomains", solver.managed.List(),
	)

	// RunWebhookServer blocks until the apiserver process is signalled
	// to terminate. It reads --secure-port / --tls-cert-file etc. from
	// argv (set by the chart's args:) and serves the aggregated apiserver
	// that cert-manager calls into.
	cmd.RunWebhookServer(groupName, solver)
}

// solverConfig is the fully-resolved configuration of the webhook,
// captured into a struct so the unit tests can inject overrides without
// touching process-global env state.
type solverConfig struct {
	APIKey         string
	APISecret      string
	ManagedDomains string
	Fallback       string // legacy DYNADOT_DOMAIN
	BaseURL        string // optional override for tests
}

// loadConfigFromEnv builds a solverConfig from the documented env vars.
func loadConfigFromEnv() solverConfig {
	return solverConfig{
		APIKey:         os.Getenv("DYNADOT_API_KEY"),
		APISecret:      os.Getenv("DYNADOT_API_SECRET"),
		ManagedDomains: os.Getenv("DYNADOT_MANAGED_DOMAINS"),
		Fallback:       os.Getenv("DYNADOT_DOMAIN"),
		BaseURL:        os.Getenv("DYNADOT_BASE_URL"),
	}
}

// dynadotSolver is the cert-manager webhook.Solver implementation.
//
// It is split from main() so tests can construct one with a fixture
// httptest.Server and a deterministic managed-domain list, then drive
// Present / CleanUp directly without wiring up the aggregated-apiserver
// transport.
type dynadotSolver struct {
	client  *dynadot.Client
	managed *dynadot.ManagedDomains
}

// newDynadotSolver validates configuration and constructs a solver.
// Returns an error rather than panicking so the caller's structured
// logger can surface a clean error path on misconfiguration.
func newDynadotSolver(cfg solverConfig) (*dynadotSolver, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.APISecret) == "" {
		return nil, errors.New("DYNADOT_API_KEY and DYNADOT_API_SECRET are required")
	}
	managedRaw := cfg.ManagedDomains
	if strings.TrimSpace(managedRaw) == "" {
		managedRaw = cfg.Fallback
	}
	if strings.TrimSpace(managedRaw) == "" {
		return nil, errors.New("DYNADOT_MANAGED_DOMAINS (or legacy DYNADOT_DOMAIN) must list at least one domain")
	}
	c := dynadot.New(cfg.APIKey, cfg.APISecret)
	if cfg.BaseURL != "" {
		c.BaseURL = cfg.BaseURL
	}
	return &dynadotSolver{
		client:  c,
		managed: dynadot.NewManagedDomains(managedRaw),
	}, nil
}

// Name is the solverName referenced by the ClusterIssuer's
// solvers[].dns01.webhook.solverName field. cert-manager dispatches to
// this solver only when the issuer's solverName matches.
func (s *dynadotSolver) Name() string { return "dynadot" }

// Initialize is a no-op for this webhook. cert-manager passes its own
// kube REST config in case a solver wants to reconcile a CR; we don't.
// The signal channel is closed on shutdown — callers must return
// promptly when it closes; since Initialize itself returns immediately,
// there is nothing to wind down.
func (s *dynadotSolver) Initialize(_ *rest.Config, _ <-chan struct{}) error {
	return nil
}

// Present writes the TXT record cert-manager needs Let's Encrypt to see
// at `_acme-challenge.<subdomain>` on the apex domain.
//
// The ChallengeRequest carries:
//   - ResolvedFQDN — fully-qualified challenge name with trailing dot,
//     e.g. "_acme-challenge.console.omantel.omani.works."
//   - ResolvedZone — the zone cert-manager believes is authoritative,
//     e.g. "omani.works."
//   - Key — the TXT value Let's Encrypt is expecting.
//
// We resolve apex from the managed-domains allowlist (NOT from
// ResolvedZone) so a misconfigured Issuer or compromised
// kube-apiserver cannot trick the webhook into mutating a domain we
// don't own. If no managed domain is a suffix of ResolvedFQDN the
// challenge is rejected with a typed error.
func (s *dynadotSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	apex, sub, err := s.resolveDomain(ch.ResolvedFQDN)
	if err != nil {
		return err
	}
	slog.Info("Present",
		"apex", apex, "subdomain", sub,
		"resolvedFQDN", ch.ResolvedFQDN, "resolvedZone", ch.ResolvedZone,
	)

	rec := dynadot.Record{
		Subdomain: sub,
		Type:      "TXT",
		Value:     ch.Key,
		TTL:       60,
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultPresentTimeout)
	defer cancel()
	if err := s.client.AddRecord(ctx, apex, rec); err != nil {
		return fmt.Errorf("dynadot AddRecord %s/%s TXT: %w", apex, sub, err)
	}
	return nil
}

// CleanUp removes the TXT record written by Present.
//
// Per the webhook spec, CleanUp MUST be idempotent — Let's Encrypt may
// have already validated the challenge, or cert-manager may retry after
// a transient failure. RemoveSubRecord uses GetDomainInfo →
// SetFullDNS so the entire zone state is preserved verbatim except for
// the matching record; if no matching record exists, it returns nil.
//
// The match key is (subdomain, TXT, key) — we DO NOT remove every TXT
// at `_acme-challenge.<subdomain>` because two parallel orders for the
// same hostname (concurrent renewal + new cert) write different keys to
// the same name and BOTH must validate.
func (s *dynadotSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	apex, sub, err := s.resolveDomain(ch.ResolvedFQDN)
	if err != nil {
		return err
	}
	slog.Info("CleanUp",
		"apex", apex, "subdomain", sub,
		"resolvedFQDN", ch.ResolvedFQDN, "resolvedZone", ch.ResolvedZone,
	)

	match := dynadot.Record{
		Subdomain: sub,
		Type:      "TXT",
		Value:     ch.Key,
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultCleanUpTimeout)
	defer cancel()
	if err := s.client.RemoveSubRecord(ctx, apex, match); err != nil {
		return fmt.Errorf("dynadot RemoveSubRecord %s/%s TXT: %w", apex, sub, err)
	}
	return nil
}

// resolveDomain matches a fully-qualified ACME challenge FQDN against
// the managed-domains allowlist and returns (apex, subdomain) suitable
// for the Dynadot api3.json `set_dns2` parameters.
//
// Examples:
//
//	"_acme-challenge.console.omantel.omani.works." with apex "omani.works"
//	  → apex="omani.works", subdomain="_acme-challenge.console.omantel"
//	"_acme-challenge.openova.io." with apex "openova.io"
//	  → apex="openova.io", subdomain="_acme-challenge"
//
// We strip the trailing dot, lowercase, and pick the longest matching
// apex from the allowlist (so "omani.works" wins over "works" if both
// were configured — guards against operator typos).
func (s *dynadotSolver) resolveDomain(fqdn string) (apex, sub string, err error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	if host == "" {
		return "", "", errors.New("dynadot webhook: ChallengeRequest.ResolvedFQDN is empty")
	}
	var bestApex string
	for _, d := range s.managed.List() {
		if host == d || strings.HasSuffix(host, "."+d) {
			if len(d) > len(bestApex) {
				bestApex = d
			}
		}
	}
	if bestApex == "" {
		return "", "", fmt.Errorf("dynadot webhook: %q is not under any DYNADOT_MANAGED_DOMAINS entry %v", host, s.managed.List())
	}
	if host == bestApex {
		// Apex challenge — Dynadot uses the special "@" subdomain (or
		// equivalently empty). The client encodes this as a main_record0.
		return bestApex, "@", nil
	}
	return bestApex, strings.TrimSuffix(host, "."+bestApex), nil
}

// parseLogLevel maps the LOG_LEVEL env to a slog.Level. Defaults to
// info; "debug" and "warn" / "error" are honoured.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Compile-time guard: dynadotSolver implements the cert-manager webhook
// Solver interface. If cert-manager's contract changes the build fails
// here rather than at runtime when the apiserver dispatches the first
// ChallengeRequest.
var _ webhook.Solver = (*dynadotSolver)(nil)
