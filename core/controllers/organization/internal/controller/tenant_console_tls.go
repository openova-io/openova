// tenant_console_tls.go — per-Org console-TLS reconciler (issues #4075, #4241).
//
// Problem (#4075/#4241, live on the permanent omantel.biz Sovereign, dep
// 4635277cae4ffed9): a customer Organization that picks a free-subdomain
// hostname under a pool parent zone — e.g. `console.demo.omani.works`
// under the `omani.works` org-pool parent — resolves in DNS to the
// console LoadBalancer (212.72.24.33) and routes over HTTP, but the
// HTTPS handshake fails the cert SAN match:
//
//	curl: (60) SSL: no alternative certificate subject name matches
//	target host name 'console.demo.omani.works'
//
// Root cause (#4241): the bootstrap-rendered console Cilium Gateway
// (clusters/_template/sovereign-tls/cilium-gateway-console.yaml) carries
// the apex `*.<sovFQDN>` listener, and an earlier version of THIS
// reconciler additionally appended a SHARED-pool `*.<parentDomain>`
// (1-label) listener + cert. Neither covers the 2-label host
// `console.<slug>.<parentDomain>`: Gateway-API hostname wildcards (and
// Let's-Encrypt wildcard SANs) match exactly ONE label, so
// `*.omani.works` can serve `f4179done.omani.works` but never
// `console.f4179done.omani.works`. Every pool Org — including the BSS
// `f4179walk` control — gets a SAN mismatch on its signed-in HTTPS
// landing.
//
// The HTTPRoute alone (tenant_route.go) is necessary but NOT sufficient:
// without a SAN-matching cert + a matching listener, the request fails
// the TLS handshake before any route is selected. This reconciler
// supplies the two missing pieces, idempotently and additively,
// whenever an Org sets `spec.tenantPublic.parentDomain` — issuing the
// SAME per-Org cert/listener shape the catalyst-api BSS door already
// emits (org_console_tls.go), so the funnel + BSS doors converge on
// byte-identical resource names:
//
//  1. A cert-manager Certificate `org-wildcard-tls-<slug>-<parent-dashed>`
//     in the console Gateway's namespace, with dnsNames
//     `*.<slug>.<parentDomain>` + `<slug>.<parentDomain>`, issued by the
//     DNS-01 ClusterIssuer (a wildcard SAN can ONLY be solved via
//     DNS-01; the per-Org `*.<slug>` zone is solvable because #4236's
//     org-controller already writes the `<slug>` A-record into the
//     central pool zone). The 2-label `*.<slug>.<parent>` SAN is what
//     covers `console.<slug>.<parent>` — the apex pool wildcard cannot.
//
//  2. A listener pair on the console Gateway — `console-https-<slug>`
//     (HTTPS, terminates on the Secret above) and `console-http-<slug>`
//     (HTTP) — hostnamed `*.<slug>.<parentDomain>`, appended to the
//     EXISTING listeners array WITHOUT touching the apex `*.<sovFQDN>`
//     listeners or any other Org's listeners. The append is idempotent:
//     a listener whose name already exists is left untouched (no
//     duplicate, no spec churn).
//
// Why per-Org (not one shared pool cert): the SAN must descend a label
// per Org, so each Org needs its own cert + listener. The Let's-Encrypt
// issuance count grows one cert per Org rather than one per pool zone —
// acceptable for the funnel volume; if LE-throttled, the operator swaps
// the pool TLD (omani.works ↔ omantel.biz) or points the issuer at the
// staging ClusterIssuer for a demo (env-overridable, Principle #4).
//
// Why unstructured (not the cert-manager / gateway-api Go types): the
// whole organization-controller already manipulates the Gateway-API
// HTTPRoute as an Unstructured (tenant_route.go) precisely to avoid
// pulling those CRD type modules in for a handful of resources. We keep
// that discipline here — Certificate and Gateway are both written
// through Unstructured.
//
// Per docs/PRINCIPLES.md #4 every operationally-meaningful value (Gateway
// name/namespace, ClusterIssuer, cert namespace, host ports) flows
// through the Reconciler's env-configured fields — no hardcoded names in
// the renderer.

