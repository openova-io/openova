package suite

import (
	"encoding/json"
	"strings"
	"testing"
)

// sovereignObjectVars is the ConfigMap list of flux-system of one region:
// sovereign-variables with its values, and the delivered registry unless
// registry is empty.
func sovereignObjectVars(edit func(d obj), registry string) string {
	d := obj{"SOVEREIGN_FQDN": "t99.example.test", "SOVEREIGN_DEPLOYMENT_ID": "0123456789abcdef", "PROVIDER": "huawei",
		"SOVEREIGN_REGIONS_JSON": `[{"cloudRegion":"hw-region-a-rtz-prod","provider":"huawei"},{"cloudRegion":"hw-region-b-rtz-prod","provider":"huawei"}]`}
	if edit != nil {
		edit(d)
	}
	items := []obj{{"metadata": obj{"namespace": "flux-system", "name": "sovereign-variables"}, "data": d},
		{"metadata": obj{"namespace": "flux-system", "name": "flux-bootstrap"}, "data": obj{"SOVEREIGN_FQDN": "wrong.example.test"}}}
	if registry != "" {
		items = append(items, obj{"metadata": obj{"namespace": "flux-system", "name": "sovereign-network-registry"},
			"data": obj{"NETWORK_REGISTRY_JSON": registry, "NETWORK_REGISTRY_REVISION": "3f2a9c1b0d4e"}})
	}
	return kubeList(items...)
}

// sovereignObject is the object the writer writes: its two field managers,
// the spec from the values above, and the registry as status.network.
func sovereignObject(name string, edit func(o obj)) obj {
	meta := kubeMeta("", name)
	meta["managedFields"] = []obj{{"manager": "sovereign-ops", "operation": "Apply"},
		{"manager": "sovereign-ops", "operation": "Apply", "subresource": "status"}}
	o := obj{"metadata": meta, "spec": obj{"fqdn": "t99.example.test", "deploymentId": "0123456789abcdef", "provider": "huawei",
		"regions": []obj{{"code": "hw-region-a-rtz-prod", "provider": "huawei"}, {"code": "hw-region-b-rtz-prod", "provider": "huawei"}}},
		"status": obj{"network": networkRegistry(nil)}}
	if edit != nil {
		edit(o)
	}
	return o
}

func init() {
	scenario := func(change map[string]string) map[string]string {
		s := map[string]string{
			"* sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", nil)),
			"* configmaps flux-system":         sovereignObjectVars(nil, networkRegistryDoc),
		}
		for k, v := range change {
			if v == "" {
				delete(s, k)
			} else {
				s[k] = v
			}
		}
		return s
	}
	fakeAnswers["sovereign-object"] = scenario(nil)
	fakeAnswers["sovereign-object-none"] = scenario(map[string]string{"b sovereigns.catalyst.openova.io": kubeList()})
	fakeAnswers["sovereign-object-not-served"] = scenario(map[string]string{"b sovereigns.catalyst.openova.io": fakeNotServed})
	fakeAnswers["sovereign-object-two"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", nil), sovereignObject("hand-made", nil))})
	fakeAnswers["sovereign-object-other-name"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(sovereignObject("t99", nil))})
	fakeAnswers["sovereign-object-hand-applied"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) {
			o["metadata"].(obj)["managedFields"] = []obj{{"manager": "kubectl-client-side-apply", "operation": "Update"}}
		}))})
	fakeAnswers["sovereign-object-fqdn"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) { o["spec"].(obj)["fqdn"] = "t98.example.test" }))})
	fakeAnswers["sovereign-object-no-deployment-id"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) { delete(o["spec"].(obj), "deploymentId") }))})
	fakeAnswers["sovereign-object-value-missing"] = scenario(map[string]string{
		"a configmaps flux-system": sovereignObjectVars(func(d obj) { delete(d, "PROVIDER") }, networkRegistryDoc)})
	fakeAnswers["sovereign-object-regions"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) {
			o["spec"].(obj)["regions"] = []obj{{"code": "hw-region-a-rtz-prod"}}
		}))})
	fakeAnswers["sovereign-object-no-registry"] = scenario(map[string]string{"b configmaps flux-system": sovereignObjectVars(nil, "")})
	fakeAnswers["sovereign-object-no-network"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) { o["status"] = obj{} }))})
	fakeAnswers["sovereign-object-network-differs"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": kubeList(sovereignObject("sovereign-self", func(o obj) {
			o["status"] = obj{"network": networkRegistry(func(doc obj) { doc["mtu"] = 1370 })}
		}))})
}

