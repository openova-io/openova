package handler

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// storedModelFixture is the ComplianceModel object default as a Sovereign's
// API server returns it (kubectl get -o json, 28 November 2026, version 1.0.0):
// 22 checks of the engine, none with a weight of its own.
const storedModelFixture = "../compliance/testdata/model-stored.json"

var modelGVR = schema.GroupVersionResource{Group: "compliance.openova.io", Version: "v1alpha1", Resource: "compliancemodels"}

// modelLife is one life of catalyst-api's Factory: cluster acme holds obj,
// the ComplianceModel kind is registered, and the snapshot loop writes to dir
// every interval.
func modelLife(t *testing.T, dir string, interval time.Duration, obj *unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	reg := k8scache.NewRegistry()
	if err := reg.Add(k8scache.Kind{Name: k8scache.KindComplianceModel, GVR: modelGVR}); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{modelGVR: "ComplianceModelList"}, obj.DeepCopy())
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: quietComplianceLogger(), Registry: reg, SnapshotDir: dir, SnapshotInterval: interval,
		Clusters: []k8scache.ClusterRef{{ID: "acme", DynamicClient: dyn}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

// hydrated returns catalyst_k8scache_snapshot_hydrate_total of the
// ComplianceModel kind of cluster acme with result hydrated.
func hydrated(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "catalyst_k8scache_snapshot_hydrate_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			l := map[string]string{}
			for _, p := range m.GetLabel() {
				l[p.GetName()] = p.GetValue()
			}
			if l["cluster"] == "acme" && l["kind"] == k8scache.KindComplianceModel && l["result"] == "hydrated" {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// TestCompliance_ModelHydratedFromSnapshot: catalyst-api restarts on the disk
// snapshot of its previous life, as on a Sovereign. The Factory hydrates the
// ComplianceModel object into the informer store, and the first LIST returns
// the same resourceVersion: a resync, which an informer of resync 0 hands to
// no subscriber, so the model's object reaches the aggregator by no event.
// The API must still weigh every check of the object as the model does (the
// weight of the check, or of its severity) and the Sovereign must have a
// score with a denominator above 0: before the repair every check weighed 0
// and the denominator was 0 (gate checks CMP-G03 and CMP-G01 on the
// Sovereign, 28 November 2026).
func TestCompliance_ModelHydratedFromSnapshot(t *testing.T) {
	body, err := os.ReadFile(storedModelFixture)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(body); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// The previous life writes the snapshot that holds the object.
	first := modelLife(t, dir, 20*time.Millisecond, obj)
	waitFor(t, 5*time.Second, func() bool {
		b, err := fs.ReadFile(os.DirFS(dir), "acme-"+k8scache.KindComplianceModel+".json")
		return err == nil && strings.Contains(string(b), string(obj.GetUID()))
	})
	first.Stop()

	// The restart: the store is hydrated before the informer lists.
	before := hydrated(t)
	f := modelLife(t, dir, time.Hour, obj)
	if got := hydrated(t); got != before+1 {
		t.Fatalf("the restart did not hydrate the model from the snapshot (hydrated %v, before %v)", got, before)
	}
	nats := newFakeNATS()
	c := NewComplianceHandler(ComplianceConfig{}, quietComplianceLogger(), f, nats)
	c.Start(context.Background())
	t.Cleanup(c.Stop)
	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return f.Synced()["acme"][k8scache.KindComplianceModel] })

	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "web-report", []map[string]any{
		goldenResult("probes-present", "probes-present", "pass", "", "Deployment", "ns1", "web"),
		goldenResult("forbid-nodeport-service", "forbid-nodeport-service", "fail", "", "Deployment", "ns1", "web")}))
	waitFor(t, 2*time.Second, func() bool { _, ok := nats.Get("resource.deployment/ns1/web"); return ok })

	spec := obj.Object["spec"].(map[string]any)
	severity := spec["severityWeights"].(map[string]any)
	listed := map[string]int{}
	for _, p := range c.policiesFor(context.Background(), "acme") {
		listed[p.Name] = p.Weight
	}
	checks := spec["checks"].([]any)
	var differ []string
	for _, x := range checks {
		chk := x.(map[string]any)
		id := chk["id"].(string)
		want, ok := chk["weight"].(int64)
		if !ok {
			want = severity[chk["severity"].(string)].(int64)
		}
		if got, ok := listed[id]; !ok || int64(got) != want {
			differ = append(differ, fmt.Sprintf("%s: the API weighs it %d, the model %d", id, got, want))
		}
	}
	if len(checks) == 0 || len(differ) > 0 {
		t.Fatalf("the API does not weigh the %d checks of the hydrated model as the model does: %s", len(checks), strings.Join(differ, "; "))
	}
	if v, ok := c.model.ModelVersion("acme"); !ok || v != spec["version"] {
		t.Fatalf("model version %q %v, the object's %v", v, ok, spec["version"])
	}
	for _, s := range c.rollups("acme") {
		if s.Scope == compliance.ScopeSovereign {
			if s.Denominator <= 0 {
				t.Fatalf("the Sovereign score has denominator %d: %+v", s.Denominator, s)
			}
			return
		}
	}
	t.Fatalf("no Sovereign score: %+v", c.rollups("acme"))
}
