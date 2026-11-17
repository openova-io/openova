package upstream

import (
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// SOV-135: the origin registry host may itself be a proxy name. Its entry as a
// proxy name stands, with its rewrite, and no second entry is written for it.
// A name with an empty path is served at the root: no rewrite.
func TestRegistriesYAMLOriginHostIsAProxyName(t *testing.T) {
	parse := func(b []byte) map[string]mirror {
		t.Helper()
		var f struct {
			Mirrors map[string]mirror `json:"mirrors"`
		}
		if err := yaml.UnmarshalStrict(b, &f); err != nil {
			t.Fatalf("not a registries file: %v\n%s", err, b)
		}
		return f.Mirrors
	}
	const (
		local  = "https://registry.alpha.example.org"
		parent = "https://registry.parent.example.net"
		near   = "http://registry.near.example.net:5000"
	)
	doc := with(example(),
		"REGISTRY_HOST", "ghcr.io",
		"ORIGIN_REGISTRY_HOST", "ghcr.io",
		"REGISTRY_PROXY_PROJECTS_JSON", `{"ghcr.io":"proxy-ghcr","quay.io":"proxy-quay","registry.parent.example.net":"","root.example.net":""}`,
		"ORIGIN_PROXY_ENDPOINTS_JSON", `{"ghcr.io":"`+near+`","registry.parent.example.net":"`+parent+`","root.example.net":"https://root.example.net"}`,
	)
	v := mustFromMap(t, doc)
	if errs := v.Validate("alpha.example.org"); len(errs) > 0 {
		t.Fatalf("the document breaks a rule of the contract: %v", errs)
	}
	b, err := v.RegistriesYAML("", nil)
	if err != nil {
		t.Fatalf("the origin host is a proxy name and the document is refused: %v", err)
	}
	before := parse(b)
	want := map[string]mirror{
		"ghcr.io":                     {Endpoint: []string{local, near}, Rewrite: map[string]string{"(.*)": "proxy-ghcr/$1"}},
		"quay.io":                     {Endpoint: []string{local, "https://ghcr.io"}, Rewrite: map[string]string{"(.*)": "proxy-quay/$1"}},
		"registry.parent.example.net": {Endpoint: []string{local, parent}},
		"root.example.net":            {Endpoint: []string{local, "https://root.example.net"}},
		"registry.alpha.example.org":  {Endpoint: []string{local}},
		"*":                           {Endpoint: []string{local}},
	}
	if !reflect.DeepEqual(before, want) {
		t.Errorf("before the flip:\n got %+v\nwant %+v", before, want)
	}

	b, err = v.Flipped().RegistriesYAML("", nil)
	if err != nil {
		t.Fatal(err)
	}
	after := parse(b)
	if len(after) != len(want) {
		t.Errorf("after the flip: %d entries, want %d", len(after), len(want))
	}
	for n, m := range after {
		if !reflect.DeepEqual(m.Endpoint, []string{local}) {
			t.Errorf("after the flip %s has %v, want only the local registry", n, m.Endpoint)
		}
	}

	// Two cases stay as they were. An origin host that is not a proxy name
	// keeps its own entry (TestRegistriesYAMLTwoEndpointsThenOne). A local
	// registry host that is a proxy name is still refused.
	clash := mustFromMap(t, with(doc, "LOCAL_REGISTRY_HOST", "quay.io"))
	if _, err := clash.RegistriesYAML("", nil); err == nil || !strings.Contains(err.Error(), `"quay.io" would have two mirror entries`) {
		t.Errorf("a local registry host that is a proxy name must be refused, got: %v", err)
	}
}
