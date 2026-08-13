// #6249 (UAT rows 19 + 15, #3687) — the /apps estate is Application-keyed.
//
// THE MEASUREMENT, hw296 2026-08-13. The sovereign-admin /apps grid drew
// ELEVEN estate cards ([data-card-kind="instance"] inside sov-apps-grid)
// while the cluster held TEN Application CRs. The console contradicted
// itself on a single page load: /cloud?view=graph&lens=reconciliation
// counted `Application 10/10` from the same cluster at the same moment.
//
// The surplus card was `sov-app-card-valkey`. There is no valkey
// Application CR on hw296 — valkey exists as HelmRelease
// flux-system/bp-valkey plus pod valkey/valkey-primary-0, which is exactly
// the HelmRelease/pod keying row 19 forbids. The card also displayed
// environment "dev" (resolveAppEnvironment's fallback) while the only
// Environments on that Sovereign were hw296-omani-works-cp and
// hw296-omani-works-prod, so it named an Environment that does not exist.
//
// WHICH OF THE TWO PRODUCERS WAS WRONG. Both derivations are in this repo
// and only one can be right:
//
//   - TEN — helmwatch/declarative_reconcilers.go:126 lists ApplicationGVR
//     and is what the reconciliation lens counts; handler/applications.go
//     HandleApplicationList likewise lists ApplicationGVR() and nothing
//     else. Both read Application CRs and only Application CRs.
//   - ELEVEN — handler/sovereign.go, the HelmRelease fallback pass inside
//     HandleSovereignApps, which projects any shareable HR with no matching
//     Application CR as `Instance: true`. That is the one that invented
//     valkey, and it is the one this file pins.
//
// The front end is faithful and was NOT the defect: AppsPage.tsx:234 keys
// the estate card purely on `a.instance === true`, so a row the BFF marks
// as an instance is drawn as one. Fixing this in the FE would have hidden
// a lying wire response instead of correcting it.
//
// WHAT THE FIX PRESERVES. The fallback exists for the first-bootstrap
// window, when the Application CRD is not yet registered and the
// self-registering chart template (gated on `.Capabilities.APIVersions.Has
// "apps.openova.io/v1"`) has not yet emitted the CR. The gate is therefore
// READABILITY of the Application list, not presence of a particular CR:
// TestSovereignApps_EstateFallsBackWhenApplicationCRDUnreadable below holds
// that window open, and it is the case #3537 actually cares about.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// newSovereignHandlerApplicationsUnreadable builds the handler with the
// Application list FAILING, which is what "the CRD has not landed yet"
// looks like on the wire. The dynamic fake is otherwise identical to
// newSovereignHandler; only the applications list verb is intercepted, so
// the HelmRelease / HTTPRoute passes behave exactly as they do everywhere
// else and the difference under test is the one variable.
func newSovereignHandlerApplicationsUnreadable(t *testing.T, dynObjs []runtime.Object) *Handler {
	t.Helper()
	core := fakek8s.NewSimpleClientset()
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		helmReleaseGVR:       "HelmReleaseList",
		httpRouteGVR:         "HTTPRouteList",
		applicationGVR:       "ApplicationList",
		fluxKustomizationGVR: "KustomizationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, dynObjs...)
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(
			schema.GroupResource{Group: "apps.openova.io", Resource: "applications"}, "")
	})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: dyn}, nil
	})
	return h
}

// makeKeyspaceHR builds the hw296 bp-valkey HelmRelease: a SHAREABLE chart
// (bp-valkey declares contextSchema kind=keyspace valuesKey=keyspaces in
// the embedded catalog, which is what makes the fallback pass eligible to
// project it) whose release name is `valkey` and which has no companion
// Application CR. Without the shareable chart + populated valuesKey this
// fixture would never reach the projection branch at all and the test
// would be vacuous.
func makeKeyspaceHR(hrName, releaseName, ready string, keyspaces []map[string]interface{}) *unstructured.Unstructured {
	u := makeHR(hrName, "flux-system", ready)
	_ = unstructured.SetNestedField(u.Object, "bp-valkey", "spec", "chart", "spec", "chart")
	_ = unstructured.SetNestedField(u.Object, releaseName, "spec", "releaseName")
	ksAny := make([]interface{}, len(keyspaces))
	for i, k := range keyspaces {
		ksAny[i] = k
	}
	_ = unstructured.SetNestedSlice(u.Object, ksAny, "spec", "values", "keyspaces")
	return u
}

