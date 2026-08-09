/*
application_organizationref_5933_test.go — #5933 (UAT rows 15, 25(i)).

THE DEFECT THESE GUARDS PIN. #5814 gave the sovereign /apps grid an Org
attribution chip by reading `spec.organizationRef` off each Application CR
(sovereign.go). That reader is deliberately VERBATIM — it never synthesises the
Org from the namespace or from the CR-name prefix, because both guesses are
wrong for a spine CR (`spine-openbao` lives in `flux-system` and belongs to the
Sovereign self-org). A verbatim reader is only ever as good as its producer,
and the customer-app producer did not write the field at all: the spec map in
newApplicationUnstructured carried environmentRef / blueprintRef / placement /
regions / parameters and nothing about ownership.

The live control on hw292 is what makes this unambiguous — the spine producer
(renderSpineApplicationCR) writes organizationRef and its CRs carry
`hw292-omani-works`, while every customer CR in Org `uatco` carries an empty
one. The field, the CRD's preserve-unknown-fields spec, the reader and the chip
all worked; ONE producer omitted it.

WHY THESE GUARDS ARE SHAPED LIKE THIS. The load-bearing assertion is the
POSITIVE one on the customer path — the #5814 wire-shape guards all still pass
with the producer broken, because an empty `Org` marshals away under omitempty
and reads exactly like "this Application declares no Org". Nothing downstream
can tell an omitted field from an unowned Application, so only the producer can
be held to it. The spine case is carried alongside as a CONTROL: it already
worked, and a guard that only watches the path being changed cannot notice the
change breaking its sibling.
*/
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// customerInstallRequest — a realistic customer install body, matching the
// hw292 control (Org `uatco` launching bp-agenity through the funnel).
func customerInstallRequest() applicationInstallRequest {
	return applicationInstallRequest{
		Name:            "uatco-agenity",
		OrganizationRef: "uatco",
		EnvironmentRef:  "uatco-prod",
		BlueprintRef:    applicationBlueprintRef{Name: "bp-agenity", Version: "0.3.1"},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"me-east-215"},
		},
	}
}

// THE ROW-15 GUARD. A customer-launched Application CR must carry
// spec.organizationRef, verbatim. Before the fix this fails with
// `spec.organizationRef absent` — the reader at sovereign.go:866 has nothing
// to read and the Org chip can never render for a customer app.
func TestNewApplicationUnstructured_StampsOrganizationRef(t *testing.T) {
	obj := newApplicationUnstructured(customerInstallRequest(), "hw292.omani.works", "console.uatco.omani.homes")

	org, found, err := unstructured.NestedString(obj.Object, "spec", "organizationRef")
	if err != nil {
		t.Fatalf("spec.organizationRef is not a string: %v", err)
	}
	if !found {
		t.Fatal("spec.organizationRef ABSENT on a customer-launched Application CR — " +
			"#5814's Org-attribution reader (sovereign.go, `spec.organizationRef`) is verbatim-only, " +
			"so with no producer it returns empty forever and no customer app can appear under its Org " +
			"(UAT rows 15, 25(i)). Live control on hw292: spine-harbor carries hw292-omani-works, " +
			"uatco-agenity carries nothing.")
	}
	if org != "uatco" {
		t.Fatalf("spec.organizationRef = %q, want %q — the Org identity must be VERBATIM, "+
			"the same value stamped on the catalyst.openova.io/organization label. Never the slugged "+
			"namespace and never the CR-name prefix: both are wrong for a spine CR, which is exactly "+
			"why the reader refuses to synthesise and depends on this producer.", org, "uatco")
	}
}

// The Org identity on the CR must be the SAME string in both places it lands.
// Deriving one from the other's slugged form is the failure mode the reader's
// doc comment forbids, and a chip that disagrees with the label is worse than
// no chip.
func TestNewApplicationUnstructured_OrganizationRefMatchesTheOrganizationLabel(t *testing.T) {
	// A dotted FQDN Org ref — the case where the CR's namespace (slugged) and
	// the Org identity (verbatim) genuinely diverge, so a producer that reached
	// for the namespace instead of the request field is caught here rather than
	// passing by coincidence on an already-DNS-safe slug.
	req := customerInstallRequest()
	req.OrganizationRef = "hw292.omani.works"
	req.EnvironmentRef = "hw292-omani-works-prod"

	obj := newApplicationUnstructured(req, "hw292.omani.works", "")

	org, _, _ := unstructured.NestedString(obj.Object, "spec", "organizationRef")
	label := obj.GetLabels()["catalyst.openova.io/organization"]
	if org != label {
		t.Fatalf("spec.organizationRef = %q but catalyst.openova.io/organization label = %q — "+
			"one Application must not carry two spellings of its own Org.", org, label)
	}
	if org != "hw292.omani.works" {
		t.Fatalf("spec.organizationRef = %q, want the verbatim request value %q "+
			"(the CR namespace is the SLUGGED form %q — reading the namespace back out is the "+
			"synthesis the #5814 reader exists to avoid)",
			org, "hw292.omani.works", obj.GetNamespace())
	}
}

// FAIL-CLOSED. An install request with no Org ref must not stamp an EMPTY
// organizationRef: an absent field is the honest rendering of "attribution
// unknown", whereas `organizationRef: ""` asserts an Org whose name is the
// empty string. validateApplicationInstallRequest already rejects an empty ref
// on both HTTP seams, so this only governs direct/legacy callers of the
// builder — but it is the difference between the reader seeing "no answer" and
// seeing "the answer is nothing".
func TestNewApplicationUnstructured_OmitsOrganizationRefWhenUnset(t *testing.T) {
	req := customerInstallRequest()
	req.OrganizationRef = ""

	obj := newApplicationUnstructured(req, "", "")

	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "organizationRef"); found {
		t.Fatal("spec.organizationRef stamped as an empty string when the request carried no Org — " +
			"omit the field instead, so an unattributed Application is distinguishable from one " +
			"claiming an empty-named Org.")
	}
}

// CONTROL — the spine/bootstrap producer already wrote organizationRef and is
// the reason the field is known to work end-to-end (hw292: spine-harbor reads
// hw292-omani-works). It is a DIFFERENT function on a DIFFERENT path, so this
// guard exists to prove the customer-path change did not disturb what already
// worked. Both producers are asserted through the same accessor the reader
// uses, so a rename on one side cannot pass here while breaking the grid.
func TestSpineApplicationCR_StillStampsOrganizationRef_Control(t *testing.T) {
	sc := spineComponent{
		Chart:            "harbor",
		HRName:           "bp-harbor",
		BlueprintName:    "bp-harbor",
		BlueprintVersion: "1.2.25",
	}
	owner := map[string]string{
		"catalyst.openova.io/organization": "hw292-omani-works",
		"catalyst.openova.io/environment":  "hw292-omani-works-cp",
	}
	cr := renderSpineApplicationCR(sc, "hw292-omani-works-cp", "hw292-omani-works", []string{"me-east-215"}, owner, "")

	org, found, _ := unstructured.NestedString(cr.Object, "spec", "organizationRef")
	if !found || org != "hw292-omani-works" {
		t.Fatalf("spine spec.organizationRef = %q (found=%v), want hw292-omani-works — "+
			"the customer-path fix must not disturb the producer that already worked; "+
			"this is the live control the whole diagnosis rests on.", org, found)
	}
}
