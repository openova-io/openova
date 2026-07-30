// org_console_tls.go — #4075. Per-Organization console TLS auto-provisioning.
//
// A customer can pick ANY of the Sovereign's role=org-pool parent domains
// (omani.homes / omani.rest / omani.trade / omani.works) as their free
// subdomain. The Org's Catalyst console then lives at the 2-label host
// `console.<slug>.<parent>` (deriveConsoleHost / org_enter_org.go) — that is
// where the customer (and the Enter-Org support-impersonation handover) lands
// to manage the Org and install Blueprints (bp-agenity, WordPress, …).
//
// THE GAP this file closes: that console host did NOT serve TLS on Org
// creation, because none of the three resources it needs were ever
// auto-provisioned:
//
//  1. A cert-manager Certificate covering `*.<slug>.<parent>` +
//     `<slug>.<parent>`. The Sovereign's per-zone wildcard
//     `*.<parent>` (sovereign-wildcard-certs.yaml) covers ONE label depth
//     (`<slug>.<parent>`) but NOT the 2-label `console.<slug>.<parent>` —
//     the documented remaining gap in sovereign-wildcard-certs.yaml:111-117.
//     A wildcard requires DNS-01, so the issuer is the powerdns DNS-01
//     ClusterIssuer (letsencrypt-dns01-prod-powerdns).
//  2. A listener pair on the dedicated `cilium-gateway-console` Gateway
//     (kube-system) for hostname `*.<slug>.<parent>`, binding the cert above.
//     The console gateway is the poison-proof gateway that carries ONLY the
//     stable catalyst-ui + catalyst-api backends (#4053), so per-Org consoles
//     ride it rather than the volatile shared gateway.
//  3. An HTTPRoute (catalyst-system) attaching `console.<slug>.<parent>` to
//     the SAME catalyst-ui + catalyst-api backends the Sovereign's own
//     `console.<fqdn>` route uses — a faithful clone of the canonical
//     catalyst-ui HTTPRoute, just re-hostnamed.
//
// A peer agent verified this exact resource trio live for ONE Org (demo on
// omani.homes) — Certificate `org-wildcard-tls-demo-omani-homes` (Ready),
// listeners `console-https-demo`/`console-http-demo` on cilium-gateway-console,
// and HTTPRoute `catalyst-ui-demo-omani-homes` → console.demo.omani.homes=200.
// This file CODIFIES that live hand-fix as the deterministic provisioning
// behaviour for EVERY Org on EVERY pool parent, durably.
//
// DURABILITY of the listener (the subtle part): `cilium-gateway-console`'s
// base listener set is rendered statically by the chart into ConfigMap key
// CONSOLE_LISTENERS_YAML and inlined by Flux postBuild — the kustomize-
// controller owns the `console-https`/`console-http` apex pair via server-
// side-apply. Gateway `spec.listeners` is a name-keyed list
// (x-kubernetes-list-map-keys: [name]), so server-side-apply MERGES by
// listener name and a manager only retains the keys it declares in its LATEST
// apply, pruning keys it previously owned but no longer declares. We exploit
// this with a PER-ORG field manager (catalyst-api-org-console-tls-<slug>): each
// Org's apply declares ONLY that Org's two listeners, so it is independent and
// additive — it never touches another Org's listeners (owned by a different
// per-Org manager) nor the apex pair (owned by kustomize-controller). The
// kustomize-controller's 5-min reconcile, which only declares the apex pair,
// likewise leaves every per-Org listener untouched. This is why the listeners
// are durable across Flux reconciles AND across many Orgs.
//
// NOTE — verified live on the omantel.biz Sovereign: a SHARED field manager is
// WRONG here. With one manager, a second Org's apply (declaring only its own
// listeners) pruned the first Org's listeners and 000'd its console. The
// per-Org manager fixes that — each Org owns only its own keys.
//
// Wiring: a best-effort, non-gating finalisation step (provisionOrgConsoleTLS)
// invoked at the STSTenantRegistered → STSDone transition right after
// createOrgOrganizationCR, mirroring that helper's idiom exactly — resolve the
// in-cluster dynamic client via sovereignDepsFor() (nil-tolerant for CI /
// out-of-cluster), build unstructured objects, apply idempotently, and log
// loud on failure without failing the Org pipeline (the substrate is already
// valid; a transient apiserver failure is retried by the next reconcile /
// HandleReconcileOrganization pass). BYO-domain Orgs are skipped — they carry
// their own per-host Certificate (orgTenantCertificate IsBYO branch) and a
// CNAME, not a pool-parent wildcard.
//
// #5511 — MULTI-REGION + READ-BACK. Two defects proven live on hw291:
//
//	A. The trio above was applied to the HOST region cluster only. On a
//	   multi-region Sovereign one shared EIP round-robins every region's
//	   cilium-envoy, so a region that never received the listener pair, the
//	   cert secret, or the console HTTPRoute TLS-resets ~1/N of all
//	   connections to EVERY per-Org console — the per-region split-write
//	   class (#5394/#5406/#5414/#5416), now for the whole per-Org gateway
//	   surface. Fix: fan the listener pair + the console HTTPRoute out to
//	   every secondary region cluster registered in h.k8sCache (the same
//	   per-region client seam the D20 jobs fan-out and the /cloud page use),
//	   and mirror the ISSUED cert secret from the host region (issuing a
//	   second identical LE cert per region would burn the duplicate-cert
//	   rate limit; the mirror follows the proven live heal). The keycloak /
//	   oidc-gate per-app HTTPRoutes are deliberately NOT fanned out — their
//	   backends are region-local singletons and mirroring them trades a TLS
//	   reset for a 503; that routing decision belongs to the placement model.
//	B. A successful SSA apply was never read back. cilium-operator can miss
//	   the listener append (observed live: gateway generation=2,
//	   observedGeneration=1 for 8h after an operator restart) and the apply
//	   path only logged FAILURES — success-without-admission was invisible.
//	   Fix: after the applies, poll Gateway.status.listeners (bounded by
//	   orgConsoleListenerAdmitBudget) until BOTH per-Org listener names are
//	   PRESENT (vacuity-proof: presence is asserted, never absence of an
//	   error), and log a loud error naming the gateway, the listener names,
//	   and generation vs observedGeneration when a region never admits them.

