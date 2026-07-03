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

package handler

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

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

// provisionOrgConsoleTLS is the best-effort, non-gating finalisation step that
// makes the Org's console host (`console.<slug>.<parent>`) serve TLS. Mirrors
// createOrgOrganizationCR's idiom: resolve the in-cluster dynamic client via
// sovereignDepsFor() (nil-tolerant for CI / out-of-cluster), apply the three
// resources idempotently, and log loud on failure WITHOUT failing the Org
// pipeline. Each sub-step is independent: a failure on one is logged and the
// rest still run, and the org-controller / next HandleReconcileOrganization
// pass retries the whole thing.
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

	if err := ensureOrgConsoleCertificate(ctx, deps.dyn, names, rec); err != nil {
		h.log.Error("org-console-tls: Certificate apply failed — reconcile will retry",
			"cert", names.CertName, "host", names.WildcardHost, "err", err)
	}
	if err := ensureOrgConsoleListener(ctx, deps.dyn, names, rec); err != nil {
		h.log.Error("org-console-tls: Gateway listener apply failed — reconcile will retry",
			"gateway", consoleGatewayName, "https_listener", names.HTTPSName, "err", err)
	}
	if err := ensureOrgConsoleHTTPRoute(ctx, deps.dyn, names, rec); err != nil {
		h.log.Error("org-console-tls: HTTPRoute apply failed — reconcile will retry",
			"route", names.RouteName, "host", names.ConsoleHost, "err", err)
	}
	h.log.Info("org-console-tls: ensured per-Org console TLS trio",
		"org_tenant_id", rec.OrganizationID,
		"console_host", names.ConsoleHost,
		"cert", names.CertName,
		"https_listener", names.HTTPSName,
		"route", names.RouteName,
	)
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
