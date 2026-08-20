package gitops

import (
	"strings"
	"testing"
)

// #4276 hop 7/7b — a funnel-born Org's openova-MCP booted DEGRADED because this
// generator emitted NO openovaMCP bearer/verify-pubkey wiring at all, while the
// BSS door (organization_gitops.go orgTenantBPAgenity) always had it.
//
// #6372 fixed the SAME door-drift class for the anthropic block one hop up but
// left this block missing, so a funnel Org still could not run the agentic path
// even after its Anthropic credential was seeded: the openova-mcp had no bearer
// (create_application → -32001 unauthenticated) and no RS256 verify pubkey
// (verify=rs256 with no key → tools/list empty, tools/call 401).
//
// MEASURED on hw301 Org acme: the StatefulSet projected OPENOVA_MCP_RS256_PUBKEY_PEM
// from the chart-default catalyst-handover-jwt Secret — absent in the per-Org ns
// and optional:true — so the MCP started in DEGRADED mode and the agent had no
// create_application tool.
//
// THE DOORS DISAGREED. This test pins the funnel door to the producer contract
// so they cannot drift again.
func TestAgenityFunnelEmitsMCPBearerWiring(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			out := cartOrgFor(t, "acme", plan, []string{"agenity"})
			body, ok := out[testBasePath+"/acme/app-agenity.yaml"]
			if !ok {
				t.Skip("agenity not rendered on this tier")
			}

			// The bearer + verify-pubkey wiring the openova-mcp needs. Every one
			// of these is load-bearing: bearerSecret/rs256PubkeySecret point the
			// StatefulSet at the materialised per-Org Secret, and the mcpBearer
			// ExternalSecret is what materialises it from the seedMCPBearer path.
			for _, want := range []string{
				"openovaMCP:",
				"bearerSecret:",
				"name: agenity-mcp-bearer",
				"key: bearer",
				"rs256PubkeySecret:",
				"key: pubkeyPem",
				"mcpBearer:",
				"remoteKey: catalyst/agenity/acme/mcp-bearer",
				"remoteBearerProperty: bearer",
				"remotePubkeyProperty: pubkeyPem",
				"secretStoreRef: vault-region1",
				"secretStoreKind: ClusterSecretStore",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("funnel agenity overlay is MISSING %q (#4276 hop 7/7b).\n"+
						"Without it the openova-mcp boots DEGRADED (no bearer, no verify\n"+
						"pubkey) and the solo agent has no create_application tool —\n"+
						"while a BSS-born Org in the same Sovereign works.\nrendered:\n%s", want, body)
				}
			}

			// The mcpBearer ExternalSecret must be ENABLED — a rendered-but-disabled
			// block reads as "wired" while projecting nothing (the exact DEGRADED
			// shape this fix removes). Assert the enablement, not just its presence.
			if !strings.Contains(body, "enabled: true") {
				t.Errorf("funnel agenity mcpBearer.externalSecret is not enabled — the block would render inert:\n%s", body)
			}

			// CONTROL: the bearer path must stay under catalyst/. That prefix is
			// the only KV sub-tree a Sovereign may WRITE via catalyst-api-write;
			// pointing elsewhere renders a block that can never resolve.
			if strings.Contains(body, "remoteKey: catalyst/agenity/") &&
				!strings.Contains(body, "remoteKey: catalyst/agenity/acme/mcp-bearer") {
				t.Errorf("mcp-bearer remoteKey drifted off the per-Org catalyst/ path:\n%s", body)
			}

			// The X-Tenant-Host pin must be the ORG console host, not the Sovereign
			// host — a Sovereign-host X-Tenant-Host 404s tenant-not-registered on
			// every agent create_application (#4610).
			if !strings.Contains(body, "tenantHost: console.acme.") {
				t.Errorf("openovaMCP.tenantHost is not pinned to the Org console host (console.acme.<pool>):\n%s", body)
			}
		})
	}
}

// CONTROL — the openovaMCP block must NOT leak into non-agenity overlays.
func TestMCPBearerBlockIsAgenityOnly(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"wordpress", "openclaw", "stalwart-mail", "agenity"})
	for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml", "app-wordpress.yaml"} {
		body, ok := out[testBasePath+"/acme/"+f]
		if !ok {
			continue
		}
		if strings.Contains(body, "remoteKey: catalyst/agenity/") {
			t.Errorf("%s wrongly carries the openova-MCP bearer wiring — it is agenity-only:\n%s", f, body)
		}
	}
}
