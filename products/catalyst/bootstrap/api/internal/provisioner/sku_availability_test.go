// Package provisioner — sku_availability_test.go (issue #916).
//
// Two-sided enforcement check: this Go validator MUST agree with the
// wizard-side `isSkuAvailableInRegion` in
// `products/catalyst/bootstrap/ui/src/shared/constants/providerSizes.ts`
// for every SKU+region pair the matrix knows about. When a regression
// here fails to reject otech109's exact failure mode (cpx32 + ash),
// `tofu apply` will get to ~T+41s before Hetzner rejects the worker
// creation, leaving the CP + LB + firewall orphaned in Hetzner — see
// the issue body for the full failure-mode description.
//
// The test list mirrors the wizard's providerSizes.test.ts cases so a
// future audit can grep for "cpx32 + ash" and find both sides
// asserting the same fact.

package provisioner

import (
	"strings"
	"testing"
)

func TestIsSkuAvailableInRegion(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		sku      string
		region   string
		want     bool
	}{
		// Unknown SKU: skip (let downstream report).
		{"unknown sku → skip", "hetzner", "cpx9999", "fsn1", true},

		// No constraint registered: orderable everywhere.
		{"cpx22 in fsn1 (no constraint)", "hetzner", "cpx22", "fsn1", true},
		{"cpx22 in ash (no constraint)", "hetzner", "cpx22", "ash", true},
		{"cpx52 in fsn1 (no constraint)", "hetzner", "cpx52", "fsn1", true},

		// cpx32 — EU only (otech109 root cause).
		{"cpx32 in fsn1 OK", "hetzner", "cpx32", "fsn1", true},
		{"cpx32 in nbg1 OK", "hetzner", "cpx32", "nbg1", true},
		{"cpx32 in hel1 OK", "hetzner", "cpx32", "hel1", true},
		{"cpx32 in ash REJECT (otech109 evidence)", "hetzner", "cpx32", "ash", false},
		{"cpx32 in hil REJECT", "hetzner", "cpx32", "hil", false},

		// cpx21/cpx31 — empty list = orderable nowhere new (issue #752).
		{"cpx21 in fsn1 REJECT", "hetzner", "cpx21", "fsn1", false},
		{"cpx21 in ash REJECT", "hetzner", "cpx21", "ash", false},
		{"cpx31 in fsn1 REJECT", "hetzner", "cpx31", "fsn1", false},
		{"cpx31 in ash REJECT", "hetzner", "cpx31", "ash", false},

		// Hyperscaler SKUs: no entries → orderable everywhere.
		{"AWS m6i.xlarge in eu-central-1", "aws", "m6i.xlarge", "eu-central-1", true},
		{"Azure Standard_D4s_v5 in westeurope", "azure", "Standard_D4s_v5", "westeurope", true},
		{"OCI VM.Standard.E5.Flex.2.16 in eu-frankfurt-1", "oci", "VM.Standard.E5.Flex.2.16", "eu-frankfurt-1", true},

		// Case-insensitive region matching.
		{"cpx32 in FSN1 (uppercase)", "hetzner", "cpx32", "FSN1", true},
		{"cpx32 in ASH (uppercase)", "hetzner", "cpx32", "ASH", false},

		// Empty inputs → skip (surrounding Validate handles required).
		{"empty provider → skip", "", "cpx32", "fsn1", true},
		{"empty sku → skip", "hetzner", "", "fsn1", true},
		{"empty region → skip", "hetzner", "cpx32", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSkuAvailableInRegion(tt.provider, tt.sku, tt.region)
			if got != tt.want {
				t.Errorf(
					"IsSkuAvailableInRegion(%q, %q, %q) = %v, want %v",
					tt.provider, tt.sku, tt.region, got, tt.want,
				)
			}
		})
	}
}

func TestAvailableRegionsForSku(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		sku         string
		wantOk      bool
		wantRegions []string
	}{
		{"cpx32 returns EU regions", "hetzner", "cpx32", true, []string{"fsn1", "nbg1", "hel1"}},
		{"cpx21 returns empty list (orderable nowhere new)", "hetzner", "cpx21", true, []string{}},
		{"cpx31 returns empty list", "hetzner", "cpx31", true, []string{}},
		{"cpx22 unconstrained", "hetzner", "cpx22", false, nil},
		{"unknown SKU unconstrained", "hetzner", "cpx9999", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			regions, ok := AvailableRegionsForSku(tt.provider, tt.sku)
			if ok != tt.wantOk {
				t.Errorf("AvailableRegionsForSku ok = %v, want %v", ok, tt.wantOk)
			}
			if !sliceEqual(regions, tt.wantRegions) {
				t.Errorf("AvailableRegionsForSku regions = %v, want %v", regions, tt.wantRegions)
			}
		})
	}
}

