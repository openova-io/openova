// org_app_standby_regions_6268_test.go — #6268 / UAT row 60.
//
// Every test here drives ONLY pre-existing entry points (reconcileOrgConsoleTLSOnce,
// newReconcileHandler, newConsoleTLSHandlerWithCore) and declares its own GVRs,
// so the file COMPILES UNCHANGED against origin/main and the failures it reports
// there are RUNTIME failures describing the live defect — never build errors
// about a symbol the fix introduces. That is what makes the red/green
// transition evidence rather than tautology.
//
//	lock 1  TestReconcileOrgAppStandby_SecondaryRegionCarriesTheHotStandbyLeg
//	        RED on origin/main. Six assertions, each a DISTINCT property, and
//	        ordered so the substantive one (does the leg exist in region B at
//	        all?) is evaluated BEFORE the cheaper label/marker checks — a
//	        producer that stamped the right labels on nothing must not be able
//	        to short-circuit ahead of the check that matters.
//
//	        Each was proven falsifiable INDIVIDUALLY by mutating one behaviour
//	        of the producer at a time: keeping `replicas` reddens assertion 2
//	        alone, skipping the chart-source projection reddens 4 alone, and
//	        dropping the delivery label reddens 5 alone. That sweep is also what
//	        found a VACUOUS assertion in an earlier draft of this file — a
//	        "carries no spec.kubeConfig" check that stayed green when the strip
//	        was removed, because this fixture never had one. It now lives in
//	        lock 7 on a fixture that does.
//
//	lock 2  TestReconcileOrgAppStandby_ColdStandbyKeepsItsScaleDown
//	        RED on origin/main (the leg is absent). Pins the half of lock 1
//	        that a blanket "always drop replicas" would break: an
//	        active-passive standby is COLD by definition and keeps replicas: 0.
//
//	lock 3  TestReconcileOrgAppStandby_SameRegionStandbyIsNotProjected
//	        PASSES on origin/main AND with the fix. The control against
//	        "project every passive leg": a standby whose cluster ID shares the
//	        active leg's region has no cross-region delivery to make.
//
//	lock 4  TestReconcileOrgAppStandby_PassiveWithNoActiveLegIsNotProjected
//	        PASSES on both. A placement with no primary has no derivable host
//	        region; guessing one would project a standby of nothing.
//
//	lock 5  TestReconcileOrgAppStandby_SingleRegionWritesNothingExtra
//	        PASSES on both. Anti-suppression: a single-region Sovereign gains
//	        no objects, while the console surface is still produced.
//
//	lock 6  TestReconcileOrgAppStandby_ReapsAProjectionWhoseSourceLegIsGone
//	        RED on origin/main. Level-triggered in the delete direction too.
//
//	lock 7  TestReconcileOrgAppStandby_DeliveredLegCarriesNoKubeConfig
//	        RED on origin/main, and RED against a producer that copies the spec
//	        verbatim. Uses a vCluster-placed NON-CNPG app, which is the only
//	        shape that HAS a kubeConfig to strip.
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

// GVRs declared LOCALLY, not imported from the fix, so this file has no
// compile-time dependency on anything the fix adds.
var (
	standbyHRGVR   = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	standbyRepoGVR = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}
	standbySvcGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
)

const (
	standbyOrgSlug   = "walkfour"
	standbyOrgParent = "omani.homes"
	standbyAppName   = "r60fresh"
	standbyActiveHR  = "r60fresh-rtz-a"
	standbyPassiveHR = "r60fresh-rtz-b"
	standbyChartRepo = "openova-catalog"
	standbyRepoNS    = "flux-system"
)

