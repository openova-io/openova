package gitops

import (
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// NET-170 — the Organization fence: two host-side deny objects per
// Organization (orgFenceTemplate, orgFenceAPITemplate). Each test decodes the
// rendered object strictly, so a field the fence must not carry (an allow
// rule, a CIDR, an FQDN, a world entity) fails the decode or the comparison.

type fenceExpr struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type fenceSelector struct {
	MatchLabels      map[string]string `json:"matchLabels,omitempty"`
	MatchExpressions []fenceExpr       `json:"matchExpressions,omitempty"`
}

type fenceRule struct {
	FromEndpoints []fenceSelector `json:"fromEndpoints,omitempty"`
	ToEndpoints   []fenceSelector `json:"toEndpoints,omitempty"`
	ToEntities    []string        `json:"toEntities,omitempty"`
}

type fenceDefaultDeny struct {
	Ingress *bool `json:"ingress"`
	Egress  *bool `json:"egress"`
}

type fenceSpec struct {
	EndpointSelector  fenceSelector     `json:"endpointSelector"`
	EnableDefaultDeny *fenceDefaultDeny `json:"enableDefaultDeny"`
	IngressDeny       []fenceRule       `json:"ingressDeny,omitempty"`
	EgressDeny        []fenceRule       `json:"egressDeny,omitempty"`
}

type fenceMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type fencePolicy struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Metadata   fenceMeta `json:"metadata"`
	Spec       fenceSpec `json:"spec"`
}

func fenceRender(t *testing.T, plan string, allow []string) map[string][]byte {
	t.Helper()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: plan,
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.4",
		OrgFenceAllowNamespaces: allow})
	if err != nil {
		t.Fatalf("Render(%s): %v", plan, err)
	}
	return out
}

func fenceDecode(t *testing.T, out map[string][]byte, path string) fencePolicy {
	t.Helper()
	raw, ok := out[path]
	if !ok {
		t.Fatalf("missing %s", path)
	}
	var p fencePolicy
	if err := yaml.UnmarshalStrict(raw, &p); err != nil {
		t.Fatalf("%s does not decode strictly as a fence object: %v\n%s", path, err, raw)
	}
	return p
}

func boolPtr(b bool) *bool { return &b }

// syncedNonSessionPods is the endpoint selector of both fence objects.
var syncedNonSessionPods = fenceSelector{MatchExpressions: []fenceExpr{
	{Key: "vcluster.loft.sh/managed-by", Operator: "Exists"},
	{Key: "access.openova.io/session", Operator: "DoesNotExist"},
}}

// denyPeers is a deny selector with its three terms: the namespace label
// Exists, the cluster label Exists, and the namespaces NotIn.
func denyPeers(namespaces ...string) fenceSelector {
	return fenceSelector{MatchExpressions: []fenceExpr{
		{Key: "k8s:io.kubernetes.pod.namespace", Operator: "Exists"},
		{Key: "io.cilium.k8s.policy.cluster", Operator: "Exists"},
		{Key: "k8s:io.kubernetes.pod.namespace", Operator: "NotIn", Values: namespaces},
	}}
}

func wantOrgFence(allowed ...string) fencePolicy {
	return fencePolicy{
		APIVersion: "cilium.io/v2",
		Kind:       "CiliumNetworkPolicy",
		Metadata: fenceMeta{Name: "org-fence", Namespace: "apps", Labels: map[string]string{
			"openova.io/organization":       "acme",
			"openova.io/managed-by":         "catalyst",
			"catalyst.openova.io/component": "org-fence",
		}},
		Spec: fenceSpec{
			EndpointSelector:  syncedNonSessionPods,
			EnableDefaultDeny: &fenceDefaultDeny{Ingress: boolPtr(false), Egress: boolPtr(false)},
			IngressDeny:       []fenceRule{{FromEndpoints: []fenceSelector{denyPeers("acme", "flux-system")}}},
			EgressDeny: []fenceRule{
				{ToEndpoints: []fenceSelector{denyPeers(append([]string{"acme"}, allowed...)...)}},
				{ToEntities: []string{"host", "remote-node"}},
			},
		},
	}
}

var wantOrgFenceAPI = fencePolicy{
	APIVersion: "cilium.io/v2",
	Kind:       "CiliumNetworkPolicy",
	Metadata: fenceMeta{Name: "org-fence-api", Namespace: "apps", Labels: map[string]string{
		"openova.io/organization":       "acme",
		"openova.io/managed-by":         "catalyst",
		"catalyst.openova.io/component": "org-fence",
	}},
	Spec: fenceSpec{
		EndpointSelector:  syncedNonSessionPods,
		EnableDefaultDeny: &fenceDefaultDeny{Ingress: boolPtr(false), Egress: boolPtr(false)},
		EgressDeny:        []fenceRule{{ToEntities: []string{"kube-apiserver"}}},
	},
}