// makeBlueprintApplication builds an Application CR carrying a
// blueprintRef, deliberately WITHOUT spec.helmRelease and with no
// catalyst.openova.io/app label on its HelmRelease. That matters: it means
// neither pre-existing suppression path (adoptedHRs, which keys off
// spec.helmRelease.name, nor the #5429 label path) can be what keeps the
// count honest in the control below.
func makeBlueprintApplication(name, ns, env, blueprint string) *unstructured.Unstructured {
	u := makeApplication(name, ns, env)
	_ = unstructured.SetNestedField(u.Object, blueprint, "spec", "blueprintRef", "name")
	_ = unstructured.SetNestedField(u.Object, "Ready", "status", "phase")
	return u
}

// hw296Estate returns the measured hw296 shape, reduced to its smallest
// discriminating form.
//
// THE CONTROL SHARES THE SUSPECT PROPERTY. shared-pg and shared-pg-b are
// bp-postgres HelmReleases — shareable charts with a populated
// contextSchema valuesKey, the identical property that makes bp-valkey
// eligible for the fallback — but each HAS a matching Application CR. They
// must render exactly one card each, before and after the fix. Without
// them a handler that projected NO instance cards at all (or dropped every
// shareable HR) would pass the valkey assertion, and the test would be
// evidence of nothing.
//
// bp-cilium is the non-shareable control: it must never be an estate card
// under any of these conditions.
func hw296Estate() []runtime.Object {
	return []runtime.Object{
		makeBlueprintApplication("shared-pg", "flux-system", "hw296-omani-works-prod", "bp-postgres"),
		makeBlueprintApplication("shared-pg-b", "flux-system", "hw296-omani-works-prod", "bp-postgres"),
		makeShareableHR("bp-postgres-shared", "shared-pg", "True", []map[string]interface{}{
			{"name": "gitea", "owner": "gitea"},
			{"name": "keycloak", "owner": "keycloak"},
		}),
		makeShareableHR("bp-postgres-shared-b", "shared-pg-b", "True", []map[string]interface{}{
			{"name": "newapi", "owner": "newapi"},
		}),
		// The surplus card measured on hw296: a shareable HelmRelease and a
		// running pod, with no Application CR anywhere.
		makeKeyspaceHR("bp-valkey", "valkey", "True", []map[string]interface{}{
			{"name": "newapi", "index": int64(1)},
		}),
		makeHR("bp-cilium", "flux-system", "True"),
	}
}

func sovereignAppsOf(t *testing.T, h *Handler) []sovereignAppItem {
	t.Helper()
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
	return got.Apps
}

func estateIDs(apps []sovereignAppItem) []string {
	out := []string{}
	for _, a := range apps {
		if a.Instance {
			out = append(out, a.ID)
		}
	}
	return out
}

// TestSovereignApps_EstateCountEqualsApplicationCRCount is the row-19
// assertion stated the way the walker settles it: by COUNTING. Two
// Application CRs on the cluster, two estate cards on the wire.
func TestSovereignApps_EstateCountEqualsApplicationCRCount(t *testing.T) {
	const applicationCRCount = 2 // shared-pg, shared-pg-b

	apps := sovereignAppsOf(t, newSovereignHandler(t, nil, hw296Estate()))
	ids := estateIDs(apps)

	// Shape first. Every assertion below is a count, and a count is
	// satisfied by an empty response, so if this fails nothing after it
	// means anything.
	if len(apps) == 0 {
		t.Fatalf("no app rows at all — the fixture never reached the handler")
	}
	if len(ids) != applicationCRCount {
		t.Fatalf("estate = %d cards %v; want %d, one per Application CR "+
			"(row 19: one card per Application, NOT one per HelmRelease/pod). "+
			"This is the hw296 11-vs-10 defect: a shareable HelmRelease with no "+
			"Application CR was projected as an estate card.", len(ids), ids, applicationCRCount)
	}
}

