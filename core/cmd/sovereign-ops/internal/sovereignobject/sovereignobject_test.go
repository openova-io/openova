package sovereignobject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
)

const (
	varsPath = "/api/v1/namespaces/flux-system/configmaps/sovereign-variables"
	regPath  = "/api/v1/namespaces/flux-system/configmaps/sovereign-network-registry"
	objPath  = Resource + "/" + Name
)

// fakeCluster holds objects by path and records every apply.
type fakeCluster struct {
	objects map[string]map[string]interface{}
	applied map[string]map[string]interface{}
	order   []string
	getErr  error
}

func (f *fakeCluster) Get(_ context.Context, path string) (map[string]interface{}, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.objects[path], nil
}

func (f *fakeCluster) Apply(_ context.Context, path string, body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]interface{}
	if err := dec.Decode(&m); err != nil {
		return err
	}
	f.applied[path] = m
	f.order = append(f.order, path)
	return nil
}

// registryDoc is the module output of a two-region Sovereign: the document
// that scripts/check-network-registry.py accepts and the Huawei module test
// compares output network_registry with.
func registryDoc(t *testing.T) string {
	b, err := os.ReadFile(filepath.Clean("../../../../../scripts/tests/check-network-registry/valid-two-regions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func variables() map[string]interface{} {
	return map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "data": map[string]interface{}{
		"SOVEREIGN_FQDN":          "t99.example.invalid",
		"SOVEREIGN_DEPLOYMENT_ID": "0123456789abcdef",
		"PROVIDER":                "huawei",
		"SOVEREIGN_BCP_TOPOLOGY":  "active-hotstandby",
		"SOVEREIGN_REGIONS_JSON": `[{"cloudRegion":"hw-region-a-rtz-prod","controlPlaneSize":"m","provider":"huawei","workerCount":3,"workerSize":"l"},` +
			`{"cloudRegion":"hw-region-b-rtz-prod","controlPlaneSize":"m","provider":"huawei","workerCount":3,"workerSize":"l"}]`,
		"PARENT_DOMAINS_YAML": `[{name: "example.invalid", role: "primary"}, {name: "pool.invalid", role: "org-pool"}]`,
		"ORG_NAME":            "not copied",
	}}
}

func newCluster(t *testing.T, withRegistry bool) *fakeCluster {
	f := &fakeCluster{objects: map[string]map[string]interface{}{varsPath: variables(),
		Resource: {"items": []interface{}{map[string]interface{}{"metadata": map[string]interface{}{"name": Name}}}}},
		applied: map[string]map[string]interface{}{}}
	if withRegistry {
		f.objects[regPath] = map[string]interface{}{"data": map[string]interface{}{RegistryKey: registryDoc(t), RevisionKey: "3f2a9c1b0d4e"}}
	}
	return f
}

func data(f *fakeCluster) map[string]interface{} {
	return f.objects[varsPath]["data"].(map[string]interface{})
}

// TestSovereignObjectSpecFromVariables: every spec field comes from its key
// of sovereign-variables, nothing else is written, and the object carries
// the name sovereign-self and the label of its writer.
func TestSovereignObjectSpecFromVariables(t *testing.T) {
	f := newCluster(t, true)
	evidence, err := Write(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.order, " "); got != objPath+" "+objPath+"/status" {
		t.Fatalf("applied %s", got)
	}
	want := map[string]interface{}{"apiVersion": APIVersion, "kind": Kind,
		"metadata": map[string]interface{}{"name": Name, "labels": map[string]interface{}{"app.kubernetes.io/managed-by": "sovereign-ops"}},
		"spec": map[string]interface{}{"fqdn": "t99.example.invalid", "deploymentId": "0123456789abcdef", "provider": "huawei",
			"bcpTopology": "active-hotstandby",
			"regions": []interface{}{map[string]interface{}{"code": "hw-region-a-rtz-prod", "provider": "huawei"},
				map[string]interface{}{"code": "hw-region-b-rtz-prod", "provider": "huawei"}},
			"parentDomains": []interface{}{map[string]interface{}{"name": "example.invalid", "role": "primary"},
				map[string]interface{}{"name": "pool.invalid", "role": "org-pool"}}}}
	if !reflect.DeepEqual(f.applied[objPath], want) {
		got, _ := json.Marshal(f.applied[objPath])
		t.Fatalf("object %s", got)
	}
	if evidence[0] != "applied Sovereign sovereign-self spec: fqdn, deploymentId, provider, bcpTopology, regions (2), parentDomains (2)" {
		t.Fatalf("evidence %q", evidence)
	}
}

// TestSovereignObjectAbsentFields: a key that is absent or does not qualify
// leaves its field out, and the evidence names the field and the reason.
func TestSovereignObjectAbsentFields(t *testing.T) {
	f := newCluster(t, true)
	d := data(f)
	delete(d, "SOVEREIGN_DEPLOYMENT_ID")
	delete(d, "PROVIDER")
	delete(d, "SOVEREIGN_BCP_TOPOLOGY")
	d["SOVEREIGN_REGIONS_JSON"] = `[{"provider":"huawei"}]`
	d["PARENT_DOMAINS_YAML"] = `[{name: "example.invalid", role: "sme-pool"}]`
	evidence, err := Write(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	spec := f.applied[objPath]["spec"].(map[string]interface{})
	if len(spec) != 1 || spec["fqdn"] != "t99.example.invalid" {
		t.Fatalf("spec %v", spec)
	}
	want := []string{"applied Sovereign sovereign-self spec: fqdn",
		"absent from spec: deploymentId (the key SOVEREIGN_DEPLOYMENT_ID is absent or empty)",
		"absent from spec: provider (the key PROVIDER is absent or empty)",
		"absent from spec: bcpTopology (the key SOVEREIGN_BCP_TOPOLOGY is absent or empty)",
		"absent from spec: regions (entry 0 of SOVEREIGN_REGIONS_JSON has no cloudRegion)",
		"absent from spec: parentDomains (entry 0 of PARENT_DOMAINS_YAML has no name or a role other than primary and org-pool)"}
	if !reflect.DeepEqual(evidence[:len(want)], want) {
		t.Fatalf("evidence %q", evidence)
	}
}

// TestSovereignObjectStatusFromRegistry: status.network is the registry
// document unchanged, numbers included; without the ConfigMap the status is
// applied empty, which removes a status.network written before.
func TestSovereignObjectStatusFromRegistry(t *testing.T) {
	f := newCluster(t, true)
	evidence, err := Write(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(registryDoc(t)))
	dec.UseNumber()
	var doc interface{}
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	st := f.applied[objPath+"/status"]
	if !reflect.DeepEqual(st["status"].(map[string]interface{})["network"], doc) || st["metadata"].(map[string]interface{})["name"] != Name {
		got, _ := json.Marshal(st)
		t.Fatalf("status %s", got)
	}
	if last := evidence[len(evidence)-1]; last != `status.network from ConfigMap flux-system/sovereign-network-registry revision "3f2a9c1b0d4e": 2 region(s); `+
		`status.regionSeparation none, condition RegionSeparation False (SameFailureDomain)` {
		t.Fatalf("evidence %q", evidence)
	}

	f = newCluster(t, false)
	evidence, err = Write(context.Background(), f)
	if err != nil || !reflect.DeepEqual(f.applied[objPath+"/status"]["status"], map[string]interface{}{}) {
		t.Fatalf("without the registry: error %v, status %v", err, f.applied[objPath+"/status"])
	}
	if last := evidence[len(evidence)-1]; last != "status.network absent: ConfigMap flux-system/sovereign-network-registry does not exist" {
		t.Fatalf("evidence %q", evidence)
	}
}

// TestSovereignObjectRefusals: each refusal names its cause; the ones found
// before writing write nothing.
func TestSovereignObjectRefusals(t *testing.T) {
	for _, c := range []struct {
		want    string
		writes  int
		change  func(*fakeCluster)
		transit bool
	}{
		{"does not exist or has no value SOVEREIGN_FQDN", 0, func(f *fakeCluster) { delete(data(f), "SOVEREIGN_FQDN") }, false},
		{"does not exist or has no value SOVEREIGN_FQDN", 0, func(f *fakeCluster) { delete(f.objects, varsPath) }, false},
		{"key NETWORK_REGISTRY_JSON: not JSON", 0, func(f *fakeCluster) {
			f.objects[regPath]["data"].(map[string]interface{})[RegistryKey] = "{"
		}, false},
		{"key NETWORK_REGISTRY_JSON: not a JSON object", 0, func(f *fakeCluster) {
			f.objects[regPath]["data"].(map[string]interface{})[RegistryKey] = "[1]"
		}, false},
		{"key NETWORK_REGISTRY_JSON: the key is absent or empty", 0, func(f *fakeCluster) {
			delete(f.objects[regPath]["data"].(map[string]interface{}), RegistryKey)
		}, false},
		{"other Sovereign objects besides sovereign-self: hand-made", 2, func(f *fakeCluster) {
			f.objects[Resource]["items"] = append(f.objects[Resource]["items"].([]interface{}),
				map[string]interface{}{"metadata": map[string]interface{}{"name": "hand-made"}})
		}, false},
		{"transient: no answer", 0, func(f *fakeCluster) { f.getErr = ops.Transient(errors.New("no answer")) }, true},
	} {
		f := newCluster(t, true)
		c.change(f)
		_, err := Write(context.Background(), f)
		if err == nil || !strings.Contains(err.Error(), c.want) || len(f.order) != c.writes || errors.Is(err, ops.ErrTransient) != c.transit {
			t.Errorf("error %v, %d writes; want an error with %q and %d writes", err, len(f.order), c.want, c.writes)
		}
	}
}

// TestAPIServerApply: the paths, the server-side apply parameters and
// header, the token, and how answers turn into results.
func TestAPIServerApply(t *testing.T) {
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Content-Type")+" "+string(body))
		switch {
		case r.Header.Get("Authorization") != "Bearer tok":
			rw.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == varsPath:
			_, _ = rw.Write([]byte(`{"kind":"ConfigMap","data":{"MTU":"1420"},"metadata":{"generation":1000000}}`))
		case r.URL.Path == objPath && r.Method == http.MethodPatch:
			_, _ = rw.Write([]byte(`{}`))
		case r.URL.Path == objPath+"/status":
			rw.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = rw.Write([]byte(`{"message":"status.network.mtu: Invalid value"}`))
		case r.URL.Path == "/apis/broken":
			rw.WriteHeader(http.StatusBadGateway)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := &apiServer{base: srv.URL, token: "tok", client: srv.Client()}
	ctx := context.Background()
	if obj, err := a.Get(ctx, varsPath); err != nil || obj["kind"] != "ConfigMap" || obj["metadata"].(map[string]interface{})["generation"] != json.Number("1000000") {
		t.Fatalf("Get: %v %v", obj, err)
	}
	if obj, err := a.Get(ctx, regPath); err != nil || obj != nil {
		t.Fatalf("absent: %v %v", obj, err)
	}
	if _, err := a.Get(ctx, "/apis/broken"); !errors.Is(err, ops.ErrTransient) {
		t.Fatalf("HTTP 502 is not transient: %v", err)
	}
	if err := a.Apply(ctx, objPath, []byte(`{"kind":"Sovereign"}`)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := "PATCH " + objPath + "?fieldManager=sovereign-ops&force=true application/apply-patch+yaml {\"kind\":\"Sovereign\"}"; seen[len(seen)-1] != want {
		t.Fatalf("request %q, want %q", seen[len(seen)-1], want)
	}
	if err := a.Apply(ctx, objPath+"/status", []byte(`{}`)); err == nil || errors.Is(err, ops.ErrTransient) ||
		!strings.HasSuffix(err.Error(), "HTTP 422 status.network.mtu: Invalid value") {
		t.Fatalf("HTTP 422: %v", err)
	}
	if err := a.Apply(ctx, Resource+"/other", []byte(`{}`)); !errors.Is(err, ops.ErrTransient) {
		t.Fatalf("HTTP 404 on apply is not transient: %v", err)
	}
	a.token = "wrong"
	if _, err := a.Get(ctx, varsPath); err == nil || errors.Is(err, ops.ErrTransient) {
		t.Fatalf("HTTP 401 is transient or no error: %v", err)
	}
}
