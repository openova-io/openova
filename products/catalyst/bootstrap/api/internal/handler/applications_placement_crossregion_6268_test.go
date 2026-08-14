// applications_placement_crossregion_6268_test.go — #6268 / UAT row 60,
// the PLACEMENT half of the same region-A-only seam.
//
// THE DEFECT. `augmentWithCNPGStandby` resolves the app's cnpg pair through the
// region-A-scoped `sovereignDynamicClient`, and #6271's Primary-emitting term
// (`mergeCNPGPairIntoTargets`) sits behind that resolution. A genuinely
// 2-region pair has its replica half on the OTHER apiserver, so the resolution
// always fails and the Primary term is unreachable — for the exact shape it was
// written for.
//
// Occupancy cannot cover for it either. `derivePlacementTargets` keys on
// `podBelongsToComponent`, and the Pods behind a Catalog-provisioned app carry
// the DATABASE's identity, not the app's: measured on hw296, `walkfour`'s
// postgres Pods are labelled `app.kubernetes.io/instance=postgres` /
// `app.kubernetes.io/name=postgresql` while the app is `r60fresh`.
// (`shared-pg` — the green control on that Sovereign — only escapes because its
// app name happens to EQUAL its CNPG instance label.) So the endpoint answered
// with one Standby carrying an empty `cluster` and `unresolvedPrimary: true`,
// which the Topology tab renders as `Pattern: not reported` with a single card.
//
// The control below reproduces that measured pre-fix answer byte-for-byte, so
// the fix's own test cannot pass vacuously.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const (
	xrRegionA = "hw-me-east-215-a-rtz-prod"
	xrRegionB = "hw-me-east-215-b-rtz-prod"
	xrNS      = "walkfour"
	xrApp     = "r60fresh"
)

// xrFixtureClients builds a fake dynamic client that serves the Pod + CNPG
// Cluster kinds the placement fan-out reads.
func xrFixtureClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, *kfake.Clientset) {
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "Pod"}, {Version: "v1", Kind: "PodList"},
		{Version: "v1", Kind: "Namespace"}, {Version: "v1", Kind: "NamespaceList"},
		{Version: "v1", Kind: "Node"}, {Version: "v1", Kind: "NodeList"},
		{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
		{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ClusterList"},
	} {
		if len(gvk.Kind) > 4 && gvk.Kind[len(gvk.Kind)-4:] == "List" {
			scheme.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
		} else {
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		}
	}
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:       "PodList",
		{Version: "v1", Resource: "namespaces"}: "NamespaceList",
		{Version: "v1", Resource: "nodes"}:      "NodeList",
		cnpgClusterGVR:                          "ClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...),
		kfake.NewSimpleClientset()
}

// newCrossRegionPlacementHandler stands up a Handler whose k8scache holds the
// deployment's TWO region clusters, each seeded with its own objects, and whose
// region-A dynamic client (the `sovereignDynamicClient` seam) sees only what
// region A actually holds.
func newCrossRegionPlacementHandler(
	t *testing.T, depID string,
	regionAObjs, regionBObjs []runtime.Object,
	regionAOnlyCRs ...runtime.Object,
) *Handler {
	t.Helper()
	clusterA := depID
	clusterB := depID + "-me-east-215-b-1"

	reg := k8scache.NewRegistry()
	_ = reg.Add(k8scache.Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = reg.Add(k8scache.Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}})
	_ = reg.Add(k8scache.Kind{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}})
	_ = reg.Add(k8scache.Kind{Name: "cnpgcluster", GVR: cnpgClusterGVR, Namespaced: true})

	dynA, coreA := xrFixtureClients(regionAObjs...)
	dynB, coreB := xrFixtureClients(regionBObjs...)
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: reg,
		Clusters: []k8scache.ClusterRef{
			{ID: clusterA, DynamicClient: dynA, CoreClient: coreA},
			{ID: clusterB, DynamicClient: dynB, CoreClient: coreB},
		},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, _, _ := f.List(clusterA, "cnpgcluster", labels.Everything())
		b, _, _ := f.List(clusterB, "cnpgcluster", labels.Everything())
		if len(a) >= 1 && len(b) >= len(regionBObjs) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	// The region-A `sovereignDynamicClient` seam — it sees the Continuum CR and
	// region A's own cnpg half, and NOT region B's.
	seed := append([]runtime.Object{}, regionAOnlyCRs...)
	seed = append(seed, regionAObjs...)
	factory, _ := fakeContinuumDynamicFactory(seed...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, depID)
	dep.Request.Regions = []provisioner.RegionSpec{{CloudRegion: xrRegionA}, {CloudRegion: xrRegionB}}
	return h
}

