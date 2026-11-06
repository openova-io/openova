package switchover

// Cross-region (two API servers) cnpg-pair switchover — the hw307
// 2026-11-06 defect: step-2 failed with `pair "…" incomplete
// (primary=true replica=false)` because the replica Cluster CR lives in
// region B's API server. These tests run the 7-step sequence against TWO
// fake dynamic clients (region A = fsn, region B = hel) with a simulated
// flux → helm → CNPG loop that renders each region's bootstrap-kit
// substitutes into its Cluster CR the way bp-cnpg-pair's distributed
// mode does, and publishes CNPG's demotion / promotion tokens.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pgprobe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

const (
	xrNS      = "ns"
	xrPair    = "demo"
	xrPrimary = "demo-primary"
	xrReplica = "demo-replica"
)

func xrListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		cnpg.ClusterGVR:       "ClusterList",
		cnpg.KustomizationGVR: "KustomizationList",
	}
}

// xrCluster builds one half in CNPG distributed-topology form.
func xrCluster(name, role, region, activePrimary, source string) *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	c.SetNamespace(xrNS)
	c.SetName(name)
	c.SetLabels(map[string]string{
		cnpg.PairLabel:     xrPair,
		cnpg.PairRoleLabel: role,
		cnpg.RegionLabel:   region,
	})
	_ = unstructured.SetNestedField(c.Object, activePrimary, "spec", "replica", "primary")
	_ = unstructured.SetNestedField(c.Object, source, "spec", "replica", "source")
	_ = unstructured.SetNestedSlice(c.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(c.Object, "Cluster in healthy state", "status", "phase")
	return c
}

func xrKustomization(sub map[string]interface{}) *unstructured.Unstructured {
	k := &unstructured.Unstructured{}
	k.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	k.SetNamespace("flux-system")
	k.SetName("bootstrap-kit")
	_ = unstructured.SetNestedMap(k.Object, sub, "spec", "postBuild", "substitute")
	return k
}

// fakeRegions resolves the peer region's client.
type fakeRegions struct {
	clients map[string]dynamic.Interface
	err     error
	calls   []string
}

func (f *fakeRegions) ClientFor(_ context.Context, region string) (dynamic.Interface, error) {
	f.calls = append(f.calls, region)
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.clients[region]
	if !ok {
		return nil, fmt.Errorf("no client for %q", region)
	}
	return c, nil
}

// fakeProbe is a pgprobe.Prober returning a fixed posture.
type fakeProbe struct {
	posture pgprobe.Posture
	err     error
}

func (f fakeProbe) Observe(context.Context, string, string, string) (pgprobe.Posture, error) {
	return f.posture, f.err
}

// xrWorld is the two-region fixture plus the simulated flux/helm/CNPG.
type xrWorld struct {
	mu       sync.Mutex
	a, b     *dynamicfake.FakeDynamicClient
	tokenSeq int
	// bStuck: region B's CNPG never consumes the promotion token.
	bStuck bool
}

func newXRWorld() *xrWorld {
	scheme := runtime.NewScheme()
	a := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, xrListKinds(),
		xrCluster(xrPrimary, cnpg.RolePrimary, "fsn", xrPrimary, xrReplica),
		xrKustomization(map[string]interface{}{cnpg.SubstituteDemoted: "false"}),
	)
	b := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), xrListKinds(),
		xrCluster(xrReplica, cnpg.RoleReplica, "hel", xrPrimary, xrPrimary),
		xrKustomization(map[string]interface{}{cnpg.SubstitutePromoted: "false"}),
	)
	return &xrWorld{a: a, b: b}
}

func (w *xrWorld) get(dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, ns, name string) *unstructured.Unstructured {
	obj, err := dyn.Tracker().Get(gvr, ns, name)
	if err != nil {
		panic(err)
	}
	return obj.(*unstructured.Unstructured).DeepCopy()
}

func (w *xrWorld) substitutes(dyn *dynamicfake.FakeDynamicClient) map[string]string {
	ks := w.get(dyn, cnpg.KustomizationGVR, "flux-system", "bootstrap-kit")
	m, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	return m
}

func (w *xrWorld) cluster(dyn *dynamicfake.FakeDynamicClient, name string) *unstructured.Unstructured {
	return w.get(dyn, cnpg.ClusterGVR, xrNS, name)
}

