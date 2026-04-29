// Package provisioner — unit tests for Request.Validate.
//
// Covers the per-provider rework:
//   1. Empty Regions falls back to the legacy singular fields (back-compat
//      path used by handler/load_test.go and any pre-rework wizard payload).
//   2. Non-empty Regions mirrors Regions[0] into the legacy singular fields
//      so writeTfvars()'s single-region apply path keeps working.
//   3. Per-region validation errors fire (provider, cloudRegion,
//      controlPlaneSize required; workerSize required when workerCount > 0).
package provisioner

import (
	"strings"
	"testing"
)

// validBase returns a Request with the non-Region fields filled so the
// downstream "hetzner token is required" / "SSH public key is required" /
// etc. checks don't short-circuit the test for the field under exam.
func validBase() Request {
	return Request{
		OrgName:          "ACME",
		OrgEmail:         "ops@acme.io",
		SovereignFQDN:    "acme.openova.io",
		HetznerToken:     "TEST-TOKEN-NOT-REAL",
		HetznerProjectID: "test-project",
		SSHPublicKey:     "ssh-ed25519 AAAA test-not-a-real-key",
	}
}

func TestValidate_EmptyRegions_UsesLegacySingularFields(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.WorkerSize = "cx32"
	r.WorkerCount = 0

	if err := r.Validate(); err != nil {
		t.Fatalf("empty Regions + valid singular fields should pass: %v", err)
	}
	if r.Region != "fsn1" {
		t.Errorf("legacy Region was clobbered: got %q, want fsn1", r.Region)
	}
}

func TestValidate_EmptyRegions_RejectsMissingRegion(t *testing.T) {
	r := validBase()
	// Region intentionally empty — the legacy fallback path must reject.
	if err := r.Validate(); err == nil {
		t.Fatalf("empty Regions + empty Region should be rejected")
	}
}

func TestValidate_NonEmptyRegions_MirrorsIndex0ToSingularFields(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx42", WorkerSize: "cx32", WorkerCount: 2},
		{Provider: "aws", CloudRegion: "eu-west-1", ControlPlaneSize: "m6i.xlarge", WorkerSize: "m6i.xlarge", WorkerCount: 0},
	}

	if err := r.Validate(); err != nil {
		t.Fatalf("valid Regions should pass: %v", err)
	}
	if r.Region != "fsn1" {
		t.Errorf("Region was not mirrored from Regions[0]: got %q, want fsn1", r.Region)
	}
	if r.ControlPlaneSize != "cx42" {
		t.Errorf("ControlPlaneSize was not mirrored: got %q", r.ControlPlaneSize)
	}
	if r.WorkerSize != "cx32" {
		t.Errorf("WorkerSize was not mirrored: got %q", r.WorkerSize)
	}
	if r.WorkerCount != 2 {
		t.Errorf("WorkerCount was not mirrored: got %d", r.WorkerCount)
	}
}

func TestValidate_RegionsEntry_RequiresProvider(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "", CloudRegion: "fsn1", ControlPlaneSize: "cx42"},
	}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider-required error, got %v", err)
	}
}

func TestValidate_RegionsEntry_RequiresCloudRegion(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "", ControlPlaneSize: "cx42"},
	}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "cloudRegion") {
		t.Fatalf("expected cloudRegion-required error, got %v", err)
	}
}

func TestValidate_RegionsEntry_RequiresControlPlaneSize(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: ""},
	}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "controlPlaneSize") {
		t.Fatalf("expected controlPlaneSize-required error, got %v", err)
	}
}

func TestValidate_RegionsEntry_RequiresWorkerSizeWhenCountGtZero(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx42", WorkerSize: "", WorkerCount: 3},
	}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "workerSize") {
		t.Fatalf("expected workerSize-required error when count>0, got %v", err)
	}
}

func TestValidate_RegionsEntry_AcceptsZeroWorkers(t *testing.T) {
	r := validBase()
	// Solo deployment — workerCount=0 means no workers, workerSize is allowed
	// to be empty.
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx42", WorkerSize: "", WorkerCount: 0},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("workerCount=0 + empty workerSize should pass: %v", err)
	}
}

func TestValidate_RegionsEntry_RejectsNegativeWorkerCount(t *testing.T) {
	r := validBase()
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx42", WorkerCount: -1},
	}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "workerCount") {
		t.Fatalf("expected workerCount-non-negative error, got %v", err)
	}
}