// callPlacementNS invokes the placement endpoint WITH the namespace query param
// the console passes.
func callPlacementNS(t *testing.T, h *Handler, depID, name, ns string) runtimePlacementResponse {
	t.Helper()
	router := chi.NewRouter()
	router.Get("/api/v1/sovereigns/{id}/applications/{name}/placement", h.HandleApplicationPlacement)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+depID+"/applications/"+name+"/placement?namespace="+ns, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("placement: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp runtimePlacementResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return resp
}

// xrContinuum is the app's Continuum CR — present in region A, naming the
// standby region. It is what makes the PRE-fix answer a one-card half-pair
// rather than an empty list, i.e. the exact hw296 reproduction.
func xrContinuum() *unstructured.Unstructured {
	return newContinuumUnstructured("dr-"+xrApp, xrNS, xrNS+"/"+xrApp, xrRegionA, []string{xrRegionB})
}

func targetByRole(targets []bpv1.PlacementTarget, role bpv1.DataRole) *bpv1.PlacementTarget {
	for i := range targets {
		if targets[i].Role == role {
			return &targets[i]
		}
	}
	return nil
}

// ── THE FIX ──────────────────────────────────────────────────────────────

// A pair whose halves live in two registered clusters must project BOTH legs
// with populated cluster ids, so the Topology tab can derive
// `active-hot-standby` instead of `not reported`.
func TestRow60_Placement_CrossRegionPairEmitsBothLegs_6268(t *testing.T) {
	primary, replica := newCNPGPairFixture("postgres", xrNS, xrRegionA, xrRegionB)
	h := newCrossRegionPlacementHandler(t, "dep-row60-xr-pair",
		[]runtime.Object{primary}, []runtime.Object{replica}, xrContinuum())

	resp := callPlacementNS(t, h, "dep-row60-xr-pair", xrApp, xrNS)

	// SUBSTANCE FIRST — a Primary must exist at all; without it derivePattern
	// returns `not reported` no matter what else is on the wire.
	p := targetByRole(resp.Targets, bpv1.DataRolePrimary)
	if p == nil {
		t.Fatalf("no Primary target: %+v", resp.Targets)
	}
	if p.Cluster == "" {
		t.Fatalf("Primary target has an EMPTY cluster — row 60's clause requires a populated cluster id; %+v", *p)
	}
	s := targetByRole(resp.Targets, bpv1.DataRoleStandby)
	if s == nil {
		t.Fatalf("no Standby target: %+v", resp.Targets)
	}
	if s.StandbyType != bpv1.StandbyHot {
		t.Fatalf("Standby type: got %q want Hot — a Cold standby derives active-passive, not active-hot-standby", s.StandbyType)
	}
	if s.Cluster == "" {
		t.Fatalf("Standby target has an EMPTY cluster — that is the pre-fix Continuum-region-only projection, not a resolved leg; %+v", *s)
	}
	if p.Region != xrRegionA || s.Region != xrRegionB {
		t.Fatalf("regions: primary %q standby %q, want %q / %q", p.Region, s.Region, xrRegionA, xrRegionB)
	}
	if resp.UnresolvedPrimary {
		t.Fatalf("unresolvedPrimary: got true after both legs resolved")
	}
	if !resp.DerivedFromRuntime {
		t.Fatalf("derivedFromRuntime: got false — both of this deployment's clusters were listed and the pair resolved; %+v", resp)
	}
}

// ── CONTROLS ─────────────────────────────────────────────────────────────

// The measured PRE-fix shape, reproduced exactly: region B holds no replica
// half, so only the Continuum's declared region is available and the answer is
// an honest half-pair — one Standby, EMPTY cluster, unresolvedPrimary true,
// derivedFromRuntime false. If the fix were removed, the test above would land
// here.
func TestRow60_Placement_NoRegionBHalfIsAnHonestHalfPair_6268(t *testing.T) {
	primary, _ := newCNPGPairFixture("postgres", xrNS, xrRegionA, xrRegionB)
	h := newCrossRegionPlacementHandler(t, "dep-row60-xr-nohalf",
		[]runtime.Object{primary}, nil, xrContinuum())

	resp := callPlacementNS(t, h, "dep-row60-xr-nohalf", xrApp, xrNS)

	if p := targetByRole(resp.Targets, bpv1.DataRolePrimary); p != nil {
		t.Fatalf("a Primary was emitted with no resolvable pair: %+v", *p)
	}
	s := targetByRole(resp.Targets, bpv1.DataRoleStandby)
	if s == nil {
		t.Fatalf("the Continuum declares a standby region, so the honest projection still names it: %+v", resp.Targets)
	}
	if s.Cluster != "" {
		t.Fatalf("Standby cluster: got %q want empty — the replica half is not observable, so no cluster id may be asserted", s.Cluster)
	}
	if !resp.UnresolvedPrimary {
		t.Fatalf("unresolvedPrimary: got false — a Standby with no Primary is a half-pair and must say so")
	}
	if resp.DerivedFromRuntime {
		t.Fatalf("derivedFromRuntime: got true on a half-pair")
	}
}

// A SAME-region pair (both halves carrying the same region label) is not a
// cross-region placement and must never be projected as one — the same
// two-DISTINCT-regions invariant deriveLiveContinuumRecord enforces.
func TestRow60_Placement_SameRegionHalvesAreNotACrossRegionPair_6268(t *testing.T) {
	primary, replica := newCNPGPairFixture("postgres", xrNS, xrRegionA, xrRegionA)
	h := newCrossRegionPlacementHandler(t, "dep-row60-xr-sameregion",
		[]runtime.Object{primary, replica}, nil, xrContinuum())

	resp := callPlacementNS(t, h, "dep-row60-xr-sameregion", xrApp, xrNS)

	for _, tg := range resp.Targets {
		if tg.Role == bpv1.DataRoleStandby && tg.Region == xrRegionA {
			t.Fatalf("projected a Standby in the PRIMARY's own region — that is not a cross-region standby: %+v", resp.Targets)
		}
		if tg.Role == bpv1.DataRolePrimary && tg.Cluster != "" {
			t.Fatalf("projected a resolved cross-region Primary off a same-region pair: %+v", tg)
		}
	}
}
