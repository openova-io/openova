// #4637 — registry-pivot KUBELET-HAIRPIN regression test.
//
// THE BUG (live wedge f7464ffc / omantel.biz, kom4dc). The self-sovereignty
// cutover wedges at step-07 (catalyst-api-env-patch, 7/11, 54%). That step
// restarts catalyst-api with the registry-pivoted image
// `registry.<sov-fqdn>/openova-io/openova/catalyst-api:<tag>`. The new pod
// ErrImagePulls `dial tcp <public-EIP>:443: i/o timeout` because the v2
// containerd mirror (registries.yaml v2 + the certs.d/<host>/hosts.toml the
// step-04 pivot DaemonSet regenerates) routed the kubelet's pull at
// `https://registry.<sov-fqdn>` — which the NODE resolver pins to the cluster's
// PUBLIC gateway EIP (cloudinit-worker.tftpl /etc/hosts). On a no-hairpin cloud
// the kubelet cannot dial its own cluster's public EIP. The image IS in local
// Harbor; only the kubelet/node path lacked a node-reachable mirror endpoint.
//
// THE FIX (chart 0.1.91). The pivot DaemonSet now resolves harbor-core's
// node-routable ClusterIP and points the containerd `[host]` mirror endpoints
// (the 7 upstream-mirror redirects AND the direct registry.<sov-fqdn> host) at
// `http://<clusterip>:<port>` instead of `https://registry.<sov-fqdn>`.
//
// What this test proves — against the REAL chart shell code (the
// `write_hosts_toml` function is sliced out of
// platform/self-sovereign-cutover/chart/templates/04-registry-pivot-daemonset.yaml
// and executed in /bin/sh), NOT a hand-copied duplicate:
//
//  1. v2 path with a node-reachable ClusterIP endpoint → the direct
//     registry.<sov-fqdn> hosts.toml AND every upstream-mirror hosts.toml
//     point their containerd `[host]` block at the http://<clusterip>:<port>
//     ClusterIP — NEVER at the public registry host.
//  2. The certs.d dir name + `server` line keep the PUBLIC host (containerd
//     keys the host-config dir off the image-ref host), so a pull of
//     `registry.<sov-fqdn>/...` still MATCHES — only the resolved endpoint is
//     the in-cluster ClusterIP.
//  3. Fail-open: with NO node endpoint resolved (3rd arg empty — the
//     pre-#4637 / resolve-miss behaviour) the endpoints fall back to the
//     public host, so the change never regresses a cluster where the
//     ClusterIP could not be read.
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

// extractPivotShellFns slices the `upstream_mirrors=` assignment and the
// `write_hosts_toml()` function body verbatim out of the chart template so the
// test exercises the REAL chart code, not a copy. The Helm `{{ }}` directives
// in the rest of the script (v1/rollback path) are NOT in this slice, so the
// extracted fragment runs in a plain shell with no template rendering.
func extractPivotShellFns(t *testing.T, chartScript string) string {
	t.Helper()

	// upstream_mirrors="..." (multi-line, ends at the closing quote line).
	umRe := regexp.MustCompile(`(?s)upstream_mirrors="[^"]*"`)
	um := umRe.FindString(chartScript)
	if um == "" {
		t.Fatal("could not locate upstream_mirrors= assignment in chart pivot script")
	}

	// write_hosts_toml() { ... } — from the function header to the matching
	// closing brace at column 4 (`    }`), the indent used in the chart's
	// data: | block.
	startIdx := strings.Index(chartScript, "    write_hosts_toml() {")
	if startIdx < 0 {
		t.Fatal("could not locate write_hosts_toml() definition in chart pivot script")
	}
	rest := chartScript[startIdx:]
	endRe := regexp.MustCompile(`(?m)^    \}$`)
	loc := endRe.FindStringIndex(rest)
	if loc == nil {
		t.Fatal("could not locate end of write_hosts_toml() function")
	}
	fn := rest[:loc[1]]

	return um + "\n" + fn + "\n"
}

