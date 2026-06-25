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
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

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
// (environmentRef, organizationRef, blueprintRef.{name,version}, regions[],
// placement) AND the adopt-not-roll labels. On a MULTI-region deployment the
// stamped placement is the Blueprint's topology.defaults.multi-region (here
// active-passive for openbao) — load-bearing so buildContinuumPlan resolves a
// primary+standby plan and mints the Continuum CR (Seam 3).
func TestRenderSpineApplicationCR_ContractShape(t *testing.T) {
	sc := spineComponent{Chart: "openbao", HRName: "bp-openbao", BlueprintName: "bp-openbao", BlueprintVersion: "1.2.25", MultiRegionMode: "active-passive"}
	owner := map[string]string{
		"catalyst.openova.io/organization": "t99.omani.works",
		"catalyst.openova.io/environment":  "t99-omani-works-cp",
	}
	cr := renderSpineApplicationCR(sc, "t99-omani-works-cp", "t99-omani-works", []string{"me-east-215", "eu-west-101"}, owner)

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
	org, _, _ := unstructured.NestedString(cr.Object, "spec", "organizationRef")
	if org != "t99-omani-works" {
		t.Fatalf("spec.organizationRef = %q, want t99-omani-works", org)
	}
	bpName, _, _ := unstructured.NestedString(cr.Object, "spec", "blueprintRef", "name")
	bpVer, _, _ := unstructured.NestedString(cr.Object, "spec", "blueprintRef", "version")
	if bpName != "bp-openbao" || bpVer != "1.2.25" {
		t.Fatalf("spec.blueprintRef = %q@%q", bpName, bpVer)
	}
	rgs, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "regions")
	if len(rgs) != 2 || rgs[0] != "me-east-215" || rgs[1] != "eu-west-101" {
		t.Fatalf("spec.regions = %v", rgs)
	}

	// spec.placement is stamped EXPLICITLY (multi-region → the Blueprint's
	// active-passive default) so placement.Resolve emits a primary+standby
	// plan and buildContinuumPlan mints the Continuum CR (the consumer half).
	pl, found, _ := unstructured.NestedString(cr.Object, "spec", "placement")
	if !found {
		t.Fatalf("spec.placement must be stamped explicitly; it was omitted")
	}
	if pl != "active-passive" {
		t.Fatalf("spec.placement = %q, want active-passive (multi-region openbao default)", pl)
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
	if lbls["catalyst.openova.io/blueprint"] != "bp-openbao" || lbls["catalyst.openova.io/blueprint-version"] != "1.2.25" {
		t.Fatalf("blueprint labels = %v", lbls)
	}
	// Owner labels mirrored through; org label pinned to the control-plane slug.
	if lbls["catalyst.openova.io/organization"] != "t99-omani-works" {
		t.Fatalf("organization label = %q, want control-plane slug t99-omani-works", lbls["catalyst.openova.io/organization"])
	}
}

// TestSpinePlacementMode_SingleVsMultiRegion proves the producer stamps
// singleton on a single-region Sovereign and the Blueprint's multi-region
// default on a ≥2-region one — the exact gate buildContinuumPlan reads.
func TestSpinePlacementMode_SingleVsMultiRegion(t *testing.T) {
	sc := spineComponent{Chart: "gitea", MultiRegionMode: "active-hot-standby"}
	if got := spinePlacementMode(sc, 1); got != "singleton" {
		t.Fatalf("single-region placement = %q, want singleton", got)
	}
	if got := spinePlacementMode(sc, 2); got != "active-hot-standby" {
		t.Fatalf("multi-region placement = %q, want active-hot-standby", got)
	}
	// Defensive: a roster row with no MultiRegionMode never stamps an
	// unrenderable mode even when multi-region.
	bare := spineComponent{Chart: "x"}
	if got := spinePlacementMode(bare, 3); got != "singleton" {
		t.Fatalf("bare multi-region placement = %q, want singleton fallback", got)
	}
}

