package handler

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// #5814 (UAT row 15) — the sovereign /apps projection must carry the
// Organization each instance belongs to.
//
// WHAT ROW 15 ACTUALLY ASSERTS: "a customer-launched app card would appear
// UNDER ITS ORG". The walk on hw292 found 46 cards, every one BOOTSTRAP-badged,
// and recorded "no Org grouping, no scope filter". That second clause was not a
// listing failure — it was a PROJECTION failure. sovereignAppItem carried
// (id, slug, blueprint, topology, contextCount) and nothing about ownership, so
// even a correctly-listed customer Application had nothing on the wire for the
// grid to attribute it by.
//
// WHY THE ASSERTION IS ON THE JSON AND NOT THE STRUCT FIELD. A struct field
// named Org proves nothing: drop the tag, rename it, mark it `json:"-"`, and
// every field-level test still passes while the FE receives no `org` key. The
// contract that matters here is the wire, so that is what is pinned — including
// the exact key name the FE reads.

func TestSovereignAppItem_CarriesOrgOnTheWire(t *testing.T) {
	b, err := json.Marshal(sovereignAppItem{
		ID:       "uatco-agenity",
		Slug:     "agenity",
		Status:   "installed",
		Instance: true,
		Org:      "uatco",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["org"] != "uatco" {
		t.Fatalf(`instance row did not serialise org="uatco" — the FE reads the "org" key `+
			`verbatim, so a rename or a dropped json tag silently removes Org attribution `+
			`from every card (#5814, UAT row 15). Got: %s`, b)
	}
}

// Control for the test above: with no Org the key must be ABSENT, not "".
//
// This is the half that keeps the chip honest. `omitempty` is what lets the FE
// distinguish "this Application declares no organizationRef" from "it belongs to
// an Org named empty-string". Without it every legacy CR would ship `"org":""`
// and the render check (`org ? <chip> : null`) would still suppress the chip —
// so the bug would be invisible until something downstream started grouping by
// the key and produced a phantom empty group.
func TestSovereignAppItem_OmitsOrgWhenUnset(t *testing.T) {
	b, err := json.Marshal(sovereignAppItem{ID: "bp-harbor", Slug: "harbor", Status: "bootstrap"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["org"]; present {
		t.Fatalf(`row with no organizationRef still carries an "org" key — omitempty was `+
			`dropped, so "unattributed" and "attributed to nothing" became `+
			`indistinguishable downstream: %s`, b)
	}
}

// TestHandleSovereignApps_PopulatesOrgFromOrganizationRef pins the CALL SITE.
//
// This is the guard the repo keeps needing. The two tests above prove the WIRE
// SHAPE works; neither would notice if `Org: orgRef` were deleted from the
// projection loop — the field would still marshal, the omitempty control would
// still pass, and every card would silently lose its attribution again. That is
// the exact failure mode #5434 documented: the helper is tested, the call site
// is not, so removing the one line that does the work leaves the suite green.
//
// Two things are pinned, both structural:
//
//  1. HandleSovereignApps reads spec.organizationRef. The ARGUMENTS are asserted,
//     not just the call — `NestedString` appears many times in this function, so
//     "it calls NestedString" is a check that cannot go red.
//  2. Some sovereignAppItem literal inside HandleSovereignApps sets Org. Without
//     this, the read could survive while the assignment is dropped.
func TestHandleSovereignApps_PopulatesOrgFromOrganizationRef(t *testing.T) {
	_, f := parseHandlerFile(t, "sovereign.go")

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == "HandleSovereignApps" {
			fn = fd
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("HandleSovereignApps not found — this guard is asserting on nothing")
	}

	readsOrgRef := false
	setsOrgField := false
	litCount := 0

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NestedString" {
				for _, a := range call.Args {
					if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING &&
						strings.Contains(bl.Value, "organizationRef") {
						readsOrgRef = true
					}
				}
			}
		}
		if cl, ok := n.(*ast.CompositeLit); ok {
			id, ok := cl.Type.(*ast.Ident)
			if !ok || id.Name != "sovereignAppItem" {
				return true
			}
			litCount++
			for _, e := range cl.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Org" {
					setsOrgField = true
				}
			}
		}
		return true
	})

	// Vacuity: this function builds several sovereignAppItem literals (the
	// Application-CR instance pass, the shareable-HR fallback, the bootstrap
	// slot rows, the listed-catalog rows). Seeing none means the walk broke
	// and both assertions below would pass on an empty scan.
	if litCount < 3 {
		t.Fatalf("only %d sovereignAppItem literals found in HandleSovereignApps — the AST walk is broken", litCount)
	}

	if !readsOrgRef {
		t.Fatal("HandleSovereignApps no longer reads spec.organizationRef — " +
			"the Org chip has no source, so every customer-launched app card goes back to " +
			"rendering anonymously among the spine (#5814, UAT row 15). Note the namespace " +
			"and the CR-name prefix are NOT substitutes: they disagree with organizationRef " +
			"for spine CRs, so a guess here is worse than an absent chip.")
	}
	if !setsOrgField {
		t.Fatal("no sovereignAppItem literal in HandleSovereignApps sets Org — " +
			"the field still marshals and its omitempty control still passes, so the wire-shape " +
			"tests in this file cannot catch this. Every instance row would ship without " +
			"attribution while the suite stays green.")
	}
}