package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// certificateGVK identifies the cert-manager Certificate v1 resource the
// reconciler issues for the per-pool wildcard. cert-manager is installed
// on every Sovereign (bp-cert-manager + bp-cert-manager-powerdns-webhook)
// so this GVK always resolves at runtime.
var certificateGVK = schema.GroupVersionKind{
	Group:   "cert-manager.io",
	Version: "v1",
	Kind:    "Certificate",
}

// gatewayGVK identifies the Gateway-API Gateway v1 resource the console
// listener is appended to. Same group the HTTPRoute reconciler already
// writes against.
var gatewayGVK = schema.GroupVersionKind{
	Group:   "gateway.networking.k8s.io",
	Version: "v1",
	Kind:    "Gateway",
}

// Console-TLS defaults (overridable via the Reconciler's env-configured
// fields). They match the canonical console Gateway placement
// (clusters/_template/sovereign-tls/cilium-gateway-console.yaml) and the
// DNS-01 ClusterIssuer every Sovereign installs.
const (
	consoleGatewayDefaultName      = "cilium-gateway-console"
	consoleGatewayDefaultNamespace = "kube-system"
	consoleTLSDefaultClusterIssuer = "letsencrypt-dns01-prod-powerdns"
	consoleTLSDefaultCertNamespace = "kube-system"

	// Per-Org console HTTPRoute backend defaults (#4186). The route + these
	// Services live in catalyst-system so backendRefs resolve same-namespace.
	consoleRouteDefaultNamespace = "catalyst-system"
	consoleAPIDefaultService     = "catalyst-api"
	consoleAPIDefaultPort        = int32(8080)
	consoleUIDefaultService      = "catalyst-ui"
	consoleUIDefaultPort         = int32(80)

	// Apex console listener names on the shared console Gateway. The per-Org
	// listeners derive their PORTS from these at apply time — see
	// consoleApexListenerPorts (#5511).
	consoleApexListenerHTTPSName = "console-https"
	consoleApexListenerHTTPName  = "console-http"

	// Fallback host ports, used ONLY when the apex pair cannot be read off
	// the live Gateway. These are the #4718 canonical hostNetwork scheme and
	// match the catalyst-api BSS-door emitter's fallbacks byte-for-byte
	// (org_console_tls.go consoleListener*PortFallback).
	//
	// 🛑 #5511 — these are a FALLBACK, never a hardcode. The per-Org listeners
	// MUST ride the SAME ports as the live apex `console-https`/`console-http`
	// pair, because the console ELB forwards public 443/80 to exactly those
	// ports and NOTHING else. A per-Org listener on any other port receives no
	// traffic at all: the customer door answers `000` on every probe while the
	// workload behind it is perfectly healthy. This reconciler previously
	// hardcoded the pre-#4718 pair 31443/31080, which is dead on every current
	// Sovereign — the catalyst-api twin was fixed for exactly this in #4732(2)
	// (commit 8663202bc) and this twin was left behind, so whichever door
	// provisioned a given Org decided whether its console worked. A Gateway
	// listener is selected by hostname+port, so multiple HTTPS listeners share
	// the apex port safely — SNI differentiates the per-Org wildcard hosts
	// from the apex hosts.
	consoleListenerHTTPSPortFallback = int64(8443)
	consoleListenerHTTPPortFallback  = int64(8080)
)

// listenerPort reads a Gateway listener's `port`, tolerating every numeric
// shape an unstructured round-trip can produce (int64 from apimachinery's JSON
// decode, float64 from a raw map, int from a hand-built test fixture).
func listenerPort(l map[string]any) (int64, bool) {
	switch v := l["port"].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	}
	return 0, false
}

