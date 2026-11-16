package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

const wireDoc = `{"schema":"openova.io/network-registry/v1","regions":[{"key":"region-a"},{"key":"region-b"}]}`

// wireRegion is a fake region whose server-side apply creates an absent
// ConfigMap and replaces a present one.
func wireRegion() *dynamicfake.FakeDynamicClient {
	c := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
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

// wireRevision is the revision the region's ConfigMap carries, "" when absent.
func wireRevision(c *dynamicfake.FakeDynamicClient) string {
	obj, err := c.Tracker().Get(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "flux-system", "sovereign-network-registry")
	if err != nil {
		return ""
	}
	rev, _, _ := unstructured.NestedString(obj.(*unstructured.Unstructured).Object, "data", "NETWORK_REGISTRY_REVISION")
	return rev
}

// TestSetK8sCacheStartsTheNetworkRegistryPass: the start-up line of main
// that wires the cluster cache into the handler also starts the registry's
// start pass over the parent's deployment store: the ready record gets the
// registry in both its regions, the record that is not ready gets no call.
func TestSetK8sCacheStartsTheNetworkRegistryPass(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	depDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("CATALYST_DEPLOYMENTS_DIR", depDir)
	t.Setenv("CATALYST_TOFU_WORKDIR", stateDir)
	st, err := store.New(depDir)
	if err != nil {
		t.Fatal(err)
	}
	const ready, notReady = "aaaa000000000001", "bbbb000000000002"
	value, _ := json.Marshal(wireDoc)
	for id, status := range map[string]string{ready: "ready", notReady: "phase1-watching"} {
		if err := st.Save(store.Record{ID: id, Status: status}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stateDir, id), 0o750); err != nil {
			t.Fatal(err)
		}
		state := `{"version":4,"outputs":{"network_registry":{"value":` + string(value) + `,"type":"string"}},"resources":[]}`
		if err := os.WriteFile(filepath.Join(stateDir, id, "terraform.tfstate"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	regions := map[string]*dynamicfake.FakeDynamicClient{ready: wireRegion(), ready + "-region-b-1": wireRegion(), notReady: wireRegion()}
	refs := make([]k8scache.ClusterRef, 0, len(regions))
	for id, c := range regions {
		refs = append(refs, k8scache.ClusterRef{ID: id, DynamicClient: c})
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f, err := k8scache.NewFactory(k8scache.Config{Logger: log, Registry: k8scache.NewRegistry(), Clusters: refs})
	if err != nil {
		t.Fatal(err)
	}
	h := &handler.Handler{}

	setK8sCache(log, h, f, k8scache.NewSARCache(), "")

	if h.K8sCache() != f {
		t.Fatal("the cache is not wired into the handler")
	}
	sum := sha256.Sum256([]byte(wireDoc))
	rev := hex.EncodeToString(sum[:])[:12]
	deadline := time.Now().Add(5 * time.Second)
	for wireRevision(regions[ready]) != rev || wireRevision(regions[ready+"-region-b-1"]) != rev {
		if time.Now().After(deadline) {
			t.Fatalf("after 5s: %s holds %q, %s holds %q, want %q", ready, wireRevision(regions[ready]),
				ready+"-region-b-1", wireRevision(regions[ready+"-region-b-1"]), rev)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := regions[notReady].Actions(); len(got) != 0 {
		t.Errorf("the record that is not ready got calls: %v", got)
	}
}

// TestStoreRecordsReadTheDeploymentStore: the registry reads id, status and
// owner of the store's records, and an unknown id is os.ErrNotExist.
func TestStoreRecordsReadTheDeploymentStore(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(store.Record{ID: "aaaa000000000001", Status: "ready", OwnerEmail: "owner@example.org"}); err != nil {
		t.Fatal(err)
	}
	recs := storeRecords{st: st}
	rec, err := recs.Load("aaaa000000000001")
	if err != nil || rec.ID != "aaaa000000000001" || rec.Status != "ready" || rec.OwnerEmail != "owner@example.org" {
		t.Fatalf("Load: %+v, %v", rec, err)
	}
	if _, err := recs.Load("ffff000000000009"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unknown id: %v", err)
	}
	all, err := recs.LoadAll()
	if err != nil || len(all) != 1 || all[0] != rec {
		t.Errorf("LoadAll: %+v, %v", all, err)
	}
}
