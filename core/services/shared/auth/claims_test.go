// claims_test.go — table-driven coverage for Claims.HasCapability,
// extended in PR #1671 to honour wildcard prefixes (`sandbox.db.*`)
// so tier-bound MCP capability grants can be coarse without
// enumerating every per-tool RequiredCapability.

package auth

import "testing"

func TestClaims_HasCapability_ExactAndWildcard(t *testing.T) {
	cases := []struct {
		name string
		held []string
		want string
		ok   bool
	}{
		{"nil-claims-rejects", nil, "sandbox.db.provision", false},
		{"empty-want-rejects", []string{"sandbox.db.*"}, "", false},
		{"empty-held-rejects", []string{""}, "sandbox.db.provision", false},

		// Exact match.
		{"exact-match", []string{"sandbox.db.provision"}, "sandbox.db.provision", true},
		{"exact-miss", []string{"sandbox.db.provision"}, "sandbox.db.list", false},

		// Wildcard prefix matches the stem.
		{"wildcard-matches-stem", []string{"sandbox.db.*"}, "sandbox.db", true},
		// Wildcard prefix matches dotted descendants.
		{"wildcard-matches-descendant", []string{"sandbox.db.*"}, "sandbox.db.provision", true},
		{"wildcard-matches-deep-descendant", []string{"sandbox.db.*"}, "sandbox.db.list.x", true},

		// Wildcard prefix does NOT match an unrelated namespace.
		{"wildcard-isolated-namespace", []string{"sandbox.db.*"}, "sandbox.deploy.production", false},
		// Wildcard does NOT match a name that shares a prefix-string but
		// crosses the dotted-component boundary (`sandbox.db` vs
		// `sandbox.dbz`).
		{"wildcard-boundary-respected", []string{"sandbox.db.*"}, "sandbox.dbz.provision", false},

		// Plain stem (no `.*`) does NOT widen to descendants — the
		// production gate in sandbox_deploy.go depends on this.
		{"plain-stem-no-widen", []string{"sandbox.deploy"}, "sandbox.deploy.production", false},
		// …but the same plain stem still matches itself.
		{"plain-stem-exact-self", []string{"sandbox.deploy"}, "sandbox.deploy", true},

		// Multiple held caps — first-hit wins.
		{
			"multiple-held-first-wins",
			[]string{"k8s.read.get", "sandbox.db.*"},
			"sandbox.db.list",
			true,
		},
		{
			"multiple-held-none-match",
			[]string{"k8s.read.get", "rag.search"},
			"sandbox.deploy.production",
			false,
		},

		// `*` alone (no leading stem) is a malformed grant and MUST NOT
		// open every namespace.
		{"bare-wildcard-rejects", []string{"*"}, "sandbox.deploy.production", false},
		{"bare-dotwildcard-rejects", []string{".*"}, "sandbox.deploy.production", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c *Claims
			if tc.name != "nil-claims-rejects" {
				c = &Claims{Capabilities: tc.held}
			}
			if got := c.HasCapability(tc.want); got != tc.ok {
				t.Errorf("HasCapability(held=%v, want=%q) = %v, want %v",
					tc.held, tc.want, got, tc.ok)
			}
		})
	}
}
