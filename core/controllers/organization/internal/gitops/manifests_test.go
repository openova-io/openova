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

// TestRender_ProvisioningRBACAndVclusterTargetNS_4991 locks in the two #4991
// deliveries: (1) the provisioning-tenant Role+RoleBinding lands in the
// ALWAYS-host-applied host-apps tree for BOTH tiers, and (2) the vcluster-tier
// apps tree creates the target namespace INSIDE the vcluster (host tier must NOT
// re-declare the boundary-owned host ns).
func TestRender_ProvisioningRBACAndVclusterTargetNS_4991(t *testing.T) {
	t.Parallel()

	// --- vcluster tier (m) ---
	vc, err := Render(Inputs{Slug: "acme", DisplayName: "ACME", Tier: "org",
		PlanSlug: "m", SovereignFQDN: "omantel.omani.works", HostCluster: "hz"})
	if err != nil {
		t.Fatalf("Render(m): %v", err)
	}
	rbac, ok := vc["vcluster/host-apps/"+provisioningRBACDoc]
	if !ok {
		t.Fatalf("vcluster tier: missing vcluster/host-apps/%s — provisioning SA can't create the kubeconfig mirror Secret → mirror step 403s and the whole provision aborts (#4991)", provisioningRBACDoc)
	}
	for _, want := range []string{
		"kind: Role", "kind: RoleBinding", "name: provisioning-tenant",
		"namespace: acme", "name: provisioning", "namespace: org-services",
		"create", "delete",
	} {
		if !strings.Contains(string(rbac), want) {
			t.Errorf("provisioning-rbac.yaml missing %q\n%s", want, string(rbac))
		}
	}
	// host-apps kustomization enumerates BOTH the CNP and the RBAC.
	hk := string(vc["vcluster/host-apps/kustomization.yaml"])
	for _, want := range []string{"- " + ciliumNetworkPolicyDoc, "- " + provisioningRBACDoc} {
		if !strings.Contains(hk, want) {
			t.Errorf("host-apps kustomization missing %q\n%s", want, hk)
		}
	}
	// vcluster tier creates the vcluster-internal target ns + lists it.
	tns, ok := vc["vcluster/apps/"+appsNamespaceDoc]
	if !ok {
		t.Fatalf("vcluster tier: missing vcluster/apps/%s — the kubeConfig-targeted apps Kustomization fails 'namespaces \"acme\" not found' and the app never deploys (#4991)", appsNamespaceDoc)
	}
	if !strings.Contains(string(tns), "name: acme") {
		t.Errorf("apps namespace.yaml must be name: acme\n%s", string(tns))
	}
	ak := string(vc["vcluster/apps/kustomization.yaml"])
	for _, want := range []string{"- " + networkPolicyDoc, "- " + appsNamespaceDoc} {
		if !strings.Contains(ak, want) {
			t.Errorf("vcluster-tier apps kustomization missing %q\n%s", want, ak)
		}
	}

	// --- host tier (s): RBAC still delivered; NO apps target ns ---
	hs, err := Render(Inputs{Slug: "bob", DisplayName: "Bob", Tier: "org",
		PlanSlug: "s", SovereignFQDN: "omantel.omani.works", HostCluster: "hz"})
	if err != nil {
		t.Fatalf("Render(s): %v", err)
	}
	if _, ok := hs["vcluster/host-apps/"+provisioningRBACDoc]; !ok {
		t.Errorf("host tier: provisioning RBAC must ALSO be delivered (the SA still mirrors/kicks in the host ns)")
	}
	if _, ok := hs["vcluster/apps/"+appsNamespaceDoc]; ok {
		t.Errorf("host tier: must NOT emit vcluster/apps/namespace.yaml — the boundary already owns the host ns; a second Flux Kustomization managing the same Namespace fights it")
	}
	if strings.Contains(string(hs["vcluster/apps/kustomization.yaml"]), "- "+appsNamespaceDoc) {
		t.Errorf("host tier: apps kustomization must not list namespace.yaml")
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
// #4758 — the vcluster-Org host-namespace LimitRange must NOT set
// maxLimitRequestRatio: the vcluster syncer reflects the vcluster's own
// (non-Guaranteed) system pods (coredns, ratio 50:1) into this ns, and a
// ratio=1 forbids every synced pod at admission → vcluster runs nothing →
// customer app 404. defaultRequest/default stay (quota admission), ratio goes.
func TestRender_LimitRangeNoRatioForVclusterOrg(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
		PlanSlug: "m", SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	lr := string(out["vcluster/limitrange.yaml"])
	for _, want := range []string{
		"kind: LimitRange",
		"defaultRequest:",
		"default:",
		"namespace: acme",
	} {
		if !strings.Contains(lr, want) {
			t.Errorf("M-tier limitrange.yaml missing %q\n%s", want, lr)
		}
	}
	if strings.Contains(lr, "maxLimitRequestRatio") {
		t.Errorf("#4758: vcluster-Org limitrange.yaml must NOT set maxLimitRequestRatio (breaks vcluster pod-sync)\n%s", lr)
	}
	var v any
	if err := yaml.Unmarshal([]byte(lr), &v); err != nil {
		t.Errorf("limitrange.yaml invalid YAML: %v", err)
	}
}

// TestRender_VclusterRegistersHTTPRouteCRD_4785 locks in the #4785 fix: the per-Org
// vcluster HR must register the Gateway-API HTTPRoute CRD via
// experimental.deploy.vcluster.manifests so a customer app's chart (bp-wordpress)
// can CREATE its ingress HTTPRoute. vcluster 0.33.4 sync.toHost.customResources does
// NOT register the CRD (only reflects instances host-ward), so without this the
// tenant-<slug>-apps Kustomization dry-run fails "no matches for kind HTTPRoute" and
// wedges every customer app. Validated live on hw228 acme 2026-07-06 — WordPress+MySQL
// reached 1/1 Running the moment this CRD was registered.
func TestRender_VclusterRegistersHTTPRouteCRD_4785(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
		PlanSlug: "m", SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vc := string(out["vcluster/vcluster.yaml"])
	for _, want := range []string{
		"experimental:",
		"deploy:",
		"manifests: |",
		"httproutes.gateway.networking.k8s.io",
		"kind: HTTPRoute",
		"x-kubernetes-preserve-unknown-fields: true",
	} {
		if !strings.Contains(vc, want) {
			t.Errorf("#4785: vcluster.yaml must register the HTTPRoute CRD in the per-Org vcluster, missing %q", want)
		}
	}
	var v any
	if err := yaml.Unmarshal([]byte(vc), &v); err != nil {
		t.Errorf("#4785: vcluster.yaml invalid YAML after the CRD-register block: %v", err)
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
			// vcluster bootstrap-deadlock fix (proven live hw225): the same-Org
			// (same-ns) allow lets the synced coredns reach vcluster-0:8443, and
			// the flux-system allow lets the kustomize-controller mint the
			// kubeconfig + apply apps. Without BOTH, the vcluster never functions
			// and no customer app ever deploys.
			"fromEndpoints:",
			"k8s:io.kubernetes.pod.namespace: flux-system",
			"toEndpoints:",
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

// TestRender_VclusterHRSelfHealsColdPull_5003 locks in the #5003 durable fix:
// the per-Org vcluster HelmRelease MUST carry install/upgrade remediation
// retries of -1 (retry indefinitely — never terminally Stalled) and a widened
// spec.timeout so the FIRST cold Harbor proxy-ghcr pull of the vcluster init
// images (~3m11s live on hw241) fits inside a single Helm-operation deadline.
// Without both, a transient cold-pull "context deadline exceeded" marks the HR
// Stalled=True/RetriesExceeded, the tenant-<slug>-kubeconfig secret is never
// minted, and the customer funnel app 404s forever (the last zero-touch blocker).
func TestRender_VclusterHRSelfHealsColdPull_5003(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: "m",
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])

	// (1) String surface — the fields are present in the rendered YAML.
	for _, want := range []string{"timeout: 10m", "install:", "upgrade:", "remediation:", "retries: -1"} {
		if !strings.Contains(vcl, want) {
			t.Errorf("#5003: vcluster.yaml missing %q\nfull contents:\n%s", want, vcl)
		}
	}

	// (2) Structural surface — parse the HR and assert the exact spec shape so a
	// stray indentation/typo can't pass the string check while breaking Flux.
	var hr struct {
		Spec struct {
			Timeout string `json:"timeout"`
			Install struct {
				Remediation struct {
					Retries int `json:"retries"`
				} `json:"remediation"`
			} `json:"install"`
			Upgrade struct {
				Remediation struct {
					Retries int `json:"retries"`
				} `json:"remediation"`
			} `json:"upgrade"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(vcl), &hr); err != nil {
		t.Fatalf("#5003: vcluster.yaml is not valid HelmRelease YAML: %v\n%s", err, vcl)
	}
	if hr.Spec.Timeout != "10m" {
		t.Errorf("#5003: spec.timeout = %q, want \"10m\" (cold proxy-ghcr pull ~3m11s must fit)", hr.Spec.Timeout)
	}
	if hr.Spec.Install.Remediation.Retries != -1 {
		t.Errorf("#5003: spec.install.remediation.retries = %d, want -1 (retry forever, never terminally Stalled)", hr.Spec.Install.Remediation.Retries)
	}
	if hr.Spec.Upgrade.Remediation.Retries != -1 {
		t.Errorf("#5003: spec.upgrade.remediation.retries = %d, want -1 (retry forever, never terminally Stalled)", hr.Spec.Upgrade.Remediation.Retries)
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

// TestRender_CiliumNetworkPolicyCarriesClusterDNSEgress is the #5617 regression
// gate, asserted on the VALUE not the key.
//
// The per-Org host-side CNP selects every endpoint in the Org namespace
// (endpointSelector {}) AND declares egress. Under Cilium that makes egress
// deny-by-default for every pod in the namespace, so whatever this rule set
// omits is unreachable for the whole Org. It used to omit cluster DNS: the
// bp-oidc-gate companion (which ships an ingress-only CNP) could not resolve
// keycloak.keycloak.svc.cluster.local and 500'd every OAuth callback AFTER a
// successful login (live hw292, Org uatco, oauthproxy.go:881).
//
// A `strings.Contains(s, "53")` here would pass on a port number appearing
// anywhere in the document, so this walks the parsed structure and requires a
// rule that pairs kube-system/kube-dns with BOTH 53/UDP and 53/TCP.
func TestRender_CiliumNetworkPolicyCarriesClusterDNSEgress(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"m", "s"} {
		out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org",
			PlanSlug: slug, SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
		if err != nil {
			t.Fatalf("Render(%s): %v", slug, err)
		}
		raw, ok := out["vcluster/host-apps/ciliumnetworkpolicy.yaml"]
		if !ok {
			t.Fatalf("plan %s: missing host-apps/ciliumnetworkpolicy.yaml", slug)
		}
		var doc struct {
			Spec struct {
				Egress []struct {
					ToEndpoints []map[string]map[string]string `json:"toEndpoints"`
					ToCIDR      []string                       `json:"toCIDR"`
					ToCIDRSet   []map[string]string            `json:"toCIDRSet"`
					ToPorts     []struct {
						Ports []struct {
							Port     string `json:"port"`
							Protocol string `json:"protocol"`
						} `json:"ports"`
					} `json:"toPorts"`
				} `json:"egress"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("plan %s: CNP did not parse: %v", slug, err)
		}
		if len(doc.Spec.Egress) == 0 {
			t.Fatalf("plan %s: CNP declares no egress rules at all", slug)
		}
		var udp, tcp bool
		for _, rule := range doc.Spec.Egress {
			// The destination must be cluster DNS itself. A port-53 allow aimed
			// at the Org's own namespace is not DNS coverage.
			toDNS := false
			for _, ep := range rule.ToEndpoints {
				ml := ep["matchLabels"]
				if ml["k8s:io.kubernetes.pod.namespace"] == "kube-system" && ml["k8s-app"] == "kube-dns" {
					toDNS = true
				}
			}
			if !toDNS {
				continue
			}
			for _, tp := range rule.ToPorts {
				for _, p := range tp.Ports {
					if p.Port != "53" {
						continue
					}
					switch p.Protocol {
					case "UDP":
						udp = true
					case "TCP":
						tcp = true
					}
				}
			}
		}
		if !udp || !tcp {
			t.Errorf("plan %s: #5617 — the per-Org CNP constrains egress for EVERY pod in the "+
				"Org namespace but does not allow 53/UDP+53/TCP to kube-system/kube-dns "+
				"(udp=%v tcp=%v). Every workload that ships no DNS egress of its own is mute.\n%s",
				slug, udp, tcp, string(raw))
		}
		// #4360/#4656: a CIDR rule matches neither an in-cluster pod identity nor
		// a ClusterMesh remote identity, so a CIDR-shaped DNS allow would be inert
		// and would silently re-break the peer region.
		for _, rule := range doc.Spec.Egress {
			if len(rule.ToCIDR) > 0 || len(rule.ToCIDRSet) > 0 {
				t.Errorf("plan %s: the per-Org CNP must express destinations as toEndpoints/"+
					"toEntities — a toCIDR/ipBlock never matches a ClusterMesh remote identity "+
					"(#4360/#4656)\n%s", slug, string(raw))
			}
		}
	}
}
