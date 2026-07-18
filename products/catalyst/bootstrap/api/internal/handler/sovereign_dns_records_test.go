package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
)

// TestCanonicalSovereignSubdomainsCoversBrowserFacingApps guards the parent-zone
// A-record allowlist against the recurring class of bug where a Sovereign app
// ships a Cilium Gateway HTTPRoute (Accepted=True) yet its public hostname
// returns NXDOMAIN because catalyst-api never wrote the A record into the parent
// pool zone.
//
// This is the discriminating attribute behind #3263 row 6 (newapi.<fqdn>
// ERR_NAME_NOT_RESOLVED) and #3225 (pdns-admin.<fqdn> NXDOMAIN): on hw126 every
// browser-facing HTTPRoute was Accepted by the cilium-gateway, but only the
// subdomains present in CanonicalSovereignSubdomains resolved publicly. newapi
// and pdns-admin were absent from the list, so only those two hosts 404'd at
// DNS resolution.
//
// Every browser-facing bp-* host wired in clusters/_template/bootstrap-kit/*.yaml
// (host: <prefix>.${SOVEREIGN_FQDN}) MUST appear here. Add a row when a new
// public app HTTPRoute lands.
func TestCanonicalSovereignSubdomainsCoversBrowserFacingApps(t *testing.T) {
	have := make(map[string]struct{}, len(CanonicalSovereignSubdomains))
	for _, s := range CanonicalSovereignSubdomains {
		have[s] = struct{}{}
	}

	// The full set of browser-facing public subdomains a stock Sovereign
	// exposes through its Cilium Gateway. Sourced from the bootstrap-kit slot
	// overlays' HTTPRoute hostnames (host: <prefix>.${SOVEREIGN_FQDN}).
	want := []string{
		"console",      // bp-catalyst-platform (catalyst-ui)
		"api",          // bp-catalyst-platform (catalyst-api)
		"marketplace",  // bp-catalyst-platform (marketplace)
		"guacamole",    // bp-guacamole
		"auth",         // bp-keycloak
		"gitea",        // bp-gitea
		"registry",     // bp-harbor
		"bao",          // bp-openbao
		"grafana",      // bp-grafana
		"hubble",       // cilium hubble-ui
		"pdns",         // bp-powerdns (REST API host)
		"pdns-admin",   // bp-powerdns-admin (UI host) — #3225
		"openova-flow", // bp-openova-flow-server
		"newapi",       // bp-newapi (LLM gateway) — #3263 row 6
		"sandbox",      // bp-sandbox per-Sandbox pty-server
		"mcp",          // bp-openova-mcp sovereign-mode instance — #5206
	}

	for _, sub := range want {
		if _, ok := have[sub]; !ok {
			t.Errorf("CanonicalSovereignSubdomains is missing %q — its HTTPRoute is Accepted but %s.<fqdn> will return NXDOMAIN (the #3263/#3225 gap class)", sub, sub)
		}
	}
}

// TestConsoleGatewaySubdomainsAreCanonicalSubset guards #4069: every host that
// parents the dedicated cilium-gateway-console (and thus must resolve at the
// console LB IP) MUST also be a CanonicalSovereignSubdomain, otherwise the
// DNS-write loop would never visit it and the console LB record would never be
// written. It also pins the expected set (console/api/marketplace) so a future
// re-parenting in the chart is reflected here in lockstep.
func TestConsoleGatewaySubdomainsAreCanonicalSubset(t *testing.T) {
	canon := make(map[string]struct{}, len(CanonicalSovereignSubdomains))
	for _, s := range CanonicalSovereignSubdomains {
		canon[s] = struct{}{}
	}
	for _, s := range ConsoleGatewaySubdomains {
		if _, ok := canon[s]; !ok {
			t.Errorf("ConsoleGatewaySubdomains entry %q is not in CanonicalSovereignSubdomains — the DNS-write loop iterates the canonical set, so %s.<fqdn> would never be written at the console LB", s, s)
		}
	}

	want := map[string]struct{}{"console": {}, "api": {}, "marketplace": {}}
	if len(ConsoleGatewaySubdomains) != len(want) {
		t.Fatalf("ConsoleGatewaySubdomains = %v; want exactly console/api/marketplace (the live cilium-gateway-console membership on omantel.biz, Refs #4069)", ConsoleGatewaySubdomains)
	}
	for _, s := range ConsoleGatewaySubdomains {
		if _, ok := want[s]; !ok {
			t.Errorf("unexpected ConsoleGatewaySubdomains entry %q — keep in lockstep with the chart's cilium-gateway-console parentRef membership", s)
		}
	}
}

