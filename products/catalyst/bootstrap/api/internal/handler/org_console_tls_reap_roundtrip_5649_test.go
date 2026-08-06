// org_console_tls_reap_roundtrip_5649_test.go — #5649, the producer→reaper
// ROUND-TRIP guard.
//
// WHY THIS EXISTS, on top of the #5364 fixture test next door.
//
// TestOrgConsoleTeardown_OrphanedOrg_BothProducersBothRegions_AllReaped seeds
// its orphans from HAND-WRITTEN fixtures — `catalystAPIConsoleRoute`,
// `seedOrgWildcardCert`, `mirroredOrgSecret` and friends restate each
// producer's name+label contract in the test file. That makes the test a COPY
// of the contract, and a copy cannot notice when the original moves. Measured,
// not assumed: flipping catalyst-api's own emitter label
// `catalyst.openova.io/pool-parent` → `catalyst.openova.io/parent-zone` in
// orgConsoleTLSLabels (a plausible consolidation onto the org-controller's
// vocabulary — it stamps `parent-zone` at tenant_console_tls.go:363) left the
// ENTIRE internal/handler package green:
//
//	ok  github.com/.../internal/handler  256.073s
//
// while in production it made the #5511 mirrored per-Org TLS Secret in EVERY
// secondary region permanently unreapable — the mirror carries no
// `cert-manager.io/common-name` annotation, so there is no fallback behind the
// label the scan reads. Green suite, live leak, in exactly the class #5649 was
// filed for. (That specific hole is closed in this PR by
// orgZoneFromProducerLabels; this test is what makes the NEXT one visible.)
//
// WHAT THIS TEST DOES DIFFERENTLY. It writes nothing by hand. It runs the real
// reconcile pass so catalyst-api's OWN emitters produce the per-Org console
// surface in both regions, derives the expected names from the producer's own
// resolver (resolveOrgConsoleTLSNames — production code, not a restatement),
// deletes ONE Organization CR, runs the pass again, and asserts that
// everything the emitters wrote for the deleted Org is gone in EVERY region
// while everything they wrote for the LIVE Org is untouched. If a producer
// changes a name shape, a label, or a region fan-out and the reaper does not
// follow, this test goes red by construction.
//
// SCOPE, stated rather than implied. The fake dynamic client and the fake
// typed clientset are SEPARATE object stores, so a Namespace the app-surface
// emitter creates through the dynamic client is not visible to the typed
// client the namespace scan lists through. That is a harness artifact, not a
// production property (one apiserver backs both). Rather than paper over it
// with a hand-seeded duplicate, this test asserts the namespace contract at
// its real seam: the Namespace object ensureOrgBoundaryNamespaceForApps
// actually wrote is fed to orgNamespaceReapIdentity — the exact predicate
// scanOrgNamespaces applies — and must yield the Org's identity. Likewise the
// cert-manager-issued host Secret is injected by hand because cert-manager is
// an EXTERNAL producer; its shape is pinned to what hw292 carries live
// (labels: only `controller.cert-manager.io/fao`; annotations include
// `cert-manager.io/common-name: <slug>.<parent>`), and the object under test
// is the MIRROR catalyst-api derives from it.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// certManagerIssuedSecret — the EXTERNAL producer's shape, pinned to the live
// hw292 object `kube-system/org-wildcard-tls-<slug>-omani-homes`: cert-manager
// copies none of the Certificate's labels onto the backing Secret, so the
// `cert-manager.io/common-name` annotation is the only org identity on it.
func certManagerIssuedSecret(certName, orgZone string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      certName,
			Namespace: consoleCertNamespace,
			Labels:    map[string]string{"controller.cert-manager.io/fao": "true"},
			Annotations: map[string]string{
				"cert-manager.io/certificate-name": certName,
				"cert-manager.io/common-name":      orgZone,
				"cert-manager.io/alt-names":        "*." + orgZone + "," + orgZone,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("issued"), "tls.key": []byte("key")},
	}
}

// producerNames resolves one Org's artifact names through the PRODUCER's own
// resolver, from the same Organization CR the reconcile pass reads. Every
// expectation in this test comes from here — never from a string literal — so
// a producer-side rename moves the expectation with it and the assertion then
// measures only whether the REAPER followed.
func producerNames(t *testing.T, slug, parent, sovereignFQDN string) orgConsoleTLSNames {
	t.Helper()
	rec, ok := orgConsoleTLSRecordFromOrgCR(funnelOrgCR(slug, parent), sovereignFQDN)
	if !ok {
		t.Fatalf("producer refused to derive a console surface for %s.%s", slug, parent)
	}
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok {
		t.Fatalf("resolveOrgConsoleTLSNames refused %s.%s", slug, parent)
	}
	return names
}

