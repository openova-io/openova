// informer_sync_watcher_release_5642_test.go — #5642 SECOND defect: the
// RETENTION mechanism, as distinct from the rebuild DRIVER #5645 removed.
//
// Defect: runSyncWatcher spawned one goroutine per informer that blocked in
// cache.WaitForCacheSync on `ctx.Done()` — f.runCtx, the PROCESS-lifetime
// context. Nothing in that goroutine observed cs.stop. So every path that
// supersedes or tears a clusterState down —
//
//	AddCluster's rebuild branch  (prev.stopOnce.Do(close(prev.stop)))
//	RemoveCluster on a wipe      (#156)
//	QuarantineDeployment         (#5285, terminal-FAILED conclusion)
//	the rescan prune             (#3987, kubeconfig vanished)
//
// — closed cs.stop and dropped the map entry while leaving the watcher
// goroutines parked for the rest of the process's life. The closure
// captures `cs`, so ONE surviving watcher pins the WHOLE superseded
// clusterState: every informer it holds, every Indexer those informers
// hold, and therefore a full retained copy of that cluster's object graph.
//
// That is the memory that was never reclaimed on hw292 (dep
// 1c56518035a83e03): live heap +0.6 MB/s, RSS +1.26 MB/s, OOMKilled at the
// 4Gi limit on a ~60-minute metronome — restartCount 15 when #5642 was
// filed, 22 on 2026-08-04, 66 by 2026-08-06.
//
// Why #5645 did not close this. #5645 made AddCluster a no-op on a
// BYTE-IDENTICAL kubeconfig, which removes the level-triggered driver that
// was firing ~2 rebuilds per 5 minutes. It says so itself and files the
// residue as a follow-up. But removing one driver is not the same as
// making a teardown safe, and rebuilds still happen by design:
//
//   - a CHANGED kubeconfig must rebuild — token rotation (#5210), the
//     EIP -> private-SAN heal (#3991/#4000);
//   - a pre-built-client ClusterRef carries no fingerprint (kubeconfigSHA
//     is ""), so the #5645 gate cannot fire and it ALWAYS rebuilds;
//   - every wipe / quarantine / prune tears a set down outright.
//
// Each of those permanently retained a whole cluster's object graph.
//
// OBSERVABLE. These tests count goroutines currently parked inside
// runSyncWatcher, read from a live runtime.Stack dump. That is the
// retention root itself rather than a proxy for it: a parked watcher is
// exactly what keeps the superseded clusterState reachable.
//
// DIRECTIONS. Two guards assert release (both report RED on the unfixed
// tree). Three controls assert the watcher still does its job and still
// stops on process shutdown — they PASS on both trees, so the guards
// cannot be satisfied by removing or short-circuiting the watcher.
package k8scache

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// swLiveWatchers returns the number of goroutines whose stack is inside
// runSyncWatcher right now. Counts whole goroutine blocks (the dump
// separates them by a blank line), not frame occurrences, so a goroutine
// appearing under more than one runSyncWatcher frame still counts once.
func swLiveWatchers() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "runSyncWatcher") {
			n++
		}
	}
	return n
}

// swBaseline settles, then returns the watcher count to measure against.
// Every assertion in this file is a DELTA from this baseline, never an
// absolute count: sibling tests in the package also start factories, and
// on the UNFIXED tree their watchers never exit, so an absolute count
// would be polluted by whatever ran before. A delta measures only what the
// test in front of it created.
func swBaseline() int {
	// The FLOOR over a settle window, not a "has it stopped moving yet"
	// guess. A previous test's watchers may still be draining, and a drain
	// has plateaus — a settle heuristic latches onto one and returns an
	// inflated baseline, which would let a later reading come out at or
	// below it and pass an assertion vacuously. The minimum cannot be
	// inflated by a drain in progress, and a drain that continues after the
	// window can never push a later reading below it.
	min := swLiveWatchers()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if cur := swLiveWatchers(); cur < min {
			min = cur
		}
	}
	return min
}

// swWaitUntilDeltaAtMost polls until (live - base) <= want, or the
// deadline expires. Returns the last delta. Goroutine exit is
// asynchronous, so the assertion has to be "settles at or below", never a
// single instantaneous sample.
func swWaitUntilDeltaAtMost(base, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	got := swLiveWatchers() - base
	for time.Now().Before(deadline) {
		if got <= want {
			return got
		}
		time.Sleep(50 * time.Millisecond)
		got = swLiveWatchers() - base
	}
	return got
}

