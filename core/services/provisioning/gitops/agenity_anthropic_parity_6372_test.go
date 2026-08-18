package gitops

import (
	"strings"
	"testing"
)

// #6372 — a funnel-born Org could never run the agent, because this
// generator emitted no anthropic credential wiring at all while the
// BSS door (organization_gitops.go orgTenantBPAgenity) always had it.
//
// MEASURED on hw299: bp-agenity-0 sat Pending on its seed-claude-creds
// init container, which waits for /creds/<credentialsKey> and then fails
// by design (#6163). The backing Secret is optional:true, so the pod
// schedules, the mount stays empty, and the workspace never starts.
//
// THE DOORS DISAGREED. That is the whole defect: two emitters for the
// same Blueprint, one wired and one not. This test pins them together so
// they cannot drift again.

func TestAgenityFunnelEmitsAnthropicCredential(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			out := cartOrgFor(t, "acme", plan, []string{"agenity"})
			body, ok := out[testBasePath+"/acme/app-agenity.yaml"]
			if !ok {
				t.Skip("agenity not rendered on this tier")
			}

			// The credential block itself.
			for _, want := range []string{
				"anthropic:",
				"externalSecret:",
				"remoteKey: catalyst/anthropic/token",
				"remoteProperty: apiKey",
				"remoteCredentialsProperty: credentialsJson",
				"secretStoreRef: vault-region1",
				"secretStoreKind: ClusterSecretStore",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("funnel agenity overlay is MISSING %q (#6372).\n"+
						"Without it nothing materialises the Secret that seed-claude-creds\n"+
						"waits for, so bp-agenity-0 sits Pending forever and the solo agent\n"+
						"never starts — while a BSS-born Org in the same Sovereign works.\n"+
						"rendered:\n%s", want, body)
				}
			}

			// CONTROL: the path must stay under catalyst/. That prefix is the
			// only KV sub-tree a Sovereign may WRITE; pointing elsewhere would
			// render a block that can never resolve, which reads as "wired"
			// while failing exactly as before.
			if strings.Contains(body, "remoteKey:") && !strings.Contains(body, "remoteKey: catalyst/") {
				t.Errorf("anthropic remoteKey escaped the catalyst/ prefix — the Sovereign cannot write there:\n%s", body)
			}
		})
	}
}

// CONTROL — the block must NOT leak into non-agenity overlays. Without
// this the fix could pass by stamping the credential onto every app,
// which would attach an unrelated Secret to workloads that never need it.
func TestAnthropicBlockIsAgenityOnly(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"wordpress", "openclaw", "stalwart-mail", "agenity"})
	for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml", "app-wordpress.yaml"} {
		body, ok := out[testBasePath+"/acme/"+f]
		if !ok {
			continue
		}
		if strings.Contains(body, "remoteKey: catalyst/anthropic/token") {
			t.Errorf("%s wrongly carries the anthropic credential — it is agenity-only:\n%s", f, body)
		}
	}
}
