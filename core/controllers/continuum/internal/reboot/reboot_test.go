package reboot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var t0 = time.Date(2026, 11, 21, 18, 15, 0, 0, time.UTC)

// daemonSet is kube-system/kured as bp-kured renders it after the install
// hold is released (the hold-era annotation is still on it, reading
// "released").
func daemonSet(lockKey, lock string) *unstructured.Unstructured {
	args := []interface{}{"--ds-name=kured", "--ds-namespace=kube-system", "--period=30m", "--concurrency=1", "--lock-ttl=1h"}
	if lockKey != DefaultLockAnnotation {
		args = append(args, "--lock-annotation="+lockKey)
	}
	ann := map[string]interface{}{"catalyst.openova.io/kured-install-hold": "released"}
	if lock != "" {
		ann[lockKey] = lock
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]interface{}{"name": "kured", "namespace": "kube-system", "resourceVersion": "41", "annotations": ann},
		"spec": map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{"name": "kured", "command": []interface{}{"/usr/bin/kured"}, "args": args}},
		}}},
	}}
}

// node is a worker node, cordoned or not.
func node(name string, cordoned bool) *unstructured.Unstructured {
	spec := map[string]interface{}{}
	if cordoned {
		spec["unschedulable"] = true
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]interface{}{"name": name}, "spec": spec,
	}}
}

// newDyn is a fake API with the kinds the agent reads.
func newDyn(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kuredDaemonSetGVR: "DaemonSetList", nodeGVR: "NodeList",
	}, objs...)
}

