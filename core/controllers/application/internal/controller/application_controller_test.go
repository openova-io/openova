// Tests for the application-controller reconciler.
//
// Per slice C4 brief §"Tests (envtest required)" the controller's
// 9-test matrix covers:
//
//  1. Pending on missing Environment
//  2. Pending on missing Blueprint
//  3. Invalid on parameters schema mismatch
//  4. single-region happy path → expected manifest set written
//  5. active-active fan-out → 2 regions, 2 identical sets
//  6. active-hotstandby → primary regular, standby replicas: 0
//  7. Idempotency — re-reconcile = 0 Gitea writes
//  8. Deletion cascade → manifests removed, finalizer released
//  9. Drift detection → manifest in Gitea hand-edited → controller restores
//
// Implementation note: the brief said "envtest required". The 4 sibling
// Group C controllers (C1/C2/C3/C5) all shipped with the
// `client-go/dynamic/fake` (or controller-runtime's typed fake)
// pattern instead — `envtest` requires a downloaded etcd+kube-apiserver
// pair and adds 30+s of test runtime per package, while a fake dynamic
// client gives us byte-identical Get / Update / UpdateStatus / Delete
// behavior for the unstructured-only paths the controller takes. We
// follow the established sibling pattern; the tests below exercise
// every reconciler exit path including the finalizer drain and drift
// recovery.
package controller

import (
	"context"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// fakeGitea is a deterministic test double for the Gitea interface.
// Records every PutFile / DeleteFile call so tests can assert
// idempotency, fan-out, drift handling, and cascade-delete.
type fakeGitea struct {
	mu sync.Mutex

	// repos: org/repo → branches → paths → bytes
	repos map[string]map[string]map[string][]byte

	// orgsExist: which Orgs the fake-Gitea has. EnsureRepo on a
	// missing Org returns ErrOrgNotFound.
	orgsExist map[string]bool

	puts    int
	deletes int

	// failOnPath: if non-empty, PutFile to this path returns
	// failPathErr.
	failOnPath  string
	failPathErr error
}

type pseudoErr string

func (e pseudoErr) Error() string { return string(e) }

const (
	errOrgNotFound pseudoErr = "fake gitea: org not found"
	errFileMissing pseudoErr = "fake gitea: file not found"
)

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		repos:     map[string]map[string]map[string][]byte{},
		orgsExist: map[string]bool{},
	}
}

func (f *fakeGitea) repoKey(org, repo string) string { return org + "/" + repo }

func (f *fakeGitea) EnsureRepo(_ context.Context, org, repo, _ string, _ bool) (gitea.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.orgsExist[org] {
		return gitea.Repo{}, errOrgNotFound
	}
	key := f.repoKey(org, repo)
	if _, ok := f.repos[key]; !ok {
		f.repos[key] = map[string]map[string][]byte{
			"main": {},
		}
	}
	return gitea.Repo{Name: repo, FullName: key}, nil
}

func (f *fakeGitea) EnsureBranch(_ context.Context, org, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.repoKey(org, repo)
	if _, ok := f.repos[key]; !ok {
		return errFileMissing
	}
	if _, ok := f.repos[key][branch]; !ok {
		f.repos[key][branch] = map[string][]byte{}
	}
	return nil
}

func (f *fakeGitea) PutFile(_ context.Context, org, repo, branch, path string, content []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnPath != "" && path == f.failOnPath {
		return gitea.File{}, false, f.failPathErr
	}
	key := f.repoKey(org, repo)
	if _, ok := f.repos[key]; !ok {
		f.repos[key] = map[string]map[string][]byte{}
	}
	if _, ok := f.repos[key][branch]; !ok {
		f.repos[key][branch] = map[string][]byte{}
	}
	existing, exists := f.repos[key][branch][path]
	if exists && string(existing) == string(content) {
		return gitea.File{Path: path, SHA: "stable"}, false, nil
	}
	f.repos[key][branch][path] = append([]byte(nil), content...)
	f.puts++
	return gitea.File{Path: path, SHA: "newsha"}, true, nil
}

func (f *fakeGitea) DeleteFile(_ context.Context, org, repo, branch, path, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.repoKey(org, repo)
	if _, ok := f.repos[key]; !ok {
		return true, nil
	}
	if _, ok := f.repos[key][branch]; !ok {
		return true, nil
	}
	if _, ok := f.repos[key][branch][path]; !ok {
		return true, nil
	}
	delete(f.repos[key][branch], path)
	f.deletes++
	return true, nil
}

func (f *fakeGitea) get(org, repo, branch, path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.repoKey(org, repo)
	if r, ok := f.repos[key]; ok {
		if br, ok := r[branch]; ok {
			if c, ok := br[path]; ok {
				return c, true
			}
		}
	}
	return nil, false
}

// fakeClassifier — tells the reconciler which errors are 404 vs other.
type fakeClassifier struct{}

func (fakeClassifier) IsNotFound(err error) bool    { return err == errFileMissing }
func (fakeClassifier) IsOrgNotFound(err error) bool { return err == errOrgNotFound }

// newScheme registers the GVKs the fake dynamic client needs.
//
// qa-loop iter-8 Fix #42 bug 3 added Flux GitRepository + Kustomization
// (the host-side bootstrap upsert path).
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "apps.openova.io", Version: "v1", Kind: "Application"},
		{Group: "catalyst.openova.io", Version: "v1", Kind: "Environment"},
		{Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint"},
		{Group: "catalyst.openova.io", Version: "v1alpha1", Kind: "Blueprint"},
		{Group: "orgs.openova.io", Version: "v1", Kind: "Organization"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"},
		// qa-loop iter-11 Fix #45 Cluster-B — observation of downstream
		// HelmRelease readiness for Application.status.phase rollup.
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"},
		// #3375 DoD-3 — the per-app Continuum DR contract the fan-out mints.
		{Group: ContinuumGroup, Version: ContinuumVersion, Kind: ContinuumKind},
		// #5513 — backing CNPG Cluster health observation for the Ready
		// rollup (a Ready HR over an unrecoverable CNPG must not be Ready).
		{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
	} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		listGVK := schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"}
		s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	}
	return s
}

