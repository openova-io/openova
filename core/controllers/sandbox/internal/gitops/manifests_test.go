// Tests for the gitops Render() function — specifically the TBD-P4 A4
// per-agent dispatch wiring. The controller reads sb.Spec.AgentCatalogue[0]
// and writes it into Inputs.DefaultAgent; the StatefulSet template MUST
// then emit a `SANDBOX_DEFAULT_AGENT` env var so the pty-server's
// lazy-spawn-on-attach branch (products/sandbox/pty-server/internal/
// server/routes.go: lazySpawn) can execve the right agent binary.
//
// Why this matters: without this wire the FE's 6-option agent dropdown
// is cosmetic — every fresh WS attach returns 404 and the xterm panel
// stays blank. See TBD-P4 #1986 A4 sub-break.
package gitops

import (
	"strings"
	"testing"

	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// baseInputs returns a minimally-valid Inputs for Render(). Tests
// override DefaultAgent + AgentCatalogue to exercise the dispatch path.
func baseInputs() Inputs {
	return Inputs{
		Name:           "demo",
		OwnerUID:       "ceo-at-acme-com",
		OwnerEmail:     "ceo@acme.com",
		OrgSlug:        "acme",
		SovereignFQDN:  "t99.omani.works",
		Quota:          sandboxapi.SandboxQuota{CPU: "4", Memory: "8Gi", Storage: "50Gi", ConcurrentSessions: 3},
		PtyServerImage: "ghcr.io/example/pty-server:test",
		MCPImage:       "ghcr.io/example/mcp:test",
		NewapiURL:      "https://newapi.t99.omani.works",
	}
}

// TestRender_DefaultAgent_PerSlug walks every FE-visible agent slug and
// asserts the StatefulSet renders the SANDBOX_DEFAULT_AGENT env var with
// the expected value. This is the explicit table-driven proof that the
// 6-row dropdown is no longer cosmetic for non-claude-code agents.
//
// The slugs MUST stay in lock-step with:
//   - products/sandbox/pty-server/internal/agentcatalog/agentcatalog.go (Builtin)
//   - products/catalyst/bootstrap/api/internal/handler/sandbox_sessions.go (sandboxAllowedAgents)
//   - products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts (SANDBOX_AGENTS)
//   - products/catalyst/chart/crds/sandbox.yaml (spec.agentCatalogue.items.enum)
func TestRender_DefaultAgent_PerSlug(t *testing.T) {
	t.Parallel()
	agents := []string{
		"aider",
		"claude-code",
		"cursor-agent",
		"little-coder",
		"opencode",
		"qwen-code",
		"sovereign-shell",
	}
	for _, slug := range agents {
		slug := slug
		t.Run(slug, func(t *testing.T) {
			t.Parallel()
			in := baseInputs()
			in.AgentCatalogue = []string{slug}
			in.DefaultAgent = slug

			manifests, err := Render(in)
			if err != nil {
				t.Fatalf("Render(%q): %v", slug, err)
			}
			body, ok := manifests["statefulset-pty-server.yaml"]
			if !ok {
				t.Fatalf("expected statefulset-pty-server.yaml in render output")
			}
			s := string(body)
			// The env entry MUST be present.
			if !strings.Contains(s, "name: SANDBOX_DEFAULT_AGENT") {
				t.Errorf("statefulset missing SANDBOX_DEFAULT_AGENT env var for slug %q\n--- rendered ---\n%s", slug, s)
			}
			// And it must carry the expected value (quoted by template).
			wantVal := "value: \"" + slug + "\""
			if !strings.Contains(s, wantVal) {
				t.Errorf("statefulset SANDBOX_DEFAULT_AGENT value missing for slug %q (expected %q)\n--- rendered ---\n%s",
					slug, wantVal, s)
			}
		})
	}
}

// TestRender_DefaultAgent_OmittedWhenEmpty asserts that an empty
// DefaultAgent leaves the env var UNRENDERED — preserving the historic
// 404-on-attach behaviour for hand-rolled CRs without a populated
// catalogue. This guards against accidentally emitting `value: ""` which
// would have lazy-spawn enter the dispatch branch with an empty slug
// and return invalid-agent instead of 404 (semantic regression).
func TestRender_DefaultAgent_OmittedWhenEmpty(t *testing.T) {
	t.Parallel()
	in := baseInputs()
	// no AgentCatalogue, no DefaultAgent

	manifests, err := Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body, ok := manifests["statefulset-pty-server.yaml"]
	if !ok {
		t.Fatalf("expected statefulset-pty-server.yaml in render output")
	}
	s := string(body)
	if strings.Contains(s, "SANDBOX_DEFAULT_AGENT") {
		t.Errorf("statefulset must NOT emit SANDBOX_DEFAULT_AGENT when DefaultAgent is empty\n--- rendered ---\n%s", s)
	}
}

// TestRender_DefaultAgent_QwenCodeIsCanonical pins the canonical-journey
// agent (CLAUDE.md §0 Phase 2: agent = qwen-code) to a dedicated assert
// so the next reader can grep for the exact wire-level evidence that
// the canonical journey is no longer cosmetic.
func TestRender_DefaultAgent_QwenCodeIsCanonical(t *testing.T) {
	t.Parallel()
	in := baseInputs()
	in.AgentCatalogue = []string{"qwen-code"}
	in.DefaultAgent = "qwen-code"

	manifests, err := Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body, ok := manifests["statefulset-pty-server.yaml"]
	if !ok {
		t.Fatalf("expected statefulset-pty-server.yaml in render output")
	}
	s := string(body)
	if !strings.Contains(s, "name: SANDBOX_DEFAULT_AGENT") || !strings.Contains(s, "value: \"qwen-code\"") {
		t.Errorf("canonical journey agent qwen-code not wired into pty-server env\n--- rendered ---\n%s", s)
	}
	// Sanity: no BYOS ANTHROPIC_API_KEY for non-claude-code agent.
	if strings.Contains(s, "ANTHROPIC_API_KEY") {
		t.Errorf("qwen-code must NOT emit ANTHROPIC_API_KEY env (BYOS branch must be claude-code-only)\n--- rendered ---\n%s", s)
	}
}