package handler

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgConsoleTLSFieldManagerPrefix builds the PER-ORG server-side-apply field
// manager for that Org's console Gateway listeners — orgConsoleTLSFieldManager(slug)
// returns "catalyst-api-org-console-tls-<slug>".
//
// CRITICAL: the field manager MUST be unique PER ORG, not a single shared
// manager. SSA's name-keyed-list merge means a manager only retains the keys it
// declares in its LATEST apply and PRUNES the keys it previously owned but no
// longer declares. With a single shared manager, Org B's apply (which declares
// only B's two listeners) would prune Org A's listeners — verified live on the
// omantel.biz Sovereign: a second Org's apply under a shared manager dropped
// the first Org's listeners and 000'd its console. A per-Org manager owns ONLY
// that Org's two listeners, so each Org's apply is independent and additive;
// SSA's per-key ownership keeps every Org's listeners (and the apex pair owned
// by kustomize-controller) intact across reconciles.
const orgConsoleTLSFieldManagerPrefix = "catalyst-api-org-console-tls-"

func orgConsoleTLSFieldManager(slug string) string {
	return orgConsoleTLSFieldManagerPrefix + slug
}

// orgConsoleTLSIssuer is the DNS-01 ClusterIssuer that issues the per-Org
// wildcard. A wildcard SAN (`*.<slug>.<parent>`) can only be validated via
// DNS-01, so this is the powerdns DNS-01 issuer shipped by
// bp-cert-manager-powerdns-webhook — the same issuer
// products/catalyst/chart/templates/sovereign-wildcard-certs.yaml uses for the
// per-zone `*.<parent>` wildcards. Per Inviolable Principle #4 the name is the
// canonical default and matches the chart's wildcardCert.issuerName default.
const orgConsoleTLSIssuer = "letsencrypt-dns01-prod-powerdns"

// console gateway + catalyst console backend placement. These match the
// canonical Sovereign install: the dedicated console Gateway lives as
// `cilium-gateway-console` in kube-system (#4053,
// clusters/_template/sovereign-tls/cilium-gateway-console.yaml) and the
// catalyst-ui + catalyst-api Services live in catalyst-system.
//
// #4732(2): the per-Org listener PORTS are no longer hardcoded — they are
// read from the console gateway's own apex `console-https`/`console-http`
// listeners at apply time (consoleApexListenerPorts). The console ELB
// forwards 443/80 to exactly those node ports (#4718 hostNetwork scheme:
// 8443/8080), so a per-Org listener on any OTHER port receives no traffic
// — the prior 31443/31080 constants were the pre-#4718 scheme and left
// every per-Org console dead (the nstar failure). SNI differentiates the
// per-Org wildcard hosts from the apex host on the shared port pair. The
// constants below remain only as the fallback when the apex pair cannot
// be read.
const (
	consoleGatewayName               = "cilium-gateway-console"
	consoleGatewayNamespace          = "kube-system"
	consoleCertNamespace             = "kube-system"
	catalystConsoleNamespace         = "catalyst-system"
	consoleApexListenerHTTPSName     = "console-https"
	consoleApexListenerHTTPName      = "console-http"
	consoleListenerHTTPSPortFallback = int64(8443)
	consoleListenerHTTPPortFallback  = int64(8080)
	catalystUIServiceName            = "catalyst-ui"
	catalystUIServicePort            = int64(80)
	catalystAPIServiceName           = "catalyst-api"
	catalystAPIServicePort           = int64(8080)
)

