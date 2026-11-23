package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/observe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/reboot"
	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// A move and a drain take turns, end to end: two regions, each with its own
// API holding kured's DaemonSet and its nodes, each with its own Continuum.

var (
	daemonSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	nodeGVR      = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

const kuredLockKey = "weave.works/kured-node-lock"

func kuredDaemonSet(lock string) *unstructured.Unstructured {
	ann := map[string]interface{}{"catalyst.openova.io/kured-install-hold": "released"}
	if lock != "" {
		ann[kuredLockKey] = lock
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]interface{}{"name": "kured", "namespace": "kube-system", "annotations": ann},
		"spec": map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{"name": "kured",
				"args": []interface{}{"--ds-name=kured", "--period=30m", "--concurrency=1", "--lock-ttl=1h", "--lock-annotation=" + kuredLockKey}}},
		}}},
	}}
}

func workerNode(name string, cordoned bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]interface{}{"name": name},
		"spec": map[string]interface{}{"unschedulable": cordoned},
	}}
}

func regionDyn(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		ContinuumGVR: "ContinuumList", observe.ApplicationGVR: "ApplicationList", daemonSetGVR: "DaemonSetList", nodeGVR: "NodeList",
	}, append([]runtime.Object{continuumCR(regionA)}, objs...)...)
}

// kuredLock is kured 1.23's single lock value (pkg/daemonsetlock).
type kuredLock struct {
	NodeID  string        `json:"nodeID"`
	Created time.Time     `json:"created"`
	TTL     time.Duration `json:"TTL"`
}

