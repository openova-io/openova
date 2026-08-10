/*
application_organizationref_seed_row15_test.go — UAT row 15, the producer
#5933 did not enrol.

WHAT #5933 FIXED AND WHAT IT MISSED. #5814 gave the sovereign /apps grid its Org
attribution chip by reading `spec.organizationRef` off each Application CR
(sovereign.go:866), verbatim. #5933 then found that the REST install producer
(newApplicationUnstructured) never wrote that field and fixed it, guarding the
change with the spine producer (renderSpineApplicationCR) as a control.

There is a THIRD persisted producer: newApplicationCRFromSeed
(endpoint_handler.go:2312), the multi-instance create path behind
POST create-instance AND behind wireBackingServices' auto-created backing
services. It stamps metadata, labels and a full spec — and no organizationRef.
So the chip still fails on every Application born through the seed path, which
is why row 15 measured 4 of 7 cards with no Org chip while the other 3 were
fine: the passing cards came from the two producers #5933 covered.

WHY THE OBVIOUS GUARD IS BLIND HERE — the point of this file.
The Org identity travels on the CR in TWO places, and only one of them is what
the chip reads:

  - the LABEL `catalyst.openova.io/organization` — stamped by ALL THREE
    producers already (seed path: obj.SetLabels(seed.Labels) at
    endpoint_handler.go:2322, populated at instances/create.go:376).
  - the SPEC FIELD `spec.organizationRef` — stamped by producer 1 and 3 only.
    This is the one sovereign.go reads.

A guard written against the LABEL therefore passes on all three producers and
cannot see this defect at all. placement_3373_test.go:217 is exactly that guard:
it exercises newApplicationCRFromSeed with a dotted org, and asserts only
metadata.namespace and spec.environmentRef — both derived — so it has been
green throughout. The assertions below are on the SPEC FIELD deliberately, and
the label is asserted only as the equality partner (two spellings of one Org is
its own defect, #5933's rule).

The empty-org case pins OMISSION rather than "". An empty string marshals away
under the reader's omitempty and reads downstream as "this Application declares
no Org" — indistinguishable from an unowned app, which is precisely how this
stayed invisible. Fail-closed matches producer 1 (application_organizationref_
5933_test.go:114).
*/
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// seedForOrg — a realistic multi-instance seed. instances.Build sets
// Namespace to the Org ref VERBATIM (create.go:368 `Namespace: r.Org`) and
// mirrors the same string onto the organization label (create.go:378), so a
// fixture that disagrees between the two would not represent any real call.
func seedForOrg(org string) instances.ApplicationSeed {
	return instances.ApplicationSeed{
		Name:       "uatco-openclaw",
		Namespace:  org,
		Blueprint:  "bp-openclaw",
		Topology:   "single-region",
		InstanceID: "inst-abc123",
		Labels: map[string]string{
			"catalyst.openova.io/managed-by":   "catalyst-api",
			"catalyst.openova.io/organization": org,
			"catalyst.openova.io/blueprint":    "openclaw",
			"catalyst.openova.io/instance":     "inst-abc123",
		},
	}
}

// THE ROW-15 GUARD. An Application born through the multi-instance seed path
// must carry spec.organizationRef, verbatim.
func TestNewApplicationCRFromSeed_StampsOrganizationRef_Row15(t *testing.T) {
	obj := newApplicationCRFromSeed(seedForOrg("uatco"))

	org, found, err := unstructured.NestedString(obj.Object, "spec", "organizationRef")
	if err != nil {
		t.Fatalf("read spec.organizationRef: %v", err)
	}
	if !found {
		t.Fatalf("spec.organizationRef absent — the seed producer never stamps the Org, " +
			"so sovereign.go:866 reads empty and the /apps Org chip cannot render for " +
			"any Application created through the multi-instance path (UAT row 15)")
	}
	if org != "uatco" {
		t.Errorf("spec.organizationRef = %q, want %q (verbatim)", org, "uatco")
	}
}

