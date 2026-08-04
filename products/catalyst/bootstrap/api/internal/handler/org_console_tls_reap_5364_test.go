// org_console_tls_reap_5364_test.go — #5364 / #5649, both directions of the
// teardown contract:
//
//  1. an Org whose CR is GONE but whose artifacts — from BOTH producers, in
//     BOTH regions, including the org boundary Namespace — still exist is
//     fully reaped by one reconcile pass (the hw292 R17 residue, plus the
//     hw288 beta-corp namespace orphan, in one fixture); and
//  2. an Org that is alive and fully served loses NOTHING — the pass is a
//     no-op that issues zero deletes (the over-eager-reaper direction #5649
//     names explicitly).
//
// This file deliberately uses ONLY symbols that predate the reap fix, so it
// compiles against the unfixed tree and fails there at RUNTIME (the orphaned
// artifacts survive the pass) rather than at build time.
//
// Adversarial controls carried by the fixture:
//   - the Sovereign's own console route `catalyst-ui`
//     (console.<sovereignFQDN>) must survive — the bare name never matches
//     the per-Org prefix;
//   - a trap route `catalyst-ui-hw292-omani-works` whose hostname is the
//     Sovereign console host parses EXACTLY like a per-Org host
//     (console.<slug=hw292>.<parent=omani.works>) and must survive via the
//     FQDN guard — this is the assertion that stops the reaper from eating
//     the Sovereign's front door;
//   - the apex listener pair must survive every pass;
//   - a TLS Secret whose org identity is underivable (no labels, no
//     cert-manager annotation) must survive — never delete what cannot be
//     positively identified.

package handler

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stesting "k8s.io/client-go/testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// agedTime — older than any plausible reap grace window, so the age-grace
// guard can never mask a missing reap in these tests.
func agedTime() metav1.Time { return metav1.NewTime(time.Now().Add(-24 * time.Hour)) }

// catalystAPIConsoleRoute builds the console HTTPRoute exactly as
// catalyst-api's own emitter names+labels it (org_console_tls.go
// resolveOrgConsoleTLSNames / createOrgConsoleHTTPRoute):
// `catalyst-ui-<slug>-<parent-dashed>`. Hand-written so the test pins the
// producer contract literally, mirroring orgControllerConsoleRoute.
func catalystAPIConsoleRoute(name, host string) *unstructured.Unstructured {
	r := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         catalystConsoleNamespace,
			"creationTimestamp": agedTime().UTC().Format(time.RFC3339),
			"labels": map[string]any{
				"catalyst.openova.io/managed-by": "catalyst-api",
				"app.kubernetes.io/managed-by":   "catalyst-api",
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
	return r
}

// seedRoute Creates a route on a region fake, failing the test on error.
func seedRoute(t *testing.T, dyn *dynamicfake.FakeDynamicClient, route *unstructured.Unstructured) {
	t.Helper()
	if _, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Create(context.Background(), route, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed route %s: %v", route.GetName(), err)
	}
}

// seedOrgListenerPair appends a per-Org `console-https-<slug>`/
// `console-http-<slug>` listener pair (hostname `*.<slug>.<parent>`) onto the
// already-seeded console Gateway, exactly as either producer leaves it.
func seedOrgListenerPair(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug, parent string) {
	t.Helper()
	ctx := context.Background()
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(ctx, consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get seeded console gateway: %v", err)
	}
	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read seeded gateway listeners: %v", err)
	}
	wildcard := "*." + slug + "." + parent
	listeners = append(listeners,
		map[string]any{"name": "console-https-" + slug, "port": int64(8443), "protocol": "HTTPS", "hostname": wildcard,
			"tls": map[string]any{"mode": "Terminate", "certificateRefs": []any{
				map[string]any{"kind": "Secret", "name": "org-wildcard-tls-" + slug + "-omani-homes"},
			}},
		},
		map[string]any{"name": "console-http-" + slug, "port": int64(8080), "protocol": "HTTP", "hostname": wildcard},
	)
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		t.Fatalf("set seeded gateway listeners: %v", err)
	}
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Update(ctx, gw, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update seeded gateway: %v", err)
	}
}

