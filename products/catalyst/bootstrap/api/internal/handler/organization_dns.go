// Package handler — organization_dns.go: DNS provisioner for the SME
// tenant pipeline (issue #804).
//
// Two flows:
//
//   - Free-subdomain mode (default): the orchestrator calls PowerDNS
//     directly via the otech's in-cluster PowerDNS service to add an
//     A record for `console.<subdomain>.<otech-fqdn>` (and optional
//     wildcard CNAME for sister hosts). Idempotent — PowerDNS PATCH
//     is naturally re-runnable.
//
//   - BYO mode: the orchestrator does NO writes; instead it resolves
//     `console.<byo_domain>` and confirms the CNAME target is the
//     otech ingress hostname. A failed lookup or mismatched target is
//     surfaced as a structured error so the wizard UI can render
//     "your CNAME doesn't point here yet" without a chat-with-support
//     loop.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the PowerDNS endpoint + API
// key are env-configured (CATALYST_POWERDNS_URL,
// CATALYST_POWERDNS_API_KEY). A nil writer (env unset) returns a
// graceful error rather than a panic; the orchestrator surfaces that
// as a transient `dns:transient:powerdns not wired` until the
// catalyst-api is restarted with the env populated.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// PowerDNSWriter is a minimal PowerDNS API client matching the subset
// the SME-tenant pipeline needs: PATCH zones/<zone> with one or more
// RRsets. Kept narrow on purpose — the orchestrator never lists,
// creates, or deletes whole zones (the otech zone already exists).
type PowerDNSWriter struct {
	BaseURL    string
	ServerID   string // PowerDNS server identifier; "localhost" by default
	APIKey     string
	HTTPClient *http.Client
}

