// tenant_console_tls_net195_test.go — NET-195. A host an Organization lists
// in spec.tenantPublic.passthroughHosts gets a TLS passthrough listener on the
// console Gateway (PRD-050, outcome B): the exact host, on the port of the
// Organization's HTTPS listener, admitting TLSRoutes of the Organization only.
package controller

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// net195Listener is the passthrough listener of api.acme.omani.homes on the
// apex HTTPS port of apexPair (443), written out in full.
const net195Listener = `{"allowedRoutes":{"kinds":[{"kind":"TLSRoute"}],"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
	`{"key":"kubernetes.io/metadata.name","operator":"In","values":["acme","catalyst-system"]}]}}},` +
	`"hostname":"api.acme.omani.homes","name":"console-tls-acme-api","port":443,"protocol":"TLS","tls":{"mode":"Passthrough"}}`

// net195Listeners returns the console Gateway's spec listeners by name, as JSON.
func net195Listeners(t *testing.T, r *Reconciler) (map[string]string, []string) {
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
	byName, order := map[string]string{}, []string{}
	for _, l := range raw {
		m := l.(map[string]any)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal listener: %v", err)
		}
		n, _ := m["name"].(string)
		byName[n] = string(b)
		order = append(order, n)
	}
	return byName, order
}

// The up-path writes the passthrough listener of a listed host beside the
// pair, leaves the listener of another Organization whose slug starts with
// this one's as it is, removes the listener when the host is no longer
// listed, and the teardown removes it with the pair.
func TestReconcileTenantConsoleTLS_NET195_PassthroughListenerOfAListedHost(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	org.Spec.TenantPublic.PassthroughHosts = []string{"api", "console"}
	other := map[string]any{
		"name": "console-tls-acme-x-api", "hostname": "api.acme-x.omani.homes",
		"port": int64(443), "protocol": "TLS", "tls": map[string]any{"mode": "Passthrough"},
	}
	r := consoleTLSReconciler(t, org, append(apexPair(), other))

	if _, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil {
		t.Fatalf("reconcileTenantConsoleTLS: %v", err)
	}
	got, order := net195Listeners(t, r)
	if got["console-tls-acme-api"] != net195Listener {
		t.Errorf("passthrough listener:\n got %s\nwant %s", got["console-tls-acme-api"], net195Listener)
	}
	if len(order) != 6 {
		t.Errorf("listeners: got %v, want the apex pair, acme-x's listener, acme's pair and console-tls-acme-api (no listener for console)", order)
	}
	if changed, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil || changed {
		t.Errorf("a second pass must change nothing: changed=%v err=%v", changed, err)
	}

	org.Spec.TenantPublic.PassthroughHosts = nil
	if _, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil {
		t.Fatalf("reconcileTenantConsoleTLS without hosts: %v", err)
	}
	got, order = net195Listeners(t, r)
	if _, still := got["console-tls-acme-api"]; still || len(order) != 5 {
		t.Errorf("a host no longer listed must lose its listener, and only it: %v", order)
	}

	org.Spec.TenantPublic.PassthroughHosts = []string{"api"}
	if _, err := r.reconcileTenantConsoleTLS(context.Background(), org); err != nil {
		t.Fatalf("reconcileTenantConsoleTLS again: %v", err)
	}
	if _, err := r.teardownTenantConsoleTLS(context.Background(), org); err != nil {
		t.Fatalf("teardownTenantConsoleTLS: %v", err)
	}
	got, order = net195Listeners(t, r)
	if len(order) != 3 || got["console-tls-acme-x-api"] == "" {
		t.Errorf("teardown must leave the apex pair and acme-x's listener only: %v", order)
	}
}
