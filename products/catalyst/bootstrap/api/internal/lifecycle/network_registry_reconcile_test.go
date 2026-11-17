package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

// applyingClient is a fake region: a dynamic client whose server-side apply
// creates an absent object and replaces a present one, as an apiserver does
// for an apply that owns every field. objs is what the region holds.
func applyingClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
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

// regions is a cluster cache of fake regions; an id in broken has no client.
type regions struct {
	clients map[string]*dynamicfake.FakeDynamicClient
	broken  map[string]bool
}

func (r *regions) Clusters() []string {
	out := make([]string, 0, len(r.clients)+len(r.broken))
	for id := range r.clients {
		out = append(out, id)
	}
	for id := range r.broken {
		out = append(out, id)
	}
	return out
}

func (r *regions) DynamicClientFor(id string) (dynamic.Interface, error) {
	if c, ok := r.clients[id]; ok {
		return c, nil
	}
	return nil, errors.New("not registered")
}

// calls lists the calls a region received, "<verb> <resource> <namespace>/<name>".
func calls(c *dynamicfake.FakeDynamicClient) []string {
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

// held returns the registry document and revision the region's ConfigMap carries.
func held(t *testing.T, c *dynamicfake.FakeDynamicClient) (string, string) {
	t.Helper()
	obj, err := c.Tracker().Get(configMapsGVR, NetworkRegistryNamespace, NetworkRegistryName)
	if err != nil {
		t.Fatalf("the region holds no ConfigMap: %v", err)
	}
	u := obj.(*unstructured.Unstructured)
	doc, _, _ := unstructured.NestedString(u.Object, "data", NetworkRegistryKey)
	rev, _, _ := unstructured.NestedString(u.Object, "data", NetworkRegistryRevisionKey)
	return doc, rev
}

const (
	getCall   = "get configmaps flux-system/sovereign-network-registry"
	patchCall = "patch configmaps flux-system/sovereign-network-registry"
)

// TestReconcileNetworkRegistry: in every region of the deployment the
// ConfigMap is read first; it is written where it is absent or carries
// another document, and left alone where it carries this one. A region that
// fails does not stop the next; regions of another deployment get no call.
func TestReconcileNetworkRegistry(t *testing.T) {
	doc := registry(t)
	st := state(t, `"network_registry":{"value":`+quoted(doc)+`,"type":"string"}`)
	want := NetworkRegistryConfigMap(doc)
	rev := want.Object["data"].(map[string]any)[NetworkRegistryRevisionKey].(string)
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{
		"0123456789abcdef":            applyingClient(),                                         // absent
		"0123456789abcdef-region-b-1": applyingClient(NetworkRegistryConfigMap(`{"mtu":1400}`)), // another document
		"0123456789abcdef-region-c-2": applyingClient(want.DeepCopy()),                          // this document
		"fedcba9876543210":            applyingClient(),                                         // another deployment
	}, broken: map[string]bool{"0123456789abcdef-region-d-3": true}}

	gotRev, got, err := ReconcileNetworkRegistry(context.Background(), st, "0123456789abcdef", cache)
	if err != nil || gotRev != rev {
		t.Fatalf("revision %q, %v", gotRev, err)
	}
	wantResults := []RegionResult{
		{Cluster: "0123456789abcdef", Written: true},
		{Cluster: "0123456789abcdef-region-b-1", Written: true},
		{Cluster: "0123456789abcdef-region-c-2", Written: false},
		{Cluster: "0123456789abcdef-region-d-3", Error: "not registered"},
	}
	if !reflect.DeepEqual(got, wantResults) {
		t.Fatalf("results %+v", got)
	}
	for id, wantCalls := range map[string][]string{
		"0123456789abcdef":            {getCall, patchCall},
		"0123456789abcdef-region-b-1": {getCall, patchCall},
		"0123456789abcdef-region-c-2": {getCall},
		"fedcba9876543210":            nil,
	} {
		if c := calls(cache.clients[id]); !reflect.DeepEqual(c, wantCalls) {
			t.Errorf("%s: calls %q, want %q", id, c, wantCalls)
		}
	}
	for _, id := range []string{"0123456789abcdef", "0123456789abcdef-region-b-1", "0123456789abcdef-region-c-2"} {
		if d, r := held(t, cache.clients[id]); d != doc || r != rev {
			t.Errorf("%s holds revision %q", id, r)
		}
	}

	// A second pass finds every region current and writes nothing.
	for _, c := range cache.clients {
		c.ClearActions()
	}
	if _, got, err := ReconcileNetworkRegistry(context.Background(), st, "0123456789abcdef", cache); err != nil || got[0].Written || got[1].Written || got[2].Written {
		t.Fatalf("second pass %+v, %v", got, err)
	}
	for _, id := range []string{"0123456789abcdef", "0123456789abcdef-region-b-1", "0123456789abcdef-region-c-2"} {
		if c := calls(cache.clients[id]); !reflect.DeepEqual(c, []string{getCall}) {
			t.Errorf("second pass, %s: calls %q", id, c)
		}
	}

	// A region whose read fails is reported and not written.
	failing := applyingClient()
	failing.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	one := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{"0123456789abcdef": failing}}
	if _, got, err := ReconcileNetworkRegistry(context.Background(), st, "0123456789abcdef", one); err != nil ||
		!reflect.DeepEqual(got, []RegionResult{{Cluster: "0123456789abcdef", Error: "connection refused"}}) {
		t.Fatalf("failing read: %+v, %v", got, err)
	}
	if c := calls(failing); !reflect.DeepEqual(c, []string{getCall}) {
		t.Errorf("failing read: calls %q", c)
	}

	// Nothing is judged without a registry, a cache or a cluster of the deployment.
	if _, _, err := ReconcileNetworkRegistry(context.Background(), state(t, ""), "0123456789abcdef", cache); !errors.Is(err, ErrNoNetworkRegistry) {
		t.Errorf("no output: %v", err)
	}
	var none *regions
	if _, _, err := ReconcileNetworkRegistry(context.Background(), st, "0123456789abcdef", none); !errors.Is(err, ErrNoClusterCache) {
		t.Errorf("no cache: %v", err)
	}
	if _, _, err := ReconcileNetworkRegistry(context.Background(), st, "1111111111111111", cache); !errors.Is(err, ErrNoRegionCluster) ||
		!strings.HasSuffix(err.Error(), " 1111111111111111") {
		t.Errorf("no cluster: %v", err)
	}
}

