// org_app_surface_mesh_5635_test.go — #5635 APP half.
//
// Both tests drive ONLY pre-existing entry points (reconcileOrgConsoleTLSOnce,
// newReconcileHandler, newConsoleTLSHandlerWithCore) and assert on OUTCOMES, so
// this file compiles unchanged against origin/main and the failure it reports
// there is a RUNTIME failure describing the live defect — not a build error
// about a symbol the fix introduces.
//
//	lock 1  TestReconcileOrgAppSurface_SecondaryRegionServesPerOrgAppHost
//	        FAILS on origin/main  — the secondary region carries no route and
//	        no mesh stub for the Org's app host, which is the ~50% reset.
//	        PASSES with the fix.
//
//	lock 2  TestReconcileOrgAppSurface_SingleRegionWritesNothingExtra
//	        PASSES on BOTH trees. It pins the two properties a blanket
//	        suppression would have to break in order to satisfy lock 1:
//	        the console surface is still produced, and a single-region
//	        Sovereign gains no app-surface objects at all.
package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// appSurfaceServicesGVR is declared locally, NOT imported from the fix, so this
// file has no compile-time dependency on anything the fix adds.
var appSurfaceServicesGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}

// fakeDynForOrgAppSurface is fakeDynForOrgConsoleReconcile plus the Service and
// Namespace GVRs, so a region cluster can hold a per-Org app surface.
func fakeDynForOrgAppSurface(t *testing.T, orgs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			certificateGVR:        "CertificateList",
			consoleGatewayGVR:     "GatewayList",
			httpRouteGVR:          "HTTPRouteList",
			organizationGVR():     "OrganizationList",
			appSurfaceServicesGVR: "ServiceList",
			namespacesGVR():       "NamespaceList",
		})
	ctx := context.Background()
	gw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]any{
			"name":      consoleGatewayName,
			"namespace": consoleGatewayNamespace,
		},
		"spec": map[string]any{
			"gatewayClassName": "cilium",
			"listeners": []any{
				map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS", "hostname": "*.hw292.omani.works"},
				map[string]any{"name": "console-http", "port": int64(8080), "protocol": "HTTP", "hostname": "*.hw292.omani.works"},
			},
		},
	}}
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Create(ctx, gw, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed console gateway: %v", err)
	}
	for _, o := range orgs {
		if _, err := dyn.Resource(organizationGVR()).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed Organization CR %s: %v", o.GetName(), err)
		}
	}
	return dyn
}

// seedPerOrgAppSurface writes into a region cluster exactly what hw292's
// region-a carries for the funnel Org `uatco`: the boundary Namespace, the
// vcluster-syncer-reflected backing Service, and the host-native app HTTPRoute
// the per-Org GitOps tree delivers (gitops.generateHostNativeAppRoute).
func seedPerOrgAppSurface(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug, appHost, svcName string) {
	t.Helper()
	ctx := context.Background()

	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": slug},
	}}
	if _, err := dyn.Resource(namespacesGVR()).Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed namespace %s: %v", slug, err)
	}

	svc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{
			"name": svcName, "namespace": slug,
			// The live Service is syncer-owned and carries NO cilium
			// annotations — that is the export half of the defect.
			"labels": map[string]any{"vcluster.loft.sh/managed-by": "vcluster"},
		},
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": map[string]any{"vcluster.loft.sh/owner-set-kind": "Deployment"},
			"ports":    []any{map[string]any{"port": int64(80), "targetPort": int64(80), "protocol": "TCP"}},
		},
	}}
	if _, err := dyn.Resource(appSurfaceServicesGVR).Namespace(slug).
		Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed backing Service %s/%s: %v", slug, svcName, err)
	}

	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "app-wordpress-hostroute", "namespace": slug},
		"spec": map[string]any{
			"hostnames": []any{appHost},
			"parentRefs": []any{map[string]any{
				"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"name": consoleGatewayName, "namespace": consoleGatewayNamespace,
			}},
			"rules": []any{map[string]any{
				"backendRefs": []any{map[string]any{
					"group": "", "kind": "Service",
					"name": svcName, "port": int64(80), "weight": int64(1),
				}},
				"matches": []any{map[string]any{
					"path": map[string]any{"type": "PathPrefix", "value": "/"},
				}},
			}},
		},
	}}
	if _, err := dyn.Resource(httpRouteGVR).Namespace(slug).
		Create(ctx, route, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed app HTTPRoute %s/%s: %v", slug, appHost, err)
	}
}