// consoleApexListenerPorts reads the console Gateway's apex
// `console-https`/`console-http` listener ports out of the listener slice
// already fetched from the live Gateway — the ONLY ports the console
// LoadBalancer actually forwards to (#4732 item 2, #5511). Falls back to the
// #4718 canonical 8443/8080 pair when an apex listener is absent (fresh
// install race), so the append still lands on the current canonical scheme
// rather than resurrecting a dead one.
//
// Reading from the passed-in slice rather than issuing its own Get is
// deliberate: the caller has already read the Gateway, and deriving the port
// from the SAME read that the append is written back onto makes the port and
// the listener set consistent by construction.
func consoleApexListenerPorts(listeners []any) (httpsPort, httpPort int64) {
	httpsPort = consoleListenerHTTPSPortFallback
	httpPort = consoleListenerHTTPPortFallback
	for _, l := range listeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		port, ok := listenerPort(m)
		if !ok {
			continue
		}
		switch n, _ := m["name"].(string); n {
		case consoleApexListenerHTTPSName:
			httpsPort = port
		case consoleApexListenerHTTPName:
			httpPort = port
		}
	}
	return httpsPort, httpPort
}

// dnsDashed converts a DNS name to a Secret/Certificate-safe slug:
// `omani.homes` → `omani-homes`. Matches the apex wildcard naming
// convention `sovereign-wildcard-tls-<sovFQDN-dashed>`.
func dnsDashed(domain string) string {
	return strings.ReplaceAll(strings.TrimSpace(domain), ".", "-")
}

func (r *Reconciler) consoleGatewayName() string {
	if v := strings.TrimSpace(r.ConsoleGatewayName); v != "" {
		return v
	}
	return consoleGatewayDefaultName
}

func (r *Reconciler) consoleGatewayNamespace() string {
	if v := strings.TrimSpace(r.ConsoleGatewayNamespace); v != "" {
		return v
	}
	return consoleGatewayDefaultNamespace
}

func (r *Reconciler) consoleTLSClusterIssuer() string {
	if v := strings.TrimSpace(r.ConsoleTLSClusterIssuer); v != "" {
		return v
	}
	return consoleTLSDefaultClusterIssuer
}

func (r *Reconciler) consoleTLSCertNamespace() string {
	if v := strings.TrimSpace(r.ConsoleTLSCertNamespace); v != "" {
		return v
	}
	return consoleTLSDefaultCertNamespace
}

// consoleRouteNamespace is the namespace the per-Org console HTTPRoute is
// written to — MUST be where catalyst-api + catalyst-ui Services live so the
// route's backendRefs resolve same-namespace (no ReferenceGrant). Default:
// catalyst-system (#4186).
func (r *Reconciler) consoleRouteNamespace() string {
	if v := strings.TrimSpace(r.ConsoleRouteNamespace); v != "" {
		return v
	}
	return consoleRouteDefaultNamespace
}

// consoleAPIBackend returns the catalyst-api Service name + port for the
// per-Org console route's auth/api/catalyst rules (#4186).
func (r *Reconciler) consoleAPIBackend() (string, int32) {
	svc := strings.TrimSpace(r.ConsoleAPIService)
	if svc == "" {
		svc = consoleAPIDefaultService
	}
	port := r.ConsoleAPIPort
	if port == 0 {
		port = consoleAPIDefaultPort
	}
	return svc, port
}

// consoleUIBackend returns the catalyst-ui Service name + port for the
// per-Org console route's catch-all `/` rule (#4186).
func (r *Reconciler) consoleUIBackend() (string, int32) {
	svc := strings.TrimSpace(r.ConsoleUIService)
	if svc == "" {
		svc = consoleUIDefaultService
	}
	port := r.ConsoleUIPort
	if port == 0 {
		port = consoleUIDefaultPort
	}
	return svc, port
}

