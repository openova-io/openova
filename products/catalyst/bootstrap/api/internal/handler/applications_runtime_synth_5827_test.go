package handler

import (
	"go/ast"
	"strings"
	"testing"
)

// #5827 (UAT row 188) — GET /applications/{name} must not 404 for a component
// its own sibling endpoint reports on.
//
// `catalyst-api` is a Deployment rendered BY the bp-catalyst-platform
// HelmRelease: no Application CR, no same-named HR. Both existing lookups miss,
// so the detail endpoint 404s — while .../applications/catalyst-api/placement
// answers 200 with a correct single Primary target, because it derives from
// live Pods. The API knew the component and denied it existed, depending on
// which suffix you asked for, and the console rendered "App not found — the
// component catalyst-api is not part of this deployment" for something plainly
// running in front of the caller.
//
// WHY THESE ARE AST TESTS. The behaviour of the new fallback is thin — read
// Pods, match, report. What actually matters, and what a behaviour test over
// the helper would say nothing about, is:
//
//	(a) that HandleApplicationGet REACHES it (the #5434 shape: helper tested,
//	    call site not — delete the one line and every behaviour test stays green);
//	(b) that it decides existence with the SAME predicate the placement path
//	    uses, because the whole defect is two endpoints answering "does this
//	    component exist" differently.
//
// Both are structural properties, so both are asserted structurally.

func TestHandleApplicationGet_ReachesTheRuntimeFallback(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")
	calls := callsWithin(t, f, "HandleApplicationGet")

	// Vacuity: the extractor must see this function's calls at all.
	if len(calls) < 5 {
		t.Fatalf("only %d calls found inside HandleApplicationGet — the AST walk is broken", len(calls))
	}

	if !calls["synthesiseAppFromRuntime"] {
		t.Fatal("HandleApplicationGet no longer calls synthesiseAppFromRuntime — " +
			"a component with neither an Application CR nor a same-named HelmRelease " +
			"(catalyst-api is the canonical case) 404s again, while .../placement keeps " +
			"answering 200 for the same name (#5827, UAT row 188).")
	}

	// Ordering matters as much as presence: the runtime fallback is the LAST
	// resort. If it ran before the HelmRelease lookup, every bootstrap-kit app
	// would lose its chart identity — blueprint, version, parameters — and get
	// the bare runtime shape instead. Asserting only "it is called" would not
	// notice that.
	if !calls["synthesiseAppFromHelmRelease"] {
		t.Fatal("the HelmRelease fallback is gone from HandleApplicationGet — " +
			"the runtime path would then answer for every bootstrap-kit component and " +
			"strip its declared blueprint/version/parameters down to observed Pods.")
	}
}

func TestSynthesiseAppFromRuntime_UsesThePlacementIdentityPredicate(t *testing.T) {
	_, f := parseHandlerFile(t, "applications_runtime_synth_5827.go")
	calls := callsWithin(t, f, "synthesiseAppFromRuntime")

	if len(calls) < 3 {
		t.Fatalf("only %d calls found inside synthesiseAppFromRuntime — the AST walk is broken", len(calls))
	}

	if !calls["podBelongsToComponent"] {
		t.Fatal("synthesiseAppFromRuntime no longer decides existence with " +
			"podBelongsToComponent — that is the predicate .../placement uses, and the " +
			"entire point of #5827 is that the two endpoints must not answer " +
			"\"does this component exist\" differently. A second derivation here will drift.")
	}
	if !calls["podIsReady"] {
		t.Fatal("synthesiseAppFromRuntime no longer uses the shared podIsReady — " +
			"a local copy of \"is this Pod healthy\" is how two surfaces start disagreeing " +
			"about the same Pod.")
	}
}

// Guard the honesty marker. A runtime-derived response has no uid, blueprint,
// version or parameters — there is no declaration to read them from — and a
// consumer must be able to tell "declares nothing" from "declaration not
// fetched" WITHOUT inferring it from which fields happen to be blank.
func TestSynthesiseAppFromRuntime_MarksTheAnswerAsAnObservation(t *testing.T) {
	_, f := parseHandlerFile(t, "applications_runtime_synth_5827.go")

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == "synthesiseAppFromRuntime" {
			fn = fd
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("synthesiseAppFromRuntime not found — this guard is asserting on nothing")
	}

	setsMarker := false
	fabricates := []string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "out" {
				continue
			}
			switch sel.Sel.Name {
			case "RuntimeDerived":
				setsMarker = true
			// Fields that can ONLY come from a declaration. Populating any of
			// them here means the function invented it.
			case "UID", "Blueprint", "Version", "EnvironmentRef", "Parameters", "GiteaRepo":
				fabricates = append(fabricates, sel.Sel.Name)
			}
		}
		return true
	})

	if !setsMarker {
		t.Fatal("synthesiseAppFromRuntime no longer sets RuntimeDerived — its output is now " +
			"indistinguishable from a CR-backed response whose declaration happened to be empty")
	}
	if len(fabricates) > 0 {
		t.Fatalf("synthesiseAppFromRuntime populates declaration-only field(s) %v — "+
			"there is no Application CR and no HelmRelease behind this answer, so any value "+
			"in those fields was invented", fabricates)
	}
}

// The wire field must be `omitempty`, so every CR- and HR-backed response is
// byte-identical to what it was before this change. Without the tag they all
// start carrying `"runtimeDerived":false`, which advertises the mechanism on
// every response and invites someone to key off it in the wrong direction.
func TestApplicationDetailResponse_RuntimeDerivedIsOmitEmpty(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")

	found, omitEmpty := false, false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "applicationDetailResponse" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fl := range st.Fields.List {
			for _, nm := range fl.Names {
				if nm.Name != "RuntimeDerived" || fl.Tag == nil {
					continue
				}
				found = true
				omitEmpty = strings.Contains(fl.Tag.Value, `json:"runtimeDerived,omitempty"`)
			}
		}
		return false
	})

	if !found {
		t.Fatal("applicationDetailResponse has no RuntimeDerived field — the honesty marker never reaches the wire")
	}
	if !omitEmpty {
		t.Fatal(`RuntimeDerived is not tagged json:"runtimeDerived,omitempty" — every CR- and ` +
			`HR-backed response now carries "runtimeDerived":false, changing a shape that had no reason to change`)
	}
}
