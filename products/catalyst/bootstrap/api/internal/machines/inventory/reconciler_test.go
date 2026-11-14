package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
)

const ns = "catalyst-infra"

type world struct {
	t   *testing.T
	c   client.Client
	r   *Reconciler
	now time.Time
	key types.NamespacedName
}

func node(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{ProviderID: "k3s://" + name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
			NodeInfo:  corev1.NodeSystemInfo{MachineID: "m-" + name, SystemUUID: "u-" + name, OSImage: "Ubuntu 22.04.4 LTS", KernelVersion: "5.15.0", Architecture: "amd64"},
		},
	}
}

// newWorld is a fake API server (controller-runtime's fake client; no server binary)
// with one MachineSource "nodes" of type nodes and the given Nodes.
func newWorld(t *testing.T, stale map[string]interface{}, funcs *interceptor.Funcs, nodes ...string) *world {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct{ kind, list string }{{"Machine", "MachineList"}, {"MachineSource", "MachineSourceList"}} {
		scheme.AddKnownTypeWithName(v1alpha1.GroupVersion.WithKind(k.kind), &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(v1alpha1.GroupVersion.WithKind(k.list), &unstructured.UnstructuredList{})
	}
	src := newObject(v1alpha1.MachineSourceKind)
	src.SetNamespace(ns)
	src.SetName("nodes")
	spec := map[string]interface{}{"type": "nodes"}
	if stale != nil {
		spec["stale"] = stale
	}
	src.Object["spec"] = spec
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).
		WithStatusSubresource(newObject(v1alpha1.MachineKind), newObject(v1alpha1.MachineSourceKind))
	for _, n := range nodes {
		b = b.WithObjects(node(n))
	}
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	w := &world{t: t, c: b.Build(), now: t0, key: types.NamespacedName{Namespace: ns, Name: "nodes"}}
	w.r = &Reconciler{Client: w.c, Reader: w.c, Now: func() time.Time { return w.now }}
	return w
}

func (w *world) reconcile() (ctrl.Result, error) {
	w.t.Helper()
	return w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: w.key})
}

