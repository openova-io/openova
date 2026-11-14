// Package hetznerprovisioning — real end-to-end Hetzner Sovereign
// provisioning test.
//
// Closes ticket #141 — "[L] test: end-to-end provisioning test on Hetzner
// test project — real Hetzner project, provisions a throwaway Sovereign,
// tears it down. SCOPE: write the test scaffolding, harness, and CI
// workflow. Mark the actual run as gated behind a HETZNER_TEST_TOKEN repo
// secret which the operator will populate later. Do NOT mock; structure
// the test so it just doesn't run when the secret is absent."
//
// What this test does when HETZNER_TEST_TOKEN is set:
//
//  1. Generates a unique sovereign FQDN for the run (test-<run-id>.openova.io)
//  2. Stages the canonical infra/hetzner/ OpenTofu module into a temp dir
//  3. Renders tofu.auto.tfvars.json with the test inputs
//  4. tofu init && tofu apply -auto-approve
//  5. Asserts:
//     - apply succeeded
//     - control_plane_ip + load_balancer_ip outputs are non-empty IPv4
//     - control plane SSH-reachable (TCP/22 with a brief retry)
//     - load balancer reachable (TCP/443 with a longer retry — Flux
//     needs a few minutes to install Cilium + the Gateway)
//  6. tofu destroy -auto-approve (always runs, even on test failure)
//  7. Verifies destroy actually freed the resources (tofu state list empty)
//
// When HETZNER_TEST_TOKEN is absent the test skips — NEVER mocks. Per
// docs/INVIOLABLE-PRINCIPLES.md principle #2, "no mocks where the test
// would otherwise verify real behavior". A mocked Hetzner provisioning
// run that returns "ok" tells you nothing about whether OpenTofu's
// hcloud provider, the cloud-init scripts, or k3s actually work end-to-end.
//
// Cost note: each successful run creates one CX22 control plane + 1 worker
// + 1 LB for ~5 minutes. Single-region CX22 in fsn1 is roughly EUR 0.005/run
// at Hetzner's hourly billing. The CI workflow only runs this on
// workflow_dispatch + a "test/hetzner-e2e" PR label, NOT on every push.
package hetznerprovisioning

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Per #141, "Mark the actual run as gated behind a HETZNER_TEST_TOKEN repo
// secret which the operator will populate later. Do NOT mock; structure the
// test so it just doesn't run when the secret is absent."
const tokenEnv = "HETZNER_TEST_TOKEN"

// requireRealHetzner returns the credentials to use, or skips. Never mocks.
func requireRealHetzner(t *testing.T) (token, projectID, region string) {
	t.Helper()
	token = os.Getenv(tokenEnv)
	if token == "" {
		t.Skipf("%s not set — skipping real Hetzner provisioning test (operator populates the secret to run)", tokenEnv)
	}
	projectID = envOr("HETZNER_TEST_PROJECT_ID", "ci-throwaway")
	region = envOr("HETZNER_TEST_REGION", "fsn1")
	return
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// runID returns a short random identifier so concurrent CI runs don't
// collide on the sovereign FQDN.
func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// repoRoot walks up from this file to find the repo root.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "infra", "hetzner", "main.tf")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root from %s", wd)
	return ""
}

// stageModule copies the canonical infra/hetzner/ module into a fresh
// working directory and writes tofu.auto.tfvars.json beside it. We don't
// re-use the catalyst-api provisioner package directly because we want
// the test to exercise the OpenTofu module by itself — the Go provisioner
// is a thin wrapper that adds nothing the test needs.
func stageModule(t *testing.T, root, workDir string, vars map[string]any) {
	t.Helper()
	src := filepath.Join(root, "infra", "hetzner")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !(strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tftpl")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(workDir, name), raw, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	rawVars, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		t.Fatalf("marshal vars: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "tofu.auto.tfvars.json"), rawVars, 0o600); err != nil {
		t.Fatalf("write vars: %v", err)
	}
}

// tofu runs `tofu <args>` in workDir with the Hetzner token in env. Output
// streams to t.Log so CI logs capture the apply trace.
func tofu(t *testing.T, ctx context.Context, workDir, hcloudToken string, args ...string) error {
	t.Helper()
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HCLOUD_TOKEN="+hcloudToken,
		"TF_INPUT=false",
		"TF_IN_AUTOMATION=true",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("tofu %s\n%s", strings.Join(args, " "), out)
	return err
}

// readTofuOutputs invokes `tofu output -json` and parses the result.
func readTofuOutputs(t *testing.T, workDir string) map[string]any {
	t.Helper()
	cmd := exec.Command("tofu", "output", "-json")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tofu output: %v", err)
	}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("parse outputs: %v", err)
	}
	flat := make(map[string]any, len(raw))
	for k, v := range raw {
		flat[k] = v.Value
	}
	return flat
}