// operate is one pass of flux (substitute → HR values) + helm (values →
// Cluster spec, as bp-cnpg-pair distributed mode renders it) + CNPG
// (demotion token on demote, token consumption on promote). It writes
// through the TRACKER, so it never shows up as a client action — the
// engine's own actions stay assertable.
func (w *xrWorld) operate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, side := range []struct {
		dyn  *dynamicfake.FakeDynamicClient
		self string
		peer *dynamicfake.FakeDynamicClient
		name string
	}{
		{w.a, xrPrimary, w.b, xrReplica},
		{w.b, xrReplica, w.a, xrPrimary},
	} {
		sub := w.substitutes(side.dyn)
		active := sub[cnpg.SubstituteActivePrimary]
		if active == "" {
			active = xrPrimary // chart default: the pair-role primary
		}
		token := sub[cnpg.SubstitutePromotionToken]
		c := w.cluster(side.dyn, side.self)
		wasPrimary, _, _ := unstructured.NestedString(c.Object, "spec", "replica", "primary")
		_ = unstructured.SetNestedField(c.Object, active, "spec", "replica", "primary")
		if active == side.self && token != "" {
			_ = unstructured.SetNestedField(c.Object, token, "spec", "replica", "promotionToken")
		} else {
			unstructured.RemoveNestedField(c.Object, "spec", "replica", "promotionToken")
		}
		switch {
		case wasPrimary == side.self && active != side.self:
			// CNPG demotes: shutdown checkpoint → fresh demotion token.
			w.tokenSeq++
			_ = unstructured.SetNestedField(c.Object, fmt.Sprintf("demotion-token-%d", w.tokenSeq), "status", "demotionToken")
		case active == side.self && token != "":
			peerTok, _, _ := unstructured.NestedString(w.cluster(side.peer, side.name).Object, "status", "demotionToken")
			if !(side.dyn == w.b && w.bStuck) && token == peerTok {
				_ = unstructured.SetNestedField(c.Object, token, "status", "lastPromotionToken")
			}
		}
		if err := side.dyn.Tracker().Update(cnpg.ClusterGVR, c, xrNS); err != nil {
			panic(err)
		}
	}
}

// engineWroteClusters reports whether the ENGINE itself mutated a Cluster
// CR (it must never: the halves are Flux/Helm-owned).
func engineWroteClusters(dyn *dynamicfake.FakeDynamicClient) []string {
	var bad []string
	for _, a := range dyn.Actions() {
		if a.GetResource() == cnpg.ClusterGVR && a.GetVerb() != "get" && a.GetVerb() != "list" && a.GetVerb() != "watch" {
			bad = append(bad, a.GetVerb())
		}
	}
	return bad
}

func makeXRSequencer(t *testing.T, w *xrWorld, regions RegionClients) (*Sequencer, *witness.InMemoryClient) {
	t.Helper()
	store := witness.NewInMemoryStore()
	wc := store.Client("ns/cr")
	if _, err := wc.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	return &Sequencer{
		CNPG: cnpg.NewReader(w.a),
		CrossRegion: CrossRegionOptions{
			Regions:      regions,
			Timeout:      50 * time.Millisecond,
			PollInterval: 10 * time.Millisecond,
		},
		StandbyProbe: fakeProbe{posture: pgprobe.Posture{StandbyPresent: true, Streaming: true, ReplayLagSeconds: 0, AppName: xrReplica}},
		Witness:      wc,
		HTTPRoute:    &fakeHTTPRoute{priorReturned: []int{100, 0}},
		Audit:        events.NewRecorder(),
		Sleep:        func(time.Duration) { w.operate() },
		PDMCommit:    func(context.Context, []dns.Record) error { return nil },
	}, wc
}

