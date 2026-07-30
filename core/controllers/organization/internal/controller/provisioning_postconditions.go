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
//  4. That the SAME pair is ADMITTED — present in the Gateway's
//     `status.listeners`, and bound to the ports the console LoadBalancer
//     actually forwards to (#5511). Presence in `spec` is not serving. On
//     hw291 `cilium-gateway-console` accepted 8 listeners into spec and
//     published only 6 in status; the two it silently declined were exactly
//     the per-Org pair, with no error, no condition and no event anywhere, so
//     `agenity.uatcorp.omani.homes` answered 000 on 6 of 6 probes while its
//     HelmRelease, Pod and HTTPRoute were all healthy and the apex console on
//     the same VIP served 200. A listener accepted into spec and absent from
//     status is, from the operator's side, indistinguishable from one that was
//     never configured — so it must be named in status, loudly, the same way
//     an absent one is.
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
	rb, err := r.consoleOrgListenersReadback(ctx, names)
	gwNS, gwName := r.consoleGatewayNamespace(), r.consoleGatewayName()
	switch {
	case err != nil:
		out.Unverifiable = append(out.Unverifiable, fmt.Sprintf(
			"console Gateway %s/%s listeners for %q: %s",
			gwNS, gwName, names.WildcardHost, err))
	case rb.GatewayAbsent || len(rb.SpecMissing) > 0:
		out.Missing = append(out.Missing, fmt.Sprintf(
			"console Gateway listeners %s/%s on %s/%s for hostname %q (without them every HTTPRoute on that host is Accepted=False NoMatchingListenerHostname — the Org is unreachable)",
			names.HTTPSName, names.HTTPName, gwNS, gwName, names.WildcardHost))
	default:
		// 4 — the listener is in spec. That is NOT the same as serving. Both
		// checks below close a way for the door to be dead while every status
		// surface reads green (#5511).
		if len(rb.PortDrift) > 0 {
			out.Missing = append(out.Missing, fmt.Sprintf(
				"console Gateway listeners on %s/%s bind the wrong port — %s (the console LoadBalancer forwards public 443/80 to the apex console-https/console-http ports and nothing else, so a per-Org listener on any other port receives no traffic and %q answers 000 while the workload behind it is healthy)",
				gwNS, gwName, strings.Join(rb.PortDrift, ", "), names.WildcardHost))
		}
		switch {
		case !rb.StatusObserved:
			// The Gateway controller has not published ANY listener status
			// yet. Absence of evidence, not evidence of absence — requeue.
			// This is also the vacuity guard on the check below: without it,
			// "our listener is in status" would pass trivially on an empty
			// status and assert nothing at all.
			out.Unverifiable = append(out.Unverifiable, fmt.Sprintf(
				"console Gateway %s/%s status.listeners is empty — the Gateway controller has not published listener status yet, so acceptance of %s/%s cannot be decided",
				gwNS, gwName, names.HTTPSName, names.HTTPName))
		case len(rb.StatusDropped) > 0:
			out.Missing = append(out.Missing, fmt.Sprintf(
				"console Gateway %s/%s ACCEPTED %d listeners into spec but published only %d in status — silently dropped: %s (a listener in spec and absent from status carries no condition and no event: from the operator's side it is indistinguishable from one that was never configured, yet every per-Org customer door depends on it)",
				gwNS, gwName, rb.SpecCount, rb.StatusCount, strings.Join(rb.StatusDropped, ", ")))
		}
	}
	return out
}

