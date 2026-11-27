package orglisteners

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// wantUatcoAPI is the passthrough listener of api.uatco.omani.homes on the apex
// HTTPS port the fixture seeds (9443), written out in full.
const wantUatcoAPI = `{"allowedRoutes":{"kinds":[{"kind":"TLSRoute"}],"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
	`{"key":"kubernetes.io/metadata.name","operator":"In","values":["uatco","catalyst-system"]}]}}},` +
	`"hostname":"api.uatco.omani.homes","name":"console-tls-uatco-api","port":9443,"protocol":"TLS","tls":{"mode":"Passthrough"}}`

func seeded(t *testing.T, listeners ...any) (Gateway, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gatewayGVR: "GatewayList"})
	gw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
		"metadata": map[string]any{"name": "cilium-gateway-console", "namespace": "kube-system"},
		"spec":     map[string]any{"gatewayClassName": "cilium", "listeners": listeners},
	}}
	if _, err := dyn.Resource(gatewayGVR).Namespace("kube-system").Create(context.Background(), gw, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed Gateway: %v", err)
	}
	return Gateway{Dyn: dyn, Namespace: "kube-system", Name: "cilium-gateway-console"}, dyn
}

// listeners returns the Gateway's listeners by name, as JSON, and their names in order.
func listeners(t *testing.T, g Gateway) (map[string]string, []string) {
	t.Helper()
	gw, err := g.Dyn.Resource(gatewayGVR).Namespace(g.Namespace).Get(context.Background(), g.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	raw, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	byName, order := map[string]string{}, []string{}
	for _, l := range raw {
		b, _ := json.Marshal(l)
		n, _ := l.(map[string]any)["name"].(string)
		byName[n], order = string(b), append(order, n)
	}
	return byName, order
}

// writeBack writes a merged Gateway back, as the handler's
// updateConsoleGateway does: this package returns what to write and writes
// nothing itself.
func writeBack(t *testing.T, g Gateway, obj *unstructured.Unstructured) {
	t.Helper()
	if _, err := g.Dyn.Resource(gatewayGVR).Namespace(g.Namespace).Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update Gateway: %v", err)
	}
}

// ensure runs EnsurePassthrough and writes back every Gateway it changed.
func ensure(t *testing.T, org *unstructured.Unstructured, slug, zone, ns string, gws []Gateway) error {
	t.Helper()
	changes, err := EnsurePassthrough(context.Background(), org, slug, zone, ns, gws)
	for _, c := range changes {
		writeBack(t, c.Gateway, c.Object)
	}
	return err
}

// removeOrg runs RemoveOrg, writes back the Gateway it changed and reports
// whether it changed one.
func removeOrg(t *testing.T, g Gateway, slug string) (bool, error) {
	t.Helper()
	obj, err := g.RemoveOrg(context.Background(), slug)
	if obj != nil {
		writeBack(t, g, obj)
	}
	return obj != nil, err
}

func updates(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "update" {
			n++
		}
	}
	return n
}

// org is an Organization CR with passthroughHosts set to hosts, or without the
// field when hosts is nil.
func org(hosts any) *unstructured.Unstructured {
	tp := map[string]any{"parentDomain": "omani.homes"}
	if hosts != nil {
		tp["passthroughHosts"] = hosts
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"slug": "uatco", "tenantPublic": tp}}}
}

// The listed host gets its listener on the apex HTTPS port and the console's
// host none; a second pass and an unreadable host list write nothing.
func TestEnsurePassthrough_NET195_WritesListedHostsOnce(t *testing.T) {
	g, dyn := seeded(t, map[string]any{"name": "console-https", "port": int64(9443), "protocol": "HTTPS"})
	if err := ensure(t, org([]any{"api", "console"}), "uatco", "uatco.omani.homes", "catalyst-system", []Gateway{g}); err != nil {
		t.Fatalf("EnsurePassthrough: %v", err)
	}
	got, order := listeners(t, g)
	if got["console-tls-uatco-api"] != wantUatcoAPI || len(order) != 2 || updates(dyn) != 1 {
		t.Fatalf("after pass 1 (%d updates): %v\n got %s\nwant %s", updates(dyn), order, got["console-tls-uatco-api"], wantUatcoAPI)
	}
	if err := ensure(t, org([]any{"api"}), "uatco", "uatco.omani.homes", "catalyst-system", []Gateway{g}); err != nil || updates(dyn) != 1 {
		t.Errorf("pass 2 must not write: err=%v, %d updates", err, updates(dyn))
	}
	if err := ensure(t, org("api"), "uatco", "uatco.omani.homes", "catalyst-system", []Gateway{g}); err == nil || updates(dyn) != 1 {
		t.Errorf("an unreadable host list must be an error and write nothing: err=%v, %d updates", err, updates(dyn))
	}
	if err := ensure(t, org(nil), "uatco", "uatco.omani.homes", "catalyst-system", []Gateway{g}); err != nil {
		t.Fatalf("EnsurePassthrough without hosts: %v", err)
	}
	if _, order := listeners(t, g); strings.Join(order, ",") != "console-https" {
		t.Errorf("a host no longer listed must lose its listener: %v", order)
	}
}

// RemoveOrg strips the pair and the passthrough listeners of the Organization,
// and keeps those of an Organization whose slug starts with its own.
func TestRemoveOrg_NET195_StripsPairAndPassthroughListeners(t *testing.T) {
	tls := func(name string) map[string]any { return map[string]any{"name": name, "protocol": "TLS"} }
	g, _ := seeded(t,
		map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS"},
		map[string]any{"name": "console-https-gone", "protocol": "HTTPS"},
		map[string]any{"name": "console-http-gone", "protocol": "HTTP"},
		tls("console-tls-gone-api"), tls("console-tls-gone-x-api"))
	removed, err := removeOrg(t, g, "gone")
	if err != nil || !removed {
		t.Fatalf("RemoveOrg: removed=%v err=%v", removed, err)
	}
	if _, order := listeners(t, g); strings.Join(order, ",") != "console-https,console-tls-gone-x-api" {
		t.Errorf("left after RemoveOrg: %v", order)
	}
	if removed, err := removeOrg(t, g, "gone"); err != nil || removed {
		t.Errorf("a second RemoveOrg must change nothing: removed=%v err=%v", removed, err)
	}
}
