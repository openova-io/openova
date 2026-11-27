package binding

import (
	"reflect"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
)

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
