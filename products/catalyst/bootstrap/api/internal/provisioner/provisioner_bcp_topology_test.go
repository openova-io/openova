// provisioner_bcp_topology_test.go — G93.1 (Refs #2666) coverage for
// the BCP-topology seam.
//
// Pillar 3 zero-transactions-lost requires that every multi-region
// Sovereign land on the active-hotstandby CNPG shape automatically.
// Pre-G93.1 the operator had to flip SOVEREIGN_ENABLE_HOT_STANDBY=true
// on a per-Sovereign overlay AFTER provisioning; every multi-region
// prov in production silently shipped Pillar 3 broken until that
// manual step. These tests pin the four observable behaviours that
// together close that gap end-to-end:
//
//  1. deriveBcpTopology auto-defaults empty → active-hotstandby when
//     len(Regions) >= 2; empty + single-region → single-region;
//     explicit value preserved.
//  2. Validate() rejects unknown topology strings with a clear 400.
//  3. Validate() rejects active-hotstandby (or active-active) when
//     len(Regions) < 2 so a hand-crafted POST cannot lie about
//     Pillar 3 readiness.
//  4. writeTfvars emits enable_hot_standby="true" + bcp_topology=
//     "active-hotstandby" for the canonical 2-region prov so the
//     downstream cloud-init Kustomization substitute resolves
//     correctly.
package provisioner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 1. deriveBcpTopology default-derivation rule ──────────────────────

func TestDeriveBcpTopology_DefaultsToActiveHotStandbyOnMultiRegion(t *testing.T) {
	req := Request{
		Regions: []RegionSpec{
			{Provider: "hetzner", CloudRegion: "fsn1"},
			{Provider: "hetzner", CloudRegion: "hel1"},
		},
	}
	got := deriveBcpTopology(req)
	if got != BcpTopologyActiveHotStandby {
		t.Fatalf("deriveBcpTopology(2-region, empty) = %q, want %q", got, BcpTopologyActiveHotStandby)
	}
}

func TestDeriveBcpTopology_DefaultsToSingleRegionOnSingleRegion(t *testing.T) {
	req := Request{
		Regions: []RegionSpec{
			{Provider: "hetzner", CloudRegion: "fsn1"},
		},
	}
	got := deriveBcpTopology(req)
	if got != BcpTopologySingleRegion {
		t.Fatalf("deriveBcpTopology(1-region, empty) = %q, want %q", got, BcpTopologySingleRegion)
	}
}

func TestDeriveBcpTopology_DefaultsToSingleRegionOnEmptyRegions(t *testing.T) {
	// Back-compat single-region payload (no Regions[] supplied) MUST
	// land on single-region — never silently promote to
	// active-hotstandby just because someone forgot the field.
	req := Request{}
	got := deriveBcpTopology(req)
	if got != BcpTopologySingleRegion {
		t.Fatalf("deriveBcpTopology(no-regions, empty) = %q, want %q", got, BcpTopologySingleRegion)
	}
}

func TestDeriveBcpTopology_PreservesExplicit(t *testing.T) {
	for _, explicit := range []string{
		BcpTopologySingleRegion,
		BcpTopologyActiveHotStandby,
		BcpTopologyActiveActive,
	} {
		req := Request{
			BcpTopology: explicit,
			Regions: []RegionSpec{
				{Provider: "hetzner", CloudRegion: "fsn1"},
				{Provider: "hetzner", CloudRegion: "hel1"},
			},
		}
		if got := deriveBcpTopology(req); got != explicit {
			t.Fatalf("deriveBcpTopology(explicit=%q) = %q, want %q", explicit, got, explicit)
		}
	}
}

// ── 2. bcpTopologyEnableHotStandby boolean mapping ────────────────────

func TestBcpTopologyEnableHotStandby(t *testing.T) {
	cases := []struct {
		topology string
		want     string
	}{
		{BcpTopologySingleRegion, "false"},
		{BcpTopologyActiveHotStandby, "true"},
		{BcpTopologyActiveActive, "true"},
		{"", "false"},          // unset → safe default
		{"garbage", "false"},   // unknown → safe default
	}
	for _, c := range cases {
		if got := bcpTopologyEnableHotStandby(c.topology); got != c.want {
			t.Errorf("bcpTopologyEnableHotStandby(%q) = %q, want %q", c.topology, got, c.want)
		}
	}
}

// ── 3. Validate() rejects unknown / mismatched topologies ─────────────

func TestValidate_RejectsUnknownBcpTopology(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = "highly-available"
	err := req.Validate()
	if err == nil {
		t.Fatal("Validate(): want error for unknown bcpTopology, got nil")
	}
	if !strings.Contains(err.Error(), "bcpTopology") {
		t.Fatalf("Validate(): error should name bcpTopology, got %q", err.Error())
	}
}

