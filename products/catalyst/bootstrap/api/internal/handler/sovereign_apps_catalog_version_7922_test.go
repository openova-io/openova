// #7922 — a catalog row of GET /api/v1/sovereign/apps reports the version of
// the Sovereign's own catalog, not the version frozen into the catalyst-api
// image.
//
// The defect, measured on candidate rc/20261115T144352Z-67402a1 (hw307,
// image catalyst-api:2c6f654): the image's embedded blueprints.json said
// bp-chargeback 0.1.66, while the candidate's kit pin, the catalog-seed
// Blueprint CR and the installed HelmRelease all said 0.1.67. The deploy
// bot's lockstep commit that moved those to 0.1.67 carries [skip ci], so it
// never rebuilt the image; the row reported the image's 0.1.66 next to an
// "installed" status that came from the 0.1.67 HelmRelease.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
)

// makeBlueprintCR builds the cluster-scoped catalyst.openova.io/v1 Blueprint
// the umbrella chart renders from templates/catalog-seed/blueprints.yaml.
func makeBlueprintCR(name, version string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint"})
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, version, "spec", "version")
	return u
}

func sovereignAppsRows(t *testing.T, h *Handler) map[string]sovereignAppItem {
	t.Helper()
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := map[string]sovereignAppItem{}
	for _, a := range got.Apps {
		if !a.Instance {
			rows[a.ID] = a
		}
	}
	return rows
}

// embeddedListedVersion returns the image's (embedded) version of a listed
// Blueprint, failing the test when the fixture's premise does not hold.
func embeddedListedVersion(t *testing.T, id string) string {
	t.Helper()
	bp, ok := catalog.BlueprintByID(id)
	if !ok || bp.Visibility != catalog.VisibilityListed || bp.Version == nil || *bp.Version == "" {
		t.Fatalf("%s must be a listed Blueprint with a version in the embedded catalogue for this test to mean anything", id)
	}
	return *bp.Version
}

func TestSovereignApps_CatalogRowReportsTheSovereignsCatalogVersionNotTheImages(t *testing.T) {
	const id = "bp-chargeback"
	embedded := embeddedListedVersion(t, id)
	kit := embedded + "-kit" // any version the image does not carry

	h := newSovereignHandler(t, nil, []runtime.Object{
		makeHR(id, "flux-system", "True"),
		makeBlueprintCR(id, kit),
	})
	row, ok := sovereignAppsRows(t, h)[id]
	if !ok {
		t.Fatalf("no catalog row for %s", id)
	}
	if row.Status != "installed" {
		t.Fatalf("%s status = %q; want installed (the fixture's HelmRelease is Ready)", id, row.Status)
	}
	if row.Version != kit {
		t.Errorf("%s version = %q; want %q from the Sovereign's Blueprint CR (%q is the version frozen into the image at build time)", id, row.Version, kit, embedded)
	}
}

func TestSovereignApps_CatalogRowWithoutABlueprintCRKeepsTheEmbeddedVersion(t *testing.T) {
	const id = "bp-chargeback"
	embedded := embeddedListedVersion(t, id)

	// A Blueprint CR for a different Blueprint must not leak into this row,
	// and an empty spec.version is no answer.
	h := newSovereignHandler(t, nil, []runtime.Object{
		makeBlueprintCR("bp-temporal", "9.9.9"),
		makeBlueprintCR(id+"-other", "9.9.9"),
	})
	row, ok := sovereignAppsRows(t, h)[id]
	if !ok {
		t.Fatalf("no catalog row for %s", id)
	}
	if row.Version != embedded {
		t.Errorf("%s version = %q; want the embedded %q when no Blueprint CR names it", id, row.Version, embedded)
	}

	h = newSovereignHandler(t, nil, []runtime.Object{makeBlueprintCR(id, "")})
	if got := sovereignAppsRows(t, h)[id].Version; got != embedded {
		t.Errorf("%s version = %q with an empty CR spec.version; want the embedded %q", id, got, embedded)
	}
}