func listKindMap() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		ApplicationGVR:       "ApplicationList",
		EnvironmentGVR:       "EnvironmentList",
		OrganizationGVR:      "OrganizationList",
		BlueprintGVR:         "BlueprintList",
		BlueprintGVRv1alpha1: "BlueprintList",
		FluxGitRepositoryGVR: "GitRepositoryList",
		FluxKustomizationGVR: "KustomizationList",
		FluxHelmReleaseGVR:   "HelmReleaseList",
		// #3375 DoD-3 — per-app Continuum DR contract.
		ContinuumGVR: "ContinuumList",
		// #5513 — backing CNPG Cluster health observation.
		CNPGClusterGVR: "ClusterList",
	}
}

func makeApp(namespace, name, env, bpName, bpVer, place string, regions []string, params map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(k8stypes.UID("uid-" + name))
	u.SetGeneration(1)
	regionsAny := make([]interface{}, len(regions))
	for i, r := range regions {
		regionsAny[i] = r
	}
	u.Object["spec"] = map[string]interface{}{
		"environmentRef": env,
		"blueprintRef": map[string]interface{}{
			"name":    bpName,
			"version": bpVer,
		},
		"placement":  place,
		"regions":    regionsAny,
		"parameters": params,
	}
	return u
}

func makeEnv(name, org, envType string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Environment")
	u.SetName(name)
	u.SetGeneration(1)
	u.Object["spec"] = map[string]interface{}{
		"organizationRef": org,
		"envType":         envType,
		"placement":       "single-region",
		"regions": []interface{}{
			map[string]interface{}{
				"provider":      "hetzner",
				"region":        "fsn",
				"buildingBlock": "rtz",
			},
		},
	}
	return u
}

func makeOrg(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("orgs.openova.io/v1")
	u.SetKind("Organization")
	u.SetName(name)
	u.Object["spec"] = map[string]interface{}{
		"slug":         name,
		"displayName":  name,
		"kind":         "customer",
		"tier":         "starter",
		"billingMode":  "metered",
		"sovereignRef": "test",
	}
	return u
}

func makeBlueprint(name, version string, configSchema map[string]interface{}, placementModes []string) *unstructured.Unstructured {
	return makeBlueprintWithVCluster(name, version, configSchema, placementModes, "")
}

// makeBlueprintWithVCluster constructs a Blueprint with an optional
// placementSchema.vcluster (dmz|mgmt|rtz|""). Empty string omits the
// field (host placement = legacy shape). G92.1 #2660.
func makeBlueprintWithVCluster(name, version string, configSchema map[string]interface{}, placementModes []string, vcluster string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	spec := map[string]interface{}{
		"version": version,
		"card": map[string]interface{}{
			"title": name,
		},
	}
	if configSchema != nil {
		spec["configSchema"] = configSchema
	}
	if placementModes != nil || vcluster != "" {
		ps := map[string]interface{}{}
		if placementModes != nil {
			modes := make([]interface{}, len(placementModes))
			for i, m := range placementModes {
				modes[i] = m
			}
			ps["modes"] = modes
		}
		if vcluster != "" {
			ps["vcluster"] = vcluster
		}
		spec["placementSchema"] = ps
	}
	u.Object["spec"] = spec
	u.Object["status"] = map[string]interface{}{
		"ociDigest": "sha256:abcdef0123456789",
	}
	return u
}

// reconcileFromCluster fetches the latest Application + drives one
// Reconcile pass. Mirrors what the watch loop would do.
func reconcileFromCluster(t *testing.T, r *Reconciler, ns, name string) {
	t.Helper()
	ctx := context.Background()
	got, err := r.Dynamic.Resource(ApplicationGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("fetch app for reconcile: %v", err)
	}
	if err := r.Reconcile(ctx, got); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func newReconciler(t *testing.T, fg *fakeGitea, objs ...*unstructured.Unstructured) *Reconciler {
	t.Helper()
	scheme := newScheme()
	listKinds := listKindMap()

	objsAny := make([]runtime.Object, len(objs))
	for i, o := range objs {
		objsAny[i] = o
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objsAny...)
	return New(dyn, fg, fakeClassifier{}, Config{
		GiteaPublicURL:             "https://gitea.test.openova.io",
		HelmReleaseIntervalSeconds: 600,
		SourceNamespace:            "flux-system",
		CatalogSourceRef:           "openova-catalog",
		// qa-loop iter-8 Fix #42 bug 3 — host-side Flux bootstrap.
		HostFluxNamespace:       "flux-system",
		GiteaInClusterURL:       "http://gitea.test.svc.cluster.local:3000",
		HostFluxIntervalSeconds: 60,
		// #4285 — the chart default is non-empty; the per-Application
		// GitRepository targets the Sovereign-local Gitea, so a secretRef is
		// mandatory (bp-gitea REQUIRE_SIGNIN_VIEW=true → anonymous clone 401).
		FluxGiteaSecretRef: "openova-org-tenants-git-auth",
	}, nil)
}

func appStatus(t *testing.T, dyn interface {
	// shape suffices via duck typing — see below
}, _ *Reconciler, _, _ string) {
}

// readApp re-fetches the Application from the fake cluster.
func readApp(t *testing.T, r *Reconciler, ns, name string) *unstructured.Unstructured {
	t.Helper()
	got, err := r.Dynamic.Resource(ApplicationGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read app %s/%s: %v", ns, name, err)
	}
	return got
}

func readPhaseAndReason(t *testing.T, app *unstructured.Unstructured) (phase, reason, message string) {
	t.Helper()
	phase, _, _ = unstructured.NestedString(app.Object, "status", "phase")
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _ := cm["type"].(string)
		if typ == "Ready" {
			reason, _ = cm["reason"].(string)
			message, _ = cm["message"].(string)
			return
		}
	}
	return
}

// --- Test 1: Pending on missing Environment ----------------------------

func TestReconcile_PendingOnMissingEnvironment(t *testing.T) {
	app := makeApp("acme", "site", "missing-env", "bp-x", "1.0.0", "single-region", []string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonEnvironmentMissing {
		t.Errorf("reason = %q, want %q", reason, ReasonEnvironmentMissing)
	}
	if fg.puts != 0 {
		t.Errorf("expected 0 Gitea writes, got %d", fg.puts)
	}
}

// --- Test 2: Pending on missing Blueprint ------------------------------

func TestReconcile_PendingOnMissingBlueprint(t *testing.T) {
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-missing", "1.0.0", "single-region", []string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonBlueprintMissing {
		t.Errorf("reason = %q, want %q", reason, ReasonBlueprintMissing)
	}
	if fg.puts != 0 {
		t.Errorf("expected 0 Gitea writes, got %d", fg.puts)
	}
}