// consoleGatewayGVR — Gateway API v1 Gateway (for the per-Org listener SSA
// patch). The Certificate + HTTPRoute reuse the package-level certificateGVR
// (phase1_watch.go) + httpRouteGVR (sovereign.go) so the GVRs stay aligned
// with the rest of the handler suite.
var consoleGatewayGVR = schema.GroupVersionResource{
	Group:    "gateway.networking.k8s.io",
	Version:  "v1",
	Resource: "gateways",
}

// orgConsoleTLSNames bundles the deterministic resource names + hosts for an
// Org's console TLS trio. Centralised so the cert secretName, the listener
// certificateRef, and the test all agree byte-for-byte.
type orgConsoleTLSNames struct {
	Slug         string // RFC-1123 org subdomain, lowercased
	ParentDomain string // chosen org-pool parent (omani.homes/rest/trade/works)
	WildcardHost string // *.<slug>.<parent>  — listener hostname + cert SAN
	OrgZone      string //  <slug>.<parent>   — cert CN + SAN
	ConsoleHost  string // console.<slug>.<parent> — HTTPRoute hostname
	CertName     string // org-wildcard-tls-<slug>-<parent-dashed> (== secretName)
	HTTPSName    string // console-https-<slug> — listener name
	HTTPName     string // console-http-<slug>  — listener name
	RouteName    string // catalyst-ui-<slug>-<parent-dashed>
}

// resolveOrgConsoleTLSNames computes the deterministic names/hosts from a
// provision record. Returns (names, true) for a free-subdomain Org with a
// valid slug + parent, or (zero, false) when the trio should NOT be
// provisioned (BYO mode, blank slug/parent, or invalid slug).
func resolveOrgConsoleTLSNames(rec store.OrganizationProvisionRecord) (orgConsoleTLSNames, bool) {
	if rec.DomainMode == store.OrganizationDomainBYO {
		// BYO Orgs bring their own host + per-host Certificate
		// (orgTenantCertificate IsBYO branch) and a CNAME — no pool-parent
		// wildcard listener applies.
		return orgConsoleTLSNames{}, false
	}
	slug := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	if !orgSlugRE.MatchString(slug) {
		return orgConsoleTLSNames{}, false
	}
	parent := strings.ToLower(strings.TrimSpace(rec.ParentDomain))
	if parent == "" {
		// Single-domain back-compat: the Org rides the Sovereign FQDN apex,
		// whose own per-prov wildcard already covers console.<slug>.<fqdn>
		// via the apex console-https listener. No per-Org resource needed.
		return orgConsoleTLSNames{}, false
	}
	parentDashed := strings.ReplaceAll(parent, ".", "-")
	return orgConsoleTLSNames{
		Slug:         slug,
		ParentDomain: parent,
		WildcardHost: "*." + slug + "." + parent,
		OrgZone:      slug + "." + parent,
		ConsoleHost:  "console." + slug + "." + parent,
		CertName:     "org-wildcard-tls-" + slug + "-" + parentDashed,
		HTTPSName:    "console-https-" + slug,
		HTTPName:     "console-http-" + slug,
		RouteName:    "catalyst-ui-" + slug + "-" + parentDashed,
	}, true
}

// orgConsoleListenerAdmitBudget bounds the #5511 read-back verification: how
// long provisionOrgConsoleTLS waits, TOTAL across all regions, for every
// region's cilium-gateway-console to ADMIT the per-Org listener pair into
// status.listeners after the SSA apply. In the healthy case cilium-operator
// admits within a second or two, so the poll returns almost immediately; the
// budget is only consumed when a region's operator has missed the append (the
// hw291 defect-A shape), which is exactly when the loud error must fire.
// Package vars (not consts) so tests shrink them.
var (
	orgConsoleListenerAdmitBudget       = 60 * time.Second
	orgConsoleListenerAdmitPollInterval = 3 * time.Second
)

// orgConsoleTLSHostRegion labels the host-region (in-cluster) target in logs.
const orgConsoleTLSHostRegion = "host"

