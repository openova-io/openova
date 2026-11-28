package blueprintedit

import (
	"encoding/json"
	"reflect"
	"testing"

	yamlv3 "gopkg.in/yaml.v3"
)

// TestPartial: the partial object holds the name and the card fields and
// visibility of the document, and nothing else.
func TestPartial(t *testing.T) {
	doc := `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-redis
  labels:
    catalyst.openova.io/managed-by: catalog-seed
spec:
  version: 1.2.3
  class: data-service
  lifecycle: supported
  visibility: unlisted
  topology:
    supported: [singleton]
  card:
    title: Redis Cache
    summary: In-memory store
    license: BSD-3-Clause
`
	out, err := Partial([]byte(doc))
	if err != nil {
		t.Fatalf("Partial: %v", err)
	}
	var got map[string]interface{}
	if err := yamlv3.Unmarshal(out, &got); err != nil {
		t.Fatalf("the partial object does not parse: %v\n%s", err, out)
	}
	want := map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1",
		"kind":       "Blueprint",
		"metadata":   map[string]interface{}{"name": "bp-redis"},
		"spec": map[string]interface{}{
			"visibility": "unlisted",
			"card":       map[string]interface{}{"title": "Redis Cache", "summary": "In-memory store"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Partial:\n got %v\nwant %v", got, want)
	}
	for _, bad := range []string{"- a\n", "kind: Blueprint\nspec:\n  visibility: listed\n"} {
		if _, err := Partial([]byte(bad)); err == nil {
			t.Errorf("Partial(%q) must fail: not a mapping, or no metadata.name", bad)
		}
	}
	if whole, err := FluxPayload([]byte(doc), nil); err != nil || string(whole) != doc {
		t.Errorf("FluxPayload with no in-cluster object must return the whole document unchanged; got %v\n%s", err, whole)
	}
}

// TestPartialKeeps: Flux may leave out a spec field only where another field
// manager owns it too; a missing object is never kept.
func TestPartialKeeps(t *testing.T) {
	helmAll := `{"manager":"helm-controller","operation":"Update","fieldsV1":{"f:metadata":{"f:labels":{}},
		"f:spec":{"f:version":{},"f:class":{},"f:visibility":{},"f:card":{"f:title":{},"f:license":{}},"f:manifests":{"f:chart":{}}}}}`
	helmNoVersion := `{"manager":"helm-controller","operation":"Update","fieldsV1":{"f:spec":{"f:class":{},"f:card":{"f:title":{}}}}}`
	fluxCard := `{"manager":"kustomize-controller","operation":"Apply","fieldsV1":{"f:spec":{"f:visibility":{},"f:card":{"f:title":{},"f:summary":{}}}}}`
	fluxWhole := `{"manager":"kustomize-controller","operation":"Apply","fieldsV1":{"f:spec":{"f:version":{},"f:class":{},"f:card":{"f:title":{},"f:license":{}},"f:manifests":{"f:chart":{}}}}}`
	kubectlVersion := `{"manager":"kubectl-client-side-apply","operation":"Update","fieldsV1":{"f:spec":{"f:version":{}}}}`
	for _, c := range []struct {
		name    string
		entries []string
		want    bool
	}{
		{"the chart seed owns every field", []string{helmAll}, true},
		{"Flux owns only card fields and visibility", []string{helmNoVersion, fluxCard}, true},
		{"Flux shares every field it owns with the seed", []string{helmAll, fluxWhole}, true},
		{"Flux alone owns spec.version (an upgrade was forced back)", []string{helmNoVersion, fluxWhole}, false},
		{"Flux takes over kubectl's spec.version, which nobody else owns", []string{helmNoVersion, fluxCard, kubectlVersion}, false},
		{"Flux alone wrote the object", []string{fluxWhole}, false},
	} {
		var fields []interface{}
		for _, e := range c.entries {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(e), &m); err != nil {
				t.Fatalf("%s: fixture: %v", c.name, err)
			}
			fields = append(fields, m)
		}
		live := map[string]interface{}{"metadata": map[string]interface{}{"name": "bp-redis", "managedFields": fields}}
		if got := partialKeeps(live); got != c.want {
			t.Errorf("%s: partialKeeps = %v, want %v", c.name, got, c.want)
		}
	}
	if partialKeeps(nil) {
		t.Errorf("partialKeeps(nil) must be false: a partial object cannot create a Blueprint")
	}
}
