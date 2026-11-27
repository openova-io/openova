package capability

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// testIndex: object-storage (default bp-store-a, contracts v1 current and v2 current, v3 deprecated) and
// cache (default bp-cache-native).
const testIndex = `{"comment": "test", "capabilities": [
 {"name": "object-storage", "title": "Object storage", "defaultImplementation": "bp-store-a", "contracts": [
   {"version": "v1", "status": "current", "probe": "p1", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}],
    "contextFields": [
      {"name": "versioning", "type": "boolean", "required": false, "default": false, "description": "d"},
      {"name": "retentionDays", "type": "integer", "required": false, "default": 7, "description": "d"},
      {"name": "class", "type": "string", "required": true, "enum": ["hot", "cold"], "description": "d"},
      {"name": "tags", "type": "array", "required": false, "enum": ["a", "b"], "description": "d"}]},
   {"version": "v2", "status": "current", "probe": "p2", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}], "contextFields": []},
   {"version": "v3", "status": "deprecated", "probe": "p3", "keys": [{"name": "bucket", "required": true, "secret": false, "description": "d"}], "contextFields": []}],
  "providers": []},
 {"name": "cache", "title": "Cache", "defaultImplementation": "bp-cache-native", "contracts": [
   {"version": "v1", "status": "current", "probe": "c1", "keys": [{"name": "host", "required": true, "secret": false, "description": "d"}], "contextFields": []}],
  "providers": []}]}`

