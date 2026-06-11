package handler

import "testing"

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
