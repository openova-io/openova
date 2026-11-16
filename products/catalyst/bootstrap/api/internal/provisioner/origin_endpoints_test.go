package provisioner

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
	"sigs.k8s.io/yaml"
)

// SOV-135: the variables document of a new Sovereign names an origin address
// for every proxy name, and the renderer of the node registry file accepts it.
// The provider name of the second case is made up: this package cannot import
// a provider adapter. The adapter's own test asserts the address it registers.
func TestChildVariablesOriginAddresses(t *testing.T) {
	const (
		apiURL     = "https://console.openova.io/sovereign"
		parent     = "https://harbor.openova.io"
		near       = "http://registry.nearcloud.example:5000"
		xpkgOrigin = "https://xpkg.upbound.io"
		local      = "https://registry.t31.omani.works"
	)
	wantProjects := map[string]string{
		"docker.io":            "proxy-dockerhub",
		"registry-1.docker.io": "proxy-dockerhub",
		"quay.io":              "proxy-quay",
		"gcr.io":               "proxy-gcr",
		"registry.k8s.io":      "proxy-k8s",
		"ghcr.io":              "proxy-ghcr",
		"harbor.openova.io":    "",
		"xpkg.upbound.io":      "",
	}
	OriginRegistryByProvider["nearcloud"] = near
	t.Cleanup(func() { delete(OriginRegistryByProvider, "nearcloud") })

	for _, tc := range []struct {
		name, provider, origin string
	}{
		{"no provider name", "", parent},
		{"a provider without an address of its own", "plaincloud", parent},
		{"a provider with an address of its own", "nearcloud", near},
		{"the same provider, name not normalised", " NearCloud ", near},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{SovereignFQDN: "t31.omani.works", Provider: tc.provider}
			doc, err := childVariables(req, apiURL)
			if err != nil {
				t.Fatalf("childVariables: %v", err)
			}
			checkVariablesDocument(t, tc.name, doc, req.SovereignFQDN)

			var projects, endpoints map[string]string
			if err := json.Unmarshal([]byte(doc["REGISTRY_PROXY_PROJECTS_JSON"]), &projects); err != nil {
				t.Fatalf("REGISTRY_PROXY_PROJECTS_JSON: %v", err)
			}
			if !reflect.DeepEqual(projects, wantProjects) {
				t.Errorf("REGISTRY_PROXY_PROJECTS_JSON = %v, want %v", projects, wantProjects)
			}
			if err := json.Unmarshal([]byte(doc["ORIGIN_PROXY_ENDPOINTS_JSON"]), &endpoints); err != nil {
				t.Fatalf("ORIGIN_PROXY_ENDPOINTS_JSON: %v", err)
			}
			wantEndpoints := map[string]string{}
			for name := range wantProjects {
				wantEndpoints[name] = tc.origin
			}
			wantEndpoints["xpkg.upbound.io"] = xpkgOrigin
			if !reflect.DeepEqual(endpoints, wantEndpoints) {
				t.Errorf("ORIGIN_PROXY_ENDPOINTS_JSON = %v, want %v", endpoints, wantEndpoints)
			}
			if got := doc["REGISTRY_PATH_XPKG"]; got != "proxy-xpkg" {
				t.Errorf("REGISTRY_PATH_XPKG = %q, want proxy-xpkg: this change must not move the package path", got)
			}

			v, err := upstream.FromMap(doc)
			if err != nil {
				t.Fatalf("FromMap: %v", err)
			}
			raw, err := v.RegistriesYAML("", nil)
			if err != nil {
				t.Fatalf("the renderer refuses the document: %v", err)
			}
			var file struct {
				Mirrors map[string]struct {
					Endpoint []string          `json:"endpoint"`
					Rewrite  map[string]string `json:"rewrite"`
				} `json:"mirrors"`
			}
			if err := yaml.UnmarshalStrict(raw, &file); err != nil {
				t.Fatalf("not a registries file: %v\n%s", err, raw)
			}
			// The eight proxy names, the local registry host and "*". The origin
			// host ghcr.io is a proxy name, so it has no entry of its own.
			if len(file.Mirrors) != len(wantProjects)+2 {
				t.Errorf("%d mirror entries, want %d:\n%s", len(file.Mirrors), len(wantProjects)+2, raw)
			}
			for name, path := range wantProjects {
				m, ok := file.Mirrors[name]
				if !ok {
					t.Errorf("no mirror entry for %s", name)
					continue
				}
				if want := []string{local, wantEndpoints[name]}; !reflect.DeepEqual(m.Endpoint, want) {
					t.Errorf("%s: endpoints %v, want %v", name, m.Endpoint, want)
				}
				if path == "" && m.Rewrite != nil {
					t.Errorf("%s is served at the root and must have no rewrite: %v", name, m.Rewrite)
				}
				if path != "" && !reflect.DeepEqual(m.Rewrite, map[string]string{"(.*)": path + "/$1"}) {
					t.Errorf("%s: rewrite %v, want (.*) to %s/$1", name, m.Rewrite, path)
				}
			}
			for _, name := range []string{"registry.t31.omani.works", "*"} {
				if m := file.Mirrors[name]; !reflect.DeepEqual(m.Endpoint, []string{local}) || m.Rewrite != nil {
					t.Errorf("%s: %+v, want the local registry only and no rewrite", name, m)
				}
			}
		})
	}
}
