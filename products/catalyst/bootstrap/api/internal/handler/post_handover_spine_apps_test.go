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
	"k8s.io/apimachinery/pkg/types"
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
	cr := renderSpineApplicationCR(sc, "t99-omani-works-cp", "t99-omani-works", []string{"me-east-215", "eu-west-101"}, owner, "")

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

	// #4416 — ADOPT, never roll (Invariant #3): the CR is bootstrap-owned +
	// names the existing bootstrap HelmRelease so the application-controller
	// takes its adoption path (observe the healthy HR's Ready condition) and
	// NEVER renders a duplicate fan-out HelmRelease.
	boot, found, _ := unstructured.NestedBool(cr.Object, "spec", "bootstrap")
	if !found || !boot {
		t.Fatalf("spec.bootstrap must be true so the controller ADOPTS the existing spine HR (no duplicate render); got found=%v boot=%v", found, boot)
	}
	hrName, _, _ := unstructured.NestedString(cr.Object, "spec", "helmRelease", "name")
	hrNS, _, _ := unstructured.NestedString(cr.Object, "spec", "helmRelease", "namespace")
	if hrName != "bp-openbao" {
		t.Fatalf("spec.helmRelease.name = %q, want bp-openbao", hrName)
	}
	if hrNS != "flux-system" {
		t.Fatalf("spec.helmRelease.namespace = %q, want flux-system", hrNS)
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

// TestRenderSpineApplicationCR_StampsRequiredConfig proves the producer stamps
// the minimal spec.PARAMETERS a Blueprint's configSchema requires (#4402/#4416).
// The live bp-keycloak configSchema is `required: [realmName]`, so a
// spine-keycloak CR with no parameters Fails validation in the
// application-controller (`missing properties: 'realmName'`) BEFORE it can reach
// the Continuum producer. The controller validates `spec.parameters` (the CRD +
// REST install + parseSpec all use that key) — the prior `spec.config` key was
// never read, which is why spine-keycloak Failed despite carrying the value. A
// component whose roster row carries no Config stamps no spec.parameters.
func TestRenderSpineApplicationCR_StampsRequiredConfig(t *testing.T) {
	owner := map[string]string{}

	// keycloak roster row carries Config{realmName} → spec.parameters.realmName set.
	kc := spineComponent{Chart: "keycloak", HRName: "bp-keycloak", BlueprintName: "bp-keycloak", BlueprintVersion: "1.0.0", MultiRegionMode: "active-hot-standby", Config: map[string]interface{}{"realmName": "sovereign"}}
	cr := renderSpineApplicationCR(kc, "t99-omani-works-cp", "t99-omani-works", []string{"me-east-215", "eu-west-101"}, owner, "")
	realm, found, _ := unstructured.NestedString(cr.Object, "spec", "parameters", "realmName")
	if !found {
		t.Fatalf("spec.parameters.realmName must be stamped for the keycloak spine (configSchema required: [realmName]); it was omitted")
	}
	if realm != "sovereign" {
		t.Fatalf("spec.parameters.realmName = %q, want sovereign", realm)
	}
	// Regression pin: the config must NOT land under the never-read spec.config key.
	if _, badFound, _ := unstructured.NestedMap(cr.Object, "spec", "config"); badFound {
		t.Fatalf("spec.config must NOT be set — the controller reads spec.parameters, never spec.config (the #4416 bug)")
	}

	// openbao roster row has no Config → no spec.parameters at all (Blueprint
	// declares no required config; stamping an empty map would be noise).
	ob := spineComponent{Chart: "openbao", HRName: "bp-openbao", BlueprintName: "bp-openbao", BlueprintVersion: "1.2.25", MultiRegionMode: "active-passive"}
	crOB := renderSpineApplicationCR(ob, "t99-omani-works-cp", "t99-omani-works", []string{"me-east-215", "eu-west-101"}, owner, "")
	if _, found, _ := unstructured.NestedMap(crOB.Object, "spec", "parameters"); found {
		t.Fatalf("spec.parameters must be absent for a component with no required config (openbao)")
	}

	// The live keycloak roster row carries the realmName config (regression pin).
	for _, sc := range drCapableSpine {
		if sc.Chart == "keycloak" {
			if sc.Config["realmName"] != "sovereign" {
				t.Fatalf("drCapableSpine keycloak Config.realmName = %v, want sovereign", sc.Config["realmName"])
			}
		}
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
	applySpineApplicationCR = func(dyn dynamic.Interface, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		ri := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace)
		existing, err := ri.Get(context.Background(), obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			return ri.Update(context.Background(), obj, metav1.UpdateOptions{})
		}
		return ri.Create(context.Background(), obj, metav1.CreateOptions{})
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

	enrolled := h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, owner, "")
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
	enrolled2 := h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, owner, "")
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
	applySpineClusterResource = func(dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		ri := dyn.Resource(gvr)
		var applied dynamic.ResourceInterface = ri
		if namespace != "" {
			applied = ri.Namespace(namespace)
		}
		existing, err := applied.Get(context.Background(), obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			// Preserve the UID the "apiserver" assigned on first create so a
			// re-run returns the same UID (the ownership chain stays stable).
			if obj.GetUID() == "" {
				obj.SetUID(existing.GetUID())
			}
			return applied.Update(context.Background(), obj, metav1.UpdateOptions{})
		}
		// Simulate the apiserver assigning a UID on create so ensureSpine*
		// can thread a real UID down the ownership chain (#5476). Deterministic
		// so tests can assert the exact owner-ref UID.
		if obj.GetUID() == "" {
			obj.SetUID(types.UID("uid-" + obj.GetName()))
		}
		return applied.Create(context.Background(), obj, metav1.CreateOptions{})
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

	orgUID, err := h.ensureSpineOrganization(dyn, dep, orgRef)
	if err != nil {
		t.Fatalf("ensureSpineOrganization: %v", err)
	}
	if _, err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions, orgUID); err != nil {
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
	// #4402 — spec.tier/billingMode/kind MUST be CRD-valid enum values, else
	// the apiserver rejects the Organization (tier∈{org,corporate},
	// billingMode∈{real,chargeback,showback}, kind∈{customer,internal}). An
	// earlier draft stamped tier:"platform"/billingMode:"free" — both rejected
	// live → the control-plane Org never landed → the spine wedged with no
	// Continuum. Pin the valid values so the regression can't return.
	if tier, _, _ := unstructured.NestedString(org.Object, "spec", "tier"); tier != "org" {
		t.Fatalf("Organization spec.tier = %q, want CRD-valid %q", tier, "org")
	}
	if bm, _, _ := unstructured.NestedString(org.Object, "spec", "billingMode"); bm != "showback" {
		t.Fatalf("Organization spec.billingMode = %q, want CRD-valid %q", bm, "showback")
	}
	if kind, _, _ := unstructured.NestedString(org.Object, "spec", "kind"); kind != "internal" {
		t.Fatalf("Organization spec.kind = %q, want CRD-valid %q", kind, "internal")
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
	if _, err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions, orgUID); err != nil {
		t.Fatalf("ensureSpineEnvironment (re-run): %v", err)
	}
	list, _ := dyn.Resource(EnvironmentGVR()).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("Environment count after re-run = %d, want 1 (idempotent)", len(list.Items))
	}
}