// TestSovereignApps_HelmReleaseWithoutApplicationCRIsNotAnEstateCard names
// the surplus card directly, so a future regression reports `valkey` rather
// than an off-by-one.
func TestSovereignApps_HelmReleaseWithoutApplicationCRIsNotAnEstateCard(t *testing.T) {
	apps := sovereignAppsOf(t, newSovereignHandler(t, nil, hw296Estate()))

	for _, a := range apps {
		if a.Instance && a.ID == "valkey" {
			t.Fatalf("valkey projected as an estate card (environment=%q, bootstrapKit=%v) "+
				"but has NO Application CR — only HelmRelease flux-system/bp-valkey. "+
				"A HelmRelease is how an Application is installed, not what an Application is.",
				a.Environment, a.BootstrapKit)
		}
		if a.Instance && a.ID == "cilium" {
			t.Errorf("non-shareable bp-cilium projected as an estate card")
		}
	}

	// THE CONTROL. Both shareable HRs that DO have an Application CR must
	// still render, exactly once each. A fix that suppressed every shareable
	// HelmRelease — or every instance card — would pass the loop above and
	// fail here.
	for _, want := range []string{"shared-pg", "shared-pg-b"} {
		n := 0
		for _, a := range apps {
			if a.Instance && a.ID == want {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Application %s rendered %d estate cards; want exactly 1", want, n)
		}
	}
}

// TestSovereignApps_SuppressedHelmReleaseStillRendersAsCatalogRow — nothing
// vanishes from the page. bp-valkey is a bootstrap-kit blueprint, so once
// it stops claiming to be an Application it must still appear as its
// bootstrap/catalog row, still marked installed off the HelmRelease. The
// operator loses a false Application, not a component.
func TestSovereignApps_SuppressedHelmReleaseStillRendersAsCatalogRow(t *testing.T) {
	apps := sovereignAppsOf(t, newSovereignHandler(t, nil, hw296Estate()))

	var row *sovereignAppItem
	for i := range apps {
		if apps[i].ID == "bp-valkey" {
			row = &apps[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("bp-valkey disappeared from /apps entirely; suppression must drop the "+
			"ESTATE row only, leaving the bootstrap/catalog row intact. got %d rows", len(apps))
	}
	if row.Instance {
		t.Errorf("bp-valkey row still marked instance=true")
	}
	if row.Status != "bootstrap" && row.Status != "installed" {
		t.Errorf("bp-valkey status = %q; want bootstrap/installed — the HelmRelease is Ready "+
			"and that must still reach the card", row.Status)
	}
}

// TestSovereignApps_EstateFallsBackWhenApplicationCRDUnreadable holds open
// the window the fallback was written for, and is the reason the gate is
// readability rather than per-CR presence. During first bootstrap the
// Application CRD is not registered, the list errors, and the HelmReleases
// are the only surviving description of what is running — suppressing them
// there would trade a phantom card for a blank estate.
func TestSovereignApps_EstateFallsBackWhenApplicationCRDUnreadable(t *testing.T) {
	apps := sovereignAppsOf(t, newSovereignHandlerApplicationsUnreadable(t, hw296Estate()))
	ids := estateIDs(apps)

	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	// All three shareable instances project here — including valkey, which
	// is correct when nothing authoritative can be consulted.
	for _, want := range []string{"shared-pg", "shared-pg-b", "valkey"} {
		if !found[want] {
			t.Errorf("with the Application CRD unreadable, %s projected no estate card %v; "+
				"the HelmRelease fallback must still run in the pre-CRD bootstrap window", want, ids)
		}
	}
	if found["cilium"] {
		t.Errorf("non-shareable bp-cilium projected an estate card even in fallback mode")
	}
}