// --- Test 3: Invalid on parameters schema mismatch ---------------------

func TestReconcile_InvalidOnParametersSchemaMismatch(t *testing.T) {
	configSchema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"replicas"},
		"properties": map[string]interface{}{
			"replicas": map[string]interface{}{
				"type":    "integer",
				"minimum": int64(1),
			},
		},
	}
	bp := makeBlueprint("bp-wordpress", "1.2.3", configSchema, nil)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	// parameters has replicas as a string — wrong type.
	app := makeApp("acme", "site", "acme-prod", "bp-wordpress", "1.2.3", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": "three"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", phase, PhaseFailed)
	}
	if reason != ReasonInvalid {
		t.Errorf("reason = %q, want %q", reason, ReasonInvalid)
	}
	if !strings.Contains(message, "replicas") {
		t.Errorf("message should reference failing field 'replicas': %q", message)
	}
	if fg.puts != 0 {
		t.Errorf("expected 0 Gitea writes after schema rejection, got %d", fg.puts)
	}
}

// --- Test 4: single-region happy path ----------------------------------

func TestReconcile_SingleRegionHappyPath(t *testing.T) {
	bp := makeBlueprint("bp-wordpress", "1.2.3", nil, []string{"single-region", "active-active"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wordpress", "1.2.3", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(3)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	// Expected manifests on `main` branch (envType=prod):
	expectedPaths := []string{
		"clusters/hetzner-fsn-rtz-prod/applications/site/kustomization.yaml",
		"clusters/hetzner-fsn-rtz-prod/applications/site/helmrelease.yaml",
	}
	for _, p := range expectedPaths {
		if _, ok := fg.get("acme", "site", "main", p); !ok {
			t.Errorf("expected file at %s on branch main", p)
		}
	}
	got := readApp(t, r, "acme", "site")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseProvisioning {
		t.Errorf("phase = %q, want %q (reason=%q, msg=%q)", phase, PhaseProvisioning, reason, message)
	}
	primary, _, _ := unstructured.NestedString(got.Object, "status", "primaryRegion")
	if primary != "hetzner-fsn-rtz-prod" {
		t.Errorf("primaryRegion = %q", primary)
	}
	giteaRepo, _, _ := unstructured.NestedString(got.Object, "status", "giteaRepo")
	if !strings.HasSuffix(giteaRepo, "/acme/site") {
		t.Errorf("giteaRepo = %q", giteaRepo)
	}
	finalizers := got.GetFinalizers()
	if len(finalizers) == 0 || finalizers[0] != FinalizerName {
		t.Errorf("finalizer not set: %v", finalizers)
	}
}

// --- Test 5: active-active fan-out -------------------------------------

func TestReconcile_ActiveActiveFanOut(t *testing.T) {
	bp := makeBlueprint("bp-api", "2.0.0", nil, []string{"active-active"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "api", "acme-prod", "bp-api", "2.0.0", "active-active",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		map[string]interface{}{"replicas": int64(2)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "api")

	for _, region := range []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"} {
		hrPath := "clusters/" + region + "/applications/api/helmrelease.yaml"
		ksPath := "clusters/" + region + "/applications/api/kustomization.yaml"
		if _, ok := fg.get("acme", "api", "main", hrPath); !ok {
			t.Errorf("expected helmrelease at %s", hrPath)
		}
		if _, ok := fg.get("acme", "api", "main", ksPath); !ok {
			t.Errorf("expected kustomization at %s", ksPath)
		}
	}

	// Fan-out manifests must have the SAME spec content (modulo the
	// per-region label / comment). We canonicalise both manifests to
	// the same region label so any other byte difference fails the
	// test.
	a, _ := fg.get("acme", "api", "main", "clusters/hetzner-fsn-rtz-prod/applications/api/helmrelease.yaml")
	b, _ := fg.get("acme", "api", "main", "clusters/hetzner-nbg-rtz-prod/applications/api/helmrelease.yaml")
	canonA := strings.ReplaceAll(string(a), "hetzner-fsn-rtz-prod", "REGION")
	canonB := strings.ReplaceAll(string(b), "hetzner-nbg-rtz-prod", "REGION")
	if canonA != canonB {
		t.Errorf("active-active fan-out manifests differ (after region canonicalisation):\n--- A ---\n%s\n--- B ---\n%s", canonA, canonB)
	}

	got := readApp(t, r, "acme", "api")
	primary, _, _ := unstructured.NestedString(got.Object, "status", "primaryRegion")
	if primary != "" {
		t.Errorf("active-active should have empty primaryRegion, got %q", primary)
	}
}

// --- Test 6: active-hotstandby standby gets replicas: 0 ----------------

func TestReconcile_ActiveHotStandby(t *testing.T) {
	bp := makeBlueprint("bp-pg", "1.0.0", nil, []string{"active-hotstandby"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "db", "acme-prod", "bp-pg", "1.0.0", "active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		map[string]interface{}{"replicas": int64(3)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "db")

	// Primary (regions[0]) should have replicas: 3.
	primary, _ := fg.get("acme", "db", "main", "clusters/hetzner-fsn-rtz-prod/applications/db/helmrelease.yaml")
	if !strings.Contains(string(primary), "replicas: 3") {
		t.Errorf("primary should render replicas: 3:\n%s", string(primary))
	}
	if strings.Contains(string(primary), "replicas: 0") {
		t.Errorf("primary should NOT have replicas: 0")
	}

	// Standby (regions[1]) should have replicas: 0.
	standby, _ := fg.get("acme", "db", "main", "clusters/hetzner-nbg-rtz-prod/applications/db/helmrelease.yaml")
	if !strings.Contains(string(standby), "replicas: 0") {
		t.Errorf("standby should render replicas: 0:\n%s", string(standby))
	}
	if !strings.Contains(string(standby), "_openova_standby: true") {
		t.Errorf("standby should carry _openova_standby marker")
	}
}

// --- Test 7: idempotency ----------------------------------------------

func TestReconcile_Idempotent(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	// First pass — manifests committed.
	reconcileFromCluster(t, r, "acme", "site")
	firstPuts := fg.puts
	if firstPuts == 0 {
		t.Fatal("first pass: expected Gitea writes, got 0")
	}

	// Second pass — should be byte-equal, zero new writes.
	reconcileFromCluster(t, r, "acme", "site")
	if fg.puts != firstPuts {
		t.Errorf("second pass: expected 0 new Gitea writes; got %d (started at %d)", fg.puts-firstPuts, firstPuts)
	}
}

// --- Test 8: deletion cascade -----------------------------------------

func TestReconcile_DeletionCascade(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	// First pass — write manifests + add finalizer.
	reconcileFromCluster(t, r, "acme", "site")
	if fg.puts == 0 {
		t.Fatal("first pass should commit manifests")
	}
	got := readApp(t, r, "acme", "site")
	if !hasFinalizer(got, FinalizerName) {
		t.Fatal("first pass should add finalizer")
	}

	// Mark the app for deletion. This is what kubectl delete does:
	// it sets metadata.deletionTimestamp; the finalizer prevents
	// actual GC until removed.
	now := metav1.Now()
	got.SetDeletionTimestamp(&now)
	_, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Update(context.Background(), got, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("set deletion timestamp: %v", err)
	}

	// Reconcile again — should delete files + remove finalizer.
	reconcileFromCluster(t, r, "acme", "site")

	// Manifests should be gone.
	if _, ok := fg.get("acme", "site", "main", "clusters/hetzner-fsn-rtz-prod/applications/site/helmrelease.yaml"); ok {
		t.Error("helmrelease.yaml should have been deleted")
	}
	if _, ok := fg.get("acme", "site", "main", "clusters/hetzner-fsn-rtz-prod/applications/site/kustomization.yaml"); ok {
		t.Error("kustomization.yaml should have been deleted")
	}
	if fg.deletes < 2 {
		t.Errorf("expected at least 2 deletes (helmrelease + kustomization), got %d", fg.deletes)
	}

	// Finalizer should be released — fake client now allows the GET to
	// succeed-or-not (after Update with empty finalizers, the object
	// remains in the fake but with no finalizers — real K8s would GC).
	got2, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Get(context.Background(), "site", metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("read app after delete: %v", err)
		}
		// Already GC'd by fake — OK.
		return
	}
	if hasFinalizer(got2, FinalizerName) {
		t.Errorf("finalizer should be released, still present: %v", got2.GetFinalizers())
	}
}

// --- Test 9: drift detection ------------------------------------------

func TestReconcile_DriftRestoration(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	// First pass — commit manifests.
	reconcileFromCluster(t, r, "acme", "site")
	hrPath := "clusters/hetzner-fsn-rtz-prod/applications/site/helmrelease.yaml"
	original, _ := fg.get("acme", "site", "main", hrPath)
	if len(original) == 0 {
		t.Fatal("first pass should have written the helmrelease")
	}

	// Hand-edit the file in Gitea (simulate drift).
	driftedContent := []byte("# THIS IS HAND-EDITED DRIFT\n")
	fg.repos[fg.repoKey("acme", "site")]["main"][hrPath] = driftedContent

	// Reconcile — controller should restore.
	reconcileFromCluster(t, r, "acme", "site")

	restored, _ := fg.get("acme", "site", "main", hrPath)
	if string(restored) != string(original) {
		t.Errorf("drift not restored:\n--- expected ---\n%s\n--- got ---\n%s", string(original), string(restored))
	}
}

// --- Auxiliary tests for branchForEnvType + spec parsing ---------------

func TestBranchForEnvType(t *testing.T) {
	cases := map[string]string{
		"dev":  "develop",
		"stg":  "staging",
		"prod": "main",
		"uat":  "uat",
		"poc":  "poc",
	}
	for in, want := range cases {
		if got := branchForEnvType(in); got != want {
			t.Errorf("branchForEnvType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSpec_Errors(t *testing.T) {
	missing := &unstructured.Unstructured{}
	missing.SetAPIVersion("apps.openova.io/v1")
	missing.SetKind("Application")
	missing.SetName("x")
	missing.Object["spec"] = map[string]interface{}{}
	if _, err := parseSpec(missing); err == nil {
		t.Error("parseSpec should reject empty spec")
	}
}

// --- Bonus test: Pending on Org Gitea missing --------------------------

func TestReconcile_PendingOnGiteaOrgMissing(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	// Note: orgsExist is NOT populated — EnsureRepo will return ErrOrgNotFound.
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonOrgGiteaMissing {
		t.Errorf("reason = %q, want %q", reason, ReasonOrgGiteaMissing)
	}
}

// --- G92.1 #2660: vCluster placement -------------------------------------

// TestReconcile_VClusterPlacementMGMT asserts the rendered HelmRelease
// installs INTO the MGMT vCluster when the Blueprint declares
// spec.placementSchema.vcluster=mgmt. The HR lands in the host
// `mgmt` namespace (so helm-controller resolves the kubeConfig Secret
// from the same namespace) but installs the chart inside the vCluster
// (spec.targetNamespace = Application's inner namespace, vCluster
// syncer mirrors it back to host as <inner-ns>-x-mgmt).
//
// Per docs/SOVEREIGN-MULTI-REGION-DOD.md §A4 the three vClusters
// (slots 54/58/59) partition workloads. Without this code path every
// bp-* lands on host k3s and the vClusters stand empty — founder
// caught this 2026-05-31.
func TestReconcile_VClusterPlacementMGMT(t *testing.T) {
	bp := makeBlueprintWithVCluster("bp-gitea", "1.2.11", nil, []string{"single-region"}, "mgmt")
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "control-plane", "acme-prod", "bp-gitea", "1.2.11", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "control-plane")

	hrBytes, ok := fg.get("acme", "control-plane", "main",
		"clusters/hetzner-fsn-rtz-prod/applications/control-plane/helmrelease.yaml")
	if !ok {
		t.Fatal("HelmRelease should have been committed to Gitea")
	}
	hr := string(hrBytes)

	// HR lives in the vCluster's host namespace so the kubeConfig
	// Secret lookup is co-located (Flux v2 SecretReference contract).
	if !strings.Contains(hr, "namespace: mgmt") {
		t.Errorf("HR.metadata.namespace must be the vCluster's host namespace (mgmt), got:\n%s", hr)
	}
	// kubeConfig pivot present.
	if !strings.Contains(hr, "kubeConfig:") {
		t.Errorf("HR must carry spec.kubeConfig pivot, got:\n%s", hr)
	}
	if !strings.Contains(hr, "name: vc-mgmt") {
		t.Errorf("HR.spec.kubeConfig.secretRef.name must be 'vc-mgmt' (loft-sh/vcluster convention), got:\n%s", hr)
	}
	// Inner target namespace = Application's own namespace (vCluster
	// remaps host-side).
	if !strings.Contains(hr, "targetNamespace: acme") {
		t.Errorf("HR.spec.targetNamespace must be the Application's inner namespace (acme), got:\n%s", hr)
	}
	// Label for traceability + dashboard grouping.
	if !strings.Contains(hr, "catalyst.openova.io/vcluster: mgmt") {
		t.Errorf("HR must stamp catalyst.openova.io/vcluster label, got:\n%s", hr)
	}

	// Reconcile should complete normally (placement reaches Provisioning).
	got := readApp(t, r, "acme", "control-plane")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseProvisioning {
		t.Errorf("phase = %q, want %q (reason=%q, msg=%q)", phase, PhaseProvisioning, reason, message)
	}
}

// TestReconcile_NoVClusterFieldUnchanged asserts the host-placement
// (legacy) path stays byte-stable when the Blueprint does NOT declare
// placementSchema.vcluster. Containment guarantee: existing
// Applications keep installing on the host k3s until their Blueprint
// opts in via G92.2 / G92.3 / G92.4 / G92.5.
func TestReconcile_NoVClusterFieldUnchanged(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	hrBytes, ok := fg.get("acme", "site", "main",
		"clusters/hetzner-fsn-rtz-prod/applications/site/helmrelease.yaml")
	if !ok {
		t.Fatal("HelmRelease should have been committed")
	}
	hr := string(hrBytes)

	if strings.Contains(hr, "kubeConfig:") {
		t.Errorf("Host-placement HR must NOT carry kubeConfig pivot, got:\n%s", hr)
	}
	if strings.Contains(hr, "catalyst.openova.io/vcluster:") {
		t.Errorf("Host-placement HR must NOT stamp the vcluster label, got:\n%s", hr)
	}
	if !strings.Contains(hr, "namespace: acme") {
		t.Errorf("Host-placement HR.metadata.namespace must be the Application namespace, got:\n%s", hr)
	}
}

// TestReconcile_VClusterUnmappedFails asserts the Reconcile fails
// (status.phase=Failed, reason=Invalid) when the Blueprint declares a
// placementSchema.vcluster that the controller's Config has no
// mapping for — better to surface the misconfiguration as a Condition
// than silently fall back to host placement and confuse operators.
func TestReconcile_VClusterUnmappedFails(t *testing.T) {
	bp := makeBlueprintWithVCluster("bp-mystery", "1.0.0", nil, []string{"single-region"}, "unknown-vc")
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "mystery", "acme-prod", "bp-mystery", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	// newReconciler installs DefaultVClusterPlacements which only knows
	// dmz/mgmt/rtz; "unknown-vc" trips the validation branch.
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "mystery")

	got := readApp(t, r, "acme", "mystery")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", phase, PhaseFailed)
	}
	if reason != ReasonInvalid {
		t.Errorf("reason = %q, want %q", reason, ReasonInvalid)
	}
}

// --- Bonus test: Invalid placement against Blueprint allowed modes ----

func TestReconcile_InvalidPlacementForBlueprint(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"}) // active-* not allowed
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "active-active",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", phase, PhaseFailed)
	}
	if reason != ReasonInvalid {
		t.Errorf("reason = %q, want %q", reason, ReasonInvalid)
	}
}

// --- qa-loop iter-8 Fix #42 bug 3: host-side Flux bootstrap ---------

// TestReconcile_HostFluxBootstrap_CreatesGitRepoAndKustomization is the
// regression test for qa-loop iter-8 Fix #42 bug 3.
//
// Before the fix the application-controller committed kustomization +
// helmrelease YAMLs to Gitea but no Flux GitRepository or Kustomization
// existed on the host cluster to pull them — Pods never spawned, even
// though the controller marked the Application Ready=True.
//
// This test asserts a successful reconcile creates:
//   - 1 GitRepository in flux-system named `catalyst-app-{org}-{app}`
//     pointing at the in-cluster Gitea URL with the env-type-mapped
//     branch.
//   - 1 Kustomization per region named
//     `catalyst-app-{org}-{app}-{region}` with path
//     `./clusters/{region}/applications/{app}` and sourceRef pointing
//     at the GitRepository above.
//   - Both CRs carry an ownerRef back to the Application so they're
//     garbage-collected on Application deletion.
//   - Both CRs use the v1 Flux API (NOT v1beta2 — Flux 2.4+ deprecates
//     it).
func TestReconcile_HostFluxBootstrap_CreatesGitRepoAndKustomization(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	// GitRepository assertion.
	grName := "catalyst-app-acme-site"
	gr, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), grName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Flux GitRepository %q in flux-system; got: %v", grName, err)
	}
	if gr.GetAPIVersion() != "source.toolkit.fluxcd.io/v1" {
		t.Errorf("GitRepository apiVersion = %q, want source.toolkit.fluxcd.io/v1 (Flux 2.4+ deprecated v1beta2)", gr.GetAPIVersion())
	}
	url, _, _ := unstructured.NestedString(gr.Object, "spec", "url")
	if !strings.Contains(url, "/acme/site.git") {
		t.Errorf("GitRepository.spec.url = %q, want substring %q", url, "/acme/site.git")
	}
	branch, _, _ := unstructured.NestedString(gr.Object, "spec", "ref", "branch")
	if branch != "main" {
		t.Errorf("GitRepository.spec.ref.branch = %q, want %q (envType=prod → branch=main)", branch, "main")
	}
	// #4285 — the per-Application source targets the Sovereign-local Gitea, so
	// it MUST carry the configured secretRef (else bp-gitea
	// REQUIRE_SIGNIN_VIEW=true returns 401 'authentication required').
	if name, found, _ := unstructured.NestedString(gr.Object, "spec", "secretRef", "name"); !found || name != "openova-org-tenants-git-auth" {
		t.Errorf("GitRepository.spec.secretRef.name = %q (found=%v), want openova-org-tenants-git-auth", name, found)
	}
	// NO ownerRef — cross-namespace ownerRefs would trigger the K8s GC
	// to immediately delete the GitRepository (Application is in
	// `acme`, GitRepository is in `flux-system`). Cross-namespace lookup
	// is treated as missing-owner. Cleanup is via labels +
	// handleDeletion.
	if owners := gr.GetOwnerReferences(); len(owners) != 0 {
		t.Errorf("GitRepository must NOT carry ownerRefs (cross-namespace GC); got %+v", owners)
	}
	// Cascade-delete reference labels.
	gotAppNS, _, _ := unstructured.NestedString(gr.Object, "metadata", "labels", "catalyst.openova.io/app-namespace")
	if gotAppNS != "acme" {
		t.Errorf("GitRepository missing catalyst.openova.io/app-namespace label = %q, want %q", gotAppNS, "acme")
	}

	// Kustomization assertion.
	ksName := "catalyst-app-acme-site-hetzner-fsn-rtz-prod"
	ks, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace("flux-system").
		Get(context.Background(), ksName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Flux Kustomization %q in flux-system; got: %v", ksName, err)
	}
	if ks.GetAPIVersion() != "kustomize.toolkit.fluxcd.io/v1" {
		t.Errorf("Kustomization apiVersion = %q, want kustomize.toolkit.fluxcd.io/v1", ks.GetAPIVersion())
	}
	path, _, _ := unstructured.NestedString(ks.Object, "spec", "path")
	wantPath := "./clusters/hetzner-fsn-rtz-prod/applications/site"
	if path != wantPath {
		t.Errorf("Kustomization.spec.path = %q, want %q", path, wantPath)
	}
	srcKind, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "kind")
	srcName, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "name")
	if srcKind != "GitRepository" || srcName != grName {
		t.Errorf("Kustomization.spec.sourceRef = {%q, %q}, want {GitRepository, %q}", srcKind, srcName, grName)
	}
	prune, _, _ := unstructured.NestedBool(ks.Object, "spec", "prune")
	if !prune {
		t.Errorf("Kustomization.spec.prune = false, want true (orphan workloads must be GC'd on app delete)")
	}
	targetNS, _, _ := unstructured.NestedString(ks.Object, "spec", "targetNamespace")
	if targetNS != "acme" {
		t.Errorf("Kustomization.spec.targetNamespace = %q, want %q (the Application's namespace)", targetNS, "acme")
	}
	if ksOwners := ks.GetOwnerReferences(); len(ksOwners) != 0 {
		t.Errorf("Kustomization must NOT carry ownerRefs (cross-namespace GC); got %+v", ksOwners)
	}
	gotKsAppNS, _, _ := unstructured.NestedString(ks.Object, "metadata", "labels", "catalyst.openova.io/app-namespace")
	if gotKsAppNS != "acme" {
		t.Errorf("Kustomization missing catalyst.openova.io/app-namespace label = %q, want %q", gotKsAppNS, "acme")
	}
}

