package gitops

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestRender_AllPathsAndStructuralYAML(t *testing.T) {
	t.Parallel()
	// PlanSlug "m" → paid tier → dedicated vCluster boundary (#4292 tier-gate).
	out, err := Render(Inputs{
		Slug:                 "acme",
		DisplayName:          "ACME Corp",
		Tier:                 "org",
		PlanSlug:             "m",
		SovereignFQDN:        "omantel.omani.works",
		HostCluster:          "hz-fsn-rtz-prod",
		VClusterChartVersion: "0.33.*",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// #4292: the paid-tier set is namespace + vcluster + resourcequota +
	// limitrange + kustomization + the apps-tree networkpolicy baseline +
	// (#4293 MAJOR-2) the apps-tree kustomization index + (#4475 §1) the
	// host-apps-tree CNP + its kustomization index.
	wantPaths := []string{
		"vcluster/namespace.yaml",
		"vcluster/vcluster.yaml",
		"vcluster/resourcequota.yaml",
		"vcluster/limitrange.yaml",
		"vcluster/kustomization.yaml",
		"vcluster/apps/networkpolicy.yaml",
		"vcluster/apps/kustomization.yaml",
		"vcluster/host-apps/ciliumnetworkpolicy.yaml",
		"vcluster/host-apps/kustomization.yaml",
	}
	for _, p := range wantPaths {
		if _, ok := out[p]; !ok {
			t.Errorf("missing path %q", p)
		}
	}
	// Each rendered file must be valid YAML.
	for path, data := range out {
		var v any
		if err := yaml.Unmarshal(data, &v); err != nil {
			t.Errorf("rendered %s is not valid YAML: %v\n%s", path, err, string(data))
		}
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	for _, want := range []string{
		"namespace: acme",
		"server: https://vcluster.acme:443",
		"openova.io/organization: acme",
		"openova.io/host-cluster: hz-fsn-rtz-prod",
		"openova.io/sovereign: omantel.omani.works",
		"version: \"0.33.*\"",
		// MIRROR-EVERYTHING (#3760).
		"registry: harbor.openova.io",
		"repository: proxy-ghcr/loft-sh/kubernetes",
		"repository: proxy-ghcr/loft-sh/vcluster-oss",
		// #4292 MANDATORY: networkPolicy sync ON, else in-vcluster NPs are inert.
		"networkPolicies:",
	} {
		if !strings.Contains(vcl, want) {
			t.Errorf("vcluster.yaml missing %q\nfull contents:\n%s", want, vcl)
		}
	}
	// Guard: no image FIELD may resolve to un-proxied ghcr.io.
	for _, line := range strings.Split(vcl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "registry:") && strings.Contains(trimmed, "ghcr.io") {
			t.Errorf("vcluster.yaml has an un-proxied image registry %q — harbor-proxy-pull will DENY the StatefulSet\nfull contents:\n%s", trimmed, vcl)
		}
	}
	// namespace.yaml must carry the canonical labels.
	ns := string(out["vcluster/namespace.yaml"])
	for _, want := range []string{
		"name: acme",
		"openova.io/organization: acme",
		"openova.io/tier: org",
		"openova.io/sovereign: omantel.omani.works",
		"openova.io/host-cluster: hz-fsn-rtz-prod",
	} {
		if !strings.Contains(ns, want) {
			t.Errorf("namespace.yaml missing %q\nfull contents:\n%s", want, ns)
		}
	}
}

func TestRender_HelmRepoDefaults(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug:                 "acme",
		DisplayName:          "Acme",
		Tier:                 "org",
		PlanSlug:             "m", // paid → vcluster renders → helmrepo refs present
		SovereignFQDN:        "x.example",
		HostCluster:          "hz-fsn-rtz-prod",
		VClusterChartVersion: "0.33.*",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if !strings.Contains(vcl, "name: loft") {
		t.Errorf("expected default helmrepo name 'loft' in vcluster.yaml")
	}
	if !strings.Contains(vcl, "namespace: vcluster-system") {
		t.Errorf("expected default helmrepo namespace 'vcluster-system' in vcluster.yaml")
	}
}

// TestRender_VClusterImageRegistryOverride proves the proxy registry is
// operator-overridable (Principle #4) — cutover Step-04 flips it to
// harbor.<sovereign-fqdn> post-handover (#3760).
func TestRender_VClusterImageRegistryOverride(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug:                  "acme",
		DisplayName:           "Acme",
		Tier:                  "org",
		PlanSlug:              "m",
		SovereignFQDN:         "omantel.omani.works",
		HostCluster:           "hz-fsn-rtz-prod",
		VClusterChartVersion:  "0.33.*",
		VClusterImageRegistry: "harbor.omantel.omani.works",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if !strings.Contains(vcl, "registry: harbor.omantel.omani.works") {
		t.Errorf("expected overridden registry 'harbor.omantel.omani.works' in vcluster.yaml\nfull contents:\n%s", vcl)
	}
	if !strings.Contains(vcl, "repository: proxy-ghcr/loft-sh/kubernetes") {
		t.Errorf("expected proxy-relative k8s-distro repository under overridden registry\nfull contents:\n%s", vcl)
	}
	if strings.Contains(vcl, "harbor.openova.io") {
		t.Errorf("override leaked the default 'harbor.openova.io' into vcluster.yaml\nfull contents:\n%s", vcl)
	}
}

// ---- #4292 Workstream B: plan-templated quota / LimitRange / np-sync / QoS / tier-gate ----

// TestPlanQuota_CatalogSlugMapping asserts the plan-slug → host-ns cap table
// (the seed.go target: S=2/4Gi, M=4/8, L=8/16, XL=16/32, Flexi=on-demand).
func TestPlanQuota_CatalogSlugMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		slug, cpu, mem string
		burstable      bool
	}{
		{"s", "2", "4Gi", false},
		{"m", "4", "8Gi", false},
		{"l", "8", "16Gi", false},
		{"xl", "16", "32Gi", false},
		{"flexi", "", "", true},
		{"", "2", "4Gi", false},      // empty → smallest paid cap, never uncapped
		{"bogus", "2", "4Gi", false}, // unknown → smallest paid cap
		{"M", "4", "8Gi", false},     // case-insensitive
	}
	for _, c := range cases {
		q := planQuota(c.slug)
		if q.CPU != c.cpu || q.Mem != c.mem || q.Burstable != c.burstable {
			t.Errorf("planQuota(%q) = {%s,%s,burstable=%v}, want {%s,%s,burstable=%v}",
				c.slug, q.CPU, q.Mem, q.Burstable, c.cpu, c.mem, c.burstable)
		}
	}
}