// orgConsoleTLSNames bundles the deterministic per-Org resource names +
// hosts the reconciler issues. Centralised so the cert secretName, the
// listener certificateRef, the listener hostname, and the tests all agree
// byte-for-byte — AND so they match the catalyst-api BSS-door emitter
// (org_console_tls.go::orgConsoleTLSNames) exactly, the #4241 single-
// source-of-truth consolidation: both doors render identically-named
// resources, so whichever pipeline runs for a given Org, the gateway edge
// sees one consistent cert/listener for `console.<slug>.<parent>`.
type orgConsoleTLSNames struct {
	Slug         string // org subdomain (tenantPublic.subdomain || spec.slug), lowercased
	ParentDomain string // chosen org-pool parent (omani.works/homes/rest/trade)
	WildcardHost string // *.<slug>.<parent>  — listener hostname + cert SAN (2-label)
	OrgZone      string //  <slug>.<parent>   — cert CN + SAN
	CertName     string // org-wildcard-tls-<slug>-<parent-dashed> (== secretName)
	HTTPSName    string // console-https-<slug> — listener name
	HTTPName     string // console-http-<slug>  — listener name
}

// orgConsoleTLSNamesFor computes the deterministic per-Org names from a
// parentDomain + subdomain. The subdomain is the leftmost label of the
// Org's public host (tenantPublic.subdomain, defaulting to spec.slug — the
// same derivation tenant_route.go uses for the HTTPRoute hostname, so the
// listener hostname + cert SAN + route host all line up).
func orgConsoleTLSNamesFor(subdomain, parentDomain string) orgConsoleTLSNames {
	slug := strings.ToLower(strings.TrimSpace(subdomain))
	parent := strings.ToLower(strings.TrimSpace(parentDomain))
	parentDashed := dnsDashed(parent)
	return orgConsoleTLSNames{
		Slug:         slug,
		ParentDomain: parent,
		WildcardHost: "*." + slug + "." + parent,
		OrgZone:      slug + "." + parent,
		CertName:     "org-wildcard-tls-" + slug + "-" + parentDashed,
		HTTPSName:    "console-https-" + slug,
		HTTPName:     "console-http-" + slug,
	}
}

// reconcileTenantConsoleTLS issues the per-pool wildcard Certificate and
// appends the matching `*.<parentDomain>` listener pair to the console
// Gateway, so the console Gateway can terminate TLS for
// `console.<slug>.<parentDomain>`. Returns (changed, err). It is a no-op
// (false, nil) when the Org has no public parentDomain. Both writes are
// idempotent and strictly additive — the apex `*.<sovFQDN>` listeners
// and any other pool listeners are never modified.
//
// Failure here is transient (DNS-01 issuance latency, a momentary
// Gateway-update conflict) — the caller logs and requeues rather than
// failing the whole Org reconcile, so the per-Org Flux loop / realm /
// UserAccess steps still converge while the cert is being issued.
func (r *Reconciler) reconcileTenantConsoleTLS(ctx context.Context, org *orgapi.Organization) (bool, error) {
	// Feature disabled (no pool-parent public hostname) or no slug to form a
	// 2-label host from → no-op; the Org's other reconcile outputs still land
	// and a later pass re-runs this. The derivation lives in
	// orgConsoleTLSNamesForOrg (provisioning_postconditions.go) so the up-path,
	// the teardown, and the #5395 postcondition verifier cannot drift onto
	// different listener names.
	names, ok := orgConsoleTLSNamesForOrg(org)
	if !ok {
		return false, nil
	}

	certChanged, err := r.reconcileOrgWildcardCert(ctx, org, names)
	if err != nil {
		return false, fmt.Errorf("org wildcard cert for %q: %w", names.WildcardHost, err)
	}
	listenerChanged, err := r.ensureConsoleOrgListener(ctx, names)
	if err != nil {
		return certChanged, fmt.Errorf("console listener for %q: %w", names.WildcardHost, err)
	}
	return certChanged || listenerChanged, nil
}