// regionServesHost reports whether a region cluster has an HTTPRoute in ns
// serving host — the property an envoy needs in order to answer instead of
// resetting or 404-ing.
func regionServesHost(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, host string) bool {
	t.Helper()
	list, err := dyn.Resource(httpRouteGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return false
	}
	for i := range list.Items {
		hosts, _, _ := unstructured.NestedStringSlice(list.Items[i].Object, "spec", "hostnames")
		for _, h := range hosts {
			if h == host {
				return true
			}
		}
	}
	return false
}

func serviceAnnotation(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, name, key string) (string, bool) {
	t.Helper()
	svc, err := dyn.Resource(appSurfaceServicesGVR).Namespace(ns).
		Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	v, ok := svc.GetAnnotations()[key]
	return v, ok
}

// TestReconcileOrgAppSurface_SecondaryRegionServesPerOrgAppHost — #5635 lock 1.
//
// hw292's exact shape: a marketplace-funnel Org whose purchased app surface was
// written to region-a only, while a shared EIP round-robins both regions'
// envoy. Ten fresh-TCP samples against the real host measured 9/20 reachable;
// the failures were TLS handshake resets, i.e. region-b matched no listener and
// carries no route for the host.
//
// After one reconcile pass region-b must be able to SERVE that host: a route in
// the Org's namespace, and a same-name ClusterMesh Service stub so the route's
// backend resolves across the mesh to region-a's singleton. The host region's
// backing Service must be exported (global+shared) or the stub resolves to
// nothing.
//
// On origin/main every assertion below fails: region-b's namespace, Service and
// route are all absent, and region-a's Service carries no cilium annotation.
func TestReconcileOrgAppSurface_SecondaryRegionServesPerOrgAppHost(t *testing.T) {
	const (
		slug    = "uatco"
		parent  = "omani.homes"
		appHost = "wordpress.uatco.omani.homes"
		svcName = "wordpress-x-uatco-x-vcluster"
	)
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works") // chroot gate for the fan-out

	org := funnelOrgCR(slug, parent)
	hostDyn := fakeDynForOrgAppSurface(t, org)
	secDyn := fakeDynForOrgAppSurface(t, org)
	hostCore := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"))
	secCore := k8sfake.NewSimpleClientset()

	// region-a carries the full per-Org app surface; region-b carries none of
	// it — the live hw292 split.
	seedPerOrgAppSurface(t, hostDyn, slug, appHost, svcName)

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	// 1. the host region's backing Service is EXPORTED to the mesh.
	if v, ok := serviceAnnotation(t, hostDyn, slug, svcName, "service.cilium.io/global"); !ok || v != "true" {
		t.Errorf("#5635: host-region Service %s/%s is not exported to the ClusterMesh "+
			"(service.cilium.io/global=%q present=%v, want \"true\") — a secondary-region stub "+
			"for it can never resolve", slug, svcName, v, ok)
	}

	// 2. the secondary region carries a same-name mesh stub.
	if v, ok := serviceAnnotation(t, secDyn, slug, svcName, "service.cilium.io/global"); !ok || v != "true" {
		t.Errorf("#5635: secondary region has no ClusterMesh Service stub %s/%s "+
			"(service.cilium.io/global=%q present=%v, want \"true\") — its envoy has no backend "+
			"for the app host", slug, svcName, v, ok)
	}

	// 3. and it SERVES the app host. This is the customer-visible assertion:
	//    without it ~half of all fresh connections to the host fail.
	if !regionServesHost(t, secDyn, slug, appHost) {
		t.Errorf("#5635: secondary region serves NO route for %q — half of all fresh TCP "+
			"connections to that host land on this region and fail (measured 9/20 reachable "+
			"on hw292, failures = TLS handshake reset)", appHost)
	}

	// 4. the host region is untouched apart from the annotation: its route is
	//    still the single authority for the host there.
	if !regionServesHost(t, hostDyn, slug, appHost) {
		t.Fatalf("#5635: host region stopped serving %q — the projection must be additive", appHost)
	}
}