// orgConsoleTLSTarget is one region cluster the per-Org console gateway
// surface must be written to: the host region (via sovereignDepsFor) plus, on
// a multi-region Sovereign, every secondary region registered in h.k8sCache.
type orgConsoleTLSTarget struct {
	region    string // orgConsoleTLSHostRegion, or the secondary's region key
	clusterID string // k8sCache cluster id ("" for the host target)
	dyn       dynamic.Interface
	core      kubernetes.Interface
}

// orgConsoleTLSSecondaryRegions — pure helper: given the full k8sCache
// cluster-id set of a CHROOT (one primary + this Sovereign's secondaries, per
// the deploymentScopedClusterIDs invariant), map each SECONDARY cluster id to
// its region key.
//
// Secondary ids follow `<primaryID>-<regionKey>` (HandleSovereignSecondary-
// Kubeconfig). The primary id is derived from the set itself rather than a
// *Deployment record (the Org funnel has none in hand): an id is a candidate
// primary iff NO other id in the set is a prefix of it; every non-candidate
// derives its region by stripping its LONGEST candidate-primary prefix
// (longest so region keys that themselves contain hyphens — "nbg1-1",
// "ap-southeast-3" — split on the deployment boundary, mirroring
// regionFromSecondaryClusterID's contract). A set with no prefix relations
// (e.g. a mothership cache holding sibling deployments, or a chroot whose
// self-registered alias never matched its secondaries) yields the empty map —
// the safe no-op that preserves the pre-#5511 host-only behaviour.
func orgConsoleTLSSecondaryRegions(clusterIDs []string) map[string]string {
	isSecondary := func(id string) bool {
		for _, p := range clusterIDs {
			if p != id && strings.HasPrefix(id, p+"-") {
				return true
			}
		}
		return false
	}
	out := map[string]string{}
	for _, id := range clusterIDs {
		best := ""
		for _, p := range clusterIDs {
			if p == id || isSecondary(p) {
				continue // only candidate primaries may donate a prefix
			}
			if strings.HasPrefix(id, p+"-") && len(p) > len(best) {
				best = p
			}
		}
		if best != "" {
			out[id] = strings.TrimPrefix(id, best+"-")
		}
	}
	return out
}

// orgConsoleTLSTargets resolves every region cluster the per-Org console
// gateway surface must be written to. The host region always leads; secondary
// regions are appended ONLY on a chroot (SOVEREIGN_FQDN set) — on the
// mothership h.k8sCache holds EVERY managed Sovereign's clusters (#3987) and
// a blind fan-out would write per-Org listeners onto alien deployments.
// This is the same per-region client seam the D20 jobs fan-out
// (chrootSeedSecondaryRegions) and the /cloud page use — no new plumbing.
func (h *Handler) orgConsoleTLSTargets(deps *sovereignDeps) []orgConsoleTLSTarget {
	targets := []orgConsoleTLSTarget{{region: orgConsoleTLSHostRegion, dyn: deps.dyn, core: deps.core}}
	if h.k8sCache == nil || !isChroot() {
		return targets
	}
	secondaries := orgConsoleTLSSecondaryRegions(h.k8sCache.Clusters())
	cids := make([]string, 0, len(secondaries))
	for cid := range secondaries {
		cids = append(cids, cid)
	}
	sort.Strings(cids)
	for _, cid := range cids {
		region := secondaries[cid]
		dyn, err := h.k8sCache.DynamicClientFor(cid)
		if err != nil || dyn == nil {
			h.log.Warn("org-console-tls: secondary region dynamic client unavailable — its per-Org gateway surface is NOT written this pass; the next reconcile retries (#5511)",
				"clusterID", cid, "region", region, "err", err)
			continue
		}
		targets = append(targets, orgConsoleTLSTarget{
			region:    region,
			clusterID: cid,
			dyn:       dyn,
			core:      h.k8sCache.CoreClient(cid),
		})
	}
	return targets
}

