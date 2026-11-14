package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// routeVerbs are the chi methods that register one handler for one HTTP
// method and one path pattern.
var routeVerbs = map[string]string{
	"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH",
	"Delete": "DELETE", "Head": "HEAD", "Options": "OPTIONS",
	"Connect": "CONNECT", "Trace": "TRACE",
}

type routeReg struct {
	method, path, handler, at string
}

// routeRegistrations parses every non-test Go file of this package and
// returns each call of the form <router>.<Verb>("<path>", <handler>).
// Asserted against the parsed AST, so a route in a comment or a string
// does not count.
func routeRegistrations(t *testing.T) []routeReg {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	var regs []routeReg
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method, ok := routeVerbs[sel.Sel.Name]
			if !ok {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			path, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			handler := ""
			if hs, ok := call.Args[1].(*ast.SelectorExpr); ok {
				if x, ok := hs.X.(*ast.Ident); ok {
					handler = x.Name + "." + hs.Sel.Name
				}
			}
			regs = append(regs, routeReg{method, path, handler, fset.Position(call.Pos()).String()})
			return true
		})
	}
	return regs
}

// TestChiServesTheLaterOfTwoRegistrations records why a second
// registration of one method and path is never harmless: chi keeps only
// the handler registered last, so the earlier one is dead code that reads
// as if it served the route.
func TestChiServesTheLaterOfTwoRegistrations(t *testing.T) {
	r := chi.NewRouter()
	r.Group(func(rg chi.Router) {
		rg.Put("/x/{name}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("first")) })
		rg.Put("/x/{name}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("second")) })
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x/a", nil))
	if got := rec.Body.String(); got != "second" {
		t.Fatalf("chi served %q; this test expects the later registration to serve", got)
	}
}

// TestNoRouteRegisteredTwice fails when one HTTP method and one path are
// registered twice in this package.
func TestNoRouteRegisteredTwice(t *testing.T) {
	regs := routeRegistrations(t)
	if len(regs) < 200 {
		t.Fatalf("found %d route registrations; expected at least 200, so the parser read the wrong files", len(regs))
	}
	seen := map[string]string{}
	var dups []string
	for _, rr := range regs {
		key := rr.method + " " + rr.path
		if first, ok := seen[key]; ok {
			dups = append(dups, key+" at "+first+" and "+rr.at)
			continue
		}
		seen[key] = rr.at
	}
	sort.Strings(dups)
	for _, d := range dups {
		t.Errorf("registered twice: %s", d)
	}
}

// TestKubernetesResourcePutRoutes pins which handler serves the two PUT
// routes of the resource editor, and that the endpoint pipeline routes
// are gone while the endpoint list stays.
func TestKubernetesResourcePutRoutes(t *testing.T) {
	want := map[string]string{
		"PUT /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}":        "h.HandleK8sResourcePut",
		"PUT /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale":  "h.HandleK8sResourceScale",
		"POST /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply": "h.HandleK8sResourceApply",
		"GET /catalyst/v1/apps/{id}/endpoints":                      "h.HandleListAppEndpoints",
	}
	gone := map[string]bool{
		"POST /catalyst/v1/apps/{id}/endpoints":          true,
		"PATCH /catalyst/v1/apps/{id}/endpoints/{name}":  true,
		"DELETE /catalyst/v1/apps/{id}/endpoints/{name}": true,
	}
	got := map[string][]string{}
	for _, rr := range routeRegistrations(t) {
		key := rr.method + " " + rr.path
		got[key] = append(got[key], rr.handler)
	}
	for key, handler := range want {
		if len(got[key]) != 1 || got[key][0] != handler {
			t.Errorf("%s: registered with %v; want exactly one registration with %s", key, got[key], handler)
		}
	}
	for key := range gone {
		if len(got[key]) != 0 {
			t.Errorf("%s: still registered with %v; the endpoint pipeline is deleted", key, got[key])
		}
	}
}
