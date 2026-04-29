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
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// jsonMarshal is a tiny helper so the GHCR-token serialization-leak test
// reads naturally: the test asserts on a string, the helper produces a
// string from the JSON-marshaled Request.
func jsonMarshal(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// validBase returns a Request with the non-Region fields filled so the
// downstream "hetzner token is required" / "SSH public key is required" /
// etc. checks don't short-circuit the test for the field under exam.
//
// validBase intentionally leaves SovereignDomainMode unset — tests that
// exercise the GHCR-token gating path set DomainMode=pool explicitly so
// the validator's pool-only branch fires. Tests for back-compat paths
// keep DomainMode empty (treated as BYO for validation purposes).
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

// ─────────────────────────────────────────────────────────────────────────
// GHCR pull token coverage — the durable fix for the
// `secrets "ghcr-pull" not found` Phase-1 stall verified live on
// omantel.omani.works pre-fix.
// ─────────────────────────────────────────────────────────────────────────

// TestNew_ReadsGHCRPullTokenFromEnv proves provisioner.New() picks up
// CATALYST_GHCR_PULL_TOKEN from the process env. The catalyst chart
// mounts the value from the `catalyst-ghcr-pull-token` Secret in the
// catalyst namespace; this test mirrors the deployment-time wiring.
func TestNew_ReadsGHCRPullTokenFromEnv(t *testing.T) {
	const tok = "ghp_TEST_TOKEN_FOR_NEW_READ_DO_NOT_LEAK"
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", tok)

	p := New()
	if p.GHCRPullToken != tok {
		t.Fatalf("New() did not read CATALYST_GHCR_PULL_TOKEN: got %q, want %q", p.GHCRPullToken, tok)
	}
}

// TestNew_TolerantOfMissingGHCRPullToken proves the catalyst-api Pod
// can come up cleanly when the env var (and therefore the underlying
// K8s Secret) is missing — the chart's secretKeyRef has optional=true
// for exactly this reason. Validate() then rejects managed-pool
// deployments with a clear error pointing at the rotation runbook;
// BYO-flow endpoints continue to work.
func TestNew_TolerantOfMissingGHCRPullToken(t *testing.T) {
	// Force the env var unset so the test is deterministic regardless of
	// the runner's environment.
	os.Unsetenv("CATALYST_GHCR_PULL_TOKEN")

	p := New()
	if p.GHCRPullToken != "" {
		t.Fatalf("New() should leave GHCRPullToken empty when env is missing, got %q", p.GHCRPullToken)
	}
	// And the Provisioner must still be wired with the other fields the
	// rest of the code path reads — proves the whole struct didn't
	// short-circuit on the missing env.
	if p.ModulePath == "" || p.WorkDir == "" {
		t.Fatalf("New() returned an under-populated Provisioner: %+v", p)
	}
}

// TestValidate_PoolDomainMode_RejectsEmptyGHCRPullToken proves a managed-
// pool deployment fails fast at /api/v1/deployments POST time when the
// catalyst-api Pod was launched without the token. The error message
// must point at the secret name + rotation runbook so an operator
// chasing an unprovisioned Sovereign sees the fix path immediately.
func TestValidate_PoolDomainMode_RejectsEmptyGHCRPullToken(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.SovereignDomainMode = "pool"
	r.SovereignPoolDomain = "omani.works"
	r.SovereignSubdomain = "acme"
	// GHCRPullToken intentionally empty.

	err := r.Validate()
	if err == nil {
		t.Fatalf("pool-mode + empty GHCRPullToken should be rejected")
	}
	// Operator-facing error: must mention the env var name and the
	// runbook. A generic "token required" string would force the
	// operator to grep the source.
	msg := err.Error()
	if !strings.Contains(msg, "CATALYST_GHCR_PULL_TOKEN") {
		t.Errorf("error must reference CATALYST_GHCR_PULL_TOKEN env var, got %q", msg)
	}
	if !strings.Contains(msg, "SECRET-ROTATION") {
		t.Errorf("error must reference docs/SECRET-ROTATION.md, got %q", msg)
	}
}

// TestValidate_PoolDomainMode_AcceptsNonEmptyGHCRPullToken is the
// happy-path counterpart — managed-pool deployment with a token in
// the Request validates cleanly.
func TestValidate_PoolDomainMode_AcceptsNonEmptyGHCRPullToken(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.SovereignDomainMode = "pool"
	r.SovereignPoolDomain = "omani.works"
	r.SovereignSubdomain = "acme"
	r.GHCRPullToken = "ghp_TEST_VALID_FORMAT_NOT_REAL"

	if err := r.Validate(); err != nil {
		t.Fatalf("pool-mode + non-empty GHCRPullToken should pass: %v", err)
	}
}

// TestValidate_BYOMode_AcceptsEmptyGHCRPullToken — the catalyst-api
// Pod must keep working for BYO deployments when the token is missing.
// BYO Sovereigns will still hit Phase-1 GHCR pulls on their own
// cluster; that gating is Flow B's concern (issue #169) and lives
// downstream. Here we prove only that the validator does NOT block
// BYO submission when the catalyst-api was deployed without the
// Secret rolled out.
func TestValidate_BYOMode_AcceptsEmptyGHCRPullToken(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.SovereignDomainMode = "byo"
	// GHCRPullToken intentionally empty.

	if err := r.Validate(); err != nil {
		t.Fatalf("byo-mode + empty GHCRPullToken should pass: %v", err)
	}
}

// TestRequest_GHCRPullToken_NotSerialized proves the json:"-" tag is
// load-bearing: the persistence agent's Redact() in internal/store
// already drops every credential field, but keeping this one off the
// wire entirely is the simpler invariant. A regression that drops the
// `json:"-"` tag would land here as a test failure rather than as a
// silent leak through any path that marshals a Request.
func TestRequest_GHCRPullToken_NotSerialized(t *testing.T) {
	const sentinel = "ghp_LEAKED_IF_BROKEN_NOT_REAL"
	r := Request{GHCRPullToken: sentinel}

	// Use the same json package the persistence + handler layers use.
	// json.Marshal on a Request with the field tagged json:"-" must
	// produce output that does NOT contain the sentinel.
	raw, err := jsonMarshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(raw, sentinel) {
		t.Fatalf("Request.GHCRPullToken leaked through json.Marshal output:\n%s", raw)
	}
}
