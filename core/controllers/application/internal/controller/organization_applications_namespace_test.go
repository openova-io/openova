package controller

// A new Organization's applications land only in namespaces the product
// creates, so the Organization's region-apps Kustomization can apply them.
//
// Measured on the fresh two-region install of 19 November 2026 (gate run 24,
// check NET-G07): the signup funnel gives every new Organization the
// Applications agenity, newapi, openclaw, stalwart-tenant and
// wordpress-tenant (products/catalyst/bootstrap/api/internal/handler/
// organization_funnel_apps.go, funnelApplicationFiles). Blueprint bp-newapi
// declared `placement.tier: mgmt`, so this controller addressed its
// HelmRelease to the host namespace `mgmt` (VClusterPlacements). No
// Sovereign has that namespace: the mgmt plane vCluster was removed (#4325)
// and its chart deleted (NET-221). kustomize-controller applies a region
// folder as one unit, so the one object it could not place failed the whole
// `catalyst-tenant-<org>-region-apps` Kustomization and NONE of the
// Organization's applications arrived, the customer's own included.
//
// These tests read the Blueprint FILES the catalogue seed is generated from
// (SVC-160). Since NET-223 the controller ignores a placement tier, so they
// keep holding when a funnel Blueprint names one again.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
)

// repoRoot is the monorepo root seen from this package's directory.
const repoRoot = "../../../../.."

// funnelBlueprintFiles are the Blueprints the signup funnel installs into
// every new Organization (the five entries of funnelApplicationFiles), keyed
// by the Application name the funnel gives each one.
var funnelBlueprintFiles = map[string]string{
	"agenity":          "products/agenity/blueprint.yaml",
	"newapi":           "platform/newapi/blueprint.yaml",
	"openclaw":         "platform/openclaw/blueprint.yaml",
	"stalwart-tenant":  "platform/stalwart-tenant/blueprint.yaml",
	"wordpress-tenant": "platform/wordpress-tenant/blueprint.yaml",
}

var namespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// loadBlueprintFile parses a Blueprint file of the repository into the object
// the catalogue seed installs, with the published digest the Blueprint
// controller would have recorded.
func loadBlueprintFile(t *testing.T, rel string) *unstructured.Unstructured {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(repoRoot), rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	obj := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGeneration(1)
	u.Object["status"] = map[string]interface{}{"ociDigest": "sha256:" + strings.Repeat("0", 64)}
	return u
}

// funnelParameters are the values a funnel Application needs to pass its
// Blueprint's configSchema (the overlay's values carry them in production).
var funnelParameters = map[string]map[string]interface{}{
	"bp-wordpress-tenant": {"adminUser": map[string]interface{}{"email": "owner@acme.omani.homes"}},
}

// funnelApplication is the Application the funnel creates through
// createApplicationInstance: in the Organization's namespace, no instance
// placement tier, the Environment's regions and the topology chooseTopology
// picks (the Blueprint's default for the Sovereign's region count).
func funnelApplication(org, name string, bp *unstructured.Unstructured, regionIDs []string) *unstructured.Unstructured {
	version, _, _ := unstructured.NestedString(bp.Object, "spec", "version")
	key := "single-region"
	if len(regionIDs) > 1 {
		key = "multi-region"
	}
	mode, _, _ := unstructured.NestedString(bp.Object, "spec", "topology", "defaults", key)
	if mode == "" {
		mode = "singleton"
	}
	if mode == "singleton" {
		// validateInstallRefs resolves a singleton to one region.
		regionIDs = regionIDs[:1]
	}
	params := map[string]interface{}{}
	for k, v := range funnelParameters[bp.GetName()] {
		params[k] = v
	}
	u := makeApp(org, name, org+"-prod", bp.GetName(), version, "", regionIDs, params)
	regionsAny := make([]interface{}, len(regionIDs))
	for i, r := range regionIDs {
		regionsAny[i] = r
	}
	u.Object["spec"].(map[string]interface{})["placement"] = map[string]interface{}{
		"mode":    mode,
		"regions": regionsAny,
	}
	return u
}

