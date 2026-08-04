// org_console_tls_reconcile_5635_test.go — #5635, the two locks the issue asks
// for:
//
//  1. the per-Org console gateway surface is emitted for BOTH creation paths
//     (marketplace funnel and BSS/direct), in EVERY region; and
//  2. a SECOND reconcile of an Org whose region-B surface is missing ADDS it
//     — the backfill direction, which is what "reconciled, not write-once"
//     means.
//
// Both drive the CR-keyed reconcile pass, because the defect is at the trigger
// level: provisionOrgConsoleTLS already fans out to every region (#5511 / PR
// #5524), but it only ever ran for Orgs in catalyst-api's OWN provisioning
// store. A funnel Org has no such record — its CR is minted by the
// provisioning service (`openova.io/source: tenant-created-event`,
// core/services/provisioning/handlers/organization_create.go:162) and its
// surface is written by the org-controller, which has no per-region client.

package handler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// fakeDynForOrgConsoleReconcile is fakeDynForConsoleTLSWithApexPorts plus the
// Organization GVR, so the reconcile pass can List Organization CRs off the
// same fake the surface is written to. The Gateway is seeded through an
// explicit-GVR Create for the "gatewaies" pluralizer reason documented on
// fakeDynForConsoleTLSWithApexPorts.
func fakeDynForOrgConsoleReconcile(t *testing.T, orgs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		certificateGVR:    "CertificateList",
		consoleGatewayGVR: "GatewayList",
		httpRouteGVR:      "HTTPRouteList",
		organizationGVR(): "OrganizationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
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

// funnelOrgCR — an Organization CR shaped exactly like the marketplace-funnel
// door produces (organization_create.go:162 stamps
// `openova.io/source: tenant-created-event`; hw292's `uatco` carried precisely
// this). No catalyst-api provisioning record exists for such an Org.
func funnelOrgCR(slug, parentDomain string) *unstructured.Unstructured {
	return orgCRWithSource(slug, parentDomain, "tenant-created-event")
}

// bssOrgCR — an Organization CR shaped like the BSS/direct door produces
// (createOrgOrganizationCR). hw292's `r17probe` came through this door.
func bssOrgCR(slug, parentDomain string) *unstructured.Unstructured {
	return orgCRWithSource(slug, parentDomain, "org-tenant-pipeline")
}

func orgCRWithSource(slug, parentDomain, source string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name": slug,
			"labels": map[string]any{
				"openova.io/source":    source,
				"openova.io/tenant-id": "tid-" + slug,
			},
		},
		"spec": map[string]any{
			"slug":         slug,
			"displayName":  slug + " Co",
			"sovereignRef": "hw292.omani.works",
			"tenantPublic": map[string]any{
				"parentDomain": parentDomain,
				"subdomain":    slug,
			},
		},
	}}
}

// consoleSurfacePresent reports whether a region cluster carries BOTH halves of
// an Org's console gateway surface: a Gateway listener SSA patch was issued for
// this Org's listener pair, and an HTTPRoute in catalyst-system serves the Org's
// console host (under EITHER producer's name — the gateway edge does not care
// which name serves the host, only that one does).
func consoleSurfacePresent(t *testing.T, dyn *dynamicfake.FakeDynamicClient, consoleHost string) (listener bool, route string) {
	t.Helper()
	for _, a := range dyn.Actions() {
		if a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
			if pa, ok := a.(interface{ GetName() string }); ok && pa.GetName() == consoleGatewayName {
				listener = true
			}
		}
	}
	list, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HTTPRoutes: %v", err)
	}
	for i := range list.Items {
		hosts, _, _ := unstructured.NestedStringSlice(list.Items[i].Object, "spec", "hostnames")
		for _, h := range hosts {
			if h == consoleHost {
				return listener, list.Items[i].GetName()
			}
		}
	}
	return listener, ""
}