// TestReconcileOrgAppSurface_SingleRegionWritesNothingExtra — #5635 lock 2, the
// anti-suppression control. PASSES on origin/main AND with the fix.
//
// It pins the two things a blanket "do nothing" could not satisfy alongside
// lock 1: on a Sovereign with NO secondary region registered, the pass must add
// no app-surface objects (no stub Service, no extra route, and the backing
// Service must not be dragged into a mesh that does not exist) — while STILL
// producing the Org's console surface. A patch that disabled the emitter
// outright would pass this and fail lock 1; a patch that projected
// unconditionally would pass lock 1 and fail this.
func TestReconcileOrgAppSurface_SingleRegionWritesNothingExtra(t *testing.T) {
	const (
		slug        = "uatco"
		parent      = "omani.homes"
		appHost     = "wordpress.uatco.omani.homes"
		svcName     = "wordpress-x-uatco-x-vcluster"
		consoleHost = "console.uatco.omani.homes"
	)
	// A real single-region Sovereign IS a chroot — so the no-op below is
	// attributable to "no secondary region registered", not to the chroot gate
	// happening to be closed.
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")

	org := funnelOrgCR(slug, parent)
	dyn := fakeDynForOrgAppSurface(t, org)
	core := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"))
	seedPerOrgAppSurface(t, dyn, slug, appHost, svcName)

	// No k8sCache => orgConsoleTLSTargets yields the host region only.
	h := newConsoleTLSHandlerWithCore(t, dyn, core)
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "hw292.omani.works"})
	h.reconcileOrgConsoleTLSOnce(context.Background())

	// a. the console surface is still produced — the property a blanket
	//    suppression of the reconcile pass would destroy.
	if _, route := consoleSurfacePresent(t, dyn, consoleHost); route == "" {
		t.Errorf("#5635: single-region pass produced no console HTTPRoute for %q — "+
			"the app half must not disable the console half", consoleHost)
	}

	// b. exactly ONE route in the Org namespace: the seeded one. No mirror.
	routes, err := dyn.Resource(httpRouteGVR).Namespace(slug).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HTTPRoutes in %s: %v", slug, err)
	}
	if len(routes.Items) != 1 {
		t.Errorf("#5635: single-region Sovereign has %d HTTPRoutes in ns %s, want exactly 1 "+
			"(the GitOps-delivered original) — the cross-region projection must be a no-op "+
			"without a secondary region", len(routes.Items), slug)
	}

	// c. exactly ONE Service, and it is NOT dragged into a nonexistent mesh.
	svcs, err := dyn.Resource(appSurfaceServicesGVR).Namespace(slug).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Services in %s: %v", slug, err)
	}
	if len(svcs.Items) != 1 {
		t.Errorf("#5635: single-region Sovereign has %d Services in ns %s, want exactly 1",
			len(svcs.Items), slug)
	}
	if _, ok := serviceAnnotation(t, dyn, slug, svcName, "service.cilium.io/global"); ok {
		t.Errorf("#5635: single-region Sovereign annotated %s/%s for ClusterMesh export — "+
			"there is no mesh to export into; single-region output must be unchanged",
			slug, svcName)
	}
}