// NewPowerDNSWriter returns a configured client. Empty baseURL or
// apiKey returns nil so the orchestrator can fall back to the no-op
// writer below.
func NewPowerDNSWriter(baseURL, apiKey string) *PowerDNSWriter {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return &PowerDNSWriter{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		ServerID:   "localhost",
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// pdnsRRSet is the API shape the orchestrator emits.
type pdnsRRSet struct {
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	TTL        int          `json:"ttl"`
	ChangeType string       `json:"changetype"`
	Records    []pdnsRecord `json:"records"`
}

type pdnsRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// PatchRRSets PATCHes a list of RRsets. PowerDNS REPLACE is the only
// changetype the orchestrator uses — idempotent, re-runnable, and the
// canonical "create or update" shape per the PowerDNS HTTP API.
func (w *PowerDNSWriter) PatchRRSets(ctx context.Context, zone string, rrsets []pdnsRRSet) error {
	body := struct {
		RRSets []pdnsRRSet `json:"rrsets"`
	}{RRSets: rrsets}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	srv := w.ServerID
	if srv == "" {
		srv = "localhost"
	}
	url := fmt.Sprintf("%s/api/v1/servers/%s/zones/%s",
		w.BaseURL, srv, zoneCanonical(zone))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", w.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("powerdns PATCH: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("powerdns PATCH HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// zoneCanonical ensures the zone name ends with a single trailing
// dot (PowerDNS API convention).
func zoneCanonical(zone string) string {
	zone = strings.TrimRight(zone, ".")
	return zone + "."
}

// DefaultOrganizationDNSProvisioner is the production
// OrganizationDNSProvisioner. Wraps a PowerDNSWriter for free-subdomain
// writes and net.LookupCNAME for BYO validation.
type DefaultOrganizationDNSProvisioner struct {
	Writer *PowerDNSWriter
	// Resolver — net resolver used for BYO CNAME lookups. Defaults to
	// net.DefaultResolver; tests inject a stub that returns canned
	// responses without hitting the live DNS root.
	Resolver Resolver
}

// Resolver is the narrow interface the BYO validator needs.
// net.DefaultResolver satisfies it.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
}

// ProvisionFreeSubdomain implements OrganizationDNSProvisioner. Writes
// A record for `console.<subdomain>.<parentZone>` plus the standard
// app-host A records (wordpress, openclaw, mail, keycloak) under the
// chosen parent zone so the per-tenant overlay's ingress hostnames
// resolve as soon as Flux finishes reconciling. Per epic #825 the
// parentZone is operator-supplied (one of the Sovereign's
// role:org-pool entries) — never inferred from a hardcoded OTECHFQDN.
func (p DefaultOrganizationDNSProvisioner) ProvisionFreeSubdomain(ctx context.Context, subdomain, parentZone, ingressIPv4 string) error {
	if p.Writer == nil {
		return errors.New("powerdns writer not wired (CATALYST_POWERDNS_URL / CATALYST_POWERDNS_API_KEY)")
	}
	if strings.TrimSpace(ingressIPv4) == "" {
		return errors.New("otech ingress IPv4 unconfigured")
	}
	if strings.TrimSpace(parentZone) == "" {
		return errors.New("parent zone unconfigured (multi-domain Sovereign requires a org-pool parent_domain)")
	}
	zone := parentZone
	rrsets := []pdnsRRSet{}
	for _, prefix := range []string{"console", "wordpress", "openclaw", "mail", "keycloak"} {
		fqdn := fmt.Sprintf("%s.%s.%s.", prefix, subdomain, parentZone)
		rrsets = append(rrsets, pdnsRRSet{
			Name:       fqdn,
			Type:       "A",
			TTL:        300,
			ChangeType: "REPLACE",
			Records: []pdnsRecord{
				{Content: ingressIPv4, Disabled: false},
			},
		})
	}
	return p.Writer.PatchRRSets(ctx, zone, rrsets)
}

// ValidateBYOCNAME implements OrganizationDNSProvisioner. Resolves
// `console.<byo_domain>` and confirms its CNAME target ends with one
// of the operator-supplied accepted targets (or, with no targets, the
// legacy single-target path). The multi-target shape backs epic #825
// (multi-domain Sovereign) where any parent in the role:org-pool list
// is a valid CNAME target.
func (p DefaultOrganizationDNSProvisioner) ValidateBYOCNAME(ctx context.Context, byoDomain, legacyTarget string, acceptedTargets ...string) error {
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	host := "console." + strings.Trim(byoDomain, ".")
	target, err := resolver.LookupCNAME(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve CNAME for %s: %w", host, err)
	}
	target = strings.Trim(strings.ToLower(target), ".")
	// Build the candidate list: the supplied accepted targets (multi-
	// domain pool) plus the legacy single target as a fallback.
	candidates := make([]string, 0, len(acceptedTargets)+1)
	for _, c := range acceptedTargets {
		c = strings.Trim(strings.ToLower(c), ".")
		if c != "" {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		legacy := strings.Trim(strings.ToLower(legacyTarget), ".")
		if legacy == "" {
			return errors.New("no accepted CNAME targets supplied")
		}
		candidates = append(candidates, legacy)
	}
	for _, expected := range candidates {
		if strings.HasSuffix(target, expected) {
			return nil
		}
	}
	return fmt.Errorf("%w (got %q, expected suffix from %v)", errBYOCNAMEMismatch, target, candidates)
}

// NoopOrganizationDNSProvisioner satisfies OrganizationDNSProvisioner with
// success-on-call no-ops. Used in CI / test environments without
// PowerDNS or a public DNS resolver. Production main.go wires this
// only when both env knobs (CATALYST_POWERDNS_URL,
// CATALYST_POWERDNS_API_KEY) are absent — that's an explicit signal
// from the operator that they're not running the DNS step here.
type NoopOrganizationDNSProvisioner struct{}

// ProvisionFreeSubdomain is a no-op.
func (NoopOrganizationDNSProvisioner) ProvisionFreeSubdomain(_ context.Context, _, _, _ string) error {
	return nil
}

// ValidateBYOCNAME is a no-op (returns nil — accepts any domain).
func (NoopOrganizationDNSProvisioner) ValidateBYOCNAME(_ context.Context, _, _ string, _ ...string) error {
	return nil
}