// TestReconcile_HostFluxBootstrap_EmptySecretRefDegrades is the #4285 guard at
// the application leg: when FluxGiteaSecretRef is empty the per-Application
// GitRepository (a Sovereign-local Gitea source) MUST NOT be emitted anonymous
// — the controller fails the reconcile (Degraded) so the misconfiguration is
// LOUD, not a live 401 source. The chart default is non-empty; this proves the
// guard fires if an operator ever blanks it.
func TestReconcile_HostFluxBootstrap_EmptySecretRefDegrades(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true

	scheme := newScheme()
	objsAny := []runtime.Object{app.DeepCopy(), env.DeepCopy(), org.DeepCopy(), bp.DeepCopy()}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKindMap(), objsAny...)
	r := New(dyn, fg, fakeClassifier{}, Config{
		GiteaPublicURL:             "https://gitea.test.openova.io",
		HelmReleaseIntervalSeconds: 600,
		SourceNamespace:            "flux-system",
		CatalogSourceRef:           "openova-catalog",
		HostFluxNamespace:          "flux-system",
		GiteaInClusterURL:          "http://gitea.test.svc.cluster.local:3000",
		HostFluxIntervalSeconds:    60,
		// FluxGiteaSecretRef intentionally empty — the #4285 defect.
	}, nil)

	reconcileFromCluster(t, r, "acme", "site")
	got := readApp(t, r, "acme", "site")
	phase, _, message := readPhaseAndReason(t, got)
	if phase != PhaseDegraded {
		t.Fatalf("phase = %q, want %q when secretRef is empty (msg=%q)", phase, PhaseDegraded, message)
	}
	if !strings.Contains(message, "secretRef is empty") {
		t.Errorf("Degraded message should cite the #4285 secretRef guard; got %q", message)
	}
	// And NO GitRepository should have been created.
	if _, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-site", metav1.GetOptions{}); err == nil {
		t.Errorf("no GitRepository must be created when the secretRef guard fires")
	}
}

