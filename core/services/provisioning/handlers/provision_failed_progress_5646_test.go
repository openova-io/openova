package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// TestMarkProvisionFailed_ProgressNeverStaysHundred is the behavioural half.
//
// The live shape it reproduces: a concurrent pod-truth reconcile rolls Progress
// to 100, then a step fails. Without the recompute the customer is shown a full
// progress bar sitting directly above "Provisioning didn't finish" — the exact
// contradiction #5646 was filed for.
func TestMarkProvisionFailed_ProgressNeverStaysHundred(t *testing.T) {
	p := &store.Provision{
		Status:   "provisioning",
		Progress: 100, // rolled to 100 by a concurrent reconcile before the failure
		Steps: []store.ProvisionStep{
			{Name: "Creating Organization", Status: "completed"},
			{Name: "Committing manifests to Git", Status: "completed"},
			{Name: "Provisioning vCluster", Status: "running"},
			{Name: "Running health checks", Status: "pending"},
		},
	}

	markProvisionFailed(p, 2, "vcluster not ready: tenant-uatco-kubeconfig missing")

	if p.Status != "failed" {
		t.Fatalf("status = %q, want failed", p.Status)
	}
	if p.Progress >= 100 {
		t.Fatalf("progress = %d on a FAILED run — the funnel draws a full bar above "+
			`"Provisioning didn't finish". The invariant is progress == 100 <=> status == completed`, p.Progress)
	}
	if p.Steps[2].Status != "failed" {
		t.Fatalf("steps[2].status = %q, want failed", p.Steps[2].Status)
	}
	// The failed step's message is customer-visible; it must not carry the
	// runtime object name (same boundary as TestFailedStepMessage_*).
	if bannedProductTerm.MatchString(p.Steps[2].Message) {
		t.Fatalf("banned term in customer-visible steps[2].message = %q", p.Steps[2].Message)
	}
	// Control: the diagnostic must survive the sanitisation, or operators lose
	// the only signal they have.
	if p.Steps[2].Message == "" {
		t.Fatal("steps[2].message is empty — sanitising must not discard the diagnostic")
	}
}

// TestMarkProvisionFailed_AllStepsCompleteStillNotHundred covers the boundary
// the recompute exists for: every step reads "completed" but the run failed
// anyway (a failure raised outside the step loop). Progress must still not be
// 100, because the invariant is keyed on RUN status, not step tally.
func TestMarkProvisionFailed_AllStepsCompleteStillNotHundred(t *testing.T) {
	p := &store.Provision{
		Status:   "provisioning",
		Progress: 100,
		Steps: []store.ProvisionStep{
			{Name: "Creating Organization", Status: "completed"},
			{Name: "Running health checks", Status: "completed"},
		},
	}

	markProvisionFailed(p, 1, "post-flight verification failed")

	if p.Status != "failed" {
		t.Fatalf("status = %q, want failed", p.Status)
	}
	if p.Progress >= 100 {
		t.Fatalf("progress = %d with status=failed — violates progress == 100 <=> completed", p.Progress)
	}
}

// TestFailProvision_CallsMarkProvisionFailed is the wiring half, and it is the
// one the package was missing.
//
// markProvisionFailed is pure, so every behavioural test above calls it
// DIRECTLY. Deleting the call from failProvision therefore leaves them all
// green while the shipped code stops recomputing progress entirely — which is
// exactly the mutation that survived on this package before this file existed.
//
// Asserted against the parsed AST, so a mention in a comment cannot satisfy it.
func TestFailProvision_CallsMarkProvisionFailed(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "consumer.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse consumer.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == "failProvision" && d.Recv != nil {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("no (*Handler).failProvision in consumer.go")
	}

	var calls bool
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "markProvisionFailed" {
			calls = true
		}
		return true
	})

	if !calls {
		t.Fatal("failProvision does not call markProvisionFailed — the failed run keeps whatever " +
			"Progress a concurrent reconcile last wrote, so the funnel can show 100% above " +
			`"Provisioning didn't finish" (#5646)`)
	}
}
