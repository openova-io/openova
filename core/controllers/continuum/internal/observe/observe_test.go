package observe

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
)

const region = "hw-me-east-215-a-rtz-prod"

func objects() []runtime.Object {
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]interface{}{"name": "wp", "namespace": "acme", "generation": int64(3),
			"labels": map[string]interface{}{LabelApp: "wp", LabelRegion: region}},
		"spec": map[string]interface{}{"releaseName": "wp", "targetNamespace": "acme",
			"values": map[string]interface{}{"openova": map[string]interface{}{"topology": map[string]interface{}{"primary": "hw-me-east-215-b-rtz-prod"}}}},
		"status": map[string]interface{}{"observedGeneration": int64(3),
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "message": "upgrade succeeded"}}},
	}}
	db := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": "wordpress-db-a", "namespace": "acme",
			"labels": map[string]interface{}{"app.kubernetes.io/instance": "wp"}},
		"spec": map[string]interface{}{"replica": map[string]interface{}{"self": "wordpress-db-a", "primary": "wordpress-db-b", "source": "wordpress-db-b"}},
		"status": map[string]interface{}{"phase": "Cluster in healthy state", "demotionToken": "TOKEN",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openova.io/v1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "wp", "namespace": "acme"},
		"status": map[string]interface{}{"regions": []interface{}{map[string]interface{}{"name": region,
			"conditions": []interface{}{map[string]interface{}{"type": "Serving", "status": "False"}}}}},
	}}
	return []runtime.Object{hr, db, app}
}

func newObserver() *Observer {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		HelmReleaseGVR: "HelmReleaseList", CNPGClusterGVR: "ClusterList", ApplicationGVR: "ApplicationList",
	}, objects()...)
	return &Observer{Dyn: dyn, Region: region}
}

func TestObserve_ReadsNativeStatusVerbatim(t *testing.T) {
	o := newObserver()
	got := o.Observe(context.Background(), []Target{{Namespace: "acme", App: "wp", Stateful: true}})["acme/wp"]
	if !got.Observed || !got.ReleaseReady || got.AppliedPrimary != "hw-me-east-215-b-rtz-prod" {
		t.Fatalf("HelmRelease: %+v", got)
	}
	if !got.DB.Found || got.DB.Primary || got.DB.DemotionToken != "TOKEN" || !got.DB.Ready {
		t.Fatalf("CNPG: %+v", got.DB)
	}
	if got.Serving != transition.False {
		t.Fatalf("Serving must be read from the Application, got %v", got.Serving)
	}
	missing := o.Observe(context.Background(), []Target{{Namespace: "acme", App: "nope"}})["acme/nope"]
	if missing.Observed || missing.ReleaseReady {
		t.Fatalf("absent HelmRelease must not read as Ready: %+v", missing)
	}
}