// TestSpineEnvRegionCode_CRDPatternValid proves the derived region code
// satisfies the Environment CRD pattern `^[a-z]{3}[a-z0-9]?$` for the real
// cloud-region shapes the spine env carries.
func TestSpineEnvRegionCode_CRDPatternValid(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]{3}[a-z0-9]?$`)
	cases := map[string]string{
		"me-east-215-a": "meea", // m,e,e + first trailing letter 'a'
		"eu-west-101":   "euwe", // e,u,w + 'e' from "west"
		"fsn1":          "fsn1", // 3 letters + trailing digit (pattern-valid)
		"hel1":          "hel1",
		"":              "reg", // no letters → fallback
		"12":            "reg", // no 3 letters → fallback
	}
	for in, want := range cases {
		got := spineEnvRegionCode(in)
		if got != want {
			t.Errorf("spineEnvRegionCode(%q) = %q, want %q", in, got, want)
		}
		if !re.MatchString(got) {
			t.Errorf("spineEnvRegionCode(%q) = %q does NOT match CRD region pattern", in, got)
		}
	}
}

// TestSpineEnvProvider_FoldsToEnum proves the provider folds to the CRD enum
// (unknown / empty → huawei default).
func TestSpineEnvProvider_FoldsToEnum(t *testing.T) {
	if got := spineEnvProvider("Hetzner"); got != "hetzner" {
		t.Fatalf("spineEnvProvider(Hetzner) = %q, want hetzner", got)
	}
	if got := spineEnvProvider(""); got != "huawei" {
		t.Fatalf("spineEnvProvider(empty) = %q, want huawei", got)
	}
	if got := spineEnvProvider("digitalocean"); got != "huawei" {
		t.Fatalf("spineEnvProvider(unknown) = %q, want huawei default", got)
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
	orgRef := spineOrganizationSlug(dep)
	regions := spineRegions(dep)
	owner := spineOwnerLabels(dep)

	enrolled := h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, owner)
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
	enrolled2 := h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, owner)
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

// withCreateUpdateClusterApplier swaps the applySpineClusterResource seam for
// a Create-or-Update applier the dynamic-fake client supports (it cannot
// decode an ApplyPatchType body). Returns a restore func.
func withCreateUpdateClusterApplier(t *testing.T) func() {
	t.Helper()
	prev := applySpineClusterResource
	applySpineClusterResource = func(dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
		ri := dyn.Resource(gvr)
		var applied dynamic.ResourceInterface = ri
		if namespace != "" {
			applied = ri.Namespace(namespace)
		}
		existing, err := applied.Get(context.Background(), obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			_, uerr := applied.Update(context.Background(), obj, metav1.UpdateOptions{})
			return uerr
		}
		_, cerr := applied.Create(context.Background(), obj, metav1.CreateOptions{})
		return cerr
	}
	return func() { applySpineClusterResource = prev }
}

// spinePrereqFakeDynamic registers the Organization + Environment GVRs (plus
// HelmRelease + Application) so the prereq ensures resolve.
func spinePrereqFakeDynamic(seed ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		helmwatch.HelmReleaseGVR: "HelmReleaseList",
		ApplicationGVR():         "ApplicationList",
		organizationGVR():        "OrganizationList",
		EnvironmentGVR():         "EnvironmentList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
}

// TestEnsureSpinePrereqs_OrgAndMultiRegionEnv proves the producer stamps the
// CONSUMER prerequisites the application-controller hard-requires: the
// control-plane Organization (existence check) + the control-plane
// Environment whose len(spec.regions) == the deployment's real region count
// (so the controller resolves MULTI-region topology → active-passive → a
// Continuum CR is minted). This is the round-trip the orchestrator's live
// re-verify flagged as missing ("Continuum CRs never created").
func TestEnsureSpinePrereqs_OrgAndMultiRegionEnv(t *testing.T) {
	defer withCreateUpdateClusterApplier(t)()
	dyn := spinePrereqFakeDynamic()
	h := New(silentLogger())
	dep := newSpineTestDeployment() // 2 regions
	dep.Request.Regions[0].Provider = "huawei"
	dep.Request.Regions[1].Provider = "huawei"

	orgRef := spineOrganizationSlug(dep)
	envRef := spineEnvironmentRef(dep)
	regions := spineRegions(dep)

	if err := h.ensureSpineOrganization(dyn, dep, orgRef); err != nil {
		t.Fatalf("ensureSpineOrganization: %v", err)
	}
	if err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions); err != nil {
		t.Fatalf("ensureSpineEnvironment: %v", err)
	}

	// Organization exists with the platform-scope marker + the slug the
	// Environment references (so fetchOrganization passes).
	org, err := dyn.Resource(organizationGVR()).Get(context.Background(), orgRef, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("control-plane Organization %q not created: %v", orgRef, err)
	}
	if slug, _, _ := unstructured.NestedString(org.Object, "spec", "slug"); slug != orgRef {
		t.Fatalf("Organization spec.slug = %q, want %q", slug, orgRef)
	}
	if org.GetLabels()["openova.io/scope"] != "platform" {
		t.Fatalf("Organization missing platform-scope label: %v", org.GetLabels())
	}

	// Environment exists, references the org, and carries the REAL region
	// count (2) so ResolveTopology picks defaults.multi-region.
	env, err := dyn.Resource(EnvironmentGVR()).Get(context.Background(), envRef, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("control-plane Environment %q not created: %v", envRef, err)
	}
	if ref, _, _ := unstructured.NestedString(env.Object, "spec", "organizationRef"); ref != orgRef {
		t.Fatalf("Environment spec.organizationRef = %q, want %q", ref, orgRef)
	}
	envRegions, _, _ := unstructured.NestedSlice(env.Object, "spec", "regions")
	if len(envRegions) != 2 {
		t.Fatalf("Environment spec.regions count = %d, want 2 (drives multi-region topology)", len(envRegions))
	}
	if pl, _, _ := unstructured.NestedString(env.Object, "spec", "placement"); pl != "multi-region" {
		t.Fatalf("Environment spec.placement = %q, want multi-region", pl)
	}
	// Each region triple is CRD-schema-valid (provider enum + region pattern).
	re := regexp.MustCompile(`^[a-z]{3}[a-z0-9]?$`)
	for i, r := range envRegions {
		m := r.(map[string]interface{})
		if _, ok := spineEnvProviderAllowed[m["provider"].(string)]; !ok {
			t.Fatalf("region[%d] provider %q not in CRD enum", i, m["provider"])
		}
		if !re.MatchString(m["region"].(string)) {
			t.Fatalf("region[%d] region %q does not match CRD pattern", i, m["region"])
		}
	}

	// Idempotent re-run: no error, no duplicate Environment.
	if err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions); err != nil {
		t.Fatalf("ensureSpineEnvironment (re-run): %v", err)
	}
	list, _ := dyn.Resource(EnvironmentGVR()).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("Environment count after re-run = %d, want 1 (idempotent)", len(list.Items))
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
		// Every DR-capable spine row must carry a multi-region placement
		// mode mirroring its Blueprint's topology.defaults.multi-region —
		// without it the producer stamps singleton on a multi-region
		// Sovereign and no Continuum CR is ever minted.
		switch sc.MultiRegionMode {
		case "active-passive", "active-hot-standby":
		default:
			t.Fatalf("chart %q MultiRegionMode = %q, want a DR-capable multi-region mode", sc.Chart, sc.MultiRegionMode)
		}
	}
}

// TestShouldStartupSpineReconcile_Guards covers the #4212 startup-enrollment
// gate: a ready, handed-over deployment with a readable kubeconfig re-fires
// the idempotent spine producer (so an env that handed over before the
// producer shipped heals zero-touch on the next catalyst-api roll), while
// not-ready / not-handed-over / regionless / kubeconfig-lost records are
// skipped.
func TestShouldStartupSpineReconcile_Guards(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	kcPath := filepath.Join(dir, "depspine.yaml")
	if err := os.WriteFile(kcPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	now := time.Now().UTC()
	twoRegions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	cases := []struct {
		name string
		dep  *Deployment
		want bool
	}{
		{
			name: "ready-handed-over-kubeconfig-present",
			dep: &Deployment{ID: "s1", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, HandoverFiredAt: &now}},
			want: true,
		},
		{
			name: "single-region-ready-handed-over-still-enrolls",
			dep: &Deployment{ID: "s2", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions[:1]},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, HandoverFiredAt: &now}},
			want: true,
		},
		{
			name: "not-yet-handed-over-skipped",
			dep: &Deployment{ID: "s3", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath}},
			want: false,
		},
		{
			name: "failed-status-skipped",
			dep: &Deployment{ID: "s4", Status: "failed",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, HandoverFiredAt: &now}},
			want: false,
		},
		{
			name: "no-regions-mothership-self-skipped",
			dep: &Deployment{ID: "s5", Status: "ready",
				Request: provisioner.Request{},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, HandoverFiredAt: &now}},
			want: false,
		},
		{
			name: "kubeconfig-missing-warn-and-skip",
			dep: &Deployment{ID: "s6", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: filepath.Join(dir, "absent.yaml"), HandoverFiredAt: &now}},
			want: false,
		},
		{
			name: "result-nil-skipped",
			dep: &Deployment{ID: "s7", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Regions: twoRegions}},
			want: false,
		},
		{
			name: "single-region-via-request-region-field",
			dep: &Deployment{ID: "s8", Status: "ready",
				Request: provisioner.Request{SovereignFQDN: "t99.omani.works", Region: "me-east-215"},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, HandoverFiredAt: &now}},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.shouldStartupSpineReconcile(tc.dep); got != tc.want {
				t.Errorf("shouldStartupSpineReconcile = %v, want %v", got, tc.want)
			}
		})
	}
}
