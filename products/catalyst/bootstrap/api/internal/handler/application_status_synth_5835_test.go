package handler

import (
	"encoding/json"
	"go/ast"
	"testing"
)

// #5835 (UAT rows 67 + 69) — GET /applications/{name}/status must answer for a
// component whose sibling GET /applications/{name} already does.
//
// The status endpoint read the Application CR and nothing else, so a
// bootstrap-kit component (a HelmRelease with no companion CR — bp-grafana,
// bp-keycloak, every spine slot) got a hard 404 while the detail endpoint
// returned a complete synthesised answer. The console's Topology tab reacted by
// disabling the poll entirely and printing "n/a — bootstrap component
// (HelmRelease, no Application CR)".
//
// One page then contradicted itself. Rows 67 and 69 both measured the header
// and Overview reading STATUS Ready — from the synthesised detail response —
// while the Status panel three inches below said there was no status to report.
// Both rows name that panel as their single remaining residual.
//
// WHY THESE ARE AST TESTS. The projection is thin: copy four fields. What
// matters, and what a behaviour test over the helper would say nothing about,
// is (a) that the 404 branch REACHES it — the #5434 shape, delete one line and
// every behaviour test stays green — and (b) that it DELEGATES to the existing
// synthesisers instead of growing a third derivation of "what is this
// component's status", which is precisely how the detail and status paths came
// to disagree in front of an operator.

func TestHandleApplicationStatus_ReachesTheSynthesisedFallback(t *testing.T) {
	_, f := parseHandlerFile(t, "applications.go")
	calls := callsWithin(t, f, "HandleApplicationStatus")

	// Vacuity: the walk must actually see this function's calls.
	if len(calls) < 5 {
		t.Fatalf("only %d calls found inside HandleApplicationStatus — the AST walk is broken", len(calls))
	}

	if !calls["statusFromSynthesised"] {
		t.Fatal("HandleApplicationStatus no longer calls statusFromSynthesised — " +
			"every bootstrap-kit component 404s again on /status while its own " +
			"/applications/{name} returns a full answer, and the Topology tab goes back to " +
			"printing \"n/a — bootstrap component\" on a page whose header reads Ready " +
			"(#5835, UAT rows 67 + 69).")
	}
}

func TestStatusFromSynthesised_DelegatesToBothExistingSynthesisers(t *testing.T) {
	_, f := parseHandlerFile(t, "application_status_synth_5835.go")
	calls := callsWithin(t, f, "statusFromSynthesised")

	if !calls["synthesiseAppFromHelmRelease"] {
		t.Fatal("statusFromSynthesised no longer delegates to synthesiseAppFromHelmRelease — " +
			"if it derives a phase from the HelmRelease itself there are now THREE ways to " +
			"compute one component's status, and two was already enough for the detail and " +
			"status paths to disagree in front of an operator.")
	}
	if !calls["synthesiseAppFromRuntime"] {
		t.Fatal("statusFromSynthesised no longer falls through to synthesiseAppFromRuntime — " +
			"a component that is neither a CR nor a same-named HelmRelease (catalyst-api, " +
			"#5827) answers on /applications/{name} but 404s on /status again.")
	}
}

// The HR path must be tried BEFORE the runtime path, mirroring the detail
// endpoint's ordering. Runtime-first would strip a bootstrap component's
// declared identity down to observed Pods on this endpoint while the detail
// endpoint still reports it — reintroducing a disagreement in the other
// direction.
func TestStatusFromSynthesised_TriesHelmReleaseBeforeRuntime(t *testing.T) {
	_, f := parseHandlerFile(t, "application_status_synth_5835.go")

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == "statusFromSynthesised" {
			fn = fd
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("statusFromSynthesised not found — this guard is asserting on nothing")
	}

	var order []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "synthesiseAppFromHelmRelease", "synthesiseAppFromRuntime":
			order = append(order, sel.Sel.Name)
		}
		return true
	})

	if len(order) != 2 {
		t.Fatalf("expected exactly one call to each synthesiser, saw %v", order)
	}
	if order[0] != "synthesiseAppFromHelmRelease" {
		t.Fatalf("runtime synthesis is attempted first (%v) — a bootstrap component's declared "+
			"identity would be reduced to observed Pods here while the detail endpoint still "+
			"reports it, which is the same disagreement in the other direction", order)
	}
}

// The wire shape this path must not break: `conditions` is never JSON `null`.
//
// My first cut asserted the key is always PRESENT. That was wrong and the
// struct tag says so — `conditions` carries `omitempty`, so an empty slice is
// omitted entirely, on the CR-backed path as much as this one. Callers already
// tolerate an absent key; what they cannot tolerate is `null` where a list is
// expected. Asserting the stronger claim would have pinned a contract the
// existing code does not honour, and the first thing it would have broken is
// the CR path this change does not touch.
func TestStatusFromSynthesised_ConditionsIsNeverJSONNull(t *testing.T) {
	// Nil in, normalised out — the case the helper exists to handle.
	got, _ := json.Marshal(applicationStatusResponse{
		Name: "bp-grafana", Namespace: "flux-system", Phase: "Ready",
		Conditions: []map[string]interface{}{},
	})
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, present := m["conditions"]; present && v == nil {
		t.Fatalf(`"conditions" serialised as null: %s — callers index it as a list`, got)
	}

	// Control: a populated slice DOES serialise, so the assertion above is not
	// passing merely because omitempty drops the field in every case.
	got2, _ := json.Marshal(applicationStatusResponse{
		Conditions: []map[string]interface{}{{"type": "Ready", "status": "True"}},
	})
	var m2 map[string]any
	if err := json.Unmarshal(got2, &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	arr, ok := m2["conditions"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("populated conditions did not serialise as a 1-element array: %s", got2)
	}
}