func namespaceObject(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("Namespace")
	u.SetName(name)
	return u
}

// namespacesNamed lists every namespace an object of the region folder needs
// to exist: its own and, for a HelmRelease, the one Helm installs into and
// the one its kubeConfig Secret is read from.
func namespacesNamed(o *unstructured.Unstructured) []string {
	ns := []string{o.GetNamespace()}
	if o.GetKind() == "HelmRelease" {
		if tn, _, _ := unstructured.NestedString(o.Object, "spec", "targetNamespace"); tn != "" {
			if _, pivot, _ := unstructured.NestedMap(o.Object, "spec", "kubeConfig"); !pivot {
				ns = append(ns, tn)
			}
		}
	}
	return ns
}

// orgScenario is one shape of Organization the funnel serves.
type orgScenario struct {
	name    string
	env     func(name, org, envType string) *unstructured.Unstructured
	regions []string
	// apps — the funnel Applications installed; nil = all of them.
	apps []string
}

var orgScenarios = []orgScenario{
	// The measured shape: the gate's Organization on the two-region install
	// had a one-region Environment (topology resolved sovereignRegions=1).
	{name: "single-region", env: makeEnv, regions: []string{testRegionA}},
	// bp-newapi's active-passive variant, which a two-region Environment
	// resolves to.
	{name: "two-region newapi", env: makeMultiRegionEnv, regions: []string{testRegionA, testRegionB}, apps: []string{"newapi"}},
}

// reconcileFunnel reconciles the funnel's Applications plus extra into a
// fake cluster that holds only the namespaces the product creates for the
// Organization, and returns the reconciler and the fake Gitea.
func reconcileFunnel(t *testing.T, sc orgScenario, org string, extra ...*unstructured.Unstructured) (*Reconciler, *fakeGitea, []string) {
	t.Helper()
	objs := []*unstructured.Unstructured{
		sc.env(org+"-prod", org, "prod"),
		makeOrg(org),
		// The Organization's namespace is created by the organization
		// controller; flux-system by the bootstrap kit. Nothing creates
		// `mgmt`, `dmz` or `rtz`.
		namespaceObject(org),
		namespaceObject("flux-system"),
	}
	var apps []string
	for name, rel := range funnelBlueprintFiles {
		if sc.apps != nil && !containsString(sc.apps, name) {
			continue
		}
		bp := loadBlueprintFile(t, rel)
		objs = append(objs, bp, funnelApplication(org, name, bp, sc.regions))
		apps = append(apps, name)
	}
	for _, e := range extra {
		objs = append(objs, e)
		apps = append(apps, e.GetName())
	}
	sort.Strings(apps)
	fg := newFakeGitea()
	fg.orgsExist[org] = true
	r := newReconciler(t, fg, objs...)
	for _, name := range apps {
		reconcileFromCluster(t, r, org, name)
	}
	return r, fg, apps
}

// regionAppsApply stands in for ONE region's `catalyst-tenant-<org>-region-apps`
// Kustomization. kustomize-controller validates the whole folder before it
// applies any of it, so one object in a namespace that does not exist fails
// the apply of every object (`namespaces "mgmt" not found`). On success it
// applies the folder exactly like fluxApplyRegion.
func regionAppsApply(t *testing.T, r *Reconciler, fg *fakeGitea, org, region string) error {
	t.Helper()
	ctx := context.Background()
	prefix := topo.RegionAppsDir(region) + "/"
	fg.mu.Lock()
	var docs [][]byte
	for path, data := range fg.repos[org+"/catalyst-tenant"]["main"] {
		if strings.HasPrefix(path, prefix) {
			docs = append(docs, data)
		}
	}
	fg.mu.Unlock()
	for _, d := range docs {
		objs, err := topo.ParseRegionFile(d)
		if err != nil {
			t.Fatalf("parse region file: %v", err)
		}
		for _, o := range objs {
			for _, ns := range namespacesNamed(o) {
				if _, err := r.Dynamic.Resource(namespaceGVR).Get(ctx, ns, metav1.GetOptions{}); apierrors.IsNotFound(err) {
					return fmt.Errorf("%s %s/%s: namespaces %q not found", o.GetKind(), o.GetNamespace(), o.GetName(), ns)
				} else if err != nil {
					t.Fatalf("get namespace %s: %v", ns, err)
				}
			}
		}
	}
	fluxApplyRegion(t, r, fg, org, region)
	return nil
}

