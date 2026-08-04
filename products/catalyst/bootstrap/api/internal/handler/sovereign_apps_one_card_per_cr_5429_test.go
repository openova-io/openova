// #5429 — the Apps grid must render EXACTLY ONE card per Application CR,
// and that card's topology badge must come from the CR's spec.placement —
// never from the fanned-out HelmRelease's stale spec.values.
//
// The defect this locks out (reproduced live on hw290, 4/4 acme-corp UAT
// apps): the Application CR's `spec.helmRelease.name` is unset, so the
// `adoptedHRs` suppression in HandleSovereignApps never matched and each
// per-cluster HelmRelease emitted a SECOND row through the
// readTopologyFromValues branch. One real card plus one phantom.
//
// The phantom was not merely a cosmetic duplicate. readTopologyFromValues
// DEFAULTS to "singleton" when the HR values carry no topology.mode, so an
// active-hot-standby Application rendered one card badged
// `active-hot-standby` (from the CR) and another badged `singleton` (from
// the HR) — two contradictory topologies for the same Application, with
// nothing on screen telling a sovereign-admin which one is real.
//
// Why suppression keys off the LABEL and not spec.helmRelease.name:
// `spec.helmRelease.name` is the bootstrap-ADOPTION pointer (read by
// application_controller.reconcileBootstrapOwned, gated on
// spec.bootstrap=true) and is a SINGULAR string, while a fanned-out
// Application renders ONE HR PER CLUSTER via render.HRNameFor(app,
// cluster). No single name can suppress N HRs — an active-hot-standby app
// would keep one phantom per region. render.LabelApp is stamped on every
// fanned-out HR, so it is the identity that matches 1:N.
//
// Refs #5429, #3370 (one-card-per-CR contract), #3375/#4897 (topology badge).
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// makeFannedOutHR builds one per-cluster HelmRelease exactly as
// render.renderOneHR emits it: named `<app>-<cluster>` (render.HRNameFor)
// and labelled `catalyst.openova.io/app: <app>` back at its parent
// Application CR. It deliberately declares NO topology.mode in
// spec.values — the live shape — so readTopologyFromValues would default
// it to "singleton" if this HR were ever projected as its own card.
//
// The chart is bp-postgres because a phantom can only appear for a
// SHAREABLE blueprint (the fallback pass skips charts with no
// contextSchema valuesKey), and bp-postgres is the shareable chart the
// four hw290 UAT apps installed from.
func makeFannedOutHR(app, cluster, ready string) *unstructured.Unstructured {
	u := makeHR(app+"-"+cluster, "flux-system", ready)
	_ = unstructured.SetNestedField(u.Object, "bp-postgres", "spec", "chart", "spec", "chart")
	_ = unstructured.SetNestedField(u.Object, app+"-"+cluster, "spec", "releaseName")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"name": "app", "owner": "app"},
	}, "spec", "values", "databases")
	u.SetLabels(map[string]string{
		"catalyst.openova.io/app":      app,
		"catalyst.openova.io/topology": "active-hot-standby",
		"catalyst.openova.io/cluster":  cluster,
	})
	return u
}

// makeTopologyApplication builds an Application CR carrying the OBJECT
// form of spec.placement ({mode, regions}) — what Catalog → New-instance
// writes (#4897) — and NO spec.helmRelease, which is correct for a
// controller-fanned-out app and is precisely why adoptedHRs never fired.
func makeTopologyApplication(name, ns, env, mode string) *unstructured.Unstructured {
	u := makeApplication(name, ns, env)
	_ = unstructured.SetNestedField(u.Object, mode, "spec", "placement", "mode")
	_ = unstructured.SetNestedStringSlice(u.Object, []string{"rtz-a", "rtz-b"}, "spec", "placement", "regions")
	_ = unstructured.SetNestedField(u.Object, "bp-postgres", "spec", "blueprintRef", "name")
	return u
}