func TestPeers_FetchOverHTTP(t *testing.T) {
	srv := httptest.NewServer(Handler(newObserver()))
	defer srv.Close()
	p := Peers{URLFor: func(string) string { return srv.URL }}
	got, rb, err := p.Fetch(context.Background(), region, []Target{{Namespace: "acme", App: "wp", Stateful: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got["acme/wp"].DB.DemotionToken != "TOKEN" || got["acme/wp"].Serving != transition.False {
		t.Fatalf("round trip lost fields: %+v", got["acme/wp"])
	}
	if rb != nil {
		t.Fatalf("an observer without a reboot reader reports none, got %+v", rb)
	}
	if _, _, err := p.Fetch(context.Background(), "some-other-region", nil); err == nil {
		t.Fatal("an answer from a different region must be rejected")
	}
}

// A platform pair: the bootstrap-kit slot HelmRelease (no application-controller
// labels, contract arriving via valuesFrom) is observed by reference, and the
// applied primary is read from the rendered Cluster's annotation.
func TestObserve_PlatformMember_ByHelmReleaseRef(t *testing.T) {
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]interface{}{"name": "bp-cnpg-pair", "namespace": "flux-system", "generation": int64(7)},
		"spec": map[string]interface{}{"releaseName": "cnpg-pair", "targetNamespace": "cnpg",
			"valuesFrom": []interface{}{map[string]interface{}{"kind": "ConfigMap", "name": "bp-cnpg-pair-topology"}},
			"values":     map[string]interface{}{"cnpgPair": map[string]interface{}{"enabled": true}}},
		"status": map[string]interface{}{"observedGeneration": int64(7),
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	db := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": "cnpg-pair-bp-cnpg-pair-primary", "namespace": "cnpg",
			"labels":      map[string]interface{}{"app.kubernetes.io/instance": "cnpg-pair"},
			"annotations": map[string]interface{}{AnnotationTopologyPrimary: "hw-me-east-215-b-rtz-prod"}},
		"spec": map[string]interface{}{"replica": map[string]interface{}{"self": "cnpg-pair-bp-cnpg-pair-primary", "primary": "cnpg-pair-bp-cnpg-pair-replica"}},
		"status": map[string]interface{}{"phase": "Cluster in healthy state", "demotionToken": "TOK",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	mk := func(objs ...runtime.Object) *Observer {
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			HelmReleaseGVR: "HelmReleaseList", CNPGClusterGVR: "ClusterList", ApplicationGVR: "ApplicationList",
		}, objs...)
		return &Observer{Dyn: dyn, Region: region}
	}
	tg := Target{Namespace: "cnpg", App: "cnpg-pair", Stateful: true, HelmRelease: "flux-system/bp-cnpg-pair"}
	got := mk(hr, db).Observe(context.Background(), []Target{tg})["cnpg/cnpg-pair"]
	if !got.Observed || !got.ReleaseReady || got.AppliedPrimary != "hw-me-east-215-b-rtz-prod" {
		t.Fatalf("platform member: %+v", got)
	}
	if got.DB.Primary || got.DB.DemotionToken != "TOK" || got.DB.Cluster != "cnpg-pair-bp-cnpg-pair-primary" {
		t.Fatalf("CNPG: %+v", got.DB)
	}
	// Without the annotation nothing is inferred: the applied primary is unknown.
	bare := db.DeepCopy()
	unstructured.RemoveNestedField(bare.Object, "metadata", "annotations")
	if got := mk(hr, bare).Observe(context.Background(), []Target{tg})["cnpg/cnpg-pair"]; got.AppliedPrimary != "" {
		t.Fatalf("no annotation must read as not applied, got %q", got.AppliedPrimary)
	}
	// The target (with its HelmRelease) survives the peer query encoding.
	back := ParseTargets(EncodeTargets([]Target{tg, {Namespace: "acme", App: "wp"}}))
	if len(back) != 2 || back[0] != tg || back[1].HelmRelease != "" {
		t.Fatalf("encode/parse: %+v", back)
	}
}

// The state of an interrupted demotion, read from the Cluster's own status:
// the operator's reading of each instance (instancesReportedState[].isPrimary)
// and whether it is switching the Cluster to a replica.
func TestDBStatusOf_RunningPrimaryAndSwitching(t *testing.T) {
	c := func(status map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "db-b"},
			"spec":     map[string]interface{}{"replica": map[string]interface{}{"self": "db-b", "primary": "db-a"}},
			"status":   status,
		}}
	}
	standby := DBStatusOf(c(map[string]interface{}{"instancesReportedState": map[string]interface{}{
		"db-b-1": map[string]interface{}{"isPrimary": false, "timeLineID": int64(3)}}}))
	if standby.RunningPrimary != transition.False || standby.Switching || standby.Primary {
		t.Fatalf("a standby instance: %+v", standby)
	}
	running := DBStatusOf(c(map[string]interface{}{"switchReplicaClusterStatus": map[string]interface{}{"inProgress": true},
		"instancesReportedState": map[string]interface{}{
			"db-b-1": map[string]interface{}{"isPrimary": false}, "db-b-2": map[string]interface{}{"isPrimary": true}}}))
	if running.RunningPrimary != transition.True || !running.Switching {
		t.Fatalf("a running primary being demoted: %+v", running)
	}
	if none := DBStatusOf(c(map[string]interface{}{})); none.RunningPrimary != transition.Unknown {
		t.Fatalf("no reported instance is Unknown, never False: %+v", none)
	}
}

type fixedReboot struct{ s transition.RebootStatus }

func (f fixedReboot) Read(context.Context, time.Time) transition.RebootStatus { return f.s }

func TestPeers_CarryTheRebootAgent(t *testing.T) {
	o := newObserver()
	o.Reboot = fixedReboot{transition.RebootStatus{Agent: true, Holdable: true, Holder: "w5cce4d", Draining: []string{"w5cce4d"}}}
	srv := httptest.NewServer(Handler(o))
	defer srv.Close()
	_, rb, err := Peers{URLFor: func(string) string { return srv.URL }}.Fetch(context.Background(), region, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rb == nil || !rb.Agent || len(rb.Draining) != 1 || rb.Draining[0] != "w5cce4d" {
		t.Fatalf("the reboot agent did not cross: %+v", rb)
	}
}
