// org_console_tls_reap.go — #5364 / #5649. The DELETE-side mirror of the
// #5635/PR-#5647 create-side reconcile: reap the per-Org console gateway
// surface (and the orphaned org boundary Namespace) of every Organization
// whose CR no longer exists, in EVERY region, level-triggered.
//
// THE GAP this file closes.
//
// Creation of the per-Org console surface has TWO producers and, since
// #5511/#5635, fans out to every region. Teardown had ONE producer in ONE
// region, keyed on ONE name shape:
//
//   - the org-controller's teardownTenantRoute
//     (core/controllers/organization/internal/controller/tenant_route.go:274)
//     deletes ONLY its own route name `catalyst-ui-<console-host-dashed>` —
//     catalyst-api's `catalyst-ui-<slug>-<parent-dashed>`
//     (org_console_tls.go resolveOrgConsoleTLSNames) is invisible to it;
//   - the whole delete cascade (teardownTenantNetworking,
//     removeConsoleOrgListener, deleteOrgWildcardCert) writes through the
//     org-controller's own single-cluster client — a secondary region NEVER
//     receives any delete;
//   - the #5511 cert-secret mirror (mirrorOrgConsoleCertSecret) writes
//     `kube-system/org-wildcard-tls-<slug>-<parent-dashed>` Secrets into
//     secondaries that NO teardown path deletes, and the HOST region's
//     issued Secret is not GC'd on Certificate delete either (cert-manager
//     only owner-refs the backing Secret under
//     --enable-certificate-owner-ref, which we do not run);
//   - the org boundary Namespace `<slug>` — which contains the host-deployed
//     bp-keycloak StatefulSet + every tenant HelmRelease (#5364's original
//     orphan) — converges only through the gitops prune chain; when a link
//     in that chain misfires (the hw288 beta-corp shape) nothing ever
//     retries the delete.
//
// Proven live on hw292 (UAT row R17, 2026-08-04): HTTPRoute
// `catalyst-ui-r17probe-omani-homes` orphaned in BOTH regions, Gateway
// listeners `console-{https,http}-r17probe` orphaned in region-b, TLS Secret
// `org-wildcard-tls-r17probe-omani-homes` orphaned in region-a — hours after
// the Org was deleted. The better creation gets, the more residue the
// one-name/one-region teardown leaves (#5649).
//
// THE FIX — same shape as the create side: the Organization CR is the ONE
// identity both producers key on, and catalyst-api is the ONE process holding
// a per-region client for every region (orgConsoleTLSTargets). So the reap
// runs inside the #5635 reconcile pass (reconcileOrgConsoleTLSOnce): scan
// every region for per-Org console artifacts, list the live Organization CRs,
// and delete every artifact whose org identity has no live CR — BOTH route
// name shapes, the listener pair, the Certificate, every same-name TLS
// Secret, and the orphaned org Namespace (deleting the ns reaps the
// host-deployed bp-keycloak + its PVCs + every tenant HelmRelease inside it).
// Level-triggered: an Org that is already half-deleted converges to
// fully-deleted on the next pass; an Org that is already clean is a no-op.
//
// SAFETY MODEL — a reaper must be provably unable to eat a live Org or the
// Sovereign's own front door:
//
//  1. ORDERING. Artifacts are scanned FIRST, the Organization CRs are listed
//     AFTER. A producer only ever creates artifacts for an ALREADY-EXISTING
//     CR, so any artifact visible at scan time belongs to a CR that either
//     is in the later list (live → protected) or was deleted (orphan). An
//     Org created mid-pass cannot lose its surface.
//  2. AGE GRACE. Timestamped candidates younger than orgConsoleReapMinAge
//     are skipped this pass — belt-and-braces against creation orderings the
//     ordering argument does not model. A zero timestamp (never set) passes.
//  3. FQDN GUARD. The Sovereign's own console host
//     `console.<sovereignFQDN>` parses exactly like a per-Org host
//     (`console.<slug>.<parent>`), so the reap REFUSES to run at all when
//     the Sovereign FQDN is unknown, and excludes any candidate whose org
//     zone equals the Sovereign FQDN.
//  4. STRUCTURAL MATCH. Only objects positively identified as per-Org
//     console artifacts are candidates: route name prefix `catalyst-ui-`
//     (the bare Sovereign route `catalyst-ui` never matches) + a
//     `console.<slug>.<parent>` hostname; listener name prefix
//     `console-https-`/`console-http-` + a `*.<slug>.<parent>` hostname
//     (the chart's apex pair `console-https`/`console-http` never matches);
//     Certificate/Secret name prefix `org-wildcard-tls-` (the Sovereign's
//     per-zone wildcards are `sovereign-wildcard-tls-*`) + a label- or
//     cert-manager-annotation-derived org zone; Namespace with an org
//     identity label (`openova.io/organization` / `catalyst.openova.io/org`
//     / `kustomize.toolkit.fluxcd.io/name=org-tenants`) and NOT in the
//     protected-namespace denylist. Anything that cannot be positively
//     identified is left alone.
//  5. ABORT ON BLIND. A failed Organization List aborts the whole reap pass
//     — the reaper never acts on an unknown live set. (A successful EMPTY
//     list is authoritative: the wrong-API-group failure mode errors, it
//     does not return empty.)
//  6. DELETE BY OBSERVED NAME. Every delete targets an object the scan
//     positively matched — nothing is derived-then-deleted sight unseen.
//     Absent-as-success throughout, so repeated passes are idempotent.
//
// The listener strip is the same read-modify-write Update the
// org-controller's removeConsoleOrgListener uses (SSA cannot prune another
// field manager's list entries, and the org-controller's listeners were
// written via Update). RBAC for the delete verbs (httproutes/certificates/
// secrets/namespaces delete, gateways update) ships in
// products/catalyst/chart/templates/clusterrole-cutover-driver.yaml — the
// chart change #5647 deliberately deferred.
//
// RESIDUAL (#5359, stated, not fixed here): a region whose GitOps stream is
// severed receives DIRECT API deletes from this reaper just fine, but a
// region-b whose bootstrap-kit still reconciles from public github.com will
// keep re-applying whatever THAT stream materializes. This reaper closes the
// producer-side gap; the region-b stream pivot is #5359's own fix.