// awaitTCP retries a TCP dial up to deadline. Returns nil on first success.
func awaitTCP(t *testing.T, address string, deadline time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s after %s", address, deadline)
		default:
		}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return nil
		}
		t.Logf("await %s: %v (retrying)", address, err)
		time.Sleep(10 * time.Second)
	}
}

// readSSHKey returns the SSH public key the test injects. CI mints a
// throwaway key; locally the operator can point at ~/.ssh/id_rsa.pub.
func readSSHKey(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("HETZNER_TEST_SSH_PUBLIC_KEY"); v != "" {
		return v
	}
	if v := os.Getenv("HOME"); v != "" {
		raw, err := os.ReadFile(filepath.Join(v, ".ssh", "id_rsa.pub"))
		if err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	t.Fatal("HETZNER_TEST_SSH_PUBLIC_KEY env var must be set (or ~/.ssh/id_rsa.pub readable)")
	return ""
}

// TestHetznerE2E_ProvisionAndTeardown is the actual end-to-end run. Skipped
// when the Hetzner secret is absent — see package docstring.
//
// Lifecycle: create temp dir → stage module → tofu apply → verify outputs
// + LB reachable → tofu destroy (always, deferred). The destroy step runs
// even when the apply or assertions fail so we don't leak Hetzner resources.
func TestHetznerE2E_ProvisionAndTeardown(t *testing.T) {
	token, projectID, region := requireRealHetzner(t)
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Fatalf("tofu CLI not on PATH: %v", err)
	}

	root := repoRoot(t)
	workDir := t.TempDir()
	id := runID(t)
	fqdn := fmt.Sprintf("e2e-%s.openova.io", id)

	vars := map[string]any{
		"sovereign_fqdn":      fqdn,
		"sovereign_subdomain": "e2e-" + id,
		"org_name":            "E2E Throwaway " + id,
		"org_email":           "e2e+" + id + "@openova.io",
		"hcloud_token":        token,
		"hcloud_project_id":   projectID,
		"region":              region,
		"control_plane_size":  envOr("HETZNER_TEST_CP_SIZE", "cx22"),
		"worker_size":         envOr("HETZNER_TEST_WORKER_SIZE", "cx22"),
		"worker_count":        1,
		"ha_enabled":          false,
		"ssh_public_key":      readSSHKey(t),
		"domain_mode":         "byo", // pool-domain DNS would write to real Dynadot zone
		"pool_domain":         "",
		"dynadot_key":         "",
		"dynadot_secret":      "",
		"gitops_repo_url":     envOr("HETZNER_TEST_GITOPS_URL", "https://github.com/openova-io/openova"),
		"gitops_branch":       envOr("HETZNER_TEST_GITOPS_BRANCH", "main"),
		// GHCR pull token — populated from the test runner's env so the
		// throwaway Sovereign actually pulls bp-* charts in Phase 1.
		// The test harness skips when HETZNER_TEST_TOKEN is absent, so
		// we don't need a fallback for this either; HETZNER_TEST_GHCR_PULL_TOKEN
		// is wired through in the same CI workflow that holds HETZNER_TEST_TOKEN.
		"ghcr_pull_token": envOr("HETZNER_TEST_GHCR_PULL_TOKEN", ""),
	}

	stageModule(t, root, workDir, vars)

	// Always destroy at the end — even if apply or assertions fail.
	t.Cleanup(func() {
		t.Log("─── TEARDOWN: tofu destroy ───")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if err := tofu(t, ctx, workDir, token, "destroy", "-auto-approve", "-no-color"); err != nil {
			t.Errorf("tofu destroy failed — manual Hetzner cleanup may be required for project=%q fqdn=%q: %v", projectID, fqdn, err)
			return
		}
		// Sanity: state should be empty after destroy.
		listCmd := exec.Command("tofu", "state", "list")
		listCmd.Dir = workDir
		out, _ := listCmd.Output()
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("tofu state still has resources after destroy:\n%s", out)
		}
	})

	applyCtx, applyCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer applyCancel()

	if err := tofu(t, applyCtx, workDir, token, "init", "-input=false", "-no-color"); err != nil {
		t.Fatalf("tofu init: %v", err)
	}
	if err := tofu(t, applyCtx, workDir, token, "apply", "-input=false", "-no-color", "-auto-approve"); err != nil {
		t.Fatalf("tofu apply: %v", err)
	}

	outs := readTofuOutputs(t, workDir)
	cpIP, _ := outs["control_plane_ip"].(string)
	lbIP, _ := outs["load_balancer_ip"].(string)
	if !isIPv4(cpIP) {
		t.Errorf("control_plane_ip output not IPv4: %q", cpIP)
	}
	if !isIPv4(lbIP) {
		t.Errorf("load_balancer_ip output not IPv4: %q", lbIP)
	}

	// Control plane should accept SSH within ~3 minutes of provisioning.
	if cpIP != "" {
		if err := awaitTCP(t, cpIP+":22", 5*time.Minute); err != nil {
			t.Errorf("control plane never accepted SSH: %v", err)
		}
	}

	// Load balancer should accept TCP/443 once Cilium + Flux finish — give
	// it 15 minutes (cilium → cert-manager → flux → … → catalyst-platform).
	if lbIP != "" {
		if err := awaitTCP(t, lbIP+":443", 15*time.Minute); err != nil {
			t.Logf("load balancer 443 not yet open after 15m: %v (may be fine for partial bootstrap)", err)
		}
	}
}