// provisionOrgConsoleTLS is the best-effort, non-gating finalisation step that
// makes the Org's console host (`console.<slug>.<parent>`) serve TLS. Mirrors
// createOrgOrganizationCR's idiom: resolve the in-cluster dynamic client via
// sovereignDepsFor() (nil-tolerant for CI / out-of-cluster), apply the
// resources idempotently, and log loud on failure WITHOUT failing the Org
// pipeline. Each sub-step is independent: a failure on one is logged and the
// rest still run, and the org-controller / next HandleReconcileOrganization
// pass retries the whole thing.
//
// #5511: the listener pair + console HTTPRoute are applied to EVERY region
// cluster (host + registered secondaries); the cert-manager Certificate is
// created on the host region only and its issued secret MIRRORED to the
// secondaries; after the applies, every region's Gateway status.listeners is
// read back until both per-Org listener names are admitted — see the file
// header for the two live defects this closes.
func (h *Handler) provisionOrgConsoleTLS(ctx context.Context, rec store.OrganizationProvisionRecord) {
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok {
		h.log.Info("org-console-tls: skipped — not a free-subdomain pool-parent Org",
			"org_tenant_id", rec.OrganizationID,
			"subdomain", rec.Subdomain,
			"domain_mode", rec.DomainMode,
			"parent_domain", rec.ParentDomain,
		)
		return
	}

	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: the Org pipeline still completes; the console
		// TLS trio is simply not applied (no apiserver to POST to). On a real
		// Sovereign this branch never fires.
		h.log.Info("org-console-tls: skipped — no in-cluster dynamic client",
			"org_tenant_id", rec.OrganizationID, "err", err)
		return
	}

	targets := h.orgConsoleTLSTargets(deps)
	regions := make([]string, 0, len(targets))
	for _, tgt := range targets {
		regions = append(regions, tgt.region)
		if tgt.region == orgConsoleTLSHostRegion {
			if err := ensureOrgConsoleCertificate(ctx, tgt.dyn, names, rec); err != nil {
				h.log.Error("org-console-tls: Certificate apply failed — reconcile will retry",
					"region", tgt.region, "cert", names.CertName, "host", names.WildcardHost, "err", err)
			}
		} else if err := mirrorOrgConsoleCertSecret(ctx, deps.core, tgt.core, names, rec); err != nil {
			// Includes the benign "host cert not issued yet" case right after
			// Org creation — the next reconcile pass mirrors it. Until then the
			// secondary's listeners sit ResolvedRefs=False but PRESENT in
			// status, so the read-back below still verifies admission.
			h.log.Error("org-console-tls: cert-secret mirror to secondary region failed — reconcile will retry (#5511)",
				"region", tgt.region, "clusterID", tgt.clusterID, "secret", names.CertName, "err", err)
		}
		if err := ensureOrgConsoleListener(ctx, tgt.dyn, names, rec); err != nil {
			h.log.Error("org-console-tls: Gateway listener apply failed — reconcile will retry",
				"region", tgt.region, "gateway", consoleGatewayName, "https_listener", names.HTTPSName, "err", err)
		}
		if err := ensureOrgConsoleHTTPRoute(ctx, tgt.dyn, names, rec); err != nil {
			h.log.Error("org-console-tls: HTTPRoute apply failed — reconcile will retry",
				"region", tgt.region, "route", names.RouteName, "host", names.ConsoleHost, "err", err)
		}
	}

	// #5511 defect B — read-back verification. An SSA apply that returned nil
	// is NOT admission: cilium-operator can miss the append (observed live —
	// generation=2 / observedGeneration=1 for 8h) and nothing above would
	// notice. Poll each region's Gateway until BOTH per-Org listener names are
	// PRESENT in status.listeners; one deadline bounds the whole pass.
	admitDeadline := time.Now().Add(orgConsoleListenerAdmitBudget)
	admitted := true
	for _, tgt := range targets {
		if err := waitOrgConsoleListenersAdmitted(ctx, tgt.dyn, names, admitDeadline); err != nil {
			admitted = false
			h.log.Error("org-console-tls: Gateway did NOT admit the per-Org listener pair — the per-Org console door is CLOSED in this region (#5511)",
				"org_tenant_id", rec.OrganizationID,
				"region", tgt.region,
				"clusterID", tgt.clusterID,
				"gateway", consoleGatewayNamespace+"/"+consoleGatewayName,
				"https_listener", names.HTTPSName,
				"http_listener", names.HTTPName,
				"err", err,
			)
		}
	}
	if !admitted {
		// NOT ready — the loud per-region errors above are the durable
		// evidence trail for the next env walk. Never emit the "ensured"
		// line on an unadmitted surface.
		h.log.Error("org-console-tls: per-Org console TLS surface applied but NOT admitted in every region — NOT ready (#5511)",
			"org_tenant_id", rec.OrganizationID,
			"console_host", names.ConsoleHost,
			"regions", strings.Join(regions, ","),
		)
		return
	}
	h.log.Info("org-console-tls: ensured per-Org console TLS trio — listener pair admitted in every region",
		"org_tenant_id", rec.OrganizationID,
		"console_host", names.ConsoleHost,
		"cert", names.CertName,
		"https_listener", names.HTTPSName,
		"route", names.RouteName,
		"regions", strings.Join(regions, ","),
	)
}

