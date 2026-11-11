package render

import "testing"

// The sign-in realm is rendered as openova.identity so an SSO-wired chart
// registers its client in the realm its users live in (Refs #7067).
func TestWithTopologyContract_Identity_7067(t *testing.T) {
	got := WithTopologyContract(map[string]interface{}{}, TopologyContract{
		Mode: "singleton", Self: "a", Primary: "a",
		Identity: &Identity{Realm: "sovereign", IssuerURL: "https://auth.hw307.omani.works/realms/sovereign", IDPHint: "catalyst-pin"},
	})
	id, ok := got["openova"].(map[string]interface{})["identity"].(map[string]interface{})
	if !ok || id["realm"] != "sovereign" || id["issuerURL"] != "https://auth.hw307.omani.works/realms/sovereign" || id["idpHint"] != "catalyst-pin" {
		t.Fatalf("openova.identity = %#v", got["openova"].(map[string]interface{})["identity"])
	}
	none := WithTopologyContract(map[string]interface{}{}, TopologyContract{Mode: "singleton", Self: "a", Primary: "a"})
	if _, ok := none["openova"].(map[string]interface{})["identity"]; ok {
		t.Fatal("openova.identity rendered without an Identity")
	}
}
