// org_app_surface_mesh.go — #5635, the APP half of the per-Org two-region
// gateway surface. The console half shipped in PR #5647; this file closes the
// remainder the issue explicitly left open.
//
// THE MEASUREMENT that defines the gap. hw292 (dep 1c56518035a83e03, 2 regions,
// cutoverComplete=true), 20 FRESH TCP connections to a funnel Org's purchased
// app FQDN — separate `curl --no-keepalive` invocations, because HTTP/2
// connection pinning makes a browser loop or a keepalive curl report 100%
// success against a round-robin (#5459):
//
//	https://wordpress.uatco.omani.homes/   ok=9  fail=11  of 20
//	  ok   -> HTTP 302 (into the WordPress installer), ssl_verify_result=0
//	  fail -> curl exit 35, TLS handshake reset, no HTTP status at all
//	https://console.hw292.omani.works/     ok=10 fail=0   of 10   (control)
//
// Both hosts resolve to the SAME shared EIP 212.72.24.85, which round-robins
// both regions' cilium-envoy. The control is the decisive part: the VIP is
// fine. What differs is whether the region the connection landed on can serve
// the Host header.
//
// FAILURE LAYER, established rather than inferred. A gateway with a listener
// but no healthy upstream answers HTTP 503. A TLS handshake RESET means envoy
// matched no listener for that SNI at all. Enumerating both regions'
// cilium-gateway-console:
//
//	region-a  console-https-uatco / console-http-uatco   hostname *.uatco.omani.homes
//	region-b  (no listener for *.uatco.omani.homes)
//
// and one layer up, namespace `uatco` does not exist in region-b at all (the
// per-Org Flux loop — GitRepository catalyst-tenant-<slug> plus its three
// Kustomizations — is created by the org-controller through its own
// single-cluster client, so region-b has neither the source nor the tree).
//
// WHY THIS IS NOT #5359 (the root cause previously recorded on the issue).
// #5359 is about region-b's Flux still pulling from an external source. The
// per-Org listener is NOT a GitOps artifact: `console-https-<slug>` exists
// only in Go — this package's ensureOrgConsoleListener and the org-controller's
// reconcileTenantConsoleTLS — and appears in no kustomize tree under
// clusters/. Repairing region-b's GitRepository therefore cannot produce it,
// and the recorded falsifiable prediction ("fixing #5359 resolves this with no
// gateway or placement change") is refuted by that alone. Region-b's Flux is
// separately unhealthy — `openova` GitRepository Ready=False, GitOperationFailed
// `pkt-line 3: EOF` against the local Gitea, generation 2 / observedGeneration
// 1 — which is a real defect and is exactly why NO GitOps-delivered fix can
// land in region-b today either. The only process that can write to a secondary
// region is catalyst-api, which is where the console half went and where this
// half goes.
//
// THE IDIOM — not invented here; copied from the platform surface that already
// works on this very cluster. bp-catalyst-platform 1.4.1190 annotates the
// region-a control-plane Services `service.cilium.io/global: "true"` +
// `.../shared: "true"`, and bp-catalyst-edge-routes (bootstrap-kit slot 13b)
// renders into region-b the SAME-name+namespace Services with ZERO local
// backends plus mirrored HTTPRoutes, so Cilium ClusterMesh merges the pair and
// region-b's envoy PROXIES to region-a's singleton instead of 404-ing (#5289).
// That is why every `*.hw292.omani.works` host is a 10/10 control above. The
// same shape backs bp-openbao's cross-region-mesh-service.yaml and
// bp-cnpg-pair's replica-mesh-service.yaml.
//
// A static chart cannot cover the per-Org surface: Orgs and their purchased
// apps are created at runtime, so the host set is not knowable at render time.
// This file is therefore the runtime emitter of exactly what slot 13b renders
// statically — same annotations, same zero-backend stub, same route mirror —
// driven from the Organization CRs on the #5635 ticker so it is creation-path
// independent and self-healing for Orgs that already exist.
//
// WHAT IT DOES, per Org, per pass:
//
//	host region      — every Service backing a per-Org app HTTPRoute is
//	                   EXPORTED to the mesh (global+shared annotations added
//	                   if absent; the Service itself is left otherwise
//	                   untouched, including its selector and ports).
//	secondary region — the org boundary Namespace, a zero-backend global
//	                   Service STUB per backend (same name+namespace+ports+
//	                   selector, matching no local Pod because no workload
//	                   runs there), and a mirror of each app HTTPRoute.
//
// SINGLE-REGION IS A NO-OP. With no secondary registered in h.k8sCache the
// function returns before it reads anything, so a single-region Sovereign
// issues zero additional API calls and its object graph is byte-identical.
//
// SAFETY. Only positively-identified per-Org app objects are candidates:
// the route must live in the Org's OWN boundary namespace `<slug>` and EVERY
// one of its hostnames must sit inside that Org's own zone `<slug>.<parent>`.
// A route with no hostnames, a hostname outside the zone, or the console host
// (which lives in catalyst-system and is owned by the console half) is skipped.
// Every write is create-if-absent or an additive annotation patch; nothing is
// deleted and no existing spec is overwritten.

