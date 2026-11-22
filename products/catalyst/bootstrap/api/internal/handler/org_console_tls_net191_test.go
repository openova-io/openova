package handler

// NET-191: the per-Org listener pair that this door applies admits only the
// routes of the Organization's namespace and of its console route's namespace,
// with the values and the order that the org-controller twin writes.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func TestEnsureOrgConsoleListener_NET191_AdmitsOnlyTheOrganization(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	rec := store.OrganizationProvisionRecord{Subdomain: "shop", TenantNamespace: "acme", DomainMode: store.OrganizationDomainFreeSubdomain, ParentDomain: "omani.homes"}
	names, _ := resolveOrgConsoleTLSNames(rec)
	_ = ensureOrgConsoleListener(context.Background(), dyn, names, rec) // the fake does not merge an apply; the patch body is judged
	var body struct {
		Spec struct {
			Listeners []struct {
				Name          string         `json:"name"`
				AllowedRoutes map[string]any `json:"allowedRoutes"`
			} `json:"listeners"`
		} `json:"spec"`
	}
	for _, a := range dyn.Actions() {
		if pa, ok := a.(interface{ GetPatch() []byte }); ok && a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
			_ = json.Unmarshal(pa.GetPatch(), &body)
		}
	}
	want := `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[{"key":"kubernetes.io/metadata.name","operator":"In","values":["acme","catalyst-system"]}]}}}`
	got := map[string]string{}
	for _, l := range body.Spec.Listeners {
		b, _ := json.Marshal(l.AllowedRoutes)
		got[l.Name] = string(b)
	}
	if len(got) != 2 || got["console-https-shop"] != want || got["console-http-shop"] != want {
		t.Errorf("the pair must admit %s, got %v", want, got)
	}
}

// The reconcile loop builds the record from the Organization CR: the namespace
// of the Org is its slug, also when its public subdomain differs.
func TestOrgConsoleTLSRecordFromOrgCR_NET191_TenantNamespaceIsTheSlug(t *testing.T) {
	for _, subdomain := range []string{"", "shop"} {
		cr := funnelOrgCR("uatco", "omani.homes")
		cr.Object["spec"].(map[string]any)["tenantPublic"].(map[string]any)["subdomain"] = subdomain
		if rec, ok := orgConsoleTLSRecordFromOrgCR(cr, "hw292.omani.works"); !ok || rec.TenantNamespace != "uatco" {
			t.Errorf("subdomain %q: ok=%v TenantNamespace=%q, want uatco", subdomain, ok, rec.TenantNamespace)
		}
	}
}