// waitOrgConsoleListenersAdmitted polls the console Gateway on ONE region
// cluster until BOTH per-Org listener names are PRESENT in status.listeners,
// the shared deadline passes, or ctx is cancelled. The assertion is presence
// (vacuity-proof — an empty or apex-only status keeps failing), never the
// absence of an apply error. The timeout error names the gateway, both
// listener names, the observed status.listeners set, and generation vs
// observedGeneration so a "cilium-operator missed the append" wedge (#5511
// defect A: gen=2/obsGen=1) is diagnosable straight from the log line.
func waitOrgConsoleListenersAdmitted(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, deadline time.Time) error {
	var (
		lastNames []string
		lastGen   = int64(-1)
		lastObs   = int64(-1)
		lastErr   error
	)
	for {
		gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
			Get(ctx, consoleGatewayName, metav1.GetOptions{})
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			lastGen = gw.GetGeneration()
			lastObs = gatewayMaxObservedGeneration(gw)
			lastNames = gatewayStatusListenerNames(gw)
			if statusListenersContain(lastNames, names.HTTPSName) &&
				statusListenersContain(lastNames, names.HTTPName) {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for gateway %s/%s to admit listeners %s + %s: %w",
				consoleGatewayNamespace, consoleGatewayName, names.HTTPSName, names.HTTPName, ctx.Err())
		case <-time.After(orgConsoleListenerAdmitPollInterval):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("gateway %s/%s unreadable while verifying admission of listeners %s + %s: %w",
			consoleGatewayNamespace, consoleGatewayName, names.HTTPSName, names.HTTPName, lastErr)
	}
	return fmt.Errorf("gateway %s/%s did not admit listeners %s + %s within %s: status.listeners=%v generation=%d observedGeneration=%d",
		consoleGatewayNamespace, consoleGatewayName, names.HTTPSName, names.HTTPName,
		orgConsoleListenerAdmitBudget, lastNames, lastGen, lastObs)
}

// gatewayStatusListenerNames extracts the listener NAMES present in the
// Gateway's status.listeners — the admission ground truth (#5511): a listener
// accepted into spec but absent here is a closed door with no condition.
func gatewayStatusListenerNames(gw *unstructured.Unstructured) []string {
	out := []string{}
	listeners, found, err := unstructured.NestedSlice(gw.Object, "status", "listeners")
	if err != nil || !found {
		return out
	}
	for _, l := range listeners {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(lm, "name"); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// gatewayMaxObservedGeneration returns the highest observedGeneration across
// the Gateway's top-level status.conditions, or -1 when unreadable. Gateway
// has no single status.observedGeneration field; the per-condition value is
// what exposed the hw291 wedge (generation=2, every condition's
// observedGeneration=1).
func gatewayMaxObservedGeneration(gw *unstructured.Unstructured) int64 {
	maxObs := int64(-1)
	conds, found, err := unstructured.NestedSlice(gw.Object, "status", "conditions")
	if err != nil || !found {
		return maxObs
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if og, okOG, _ := unstructured.NestedInt64(cm, "observedGeneration"); okOG && og > maxObs {
			maxObs = og
		}
	}
	return maxObs
}

func statusListenersContain(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// mirrorOrgConsoleCertSecret copies the HOST region's issued per-Org wildcard
// cert secret (kube-system/<CertName>, written by cert-manager for the
// Certificate ensureOrgConsoleCertificate creates) onto a SECONDARY region's
// kube-system, where the mirrored listener pair references it by the same
// name (#5511 defect B live heal, codified). The secret is MIRRORED rather
// than re-issued: a second Certificate CR per region would double the
// Let's Encrypt duplicate-certificate burn for identical SANs on every Org.
// Create-or-update: a later pass (Org reconcile) refreshes a drifted copy, so
// a host-side renewal propagates on the next reconcile.
func mirrorOrgConsoleCertSecret(ctx context.Context, hostCore, regionCore kubernetes.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord) error {
	if hostCore == nil || regionCore == nil {
		return fmt.Errorf("core client unavailable (host=%t region=%t)", hostCore != nil, regionCore != nil)
	}
	src, err := hostCore.CoreV1().Secrets(consoleCertNamespace).Get(ctx, names.CertName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("host-region cert secret %s/%s not issued yet — the next reconcile pass mirrors it", consoleCertNamespace, names.CertName)
		}
		return fmt.Errorf("read host-region cert secret %s/%s: %w", consoleCertNamespace, names.CertName, err)
	}
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.CertName,
			Namespace: consoleCertNamespace,
			Labels:    orgConsoleTLSStringLabels(names, rec, consoleGatewayName),
		},
		Type: src.Type,
		Data: src.Data,
	}
	if _, err := regionCore.CoreV1().Secrets(consoleCertNamespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create mirrored cert secret %s/%s: %w", consoleCertNamespace, names.CertName, err)
		}
		existing, gerr := regionCore.CoreV1().Secrets(consoleCertNamespace).Get(ctx, names.CertName, metav1.GetOptions{})
		if gerr != nil {
			return fmt.Errorf("read existing mirrored cert secret %s/%s: %w", consoleCertNamespace, names.CertName, gerr)
		}
		if reflect.DeepEqual(existing.Data, src.Data) {
			return nil // converged — renewal-drift check is a true no-op
		}
		existing.Data = src.Data
		if _, uerr := regionCore.CoreV1().Secrets(consoleCertNamespace).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update drifted mirrored cert secret %s/%s: %w", consoleCertNamespace, names.CertName, uerr)
		}
	}
	return nil
}

