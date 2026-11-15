// #7909 — THE BSS ENGINE'S HOST IS SERVED FROM EVERY REGION, SO EVERY REGION
// HAS THE ENGINE BEHIND IT.
//
// bp-chargeback (slot 13f) is PRIMARY-ONLY: ${SECONDARY_HR_SUSPEND}, because
// its dependsOn edge bp-catalyst-platform is suspended on every secondary CP.
// bp-oidc-gate (slot 13c) is not: its chargeback gate owns chargeback.<fqdn>
// on every region's gateway and upstreams to
// chargeback.chargeback.svc.cluster.local:8080. On a fresh two-region
// Sovereign (15 Nov 2026) region-b had no such Service: its gate logged `no
// such host` and answered 6 of 12 anonymous GETs of the public offer with its
// own 502 page, without CORS — a storefront load landing there says the
// prices are unavailable.
//
// The repair is the two-leg ClusterMesh chain the mcp host already uses
// (TestBootstrapKit_McpSecondaryEdgeArmedBothLegs):
//
//	leg A (region-a): products/chargeback/chart publishes its Service
//	    (service.cilium.io/global + shared) and admits the PEER region's gate
//	    on the Service port, gated on multiRegion — armed by slot 13f.
//	leg B (region-b): platform/catalyst-edge-routes service-chargeback.yaml
//	    renders the namespace and a same-name/same-namespace stub, gated on
//	    services.chargeback.enabled — armed by slot 13e.
//
// The test pins both slot arms on their VALUES, renders both chart legs in
// both directions (armed: the resources, value-asserted; un-armed: nothing),
// and holds the name/namespace/port of the stub to the gate's own upstream
// in slot 13c, so a rename on either side cannot pass.
package bootstrapkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func cbLoadHR(t *testing.T, root, file, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "clusters", "_template", "bootstrap-kit", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil || doc["kind"] != "HelmRelease" {
			continue
		}
		if meta, _ := doc["metadata"].(map[string]any); meta != nil && meta["name"] == name {
			return doc
		}
	}
	t.Fatalf("%s: HelmRelease %q not found", file, name)
	return nil
}

