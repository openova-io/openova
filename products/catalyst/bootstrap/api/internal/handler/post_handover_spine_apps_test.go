// post_handover_spine_apps_test.go — unit coverage for the #4212 Seam 3
// spine Application-CR producer. Exercises:
//   - the pure CR-render contract (shape parseSpec requires + the
//     adopt-not-roll label),
//   - deployment-derived env-ref / regions / owner-labels,
//   - present-HR filtering (only spine HRs actually on the cluster),
//   - the full enroll loop server-side-applying one Application CR per
//     present spine HR via a fake dynamic client.
package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// spineFakeDynamic registers the HelmRelease + Application GVRs so the
// producer's List (HRs) + Patch (Application SSA) calls resolve.
func spineFakeDynamic(seed ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		helmwatch.HelmReleaseGVR: "HelmReleaseList",
		ApplicationGVR():         "ApplicationList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
}

// spineHRObj builds a minimal bootstrap-spine HelmRelease in flux-system.
func spineHRObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      name,
			"namespace": helmwatch.FluxNamespace,
		},
		"spec":   map[string]any{},
		"status": map[string]any{},
	}}
}

func newSpineTestDeployment() *Deployment {
	return &Deployment{
		ID: "dep-spine-1",
		Request: provisioner.Request{
			SovereignFQDN: "t99.omani.works",
			Region:        "me-east-215",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: "me-east-215"},
				{CloudRegion: "eu-west-101"},
			},
		},
	}
}

// TestRenderSpineApplicationCR_ContractShape asserts the produced CR carries
// exactly the fields the application-controller's parseSpec requires
// (environmentRef, blueprintRef.{name,version}, regions[]) AND the
// adopt-not-roll labels — without a spec.placement (so the controller
// derives the effective default per Blueprint × SOVEREIGN_BCP_TOPOLOGY).
func TestRenderSpineApplicationCR_ContractShape(t *testing.T) {
	sc := spineComponent{Chart: "openbao", HRName: "bp-openbao", BlueprintName: "bp-openbao", BlueprintVersion: "1.2.51"}
	owner := map[string]string{
		"catalyst.openova.io/organization": "t99.omani.works",
		"catalyst.openova.io/environment":  "t99-omani-works-cp",
	}
	cr := renderSpineApplicationCR(sc, "t99-omani-works-cp", []string{"me-east-215", "eu-west-101"}, owner)

	if got := cr.GetName(); got != "spine-openbao" {
		t.Fatalf("name = %q, want spine-openbao", got)
	}
	if got := cr.GetNamespace(); got != spineApplicationNamespace {
		t.Fatalf("namespace = %q, want %q", got, spineApplicationNamespace)
	}
	if cr.GetAPIVersion() != ApplicationGVR().Group+"/"+ApplicationGVR().Version || cr.GetKind() != "Application" {
		t.Fatalf("apiVersion/kind = %q/%q", cr.GetAPIVersion(), cr.GetKind())
	}

	// parseSpec-required fields.
	env, _, _ := unstructured.NestedString(cr.Object, "spec", "environmentRef")
	if env != "t99-omani-works-cp" {
		t.Fatalf("spec.environmentRef = %q", env)
	}
	bpName, _, _ := unstructured.NestedString(cr.Object, "spec", "blueprintRef", "name")
	bpVer, _, _ := unstructured.NestedString(cr.Object, "spec", "blueprintRef", "version")
	if bpName != "bp-openbao" || bpVer != "1.2.51" {
		t.Fatalf("spec.blueprintRef = %q@%q", bpName, bpVer)
	}
	rgs, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "regions")
	if len(rgs) != 2 || rgs[0] != "me-east-215" || rgs[1] != "eu-west-101" {
		t.Fatalf("spec.regions = %v", rgs)
	}

	// spec.placement MUST be omitted so the controller derives the default.
	if _, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "spec", "placement"); found {
		t.Fatalf("spec.placement must be omitted (controller derives effective default), but it was set")
	}

	// Adopt-not-roll contract (Invariant #3): the CR names the EXISTING
	// HelmRelease it enrolls + is marked spine.
	lbls := cr.GetLabels()
	if lbls["catalyst.openova.io/spine"] != "true" {
		t.Fatalf("missing spine marker label: %v", lbls)
	}
	if lbls["catalyst.openova.io/adopts-helmrelease"] != "bp-openbao" {
		t.Fatalf("adopts-helmrelease = %q, want bp-openbao", lbls["catalyst.openova.io/adopts-helmrelease"])
	}
	if lbls["catalyst.openova.io/blueprint"] != "bp-openbao" || lbls["catalyst.openova.io/blueprint-version"] != "1.2.51" {
		t.Fatalf("blueprint labels = %v", lbls)
	}
	// Owner labels mirrored through.
	if lbls["catalyst.openova.io/organization"] != "t99.omani.works" {
		t.Fatalf("organization owner label not mirrored: %v", lbls)
	}
}

