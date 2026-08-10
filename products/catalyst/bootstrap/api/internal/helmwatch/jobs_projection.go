// jobs_projection.go — #6045. The /jobs projection, widened to include
// per-Organization Application installs.
//
// THE DEFECT. ListAndSnapshotHelmReleases (helmwatch.go:2678) lists
// exactly one namespace — Namespace(FluxNamespace), and FluxNamespace is
// the constant "flux-system" (:86) — then keeps only `bp-` prefixed
// names. The informer path (:1116) and the ticker recensus (:1396/:1408)
// are scoped the same way. A per-Organization Application install is
// neither of those things:
//
//   - NAMESPACE. The application-controller authors the HelmRelease in
//     the Application's own namespace, or the vCluster's host namespace
//     (core/controllers/pkg/render/manifests.go, `namespace:
//     {{ .HRNamespace }}`). Never flux-system.
//   - NAME. It is named after the Application (`name: {{ .AppName }}`),
//     or `<app>-<cluster>` on the topology fan-out path. `bp-` names a
//     Blueprint; an Application is `<purpose>` — see docs/GLOSSARY.md.
//
// So the only installs a User can actually cause to fail were
// structurally invisible in EVERY filter state, the `failed` filter
// included. A User watched a real install fail and /jobs answered "No
// jobs match the current filters."
//
// WHAT THE `bp-` FILTER WAS PROTECTING, and why this does not remove it.
// Two distinct things, both real:
//
//  1. THE PHASE-1 TERMINATION GATE. The informer's FilterFunc (:1116)
//     feeds processEvent, which closes the `terminated` channel once
//     every OBSERVED HelmRelease is terminal. Widening THAT filter would
//     let a per-Org Application install — which can appear at any moment,
//     long after bootstrap — hold Phase-1 convergence open forever. This
//     file does not touch the informer. The bootstrap-kit leg below is
//     ListAndSnapshotHelmReleases verbatim.
//
//  2. INFRASTRUCTURE NOISE IN PER-ORG NAMESPACES. Every Organization gets
//     a `vcluster` HelmRelease in namespace `<slug>`
//     (core/controllers/organization/internal/gitops/manifests.go
//     vclusterTemplate). It is not `bp-` prefixed and it does not live in
//     flux-system, so a naive "list all namespaces, drop the prefix
//     filter" fix would surface it — and that template's OWN comment
//     records that it lands Stalled=True / RetriesExceeded on a cold
//     Sovereign-Harbor pull. One spurious Failed row per Organization on
//     the Sovereign, which is worse than the empty page.
//
// THE DISCRIMINATOR. Application-install HelmReleases are stamped by two
// different renderers with two different label sets:
//
//	single-HR host path (render/manifests.go:352)
//	  app.kubernetes.io/managed-by: application-controller
//	  catalyst.openova.io/application: <app>
//	  catalyst.openova.io/organization: <org>      <-- shared
//	topology fan-out (render/fanout.go:218 over fanoutOwnerLabels)
//	  catalyst.openova.io/app: <app>
//	  catalyst.openova.io/app-uid: <uid>
//	  catalyst.openova.io/organization: <org>      <-- shared
//
// `catalyst.openova.io/organization` is the ONE label both paths emit, so
// it is the discriminator. Keying on either of the others silently misses
// half the Applications on the Sovereign. Crucially the per-Org vcluster
// HR carries `openova.io/organization` — a DIFFERENT key, no
// `catalyst.` prefix — so an existence selector on the catalyst-scoped
// key includes exactly the Application installs and excludes the
// infrastructure. The selector is server-side, so the apiserver does the
// filtering and the response never carries the noise at all.
package helmwatch

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// ApplicationOrgLabel is stamped on every Application-install HelmRelease
// by BOTH application-controller render paths, and by nothing else that
// produces a HelmRelease. Its presence is what makes a HelmRelease a
// per-Organization Application install rather than platform plumbing.
//
// If a future renderer stops emitting it, per-Org installs silently
// vanish from /jobs again — jobs_projection_6045_test.go pins both label
// sets so that regression fails in CI rather than on a Sovereign.
const ApplicationOrgLabel = "catalyst.openova.io/organization"