func mustIndex(t *testing.T) Index {
	t.Helper()
	idx, err := LoadIndex([]byte(testIndex))
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// provider is a Blueprint object that provides one capability.
func provider(name, capability, implementation, lifecycle string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"card":          map[string]interface{}{"title": "Title of " + name},
		"shareable":     true,
		"contextSchema": map[string]interface{}{"kind": "bucket", "valuesKey": "buckets"},
		"provides":      []interface{}{map[string]interface{}{"capability": capability, "contract": "v1", "implementation": implementation}},
	}
	if lifecycle != "" {
		spec["lifecycle"] = lifecycle
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1", "kind": "Blueprint", "metadata": map[string]interface{}{"name": name}, "spec": spec}}
}

type availability map[string]bool

func (a availability) Available(c string) (bool, string) { return a[c], "test" }

func names(ims []Implementation) []string {
	out := []string{}
	for _, im := range ims {
		out = append(out, im.Blueprint)
	}
	return out
}

func TestLoadIndex(t *testing.T) {
	idx := mustIndex(t)
	if got := idx.Capabilities(); !reflect.DeepEqual(got, []string{"cache", "object-storage"}) {
		t.Fatalf("Capabilities = %v", got)
	}
	if c, ok := idx.Current("object-storage"); !ok || c.Version != "v2" || c.Capability != "object-storage" {
		t.Fatalf("Current = %+v %v, want v2 (the newest current; v3 is deprecated)", c, ok)
	}
	if c, ok := idx.Contract("object-storage", "v1"); !ok || c.Probe != "p1" || len(c.ContextFields) != 4 {
		t.Fatalf("Contract v1 = %+v %v", c, ok)
	}
	if _, ok := idx.Contract("object-storage", "v9"); ok {
		t.Fatal("Contract v9 found")
	}
	if idx.DefaultImplementation("object-storage") != "bp-store-a" || idx.Title("cache") != "Cache" || idx.DefaultImplementation("nope") != "" {
		t.Fatal("DefaultImplementation or Title wrong")
	}
	for name, raw := range map[string]string{
		"no capabilities list": `{"comment": "x"}`,
		"listed twice":         `{"capabilities": [{"name": "a", "contracts": []}, {"name": "a", "contracts": []}]}`,
		"no name":              `{"capabilities": [{"contracts": []}]}`,
		"not JSON":             `capabilities:`,
	} {
		if _, err := LoadIndex([]byte(raw)); err == nil {
			t.Errorf("LoadIndex(%s) passed", name)
		}
	}
	if idx, err := LoadIndex([]byte(`{"capabilities": []}`)); err != nil || len(idx.Capabilities()) != 0 {
		t.Fatalf("empty index: %v %v", idx, err)
	}
}

func TestRegistryOffer(t *testing.T) {
	idx := mustIndex(t)
	cases := []struct {
		name, cap   string
		blueprints  []*unstructured.Unstructured
		policy      *unstructured.Unstructured
		avail       Availability
		offered     []string
		def, choice string
	}{
		{name: "default rule: the contract's defaultImplementation",
			blueprints: []*unstructured.Unstructured{provider("bp-store-b", "object-storage", "in-cluster", ""), provider("bp-store-a", "object-storage", "in-cluster", "")},
			offered:    []string{"bp-store-a", "bp-store-b"}, def: "bp-store-a", choice: "allowed"},
		{name: "deprecated implementation excluded; default falls to the lowest offered in-cluster name",
			blueprints: []*unstructured.Unstructured{provider("bp-store-a", "object-storage", "in-cluster", "deprecated"),
				provider("bp-store-c", "object-storage", "in-cluster", "experimental"), provider("bp-store-b", "object-storage", "in-cluster", "supported")},
			offered: []string{"bp-store-b", "bp-store-c"}, def: "bp-store-b", choice: "allowed"},
		{name: "cloud-native excluded when Availability says no", cap: "cache",
			blueprints: []*unstructured.Unstructured{provider("bp-cache-native", "cache", "cloud-native", ""), provider("bp-cache-local", "cache", "in-cluster", "")},
			avail:      NoCloudProfile{}, offered: []string{"bp-cache-local"}, def: "bp-cache-local", choice: "allowed"},
		{name: "cloud-native offered and default when Availability says yes", cap: "cache",
			blueprints: []*unstructured.Unstructured{provider("bp-cache-native", "cache", "cloud-native", ""), provider("bp-cache-local", "cache", "in-cluster", "")},
			avail:      availability{"cache": true}, offered: []string{"bp-cache-local", "bp-cache-native"}, def: "bp-cache-native", choice: "allowed"},
		{name: "only cloud-native offered and the contract default unavailable: no default", cap: "cache",
			blueprints: []*unstructured.Unstructured{provider("bp-cache-other", "cache", "cloud-native", "")},
			avail:      availability{"cache": true}, offered: []string{"bp-cache-other"}, def: "", choice: "allowed"},
		{name: "Sovereign entry: offered narrowed, its default, its organizationChoice",
			blueprints: []*unstructured.Unstructured{provider("bp-store-a", "object-storage", "in-cluster", ""), provider("bp-store-b", "object-storage", "in-cluster", "")},
			policy:     sovereign("object-storage", []string{"bp-store-b", "bp-store-gone"}, "bp-store-b", "fixed"),
			offered:    []string{"bp-store-b"}, def: "bp-store-b", choice: "fixed"},
		{name: "Sovereign entry whose default is not offered: the default rule over the entry's offered set",
			blueprints: []*unstructured.Unstructured{provider("bp-store-a", "object-storage", "in-cluster", ""), provider("bp-store-b", "object-storage", "in-cluster", ""),
				provider("bp-store-c", "object-storage", "in-cluster", "")},
			policy:  sovereign("object-storage", []string{"bp-store-c", "bp-store-b"}, "bp-store-a", ""),
			offered: []string{"bp-store-b", "bp-store-c"}, def: "bp-store-b", choice: "allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capName := tc.cap
			if capName == "" {
				capName = "object-storage"
			}
			o := NewRegistry(idx, tc.blueprints, tc.policy, tc.avail).Offer(capName)
			if !reflect.DeepEqual(names(o.Offered), tc.offered) || o.Default != tc.def || o.OrganizationChoice != tc.choice {
				t.Fatalf("Offer = offered %v default %q choice %q; want %v %q %q", names(o.Offered), o.Default, o.OrganizationChoice, tc.offered, tc.def, tc.choice)
			}
		})
	}
}

func sovereign(capability string, offered []string, def, organizationChoice string) *unstructured.Unstructured {
	off := []interface{}{}
	for _, o := range offered {
		off = append(off, o)
	}
	item := map[string]interface{}{"name": capability, "offered": off, "default": def}
	if organizationChoice != "" {
		item["organizationChoice"] = organizationChoice
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{
		"catalog": map[string]interface{}{"capabilities": []interface{}{item}}}}}
}

