package cnpg

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func distCluster(name string, replica map[string]interface{}) *unstructured.Unstructured {
	c := &unstructured.Unstructured{Object: map[string]interface{}{}}
	c.SetName(name)
	_ = unstructured.SetNestedMap(c.Object, replica, "spec", "replica")
	return c
}

// TestParseStatus_DistributedTopology — CNPG distributed topology
// (spec.replica.primary/self): the role is DERIVED as primary != self,
// so the demoted half of a planned cross-region switchover reads as a
// replica cluster to every existing consumer.
func TestParseStatus_DistributedTopology(t *testing.T) {
	// Primary half: primary == metadata.name (self defaulted).
	p := parseStatus(distCluster("x-primary", map[string]interface{}{"primary": "x-primary", "source": "x-replica"}))
	if !p.Distributed || p.IsReplicaCluster || p.Self != "x-primary" || p.ReplicaSource != "" {
		t.Errorf("distributed primary misparsed: %+v", p)
	}
	// Demoted half: primary names the peer.
	d := distCluster("x-primary", map[string]interface{}{"primary": "x-replica", "source": "x-replica"})
	_ = unstructured.SetNestedField(d.Object, "tok-1", "status", "demotionToken")
	ds := parseStatus(d)
	if !ds.IsReplicaCluster || ds.ReplicaSource != "x-replica" || ds.DemotionToken != "tok-1" {
		t.Errorf("distributed demoted half misparsed: %+v", ds)
	}
	// Explicit self wins over metadata.name.
	s := parseStatus(distCluster("x-replica", map[string]interface{}{"primary": "other", "self": "other", "source": "x-primary", "promotionToken": "tok-1"}))
	if s.IsReplicaCluster || s.PromotionToken != "tok-1" {
		t.Errorf("explicit self not honoured: %+v", s)
	}
	// Legacy form is untouched: enabled wins, not Distributed.
	l := parseStatus(distCluster("x-replica", map[string]interface{}{"enabled": true, "source": "x-primary"}))
	if l.Distributed || !l.IsReplicaCluster || l.ReplicaSource != "x-primary" {
		t.Errorf("legacy replica form changed: %+v", l)
	}
}

// TestSeam_PatchAndRestore — substitutes are merge-patched (other keys
// kept) and a nil snapshot value deletes the key on restore.
func TestSeam_PatchAndRestore(t *testing.T) {
	ks := &unstructured.Unstructured{}
	ks.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	ks.SetNamespace("flux-system")
	ks.SetName("bootstrap-kit")
	_ = unstructured.SetNestedMap(ks.Object, map[string]interface{}{SubstituteDemoted: "false", "OTHER": "x"}, "spec", "postBuild", "substitute")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{KustomizationGVR: "KustomizationList"}, ks)
	ctx := context.Background()

	before, err := ReadSubstitutes(ctx, dyn, SeamRef{})
	if err != nil {
		t.Fatal(err)
	}
	snap := Snapshot(before, SubstituteActivePrimary)
	if err := PatchSubstitutes(ctx, dyn, SeamRef{}, map[string]*string{SubstituteActivePrimary: StrPtr("x-replica")}, time.Unix(42, 0)); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadSubstitutes(ctx, dyn, SeamRef{})
	if after[SubstituteActivePrimary] != "x-replica" || after["OTHER"] != "x" || after[SubstituteDemoted] != "false" {
		t.Errorf("patch clobbered or missed keys: %v", after)
	}
	if err := PatchSubstitutes(ctx, dyn, SeamRef{}, snap, time.Unix(43, 0)); err != nil {
		t.Fatal(err)
	}
	restored, _ := ReadSubstitutes(ctx, dyn, SeamRef{})
	if _, ok := restored[SubstituteActivePrimary]; ok || restored["OTHER"] != "x" {
		t.Errorf("restore did not delete the originally-absent key: %v", restored)
	}
	// Both writes are merge patches on the Kustomization (the fake
	// dynamic client of client-go v0.31 drops PatchOptions, so the
	// FieldManagerSwitchover stamp is not observable here).
	patches := 0
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchActionImpl); ok {
			patches++
			if string(pa.GetPatchType()) != "application/merge-patch+json" {
				t.Errorf("seam patch type=%q want merge patch", pa.GetPatchType())
			}
		}
	}
	if patches != 2 {
		t.Errorf("patches=%d want 2", patches)
	}
}