package handler

import (
	"context"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// orgConsoleReapMinAge is the age-grace window (safety model #2): a
// timestamped candidate younger than this is skipped this pass and
// re-evaluated on the next tick. A package var so tests can shrink it,
// though the shipped tests seed aged/zero timestamps instead so they also
// compile against the unfixed tree.
var orgConsoleReapMinAge = 15 * time.Minute

const (
	// orgWildcardTLSNamePrefix — per-Org wildcard Certificate + backing/
	// mirrored TLS Secret name prefix (both producers agree, #4241). The
	// Sovereign's own per-zone wildcards are `sovereign-wildcard-tls-*`
	// (sovereign-wildcard-certs.yaml) and can never match.
	orgWildcardTLSNamePrefix = "org-wildcard-tls-"
	// orgConsoleRouteNamePrefix — both producers' console HTTPRoute names
	// start with this; the Sovereign's own console route is the bare
	// `catalyst-ui` (products/catalyst/chart/templates/httproute.yaml) and
	// never matches the prefix-plus-suffix requirement.
	orgConsoleRouteNamePrefix = "catalyst-ui-"
	// per-Org listener name prefixes; the chart's apex pair is the bare
	// `console-https`/`console-http` and never matches.
	orgConsoleListenerHTTPSPrefix = consoleApexListenerHTTPSName + "-"
	orgConsoleListenerHTTPPrefix  = consoleApexListenerHTTPName + "-"
	consoleHostLabelPrefix        = "console."
)

// orgConsoleReapProtectedNamespaces — hard denylist (safety model #4). A
// namespace listed here is never deleted regardless of labels. Org boundary
// namespaces are named `<slug>` and can never legitimately collide with
// these, so the list only ever blocks a mislabelled system namespace.
var orgConsoleReapProtectedNamespaces = map[string]bool{
	"default":          true,
	"kube-system":      true,
	"kube-public":      true,
	"kube-node-lease":  true,
	"flux-system":      true,
	"catalyst-system":  true,
	"catalyst":         true,
	"openova-system":   true,
	"cert-manager":     true,
	"cilium-secrets":   true,
	"gitea":            true,
	"harbor":           true,
	"keycloak":         true,
	"openbao":          true,
	"kyverno":          true,
	"monitoring":       true,
	"external-secrets": true,
}

// orgConsoleReapScan is one region's positively-identified per-Org console
// artifact candidates, keyed by the org identity (slug) each artifact was
// derived from. Names are OBSERVED names (safety model #6).
type orgConsoleReapScan struct {
	target     orgConsoleTLSTarget
	routes     map[string][]string // slug -> HTTPRoute names (catalyst-system)
	listeners  map[string]bool     // slug -> per-Org listener pair present on the console Gateway
	certs      map[string][]string // slug -> Certificate names (kube-system)
	secrets    map[string][]string // slug -> TLS Secret names (kube-system)
	namespaces map[string][]string // slug -> Namespace names
}

// reapOrgConsoleTLSOrphans is the teardown half of the #5635 reconcile pass.
// Scans every region target for per-Org console artifacts, lists the live
// Organization CRs AFTER the scans (safety model #1), and deletes every
// candidate whose org identity has no live CR. Best-effort: every failure is
// logged and the next tick retries; nothing here can fail the pass.
func (h *Handler) reapOrgConsoleTLSOrphans(ctx context.Context, deps *sovereignDeps) {
	sovereignFQDN := strings.ToLower(strings.TrimSpace(h.orgTenantDeps.OTECHFQDN))
	if sovereignFQDN == "" {
		// Safety model #3: without the FQDN the Sovereign's own console
		// surface is indistinguishable from a per-Org one. Refuse entirely.
		h.log.Info("org-console-reap: skipped — Sovereign FQDN unset, cannot exclude the Sovereign's own console surface (#5364)")
		return
	}

	// 1. Scan every region FIRST.
	targets := h.orgConsoleTLSTargets(deps)
	scans := make([]*orgConsoleReapScan, 0, len(targets))
	for _, tgt := range targets {
		scans = append(scans, h.scanOrgConsoleArtifacts(ctx, tgt, sovereignFQDN))
	}

	// 2. List the live Organization CRs AFTER the scans.
	live, ok := h.liveOrgIdentities(ctx, deps)
	if !ok {
		// Safety model #5: never reap against an unknown live set.
		return
	}

	// 3. Reap every candidate identity with no live CR.
	for _, sc := range scans {
		h.reapScanOrphans(ctx, sc, live)
	}
}

// liveOrgIdentities lists every Organization CR and returns the set of
// lowercased identities a per-Org artifact may be keyed on: the CR name,
// spec.slug, and spec.tenantPublic.subdomain. ok=false means the list
// FAILED and the caller must not reap (a successful empty list is a valid,
// authoritative "no Orgs exist").
func (h *Handler) liveOrgIdentities(ctx context.Context, deps *sovereignDeps) (map[string]bool, bool) {
	list, err := deps.dyn.Resource(organizationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("org-console-reap: list Organizations failed — reap pass ABORTED, next tick retries (#5364)",
			"err", err)
		return nil, false
	}
	live := map[string]bool{}
	add := func(s string) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			live[s] = true
		}
	}
	for i := range list.Items {
		obj := &list.Items[i]
		add(obj.GetName())
		add(nestedString(obj.Object, "spec", "slug"))
		add(nestedString(obj.Object, "spec", "tenantPublic", "subdomain"))
	}
	return live, true
}