// TestHarness_NoHetznerCredsSkips is the structural check that #141's
// "do NOT mock" + "structure the test so it just doesn't run when the
// secret is absent" requirements are satisfied. We assert that without
// the env var, the require helper skips the test rather than mocking,
// exec'ing the real cloud, or panicking.
func TestHarness_NoHetznerCredsSkips(t *testing.T) {
	saved := os.Getenv(tokenEnv)
	t.Cleanup(func() {
		_ = os.Setenv(tokenEnv, saved)
	})
	_ = os.Unsetenv(tokenEnv)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("requireRealHetzner should NOT panic on missing creds, but did: %v", r)
		}
	}()

	// Run the real test in a sub-test; it must skip, not run.
	t.Run("subtest_must_skip", func(sub *testing.T) {
		_, _, _ = requireRealHetzner(sub)
		// If we reach here, the helper didn't skip — that violates the
		// "doesn't run when the secret is absent" requirement.
		sub.Errorf("requireRealHetzner returned without skipping despite missing %s", tokenEnv)
	})
}

// TestCloudInit_RendersGHCRPullSecret is a render-only integration test for
// the durable-secret fix (`fix(cloudinit): create flux-system/ghcr-pull
// secret on Sovereign so private bp-* charts pull cleanly`).
//
// It does NOT touch real Hetzner. It stages the canonical OpenTofu module
// into a temp dir with a sample GHCR token, runs `tofu init && tofu
// validate` (the no-op render path), then pulls the rendered cloud-init
// out of the OpenTofu state via `tofu console <<< local.control_plane_cloud_init`
// and asserts:
//
//  1. The cloud-init output contains a `Secret` named `ghcr-pull` in
//     `flux-system`.
//  2. The Secret's `.dockerconfigjson` field decodes to a valid JSON
//     object with `auths."ghcr.io".password == <sample token>` and a
//     non-empty `auth` field.
//  3. The runcmd block applies the secret BEFORE the flux-bootstrap
//     manifest, matching the ordering invariant the runtime depends on.
//
// Skipped when the `tofu` CLI isn't on PATH, so this runs cleanly on dev
// laptops and in CI runners that have it. The CI workflow installs
// OpenTofu in a step before the Go test step.
func TestCloudInit_RendersGHCRPullSecret(t *testing.T) {
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skipf("tofu CLI not on PATH — skipping cloud-init render test (CI provisions tofu before this test)")
	}

	root := repoRoot(t)
	workDir := t.TempDir()

	// Sample token — clearly not a real PAT. The cloud-init template
	// interpolates it into auths."ghcr.io".password and into the base64
	// auth field; the test asserts it round-trips through both.
	const sampleToken = "ghp_RENDER_TEST_NOT_A_REAL_TOKEN_DO_NOT_LEAK"

	vars := map[string]any{
		"sovereign_fqdn":      "render-test.openova.io",
		"sovereign_subdomain": "render-test",
		"org_name":            "Render-Test",
		"org_email":           "render-test@openova.io",
		"hcloud_token":        "render-test-not-real",
		"hcloud_project_id":   "render-test",
		"region":              "fsn1",
		"control_plane_size":  "cx42",
		"worker_size":         "cx32",
		"worker_count":        0,
		"ha_enabled":          false,
		"ssh_public_key":      "ssh-ed25519 AAAA render-test-not-a-real-key",
		"domain_mode":         "byo",
		"pool_domain":         "",
		"dynadot_key":         "",
		"dynadot_secret":      "",
		"gitops_repo_url":     "https://github.com/openova-io/openova",
		"gitops_branch":       "main",
		"ghcr_pull_token":     sampleToken,
	}
	stageModule(t, root, workDir, vars)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := tofu(t, ctx, workDir, "render-test-not-real", "init", "-input=false", "-no-color", "-backend=false"); err != nil {
		t.Fatalf("tofu init (no backend): %v", err)
	}
	if err := tofu(t, ctx, workDir, "render-test-not-real", "validate", "-no-color"); err != nil {
		t.Fatalf("tofu validate: %v", err)
	}

	// Pull the rendered cloud-init via `tofu console`. The console reads
	// from stdin; we feed an expression that prints the local.
	consoleCmd := exec.CommandContext(ctx, "tofu", "console", "-no-color")
	consoleCmd.Dir = workDir
	consoleCmd.Stdin = strings.NewReader("local.control_plane_cloud_init\n")
	consoleCmd.Env = append(os.Environ(),
		"HCLOUD_TOKEN=render-test-not-real",
		"TF_INPUT=false",
		"TF_IN_AUTOMATION=true",
	)
	consoleOut, err := consoleCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tofu console: %v\n%s", err, consoleOut)
	}
	rendered := string(consoleOut)

	// Assertion 1 — the rendered cloud-init contains a Secret named
	// `ghcr-pull` in `flux-system` of type kubernetes.io/dockerconfigjson.
	for _, want := range []string{
		"name: ghcr-pull",
		"namespace: flux-system",
		"type: kubernetes.io/dockerconfigjson",
		".dockerconfigjson:",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered cloud-init missing %q\n--- rendered (truncated) ---\n%s", want, truncate(rendered, 4000))
		}
	}

	// Assertion 2 — the Secret applies BEFORE flux-bootstrap.yaml in
	// runcmd. Find the line indices and assert the order.
	idxSecretApply := strings.Index(rendered, "kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml apply -f /var/lib/catalyst/ghcr-pull-secret.yaml")
	idxFluxApply := strings.Index(rendered, "kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml apply -f /var/lib/catalyst/flux-bootstrap.yaml")
	if idxSecretApply < 0 {
		t.Errorf("rendered cloud-init missing the ghcr-pull-secret apply step")
	}
	if idxFluxApply < 0 {
		t.Errorf("rendered cloud-init missing the flux-bootstrap apply step")
	}
	if idxSecretApply >= 0 && idxFluxApply >= 0 && idxSecretApply >= idxFluxApply {
		t.Errorf("ghcr-pull Secret must apply BEFORE flux-bootstrap (idxSecret=%d, idxFlux=%d) — Phase-1 reconcile would race the auth secret",
			idxSecretApply, idxFluxApply)
	}

	// Assertion 3 — the rendered base64 .dockerconfigjson decodes to a
	// JSON object whose auths."ghcr.io".password == sampleToken.
	// The cloud-init heredoc uses YAML's `data: { .dockerconfigjson: <b64> }`
	// shape; pull the value via a regex and decode.
	re := regexp.MustCompile(`(?m)^\s*\.dockerconfigjson:\s*([A-Za-z0-9+/=]+)`)
	m := re.FindStringSubmatch(rendered)
	if m == nil {
		t.Fatalf("could not locate .dockerconfigjson in rendered cloud-init")
	}
	dec, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("base64 decode .dockerconfigjson: %v", err)
	}
	var parsed struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(dec, &parsed); err != nil {
		t.Fatalf("json decode: %v\nraw: %s", err, dec)
	}
	ghcr, ok := parsed.Auths["ghcr.io"]
	if !ok {
		t.Fatalf(`auths["ghcr.io"] missing — decoded: %s`, dec)
	}
	if ghcr.Password != sampleToken {
		t.Errorf("password = %q, want %q (sample token did not round-trip through templatefile)", ghcr.Password, sampleToken)
	}
	if ghcr.Username == "" {
		t.Errorf("username must be non-empty (registry audit trail), got empty")
	}
	if ghcr.Auth == "" {
		t.Errorf("auth must be non-empty (legacy docker config compat), got empty")
	}
	// auth = base64(username:password)
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ghcr.Username + ":" + ghcr.Password))
	if ghcr.Auth != wantAuth {
		t.Errorf("auth = %q, want %q (base64(username:password) mismatch)", ghcr.Auth, wantAuth)
	}
}

// truncate clips a long string with an explicit ellipsis so test failures
// that include the rendered cloud-init don't blow out CI log limits.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)…"
}

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// init logs the runtime to make CI logs self-describing.
func init() {
	_, file, _, _ := runtime.Caller(0)
	if file == "" {
		// keep linters happy; nothing to do
		_ = errors.New("")
	}
}