package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Cilium ClusterMesh global-service annotations. Byte-identical to the pair
// bp-catalyst-platform 1.4.1190 puts on the region-a control-plane Services and
// bp-catalyst-edge-routes puts on the region-b stubs — the merge is keyed on
// name+namespace, so both sides must agree.
const (
	ciliumGlobalServiceAnnotation = "service.cilium.io/global"
	ciliumSharedServiceAnnotation = "service.cilium.io/shared"
)

// orgAppSurfaceComponent labels every object this emitter writes, so an
// operator (and any future reaper) can tell the projection apart from the
// GitOps-delivered original in the host region.
const orgAppSurfaceComponent = "org-app-crossregion"

// orgAppSurfaceServicesGVR is the core/v1 Service resource, addressed through
// the dynamic client for the same reason namespacesGVR is (every seam in this
// package already holds a dynamic.Interface).
func orgAppSurfaceServicesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
}

// reconcileOrgAppSurfaceAcrossRegions projects one Org's per-Org APP gateway
// surface from the host region into every secondary region, and exports the
// host region's backing Services to the ClusterMesh so the projection resolves.
//
// Best-effort and non-gating throughout, matching provisionOrgConsoleTLS: a
// failure is logged with the region that failed and the next ticker pass
// retries. Never returns an error — the caller is a reconcile loop over every
// Org and one bad Org must not starve the rest.
func (h *Handler) reconcileOrgAppSurfaceAcrossRegions(ctx context.Context, deps *sovereignDeps, rec store.OrganizationProvisionRecord) {
	if h == nil || deps == nil || deps.dyn == nil {
		return
	}
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok {
		return
	}

	targets := h.orgConsoleTLSTargets(deps)
	if len(targets) < 2 {
		// Single-region Sovereign (or a mothership, where orgConsoleTLSTargets
		// deliberately refuses to fan out). Nothing to mirror and nothing to
		// export — return before any read so the single-region object graph and
		// API-call profile are unchanged.
		return
	}
	host := targets[0]

	routes := listOrgAppRoutes(ctx, host.dyn, names)
	if len(routes) == 0 {
		// No purchased app has a host-native route yet. The console half still
		// gives this Org a working console in every region; app hosts simply do
		// not exist yet.
		return
	}

	// ── host region: export every backing Service to the mesh ──────────────
	backends := orgAppRouteBackends(routes, names.Slug)
	exported := make([]*unstructured.Unstructured, 0, len(backends))
	for _, svcName := range backends {
		svc, err := ensureOrgAppServiceExported(ctx, host.dyn, names.Slug, svcName)
		if err != nil {
			h.log.Error("org-app-surface: could not export the per-Org app Service to the ClusterMesh — the secondary region's stub cannot resolve until this succeeds (#5635)",
				"org_tenant_id", rec.OrganizationID,
				"namespace", names.Slug, "service", svcName, "err", err)
			continue
		}
		exported = append(exported, svc)
	}
	if len(exported) == 0 {
		return
	}

	// ── every secondary region: namespace + zero-backend stubs + routes ────
	for _, tgt := range targets[1:] {
		if err := ensureOrgBoundaryNamespaceForApps(ctx, tgt.dyn, names, rec); err != nil {
			h.log.Error("org-app-surface: could not ensure the Org boundary namespace in a secondary region — its per-Org app hosts stay unserved there (#5635)",
				"org_tenant_id", rec.OrganizationID,
				"region", tgt.region, "clusterID", tgt.clusterID,
				"namespace", names.Slug, "err", err)
			continue
		}
		for _, svc := range exported {
			if err := ensureOrgAppMeshStub(ctx, tgt.dyn, names, rec, svc); err != nil {
				h.log.Error("org-app-surface: could not ensure the ClusterMesh Service stub in a secondary region (#5635)",
					"org_tenant_id", rec.OrganizationID,
					"region", tgt.region, "clusterID", tgt.clusterID,
					"namespace", names.Slug, "service", svc.GetName(), "err", err)
			}
		}
		for i := range routes {
			if err := ensureOrgAppRouteMirror(ctx, tgt.dyn, names, rec, &routes[i]); err != nil {
				h.log.Error("org-app-surface: could not mirror the per-Org app HTTPRoute into a secondary region — that region still resets/404s this host (#5635)",
					"org_tenant_id", rec.OrganizationID,
					"region", tgt.region, "clusterID", tgt.clusterID,
					"namespace", names.Slug, "route", routes[i].GetName(), "err", err)
			}
		}
	}

	h.log.Info("org-app-surface: per-Org app hosts projected into every secondary region via ClusterMesh stubs (#5635)",
		"org_tenant_id", rec.OrganizationID,
		"namespace", names.Slug,
		"routes", len(routes),
		"backends", len(exported),
		"secondary_regions", len(targets)-1,
	)
}