// Both objects render for every plan, in the shared host-apps tree and its
// region-baseline copy, are listed by both host-apps indexes, and never land
// in the apps tree (synced into the vCluster) or the boundary index.
func TestOrgFence_RenderedHostSideForEveryPlan(t *testing.T) {
	t.Parallel()
	for _, plan := range []string{"s", "m", "l", "xl", "flexi"} {
		out := fenceRender(t, plan, nil)
		for _, doc := range []string{orgFenceDoc, orgFenceAPIDoc} {
			shared, ok := out[SharedHostAppsDir+"/"+doc]
			if !ok {
				t.Fatalf("plan %s: missing %s/%s", plan, SharedHostAppsDir, doc)
			}
			if copied := out[RegionBaselineHostAppsDir+"/"+doc]; string(copied) != string(shared) {
				t.Errorf("plan %s: %s/%s differs from the shared copy:\n%s", plan, RegionBaselineHostAppsDir, doc, copied)
			}
			for _, index := range []string{SharedHostAppsDir, RegionBaselineHostAppsDir} {
				if k := string(out[index+"/kustomization.yaml"]); !strings.Contains(k, "\n  - "+doc+"\n") {
					t.Errorf("plan %s: %s/kustomization.yaml does not list %s\n%s", plan, index, doc, k)
				}
			}
			for _, index := range []string{"vcluster/kustomization.yaml", SharedAppsDir + "/kustomization.yaml"} {
				if strings.Contains(string(out[index]), doc) {
					t.Errorf("plan %s: %s lists %s", plan, index, doc)
				}
			}
			for path := range out {
				if strings.HasSuffix(path, "/"+doc) && !strings.HasPrefix(path, SharedHostAppsDir+"/") && !strings.HasPrefix(path, RegionBaselineHostAppsDir+"/") {
					t.Errorf("plan %s: %s renders outside the host-apps trees", plan, path)
				}
			}
		}
	}
}

// The objects are exactly the fence: they select only the synced pods that
// are not session pods, they deny and never allow, and each deny selector
// carries the namespace term, the cluster term and the NotIn list.
func TestOrgFence_ExactObjects(t *testing.T) {
	t.Parallel()
	out := fenceRender(t, "m", nil)
	if got, want := fenceDecode(t, out, SharedHostAppsDir+"/"+orgFenceDoc), wantOrgFence("kube-system", "keycloak"); !reflect.DeepEqual(got, want) {
		t.Errorf("org-fence:\n got %+v\nwant %+v", got, want)
	}
	if got := fenceDecode(t, out, SharedHostAppsDir+"/"+orgFenceAPIDoc); !reflect.DeepEqual(got, wantOrgFenceAPI) {
		t.Errorf("org-fence-api:\n got %+v\nwant %+v", got, wantOrgFenceAPI)
	}
	for _, doc := range []string{orgFenceDoc, orgFenceAPIDoc} {
		s := string(out[SharedHostAppsDir+"/"+doc])
		for _, banned := range []string{"world", "toCIDR", "toFQDNs", "app: vcluster"} {
			if strings.Contains(s, banned) {
				t.Errorf("%s carries %q\n%s", doc, banned, s)
			}
		}
	}
}

// The namespaces of the flag --org-fence-allow-namespaces reach the egress
// deny of org-fence and nothing else.
func TestOrgFence_AllowNamespacesReachTheEgressDeny(t *testing.T) {
	t.Parallel()
	out := fenceRender(t, "m", []string{"kube-system", "keycloak", "vllm"})
	if got, want := fenceDecode(t, out, SharedHostAppsDir+"/"+orgFenceDoc), wantOrgFence("kube-system", "keycloak", "vllm"); !reflect.DeepEqual(got, want) {
		t.Errorf("org-fence with an extra namespace:\n got %+v\nwant %+v", got, want)
	}
	if got := fenceDecode(t, out, SharedHostAppsDir+"/"+orgFenceAPIDoc); !reflect.DeepEqual(got, wantOrgFenceAPI) {
		t.Errorf("org-fence-api changed with the flag:\n got %+v\nwant %+v", got, wantOrgFenceAPI)
	}
}

// The host-side policy admits the gateway entities ingress and remote-node,
// and no longer host.
func TestHostSidePolicy_IngressEntitiesWithoutHost(t *testing.T) {
	t.Parallel()
	out := fenceRender(t, "m", nil)
	var p struct {
		Spec struct {
			Ingress []struct {
				FromEntities []string `json:"fromEntities"`
			} `json:"ingress"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(out[SharedHostAppsDir+"/"+ciliumNetworkPolicyDoc], &p); err != nil {
		t.Fatalf("host-side policy: %v", err)
	}
	var entities [][]string
	for _, rule := range p.Spec.Ingress {
		if rule.FromEntities != nil {
			entities = append(entities, rule.FromEntities)
		}
	}
	if want := [][]string{{"ingress", "remote-node"}}; !reflect.DeepEqual(entities, want) {
		t.Errorf("host-side policy ingress entities = %v, want %v", entities, want)
	}
}

func TestParseOrgFenceAllowNamespaces(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want []string
	}{
		{DefaultOrgFenceAllowNamespaces, []string{"kube-system", "keycloak"}},
		{" kube-system , keycloak ,,", []string{"kube-system", "keycloak"}},
		{"kube-system,keycloak,vllm", []string{"kube-system", "keycloak", "vllm"}},
	} {
		got, err := ParseOrgFenceAllowNamespaces(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseOrgFenceAllowNamespaces(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", " , ", "Kube-System", "kube_system", "kube-system,key cloak", `keycloak"`} {
		if got, err := ParseOrgFenceAllowNamespaces(in); err == nil {
			t.Errorf("ParseOrgFenceAllowNamespaces(%q) = %v, want an error", in, got)
		}
	}
}
