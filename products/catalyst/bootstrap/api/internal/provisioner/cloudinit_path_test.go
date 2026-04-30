// cloudinit_path_test.go — locks the bootstrap-kit Flux Kustomization
// path against per-FQDN-directory regression (issue #218).
//
// Issue #218 (P0): every fresh Sovereign provision failed Phase-1
// because the cloud-init template selected a per-FQDN tree
// (`!/clusters/${sovereign_fqdn}`) and pointed the bootstrap-kit
// Kustomization at `./clusters/${sovereign_fqdn}/bootstrap-kit` — a
// directory that was NEVER committed before provisioning. Flux on
// the new cluster reconciled with:
//
//	stat /tmp/kustomization-…/clusters/<fqdn>/bootstrap-kit:
//	  no such file or directory
//
// Canonical fix: GitRepository selects `!/clusters/_template`,
// Kustomization paths point at `clusters/_template/{bootstrap-kit,
// infrastructure}`, and Flux's `postBuild.substitute.SOVEREIGN_FQDN`
// interpolates the Sovereign's FQDN into the template manifests at
// apply time.
//
// These tests pin every part of that fix. A regression that re-
// introduces per-FQDN paths into the cloud-init template lands here
// as a test failure, NOT as a stalled Phase-1 on a customer's first
// Sovereign.
package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modulePath resolves the canonical OpenTofu module directory from the
// test binary's CWD. The provisioner package lives at
// products/catalyst/bootstrap/api/internal/provisioner, so the module
// is six directories up. We resolve via filepath.Abs to keep the test
// stable when `go test` runs in different working directories
// (toolchain default is the package dir).
func modulePath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// products/catalyst/bootstrap/api/internal/provisioner → repo root
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	return filepath.Join(repoRoot, "infra", "hetzner")
}

// readCloudInit reads the canonical cloud-init control-plane template
// from infra/hetzner/cloudinit-control-plane.tftpl.
func readCloudInit(t *testing.T) string {
	t.Helper()
	p := filepath.Join(modulePath(t), "cloudinit-control-plane.tftpl")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

// TestCloudInit_GitRepositoryIgnoreSelectsTemplate proves the
// GitRepository's `spec.ignore` selects the shared `clusters/_template`
// tree, NOT a per-FQDN directory. Issue #218's failure mode was the
// `!/clusters/${sovereign_fqdn}` selector which referenced a
// directory that no provisioning step ever creates.
func TestCloudInit_GitRepositoryIgnoreSelectsTemplate(t *testing.T) {
	tpl := readCloudInit(t)

	if !strings.Contains(tpl, "!/clusters/_template") {
		t.Errorf("GitRepository.spec.ignore must include `!/clusters/_template` to select the shared template tree")
	}
	// The per-FQDN selector that issue #218 identified as the root
	// cause must NOT reappear in operative YAML. A regression here
	// would silently send every fresh Sovereign back to the original
	// failure mode. The pre-fix line was bare in the YAML
	// `ignore:` block (no leading `#`); the per-issue-218 commit
	// retains a comment that quotes the old form for context. Scope
	// the check to non-comment lines so the explanatory text is
	// allowed.
	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(line, "!/clusters/${sovereign_fqdn}") {
			t.Errorf("line %d carries per-FQDN selector `!/clusters/${sovereign_fqdn}` outside a comment — issue #218 regression:\n  %s", i+1, line)
		}
	}
}

// TestCloudInit_BootstrapKitPathPointsAtTemplate proves the
// bootstrap-kit Kustomization's `spec.path` is the shared
// `./clusters/_template/bootstrap-kit` directory, not a per-FQDN
// path that doesn't exist before provisioning runs.
func TestCloudInit_BootstrapKitPathPointsAtTemplate(t *testing.T) {
	tpl := readCloudInit(t)

	const want = "path: ./clusters/_template/bootstrap-kit"
	if !strings.Contains(tpl, want) {
		t.Errorf("bootstrap-kit Kustomization.spec.path must be %q (was missing — issue #218 fix not in place)", want)
	}
	// Per-FQDN regression guard: the pre-fix path string was
	// `./clusters/${sovereign_fqdn}/bootstrap-kit` and produced
	//   stat /tmp/kustomization-…/clusters/<fqdn>/bootstrap-kit:
	//     no such file or directory
	// on every provision. Lock it out.
	const banned = "path: ./clusters/${sovereign_fqdn}/bootstrap-kit"
	if strings.Contains(tpl, banned) {
		t.Errorf("bootstrap-kit Kustomization.spec.path must NOT be per-FQDN %q (issue #218 regression)", banned)
	}
}