// listOrgAppRoutes returns the HTTPRoutes in the Org's OWN boundary namespace
// that serve a host inside the Org's OWN zone. Both conditions are required —
// that pair is what makes an object positively identifiable as this Org's app
// surface rather than something else that happens to live nearby.
//
// The console host is excluded: its route lives in catalyst-system and is owned
// by the console half (provisionOrgConsoleTLS), which already writes it to every
// region. A route with zero hostnames is excluded because a hostname-less route
// matches every host on its listener — mirroring one would hand a secondary
// region a catch-all it was never meant to serve.
func listOrgAppRoutes(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames) []unstructured.Unstructured {
	list, err := dyn.Resource(httpRouteGVR).Namespace(names.Slug).List(ctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return nil
	}
	out := make([]unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		if orgAppRouteServesOwnZone(&list.Items[i], names) {
			out = append(out, list.Items[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// orgAppRouteServesOwnZone is the structural identity test: EVERY hostname must
// be a strict subdomain of `<slug>.<parent>`, there must be at least one, and
// none may be the console host. "Every", not "any" — a route that also serves an
// unrelated host would have that host projected too.
func orgAppRouteServesOwnZone(route *unstructured.Unstructured, names orgConsoleTLSNames) bool {
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hosts) == 0 {
		return false
	}
	suffix := "." + names.OrgZone
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == names.ConsoleHost {
			return false
		}
		if !strings.HasSuffix(h, suffix) || len(h) <= len(suffix) {
			return false
		}
	}
	return true
}

// orgAppRouteBackends collects the distinct same-namespace Service backends of
// the given routes. A backendRef naming another namespace is skipped: mirroring
// it would need a ReferenceGrant in the secondary region that nothing creates,
// and the per-Org app model (a vcluster-syncer-reflected `<app>-x-<slug>-x-vcluster`
// Service in the Org's own namespace) never produces one.
func orgAppRouteBackends(routes []unstructured.Unstructured, ns string) []string {
	seen := map[string]struct{}{}
	for i := range routes {
		rules, _, _ := unstructured.NestedSlice(routes[i].Object, "spec", "rules")
		for _, r := range rules {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
			for _, b := range refs {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if kind, _, _ := unstructured.NestedString(bm, "kind"); kind != "" && kind != "Service" {
					continue
				}
				if group, _, _ := unstructured.NestedString(bm, "group"); group != "" {
					continue
				}
				if bns, _, _ := unstructured.NestedString(bm, "namespace"); bns != "" && bns != ns {
					continue
				}
				name, _, _ := unstructured.NestedString(bm, "name")
				if name = strings.TrimSpace(name); name != "" {
					seen[name] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ensureOrgAppServiceExported reads the host region's backing Service and adds
// the two ClusterMesh annotations if either is missing, returning the Service
// as it now stands (the caller copies its ports + selector into the stub).
//
// ADDITIVE ONLY. The Service in the host region is owned by the vcluster syncer
// (`vcluster.loft.sh/managed-by: vcluster`); this touches nothing but the two
// annotations, and because the reconcile is level-triggered an owner that
// strips them is corrected on the next pass rather than fought over.
func ensureOrgAppServiceExported(ctx context.Context, dyn dynamic.Interface, ns, name string) (*unstructured.Unstructured, error) {
	svc, err := dyn.Resource(orgAppSurfaceServicesGVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Service %s/%s: %w", ns, name, err)
	}
	ann := svc.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	if ann[ciliumGlobalServiceAnnotation] == "true" && ann[ciliumSharedServiceAnnotation] == "true" {
		return svc, nil
	}
	ann[ciliumGlobalServiceAnnotation] = "true"
	ann[ciliumSharedServiceAnnotation] = "true"
	svc.SetAnnotations(ann)
	updated, err := dyn.Resource(orgAppSurfaceServicesGVR()).Namespace(ns).Update(ctx, svc, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("annotate Service %s/%s for ClusterMesh export: %w", ns, name, err)
	}
	return updated, nil
}

// ensureOrgBoundaryNamespaceForApps creates the Org's boundary namespace in a
// secondary region. The org-controller creates it in its own cluster only, so
// on a secondary it is simply absent — which is one layer ABOVE the missing
// route, and why no amount of gateway fan-out alone could have helped.
//
// The labels are the same org-identity labels the #5364/#5649 reap keys on, so
// a namespace this emitter creates is reaped with the rest of the Org's surface
// when the Org is deleted rather than becoming a new orphan class.
func ensureOrgBoundaryNamespaceForApps(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord) error {
	labels := orgConsoleTLSStringLabels(names, rec, orgAppSurfaceComponent)
	labels["openova.io/organization"] = names.Slug
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   names.Slug,
			"labels": toAnyMap(labels),
		},
	}}
	if _, err := dyn.Resource(namespacesGVR()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create Namespace %s: %w", names.Slug, err)
	}
	return nil
}

// ensureOrgAppMeshStub creates the zero-backend ClusterMesh Service stub in a
// secondary region: same name, same namespace, same ports and same selector as
// the host region's Service, carrying the same global+shared annotations.
//
// The selector is copied deliberately rather than dropped. Cilium merges the two
// same-named global Services into one; on the secondary the selector matches
// ZERO local Pods (the workload is placed in the host region), so every dial
// falls through the mesh to the host region's endpoints. That is the same
// arrangement bp-catalyst-edge-routes uses for catalyst-api/catalyst-ui and
// bp-openbao uses for its active member. A selectorless Service would instead
// require someone to author EndpointSlices, which nothing does.
func ensureOrgAppMeshStub(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord, src *unstructured.Unstructured) error {
	ports, _, _ := unstructured.NestedSlice(src.Object, "spec", "ports")
	selector, _, _ := unstructured.NestedMap(src.Object, "spec", "selector")

	spec := map[string]any{"type": "ClusterIP"}
	if len(ports) > 0 {
		spec["ports"] = ports
	}
	if len(selector) > 0 {
		spec["selector"] = selector
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      src.GetName(),
			"namespace": names.Slug,
			"labels":    orgConsoleTLSLabels(names, rec, orgAppSurfaceComponent),
			"annotations": map[string]any{
				ciliumGlobalServiceAnnotation: "true",
				ciliumSharedServiceAnnotation: "true",
			},
		},
		"spec": spec,
	}}
	if _, err := dyn.Resource(orgAppSurfaceServicesGVR()).Namespace(names.Slug).
		Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create ClusterMesh Service stub %s/%s: %w", names.Slug, src.GetName(), err)
	}
	return nil
}

// ensureOrgAppRouteMirror creates a copy of a per-Org app HTTPRoute in a
// secondary region: same name, same namespace, spec copied verbatim so the
// hostname, the parentRef onto cilium-gateway-console and the backendRefs all
// match the host region's. Create-if-absent — an existing route in that region
// (whatever produced it) is left exactly as it is.
func ensureOrgAppRouteMirror(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord, src *unstructured.Unstructured) error {
	spec, found, err := unstructured.NestedMap(src.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("read spec of HTTPRoute %s/%s", names.Slug, src.GetName())
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      src.GetName(),
			"namespace": names.Slug,
			"labels":    orgConsoleTLSLabels(names, rec, orgAppSurfaceComponent),
		},
		"spec": spec,
	}}
	if _, err := dyn.Resource(httpRouteGVR).Namespace(names.Slug).
		Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create HTTPRoute mirror %s/%s: %w", names.Slug, src.GetName(), err)
	}
	return nil
}

// toAnyMap widens a string label map for unstructured metadata.
func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
