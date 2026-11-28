package regionpin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func pinnedBlueprint(v any) map[string]any {
	return map[string]any{"spec": map[string]any{"version": "1.0.0", "regionPinned": v}}
}

func TestPinned_OnlyBooleanTrue(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{"true", pinnedBlueprint(true), true},
		{"false", pinnedBlueprint(false), false},
		{"a string", pinnedBlueprint("true"), false},
		{"no key", map[string]any{"spec": map[string]any{"version": "1.0.0"}}, false},
		{"no spec", map[string]any{}, false},
	}
	for _, c := range cases {
		if got := Pinned(c.obj); got != c.want {
			t.Errorf("%s: Pinned = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInstallRefusal_OnlySingletonInOneRegion(t *testing.T) {
	cases := []struct {
		name    string
		obj     map[string]any
		mode    string
		regions []string
		refused bool
	}{
		{"pinned singleton in one region", pinnedBlueprint(true), "singleton", []string{"a"}, false},
		{"pinned singleton in two regions", pinnedBlueprint(true), "singleton", []string{"a", "b"}, true},
		{"pinned hot standby in one region", pinnedBlueprint(true), "active-hot-standby", []string{"a"}, true},
		{"pinned active-active in two regions", pinnedBlueprint(true), "active-active", []string{"a", "b"}, true},
		{"not pinned, active-active in two regions", pinnedBlueprint(false), "active-active", []string{"a", "b"}, false},
	}
	for _, c := range cases {
		rej := InstallRefusal("bp-vm", c.obj, c.mode, c.regions)
		if !c.refused {
			if rej != nil {
				t.Errorf("%s: refused %+v, want admitted", c.name, rej)
			}
			continue
		}
		if rej == nil || rej.Status != 422 || rej.Code != "placement-immutable" || !strings.Contains(rej.Message, "bp-vm") {
			t.Errorf("%s: got %+v, want 422 placement-immutable naming bp-vm", c.name, rej)
		}
	}
}

func TestChange(t *testing.T) {
	cases := []struct {
		name string
		cur  []string
		next [][]string
		want string
	}{
		{"same region", []string{"a"}, [][]string{{"a"}}, ""},
		{"same regions and Primary in both lists", []string{"a", "b"}, [][]string{{"a", "b"}, {"a", "b"}}, ""},
		{"another region", []string{"a"}, [][]string{{"b"}}, "regions"},
		{"a region added", []string{"a"}, [][]string{{"a", "b"}}, "regions"},
		{"regions left to the controller", []string{"a"}, [][]string{{}}, "regions"},
		{"no recorded region", nil, [][]string{{"a"}}, "regions"},
		{"Primary moved", []string{"a", "b"}, [][]string{{"b", "a"}}, "primary"},
		{"Primary moved in the targets only", []string{"a", "b"}, [][]string{{"a", "b"}, {"b", "a"}}, "primary"},
		{"targets drop a region", []string{"a", "b"}, [][]string{{"a", "b"}, {"a"}}, "regions"},
	}
	for _, c := range cases {
		if got := Change(c.cur, c.next); got != c.want {
			t.Errorf("%s: Change(%v, %v) = %q, want %q", c.name, c.cur, c.next, got, c.want)
		}
	}
}

func TestEditRefusal(t *testing.T) {
	if rej := EditRefusal("bp-vm", "vm-1", pinnedBlueprint(true), "", []string{"a"}, [][]string{{"a"}}); rej != nil {
		t.Errorf("no change refused: %+v", rej)
	}
	if rej := EditRefusal("bp-vm", "vm-1", pinnedBlueprint(false), "regions", []string{"a"}, [][]string{{"b"}}); rej != nil {
		t.Errorf("a Blueprint that is not pinned refused: %+v", rej)
	}
	cases := []struct {
		name, change, message string
		cur                   []string
		next                  [][]string
	}{
		{"region change", "regions", "change its regions from [a] to [b]", []string{"a"}, [][]string{{"b"}}},
		{"no recorded region", "regions", "change its regions from [] to [b]", nil, [][]string{{"b"}}},
		{"Primary move", "primary", "move its Primary from a to b", []string{"a", "b"}, [][]string{{"b", "a"}}},
	}
	for _, c := range cases {
		rej := EditRefusal("bp-vm", "vm-1", pinnedBlueprint(true), c.change, c.cur, c.next)
		if rej == nil || rej.Status != 422 || rej.Code != "placement-immutable" || !strings.Contains(rej.Message, c.message) {
			t.Errorf("%s: got %+v, want 422 placement-immutable saying %q", c.name, rej, c.message)
		}
	}
}

// The apiserver does not answer the read of the Blueprint: the edit is
// refused with 503, never admitted.
func TestEditGate_BlueprintReadFails_503(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{blueprints: "BlueprintList"})
	client.PrependReactor("get", "blueprints", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	app := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "vm-1", "namespace": "acme"},
		"spec":     map[string]any{"blueprintRef": map[string]any{"name": "bp-vm"}},
	}}
	rej := EditGate(context.Background(), client, app, []string{"a"}, []string{"b"}, nil)
	if rej == nil || rej.Status != 503 || rej.Code != "reference-lookup-failed" || !strings.Contains(rej.Message, "connection refused") {
		t.Fatalf("got %+v, want 503 reference-lookup-failed naming the error", rej)
	}
	if rej := EditGate(context.Background(), client, app, []string{"a"}, []string{"a"}, nil); rej != nil {
		t.Fatalf("an edit that keeps the region read the Blueprint and was refused: %+v", rej)
	}
}
