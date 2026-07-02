// provisioner_storage_class_test.go — #4057 coverage for the
// operator-choosable default StorageClass seam (founder point #1: "storage
// class is an INPUT chosen by the user, with defaults").
//
// The class threads Request.StorageClass → writeTfvars `default_storage_class`
// tofu var → the cloud-init `SOVEREIGN_CNPG_STORAGE_CLASS` substitute (which
// previously HARDCODED the per-provider literal) → the host-shared CNPG
// Cluster CRs (slots 10/19/52) + the bp-cnpg / bp-mgmt-vcluster default class.
//
// These tests pin the wire:
//
//  1. Empty + Hetzner → hcloud-volumes (the per-provider durable CSI default).
//  2. Empty + Huawei  → evs-ssd        (the per-provider durable CSI default).
//  3. Explicit value  → preserved verbatim (operator chose a class).
//  4. Per-region value folds into the umbrella when the umbrella is empty.
//  5. local-path is NEVER emitted — the default is always a durable cloud class.
package provisioner

import "testing"

// huaweiTfvarsRequest returns a minimal valid Huawei Request for the
// storage-class tests. The Huawei credentials are `json:"-"` server-side
// fields, so they are set directly here (the same way the handler stamps them
// from env at POST time).
func huaweiTfvarsRequest(t *testing.T) Request {
	t.Helper()
	r := baseValidateRequest(t)
	r.Provider = "huawei"
	// Hetzner creds are not required once Provider=huawei; the Huawei triplet is.
	r.HetznerToken = ""
	r.HetznerProjectID = ""
	r.HuaweiAccessKey = "AKIA-huawei-fake"
	r.HuaweiSecretKey = "SK-huawei-fake"
	r.HuaweiProjectID = "f27698137bdc4b00ad509cf27f1e5547"
	r.HuaweiRegion = "me-east-215"
	r.Region = "me-east-215-a"
	r.ControlPlaneSize = "s7n.large.4"
	return *r
}

// 1. Empty StorageClass on a Hetzner prov resolves to the durable hcloud CSI
//    default — never empty, never local-path.
func TestWriteTfvars_StorageClass_HetznerDefault(t *testing.T) {
	req := tfvarsRequest(t) // Hetzner base; StorageClass left empty.
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion

	out := writeTfvarsSharedPG(t, req)

	if got := out["default_storage_class"]; got != StorageClassDefaultHetzner {
		t.Errorf("default_storage_class = %v, want %q (empty input → per-provider Hetzner CSI default)", got, StorageClassDefaultHetzner)
	}
}

// 2. Empty StorageClass on a Huawei prov resolves to the durable EVS CSI
//    default (evs-ssd) — the bp-huawei-evs-csi slot-55b default class.
func TestWriteTfvars_StorageClass_HuaweiDefault(t *testing.T) {
	req := huaweiTfvarsRequest(t) // StorageClass left empty.
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion

	out := writeTfvarsSharedPG(t, req)

	if got := out["default_storage_class"]; got != StorageClassDefaultHuawei {
		t.Errorf("default_storage_class = %v, want %q (empty input → per-provider Huawei CSI default)", got, StorageClassDefaultHuawei)
	}
}

// 3. An explicit operator choice is preserved verbatim (the founder's "INPUT
//    chosen by the user" requirement). Surrounding whitespace is trimmed.
func TestWriteTfvars_StorageClass_ExplicitHonored(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	req.StorageClass = "  hcloud-volumes-xfs  " // operator chose a non-default class.

	out := writeTfvarsSharedPG(t, req)

	if got := out["default_storage_class"]; got != "hcloud-volumes-xfs" {
		t.Errorf("default_storage_class = %v, want %q (explicit operator choice preserved, trimmed)", got, "hcloud-volumes-xfs")
	}
}

// 4. A per-region StorageClass folds into the umbrella value when the umbrella
//    field is empty (mirrors the ControlPlaneSize/WorkerSize Regions[0]
//    derivation), so a Regions-only payload still surfaces a class to tofu.
func TestWriteTfvars_StorageClass_PerRegionFoldsToUmbrella(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	req.StorageClass = "" // umbrella empty.
	req.Regions = []RegionSpec{
		{
			Provider:         "hetzner",
			CloudRegion:      "fsn1",
			ControlPlaneSize: "cpx32",
			WorkerCount:      0,
			StorageClass:     "hcloud-volumes-fast",
		},
	}

	out := writeTfvarsSharedPG(t, req)

	if got := out["default_storage_class"]; got != "hcloud-volumes-fast" {
		t.Errorf("default_storage_class = %v, want %q (Regions[0].StorageClass folded into umbrella)", got, "hcloud-volumes-fast")
	}
}

// 5. An umbrella StorageClass wins over a per-region value (the common wizard
//    shape sets the class at the top level).
func TestWriteTfvars_StorageClass_UmbrellaWinsOverRegion(t *testing.T) {
	req := tfvarsRequest(t)
	// #4706 — deliberate single-region shape (implicit 1-region is rejected).
	req.BcpTopology = BcpTopologySingleRegion
	req.StorageClass = "hcloud-volumes" // umbrella set.
	req.Regions = []RegionSpec{
		{
			Provider:         "hetzner",
			CloudRegion:      "fsn1",
			ControlPlaneSize: "cpx32",
			WorkerCount:      0,
			StorageClass:     "should-be-ignored",
		},
	}

	out := writeTfvarsSharedPG(t, req)

	if got := out["default_storage_class"]; got != "hcloud-volumes" {
		t.Errorf("default_storage_class = %v, want %q (umbrella value wins over per-region)", got, "hcloud-volumes")
	}
}

// DeriveStorageClass / defaultStorageClassForProvider unit coverage — the
// single source of truth for the effective class. local-path is NEVER a
// possible output.
func TestDeriveStorageClass(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		input    string
		want     string
	}{
		{"hetzner-empty", "hetzner", "", StorageClassDefaultHetzner},
		{"huawei-empty", "huawei", "", StorageClassDefaultHuawei},
		{"empty-provider-falls-back-hetzner", "", "", StorageClassDefaultHetzner},
		{"unknown-provider-falls-back-hetzner", "aws", "", StorageClassDefaultHetzner},
		{"explicit-honored", "hetzner", "my-class", "my-class"},
		{"explicit-trimmed", "huawei", "  evs-sas  ", "evs-sas"},
		{"explicit-overrides-provider-default", "huawei", "custom", "custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveStorageClass(Request{Provider: tc.provider, StorageClass: tc.input})
			if got != tc.want {
				t.Errorf("DeriveStorageClass(provider=%q, input=%q) = %q, want %q", tc.provider, tc.input, got, tc.want)
			}
			if got == "local-path" || got == "" {
				t.Errorf("DeriveStorageClass returned a FORBIDDEN/empty class %q — the default must always be a durable cloud CSI class (#3971/#892)", got)
			}
		})
	}
}

// defaultStorageClassForProvider returns the right per-provider class and is
// case/space-insensitive on the provider name.
func TestDefaultStorageClassForProvider(t *testing.T) {
	cases := map[string]string{
		"hetzner":   StorageClassDefaultHetzner,
		"Hetzner":   StorageClassDefaultHetzner,
		"  huawei ": StorageClassDefaultHuawei,
		"HUAWEI":    StorageClassDefaultHuawei,
		"":          StorageClassDefaultHetzner,
		"oci":       StorageClassDefaultHetzner,
	}
	for provider, want := range cases {
		if got := defaultStorageClassForProvider(provider); got != want {
			t.Errorf("defaultStorageClassForProvider(%q) = %q, want %q", provider, got, want)
		}
	}
}