// TestSovereignApps_OneCardPerApplicationCR_NoFanoutPhantom is the #5429
// regression. An active-hot-standby Application CR fanned out across two
// clusters must produce EXACTLY ONE card, badged from the CR.
func TestSovereignApps_OneCardPerApplicationCR_NoFanoutPhantom(t *testing.T) {
	dynObjs := []runtime.Object{
		makeTopologyApplication("uat16-topo", "default", "acme-corp-prod", "active-hot-standby"),
		// The two per-cluster HRs the application-controller fanned out.
		makeFannedOutHR("uat16-topo", "hw290-rtz-a", "True"),
		makeFannedOutHR("uat16-topo", "hw290-rtz-b", "True"),
	}
	h := newSovereignHandler(t, nil, dynObjs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// (1) EXACTLY ONE card per Application CR — the #3370 contract.
	// Count every instance row that belongs to this Application: its own
	// card (ID == CR name) plus any phantom projected from a fanned-out
	// HR (ID == releaseName `<app>-<cluster>`).
	var mine []sovereignAppItem
	for _, a := range got.Apps {
		if !a.Instance {
			continue
		}
		if a.ID == "uat16-topo" || a.ID == "uat16-topo-hw290-rtz-a" || a.ID == "uat16-topo-hw290-rtz-b" {
			mine = append(mine, a)
		}
	}
	if len(mine) != 1 {
		ids := make([]string, 0, len(mine))
		for _, a := range mine {
			ids = append(ids, a.ID+"[topology="+a.Topology+",env="+a.Environment+"]")
		}
		t.Fatalf("Application CR uat16-topo projected %d cards %v; want exactly 1 (#3370 one-card-per-CR; the extra rows are fanned-out-HR phantoms)", len(mine), ids)
	}

	// (2) The surviving card is the CR's own, not an HR-derived row.
	card := mine[0]
	if card.ID != "uat16-topo" {
		t.Errorf("surviving card ID = %q; want uat16-topo (the Application CR, not an HR release name)", card.ID)
	}

	// (3) The badge derives from the CR's spec.placement — NOT from the
	// HR values, which carry no topology.mode and would default to
	// "singleton" via readTopologyFromValues. A card badged "singleton"
	// here is the exact contradiction #5429 reported.
	if card.Topology != "active-hot-standby" {
		t.Errorf("topology badge = %q; want active-hot-standby from the CR's spec.placement.mode (a %q badge means the card was sourced from stale HR values)", card.Topology, card.Topology)
	}

	// (4) The CR-sourced card carries the CR's environment, not the
	// phantom's "dev" default, and is not mislabelled BOOTSTRAP (the HR
	// branch hardcodes BootstrapKit: true).
	if card.Environment != "acme-corp-prod" {
		t.Errorf("environment chip = %q; want acme-corp-prod (from the CR's spec.environmentRef)", card.Environment)
	}
	if card.BootstrapKit {
		t.Errorf("card marked BootstrapKit; a controller-fanned-out Application is not a bootstrap-kit slot (the HR branch hardcodes it true)")
	}
}

// TestSovereignApps_FanoutSuppressionScopedToPresentCRs proves the
// suppression is not a blanket "drop every labelled HR". When the parent
// Application CR is absent (deleted, or its CRD/RBAC unreadable — the
// handler lists Applications in a separate best-effort call), the
// HelmRelease is the only surviving representation of a real running
// install and MUST still project a card. Without this the fix would trade
// a duplicate card for a vanished one.
func TestSovereignApps_FanoutSuppressionScopedToPresentCRs(t *testing.T) {
	dynObjs := []runtime.Object{
		// Labelled HR whose parent Application CR does NOT exist.
		makeFannedOutHR("orphan-app", "hw290-rtz-a", "True"),
	}
	h := newSovereignHandler(t, nil, dynObjs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, a := range got.Apps {
		if a.Instance && a.ID == "orphan-app-hw290-rtz-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("HR orphan-app-hw290-rtz-a projected no card; an HR whose parent Application CR is absent must still render (suppression must be scoped to CRs actually projected)")
	}
}
