package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestMainWiresDrainOnSignal is the guard the other drain tests cannot be.
//
// drainOnSignal was extracted from main() precisely so its ordering could be
// tested without delivering a real signal to the test process (#5767). That
// extraction bought testability at the cost of a seam: every behavioural test
// in drain_test.go calls drainOnSignal DIRECTLY, so deleting the one call site
// in main() leaves all of them green while the shipped binary goes straight
// back to the abrupt SIGTERM death the ticket was filed for.
//
// Asserted against the parsed AST rather than the file text, so a mention of
// drainOnSignal in a comment or a string literal cannot satisfy it — the call
// has to be a real CallExpr reachable from main's body.
func TestMainWiresDrainOnSignal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var mainFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "main" {
			mainFn = fn
			break
		}
	}
	if mainFn == nil {
		t.Fatal("no func main in main.go")
	}

	var (
		callsDrain   bool
		passesBudget bool
		notifiesTerm bool
	)
	ast.Inspect(mainFn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name != "drainOnSignal" {
				return true
			}
			callsDrain = true
			// The budget must be the shared constant, not a literal that can
			// drift past terminationGracePeriodSeconds without tripping
			// TestDefaultDrainBudget_FitsKubernetesGracePeriod.
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && id.Name == "defaultDrainBudget" {
					passesBudget = true
				}
			}
		case *ast.SelectorExpr:
			// signal.Notify(..., syscall.SIGTERM) — without SIGTERM the drain
			// never fires under Kubernetes, which sends SIGTERM, not SIGINT.
			if fn.Sel.Name != "Notify" {
				return true
			}
			for _, arg := range call.Args {
				if sel, ok := arg.(*ast.SelectorExpr); ok && sel.Sel.Name == "SIGTERM" {
					notifiesTerm = true
				}
			}
		}
		return true
	})

	if !callsDrain {
		t.Fatal("main() does not call drainOnSignal — the graceful shutdown is dead code and " +
			"SIGTERM kills orphan-release mid-persistDeployment again (#5767, #489 subdomain lock)")
	}
	if !passesBudget {
		t.Fatal("main() calls drainOnSignal without passing defaultDrainBudget — a literal budget " +
			"escapes the grace-period guard in TestDefaultDrainBudget_FitsKubernetesGracePeriod")
	}
	if !notifiesTerm {
		t.Fatal("main() does not register syscall.SIGTERM — Kubernetes sends SIGTERM on pod " +
			"termination, so the drain would never run in the environment it exists for")
	}
}
