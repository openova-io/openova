package gitops

import (
	"testing"

	"sigs.k8s.io/yaml"
)

// The CNPG operator must be able to read the database instances the
// Organization's namespace holds on the host (bp-newapi's, which the funnel
// installs there for every new Organization; the WordPress database; the
// funnel's CNPG primary). The boundary CiliumNetworkPolicy selects every pod
// of the namespace, so whatever it does not admit is refused: without a rule
// for cnpg-system the operator cannot reach :8000 and keeps the Cluster
// Ready=False ("Instance Status Extraction Error", #6311 on hw296).
//
// Fails on main: the policy admits the gateway, the namespace and flux-system
// only. The second half keeps the carve-out from reaching the customer's
// synced pods: the organization fence must still deny them ingress from
// cnpg-system.
func TestBoundaryPolicyAdmitsTheCNPGOperatorToPlatformDatabases(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		out, err := Render(Inputs{Slug: "g01-w3ojsz0a", DisplayName: "G01", Tier: "org",
			PlanSlug: plan, SovereignFQDN: "hw307.omani.works", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
		if err != nil {
			t.Fatalf("Render(%s): %v", plan, err)
		}
		for _, tree := range []string{"vcluster/host-apps/", RegionBaselineHostAppsDir + "/"} {
			raw, ok := out[tree+ciliumNetworkPolicyDoc]
			if !ok {
				t.Fatalf("plan %s: no %s%s", plan, tree, ciliumNetworkPolicyDoc)
			}
			var cnp struct {
				Spec struct {
					EndpointSelector map[string]any `json:"endpointSelector"`
					Ingress          []struct {
						FromEndpoints []struct {
							MatchLabels map[string]string `json:"matchLabels"`
						} `json:"fromEndpoints"`
						ToPorts []struct {
							Ports []struct {
								Port     string `json:"port"`
								Protocol string `json:"protocol"`
							} `json:"ports"`
						} `json:"toPorts"`
					} `json:"ingress"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(raw, &cnp); err != nil {
				t.Fatalf("plan %s %s: %v", plan, tree, err)
			}
			if len(cnp.Spec.EndpointSelector) != 0 {
				t.Fatalf("plan %s %s: the policy no longer selects every pod (%v) — re-point this test", plan, tree, cnp.Spec.EndpointSelector)
			}
			ports := map[string]bool{}
			for _, r := range cnp.Spec.Ingress {
				fromOperator := false
				for _, e := range r.FromEndpoints {
					if e.MatchLabels["k8s:io.kubernetes.pod.namespace"] == "cnpg-system" {
						fromOperator = true
					}
				}
				if !fromOperator {
					continue
				}
				if len(r.ToPorts) == 0 {
					t.Errorf("plan %s %s: the cnpg-system rule opens every port; it must name 8000 and 9187 only", plan, tree)
				}
				for _, tp := range r.ToPorts {
					for _, p := range tp.Ports {
						if p.Protocol == "TCP" {
							ports[p.Port] = true
						}
					}
				}
			}
			for _, want := range []string{"8000", "9187"} {
				if !ports[want] {
					t.Errorf("plan %s %s%s admits no ingress from the CNPG operator (cnpg-system) on TCP %s: the operator cannot read the "+
						"database instances of the namespace and the Cluster stays Ready=False", plan, tree, ciliumNetworkPolicyDoc, want)
				}
			}
			if len(ports) != 2 {
				t.Errorf("plan %s %s: the cnpg-system rule opens %v, want exactly 8000 and 9187", plan, tree, ports)
			}
		}

		var fence struct {
			Spec struct {
				IngressDeny []struct {
					FromEndpoints []struct {
						MatchExpressions []struct {
							Key      string   `json:"key"`
							Operator string   `json:"operator"`
							Values   []string `json:"values"`
						} `json:"matchExpressions"`
					} `json:"fromEndpoints"`
				} `json:"ingressDeny"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(out["vcluster/host-apps/"+orgFenceDoc], &fence); err != nil {
			t.Fatalf("plan %s %s: %v", plan, orgFenceDoc, err)
		}
		denied := false
		for _, r := range fence.Spec.IngressDeny {
			for _, e := range r.FromEndpoints {
				for _, x := range e.MatchExpressions {
					if x.Key != "k8s:io.kubernetes.pod.namespace" || x.Operator != "NotIn" {
						continue
					}
					denied = true
					for _, v := range x.Values {
						if v == "cnpg-system" {
							t.Errorf("plan %s: the organization fence lets cnpg-system reach the synced pods", plan)
						}
					}
				}
			}
		}
		if !denied {
			t.Fatalf("plan %s: the organization fence no longer denies ingress by namespace — re-point this test", plan)
		}
	}
}