// orgZoneSlugParent splits an org zone `<slug>.<parent>` into its slug +
// parent, applying the structural guards: the slug must satisfy orgSlugRE,
// the parent must itself contain a dot (every org-pool parent is at least
// two labels — omani.homes/rest/trade/works), and the zone must NOT be the
// Sovereign's own FQDN (safety model #3 — `console.<sovFQDN>` parses exactly
// like a per-Org console host).
func orgZoneSlugParent(zone, sovereignFQDN string) (slug string, ok bool) {
	zone = strings.ToLower(strings.TrimSpace(zone))
	if zone == "" || zone == sovereignFQDN {
		return "", false
	}
	idx := strings.Index(zone, ".")
	if idx <= 0 {
		return "", false
	}
	slug, parent := zone[:idx], zone[idx+1:]
	if !orgSlugRE.MatchString(slug) || !strings.Contains(parent, ".") {
		return "", false
	}
	return slug, true
}

// reapCandidateOldEnough applies the age grace (safety model #2). A zero
// creationTimestamp (never stamped — e.g. a test fake) passes.
func reapCandidateOldEnough(ts metav1.Time) bool {
	if ts.IsZero() {
		return true
	}
	return time.Since(ts.Time) >= orgConsoleReapMinAge
}

// scanOrgConsoleArtifacts collects one region's per-Org console artifact
// candidates. Every match is structural (safety model #4); anything that
// cannot be positively identified is skipped. Scan failures are logged and
// yield an empty category — the reap then simply has no candidates there.
func (h *Handler) scanOrgConsoleArtifacts(ctx context.Context, tgt orgConsoleTLSTarget, sovereignFQDN string) *orgConsoleReapScan {
	sc := &orgConsoleReapScan{
		target:     tgt,
		routes:     map[string][]string{},
		listeners:  map[string]bool{},
		certs:      map[string][]string{},
		secrets:    map[string][]string{},
		namespaces: map[string][]string{},
	}
	if tgt.dyn != nil {
		h.scanOrgConsoleRoutes(ctx, sc, sovereignFQDN)
		h.scanOrgConsoleListeners(ctx, sc, sovereignFQDN)
		h.scanOrgConsoleCertificates(ctx, sc, sovereignFQDN)
	}
	if tgt.core != nil {
		h.scanOrgConsoleSecrets(ctx, sc, sovereignFQDN)
		h.scanOrgNamespaces(ctx, sc)
	}
	return sc
}

