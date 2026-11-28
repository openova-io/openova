package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
)

// The capability of these tests: object-storage, contract v1, with two context fields that have defaults.
const backingTestIndex = `{"comment": "test", "capabilities": [{"name": "object-storage", "title": "Object storage",
  "defaultImplementation": "bp-object-bucket", "compositeKind": "ObjectBucket",
  "contracts": [{"version": "v1", "status": "current", "probe": "object-storage-v1",
    "keys": [{"name": "bucket", "required": true, "secret": false, "description": "Bucket name"}],
    "contextFields": [
      {"name": "versioning", "type": "boolean", "required": false, "default": false, "description": "Keep old versions"},
      {"name": "retentionDays", "type": "integer", "required": false, "default": 0, "description": "Days to keep objects"}]}],
  "providers": []}]}`

// providerBlueprintCR is a Blueprint object that provides object-storage v1.
func providerBlueprintCR(name, implementation string) *unstructured.Unstructured {
	bp := refTestBlueprint(name)
	spec := bp.Object["spec"].(map[string]interface{})
	spec["provides"] = []interface{}{map[string]interface{}{"capability": "object-storage", "contract": "v1", "implementation": implementation}}
	spec["shareable"] = true
	spec["contextSchema"] = map[string]interface{}{"kind": "bucket", "valuesKey": "buckets"}
	spec["lifecycle"] = "experimental"
	return bp
}

// providerCatalogEntry is the catalogue view of a provider: shareable, one Context per bucket, singleton.
func providerCatalogEntry(name string) *CatalogBlueprint {
	return &CatalogBlueprint{Name: name, Version: "0.1.0", Raw: map[string]interface{}{"spec": map[string]interface{}{
		"version":       "0.1.0",
		"shareable":     true,
		"contextSchema": map[string]interface{}{"kind": "bucket", "valuesKey": "buckets"},
		"multiInstance": map[string]interface{}{"enabled": true},
		"topology": map[string]interface{}{"supported": []interface{}{"singleton"},
			"defaults": map[string]interface{}{"multi-region": "singleton", "single-region": "singleton"}},
	}}}
}

// newCapabilityTestHandler: Organization acme; the consumer wordpress, which requires object-storage v1 as
// "objects" and, optionally, as "archive"; the in-cluster provider bp-object-bucket; the cloud-native
// provider bp-object-bucket-managed; bp-postgres, which provides nothing.
func newCapabilityTestHandler(t *testing.T, seed ...runtime.Object) (*Handler, func(string) *httptest.ResponseRecorder, func() []string) {
	t.Helper()
	seed = append(seed, providerBlueprintCR("bp-object-bucket", "in-cluster"), providerBlueprintCR("bp-object-bucket-managed", "cloud-native"))
	h, dyn := newTestHandlerWithEndpoint(t, seed...)
	idx, err := capability.LoadIndex([]byte(backingTestIndex))
	if err != nil {
		t.Fatal(err)
	}
	deps := h.endpointDeps
	deps.CapabilityIndex = idx
	h.SetEndpointPrecheckDeps(deps)
	consumer := fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton"}).byKey["wordpress@1.0.0"]
	consumer.Raw["spec"].(map[string]interface{})["requires"] = []interface{}{
		map[string]interface{}{"capability": "object-storage", "contract": "v1", "alias": "objects"},
		map[string]interface{}{"capability": "object-storage", "contract": "v1", "alias": "archive", "optional": true}}
	h.SetCatalogClient(newFakeCatalog(consumer, providerCatalogEntry("bp-object-bucket"), providerCatalogEntry("bp-object-bucket-managed"),
		shareablePostgresCatalog().byKey["bp-postgres@0.1.6"]))
	r := newTestRouter(h)
	post := func(backing string) *httptest.ResponseRecorder {
		body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wiki","topology":"singleton","backing":` + backing + `}`)
		req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	apps := func() []string {
		list, err := dyn.Resource(ApplicationGVR()).Namespace("").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range list.Items {
			out = append(out, it.GetName())
		}
		sort.Strings(out)
		return out
	}
	return h, post, apps
}

func getApp(t *testing.T, h *Handler, name string) *unstructured.Unstructured {
	t.Helper()
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Application %s: %v", name, err)
	}
	return got
}

