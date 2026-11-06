package handler

import (
	"context"
	"testing"
	"time"

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

// hw307 2026-11-06: bp-cnpg-pair's Topology tab read `active-active` with
// BOTH regions "PRIMARY serves writes". Its pods carry no openova.io/cnpg-role
// label, only CNPG's cnpg.io/instanceRole — and CNPG labels the LEAD instance
// of the region-B REPLICA cluster (spec.replica.enabled=true) `primary` while
// it is in recovery. Region B must read Standby·Hot.

// Exactly the live shape: CNPG operator-created Pods are labelled with their
// CLUSTER name as app.kubernetes.io/instance — NOT the Helm release
// (cnpg-pair) — so only the Cluster CR ties them to the app.
func cnpgPodOnly(ns, name, cluster, instanceRole, region string) *unstructured.Unstructured {
	p := placementFixturePod(ns, name, cluster, region, "")
	lbls := p.GetLabels()
	lbls["app.kubernetes.io/name"] = "postgresql"
	lbls["cnpg.io/cluster"] = cluster
	lbls["cnpg.io/instanceRole"] = instanceRole
	p.SetLabels(lbls)
	return p
}

func cnpgClusterObj(ns, name string, replica bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"namespace": ns, "name": name, "resourceVersion": "1",
			"labels": map[string]any{
				"app.kubernetes.io/instance":    "cnpg-pair",
				"catalyst.openova.io/blueprint": "bp-cnpg-pair",
			},
			"creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
		"spec": map[string]any{"instances": int64(3)},
	}}
	if replica {
		_ = unstructured.SetNestedField(u.Object, true, "spec", "replica", "enabled")
	}
	return u
}

func cnpgFixtureClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, *kfake.Clientset) {
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "Pod"}, {Version: "v1", Kind: "Namespace"}, {Version: "v1", Kind: "Node"},
		{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                                    "PodList",
		{Version: "v1", Resource: "namespaces"}:                              "NamespaceList",
		{Version: "v1", Resource: "nodes"}:                                   "NodeList",
		{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}: "ClusterList",
	}, objs...)
	return dyn, kfake.NewSimpleClientset()
}

func newCNPGPlacementHandler(t *testing.T, depID, regionA, regionB string, objsA, objsB []runtime.Object) *Handler {
	t.Helper()
	clusterA, clusterB := depID, depID+"-"+regionB
	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(k8scache.Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Namespaced: false})
	_ = r.Add(k8scache.Kind{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Namespaced: false})
	_ = r.Add(k8scache.Kind{Name: "cnpgcluster", GVR: schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}, Namespaced: true})
	dynA, coreA := cnpgFixtureClients(objsA...)
	dynB, coreB := cnpgFixtureClients(objsB...)
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: quietHandlerLogger(), Registry: r,
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ca, _, _ := f.List(clusterA, "cnpgcluster", labels.Everything())
		cb, _, _ := f.List(clusterB, "cnpgcluster", labels.Everything())
		pb, _, _ := f.List(clusterB, "pod", labels.Everything())
		if len(ca) > 0 && len(cb) > 0 && len(pb) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	h.deployments.Store(depID, &Deployment{ID: depID, Request: provisioner.Request{
		SovereignFQDN: "hw307.omani.works",
		Regions:       []provisioner.RegionSpec{{CloudRegion: regionA}, {CloudRegion: regionB}},
	}})
	return h
}

func TestPlacementRuntime_CNPGReplicaCluster_LeadInstanceIsStandby_hw307(t *testing.T) {
	depID := "dep-hw307-cnpg"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newCNPGPlacementHandler(t, depID, regionA, regionB,
		[]runtime.Object{
			cnpgClusterObj("cnpg", "cnpg-pair-bp-cnpg-pair-primary", false),
			cnpgPodOnly("cnpg", "cnpg-pair-bp-cnpg-pair-primary-4", "cnpg-pair-bp-cnpg-pair-primary", "primary", regionA),
			cnpgPodOnly("cnpg", "cnpg-pair-bp-cnpg-pair-primary-5", "cnpg-pair-bp-cnpg-pair-primary", "replica", regionA),
			placementFixturePod("cnpg", "cnpg-pair-bp-cnpg-pair-dr-failback-x", "cnpg-pair", regionA, ""),
		},
		[]runtime.Object{
			cnpgClusterObj("cnpg", "cnpg-pair-bp-cnpg-pair-replica", true),
			cnpgPodOnly("cnpg", "cnpg-pair-bp-cnpg-pair-replica-1", "cnpg-pair-bp-cnpg-pair-replica", "primary", regionB),
			cnpgPodOnly("cnpg", "cnpg-pair-bp-cnpg-pair-replica-2", "cnpg-pair-bp-cnpg-pair-replica", "replica", regionB),
			placementFixturePod("cnpg", "cnpg-pair-bp-cnpg-pair-dr-promoter-x", "cnpg-pair", regionB, ""),
		},
	)
	resp := callPlacement(t, h, depID, "bp-cnpg-pair")
	var primaries, standbys int
	for _, tgt := range resp.Targets {
		switch tgt.Role {
		case bpv1.DataRolePrimary:
			primaries++
			if tgt.Region != regionA {
				t.Errorf("primary region: got %q want %q", tgt.Region, regionA)
			}
		case bpv1.DataRoleStandby:
			standbys++
		}
	}
	if primaries != 1 || standbys != 1 {
		t.Fatalf("roles: primaries=%d standbys=%d want 1+1 — a replica cluster's lead instance is NOT a primary (%+v)", primaries, standbys, resp.Targets)
	}
	if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != bpv1.PatternActiveHotStandby {
		t.Fatalf("pattern: got %q want active-hot-standby", got)
	}
}

func TestCNPGReplicaClusterSet(t *testing.T) {
	set := cnpgReplicaClusterSet([]*unstructured.Unstructured{
		cnpgClusterObj("cnpg", "a", false), cnpgClusterObj("cnpg", "b", true), nil,
	})
	if set["cnpg/a"] || !set["cnpg/b"] || len(set) != 1 {
		t.Fatalf("replica set wrong: %v", set)
	}
}
