package controller

// ADR-0015 D1 — the per-Org Flux objects are COMMITTED once per region, never
// created by API call; each region's copy reconciles its own Application
// folder from its own Gitea.

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPerOrgFlux_ADR0015_OneFilePerRegion_NoAPIWrites(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	fg := newFakeRegionGit()
	r := &Reconciler{
		Log:                logr.Discard(),
		GiteaInClusterURL:  "http://gitea-http.gitea.svc.cluster.local:3000",
		FluxGiteaSecretRef: "openova-org-tenants-git-auth",
		Regions:            testRegions,
		RegionGit:          fg,
	}
	r.Client = cl
	org := newTestOrg("acme")
	if err := r.reconcilePerOrgFlux(context.Background(), org); err != nil {
		t.Fatal(err)
	}

	// No API object was created by the controller.
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(fluxKustomizationGVK.GroupVersion().WithKind("KustomizationList"))
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("controller created %d Kustomizations by API call; ADR-0015 D1 forbids it", len(list.Items))
	}

	for _, region := range []string{testRegionA, testRegionB} {
		data, ok := fg.get("openova/sovereign-regions/" + perOrgRegionFilePath("t99.omani.works", region, "acme"))
		if !ok {
			t.Fatalf("no file for region %s", region)
		}
		if !strings.Contains(string(data), "${SOVEREIGN_REGION_GIT_SECRET:=openova-org-tenants-git-auth}") {
			t.Errorf("region %s: the source Secret must be region-substitutable", region)
		}
		objs := decodeDocs(t, data)
		if len(objs) != 5 {
			t.Fatalf("region %s: %d objects, want GitRepository + 4 Kustomizations", region, len(objs))
		}
		var regionApps string
		for _, o := range objs {
			if o.GetLabels()["openova.io/region"] != region {
				t.Errorf("%s in region %s file labelled region %q", o.GetName(), region, o.GetLabels()["openova.io/region"])
			}
			if o.GetName() == perOrgRegionAppsKustomizationName("acme") {
				regionApps, _, _ = unstructured.NestedString(o.Object, "spec", "path")
			}
		}
		if regionApps != "./regions/"+region+"/apps" {
			t.Errorf("region %s region-apps path = %q", region, regionApps)
		}
		if _, ok := fg.get("acme/catalyst-tenant/" + perOrgRegionAppsReadmePath(region)); !ok {
			t.Errorf("region %s apps folder not seeded in the Org repo", region)
		}
	}

	// Steady state commits nothing.
	before := fg.commits
	if err := r.reconcilePerOrgFlux(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if fg.commits != before {
		t.Errorf("second pass committed %d files", fg.commits-before)
	}

	// Teardown removes every region's file.
	if err := r.teardownPerOrgFlux(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	for _, region := range []string{testRegionA, testRegionB} {
		if _, ok := fg.get("openova/sovereign-regions/" + perOrgRegionFilePath("t99.omani.works", region, "acme")); ok {
			t.Errorf("region %s file survived teardown", region)
		}
	}
}

func TestPerOrgFlux_ADR0015_EmptyRegistryRefuses(t *testing.T) {
	r := &Reconciler{
		Log:                logr.Discard(),
		GiteaInClusterURL:  "http://gitea-http.gitea.svc.cluster.local:3000",
		FluxGiteaSecretRef: "x",
		RegionGit:          newFakeRegionGit(),
	}
	if err := r.reconcilePerOrgFlux(context.Background(), newTestOrg("acme")); err == nil {
		t.Fatal("an empty region registry must fail, never guess a region")
	}
}
