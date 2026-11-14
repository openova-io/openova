package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// mothershipOnly is the closed list of provisioning routes that answer 403
// not-in-mothership-mode in mode sovereign.
var mothershipOnly = []string{
	"DELETE /api/v1/deployments/{id}",
	"DELETE /api/v1/deployments/{id}/release-subdomain",
	"GET /api/v1/deployments/{id}/kubeconfig",
	"POST /api/v1/deployments",
	"POST /api/v1/deployments/{id}/mint-handover-token",
	"POST /api/v1/deployments/{id}/phases/{phase}/retry",
	"POST /api/v1/deployments/{id}/republish-dns",
	"POST /api/v1/deployments/{id}/wipe",
	"POST /api/v1/registrar/{registrar}/set-ns",
}

// consoleReads are routes the console of a Sovereign and the acceptance
// suite read. They stay open in mode sovereign.
var consoleReads = []string{
	"GET /api/v1/deployments/{id}",
	"GET /api/v1/deployments/{id}/events",
	"GET /api/v1/deployments/{depId}/jobs",
	"GET /api/v1/deployments/{depId}/jobs/{jobId}",
	"GET /api/v1/deployments/{depId}/components/state",
	"GET /api/v1/deployments/{depId}/reconcilers",
	"GET /api/v1/deployments/{depId}/reconciliation",
	"GET /api/v1/deployments/{depId}/infrastructure/topology",
	"GET /api/v1/sovereign/jobs",
}

const gateName = "sovself.(*Route).MothershipOnly-fm"

// walked is one route of the router: its middleware chain and its names.
type walked struct {
	mws   []func(http.Handler) http.Handler
	names []string
}

func walkRoutes(t *testing.T) map[string]walked {
	t.Helper()
	routes := map[string]walked{}
	err := chi.Walk(buildRouter(goldenDeps()), func(method, path string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		w := walked{mws: mws}
		for _, mw := range mws {
			w.names = append(w.names, funcName(reflect.ValueOf(mw).Pointer()))
		}
		routes[method+" "+path] = w
		return nil
	})
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}
	return routes
}

// TestMothershipOnlyRoutes: the routes wrapped by MothershipOnly are exactly
// the closed list.
func TestMothershipOnlyRoutes(t *testing.T) {
	var got []string
	for route, w := range walkRoutes(t) {
		for _, name := range w.names {
			if name == gateName {
				got = append(got, route)
			}
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(mothershipOnly, "\n") {
		t.Errorf("routes wrapped by MothershipOnly:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(mothershipOnly, "\n"))
	}
}

// serve sends one request for route through the route's own middleware
// chain to a stand-in for its handler, and returns the status, the body and
// whether the stand-in ran.
func serve(t *testing.T, routes map[string]walked, route string) (int, string, bool) {
	t.Helper()
	w, ok := routes[route]
	if !ok {
		t.Fatalf("%s: not registered", route)
	}
	reached := false
	endpoint := http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		reached = true
		rw.WriteHeader(http.StatusTeapot)
	})
	method, path, _ := strings.Cut(route, " ")
	concrete := strings.ReplaceAll(pathParam.ReplaceAllString(path, "x"), "*", "x")
	rec := httptest.NewRecorder()
	chi.Chain(w.mws...).Handler(endpoint).ServeHTTP(rec, httptest.NewRequest(method, concrete, nil))
	return rec.Code, rec.Body.String(), reached
}

// TestMothershipOnlyRoutesBySovereignMode: in mode sovereign each route of
// the closed list answers 403 not-in-mothership-mode and its handler does
// not run; in mode mothership each reaches its handler unchanged; the
// console's read routes reach their handler in both modes. Outside a
// cluster the mode follows SOVEREIGN_FQDN: set is a Sovereign, empty is
// the mothership.
func TestMothershipOnlyRoutesBySovereignMode(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	for _, mode := range []struct {
		name, fqdn string
		refused    bool
	}{{"sovereign", "alpha.example.org", true}, {"mothership", "", false}} {
		t.Setenv("SOVEREIGN_FQDN", mode.fqdn)
		routes := walkRoutes(t)
		for _, route := range mothershipOnly {
			code, body, reached := serve(t, routes, route)
			if !mode.refused {
				if code != http.StatusTeapot || !reached {
					t.Errorf("mode %s, %s: status %d, handler ran %v; want the handler", mode.name, route, code, reached)
				}
				continue
			}
			if code != http.StatusForbidden || reached || body != "{\"error\":\"not-in-mothership-mode\"}\n" {
				t.Errorf("mode %s, %s: status %d, handler ran %v, body %q; want 403 not-in-mothership-mode", mode.name, route, code, reached, body)
			}
		}
		for _, route := range consoleReads {
			if code, _, reached := serve(t, routes, route); code != http.StatusTeapot || !reached {
				t.Errorf("mode %s, %s: status %d, handler ran %v; want the handler", mode.name, route, code, reached)
			}
		}
	}
}
