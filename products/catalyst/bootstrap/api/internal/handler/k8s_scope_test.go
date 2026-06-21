// k8s_scope_test.go — #3987: the per-deployment Cloud Nodes / k8s-list
// pages must show ONLY the requested deployment's clusters. The
// mothership process cache holds EVERY managed Sovereign's clusters at
// once (one kubeconfig per deployment), so a blind fan-out across the
// whole cache leaked sibling — and stale wiped — deployments' Nodes into
// the page being viewed (the hw178 page rendering 39 nodes spanning
// hw136 + two wiped deployments). deploymentScopedClusterIDs is the
// read-tier scope that fixes it.

package handler

import (
	"io"
	"log/slog"
	"sort"
	"testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDeploymentScopedClusterIDs_ExcludesSiblingAndWipedDeployments(t *testing.T) {
	const (
		depA       = "02bc518fcb1b4cb3" // the deployment whose page we view (hw178)
		depASecond = depA + "-me-east-215-b-1"
		depB       = "3a2ee904b1d0366a" // a live SIBLING deployment (hw136)
		depBSecond = depB + "-me-east-215-b-1"
		wiped      = "068f217746ca7779" // a wiped deployment whose stale id lingered
	)

	// Register all five clusters in ONE process cache, exactly as the
	// mothership does (every managed Sovereign's kubeconfigs loaded into
	// the same Factory).
	cache := newK8sCacheWithClusters(t, map[string]*dynamicfake.FakeDynamicClient{
		depA:       fakeHRDynamicClient(t),
		depASecond: fakeHRDynamicClient(t),
		depB:       fakeHRDynamicClient(t),
		depBSecond: fakeHRDynamicClient(t),
		wiped:      fakeHRDynamicClient(t),
	})

	h := &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}

	// Scope to depA. We must get depA + its OWN secondary, and NOTHING
	// from depB or the wiped deployment.
	got := h.deploymentScopedClusterIDs(depA)
	sort.Strings(got)

	want := []string{depA, depASecond}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("scoped fan-out leaked sibling/wiped clusters: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scoped fan-out mismatch at %d: got %v, want %v", i, got, want)
		}
	}

	// Explicit cross-deployment assertions — the exact #3987 leak.
	for _, leaked := range []string{depB, depBSecond, wiped} {
		for _, id := range got {
			if id == leaked {
				t.Fatalf("deployment %s leaked cluster %q from a different deployment", depA, leaked)
			}
		}
	}

	// And the symmetric case: scoping to depB returns only depB's set,
	// never depA's — so neither deployment's page bleeds into the other.
	gotB := h.deploymentScopedClusterIDs(depB)
	sort.Strings(gotB)
	wantB := []string{depB, depBSecond}
	sort.Strings(wantB)
	if len(gotB) != len(wantB) {
		t.Fatalf("depB scope wrong: got %v, want %v", gotB, wantB)
	}
	for i := range wantB {
		if gotB[i] != wantB[i] {
			t.Fatalf("depB scope mismatch: got %v, want %v", gotB, wantB)
		}
	}
}
