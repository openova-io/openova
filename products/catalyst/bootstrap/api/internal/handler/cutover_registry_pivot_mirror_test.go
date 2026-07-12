// #4637 / #4664 — registry-pivot KUBELET-HAIRPIN regression test.
//
// THE BUG (live wedge f7464ffc / omantel.biz, kom4dc). The self-sovereignty
// cutover wedges at step-07 (catalyst-api-env-patch, 7/11, 54%). That step
// restarts catalyst-api with the registry-pivoted image
// `registry.<sov-fqdn>/openova-io/openova/catalyst-api:<tag>`. The new pod
// ErrImagePulls `dial tcp <public-EIP>:443: i/o timeout` because the pivot
// routed the kubelet's pull at `https://registry.<sov-fqdn>` — which the NODE
// resolver pins to the cluster's PUBLIC gateway EIP (cloudinit-worker.tftpl
// /etc/hosts). On a no-hairpin cloud the kubelet cannot dial its own cluster's
// public EIP. The image IS in local Harbor; only the kubelet/node path lacked a
// node-reachable mirror endpoint.
//
// THE FIX — two layers, both NODE-REACHABLE, never the public hairpin EIP:
//
//	#4638 (chart 0.1.91, the ORIGINAL fix): the pivot DaemonSet resolved
//	harbor-core's node-routable ClusterIP and pointed the containerd `[host]`
//	mirror endpoints at `http://<clusterip>:<port>` instead of
//	`https://registry.<sov-fqdn>` — via a `write_hosts_toml` /
//	`upstream_mirrors=` shell mechanism.
//
//	#4641/#4653 (the DRAGONFLY REWRITE, chart 0.1.94 — the contract THIS test
//	now asserts): containerd no longer mirrors per-host at a Harbor ClusterIP.
//	Every node's containerd mirrors EVERY registry namespace through the
//	node-LOCAL dfdaemon proxy on `http://127.0.0.1:<DF_PROXY_PORT>` via a
//	`_default/hosts.toml` catch-all (`write_default_mirror`). That loopback
//	endpoint is THE most node-reachable address possible — it can never hairpin
//	to the public EIP. dfdaemon's OWN upstream fetch is then pointed at the
//	in-cluster Harbor via the node-routable cilium-gateway **ClusterIP**
//	(hostAlias + CoreDNS override in `df_flip_to_local`), and when that
//	ClusterIP cannot be resolved the flip DEFERS / falls back rather than ever
//	pointing dfdaemon at the un-hairpinnable public EIP.
//
// The #4638 `upstream_mirrors=` / `write_hosts_toml()` slice no longer exists in
// the chart (the Dragonfly rewrite replaced it). This test was rewritten in
// #4664 to assert the NEW mechanism, preserving the #4638 anti-hairpin INTENT.
//
// What this test proves — against the REAL chart shell code (functions are
// sliced verbatim out of
// platform/self-sovereign-cutover/chart/templates/04-registry-pivot-daemonset.yaml
// and executed in /bin/sh), NOT a hand-copied duplicate:
//
//  1. The containerd pivot (`write_default_mirror`) points the `_default`
//     catch-all `[host]` block at the NODE-LOCAL dfdaemon loopback proxy
//     `http://127.0.0.1:<DF_PROXY_PORT>` — NEVER at the public registry host.
//     This is the step-07 catalyst-api image-pull path that hairpinned in #4637.
//  2. Fail-open: the cluster-wide dfdaemon upstream flip (`df_flip_to_local`)
//     resolves the node-routable in-cluster cilium-gateway ClusterIP and only
//     proceeds when it is reachable; with NO ClusterIP resolved it DEFERS
//     (returns non-zero, never patches the dfdaemon upstream / hostAlias at the
//     public host) so a cluster where the ClusterIP could not be read never
//     regresses to the un-hairpinnable EIP.
package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// repoRootForPivotTest walks up from this test file to the monorepo root (the
// dir containing platform/self-sovereign-cutover).
func repoRootForPivotTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "platform", "self-sovereign-cutover", "chart")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("monorepo root (platform/self-sovereign-cutover) not found from test cwd — skipping chart-script test")
	return ""
}