// TestCrossRegion_HappyPath — fsn → hel across two API servers: A is
// demoted via ITS seam (local client), B is promoted via ITS seam (the
// region-B client) with A's demotion token; no Cluster CR is ever
// patched by the engine.
func TestCrossRegion_HappyPath(t *testing.T) {
	w := newXRWorld()
	regions := &fakeRegions{clients: map[string]dynamic.Interface{"hel": w.b}}
	seq, wc := makeXRSequencer(t, w, regions)

	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err != nil {
		t.Fatalf("Execute: %v (failed at step %d)", res.Err, res.FailedAtStep)
	}
	if len(res.StepsCompleted) != 7 {
		t.Fatalf("StepsCompleted=%v want 7 steps", res.StepsCompleted)
	}
	subA, subB := w.substitutes(w.a), w.substitutes(w.b)
	if got := subA[cnpg.SubstituteActivePrimary]; got != xrReplica {
		t.Errorf("region A seam %s=%q want %q", cnpg.SubstituteActivePrimary, got, xrReplica)
	}
	if got := subB[cnpg.SubstituteActivePrimary]; got != xrReplica {
		t.Errorf("region B seam %s=%q want %q", cnpg.SubstituteActivePrimary, got, xrReplica)
	}
	demTok, _, _ := unstructured.NestedString(w.cluster(w.a, xrPrimary).Object, "status", "demotionToken")
	if demTok == "" || subB[cnpg.SubstitutePromotionToken] != demTok {
		t.Errorf("region B promotion token %q, want A's demotion token %q", subB[cnpg.SubstitutePromotionToken], demTok)
	}
	if _, ok := subA[cnpg.SubstitutePromotionToken]; ok {
		t.Errorf("region A seam must not carry a promotion token: %v", subA)
	}
	// The DR seams are untouched.
	if subA[cnpg.SubstituteDemoted] != "false" || subB[cnpg.SubstitutePromoted] != "false" {
		t.Errorf("DR seams changed: A=%v B=%v", subA, subB)
	}
	stB, _, _ := cnpg.NewReader(w.b).Get(context.Background(), xrNS, xrReplica)
	if stB.IsReplicaCluster || stB.LastPromotionToken != demTok {
		t.Errorf("region B not promoted with the token: %+v", stB)
	}
	stA, _, _ := cnpg.NewReader(w.a).Get(context.Background(), xrNS, xrPrimary)
	if !stA.IsReplicaCluster {
		t.Errorf("region A still primary: %+v", stA)
	}
	for name, dyn := range map[string]*dynamicfake.FakeDynamicClient{"A": w.a, "B": w.b} {
		if bad := engineWroteClusters(dyn); len(bad) > 0 {
			t.Errorf("engine wrote Cluster CRs in region %s directly (%v) — must go through the seam", name, bad)
		}
	}
	if len(regions.calls) == 0 || regions.calls[0] != "hel" {
		t.Errorf("region-B client not resolved for hel: %v", regions.calls)
	}
	if st, _ := wc.Read(context.Background()); st.Holder != "hel" {
		t.Errorf("lease holder=%q want hel", st.Holder)
	}

	// Post-switchover health sees the cross-region pair healthy.
	rep, err := seq.PostSwitchoverHealth(context.Background(), defaultPlan(), HealthOptions{})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == CheckReplicasHealthy && !c.Passed {
			t.Errorf("replicas-healthy failed after cross-region switchover: %s", c.Detail)
		}
	}
}

// TestCrossRegion_RollbackOnPromoteFailure — region B never consumes the
// token: step-6 fails, B's seam is restored, and the step-2 rollback
// re-instates A as primary through A's seam (exact prior value).
func TestCrossRegion_RollbackOnPromoteFailure(t *testing.T) {
	w := newXRWorld()
	w.bStuck = true
	seq, wc := makeXRSequencer(t, w, &fakeRegions{clients: map[string]dynamic.Interface{"hel": w.b}})

	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err == nil || res.FailedAtStep != 6 {
		t.Fatalf("want failure at step 6, got step=%d err=%v", res.FailedAtStep, res.Err)
	}
	if !strings.Contains(res.Err.Error(), "promotion of") {
		t.Errorf("error should name the promotion wait: %v", res.Err)
	}
	rolledBack2 := false
	for _, s := range res.RolledBack {
		if s == 2 {
			rolledBack2 = true
		}
	}
	if !rolledBack2 {
		t.Fatalf("step-2 (demote) not rolled back: RolledBack=%v", res.RolledBack)
	}
	subA, subB := w.substitutes(w.a), w.substitutes(w.b)
	for name, sub := range map[string]map[string]string{"A": subA, "B": subB} {
		for _, k := range []string{cnpg.SubstituteActivePrimary, cnpg.SubstitutePromotionToken} {
			if v, ok := sub[k]; ok {
				t.Errorf("region %s seam %s=%q not restored to absent", name, k, v)
			}
		}
	}
	w.operate()
	stA, _, _ := cnpg.NewReader(w.a).Get(context.Background(), xrNS, xrPrimary)
	stB, _, _ := cnpg.NewReader(w.b).Get(context.Background(), xrNS, xrReplica)
	if stA.IsReplicaCluster || !stB.IsReplicaCluster {
		t.Errorf("after rollback want A primary / B replica, got A.replica=%v B.replica=%v", stA.IsReplicaCluster, stB.IsReplicaCluster)
	}
	if st, _ := wc.Read(context.Background()); st.Holder != "fsn" {
		t.Errorf("lease holder=%q want fsn after rollback", st.Holder)
	}
}