// newSpineAdjacentRegionDeployment builds a 2-region deployment whose cloud
// regions are ADJACENT (me-east-215-a / me-east-215-b) — the exact shape that
// pre-#5476 collapsed to a single "meea" region code twice, so the -cp
// Environment carried two IDENTICAL region triples (issue Part 2).
func newSpineAdjacentRegionDeployment() *Deployment {
	return &Deployment{
		ID: "dep-spine-adj",
		Request: provisioner.Request{
			SovereignFQDN: "hw291.omantel.biz",
			Regions: []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "me-east-215-a"},
				{Provider: "huawei", CloudRegion: "me-east-215-b"},
			},
		},
	}
}

// TestSpineEnvRegionCode_AdjacentRegionsCollide documents the raw producer
// defect: spineEnvRegionCode derives its code from the first three letters +
// the first trailing alnum, which never reaches the -a/-b discriminator, so
// two adjacent cloud regions collapse to the SAME base code. The Environment
// ensure MUST de-dupe this (asserted separately below).
func TestSpineEnvRegionCode_AdjacentRegionsCollide(t *testing.T) {
	a := spineEnvRegionCode("me-east-215-a")
	b := spineEnvRegionCode("me-east-215-b")
	if a != b {
		t.Fatalf("expected the raw region-code deriver to collide on adjacent regions "+
			"(this is the #5476 root cause); got %q and %q", a, b)
	}
	if a != "meea" {
		t.Fatalf("spineEnvRegionCode(me-east-215-a) = %q, want meea", a)
	}
}