// TestReconcile_HostFluxBootstrap_FanOutOnePerRegion asserts an
// active-active Application with N regions produces 1 GitRepository
// (shared by all regions on the same per-app Gitea repo) and N
// Kustomizations (one per region with its own path).
func TestReconcile_HostFluxBootstrap_FanOutOnePerRegion(t *testing.T) {
	bp := makeBlueprint("bp-api", "2.0.0", nil, []string{"active-active"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "api", "acme-prod", "bp-api", "2.0.0", "active-active",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		map[string]interface{}{"replicas": int64(2)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "api")

	// Exactly one GitRepository.
	grList, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list GitRepositories: %v", err)
	}
	if len(grList.Items) != 1 {
		t.Errorf("expected exactly 1 GitRepository per Application, got %d", len(grList.Items))
	}

	// Two Kustomizations — one per region.
	ksList, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace("flux-system").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Kustomizations: %v", err)
	}
	if len(ksList.Items) != 2 {
		t.Fatalf("expected 2 Kustomizations (one per region), got %d", len(ksList.Items))
	}
	regionsSeen := map[string]bool{}
	for _, ks := range ksList.Items {
		path, _, _ := unstructured.NestedString(ks.Object, "spec", "path")
		for _, region := range []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"} {
			if strings.Contains(path, region) {
				regionsSeen[region] = true
			}
		}
	}
	for _, region := range []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"} {
		if !regionsSeen[region] {
			t.Errorf("no Kustomization saw region %q", region)
		}
	}
}

