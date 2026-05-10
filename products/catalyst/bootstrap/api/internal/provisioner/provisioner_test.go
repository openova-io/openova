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
//
// Object Storage fields (issue #371) are pre-populated with a valid
// fsn1 bucket triple so non-Object-Storage validation paths don't trip
// the new required-field checks. The handler derives the bucket name
// from the FQDN at runtime; here we hardcode a matching slug so the
// provisioner-layer tests don't depend on handler-layer logic.
func validBase() Request {
	return Request{
		OrgName:                "ACME",
		OrgEmail:               "ops@acme.io",
		SovereignFQDN:          "acme.openova.io",
		HetznerToken:           "TEST-TOKEN-NOT-REAL",
		HetznerProjectID:       "test-project",
		SSHPublicKey:           "ssh-ed25519 AAAA test-not-a-real-key",
		// HarborRobotToken — issue #557 tightened Validate() to require
		// this on every Request (Inviolable Principle #11: every Sovereign
		// image pull MUST go through harbor.openova.io; falling through to
		// docker.io is not allowed). Production catalyst-api reads
		// CATALYST_HARBOR_ROBOT_TOKEN from the env at New() and stamps
		// it into every Request; the tests need to mirror that contract.
		HarborRobotToken:       "test-harbor-robot-token-not-real",
		ObjectStorageRegion:    "fsn1",
		ObjectStorageAccessKey: "TESTACCESSKEY1234567",
		ObjectStorageSecretKey: "TESTSECRETKEY1234567890123456789012345678",
		ObjectStorageBucket:    "catalyst-acme-openova-io",
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

// ─────────────────────────────────────────────────────────────────────────
// Hetzner Object Storage credentials coverage (issue #371). All four
// fields are required: a missing region / access / secret / bucket has
// to surface as 400 at /api/v1/deployments POST time, not 5 minutes
// into `tofu apply` against an unauthorised S3 endpoint.
// ─────────────────────────────────────────────────────────────────────────

func TestValidate_ObjectStorage_RejectsEmptyRegion(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageRegion = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "object storage region") {
		t.Fatalf("expected object storage region required, got %v", err)
	}
}

func TestValidate_ObjectStorage_RejectsInvalidRegion(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageRegion = "us-east-1" // not a Hetzner OS region
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "fsn1") {
		t.Fatalf("expected fsn1/nbg1/hel1 enumerated error, got %v", err)
	}
}

func TestValidate_ObjectStorage_RejectsEmptyAccessKey(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageAccessKey = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("expected access key required, got %v", err)
	}
}

func TestValidate_ObjectStorage_RejectsEmptySecretKey(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageSecretKey = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Fatalf("expected secret key required, got %v", err)
	}
}

func TestValidate_ObjectStorage_RejectsEmptyBucket(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageBucket = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("expected bucket required, got %v", err)
	}
}

func TestValidate_ObjectStorage_RejectsBadBucketName(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageBucket = "BAD_BUCKET_NAME" // uppercase + underscore — fails S3 RFC
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("expected bucket-name RFC violation, got %v", err)
	}
}

func TestValidate_ObjectStorage_AcceptsAllValidRegions(t *testing.T) {
	for _, region := range []string{"fsn1", "nbg1", "hel1"} {
		r := validBase()
		r.Region = "fsn1"
		r.ControlPlaneSize = "cx42"
		r.ObjectStorageRegion = region
		if err := r.Validate(); err != nil {
			t.Errorf("region %q should validate: %v", region, err)
		}
	}
}

// TestRequest_ObjectStorageSecretKey_Serialized proves the secret key
// IS serialised in the wire format (it has json:"objectStorageSecretKey"
// not json:"-"). The wizard's CreateDeployment POST carries it; the
// store's Redact path is what scrubs it from on-disk records. This
// test just guards against an accidental json:"-" change that would
// silently drop the key from the wire and break provisioning.
func TestRequest_ObjectStorageSecretKey_Serialized(t *testing.T) {
	const sentinel = "test-secret-key-must-appear-in-wire"
	r := Request{ObjectStorageSecretKey: sentinel}
	raw, err := jsonMarshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(raw, sentinel) {
		t.Fatalf("ObjectStorageSecretKey must serialise to wire (wizard payload depends on it):\n%s", raw)
	}
}

