package gitops

import (
	"strings"
	"testing"
)

// AIP-158 — the funnel's install path wires no long-lived tool bearer, as the BSS
// door (orgTenantBPAgenity) does since AIP-157: bp-agenity keeps
// openovaMCP.tenantHost, and no file of the Org's tree names the bearer wiring.
func TestAgenityFunnelWiresNoToolBearer(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			out := cartOrgFor(t, "acme", plan, []string{"wordpress", "openclaw", "stalwart-mail", "agenity"})
			body, ok := out[testBasePath+"/acme/app-agenity.yaml"]
			if !ok {
				t.Fatalf("bp-agenity HelmRelease NOT rendered for a cart Org (keys: %v)", keys(out))
			}

			// No bearer Secret, verify-key Secret or OpenBao bearer path, in any overlay.
			for f, file := range out {
				for _, gone := range []string{"bearerSecret", "rs256PubkeySecret", "mcpBearer", "mcp-bearer", "remoteBearerProperty", "remotePubkeyProperty"} {
					if strings.Contains(file, gone) {
						t.Errorf("%s still wires the tool bearer: it contains %q\n%s", f, gone, file)
					}
				}
			}

			// The X-Tenant-Host pin must be the ORG console host, not the Sovereign
			// host — a Sovereign-host X-Tenant-Host 404s tenant-not-registered on
			// every agent create_application (#4610).
			if !strings.Contains(body, "    openovaMCP:\n") || !strings.Contains(body, "      tenantHost: console.acme.omani.homes\n") {
				t.Errorf("openovaMCP.tenantHost is not pinned to the Org console host (console.acme.<pool>):\n%s", body)
			}
		})
	}
}
