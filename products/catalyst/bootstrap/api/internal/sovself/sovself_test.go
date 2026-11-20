package sovself

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// schemaPath is the published schema of contract C5, from this directory.
const schemaPath = "../../../../../../docs/contracts/c5-upstream-variables.schema.json"

// TestDefaultsAreTheContracts: the default, the allowed values and the
// pattern this package uses for each key are those of the published schema.
func TestDefaultsAreTheContracts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(schemaPath))
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	var doc struct {
		Properties map[string]struct {
			Default *string  `json:"default"`
			Enum    []string `json:"enum"`
			Pattern string   `json:"pattern"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the schema: %v", err)
	}
	for key, want := range contractDefaults {
		p, ok := doc.Properties[key]
		if !ok {
			t.Errorf("%s: not a key of the schema", key)
			continue
		}
		if p.Default == nil || *p.Default != want {
			t.Errorf("%s: this package's default is %q, the schema's is %v", key, want, p.Default)
		}
		for _, v := range p.Enum {
			if !contractAllows[key](v) {
				t.Errorf("%s: the schema allows %q and this package does not", key, v)
			}
		}
		if len(p.Enum) > 0 && contractAllows[key]("not-a-value") {
			t.Errorf("%s: this package allows a value the schema does not", key)
		}
		if p.Pattern != "" && p.Pattern != releaseVersion.String() {
			t.Errorf("%s: the schema's pattern is %q, this package's %q", key, p.Pattern, releaseVersion.String())
		}
	}
}

func TestResolve(t *testing.T) {
	kitOnly := map[string]string{"SOVEREIGN_FQDN": "alpha.example.org", "CILIUM_VERSION": "1.19.3"}
	full := map[string]string{
		"SOVEREIGN_MODE": "mothership", "SOVEREIGN_CONSUME": "local",
		"RELEASE_CHANNEL": "candidate", "RELEASE_VERSION": "2.2.0",
	}
	invalid := map[string]string{
		"SOVEREIGN_MODE": "parent", "SOVEREIGN_CONSUME": "both",
		"RELEASE_CHANNEL": "nightly", "RELEASE_VERSION": "v2",
	}
	cases := []struct {
		name             string
		vars             map[string]string
		present, legacyM bool
		status           map[string]string
		want             state
	}{
		{"a document with only kit keys gives the defaults", kitOnly, true, false, nil,
			state{"sovereign", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
		{"a document with only kit keys stays sovereign without SOVEREIGN_FQDN", kitOnly, true, true, nil,
			state{"sovereign", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
		{"the contract keys are read", full, true, false, nil,
			state{"mothership", "local", "candidate", "2.2.0", "Bootstrapping", "parent", "v1"}},
		{"a value the contract does not allow gives the default", invalid, true, false, nil,
			state{"sovereign", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
		{"no document and no SOVEREIGN_FQDN is the mothership", nil, false, true, nil,
			state{"mothership", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
		{"no document and SOVEREIGN_FQDN is a Sovereign", nil, false, false, nil,
			state{"sovereign", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
		{"phase HandedOver and custody self are read", nil, false, false,
			map[string]string{"phase": "HandedOver", "custodyHolder": "self"},
			state{"sovereign", "origin", "stable", "", "HandedOver", "self", "v1"}},
		{"phase Independent is read", nil, false, false, map[string]string{"phase": "Independent"},
			state{"sovereign", "origin", "stable", "", "Independent", "parent", "v1"}},
		{"another phase or custody value counts as absent", nil, false, false,
			map[string]string{"phase": "FluxOwned", "custodyHolder": "nobody"},
			state{"sovereign", "origin", "stable", "", "Bootstrapping", "parent", "v1"}},
	}
	for _, c := range cases {
		if got := resolve(c.vars, c.present, c.legacyM, c.status, "v1"); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// cluster is a fake cluster with the given ConfigMaps and server version.
func cluster(t *testing.T, objects ...runtime.Object) *fake.Clientset {
	t.Helper()
	cs := fake.NewSimpleClientset(objects...)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.32.9+k3s1"}
	return cs
}

func configMap(ns, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: data}
}

// clock is a settable time.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func route(cs kubernetes.Interface, env map[string]string, ok bool, c *clock) *Route {
	return routeWith(cs, objects(), env, ok, c)
}

// routeWith is route on a cluster whose custom objects dyn holds.
func routeWith(cs kubernetes.Interface, dyn dynamic.Interface, env map[string]string, ok bool, c *clock) *Route {
	resolver := func(*http.Request) (string, string, bool) {
		if !ok {
			return "", "", false
		}
		return "sovereign-alpha.example.org", "alpha.example.org", true
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := newRoute(resolver, log, func() (kubernetes.Interface, error) { return cs, nil },
		func(k string) string { return env[k] }, c.now)
	rt.newDynamic = func() (dynamic.Interface, error) { return dyn, nil }
	return rt
}

// objects is a fake cluster with the given Sovereign objects.
func objects(sovereigns ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{sovereignGVR: "SovereignList"}, sovereigns...)
}

// sovereign is a Sovereign object with the name its writer gives it and the
// given status; a nil status leaves the status out.
func sovereign(status map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1alpha1",
		"kind":       "Sovereign",
		"metadata":   map[string]interface{}{"name": "sovereign-self"},
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func get(t *testing.T, rt *Route) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.Serve(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/self", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec.Code, body
}

func want(t *testing.T, body map[string]any, key string, value any) {
	t.Helper()
	got, _ := json.Marshal(body[key])
	exp, _ := json.Marshal(value)
	if string(got) != string(exp) {
		t.Errorf("%s = %s, want %s", key, got, exp)
	}
}

// TestServeKitOnlyDocument: a new Sovereign whose document holds only kit
// keys answers with the contract defaults and its Kubernetes version.
func TestServeKitOnlyDocument(t *testing.T) {
	cs := cluster(t, configMap("flux-system", "sovereign-variables", map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}))
	rt := route(cs, map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, true, &clock{time.Unix(0, 0)})
	code, body := get(t, rt)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	want(t, body, "deploymentId", "sovereign-alpha.example.org")
	want(t, body, "sovereignFQDN", "alpha.example.org")
	want(t, body, "mode", "sovereign")
	want(t, body, "consume", "origin")
	want(t, body, "phase", "Bootstrapping")
	want(t, body, "custodyHolder", "parent")
	want(t, body, "release", map[string]string{"channel": "stable", "version": ""})
	want(t, body, "kubernetesVersion", "v1.32.9+k3s1")
}

// TestServeContractKeys: the keys of the document and of the status
// ConfigMap, in the namespace the cutover engine is told to use, are read.
func TestServeContractKeys(t *testing.T) {
	cs := cluster(t,
		configMap("flux-system", "sovereign-variables", map[string]string{
			"SOVEREIGN_MODE": "mothership", "SOVEREIGN_CONSUME": "local",
			"RELEASE_CHANNEL": "edge", "RELEASE_VERSION": "2.2.0",
		}),
		configMap("ops", "status", map[string]string{"phase": "HandedOver", "custodyHolder": "self"}))
	env := map[string]string{"SOVEREIGN_FQDN": "alpha.example.org", "CATALYST_CUTOVER_NAMESPACE": "ops", "CATALYST_CUTOVER_STATUS_CONFIGMAP": "status"}
	code, body := get(t, route(cs, env, true, &clock{time.Unix(0, 0)}))
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	want(t, body, "mode", "mothership")
	want(t, body, "consume", "local")
	want(t, body, "phase", "HandedOver")
	want(t, body, "custodyHolder", "self")
	want(t, body, "release", map[string]string{"channel": "edge", "version": "2.2.0"})
}

// TestServeMothership: without a document and without SOVEREIGN_FQDN the
// route answers 404 not-a-sovereign as before, and names the mode.
func TestServeMothership(t *testing.T) {
	code, body := get(t, route(cluster(t), map[string]string{}, false, &clock{time.Unix(0, 0)}))
	if code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", code)
	}
	want(t, body, "error", "not-a-sovereign")
	want(t, body, "mode", "mothership")
	want(t, body, "status", 404)
	want(t, body, "code", "404")
}

// TestServeKeepsOneAnswer: within CacheTTL the cluster is read once; after
// it, again. An answer whose read failed is not kept.
func TestServeKeepsOneAnswer(t *testing.T) {
	cs := cluster(t, configMap("flux-system", "sovereign-variables", map[string]string{"SOVEREIGN_MODE": "sovereign"}))
	c := &clock{time.Unix(0, 0)}
	rt := route(cs, map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, true, c)
	reads := func() int { return len(cs.Actions()) }
	get(t, rt)
	first := reads()
	if first == 0 {
		t.Fatalf("the first request read nothing")
	}
	get(t, rt)
	if reads() != first {
		t.Errorf("a second request within CacheTTL read the cluster again")
	}
	c.t = c.t.Add(CacheTTL)
	get(t, rt)
	if reads() == first {
		t.Errorf("a request after CacheTTL did not read the cluster")
	}

	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	c.t = c.t.Add(CacheTTL)
	_, body := get(t, rt)
	want(t, body, "mode", "sovereign")
	before := reads()
	get(t, rt)
	if reads() == before {
		t.Errorf("an answer whose read failed was kept")
	}
}

// TestServeWithoutCluster: when no cluster client can be made, the route
// still answers, with the defaults.
func TestServeWithoutCluster(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := newRoute(func(*http.Request) (string, string, bool) { return "id", "alpha.example.org", true }, log,
		func() (kubernetes.Interface, error) { return nil, errors.New("not in a cluster") },
		func(string) string { return "alpha.example.org" }, time.Now)
	code, body := get(t, rt)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	want(t, body, "mode", "sovereign")
	want(t, body, "consume", "origin")
	want(t, body, "kubernetesVersion", "")
}

// sameFailureDomain is the message the writer gives the condition
// RegionSeparation for none on two or more regions.
const sameFailureDomain = "Regions share one failure domain. Not a disaster-recovery configuration."

// kitOnly is a fake cluster whose document holds only kit keys.
func kitOnly(t *testing.T) *fake.Clientset {
	return cluster(t, configMap("flux-system", "sovereign-variables", map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}))
}

var alphaEnv = map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}

// theOtherFields: the fields of the answer that do not come from the
// Sovereign object are those of a Sovereign whose document holds only kit
// keys.
func theOtherFields(t *testing.T, name string, body map[string]any) {
	t.Helper()
	exp := map[string]any{
		"deploymentId": "sovereign-alpha.example.org", "sovereignFQDN": "alpha.example.org",
		"mode": "sovereign", "consume": "origin", "phase": "Bootstrapping", "custodyHolder": "parent",
		"release": map[string]any{"channel": "stable", "version": ""}, "kubernetesVersion": "v1.32.9+k3s1",
	}
	for key, value := range exp {
		got, _ := json.Marshal(body[key])
		want, _ := json.Marshal(value)
		if string(got) != string(want) {
			t.Errorf("%s: %s = %s, want %s", name, key, got, want)
		}
	}
}

// TestServeRegionSeparation: the answer carries status.regionSeparation of
// the Sovereign object and the message of its condition RegionSeparation,
// as the object holds them, beside the other fields.
func TestServeRegionSeparation(t *testing.T) {
	obj := sovereign(map[string]interface{}{
		"regionSeparation": "none",
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True", "message": "another condition"},
			map[string]interface{}{"type": "RegionSeparation", "status": "False", "reason": "SameFailureDomain", "message": sameFailureDomain},
		},
	})
	code, body := get(t, routeWith(kitOnly(t), objects(obj), alphaEnv, true, &clock{time.Unix(0, 0)}))
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	want(t, body, "regionSeparation", "none")
	want(t, body, "regionSeparationMessage", sameFailureDomain)
	theOtherFields(t, "with the Sovereign object", body)

	site := sovereign(map[string]interface{}{
		"regionSeparation": "site",
		"conditions": []interface{}{
			map[string]interface{}{"type": "RegionSeparation", "status": "True", "reason": "SeparateSites", "message": "Every region runs in its own cloud region."},
		},
	})
	_, body = get(t, routeWith(kitOnly(t), objects(site), alphaEnv, true, &clock{time.Unix(0, 0)}))
	want(t, body, "regionSeparation", "site")
	want(t, body, "regionSeparationMessage", "Every region runs in its own cloud region.")
}

// TestServeWithoutRegionSeparation: without a Sovereign object, or with one
// that holds neither the field nor the condition, the answer is 200 with
// its other fields unchanged and leaves both fields out.
func TestServeWithoutRegionSeparation(t *testing.T) {
	for _, c := range []struct {
		name string
		dyn  *dynamicfake.FakeDynamicClient
	}{
		{"no Sovereign object", objects()},
		{"a Sovereign object without a status", objects(sovereign(nil))},
		{"a Sovereign object whose status holds neither", objects(sovereign(map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "message": "another condition"}},
		}))},
	} {
		code, body := get(t, routeWith(kitOnly(t), c.dyn, alphaEnv, true, &clock{time.Unix(0, 0)}))
		if code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", c.name, code)
		}
		for _, key := range []string{"regionSeparation", "regionSeparationMessage"} {
			if _, ok := body[key]; ok {
				t.Errorf("%s: the answer carries %s = %v", c.name, key, body[key])
			}
		}
		theOtherFields(t, c.name, body)
	}
}

// TestServeRegionSeparationReadError: a read error of the Sovereign object
// leaves the other fields as they are, leaves both fields out, and the
// answer is not kept.
func TestServeRegionSeparationReadError(t *testing.T) {
	dyn := objects(sovereign(map[string]interface{}{"regionSeparation": "site"}))
	dyn.PrependReactor("get", "sovereigns", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	rt := routeWith(kitOnly(t), dyn, alphaEnv, true, &clock{time.Unix(0, 0)})
	code, body := get(t, rt)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	if _, ok := body["regionSeparation"]; ok {
		t.Errorf("the answer carries regionSeparation after a read error")
	}
	theOtherFields(t, "a read error of the Sovereign object", body)
	before := len(dyn.Actions())
	get(t, rt)
	if len(dyn.Actions()) == before {
		t.Errorf("an answer whose Sovereign object could not be read was kept")
	}
}

// TestSovereignObjectIsTheWriters: the route reads the object and the
// condition that the writer of the Sovereign object writes.
func TestSovereignObjectIsTheWriters(t *testing.T) {
	const dir = "../../../../../../core/cmd/sovereign-ops/internal/sovereignobject/"
	for file, pattern := range map[string]string{
		"sovereignobject.go": `(?m)^\s*Name\s*=\s*"` + sovereignName + `"$`,
		"separation.go":      `(?m)^const ConditionType = "` + separationCondition + `"$`,
	} {
		raw, err := os.ReadFile(filepath.Clean(dir + file))
		if err != nil {
			t.Fatalf("read the writer: %v", err)
		}
		if !regexp.MustCompile(pattern).Match(raw) {
			t.Errorf("%s does not match %s", file, pattern)
		}
	}
	gvr := "/apis/" + sovereignGVR.Group + "/" + sovereignGVR.Version + "/" + sovereignGVR.Resource
	raw, _ := os.ReadFile(filepath.Clean(dir + "sovereignobject.go"))
	if !regexp.MustCompile(`(?m)^\s*Resource\s*=\s*"` + regexp.QuoteMeta(gvr) + `"$`).Match(raw) {
		t.Errorf("the writer's Resource is not %s", gvr)
	}
}