func TestSovereignObjectCheck(t *testing.T) {
	if _, ok := registry[sovereignObjectID]; !ok || sovereignObjectID != "SOV-G17" {
		t.Fatalf("check %s is not registered", sovereignObjectID)
	}
	for name, c := range map[string]struct {
		scenario string
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"each region holds its object":           {"sovereign-object", ""},
		"a region has no object":                 {"sovereign-object-none", "region region-b has the Sovereign objects [], want exactly [sovereign-self]"},
		"a region does not serve the kind":       {"sovereign-object-not-served", `the server doesn't have a resource type "sovereigns.catalyst.openova.io"`},
		"a region has two objects":               {"sovereign-object-two", "region region-a has the Sovereign objects [hand-made sovereign-self], want exactly [sovereign-self]"},
		"the object has another name":            {"sovereign-object-other-name", "region region-a has the Sovereign objects [t99], want exactly [sovereign-self]"},
		"the object was applied by hand":         {"sovereign-object-hand-applied", "the field manager sovereign-ops did not write both sovereign-self and its status; the managers are [kubectl-client-side-apply]"},
		"the FQDN differs":                       {"sovereign-object-fqdn", `region region-b: spec.fqdn is "t98.example.test", SOVEREIGN_FQDN is "t99.example.test"`},
		"the deployment id is absent":            {"sovereign-object-no-deployment-id", `spec.deploymentId is "", SOVEREIGN_DEPLOYMENT_ID is "0123456789abcdef"`},
		"a value to compare with is missing":     {"sovereign-object-value-missing", "sovereign-variables has no PROVIDER to compare spec.provider with"},
		"the regions differ":                     {"sovereign-object-regions", "the codes of spec.regions are [hw-region-a-rtz-prod], the cloudRegion values of SOVEREIGN_REGIONS_JSON are [hw-region-a-rtz-prod hw-region-b-rtz-prod]"},
		"the parent delivered no registry":       {"sovereign-object-no-registry", "region region-b: no ConfigMap flux-system/sovereign-network-registry: the parent delivered no network registry to this region"},
		"the object has no status.network":       {"sovereign-object-no-network", `region region-a: status.network differs from NETWORK_REGISTRY_JSON of sovereign-network-registry revision "3f2a9c1b0d4e"`},
		"status.network differs from the source": {"sovereign-object-network-differs", "region region-b: status.network differs"},
	} {
		r := runCheck(t, c.scenario, sovereignObjectCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}

	r := runCheck(t, "sovereign-object", sovereignObjectCheck{}, &CheckContext{})
	if len(r.run.Steps) != 2 || r.run.Steps[0].ID != "SOV-G17.1" || r.run.Steps[1].ID != "SOV-G17.2" {
		t.Fatalf("want the steps SOV-G17.1 and SOV-G17.2, got %+v", r.run.Steps)
	}
	b, err := json.Marshal(r.run.Steps[1].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sovereigns":["sovereign-self"]`, `"managers":["sovereign-ops","sovereign-ops/status"]`,
		`"fqdn":"t99.example.test"`, `"regions":["hw-region-a-rtz-prod","hw-region-b-rtz-prod"]`, `"revision":"3f2a9c1b0d4e"`, `"network":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the evidence of SOV-G17.2 lacks %s: %s", want, b)
		}
	}
}
