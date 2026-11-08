package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ADR-0015 D7 guard — the object is the status, and statusOf (status_of.go)
// is the ONLY handler code that turns an object into a status. This test
// fails the build when non-test handler code
//
//   - assigns a string literal to a field or variable named Phase/phase
//     (`resp.Phase = "Ready"`, `Phase: "Pending"`, `phase := "Healthy"`), or
//   - assigns the literal "Ready" or "Healthy" to anything at all.
//
// Those are exactly the shapes of the 13 status derivations ADR-0015 found
// (the HR-Ready overlay, the HR-synth "Pending" default, the runtime-synth
// pod-ready ⇒ Ready, the live Continuum record defaulting "Healthy", …).
//
// statusDerivationAllowlist is a RATCHET over pre-existing sites that are not
// Application/Continuum status (Organization boundary phases, provisioning
// phases, etc.). It may only shrink: a new site fails, and an allowlisted
// file whose count drops below its entry fails too, so the entry is lowered
// in the same PR that removes the site.
var statusDerivationAllowlist = map[string]int{
	// Endpoint-card / launch readiness (owned by the endpoint slice; this PR
	// must not edit endpoint_handler.go). Tracked for the D7 follow-up.
	"endpoint_handler.go": 2,
	// mapNode: a Kubernetes Node row's own NodeReady condition rendered as
	// Ready/NotReady/Unknown — a node, not an Application/Continuum status.
	"sovereign.go": 1,
}

// statusVocabulary is the set of status words a Phase literal may not be
// assigned from outside statusOf. Event phase NAMES ("pdm-commit",
// "post-handover") are not statuses and are not flagged.
var statusVocabulary = map[string]bool{
	"Pending": true, "Provisioning": true, "Ready": true, "Degraded": true,
	"Failed": true, "Healthy": true, "Installed": true, "Reconciling": true,
	"Unknown": true, "NotReady": true,
}

func statusLiteral(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

func phaseName(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "Phase" || x.Name == "phase"
	case *ast.SelectorExpr:
		return x.Sel.Name == "Phase"
	}
	return false
}

func isVerdictLiteral(v string) bool { return v == "Ready" || v == "Healthy" }

// statusDerivationSites returns "file:line: snippet" for every offending
// assignment in the given Go source.
func statusDerivationSites(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	add := func(pos token.Pos, what string) {
		p := fset.Position(pos)
		out = append(out, filepath.Base(p.Filename)+":"+strconv.Itoa(p.Line)+": "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				v, ok := statusLiteral(rhs)
				if !ok || i >= len(x.Lhs) {
					continue
				}
				if (phaseName(x.Lhs[i]) && statusVocabulary[v]) || isVerdictLiteral(v) {
					add(x.Pos(), "assigns "+strconv.Quote(v))
				}
			}
		case *ast.KeyValueExpr:
			v, ok := statusLiteral(x.Value)
			if !ok {
				return true
			}
			if k, isIdent := x.Key.(*ast.Ident); isIdent && ((k.Name == "Phase" && statusVocabulary[v]) || isVerdictLiteral(v)) {
				add(x.Pos(), k.Name+": "+strconv.Quote(v))
			}
		case *ast.ValueSpec:
			for i, val := range x.Values {
				v, ok := statusLiteral(val)
				if !ok || i >= len(x.Names) {
					continue
				}
				if (x.Names[i].Name == "phase" || x.Names[i].Name == "Phase") && statusVocabulary[v] {
					add(x.Pos(), "var "+x.Names[i].Name+" = "+strconv.Quote(v))
				}
			}
		}
		return true
	})
	return out
}

func TestStatusOfIsTheOnlyStatusDerivation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	perFile := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "status_of.go" {
			continue
		}
		scanned++
		if sites := statusDerivationSites(t, f); len(sites) > 0 {
			perFile[f] = sites
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d handler files — the glob is broken (vacuity guard)", scanned)
	}
	var fails []string
	for f, sites := range perFile {
		if allowed, ok := statusDerivationAllowlist[f]; ok && len(sites) <= allowed {
			if len(sites) < allowed {
				fails = append(fails, f+": allowlisted "+strconv.Itoa(allowed)+" but only "+strconv.Itoa(len(sites))+" remain — lower the ratchet")
			}
			continue
		}
		fails = append(fails, sites...)
	}
	for f, allowed := range statusDerivationAllowlist {
		if _, ok := perFile[f]; !ok && allowed > 0 {
			if _, statErr := os.Stat(f); statErr == nil {
				fails = append(fails, f+": allowlisted "+strconv.Itoa(allowed)+" but 0 remain — delete the entry")
			}
		}
	}
	sort.Strings(fails)
	if len(fails) > 0 {
		t.Fatalf("ADR-0015 D7: status must come from statusOf (status_of.go), not a literal:\n  %s",
			strings.Join(fails, "\n  "))
	}
}

// The guard must be able to fail: each derivation shape ADR-0015 deleted is
// detected when fed back in.
func TestStatusOfGuard_DetectsTheDeletedShapes(t *testing.T) {
	src := `package handler
func a(resp *applicationDetailResponse) { resp.Phase = "Ready" }
func b() { phase := "Pending"; _ = phase }
func c() map[string]string { return map[string]string{"x": "y"} }
func d() continuumGetResponse { return continuumGetResponse{Phase: "Healthy"} }
func e() { s := "Healthy"; _ = s }
`
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := statusDerivationSites(t, p)
	if len(got) != 4 {
		t.Fatalf("guard detected %d sites, want 4 (overlay, Pending default, Healthy composite, Healthy var): %v", len(got), got)
	}
}