// scanOrgConsoleRoutes — candidates are HTTPRoutes in catalyst-system whose
// name starts with `catalyst-ui-` (with a non-empty suffix, so the Sovereign's
// bare `catalyst-ui` route never matches) and which serve a
// `console.<slug>.<parent>` hostname. This catches BOTH producers' name
// shapes (`catalyst-ui-<slug>-<parent-dashed>` and
// `catalyst-ui-console-<host-dashed>`) AND any adopted/hand-applied variant,
// because the hostname — not the name — is the identity.
func (h *Handler) scanOrgConsoleRoutes(ctx context.Context, sc *orgConsoleReapScan, sovereignFQDN string) {
	list, err := sc.target.dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("org-console-reap: list HTTPRoutes failed — routes not reaped this pass",
			"region", sc.target.region, "err", err)
		return
	}
	for i := range list.Items {
		r := &list.Items[i]
		name := r.GetName()
		if !strings.HasPrefix(name, orgConsoleRouteNamePrefix) || name == orgConsoleRouteNamePrefix {
			continue
		}
		if !reapCandidateOldEnough(r.GetCreationTimestamp()) {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames")
		for _, host := range hosts {
			host = strings.ToLower(strings.TrimSpace(host))
			if !strings.HasPrefix(host, consoleHostLabelPrefix) {
				continue
			}
			if slug, ok := orgZoneSlugParent(strings.TrimPrefix(host, consoleHostLabelPrefix), sovereignFQDN); ok {
				sc.routes[slug] = append(sc.routes[slug], name)
				break
			}
		}
	}
}