func TestSpineEnvironmentRef_SluggedRFC1123(t *testing.T) {
	dep := newSpineTestDeployment()
	got := spineEnvironmentRef(dep)
	// sanitizeEnvironmentRef lowercases + replaces dots with hyphens; the
	// FQDN t99.omani.works → t99-omani-works, plus the -cp suffix.
	if got != "t99-omani-works-cp" {
		t.Fatalf("spineEnvironmentRef = %q, want t99-omani-works-cp", got)
	}
}

func TestSpineRegions_PrefersRegionsThenSingle(t *testing.T) {
	dep := newSpineTestDeployment()
	got := spineRegions(dep)
	if len(got) != 2 || got[0] != "me-east-215" || got[1] != "eu-west-101" {
		t.Fatalf("spineRegions(multi) = %v", got)
	}

	// Single-region fallback.
	single := &Deployment{ID: "x", Request: provisioner.Request{Region: "me-east-215"}}
	if g := spineRegions(single); len(g) != 1 || g[0] != "me-east-215" {
		t.Fatalf("spineRegions(single) = %v", g)
	}

	// No regions at all → empty (producer skips).
	none := &Deployment{ID: "x", Request: provisioner.Request{}}
	if g := spineRegions(none); len(g) != 0 {
		t.Fatalf("spineRegions(none) = %v, want empty", g)
	}
}

// TestPresentSpineHRs_OnlyExistingSpine asserts the producer enrolls only
// spine HRs that actually exist — never fabricates a CR for a missing one.
func TestPresentSpineHRs_OnlyExistingSpine(t *testing.T) {
	// Cluster has openbao + gitea spine HRs (+ an unrelated bp-velero), but
	// NOT keycloak/harbor.
	dyn := spineFakeDynamic(
		spineHRObj("bp-openbao"),
		spineHRObj("bp-gitea"),
		spineHRObj("bp-velero"), // not in the DR-capable spine roster
	)
	h := New(silentLogger())
	dep := newSpineTestDeployment()

	present := h.presentSpineHRs(dyn, dep)
	gotCharts := map[string]bool{}
	for _, sc := range present {
		gotCharts[sc.Chart] = true
	}
	if len(present) != 2 || !gotCharts["openbao"] || !gotCharts["gitea"] {
		t.Fatalf("presentSpineHRs = %v charts (%v), want exactly openbao+gitea", len(present), gotCharts)
	}
	if gotCharts["keycloak"] || gotCharts["harbor"] {
		t.Fatalf("presentSpineHRs fabricated a CR for an absent spine HR: %v", gotCharts)
	}
}

