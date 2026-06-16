package huawei

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRotateBlocklistedNATEIPs_PassesAccessKeyThrough is the regression
// guard for #3716. The original RotateBlocklistedNATEIPs built a
// providers.ProviderCreds{Raw} map keyed with the OpenTofu *tfvars*
// names (`huawei_access_key` etc.) and handed it to
// credsFromProviderCreds(), which reads the BARE signing keys
// (`access_key`/`secret_key`/`project_id`). The key-name mismatch made
// the function fail with "huawei: access_key is required" on EVERY
// Huawei prov — even when a valid access_key WAS supplied — so the
// poisoned-EIP self-heal never ran and hw151–154 wedged with no egress
// to harbor.openova.io.
//
// We cannot point endpointFor() at an httptest server without a wider
// refactor, so we assert the contract at the creds boundary:
//   - a SUPPLIED access_key must NOT yield "access_key is required"
//     (the call proceeds to the HTTP layer and fails there on DNS/dial,
//     which is a DIFFERENT error) — this is exactly what the bug broke;
//   - an EMPTY access_key MUST yield "access_key is required".
func TestRotateBlocklistedNATEIPs_PassesAccessKeyThrough(t *testing.T) {
	// Short ctx so the real signed request to nat.<region>.kom4dc
	// .nationalcloud.om (which does not resolve from CI) fails fast
	// instead of hanging on the httpTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	t.Run("supplied access_key is not reported missing", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"AKIA-test-key", "secret-key-of-decent-length-32chrs", "0123456789abcdef0123456789abcdef",
			"me-east-215", nil)
		if err == nil {
			// A nil error would mean the HTTP call somehow succeeded — not
			// expected from CI, but it definitively proves the creds were
			// accepted, so it satisfies the regression guard.
			return
		}
		if strings.Contains(err.Error(), "access_key is required") {
			t.Fatalf("#3716 regression: valid access_key reported as missing — creds wiring broken: %v", err)
		}
		if strings.Contains(err.Error(), "secret_key is required") ||
			strings.Contains(err.Error(), "project_id is required") {
			t.Fatalf("#3716 regression: valid creds reported as missing: %v", err)
		}
	})

	t.Run("empty access_key fails closed", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"", "secret-key", "project-id", "me-east-215", nil)
		if err == nil || !strings.Contains(err.Error(), "access_key is required") {
			t.Fatalf("empty access_key must fail with 'access_key is required', got: %v", err)
		}
	})

	t.Run("empty secret_key fails closed", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"AKIA-test-key", "", "project-id", "me-east-215", nil)
		if err == nil || !strings.Contains(err.Error(), "secret_key is required") {
			t.Fatalf("empty secret_key must fail with 'secret_key is required', got: %v", err)
		}
	})
}

// TestPreflightBlocklist_SeedAndEnvMerge guards the blocklist() helper —
// the seed addresses are always present and CATALYST_HUAWEI_NAT_EIP_BLOCKLIST
// extends (never replaces) them. The poisoned-pool self-heal leans on
// operators being able to add freshly-discovered bad EIPs without a code
// change.
func TestPreflightBlocklist_SeedAndEnvMerge(t *testing.T) {
	for _, seed := range []string{"212.72.24.48", "212.72.24.14"} {
		if !blocklist()[seed] {
			t.Fatalf("seed blocklist missing %s", seed)
		}
	}

	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", " 45.151.123.77 , 45.151.123.88 ")
	bl := blocklist()
	for _, want := range []string{"212.72.24.48", "212.72.24.14", "45.151.123.77", "45.151.123.88"} {
		if !bl[want] {
			t.Fatalf("merged blocklist missing %s (got %v)", want, bl)
		}
	}
}