// TestWriteTfvars_OmitsEmptySingularSizes proves writeTfvars does NOT emit
// "control_plane_size": "" / "worker_size": "" when the legacy singular
// fields are empty. An empty string in tofu.auto.tfvars.json overrides the
// variables.tf default ("cpx21" / "cpx31") with "" — which fails the SKU
// regex validator at `tofu plan`. Live failure surfaced on otech85
// (DID a3c32a2b82758007, 2026-05-04 11:04:27Z) when the autopilot launched
// the cost-optimized-defaults verification cycle without per-request
// SKU overrides.
func TestWriteTfvars_OmitsEmptySingularSizes(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	// Zero-override request: every singular SKU field empty, WorkerCount
	// set explicitly to 2 (the wizard default). Every required identity
	// + secret field populated so writeTfvars can run.
	req := Request{
		SovereignFQDN:    "otech85.omani.works",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		// ControlPlaneSize / WorkerSize intentionally empty.
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}

	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read tfvars: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse tfvars: %v", err)
	}
	if _, ok := parsed["control_plane_size"]; ok {
		t.Fatalf("control_plane_size MUST be omitted when empty (variables.tf default cpx21 takes effect). Got: %s", string(raw))
	}
	if _, ok := parsed["worker_size"]; ok {
		t.Fatalf("worker_size MUST be omitted when empty (variables.tf default cpx31 takes effect). Got: %s", string(raw))
	}
	// worker_count is always emitted (zero is a valid solo-Sovereign
	// choice; the wizard always sends 2 by default).
	if v, ok := parsed["worker_count"]; !ok || v.(float64) != 2 {
		t.Fatalf("worker_count must be emitted with the request value (2). Got: %v", parsed["worker_count"])
	}
}

// TestWriteTfvars_EmitsRegionsAsEmptyArrayNotNull proves writeTfvars
// emits an empty JSON array for `regions` when the request has no
// per-region overrides — never JSON null. The OpenTofu module's
// variables.tf has a validation block (`for r in var.regions`) that
// fails on null with "Error: Iteration over null value" but accepts
// an empty list. Live failure on otech86 (DID 103c52d08510006f,
// 2026-05-04 11:12:43Z).
func TestWriteTfvars_EmitsRegionsAsEmptyArrayNotNull(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "otech86.omani.works",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		// Regions intentionally nil — the legacy singular path.
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Must contain `"regions": []` — never `"regions": null`.
	if strings.Contains(string(raw), `"regions": null`) {
		t.Fatalf("regions must serialise as [] (not null) so OpenTofu's `for r in var.regions` validator accepts the input. Got:\n%s", string(raw))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	regions, ok := parsed["regions"].([]any)
	if !ok {
		t.Fatalf("regions must be a JSON array, got %T (%v)", parsed["regions"], parsed["regions"])
	}
	if len(regions) != 0 {
		t.Fatalf("regions must be empty when request has none, got %d entries", len(regions))
	}
}

// TestWriteTfvars_EmitsSingularSizesWhenSet proves writeTfvars DOES emit
// the singular SKU fields when the operator sets them explicitly. Guards
// against a regression where an over-eager omission rule drops legitimate
// operator overrides.
func TestWriteTfvars_EmitsSingularSizesWhenSet(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "otech85.omani.works",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		ControlPlaneSize: "cpx52",
		WorkerSize:       "cpx41",
		WorkerCount:      3,
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read tfvars: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse tfvars: %v", err)
	}
	if v, _ := parsed["control_plane_size"].(string); v != "cpx52" {
		t.Fatalf("control_plane_size must round-trip operator override: got %q want cpx52", v)
	}
	if v, _ := parsed["worker_size"].(string); v != "cpx41" {
		t.Fatalf("worker_size must round-trip operator override: got %q want cpx41", v)
	}
}

// TestWriteTfvars_QAFixtures_DefaultDisabled proves writeTfvars emits
// qa_fixtures_enabled="false" + qa_test_session_enabled="false" when the
// caller does NOT set Request.QATestEnabled. This is the customer-Sovereign
// invariant: a zero-touch provision MUST NOT spawn the qaFixtures stack
// (qa-<sov> ns + qa-wp Application + Continuum CR + CNPGPair + PDM CRs +
// status-seeder Jobs + tier-bound UserAccess seeder) on a customer's
// production Sovereign — those resources are QA scaffolding, not customer
// workloads. Fix #73 root-cause: the provisioner never threaded the
// toggle so the chart's `${QA_FIXTURES_ENABLED:-false}` default fired
// uniformly — including on QA Sovereigns where ~140 matrix TCs depend
// on the fixtures being present.
func TestWriteTfvars_QAFixtures_DefaultDisabled(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-qa-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "acme-customer.openova.io",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		// QATestEnabled intentionally false (customer Sovereign).
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, _ := parsed["qa_fixtures_enabled"].(string); v != "false" {
		t.Fatalf("qa_fixtures_enabled MUST default 'false' on customer Sovereigns, got %q", v)
	}
	if v, _ := parsed["qa_test_session_enabled"].(string); v != "false" {
		t.Fatalf("qa_test_session_enabled MUST default 'false' on customer Sovereigns, got %q", v)
	}
	// Fix #123 — wildcard_cert_use_staging MUST default 'false' so a
	// customer Sovereign issues real-trusted production LE certs (not
	// Fake-LE-Intermediate-X1 staging certs that browsers reject).
	if v, _ := parsed["wildcard_cert_use_staging"].(string); v != "false" {
		t.Fatalf("wildcard_cert_use_staging MUST default 'false' on customer Sovereigns (real-trusted production certs), got %q", v)
	}
}

