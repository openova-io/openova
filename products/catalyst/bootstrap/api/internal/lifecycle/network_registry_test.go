package lifecycle

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// registry is the module output of a two-region Sovereign, as the state holds
// it: scripts/tests/check-network-registry/valid-two-regions.json, encoded
// the way jsonencode writes it.
func registry(t *testing.T) string {
	b, err := os.ReadFile(filepath.Clean("../../../../../../scripts/tests/check-network-registry/valid-two-regions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(b)), "")
}

func state(t *testing.T, outputs string) []byte {
	return []byte(`{"version":4,"terraform_version":"1.11.6","outputs":{` + outputs + `},"resources":[]}`)
}

func quoted(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// TestNetworkRegistryFromState: the output's value is returned as the module
// wrote it; a state without the output, or with a value that is no JSON
// object, is refused.
func TestNetworkRegistryFromState(t *testing.T) {
	doc := registry(t)
	got, err := NetworkRegistryFromState(state(t, `"network_registry":{"value":`+quoted(doc)+`,"type":"string"},"load_balancer_ip":{"value":"192.0.2.1","type":"string"}`))
	if err != nil || got != doc {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := NetworkRegistryFromState(state(t, `"load_balancer_ip":{"value":"192.0.2.1","type":"string"}`)); !errors.Is(err, ErrNoNetworkRegistry) {
		t.Fatalf("no output: %v", err)
	}
	for _, bad := range []string{`{"value":["a"],"type":"list"}`, `{"value":"[1]","type":"string"}`, `{"value":"{","type":"string"}`} {
		if _, err := NetworkRegistryFromState(state(t, `"network_registry":`+bad)); err == nil || errors.Is(err, ErrNoNetworkRegistry) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := NetworkRegistryFromState([]byte("not json")); err == nil {
		t.Error("a broken state is read")
	}
}

// TestNetworkRegistryConfigMap: the ConfigMap's name, namespace, keys and
// revision; another document gives another revision.
func TestNetworkRegistryConfigMap(t *testing.T) {
	cm := NetworkRegistryConfigMap(`{"mtu":1420}`)
	want := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "sovereign-network-registry", "namespace": "flux-system",
			"labels": map[string]any{"app.kubernetes.io/managed-by": "catalyst-api"}},
		// sha256 of {"mtu":1420} is 335793e55eff...
		"data": map[string]any{"NETWORK_REGISTRY_JSON": `{"mtu":1420}`, "NETWORK_REGISTRY_REVISION": "335793e55eff"}}
	if !reflect.DeepEqual(cm.Object, want) {
		t.Fatalf("ConfigMap %v", cm.Object)
	}
	if NetworkRegistryConfigMap(`{"mtu":1400}`).Object["data"].(map[string]any)["NETWORK_REGISTRY_REVISION"] == "335793e55eff" {
		t.Fatal("two documents have one revision")
	}
}

// fakeClusters is a cluster cache with the given cluster ids; a nil client
// stands for each, and an id in broken has no client.
type fakeClusters struct {
	ids    []string
	broken map[string]bool
}

func (f *fakeClusters) Clusters() []string { return f.ids }

func (f *fakeClusters) DynamicClientFor(id string) (dynamic.Interface, error) {
	if f.broken[id] {
		return nil, errors.New("not registered")
	}
	return nil, nil
}

// TestDeliverNetworkRegistry: every cluster of the deployment gets the
// ConfigMap, the primary first; clusters of other deployments are left
// alone; a failing cluster does not stop the next one.
func TestDeliverNetworkRegistry(t *testing.T) {
	doc := registry(t)
	clusters := &fakeClusters{ids: []string{"0123456789abcdef-region-c-2", "fedcba9876543210", "0123456789abcdef-region-b-1",
		"0123456789abcdef", "0123456789abcdef-", "0123456789abcdef-region-d-3"}, broken: map[string]bool{"0123456789abcdef-region-d-3": true}}
	calls := 0
	apply := func(_ context.Context, _ dynamic.Interface, cm *unstructured.Unstructured) error {
		calls++
		if cm.GetNamespace() != "flux-system" || cm.Object["data"].(map[string]any)["NETWORK_REGISTRY_JSON"] != doc {
			t.Errorf("ConfigMap %v", cm.Object)
		}
		if calls == 2 {
			return errors.New("connection refused")
		}
		return nil
	}
	rev := NetworkRegistryConfigMap(doc).Object["data"].(map[string]any)["NETWORK_REGISTRY_REVISION"].(string)
	got := DeliverNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`,"type":"string"}`), "0123456789abcdef", clusters, apply)
	want := []string{"0123456789abcdef: revision " + rev, "0123456789abcdef-region-b-1: connection refused",
		"0123456789abcdef-region-c-2: revision " + rev, "0123456789abcdef-region-d-3: not registered"}
	if !reflect.DeepEqual(got, want) || calls != 3 {
		t.Fatalf("lines %q, %d applies", got, calls)
	}
	if got := DeliverNetworkRegistry(context.Background(), state(t, ""), "0123456789abcdef", clusters, apply); len(got) != 1 ||
		got[0] != "not delivered: "+ErrNoNetworkRegistry.Error() {
		t.Fatalf("no output: %q", got)
	}
	if got := DeliverNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`}`), "1111111111111111", clusters, apply); len(got) != 1 ||
		!strings.HasPrefix(got[0], "not delivered: the cluster cache holds no cluster of deployment 1111111111111111") {
		t.Fatalf("no cluster: %q", got)
	}
	var none *fakeClusters
	if got := DeliverNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`}`), "0123456789abcdef", none, apply); len(got) != 1 ||
		got[0] != "not delivered: the parent has no cluster cache" {
		t.Fatalf("no cache: %q", got)
	}
}

// TestApplyConfigMap: the write is a server-side apply of the ConfigMap in
// its namespace, under the field manager of the delivery, taking over fields.
func TestApplyConfigMap(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery + " " + r.Header.Get("Content-Type") + " " + string(body)
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"sovereign-network-registry","namespace":"flux-system"}}`))
	}))
	defer srv.Close()
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyConfigMap(context.Background(), dyn, NetworkRegistryConfigMap(`{"mtu":1420}`)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PATCH /api/v1/namespaces/flux-system/configmaps/sovereign-network-registry?",
		"fieldManager=catalyst-api%2Fnetwork-registry", "force=true", " application/apply-patch+yaml ", `"NETWORK_REGISTRY_REVISION":"335793e55eff"`} {
		if !strings.Contains(got, want) {
			t.Errorf("request %q lacks %q", got, want)
		}
	}
}