// scanOrgConsoleListeners — candidates are listener pairs on the console
// Gateway named `console-https-<slug>`/`console-http-<slug>` whose hostname
// is the matching `*.<slug>.<parent>` wildcard. The name suffix must agree
// with the hostname's leftmost zone label, so a differently-purposed
// listener that merely shares the prefix can never be claimed.
func (h *Handler) scanOrgConsoleListeners(ctx context.Context, sc *orgConsoleReapScan, sovereignFQDN string) {
	gw, err := sc.target.dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(ctx, consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			h.log.Warn("org-console-reap: read console Gateway failed — listeners not reaped this pass",
				"region", sc.target.region, "err", err)
		}
		return
	}
	listeners, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil || !found {
		return
	}
	for _, l := range listeners {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		name, _ := lm["name"].(string)
		if !strings.HasPrefix(name, orgConsoleListenerHTTPSPrefix) {
			continue
		}
		slug := strings.TrimPrefix(name, orgConsoleListenerHTTPSPrefix)
		hostname, _ := lm["hostname"].(string)
		zone := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(hostname)), "*.")
		zoneSlug, ok := orgZoneSlugParent(zone, sovereignFQDN)
		if !ok || zoneSlug != slug {
			continue
		}
		sc.listeners[slug] = true
	}
}

// scanOrgConsoleCertificates — candidates are cert-manager Certificates in
// kube-system named `org-wildcard-tls-*`, with the org identity taken from
// the producers' own labels (`catalyst.openova.io/org-subdomain` — both
// stamp it) or, failing that, from spec.commonName (`<slug>.<parent>`).
func (h *Handler) scanOrgConsoleCertificates(ctx context.Context, sc *orgConsoleReapScan, sovereignFQDN string) {
	list, err := sc.target.dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("org-console-reap: list Certificates failed — certs not reaped this pass",
			"region", sc.target.region, "err", err)
		return
	}
	for i := range list.Items {
		c := &list.Items[i]
		if !strings.HasPrefix(c.GetName(), orgWildcardTLSNamePrefix) {
			continue
		}
		if !reapCandidateOldEnough(c.GetCreationTimestamp()) {
			continue
		}
		zone := strings.TrimSpace(c.GetLabels()["catalyst.openova.io/org-subdomain"])
		if zone != "" {
			parent := strings.TrimSpace(c.GetLabels()["catalyst.openova.io/pool-parent"])
			if parent == "" {
				parent = strings.TrimSpace(c.GetLabels()["catalyst.openova.io/parent-zone"])
			}
			if parent == "" {
				zone = "" // label slug without a parent zone — fall through to commonName
			} else {
				zone = zone + "." + parent
			}
		}
		if zone == "" {
			zone = nestedString(c.Object, "spec", "commonName")
		}
		if slug, ok := orgZoneSlugParent(zone, sovereignFQDN); ok {
			sc.certs[slug] = append(sc.certs[slug], c.GetName())
		}
	}
}

// scanOrgConsoleSecrets — candidates are kube-system TLS Secrets named
// `org-wildcard-tls-*`: the host region's cert-manager-issued Secret
// (identified via the `cert-manager.io/common-name` annotation) and the
// #5511 mirrored copies in secondaries (identified via the producers'
// labels). A Secret whose org identity cannot be derived is left alone.
func (h *Handler) scanOrgConsoleSecrets(ctx context.Context, sc *orgConsoleReapScan, sovereignFQDN string) {
	list, err := sc.target.core.CoreV1().Secrets(consoleCertNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("org-console-reap: list Secrets failed — cert secrets not reaped this pass",
			"region", sc.target.region, "err", err)
		return
	}
	for i := range list.Items {
		s := &list.Items[i]
		if !strings.HasPrefix(s.Name, orgWildcardTLSNamePrefix) || s.Type != "kubernetes.io/tls" {
			continue
		}
		if !reapCandidateOldEnough(s.CreationTimestamp) {
			continue
		}
		zone := ""
		if sub := strings.TrimSpace(s.Labels["catalyst.openova.io/org-subdomain"]); sub != "" {
			if parent := strings.TrimSpace(s.Labels["catalyst.openova.io/pool-parent"]); parent != "" {
				zone = sub + "." + parent
			}
		}
		if zone == "" {
			zone = strings.TrimSpace(s.Annotations["cert-manager.io/common-name"])
		}
		if slug, ok := orgZoneSlugParent(zone, sovereignFQDN); ok {
			sc.secrets[slug] = append(sc.secrets[slug], s.Name)
		}
	}
}