// pivotChartScript reads the chart template that carries the registry-pivot
// shell script (the `data.pivot.sh` ConfigMap block).
func pivotChartScript(t *testing.T) string {
	t.Helper()
	root := repoRootForPivotTest(t)
	chartPath := filepath.Join(root, "platform", "self-sovereign-cutover", "chart",
		"templates", "04-registry-pivot-daemonset.yaml")
	raw, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatalf("read chart template: %v", err)
	}
	return string(raw)
}

// sliceShellFn extracts a `    <name>() { ... }` shell function VERBATIM from
// the chart template — from the function header to the matching closing brace at
// column 4 (`    }`), the indent used in the chart's `data: |` block. Slicing the
// real chart code (vs a hand-copied duplicate) is what makes this a regression
// test of the SHIPPED script, not of a stale mirror of it. The Helm `{{ }}`
// directives live in the YAML envelope OUTSIDE these function bodies, so each
// sliced fragment runs in a plain shell with no template rendering.
func sliceShellFn(t *testing.T, chartScript, name string) string {
	t.Helper()
	header := "    " + name + "() {"
	startIdx := strings.Index(chartScript, header)
	if startIdx < 0 {
		t.Fatalf("could not locate %s() definition in chart pivot script", name)
	}
	rest := chartScript[startIdx:]
	endRe := regexp.MustCompile(`(?m)^    \}$`)
	loc := endRe.FindStringIndex(rest)
	if loc == nil {
		t.Fatalf("could not locate end of %s() function", name)
	}
	return rest[:loc[1]] + "\n"
}