// fakeDynForOrgAppStandby is the console-reconcile fake plus the Flux
// HelmRelease + HelmRepository GVRs, so a region cluster can hold per-cluster
// fan-out HelmReleases and the chart source they resolve through.
func fakeDynForOrgAppStandby(t *testing.T, orgs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			certificateGVR:    "CertificateList",
			consoleGatewayGVR: "GatewayList",
			httpRouteGVR:      "HTTPRouteList",
			organizationGVR(): "OrganizationList",
			namespacesGVR():   "NamespaceList",
			standbySvcGVR:     "ServiceList",
			standbyHRGVR:      "HelmReleaseList",
			standbyRepoGVR:    "HelmRepositoryList",
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
				map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS", "hostname": "*.hw296.omani.works"},
				map[string]any{"name": "console-http", "port": int64(8080), "protocol": "HTTP", "hostname": "*.hw296.omani.works"},
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

// fanoutHR builds one per-cluster fan-out HelmRelease exactly as the
// application-controller renders it — the shape read LIVE off hw296
// (dep e689e3b34a75fdec) for walkfour/r60fresh-rtz-b.
func fanoutHR(name, ns, app, cluster, role, topology string, values map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]any{
				"catalyst.openova.io/app":          app,
				"catalyst.openova.io/cluster":      cluster,
				"catalyst.openova.io/role":         role,
				"catalyst.openova.io/topology":     topology,
				"catalyst.openova.io/organization": ns,
			},
		},
		"spec": map[string]any{
			"interval": "600s",
			"chart": map[string]any{
				"spec": map[string]any{
					"chart":   "bp-postgres",
					"version": "0.2.23",
					"sourceRef": map[string]any{
						"kind":      "HelmRepository",
						"name":      standbyChartRepo,
						"namespace": standbyRepoNS,
					},
				},
			},
		},
	}
	if values != nil {
		obj["spec"].(map[string]any)["values"] = values
	}
	return &unstructured.Unstructured{Object: obj}
}

// seedCatalogChartSource writes the HelmRepository a Catalog-provisioned app's
// HelmRelease resolves through. Region A of hw296 has it; region B does not,
// which is why the projection has to carry it.
func seedCatalogChartSource(t *testing.T, dyn *dynamicfake.FakeDynamicClient) {
	t.Helper()
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata":   map[string]any{"name": standbyChartRepo, "namespace": standbyRepoNS},
		"spec": map[string]any{
			"type":     "oci",
			"url":      "oci://ghcr.io/openova-io",
			"interval": "10m",
		},
	}}
	if _, err := dyn.Resource(standbyRepoGVR).Namespace(standbyRepoNS).
		Create(context.Background(), repo, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed HelmRepository %s/%s: %v", standbyRepoNS, standbyChartRepo, err)
	}
}

func seedFanoutHR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, hr *unstructured.Unstructured) {
	t.Helper()
	if _, err := dyn.Resource(standbyHRGVR).Namespace(hr.GetNamespace()).
		Create(context.Background(), hr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed HelmRelease %s/%s: %v", hr.GetNamespace(), hr.GetName(), err)
	}
}

func getHR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	hr, err := dyn.Resource(standbyHRGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, false
	}
	return hr, true
}

