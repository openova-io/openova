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
//  1. The cloud-init `cilium-values.yaml` is INSIDE a tftpl `content: |`
//     block with OpenTofu interpolations adjacent in the same file —
//     unmarshalling the surrounding tftpl is non-trivial and adds a
//     Terraform-specific test dep.
//  2. The chart values.yaml carries `cilium:` as the umbrella subchart
//     key while the bootstrap install consumes the upstream cilium/cilium
//     chart directly (values must be at TOP LEVEL). Structural equality
//     across a renaming boundary requires a sub-tree slice, which is
//     more code to maintain than the focused presence checks below.
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

// TestCiliumValuesParity_GatewayHostNetworkLockstep (#4706) locks the
// three-way gateway-api hostNetwork agreement:
//  1. the cloud-init cilium-values.yaml block carries a huawei-conditional
//     `hostNetwork:` + `enabled: true` (bootstrap install binds node:443/:80),
//  2. the bootstrap-kit slot-01 HR wires
//     `${CILIUM_GATEWAY_HOSTNETWORK_ENABLED...}` (the HR upgrade agrees),
//  3. cloud-init's flux-bootstrap substitutes set
//     CILIUM_GATEWAY_HOSTNETWORK_ENABLED: "true" on huawei.
//
// Drift between (1) and (2)+(3) is the #491 class: the bootstrap install and
// the slot-01 upgrade disagree mid-Phase-1 and the gateway flaps between a
// host bind and a Service-only shape. The ELB targets node:443/:80, so a
// regression here = console 000 (the hw217 shape).
func TestCiliumValuesParity_GatewayHostNetworkLockstep(t *testing.T) {
	tpl := readCloudInit(t)
	overlay := readBootstrapKitOverlay(t)

	// (1) bootstrap cilium-values block: hostNetwork enabled inside the
	// huawei conditional.
	startMarker := "- path: /var/lib/catalyst/cilium-values.yaml"
	endMarker := "- path: /var/lib/catalyst/flux-bootstrap.yaml"
	si := strings.Index(tpl, startMarker)
	ei := strings.Index(tpl, endMarker)
	if si < 0 || ei < 0 || ei <= si {
		t.Fatalf("could not locate cilium-values.yaml block in cloud-init template (start=%d, end=%d)", si, ei)
	}
	bootstrapBlock := tpl[si:ei]
	if !strings.Contains(bootstrapBlock, "hostNetwork:") {
		t.Errorf("bootstrap cilium-values.yaml must carry the huawei-conditional gatewayAPI hostNetwork block (#4706) — without it the bootstrap install serves no host bind and the gateway ELB (node:443/:80) has nothing to forward to")
	}

	// (1b) the envoy NET_BIND_SERVICE cap pair — without BOTH knobs envoy
	// cannot bind the privileged host ports ("cannot bind '0.0.0.0:80':
	// Permission denied", live-proven hw217): the cap in the container list
	// AND keepCapNetBindService (cilium-envoy-starter drops ambient caps at
	// exec otherwise). DELIBERATELY asserted on the CHART values ONLY, not
	// the bootstrap heredoc: gateway listeners exist only after Flux applies
	// the Gateway CR (sovereign-tls, post-bootstrap), and slot-01's bp-cilium
	// HR upgrade delivers the caps before any bind attempt — so carrying
	// them in cloud-init buys nothing and costs bytes against Hetzner's
	// 32 KiB user_data cap (#966: the 3-region CP render sits within ~30 B
	// of it). This is NOT the #491 drift class (nothing crashes pre-Gateway).
	chart := readChartValues(t)
	for _, tok := range []string{"NET_BIND_SERVICE", "keepCapNetBindService: true"} {
		if !strings.Contains(chart, tok) {
			t.Errorf("platform/cilium/chart/values.yaml must carry %q (#4706) — the slot-01 HR upgrade would strip the envoy bind capability mid-bootstrap", tok)
		}
	}

	// (2) slot-01 HR wiring.
	if !strings.Contains(overlay, "CILIUM_GATEWAY_HOSTNETWORK_ENABLED") {
		t.Errorf("clusters/_template/bootstrap-kit/01-cilium.yaml must wire gatewayAPI.hostNetwork.enabled from CILIUM_GATEWAY_HOSTNETWORK_ENABLED (#4706)")
	}

	// (3) huawei substitute set to "true".
	if !strings.Contains(tpl, `CILIUM_GATEWAY_HOSTNETWORK_ENABLED: "true"`) {
		t.Errorf(`cloud-init flux-bootstrap substitutes must set CILIUM_GATEWAY_HOSTNETWORK_ENABLED: "true" on huawei (#4706)`)
	}
}

// TestCiliumValuesParity_BootstrapHelmVersionMatchesChartPin (#4706) locks the
// bootstrap `helm upgrade --install cilium --version X` against the bp-cilium
// Chart.yaml dependency pin. If they drift, every fresh prov does an
// unsupported multi-minor in-place CNI upgrade mid-Phase-1 (cloud-init
// installs one cilium, the slot-01 HR immediately upgrades to another) — the
// exact trap the 1.16.5→1.19.3 bump would have created without this guard.