// TestRegistryPivotOfflineMirrorPerHostRewrite asserts the CURRENT #4975/
// #4977 offline-mirror contract: write_offline_mirror_hosts RETIRES the
// legacy dfdaemon `_default/hosts.toml` catch-all and writes PER-UPSTREAM-
// HOST certs.d entries — a project host (ghcr.io:proxy-ghcr) resolves to
// the local Harbor project path with override_path, the mothership host
// (empty project) is a pure host swap with the path preserved, and the
// local registry gets a self-trust entry. The old contract this replaces
// (write_default_mirror + node-local dfdaemon `_default` catch-all) died
// in #4977: under the deny-egress hold a dfdaemon back-to-source only
// serves node-cached images, so the catch-all was superseded by the
// complete-local-mirror per-host rewrite — the pre-#4977 test sliced a
// function that no longer exists and left main CI red (same shape as the
// #4684 note on the flip test below).
func TestRegistryPivotOfflineMirrorPerHostRewrite(t *testing.T) {
	chart := pivotChartScript(t)

	writeHosts := sliceShellFn(t, chart, "write_offline_mirror_hosts")

	const localHost = "registry.t99.omani.works"
	certsD := t.TempDir()

	// Seed a legacy dfdaemon catch-all — the function MUST retire it.
	if err := os.MkdirAll(filepath.Join(certsD, "_default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certsD, "_default", "hosts.toml"),
		[]byte("# legacy dfdaemon catch-all\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Harness: env the function reads (certs_d, LOCAL_REGISTRY_HOST,
	// HOST_PROJECT_MAP, NODE_NAME), source the sliced chart function, run it.
	script := `set -u
certs_d="` + certsD + `"
LOCAL_REGISTRY_HOST="` + localHost + `"
HOST_PROJECT_MAP="ghcr.io:proxy-ghcr harbor.openova.io"
NODE_NAME=test-node
` + writeHosts + `
write_offline_mirror_hosts
`
	runShell(t, script)

	// (1) The legacy `_default` catch-all is retired.
	if _, err := os.Stat(filepath.Join(certsD, "_default", "hosts.toml")); !os.IsNotExist(err) {
		t.Errorf("_default/hosts.toml still present — the legacy dfdaemon catch-all must be retired (#4975)")
	}
	// (2) Self-trust entry for direct openova-io host-drop pulls.
	selfToml := readToml(t, filepath.Join(certsD, localHost, "hosts.toml"))
	if !strings.Contains(selfToml, `server = "https://`+localHost+`"`) ||
		!strings.Contains(selfToml, "skip_verify = true") {
		t.Errorf("local self-trust hosts.toml wrong; got:\n%s", selfToml)
	}
	// (3) Project host: resolves to the local Harbor PROJECT path with
	// override_path (a pull of ghcr.io/<repo> lands on /v2/proxy-ghcr/<repo>).
	ghcrToml := readToml(t, filepath.Join(certsD, "ghcr.io", "hosts.toml"))
	if !strings.Contains(ghcrToml, `[host."https://`+localHost+`/v2/proxy-ghcr"]`) {
		t.Errorf("ghcr.io hosts.toml must mirror to the local Harbor project path; got:\n%s", ghcrToml)
	}
	if !strings.Contains(ghcrToml, "override_path = true") {
		t.Errorf("ghcr.io hosts.toml missing override_path = true (containerd would append its own /v2); got:\n%s", ghcrToml)
	}
	// (4) Mothership host: pure host swap, path PRESERVED (no override_path —
	// the pulled path already carries the proxy-<x> project segment).
	moToml := readToml(t, filepath.Join(certsD, "harbor.openova.io", "hosts.toml"))
	if !strings.Contains(moToml, `[host."https://`+localHost+`"]`) {
		t.Errorf("harbor.openova.io hosts.toml must host-swap to the local registry; got:\n%s", moToml)
	}
	if strings.Contains(moToml, "override_path") {
		t.Errorf("harbor.openova.io hosts.toml must NOT set override_path (path carries the proxy-<x> segment); got:\n%s", moToml)
	}
	// (5) §854 guard: no NodePort-range port anywhere in the generated mirrors.
	for _, toml := range []string{selfToml, ghcrToml, moToml} {
		if regexp.MustCompile(`:3[0-2][0-9]{3}`).MatchString(toml) {
			t.Errorf("generated hosts.toml carries a NodePort-range port — forbidden (§854):\n%s", toml)
		}
	}
}

// TestRegistryPivotV2FlipUpstreamIsExternalHost443_NeverNodePort asserts the
// CURRENT #4682/#4684 dfdaemon-upstream contract: the flip target is the
// cluster-local Harbor's EXTERNAL routable host on the standard :443 (the
// Cilium Gateway serves registry.<fqdn> on :443) — with NO port suffix, and
// NEVER a NodePort-range port (:30443 was the retired workaround; nodePorts
// are forbidden, §854). The old contract this replaces (defer on unresolved
// gateway ClusterIP) died in #4684: read_gateway_clusterip no longer exists
// in the chart, so the pre-#4684 test sliced a dependency that was never
// consulted and tripwired on the flip proceeding — leaving main CI red.
func TestRegistryPivotV2FlipUpstreamIsExternalHost443_NeverNodePort(t *testing.T) {
	chart := pivotChartScript(t)

	// The derivation must exist verbatim in the chart: bare host, no port.
	if !strings.Contains(chart, `local_upstream="https://${harbor_host}"`) {
		t.Fatalf("chart must derive local_upstream as https://<harbor_host> with NO port suffix (#4682/#4684 external-host-:443 contract)")
	}
	if strings.Contains(chart, ":30443") {
		t.Fatalf("chart references :30443 — the retired NodePort workaround must not reappear (§854)")
	}

	// Run the REAL derivation lines against a scheme+slash-decorated
	// HARBOR_PUBLIC_URL and assert the normalised, portless upstream.
	script := `set -u
HARBOR_PUBLIC_URL="https://registry.omani.works/"
harbor_host=$(printf '%s' "${HARBOR_PUBLIC_URL}" | sed -E 's,^https?://,,; s,/$,,')
local_upstream="https://${harbor_host}"
echo "RESULT_UPSTREAM=${local_upstream}"
`
	out := runShellOutput(t, script)
	if !strings.Contains(out, "RESULT_UPSTREAM=https://registry.omani.works") {
		t.Errorf("local_upstream derivation broken; harness output:\n%s", out)
	}
	if strings.Contains(out, ":30443") || regexp.MustCompile(`:3[0-2][0-9]{3}`).MatchString(out) {
		t.Errorf("dfdaemon upstream carries a NodePort-range port — forbidden (§854); harness output:\n%s", out)
	}
}

// TestRegistryPivotV2FlipDefersGuardWhenHarborCAUnmaterialised asserts the
// LIVE defer semantics of df_flip_to_local (#4652): when the Harbor trust
// anchor cannot be materialised in the dragonfly namespace this sweep, the
// flip goes ADDR-ONLY (empty cert arg — never a path to a non-existent PEM)
// and the cluster-wide dragonflyUpstream=local guard is NOT set, so the next
// sweep retries the cert leg. Setting the guard on an incomplete flip would
// permanently strand the dfdaemon without the CA (x509 on every
// back-to-source pull).
func TestRegistryPivotV2FlipDefersGuardWhenHarborCAUnmaterialised(t *testing.T) {
	chart := pivotChartScript(t)
	dfFlip := sliceShellFn(t, chart, "df_flip_to_local")

	script := `set -u
NODE_NAME=test-node
local_upstream="https://registry.omani.works"
local_cert_path="/etc/certs/ca.crt"
CT="--max-time 20"
api="https://kubernetes.default.svc"
token=stub
cacert=/dev/null
cm_url="${api}/stub"
DF_CLIENT_CONFIGMAP="dragonfly-client-config"
DF_SEED_CONFIGMAP="dragonfly-seed-config"
DF_CLIENT_DAEMONSET="dragonfly-client"
DF_SEED_STATEFULSET="dragonfly-seed-client"

curl() { printf ''; }   # guard read -> empty -> != local -> flip attempted
harbor_ca_to_dragonfly_ns() { return 1; }   # CA NOT materialisable this sweep
patch_df_configmap_addr() { echo "PATCHED cm=$1 addr=$2 cert=$3"; return 0; }
restart_workload() { echo "RESTARTED $1/$2"; return 0; }
# TRIPWIRE: setting the guard on an incomplete (cert-less) flip strands the
# dfdaemon without the CA forever.
cm_patch_status() { echo "TRIPWIRE cm_patch_status reached ($1=$2)"; exit 95; }

` + dfFlip + `

if df_flip_to_local; then
  echo "RESULT_FLIP=converged"
else
  echo "RESULT_FLIP=deferred"
fi
`
	out := runShellOutput(t, script)
	if !strings.Contains(out, "RESULT_FLIP=deferred") {
		t.Errorf("with the Harbor CA unmaterialisable, df_flip_to_local must return non-zero (guard deferred, retried next sweep); harness output:\n%s", out)
	}
	if strings.Contains(out, "TRIPWIRE cm_patch_status") {
		t.Errorf("df_flip_to_local set the dragonflyUpstream=local guard on an INCOMPLETE (cert-less) flip; harness output:\n%s", out)
	}
	if !strings.Contains(out, "addr=https://registry.omani.works cert=") {
		t.Errorf("flip must be addr-only (empty cert) when the CA is unmaterialisable; harness output:\n%s", out)
	}
	if !strings.Contains(out, "guard NOT set, will retry next sweep") {
		t.Errorf("df_flip_to_local must log the defer; harness output:\n%s", out)
	}
}

// runShell runs the harness and FAILS the test on any non-zero exit (a tripwire
// firing, an unset var, a syntax error in the sliced chart code).
func runShell(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pivot shell harness failed: %v\noutput:\n%s", err, out)
	}
}

// runShellOutput runs the harness and returns combined output. Used when the
// test inspects a RESULT_* line rather than asserting overall exit==0 (here a
// non-zero exit is itself a failure signal via a fired tripwire, surfaced in the
// returned text).
func runShellOutput(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func readToml(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