// withCreateUpdateApplier swaps the package-level applySpineApplicationCR
// seam for a Create-or-Update applier the dynamic-fake client supports
// (the fake client cannot decode an ApplyPatchType body — it tries to map
// the unstructured JSON onto a typed struct). The replacement preserves the
// SAME create-or-merge idempotency the production server-side-apply has, so
// the enroll loop's behaviour (one CR per HR, idempotent re-run) is faithfully
// exercised. Returns a restore func.
func withCreateUpdateApplier(t *testing.T) func() {
	t.Helper()
	prev := applySpineApplicationCR
	applySpineApplicationCR = func(dyn dynamic.Interface, obj *unstructured.Unstructured) error {
		ri := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace)
		existing, err := ri.Get(context.Background(), obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			_, uerr := ri.Update(context.Background(), obj, metav1.UpdateOptions{})
			return uerr
		}
		_, cerr := ri.Create(context.Background(), obj, metav1.CreateOptions{})
		return cerr
	}
	return func() { applySpineApplicationCR = prev }
}

// TestEnrollSpineApplications_StampsOneCRPerPresentHR drives the full enroll
// loop through the fake dynamic client and asserts one Application CR landed
// per present spine HR, with the right name + adopt label, and that a
// re-run is an idempotent no-op (same count, no duplicates).
func TestEnrollSpineApplications_StampsOneCRPerPresentHR(t *testing.T) {
	defer withCreateUpdateApplier(t)()
	dyn := spineFakeDynamic(
		spineHRObj("bp-openbao"),
		spineHRObj("bp-keycloak"),
		spineHRObj("bp-harbor"),
		spineHRObj("bp-gitea"),
	)
	h := New(silentLogger())
	dep := newSpineTestDeployment()

	present := h.presentSpineHRs(dyn, dep)
	if len(present) != 4 {
		t.Fatalf("present = %d, want 4", len(present))
	}
	envRef := spineEnvironmentRef(dep)
	regions := spineRegions(dep)
	owner := spineOwnerLabels(dep)

	enrolled := h.enrollSpineApplications(dyn, dep, present, envRef, regions, owner)
	if enrolled != 4 {
		t.Fatalf("enrolled = %d, want 4", enrolled)
	}

	// Verify each spine Application CR exists with the adopt label.
	for _, sc := range present {
		got, err := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).
			Get(context.Background(), spineApplicationName(sc.Chart), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("spine Application %s not created: %v", spineApplicationName(sc.Chart), err)
		}
		if got.GetLabels()["catalyst.openova.io/adopts-helmrelease"] != sc.HRName {
			t.Fatalf("CR %s adopts-helmrelease = %q, want %q",
				got.GetName(), got.GetLabels()["catalyst.openova.io/adopts-helmrelease"], sc.HRName)
		}
	}

	// Idempotent re-run: still 4 enrolled, and the total Application count
	// stays 4 (server-side-apply merge, no duplicates).
	enrolled2 := h.enrollSpineApplications(dyn, dep, present, envRef, regions, owner)
	if enrolled2 != 4 {
		t.Fatalf("re-run enrolled = %d, want 4 (idempotent)", enrolled2)
	}
	list, err := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list applications: %v", err)
	}
	if len(list.Items) != 4 {
		t.Fatalf("Application count after re-run = %d, want 4 (no duplicates)", len(list.Items))
	}
}

// TestDRCapableSpineRoster_MatchesEPICSet pins the spine roster to the exact
// four DR-capable components EPIC #4212's acceptance walk asserts.
func TestDRCapableSpineRoster_MatchesEPICSet(t *testing.T) {
	want := map[string]string{
		"openbao":  "bp-openbao",
		"keycloak": "bp-keycloak",
		"harbor":   "bp-harbor",
		"gitea":    "bp-gitea",
	}
	if len(drCapableSpine) != len(want) {
		t.Fatalf("roster size = %d, want %d", len(drCapableSpine), len(want))
	}
	for _, sc := range drCapableSpine {
		hr, ok := want[sc.Chart]
		if !ok {
			t.Fatalf("unexpected spine chart %q in roster", sc.Chart)
		}
		if sc.HRName != hr {
			t.Fatalf("chart %q HRName = %q, want %q", sc.Chart, sc.HRName, hr)
		}
		if sc.BlueprintName == "" || sc.BlueprintVersion == "" {
			t.Fatalf("chart %q missing Blueprint pin: %+v", sc.Chart, sc)
		}
	}
}