func countHRs(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns string) int {
	t.Helper()
	list, err := dyn.Resource(standbyHRGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil || list == nil {
		return 0
	}
	return len(list.Items)
}

// standbyOrgCR — a funnel Org CR on hw296's Sovereign FQDN.
func standbyOrgCR(slug, parent string) *unstructured.Unstructured {
	org := funnelOrgCR(slug, parent)
	_ = unstructured.SetNestedField(org.Object, "hw296.omani.works", "spec", "sovereignRef")
	return org
}

func newStandbyHandler(t *testing.T, hostDyn, secDyn *dynamicfake.FakeDynamicClient) *Handler {
	t.Helper()
	hostCore := k8sfake.NewSimpleClientset(
		issuedOrgWildcardSecret("org-wildcard-tls-" + standbyOrgSlug + "-omani-homes"))
	secCore := k8sfake.NewSimpleClientset()
	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "hw296.omani.works"})
	return h
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 1 — the substantive one.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_SecondaryRegionCarriesTheHotStandbyLeg reproduces
// hw296 walkfour/r60fresh exactly: an active-hot-standby Application whose two
// fan-out HelmReleases BOTH sit in region A, and a region B that has neither the
// Org namespace nor the leg nor the chart source.
//
// The assertions are ordered deliberately. Assertion 1 is the one the row is
// about — does the standby leg EXIST in the region its placement names — and it
// runs before every cheaper check, so a producer that stamped the right labels
// on nothing could not short-circuit ahead of it. (That ordering trap is real:
// a label check placed first would have let the `replicas` assertion go
// unevaluated and therefore unproven.)
//
// On origin/main NOTHING writes a per-Org HelmRelease into a secondary region,
// so assertion 1 fails and every later one fails with it.
func TestReconcileOrgAppStandby_SecondaryRegionCarriesTheHotStandbyLeg(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works") // chroot gate for the fan-out

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR(standbyActiveHR, standbyOrgSlug, standbyAppName,
		"rtz-A", "active", "active-hot-standby", map[string]any{
			"topology": map[string]any{"mode": "active-hot-standby"},
		}))
	// The live standby leg, byte-shaped: the COLD overlay on a HOT posture.
	seedFanoutHR(t, hostDyn, fanoutHR(standbyPassiveHR, standbyOrgSlug, standbyAppName,
		"rtz-B", "passive", "active-hot-standby", map[string]any{
			"_openova_standby": true,
			"replicas":         int64(0),
			"topology":         map[string]any{"mode": "active-hot-standby"},
		}))

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	// 1. SUBSTANTIVE — the standby leg exists in region B at all. This is the
	//    whole of UAT row 60's write half; on hw296 region-b walkfour holds
	//    ZERO HelmReleases and the namespace does not exist.
	hr, ok := getHR(t, secDyn, standbyOrgSlug, standbyPassiveHR)
	if !ok {
		t.Fatalf("#6268: the secondary region carries NO HelmRelease %s/%s — the standby leg "+
			"of an active-hot-standby Application never leaves the primary region, so the "+
			"Application is single-region in fact while its placement says two",
			standbyOrgSlug, standbyPassiveHR)
	}

	// 2. it is HOT — a streaming replica, not a rebuild-on-failover shell.
	//    `replicas: 0` is the COLD (active-passive) semantic and a workload
	//    scaled to zero cannot stream.
	if _, found, _ := unstructured.NestedFieldNoCopy(hr.Object, "spec", "values", "replicas"); found {
		t.Errorf("#6268: the delivered standby leg %s/%s still carries `replicas` in its values — "+
			"an active-hot-standby standby is DEFINED by streaming from the primary and a "+
			"replica scaled to zero cannot stream", standbyOrgSlug, standbyPassiveHR)
	}

	// 3. it is still marked as the standby. Charts whose standby semantic is a
	//    boolean rather than an integer (CNPG `replica.enabled`) read this and
	//    nothing else; dropping it would turn the leg into a second primary.
	if v, found, _ := unstructured.NestedBool(hr.Object, "spec", "values", "_openova_standby"); !found || !v {
		t.Errorf("#6268: the delivered standby leg %s/%s lost its `_openova_standby` marker "+
			"(found=%v value=%v) — without it a boolean-standby chart installs a second PRIMARY "+
			"in the secondary region", standbyOrgSlug, standbyPassiveHR, found, v)
	}

	// (The kubeConfig-stripping property is asserted in lock 7, on a fixture
	// that HAS one. Asserting it here would be vacuous: bp-postgres carries the
	// bp-cnpg companion label, so #4282 renders this leg with no kubeConfig at
	// all — the assertion could not fail no matter what the producer did.)

	// 4. the chart source it resolves through exists in that region. hw296's
	//    region B holds 64 bp-* HelmRepositories but NOT `openova-catalog`, so
	//    without this the leg would exist and install nothing — a leg that
	//    LOOKS delivered, which is worse than an absent one.
	if _, err := secDyn.Resource(standbyRepoGVR).Namespace(standbyRepoNS).
		Get(context.Background(), standbyChartRepo, metav1.GetOptions{}); err != nil {
		t.Errorf("#6268: the secondary region has no HelmRepository %s/%s (%v) — the standby "+
			"HelmRelease resolves no chart there and installs nothing while reporting as present",
			standbyRepoNS, standbyChartRepo, err)
	}

	// 5. the delivery is RECORDED on the object. #6287 stamps
	//    `local-undelivered` on the host-region copy precisely because it never
	//    left; a reader must be able to tell the two apart.
	if got := hr.GetLabels()["catalyst.openova.io/standby-delivery"]; got != "remote" {
		t.Errorf("#6268: delivered standby leg %s/%s carries standby-delivery=%q, want %q — "+
			"a delivered standby must be distinguishable from one that installed beside its "+
			"active peer", standbyOrgSlug, standbyPassiveHR, got, "remote")
	}

	// 6. the host region is untouched: the projection is ADDITIVE, and the
	//    active leg is still the single authority there.
	if n := countHRs(t, hostDyn, standbyOrgSlug); n != 2 {
		t.Errorf("#6268: host region now has %d HelmReleases in ns %s, want the 2 it started "+
			"with — the projection must not mutate the host region's fan-out", n, standbyOrgSlug)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 2 — the posture the hot fix must NOT swallow.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_ColdStandbyKeepsItsScaleDown pins the other half of
// lock 1's assertion 2. `active-passive` is the COLD posture by definition —
// rebuild-on-failover — so its standby must arrive in the secondary region with
// `replicas: 0` intact. A fix that dropped `replicas` unconditionally would pass
// lock 1 and fail here; one that never dropped it would fail lock 1 and pass
// here. Only the posture-aware fix passes both.
//
// RED on origin/main for the same reason as lock 1: the leg is absent entirely.
func TestReconcileOrgAppStandby_ColdStandbyKeepsItsScaleDown(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR(standbyActiveHR, standbyOrgSlug, standbyAppName,
		"rtz-A", "active", "active-passive", nil))
	seedFanoutHR(t, hostDyn, fanoutHR(standbyPassiveHR, standbyOrgSlug, standbyAppName,
		"rtz-B", "passive", "active-passive", map[string]any{
			"_openova_standby": true,
			"replicas":         int64(0),
		}))

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	hr, ok := getHR(t, secDyn, standbyOrgSlug, standbyPassiveHR)
	if !ok {
		t.Fatalf("#6268: the secondary region carries NO HelmRelease %s/%s — a cold standby "+
			"still has to exist in the region its placement names", standbyOrgSlug, standbyPassiveHR)
	}
	got, found, _ := unstructured.NestedInt64(hr.Object, "spec", "values", "replicas")
	if !found || got != 0 {
		t.Errorf("#6268: active-passive standby %s/%s arrived with replicas=%d found=%v, want 0 — "+
			"a COLD standby is rebuild-on-failover and must not be booted; only the "+
			"active-hot-standby posture drops the scale-down",
			standbyOrgSlug, standbyPassiveHR, got, found)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 3 — the control against "project every passive leg".
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_SameRegionStandbyIsNotProjected. Both legs name a
// cluster in the SAME region (`rtz-A`), which is a same-region standby: there is
// no cross-region delivery to make and copying it into another region would
// invent a placement the Application never declared.
//
// PASSES on origin/main (nothing is projected there at all) AND with the fix.
// Its job is to constrain the fix, not to describe the defect: a producer that
// projected on `role == passive` alone would pass lock 1 and fail this.
func TestReconcileOrgAppStandby_SameRegionStandbyIsNotProjected(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR("samereg-rtz-a", standbyOrgSlug, "samereg",
		"rtz-A", "active", "active-hot-standby", nil))
	seedFanoutHR(t, hostDyn, fanoutHR("samereg-rtz-a-2", standbyOrgSlug, "samereg",
		"rtz-A", "passive", "active-hot-standby", map[string]any{"replicas": int64(0)}))

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	if n := countHRs(t, secDyn, standbyOrgSlug); n != 0 {
		t.Errorf("#6268: a SAME-region standby was projected into a secondary region "+
			"(%d HelmReleases in ns %s, want 0) — both legs name a cluster in one region, so "+
			"there is no cross-region delivery to make", n, standbyOrgSlug)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 4 — a placement with no primary.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_PassiveWithNoActiveLegIsNotProjected. #6287 fixed a
// renderer fallback that rendered TWO standbys and NO primary for a
// multi-cluster variant with no Roles map. If such an Application still reaches
// this producer, its host region is not derivable from its own legs — so the
// standby is NOT projected and the Application is named in the log rather than
// having a region guessed for it.
//
// PASSES on both trees; it constrains the fix.
func TestReconcileOrgAppStandby_PassiveWithNoActiveLegIsNotProjected(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR("noprimary-rtz-a", standbyOrgSlug, "noprimary",
		"rtz-A", "passive", "active-hot-standby", map[string]any{"replicas": int64(0)}))
	seedFanoutHR(t, hostDyn, fanoutHR("noprimary-rtz-b", standbyOrgSlug, "noprimary",
		"rtz-B", "passive", "active-hot-standby", map[string]any{"replicas": int64(0)}))

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	if n := countHRs(t, secDyn, standbyOrgSlug); n != 0 {
		t.Errorf("#6268: an Application with NO active leg had a standby projected anyway "+
			"(%d HelmReleases in ns %s, want 0) — there is no primary for it to be a standby OF, "+
			"and picking a host region by guess would make the guess authoritative",
			n, standbyOrgSlug)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 5 — anti-suppression.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_SingleRegionWritesNothingExtra. On a Sovereign with
// no secondary region registered the pass must add nothing at all — while STILL
// producing the Org's console surface. A patch that disabled the reconcile loop
// outright would satisfy every "want 0" above and fail this.
//
// PASSES on origin/main AND with the fix.
func TestReconcileOrgAppStandby_SingleRegionWritesNothingExtra(t *testing.T) {
	// A real single-region Sovereign IS a chroot, so the no-op below is
	// attributable to "no secondary region registered", not to a closed gate.
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	dyn := fakeDynForOrgAppStandby(t, org)
	core := k8sfake.NewSimpleClientset(
		issuedOrgWildcardSecret("org-wildcard-tls-" + standbyOrgSlug + "-omani-homes"))

	seedCatalogChartSource(t, dyn)
	seedFanoutHR(t, dyn, fanoutHR(standbyActiveHR, standbyOrgSlug, standbyAppName,
		"rtz-A", "active", "active-hot-standby", nil))
	seedFanoutHR(t, dyn, fanoutHR(standbyPassiveHR, standbyOrgSlug, standbyAppName,
		"rtz-B", "passive", "active-hot-standby", map[string]any{
			"_openova_standby": true, "replicas": int64(0),
		}))

	// No k8sCache => orgConsoleTLSTargets yields the host region only.
	h := newConsoleTLSHandlerWithCore(t, dyn, core)
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "hw296.omani.works"})
	h.reconcileOrgConsoleTLSOnce(context.Background())

	// a. the console surface is still produced.
	if _, route := consoleSurfacePresent(t, dyn, "console."+standbyOrgSlug+"."+standbyOrgParent); route == "" {
		t.Errorf("#6268: single-region pass produced no console HTTPRoute — the standby half " +
			"must not disable the console half")
	}
	// b. exactly the two seeded HelmReleases; no third, no mutation in place.
	if n := countHRs(t, dyn, standbyOrgSlug); n != 2 {
		t.Errorf("#6268: single-region Sovereign has %d HelmReleases in ns %s, want exactly 2 "+
			"(the seeded fan-out) — the cross-region projection must be a no-op without a "+
			"secondary region", n, standbyOrgSlug)
	}
	// c. the standby leg's own values are untouched in the host region: while
	//    it is undelivered it stays COLD on purpose (#6287) — booting it hot
	//    beside its active peer would install a duplicate primary.
	hr, ok := getHR(t, dyn, standbyOrgSlug, standbyPassiveHR)
	if !ok {
		t.Fatalf("#6268: the host region's own standby leg %s/%s disappeared",
			standbyOrgSlug, standbyPassiveHR)
	}
	if got, found, _ := unstructured.NestedInt64(hr.Object, "spec", "values", "replicas"); !found || got != 0 {
		t.Errorf("#6268: the UNDELIVERED host-region standby leg %s/%s was booted "+
			"(replicas=%d found=%v, want 0) — a standby that never left its primary's cluster "+
			"must stay inert, or it becomes a second primary",
			standbyOrgSlug, standbyPassiveHR, got, found)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 7 — the kubeConfig strip, on a fixture that can actually falsify it.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_DeliveredLegCarriesNoKubeConfig.
//
// This lock exists because the obvious place for the assertion — lock 1 — could
// not fail there. `bp-postgres` carries the `bp-cnpg` companion label, so #4282
// renders its legs with NO kubeConfig at all; a "spec.kubeConfig is absent"
// check against that fixture passes whether or not the producer strips
// anything, which is an assertion that cannot fail. Proven, not assumed: a
// mutation that removed the strip left lock 1 green.
//
// So the source leg here is a vCluster-placed, NON-CNPG app — the shape that
// DOES carry `spec.kubeConfig.secretRef.name: vc-rtz`. That secretRef names a
// HOST-region Secret; Flux resolves a secretRef from the HelmRelease's own
// namespace, so carrying it into the secondary region would produce a HelmRelease
// that can never resolve its own credential. Worse, a kubeConfig that DID
// resolve would hand ownership of region B's standby back to region A — the
// design this producer deliberately does not implement.
//
// RED on origin/main (the leg is absent), and RED against a producer that
// copies the spec verbatim.
func TestReconcileOrgAppStandby_DeliveredLegCarriesNoKubeConfig(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR("vcapp-rtz-a", standbyOrgSlug, "vcapp",
		"rtz-A", "active", "active-hot-standby", nil))
	passive := fanoutHR("vcapp-rtz-b", standbyOrgSlug, "vcapp",
		"rtz-B", "passive", "active-hot-standby", map[string]any{
			"_openova_standby": true, "replicas": int64(0),
		})
	// The G92.1 #2674 vCluster pivot, exactly as the renderer emits it.
	_ = unstructured.SetNestedMap(passive.Object, map[string]any{
		"secretRef": map[string]any{"name": "vc-rtz", "key": "config"},
	}, "spec", "kubeConfig")
	seedFanoutHR(t, hostDyn, passive)

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	hr, ok := getHR(t, secDyn, standbyOrgSlug, "vcapp-rtz-b")
	if !ok {
		t.Fatalf("#6268: the secondary region carries NO HelmRelease %s/vcapp-rtz-b — a "+
			"vCluster-placed app's standby leg must be delivered too", standbyOrgSlug)
	}
	if name, found, _ := unstructured.NestedString(hr.Object, "spec", "kubeConfig", "secretRef", "name"); found {
		t.Errorf("#6268: the delivered standby leg %s/vcapp-rtz-b still carries "+
			"spec.kubeConfig.secretRef.name=%q — that Secret is a HOST-region object and Flux "+
			"resolves a secretRef from the HelmRelease's own namespace, so the leg can never "+
			"resolve its credential in the secondary region; and a kubeConfig that DID resolve "+
			"would hand ownership of this standby back to the region it exists to survive",
			standbyOrgSlug, name)
	}
	// Control on the same object: stripping kubeConfig must not have taken the
	// rest of the spec with it.
	if chart, found, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart"); !found || chart != "bp-postgres" {
		t.Errorf("#6268: the delivered standby leg %s/vcapp-rtz-b lost its chart (%q found=%v) — "+
			"only the kubeConfig block may be removed", standbyOrgSlug, chart, found)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// lock 6 — the delete direction.
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileOrgAppStandby_ReapsAProjectionWhoseSourceLegIsGone. A topology
// downgrade (active-hot-standby → singleton) or an Application deletion removes
// the passive leg in the host region. Without a delete pass the projected copy
// keeps running in the secondary region with nothing left to be a standby of —
// the orphan class #5364/#5649 exists to prevent.
//
// RED on origin/main: nothing reaps it, because nothing knows it is a
// projection.
func TestReconcileOrgAppStandby_ReapsAProjectionWhoseSourceLegIsGone(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw296.omani.works")

	org := standbyOrgCR(standbyOrgSlug, standbyOrgParent)
	hostDyn := fakeDynForOrgAppStandby(t, org)
	secDyn := fakeDynForOrgAppStandby(t, org)

	// Host region: the Application is now a SINGLETON — one active leg, no
	// passive leg at all.
	seedCatalogChartSource(t, hostDyn)
	seedFanoutHR(t, hostDyn, fanoutHR(standbyActiveHR, standbyOrgSlug, standbyAppName,
		"rtz-A", "active", "singleton", nil))

	// Secondary region: a previously-projected standby, carrying this
	// producer's component label.
	orphan := fanoutHR(standbyPassiveHR, standbyOrgSlug, standbyAppName,
		"rtz-B", "passive", "active-hot-standby", map[string]any{"_openova_standby": true})
	labels := orphan.GetLabels()
	labels["catalyst.openova.io/component"] = "org-app-standby-crossregion"
	labels["catalyst.openova.io/managed-by"] = "catalyst-api"
	orphan.SetLabels(labels)
	seedFanoutHR(t, secDyn, orphan)

	h := newStandbyHandler(t, hostDyn, secDyn)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	if _, ok := getHR(t, secDyn, standbyOrgSlug, standbyPassiveHR); ok {
		t.Errorf("#6268: the projected standby %s/%s survived in the secondary region after its "+
			"source leg was withdrawn in the host region — it keeps running with nothing to be "+
			"a standby of, and no other teardown path knows about it",
			standbyOrgSlug, standbyPassiveHR)
	}
}