func TestRequestValidate_RejectsCpx32InAsh_Issue916(t *testing.T) {
	// Otech109's exact failure mode — Phase 0 already created CP +
	// network + LB + firewall before the worker creation got rejected
	// 41s in. Validate() MUST catch this BEFORE the request reaches
	// the OpenTofu module so we never leak orphans into Hetzner.
	req := validBase()
	req.HarborRobotToken = "test-harbor-robot-token-not-real"
	req.Regions = []RegionSpec{{
		Provider:         "hetzner",
		CloudRegion:      "ash",
		ControlPlaneSize: "cpx22", // OK in ash (unconstrained)
		WorkerSize:       "cpx32", // NOT orderable in ash — must reject
		WorkerCount:      3,
	}}
	err := req.Validate()
	if err == nil {
		t.Fatal("Validate() accepted cpx32 worker in ash — expected rejection (issue #916)")
	}
	if !strings.Contains(err.Error(), "cpx32") {
		t.Errorf("Validate() error did not mention 'cpx32': %v", err)
	}
	if !strings.Contains(err.Error(), "ash") {
		t.Errorf("Validate() error did not mention 'ash': %v", err)
	}
	// Suggestion list is operator-actionable, not just a "no":
	if !strings.Contains(err.Error(), "fsn1") {
		t.Errorf("Validate() error did not surface alternative regions (fsn1/nbg1/hel1): %v", err)
	}
}

func TestRequestValidate_RejectsCpx32CpInAsh_Issue916(t *testing.T) {
	// Same root cause but the operator picked cpx32 as the CP SKU.
	req := validBase()
	req.HarborRobotToken = "test-harbor-robot-token-not-real"
	req.Regions = []RegionSpec{{
		Provider:         "hetzner",
		CloudRegion:      "ash",
		ControlPlaneSize: "cpx32", // NOT orderable in ash
		WorkerSize:       "",
		WorkerCount:      0,
	}}
	err := req.Validate()
	if err == nil {
		t.Fatal("Validate() accepted cpx32 CP in ash — expected rejection")
	}
	if !strings.Contains(err.Error(), "controlPlaneSize") {
		t.Errorf("Validate() error did not blame controlPlaneSize: %v", err)
	}
}

func TestRequestValidate_AcceptsCpx32InFsn1_Issue916(t *testing.T) {
	// Sanity check — the canonical EU topology must NOT be rejected
	// by the issue #916 SKU/region gate. Validate() may still surface
	// other unrelated requirements (Harbor robot token, etc.) that
	// guard later phases; this test pinpoints the #916 gate by
	// asserting the error message — when present — does NOT mention
	// SKU or region. A green pass (err == nil) is also acceptable.
	req := validBase()
	req.HarborRobotToken = "test-harbor-robot-token-not-real"
	req.Regions = []RegionSpec{{
		Provider:         "hetzner",
		CloudRegion:      "fsn1",
		ControlPlaneSize: "cpx22",
		WorkerSize:       "cpx32",
		WorkerCount:      3,
	}}
	err := req.Validate()
	if err == nil {
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "cpx22") ||
		strings.Contains(msg, "cpx32") ||
		strings.Contains(msg, "fsn1") ||
		strings.Contains(msg, "not orderable") {
		t.Fatalf("issue #916 gate falsely rejected canonical EU topology: %v", err)
	}
}

func TestRequestValidate_LegacySingularPath_RejectsCpx32InAsh_Issue916(t *testing.T) {
	// Pre-multi-region wizard payloads (Regions == nil) feed the
	// singular ControlPlaneSize/WorkerSize/Region fields. The legacy
	// path is hetzner-only and must catch the same regression.
	req := validBase()
	req.HarborRobotToken = "test-harbor-robot-token-not-real"
	req.Regions = nil
	req.Region = "ash"
	req.ControlPlaneSize = "cpx22"
	req.WorkerSize = "cpx32"
	req.WorkerCount = 3
	err := req.Validate()
	if err == nil {
		t.Fatal("Validate() accepted cpx32 worker in ash via legacy path — expected rejection")
	}
	if !strings.Contains(err.Error(), "cpx32") {
		t.Errorf("legacy path error did not mention cpx32: %v", err)
	}
}

func TestRequestValidate_RejectsCpx21Anywhere_Issue752Plus916(t *testing.T) {
	// cpx21 has empty availableRegions — orderable nowhere new.
	for _, region := range []string{"fsn1", "nbg1", "hel1", "ash", "hil"} {
		t.Run("cpx21 in "+region, func(t *testing.T) {
			req := validBase()
			req.Regions = []RegionSpec{{
				Provider:         "hetzner",
				CloudRegion:      region,
				ControlPlaneSize: "cpx21",
				WorkerCount:      0,
			}}
			err := req.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted cpx21 in %s — expected rejection", region)
			}
			if !strings.Contains(err.Error(), "cpx21") {
				t.Errorf("error missing cpx21: %v", err)
			}
		})
	}
}

// sliceEqual — small local helper. The matrix only stores tiny slices
// so an O(n²) comparator is fine.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