func lockOf(t *testing.T, dyn *dynfake.FakeDynamicClient) (kuredLock, bool) {
	t.Helper()
	ds, err := dyn.Resource(daemonSetGVR).Namespace("kube-system").Get(context.Background(), "kured", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := ds.GetAnnotations()[kuredLockKey]
	var l kuredLock
	if ok {
		if err := json.Unmarshal([]byte(v), &l); err != nil {
			t.Fatalf("kured cannot read its lock %q: %v", v, err)
		}
	}
	return l, ok
}

// kuredAcquire is kured 1.23's DaemonSetSingleLock.Acquire (and its expiry
// test) on the fake API, at the test clock: it takes the lock for node unless
// a live lock names someone else.
func kuredAcquire(t *testing.T, dyn *dynfake.FakeDynamicClient, node string, now time.Time) (bool, string) {
	t.Helper()
	if l, ok := lockOf(t, dyn); ok && (l.TTL <= 0 || now.Sub(l.Created) < l.TTL) {
		return l.NodeID == node, l.NodeID
	}
	ds, _ := dyn.Resource(daemonSetGVR).Namespace("kube-system").Get(context.Background(), "kured", metav1.GetOptions{})
	b, _ := json.Marshal(kuredLock{NodeID: node, Created: now, TTL: time.Hour})
	ann := ds.GetAnnotations()
	ann[kuredLockKey] = string(b)
	ds.SetAnnotations(ann)
	if _, err := dyn.Resource(daemonSetGVR).Namespace("kube-system").Update(context.Background(), ds, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	return true, node
}

// kuredFinishes is kured after the node's restart: it uncordons the node and
// releases its lock.
func kuredFinishes(t *testing.T, dyn *dynfake.FakeDynamicClient, node string) {
	t.Helper()
	n, _ := dyn.Resource(nodeGVR).Get(context.Background(), node, metav1.GetOptions{})
	_ = unstructured.SetNestedField(n.Object, false, "spec", "unschedulable")
	if _, err := dyn.Resource(nodeGVR).Update(context.Background(), n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ds, _ := dyn.Resource(daemonSetGVR).Namespace("kube-system").Get(context.Background(), "kured", metav1.GetOptions{})
	ann := ds.GetAnnotations()
	delete(ann, kuredLockKey)
	ds.SetAnnotations(ann)
	if _, err := dyn.Resource(daemonSetGVR).Namespace("kube-system").Update(context.Background(), ds, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// holdPeers serves a peer's observations and its reboot agent, as the
// peer's /v1/observations does.
type holdPeers struct {
	s      *sim
	agents map[string]*reboot.Agent
	clock  *time.Time
}

func (p holdPeers) Fetch(ctx context.Context, region string, ts []observe.Target) (map[string]transition.MemberStatus, *transition.RebootStatus, error) {
	members, _, err := simPeers{p.s}.Fetch(ctx, region, ts)
	if err != nil {
		return nil, nil, err
	}
	rs := p.agents[region].Read(ctx, *p.clock)
	return members, &rs, nil
}

type holdWorld struct {
	store  *fakeStore
	s      *sim
	dyn    map[string]*dynfake.FakeDynamicClient
	ctl    map[string]*Controller
	clock  time.Time
	rounds int
}

// newHoldWorld: the writer is in A; every file declares B (the move A → B is
// asked for); regionB may hold extra objects (a lock kured holds, its node).
func newHoldWorld(t *testing.T, extraB ...runtime.Object) *holdWorld {
	w := &holdWorld{store: newFakeStore(), clock: time.Date(2026, 11, 21, 18, 15, 0, 0, time.UTC)}
	w.store.files[regionfile.AppPath(regionA, "wp")] = regionFile(t, regionA, regionA, regionB)
	w.store.files[regionfile.AppPath(regionB, "wp")] = regionFile(t, regionB, regionA, regionB)
	w.s = &sim{store: w.store, dbs: map[string]*liveDB{regionA: {primary: true}, regionB: {}}, down: map[string]bool{}}
	b := extraB
	if len(b) == 0 {
		b = []runtime.Object{kuredDaemonSet(""), workerNode("wb1", false)}
	}
	w.dyn = map[string]*dynfake.FakeDynamicClient{
		regionA: regionDyn(kuredDaemonSet(""), workerNode("wa1", false)),
		regionB: regionDyn(b...),
	}
	agents := map[string]*reboot.Agent{}
	for _, r := range []string{regionA, regionB} {
		agents[r] = &reboot.Agent{Dyn: w.dyn[r], HolderID: "continuum-" + r, TTL: 5 * time.Minute}
	}
	now := func() time.Time { return w.clock }
	w.ctl = map[string]*Controller{}
	for _, r := range []string{regionA, regionB} {
		w.ctl[r] = &Controller{Dyn: w.dyn[r], Store: w.store, Local: simLocal{w.s, r}, Peers: holdPeers{w.s, agents, &w.clock},
			Region: r, Now: now, Reboot: agents[r]}
	}
	return w
}

// round: both regions tick (order alternates), then Flux + CNPG apply Git.
// after, when set, runs after each region's tick.
func (w *holdWorld) round(t *testing.T, after ...func()) {
	t.Helper()
	first, second := w.ctl[regionA], w.ctl[regionB]
	if w.rounds%2 == 1 {
		first, second = second, first
	}
	for _, c := range []*Controller{first, second} {
		if err := c.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, f := range after {
			f()
		}
	}
	w.s.step(t)
	w.clock = w.clock.Add(10 * time.Second)
	w.rounds++
}

func (w *holdWorld) fileCommits() int {
	n := 0
	for _, c := range w.store.commits {
		if strings.HasPrefix(c, "regions/") {
			n++
		}
	}
	return n
}

func interrupted(t *testing.T, c *Controller) map[string]interface{} {
	t.Helper()
	cr, err := c.Dyn.Resource(ContinuumGVR).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conds, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	for _, x := range conds {
		if cm, ok := x.(map[string]interface{}); ok && cm["type"] == "Interrupted" {
			return cm
		}
	}
	return nil
}

func TestRebootAgent_HeldWhileAMoveRuns_ReleasedWhenItEnds(t *testing.T) {
	w := newHoldWorld(t)
	sawHeld := false
	for i := 0; i < 40; i++ {
		// After every single tick: the first region file is committed only
		// once both regions hold their reboot agent.
		w.round(t, func() {
			la, okA := lockOf(t, w.dyn[regionA])
			lb, okB := lockOf(t, w.dyn[regionB])
			bothHeld := okA && okB && la.NodeID == "continuum-"+regionA && lb.NodeID == "continuum-"+regionB
			if w.fileCommits() == 1 && !bothHeld && !sawHeld {
				t.Fatalf("round %d: the move committed before both regions held their reboot agent (a=%+v b=%+v)", i, la, lb)
			}
		})
		if w.fileCommits() > 0 && w.fileCommits() < 2 {
			// The move is in progress: kured can take the lock in neither region.
			for r, n := range map[string]string{regionA: "wa1", regionB: "wb1"} {
				if ok, holder := kuredAcquire(t, w.dyn[r], n, w.clock); ok || holder != "continuum-"+r {
					t.Fatalf("round %d: kured took its lock in %s during the move (holder %q)", i, r, holder)
				}
			}
			sawHeld = true
		}
	}
	if !sawHeld {
		t.Fatal("the move never ran with the reboot agent held")
	}
	if !w.s.dbs[regionB].primary || w.s.dbs[regionA].primary || w.fileCommits() != 2 {
		t.Fatalf("the move did not converge: %+v %+v commits=%d", w.s.dbs[regionA], w.s.dbs[regionB], w.fileCommits())
	}
	// Ended: the hold is released in both regions and kured takes its lock.
	for r, n := range map[string]string{regionA: "wa1", regionB: "wb1"} {
		if l, ok := lockOf(t, w.dyn[r]); ok {
			t.Fatalf("%s still holds kured after the move: %+v", r, l)
		}
		if ok, _ := kuredAcquire(t, w.dyn[r], n, w.clock); !ok {
			t.Fatalf("kured cannot take its lock in %s after the move", r)
		}
	}
}

func TestRebootAgent_HoldLapsesWhenTheControllerIsGone(t *testing.T) {
	w := newHoldWorld(t)
	for i := 0; i < 3; i++ {
		w.round(t)
	}
	lb, ok := lockOf(t, w.dyn[regionB])
	if !ok || lb.NodeID != "continuum-"+regionB {
		t.Fatalf("B does not hold its reboot agent during the move: %+v", lb)
	}
	// The controllers are gone (nothing ticks any more). Before the TTL has
	// passed kured still cannot drain; once it has, kured's own expiry test
	// frees the lock and the node takes it.
	if ok, _ := kuredAcquire(t, w.dyn[regionB], "wb1", lb.Created.Add(5*time.Minute-time.Second)); ok {
		t.Fatal("the hold lapsed before its TTL")
	}
	if ok, _ := kuredAcquire(t, w.dyn[regionB], "wb1", lb.Created.Add(5*time.Minute)); !ok {
		t.Fatal("the hold of a gone controller must lapse at its TTL")
	}
}

// leaseWriteFails is the writer Gitea refusing every lease write (a 5xx, not
// a compare-and-swap conflict) while fail is set.
type leaseWriteFails struct {
	*fakeStore
	fail *bool
}

func (s leaseWriteFails) Write(ctx context.Context, repo Repo, path string, data []byte, sha, msg string) error {
	if *s.fail && strings.Contains(path, "continuum/transitions/") {
		return fmt.Errorf("gitea: 502 Bad Gateway")
	}
	return s.fakeStore.Write(ctx, repo, path, data, sha, msg)
}

func TestRebootAgent_HoldIsNotReleasedWhileALeaseWriteFailsMidMove(t *testing.T) {
	w := newHoldWorld(t)
	fail := false
	for _, c := range w.ctl {
		c.Store = leaseWriteFails{w.store, &fail}
	}
	for i := 0; i < 40 && w.fileCommits() == 0; i++ {
		w.round(t)
	}
	if w.fileCommits() != 1 {
		t.Fatalf("the move did not start: commits=%d", w.fileCommits())
	}
	// The move is in progress and the writer Gitea fails: the region that
	// holds the lease cannot write it. Its group reports an error, and the
	// hold must be left as it is, not released in the middle of the move.
	fail = true
	failed := 0
	for i := 0; i < 6; i++ {
		for _, r := range []string{regionA, regionB} {
			if err := w.ctl[r].Reconcile(context.Background()); err != nil {
				failed++
			}
			for rr, n := range map[string]string{regionA: "wa1", regionB: "wb1"} {
				if ok, holder := kuredAcquire(t, w.dyn[rr], n, w.clock); ok || holder != "continuum-"+rr {
					t.Fatalf("tick %d of %s: kured took its lock in %s while a lease write failed mid-move (holder %q)", i, r, rr, holder)
				}
			}
		}
		w.s.step(t)
		w.clock = w.clock.Add(10 * time.Second)
	}
	if failed == 0 {
		t.Fatal("no lease write was attempted while the writer failed: the test proves nothing")
	}
	// The writer recovers: the move converges and the hold is released.
	fail = false
	for i := 0; i < 40; i++ {
		w.round(t)
	}
	if !w.s.dbs[regionB].primary || w.fileCommits() != 2 {
		t.Fatalf("the move did not converge after the writer recovered: %+v commits=%d", w.s.dbs[regionB], w.fileCommits())
	}
	for _, r := range []string{regionA, regionB} {
		if l, ok := lockOf(t, w.dyn[r]); ok {
			t.Fatalf("%s still holds kured after the move: %+v", r, l)
		}
	}
}

func TestMove_DoesNotStartWhileANodeIsDrained_AndSaysSoOnTheContinuum(t *testing.T) {
	start := time.Date(2026, 11, 21, 18, 15, 0, 0, time.UTC)
	b, _ := json.Marshal(kuredLock{NodeID: "wb1", Created: start, TTL: time.Hour})
	w := newHoldWorld(t, kuredDaemonSet(string(b)), workerNode("wb1", true))
	for i := 0; i < 6; i++ {
		w.round(t)
	}
	if n := w.fileCommits(); n != 0 {
		t.Fatalf("the move committed %d region files while kured drains wb1 in B", n)
	}
	if l, _ := lockOf(t, w.dyn[regionB]); l.NodeID != "wb1" {
		t.Fatalf("kured's lock was written over: %+v", l)
	}
	for _, r := range []string{regionA, regionB} {
		c := interrupted(t, w.ctl[r])
		if c == nil || c["reason"] != transition.ReasonNodeDraining || c["status"] != "True" || !strings.Contains(c["message"].(string), "wb1") {
			t.Fatalf("%s: the Continuum must name the drain it waits for, got %v", r, c)
		}
	}
	// kured finishes (node back, lock released): B holds its agent, the move
	// runs and converges, and the condition is gone.
	kuredFinishes(t, w.dyn[regionB], "wb1")
	for i := 0; i < 40; i++ {
		w.round(t)
	}
	if !w.s.dbs[regionB].primary || w.fileCommits() != 2 {
		t.Fatalf("the move did not run after the drain: %+v commits=%d", w.s.dbs[regionB], w.fileCommits())
	}
	for _, r := range []string{regionA, regionB} {
		if c := interrupted(t, w.ctl[r]); c != nil {
			t.Fatalf("%s: a converged move carries no Interrupted condition, got %v", r, c)
		}
	}
}