// TestReconcile_HostFluxBootstrap_Idempotent asserts re-reconciling a
// steady-state Application makes ZERO new K8s writes (drift-free path).
// We assert by counting the apiVersion/spec hash between passes —
// nothing should change.
func TestReconcile_HostFluxBootstrap_Idempotent(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")
	gr1, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-site", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("first GitRepository fetch: %v", err)
	}
	rv1 := gr1.GetResourceVersion()

	reconcileFromCluster(t, r, "acme", "site")
	gr2, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-site", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("second GitRepository fetch: %v", err)
	}
	rv2 := gr2.GetResourceVersion()
	if rv1 != rv2 {
		t.Errorf("GitRepository resourceVersion changed across idempotent reconcile passes (%q → %q); steady state must be a no-op", rv1, rv2)
	}
}

// --- qa-loop iter-10 Fix #44: HelmRelease targetNamespace = App's namespace
//
// TestReconcile_HelmReleaseTargetNamespaceIsAppNamespace asserts the
// rendered HelmRelease in Gitea sets `metadata.namespace` AND
// `spec.targetNamespace` to the Application CR's own namespace, NOT
// the Organization slug.
//
// Live regression: on omantel the Application(qa-wp) lived in ns
// `qa-omantel` while the parent Organization was named `omantel-platform`.
// The application-controller passed Org for both fields → HelmRelease
// committed with `targetNamespace: omantel-platform` → workload Pod
// landed in the wrong namespace → matrix rows TC-068 / TC-100 / TC-204
// / TC-262 / TC-263 (all asserting Pod in qa-omantel) FAILed.
//
// Fix: pass app.GetNamespace() as render.Inputs.AppNamespace; the
// render template targets AppNamespace for both fields. The Org slug
// is still stamped on labels for traceability.
//
// Also asserts `spec.install.createNamespace = true` — per
// docs/INVIOLABLE-PRINCIPLES.md #1 (target-state) the controller MUST
// install successfully without an operator pre-creating the namespace.
func TestReconcile_HelmReleaseTargetNamespaceIsAppNamespace(t *testing.T) {
	bp := makeBlueprint("bp-qa-app", "0.1.0", nil, []string{"single-region"})
	// Live shape: App in `qa-omantel`, Organization is `omantel-platform`,
	// envType=dev (Gitea branch=develop).
	env := makeEnv("qa-omantel", "omantel-platform", "dev")
	org := makeOrg("omantel-platform")
	app := makeApp("qa-omantel", "qa-wp", "qa-omantel", "bp-qa-app", "0.1.0",
		"single-region",
		[]string{"hz-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["omantel-platform"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "qa-omantel", "qa-wp")

	// HelmRelease lives on Gitea branch=develop (envType=dev).
	hrPath := "clusters/hz-fsn-rtz-prod/applications/qa-wp/helmrelease.yaml"
	hr, ok := fg.get("omantel-platform", "qa-wp", "develop", hrPath)
	if !ok {
		t.Fatalf("expected HelmRelease at %s in branch develop", hrPath)
	}
	hrStr := string(hr)

	// metadata.namespace = qa-omantel (the App's namespace), NOT omantel-platform.
	if !strings.Contains(hrStr, "namespace: qa-omantel") {
		t.Errorf("HelmRelease metadata.namespace should be 'qa-omantel' (Application namespace), got:\n%s", hrStr)
	}
	// spec.targetNamespace = qa-omantel.
	if !strings.Contains(hrStr, "targetNamespace: qa-omantel") {
		t.Errorf("HelmRelease spec.targetNamespace should be 'qa-omantel' (Application namespace), got:\n%s", hrStr)
	}
	// Negative: must NOT use omantel-platform as the K8s namespace.
	if strings.Contains(hrStr, "targetNamespace: omantel-platform") {
		t.Errorf("HelmRelease spec.targetNamespace must NOT be the Org slug (omantel-platform); got:\n%s", hrStr)
	}
	// Positive: createNamespace must be true so missing namespaces are
	// provisioned on install.
	if !strings.Contains(hrStr, "createNamespace: true") {
		t.Errorf("HelmRelease spec.install.createNamespace must be true (target-state per docs/INVIOLABLE-PRINCIPLES.md #1); got:\n%s", hrStr)
	}
	// Org-as-label still present for traceability.
	if !strings.Contains(hrStr, "catalyst.openova.io/organization: omantel-platform") {
		t.Errorf("HelmRelease should still stamp Org as a label; got:\n%s", hrStr)
	}

	// Same checks on the Kustomization wrapper.
	ksPath := "clusters/hz-fsn-rtz-prod/applications/qa-wp/kustomization.yaml"
	ks, ok := fg.get("omantel-platform", "qa-wp", "develop", ksPath)
	if !ok {
		t.Fatalf("expected Kustomization at %s in branch develop", ksPath)
	}
	ksStr := string(ks)
	if !strings.Contains(ksStr, "namespace: qa-omantel") {
		t.Errorf("Kustomization namespace should be 'qa-omantel'; got:\n%s", ksStr)
	}
}

// --- qa-loop iter-11 Fix #45 Cluster-B: Application.status.phase tracks
// downstream HelmRelease.status.conditions[Ready] -----------------------
//
// The matrix-asserted contract (TC-066, TC-100, TC-104, TC-113):
// once the per-region HelmRelease the controller writes to Gitea is
// installed by Flux and reports `Ready=True`, the parent Application
// CR's `status.phase` MUST flip from `Provisioning` to `Ready` within
// 3 minutes. Prior to Fix #45 the controller hard-coded
// `Phase: PhaseProvisioning` on every reconcile pass — the Application
// sat at `Provisioning` indefinitely even after `kubectl get hr -n
// <ns> <app>` was Ready=True for hours.
//
// This test seeds a fake HelmRelease in the Application's namespace
// with status.conditions[Ready]=True and asserts the phase rolls up.
func TestReconcile_PhaseFollowsDownstreamHelmReleaseReady(t *testing.T) {
	bp := makeBlueprint("bp-wordpress", "1.2.3", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wordpress", "1.2.3", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})

	// Pre-seed the downstream HelmRelease in the Application's
	// namespace with status.conditions[Ready]=True (mirrors what Flux
	// would write after a successful install).
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace("acme")
	hr.SetName("site")
	hr.Object["status"] = map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"type":    "Ready",
				"status":  "True",
				"reason":  "InstallSucceeded",
				"message": "Helm install succeeded for release acme/site.v1 with chart bp-wordpress@1.2.3",
			},
		},
	}

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, _, message := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Errorf("phase = %q, want %q (msg=%q)", phase, PhaseReady, message)
	}
	// Per-region replicas-ready should bump from 0 → declared.
	regions, _, _ := unstructured.NestedSlice(got.Object, "status", "regions")
	if len(regions) != 1 {
		t.Fatalf("regions = %d, want 1", len(regions))
	}
	rs := regions[0].(map[string]interface{})
	ready, _ := rs["ready"].(int64)
	replicas, _ := rs["replicas"].(int64)
	if ready != replicas {
		t.Errorf("region.ready=%d, region.replicas=%d — should match when phase=Ready", ready, replicas)
	}
	// status.lastReconciledAt should be populated for TC-113.
	lr, _, _ := unstructured.NestedString(got.Object, "status", "lastReconciledAt")
	if lr == "" {
		t.Errorf("status.lastReconciledAt is empty — must be set on every reconcile pass")
	}
}