// seedOrgWildcardCert Creates a per-Org wildcard Certificate carrying the
// producers' shared labels (both doors stamp catalyst.openova.io/org-subdomain).
func seedOrgWildcardCert(t *testing.T, dyn *dynamicfake.FakeDynamicClient, slug, parent string) string {
	t.Helper()
	name := "org-wildcard-tls-" + slug + "-omani-homes"
	c := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         consoleCertNamespace,
			"creationTimestamp": agedTime().UTC().Format(time.RFC3339),
			"labels": map[string]any{
				"catalyst.openova.io/org-subdomain": slug,
				"catalyst.openova.io/pool-parent":   parent,
			},
		},
		"spec": map[string]any{
			"secretName": name,
			"commonName": slug + "." + parent,
			"dnsNames":   []any{"*." + slug + "." + parent, slug + "." + parent},
		},
	}}
	if _, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Create(context.Background(), c, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed certificate %s: %v", name, err)
	}
	return name
}

// hostIssuedOrgSecret — the HOST-region shape: written by cert-manager, so it
// carries the cert-manager annotations but NONE of our labels (cert-manager
// does not copy Certificate labels onto the Secret).
func hostIssuedOrgSecret(slug, parent string) *corev1.Secret {
	name := "org-wildcard-tls-" + slug + "-omani-homes"
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: consoleCertNamespace,
			CreationTimestamp: agedTime(),
			Annotations: map[string]string{
				"cert-manager.io/certificate-name": name,
				"cert-manager.io/common-name":      slug + "." + parent,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("issued"), "tls.key": []byte("key")},
	}
}

// mirroredOrgSecret — the SECONDARY-region shape: written by
// mirrorOrgConsoleCertSecret, so it carries the producers' labels
// (orgConsoleTLSStringLabels) and no cert-manager annotations.
func mirroredOrgSecret(slug, parent string) *corev1.Secret {
	name := "org-wildcard-tls-" + slug + "-omani-homes"
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: consoleCertNamespace,
			CreationTimestamp: agedTime(),
			Labels: map[string]string{
				"catalyst.openova.io/org-subdomain": slug,
				"catalyst.openova.io/pool-parent":   parent,
				"catalyst.openova.io/managed-by":    "catalyst-api",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("mirrored"), "tls.key": []byte("key")},
	}
}

// orgBoundaryNamespace — the org-controller's boundary ns shape
// (core/controllers/organization/internal/gitops/manifests.go: labels
// openova.io/organization=<slug>). Deleting it is what reaps the
// host-deployed bp-keycloak + tenant HelmReleases inside it (#5364).
func orgBoundaryNamespace(slug string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              slug,
		CreationTimestamp: agedTime(),
		Labels: map[string]string{
			"openova.io/organization": slug,
			"openova.io/managed-by":   "catalyst",
		},
	}}
}