// TestRender_ResourceQuotaPerPlan proves the ResourceQuota renders the
// purchased plan's cap with requests==limits (the Guaranteed precondition).
func TestRender_ResourceQuotaPerPlan(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ cpu, mem string }{
		"s":  {"2", "4Gi"},
		"m":  {"4", "8Gi"},
		"l":  {"8", "16Gi"},
		"xl": {"16", "32Gi"},
	}
	for slug, want := range cases {
		out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
			PlanSlug: slug, SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
		if err != nil {
			t.Fatalf("Render(%s): %v", slug, err)
		}
		rq, ok := out["vcluster/resourcequota.yaml"]
		if !ok {
			t.Fatalf("plan %s: missing resourcequota.yaml", slug)
		}
		s := string(rq)
		for _, line := range []string{
			"requests.cpu: \"" + want.cpu + "\"",
			"limits.cpu: \"" + want.cpu + "\"",
			"requests.memory: \"" + want.mem + "\"",
			"limits.memory: \"" + want.mem + "\"",
			"namespace: acme",
			"openova.io/plan: " + slug,
		} {
			if !strings.Contains(s, line) {
				t.Errorf("plan %s resourcequota.yaml missing %q\n%s", slug, line, s)
			}
		}
		var v any
		if err := yaml.Unmarshal(rq, &v); err != nil {
			t.Errorf("plan %s resourcequota.yaml invalid YAML: %v", slug, err)
		}
	}
}

// TestRender_FlexiNoResourceQuota proves Flexi is soft-capped: a LimitRange
// renders (so default-less pods still admit) but NO hard ResourceQuota.
func TestRender_FlexiNoResourceQuota(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
		PlanSlug: "flexi", SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, ok := out["vcluster/resourcequota.yaml"]; ok {
		t.Errorf("Flexi must NOT render a hard ResourceQuota (on-demand/soft cap)")
	}
	lr, ok := out["vcluster/limitrange.yaml"]
	if !ok {
		t.Fatalf("Flexi must still render a LimitRange so default-less pods admit")
	}
	if strings.Contains(string(lr), "maxLimitRequestRatio") {
		t.Errorf("Flexi LimitRange must omit maxLimitRequestRatio (Burstable QoS allowed)\n%s", string(lr))
	}
}