// consoleListenerReadback is one probe of the shared console Gateway, across
// BOTH spec and status (#5511).
//
// Reading spec alone is what let the #5511 defect ship: the org-controller
// appended the per-Org pair, the pair WAS in spec, the postcondition check
// passed green — and the Gateway controller had silently declined to admit both
// listeners, so `status.listeners` carried 6 of the 8 in spec, with no error, no
// condition and no event anywhere. The customer door answered 000 on every
// probe while the Organization, its HelmRelease and its Pod all read Ready.
type consoleListenerReadback struct {
	// GatewayAbsent — the console Gateway itself is NotFound.
	GatewayAbsent bool
	// SpecMissing — our listener names absent from spec.listeners.
	SpecMissing []string
	// SpecCount / StatusCount — the two listener counts. A gateway that
	// admits fewer listeners than it accepted is the #5511 signature.
	SpecCount   int
	StatusCount int
	// StatusObserved — the Gateway controller has published a NON-EMPTY
	// status.listeners. Everything derived from status is meaningless until
	// this is true, so it gates the StatusDropped verdict.
	StatusObserved bool
	// StatusDropped — listener names present in spec.listeners and absent
	// from a non-empty status.listeners. Not scoped to this Org: a sibling
	// Org's dropped listener is the same defect and worth surfacing on the
	// first Org that notices it.
	StatusDropped []string
	// PortDrift — our listeners whose port differs from the live apex pair,
	// described for the operator.
	PortDrift []string
}

// consoleOrgListenersReadback probes the shared console Gateway for this Org's
// listener pair and reports what it found in spec, in status, and on the ports.
// A missing Gateway is reported as GatewayAbsent (not an error): during the
// bootstrap window before sovereign-tls has applied the Gateway the Org
// genuinely has no console edge, and saying so honestly is the entire point —
// `ensureConsoleOrgListener` returning `(false, nil)` for that same case is
// what let the gap pass as success in the first place.
func (r *Reconciler) consoleOrgListenersReadback(ctx context.Context, names orgConsoleTLSNames) (consoleListenerReadback, error) {
	var out consoleListenerReadback

	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: r.consoleGatewayNamespace(),
		Name:      r.consoleGatewayName(),
	}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			out.GatewayAbsent = true
			return out, nil
		}
		return out, err
	}

	specListeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		return out, fmt.Errorf("read spec.listeners: %w", err)
	}
	statusListeners, _, err := unstructured.NestedSlice(gw.Object, "status", "listeners")
	if err != nil {
		return out, fmt.Errorf("read status.listeners: %w", err)
	}
	out.SpecCount = len(specListeners)
	out.StatusCount = len(statusListeners)
	out.StatusObserved = len(statusListeners) > 0

	specByName := map[string]map[string]any{}
	specOrder := make([]string, 0, len(specListeners))
	for _, l := range specListeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n != "" {
			specByName[n] = m
			specOrder = append(specOrder, n)
		}
	}
	inStatus := map[string]bool{}
	for _, l := range statusListeners {
		if m, ok := l.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				inStatus[n] = true
			}
		}
	}

	// The per-Org pair MUST ride the live apex ports — same derivation the
	// up-path uses, so the verifier can never demand a port the writer would
	// not have written.
	httpsPort, httpPort := consoleApexListenerPorts(specListeners)
	wantPort := map[string]int64{names.HTTPSName: httpsPort, names.HTTPName: httpPort}

	for _, n := range []string{names.HTTPSName, names.HTTPName} {
		m, ok := specByName[n]
		if !ok {
			out.SpecMissing = append(out.SpecMissing, n)
			continue
		}
		if got, ok := listenerPort(m); ok && got != wantPort[n] {
			out.PortDrift = append(out.PortDrift, fmt.Sprintf(
				"%s binds %d, the apex pair serves %d", n, got, wantPort[n]))
		}
	}

	// Count-based: every listener the Gateway accepted into spec but did not
	// publish in status. Iterated in spec order, so the rendered message is
	// stable across reconciles (an unstable message defeats the byte-equal
	// status-skip in patchStatus and hot-loops the controller, #5305).
	if out.StatusObserved {
		for _, n := range specOrder {
			if !inStatus[n] {
				out.StatusDropped = append(out.StatusDropped, n)
			}
		}
	}
	return out, nil
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