func TestValidate_RejectsActiveHotStandbyOnSingleRegion(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = BcpTopologyActiveHotStandby
	// Only one region — operator is making a claim the prov can't honour.
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
	}
	err := req.Validate()
	if err == nil {
		t.Fatal("Validate(): want error for active-hotstandby + 1 region, got nil")
	}
	if !strings.Contains(err.Error(), "requires len(regions)>=2") {
		t.Fatalf("Validate(): error should explain Pillar-3 invariant, got %q", err.Error())
	}
}

func TestValidate_RejectsActiveActiveOnSingleRegion(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = BcpTopologyActiveActive
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
	}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate(): want error for active-active + 1 region, got nil")
	}
}

func TestValidate_NormalisesCaseAndWhitespace(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = "  Active-HotStandby "
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx32"},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if req.BcpTopology != BcpTopologyActiveHotStandby {
		t.Fatalf("Validate(): topology not normalised, got %q want %q", req.BcpTopology, BcpTopologyActiveHotStandby)
	}
}

func TestValidate_AutoDerivesOnMultiRegionEmpty(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = "" // unset
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx32"},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if req.BcpTopology != BcpTopologyActiveHotStandby {
		t.Fatalf("Validate(): empty topology + 2 regions should auto-derive active-hotstandby, got %q", req.BcpTopology)
	}
}

func TestValidate_AutoDerivesOnSingleRegionEmpty(t *testing.T) {
	req := baseValidateRequest(t)
	req.BcpTopology = ""
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if req.BcpTopology != BcpTopologySingleRegion {
		t.Fatalf("Validate(): empty topology + 1 region should auto-derive single-region, got %q", req.BcpTopology)
	}
}

// ── 4. writeTfvars emits enable_hot_standby + bcp_topology ────────────

func TestWriteTfvars_EmitsActiveHotStandbyForMultiRegion(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-bcp-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	req := tfvarsRequest(t)
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32"},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx32"},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "tofu.auto.tfvars.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if got := out["bcp_topology"]; got != BcpTopologyActiveHotStandby {
		t.Errorf("bcp_topology = %v, want %q", got, BcpTopologyActiveHotStandby)
	}
	if got := out["enable_hot_standby"]; got != "true" {
		t.Errorf("enable_hot_standby = %v, want %q", got, "true")
	}
}

func TestWriteTfvars_EmitsSingleRegionForLegacyPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-bcp-sr-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	req := tfvarsRequest(t)
	// Legacy single-region payload (no Regions[] supplied) — exercises
	// the back-compat path the wizard used pre-multi-region work.
	req.Regions = nil
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tofu.auto.tfvars.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if got := out["bcp_topology"]; got != BcpTopologySingleRegion {
		t.Errorf("bcp_topology = %v, want %q", got, BcpTopologySingleRegion)
	}
	if got := out["enable_hot_standby"]; got != "false" {
		t.Errorf("enable_hot_standby = %v, want %q", got, "false")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────

// baseValidateRequest returns a minimally-valid Request for exercising
// Validate() without requiring every credential / bucket / SSH key the
// real handler stamps. Region SKUs are populated so the SKU-availability
// gate does not bail out before the topology check we want to test.
func baseValidateRequest(t *testing.T) *Request {
	t.Helper()
	return &Request{
		OrgName:                "Acme",
		OrgEmail:               "ops@acme.test",
		SovereignFQDN:          "acme.omani.works",
		SovereignDomainMode:    "pool",
		SovereignPoolDomain:    "omani.works",
		SovereignSubdomain:     "acme",
		Provider:               "hetzner",
		HetznerToken:           "fake-token",
		HetznerProjectID:       "fake-project",
		Region:                 "fsn1",
		ControlPlaneSize:       "cpx32",
		WorkerCount:            0,
		SSHPublicKey:           "ssh-ed25519 AAAA fake",
		GHCRPullToken:          "ghp_fake",
		HarborRobotToken:       "harbor-robot-fake",
		ObjectStorageRegion:    "fsn1",
		ObjectStorageAccessKey: "AKIA-fake",
		ObjectStorageSecretKey: "SK-fake",
		ObjectStorageBucket:    "acme-omani-works",
	}
}

// tfvarsRequest mirrors baseValidateRequest but returns a value (not a
// pointer) so writeTfvars's existing signature is satisfied.
func tfvarsRequest(t *testing.T) Request {
	t.Helper()
	r := baseValidateRequest(t)
	return *r
}