// kuredLock is kured 1.23's pkg/daemonsetlock LockAnnotationValue, verbatim.
type kuredLock struct {
	NodeID   string `json:"nodeID"`
	Metadata struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"metadata,omitempty"`
	Created time.Time     `json:"created"`
	TTL     time.Duration `json:"TTL"`
}

func annotation(t *testing.T, dyn *dynfake.FakeDynamicClient, key string) (string, bool) {
	t.Helper()
	ds, err := dyn.Resource(kuredDaemonSetGVR).Namespace("kube-system").Get(context.Background(), "kured", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := ds.GetAnnotations()[key]
	return v, ok
}

func patches(dyn *dynfake.FakeDynamicClient) []k8stesting.PatchAction {
	var out []k8stesting.PatchAction
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok {
			out = append(out, p)
		}
	}
	return out
}

func lockJSON(id string, created time.Time, ttl time.Duration) string {
	b, _ := json.Marshal(Lock{NodeID: id, Created: created, TTL: ttl})
	return string(b)
}

func TestNoAgent_NothingHeldNothingWritten(t *testing.T) {
	dyn := newDyn(node("w1", false))
	a := &Agent{Dyn: dyn, HolderID: "continuum-b"}
	if s := a.Read(context.Background(), t0); s.Agent || s.Held || len(s.Draining) > 0 {
		t.Fatalf("no DaemonSet: %+v", s)
	}
	if err := a.Sync(context.Background(), Hold, t0); err != nil || len(patches(dyn)) != 0 {
		t.Fatalf("no DaemonSet, no write: %v %d", err, len(patches(dyn)))
	}
}

func TestHold_TakenInKuredsOwnFormatWithTheReadResourceVersion(t *testing.T) {
	dyn := newDyn(daemonSet(DefaultLockAnnotation, ""), node("w1", false))
	a := &Agent{Dyn: dyn, HolderID: "continuum-b", TTL: 5 * time.Minute}
	if s := a.Read(context.Background(), t0); !s.Agent || !s.Holdable || s.Held || s.Holder != "" {
		t.Fatalf("free lock: %+v", s)
	}
	if err := a.Sync(context.Background(), Hold, t0); err != nil {
		t.Fatal(err)
	}
	v, ok := annotation(t, dyn, DefaultLockAnnotation)
	if !ok {
		t.Fatal("the hold was not written")
	}
	var k kuredLock
	if err := json.Unmarshal([]byte(v), &k); err != nil || k.NodeID != "continuum-b" || !k.Created.Equal(t0) || k.TTL != 5*time.Minute {
		t.Fatalf("kured must read the hold as its own lock value: %q %+v %v", v, k, err)
	}
	ps := patches(dyn)
	if len(ps) != 1 || !strings.Contains(string(ps[0].GetPatch()), `"resourceVersion":"41"`) {
		t.Fatalf("one merge patch carrying the read resourceVersion, got %d", len(ps))
	}
	if s := a.Read(context.Background(), t0); !s.Held || s.Holder != "continuum-b" || len(s.Draining) > 0 {
		t.Fatalf("held: %+v", s)
	}
	// Renewed only when a third of the TTL has passed.
	_ = a.Sync(context.Background(), Hold, t0.Add(time.Minute))
	if len(patches(dyn)) != 1 {
		t.Fatal("a fresh hold is not rewritten")
	}
	_ = a.Sync(context.Background(), Hold, t0.Add(2*time.Minute))
	if len(patches(dyn)) != 2 {
		t.Fatal("the hold is renewed after a third of its TTL")
	}
	// Released: the key is removed.
	if err := a.Sync(context.Background(), Release, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok := annotation(t, dyn, DefaultLockAnnotation); ok {
		t.Fatal("the hold was not released")
	}
}

// Writers of kured's lock cannot overwrite each other: a live lock of kured
// (a node it drains), of the install hold, or of a person is never written
// over, nor released.
func TestHold_NeverOverwritesAnotherHoldersLiveLock(t *testing.T) {
	for _, tc := range []struct {
		name, key, lock string
		cordoned        bool
		want            func(s statusView) bool
	}{
		{"a node kured drains", DefaultLockAnnotation, lockJSON("w5cce4d", t0, time.Hour), true,
			func(s statusView) bool { return !s.Held && len(s.Draining) == 1 && s.Draining[0] == "w5cce4d" }},
		{"a node kured holds, back from its restart", DefaultLockAnnotation, lockJSON("w5cce4d", t0, time.Hour), false,
			func(s statusView) bool { return !s.Held && len(s.Draining) == 0 && s.Holder == "w5cce4d" }},
		{"the install hold of bp-kured", "catalyst.openova.io/kured-install-hold", `{"nodeID":"catalyst-install-hold","TTL":0}`, false,
			func(s statusView) bool { return s.Held && s.Holder == "catalyst-install-hold" }},
		{"a person's manual hold", DefaultLockAnnotation, `{"nodeID":"manual"}`, false,
			func(s statusView) bool { return s.Held && s.Holder == "manual" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newDyn(daemonSet(tc.key, tc.lock), node("w5cce4d", tc.cordoned))
			a := &Agent{Dyn: dyn, HolderID: "continuum-b"}
			s := a.Read(context.Background(), t0.Add(time.Minute))
			if !tc.want(statusView{Held: s.Held, Holder: s.Holder, Draining: s.Draining}) {
				t.Fatalf("read: %+v", s)
			}
			_ = a.Sync(context.Background(), Hold, t0.Add(time.Minute))
			_ = a.Sync(context.Background(), Release, t0.Add(time.Minute))
			if v, _ := annotation(t, dyn, tc.key); v != tc.lock || len(patches(dyn)) != 0 {
				t.Fatalf("another holder's lock was written: %q (%d patches)", v, len(patches(dyn)))
			}
		})
	}
}

type statusView struct {
	Held     bool
	Holder   string
	Draining []string
}

// When the controller is gone nothing renews the hold, and kured's own expiry
// test frees it.
func TestHold_ExpiresWhenNotRenewed(t *testing.T) {
	dyn := newDyn(daemonSet(DefaultLockAnnotation, ""), node("w1", false))
	a := &Agent{Dyn: dyn, HolderID: "continuum-b", TTL: 5 * time.Minute}
	_ = a.Sync(context.Background(), Hold, t0)
	v, _ := annotation(t, dyn, DefaultLockAnnotation)
	var k Lock
	_ = json.Unmarshal([]byte(v), &k)
	if k.Expired(t0.Add(5*time.Minute - time.Second)) {
		t.Fatal("the hold must hold for its TTL")
	}
	if !k.Expired(t0.Add(5 * time.Minute)) {
		t.Fatal("kured's test (created + TTL passed) must free the hold")
	}
	if s := a.Read(context.Background(), t0.Add(5*time.Minute)); s.Held || s.Holder != "" {
		t.Fatalf("an expired hold reads free: %+v", s)
	}
	// Another writer takes it once it is expired, and the controller, back,
	// does not write over the new holder.
	other := &Agent{Dyn: dyn, HolderID: "w1"}
	_ = other.Sync(context.Background(), Hold, t0.Add(6*time.Minute))
	_ = a.Sync(context.Background(), Hold, t0.Add(6*time.Minute))
	if v, _ := annotation(t, dyn, DefaultLockAnnotation); !strings.Contains(v, `"nodeID":"w1"`) {
		t.Fatalf("the new holder's lock was written over: %q", v)
	}
}

func TestConcurrencyAboveOne_NotHeldButDrainsSeen(t *testing.T) {
	multi := `{"maxOwners":2,"locks":[{"nodeID":"w2","created":"2026-11-21T18:14:00Z","TTL":3600000000000}]}`
	ds := daemonSet(DefaultLockAnnotation, multi)
	containers, _, _ := unstructured.NestedSlice(ds.Object, "spec", "template", "spec", "containers")
	list := containers[0].(map[string]interface{})["args"].([]interface{})
	for i, x := range list {
		if x == "--concurrency=1" {
			list[i] = "--concurrency=2"
		}
	}
	if err := unstructured.SetNestedSlice(ds.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	dyn := newDyn(ds, node("w2", true))
	a := &Agent{Dyn: dyn, HolderID: "continuum-b"}
	s := a.Read(context.Background(), t0)
	if !s.Agent || s.Holdable || len(s.Draining) != 1 || s.Draining[0] != "w2" || s.Message == "" {
		t.Fatalf("concurrency 2: %+v", s)
	}
	_ = a.Sync(context.Background(), Hold, t0)
	if len(patches(dyn)) != 0 {
		t.Fatal("a multi-lock is never written")
	}
}
