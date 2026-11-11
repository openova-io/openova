package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

// The test Sovereign: two regions. Canonical cluster IDs *-A resolve to the
// first, *-B to the second (pkg/regions provisioning-index rule).
const (
	testRegionA = "hetzner-fsn-rtz-prod"
	testRegionB = "hetzner-nbg-rtz-prod"
)

var testRegions = regions.New(testRegionA, testRegionB)

// regionFileObjs returns the objects committed for app in region's folder of
// the Org repo, or nil when no file exists.
func regionFileObjs(t *testing.T, fg *fakeGitea, org, region, app string) []*unstructured.Unstructured {
	t.Helper()
	data, ok := fg.get(org, "catalyst-tenant", "main", topo.RegionAppPath(region, app))
	if !ok {
		return nil
	}
	objs, err := topo.ParseRegionFile(data)
	if err != nil {
		t.Fatalf("parse region file %s/%s: %v", region, app, err)
	}
	return objs
}

// gitContinuum returns the Continuum document committed into the app's region
// files (ADR-0015 D8: a governed Application's Continuum is delivered from
// Git in every region, never created by API). Every region file must carry
// the identical document; the first is returned.
func gitContinuum(t *testing.T, r *Reconciler, ns, app string) (*unstructured.Unstructured, bool) {
	t.Helper()
	fg, ok := r.Gitea.(*fakeGitea)
	if !ok {
		t.Fatalf("gitContinuum needs the fake Gitea")
	}
	var found *unstructured.Unstructured
	for _, region := range r.Cfg.Regions.IDs() {
		for _, org := range []string{ns, "acme"} {
			for _, o := range regionFileObjs(t, fg, org, region, app) {
				if o.GetKind() != "Continuum" || o.GetName() != ContinuumNameFor(app) {
					continue
				}
				if found == nil {
					found = o
				} else {
					a, _, _ := unstructured.NestedMap(found.Object, "spec")
					b, _, _ := unstructured.NestedMap(o.Object, "spec")
					if fmt.Sprint(a) != fmt.Sprint(b) {
						t.Fatalf("region files carry different Continuum specs:\n%v\n%v", a, b)
					}
				}
			}
		}
	}
	return found, found != nil
}

// reconcileAndApplyAll runs one reconcile pass and then stands in for EVERY
// region's Flux applying its folder of every Org repo into the one fake API
// (the union of all regions). Tests that assert on rendered HelmRelease
// shape use it; tests about WHICH region applies what read the files.
func reconcileAndApplyAll(t *testing.T, r *Reconciler, fg *fakeGitea, ns, name string) {
	t.Helper()
	reconcileFromCluster(t, r, ns, name)
	fg.mu.Lock()
	var orgs []string
	for key := range fg.repos {
		if strings.HasSuffix(key, "/catalyst-tenant") {
			orgs = append(orgs, strings.TrimSuffix(key, "/catalyst-tenant"))
		}
	}
	fg.mu.Unlock()
	for _, org := range orgs {
		for _, region := range r.Cfg.Regions.IDs() {
			fluxApplyRegion(t, r, fg, org, region)
		}
	}
}

// fluxApplyRegion stands in for ONE region's Flux: it applies every object
// committed under `regions/<region>/apps/` of the Org repo to r.Dynamic,
// stamping the kustomize-controller label the real apply stamps. Existing
// objects keep their status (only spec + labels are replaced), exactly like a
// server-side apply by kustomize-controller.
func fluxApplyRegion(t *testing.T, r *Reconciler, fg *fakeGitea, org, region string) []*unstructured.Unstructured {
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
	var applied []*unstructured.Unstructured
	for _, d := range docs {
		objs, err := topo.ParseRegionFile(d)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, o := range objs {
			labels := o.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			labels[fluxKustomizeNameLabel] = "catalyst-tenant-" + org + "-region-apps"
			o.SetLabels(labels)
			res := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(o.GetNamespace())
			cur, err := res.Get(ctx, o.GetName(), metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(err):
				if _, err := res.Create(ctx, o, metav1.CreateOptions{}); err != nil {
					t.Fatalf("flux apply create %s/%s: %v", o.GetNamespace(), o.GetName(), err)
				}
			case err != nil:
				t.Fatalf("flux apply get: %v", err)
			default:
				cur.Object["spec"] = o.Object["spec"]
				cur.SetLabels(o.GetLabels())
				if _, err := res.Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
					t.Fatalf("flux apply update: %v", err)
				}
			}
			applied = append(applied, o)
		}
	}
	return applied
}

// regionFilesFor counts the region files any region folder of the Org repo
// still holds for the Application.
func regionFilesFor(fg *fakeGitea, org, app string) int {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	n := 0
	for path := range fg.repos[org+"/catalyst-tenant"]["main"] {
		if strings.HasPrefix(path, "regions/") && strings.HasSuffix(path, "/apps/"+app+".yaml") {
			n++
		}
	}
	return n
}

// fluxPruneAll stands in for every region's Flux prune: a HelmRelease that
// carries the kustomize-controller label but is declared by no region file
// any more is deleted, exactly what prune=true does after the commit lands.
func fluxPruneAll(t *testing.T, r *Reconciler, fg *fakeGitea) {
	t.Helper()
	ctx := context.Background()
	declared := map[string]bool{}
	fg.mu.Lock()
	var docs [][]byte
	for key, branches := range fg.repos {
		if !strings.HasSuffix(key, "/catalyst-tenant") {
			continue
		}
		for path, data := range branches["main"] {
			if strings.HasPrefix(path, "regions/") {
				docs = append(docs, data)
			}
		}
	}
	fg.mu.Unlock()
	for _, d := range docs {
		objs, err := topo.ParseRegionFile(d)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, o := range objs {
			declared[o.GetNamespace()+"/"+o.GetName()] = true
		}
	}
	list, err := r.Dynamic.Resource(FluxHelmReleaseGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs: %v", err)
	}
	for _, hr := range list.Items {
		if hr.GetLabels()[fluxKustomizeNameLabel] == "" || declared[hr.GetNamespace()+"/"+hr.GetName()] {
			continue
		}
		if err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(hr.GetNamespace()).
			Delete(ctx, hr.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("flux prune %s/%s: %v", hr.GetNamespace(), hr.GetName(), err)
		}
	}
}
