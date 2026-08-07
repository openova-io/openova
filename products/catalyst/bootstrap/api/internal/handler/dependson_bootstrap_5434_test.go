package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// #5434 (UAT row 4) — the App-detail Dependencies panel renders nothing for a
// bootstrap-synthesised Application.
//
// WHY THIS IS AN AST TEST AND NOT A BEHAVIOUR TEST. The defect was never that
// the resolver was wrong. `dependsOnFromHRGraph` was correct the whole time —
// it was simply never CALLED on this path, because it lives in
// `synthesiseAppFromHelmRelease`, which only runs when the Application CR is
// ABSENT. `spine-harbor` HAS a CR, so it took `HandleApplicationGet`, found no
// `spec.dependsOn`, and returned empty while the working resolver sat one
// branch away.
//
// That is the failure mode this repo keeps hitting: the helper is tested, the
// CALL SITE is not, so deleting the single line that invokes it leaves every
// test green. A behaviour test over `dependsOnForBootstrapCR` would have the
// same hole — it would prove the helper works while saying nothing about
// whether anything reaches it. So the assertion is on the wiring itself.
//
// The check is deliberately narrow: HandleApplicationGet must call
// dependsOnForBootstrapCR, and that helper must delegate to
// dependsOnFromHRGraph rather than growing a second derivation of the same
// edge. Two ways to derive one fact is how these paths drifted apart to begin
// with.

func parseHandlerFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return fset, f
}

// callsWithin reports the set of function names called inside funcName.
func callsWithin(t *testing.T, f *ast.File, funcName string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != funcName {
			return true
		}
		found = true
		ast.Inspect(fd.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				out[fn.Sel.Name] = true
			case *ast.Ident:
				out[fn.Name] = true
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatalf("function %s not found — this guard is asserting on nothing", funcName)
	}
	return out
}

func TestHandleApplicationGet_CallsBootstrapDependsOnFallback(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")

	calls := callsWithin(t, f, "HandleApplicationGet")

	// Vacuity: the extractor must actually see this function's calls. If the
	// walk silently found nothing, every assertion below passes on an empty set.
	if len(calls) < 5 {
		t.Fatalf("only %d calls found inside HandleApplicationGet — the AST walk is broken", len(calls))
	}

	if !calls["dependsOnForBootstrapCR"] {
		t.Fatal("HandleApplicationGet no longer calls dependsOnForBootstrapCR — " +
			"a bootstrap-synthesised Application has no spec.dependsOn, so without this " +
			"fallback the App-detail Dependencies panel renders empty again (#5434, UAT row 4). " +
			"The resolver being correct does not help if nothing reaches it.")
	}
}

func TestDependsOnForBootstrapCR_DelegatesToTheExistingResolver(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")

	calls := callsWithin(t, f, "dependsOnForBootstrapCR")

	if !calls["dependsOnFromHRGraph"] {
		t.Fatal("dependsOnForBootstrapCR no longer delegates to dependsOnFromHRGraph — " +
			"if it has grown its own edge derivation, there are now TWO ways to compute the " +
			"same dependency and they will drift, which is exactly how the CR path and the " +
			"HR path diverged in #5434.")
	}

	// It must read the HR reference off the CR; without that it cannot find
	// which HelmRelease to resolve and would silently return nil forever.
	if !calls["NestedString"] {
		t.Fatal("dependsOnForBootstrapCR no longer reads spec.helmRelease off the CR — " +
			"it cannot locate the HelmRelease whose Flux graph carries the edges.")
	}
}

// TestDependsOnForBootstrapCR_FailsSilentlyWithoutCache pins the safety
// property: every miss must yield nil, never a fabricated edge. The caller only
// reaches this helper when it has nothing to show, so inventing a dependency
// would be worse than the blank panel being fixed.
func TestDependsOnForBootstrapCR_FailsSilentlyWithoutCache(t *testing.T) {
	h := &Handler{} // no k8sCache
	if got := h.dependsOnForBootstrapCR(nil, "dep-1", nil, "bp-harbor"); got != nil {
		t.Fatalf("expected nil with no k8sCache, got %v — a miss must never manufacture an edge", got)
	}
}

// Guard the comment that carries the reasoning. The "why not invert the
// producer map" decision is the part a future author is most likely to undo,
// because inverting looks like the obvious fix and the issue itself proposed it.
func TestDependsOnFallback_RecordsWhyNotProducerInversion(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")
	var text strings.Builder
	for _, cg := range f.Comments {
		text.WriteString(cg.Text())
	}
	body := text.String()
	if !strings.Contains(body, "#5434") {
		t.Fatal("the #5434 reasoning comment is gone from applications.go")
	}
	if !strings.Contains(body, "parameters.databases") {
		t.Fatal("the note explaining why the producer map is NOT inverted has been removed — " +
			"without it the next author re-adds a second derivation of the same edge")
	}
}
