package handler

import (
	"go/ast"
	"strings"
	"testing"
)

// #5819 (UAT row 25) — showback and the Organization directory must name the
// parent Organization with the SAME identifier.
//
// consumptionParentOrg returned the raw Sovereign FQDN, so showback labelled the
// parent group `hw292.omani.works` while the directory and the Organization CR
// both said `hw292-omani-works`. Row 25 asserts "one consistent model across
// surfaces"; two spellings of one Org is precisely the failure it names.
//
// It also broke a join that could never match: the parent bucket is selected by
// `org == parentOrg`, where org comes from the `openova.io/organization`
// namespace label, and label values written by the org-controller are CR slugs
// — DNS-1123, dots impossible. So an FQDN-shaped parentOrg matched nothing and
// the parent rendered at 0 cost units on a Sovereign plainly running workloads.

func newDepWithFQDN(id, fqdn string) *Deployment {
	d := &Deployment{ID: id}
	d.Request.SovereignFQDN = fqdn
	return d
}

func TestConsumptionParentOrg_ReturnsTheSlugNotTheFQDN(t *testing.T) {
	h := &Handler{}
	h.deployments.Store("dep-1", newDepWithFQDN("dep-1", "hw292.omani.works"))

	got := h.consumptionParentOrg("dep-1")
	if got != "hw292-omani-works" {
		t.Fatalf("consumptionParentOrg = %q, want %q — showback would label the parent "+
			"Organization differently from the directory, the treemap and the CR, which is "+
			"the exact inconsistency UAT row 25 asserts against (#5819)", got, "hw292-omani-works")
	}
}

// The identifier must be byte-identical to the one the self-org CR is minted
// with. Asserting a hand-written literal would let the two drift the moment
// either derivation changed; asserting AGREEMENT is what actually holds.
//
// spineOrganizationSlug is the producer side (it names the Organization CR the
// spine Environment references). Comparing against it — rather than against a
// second copy of the sanitising rules — is the whole point: one seam, one answer.
func TestConsumptionParentOrg_AgreesWithTheSelfOrgProducer(t *testing.T) {
	for _, fqdn := range []string{
		"hw292.omani.works",
		"HW292.Omani.Works",     // case folding
		"t38.omantel.biz",       // the alternate pool TLD
		"a--b..c.example",       // dash/dot runs collapse
		"  hw292.omani.works  ", // surrounding whitespace
	} {
		h := &Handler{}
		h.deployments.Store("d", newDepWithFQDN("d", fqdn))

		want := spineOrganizationSlug(newDepWithFQDN("d", fqdn))
		if got := h.consumptionParentOrg("d"); got != want {
			t.Fatalf("FQDN %q: showback says %q, the self-org CR producer says %q — "+
				"the two derivations have drifted apart again (#5819)", fqdn, got, want)
		}
	}
}

// Unresolvable deployment keeps the visibly-synthetic fallback. A plausible
// LOOKING slug for an Org we cannot identify would be worse than an obviously
// placeholder one — it would read as a real Organization on the showback panel.
func TestConsumptionParentOrg_UnknownDeploymentKeepsSyntheticFallback(t *testing.T) {
	h := &Handler{}
	if got := h.consumptionParentOrg("no-such-dep"); got != "sovereign" {
		t.Fatalf("consumptionParentOrg on an unknown deployment = %q, want %q — "+
			"an unidentifiable Org must not be given a real-looking slug", got, "sovereign")
	}
}

// TestConsumptionParentOrg_RoutesThroughTheCanonicalSeam pins the MECHANISM.
//
// The behaviour tests above all pass if someone reimplements the sanitising
// inline — strings.ReplaceAll(fqdn, ".", "-") gets every case in this file
// right, and then diverges the first time orgNamespace's rules change (the
// 63-char cap, the dash-run collapse, the empty-input guard). That is how the
// two derivations drifted in the first place, so the seam itself is asserted:
// this function must CALL orgNamespace, not re-derive what it does.
func TestConsumptionParentOrg_RoutesThroughTheCanonicalSeam(t *testing.T) {
	_, f := parseHandlerFile(t, "org_consumption.go")
	calls := callsWithin(t, f, "consumptionParentOrg")

	// Vacuity: the walk must actually see this function's calls. A silent
	// miss would make the assertion below pass on an empty set.
	if len(calls) < 2 {
		t.Fatalf("only %d calls found inside consumptionParentOrg — the AST walk is broken", len(calls))
	}

	if !calls["orgNamespace"] {
		t.Fatal("consumptionParentOrg no longer calls orgNamespace — " +
			"if it sanitises the FQDN inline there are now TWO derivations of one Org " +
			"identifier, and they will drift exactly as they did before #5819. " +
			"orgNamespace is the single canonical org→identifier seam (namespace_ensure.go).")
	}
}

// Guard the comment carrying the reasoning. The join-repair argument is the
// part most likely to be undone: reverting to the FQDN looks harmless and even
// reads as "more informative" on a label, and nothing about the wire shape
// signals that it silently empties the parent bucket.
func TestConsumptionParentOrg_RecordsWhyTheSlugAndNotTheFQDN(t *testing.T) {
	_, f := parseHandlerFile(t, "org_consumption.go")
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "consumptionParentOrg" || fd.Doc == nil {
			return true
		}
		doc := fd.Doc.Text()
		found = true
		for _, want := range []string{"#5819", "openova.io/organization", "orgNamespace"} {
			if !strings.Contains(doc, want) {
				t.Fatalf("consumptionParentOrg's doc comment no longer explains %q — "+
					"without it the next author reverts to the FQDN, which looks harmless "+
					"and silently empties the parent showback bucket again", want)
			}
		}
		return false
	})
	if !found {
		t.Fatal("consumptionParentOrg has no doc comment — the reasoning is gone")
	}
}