// routeNamesInRegion lists the console HTTPRoute names a region actually
// holds, so the test can assert on OBSERVED state rather than on a guess.
func routeNamesInRegion(t *testing.T, dyn *dynamicfake.FakeDynamicClient) map[string]bool {
	t.Helper()
	list, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list console routes: %v", err)
	}
	out := map[string]bool{}
	for i := range list.Items {
		out[list.Items[i].GetName()] = true
	}
	return out
}

// applySSAListeners is the harness bridge for the ONE thing a fake apiserver
// cannot do. ensureOrgConsoleListener adds the per-Org listener pair with a
// server-side-apply Patch (org_console_tls.go:791), and `dynamicfake` records
// ApplyPatchType patches without performing the name-keyed list merge a real
// apiserver performs — so on a fake the Gateway object never grows the pair.
// The #5635 tests work around this by asserting on the ACTION log; the reap,
// however, scans and strips OBJECT state, so the round trip needs the merged
// object.
//
// This performs exactly that merge, and it takes the listener definitions from
// the producer's OWN recorded patch bytes — nothing here is retyped from the
// emitter. A producer-side rename of the listener name or hostname therefore
// still flows through to the assertions unchanged.
func applySSAListeners(t *testing.T, dyn *dynamicfake.FakeDynamicClient) {
	t.Helper()
	ctx := context.Background()
	gw, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Get(ctx, consoleGatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get console gateway: %v", err)
	}
	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read gateway listeners: %v", err)
	}
	byName := map[string]bool{}
	for _, l := range listeners {
		if lm, ok := l.(map[string]any); ok {
			if n, ok := lm["name"].(string); ok {
				byName[n] = true
			}
		}
	}
	merged := false
	for _, a := range dyn.Actions() {
		pa, ok := a.(k8stesting.PatchAction)
		if !ok || pa.GetPatchType() != types.ApplyPatchType ||
			pa.GetResource() != consoleGatewayGVR || pa.GetName() != consoleGatewayName {
			continue
		}
		var applied map[string]any
		if err := json.Unmarshal(pa.GetPatch(), &applied); err != nil {
			t.Fatalf("decode recorded SSA patch: %v", err)
		}
		patched, _, err := unstructured.NestedSlice(applied, "spec", "listeners")
		if err != nil {
			t.Fatalf("read listeners out of the recorded SSA patch: %v", err)
		}
		for _, l := range patched {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			n, _ := lm["name"].(string)
			if n == "" || byName[n] {
				continue
			}
			byName[n] = true
			listeners = append(listeners, lm)
			merged = true
		}
	}
	if !merged {
		return
	}
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		t.Fatalf("set merged gateway listeners: %v", err)
	}
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Update(ctx, gw, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("write merged gateway: %v", err)
	}
}