// scanOrgNamespaces — candidates are namespaces carrying an org identity
// label: `openova.io/organization` (the org-controller's boundary-ns label,
// value = slug), `catalyst.openova.io/org` (catalyst-api's ensureOrgNamespace
// label), or `kustomize.toolkit.fluxcd.io/name=org-tenants` (the
// funnel-materialized tenant ns shape — identity = the ns name). The
// protected denylist + a `kube-` name-prefix guard apply on top; deleting an
// orphaned org ns is what reaps the host-deployed bp-keycloak + its PVCs +
// every tenant HelmRelease inside it (#5364).
func (h *Handler) scanOrgNamespaces(ctx context.Context, sc *orgConsoleReapScan) {
	seen := map[string]bool{}
	addNS := func(name, identity string, ts metav1.Time) {
		name = strings.ToLower(strings.TrimSpace(name))
		identity = strings.ToLower(strings.TrimSpace(identity))
		if name == "" || identity == "" || seen[name] {
			return
		}
		if orgConsoleReapProtectedNamespaces[name] || strings.HasPrefix(name, "kube-") {
			return
		}
		if !orgSlugRE.MatchString(identity) || !reapCandidateOldEnough(ts) {
			return
		}
		seen[name] = true
		sc.namespaces[identity] = append(sc.namespaces[identity], name)
	}
	for _, selector := range []string{
		"openova.io/organization",
		"catalyst.openova.io/org",
		"kustomize.toolkit.fluxcd.io/name=org-tenants",
	} {
		list, err := sc.target.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			h.log.Warn("org-console-reap: list namespaces failed — namespaces not reaped this pass",
				"region", sc.target.region, "selector", selector, "err", err)
			continue
		}
		for i := range list.Items {
			ns := &list.Items[i]
			identity := ns.Labels["openova.io/organization"]
			if identity == "" {
				identity = ns.Labels["catalyst.openova.io/org"]
			}
			if identity == "" {
				identity = ns.Name
			}
			addNS(ns.Name, identity, ns.CreationTimestamp)
		}
	}
}