// swWaitUntilDeltaAtLeast polls until (live - base) >= want, or the
// deadline expires. Returns the last delta. Used for PRECONDITIONS:
// goroutines are spawned with `go` and are not on any stack until the
// scheduler runs them, so a single sample immediately after AddCluster can
// read 0 even though the watchers exist.
func swWaitUntilDeltaAtLeast(base, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	got := swLiveWatchers() - base
	for time.Now().Before(deadline) {
		if got >= want {
			return got
		}
		time.Sleep(50 * time.Millisecond)
		got = swLiveWatchers() - base
	}
	return got
}

// swKinds is the size of the registry these tests drive. Deliberately
// small and non-Optional so AddCluster takes the no-discovery-probe fast
// path (#5352). Production registers 42 kinds, so the live leak is 21x
// what these numbers show.
const swKinds = 2

func swRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true}); err != nil {
		t.Fatalf("registry add pod: %v", err)
	}
	if err := r.Add(Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}}); err != nil {
		t.Fatalf("registry add namespace: %v", err)
	}
	return r
}

// swMaxForOneLiveSet is the ceiling on parked watchers when exactly ONE
// informer set is live: one per kind, plus at most one shared
// stop-multiplexer goroutine. Derived from the registry, NOT measured from
// the tree under test — so the bound means the same thing before and after
// the fix and cannot drift to fit whatever the code happens to do.
const swMaxForOneLiveSet = swKinds + 1

// ---------------------------------------------------------------------------
// GUARD 1 — a rebuild must RELEASE the set it supersedes.
// ---------------------------------------------------------------------------

// TestSupersededInformerSet_ReleasesSyncWatchers_5642 drives the rebuild
// path with a kubeconfig whose bytes CHANGE every time, so the #5645
// idempotence gate deliberately does not fire and every call really does
// build a new informer set. The property under test is that the SUPERSEDED
// sets are released: parked watcher goroutines must reflect one live set,
// not one per rebuild.
//
// Unfixed tree: linear in the number of rebuilds.
// Fixed tree: constant.
func TestSupersededInformerSet_ReleasesSyncWatchers_5642(t *testing.T) {
	const rebuilds = 12

	dir := t.TempDir()
	path := dir + "/1c56518035a83e03-me-east-215-b-1.yaml"
	writeKubeconfig(t, path, "token-0")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: swRegistry(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer f.Stop()
	base := swBaseline()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ref := ClusterRef{ID: "1c56518035a83e03-me-east-215-b-1", KubeconfigPath: path}
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster (initial): %v", err)
	}

	for i := 1; i <= rebuilds; i++ {
		// Changed bytes => the #5645 gate must NOT fire; this is a real
		// rebuild, the shape a rotated token (#5210) produces in production.
		writeKubeconfig(t, path, fmt.Sprintf("token-%d", i))
		if err := f.AddCluster(ref); err != nil {
			t.Fatalf("AddCluster rebuild %d: %v", i, err)
		}
	}

	got := swWaitUntilDeltaAtMost(base, swMaxForOneLiveSet, 10*time.Second)
	if got > swMaxForOneLiveSet {
		t.Fatalf("after %d rebuilds of ONE cluster id, %d sync-watcher goroutines are still parked; want <= %d (one live set of %d kinds).\n"+
			"Each parked watcher captures the superseded *clusterState, pinning all %d of its informers and their Indexers — a full retained copy of that cluster's object graph per rebuild. "+
			"That is the unreclaimed memory behind the hw292 OOMKill metronome (#5642); #5645 removed one rebuild DRIVER but left the teardown unable to release.",
			rebuilds, got, swMaxForOneLiveSet, swKinds, swKinds)
	}

	// The surviving registration must still be a working one — a "release"
	// that also dropped the live cluster would pass the count assertion.
	if !containsClusterID(f.Clusters(), ref.ID) {
		t.Fatalf("Clusters() = %v, want to contain %q", f.Clusters(), ref.ID)
	}
	if cs := clusterStateFor(f, ref.ID); cs == nil || len(cs.informers) != swKinds {
		t.Fatalf("live clusterState after rebuilds = %+v; want %d informers", cs, swKinds)
	}
}

// ---------------------------------------------------------------------------
// GUARD 2 — RemoveCluster must RELEASE, not merely unregister.
// ---------------------------------------------------------------------------