// TestCrossRegion_PreflightFailsWhenRegionBUnreachable — the dry-run
// reports a clear step-2 blocker naming the region, and a real switchover
// stops at step-2 before writing ANY seam.
func TestCrossRegion_PreflightFailsWhenRegionBUnreachable(t *testing.T) {
	w := newXRWorld()
	regions := &fakeRegions{err: errors.New("dial tcp 10.0.0.2:6443: i/o timeout")}
	seq, _ := makeXRSequencer(t, w, regions)

	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	found := false
	for _, b := range rep.Blockers {
		if strings.HasPrefix(b, "step-2:") && strings.Contains(b, "standby region unreachable") && strings.Contains(b, "hel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no step-2 region-unreachable blocker: %v", rep.Blockers)
	}
	if rep.Steps[1].PreconditionsMet {
		t.Errorf("step-2 PreconditionsMet=true with region B unreachable")
	}

	res := seq.Execute(context.Background(), defaultPlan())
	if res.FailedAtStep != 2 || !errors.Is(res.Err, ErrRegionUnreachable) {
		t.Fatalf("want step-2 ErrRegionUnreachable, got step=%d err=%v", res.FailedAtStep, res.Err)
	}
	if _, ok := w.substitutes(w.a)[cnpg.SubstituteActivePrimary]; ok {
		t.Errorf("region A seam written although region B was unreachable")
	}

	// No resolver wired at all → same clear failure, never a silent no-op.
	seq.CrossRegion.Regions = nil
	res = seq.Execute(context.Background(), defaultPlan())
	if res.FailedAtStep != 2 || !errors.Is(res.Err, ErrRegionUnreachable) || !strings.Contains(res.Err.Error(), "no region client resolver") {
		t.Fatalf("nil resolver: want step-2 ErrRegionUnreachable, got step=%d err=%v", res.FailedAtStep, res.Err)
	}
}

// TestCrossRegion_PreflightBlocksLaggingStandbyAndDRLatch — the standby
// must be streaming and caught up, and a latched unplanned-DR seam blocks
// a planned switchover.
func TestCrossRegion_PreflightBlocksLaggingStandbyAndDRLatch(t *testing.T) {
	w := newXRWorld()
	seq, _ := makeXRSequencer(t, w, &fakeRegions{clients: map[string]dynamic.Interface{"hel": w.b}})
	seq.StandbyProbe = fakeProbe{posture: pgprobe.Posture{StandbyPresent: true, ReplayLagSeconds: 120}}
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsSub(rep.Blockers, "replay lag 120s exceeds 30s") {
		t.Errorf("lagging standby not blocked: %v", rep.Blockers)
	}

	seq.StandbyProbe = fakeProbe{posture: pgprobe.Posture{StandbyPresent: true}}
	ks := w.get(w.b, cnpg.KustomizationGVR, "flux-system", "bootstrap-kit")
	_ = unstructured.SetNestedField(ks.Object, "true", "spec", "postBuild", "substitute", cnpg.SubstitutePromoted)
	if err := w.b.Tracker().Update(cnpg.KustomizationGVR, ks, "flux-system"); err != nil {
		t.Fatal(err)
	}
	res := seq.Execute(context.Background(), defaultPlan())
	if res.FailedAtStep != 2 || !strings.Contains(res.Err.Error(), "unplanned DR episode") {
		t.Fatalf("DR latch not blocking: step=%d err=%v", res.FailedAtStep, res.Err)
	}
}

// TestCrossRegion_SingleAPIPathUnchanged — both halves visible locally
// (kom4dc 2-VPC mimic): the historical annotate + replica.enabled flip
// runs, the region resolver is NEVER consulted and no seam is touched.
func TestCrossRegion_SingleAPIPathUnchanged(t *testing.T) {
	seq, _, _, _, cnpgR, _ := makeSequencer(t)
	regions := &fakeRegions{err: errors.New("must not be called")}
	seq.CrossRegion = CrossRegionOptions{Regions: regions}

	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(regions.calls) != 0 {
		t.Errorf("region resolver consulted on a single-API pair: %v", regions.calls)
	}
	p, _, _ := cnpgR.Get(context.Background(), "ns", "demo-primary")
	r, _, _ := cnpgR.Get(context.Background(), "ns", "demo-replica")
	if !p.IsReplicaCluster || r.IsReplicaCluster || p.Distributed || r.Distributed {
		t.Errorf("single-API flip changed: primary=%+v replica=%+v", p, r)
	}
	for _, a := range cnpgR.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetResource() == cnpg.KustomizationGVR {
			t.Errorf("single-API path touched the Kustomization seam: %s", a.GetVerb())
		}
	}
}

func containsSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