// TestRender_LimitRangeGuaranteedRatio proves fixed tiers pin the
// maxLimitRequestRatio {cpu:1,memory:1} + defaultRequest==default → Guaranteed.
func TestRender_LimitRangeGuaranteedRatio(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
		PlanSlug: "m", SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	lr := string(out["vcluster/limitrange.yaml"])
	for _, want := range []string{
		"kind: LimitRange",
		"maxLimitRequestRatio",
		"cpu: \"1\"",
		"memory: \"1\"",
		"defaultRequest:",
		"default:",
		"namespace: acme",
	} {
		if !strings.Contains(lr, want) {
			t.Errorf("M-tier limitrange.yaml missing %q\n%s", want, lr)
		}
	}
	var v any
	if err := yaml.Unmarshal([]byte(lr), &v); err != nil {
		t.Errorf("limitrange.yaml invalid YAML: %v", err)
	}
}

// TestRender_NetworkPolicyBaselineInAppsTree proves the default-deny +
// same-Org-allow baseline renders in the apps tree (so the syncer reflects it
// to the host). The np-sync flag itself is asserted in TestRender_AllPaths.
func TestRender_NetworkPolicyBaselineInAppsTree(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
		PlanSlug: "m", SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	np, ok := out["vcluster/apps/networkpolicy.yaml"]
	if !ok {
		t.Fatalf("missing apps/networkpolicy.yaml — the syncer has nothing to reflect")
	}
	s := string(np)
	for _, want := range []string{
		"kind: NetworkPolicy",
		"name: default-deny-all",
		"name: allow-same-org",
		"namespace: apps",
		"openova.io/organization: acme",
		"policyTypes:",
		"- Ingress",
		"- Egress",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("networkpolicy.yaml missing %q\n%s", want, s)
		}
	}
	for i, doc := range strings.Split(s, "---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var v any
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			t.Errorf("networkpolicy.yaml doc[%d] invalid YAML: %v\n%s", i, err, doc)
		}
	}
}

// TestRender_CiliumNetworkPolicyReservedEntities proves the MANDATORY CNP
// companion renders into the HOST-applied host-apps/ tree (#4475 §1) — NOT the
// syncer-reflected apps/ tree, because a CiliumNetworkPolicy cannot apply into a
// CRD-less vcluster apiserver — and that it admits the reserved entities a plain
// K8s NetworkPolicy cannot express: `ingress`/`host`/`remote-node` (so the Org's
// Application behind its Cilium-Gateway HTTPRoute is reachable, not 503) and
// `kube-apiserver` egress (so an in-vcluster Org pod can reach the cluster API).
func TestRender_CiliumNetworkPolicyReservedEntities(t *testing.T) {
	t.Parallel()
	// Both a paid (vcluster) tier and the free/host tier must carry the CNP in
	// the host-apps tree — it binds the host `<slug>` ns endpoints (the Org's own
	// pods for the host tier; the syncer-reflected vcluster pods for the paid
	// tier) for every tier identically.
	for _, slug := range []string{"m", "s"} {
		out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
			PlanSlug: slug, SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
		if err != nil {
			t.Fatalf("Render(%s): %v", slug, err)
		}
		// It must live in the host-apps/ tree — the path the per-Org host-apps
		// Flux Kustomization (per_org_flux.go) reconciles ALWAYS host-side onto the
		// `<slug>` ns. It must NOT be in the apps/ tree (which the vcluster tier
		// routes through the CRD-less vcluster apiserver via kubeConfig), nor in
		// the boundary kustomization (./vcluster, host-applied empty shell).
		cnp, ok := out["vcluster/host-apps/ciliumnetworkpolicy.yaml"]
		if !ok {
			t.Fatalf("plan %s: missing vcluster/host-apps/ciliumnetworkpolicy.yaml — the K8s default-deny would silently 503 the Org's app behind the Cilium Gateway", slug)
		}
		s := string(cnp)
		for _, want := range []string{
			"kind: CiliumNetworkPolicy",
			"apiVersion: cilium.io/v2",
			"namespace: apps", // the apps NS the host-apps Kustomization rewrites → host <slug> ns
			"endpointSelector: {}",
			"fromEntities:",
			"- ingress",
			"- host",
			"- remote-node",
			"toEntities:",
			"- kube-apiserver",
			`port: "443"`,
			`port: "6443"`,
			"openova.io/organization: acme",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("plan %s ciliumnetworkpolicy.yaml missing %q\n%s", slug, want, s)
			}
		}
		// NO `world` — only the gateway may reach the Org, never direct public ingress.
		if strings.Contains(s, "world") {
			t.Errorf("plan %s CNP must NOT admit the `world` entity (direct public ingress forbidden)\n%s", slug, s)
		}
		var v any
		if err := yaml.Unmarshal(cnp, &v); err != nil {
			t.Errorf("plan %s ciliumnetworkpolicy.yaml invalid YAML: %v\n%s", slug, err, s)
		}
		// #4475 §1: the CNP must NOT be in the apps/ tree (it would be routed into
		// the CRD-less vcluster apiserver for the paid tier and wedge the tree).
		if _, leaked := out["vcluster/apps/ciliumnetworkpolicy.yaml"]; leaked {
			t.Errorf("plan %s: CNP leaked into vcluster/apps/ — for the vcluster tier that wedges the kubeConfig-targeted Kustomization (no cilium.io/v2 CRD in the vcluster). It must live in host-apps/", slug)
		}
		appsKz := string(out["vcluster/apps/kustomization.yaml"])
		if strings.Contains(appsKz, "ciliumnetworkpolicy.yaml") {
			t.Errorf("plan %s: apps/kustomization.yaml must NOT enumerate the CNP — it lives in host-apps/\n%s", slug, appsKz)
		}
		if !strings.Contains(appsKz, "- networkpolicy.yaml") {
			t.Errorf("plan %s apps/kustomization.yaml missing the K8s NetworkPolicy\n%s", slug, appsKz)
		}
		// The host-apps kustomization index must enumerate the CNP so
		// `kustomize build ./vcluster/host-apps` applies it deterministically.
		hostAppsKz := string(out["vcluster/host-apps/kustomization.yaml"])
		if !strings.Contains(hostAppsKz, "- ciliumnetworkpolicy.yaml") {
			t.Errorf("plan %s host-apps/kustomization.yaml missing the CNP\n%s", slug, hostAppsKz)
		}
		// Anti-theater guard: the CNP must NOT be in the host-applied boundary
		// kustomization (./vcluster) — that would apply it to the empty host
		// shell, not the workload boundary.
		boundaryKz := string(out["vcluster/kustomization.yaml"])
		if strings.Contains(boundaryKz, "ciliumnetworkpolicy.yaml") {
			t.Errorf("plan %s: CNP leaked into the boundary kustomization (host shell) — it must live in the host-apps tree (workload boundary)\n%s", slug, boundaryKz)
		}
	}
}