// TestCreateInstance_CapabilityBackingCreate — an element that names a capability and no implementation
// resolves to the contract's defaultImplementation and creates its own Application, named after the alias,
// with the canonical Context entry (defaults of the context fields filled in); the consumer gets one binding
// per alias. Two bindings of one capability are two Applications.
func TestCreateInstance_CapabilityBackingCreate(t *testing.T) {
	h, post, apps := newCapabilityTestHandler(t)
	rec := post(`[{"capability":"object-storage","alias":"objects","context":{"retentionDays":30}},{"capability":"object-storage","alias":"archive"}]`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if got := apps(); !reflect.DeepEqual(got, []string{"wiki", "wiki-archive", "wiki-objects"}) {
		t.Fatalf("Applications = %v, want [wiki wiki-archive wiki-objects]", got)
	}
	consumer := getApp(t, h, "wiki")
	for alias, retention := range map[string]int64{"objects": 30, "archive": 0} {
		provider := getApp(t, h, "wiki-"+alias)
		if bp, _, _ := unstructured.NestedString(provider.Object, "spec", "blueprintRef", "name"); bp != "bp-object-bucket" {
			t.Fatalf("provider wiki-%s blueprint = %q, want bp-object-bucket", alias, bp)
		}
		buckets, _, _ := unstructured.NestedSlice(provider.Object, "spec", "parameters", "buckets")
		wantEntry := map[string]interface{}{
			"name": "wiki", "capability": "object-storage", "contract": "v1", "alias": alias,
			"consumer":   map[string]interface{}{"blueprint": "bp-wordpress", "application": "wiki", "namespace": "acme", "mode": "shared"},
			"binding":    map[string]interface{}{"path": "mounted/data/acme/shared/binding-" + alias + "/e1"},
			"versioning": false, "retentionDays": retention,
		}
		if len(buckets) != 1 || !reflect.DeepEqual(buckets[0], wantEntry) {
			t.Fatalf("provider wiki-%s buckets = %#v\nwant [%#v]", alias, buckets, wantEntry)
		}
		binding, _, _ := unstructured.NestedMap(consumer.Object, "spec", "parameters", "bindings", alias)
		wantBinding := map[string]interface{}{"capability": "object-storage", "contract": "v1",
			"path": "mounted/data/acme/shared/binding-" + alias + "/e1", "mountPath": "/bindings/" + alias}
		if !reflect.DeepEqual(binding, wantBinding) {
			t.Fatalf("consumer bindings.%s = %#v, want %#v", alias, binding, wantBinding)
		}
	}
	wantDeps := []interface{}{
		map[string]interface{}{"name": "wiki-objects", "namespace": "acme", "context": "bucket/wiki"},
		map[string]interface{}{"name": "wiki-archive", "namespace": "acme", "context": "bucket/wiki"},
	}
	if deps, _, _ := unstructured.NestedSlice(consumer.Object, "spec", "dependsOn"); !reflect.DeepEqual(deps, wantDeps) {
		t.Fatalf("consumer dependsOn = %#v, want %#v", deps, wantDeps)
	}
}

// TestCreateInstance_CapabilityBackingReuse — mode reuse appends the canonical Context entry to the named
// instance of the resolved implementation and creates no provider Application.
func TestCreateInstance_CapabilityBackingReuse(t *testing.T) {
	target := seedApp("uid-ob", "shared-objects", "acme", "object-bucket")
	_ = unstructured.SetNestedMap(target.Object, map[string]interface{}{}, "spec", "parameters")
	h, post, apps := newCapabilityTestHandler(t, target)
	rec := post(`[{"capability":"object-storage","alias":"objects","implementation":"bp-object-bucket","mode":"reuse","instance":"shared-objects"}]`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	buckets, _, _ := unstructured.NestedSlice(getApp(t, h, "shared-objects").Object, "spec", "parameters", "buckets")
	if len(buckets) != 1 {
		t.Fatalf("reused instance buckets = %#v, want the wiki Context", buckets)
	}
	e := buckets[0].(map[string]interface{})
	if e["alias"] != "objects" || e["capability"] != "object-storage" || e["retentionDays"] != int64(0) ||
		!reflect.DeepEqual(e["binding"], map[string]interface{}{"path": "mounted/data/acme/shared/binding-objects/e1"}) {
		t.Fatalf("reused Context entry = %#v", e)
	}
	if got := apps(); !reflect.DeepEqual(got, []string{"shared-objects", "wiki"}) {
		t.Fatalf("Applications = %v, want [shared-objects wiki]", got)
	}
}

// clusterWrites lists every create, update, patch and delete the fake cluster of h received, as
// "<verb> <resource> <namespace>".
func clusterWrites(t *testing.T, h *Handler) []string {
	t.Helper()
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range client.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "create", "update", "patch", "delete":
			out = append(out, a.GetVerb()+" "+a.GetResource().Resource+" "+a.GetNamespace())
		}
	}
	return out
}

