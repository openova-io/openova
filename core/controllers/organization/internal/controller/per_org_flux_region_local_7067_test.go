package controller

// ADR-0015 D1 follow-up (Refs #7067):
//
//  1. A placement-less install (the funnel-shared `vcluster/apps` and
//     `vcluster/host-apps` trees) is reconciled by exactly ONE region — never
//     two (split brain: two databases, a global route splitting traffic).
//  2. Every region's apps Kustomization still finds the Organization's
//     kubeconfig under the name it reads, produced in THAT region.

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

func renderAllRegions(t *testing.T, r *Reconciler, slug string) map[string][]*unstructured.Unstructured {
	t.Helper()
	out := map[string][]*unstructured.Unstructured{}
	for _, region := range r.Regions.IDs() {
		objs, err := r.renderPerOrgRegionFlux(newTestOrg(slug), region)
		if err != nil {
			t.Fatal(err)
		}
		out[region] = objs
	}
	return out
}

func TestPerOrgFlux_PlacementLessTreesReconciledInExactlyOneRegion(t *testing.T) {
	for _, reg := range []regions.Registry{
		regions.New(testRegionA),
		testRegions,
		regions.New(testRegionA, testRegionB, "hw-me-east-215-c-rtz-prod"),
	} {
		r := &Reconciler{GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000", FluxGiteaSecretRef: "openova-org-tenants-git-auth", Regions: reg}
		shared := map[string][]string{} // shared-tree path → regions reconciling it
		for region, objs := range renderAllRegions(t, r, "acme") {
			for _, o := range objs {
				if o.GetKind() != "Kustomization" {
					continue
				}
				path, _, _ := unstructured.NestedString(o.Object, "spec", "path")
				p := strings.TrimPrefix(path, "./")
				if p == gitops.SharedAppsDir || p == gitops.SharedHostAppsDir {
					shared[p] = append(shared[p], region)
				}
			}
		}
		for _, p := range []string{gitops.SharedAppsDir, gitops.SharedHostAppsDir} {
			if len(shared[p]) != 1 {
				t.Errorf("%d regions: placement-less tree %s reconciled by %v, want exactly one region", reg.Len(), p, shared[p])
				continue
			}
			if shared[p][0] != testRegionA {
				t.Errorf("%d regions: %s bound to %s, want the home region %s (registry index 0)", reg.Len(), p, shared[p][0], testRegionA)
			}
		}
	}
}

func TestPerOrgFlux_NonHomeRegionsReconcileControllerOwnedBaseline(t *testing.T) {
	r := &Reconciler{GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000", FluxGiteaSecretRef: "openova-org-tenants-git-auth", Regions: testRegions}
	objs := renderAllRegions(t, r, "acme")[testRegionB]
	paths := map[string]string{}
	for _, o := range objs {
		p, _, _ := unstructured.NestedString(o.Object, "spec", "path")
		paths[o.GetName()] = p
	}
	if got := paths[perOrgAppsKustomizationName("acme")]; got != "./"+gitops.RegionBaselineAppsDir {
		t.Errorf("region B apps path = %q, want the baseline %q", got, gitops.RegionBaselineAppsDir)
	}
	if got := paths[perOrgHostAppsKustomizationName("acme")]; got != "./"+gitops.RegionBaselineHostAppsDir {
		t.Errorf("region B host-apps path = %q, want the baseline %q", got, gitops.RegionBaselineHostAppsDir)
	}
	if got := paths[perOrgRegionAppsKustomizationName("acme")]; got != "./regions/"+testRegionB+"/apps" {
		t.Errorf("region B region-apps path = %q — the placement-bound folder is how B gets Applications", got)
	}

	// The baseline trees Render writes hold ONLY controller baseline docs and
	// exist for every file the paths above reference.
	files, err := gitops.Render(gitops.Inputs{Slug: "acme", Tier: "org", PlanSlug: "m",
		SovereignFQDN: "t99.omani.works", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{gitops.RegionBaselineAppsDir, gitops.RegionBaselineHostAppsDir} {
		idx, ok := files[dir+"/kustomization.yaml"]
		if !ok {
			t.Fatalf("Render wrote no %s/kustomization.yaml", dir)
		}
		var k struct {
			Resources []string `json:"resources"`
		}
		if err := yaml.Unmarshal(idx, &k); err != nil {
			t.Fatal(err)
		}
		if len(k.Resources) == 0 {
			t.Errorf("%s index lists nothing", dir)
		}
		for _, res := range k.Resources {
			if strings.HasPrefix(res, "app-") || strings.HasPrefix(res, "db-") {
				t.Errorf("%s lists a placement-less install %q", dir, res)
			}
			if _, ok := files[dir+"/"+res]; !ok {
				t.Errorf("%s lists %q but Render wrote no such file", dir, res)
			}
		}
	}
}

func TestPerOrgFlux_EveryRegionReadsTheRegionLocalKubeconfigMirror(t *testing.T) {
	r := &Reconciler{GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000", FluxGiteaSecretRef: "openova-org-tenants-git-auth", Regions: testRegions}
	files, err := gitops.Render(gitops.Inputs{Slug: "acme", Tier: "org", PlanSlug: "m",
		SovereignFQDN: "t99.omani.works", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatal(err)
	}
	mirror := string(files["vcluster/kubeconfig-mirror.yaml"])
	if !strings.Contains(string(files["vcluster/kustomization.yaml"]), "kubeconfig-mirror.yaml") {
		t.Fatal("the boundary tree (applied in every region) does not index kubeconfig-mirror.yaml")
	}
	for region, objs := range renderAllRegions(t, r, "acme") {
		for _, o := range objs {
			if o.GetName() != perOrgAppsKustomizationName("acme") {
				continue
			}
			name, _, _ := unstructured.NestedString(o.Object, "spec", "kubeConfig", "secretRef", "name")
			want := "name: " + name + "\n  namespace: flux-system"
			if !strings.Contains(mirror, want) {
				t.Errorf("region %s apps Kustomization reads flux-system/%s, which the region-local producer does not write", region, name)
			}
		}
	}
	if strings.Contains(mirror, "\ndata:") || strings.Contains(mirror, "stringData") {
		t.Error("kubeconfig stubs must carry no data — Flux would then own (and blank) the kubeconfig bytes")
	}
}