// TestRender_TierGate proves the founder default: free/S → host-ns (NO
// vcluster.yaml), paid M+ → dedicated vCluster.
func TestRender_TierGate(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{ // planSlug → expect vcluster.yaml
		"s":     false,
		"":      false,
		"m":     true,
		"l":     true,
		"xl":    true,
		"flexi": true,
	}
	for slug, wantVcluster := range cases {
		out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
			PlanSlug: slug, SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
		if err != nil {
			t.Fatalf("Render(%q): %v", slug, err)
		}
		_, hasVcluster := out["vcluster/vcluster.yaml"]
		if hasVcluster != wantVcluster {
			t.Errorf("plan %q: vcluster.yaml present=%v, want %v (tier-gate)", slug, hasVcluster, wantVcluster)
		}
		// Either way the boundary ns + LimitRange + kustomization always render.
		for _, p := range []string{"vcluster/namespace.yaml", "vcluster/limitrange.yaml", "vcluster/kustomization.yaml"} {
			if _, ok := out[p]; !ok {
				t.Errorf("plan %q: missing %q (must render for every tier)", slug, p)
			}
		}
		kz := string(out["vcluster/kustomization.yaml"])
		listsVcluster := strings.Contains(kz, "- vcluster.yaml")
		if listsVcluster != wantVcluster {
			t.Errorf("plan %q: kustomization lists vcluster.yaml=%v, want %v\n%s", slug, listsVcluster, wantVcluster, kz)
		}
	}
}

// TestBoundaryIsVcluster_FlippableGate documents the one-line Sovereign switch.
func TestBoundaryIsVcluster_FlippableGate(t *testing.T) {
	t.Parallel()
	if allTiersVcluster {
		if !boundaryIsVcluster("s") {
			t.Errorf("allTiersVcluster=true must put S on a vcluster")
		}
		return
	}
	if boundaryIsVcluster("s") || boundaryIsVcluster("") {
		t.Errorf("default gate: free/S must be host-ns (boundaryIsVcluster=false)")
	}
	for _, paid := range []string{"m", "l", "xl", "flexi"} {
		if !boundaryIsVcluster(paid) {
			t.Errorf("default gate: %q must be vcluster (boundaryIsVcluster=true)", paid)
		}
	}
}
