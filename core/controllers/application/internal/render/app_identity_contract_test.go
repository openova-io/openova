package render

import "testing"

// The Application identity is rendered region-independently so a chart can
// derive per-app names (ADR-0015 D2; two apps in one namespace must not
// collide on a fixed default such as wordpress-db).
func TestWithTopologyContract_AppIdentity(t *testing.T) {
	in := map[string]interface{}{"openova": map[string]interface{}{"keep": "me"}}
	a := WithTopologyContract(in, TopologyContract{Mode: "active-hot-standby", Self: "a", Primary: "a", Peers: []string{"b"}, AppName: "blog", AppNamespace: "acme"})
	b := WithTopologyContract(in, TopologyContract{Mode: "active-hot-standby", Self: "b", Primary: "a", Peers: []string{"a"}, AppName: "blog", AppNamespace: "acme"})
	for _, v := range []map[string]interface{}{a, b} {
		o := v["openova"].(map[string]interface{})
		app, ok := o["application"].(map[string]interface{})
		if !ok || app["name"] != "blog" || app["namespace"] != "acme" {
			t.Fatalf("openova.application = %#v, want {name: blog, namespace: acme} in every region", o["application"])
		}
		if o["keep"] != "me" {
			t.Fatalf("existing openova keys dropped: %#v", o)
		}
	}
	none := WithTopologyContract(in, TopologyContract{Mode: "singleton", Self: "a", Primary: "a"})
	if _, ok := none["openova"].(map[string]interface{})["application"]; ok {
		t.Fatalf("openova.application rendered without an AppName")
	}
	if _, ok := in["openova"].(map[string]interface{})["application"]; ok {
		t.Fatalf("input values mutated")
	}
}
