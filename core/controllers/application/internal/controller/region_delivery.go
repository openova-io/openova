package controller

// region_delivery.go — ADR-0015 D1/D3: the application-controller is the
// single writer of placement, and it writes ONLY Git.
//
// An Application's placement becomes one file per region in the Org repo:
//
//	<org>/catalyst-tenant @ main
//	└── regions/<region-id>/apps/<app>.yaml   (that region's HelmRelease(s))
//
// Every region's Flux reconciles its own `regions/<region-id>/apps` folder
// from its own Gitea (the per-Org `catalyst-tenant-<slug>-region-apps`
// Kustomization the organization-controller commits under
// `clusters/<fqdn>/regions/<region-id>/` of the Sovereign region repo). The
// controller applies no HelmRelease to any cluster; moving an Application
// between regions is a Git change and each region's Flux prunes what left it.
//
// The pre-ADR-0015 delivery wrote to clusters imperatively: a per-Application
// GitRepository + per-region Kustomization pair in flux-system, and fanned-out
// HelmReleases upserted straight onto the host — including the legs of OTHER
// regions, which then installed in this region. retireImperativeDelivery
// removes those leftovers for one Application, safely, on its next reconcile.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
)

// fluxKustomizeNameLabel is the label kustomize-controller stamps on every
// object it applies. An Application-owned HelmRelease WITHOUT it was upserted
// by the pre-ADR-0015 controller rather than delivered from Git.
const fluxKustomizeNameLabel = "kustomize.toolkit.fluxcd.io/name"

// commitRegionFiles writes files[region] to RegionAppPath(region, app) for
// every region of the Sovereign that has one, and deletes the file of every
// region that has none. Regions outside the registry are never written.
func (r *Reconciler) commitRegionFiles(
	ctx context.Context,
	app *unstructured.Unstructured,
	org, repo, branch string,
	files map[string][]byte,
) error {
	for region := range files {
		if !r.Cfg.Regions.Contains(region) {
			return fmt.Errorf("region %q is not in the Sovereign region registry %v", region, r.Cfg.Regions.IDs())
		}
	}
	msg := fmt.Sprintf("application-controller: placement of %s/%s (gen %d)",
		app.GetNamespace(), app.GetName(), app.GetGeneration())
	for _, region := range r.Cfg.Regions.IDs() {
		path := topo.RegionAppPath(region, app.GetName())
		if data, ok := files[region]; ok {
			if _, _, err := r.Gitea.PutFile(ctx, org, repo, branch, path, data, msg); err != nil {
				return fmt.Errorf("PutFile %s: %w", path, err)
			}
			continue
		}
		if err := r.deleteRepoFile(ctx, org, repo, branch, path, msg); err != nil {
			return err
		}
	}
	return nil
}

// deleteRegionFiles removes the Application's file from every region folder
// (Application deletion). Absent files are success.
func (r *Reconciler) deleteRegionFiles(ctx context.Context, app *unstructured.Unstructured, org, repo, branch string) error {
	msg := fmt.Sprintf("application-controller: remove %s/%s from every region", app.GetNamespace(), app.GetName())
	for _, region := range r.Cfg.Regions.IDs() {
		if err := r.deleteRepoFile(ctx, org, repo, branch, topo.RegionAppPath(region, app.GetName()), msg); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) deleteRepoFile(ctx context.Context, org, repo, branch, path, msg string) error {
	if _, err := r.Gitea.DeleteFile(ctx, org, repo, branch, path, msg); err != nil {
		if r.GiteaErrors != nil && r.GiteaErrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("DeleteFile %s: %w", path, err)
	}
	return nil
}

// retireImperativeDelivery removes, for ONE Application, what the
// pre-ADR-0015 controller wrote straight onto this region's cluster:
//
//   - fanned-out HelmReleases that are not delivered from Git (no
//     kustomize-controller label) AND are not part of this region's file.
//     These are the other regions' legs that installed here. A HelmRelease
//     that IS in this region's file is left alone: the region's Flux adopts
//     it in place (same name + namespace, server-side apply), so the release
//     is never uninstalled and reinstalled.
//   - the per-Application GitRepository + per-region Kustomizations in
//     HostFluxNamespace. Each Kustomization is first set prune=false, so
//     deleting it orphans (does not uninstall) the HelmRelease it applied —
//     that HelmRelease is now delivered by the region's Flux from Git.
func (r *Reconciler) retireImperativeDelivery(ctx context.Context, app *unstructured.Unstructured, files map[string][]byte) error {
	uid := string(app.GetUID())
	if uid == "" {
		return nil
	}
	selector := fmt.Sprintf("%s=%s", LabelAppUID, uid)

	keep := map[string]bool{}
	for region, data := range files {
		if !r.observesRegion(region) {
			continue
		}
		objs, err := topo.ParseRegionFile(data)
		if err != nil {
			return err
		}
		for _, o := range objs {
			keep[o.GetNamespace()+"/"+o.GetName()] = true
		}
	}

	hrs, err := r.Dynamic.Resource(FluxHelmReleaseGVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list HelmReleases: %w", err)
	}
	if hrs != nil {
		for i := range hrs.Items {
			hr := &hrs.Items[i]
			labels := hr.GetLabels()
			if labels[LabelAppUID] != uid || labels[fluxKustomizeNameLabel] != "" {
				continue
			}
			if keep[hr.GetNamespace()+"/"+hr.GetName()] {
				continue
			}
			if derr := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(hr.GetNamespace()).
				Delete(ctx, hr.GetName(), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete %s/%s: %w", hr.GetNamespace(), hr.GetName(), derr)
			}
			r.Log.Info("retired a pre-ADR-0015 HelmRelease this region must not apply",
				"namespace", hr.GetNamespace(), "name", hr.GetName(),
				"cluster", labels[topo.LabelCluster], "app", app.GetName())
		}
	}

	ns := r.Cfg.HostFluxNamespace
	kss, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list Kustomizations: %w", err)
	}
	if kss != nil {
		for i := range kss.Items {
			ks := &kss.Items[i]
			if ks.GetLabels()[LabelAppUID] != uid {
				continue
			}
			if prune, _, _ := unstructured.NestedBool(ks.Object, "spec", "prune"); prune {
				if err := unstructured.SetNestedField(ks.Object, false, "spec", "prune"); err != nil {
					return err
				}
				if _, uerr := r.Dynamic.Resource(FluxKustomizationGVR).Namespace(ns).Update(ctx, ks, metav1.UpdateOptions{}); uerr != nil {
					return fmt.Errorf("orphan-mode %s/%s: %w", ns, ks.GetName(), uerr)
				}
			}
			if derr := r.Dynamic.Resource(FluxKustomizationGVR).Namespace(ns).
				Delete(ctx, ks.GetName(), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete %s/%s: %w", ns, ks.GetName(), derr)
			}
		}
	}
	grs, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list GitRepositories: %w", err)
	}
	if grs != nil {
		for i := range grs.Items {
			gr := &grs.Items[i]
			if gr.GetLabels()[LabelAppUID] != uid {
				continue
			}
			if derr := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace(ns).
				Delete(ctx, gr.GetName(), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete %s/%s: %w", ns, gr.GetName(), derr)
			}
		}
	}
	return nil
}