// TestEnsureSpineEnvironment_DistinctRegionCodes is the region-dedup contract,
// exercised BOTH directions (#5476, issue Part 2).
//
//   - Direction 1 (fails pre-fix): a 2-region Sovereign whose cloud regions
//     collapse to the same base code (me-east-215-a / me-east-215-b → "meea")
//     must land TWO DISTINCT, CRD-pattern-valid region codes on the -cp
//     Environment. Pre-fix wrote "meea" twice; the assertion below fails then.
//   - Direction 2 (no-op): a 2-region Sovereign whose codes are already
//     distinct (me-east-215 / eu-west-101 → meea / euwe) is passed through
//     unchanged — the de-dupe never rewrites a code that does not collide.
func TestEnsureSpineEnvironment_DistinctRegionCodes(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]{3}[a-z0-9]?$`)

	readEnvRegionCodes := func(t *testing.T, dep *Deployment) []string {
		t.Helper()
		defer withCreateUpdateClusterApplier(t)()
		dyn := spinePrereqFakeDynamic()
		h := New(silentLogger())
		envRef := spineEnvironmentRef(dep)
		orgRef := spineOrganizationSlug(dep)
		regions := spineRegions(dep)
		if _, err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions, ""); err != nil {
			t.Fatalf("ensureSpineEnvironment: %v", err)
		}
		env, err := dyn.Resource(EnvironmentGVR()).Get(context.Background(), envRef, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Environment %q not created: %v", envRef, err)
		}
		raw, _, _ := unstructured.NestedSlice(env.Object, "spec", "regions")
		codes := make([]string, 0, len(raw))
		for i, r := range raw {
			m, ok := r.(map[string]interface{})
			if !ok {
				t.Fatalf("region[%d] not a map: %T", i, r)
			}
			code, _ := m["region"].(string)
			if !re.MatchString(code) {
				t.Fatalf("region[%d] code %q does not match CRD pattern", i, code)
			}
			codes = append(codes, code)
		}
		return codes
	}

	// Direction 1 — colliding adjacent regions are de-duped to distinct codes.
	adj := readEnvRegionCodes(t, newSpineAdjacentRegionDeployment())
	if len(adj) != 2 {
		t.Fatalf("adjacent-region env: got %d region codes, want 2", len(adj))
	}
	if adj[0] == adj[1] {
		t.Fatalf("adjacent-region env carries DUPLICATE region codes %v — the #5476 defect; "+
			"the -cp Environment cannot tell its two regions apart", adj)
	}

	// Direction 2 — already-distinct regions pass through unchanged (no-op).
	dis := readEnvRegionCodes(t, newSpineTestDeployment())
	if len(dis) != 2 || dis[0] != "meea" || dis[1] != "euwe" {
		t.Fatalf("already-distinct env codes = %v, want [meea euwe] unchanged", dis)
	}
}

// TestRenderSpineApplicationCR_EnvOwnerRefWiredOnlyWhenUIDKnown covers the pure
// render both directions (#5476, issue Part 3):
//   - envUID known  → the Application carries an ownerReference to its
//     Environment (Kubernetes-native cascade), fails pre-fix (no owner refs
//     existed at all).
//   - envUID empty  → NO ownerReference is written; a dangling ref with an
//     empty UID would be worse than none, so the producer skips it.
func TestRenderSpineApplicationCR_EnvOwnerRefWiredOnlyWhenUIDKnown(t *testing.T) {
	sc := spineComponent{Chart: "harbor", HRName: "bp-harbor", BlueprintName: "bp-harbor", BlueprintVersion: "1.2.26", MultiRegionMode: "active-hot-standby"}

	// Direction 1 — UID known.
	cr := renderSpineApplicationCR(sc, "hw291-omantel-biz-cp", "hw291-omantel-biz", []string{"me-east-215-a", "me-east-215-b"}, map[string]string{}, "env-uid-123")
	refs := cr.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("want exactly one ownerReference (Application → Environment); got %d", len(refs))
	}
	or := refs[0]
	if or.Kind != "Environment" || or.Name != "hw291-omantel-biz-cp" || string(or.UID) != "env-uid-123" {
		t.Fatalf("ownerReference = %+v, want Environment/hw291-omantel-biz-cp uid env-uid-123", or)
	}
	if or.APIVersion != EnvironmentGVR().Group+"/"+EnvironmentGVR().Version {
		t.Fatalf("ownerReference APIVersion = %q, want %q", or.APIVersion, EnvironmentGVR().Group+"/"+EnvironmentGVR().Version)
	}

	// Direction 2 — UID unknown → no owner ref (graceful skip, no regression).
	crNoUID := renderSpineApplicationCR(sc, "hw291-omantel-biz-cp", "hw291-omantel-biz", []string{"me-east-215-a"}, map[string]string{}, "")
	if len(crNoUID.GetOwnerReferences()) != 0 {
		t.Fatalf("no ownerReference must be written when the Environment UID is unknown; got %v", crNoUID.GetOwnerReferences())
	}
}

// TestSpineOwnershipChain_AppOwnedByEnvOwnedByOrg drives the FULL producer flow
// and proves the Kubernetes-native ownership chain the object model needs so
// cascade + GC no longer rest entirely on label hygiene (#5476, issue Part 3):
//
//	Application  --ownerRef-->  Environment  --ownerRef-->  Organization
//
// Both directions:
//   - the fresh flow wires every hop with the real server-assigned UID
//     (fails pre-fix, where NO Application CR carried ownerReferences and the
//     -cp Environment carried none back to its Organization);
//   - a second idempotent pass is a no-op — no duplicates, the owner refs stay
//     singular, and the UIDs are stable.
func TestSpineOwnershipChain_AppOwnedByEnvOwnedByOrg(t *testing.T) {
	defer withCreateUpdateClusterApplier(t)()
	defer withCreateUpdateApplier(t)()
	dyn := spinePrereqFakeDynamic(
		spineHRObj("bp-openbao"),
		spineHRObj("bp-keycloak"),
		spineHRObj("bp-harbor"),
		spineHRObj("bp-gitea"),
	)
	h := New(silentLogger())
	dep := newSpineAdjacentRegionDeployment()

	envRef := spineEnvironmentRef(dep)
	orgRef := spineOrganizationSlug(dep)
	regions := spineRegions(dep)
	owner := spineOwnerLabels(dep)

	runChain := func(t *testing.T) {
		t.Helper()
		orgUID, err := h.ensureSpineOrganization(dyn, dep, orgRef)
		if err != nil {
			t.Fatalf("ensureSpineOrganization: %v", err)
		}
		if orgUID == "" {
			t.Fatalf("ensureSpineOrganization returned an empty UID; the chain cannot wire")
		}
		envUID, err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions, orgUID)
		if err != nil {
			t.Fatalf("ensureSpineEnvironment: %v", err)
		}
		if envUID == "" {
			t.Fatalf("ensureSpineEnvironment returned an empty UID; the chain cannot wire")
		}

		// Environment --ownerRef--> Organization.
		env, err := dyn.Resource(EnvironmentGVR()).Get(context.Background(), envRef, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Environment %q not created: %v", envRef, err)
		}
		envRefs := env.GetOwnerReferences()
		if len(envRefs) != 1 || envRefs[0].Kind != "Organization" || envRefs[0].Name != orgRef || string(envRefs[0].UID) != orgUID {
			t.Fatalf("Environment ownerReferences = %+v, want single Organization/%s uid %s", envRefs, orgRef, orgUID)
		}

		// Application --ownerRef--> Environment, for every enrolled spine app.
		present := h.presentSpineHRs(dyn, dep)
		enrolled := h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, owner, envUID)
		if enrolled != len(present) || enrolled == 0 {
			t.Fatalf("enrolled = %d, want %d (>0)", enrolled, len(present))
		}
		for _, sc := range present {
			app, err := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).
				Get(context.Background(), spineApplicationName(sc.Chart), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("spine Application %s not created: %v", spineApplicationName(sc.Chart), err)
			}
			appRefs := app.GetOwnerReferences()
			if len(appRefs) != 1 || appRefs[0].Kind != "Environment" || appRefs[0].Name != envRef || string(appRefs[0].UID) != envUID {
				t.Fatalf("Application %s ownerReferences = %+v, want single Environment/%s uid %s",
					app.GetName(), appRefs, envRef, envUID)
			}
		}
	}

	// Direction 1 — fresh flow wires the whole chain.
	runChain(t)

	// Direction 2 — re-run is an idempotent no-op: no duplicate CRs, owner refs
	// stay singular, UIDs stable.
	runChain(t)

	envList, _ := dyn.Resource(EnvironmentGVR()).List(context.Background(), metav1.ListOptions{})
	if len(envList.Items) != 1 {
		t.Fatalf("Environment count after re-run = %d, want 1 (idempotent)", len(envList.Items))
	}
	orgList, _ := dyn.Resource(organizationGVR()).List(context.Background(), metav1.ListOptions{})
	if len(orgList.Items) != 1 {
		t.Fatalf("Organization count after re-run = %d, want 1 (idempotent)", len(orgList.Items))
	}
	appList, _ := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).List(context.Background(), metav1.ListOptions{})
	if len(appList.Items) != 4 {
		t.Fatalf("Application count after re-run = %d, want 4 (no duplicates)", len(appList.Items))
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
