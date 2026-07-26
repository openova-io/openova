// provisioning_postconditions.go — #5395. Post-provisioning verification for
// the Organization reconciler.
//
// THE FAILURE SHAPE THIS CLOSES
// -----------------------------
// Live on hw290: Organization `gamma-corp` read Ready/Active on every status
// surface while ALL SIX of its HTTPRoutes sat
// `Accepted=False reason=NoMatchingListenerHostname` —
// console./agenity./keycloak./mail./openclaw./wordpress.gamma-corp.omani.homes
// were unreachable because the `*.gamma-corp.omani.homes` listener pair was
// absent from the shared `cilium-gateway-console` Gateway (six sibling Orgs had
// theirs). Nothing anywhere surfaced an error. That is the worst shape the
// platform can ship: every status surface green, the customer sees nothing.
//
// WHY IT WAS INVISIBLE
// --------------------
// The reconciler authors a SET of artifacts but derived Ready from exactly ONE
// of them:
//
//   - `vclusterReadiness` (organization_controller.go) reads back the vCluster
//     HelmRelease + the `<slug>` Namespace, and NOTHING else. For a host-tier
//     Org it is satisfied by the namespace alone.
//   - `reconcileConsoleServing` (organization_controller.go) runs the DNS +
//     console-TLS + HTTPRoute trio best-effort. Its only output is
//     `degraded bool`, which feeds a requeue and is discarded — it never
//     reaches `status`. A permanent failure therefore loops silently.
//   - `ensureConsoleOrgListener` (tenant_console_tls.go) returns `(false, nil)`
//     — indistinguishable from success — when the console Gateway is not yet
//     present, so an Org provisioned inside that window is never retried once
//     it goes Ready (a Ready Org returns `ctrl.Result{}`: no requeue, and
//     `SetupWithManager` registers no watch on the Gateway).
//   - The boundary ResourceQuota + LimitRange are authored into the per-Org
//     Gitea repo and applied by Flux. Whether they ever landed is never read
//     back at all.
//
// So the Org could be missing its console listener, its quota, or both, and
// still report `status.vcluster.phase=Ready` + `Ready=True`.
//
// WHAT THIS FILE DOES
// -------------------
// `verifyProvisioned` reads back the artifacts the reconciler CLAIMS to have
// provisioned and returns the ones that are missing. Reconcile refuses to
// report Ready over a non-empty result: `status.vcluster.phase` stays
// `Provisioning` and the Ready condition goes False with reason
// `ProvisioningIncomplete` and a message naming EXACTLY what is absent.
//
// This is deliberately a postcondition check rather than a retry. A retry
// would hide the same gap one layer down — it would keep re-appending the
// listener while the Org still read Active, so a permanent failure would still
// be invisible. Naming the missing artifact in `status` is what makes the gap
// impossible to mistake for success; the 30s requeue that follows is the
// self-heal, and because the check is what keeps the requeue alive it ALSO
// closes the drift hole above (a Ready Org that loses its listener to an
// external rewrite now re-enters the loop instead of sitting silently broken).
//
// EVIDENCE OF ABSENCE, NOT ABSENCE OF EVIDENCE
// --------------------------------------------
// Only an explicit NotFound counts as missing. Any other read error (RBAC 403
// during a partial chart rollout, an apiserver blip, a CRD that is not
// installed on a Catalyst-Zero install) is reported as UNVERIFIABLE and is
// deliberately NOT treated as a failed postcondition — otherwise one botched
// RBAC rollout would red-flag every Organization on the Sovereign at once. An
// unverifiable probe logs and requeues; it never fabricates a red.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// provisioningPostconditions is the outcome of one verification pass.
//
// Missing holds human-readable descriptions of artifacts proven ABSENT (an
// explicit NotFound). Unverifiable holds artifacts whose probe errored for a
// reason that is not evidence of absence. The two are kept apart because they
// demand opposite treatment: Missing downgrades the Org's reported readiness;
// Unverifiable only requeues.
type provisioningPostconditions struct {
	Missing      []string
	Unverifiable []string
}

// complete reports whether every postcondition that COULD be checked passed.
// An unverifiable probe is not a failure — see the file header.
func (p provisioningPostconditions) complete() bool { return len(p.Missing) == 0 }

// message renders the Missing set into the Ready condition's message. Sorted so
// the string is stable across reconciles — an unstable message would defeat the
// byte-equal status-skip in patchStatus and hot-loop the controller (#5305).
func (p provisioningPostconditions) message() string {
	missing := append([]string(nil), p.Missing...)
	sort.Strings(missing)
	return "Organization is NOT fully provisioned — missing: " + strings.Join(missing, "; ")
}

