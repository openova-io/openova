package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/oidcprovider"
)

// goldenPath holds the route table, sorted. A line "chain <n>: <names>"
// names one middleware chain; a line "<METHOD> <path> <handler> <n>" names
// one route and the chain that wraps it. METHOD is ANY when one handler
// serves all nine methods. A name is the Go name after the last "/" of its
// package path, or the Go type of a handler that is not a function. A
// function literal is "<F>.(closure)", F the innermost exported function
// it was made in, or "main.(func literal)": the compiler names a literal
// by where it is declared and inlined, which changes when code moves. A
// route whose request resolves to another pattern ends in " => <pattern>".
const goldenPath = "testdata/routes.golden"

var (
	pathParam      = regexp.MustCompile(`\{[^}]*\}`)
	closureSegment = regexp.MustCompile(`^(func[0-9]+|[0-9]+)$`)
	allMethods     = "CONNECT DELETE GET HEAD OPTIONS PATCH POST PUT TRACE"
)

func funcName(pc uintptr) string {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "?"
	}
	name := f.Name()
	name = name[strings.LastIndex(name, "/")+1:]
	segs := strings.Split(name, ".")
	closure := false
	for _, s := range segs[1:] {
		closure = closure || closureSegment.MatchString(s)
	}
	if !closure {
		return name
	}
	for i := len(segs) - 1; i >= 1; i-- {
		if s := segs[i]; s != "" && s[0] >= 'A' && s[0] <= 'Z' {
			return s + ".(closure)"
		}
	}
	return "main.(func literal)"
}

func handlerName(h http.Handler) string {
	if fn, ok := h.(http.HandlerFunc); ok {
		return funcName(reflect.ValueOf(fn).Pointer())
	}
	return reflect.TypeOf(h).String()
}

// routeTable walks the router and returns its golden lines, sorted.
func routeTable(r chi.Router) ([]string, error) {
	type route struct{ path, handler, chain, found string }
	methods := map[route][]string{}
	err := chi.Walk(r, func(method, path string, h http.Handler, mws ...func(http.Handler) http.Handler) error {
		names := make([]string, 0, len(mws))
		for _, mw := range mws {
			names = append(names, funcName(reflect.ValueOf(mw).Pointer()))
		}
		concrete := strings.ReplaceAll(pathParam.ReplaceAllString(path, "x"), "*", "x")
		key := route{path, handlerName(h), strings.Join(names, " "), r.Find(chi.NewRouteContext(), method, concrete)}
		methods[key] = append(methods[key], method)
		return nil
	})
	chains := map[string]int{}
	var chainList, lines []string
	for key := range methods {
		if chains[key.chain] == 0 {
			chains[key.chain] = 1
			chainList = append(chainList, key.chain)
		}
	}
	sort.Strings(chainList)
	for n, chain := range chainList {
		chains[chain] = n + 1
		lines = append(lines, fmt.Sprintf("chain %d: %s", n+1, chain))
	}
	for key, ms := range methods {
		sort.Strings(ms)
		if strings.Join(ms, " ") == allMethods {
			ms = []string{"ANY"}
		}
		for _, m := range ms {
			line := fmt.Sprintf("%s %s %s %d", m, key.path, key.handler, chains[key.chain])
			if key.found != key.path {
				line += " => " + key.found
			}
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines, err
}

// goldenDeps turns on the OIDC routes. The pprof routes come from chi and
// are left out of the table; TestRoutesPprofMount covers them.
func goldenDeps() routeDeps {
	return routeDeps{
		corsOrigin: "*",
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		h:          &handler.Handler{},
		oidcProv:   &oidcprovider.Provider{},
	}
}

// TestRoutesPprofMount: the pprof routes exist only when main() turns them on.
func TestRoutesPprofMount(t *testing.T) {
	for _, on := range []bool{false, true} {
		d := goldenDeps()
		d.pprofEnabled = on
		served := buildRouter(d).Find(chi.NewRouteContext(), http.MethodGet, "/debug/pprof/heap") != ""
		if served != on {
			t.Errorf("pprofEnabled=%v: GET /debug/pprof/heap served=%v", on, served)
		}
	}
}

// TestRoutesGolden fails when a route, its handler or its middleware changes.
// A change that adds, removes or rewires a route edits the golden file too.
func TestRoutesGolden(t *testing.T) {
	got, err := routeTable(buildRouter(goldenDeps()))
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	want := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for _, c := range []struct {
		where        string
		lines, other []string
	}{{"golden file", want, got}, {"router", got, want}} {
		in := map[string]bool{}
		for _, line := range c.other {
			in[line] = true
		}
		for _, line := range c.lines {
			if !in[line] {
				t.Errorf("only in the %s: %s", c.where, line)
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("the router has %d lines, %s has %d", len(got), goldenPath, len(want))
	}
}