func cbDig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func cbRender(t *testing.T, helmBin, dir string, args ...string) []map[string]any {
	t.Helper()
	cmd := exec.Command(helmBin, append([]string{"template"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %s %v: %v\n%s", dir, args, err, out)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func cbFind(docs []map[string]any, kind, namespace, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] != kind {
			continue
		}
		meta, _ := d["metadata"].(map[string]any)
		if meta == nil || meta["name"] != name {
			continue
		}
		if ns, _ := meta["namespace"].(string); namespace == "" || ns == namespace {
			return d
		}
	}
	return nil
}

func TestBootstrapKit_ChargebackServedFromEveryRegion(t *testing.T) {
	root := repoRoot(t)

	// ── The slot arms, on their values (pure YAML, always runs) ──────────
	edge := cbLoadHR(t, root, "13e-bp-catalyst-secondary-edge-routes.yaml", "bp-catalyst-secondary-edge")
	if got := cbDig(edge, "spec", "values", "services", "chargeback", "enabled"); got != true {
		t.Errorf("slot 13e services.chargeback.enabled = %v, want true — without it region-b renders no chargeback Service and its gate 502s half of chargeback.<fqdn> (#7909)", got)
	}
	if got, _ := cbDig(edge, "spec", "suspend").(string); !strings.Contains(got, "SECONDARY_EDGE_SUSPEND") {
		t.Errorf("slot 13e spec.suspend = %q, want the ${SECONDARY_EDGE_SUSPEND} conditional (inert on the primary and on single-region provs)", got)
	}
	cb := cbLoadHR(t, root, "13f-bp-chargeback.yaml", "bp-chargeback")
	if got, _ := cbDig(cb, "spec", "values", "multiRegion", "enabled").(string); !strings.Contains(got, "${SOVEREIGN_ENABLE_HOT_STANDBY") {
		t.Errorf("slot 13f values.multiRegion.enabled = %q, want the ${SOVEREIGN_ENABLE_HOT_STANDBY} substitution — without it region-a's Service never joins ClusterMesh and region-b's stub has nothing to reach", got)
	}
	if got, _ := cbDig(cb, "spec", "values", "multiRegion", "peerClusters").(string); !strings.Contains(got, "${SOVEREIGN_PEER_CLUSTERMESH_NAMES") {
		t.Errorf("slot 13f values.multiRegion.peerClusters = %q, want the ${SOVEREIGN_PEER_CLUSTERMESH_NAMES} substitution — without it region-a's policy drops region-b's gate", got)
	}
	if got, _ := cbDig(cb, "spec", "suspend").(string); !strings.Contains(got, "SECONDARY_HR_SUSPEND") {
		t.Errorf("slot 13f spec.suspend = %q, want ${SECONDARY_HR_SUSPEND}: the engine stays primary-only, region-b reaches it through the 13e stub", got)
	}
	// The gate's upstream is the contract both legs must meet.
	gate := cbLoadHR(t, root, "13c-bp-oidc-gate.yaml", "bp-oidc-gate")
	upstream := ""
	instances, _ := cbDig(gate, "spec", "values", "instances").([]any)
	for _, i := range instances {
		if m, _ := i.(map[string]any); m != nil && m["name"] == "chargeback" {
			upstream, _ = m["upstream"].(string)
		}
	}
	const wantUpstream = "http://chargeback.chargeback.svc.cluster.local:8080"
	if upstream != wantUpstream {
		t.Fatalf("slot 13c chargeback upstream = %q, want %q — the stub below is named for that address", upstream, wantUpstream)
	}

	// ── The chart legs, both directions ──────────────────────────────────
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v) — the slot arms above still ran", err)
	}
	edgeDir := filepath.Join(root, "platform", "catalyst-edge-routes", "chart")
	cbDir := filepath.Join(root, "products", "chargeback", "chart")
	ensureChartDeps(t, helmBin, cbDir) // the docrender sub-chart, file://, charts/ is gitignored
	const peer = "guard-7909-region-b"

	// Leg B armed (region-b, slot 13e values after substitution).
	armed := cbRender(t, helmBin, edgeDir, "catalyst-secondary-edge", ".", "-n", "catalyst-system",
		"--set", "ingress.gateway.enabled=true", "--set", "services.chargeback.enabled=true")
	if cbFind(armed, "Namespace", "", "chargeback") == nil {
		t.Error("edge armed render: no Namespace chargeback — region-b's suspended HR never creates it, so the stub would have nowhere to live")
	}
	stub := cbFind(armed, "Service", "chargeback", "chargeback")
	if stub == nil {
		t.Fatal("edge armed render: no Service chargeback/chargeback — region-b's gate would keep answering 502 (#7909)")
	}
	for _, a := range []string{"service.cilium.io/global", "service.cilium.io/shared"} {
		if got, _ := cbDig(stub, "metadata", "annotations", a).(string); got != "true" {
			t.Errorf("stub annotation %s = %q, want \"true\" (a key without the value does not join ClusterMesh)", a, got)
		}
	}
	if got, _ := cbDig(stub, "spec", "type").(string); got != "ClusterIP" {
		t.Errorf("stub type = %q, want ClusterIP (never NodePort, §854)", got)
	}
	// Leg B un-armed: the default render carries nothing of it.
	unarmed := cbRender(t, helmBin, edgeDir, "catalyst-secondary-edge", ".", "-n", "catalyst-system")
	if cbFind(unarmed, "Service", "chargeback", "chargeback") != nil || cbFind(unarmed, "Namespace", "", "chargeback") != nil {
		t.Error("edge DEFAULT render carries the chargeback stub — smoke-render-mode:default-off broken")
	}

	// Leg A armed (region-a, slot 13f values after substitution).
	regionA := cbRender(t, helmBin, cbDir, "chargeback", ".", "-n", "chargeback", "--api-versions", "cilium.io/v2",
		"--set", "sovereignFqdn=guard-7909.example", "--set", "forwardAuth.header=X-Forwarded-Email", "--set", "httpRoute.enabled=false",
		"--set", "multiRegion.enabled=true", "--set", "multiRegion.peerClusters={"+peer+"}")
	svcA := cbFind(regionA, "Service", "", "chargeback")
	if svcA == nil {
		t.Fatal("chargeback armed render: no Service chargeback — the name contract with the region-b stub broke (ClusterMesh merges by name+namespace)")
	}
	for _, a := range []string{"service.cilium.io/global", "service.cilium.io/shared"} {
		if got, _ := cbDig(svcA, "metadata", "annotations", a).(string); got != "true" {
			t.Errorf("region-a Service annotation %s = %q, want \"true\"", a, got)
		}
	}
	// Name, namespace, port and selector: the stub is the Service the gate dials.
	svcAPorts, _ := cbDig(svcA, "spec", "ports").([]any)
	stubPorts, _ := cbDig(stub, "spec", "ports").([]any)
	if len(svcAPorts) != 1 || len(stubPorts) != 1 {
		t.Fatalf("port contract: want one port each (svcA %v, stub %v)", svcAPorts, stubPorts)
	}
	rp, _ := cbDig(svcAPorts[0], "port").(int)
	sp, _ := cbDig(stubPorts[0], "port").(int)
	if rp != 8080 || sp != rp {
		t.Errorf("port contract: region-a %d, stub %d, the gate dials 8080", rp, sp)
	}
	for _, k := range []string{"app.kubernetes.io/name", "app.kubernetes.io/instance"} {
		if a, b := cbDig(svcA, "spec", "selector", k), cbDig(stub, "spec", "selector", k); a == nil || a != b {
			t.Errorf("selector %s: region-a %v, stub %v — the stub must select what region-a's Service selects", k, a, b)
		}
	}
	// The peer region's gate is admitted, by cluster, namespace and gate label.
	cnp := cbFind(regionA, "CiliumNetworkPolicy", "", "chargeback-ingress")
	if cnp == nil {
		t.Fatal("chargeback armed render: no chargeback-ingress policy")
	}
	peerRule := false
	ingress, _ := cbDig(cnp, "spec", "ingress").([]any)
	for _, r := range ingress {
		from, _ := cbDig(r, "fromEndpoints").([]any)
		for _, f := range from {
			exprs, _ := cbDig(f, "matchExpressions").([]any)
			cluster, ns := false, false
			for _, e := range exprs {
				vals, _ := cbDig(e, "values").([]any)
				switch cbDig(e, "key") {
				case "io.cilium.k8s.policy.cluster":
					cluster = len(vals) == 1 && vals[0] == peer
				case "k8s:io.kubernetes.pod.namespace":
					ns = len(vals) == 1 && vals[0] == "oidc-gate"
				}
			}
			if cluster && ns && cbDig(f, "matchLabels", "app.kubernetes.io/name") == "bp-oidc-gate" {
				peerRule = true
			}
		}
	}
	if !peerRule {
		t.Errorf("chargeback armed render: no ingress rule admits the gate of peer cluster %q — region-b's dial crosses the mesh and is dropped", peer)
	}
	// Leg A un-armed: a single-region install neither publishes nor admits a peer.
	single := cbRender(t, helmBin, cbDir, "chargeback", ".", "-n", "chargeback", "--api-versions", "cilium.io/v2",
		"--set", "sovereignFqdn=guard-7909.example", "--set", "forwardAuth.header=X-Forwarded-Email", "--set", "httpRoute.enabled=false")
	if s := cbFind(single, "Service", "", "chargeback"); s == nil || cbDig(s, "metadata", "annotations", "service.cilium.io/global") != nil {
		t.Error("chargeback default render publishes the Service to ClusterMesh — single-region installs must not")
	}
	if c := cbFind(single, "CiliumNetworkPolicy", "", "chargeback-ingress"); c == nil || strings.Contains(yamlText(t, c), "io.cilium.k8s.policy.cluster") {
		t.Error("chargeback default render admits a peer cluster — single-region installs must not")
	}
}

func yamlText(t *testing.T, v any) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