// TestOrgConsoleTeardown_ProducerRoundTrip_EveryEmittedArtifactIsReaped —
// emit with the real producers, delete the Org, reap, and assert the round
// trip closes in every region.
//
// Direction 1 (reaped): every artifact catalyst-api's emitters wrote for
// `ghostco` is gone in BOTH regions after the CR is deleted.
// Direction 2 (never reaped): every artifact the SAME emitters wrote for the
// still-live `liveco` survives the same pass, in both regions.
func TestOrgConsoleTeardown_ProducerRoundTrip_EveryEmittedArtifactIsReaped(t *testing.T) {
	const (
		sovereignFQDN = "hw292.omani.works"
		parent        = "omani.homes"
		liveSlug      = "liveco"
		ghostSlug     = "ghostco"
	)
	t.Setenv("SOVEREIGN_FQDN", sovereignFQDN) // chroot gate for the region fan-out
	ctx := context.Background()

	liveNames := producerNames(t, liveSlug, parent, sovereignFQDN)
	ghostNames := producerNames(t, ghostSlug, parent, sovereignFQDN)

	hostDyn := fakeDynForOrgConsoleReconcile(t,
		funnelOrgCR(liveSlug, parent), bssOrgCR(ghostSlug, parent))
	secDyn := fakeDynForOrgConsoleReconcile(t) // secondaries carry no Organization CRD (live hw292)
	hostCore := k8sfake.NewSimpleClientset()
	secCore := k8sfake.NewSimpleClientset()
	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)

	// ── PASS 1: the real emitters produce the surface in both regions. ──
	h.reconcileOrgConsoleTLSOnce(ctx)

	// cert-manager (external) issues the backing Secrets for the Certificates
	// pass 1 created, so pass 2's mirror has something to copy.
	for _, n := range []orgConsoleTLSNames{liveNames, ghostNames} {
		if !certExists(t, hostDyn, n.CertName) {
			t.Fatalf("producer did not create Certificate %s in the host region — fixture cannot proceed", n.CertName)
		}
		if _, err := hostCore.CoreV1().Secrets(consoleCertNamespace).
			Create(ctx, certManagerIssuedSecret(n.CertName, n.OrgZone), metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed cert-manager-issued Secret %s: %v", n.CertName, err)
		}
	}

	// ── PASS 2: the mirror emitter copies the issued Secrets to secondaries. ──
	h.reconcileOrgConsoleTLSOnce(ctx)

	// Materialize the SSA listener merge a real apiserver would have done, from
	// the producer's own recorded patch bytes (see applySSAListeners).
	applySSAListeners(t, hostDyn)
	applySSAListeners(t, secDyn)

	// The app-surface emitter's Namespace contract, asserted at its real seam
	// (see the file header on why this is a predicate check, not a store check).
	if err := ensureOrgBoundaryNamespaceForApps(ctx, secDyn, ghostNames,
		mustOrgRecord(t, ghostSlug, parent, sovereignFQDN)); err != nil {
		t.Fatalf("app-surface emitter could not create the secondary boundary Namespace: %v", err)
	}
	producedNS, err := secDyn.Resource(namespacesGVR()).Get(ctx, ghostNames.Slug, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read back the Namespace the app-surface emitter wrote: %v", err)
	}
	if identity, ok := orgNamespaceReapIdentity(producedNS.GetName(), producedNS.GetLabels()); !ok {
		t.Errorf("the Namespace ensureOrgBoundaryNamespaceForApps writes into a secondary region is NOT a reap candidate — "+
			"labels %v fail orgNamespaceReapIdentity, so a deleted Org's boundary namespace (and every mirrored app route + "+
			"ClusterMesh stub inside it) leaks in that region forever (#5649 #5635)", producedNS.GetLabels())
	} else if identity != ghostNames.Slug {
		t.Errorf("app-surface Namespace resolves to reap identity %q, want the Org slug %q — the reaper would key it to the wrong Org",
			identity, ghostNames.Slug)
	}

	// ── VACUITY: the producers really did write everything, in both regions. ──
	type artifact struct {
		what    string
		present bool
	}
	preState := func(n orgConsoleTLSNames) []artifact {
		hostRoutes, secRoutes := routeNamesInRegion(t, hostDyn), routeNamesInRegion(t, secDyn)
		hostListeners, secListeners := gatewayListenerNames(t, hostDyn), gatewayListenerNames(t, secDyn)
		return []artifact{
			{fmt.Sprintf("host route %s", n.RouteName), hostRoutes[n.RouteName]},
			{fmt.Sprintf("secondary route %s", n.RouteName), secRoutes[n.RouteName]},
			{fmt.Sprintf("host listener %s", n.HTTPSName), hostListeners[n.HTTPSName]},
			{fmt.Sprintf("host listener %s", n.HTTPName), hostListeners[n.HTTPName]},
			{fmt.Sprintf("secondary listener %s", n.HTTPSName), secListeners[n.HTTPSName]},
			{fmt.Sprintf("secondary listener %s", n.HTTPName), secListeners[n.HTTPName]},
			{fmt.Sprintf("host Certificate %s", n.CertName), certExists(t, hostDyn, n.CertName)},
			{fmt.Sprintf("host issued Secret %s", n.CertName), secretExists(t, hostCore, n.CertName)},
			{fmt.Sprintf("secondary mirrored Secret %s", n.CertName), secretExists(t, secCore, n.CertName)},
		}
	}
	for _, n := range []orgConsoleTLSNames{liveNames, ghostNames} {
		for _, a := range preState(n) {
			if !a.present {
				t.Fatalf("vacuity: the producers never wrote %s — a later 'reaped' assertion would pass on nothing", a.what)
			}
		}
	}

	// ── Delete the Organization CR. Nothing else. ──
	if err := hostDyn.Resource(organizationGVR()).
		Delete(ctx, ghostSlug, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete Organization CR %s: %v", ghostSlug, err)
	}

	// ── PASS 3: the reap. ──
	h.reconcileOrgConsoleTLSOnce(ctx)

	// Direction 1 — every producer-emitted ghostco artifact is gone, per region.
	var leaked []string
	for _, a := range preState(ghostNames) {
		if a.present {
			leaked = append(leaked, a.what)
		}
	}
	if len(leaked) > 0 {
		sort.Strings(leaked)
		for _, l := range leaked {
			t.Errorf("deleted Org %s: the producers wrote it and the reaper left it behind — %s (#5649)", ghostSlug, l)
		}
	}

	// Direction 2 — the SURVIVOR is untouched. A deletion path is only correct
	// if the live Org's identical surface comes through the same pass intact.
	for _, a := range preState(liveNames) {
		if !a.present {
			t.Errorf("live Org %s lost %s in the same pass that reaped %s — over-eager reaper (#5649)",
				liveSlug, a.what, ghostSlug)
		}
	}
	// And the Sovereign's own apex listeners, which parse like a per-Org pair.
	for region, dyn := range map[string]*dynamicfake.FakeDynamicClient{"host": hostDyn, "secondary": secDyn} {
		names := gatewayListenerNames(t, dyn)
		if !names[consoleApexListenerHTTPSName] || !names[consoleApexListenerHTTPName] {
			t.Errorf("[%s] the Sovereign's apex listener pair was stripped by the reap pass", region)
		}
	}
}