// TestCiliumValuesParity_ConsolePortNoCollision (#4706, hw218 regression) —
// under the huawei hostNetwork gateway, cilium-envoy binds the gateway host
// ports on the node directly, so the PRIMARY gateway and the CONSOLE gateway
// CANNOT share a port or they collide on node:443 (hw218 2026-07-03: console
// ELB → node:443 hit the PRIMARY listener's vhost table → 404 on every console
// host). This locks the console host ports (8443/8080) DISTINCT from the
// primary (443/80) in the huawei cloud-init substitutes so the collision can
// never re-ship silently. Hetzner is exempt (hostNetwork off — the two
// Gateways get separate hcloud-ccm LB Services, no host-bind contention).
func TestCiliumValuesParity_ConsolePortNoCollision(t *testing.T) {
	tpl := readCloudInit(t)
	for _, want := range []string{
		`CONSOLE_GATEWAY_HTTPS_PORT: "8443"`,
		`CONSOLE_GATEWAY_HTTP_PORT: "8080"`,
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("huawei cloud-init must set %q (#4706 — console gateway needs its own host ports under hostNetwork)", want)
		}
	}
	// The collision guard: console HTTPS port must differ from the primary
	// gateway HTTPS port (both are set in the same huawei substitute block).
	if strings.Contains(tpl, `CONSOLE_GATEWAY_HTTPS_PORT: "443"`) {
		t.Errorf("CONSOLE_GATEWAY_HTTPS_PORT must NOT be 443 — it collides with the PRIMARY gateway's node:443 host bind under hostNetwork (hw218 404 regression, #4706)")
	}
	if strings.Contains(tpl, `CONSOLE_GATEWAY_HTTP_PORT: "80"`) {
		t.Errorf("CONSOLE_GATEWAY_HTTP_PORT must NOT be 80 — collides with the PRIMARY gateway's node:80 host bind (#4706)")
	}
}

// TestConsoleGatewaySlot13ConsumesPortSubstitute (#4706, #4715 regression) —
// the cloud-init emits CONSOLE_GATEWAY_HTTPS_PORT, but that is INERT unless
// slot-13's bp-catalyst-platform HR values consume it into consoleGateway.
// httpsPort. #4715 shipped the substitute WITHOUT this slot-13 wiring — the
// console Gateway defaulted to 443 and collided even on fresh provs. This
// locks the wiring so it cannot be dropped again silently.
//
// #5187 (2026-07-18): the sovereign-tls-vars ConfigMap template that
// actually READS .Values.consoleGateway moved out of bp-catalyst-platform
// into its own always-on chart (platform/sovereign-tls-vars/chart/,
// bootstrap-kit slot 12a — installs on every region, unlike the
// primary-only bp-catalyst-platform HR). Slot 12a wires the SAME
// CONSOLE_GATEWAY_HTTPS_PORT/HTTP_PORT substitutes into its OWN HR values.
// This test's slot-13 assertion is kept as-is (harmless — the values are
// simply unread by bp-catalyst-platform's own templates now) rather than
// migrated, to avoid touching a pinned #4715 regression guard in the same
// change that fixes an unrelated region-b gap.
func TestConsoleGatewaySlot13ConsumesPortSubstitute(t *testing.T) {
	cwd, _ := os.Getwd()
	root := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "clusters", "_template", "bootstrap-kit", "13-bp-catalyst-platform.yaml"))
	if err != nil {
		t.Fatalf("read slot-13: %v", err)
	}
	slot := string(raw)
	for _, want := range []string{
		"consoleGateway:",
		"httpsPort: ${CONSOLE_GATEWAY_HTTPS_PORT",
		"httpPort: ${CONSOLE_GATEWAY_HTTP_PORT",
	} {
		if !strings.Contains(slot, want) {
			t.Errorf("slot-13 must wire the console port substitute into HR values — missing %q (#4715 regression: substitute emitted but never consumed → console 443 collision)", want)
		}
	}
}

func TestCiliumValuesParity_BootstrapHelmVersionMatchesChartPin(t *testing.T) {
	tpl := readCloudInit(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(repoRoot, "platform", "cilium", "chart", "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	chartYAML := string(raw)

	// Extract the dependency pin: the `version: "X.Y.Z"` line inside the
	// dependencies block (the only double-quoted version in the file).
	var pin string
	for _, line := range strings.Split(chartYAML, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `version: "`) {
			pin = strings.Trim(strings.TrimPrefix(trimmed, "version:"), ` "`)
			break
		}
	}
	if pin == "" {
		t.Fatalf("could not extract the cilium dependency version pin from platform/cilium/chart/Chart.yaml")
	}

	want := "helm upgrade --install cilium cilium/cilium --version " + pin
	if !strings.Contains(tpl, want) {
		t.Errorf("cloud-init bootstrap install must pin the SAME cilium version as the bp-cilium chart dependency (%q). Drift = an unsupported in-place multi-minor CNI upgrade mid-Phase-1 on every fresh prov (#4706).", pin)
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