// newReconcileHandler wires a Handler whose host region is hostDyn/hostCore and
// whose k8sCache registers one secondary region (secDyn/secCore) on a chroot,
// which is the shape orgConsoleTLSTargets fans out over.
func newReconcileHandler(t *testing.T,
	hostDyn, secDyn *dynamicfake.FakeDynamicClient,
	hostCore, secCore *k8sfake.Clientset,
) *Handler {
	t.Helper()
	h := newConsoleTLSHandlerWithCore(t, hostDyn, hostCore)
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "hw292.omani.works"})
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clusters: []k8scache.ClusterRef{
			{ID: "dep292", DynamicClient: hostDyn, CoreClient: hostCore},
			{ID: "dep292-me-east-215-b-1", DynamicClient: secDyn, CoreClient: secCore},
		},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	return h
}

func issuedOrgWildcardSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: consoleCertNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("issued"), "tls.key": []byte("key")},
	}
}

// TestReconcileOrgConsoleTLSFromOrgCRs_BothCreationPaths — #5635 lock 1.
//
// Two Orgs, one per creation path, exist ONLY as Organization CRs (no
// catalyst-api provisioning record for either — that is the whole point: the
// funnel door never writes one). After a single reconcile pass BOTH Orgs' console
// surface must be present in BOTH regions.
//
// On the unfixed tree there is no CR-keyed trigger at all, so a funnel Org's
// surface is never emitted by catalyst-api in ANY region.
func TestReconcileOrgConsoleTLSFromOrgCRs_BothCreationPaths(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works") // chroot gate for the fan-out

	funnel := funnelOrgCR("uatco", "omani.homes")
	direct := bssOrgCR("r17probe", "omani.homes")

	hostDyn := fakeDynForOrgConsoleReconcile(t, funnel, direct)
	secDyn := fakeDynForOrgConsoleReconcile(t)
	hostCore := k8sfake.NewSimpleClientset(
		issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"),
		issuedOrgWildcardSecret("org-wildcard-tls-r17probe-omani-homes"),
	)
	secCore := k8sfake.NewSimpleClientset()

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	for _, tc := range []struct{ path, host, cert string }{
		{"marketplace-funnel", "console.uatco.omani.homes", "org-wildcard-tls-uatco-omani-homes"},
		{"bss-direct", "console.r17probe.omani.homes", "org-wildcard-tls-r17probe-omani-homes"},
	} {
		for regionName, dyn := range map[string]*dynamicfake.FakeDynamicClient{
			"region-a-host": hostDyn, "region-b-secondary": secDyn,
		} {
			listener, route := consoleSurfacePresent(t, dyn, tc.host)
			if !listener {
				t.Errorf("[%s/%s] no Gateway listener apply for %s", tc.path, regionName, tc.host)
			}
			if route == "" {
				t.Errorf("[%s/%s] no HTTPRoute serves %s", tc.path, regionName, tc.host)
			}
		}
		// The issued cert secret must be mirrored into the secondary region —
		// a listener whose certificateRef resolves to nothing still closes the
		// door (per-region secret-split class #5394/#5406/#5414/#5416).
		if _, err := secCore.CoreV1().Secrets(consoleCertNamespace).
			Get(context.Background(), tc.cert, metav1.GetOptions{}); err != nil {
			t.Errorf("[%s] cert secret %s not mirrored into the secondary region: %v", tc.path, tc.cert, err)
		}
	}
}

