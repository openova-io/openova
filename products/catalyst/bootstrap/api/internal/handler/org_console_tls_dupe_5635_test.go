// org_console_tls_dupe_5635_test.go — #5635, producer-parity half.
//
// The per-Org console gateway surface has TWO producers that agree on the
// Certificate name and on BOTH listener names but NOT on the console
// HTTPRoute's name:
//
//	catalyst-api  org_console_tls.go:resolveOrgConsoleTLSNames
//	              → catalyst-ui-<slug>-<parent-dashed>
//	                e.g. catalyst-ui-r17probe-omani-homes
//	org-controller core/controllers/organization/internal/controller/
//	              tenant_route.go:141 → "catalyst-ui-" + dnsDashed(hostname)
//	              → catalyst-ui-console-<slug>-<parent-dashed>
//	                e.g. catalyst-ui-console-uatco-omani-homes
//
// Both were observed live in ONE catalyst-system on hw292 (dep
// 1c56518035a83e03, 2026-08-03), distinguishable by their labels
// (`app.kubernetes.io/managed-by` = catalyst-api vs catalyst). Once
// catalyst-api reconciles the surface for EVERY Org (org_console_tls_reconcile.go)
// it necessarily meets Orgs the org-controller already wrote a route for, and
// creating its own on top would attach a SECOND HTTPRoute to the same hostname
// on the same Gateway — a duplicated surface that only one of the two teardown
// paths reaps (teardownTenantRoute targets the org-controller name only, which
// is exactly why hw292's region-A still carried an orphan
// catalyst-ui-r17probe-omani-homes after that Org was deleted).
//
// This file deliberately uses ONLY symbols that predate the #5635 fix, so it
// compiles against the unfixed tree and fails there at RUNTIME (two routes for
// one console host) rather than at build time.

package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgControllerConsoleRoute builds the console HTTPRoute exactly as the
// org-controller's reconcileTenantRoute renders it: name
// `catalyst-ui-<console-host-dashed>`, namespace catalyst-system, the same
// hostname, and the org-controller's own managed-by labels. Hand-written from
// tenant_route.go rather than derived from a shared helper so the test pins
// the CROSS-PROCESS contract literally — a rename on either side must break it.
func orgControllerConsoleRoute(name, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      name,
			"namespace": catalystConsoleNamespace,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "catalyst",
				"openova.io/managed-by":        "organization-controller",
			},
		},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{
				"name": consoleGatewayName, "namespace": consoleGatewayNamespace,
			}},
			"hostnames": []any{host},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{
					"path": map[string]any{"type": "PathPrefix", "value": "/"},
				}},
				"backendRefs": []any{map[string]any{"name": catalystUIServiceName, "port": int64(80)}},
			}},
		},
	}}
}

// TestProvisionOrgConsoleTLS_AdoptsOrgControllerRoute_NoDuplicateSurface —
// #5635. When a region ALREADY carries the org-controller's console route for
// this Org's console host, the catalyst-api emitter must ADOPT it (leave the
// region served by exactly ONE route), not add a second route for the same
// hostname on the same Gateway.
//
// Fails on the unfixed tree: ensureOrgConsoleHTTPRoute created
// `catalyst-ui-acme-omani-homes` unconditionally, so the host ended up with 2
// routes.
func TestProvisionOrgConsoleTLS_AdoptsOrgControllerRoute_NoDuplicateSurface(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, dyn)
	ctx := context.Background()

	const (
		host          = "console.acme.omani.homes"
		orgCtrlRoute  = "catalyst-ui-console-acme-omani-homes" // tenant_route.go:141
		catalystRoute = "catalyst-ui-acme-omani-homes"         // org_console_tls.go RouteName
	)

	// The funnel door got here first: the org-controller's route is live.
	if _, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Create(ctx, orgControllerConsoleRoute(orgCtrlRoute, host), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed org-controller console route: %v", err)
	}

	h.provisionOrgConsoleTLS(ctx, store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "hw292.omani.works",
	})

	list, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HTTPRoutes: %v", err)
	}
	serving := []string{}
	for i := range list.Items {
		hosts, _, _ := unstructured.NestedStringSlice(list.Items[i].Object, "spec", "hostnames")
		for _, hh := range hosts {
			if hh == host {
				serving = append(serving, list.Items[i].GetName())
				break
			}
		}
	}
	if len(serving) != 1 {
		t.Fatalf("console host %s is served by %d HTTPRoutes %v, want exactly 1 (the org-controller's %s adopted, not duplicated by %s)",
			host, len(serving), serving, orgCtrlRoute, catalystRoute)
	}
	if serving[0] != orgCtrlRoute {
		t.Errorf("surviving route = %q, want the pre-existing org-controller route %q", serving[0], orgCtrlRoute)
	}

	// Vacuity guard, the other direction: with NO org-controller route present
	// the emitter MUST still write its own — adoption may never degrade into
	// "never emit a route".
	dyn2 := fakeDynForConsoleTLS(t)
	h2 := newConsoleTLSHandler(t, dyn2)
	h2.provisionOrgConsoleTLS(ctx, store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "hw292.omani.works",
	})
	if _, err := dyn2.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Get(ctx, catalystRoute, metav1.GetOptions{}); err != nil {
		t.Fatalf("vacuity: with no org-controller route the emitter must create %s, got: %v", catalystRoute, err)
	}
}

// TestProvisionOrgConsoleTLS_DoesNotAdoptRouteForAnotherHost — presence of a
// same-named route is not enough; it must actually serve THIS Org's console
// host. A stale/foreign route under the org-controller's name must NOT
// suppress the emitter's own route, or a single misnamed object would silently
// close the customer door.
func TestProvisionOrgConsoleTLS_DoesNotAdoptRouteForAnotherHost(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, dyn)
	ctx := context.Background()

	if _, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Create(ctx, orgControllerConsoleRoute("catalyst-ui-console-acme-omani-homes",
			"console.somebodyelse.omani.homes"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed foreign-host route: %v", err)
	}

	h.provisionOrgConsoleTLS(ctx, store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "hw292.omani.works",
	})

	if _, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Get(ctx, "catalyst-ui-acme-omani-homes", metav1.GetOptions{}); err != nil {
		t.Fatalf("emitter must still write its own route when the same-named route serves a different host: %v", err)
	}
}
