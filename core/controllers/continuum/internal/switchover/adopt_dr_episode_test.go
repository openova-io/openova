package switchover

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

func setSub(t *testing.T, w *xrWorld, dyn *dynamicfake.FakeDynamicClient, kv map[string]string) {
	t.Helper()
	ks := w.get(dyn, cnpg.KustomizationGVR, "flux-system", "bootstrap-kit")
	for k, v := range kv {
		_ = unstructured.SetNestedField(ks.Object, v, "spec", "postBuild", "substitute", k)
	}
	if err := dyn.Tracker().Update(cnpg.KustomizationGVR, ks, "flux-system"); err != nil {
		t.Fatal(err)
	}
}

// hw307 2026-11-07: the pair failed over to region B through the DR actors
// (PROMOTED on B, DEMOTED on A, A re-cloned onto B). A switchback hel → fsn
// from the console must adopt that episode (no role change), then run the
// normal lossless planned switch.
func TestCrossRegion_SwitchbackAdoptsDREpisode(t *testing.T) {
	w := newXRWorld()
	setSub(t, w, w.a, map[string]string{cnpg.SubstituteDemoted: "true"})
	setSub(t, w, w.b, map[string]string{cnpg.SubstitutePromoted: "true"})
	w.operate()
	if st, _, _ := cnpg.NewReader(w.b).Get(context.Background(), xrNS, xrReplica); st.IsReplicaCluster || st.Distributed {
		t.Fatalf("fixture: region B must be the legacy-promoted writer, got %+v", st)
	}

	seq, wc := makeXRSequencer(t, w, &fakeRegions{clients: map[string]dynamic.Interface{"hel": w.b}})
	plan := defaultPlan()
	plan.FromRegion, plan.ToRegion = "hel", "fsn"
	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("switchback: %v (failed at step %d)", res.Err, res.FailedAtStep)
	}
	subA, subB := w.substitutes(w.a), w.substitutes(w.b)
	for name, sub := range map[string]map[string]string{"A": subA, "B": subB} {
		if sub[cnpg.SubstitutePromoted] == "true" || sub[cnpg.SubstituteDemoted] == "true" {
			t.Errorf("region %s still carries a DR seam: %v", name, sub)
		}
		if sub[cnpg.SubstituteActivePrimary] != xrPrimary {
			t.Errorf("region %s ACTIVE_PRIMARY=%q want %q", name, sub[cnpg.SubstituteActivePrimary], xrPrimary)
		}
	}
	a, _, _ := cnpg.NewReader(w.a).Get(context.Background(), xrNS, xrPrimary)
	b, _, _ := cnpg.NewReader(w.b).Get(context.Background(), xrNS, xrReplica)
	if a.IsReplicaCluster || !b.IsReplicaCluster {
		t.Fatalf("after switchback want A primary, B replica: A=%+v B=%+v", a, b)
	}
	if bad := append(engineWroteClusters(w.a), engineWroteClusters(w.b)...); len(bad) > 0 {
		t.Fatalf("engine wrote Cluster CRs: %v", bad)
	}
	if st, _ := wc.Read(context.Background()); st.Holder != "fsn" {
		t.Fatalf("lease holder %q want fsn", st.Holder)
	}
}