// parentBootstrapRules returns the rules of the role that the child binds to
// ServiceAccount kube-system/parent-bootstrap, whose kubeconfig every region
// posts back and the parent's cluster cache uses for the registry's calls.
func parentBootstrapRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()
	path := filepath.Join(repoRoot(t), "infra/providers/_shared/cloudinit-control-plane.tftpl")
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	start := strings.Index(text, "N=parent-bootstrap S=kube-system")
	if start < 0 {
		t.Fatalf("%s mints no ServiceAccount parent-bootstrap", path)
	}
	item := text[start:]
	if end := strings.Index(item, "\n  - "); end > 0 {
		item = item[:end]
	}
	var binding struct {
		RoleRef  rbacv1.RoleRef    `json:"roleRef"`
		Subjects []rbacv1.Subject  `json:"subjects"`
		Metadata map[string]string `json:"metadata"`
	}
	found := false
	for _, doc := range strings.Split(item, "\n    ---\n") {
		if !strings.Contains(doc, "kind: ClusterRoleBinding") {
			continue
		}
		// The heredoc's lines, without their indentation, from apiVersion on.
		var lines []string
		for _, line := range strings.Split(doc, "\n") {
			line = strings.TrimPrefix(line, "    ")
			if len(lines) > 0 || strings.HasPrefix(line, "apiVersion:") {
				lines = append(lines, line)
			}
		}
		body := strings.NewReplacer("$N", "parent-bootstrap", "$S", "kube-system").Replace(strings.Join(lines, "\n"))
		if err := yaml.Unmarshal([]byte(body), &binding); err != nil {
			t.Fatalf("the parent-bootstrap binding of %s: %v", path, err)
		}
		found = true
	}
	if !found || len(binding.Subjects) != 1 || binding.Subjects[0].Name != "parent-bootstrap" || binding.Subjects[0].Namespace != "kube-system" {
		t.Fatalf("%s binds no role to ServiceAccount kube-system/parent-bootstrap: %+v", path, binding)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "cluster-admin" {
		t.Fatalf("parent-bootstrap is bound to %s %s: replay the registry's calls against that role's rules", binding.RoleRef.Kind, binding.RoleRef.Name)
	}
	// The rules of the built-in ClusterRole cluster-admin.
	return []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}
}

// TestNetworkRegistryReconcileVerbsAreGranted: the reconcile makes exactly a
// get and a patch of flux-system/sovereign-network-registry in a region, and
// the identity that makes them, the region's parent-bootstrap ServiceAccount,
// is granted both. The writes go through the child's kubeconfig, not through
// a role of the parent's chart.
func TestNetworkRegistryReconcileVerbsAreGranted(t *testing.T) {
	rules := parentBootstrapRules(t)
	doc := registry(t)
	region := applyingClient()
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{"0123456789abcdef": region}}
	if _, _, err := ReconcileNetworkRegistry(context.Background(), state(t, `"network_registry":{"value":`+quoted(doc)+`}`), "0123456789abcdef", cache); err != nil {
		t.Fatal(err)
	}
	if c := calls(region); !reflect.DeepEqual(c, []string{getCall, patchCall}) {
		t.Fatalf("calls %q, want exactly a get and a patch of the one ConfigMap", c)
	}
	for _, a := range region.Actions() {
		name := ""
		// A patch carries a name too: PatchAction is a GetAction.
		if g, ok := a.(k8stesting.GetAction); ok {
			name = g.GetName()
		}
		if !granted(rules, a.GetVerb(), a.GetResource().Group, a.GetResource().Resource, name) {
			t.Errorf("%s %s/%s %q is not granted to parent-bootstrap", a.GetVerb(), a.GetResource().Group, a.GetResource().Resource, name)
		}
	}
}