func (w *world) mustReconcile() ctrl.Result {
	w.t.Helper()
	res, err := w.reconcile()
	if err != nil {
		w.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (w *world) machines() map[string]*v1alpha1.Machine {
	w.t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(v1alpha1.MachineListKind)
	if err := w.c.List(context.Background(), list, client.InNamespace(ns)); err != nil {
		w.t.Fatal(err)
	}
	out := map[string]*v1alpha1.Machine{}
	for i := range list.Items {
		m, err := v1alpha1.MachineFromUnstructured(&list.Items[i])
		if err != nil {
			w.t.Fatal(err)
		}
		out[m.Name] = m
	}
	return out
}

func (w *world) source() *v1alpha1.MachineSource {
	w.t.Helper()
	u := newObject(v1alpha1.MachineSourceKind)
	if err := w.c.Get(context.Background(), w.key, u); err != nil {
		w.t.Fatal(err)
	}
	ms, err := v1alpha1.MachineSourceFromUnstructured(u)
	if err != nil {
		w.t.Fatal(err)
	}
	return ms
}

func (w *world) deleteNode(name string) {
	w.t.Helper()
	if err := w.c.Delete(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) setSpec(path []string, value interface{}) {
	w.t.Helper()
	u := newObject(v1alpha1.MachineSourceKind)
	if err := w.c.Get(context.Background(), w.key, u); err != nil {
		w.t.Fatal(err)
	}
	if err := unstructured.SetNestedField(u.Object, value, append([]string{"spec"}, path...)...); err != nil {
		w.t.Fatal(err)
	}
	if err := w.c.Update(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
}

func condition(ms *v1alpha1.MachineSource, kind string) metav1.ConditionStatus {
	if c := meta.FindStatusCondition(ms.Status.Conditions, kind); c != nil {
		return c.Status
	}
	return ""
}

func TestReconcileNodesCreatesMachinesAndMarksStale(t *testing.T) {
	w := newWorld(t, nil, nil, "cp-1", "w-1", "w-2")
	if res := w.mustReconcile(); res != (ctrl.Result{}) {
		t.Fatalf("result %+v, want no requeue", res)
	}
	got := w.machines()
	if len(got) != 3 {
		t.Fatalf("%d Machines, want 3", len(got))
	}
	for _, name := range []string{"cp-1", "w-1", "w-2"} {
		m := got[name]
		if m == nil {
			t.Fatalf("no Machine %s", name)
		}
		if m.Spec != (v1alpha1.MachineSpec{SourceRef: v1alpha1.SourceRef{Name: "nodes"}, ExternalID: name, Class: "node"}) {
			t.Errorf("%s: spec %+v", name, m.Spec)
		}
		wantLabels := map[string]string{
			"machines.openova.io/class": "node", "machines.openova.io/ownership": "declared", "machines.openova.io/os-family": "ubuntu",
			"machines.openova.io/phase": "active", "machines.openova.io/source": "nodes",
		}
		if !reflect.DeepEqual(m.Labels, wantLabels) {
			t.Errorf("%s: labels %v", name, m.Labels)
		}
		st := m.Status
		if st.Phase != "Active" || st.Ownership != "declared" || *st.OwnerRef != (v1alpha1.OwnerRef{APIVersion: "v1", Kind: "Node", Name: name}) ||
			st.ProviderID != "k3s://"+name || st.DisplayName != name || st.PrimaryAddress != "10.0.0.1" ||
			*st.Fingerprint != (v1alpha1.Fingerprint{FirmwareUUID: "u-" + name, MachineID: "m-" + name}) ||
			*st.OS != (v1alpha1.OS{Family: "ubuntu", Version: "22.04", Arch: "x86_64", Kernel: "5.15.0"}) ||
			!st.FirstSeen.Time.Equal(t0) || !st.LastSeen.Time.Equal(t0) || st.LastEnumerationID == "" || st.StaleSince != nil {
			t.Errorf("%s: status %+v", name, st)
		}
	}
	ms := w.source()
	le := ms.Status.LastEnumeration
	if le == nil || !le.Complete || le.Sightings != 3 || le.Created != 3 || le.MarkedStale != 0 || len(le.Scopes) != 1 || le.Scopes[0].Count != 3 {
		t.Fatalf("lastEnumeration %+v", le)
	}
	if condition(ms, "Ready") != "True" || condition(ms, "Complete") != "True" || condition(ms, "StaleHold") != "False" {
		t.Fatalf("conditions %+v", ms.Status.Conditions)
	}

	w.deleteNode("w-2")
	w.now = t0.Add(time.Hour)
	res := w.mustReconcile()
	got = w.machines()
	if len(got) != 3 || got["w-2"].Status.Phase != "Stale" || got["w-2"].Labels["machines.openova.io/phase"] != "stale" || !got["w-2"].Status.StaleSince.Time.Equal(w.now) {
		t.Fatalf("w-2 after its Node was deleted: %+v %v", got["w-2"].Status, got["w-2"].Labels)
	}
	if got["cp-1"].Status.Phase != "Active" || !got["cp-1"].Status.LastSeen.Time.Equal(w.now) || !got["cp-1"].Status.FirstSeen.Time.Equal(t0) {
		t.Fatalf("cp-1 after the second enumeration: %+v", got["cp-1"].Status)
	}
	if le := w.source().Status.LastEnumeration; le.MarkedStale != 1 || le.Created != 0 || le.Sightings != 2 {
		t.Fatalf("lastEnumeration after the delete %+v", le)
	}
	if res.RequeueAfter != 720*time.Hour {
		t.Fatalf("requeue after %v, want the retire time 720h", res.RequeueAfter)
	}

	if err := w.c.Create(context.Background(), node("w-2")); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(2 * time.Hour)
	w.mustReconcile()
	if m := w.machines()["w-2"]; m.Status.Phase != "Active" || m.Status.StaleSince != nil || !m.Status.FirstSeen.Time.Equal(t0) {
		t.Fatalf("rule6: w-2 after its Node came back: %+v", m.Status)
	}
}

func TestReconcileRetiresAndPurges(t *testing.T) {
	w := newWorld(t, nil, nil, "a", "b")
	w.mustReconcile()
	w.deleteNode("b")
	w.mustReconcile()
	w.now = t0.Add(720 * time.Hour)
	res := w.mustReconcile()
	if m := w.machines()["b"]; m.Status.Phase != "Retired" || m.Labels["machines.openova.io/phase"] != "retired" {
		t.Fatalf("b after retireAfter: %+v", m.Status)
	}
	if res.RequeueAfter != 2160*time.Hour {
		t.Fatalf("requeue after %v, want the purge time 2160h", res.RequeueAfter)
	}
	w.now = t0.Add(2880 * time.Hour)
	if res := w.mustReconcile(); res != (ctrl.Result{}) {
		t.Fatalf("result after the purge %+v", res)
	}
	if got := w.machines(); len(got) != 1 || got["b"] != nil {
		t.Fatalf("b was not deleted after purgeAfter: %v", got)
	}
}

func TestReconcileHoldsAndReleases(t *testing.T) {
	var names []string
	for i := 0; i < 12; i++ {
		names = append(names, fmt.Sprintf("n%02d", i))
	}
	w := newWorld(t, nil, nil, names...)
	w.mustReconcile()
	for _, n := range names[2:] {
		w.deleteNode(n)
	}
	w.mustReconcile()
	for name, m := range w.machines() {
		if m.Status.Phase != "Active" {
			t.Fatalf("rule5: %s was marked while 10 of 12 were missing", name)
		}
	}
	ms := w.source()
	if ms.Status.Held == nil || ms.Status.Held.WouldMarkStale != 10 || ms.Status.Held.Live != 12 || condition(ms, "StaleHold") != "True" {
		t.Fatalf("rule5: no hold recorded: %+v %+v", ms.Status.Held, ms.Status.Conditions)
	}
	w.setSpec([]string{"stale", "releaseHoldFor"}, ms.Status.Held.EnumerationID)
	w.mustReconcile()
	stale := 0
	for _, m := range w.machines() {
		if m.Status.Phase == "Stale" {
			stale++
		}
	}
	if ms := w.source(); stale != 10 || ms.Status.Held != nil || condition(ms, "StaleHold") != "False" {
		t.Fatalf("rule5: the release marked %d, held %+v", stale, ms.Status.Held)
	}
}

func TestReconcileFailedEnumerationMarksNothing(t *testing.T) {
	fail := false
	funcs := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*corev1.NodeList); ok && fail {
			return errors.New("connection refused")
		}
		return c.List(ctx, list, opts...)
	}}
	w := newWorld(t, nil, funcs, "a", "b")
	w.mustReconcile()
	fail = true
	if _, err := w.reconcile(); err == nil {
		t.Fatal("rule3: a failed enumeration must return its error")
	}
	for name, m := range w.machines() {
		if m.Status.Phase != "Active" {
			t.Fatalf("rule3: %s was marked by a failed enumeration", name)
		}
	}
	if ms := w.source(); condition(ms, "Ready") != "False" || condition(ms, "Complete") != "False" || ms.Status.LastEnumeration.Complete {
		t.Fatalf("rule3: conditions after a failed enumeration %+v", ms.Status.Conditions)
	}
}

func TestReconcileSuspendedEnumeratesNothing(t *testing.T) {
	w := newWorld(t, nil, nil, "a")
	w.setSpec([]string{"suspended"}, true)
	w.mustReconcile()
	if got := w.machines(); len(got) != 0 {
		t.Fatalf("a suspended source created %d Machines", len(got))
	}
	if ms := w.source(); condition(ms, "Ready") != "False" || ms.Status.LastEnumeration != nil {
		t.Fatalf("suspended: %+v", ms.Status)
	}
}

func TestReconcileRemovesForeignLabels(t *testing.T) {
	w := newWorld(t, nil, nil, "a")
	w.mustReconcile()
	u := newObject(v1alpha1.MachineKind)
	if err := w.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "a"}, u); err != nil {
		t.Fatal(err)
	}
	labels := u.GetLabels()
	labels["team"] = "x"
	u.SetLabels(labels)
	if err := w.c.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	w.mustReconcile()
	if l := w.machines()["a"].Labels; len(l) != 5 || l["team"] != "" {
		t.Fatalf("labels %v, want only the five of the controller", l)
	}
}

