// cilium_values_parity_test.go — locks cloud-init's bootstrap cilium values
// against the Flux bp-cilium HelmRelease values (issue #491).
//
// Phase-8a bug #16 (otech8 deployment 2026-05-01): the bootstrap helm
// install in `infra/hetzner/cloudinit-control-plane.tftpl` used a
// MINIMAL set of `--set` flags (kubeProxyReplacement, k8sService*,
// tunnelProtocol, bpf.masquerade) while the Flux HelmRelease at
// `clusters/_template/bootstrap-kit/01-cilium.yaml` curated a much
// fuller value set via `platform/cilium/chart/values.yaml`'s `cilium:`
// block (gatewayAPI, envoy, encryption, hubble, …) PLUS the overlay
// `envoyConfig.enabled=true` + `l7Proxy=true` from PR `66ea39f0`.
//
// The drift was fatal: cilium-agent waits forever for the operator to
// register CRDs `ciliumenvoyconfigs` + `ciliumclusterwideenvoyconfigs`
// which are only registered when `envoyConfig.enabled=true`. With the
// bootstrap install missing that flag, the agent crash-looped, the
// node taint `node.cilium.io/agent-not-ready` never lifted, and the
// bootstrap-kit Kustomization (wait: true, 30 min timeout — issue
// #492) never reconciled the upgrade that would have fixed the values.
// Every fresh Hetzner Sovereign deadlocked at Phase 1.
//
// Canonical fix: cloud-init writes a `/var/lib/catalyst/cilium-values
// .yaml` file via `write_files:` and the bootstrap helm install reads
// it via `-f`. THIS test verifies the values block in cloudinit-
// control-plane.tftpl carries every operator-curated key that
// bp-cilium's HR plus the chart values.yaml overlay carries. Future
// authors who change one file but not the other land here as a test
// failure, NOT as a customer-visible Phase-1 stall.
//
// Coverage strategy: substring-presence checks on canonical YAML lines.
// We deliberately avoid YAML unmarshalling + structural equality for
// two reasons:
//   1. The cloud-init `cilium-values.yaml` is INSIDE a tftpl `content: |`
//      block with OpenTofu interpolations adjacent in the same file —
//      unmarshalling the surrounding tftpl is non-trivial and adds a
//      Terraform-specific test dep.
//   2. The chart values.yaml carries `cilium:` as the umbrella subchart
//      key while the bootstrap install consumes the upstream cilium/cilium
//      chart directly (values must be at TOP LEVEL). Structural equality
//      across a renaming boundary requires a sub-tree slice, which is
//      more code to maintain than the focused presence checks below.
//
// The presence checks lock down the load-bearing keys identified by
// the otech8 incident postmortem. New operator-curated values added
// to the chart that are NOT covered here SHOULD be added to this test
// as a follow-up — but the existing list is sufficient to prevent the
// specific deadlock #491 documents.
package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readChartValues loads platform/cilium/chart/values.yaml as a single
// string. The path is resolved relative to the test binary's CWD using
// the same modulePath traversal cloudinit_path_test.go uses.
func readChartValues(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(repoRoot, "platform", "cilium", "chart", "values.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

// readBootstrapKitOverlay loads clusters/_template/bootstrap-kit/01-cilium.yaml
// as a single string. The path is resolved the same way as readChartValues.
func readBootstrapKitOverlay(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(repoRoot, "clusters", "_template", "bootstrap-kit", "01-cilium.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

// TestCiliumValuesParity_BootstrapHasEnvoyConfigEnabled is the load-
// bearing assertion for issue #491. Without `envoyConfig.enabled: true`
// in the bootstrap install, cilium-operator never registers the
// envoyconfig CRDs and cilium-agent crash-loops on every fresh
// Sovereign. This test fails LOUDLY if a future commit removes the
// flag.
func TestCiliumValuesParity_BootstrapHasEnvoyConfigEnabled(t *testing.T) {
	tpl := readCloudInit(t)

	// We look for the cilium-values.yaml content block specifically.
	// Anchor the search to the file path so a stray `envoyConfig:`
	// elsewhere in the template (e.g. a comment block) doesn't pass.
	// The expected block contains:
	//   envoyConfig:
	//     enabled: true
	if !strings.Contains(tpl, "/var/lib/catalyst/cilium-values.yaml") {
		t.Fatalf("cloud-init must declare /var/lib/catalyst/cilium-values.yaml via write_files (issue #491)")
	}

	// envoyConfig.enabled=true must appear after the cilium-values.yaml
	// path declaration. Slice the template at that anchor and check the
	// downstream window for the keys.
	idx := strings.Index(tpl, "/var/lib/catalyst/cilium-values.yaml")
	tail := tpl[idx:]

	// Cap the window at the next write_files entry or the runcmd: marker
	// so a `envoyConfig:` line that lives in a *later* section can't
	// satisfy the check. The next `- path:` after the cilium-values
	// entry is `flux-bootstrap.yaml`.
	if next := strings.Index(tail, "- path: /var/lib/catalyst/flux-bootstrap.yaml"); next > 0 {
		tail = tail[:next]
	}

	if !strings.Contains(tail, "envoyConfig:") {
		t.Errorf("cilium-values.yaml block must declare envoyConfig: (issue #491 — agent waits for envoyconfig CRDs the operator only registers when this is true)")
	}
	// `enabled: true` must appear inside the envoyConfig: block. Cheap
	// proximity check: same window, both tokens present.
	if !strings.Contains(tail, "envoyConfig:\n        enabled: true") {
		t.Errorf("cilium-values.yaml `envoyConfig.enabled` must be `true` (issue #491). Got window:\n%s", tail)
	}
}

// TestCiliumValuesParity_BootstrapMatchesChartCoreKeys verifies the
// operator-curated keys from `platform/cilium/chart/values.yaml`'s
// `cilium:` block are all present in the bootstrap cilium-values.yaml.
// "Present" means the key name appears in the cilium-values block;
// values are spot-checked separately for the load-bearing ones.
func TestCiliumValuesParity_BootstrapMatchesChartCoreKeys(t *testing.T) {
	tpl := readCloudInit(t)
	chart := readChartValues(t)

	// Slice the cloud-init template down to the cilium-values write_files
	// content window.
	startMarker := "- path: /var/lib/catalyst/cilium-values.yaml"
	endMarker := "- path: /var/lib/catalyst/flux-bootstrap.yaml"
	si := strings.Index(tpl, startMarker)
	ei := strings.Index(tpl, endMarker)
	if si < 0 || ei < 0 || ei <= si {
		t.Fatalf("could not locate cilium-values.yaml block in cloud-init template (start=%d, end=%d)", si, ei)
	}
	bootstrapBlock := tpl[si:ei]

	// Spot-check that `cilium:` (umbrella subchart key) appears in the
	// chart values.yaml — this anchor confirms we're reading the right
	// file. If this fails the test environment is wrong, not the values.
	if !strings.Contains(chart, "\ncilium:") {
		t.Fatalf("platform/cilium/chart/values.yaml must contain top-level `cilium:` umbrella key (test environment broken — wrong file?)")
	}

	// Operator-curated keys that MUST appear in both files. Each key is
	// a top-level child of the chart's `cilium:` block; in the bootstrap
	// cilium-values.yaml they live at top level (no umbrella wrapper).
	requiredKeys := []string{
		"kubeProxyReplacement:",
		"k8sServiceHost:",
		"k8sServicePort:",
		"bpf:",
		"ipam:",
		"encryption:",
		"hubble:",
		"gatewayAPI:",
		"envoy:",
		"l2announcements:",
		"operator:",
		"resources:",
		"prometheus:",
	}

	for _, key := range requiredKeys {
		if !strings.Contains(chart, "  "+key) {
			// Chart authors should have indented the key under `cilium:`
			// with two spaces. If this fails the chart shape changed and
			// this test needs the indentation update — fail loudly.
			t.Errorf("platform/cilium/chart/values.yaml is missing two-space-indented key %q (chart shape may have changed)", key)
		}
		if !strings.Contains(bootstrapBlock, key) {
			t.Errorf("bootstrap cilium-values.yaml is missing key %q — drift from platform/cilium/chart/values.yaml (issue #491)", key)
		}
	}
}

// TestCiliumValuesParity_BootstrapMatchesOverlayKeys verifies that
// keys ADDED by the bootstrap-kit overlay at clusters/_template/
// bootstrap-kit/01-cilium.yaml are also present in the bootstrap
// cilium-values.yaml. The overlay carries `envoyConfig.enabled=true`
// and `l7Proxy: true` — both load-bearing for issue #491.
func TestCiliumValuesParity_BootstrapMatchesOverlayKeys(t *testing.T) {
	tpl := readCloudInit(t)
	overlay := readBootstrapKitOverlay(t)

	// Sanity check on the overlay: it must declare both keys.
	if !strings.Contains(overlay, "envoyConfig:") {
		t.Fatalf("clusters/_template/bootstrap-kit/01-cilium.yaml is missing `envoyConfig:` overlay (issue #491 fix `66ea39f0` reverted?)")
	}
	if !strings.Contains(overlay, "l7Proxy: true") {
		t.Fatalf("clusters/_template/bootstrap-kit/01-cilium.yaml is missing `l7Proxy: true` overlay (issue #491 fix `66ea39f0` reverted?)")
	}

	// Bootstrap cilium-values block must carry both.
	startMarker := "- path: /var/lib/catalyst/cilium-values.yaml"
	endMarker := "- path: /var/lib/catalyst/flux-bootstrap.yaml"
	si := strings.Index(tpl, startMarker)
	ei := strings.Index(tpl, endMarker)
	if si < 0 || ei < 0 || ei <= si {
		t.Fatalf("could not locate cilium-values.yaml block in cloud-init template (start=%d, end=%d)", si, ei)
	}
	bootstrapBlock := tpl[si:ei]

	if !strings.Contains(bootstrapBlock, "envoyConfig:") {
		t.Errorf("bootstrap cilium-values.yaml must declare `envoyConfig:` (issue #491)")
	}
	if !strings.Contains(bootstrapBlock, "l7Proxy: true") {
		t.Errorf("bootstrap cilium-values.yaml must declare `l7Proxy: true` (issue #491)")
	}
}

// TestCiliumValuesParity_BootstrapHelmInstallReadsValuesFile verifies
// the bootstrap helm install command in cloud-init reads the values
// file via `-f /var/lib/catalyst/cilium-values.yaml` rather than relying
// on a minimal `--set` list (the pre-issue-491 form). Without this,
// writing the values file does nothing because the install never picks
// it up.
func TestCiliumValuesParity_BootstrapHelmInstallReadsValuesFile(t *testing.T) {
	tpl := readCloudInit(t)

	// The helm install is in runcmd: as a multi-line block. The
	// canonical form is (post-#4504 retry-wrapper + idempotent install):
	//   rt helm upgrade --install cilium cilium/cilium \
	//     --version 1.16.5 \
	//     --namespace kube-system \
	//     -f /var/lib/catalyst/cilium-values.yaml
	//
	// #4504 switched the bare `helm install` to a retried, idempotent
	// `helm upgrade --install` so a transient get-helm/repo-update
	// failure no longer leaves the node with no CNI. Both forms invoke
	// the `cilium/cilium` chart; the contract this test guards is that
	// the install reads the curated values FILE (`-f`), not a minimal
	// `--set` list. Match on the chart invocation regardless of the
	// install/upgrade verb.
	if !strings.Contains(tpl, "helm install cilium cilium/cilium") &&
		!strings.Contains(tpl, "helm upgrade --install cilium cilium/cilium") {
		t.Fatalf("cloud-init must run `helm (install|upgrade --install) cilium cilium/cilium` (this is the bootstrap exception; issue #491 didn't change that, #4504 made it idempotent)")
	}
	if !strings.Contains(tpl, "-f /var/lib/catalyst/cilium-values.yaml") {
		t.Errorf("bootstrap helm install must read values via `-f /var/lib/catalyst/cilium-values.yaml` (issue #491). Without this, the values file is never consumed and we regress to the pre-#491 minimal install which crash-loops cilium-agent.")
	}

	// The pre-#491 form used a list of `--set` flags. The fix REPLACED
	// them with a single `-f` so this regression guard rejects any
	// future change that re-introduces minimal --set flags as the
	// primary value source. (--set on top of -f is fine; --set as the
	// SOLE source is the regression.)
	const banned = "--set kubeProxyReplacement=true \\"
	if strings.Contains(tpl, banned) {
		// The presence of the banned line in combination with absence
		// of `-f` would be the regression. We've already asserted `-f`
		// presence above; if --set lines are still here AND -f is here,
		// that's belt-and-braces and not a regression. Only fail if
		// the cilium-values.yaml file write is ALSO absent.
		if !strings.Contains(tpl, "/var/lib/catalyst/cilium-values.yaml") {
			t.Errorf("bootstrap helm install carries pre-#491 --set list but is missing the cilium-values.yaml file write — regression of issue #491 fix")
		}
	}
}
