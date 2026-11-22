package controller

// NET-191: a per-Org listener on the console Gateway admits only the routes of
// its Organization's namespace and of the namespace of its console route.

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func net191Selector(orgNamespace string) string {
	return `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[{"key":"kubernetes.io/metadata.name","operator":"In","values":["` + orgNamespace + `","catalyst-system"]}]}}}`
}

// net191Admission reads back the allowedRoutes of each listener of the console Gateway, as JSON.
func net191Admission(t *testing.T, r *Reconciler) map[string]string {
	t.Helper()
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName}, &gw); err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	raw, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	out := map[string]string{}
	for _, l := range raw {
		m, _ := l.(map[string]any)
		b, _ := json.Marshal(m["allowedRoutes"])
		out[m["name"].(string)] = string(b)
	}
	return out
}

func TestReconcileTenantConsoleTLS_NET191_ListenersAdmitOnlyTheOrganization(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ subdomain, listener string }{
		"subdomain is the slug":            {"", "acme"},
		"subdomain differs from the slug":  {"shop", "shop"},
		"subdomain in another letter case": {"ACME", "acme"},
	} {
		org := poolOrgFor()
		org.Spec.TenantPublic.Subdomain = c.subdomain
		r := consoleTLSReconciler(t, org, apexPair())
		if _, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil {
			t.Fatalf("%s: reconcileTenantConsoleTLS: %v", name, err)
		}
		got := net191Admission(t, r)
		for _, l := range []string{"console-https-" + c.listener, "console-http-" + c.listener} {
			if got[l] != net191Selector("acme") {
				t.Errorf("%s: listener %s admits %s, want %s", name, l, got[l], net191Selector("acme"))
			}
		}
		if got[consoleApexListenerHTTPSName] != "null" || got[consoleApexListenerHTTPName] != "null" {
			t.Errorf("%s: the apex listeners were changed: %v", name, got)
		}
	}
}

// A pair that an earlier writer left with `from: All` is repaired in place,
// and the repaired pair is a no-op on the next pass.
func TestEnsureConsoleOrgListener_NET191_RepairsFromAllInPlace(t *testing.T) {
	t.Parallel()
	seeded := apexPair()
	for _, l := range deadPortPair("*.acme.omani.homes") {
		m := l.(map[string]any)
		m["port"] = map[string]int64{"console-https-acme": 443, "console-http-acme": 80}[m["name"].(string)]
		m["allowedRoutes"] = map[string]any{"namespaces": map[string]any{"from": "All"}}
		seeded = append(seeded, m)
	}
	r := consoleTLSReconciler(t, poolOrgFor(), seeded)
	if got := net191Admission(t, r)["console-https-acme"]; got != `{"namespaces":{"from":"All"}}` {
		t.Fatalf("the fixture must start with from All, got %s", got)
	}
	names := orgConsoleTLSNamesFor("acme", "omani.homes")
	if changed, err := r.ensureConsoleOrgListener(context.Background(), r.Client, names); err != nil || !changed {
		t.Fatalf("a pair that admits every namespace must be repaired: changed=%v err=%v", changed, err)
	}
	got := net191Admission(t, r)
	if len(got) != 4 || got["console-https-acme"] != net191Selector("acme") || got["console-http-acme"] != net191Selector("acme") {
		t.Errorf("want the pair repaired in place to %s, got %v", net191Selector("acme"), got)
	}
	if changed, err := r.ensureConsoleOrgListener(context.Background(), r.Client, names); err != nil || changed {
		t.Errorf("the second pass must be a no-op: changed=%v err=%v", changed, err)
	}
}