// ApplicationComponentID builds the AppID for a per-Org Application
// install: `<namespace>:<name>`.
//
// Namespace-qualified because jobs.Store is keyed by AppID and an
// Application may legitimately be named after a platform component — an
// Org's Application called `cilium` would otherwise land on AppID
// "cilium" and overwrite the bootstrap-kit component's row (or be
// overwritten by it), so a User's failing install would silently replace
// a platform component's status.
//
// ":" and not "/": snapshotsToSeedsForRegion already uses ":" as its
// region separator for exactly this reason — the resulting JobName
// renders as /jobs/install-<ns>:<name> without TanStack Router splitting
// it into path segments.
func ApplicationComponentID(namespace, name string) string {
	return namespace + ":" + name
}

// ListAndSnapshotJobsProjection returns the union of
//
//   - the bootstrap-kit projection (flux-system, `bp-` prefixed) exactly
//     as ListAndSnapshotHelmReleases produces it, and
//   - every per-Organization Application install, across all namespaces,
//     selected by ApplicationOrgLabel.
//
// ERROR CONTRACT — read this before calling. A failure of the
// bootstrap-kit leg is fatal: (nil, err), matching
// ListAndSnapshotHelmReleases. A failure of the Application leg returns
// the bootstrap-kit rows it DID obtain ALONGSIDE a non-nil error. So a
// non-nil error here does NOT mean "no rows", and an empty Application
// set is only trustworthy when err is nil. Callers must seed whatever
// comes back and log the error — never treat err != nil as an empty
// projection, and never treat a returned slice as complete without
// checking err. That split exists so an RBAC gap or an apiserver blip on
// the cluster-wide labelled list can never blank the bootstrap-kit rows
// that used to render fine.
func ListAndSnapshotJobsProjection(ctx context.Context, dyn dynamic.Interface) ([]ComponentSnapshot, error) {
	bootstrap, err := ListAndSnapshotHelmReleases(ctx, dyn)
	if err != nil {
		return nil, err
	}

	apps, appErr := listApplicationInstallSnapshots(ctx, dyn)
	if appErr != nil {
		// Bootstrap-kit rows still render; the caller logs appErr so an
		// operator sees that the Application half is degraded rather than
		// reading an incomplete page as complete.
		return bootstrap, appErr
	}

	if len(apps) == 0 {
		return bootstrap, nil
	}
	out := make([]ComponentSnapshot, 0, len(bootstrap)+len(apps))
	out = append(out, bootstrap...)
	out = append(out, apps...)
	return out, nil
}

// listApplicationInstallSnapshots lists Application-install HelmReleases
// across every namespace via a server-side existence selector on
// ApplicationOrgLabel and projects them into ComponentSnapshots.
//
// Returns an empty slice (not nil) when nothing matches.
func listApplicationInstallSnapshots(ctx context.Context, dyn dynamic.Interface) ([]ComponentSnapshot, error) {
	if dyn == nil {
		return nil, fmt.Errorf("helmwatch: dynamic client is required")
	}
	list, err := dyn.Resource(HelmReleaseGVR).
		Namespace(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{LabelSelector: ApplicationOrgLabel})
	if err != nil {
		return nil, fmt.Errorf("helmwatch: list Application HelmReleases: %w", err)
	}

	out := make([]ComponentSnapshot, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		ns := u.GetNamespace()
		// An Application HR is never authored in flux-system. If one ever
		// is, the bootstrap-kit leg above already governs that namespace —
		// skipping here keeps the two legs disjoint so the union can never
		// emit the same object twice under two different AppIDs.
		if ns == "" || ns == FluxNamespace {
			continue
		}
		cs := snapshotFromHelmRelease(u, ApplicationComponentID(ns, u.GetName()))
		// DependsOn is deliberately NOT carried for Application installs.
		// extractDependsOn resolves spec.dependsOn[].name against the
		// bootstrap-kit `bp-` namespace of component ids, so carrying it
		// here would wire a per-Org row to a platform component row that
		// it does not actually depend on. A missing edge renders the row
		// as a leaf; a wrong edge is a false statement about the graph.
		cs.DependsOn = nil
		out = append(out, cs)
	}
	return out, nil
}