func TestRegistryImplementations(t *testing.T) {
	reg := NewRegistry(mustIndex(t), []*unstructured.Unstructured{provider("bp-store-b", "object-storage", "cloud-native", "deprecated"),
		provider("bp-store-a", "object-storage", "in-cluster", ""), nil}, nil, nil)
	got := reg.Implementations("object-storage")
	want := []Implementation{
		{Blueprint: "bp-store-a", Title: "Title of bp-store-a", Contract: "v1", Implementation: "in-cluster", Lifecycle: "experimental", Shareable: true, ContextKind: "bucket", ValuesKey: "buckets"},
		{Blueprint: "bp-store-b", Title: "Title of bp-store-b", Contract: "v1", Implementation: "cloud-native", Lifecycle: "deprecated", Shareable: true, ContextKind: "bucket", ValuesKey: "buckets"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Implementations = %+v\nwant %+v", got, want)
	}
}

func TestRegistryResolve(t *testing.T) {
	idx := mustIndex(t)
	bps := []*unstructured.Unstructured{provider("bp-store-a", "object-storage", "in-cluster", ""),
		provider("bp-store-old", "object-storage", "in-cluster", "deprecated"), provider("bp-cache-local", "cache", "in-cluster", "deprecated")}
	reg := NewRegistry(idx, bps, nil, nil)
	cases := []struct {
		capability, requested, want string
		err                         error
	}{
		{"object-storage", "", "bp-store-a", nil},
		{"object-storage", "bp-store-a", "bp-store-a", nil},
		{"queue", "", "", ErrUnknownCapability},
		{"object-storage", "bp-cache-local", "", ErrWrongCapability},
		{"object-storage", "bp-nothing", "", ErrWrongCapability},
		{"object-storage", "bp-store-old", "", ErrNotOffered},
		{"cache", "", "", ErrNoImplementation},
	}
	for _, tc := range cases {
		im, err := reg.Resolve(tc.capability, tc.requested)
		if !errors.Is(err, tc.err) || im.Blueprint != tc.want {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q, %v", tc.capability, tc.requested, im.Blueprint, err, tc.want, tc.err)
		}
	}
	fixed := NewRegistry(idx, []*unstructured.Unstructured{provider("bp-store-a", "object-storage", "in-cluster", ""),
		provider("bp-store-b", "object-storage", "in-cluster", "")}, sovereign("object-storage", []string{"bp-store-a", "bp-store-b"}, "bp-store-a", "fixed"), nil)
	if _, err := fixed.Resolve("object-storage", "bp-store-b"); !errors.Is(err, ErrNotOffered) {
		t.Errorf("fixed choice: Resolve(bp-store-b) = %v, want ErrNotOffered", err)
	}
	if im, err := fixed.Resolve("object-storage", ""); err != nil || im.Blueprint != "bp-store-a" {
		t.Errorf("fixed choice: Resolve(\"\") = %q, %v; want bp-store-a", im.Blueprint, err)
	}
	if _, err := NewRegistry(nil, bps, nil, nil).Resolve("object-storage", ""); !errors.Is(err, ErrUnknownCapability) {
		t.Errorf("no index: %v, want ErrUnknownCapability", err)
	}
}

func TestContextValues(t *testing.T) {
	c, _ := mustIndex(t).Contract("object-storage", "v1")
	got, err := c.ContextValues(map[string]interface{}{"class": "hot", "retentionDays": float64(30), "tags": []interface{}{"a"}})
	want := map[string]interface{}{"class": "hot", "retentionDays": int64(30), "versioning": false, "tags": []interface{}{"a"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ContextValues = %#v, %v; want %#v", got, err, want)
	}
	if got, err := c.ContextValues(map[string]interface{}{"class": "cold"}); err != nil || got["retentionDays"] != int64(7) {
		t.Fatalf("default of retentionDays = %#v, %v; want int64 7", got["retentionDays"], err)
	}
	for name, in := range map[string]map[string]interface{}{
		"undeclared field":       {"class": "hot", "colour": "red"},
		"wrong type":             {"class": "hot", "versioning": "yes"},
		"not integral":           {"class": "hot", "retentionDays": 1.5},
		"outside enum":           {"class": "warm"},
		"array item not in enum": {"class": "hot", "tags": []interface{}{"a", "z"}},
		"required field missing": {},
	} {
		if _, err := c.ContextValues(in); !errors.Is(err, ErrContextField) {
			t.Errorf("%s: %v, want ErrContextField", name, err)
		}
	}
}
