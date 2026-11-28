package binding

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
)

// TestPlan — Reuse plans the consumer's Context on an instance once: a second element on that instance and an
// instance that already declares a Context of the consumer's name are refused 409 context-exists, leave their
// target object as it was and plan no write. Writes lists the planned writes in the order they were planned,
// each with its namespace and Organization, a reused instance carrying the appended entry.
func TestPlan(t *testing.T) {
	app := func(name string, params map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps.openova.io/v1", "kind": "Application",
			"metadata": map[string]interface{}{"name": name, "namespace": "acme"}, "spec": map[string]interface{}{"parameters": params}}}
	}
	shared := app("shared", map[string]interface{}{"replicas": int64(1)})
	taken := app("taken", map[string]interface{}{"buckets": []interface{}{map[string]interface{}{"name": "wiki"}}})
	entry := func(alias string) map[string]interface{} {
		return map[string]interface{}{"name": "wiki", "alias": alias}
	}

	var p Plan
	provider := app("wiki-objects", map[string]interface{}{})
	p.Create("acme", "acme.example", provider)
	reused := shared.DeepCopy()
	if r := p.Reuse(1, reused, "buckets", "bucket", entry("archive")); r != nil {
		t.Fatalf("first reuse of shared refused: %+v", r)
	}
	refusals := []struct {
		target *unstructured.Unstructured
		want   Refusal
	}{
		{shared, Refusal{Status: 409, Code: "context-exists", Message: `backing[1] and backing[2] both reuse instance "shared", and the consumer has one Context bucket/wiki on an instance`}},
		{taken, Refusal{Status: 409, Code: "context-exists", Message: `instance "taken" already declares Context bucket/wiki`}},
	}
	for _, rc := range refusals {
		target := rc.target.DeepCopy()
		r := p.Reuse(2, target, "buckets", "bucket", entry("media"))
		if r == nil || *r != rc.want {
			t.Fatalf("Reuse of %s = %+v, want %+v", rc.target.GetName(), r, rc.want)
		}
		if !reflect.DeepEqual(target.Object, rc.target.Object) {
			t.Fatalf("a refused Reuse changed %s: %#v", rc.target.GetName(), target.Object)
		}
	}

	want := []PlannedWrite{
		{Obj: provider, Namespace: "acme", Org: "acme.example", Create: true},
		{Obj: reused, Namespace: "acme", Org: "acme"},
	}
	if got := p.Writes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Writes = %+v, want %+v", got, want)
	}
	params, _, _ := unstructured.NestedMap(reused.Object, "spec", "parameters")
	if wantParams := map[string]interface{}{"replicas": int64(1), "buckets": []interface{}{entry("archive")}}; !reflect.DeepEqual(params, wantParams) {
		t.Fatalf("reused shared parameters = %#v, want %#v", params, wantParams)
	}
}

func TestPath(t *testing.T) {
	if got := Path("acme", "objects"); got != "mounted/data/acme/shared/binding-objects/e1" {
		t.Fatalf("Path = %q", got)
	}
	if got := MountPath("objects"); got != "/bindings/objects" {
		t.Fatalf("MountPath = %q", got)
	}
}

// TestEmbeddedIndexLoads — the index this binary embeds is one LoadIndex reads (on main it lists no
// capability: the first contract file arrives with its first provider).
func TestEmbeddedIndexLoads(t *testing.T) {
	if _, err := embeddedIndex(); err != nil {
		t.Fatal(err)
	}
}

func TestWrite(t *testing.T) {
	app := map[string]interface{}{"spec": map[string]interface{}{"parameters": map[string]interface{}{"replicas": int64(2)}}}
	if err := Write(app, nil); err != nil || !reflect.DeepEqual(app["spec"], map[string]interface{}{"parameters": map[string]interface{}{"replicas": int64(2)}}) {
		t.Fatalf("Write with no bindings changed the object: %v %v", app, err)
	}
	bindings := map[string]interface{}{}
	Add(bindings, nil, "acme")
	Add(bindings, &Resolved{Requirement: capability.Requirement{Capability: "object-storage", Contract: "v1", Alias: "objects"}}, "acme")
	if err := Write(app, bindings); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"replicas": int64(2), "bindings": map[string]interface{}{"objects": map[string]interface{}{
		"capability": "object-storage", "contract": "v1", "path": "mounted/data/acme/shared/binding-objects/e1", "mountPath": "/bindings/objects"}}}
	if got := app["spec"].(map[string]interface{})["parameters"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters = %#v\nwant %#v", got, want)
	}
}