// mustOrgRecord builds the provisioning record the emitters consume from the
// Organization CR, via the production derivation.
func mustOrgRecord(t *testing.T, slug, parent, sovereignFQDN string) store.OrganizationProvisionRecord {
	t.Helper()
	rec, ok := orgConsoleTLSRecordFromOrgCR(funnelOrgCR(slug, parent), sovereignFQDN)
	if !ok {
		t.Fatalf("orgConsoleTLSRecordFromOrgCR refused %s.%s", slug, parent)
	}
	return rec
}

// TestOrgZoneFromProducerLabels_BothProducerVocabularies — the unit half of
// the same contract: the reap's label-derived identity must accept BOTH
// producers' parent-label spellings, because the mirrored TLS Secret has no
// annotation fallback behind it.
//
// catalyst-api stamps `catalyst.openova.io/pool-parent`
// (org_console_tls.go orgConsoleTLSLabels); the org-controller stamps
// `catalyst.openova.io/parent-zone` (tenant_console_tls.go:363,
// tenant_route.go:149). Before this PR the Certificate scan read both and the
// Secret scan read only the first.
func TestOrgZoneFromProducerLabels_BothProducerVocabularies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name: "catalyst-api vocabulary (pool-parent)",
			labels: map[string]string{
				"catalyst.openova.io/org-subdomain": "uatco",
				"catalyst.openova.io/pool-parent":   "omani.homes",
			},
			want: "uatco.omani.homes",
		},
		{
			name: "org-controller vocabulary (parent-zone) — the live hw292 Certificate shape",
			labels: map[string]string{
				"catalyst.openova.io/org-subdomain": "uatco",
				"catalyst.openova.io/parent-zone":   "omani.homes",
			},
			want: "uatco.omani.homes",
		},
		{
			name:   "no identity at all — must stay underivable so nothing is reaped",
			labels: map[string]string{"controller.cert-manager.io/fao": "true"},
			want:   "",
		},
		{
			name:   "slug without any parent — underivable, caller falls through",
			labels: map[string]string{"catalyst.openova.io/org-subdomain": "uatco"},
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgZoneFromProducerLabels(tc.labels); got != tc.want {
				t.Errorf("orgZoneFromProducerLabels() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOrgNamespaceReapIdentity_OnlyPositivelyIdentifiedOrgNamespaces — the
// never-reap half of the namespace predicate. The `org-tenants` branch keys on
// the namespace NAME, so its label VALUE must be pinned: hw292's live org
// namespaces carry `kustomize.toolkit.fluxcd.io/name:
// catalyst-tenant-<slug>-vcluster`, and a key-only match would make any
// Flux-rendered namespace a name-keyed reap candidate.
func TestOrgNamespaceReapIdentity_OnlyPositivelyIdentifiedOrgNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name   string
		nsName string
		labels map[string]string
		wantID string
		wantOK bool
	}{
		{
			name:   "org-controller boundary ns",
			nsName: "uatco",
			labels: map[string]string{"openova.io/organization": "uatco"},
			wantID: "uatco", wantOK: true,
		},
		{
			name:   "catalyst-api ensureOrgNamespace ns",
			nsName: "uatco",
			labels: map[string]string{"catalyst.openova.io/org": "uatco"},
			wantID: "uatco", wantOK: true,
		},
		{
			name:   "funnel-materialized ns via the org-tenants Kustomization",
			nsName: "uatco",
			labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "org-tenants"},
			wantID: "uatco", wantOK: true,
		},
		{
			name:   "some OTHER Kustomization's namespace — the live hw292 label value",
			nsName: "shared-pg",
			labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "catalyst-tenant-uatco-vcluster"},
			wantOK: false,
		},
		{
			name:   "protected system namespace even when mislabelled",
			nsName: "flux-system",
			labels: map[string]string{"openova.io/organization": "flux-system"},
			wantOK: false,
		},
		{
			name:   "unlabelled namespace",
			nsName: "monitoring-extra",
			labels: map[string]string{},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := orgNamespaceReapIdentity(tc.nsName, tc.labels)
			if ok != tc.wantOK {
				t.Fatalf("orgNamespaceReapIdentity(%q) ok = %v, want %v", tc.nsName, ok, tc.wantOK)
			}
			if ok && id != tc.wantID {
				t.Errorf("orgNamespaceReapIdentity(%q) = %q, want %q", tc.nsName, id, tc.wantID)
			}
		})
	}
}