// TestRecordTargetIP is the heart of #4069: console-gateway hosts resolve at the
// console LB IP when one exists; everything else stays on the shared LB IP; and
// an empty consoleLBIP (module pre-#4053) collapses every host to the shared IP
// — byte-identical to the pre-#4069 behaviour.
func TestRecordTargetIP(t *testing.T) {
	const (
		sharedIP  = "212.72.24.31"
		consoleIP = "212.72.24.33"
	)
	set := consoleGatewaySubdomainSet()

	cases := []struct {
		name       string
		sub        string
		lbIP       string
		consoleLB  string
		wantTarget string
	}{
		// Split active: console-gateway hosts → console LB.
		{"console→consoleLB", "console", sharedIP, consoleIP, consoleIP},
		{"api→consoleLB", "api", sharedIP, consoleIP, consoleIP},
		{"marketplace→consoleLB", "marketplace", sharedIP, consoleIP, consoleIP},
		// Split active: shared-gateway hosts stay on the shared LB.
		{"gitea→sharedLB", "gitea", sharedIP, consoleIP, sharedIP},
		{"auth→sharedLB", "auth", sharedIP, consoleIP, sharedIP},
		{"registry→sharedLB", "registry", sharedIP, consoleIP, sharedIP},
		{"newapi→sharedLB", "newapi", sharedIP, consoleIP, sharedIP},
		{"sandbox→sharedLB", "sandbox", sharedIP, consoleIP, sharedIP},
		// Case-insensitivity on the subdomain.
		{"CONSOLE upper→consoleLB", "CONSOLE", sharedIP, consoleIP, consoleIP},
		// No split (module pre-#4053): everything collapses to lbIP.
		{"console no-split", "console", sharedIP, "", sharedIP},
		{"api no-split", "api", sharedIP, "", sharedIP},
		{"gitea no-split", "gitea", sharedIP, "", sharedIP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recordTargetIP(tc.sub, tc.lbIP, tc.consoleLB, set)
			if got != tc.wantTarget {
				t.Errorf("recordTargetIP(%q, %q, %q) = %q; want %q",
					tc.sub, tc.lbIP, tc.consoleLB, got, tc.wantTarget)
			}
		})
	}
}

// TestConsoleGatewaySubdomainSetOverride verifies the
// CATALYST_CONSOLE_GATEWAY_SUBDOMAINS env override replaces (not appends-to) the
// compiled-in set, so an unusual topology can re-parent a different subset.
func TestConsoleGatewaySubdomainSetOverride(t *testing.T) {
	t.Setenv("CATALYST_CONSOLE_GATEWAY_SUBDOMAINS", "console, api ,foo")
	set := consoleGatewaySubdomainSet()
	for _, want := range []string{"console", "api", "foo"} {
		if _, ok := set[want]; !ok {
			t.Errorf("override set missing %q", want)
		}
	}
	if _, ok := set["marketplace"]; ok {
		t.Errorf("override should REPLACE the default set, but marketplace leaked through")
	}
	if got := recordTargetIP("foo", "1.1.1.1", "2.2.2.2", set); got != "2.2.2.2" {
		t.Errorf("override host foo should resolve to consoleLB; got %q", got)
	}
	if got := recordTargetIP("marketplace", "1.1.1.1", "2.2.2.2", set); got != "1.1.1.1" {
		t.Errorf("marketplace no longer in overridden set → should resolve to sharedLB; got %q", got)
	}
}

