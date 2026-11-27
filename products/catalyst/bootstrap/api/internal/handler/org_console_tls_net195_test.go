// org_console_tls_net195_test.go — NET-195. A host an Organization lists in
// spec.tenantPublic.passthroughHosts gets a TLS passthrough listener on the
// console Gateway of every region (PRD-050, outcome B), written by the CR
// reconcile (internal/orglisteners).

package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// net195Listener is the passthrough listener of api.uatco.omani.homes on the
// apex HTTPS port the fakes seed (8443), written out in full.
const net195Listener = `{"allowedRoutes":{"kinds":[{"kind":"TLSRoute"}],"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
	`{"key":"kubernetes.io/metadata.name","operator":"In","values":["uatco","catalyst-system"]}]}}},` +
	`"hostname":"api.uatco.omani.homes","name":"console-tls-uatco-api","port":8443,"protocol":"TLS","tls":{"mode":"Passthrough"}}`

// net195GatewayListeners reads the console Gateway one region holds and
// returns its listeners by name, as JSON, and the names of its passthrough
// listeners.
func net195GatewayListeners(t *testing.T, dyn *dynamicfake.FakeDynamicClient) (map[string]string, []string) {
	t.Helper()
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(context.Background(), consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	raw, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners: %v", err)
	}
	byName, passthrough := map[string]string{}, []string{}
	for _, l := range raw {
		m := l.(map[string]any)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal listener: %v", err)
		}
		n, _ := m["name"].(string)
		byName[n] = string(b)
		if strings.HasPrefix(n, "console-tls-") {
			passthrough = append(passthrough, n)
		}
	}
	return byName, passthrough
}

// One reconcile pass writes the listener of a listed host into both regions
// and none for the console's host; once the CR stops listing the host, the
// next pass removes it from both.
func TestReconcileOrgConsoleTLSFromOrgCRs_NET195_PassthroughListenerInEveryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works") // chroot gate for the fan-out
	org := funnelOrgCR("uatco", "omani.homes")
	if err := unstructured.SetNestedStringSlice(org.Object, []string{"api", "console"}, "spec", "tenantPublic", "passthroughHosts"); err != nil {
		t.Fatal(err)
	}
	hostDyn := fakeDynForOrgConsoleReconcile(t, org)
	secDyn := fakeDynForOrgConsoleReconcile(t)
	h := newReconcileHandler(t, hostDyn, secDyn,
		k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes")), k8sfake.NewSimpleClientset())
	regions := map[string]*dynamicfake.FakeDynamicClient{"region-a-host": hostDyn, "region-b-secondary": secDyn}

	ctx := context.Background()
	h.reconcileOrgConsoleTLSOnce(ctx)
	for region, dyn := range regions {
		got, passthrough := net195GatewayListeners(t, dyn)
		if got["console-tls-uatco-api"] != net195Listener {
			t.Errorf("%s passthrough listener:\n got %s\nwant %s", region, got["console-tls-uatco-api"], net195Listener)
		}
		if len(passthrough) != 1 {
			t.Errorf("%s passthrough listeners: got %v, want only console-tls-uatco-api", region, passthrough)
		}
	}

	cr, err := hostDyn.Resource(organizationGVR()).Get(ctx, "uatco", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Organization: %v", err)
	}
	unstructured.RemoveNestedField(cr.Object, "spec", "tenantPublic", "passthroughHosts")
	if _, err := hostDyn.Resource(organizationGVR()).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update Organization: %v", err)
	}
	h.reconcileOrgConsoleTLSOnce(ctx)
	for region, dyn := range regions {
		if _, passthrough := net195GatewayListeners(t, dyn); len(passthrough) != 0 {
			t.Errorf("%s: a host no longer listed must lose its listener, got %v", region, passthrough)
		}
	}
}
