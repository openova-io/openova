package gitops

import (
	"strings"
	"testing"
)

// helmrelease_app_dependency_closure_test.go — UAT row 225 (issue #4278).
//
// Row 225, verbatim:
//
//	"Per-Org `bp-newapi` HR reaches Ready on a fresh Org — the admin-promote
//	 post-install hook Completes (its ServiceAccount exists) so the chart
//	 finishes."
//
// The row sat unwalkable for two environments because there was no per-Org
// bp-newapi to look at, and that was read as a missing PRECONDITION ("the Org
// simply did not buy newapi"). It is not: generateOpenClawHR stamps openclaw's
// ONLY LLM backend as `https://api.<slug>.<parent>/v1`, which is exactly the
// HTTPRoute hostname generateNewAPIHR gives bp-newapi. An openclaw-without-
// newapi cart therefore ships a workspace controller pointed at a host this Org
// never provisions.
//
// These tests assert the closure at BOTH seams that must agree — the rendered
// file set and the kustomization index set. They fail on the pre-fix tree
// (`app-newapi.yaml` missing from both) rather than on a restatement of the
// fix.

// openclawCart is the shape hw292's only customer Org actually bought.
var openclawCart = []string{"wordpress", "openclaw", "stalwart-mail", "agenity"}

func TestOpenClawCart_RendersImpliedNewAPIHelmRelease(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/t99.omani.works"}

	files := g.GenerateAllWithAppConfigs("uatco", "free", openclawCart, "pw", nil)

	var openclawPath, newapiPath string
	for path := range files {
		switch {
		case strings.HasSuffix(path, "/app-openclaw.yaml"):
			openclawPath = path
		case strings.HasSuffix(path, "/app-newapi.yaml"):
			newapiPath = path
		}
	}

	if openclawPath == "" {
		t.Fatalf("control failed: the cart %v rendered no app-openclaw.yaml at all — "+
			"this test is asserting on the wrong render seam, not proving anything about newapi. files=%v",
			openclawCart, sortedKeys(files))
	}
	if newapiPath == "" {
		t.Fatalf("cart %v rendered openclaw (%s) but NO app-newapi.yaml.\n"+
			"openclaw's llm.baseURL/newapi.baseURL point at api.<slug>.<parent>, which is\n"+
			"bp-newapi's own HTTPRoute hostname — so this Org has a workspace controller\n"+
			"whose only LLM backend is a host nothing serves (UAT row 225).\nrendered files: %v",
			openclawCart, openclawPath, sortedKeys(files))
	}

	// The implied HR must be the real bp-newapi HelmRelease, not an empty or
	// Deployment-shaped stub: the row's clause is about the CHART finishing.
	body := files[newapiPath]
	for _, want := range []string{
		"kind: HelmRelease",
		"chart: bp-newapi",
		"releaseName: newapi",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("implied app-newapi.yaml is missing %q — it must be the same bp-newapi\n"+
				"HelmRelease an explicit newapi cart renders, not a placeholder.\ngot:\n%s", want, body)
		}
	}

	// And it must be wired to the host openclaw was told to call.
	if !strings.Contains(body, "api.uatco.") {
		t.Errorf("implied bp-newapi does not carry the api.<slug>.<parent> hostname openclaw\n"+
			"points at — the closure would install a gateway openclaw still cannot reach.\ngot:\n%s", body)
	}
}

func TestOpenClawCart_ImpliedNewAPIIsIndexed_VclusterTier(t *testing.T) {
	// On the vcluster tier the HR docs live in vcluster/host-apps/ and are
	// indexed by PerOrgHostHelmReleaseAppDocs. A file rendered but not listed
	// there is never applied — so the closure has to hold at BOTH seams.
	const vclusterPlan = "medium"
	if !BoundaryIsVcluster(vclusterPlan) {
		t.Fatalf("control failed: plan %q is not a vcluster-tier plan, so this test would "+
			"pass vacuously on the nil return", vclusterPlan)
	}

	docs := PerOrgHostHelmReleaseAppDocs(vclusterPlan, openclawCart)

	if !contains(docs, "app-openclaw.yaml") {
		t.Fatalf("control failed: openclaw itself is not indexed for cart %v (docs=%v)", openclawCart, docs)
	}
	if !contains(docs, "app-newapi.yaml") {
		t.Fatalf("cart %v indexes openclaw but not its implied bp-newapi: docs=%v.\n"+
			"The rendered host-apps file set and this index MUST agree — an unindexed file\n"+
			"is never applied, and an index entry with no file breaks the kustomize build.",
			openclawCart, docs)
	}
}

func TestHelmReleaseAppsFor_ClosureIsNarrowAndStable(t *testing.T) {
	// The closure must not become a blanket "install everything".
	if got := helmReleaseAppsFor([]string{"wordpress", "stalwart-mail"}); !closureEqual(got, []string{"stalwart-mail"}) {
		t.Errorf("a cart with no openclaw must be untouched by the closure: got %v, want [stalwart-mail]", got)
	}
	if got := helmReleaseAppsFor([]string{"wordpress"}); len(got) != 0 {
		t.Errorf("a cart with no HR-shaped app must render no HR files: got %v", got)
	}
	// Explicitly buying newapi alongside openclaw must not duplicate it — a
	// duplicate doc name in the index breaks the kustomize build.
	if got := helmReleaseAppsFor([]string{"newapi", "openclaw"}); !closureEqual(got, []string{"newapi", "openclaw"}) {
		t.Errorf("explicit + implied newapi must collapse to one entry: got %v, want [newapi openclaw]", got)
	}
	// Deterministic order regardless of cart order — the gitops diff must not
	// churn on a re-render.
	a := helmReleaseAppsFor([]string{"openclaw", "stalwart-mail"})
	b := helmReleaseAppsFor([]string{"stalwart-mail", "openclaw"})
	if !closureEqual(a, b) {
		t.Errorf("cart order changed the rendered HR set: %v vs %v", a, b)
	}
}

func closureEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