// TestCreateInstance_BackingJudgedBeforeAnyWrite — the whole backing list is judged before its first write. Two
// reuse bindings of one capability on one instance answer 409 context-exists, and an element refused after a
// valid create or reuse answers its own refusal; each time the cluster receives no write, so no provider
// Application, no consumer and no Context is left behind. Two reuse bindings on two instances are accepted.
func TestCreateInstance_BackingJudgedBeforeAnyWrite(t *testing.T) {
	reuse := func(alias, instance string) string {
		return `{"capability":"object-storage","alias":"` + alias + `","mode":"reuse","instance":"` + instance + `"}`
	}
	cases := []struct {
		name, backing, code string
		status              int
	}{
		{"two reuse bindings on one instance", `[` + reuse("objects", "shared-objects") + `,` + reuse("archive", "shared-objects") + `]`, "context-exists", http.StatusConflict},
		{"a reuse, then an instance that does not exist", `[` + reuse("objects", "shared-objects") + `,` + reuse("archive", "missing") + `]`, "backing-instance-not-found", http.StatusNotFound},
		{"a create, then an instance that does not exist", `[{"capability":"object-storage","alias":"objects"},` + reuse("archive", "missing") + `]`, "backing-instance-not-found", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := seedApp("uid-ob", "shared-objects", "acme", "object-bucket")
			_ = unstructured.SetNestedMap(target.Object, map[string]interface{}{"replicas": int64(1)}, "spec", "parameters")
			h, post, apps := newCapabilityTestHandler(t, target)
			rec := post(tc.backing)
			var resp struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if rec.Code != tc.status || resp.Code != tc.code {
				t.Fatalf("got %d %q (body=%s), want %d %q", rec.Code, resp.Code, rec.Body.String(), tc.status, tc.code)
			}
			if w := clusterWrites(t, h); len(w) != 0 {
				t.Fatalf("a refused request wrote %v", w)
			}
			if got := apps(); !reflect.DeepEqual(got, []string{"shared-objects"}) {
				t.Fatalf("Applications = %v, want [shared-objects]", got)
			}
			params, _, _ := unstructured.NestedMap(getApp(t, h, "shared-objects").Object, "spec", "parameters")
			if want := map[string]interface{}{"replicas": int64(1)}; !reflect.DeepEqual(params, want) {
				t.Fatalf("shared-objects parameters = %#v, want %#v", params, want)
			}
		})
	}
	t.Run("two reuse bindings on two instances", func(t *testing.T) {
		h, post, apps := newCapabilityTestHandler(t, seedApp("uid-ob", "shared-objects", "acme", "object-bucket"),
			seedApp("uid-ar", "shared-archive", "acme", "object-bucket"))
		rec := post(`[` + reuse("objects", "shared-objects") + `,` + reuse("archive", "shared-archive") + `]`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		want := []string{"update applications acme", "update applications acme", "create applications acme"}
		if w := clusterWrites(t, h); !reflect.DeepEqual(w, want) {
			t.Fatalf("cluster writes = %v, want %v", w, want)
		}
		if got := apps(); !reflect.DeepEqual(got, []string{"shared-archive", "shared-objects", "wiki"}) {
			t.Fatalf("Applications = %v, want [shared-archive shared-objects wiki]", got)
		}
		for instance, alias := range map[string]string{"shared-objects": "objects", "shared-archive": "archive"} {
			buckets, _, _ := unstructured.NestedSlice(getApp(t, h, instance).Object, "spec", "parameters", "buckets")
			if len(buckets) != 1 || buckets[0].(map[string]interface{})["alias"] != alias {
				t.Fatalf("%s buckets = %#v, want one Context with alias %s", instance, buckets, alias)
			}
		}
	})
}

// TestCreateInstance_CapabilityBackingRefusals — one case per error code the capability path adds; a refused
// request writes no Application.
func TestCreateInstance_CapabilityBackingRefusals(t *testing.T) {
	cases := []struct {
		name, backing, code string
		status              int
	}{
		{"requirement-missing", `[]`, "requirement-missing", http.StatusUnprocessableEntity},
		{"capability-not-required", `[{"capability":"object-storage","alias":"objects"},{"capability":"object-storage","alias":"media"}]`, "capability-not-required", http.StatusUnprocessableEntity},
		{"implementation-not-offered", `[{"capability":"object-storage","alias":"objects","implementation":"bp-object-bucket-managed"}]`, "implementation-not-offered", http.StatusUnprocessableEntity},
		{"implementation-wrong-capability", `[{"capability":"object-storage","alias":"objects","implementation":"bp-postgres"}]`, "implementation-wrong-capability", http.StatusUnprocessableEntity},
		{"context-field-invalid", `[{"capability":"object-storage","alias":"objects","context":{"retentionDays":"thirty"}}]`, "context-field-invalid", http.StatusUnprocessableEntity},
		{"backing-ambiguous", `[{"capability":"object-storage","alias":"objects","blueprint":"bp-object-bucket"}]`, "backing-ambiguous", http.StatusBadRequest},
		{"backing-alias-required", `[{"capability":"object-storage"}]`, "backing-alias-required", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, post, apps := newCapabilityTestHandler(t)
			rec := post(tc.backing)
			var resp struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if rec.Code != tc.status || resp.Code != tc.code {
				t.Fatalf("got %d %q (body=%s), want %d %q", rec.Code, resp.Code, rec.Body.String(), tc.status, tc.code)
			}
			if got := apps(); len(got) != 0 {
				t.Fatalf("a refused request wrote Applications %v", got)
			}
		})
	}
}