// reconcileOrgWildcardCert create-or-updates the per-Org cert-manager
// Certificate for `*.<slug>.<parentDomain>` + `<slug>.<parentDomain>`
// (the 2-label SAN that covers `console.<slug>.<parentDomain>` — the
// apex pool wildcard `*.<parentDomain>` cannot, #4241). Idempotent:
// re-applies the desired spec only when it drifts (cert-manager treats
// an unchanged spec as a no-op, so this does not trigger re-issuance).
func (r *Reconciler) reconcileOrgWildcardCert(ctx context.Context, org *orgapi.Organization, names orgConsoleTLSNames) (bool, error) {
	ns := r.consoleTLSCertNamespace()
	name := names.CertName

	labels := map[string]string{
		"app.kubernetes.io/managed-by":      "catalyst",
		"openova.io/managed-by":             "organization-controller",
		"openova.io/sovereign":              org.Spec.SovereignRef,
		"catalyst.openova.io/component":     "cilium-gateway-console",
		"catalyst.openova.io/parent-zone":   names.ParentDomain,
		"catalyst.openova.io/org-subdomain": names.Slug,
	}

	desiredSpec := map[string]any{
		"commonName": names.OrgZone,
		"dnsNames": []any{
			names.WildcardHost,
			names.OrgZone,
		},
		"secretName": name,
		"issuerRef": map[string]any{
			"kind": "ClusterIssuer",
			"name": r.consoleTLSClusterIssuer(),
		},
	}

	desired := unstructured.Unstructured{}
	desired.SetGroupVersionKind(certificateGVK)
	desired.SetName(name)
	desired.SetNamespace(ns)
	desired.SetLabels(labels)
	desired.Object["spec"] = desiredSpec

	current := unstructured.Unstructured{}
	current.SetGroupVersionKind(certificateGVK)
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &current)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("get Certificate %s/%s: %w", ns, name, err)
		}
		if err := r.Create(ctx, &desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, nil
			}
			return false, fmt.Errorf("create Certificate %s/%s: %w", ns, name, err)
		}
		return true, nil
	}

	// Update only if the spec actually drifted, to avoid needless
	// re-issuance churn on a busy reconcile loop.
	if specEqual(current.Object["spec"], desiredSpec) {
		return false, nil
	}
	current.Object["spec"] = desiredSpec
	current.SetLabels(labels)
	if err := r.Update(ctx, &current); err != nil {
		return false, fmt.Errorf("update Certificate %s/%s: %w", ns, name, err)
	}
	return true, nil
}

