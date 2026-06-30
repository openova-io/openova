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
//   #4638 (chart 0.1.91, the ORIGINAL fix): the pivot DaemonSet resolved
//   harbor-core's node-routable ClusterIP and pointed the containerd `[host]`
//   mirror endpoints at `http://<clusterip>:<port>` instead of
//   `https://registry.<sov-fqdn>` — via a `write_hosts_toml` /
//   `upstream_mirrors=` shell mechanism.
//
//   #4641/#4653 (the DRAGONFLY REWRITE, chart 0.1.94 — the contract THIS test
//   now asserts): containerd no longer mirrors per-host at a Harbor ClusterIP.
//   Every node's containerd mirrors EVERY registry namespace through the
//   node-LOCAL dfdaemon proxy on `http://127.0.0.1:<DF_PROXY_PORT>` via a
//   `_default/hosts.toml` catch-all (`write_default_mirror`). That loopback
//   endpoint is THE most node-reachable address possible — it can never hairpin
//   to the public EIP. dfdaemon's OWN upstream fetch is then pointed at the
//   in-cluster Harbor via the node-routable cilium-gateway **ClusterIP**
//   (hostAlias + CoreDNS override in `df_flip_to_local`), and when that
//   ClusterIP cannot be resolved the flip DEFERS / falls back rather than ever
//   pointing dfdaemon at the un-hairpinnable public EIP.
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

func TestRegistryPivotV2MirrorUsesNodeReachableClusterIP(t *testing.T) {
	chart := pivotChartScript(t)

	// The REAL containerd-pivot function from the chart. It writes the
	// `_default/hosts.toml` catch-all that routes the node's containerd pulls
	// (the step-07 catalyst-api image pin pull included) through the mirror.
	writeDefaultMirror := sliceShellFn(t, chart, "write_default_mirror")

	const proxyPort = "4001" // matches registryPivot.dragonfly.proxyPort in values.yaml
	// The public host the node resolver pins to the un-hairpinnable PUBLIC
	// gateway EIP — the endpoint the #4637 wedge dialed. It must NEVER appear in
	// the generated containerd mirror.
	const publicHost = "registry.omantel.biz"
	nodeLocalProxy := "http://127.0.0.1:" + proxyPort

	certsD := t.TempDir()

	// Harness: set the env the function reads (certs_d, df_proxy, NODE_NAME),
	// source the extracted chart function, then drive it exactly as apply_state
	// does on the v2 path.
	script := `set -u
certs_d="` + certsD + `"
DF_PROXY_PORT="` + proxyPort + `"
NODE_NAME=test-node
df_proxy="http://127.0.0.1:${DF_PROXY_PORT}"
` + writeDefaultMirror + `
write_default_mirror
`
	runShell(t, script)

	// The `_default` catch-all hosts.toml is what containerd consults for ANY
	// registry namespace with no explicit certs.d/<host> dir — so it governs the
	// step-07 `registry.<fqdn>/...openova/catalyst-api` pull. Its `[host]` block
	// MUST point at the node-LOCAL dfdaemon loopback proxy, never the public EIP.
	defaultToml := readToml(t, filepath.Join(certsD, "_default", "hosts.toml"))
	if !strings.Contains(defaultToml, "[host.\""+nodeLocalProxy+"\"]") {
		t.Errorf("containerd _default/hosts.toml [host] must point at the node-local dfdaemon proxy %s; got:\n%s", nodeLocalProxy, defaultToml)
	}
	// #4637 regression guard: the public hairpin host must appear NOWHERE in the
	// generated mirror (no https://registry.<fqdn> endpoint the kubelet would
	// then dial via the un-hairpinnable public EIP).
	if strings.Contains(defaultToml, publicHost) {
		t.Errorf("containerd _default/hosts.toml references the PUBLIC host %s (the un-hairpinnable EIP) — #4637 regression:\n%s", publicHost, defaultToml)
	}
	if strings.Contains(defaultToml, "https://") {
		t.Errorf("containerd _default/hosts.toml routes via an https:// endpoint (must be the plain-HTTP loopback dfdaemon proxy):\n%s", defaultToml)
	}
}

func TestRegistryPivotV2MirrorFallsBackToPublicHostWhenClusterIPUnresolved(t *testing.T) {
	chart := pivotChartScript(t)

	// The REAL cluster-wide upstream-flip function from the chart. We slice it
	// plus its `read_gateway_clusterip` dependency, then in the harness OVERRIDE
	// read_gateway_clusterip to simulate a resolve MISS (empty ClusterIP) and
	// stub the mutating helpers as TRIPWIRES that fail the test if reached. The
	// real df_flip_to_local gate logic must DEFER (return non-zero) without ever
	// patching the dfdaemon upstream/hostAlias at the public host.
	dfFlip := sliceShellFn(t, chart, "df_flip_to_local")

	const publicHost = "registry.omantel.biz"

	script := `set -u
NODE_NAME=test-node
harbor_host="` + publicHost + `"
local_upstream="https://${harbor_host}:30443"
CT="--max-time 20"
api="https://kubernetes.default.svc"
token=stub
cacert=/dev/null
cm_url="${api}/stub"
DF_CLIENT_CONFIGMAP="dragonfly-client-config"
DF_SEED_CONFIGMAP="dragonfly-seed-config"
DF_CLIENT_DAEMONSET="dragonfly-client"
DF_SEED_STATEFULSET="dragonfly-seed-client"
DF_RESTART_ON_FLIP="false"

# The cluster-state reads df_flip_to_local makes, stubbed for an UNCONVERGED,
# UNRESOLVABLE-ClusterIP cluster:
#  - guard not yet set (dragonflyUpstream != local) so the flip is attempted;
#  - read_gateway_clusterip returns EMPTY (the resolve-miss / pre-#4637 case).
curl() { printf ''; }   # the guard read inside df_flip_to_local -> empty -> != local
read_gateway_clusterip() { printf ''; }

# TRIPWIRES: if the flip proceeds past the unresolved-ClusterIP gate it would
# call one of these against the public host — which is exactly the #4637
# regression. Any call fails the harness (non-zero exit propagated by the
# RESULT check below is not enough; these abort loudly).
coredns_override() { echo "TRIPWIRE coredns_override reached"; exit 90; }
harbor_ca_to_dragonfly_ns() { echo "TRIPWIRE harbor_ca_to_dragonfly_ns reached"; exit 91; }
patch_df_configmap_addr() { echo "TRIPWIRE patch_df_configmap_addr reached with addr=$2"; exit 92; }
patch_workload_hostalias() { echo "TRIPWIRE patch_workload_hostalias reached"; exit 93; }
restart_workload() { echo "TRIPWIRE restart_workload reached"; exit 94; }
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
		t.Errorf("with the in-cluster gateway ClusterIP unresolved, df_flip_to_local must DEFER (never point dfdaemon at the public host); harness output:\n%s", out)
	}
	if !strings.Contains(out, "cilium-gateway ClusterIP unresolved") {
		t.Errorf("df_flip_to_local must log the ClusterIP-unresolved defer; harness output:\n%s", out)
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