// stubZoneClient records PatchRRSets calls for the delete-side test.
// failZone, when set, makes PatchRRSets return a PowerDNS-shaped 404 for that
// exact zone (the record of the attempt is still captured) so the 404 →
// derived-parent fallback can be exercised deterministically.
type stubZoneClient struct {
	zones    []string
	rrsets   [][]powerdns.RRSet
	err      error
	failZone string
}

func (s *stubZoneClient) CreateZone(_ context.Context, _ powerdns.ZoneSpec) error { return nil }
func (s *stubZoneClient) ZoneExists(_ context.Context, _ string) (bool, error)   { return true, nil }
func (s *stubZoneClient) PatchRRSets(_ context.Context, zone string, rrsets []powerdns.RRSet) error {
	s.zones = append(s.zones, zone)
	s.rrsets = append(s.rrsets, rrsets)
	if s.failZone != "" && zone == s.failZone {
		// Mirrors *powerdns.Client.PatchRRSets on a missing zone; the
		// "status 404" token is what the fallback keys off.
		return fmt.Errorf("powerdns: patch zone %q status 404: Not Found", zone)
	}
	return s.err
}

// TestSovereignParentZoneRecordNames locks the record-SELECTION contract that
// both the write side and the wipe teardown share: every canonical subdomain
// plus the apex, each dot-terminated, and NO record at all for a blank FQDN
// (so a wipe with a missing FQDN can never emit a bare-dot "." apex rrset).
func TestSovereignParentZoneRecordNames(t *testing.T) {
	names := sovereignParentZoneRecordNames("hw220.omani.works")
	if want := len(CanonicalSovereignSubdomains) + 1; len(names) != want {
		t.Fatalf("name count = %d, want %d (canonical subdomains + apex)", len(names), want)
	}
	set := map[string]bool{}
	for _, n := range names {
		if !strings.HasSuffix(n, ".") {
			t.Errorf("record name %q must be dot-terminated (PowerDNS canonical rrset name)", n)
		}
		set[n] = true
	}
	for _, must := range []string{
		"console.hw220.omani.works.",
		"api.hw220.omani.works.",
		"marketplace.hw220.omani.works.",
		"hw220.omani.works.", // apex
	} {
		if !set[must] {
			t.Errorf("record-selection missing %q — the wiped-record leak class #4764 guards", must)
		}
	}
	// Case-insensitive + trailing-dot tolerant, and blank → nothing.
	if got := sovereignParentZoneRecordNames("HW220.omani.works."); !hasName(got, "console.hw220.omani.works.") {
		t.Errorf("uppercase/trailing-dot FQDN should normalise to lowercase names, got %v", got)
	}
	if got := sovereignParentZoneRecordNames("   "); got != nil {
		t.Errorf("blank FQDN must yield no record names (no bare-dot apex), got %v", got)
	}
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestDeleteSovereignParentZoneRecordsWithFallback_404RetriesDerivedParent is
// the heart of #4764. BYO back-compat stamps ParentDomains[0].Name =
// SovereignFQDN, so the wipe is handed parent == the sub-FQDN. That sub-FQDN is
// not an authoritative PowerDNS zone (the tail `omani.works` is), so the first
// DELETE 404s; the fallback MUST retry against the derived tail where the
// records — including console.<fqdn> → the released console EIP — actually live.
func TestDeleteSovereignParentZoneRecordsWithFallback_404RetriesDerivedParent(t *testing.T) {
	stub := &stubZoneClient{failZone: "hw220.omani.works"}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), powerdnsZoneClient: stub}

	if err := h.deleteSovereignParentZoneRecordsWithFallback(context.Background(), "hw220.omani.works", "hw220.omani.works"); err != nil {
		t.Fatalf("expected fallback to succeed against derived tail parent, got %v", err)
	}
	if len(stub.zones) != 2 || stub.zones[0] != "hw220.omani.works" || stub.zones[1] != "omani.works" {
		t.Fatalf("zones attempted = %v, want [hw220.omani.works omani.works] (404 then derived-parent retry)", stub.zones)
	}
	// The derived-parent DELETE must still carry the console record so the
	// released console EIP stops resolving after the wipe.
	sawConsole := false
	for _, rr := range stub.rrsets[1] {
		if rr.Name == "console.hw220.omani.works." && rr.ChangeType == "DELETE" {
			sawConsole = true
		}
	}
	if !sawConsole {
		t.Errorf("derived-parent DELETE missing console.hw220.omani.works. — the dangling-record leak #4764 fixes")
	}
}