// ensureConsoleOrgListener appends the per-Org `*.<slug>.<parentDomain>`
// HTTPS+HTTP listener pair to the console Gateway if (and only if)
// listeners with those names don't already exist. The append is strictly
// additive — it reads the live listeners array, leaves every existing
// entry byte-for-byte intact (the apex `*.<sovFQDN>` pair AND every other
// Org's per-Org listeners), and writes back the array with the two new
// entries appended. A second pass with both names already present is a
// no-op. The listener names (`console-https-<slug>` / `console-http-<slug>`)
// and the `*.<slug>.<parent>` hostname match the catalyst-api BSS-door
// emitter byte-for-byte (#4241), so whichever door provisions a given Org,
// the gateway edge converges on one consistent listener.
func (r *Reconciler) ensureConsoleOrgListener(ctx context.Context, names orgConsoleTLSNames) (bool, error) {
	gwNS := r.consoleGatewayNamespace()
	gwName := r.consoleGatewayName()
	httpsName := names.HTTPSName
	httpName := names.HTTPName
	hostname := names.WildcardHost
	secretName := names.CertName

	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: gwNS, Name: gwName}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			// Console Gateway not present yet (early bootstrap window).
			// Skip quietly — a later reconcile re-runs once sovereign-tls
			// has applied it.
			return false, nil
		}
		return false, fmt.Errorf("get Gateway %s/%s: %w", gwNS, gwName, err)
	}

	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		return false, fmt.Errorf("read Gateway %s/%s listeners: %w", gwNS, gwName, err)
	}

	// #5511 — the per-Org ports come from the LIVE apex pair, never from a
	// constant. See consoleApexListenerPorts.
	httpsPort, httpPort := consoleApexListenerPorts(listeners)

	desired := map[string]map[string]any{
		httpsName: {
			"name":     httpsName,
			"hostname": hostname,
			"port":     httpsPort,
			"protocol": "HTTPS",
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{"from": "All"},
			},
			"tls": map[string]any{
				"mode": "Terminate",
				"certificateRefs": []any{
					map[string]any{
						"group": "",
						"kind":  "Secret",
						"name":  secretName,
					},
				},
			},
		},
		httpName: {
			"name":     httpName,
			"hostname": hostname,
			"port":     httpPort,
			"protocol": "HTTP",
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{"from": "All"},
			},
		},
	}

	// Pass 1 — repair drift IN PLACE on a listener we already own.
	//
	// 🛑 #5511: this used to short-circuit on `have[httpsName] && have[httpName]`
	// — name presence alone counted as success. That made every wrong-port
	// listener PERMANENT: the pre-#4718 31443/31080 pair this reconciler used
	// to write is already on the live Gateway of every affected Sovereign, and
	// a presence-only check would never correct it, so fixing the port constant
	// alone would repair new Orgs and leave every existing customer door at
	// 000 forever. Drift is compared against the fields we OWN only (a subset
	// match, listenerSatisfies) so an apiserver-defaulted field never reads as
	// drift and hot-loops the controller.
	changed := false
	seen := map[string]bool{}
	for i, l := range listeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		want, mine := desired[n]
		if !mine {
			// Another Org's listener, or an apex listener — never touched.
			continue
		}
		seen[n] = true
		if listenerSatisfies(m, want) {
			continue
		}
		listeners[i] = want
		changed = true
	}

	// Pass 2 — append the ones that are genuinely absent, in a deterministic
	// order (HTTPS then HTTP) so two reconciles never produce different specs.
	for _, n := range []string{httpsName, httpName} {
		if seen[n] {
			continue
		}
		listeners = append(listeners, desired[n])
		changed = true
	}

	if !changed {
		// Both listeners present AND already carrying the desired hostname,
		// port, protocol and certificateRef — idempotent no-op.
		return false, nil
	}

	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		return false, fmt.Errorf("set Gateway %s/%s listeners: %w", gwNS, gwName, err)
	}
	if err := r.Update(ctx, &gw); err != nil {
		// A conflict means another writer (or our own prior pass) touched
		// the Gateway — surface it so the caller requeues and re-reads.
		return false, fmt.Errorf("update Gateway %s/%s: %w", gwNS, gwName, err)
	}
	return true, nil
}

// teardownTenantConsoleTLS removes the per-Org console-TLS artifacts the
// up-path (reconcileTenantConsoleTLS) appended/created: the two named
// listeners on the SHARED console Gateway and the per-Org wildcard
// Certificate. This is the deletion-direction counterpart — without it,
// deleting an Organization CR leaves dead `console-https-<slug>` /
// `console-http-<slug>` listeners ACCUMULATING on the shared Gateway and
// an orphan Certificate (+ its TLS Secret) in the Gateway namespace, since
// neither sits in the Flux-pruned `<slug>` namespace nor carries an
// ownerRef the GC follows (#4459). No-op (false, nil) when the Org never
// had a pool parentDomain. Best-effort + absent-as-success — a missing
// artifact is success, not an error, so a repeated reconcile is idempotent.
func (r *Reconciler) teardownTenantConsoleTLS(ctx context.Context, org *orgapi.Organization) (bool, error) {
	// Same derivation the up-path uses (orgConsoleTLSNamesForOrg) so teardown
	// strips exactly the listener names the up-path appended.
	names, ok := orgConsoleTLSNamesForOrg(org)
	if !ok {
		return false, nil
	}

	listenerChanged, err := r.removeConsoleOrgListener(ctx, names)
	if err != nil {
		return false, fmt.Errorf("remove console listener for %q: %w", names.WildcardHost, err)
	}
	certChanged, err := r.deleteOrgWildcardCert(ctx, names)
	if err != nil {
		return listenerChanged, fmt.Errorf("delete org wildcard cert %q: %w", names.CertName, err)
	}
	return listenerChanged || certChanged, nil
}