// orgConsoleTLSStringLabels — orgConsoleTLSLabels as map[string]string for
// typed objects (the mirrored Secret).
func orgConsoleTLSStringLabels(names orgConsoleTLSNames, rec store.OrganizationProvisionRecord, component string) map[string]string {
	src := orgConsoleTLSLabels(names, rec, component)
	out := make(map[string]string, len(src))
	for k, v := range src {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// orgConsoleTLSLabels is the common label set stamped on all three resources
// so an operator (and teardown) can find every per-Org console TLS object by
// org-subdomain + pool-parent. Mirrors the labels the live demo Org carried.
func orgConsoleTLSLabels(names orgConsoleTLSNames, rec store.OrganizationProvisionRecord, component string) map[string]any {
	return map[string]any{
		"catalyst.openova.io/component":     component,
		"catalyst.openova.io/managed-by":    "catalyst-api",
		"catalyst.openova.io/org-subdomain": names.Slug,
		"catalyst.openova.io/pool-parent":   names.ParentDomain,
		"catalyst.openova.io/sovereign":     strings.TrimSpace(rec.OTECHFQDN),
		"app.kubernetes.io/managed-by":      "catalyst-api",
	}
}

// ensureOrgConsoleCertificate Creates the per-Org wildcard Certificate
// (idempotent — AlreadyExists = success). CN/SAN cover `*.<slug>.<parent>` +
// `<slug>.<parent>` (the latter so the org-zone apex resolves too), issued via
// the DNS-01 ClusterIssuer. secretName == the cert name so the listener's
// certificateRef can reference it directly.
func ensureOrgConsoleCertificate(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord) error {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]any{
				"name":      names.CertName,
				"namespace": consoleCertNamespace,
				"labels":    orgConsoleTLSLabels(names, rec, consoleGatewayName),
			},
			"spec": map[string]any{
				"secretName": names.CertName,
				"commonName": names.OrgZone,
				"dnsNames": []any{
					names.WildcardHost,
					names.OrgZone,
				},
				"issuerRef": map[string]any{
					"name": orgConsoleTLSIssuer,
					"kind": "ClusterIssuer",
				},
			},
		},
	}
	if _, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create Certificate %s/%s: %w", consoleCertNamespace, names.CertName, err)
	}
	return nil
}

// consoleApexListenerPorts reads the console gateway's live apex
// `console-https`/`console-http` listeners and returns their ports —
// the ONLY ports the console ELB actually forwards to (#4732 item 2).
// Falls back to the #4718 canonical 8443/8080 pair when the gateway or
// the apex listeners cannot be read (fresh install race, RBAC), so the
// apply still lands on the current canonical scheme rather than failing.
func consoleApexListenerPorts(ctx context.Context, dyn dynamic.Interface) (httpsPort, httpPort int64) {
	httpsPort = consoleListenerHTTPSPortFallback
	httpPort = consoleListenerHTTPPortFallback
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(ctx, consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		return httpsPort, httpPort
	}
	listeners, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil || !found {
		return httpsPort, httpPort
	}
	for _, l := range listeners {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(lm, "name")
		port, foundPort, _ := unstructured.NestedInt64(lm, "port")
		if !foundPort {
			continue
		}
		switch name {
		case consoleApexListenerHTTPSName:
			httpsPort = port
		case consoleApexListenerHTTPName:
			httpPort = port
		}
	}
	return httpsPort, httpPort
}

