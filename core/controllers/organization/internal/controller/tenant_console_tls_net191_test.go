// tenant_console_tls_net191_test.go — NET-191. The per-Org console listener
// pair admits HTTPRoutes only from the Organization's own namespace and from
// the namespace of its console route. A listener that admits every namespace
// lets a route of any other Organization attach to this Organization's host
// names, so the pair the controller wrote before NET-191 must be repaired in
// place.
package controller

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// net191Admission is the allowedRoutes the pair must carry, written out in
// full: a selector on the built-in name label with exactly these two values in
// this order.
func net191Admission(orgNS, consoleNS string) string {
	return `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
		`{"key":"kubernetes.io/metadata.name","operator":"In","values":["` + orgNS + `","` + consoleNS + `"]}]}}}`
}

// listenerAdmissions reads the console Gateway's spec listeners and returns
// each listener's allowedRoutes as JSON ("absent" when it has none).
func listenerAdmissions(t *testing.T, r *Reconciler) map[string]string {
	t.Helper()
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, &gw); err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	raw, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners: %v", err)
	}
	out := map[string]string{}
	for _, l := range raw {
		m, ok := l.(map[string]any)
		if !ok {
			t.Fatalf("listener is not an object: %#v", l)
		}
		name, _ := m["name"].(string)
		ar, has := m["allowedRoutes"]
		if !has {
			out[name] = "absent"
			continue
		}
		b, err := json.Marshal(ar)
		if err != nil {
			t.Fatalf("marshal allowedRoutes of %s: %v", name, err)
		}
		out[name] = string(b)
	}
	return out
}

// admitAllPair is the pair as the controller wrote it before NET-191: right
// ports, host and certificate, and allowedRoutes from every namespace.
func admitAllPair() []any {
	all := map[string]any{"namespaces": map[string]any{"from": "All"}}
	return []any{
		map[string]any{
			"name": "console-https-acme", "hostname": "*.acme.omani.homes",
			"port": int64(443), "protocol": "HTTPS", "allowedRoutes": all,
			"tls": map[string]any{
				"mode":            "Terminate",
				"certificateRefs": []any{map[string]any{"group": "", "kind": "Secret", "name": "org-wildcard-tls-acme-omani-homes"}},
			},
		},
		map[string]any{
			"name": "console-http-acme", "hostname": "*.acme.omani.homes",
			"port": int64(80), "protocol": "HTTP", "allowedRoutes": map[string]any{"namespaces": map[string]any{"from": "All"}},
		},
	}
}

// The up-path of a pool Organization writes both listeners with the selector
// of its own namespace (acme) and of its console route (catalyst-system), and
// leaves the apex pair as it found it.
func TestReconcileTenantConsoleTLS_NET191_PairAdmitsOnlyItsOrganization(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	r := consoleTLSReconciler(t, org, apexPair())

	if _, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil {
		t.Fatalf("reconcileTenantConsoleTLS: %v", err)
	}

	got := listenerAdmissions(t, r)
	want := net191Admission("acme", "catalyst-system")
	for _, n := range []string{"console-https-acme", "console-http-acme"} {
		if got[n] != want {
			t.Errorf("%s allowedRoutes:\n got %s\nwant %s", n, got[n], want)
		}
	}
	for _, n := range []string{consoleApexListenerHTTPSName, consoleApexListenerHTTPName} {
		if got[n] != "absent" {
			t.Errorf("apex listener %s must be left as found (no allowedRoutes), got %s", n, got[n])
		}
	}
	if len(got) != 4 {
		t.Errorf("spec.listeners: got %d %v, want 4", len(got), got)
	}

	// The second value is the namespace the console route of this
	// Organization is written to: the listener admits exactly that route.
	ns, _, ok := r.tenantRouteNameNS(org)
	if !ok || ns != "catalyst-system" {
		t.Errorf("console route namespace: got %q (ok=%v), want catalyst-system", ns, ok)
	}
}

// A pair that admits every namespace (the live state of every Organization
// provisioned before NET-191) is drift: it is repaired in place, and the next
// pass is a no-op.
func TestEnsureConsoleOrgListener_NET191_RepairsAdmitAllInPlace(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	r := consoleTLSReconciler(t, org, append(apexPair(), admitAllPair()...))

	before := listenerAdmissions(t, r)
	if before["console-https-acme"] != `{"namespaces":{"from":"All"}}` || before["console-http-acme"] != `{"namespaces":{"from":"All"}}` {
		t.Fatalf("fixture must start with the pair admitting every namespace: %v", before)
	}

	changed, err := r.ensureConsoleOrgListener(context.Background(), r.Client, orgConsoleTLSNamesFor("acme", "omani.homes"))
	if err != nil {
		t.Fatalf("ensureConsoleOrgListener: %v", err)
	}
	if !changed {
		t.Fatalf("a pair that admits every namespace must be reported as changed")
	}

	after := listenerAdmissions(t, r)
	want := net191Admission("acme", "catalyst-system")
	for _, n := range []string{"console-https-acme", "console-http-acme"} {
		if after[n] != want {
			t.Errorf("%s allowedRoutes after repair:\n got %s\nwant %s", n, after[n], want)
		}
	}
	if len(after) != 4 {
		t.Errorf("repair must be in place, not an append: got %d listeners %v, want 4", len(after), after)
	}

	changed, err = r.ensureConsoleOrgListener(context.Background(), r.Client, orgConsoleTLSNamesFor("acme", "omani.homes"))
	if err != nil {
		t.Fatalf("ensureConsoleOrgListener pass 2: %v", err)
	}
	if changed {
		t.Errorf("pass 2 must be a no-op once the selector is in place, got changed=true")
	}
}

// The second value follows the configured console route namespace, so the
// listener keeps admitting the console route wherever the controller is told
// to write it.
func TestEnsureConsoleOrgListener_NET191_FollowsConsoleRouteNamespace(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	r := consoleTLSReconciler(t, org, apexPair())
	r.ConsoleRouteNamespace = "console-routes"

	if _, err := r.ensureConsoleOrgListener(context.Background(), r.Client, orgConsoleTLSNamesFor("acme", "omani.homes")); err != nil {
		t.Fatalf("ensureConsoleOrgListener: %v", err)
	}
	got := listenerAdmissions(t, r)
	want := net191Admission("acme", "console-routes")
	for _, n := range []string{"console-https-acme", "console-http-acme"} {
		if got[n] != want {
			t.Errorf("%s allowedRoutes:\n got %s\nwant %s", n, got[n], want)
		}
	}
	ns, _, ok := r.tenantRouteNameNS(org)
	if !ok || ns != "console-routes" {
		t.Errorf("console route namespace: got %q (ok=%v), want console-routes", ns, ok)
	}
}