// removeConsoleOrgListener strips the per-Org `console-https-<slug>` /
// `console-http-<slug>` listeners off the shared console Gateway, leaving
// every OTHER listener (the apex `*.<sovFQDN>` pair AND every other Org's
// per-Org listeners) byte-for-byte intact — the exact inverse of
// ensureConsoleOrgListener's strictly-additive append. Returns (changed,
// err): changed=true iff at least one listener was actually removed.
// No-op when the Gateway is absent (already gone) or carries neither
// listener (idempotent on a re-run).
func (r *Reconciler) removeConsoleOrgListener(ctx context.Context, names orgConsoleTLSNames) (bool, error) {
	gwNS := r.consoleGatewayNamespace()
	gwName := r.consoleGatewayName()

	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: gwNS, Name: gwName}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			// Gateway already gone — nothing to strip.
			return false, nil
		}
		return false, fmt.Errorf("get Gateway %s/%s: %w", gwNS, gwName, err)
	}

	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		return false, fmt.Errorf("read Gateway %s/%s listeners: %w", gwNS, gwName, err)
	}

	drop := map[string]bool{names.HTTPSName: true, names.HTTPName: true}
	kept := make([]any, 0, len(listeners))
	removed := false
	for _, l := range listeners {
		if m, ok := l.(map[string]any); ok {
			if n, ok := m["name"].(string); ok && drop[n] {
				removed = true
				continue
			}
		}
		kept = append(kept, l)
	}
	if !removed {
		// Neither per-Org listener present — idempotent no-op.
		return false, nil
	}

	if err := unstructured.SetNestedSlice(gw.Object, kept, "spec", "listeners"); err != nil {
		return false, fmt.Errorf("set Gateway %s/%s listeners: %w", gwNS, gwName, err)
	}
	if err := r.Update(ctx, &gw); err != nil {
		// A conflict means another writer touched the Gateway concurrently —
		// surface it so the caller requeues and re-reads.
		return false, fmt.Errorf("update Gateway %s/%s: %w", gwNS, gwName, err)
	}
	return true, nil
}

// deleteOrgWildcardCert removes the per-Org cert-manager Certificate the
// up-path created. cert-manager garbage-collects the backing TLS Secret
// once the Certificate is gone (the Secret carries an ownerRef to the
// Certificate), so deleting the Certificate cascades the Secret. Returns
// (changed, err): changed=true iff a Certificate was actually deleted.
// Absent-as-success.
func (r *Reconciler) deleteOrgWildcardCert(ctx context.Context, names orgConsoleTLSNames) (bool, error) {
	ns := r.consoleTLSCertNamespace()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(certificateGVK)
	obj.SetNamespace(ns)
	obj.SetName(names.CertName)
	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete Certificate %s/%s: %w", ns, names.CertName, err)
	}
	return true, nil
}

// listenerSatisfies reports whether a LIVE Gateway listener already carries
// every field the desired listener specifies — a subset match, not an equality
// check (#5511).
//
// Subset rather than equality is deliberate. The Gateway API CRD and the
// gateway controller both default fields we do not set (`tls.options`, and an
// operator may legitimately add `allowedRoutes.kinds`). A full-map compare
// would read every such defaulted field as drift, rewrite the listener on
// every single reconcile pass, and hot-loop the controller against the
// apiserver — the #5305 shape. Comparing only the fields we own means we
// correct a wrong port, hostname, protocol or certificateRef and stay silent
// about everything else.
func listenerSatisfies(live, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		l, ok := live.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			if !listenerSatisfies(l[k], wv) {
				return false
			}
		}
		return true
	case []any:
		l, ok := live.([]any)
		if !ok || len(l) != len(w) {
			return false
		}
		for i := range w {
			if !listenerSatisfies(l[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprintf("%v", live) == fmt.Sprintf("%v", want)
	}
}

// specEqual is a shallow structural compare of two map[string]any specs.
// Used to decide whether a Certificate Update is worth issuing. It walks
// nested maps/slices recursively comparing scalar values; differing
// types or missing keys count as not-equal.
func specEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !specEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !specEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}