func routeExists(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name string) bool {
	t.Helper()
	_, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func certExists(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name string) bool {
	t.Helper()
	_, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func secretExists(t *testing.T, core *k8sfake.Clientset, name string) bool {
	t.Helper()
	_, err := core.CoreV1().Secrets(consoleCertNamespace).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func namespaceExists(t *testing.T, core *k8sfake.Clientset, name string) bool {
	t.Helper()
	_, err := core.CoreV1().Namespaces().Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

// gatewayListenerNames reads the live listener NAME set off a region's
// console Gateway spec.
func gatewayListenerNames(t *testing.T, dyn *dynamicfake.FakeDynamicClient) map[string]bool {
	t.Helper()
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(context.Background(), consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get console gateway: %v", err)
	}
	names := map[string]bool{}
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	for _, l := range listeners {
		if lm, ok := l.(map[string]any); ok {
			if n, ok := lm["name"].(string); ok {
				names[n] = true
			}
		}
	}
	return names
}

// TestOrgConsoleTeardown_OrphanedOrg_BothProducersBothRegions_AllReaped —
// direction 1, the test that MUST FAIL on the unfixed tree.
//
// Fixture: live Org `uatco` (CR present) fully served; deleted Org `ghostco`
// (NO CR) still carrying, hours later:
//   - host region: BOTH producers' route name shapes, the listener pair, the
//     Certificate, the cert-manager-issued TLS Secret, and the org boundary
//     Namespace (the #5364 namespace orphan — reaping it is what removes the
//     host-deployed bp-keycloak);
//   - secondary region: catalyst-api's route shape, the listener pair, the
//     mirrored TLS Secret, and the boundary Namespace.
//
// One reconcile pass must remove ALL of it, in BOTH regions, while leaving
// every live-Org and Sovereign-owned artifact untouched.
func TestOrgConsoleTeardown_OrphanedOrg_BothProducersBothRegions_AllReaped(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works") // chroot gate for the region fan-out

	const (
		liveHost         = "console.uatco.omani.homes"
		liveOrgCtrlRoute = "catalyst-ui-console-uatco-omani-homes"
		liveCert         = "org-wildcard-tls-uatco-omani-homes"
		ghostAPIRoute    = "catalyst-ui-ghostco-omani-homes"         // catalyst-api's shape (org_console_tls.go)
		ghostCtrlRoute   = "catalyst-ui-console-ghostco-omani-homes" // org-controller's shape (tenant_route.go:141)
		ghostHost        = "console.ghostco.omani.homes"
		ghostCert        = "org-wildcard-tls-ghostco-omani-homes"
		sovereignRoute   = "catalyst-ui"                     // the Sovereign's own console route — bare name
		trapRoute        = "catalyst-ui-hw292-omani-works"   // parses like a per-Org host; FQDN guard must protect it
		sovereignHost    = "console.hw292.omani.works"
	)
	ctx := context.Background()

	// ONLY uatco has an Organization CR — ghostco's was deleted hours ago.
	hostDyn := fakeDynForOrgConsoleReconcile(t, funnelOrgCR("uatco", "omani.homes"))
	secDyn := fakeDynForOrgConsoleReconcile(t)

	// Host region pre-state.
	seedRoute(t, hostDyn, orgControllerConsoleRoute(liveOrgCtrlRoute, liveHost))
	seedRoute(t, hostDyn, catalystAPIConsoleRoute(ghostAPIRoute, ghostHost))
	seedRoute(t, hostDyn, orgControllerConsoleRoute(ghostCtrlRoute, ghostHost))
	sovereign := catalystAPIConsoleRoute(sovereignRoute, sovereignHost)
	sovereign.SetName(sovereignRoute)
	seedRoute(t, hostDyn, sovereign)
	seedRoute(t, hostDyn, catalystAPIConsoleRoute(trapRoute, sovereignHost))
	seedOrgListenerPair(t, hostDyn, "uatco", "omani.homes")
	seedOrgListenerPair(t, hostDyn, "ghostco", "omani.homes")
	seedOrgWildcardCert(t, hostDyn, "uatco", "omani.homes")
	seedOrgWildcardCert(t, hostDyn, "ghostco", "omani.homes")
	hostCore := k8sfake.NewSimpleClientset(
		hostIssuedOrgSecret("uatco", "omani.homes"),
		hostIssuedOrgSecret("ghostco", "omani.homes"),
		orgBoundaryNamespace("uatco"),
		orgBoundaryNamespace("ghostco"),
	)

	// Secondary region pre-state.
	seedRoute(t, secDyn, catalystAPIConsoleRoute(ghostAPIRoute, ghostHost))
	seedOrgListenerPair(t, secDyn, "ghostco", "omani.homes")
	secCore := k8sfake.NewSimpleClientset(
		mirroredOrgSecret("uatco", "omani.homes"),
		mirroredOrgSecret("ghostco", "omani.homes"),
		orgBoundaryNamespace("ghostco"),
	)

	// Vacuity control — the orphans must actually be there pre-pass, or every
	// "reaped" assertion below could pass on an empty fixture.
	for _, pre := range []struct {
		name string
		got  bool
	}{
		{"host ghost api-route", routeExists(t, hostDyn, ghostAPIRoute)},
		{"host ghost ctrl-route", routeExists(t, hostDyn, ghostCtrlRoute)},
		{"secondary ghost api-route", routeExists(t, secDyn, ghostAPIRoute)},
		{"host ghost listeners", gatewayListenerNames(t, hostDyn)["console-https-ghostco"]},
		{"secondary ghost listeners", gatewayListenerNames(t, secDyn)["console-https-ghostco"]},
		{"host ghost cert", certExists(t, hostDyn, ghostCert)},
		{"host ghost secret", secretExists(t, hostCore, ghostCert)},
		{"secondary ghost secret", secretExists(t, secCore, ghostCert)},
		{"host ghost namespace", namespaceExists(t, hostCore, "ghostco")},
		{"secondary ghost namespace", namespaceExists(t, secCore, "ghostco")},
	} {
		if !pre.got {
			t.Fatalf("vacuity: fixture missing pre-state %s", pre.name)
		}
	}

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(ctx)

	// ── Direction 1: every ghostco artifact reaped, in BOTH regions. ──
	if routeExists(t, hostDyn, ghostAPIRoute) {
		t.Errorf("host region still carries catalyst-api's orphaned route %s (the name shape teardownTenantRoute never targets)", ghostAPIRoute)
	}
	if routeExists(t, hostDyn, ghostCtrlRoute) {
		t.Errorf("host region still carries the org-controller's orphaned route %s", ghostCtrlRoute)
	}
	if routeExists(t, secDyn, ghostAPIRoute) {
		t.Errorf("secondary region still carries the orphaned route %s (teardown never ran in secondaries)", ghostAPIRoute)
	}
	for region, dyn := range map[string]*dynamicfake.FakeDynamicClient{"host": hostDyn, "secondary": secDyn} {
		names := gatewayListenerNames(t, dyn)
		if names["console-https-ghostco"] || names["console-http-ghostco"] {
			t.Errorf("[%s] orphaned ghostco listener pair still on the console Gateway", region)
		}
		// Harness + preservation control: the apex pair must ALWAYS survive.
		// If a pass ever clobbered the listener array wholesale, this catches
		// it — and makes the ghostco-absent assertions above non-vacuous.
		if !names[consoleApexListenerHTTPSName] || !names[consoleApexListenerHTTPName] {
			t.Errorf("[%s] apex listener pair missing after the pass — listener handling clobbered the Gateway", region)
		}
	}
	if certExists(t, hostDyn, ghostCert) {
		t.Errorf("host region still carries the orphaned Certificate %s", ghostCert)
	}
	if secretExists(t, hostCore, ghostCert) {
		t.Errorf("host region still carries the orphaned issued TLS Secret %s (cert-manager does not GC it)", ghostCert)
	}
	if secretExists(t, secCore, ghostCert) {
		t.Errorf("secondary region still carries the orphaned mirrored TLS Secret %s (the #5511 mirror has no deleter)", ghostCert)
	}
	if namespaceExists(t, hostCore, "ghostco") {
		t.Errorf("host region still carries the orphaned org Namespace ghostco — the host-deployed bp-keycloak inside it never dies (#5364)")
	}
	if namespaceExists(t, secCore, "ghostco") {
		t.Errorf("secondary region still carries the orphaned org Namespace ghostco")
	}

	// ── The never-reap direction, inside the same pass: everything live or
	// Sovereign-owned survives. ──
	if !routeExists(t, hostDyn, liveOrgCtrlRoute) {
		t.Errorf("live Org uatco's route %s was reaped — over-eager reaper (#5649)", liveOrgCtrlRoute)
	}
	if !routeExists(t, hostDyn, sovereignRoute) {
		t.Errorf("the Sovereign's own console route %s was reaped", sovereignRoute)
	}
	if !routeExists(t, hostDyn, trapRoute) {
		t.Errorf("trap route %s (console.<sovereignFQDN>) was reaped — the FQDN guard failed and the reaper would eat the Sovereign front door", trapRoute)
	}
	hostNames := gatewayListenerNames(t, hostDyn)
	if !hostNames["console-https-uatco"] || !hostNames["console-http-uatco"] {
		t.Errorf("live Org uatco's listener pair was stripped from the host Gateway")
	}
	if !certExists(t, hostDyn, liveCert) {
		t.Errorf("live Org uatco's Certificate %s was reaped", liveCert)
	}
	if !secretExists(t, hostCore, liveCert) {
		t.Errorf("live Org uatco's issued TLS Secret %s was reaped", liveCert)
	}
	if !secretExists(t, secCore, liveCert) {
		t.Errorf("live Org uatco's mirrored TLS Secret %s was reaped from the secondary", liveCert)
	}
	if !namespaceExists(t, hostCore, "uatco") {
		t.Errorf("live Org uatco's boundary Namespace was reaped")
	}
}

// TestOrgConsoleTeardown_AlreadyClean_NoOpNoDeletes — direction 2. An Org
// that is alive and fully served, with NO orphans anywhere, must come through
// a reconcile pass with zero deletes issued and every artifact intact — the
// level-triggered no-op that makes repeated passes safe.
func TestOrgConsoleTeardown_AlreadyClean_NoOpNoDeletes(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")

	const (
		liveHost  = "console.uatco.omani.homes"
		liveRoute = "catalyst-ui-console-uatco-omani-homes"
		liveCert  = "org-wildcard-tls-uatco-omani-homes"
	)
	ctx := context.Background()

	hostDyn := fakeDynForOrgConsoleReconcile(t, funnelOrgCR("uatco", "omani.homes"))
	secDyn := fakeDynForOrgConsoleReconcile(t)
	seedRoute(t, hostDyn, orgControllerConsoleRoute(liveRoute, liveHost))
	seedRoute(t, secDyn, orgControllerConsoleRoute(liveRoute, liveHost))
	seedOrgListenerPair(t, hostDyn, "uatco", "omani.homes")
	seedOrgListenerPair(t, secDyn, "uatco", "omani.homes")
	seedOrgWildcardCert(t, hostDyn, "uatco", "omani.homes")
	hostCore := k8sfake.NewSimpleClientset(
		hostIssuedOrgSecret("uatco", "omani.homes"),
		orgBoundaryNamespace("uatco"),
	)
	secCore := k8sfake.NewSimpleClientset(
		mirroredOrgSecret("uatco", "omani.homes"),
	)

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(ctx)

	// Zero delete verbs anywhere — the strongest no-op assertion the fakes
	// offer (state-only checks could miss a delete+recreate).
	countDeletes := func(actions []k8stesting.Action) int {
		n := 0
		for _, a := range actions {
			if a.GetVerb() == "delete" || a.GetVerb() == "delete-collection" {
				n++
			}
		}
		return n
	}
	if n := countDeletes(hostDyn.Actions()); n != 0 {
		t.Errorf("clean pass issued %d delete(s) on the host dynamic client, want 0", n)
	}
	if n := countDeletes(secDyn.Actions()); n != 0 {
		t.Errorf("clean pass issued %d delete(s) on the secondary dynamic client, want 0", n)
	}
	if n := countDeletes(hostCore.Actions()); n != 0 {
		t.Errorf("clean pass issued %d delete(s) on the host core client, want 0", n)
	}
	if n := countDeletes(secCore.Actions()); n != 0 {
		t.Errorf("clean pass issued %d delete(s) on the secondary core client, want 0", n)
	}

	// And the served surface is intact.
	if !routeExists(t, hostDyn, liveRoute) || !routeExists(t, secDyn, liveRoute) {
		t.Errorf("clean pass removed the live Org's console route")
	}
	names := gatewayListenerNames(t, hostDyn)
	if !names["console-https-uatco"] || !names["console-http-uatco"] {
		t.Errorf("clean pass stripped the live Org's listener pair")
	}
	if !certExists(t, hostDyn, liveCert) {
		t.Errorf("clean pass removed the live Org's Certificate")
	}
	if !secretExists(t, hostCore, liveCert) || !secretExists(t, secCore, liveCert) {
		t.Errorf("clean pass removed the live Org's TLS Secret")
	}
	if !namespaceExists(t, hostCore, "uatco") {
		t.Errorf("clean pass removed the live Org's boundary Namespace")
	}
}