// ensureOrgConsoleListener server-side-applies the per-Org HTTPS + HTTP
// listener pair onto cilium-gateway-console. SSA with our dedicated field
// manager merges by listener name (the Gateway listeners list is keyed on
// `name`), so the chart-driven kustomize-controller reconcile of the apex
// pair never prunes these — durable across Flux reconciles (see file header).
//
// Force=true: SSA returns a conflict if another manager already owns a field
// we declare (e.g. a prior live hand-fix attributed to kustomize-controller);
// force claims ownership for our manager so the codified path heals the
// hand-fix in place rather than erroring forever.
func ensureOrgConsoleListener(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord) error {
	// #4732(2): ride the SAME ports as the apex pair — SNI separates the
	// per-Org wildcard from the apex host. Any other port pair is dead
	// (the ELB only forwards to the apex ports).
	httpsPort, httpPort := consoleApexListenerPorts(ctx, dyn)
	// Apply object carries ONLY the two per-Org listeners. SSA's name-keyed
	// merge adds them to the existing listener set without touching the apex
	// console-https/console-http pair owned by kustomize-controller.
	apply := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name":      consoleGatewayName,
				"namespace": consoleGatewayNamespace,
			},
			"spec": map[string]any{
				"listeners": []any{
					map[string]any{
						"name":     names.HTTPSName,
						"port":     httpsPort,
						"protocol": "HTTPS",
						"hostname": names.WildcardHost,
						"tls": map[string]any{
							"mode": "Terminate",
							"certificateRefs": []any{
								map[string]any{"kind": "Secret", "name": names.CertName},
							},
						},
						"allowedRoutes": map[string]any{
							"namespaces": map[string]any{"from": "All"},
						},
					},
					map[string]any{
						"name":     names.HTTPName,
						"port":     httpPort,
						"protocol": "HTTP",
						"hostname": names.WildcardHost,
						"allowedRoutes": map[string]any{
							"namespaces": map[string]any{"from": "All"},
						},
					},
				},
			},
		},
	}
	data, err := apply.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal Gateway listener patch: %w", err)
	}
	force := true
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Patch(ctx, consoleGatewayName, types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: orgConsoleTLSFieldManager(names.Slug),
			Force:        &force,
		}); err != nil {
		return fmt.Errorf("apply Gateway listeners on %s/%s: %w", consoleGatewayNamespace, consoleGatewayName, err)
	}
	return nil
}

// ensureOrgConsoleHTTPRoute Creates the per-Org console HTTPRoute (idempotent
// — AlreadyExists = success). A faithful clone of the canonical catalyst-ui
// HTTPRoute (catalyst-api for /readyz, /healthz, /auth/handover, /api/,
// /catalyst/; catalyst-ui for /), re-hostnamed to console.<slug>.<parent> and
// parented on cilium-gateway-console. Lands in catalyst-system alongside the
// catalyst-ui/catalyst-api Services it references (no ReferenceGrant needed).
func ensureOrgConsoleHTTPRoute(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord) error {
	apiBackend := func() []any {
		return []any{map[string]any{
			"group": "", "kind": "Service",
			"name": catalystAPIServiceName, "port": catalystAPIServicePort, "weight": int64(1),
		}}
	}
	rule := func(backend []any, matches ...map[string]any) map[string]any {
		ms := make([]any, 0, len(matches))
		for _, m := range matches {
			ms = append(ms, m)
		}
		return map[string]any{"backendRefs": backend, "matches": ms}
	}
	exact := func(p string) map[string]any {
		return map[string]any{"path": map[string]any{"type": "Exact", "value": p}}
	}
	prefix := func(p string) map[string]any {
		return map[string]any{"path": map[string]any{"type": "PathPrefix", "value": p}}
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata": map[string]any{
				"name":      names.RouteName,
				"namespace": catalystConsoleNamespace,
				"labels":    orgConsoleTLSLabels(names, rec, catalystUIServiceName),
			},
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{
						"group":     "gateway.networking.k8s.io",
						"kind":      "Gateway",
						"name":      consoleGatewayName,
						"namespace": consoleGatewayNamespace,
					},
				},
				"hostnames": []any{names.ConsoleHost},
				"rules": []any{
					rule(apiBackend(), exact("/readyz"), exact("/healthz")),
					rule(apiBackend(), exact("/auth/handover")),
					rule(apiBackend(), prefix("/api/")),
					rule(apiBackend(), prefix("/catalyst/")),
					rule([]any{map[string]any{
						"group": "", "kind": "Service",
						"name": catalystUIServiceName, "port": catalystUIServicePort, "weight": int64(1),
					}}, prefix("/")),
				},
			},
		},
	}
	if _, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create HTTPRoute %s/%s: %w", catalystConsoleNamespace, names.RouteName, err)
	}
	return nil
}