// reapScanOrphans deletes, on ONE region target, every scanned candidate
// whose org identity is absent from the live set. Every delete is by
// observed name, absent-as-success, and independent — one failure never
// blocks the rest (the next tick retries).
func (h *Handler) reapScanOrphans(ctx context.Context, sc *orgConsoleReapScan, live map[string]bool) {
	orphan := func(slug string) bool { return !live[slug] }
	reaped := 0

	for slug, names := range sc.routes {
		if !orphan(slug) {
			continue
		}
		for _, name := range names {
			if err := deleteAbsentOK(func() error {
				return sc.target.dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
					Delete(ctx, name, metav1.DeleteOptions{})
			}); err != nil {
				h.log.Error("org-console-reap: delete orphaned console HTTPRoute failed — next tick retries",
					"region", sc.target.region, "org", slug, "route", name, "err", err)
				continue
			}
			reaped++
			h.log.Info("org-console-reap: deleted orphaned per-Org console HTTPRoute (#5364 #5649)",
				"region", sc.target.region, "org", slug, "route", name)
		}
	}

	for slug := range sc.listeners {
		if !orphan(slug) {
			continue
		}
		removed, err := removeOrphanConsoleListeners(ctx, sc.target.dyn, slug)
		if err != nil {
			h.log.Error("org-console-reap: strip orphaned console Gateway listeners failed — next tick retries",
				"region", sc.target.region, "org", slug, "err", err)
			continue
		}
		if removed {
			reaped++
			h.log.Info("org-console-reap: stripped orphaned per-Org console Gateway listener pair (#5364 #5649)",
				"region", sc.target.region, "org", slug,
				"https_listener", orgConsoleListenerHTTPSPrefix+slug,
				"http_listener", orgConsoleListenerHTTPPrefix+slug)
		}
	}

	for slug, names := range sc.certs {
		if !orphan(slug) {
			continue
		}
		for _, name := range names {
			if err := deleteAbsentOK(func() error {
				return sc.target.dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
					Delete(ctx, name, metav1.DeleteOptions{})
			}); err != nil {
				h.log.Error("org-console-reap: delete orphaned per-Org Certificate failed — next tick retries",
					"region", sc.target.region, "org", slug, "certificate", name, "err", err)
				continue
			}
			reaped++
			h.log.Info("org-console-reap: deleted orphaned per-Org wildcard Certificate (#5364 #5649)",
				"region", sc.target.region, "org", slug, "certificate", name)
		}
	}

	for slug, names := range sc.secrets {
		if !orphan(slug) {
			continue
		}
		for _, name := range names {
			if err := deleteAbsentOK(func() error {
				return sc.target.core.CoreV1().Secrets(consoleCertNamespace).Delete(ctx, name, metav1.DeleteOptions{})
			}); err != nil {
				h.log.Error("org-console-reap: delete orphaned per-Org TLS Secret failed — next tick retries",
					"region", sc.target.region, "org", slug, "secret", name, "err", err)
				continue
			}
			reaped++
			h.log.Info("org-console-reap: deleted orphaned per-Org wildcard TLS Secret (#5364 #5649)",
				"region", sc.target.region, "org", slug, "secret", name)
		}
	}

	for slug, names := range sc.namespaces {
		if !orphan(slug) {
			continue
		}
		for _, name := range names {
			if err := deleteAbsentOK(func() error {
				return sc.target.core.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
			}); err != nil {
				h.log.Error("org-console-reap: delete orphaned org Namespace failed — next tick retries",
					"region", sc.target.region, "org", slug, "namespace", name, "err", err)
				continue
			}
			reaped++
			h.log.Info("org-console-reap: deleted orphaned org Namespace — reaps the host-deployed bp-keycloak + tenant HelmReleases inside it (#5364)",
				"region", sc.target.region, "org", slug, "namespace", name)
		}
	}

	if reaped > 0 {
		h.log.Info("org-console-reap: pass complete",
			"region", sc.target.region, "clusterID", sc.target.clusterID, "reaped", reaped)
	}
}

// removeOrphanConsoleListeners strips `console-https-<slug>` +
// `console-http-<slug>` off the console Gateway via the same
// read-modify-write Update the org-controller's removeConsoleOrgListener
// uses — SSA cannot prune list entries another field manager owns, and the
// funnel-door listeners were written by the org-controller via Update.
// Every OTHER listener (the apex pair, every live Org's pair) is preserved
// byte-for-byte. Absent-as-success.
func removeOrphanConsoleListeners(ctx context.Context, dyn dynamic.Interface, slug string) (bool, error) {
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(ctx, consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	listeners, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil || !found {
		return false, err
	}
	drop := map[string]bool{
		orgConsoleListenerHTTPSPrefix + slug: true,
		orgConsoleListenerHTTPPrefix + slug:  true,
	}
	kept := make([]any, 0, len(listeners))
	removed := false
	for _, l := range listeners {
		if lm, ok := l.(map[string]any); ok {
			if n, ok := lm["name"].(string); ok && drop[n] {
				removed = true
				continue
			}
		}
		kept = append(kept, l)
	}
	if !removed {
		return false, nil
	}
	if err := unstructured.SetNestedSlice(gw.Object, kept, "spec", "listeners"); err != nil {
		return false, err
	}
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Update(ctx, gw, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

// deleteAbsentOK runs a delete and treats NotFound as success (idempotent
// re-runs are the level-triggered contract).
func deleteAbsentOK(del func() error) error {
	if err := del(); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