// TestDeleteSovereignParentZoneRecordsWithFallback_NoRetryWhenParentIsRealZone
// guards the blast-radius invariant: for a pool / explicit-multi-domain wipe the
// parent is the real registered zone (omani.works), the single DELETE succeeds,
// and the fallback must NOT fire — we never touch a second (sibling) zone.
func TestDeleteSovereignParentZoneRecordsWithFallback_NoRetryWhenParentIsRealZone(t *testing.T) {
	stub := &stubZoneClient{}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), powerdnsZoneClient: stub}

	if err := h.deleteSovereignParentZoneRecordsWithFallback(context.Background(), "hw220.omani.works", "omani.works"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.zones) != 1 || stub.zones[0] != "omani.works" {
		t.Fatalf("zones attempted = %v, want exactly [omani.works] (no fallback, no sibling-zone touch)", stub.zones)
	}
}

// TestDeleteSovereignParentZoneRecordsWithFallback_NilClientNoop keeps the
// best-effort contract: no PowerDNS client wired (pool-mode Sovereigns whose
// DNS is entirely PDM's child zone) → nil error, no fallback, no panic.
func TestDeleteSovereignParentZoneRecordsWithFallback_NilClientNoop(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := h.deleteSovereignParentZoneRecordsWithFallback(context.Background(), "hw220.omani.works", "hw220.omani.works"); err != nil {
		t.Fatalf("expected nil error with no client, got %v", err)
	}
}

// TestDeleteSovereignParentZoneRecords locks #4732 item 7: the wipe-side
// teardown DELETEs exactly the rrset names the upsert wrote — every
// canonical subdomain plus the bare apex — so a wiped Sovereign's records
// cannot linger and resolve a released EIP (the live console.hw217 case).
func TestDeleteSovereignParentZoneRecords(t *testing.T) {
	stub := &stubZoneClient{}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), powerdnsZoneClient: stub}

	if err := h.deleteSovereignParentZoneRecords(context.Background(), "hw217.omani.works", ""); err != nil {
		t.Fatalf("deleteSovereignParentZoneRecords: %v", err)
	}
	if len(stub.zones) != 1 || stub.zones[0] != "omani.works" {
		t.Fatalf("zones patched = %v, want [omani.works] (derived from FQDN tail)", stub.zones)
	}
	got := stub.rrsets[0]
	// Every canonical subdomain + the apex, all changetype=DELETE.
	if want := len(CanonicalSovereignSubdomains) + 1; len(got) != want {
		t.Fatalf("rrset count = %d, want %d (canonical set + apex)", len(got), want)
	}
	names := map[string]bool{}
	for _, rr := range got {
		if rr.ChangeType != "DELETE" {
			t.Errorf("rrset %s changetype = %q, want DELETE", rr.Name, rr.ChangeType)
		}
		names[rr.Name] = true
	}
	for _, must := range []string{"console.hw217.omani.works.", "api.hw217.omani.works.", "hw217.omani.works."} {
		if !names[must] {
			t.Errorf("missing DELETE rrset for %s", must)
		}
	}
}

// TestDeleteSovereignParentZoneRecords_NilClientNoop locks the
// best-effort contract: no powerdns client wired → nil error, no panic.
func TestDeleteSovereignParentZoneRecords_NilClientNoop(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := h.deleteSovereignParentZoneRecords(context.Background(), "hw217.omani.works", ""); err != nil {
		t.Fatalf("expected nil error with no client, got %v", err)
	}
}
