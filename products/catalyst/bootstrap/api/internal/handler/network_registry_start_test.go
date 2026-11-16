package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// These tests use only what main already has (the cache, SetK8sCache, the
// state file of the adoption step), so they run unchanged against main and
// fail there: on main nothing writes the registry after the hand-over.

var registryCM = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

const (
	registryDoc  = `{"schema":"openova.io/network-registry/v1","regions":[{"key":"region-a"},{"key":"region-b"}]}`
	registryGet  = "get configmaps flux-system/sovereign-network-registry"
	registryPtch = "patch configmaps flux-system/sovereign-network-registry"
)

// registryRevision is the revision the parent computes for doc.
func registryRevision(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return hex.EncodeToString(sum[:])[:12]
}

// registryConfigMap is the ConfigMap a region holds, carrying doc.
func registryConfigMap(doc string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "sovereign-network-registry", "namespace": "flux-system"},
		"data":     map[string]any{"NETWORK_REGISTRY_JSON": doc, "NETWORK_REGISTRY_REVISION": registryRevision(doc)},
	}}
}

// registryRegion is a fake region whose server-side apply creates an absent
// ConfigMap and replaces a present one. objs is what the region holds.
func registryRegion(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	c := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	c.PrependReactor("patch", "configmaps", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchAction)
		if p.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(p.GetPatch(), &obj.Object); err != nil {
			return true, nil, err
		}
		if _, err := c.Tracker().Get(a.GetResource(), a.GetNamespace(), p.GetName()); err != nil {
			return true, obj, c.Tracker().Create(a.GetResource(), obj, a.GetNamespace())
		}
		return true, obj, c.Tracker().Update(a.GetResource(), obj, a.GetNamespace())
	})
	return c
}

// regionCalls lists the calls a region received, "<verb> <resource> <namespace>/<name>".
func regionCalls(c *dynamicfake.FakeDynamicClient) []string {
	var out []string
	for _, a := range c.Actions() {
		name := ""
		// A patch carries a name too: PatchAction is a GetAction.
		if g, ok := a.(k8stesting.GetAction); ok {
			name = g.GetName()
		}
		out = append(out, a.GetVerb()+" "+a.GetResource().Resource+" "+a.GetNamespace()+"/"+name)
	}
	return out
}

// heldRevision is the revision the region's ConfigMap carries, "" when absent.
func heldRevision(c *dynamicfake.FakeDynamicClient) string {
	obj, err := c.Tracker().Get(registryCM, "flux-system", "sovereign-network-registry")
	if err != nil {
		return ""
	}
	rev, _, _ := unstructured.NestedString(obj.(*unstructured.Unstructured).Object, "data", "NETWORK_REGISTRY_REVISION")
	return rev
}

// registryCache is a started-free cluster cache over the given fake regions.
func registryCache(t *testing.T, regions map[string]*dynamicfake.FakeDynamicClient) *k8scache.Factory {
	t.Helper()
	refs := make([]k8scache.ClusterRef, 0, len(regions))
	for id, c := range regions {
		refs = append(refs, k8scache.ClusterRef{ID: id, DynamicClient: c})
	}
	f, err := k8scache.NewFactory(k8scache.Config{Logger: quietHandlerLogger(), Registry: k8scache.NewRegistry(), Clusters: refs})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

// writeRegistryState writes the OpenTofu state of depID, holding doc as the
// output network_registry, where the adoption step reads it.
func writeRegistryState(t *testing.T, workDir, depID, doc string) {
	t.Helper()
	value, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, depID), 0o750); err != nil {
		t.Fatal(err)
	}
	st := `{"version":4,"outputs":{"network_registry":{"value":` + string(value) + `,"type":"string"}},"resources":[]}`
	if err := os.WriteFile(filepath.Join(workDir, depID, "terraform.tfstate"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitForRevision waits until the region holds rev.
func waitForRevision(t *testing.T, id string, c *dynamicfake.FakeDynamicClient, rev string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for heldRevision(c) != rev {
		if time.Now().After(deadline) {
			t.Fatalf("%s: revision %q after 5s, want %q; calls %q", id, heldRevision(c), rev, regionCalls(c))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestParentStartReconcilesNetworkRegistry: when the parent starts with its
// records and its cluster cache (SetK8sCache), every ready deployment gets
// the registry in every one of its regions — written where it is absent or
// another, only read where it is current — and a deployment that is not
// ready, or a cluster of no deployment, gets no call at all.
//
// The pass takes the ready records in id order, one after the other, so when
// the last region of "bbbb…" holds the registry, "aaaa…" has been judged and
// "0000…" (not ready) would already have been reached.
func TestParentStartReconcilesNetworkRegistry(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	workDir := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", workDir)
	rev := registryRevision(registryDoc)

	const current, stale, notReady = "aaaa000000000001", "bbbb000000000002", "0000000000000003"
	regions := map[string]*dynamicfake.FakeDynamicClient{
		current:               registryRegion(registryConfigMap(registryDoc)),          // same revision
		stale:                 registryRegion(),                                        // absent
		stale + "-region-b-1": registryRegion(registryConfigMap(`{"schema":"older"}`)), // another revision
		notReady:              registryRegion(),
		"ffff000000000009":    registryRegion(), // a cluster of no record
	}
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	for id, status := range map[string]string{current: "ready", stale: "ready", notReady: "phase1-watching"} {
		writeRegistryState(t, workDir, id, registryDoc)
		h.deployments.Store(id, &Deployment{ID: id, Status: status})
	}

	h.SetK8sCache(registryCache(t, regions), k8scache.NewSARCache(), "")

	waitForRevision(t, stale, regions[stale], rev)
	waitForRevision(t, stale+"-region-b-1", regions[stale+"-region-b-1"], rev)
	for id, want := range map[string][]string{
		current:               {registryGet},
		stale:                 {registryGet, registryPtch},
		stale + "-region-b-1": {registryGet, registryPtch},
		notReady:              nil,
		"ffff000000000009":    nil,
	} {
		if got := regionCalls(regions[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: calls %q, want %q", id, got, want)
		}
	}
	if heldRevision(regions[current]) != rev || heldRevision(regions[notReady]) != "" {
		t.Errorf("current holds %q, not-ready holds %q", heldRevision(regions[current]), heldRevision(regions[notReady]))
	}
}
