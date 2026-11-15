package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// TestDeliverNetworkRegistry: every region's kubeconfig of the deployment
// gets the ConfigMap, the primary first; files of other deployments and
// sidecars are not read; a failing region does not stop the next one.
func TestDeliverNetworkRegistry(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"0123456789abcdef.yaml", "0123456789abcdef-region-b-1.yaml", "0123456789abcdef-region-c-2.yaml",
		"0123456789abcdef-region-b-1.peer", "0123456789abcdef-region-b-1.nodeip", "fedcba9876543210.yaml", "0123456789abcdef-.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("kubeconfig of "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	doc := registry(t)
	var seen []string
	apply := func(_ context.Context, kc []byte, cm *unstructured.Unstructured) error {
		seen = append(seen, string(kc))
		if cm.GetName() != "sovereign-network-registry" || cm.Object["data"].(map[string]any)["NETWORK_REGISTRY_JSON"] != doc {
			t.Errorf("ConfigMap %v", cm.Object)
		}
		if strings.Contains(string(kc), "region-c") {
			return errors.New("connection refused")
		}
		return nil
	}
	rev := NetworkRegistryConfigMap(doc).Object["data"].(map[string]any)["NETWORK_REGISTRY_REVISION"].(string)
	got := DeliverNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`,"type":"string"}`), dir, "0123456789abcdef", apply)
	want := []string{"0123456789abcdef.yaml: revision " + rev, "0123456789abcdef-region-b-1.yaml: revision " + rev,
		"0123456789abcdef-region-c-2.yaml: connection refused"}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(seen, []string{"kubeconfig of 0123456789abcdef.yaml",
		"kubeconfig of 0123456789abcdef-region-b-1.yaml", "kubeconfig of 0123456789abcdef-region-c-2.yaml"}) {
		t.Fatalf("lines %q, read %q", got, seen)
	}
	if got := DeliverNetworkRegistry(context.Background(), state(t, ""), dir, "0123456789abcdef", apply); len(got) != 1 ||
		got[0] != "not delivered: "+ErrNoNetworkRegistry.Error() {
		t.Fatalf("no output: %q", got)
	}
	if got := DeliverNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`}`), dir, "1111111111111111", apply); len(got) != 1 ||
		!strings.HasPrefix(got[0], "not delivered: no kubeconfig of deployment 1111111111111111") {
		t.Fatalf("no kubeconfig: %q", got)
	}
}
