package handler

import (
	"testing"
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
