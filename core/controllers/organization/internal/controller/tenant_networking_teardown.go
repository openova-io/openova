// tenant_networking_teardown.go — cascade-delete the per-Org tenant-networking
// artifacts when an Organization CR is removed (#4459, sibling of #4250).
//
// THE GAP THIS CLOSES
// -------------------
// The org-controller's deletion path already reaps everything that sits inside
// the per-Org `<slug>` namespace or carries an ownerRef the K8s garbage
// collector follows:
//
//   - the vCluster Namespace + HelmRelease + in-namespace NetworkPolicies — via
//     teardownPerOrgFlux (the per-Org Flux Kustomizations carry prune:true);
//   - the per-Org Keycloak realm — via teardownPerOrgRealm;
//   - the per-Org IaC repo + robot token — via teardownIacBootstrap;
//   - the Environment CR + the UserAccess CRs — via cluster-scoped
//     ownerReferences (GC-safe).
//
// But the per-Org artifacts the controller writes OUTSIDE that namespace, with
// NO ownerRef the GC follows, were left orphaned on every Org delete:
//
//   1. the `console-https-<slug>` / `console-http-<slug>` listener pair the
//      up-path appended to the SHARED console Gateway (cilium-gateway-console /
//      kube-system) — dead listeners ACCUMULATE on a shared resource;
//   2. the per-Org wildcard Certificate `org-wildcard-tls-<slug>-<parent>`
//      (+ its backing TLS Secret, cert-manager-GC'd with it) in kube-system;
//   3. the per-Org console HTTPRoute `catalyst-ui-<host-dashed>` in
//      catalyst-system (NOT the Org's `<slug>` ns → the ns-prune never reaches it);
//   4. the central-PowerDNS pool A-records `console.<slug>.<parent>` +
//      `*.<slug>.<parent>` (off-cluster → no GC at all; a stale record points a
//      later re-prov of the same slug at a DEAD console-ELB IP).
//
// A clean boundary must tear down as cleanly as it stands up: the same producer
// that materialized these artifacts from the CR (the org-controller) must
// de-materialize them when the CR is deleted. This orchestrator chains the four
// best-effort, absent-as-success teardown helpers each up-path step exposes, so
// the deletion path is idempotent and leaves no orphan on a repeated reconcile.
//
// OUT OF SCOPE (tracked separately): the Keycloak Sovereign-realm GROUP at
// `/<slug>` and the Gitea Org have no DeleteGroup / DeleteOrg client method
// today — adding those expands the shared core/controllers/pkg surface and is a
// follow-up. They are NOT part of this orchestrator.

package controller

import (
	"context"
	"strings"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// TenantNetworkingFinalizer guarantees the per-Org tenant-networking cascade
// runs before the CR tombstones — INDEPENDENT of whether the iac-bootstrap or
// per-Org-realm features are wired/enabled. Without a finalizer the deletion
// branch (incl. teardownTenantNetworking) only fires while SOME other finalizer
// holds the CR; a plain Org with neither iac nor realm wired would be removed by
// the API server the instant it is deleted and the cascade would never run,
// leaving the dead Gateway listeners / Certificate / HTTPRoute / DNS records
// behind (#4459). Scoped to the orgs.openova.io group per the finalizer-naming
// convention, parallel to IacBootstrapFinalizer / PerOrgRealmFinalizer.
const TenantNetworkingFinalizer = "orgs.openova.io/tenant-networking"

// orgEngagesTenantNetworking reports whether an Org engaged the tenant-
// networking up-path (a pool parentDomain → console TLS + listener + HTTPRoute +
// pool DNS were authored). Only such an Org needs the finalizer + teardown; a
// plain single-domain Org authored none of these, so adding a finalizer for a
// flow that will never run would needlessly delay its deletion.
func orgEngagesTenantNetworking(org *orgapi.Organization) bool {
	return strings.TrimSpace(org.Spec.TenantPublic.ParentDomain) != ""
}

// ensureTenantNetworkingFinalizer adds the finalizer if missing. Returns true
// when the caller should patch the CR so subsequent code uses the updated copy.
func ensureTenantNetworkingFinalizer(org *orgapi.Organization) bool {
	for _, f := range org.Finalizers {
		if f == TenantNetworkingFinalizer {
			return false
		}
	}
	org.Finalizers = append(org.Finalizers, TenantNetworkingFinalizer)
	return true
}

// removeTenantNetworkingFinalizer drops the finalizer. Returns true when the
// caller should patch the CR.
func removeTenantNetworkingFinalizer(org *orgapi.Organization) bool {
	keep := org.Finalizers[:0]
	removed := false
	for _, f := range org.Finalizers {
		if f == TenantNetworkingFinalizer {
			removed = true
			continue
		}
		keep = append(keep, f)
	}
	org.Finalizers = keep
	return removed
}

// teardownTenantNetworking removes the per-Org tenant-networking artifacts that
// live outside the Flux-pruned `<slug>` namespace + carry no GC-followed
// ownerRef: the shared-Gateway listener pair, the per-Org wildcard Certificate,
// the per-Org console HTTPRoute, and the central-PowerDNS pool A-records.
//
// Each sub-teardown is best-effort + absent-as-success (a missing artifact is
// success), so the orchestrator is idempotent — a re-reconcile of an
// already-cleaned Org is a clean no-op. We attempt ALL four even if an earlier
// one errored, accumulating the FIRST error to return (so the caller requeues
// and retries the whole set) — this avoids a single transient PowerDNS hiccup
// stranding the Certificate/HTTPRoute/listener cleanup. A no-pool-parentDomain
// Org engaged none of these up-path steps, so every helper short-circuits to a
// no-op.
func (r *Reconciler) teardownTenantNetworking(ctx context.Context, org *orgapi.Organization) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 1+2. Shared-Gateway listener pair + per-Org wildcard Certificate.
	if _, err := r.teardownTenantConsoleTLS(ctx, org); err != nil {
		r.Log.Error(err, "tenant-networking teardown: console TLS", "organization", org.Name)
		note(err)
	}
	// 3. Per-Org console HTTPRoute (catalyst-system).
	if _, err := r.teardownTenantRoute(ctx, org); err != nil {
		r.Log.Error(err, "tenant-networking teardown: console HTTPRoute", "organization", org.Name)
		note(err)
	}
	// 4. Central-PowerDNS pool A-records.
	if _, err := r.teardownTenantDNS(ctx, org); err != nil {
		r.Log.Error(err, "tenant-networking teardown: pool DNS", "organization", org.Name)
		note(err)
	}
	return firstErr
}