// The dotted-FQDN case is where a "helpful" fix goes wrong. The seed producer
// SLUGS the same string twice on purpose — metadata.namespace via orgNamespace
// and spec.environmentRef via environmentRefForOrg — because the CRD rejects
// dots there. organizationRef is the one place the identity must survive
// UNSLUGGED, because the reader is verbatim and the chip prints what it reads.
// Reusing either slugged value here would satisfy the test above and still
// paint the wrong Org name on the card.
func TestNewApplicationCRFromSeed_OrganizationRefIsVerbatimNotSlugged_Row15(t *testing.T) {
	const org = "hw293.omantel.biz"
	obj := newApplicationCRFromSeed(seedForOrg(org))

	got, found, _ := unstructured.NestedString(obj.Object, "spec", "organizationRef")
	if !found || got != org {
		t.Fatalf("spec.organizationRef = %q (found=%v), want %q verbatim — dots intact",
			got, found, org)
	}

	// It must NOT be either derived spelling.
	if ns := obj.GetNamespace(); got == ns {
		t.Errorf("spec.organizationRef must not be the slugged namespace %q", ns)
	}
	if envRef, _, _ := unstructured.NestedString(obj.Object, "spec", "environmentRef"); got == envRef {
		t.Errorf("spec.organizationRef must not be the slugged environmentRef %q", envRef)
	}

	// The two carriers of the identity must agree — one Application may not
	// carry two spellings of its own Org (#5933's rule).
	if lbl := obj.GetLabels()["catalyst.openova.io/organization"]; lbl != got {
		t.Errorf("spec.organizationRef %q disagrees with the organization label %q", got, lbl)
	}

	// CONTROL — the derivations this producer is SUPPOSED to slug must still
	// be slugged. A fix that stopped slugging everything would make the
	// assertions above pass and have the apiserver reject the CR.
	if ns := obj.GetNamespace(); ns != "hw293-omantel-biz" {
		t.Errorf("control: metadata.namespace must stay slugged, got %q", ns)
	}
}

// Fail-closed: no Org on the seed ⇒ the key is OMITTED, never "".
func TestNewApplicationCRFromSeed_OmitsOrganizationRefWhenUnset_Row15(t *testing.T) {
	seed := seedForOrg("")
	delete(seed.Labels, "catalyst.openova.io/organization")
	obj := newApplicationCRFromSeed(seed)

	if org, found, _ := unstructured.NestedString(obj.Object, "spec", "organizationRef"); found {
		t.Errorf("spec.organizationRef present as %q for an Org-less seed; an empty ref "+
			"claims an empty-named Org instead of reading as attribution-unknown", org)
	}
}

// LOCKSTEP — every persisted Application-CR producer stamps the field the chip
// reads. This is the guard the brief asks for: one written against only the
// producers that already worked cannot see a gap in the third. Enumerating all
// three in ONE test means the next producer added to this package fails here
// until it is enrolled, rather than shipping another silent blind spot.
func TestAllApplicationProducers_StampOrganizationRef_Row15(t *testing.T) {
	const org = "uatco"

	producers := map[string]*unstructured.Unstructured{
		// 1 — REST install path (applications.go:1083).
		"newApplicationUnstructured": newApplicationUnstructured(
			customerInstallRequest(), "hw293.omantel.biz", "console.uatco.omani.homes"),
		// 2 — multi-instance seed path (endpoint_handler.go:2312). The one
		// row 15 measured broken.
		"newApplicationCRFromSeed": newApplicationCRFromSeed(seedForOrg(org)),
		// 3 — spine self-registration (post_handover_spine_apps.go:472).
		"renderSpineApplicationCR": renderSpineApplicationCR(
			spineComponent{
				Chart: "harbor", HRName: "bp-harbor",
				BlueprintName: "bp-harbor", BlueprintVersion: "1.2.25",
			},
			"uatco-cp", org, []string{"me-east-215"},
			map[string]string{"catalyst.openova.io/organization": org}, ""),
	}

	for name, obj := range producers {
		got, found, err := unstructured.NestedString(obj.Object, "spec", "organizationRef")
		if err != nil {
			t.Fatalf("%s: read spec.organizationRef: %v", name, err)
		}
		if !found || got != org {
			t.Errorf("%s: spec.organizationRef = %q (found=%v), want %q — this producer's "+
				"Applications render with no Org chip on /apps", name, got, found, org)
		}
	}
}