// TestReconcile_OrganizationApplications_NameOnlyNamespacesTheProductCreates —
// every object the controller commits for a new Organization's funnel
// Applications, in every region, names only the Organization's own
// namespace. Failed on main for bp-newapi: its HelmRelease named `mgmt`.
func TestReconcile_OrganizationApplications_NameOnlyNamespacesTheProductCreates(t *testing.T) {
	for _, sc := range orgScenarios {
		t.Run(sc.name, func(t *testing.T) {
			const org = "acme"
			r, fg, apps := reconcileFunnel(t, sc, org)
			for _, name := range apps {
				got := readApp(t, r, org, name)
				if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
					t.Fatalf("Application %s failed: reason=%q msg=%q", name, reason, msg)
				}
				files := 0
				for _, region := range r.Cfg.Regions.IDs() {
					objs := regionFileObjs(t, fg, org, region, name)
					if objs != nil {
						files++
					}
					for _, o := range objs {
						for _, ns := range namespacesNamed(o) {
							if ns != org {
								t.Errorf("Application %s (%s) region %s: %s %s names namespace %q; a new Organization has only its namespace %q",
									name, got.Object["spec"].(map[string]interface{})["blueprintRef"].(map[string]interface{})["name"],
									region, o.GetKind(), o.GetName(), ns, org)
							}
						}
						if o.GetKind() == "HelmRelease" {
							if s, ok, _ := unstructured.NestedString(o.Object, "spec", "kubeConfig", "secretRef", "name"); ok {
								t.Errorf("Application %s region %s: HelmRelease %s pivots into %q; no plane vCluster exists on a Sovereign",
									name, region, o.GetName(), s)
							}
						}
					}
				}
				if files == 0 {
					t.Errorf("Application %s: no region file committed", name)
				}
			}
		})
	}
}

// TestReconcile_OrganizationReachesItsApplications_WithNewAPIAmongThem — the
// Organization's region-apps Kustomization applies in every region, so the
// customer's own Application arrives beside the funnel's bp-newapi. Failed on
// main: the bp-newapi HelmRelease in `mgmt` failed the whole folder.
func TestReconcile_OrganizationReachesItsApplications_WithNewAPIAmongThem(t *testing.T) {
	for _, sc := range orgScenarios {
		if sc.apps != nil {
			continue // the whole funnel only
		}
		t.Run(sc.name, func(t *testing.T) {
			const org = "acme"
			wp := loadBlueprintFile(t, funnelBlueprintFiles["wordpress-tenant"])
			shop := funnelApplication(org, "shop", wp, sc.regions)
			r, fg, apps := reconcileFunnel(t, sc, org, shop)
			for _, name := range apps {
				if phase, reason, msg := readPhaseAndReason(t, readApp(t, r, org, name)); phase == PhaseFailed {
					t.Fatalf("Application %s failed: reason=%q msg=%q", name, reason, msg)
				}
			}

			for _, region := range sc.regions {
				if err := regionAppsApply(t, r, fg, org, region); err != nil {
					t.Fatalf("region %s: the Organization's region-apps Kustomization cannot apply: %v", region, err)
				}
			}
			list, err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(org).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("list HelmReleases: %v", err)
			}
			byApp := map[string]bool{}
			for _, hr := range list.Items {
				byApp[hr.GetLabels()["catalyst.openova.io/application"]] = true
				byApp[hr.GetName()] = true
			}
			for _, want := range []string{"newapi", "shop"} {
				found := false
				for k := range byApp {
					if k == want || strings.HasPrefix(k, want+"-") {
						found = true
					}
				}
				if !found {
					t.Errorf("Application %s has no HelmRelease in the Organization's namespace after the apply; have %v", want, byApp)
				}
			}
		})
	}
}
