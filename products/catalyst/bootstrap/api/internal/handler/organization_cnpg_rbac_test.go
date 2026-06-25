package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
	"sigs.k8s.io/yaml"
)

// TestPerOrgBPCNPGWebhookHijackRBACOff is the generator-side guard for the
// G65 (#4322 #4143 #4201) webhook-HIJACK fix.
//
// The per-Org bp-cnpg operator must be STRUCTURALLY INCAPABLE of patching the
// cluster-singleton cnpg webhook configs. The upstream cloudnative-pg subchart
// grants get,patch on *webhookconfigurations in the operator ClusterRole
// UNCONDITIONALLY (even when clusterWide=false), so the per-Org install MUST
// turn the subchart RBAC off (cloudnative-pg.rbac.create=false) — the bp-cnpg
// umbrella then renders minimal replacement RBAC with NO webhookconfigurations
// (asserted by platform/cnpg/chart/tests/per-org-operator-rbac.sh). This test
// locks the generator half: the emitted HelmRelease values carry rbac.create
// false alongside the existing webhook-less + namespace-scoped settings.
func TestPerOrgBPCNPGWebhookHijackRBACOff(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{CNPG: "1.0.14"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-cnpg.yaml"]
	if !ok {
		t.Fatalf("bp-cnpg.yaml missing from render output")
	}

	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("bp-cnpg.yaml does not parse as YAML: %v\n--- body ---\n%s", err, body)
	}

	cnpg, ok := hr.Spec.Values["cloudnative-pg"].(map[string]interface{})
	if !ok {
		t.Fatalf("values.cloudnative-pg missing or not a map; values=%#v", hr.Spec.Values)
	}

	// rbac.create MUST be explicitly false — this is the keystone of the
	// hijack-vector removal.
	rbac, ok := cnpg["rbac"].(map[string]interface{})
	if !ok {
		t.Fatalf("values.cloudnative-pg.rbac missing — the subchart RBAC (with the cluster webhookconfigurations grant) would still render")
	}
	if create, _ := rbac["create"].(bool); create {
		t.Errorf("cloudnative-pg.rbac.create must be false (got true) — per-Org operator would retain cluster-scoped webhookconfigurations patch (#4322)")
	}

	// The pre-existing webhook-less + namespace-scoped invariants must remain.
	webhook, ok := cnpg["webhook"].(map[string]interface{})
	if !ok {
		t.Fatalf("values.cloudnative-pg.webhook missing — per-Org operator must be webhook-less")
	}
	for _, side := range []string{"mutating", "validating"} {
		w, ok := webhook[side].(map[string]interface{})
		if !ok {
			t.Fatalf("values.cloudnative-pg.webhook.%s missing", side)
		}
		if create, _ := w["create"].(bool); create {
			t.Errorf("cloudnative-pg.webhook.%s.create must be false (got true) — per-Org operator must not own the cluster webhook singleton", side)
		}
	}

	cfg, ok := cnpg["config"].(map[string]interface{})
	if !ok {
		t.Fatalf("values.cloudnative-pg.config missing")
	}
	if clusterWide, _ := cfg["clusterWide"].(bool); clusterWide {
		t.Errorf("cloudnative-pg.config.clusterWide must be false (got true) — per-Org operator must be namespace-scoped")
	}
}
