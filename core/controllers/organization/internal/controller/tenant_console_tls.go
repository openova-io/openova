// tenant_console_tls.go — per-pool console-TLS reconciler (issue #4075).
//
// Problem (#4075, live on the permanent omantel.biz Sovereign, dep
// 4635277cae4ffed9): a customer Organization that picks a free-subdomain
// hostname under a pool parent zone — e.g. `console.demo.omani.homes`
// under the `omani.homes` org-pool parent — resolved in DNS to the
// console LoadBalancer (212.72.24.33) but failed with
// ERR_CONNECTION_CLOSED. Root cause: the bootstrap-rendered console
// Cilium Gateway (clusters/_template/sovereign-tls/cilium-gateway-
// console.yaml) carries ONLY the apex `*.<sovFQDN>` listener bound to
// the apex wildcard Secret `sovereign-wildcard-tls-<sovFQDN-dashed>`.
// There is NO `*.<parentDomain>` TLS Certificate and NO matching
// listener, so a TLS ClientHello with SNI=console.<slug>.<parentDomain>
// matches no listener → Envoy closes the connection before any HTTP is
// exchanged. (`kubectl get certificate -A | grep <parentDomain>` = 0.)
//
// The HTTPRoute alone (tenant_route.go) is necessary but NOT sufficient:
// without the cert + listener the request never survives the TLS
// handshake to reach a route. This reconciler supplies the two missing
// pieces, idempotently and additively, whenever an Org sets
// `spec.tenantPublic.parentDomain`:
//
//  1. A cert-manager Certificate `org-wildcard-tls-<parentDomain-dashed>`
//     in the console Gateway's namespace, with dnsNames
//     `*.<parentDomain>` + `<parentDomain>`, issued by the DNS-01
//     ClusterIssuer (a wildcard SAN can ONLY be solved via DNS-01). The
//     Certificate is shared by every Org under the same pool parent
//     (one cert per parent zone, not per Org) so the Let's-Encrypt
//     issuance count stays bounded regardless of Org count.
//
//  2. A listener pair on the console Gateway — `pool-https-
//     <parentDomain-dashed>` (HTTPS, terminates on the Secret above) and
//     `pool-http-<parentDomain-dashed>` (HTTP) — hostnamed
//     `*.<parentDomain>`, appended to the EXISTING listeners array
//     WITHOUT touching the apex `*.<sovFQDN>` listeners. The append is
//     idempotent: a listener whose name already exists is left
//     untouched (no duplicate, no spec churn).
//
// Why unstructured (not the cert-manager / gateway-api Go types): the
// whole organization-controller already manipulates the Gateway-API
// HTTPRoute as an Unstructured (tenant_route.go) precisely to avoid
// pulling those CRD type modules in for a handful of resources. We keep
// that discipline here — Certificate and Gateway are both written
// through Unstructured.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every operationally-meaningful
// value (Gateway name/namespace, ClusterIssuer, cert namespace, host
// ports) flows through the Reconciler's env-configured fields — no
// hardcoded names in the renderer.

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

	// Host ports the console Gateway's listeners bind — the dedicated
	// console Gateway uses 31443/31080 (NOT the shared gateway's
	// 30443/30080). See cilium-gateway-console.yaml header for the
	// bind-collision rationale. The per-pool listeners reuse the SAME
	// host ports as the apex console listeners (a Gateway listener is
	// selected by hostname+port; multiple HTTPS listeners can share one
	// port as long as their hostnames differ — SNI routes between them).
	consoleListenerHTTPSPort = int64(31443)
	consoleListenerHTTPPort  = int64(31080)
)

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

// poolWildcardSecretName is the deterministic name of the per-pool
// wildcard Certificate + Secret. One per parent zone (shared across all
// Orgs under it). Mirrors the apex `sovereign-wildcard-tls-*` pattern.
func poolWildcardSecretName(parentDomain string) string {
	return "org-wildcard-tls-" + dnsDashed(parentDomain)
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
	parentDomain := strings.TrimSpace(org.Spec.TenantPublic.ParentDomain)
	if parentDomain == "" {
		// Feature disabled — Org has no pool-parent public hostname.
		return false, nil
	}

	certChanged, err := r.reconcilePoolWildcardCert(ctx, org, parentDomain)
	if err != nil {
		return false, fmt.Errorf("pool wildcard cert for %q: %w", parentDomain, err)
	}
	listenerChanged, err := r.ensureConsolePoolListener(ctx, parentDomain)
	if err != nil {
		return certChanged, fmt.Errorf("console listener for %q: %w", parentDomain, err)
	}
	return certChanged || listenerChanged, nil
}

// reconcilePoolWildcardCert create-or-updates the cert-manager
// Certificate for `*.<parentDomain>`. Idempotent: re-applies the desired
// spec on every pass (cert-manager treats an unchanged spec as a no-op,
// so this does not trigger re-issuance).
func (r *Reconciler) reconcilePoolWildcardCert(ctx context.Context, org *orgapi.Organization, parentDomain string) (bool, error) {
	ns := r.consoleTLSCertNamespace()
	name := poolWildcardSecretName(parentDomain)

	labels := map[string]string{
		"app.kubernetes.io/managed-by":    "catalyst",
		"openova.io/managed-by":           "organization-controller",
		"openova.io/sovereign":            org.Spec.SovereignRef,
		"catalyst.openova.io/component":   "cilium-gateway-console",
		"catalyst.openova.io/parent-zone": parentDomain,
	}

	desiredSpec := map[string]any{
		"commonName": parentDomain,
		"dnsNames": []any{
			"*." + parentDomain,
			parentDomain,
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

// ensureConsolePoolListener appends the `*.<parentDomain>` HTTPS+HTTP
// listener pair to the console Gateway if (and only if) listeners with
// those names don't already exist. The append is strictly additive — it
// reads the live listeners array, leaves every existing entry byte-for-
// byte intact, and writes back the array with the two new entries
// appended. A second pass with both names already present is a no-op.
func (r *Reconciler) ensureConsolePoolListener(ctx context.Context, parentDomain string) (bool, error) {
	gwNS := r.consoleGatewayNamespace()
	gwName := r.consoleGatewayName()
	dashed := dnsDashed(parentDomain)
	httpsName := "pool-https-" + dashed
	httpName := "pool-http-" + dashed
	hostname := "*." + parentDomain
	secretName := poolWildcardSecretName(parentDomain)

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

	have := map[string]bool{}
	for _, l := range listeners {
		if m, ok := l.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				have[n] = true
			}
		}
	}
	if have[httpsName] && have[httpName] {
		// Both listeners already present — idempotent no-op.
		return false, nil
	}

	if !have[httpsName] {
		listeners = append(listeners, map[string]any{
			"name":     httpsName,
			"hostname": hostname,
			"port":     consoleListenerHTTPSPort,
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
		})
	}
	if !have[httpName] {
		listeners = append(listeners, map[string]any{
			"name":     httpName,
			"hostname": hostname,
			"port":     consoleListenerHTTPPort,
			"protocol": "HTTP",
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{"from": "All"},
			},
		})
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