// TestCloudInit_InfrastructurePathPointsAtTemplate is the sibling
// guard for the second Kustomization (infrastructure-config) which
// installs Provider + ProviderConfig + Compositions and depends on
// bootstrap-kit. Same shape, same regression class.
func TestCloudInit_InfrastructurePathPointsAtTemplate(t *testing.T) {
	tpl := readCloudInit(t)

	const want = "path: ./clusters/_template/infrastructure"
	if !strings.Contains(tpl, want) {
		t.Errorf("infrastructure-config Kustomization.spec.path must be %q (was missing — issue #218 fix not in place)", want)
	}
	const banned = "path: ./clusters/${sovereign_fqdn}/infrastructure"
	if strings.Contains(tpl, banned) {
		t.Errorf("infrastructure-config Kustomization.spec.path must NOT be per-FQDN %q (issue #218 regression)", banned)
	}
}

// TestCloudInit_PostBuildSubstituteFQDN proves both Kustomizations
// declare a `postBuild.substitute.SOVEREIGN_FQDN: ${sovereign_fqdn}`
// hook, which is what makes the shared template tree usable per-
// Sovereign. Without this hook the manifests would render with the
// literal `${SOVEREIGN_FQDN}` placeholder in label values, ingress
// hostnames, and HelmRelease values — producing pods labelled
// `catalyst.openova.io/sovereign=${SOVEREIGN_FQDN}` instead of the
// actual FQDN, and console/admin/api ingress hostnames pointing at
// `${SOVEREIGN_FQDN}` instead of the real DNS records.
func TestCloudInit_PostBuildSubstituteFQDN(t *testing.T) {
	tpl := readCloudInit(t)

	// Cheap structural check: the substitute key must appear with the
	// correct envsubst-style RHS that pulls the FQDN from the
	// rendered cloud-init context. The `${sovereign_fqdn}` form is
	// the OpenTofu template variable; OpenTofu interpolates it
	// before the cloud-init userdata leaves the catalyst-api Pod.
	const want = "SOVEREIGN_FQDN: ${sovereign_fqdn}"
	if !strings.Contains(tpl, want) {
		t.Errorf("Flux Kustomization postBuild.substitute must include %q so ${SOVEREIGN_FQDN} placeholders in clusters/_template render correctly (issue #218)", want)
	}

	// Both Kustomizations need the substitute, not just one. The
	// presence-count is a defence against a partial-fix regression
	// where one Kustomization gets the postBuild and the other
	// doesn't (which would manifest as infrastructure-config
	// rendering provider-hcloud manifests with literal
	// `${SOVEREIGN_FQDN}` in labels).
	count := strings.Count(tpl, want)
	if count < 2 {
		t.Errorf("expected SOVEREIGN_FQDN substitute on BOTH Kustomizations (got %d occurrences, want ≥2 — issue #218 partial-fix regression)", count)
	}
}

// TestCloudInit_NoPerFQDNPathReferences is the catch-all regression
// guard: NO substring of the form `clusters/${sovereign_fqdn}`
// appears as an actual Flux resource path/ignore selector anywhere
// in the rendered cloud-init template. Comments are out-of-scope
// (we may legitimately reference the prior shape in the
// "issue #218" explanatory comment), but operative YAML keys
// (`path:`, `ignore:`) MUST NOT carry that string.
func TestCloudInit_NoPerFQDNPathReferences(t *testing.T) {
	tpl := readCloudInit(t)

	// Walk lines; flag any line that is operative YAML (not a
	// pure-comment line whose first non-whitespace char is `#`)
	// and contains `clusters/${sovereign_fqdn}`.
	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // comment — explanatory references are fine
		}
		if !strings.Contains(line, "clusters/${sovereign_fqdn}") {
			continue
		}
		t.Errorf("line %d carries per-FQDN path `clusters/${sovereign_fqdn}` outside a comment — issue #218 regression:\n  %s", i+1, line)
	}
}