// TestRemoveCluster_ReleasesSyncWatchers_5642 covers the wipe / quarantine
// / prune direction. RemoveCluster closed cs.stop and deleted the map
// entry, which stops the reflectors — but the watcher goroutines were tied
// to the process context, so they stayed parked holding the whole removed
// clusterState. On a Sovereign that provisions and wipes repeatedly, every
// wiped deployment's object graph was retained until the Pod restarted.
//
// Unfixed tree: watchers stay parked after RemoveCluster.
// Fixed tree: they exit.
func TestRemoveCluster_ReleasesSyncWatchers_5642(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/wiped-dep.yaml"
	writeKubeconfig(t, path, "token-wiped")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: swRegistry(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer f.Stop()
	base := swBaseline()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ref := ClusterRef{ID: "wiped-dep", KubeconfigPath: path}
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}

	// Precondition: the watchers really are running, so "0 afterwards" is a
	// state change and not the value the counter had all along.
	if live := swWaitUntilDeltaAtLeast(base, swKinds, 5*time.Second); live < swKinds {
		t.Fatalf("precondition failed: %d new sync-watcher goroutines after AddCluster+Start, want >= %d — the observable is not wired, so this test could never go red", live, swKinds)
	}

	f.RemoveCluster(ref.ID)

	if got := swWaitUntilDeltaAtMost(base, 0, 10*time.Second); got > 0 {
		t.Fatalf("RemoveCluster left %d sync-watcher goroutines parked; want 0.\n"+
			"Each one captures the removed *clusterState and pins its %d informers plus their Indexers, so a wiped/quarantined deployment's whole object graph stays resident for the life of the process (#5642, #156, #5285).",
			got, swKinds)
	}
}

// ---------------------------------------------------------------------------
// CONTROLS — must PASS on BOTH the unfixed and the fixed tree.
//
// Without these, both guards above are satisfiable by deleting
// runSyncWatcher outright, or by handing WaitForCacheSync an
// already-closed channel. Each control fails under exactly that kind of
// blanket suppression.
// ---------------------------------------------------------------------------

// CONTROL 1 — the watcher still does its actual job: it must observe a
// healthy informer reaching sync and record it. Fails if the watcher is
// removed, never spawned, or handed a pre-closed stop channel
// (WaitForCacheSync would then return false and synced[] would stay false).
func TestSyncWatcher_StillRecordsSynced_Control(t *testing.T) {
	// Registry matched to fakeClients()' scheme (pods + secrets) so the
	// informers actually LIST and reach sync. swRegistry's `namespace` kind
	// is not in that scheme and would panic the fake reflector.
	r := NewRegistry()
	if err := r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true}); err != nil {
		t.Fatalf("registry add pod: %v", err)
	}
	if err := r.Add(Kind{Name: "secret", GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Namespaced: true}); err != nil {
		t.Fatalf("registry add secret: %v", err)
	}

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: r})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer f.Stop()

	// Fake clients sync immediately — the opposite of the black-hole
	// kubeconfig the guards use.
	dyn, core := fakeClients()
	if err := f.AddCluster(ClusterRef{ID: "healthy", DynamicClient: dyn, CoreClient: core}); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.RLock()
		cs := f.clusters["healthy"]
		var ok bool
		if cs != nil {
			ok = cs.synced["pod"] && cs.synced["secret"]
		}
		f.mu.RUnlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("sync-watcher never recorded synced=true for a healthy cluster — the watcher is not doing its job, so the release guards would be passing vacuously")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// CONTROL 2 — the LIVE registration keeps its watchers. Fails if release
// were implemented by suppressing every watcher rather than by scoping
// them to the cluster's own lifetime.
func TestSyncWatcher_LiveClusterKeepsItsWatchers_Control(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/live-dep.yaml"
	writeKubeconfig(t, path, "token-live")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: swRegistry(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer f.Stop()
	base := swBaseline()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := f.AddCluster(ClusterRef{ID: "live-dep", KubeconfigPath: path}); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if swLiveWatchers()-base >= swKinds {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a live, never-synced cluster added %d parked sync-watchers; want >= %d — its informers are unobserved, so nothing would ever flip synced[] for a slow-syncing cluster",
				swLiveWatchers()-base, swKinds)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// CONTROL 3 — process-context cancellation still stops the watchers. This
// is the pre-existing contract; scoping to cs.stop must ADD a stop
// condition, not replace this one.
func TestSyncWatcher_ProcessContextCancelStillStops_Control(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ctx-dep.yaml"
	writeKubeconfig(t, path, "token-ctx")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: swRegistry(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer f.Stop()
	base := swBaseline()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.AddCluster(ClusterRef{ID: "ctx-dep", KubeconfigPath: path}); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}
	if live := swWaitUntilDeltaAtLeast(base, swKinds, 5*time.Second); live < swKinds {
		t.Fatalf("precondition failed: %d new sync-watcher goroutines to cancel, want >= %d", live, swKinds)
	}

	cancel()

	if got := swWaitUntilDeltaAtMost(base, 0, 10*time.Second); got > 0 {
		t.Fatalf("process-context cancel left %d sync-watcher goroutines parked; want 0 — the process-shutdown stop condition regressed", got)
	}
}
