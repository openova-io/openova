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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
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

	writeHosts := pivotWriterFns(t, chart)

	const localHost = "registry.t99.omani.works"
	certsD := t.TempDir()

	// Seed a legacy dfdaemon catch-all, first line the mark the #4639 pivot
	// wrote — the function MUST retire it (and only it: SOV-132, a k3s-written
	// _default is kept; tests/registry-pivot-keeps-k3s-files.sh).
	if err := os.MkdirAll(filepath.Join(certsD, "_default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certsD, "_default", "hosts.toml"),
		[]byte("# managed by bp-self-sovereign-cutover registry-pivot (#4639) — DO NOT EDIT\n"), 0o644); err != nil {
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

// pivotWriterFns slices the shell functions write_offline_mirror_hosts needs.
func pivotWriterFns(t *testing.T, chart string) string {
	t.Helper()
	return sliceShellFn(t, chart, "pivot_owns") + sliceShellFn(t, chart, "put_hosts_toml") +
		sliceShellFn(t, chart, "k3s_mirror_block") + sliceShellFn(t, chart, "write_offline_mirror_hosts")
}

// SOV-137 — the nodes share what they hold, also after the cutover.
//
// With --embedded-registry (SOV-136) k3s v1.31.8+k3s1 puts the node's embedded mirror, its
// loopback on the supervisor port with the cluster's client certificate, as the FIRST host of
// the file it writes for every name of registries.yaml (pkg/spegel/registry.go InjectMirror;
// pkg/agent/templates HostsTomlTemplate, rendered through trimEmpty), so containerd asks the
// nodes of the region before any registry. Step 04 replaces those files. Before SOV-137 its
// files named the region's registry alone, so a Pod that a drain moved to a node without its
// image could come only from that registry: on 20 November a 503 of the registry's front held
// 14 moved Pods although a node of the region held every image they needed.

// k3sEmbeddedMirrorBlock is the block k3s v1.31.8+k3s1 writes for the embedded mirror.
const k3sEmbeddedMirrorBlock = `[host."https://127.0.0.1:6443/v2"]
  capabilities = ["pull", "resolve"]
  ca = ["/var/lib/rancher/k3s/agent/server-ca.crt"]
  client = [["/var/lib/rancher/k3s/agent/client-k3s-controller.crt", "/var/lib/rancher/k3s/agent/client-k3s-controller.key"]]
`

// k3sDefaultMirrorBlock is the block as k3s writes it on a node whose loopback is [::1], here on
// another port. k3s writes one block in every file; the test seeds _default with this one so
// that it sees from which file each block of the pivot was taken.
const k3sDefaultMirrorBlock = `[host."https://[::1]:6444/v2"]
  capabilities = ["pull", "resolve"]
  ca = ["/var/lib/rancher/k3s/agent/server-ca.crt"]
  client = [["/var/lib/rancher/k3s/agent/client-k3s-controller.crt", "/var/lib/rancher/k3s/agent/client-k3s-controller.key"]]
`

// k3sHostsToml is the file k3s v1.31.8+k3s1 writes for a name whose one endpoint is the
// region's registry, under --disable-default-registry-endpoint (no server key); project is the
// path of its rewrite, block the mirror block k3s puts first ("" without the embedded mirror).
func k3sHostsToml(local, project, block string) string {
	s := "# File generated by k3s. DO NOT EDIT.\n\n[host]\n"
	if block != "" {
		s += "\n" + block
	}
	s += "\n[host.\"https://" + local + "/v2\"]\n  capabilities = [\"pull\", \"resolve\"]\n"
	if project != "" {
		s += "  [host.\"https://" + local + "/v2\".rewrite]\n    \"(.*)\" = \"" + project + "/$1\"\n"
	}
	return s
}

// seedK3sFiles writes what k3s writes at its start for the names, each with the mirror block.
func seedK3sFiles(t *testing.T, certsD, local, block string, names map[string]string) {
	t.Helper()
	for n, p := range names {
		if err := os.RemoveAll(filepath.Join(certsD, n)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(certsD, n), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(certsD, n, "hosts.toml"), []byte(k3sHostsToml(local, p, block)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// runPivotWriter runs the chart's write_offline_mirror_hosts once against certsD.
func runPivotWriter(t *testing.T, fns, certsD, local, hostProjectMap string) {
	t.Helper()
	runShell(t, "set -u\ncerts_d=\""+certsD+"\"\nLOCAL_REGISTRY_HOST=\""+local+"\"\nHOST_PROJECT_MAP=\""+
		hostProjectMap+"\"\nNODE_NAME=test-node\n"+fns+"write_offline_mirror_hosts\n")
}

var hostTableRe = regexp.MustCompile(`(?m)^\[host\."([^"]*)"\]$`)

func TestRegistryPivotKeepsEmbeddedMirrorFirst(t *testing.T) {
	fns := pivotWriterFns(t, pivotChartScript(t))
	const local = "registry.t99.omani.works"
	const mirror, defaultMirror = "https://127.0.0.1:6443/v2", "https://[::1]:6444/v2"
	// k3s writes a file for the region's registry, a proxy name, the origin's name served at the
	// root and "*" (_default); harbor.t99.omani.works is a name of the map that registries.yaml
	// lacks, so its block can come only from _default. _default carries the [::1] block on
	// another port, so a name whose block is taken from the wrong file shows it.
	k3sNames := map[string]string{local: "", "ghcr.io": "proxy-ghcr", "harbor.openova.io": ""}
	const hostProjectMap = "ghcr.io:proxy-ghcr harbor.openova.io harbor.t99.omani.works"
	type file struct {
		block string
		hosts []string
	}
	want := map[string]file{
		local:                    {k3sEmbeddedMirrorBlock, []string{mirror, "https://" + local}},
		"ghcr.io":                {k3sEmbeddedMirrorBlock, []string{mirror, "https://" + local + "/v2/proxy-ghcr"}},
		"harbor.openova.io":      {k3sEmbeddedMirrorBlock, []string{mirror, "https://" + local}},
		"harbor.t99.omani.works": {k3sDefaultMirrorBlock, []string{defaultMirror, "https://" + local}},
	}
	certsD := t.TempDir()
	seed := func() {
		seedK3sFiles(t, certsD, local, k3sEmbeddedMirrorBlock, k3sNames)
		seedK3sFiles(t, certsD, local, k3sDefaultMirrorBlock, map[string]string{"_default": ""})
	}
	// check asserts which block landed in each file and returns the files.
	check := func(sweep string) map[string]string {
		got := map[string]string{}
		for n, w := range want {
			f := readToml(t, filepath.Join(certsD, n, "hosts.toml"))
			got[n] = f
			if !strings.HasPrefix(f, "# managed by bp-self-sovereign-cutover registry-pivot (") {
				t.Errorf("%s, %s: the pivot does not own its hosts.toml:\n%s", sweep, n, f)
			}
			var tables []string
			for _, m := range hostTableRe.FindAllStringSubmatch(f, -1) {
				tables = append(tables, m[1])
			}
			if strings.Join(tables, " ") != strings.Join(w.hosts, " ") {
				t.Errorf("%s, %s: host tables %q, want the nodes' mirror first and the region's registry second %q:\n%s", sweep, n, tables, w.hosts, f)
			}
			if !strings.Contains(f, "\n"+w.block+"[host.\"https://"+local) {
				t.Errorf("%s, %s: the k3s mirror block of %s is not copied byte for byte right before the region's registry:\n%s", sweep, n, w.hosts[0], f)
			}
			if strings.Contains(f, "openova.io\"") || strings.Contains(f, "https://harbor.") || strings.Contains(f, "https://ghcr.io") {
				t.Errorf("%s, %s: the pivot's file names an origin:\n%s", sweep, n, f)
			}
		}
		return got
	}

	seed()
	runPivotWriter(t, fns, certsD, local, hostProjectMap)
	first := check("first sweep")
	if readToml(t, filepath.Join(certsD, "ghcr.io", "hosts.toml.pre-pivot")) != k3sHostsToml(local, "proxy-ghcr", k3sEmbeddedMirrorBlock) {
		t.Error("k3s's ghcr.io/hosts.toml is not kept aside byte for byte")
	}

	// A later sweep (the files are the pivot's now): the block comes from hosts.toml.pre-pivot,
	// or from _default for the name k3s writes no file for, and nothing changes. A name whose
	// pre-pivot file were skipped would fall through to _default and get the [::1] block.
	runPivotWriter(t, fns, certsD, local, hostProjectMap)
	for n, f := range check("later sweep") {
		if f != first[n] {
			t.Errorf("%s: a second sweep changed the file:\n%s\nwas:\n%s", n, f, first[n])
		}
	}

	// A node reboot: k3s rewrites its names at its start; the next sweep puts the block first again.
	seed()
	runPivotWriter(t, fns, certsD, local, hostProjectMap)
	for n, f := range check("sweep after k3s rewrote the files") {
		if f != first[n] {
			t.Errorf("%s: after k3s rewrote the files the sweep wrote another file:\n%s\nwant:\n%s", n, f, first[n])
		}
	}
}

// TestRegistryPivotWithoutEmbeddedMirrorWritesTodaysFile: a node whose k3s runs without
// --embedded-registry (no mirror block in k3s's files) gets exactly the file of the pivot
// before SOV-137.
func TestRegistryPivotWithoutEmbeddedMirrorWritesTodaysFile(t *testing.T) {
	fns := pivotWriterFns(t, pivotChartScript(t))
	const local = "registry.t99.omani.works"
	certsD := t.TempDir()
	seedK3sFiles(t, certsD, local, "", map[string]string{local: "", "ghcr.io": "proxy-ghcr", "harbor.openova.io": "", "_default": ""})
	runPivotWriter(t, fns, certsD, local, "ghcr.io:proxy-ghcr harbor.openova.io harbor.t99.omani.works")
	const mark = "# managed by bp-self-sovereign-cutover registry-pivot (#4975) — DO NOT EDIT\n"
	root := mark + "[host.\"https://" + local + "\"]\n  capabilities = [\"pull\", \"resolve\"]\n  skip_verify = true\n"
	want := map[string]string{
		local:                    mark + "server = \"https://" + local + "\"\n[host.\"https://" + local + "\"]\n  capabilities = [\"pull\", \"resolve\"]\n  skip_verify = true\n",
		"ghcr.io":                mark + "[host.\"https://" + local + "/v2/proxy-ghcr\"]\n  capabilities = [\"pull\", \"resolve\"]\n  override_path = true\n  skip_verify = true\n",
		"harbor.openova.io":      root,
		"harbor.t99.omani.works": root,
	}
	for n, w := range want {
		if got := readToml(t, filepath.Join(certsD, n, "hosts.toml")); got != w {
			t.Errorf("%s: without the embedded mirror the pivot must write today's file;\ngot:\n%s\nwant:\n%s", n, got, w)
		}
	}
}

// TestRegistryPivotStartsWithOutsideBlocked: the pivot's Pod starts on a rebooted node with
// the outside blocked. Its image carries curl and jq (images.kubectl, alpine/k8s, which steps
// 06, 10 and 11 run), so the script installs nothing: its start, run up to its first API call
// with an apk that refuses (the outside is blocked), never calls apk and goes on; and a tool
// missing from the image still fails loud (#5191).
func TestRegistryPivotStartsWithOutsideBlocked(t *testing.T) {
	chart := pivotChartScript(t)
	_, block, ok := strings.Cut(chart, "\n  pivot.sh: |\n")
	if !ok {
		t.Fatal("no pivot.sh block in the chart template")
	}
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, `    api="https://kubernetes.default.svc"`) {
			break
		}
		if l != "" && !strings.HasPrefix(l, "    ") {
			t.Fatal("pivot.sh has no line api=\"https://kubernetes.default.svc\"; this test reads its start up to it")
		}
		lines = append(lines, strings.TrimPrefix(l, "    "))
	}
	start := strings.Join(lines, "\n")

	// The image's tools are shell functions on an empty PATH: curl and jq answer; apk and wget
	// stand for the outside, which is blocked, and record each call in a file; sleep returns at
	// once. Go reads the record after the run: on the empty PATH the script finds no cat.
	run := func(t *testing.T, start string, withJq bool) (out, outside string) {
		t.Helper()
		dir := t.TempDir()
		record := filepath.Join(dir, "outside")
		tools := "PATH=\"" + dir + "\"\noutside=\"" + record + "\"\n" +
			"apk() { echo \"OUTSIDE apk $*\" >>\"${outside}\"; return 1; }\n" +
			"wget() { echo \"OUTSIDE wget $*\" >>\"${outside}\"; return 1; }\n" +
			"sleep() { :; }\ncurl() { :; }\n"
		if withJq {
			tools += "jq() { :; }\n"
		}
		out = runShellOutput(t, tools+start+"\necho START_DONE\n")
		if _, err := os.Stat(record); err == nil {
			outside = readToml(t, record)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return out, outside
	}

	out, outside := run(t, start, true)
	if !strings.Contains(out, "START_DONE") {
		t.Errorf("with curl and jq in the image the start of pivot.sh must go on to its first API call:\n%s", out)
	}
	if outside != "" {
		t.Errorf("the start of pivot.sh reaches outside the cluster (the outside is blocked on a rebooted node):\n%s", outside)
	}
	out, _ = run(t, start, false)
	if strings.Contains(out, "START_DONE") || !strings.Contains(out, "FATAL jq") {
		t.Errorf("without jq in the image the start of pivot.sh must fail loud (#5191):\n%s", out)
	}

	// The record bites: an install put back into the start, which goes on past a refusal, is seen.
	if !strings.Contains(start, "\nset -u\n") {
		t.Fatal("pivot.sh has no line set -u; the cases below put an install back right after it")
	}
	for _, c := range []struct{ name, put string }{
		{"apk_add_put_back", "apk add jq || true\n"},
		{"wget_put_back", "wget -q -O /usr/local/bin/jq https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-linux-amd64 || true\n"},
		{"apk_loop_inside_a_line", "for i in 1 2 3; do apk add --no-cache curl jq && break; sleep 2; done\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, outside := run(t, strings.Replace(start, "\nset -u\n", "\nset -u\n"+c.put, 1), true)
			if !strings.Contains(out, "START_DONE") || !strings.Contains(outside, "OUTSIDE") {
				t.Errorf("with %q put back the start must still reach START_DONE and its outside call be recorded; out:\n%s\nrecord:\n%s", c.put, out, outside)
			}
		})
	}

	// The DaemonSet runs images.kubectl, the image that carries curl and jq.
	if !strings.Contains(chart, `(dict "repository" .Values.images.kubectl.repository "tag" .Values.images.kubectl.tag "cutoverPhase" "pre" "Values" .Values)`) {
		t.Error("the registry-pivot DaemonSet must run images.kubectl (alpine/k8s carries curl and jq)")
	}
	raw, err := os.ReadFile(filepath.Join(repoRootForPivotTest(t), "platform", "self-sovereign-cutover", "chart", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Images map[string]struct {
			Repository string `json:"repository"`
		} `json:"images"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if r := values.Images["kubectl"].Repository; !strings.HasSuffix(r, "/alpine/k8s") {
		t.Errorf("images.kubectl.repository is %q, not alpine/k8s", r)
	}
	if _, ok := values.Images["hostExec"]; ok {
		t.Error("images.hostExec (alpine without curl and jq) is still in the values; nothing runs it")
	}
}