// TestReconcileOrgConsoleTLSFromOrgCRs_BackfillsMissingSecondaryRegion —
// #5635 lock 2, the backfill direction.
//
// This is hw292's `uatco` exactly: region-A already carries the full surface
// written by the ORG-CONTROLLER (its own route name, its listener pair, the
// issued cert), region-B carries nothing, and the Org has no catalyst-api
// provisioning record. A reconcile pass over an ALREADY-EXISTING Org must ADD
// the missing region-B surface — write-once is the defect shape.
//
// It also asserts the pass does not duplicate region-A's surface: the region
// keeps exactly the one route it already had.
func TestReconcileOrgConsoleTLSFromOrgCRs_BackfillsMissingSecondaryRegion(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")

	const (
		host         = "console.uatco.omani.homes"
		orgCtrlRoute = "catalyst-ui-console-uatco-omani-homes" // tenant_route.go:141
		certName     = "org-wildcard-tls-uatco-omani-homes"
	)
	ctx := context.Background()

	hostDyn := fakeDynForOrgConsoleReconcile(t, funnelOrgCR("uatco", "omani.homes"))
	secDyn := fakeDynForOrgConsoleReconcile(t)

	// Region-A pre-state: the org-controller already served this Org.
	if _, err := hostDyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Create(ctx, orgControllerConsoleRoute(orgCtrlRoute, host), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed org-controller console route: %v", err)
	}
	hostCore := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret(certName))
	secCore := k8sfake.NewSimpleClientset()

	// Region-B pre-state assertion — the gap must actually be there, or the
	// backfill claim is vacuous.
	if _, route := consoleSurfacePresent(t, secDyn, host); route != "" {
		t.Fatalf("vacuity: secondary region already served %s before the reconcile (route %s)", host, route)
	}

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(ctx)

	listener, route := consoleSurfacePresent(t, secDyn, host)
	if !listener {
		t.Errorf("backfill: no Gateway listener apply reached the secondary region for %s", host)
	}
	if route == "" {
		t.Fatalf("backfill: secondary region still has NO HTTPRoute serving %s after a reconcile pass", host)
	}
	if _, err := secCore.CoreV1().Secrets(consoleCertNamespace).Get(ctx, certName, metav1.GetOptions{}); err != nil {
		t.Errorf("backfill: cert secret %s not mirrored into the secondary region: %v", certName, err)
	}

	// Region-A must be converged, not duplicated.
	list, err := hostDyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list host-region HTTPRoutes: %v", err)
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
	if len(serving) != 1 || serving[0] != orgCtrlRoute {
		t.Errorf("host region routes for %s = %v, want exactly [%s] (adopted, not duplicated)", host, serving, orgCtrlRoute)
	}
}

// TestOrgConsoleTLSRecordFromOrgCR_SkipCases — the CR→record derivation must
// refuse exactly what the emitter would refuse, so the loop can never accept an
// Org the emitter then skips. No pool parentDomain (single-domain Org, covered
// by the Sovereign apex wildcard) and a slug that fails orgSlugRE both yield
// no record.
func TestOrgConsoleTLSRecordFromOrgCR_SkipCases(t *testing.T) {
	noParent := funnelOrgCR("acme", "")
	unstructured.RemoveNestedField(noParent.Object, "spec", "tenantPublic", "parentDomain")
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
	}{
		{"no-pool-parent", noParent},
		{"invalid-slug-too-short", funnelOrgCR("ab", "omani.homes")},
		{"invalid-slug-leading-digit", funnelOrgCR("1acme", "omani.homes")},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := orgConsoleTLSRecordFromOrgCR(tc.obj, "hw292.omani.works"); ok {
				t.Errorf("expected skip for %s, got a provisioning record", tc.name)
			}
		})
	}
	// Positive control — a well-formed funnel Org DOES produce a record whose
	// names match what the org-controller emits for the same CR.
	rec, ok := orgConsoleTLSRecordFromOrgCR(funnelOrgCR("uatco", "omani.homes"), "hw292.omani.works")
	if !ok {
		t.Fatal("well-formed funnel Org produced no record")
	}
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok {
		t.Fatal("record did not resolve to console TLS names")
	}
	if names.ConsoleHost != "console.uatco.omani.homes" {
		t.Errorf("ConsoleHost = %q, want console.uatco.omani.homes", names.ConsoleHost)
	}
	if names.CertName != "org-wildcard-tls-uatco-omani-homes" {
		t.Errorf("CertName = %q, want org-wildcard-tls-uatco-omani-homes", names.CertName)
	}
	if names.HTTPSName != "console-https-uatco" || names.HTTPName != "console-http-uatco" {
		t.Errorf("listener names = %q/%q, want console-https-uatco/console-http-uatco", names.HTTPSName, names.HTTPName)
	}
	if names.RouteNameOrgController != "catalyst-ui-console-uatco-omani-homes" {
		t.Errorf("RouteNameOrgController = %q, want catalyst-ui-console-uatco-omani-homes (tenant_route.go:141)", names.RouteNameOrgController)
	}
}