// verifyProvisioned reads back the artifacts this controller authored for the
// Org and reports which ones are absent.
//
// Scope note: it verifies only artifacts THIS controller is the producer of.
// It does not assert on the customer's purchased Applications, the funnel's
// app tree, or anything a peer component owns — a postcondition check that
// reaches past its own producer boundary produces false reds it cannot fix.
//
// Checked:
//
//  1. The boundary LimitRange (`plan-limits`) in ns `<slug>` — rendered for
//     EVERY plan by gitops.Render, so its absence always means the boundary
//     tree never landed.
//  2. The boundary ResourceQuota (`plan-quota`) in ns `<slug>` — rendered only
//     for plans where gitops.PlanRendersResourceQuota is true. Flexi is the
//     on-demand soft-cap plan and renders none, so for Flexi the check is
//     skipped rather than failed. Pairing (1) and (2) is what turns "no
//     ResourceQuota" from an ambiguous observation into a decided one: quota
//     absent + LimitRange present == Flexi by design; both absent == the
//     boundary tree never landed (#5395 symptom B).
//  3. The per-Org console Gateway listener pair
//     (`console-https-<sub>` / `console-http-<sub>`) — only for an Org that
//     engages the tenant-networking up-path (a pool parentDomain). This is the
//     artifact whose absence produced NoMatchingListenerHostname on all six of
//     gamma-corp's routes (#5395 symptom A).
//
// The namespace itself is NOT re-probed here — vclusterReadiness already gates
// on it and verifyProvisioned is only consulted once that returned ready, so a
// second probe would be redundant work on the hot path.
func (r *Reconciler) verifyProvisioned(ctx context.Context, org *orgapi.Organization) provisioningPostconditions {
	var out provisioningPostconditions
	slug := strings.TrimSpace(org.Spec.Slug)
	if slug == "" {
		return out
	}

	// 1 + 2 — the plan boundary the customer paid for.
	lr := &corev1.LimitRange{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: slug, Name: gitops.BoundaryLimitRangeName}, lr); {
	case err == nil:
	case apierrors.IsNotFound(err):
		out.Missing = append(out.Missing, fmt.Sprintf(
			"LimitRange %s/%s (the per-plan boundary the org-controller renders into the per-Org repo — its absence means the boundary tree was never applied by Flux)",
			slug, gitops.BoundaryLimitRangeName))
	default:
		out.Unverifiable = append(out.Unverifiable, fmt.Sprintf(
			"LimitRange %s/%s: %s", slug, gitops.BoundaryLimitRangeName, err))
	}

	if gitops.PlanRendersResourceQuota(org.Spec.PlanSlug) {
		rq := &corev1.ResourceQuota{}
		switch err := r.Get(ctx, client.ObjectKey{Namespace: slug, Name: gitops.BoundaryResourceQuotaName}, rq); {
		case err == nil:
		case apierrors.IsNotFound(err):
			out.Missing = append(out.Missing, fmt.Sprintf(
				"ResourceQuota %s/%s (plan %q is a hard-capped tier — the cap the customer pays for is not on the namespace)",
				slug, gitops.BoundaryResourceQuotaName, org.Spec.PlanSlug))
		default:
			out.Unverifiable = append(out.Unverifiable, fmt.Sprintf(
				"ResourceQuota %s/%s: %s", slug, gitops.BoundaryResourceQuotaName, err))
		}
	}

	// 3 — the console edge. Only meaningful for an Org that engages the
	// tenant-networking up-path; an Org with no pool parentDomain is reached
	// via the Sovereign-wide `*.<sovFQDN>` wildcard and authors no listener.
	names, ok := orgConsoleTLSNamesForOrg(org)
	if !ok {
		return out
	}
	present, err := r.consoleOrgListenersPresent(ctx, names)
	switch {
	case err != nil:
		out.Unverifiable = append(out.Unverifiable, fmt.Sprintf(
			"console Gateway %s/%s listeners for %q: %s",
			r.consoleGatewayNamespace(), r.consoleGatewayName(), names.WildcardHost, err))
	case !present:
		out.Missing = append(out.Missing, fmt.Sprintf(
			"console Gateway listeners %s/%s on %s/%s for hostname %q (without them every HTTPRoute on that host is Accepted=False NoMatchingListenerHostname — the Org is unreachable)",
			names.HTTPSName, names.HTTPName,
			r.consoleGatewayNamespace(), r.consoleGatewayName(), names.WildcardHost))
	}
	return out
}

// consoleOrgListenersPresent reports whether BOTH per-Org listeners are live on
// the shared console Gateway. A missing Gateway is reported as absent listeners
// (not an error): during the bootstrap window before sovereign-tls has applied
// the Gateway the Org genuinely has no console edge, and saying so honestly is
// the entire point — `ensureConsoleOrgListener` returning `(false, nil)` for
// that same case is what let the gap pass as success in the first place.
func (r *Reconciler) consoleOrgListenersPresent(ctx context.Context, names orgConsoleTLSNames) (bool, error) {
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: r.consoleGatewayNamespace(),
		Name:      r.consoleGatewayName(),
	}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		return false, err
	}
	var https, http bool
	for _, l := range listeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		switch n, _ := m["name"].(string); n {
		case names.HTTPSName:
			https = true
		case names.HTTPName:
			http = true
		}
	}
	return https && http, nil
}

// orgConsoleTLSNamesForOrg derives the per-Org console-TLS names from the CR,
// reporting ok=false when the Org does not engage the tenant-networking up-path
// (no pool parentDomain, or no subdomain to form a 2-label host from).
//
// This is the SAME derivation reconcileTenantConsoleTLS + teardownTenantConsoleTLS
// perform inline; centralising it here means the verifier can never probe for a
// listener name different from the one the up-path writes.
func orgConsoleTLSNamesForOrg(org *orgapi.Organization) (orgConsoleTLSNames, bool) {
	parentDomain := strings.TrimSpace(org.Spec.TenantPublic.ParentDomain)
	if parentDomain == "" {
		return orgConsoleTLSNames{}, false
	}
	subdomain := strings.TrimSpace(org.Spec.TenantPublic.Subdomain)
	if subdomain == "" {
		subdomain = strings.TrimSpace(org.Spec.Slug)
	}
	if subdomain == "" {
		return orgConsoleTLSNames{}, false
	}
	return orgConsoleTLSNamesFor(subdomain, parentDomain), true
}