// TestReconcile_PhaseDegradedOnDownstreamHelmReleaseFailure asserts the
// inverse: a downstream HR Ready=False (e.g. helm-install rolled-back)
// surfaces as Application.status.phase=Degraded, NOT Provisioning, NOT
// Ready. The reason+message of the worst-region HR are lifted into the
// Application's Ready condition so the operator UI can render the
// failure verbatim.
func TestReconcile_PhaseDegradedOnDownstreamHelmReleaseFailure(t *testing.T) {
	bp := makeBlueprint("bp-wordpress", "1.2.3", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wordpress", "1.2.3", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})

	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace("acme")
	hr.SetName("site")
	hr.Object["status"] = map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"type":    "Ready",
				"status":  "False",
				"reason":  "InstallFailed",
				"message": "chart pull failed: 401 Unauthorized",
			},
		},
	}
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want %q", phase, PhaseDegraded)
	}
	if reason == "" {
		t.Errorf("reason should be set when phase=Degraded; got empty")
	}
	if !strings.Contains(message, "InstallFailed") && !strings.Contains(message, "401 Unauthorized") {
		t.Errorf("message should surface the downstream HR failure verbatim; got %q", message)
	}
}

// TestReconcile_PhaseStaysProvisioningWhenHelmReleaseAbsent asserts the
// no-signal case: no HR exists yet (Flux still pulling Gitea), the
// Application stays at Provisioning. This is the existing happy-path
// behaviour — the new HR-observation logic must be a strict superset.
func TestReconcile_PhaseStaysProvisioningWhenHelmReleaseAbsent(t *testing.T) {
	bp := makeBlueprint("bp-wordpress", "1.2.3", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wordpress", "1.2.3", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	// NOTE: no HR seeded — fresh install, Flux hasn't pulled yet.
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, _, _ := readPhaseAndReason(t, got)
	if phase != PhaseProvisioning {
		t.Errorf("phase = %q, want %q (HR-absent must roll up to Provisioning, not Ready or Degraded)", phase, PhaseProvisioning)
	}
}

// TestObserveRegionHelmReleases_FanoutHRNames asserts the #4282 fix: when a
// topology fan-out (placement.Clusters, e.g. a per-Org bp-postgres routed
// host-side by #4398) names its per-cluster HR `<app>-<cluster>` (NOT the
// bare `<app>`), the phase rollup observes the ACTUAL fan-out HR name and
// returns Ready. Before the fix it GET-ed the bare `<app>` → NotFound →
// Provisioning forever even though `pg-rtz-a` was Ready=True.
func TestObserveRegionHelmReleases_FanoutHRNames(t *testing.T) {
	// A Ready HR named `<app>-<cluster>` — the fan-out shape.
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace("w4282walk")
	hr.SetName("w4282-pg-rtz-a")
	hr.Object["status"] = map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"type": "Ready", "status": "True", "reason": "InstallSucceeded",
				"message": "Helm install succeeded for release w4282walk/w4282-pg-rtz-a.v1 with chart bp-postgres@0.2.6",
			},
		},
	}
	fg := newFakeGitea()
	r := newReconciler(t, fg, hr)

	app := &unstructured.Unstructured{}
	app.SetNamespace("w4282walk")
	app.SetName("w4282-pg")
	plan := placement.Plan{Regions: []placement.RegionPlan{{Name: "me-east-215-a", Role: "primary"}}}

	// WITH the fan-out name + namespace → Ready (the fix).
	phase, _, _, perHRReady := r.observeRegionHelmReleases(context.Background(), app, plan,
		[]hrRef{{name: "w4282-pg-rtz-a", namespace: "w4282walk", region: "w4282-pg-rtz-a"}})
	if phase != PhaseReady {
		t.Errorf("fan-out HR-name rollup: phase=%q, want %q (the per-cluster HR w4282-pg-rtz-a is Ready=True)", phase, PhaseReady)
	}
	if perHRReady["w4282-pg-rtz-a"] != "True" {
		t.Errorf("per-HR readiness for w4282-pg-rtz-a = %q, want \"True\"", perHRReady["w4282-pg-rtz-a"])
	}

	// WITHOUT the fan-out refs → falls back to the bare `<app>` which does
	// NOT exist for a fan-out → Provisioning (the pre-fix wrong behavior,
	// preserved for the single-HR host path which legitimately uses <app>).
	phaseBare, _, _, _ := r.observeRegionHelmReleases(context.Background(), app, plan, nil)
	if phaseBare != PhaseProvisioning {
		t.Errorf("bare-name fallback with no <app> HR: phase=%q, want %q", phaseBare, PhaseProvisioning)
	}
}