func TestReconcileGoneSourceAndBadSpec(t *testing.T) {
	w := newWorld(t, map[string]interface{}{"retireAfter": "soon"}, nil, "a")
	w.mustReconcile()
	if ms := w.source(); condition(ms, "Ready") != "False" || len(w.machines()) != 0 {
		t.Fatalf("a bad spec.stale must stop the source: %+v", ms.Status.Conditions)
	}
	w.key.Name = "gone"
	if _, err := w.reconcile(); err != nil {
		t.Fatalf("a MachineSource that is gone: %v", err)
	}
}

func TestNodeEvents(t *testing.T) {
	w := newWorld(t, nil, nil)
	reqs := w.r.NodeSources(context.Background(), node("a"))
	if len(reqs) != 1 || reqs[0].NamespacedName != w.key {
		t.Fatalf("a Node event maps to %v, want the one nodes source", reqs)
	}
	p := NodeFactsChanged()
	old, beat := node("a"), node("a")
	beat.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: beat}) {
		t.Error("a Node update that changes no recorded fact passed")
	}
	moved := node("a")
	moved.Status.Addresses[0].Address = "10.0.0.9"
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: moved}) {
		t.Error("a Node update that changes an address did not pass")
	}
	if !p.Create(event.CreateEvent{Object: old}) || !p.Delete(event.DeleteEvent{Object: old}) {
		t.Error("a Node that is added or removed must pass")
	}
}
