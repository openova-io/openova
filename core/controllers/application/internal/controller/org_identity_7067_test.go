package controller

import "testing"

// The Sovereign's own Organization signs in with the Sovereign realm; a tenant
// Organization's realm is not resolvable here, so nothing is rendered.
func TestResolveOrgIdentity_7067(t *testing.T) {
	r := &Reconciler{}
	r.Cfg.SovereignFQDN = "hw307.omani.works"

	internal := makeOrg("hw307-omani-works")
	internal.Object["spec"].(map[string]interface{})["kind"] = "internal"
	id := r.resolveOrgIdentity(internal)
	if id == nil || id.Realm != "sovereign" || id.IssuerURL != "https://auth.hw307.omani.works/realms/sovereign" || id.IDPHint != "catalyst-pin" {
		t.Fatalf("internal Org identity = %+v, want realm sovereign at auth.hw307.omani.works", id)
	}
	if got := r.resolveOrgIdentity(makeOrg("acme")); got != nil {
		t.Fatalf("tenant Org identity = %+v, want none", got)
	}
	r.Cfg.SovereignFQDN = ""
	if got := r.resolveOrgIdentity(internal); got != nil {
		t.Fatalf("identity without a Sovereign FQDN = %+v, want none", got)
	}
}
