package main

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// contractPath is contract C9, the mutating-route table, read from this
// directory. Its rules are at the head of scripts/generators/mutating-routes.py.
const contractPath = "../../../../../../docs/contracts/C9-mutating-routes.yaml"

// TestMutatingRoutesContract walks the router and fails when a POST, PUT,
// PATCH or DELETE route has no entry of component catalyst-api in contract
// C9, or when such an entry names a route the router does not register. A
// path that the router serves for every method is a mount, which the golden
// file writes as ANY; it is not a route of the table.
func TestMutatingRoutesContract(t *testing.T) {
	data, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read %s: %v", contractPath, err)
	}
	var table struct {
		Routes []struct {
			Component string `yaml:"component"`
			Method    string `yaml:"method"`
			Path      string `yaml:"path"`
		} `yaml:"routes"`
	}
	if err := yaml.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse %s: %v", contractPath, err)
	}
	listed := map[string]bool{}
	for _, r := range table.Routes {
		if r.Component == "catalyst-api" {
			listed[r.Method+" "+r.Path] = true
		}
	}
	methods := map[string][]string{}
	err = chi.Walk(buildRouter(goldenDeps()), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		methods[path] = append(methods[path], method)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}
	served := map[string]bool{}
	for path, ms := range methods {
		sort.Strings(ms)
		if strings.Join(ms, " ") == allMethods {
			continue
		}
		for _, m := range ms {
			switch m {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				served[m+" "+path] = true
			}
		}
	}
	if len(served) == 0 {
		t.Fatal("the router has no POST, PUT, PATCH or DELETE route; the walk is broken")
	}
	for _, c := range []struct {
		these, other map[string]bool
		message      string
	}{
		{served, listed, "registered in the router and missing from " + contractPath},
		{listed, served, "listed in " + contractPath + " and not registered in the router"},
	} {
		var missing []string
		for route := range c.these {
			if !c.other[route] {
				missing = append(missing, route)
			}
		}
		sort.Strings(missing)
		for _, route := range missing {
			t.Errorf("%s: %s", route, c.message)
		}
	}
}