// TestWriteTfvars_QAFixtures_EnabledDerivesNamespaceAndOrg proves that when
// QATestEnabled=true, writeTfvars emits qa_fixtures_enabled="true" AND
// derives qa_fixtures_namespace + qa_organization from the SovereignFQDN's
// first DNS label per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode).
//
// "qa.example.com" → "qa-qa" / "qa-platform"  (first label is "qa")
// "omantel.biz"    → "qa-omantel" / "omantel-platform"
// "demo.openova.io" → "qa-demo" / "demo-platform"
//
// Without this derivation the chart's bootstrapping defaults
// ("qa-omantel" / "omantel-platform") would leak onto every QA Sovereign
// — exactly the cross-Sovereign collision shape principle #4 forbids.
func TestWriteTfvars_QAFixtures_EnabledDerivesNamespaceAndOrg(t *testing.T) {
	cases := []struct {
		name        string
		fqdn        string
		wantNs      string
		wantOrgName string
	}{
		{name: "omantel", fqdn: "omantel.biz", wantNs: "qa-omantel", wantOrgName: "omantel-platform"},
		{name: "qa-prefix", fqdn: "qa.example.com", wantNs: "qa-qa", wantOrgName: "qa-platform"},
		{name: "demo-openova", fqdn: "demo.openova.io", wantNs: "qa-demo", wantOrgName: "demo-platform"},
		{name: "uppercase-input-normalised", fqdn: "QA.Example.COM", wantNs: "qa-qa", wantOrgName: "qa-platform"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "writeTfvars-qa-on-*")
			if err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			defer os.RemoveAll(dir)

			req := Request{
				SovereignFQDN:    tc.fqdn,
				OrgName:          "QA",
				OrgEmail:         "qa@openova.io",
				HetznerToken:     "tok",
				HetznerProjectID: "p1",
				Region:           "fsn1",
				WorkerCount:      2,
				QATestEnabled:    true,
			}
			if err := writeTfvars(dir, req); err != nil {
				t.Fatalf("writeTfvars: %v", err)
			}
			raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if v, _ := parsed["qa_fixtures_enabled"].(string); v != "true" {
				t.Errorf("qa_fixtures_enabled: got %q want \"true\"", v)
			}
			if v, _ := parsed["qa_test_session_enabled"].(string); v != "true" {
				t.Errorf("qa_test_session_enabled: got %q want \"true\"", v)
			}
			// Fix #123 — wildcard_cert_use_staging auto-flips 'true' on QA
			// Sovereigns so the Sovereign issues from LE staging (separate
			// generous rate limits) instead of production. Without this the
			// wipe + re-provision cadence of QA Sovereigns trips the
			// production 5/168h ceiling within hours.
			if v, _ := parsed["wildcard_cert_use_staging"].(string); v != "true" {
				t.Errorf("wildcard_cert_use_staging: got %q want \"true\" (QA Sovereigns MUST issue staging certs to bypass LE production rate limit)", v)
			}
			if v, _ := parsed["qa_fixtures_namespace"].(string); v != tc.wantNs {
				t.Errorf("qa_fixtures_namespace: got %q want %q (derived from FQDN first label)", v, tc.wantNs)
			}
			if v, _ := parsed["qa_organization"].(string); v != tc.wantOrgName {
				t.Errorf("qa_organization: got %q want %q (derived from FQDN first label)", v, tc.wantOrgName)
			}
		})
	}
}

// TestWriteTfvars_QAFixtures_OperatorOverrideWins proves that explicit
// Request.QAFixturesNamespace / QAOrganization values take precedence
// over the FQDN-derived defaults. Reserved for the rare future case
// where one Sovereign hosts multiple isolated QA tenants.
func TestWriteTfvars_QAFixtures_OperatorOverrideWins(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-qa-override-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:       "omantel.biz",
		OrgName:             "QA",
		OrgEmail:            "qa@openova.io",
		HetznerToken:        "tok",
		HetznerProjectID:    "p1",
		Region:              "fsn1",
		WorkerCount:         2,
		QATestEnabled:       true,
		QAFixturesNamespace: "qa-team-alpha",
		QAOrganization:      "team-alpha-platform",
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, _ := parsed["qa_fixtures_namespace"].(string); v != "qa-team-alpha" {
		t.Errorf("qa_fixtures_namespace: operator override must win, got %q", v)
	}
	if v, _ := parsed["qa_organization"].(string); v != "team-alpha-platform" {
		t.Errorf("qa_organization: operator override must win, got %q", v)
	}
}