func TestRegistryPivotV2MirrorUsesNodeReachableClusterIP(t *testing.T) {
	root := repoRootForPivotTest(t)
	chartPath := filepath.Join(root, "platform", "self-sovereign-cutover", "chart",
		"templates", "04-registry-pivot-daemonset.yaml")
	raw, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatalf("read chart template: %v", err)
	}
	chart := string(raw)

	fns := extractPivotShellFns(t, chart)

	const (
		publicHost = "registry.omantel.biz" // resolves to the un-hairpinnable PUBLIC gateway EIP on the node
		clusterIP  = "10.96.9.166"
		port       = "80"
	)
	nodeEndpoint := "http://" + clusterIP + ":" + port

	certsD := t.TempDir()

	// Harness: set NODE_NAME + certs_d, source the extracted chart functions,
	// then drive the v2 path exactly as apply_state does — with the
	// node-reachable ClusterIP endpoint as the 3rd arg.
	script := `set -eu
NODE_NAME=test-node
certs_d="` + certsD + `"
` + fns + `
write_hosts_toml "` + publicHost + `" "true" "` + nodeEndpoint + `"
`
	runShell(t, script)

	// (1) direct registry.<fqdn> hosts.toml — the step-07 catalyst-api image
	//     pin pull path — must dial the ClusterIP, never the public host.
	directToml := readToml(t, filepath.Join(certsD, publicHost, "hosts.toml"))
	if !strings.Contains(directToml, "[host.\""+nodeEndpoint+"/v2\"]") {
		t.Errorf("direct hosts.toml [host] must point at node-reachable ClusterIP %s; got:\n%s", nodeEndpoint, directToml)
	}
	if strings.Contains(directToml, "[host.\"https://"+publicHost) {
		t.Errorf("direct hosts.toml [host] still points at the PUBLIC host https://%s (the un-hairpinnable EIP) — #4637 regression:\n%s", publicHost, directToml)
	}
	// containerd keys the host-config dir off the image-ref host, so `server`
	// MUST stay the public host even though the endpoint is the ClusterIP.
	if !strings.Contains(directToml, "server = \"https://"+publicHost+"/v2\"") {
		t.Errorf("direct hosts.toml `server` must remain the public host (containerd host-dir key); got:\n%s", directToml)
	}

	// (2) every upstream mirror redirect must also go via the ClusterIP.
	upstreamToml := readToml(t, filepath.Join(certsD, "ghcr.io", "hosts.toml"))
	if !strings.Contains(upstreamToml, "[host.\""+nodeEndpoint+"/v2/proxy-ghcr\"]") {
		t.Errorf("ghcr.io upstream mirror must redirect via node-reachable ClusterIP %s; got:\n%s", nodeEndpoint, upstreamToml)
	}
	if strings.Contains(upstreamToml, "https://"+publicHost) {
		t.Errorf("ghcr.io upstream mirror still redirects through the PUBLIC host https://%s — #4637 regression:\n%s", publicHost, upstreamToml)
	}
}

func TestRegistryPivotV2MirrorFallsBackToPublicHostWhenClusterIPUnresolved(t *testing.T) {
	root := repoRootForPivotTest(t)
	chartPath := filepath.Join(root, "platform", "self-sovereign-cutover", "chart",
		"templates", "04-registry-pivot-daemonset.yaml")
	raw, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatalf("read chart template: %v", err)
	}
	fns := extractPivotShellFns(t, string(raw))

	const publicHost = "registry.omantel.biz"
	certsD := t.TempDir()

	// 3rd arg EMPTY — the resolve-miss / pre-#4637 behaviour. Endpoints must
	// fall back to the public host so the change is never worse than before.
	script := `set -eu
NODE_NAME=test-node
certs_d="` + certsD + `"
` + fns + `
write_hosts_toml "` + publicHost + `" "true" ""
`
	runShell(t, script)

	directToml := readToml(t, filepath.Join(certsD, publicHost, "hosts.toml"))
	if !strings.Contains(directToml, "[host.\"https://"+publicHost+"/v2\"]") {
		t.Errorf("with no node endpoint the direct hosts.toml must fall back to the public host https://%s; got:\n%s", publicHost, directToml)
	}
}

func runShell(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pivot shell harness failed: %v\noutput:\n%s", err, out)
	}
}

func readToml(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
