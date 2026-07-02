// provisioner_console_isolation_test.go — #4053 console-isolation toggle
// coverage (Refs #4431 #4212).
//
// The boolean (pointer) Request.ConsoleIsolationEnabled field stringifies to
// the `console_isolation_enabled` tofu var, which:
//   - gates the dedicated console-ELB `count` in
//     infra/providers/{hetzner,huawei}/main.tf, and
//   - feeds the shared cloud-init template's SOVEREIGN_CONSOLE_GATEWAY
//     substitute → bootstrap-kit slot 13 ingress.gateway.parentRef.name.
//
// Default-TRUE rule (the production posture): an OMITTED field (nil pointer)
// resolves to "true" so a fresh prov is byte-identical to today (dedicated
// console ELB + isolated cilium-gateway-console). An explicit false drops the
// console ELB (one fewer EIP) and re-parents the console onto the shared
// cilium-gateway — the 3-EIP single-region validation shape.
package provisioner

import "testing"

func boolPtr(b bool) *bool { return &b }

// 1. Omitted (nil pointer) → default TRUE — production posture preserved.
func TestWriteTfvars_ConsoleIsolation_DefaultResolvesTrue(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	// ConsoleIsolationEnabled left nil (the omit-from-POST default).

	out := writeTfvarsSharedPG(t, req)

	if got := out["console_isolation_enabled"]; got != "true" {
		t.Errorf("console_isolation_enabled = %v, want %q (omitted → dedicated console ELB + isolated gateway, production-byte-identical)", got, "true")
	}
}

// 2. Explicit true → TRUE.
func TestWriteTfvars_ConsoleIsolation_ExplicitTrueResolvesTrue(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	req.ConsoleIsolationEnabled = boolPtr(true)

	out := writeTfvarsSharedPG(t, req)

	if got := out["console_isolation_enabled"]; got != "true" {
		t.Errorf("console_isolation_enabled = %v, want %q (explicit true)", got, "true")
	}
}

// 3. Explicit false → FALSE — the 3-EIP validation shape (no console ELB,
//    console re-parents onto the shared cilium-gateway).
func TestWriteTfvars_ConsoleIsolation_ExplicitFalseResolvesFalse(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	req.ConsoleIsolationEnabled = boolPtr(false)

	out := writeTfvarsSharedPG(t, req)

	if got := out["console_isolation_enabled"]; got != "false" {
		t.Errorf("console_isolation_enabled = %v, want %q (explicit false → drop console ELB, shared gateway)", got, "false")
	}
}

// consoleIsolationEnabled() helper — the single source of truth for the
// default-TRUE resolution. Pin all three branches directly.
func TestConsoleIsolationEnabled_Default(t *testing.T) {
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{"nil-pointer-defaults-true", nil, true},
		{"explicit-true", boolPtr(true), true},
		{"explicit-false", boolPtr(false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{ConsoleIsolationEnabled: tc.in}
			if got := consoleIsolationEnabled(req); got != tc.want {
				t.Errorf("consoleIsolationEnabled(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
