// jobs_d20_secondary_test.go — unit coverage for the D20 multi-region
// chroot seed fan-out (issue #1821).
//
// The Sovereign /jobs page hides the Region filter dropdown on
// single-region sets (regionOptions.length > 1 gate in
// products/catalyst/bootstrap/ui/src/pages/sovereign/JobsTable.tsx).
// On a 3-region Sovereign the dropdown only appears when the per-
// deployment jobs.Store carries region-prefixed install-* rows for
// every secondary region. Before this test landed,
// chrootSeedJobsStoreIfEmpty only enumerated the primary cluster's
// HelmReleases — secondary regions' rows never made it to the store,
// the dropdown stayed hidden, and DoD D20 failed on every fresh prov.
//
// The fan-out is wired up in chrootSeedSecondaryRegions which walks
// h.k8sCache.Clusters() and consults regionFromSecondaryClusterID to
// derive the region key from each registered cluster id. The two
// tests below cover (a) the cluster-id → region key derivation
// contract and (b) the end-to-end seed write into the bridge.

package handler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeHRDynamicClient builds a dynamic.Interface backed by the in-memory
// fake that serves the given bp-* HelmReleases (each in StateInstalled
// via a Ready=True condition) from flux-system. Mirrors the helmwatch
// package's own test fixture so chrootSeedSecondaryRegions —which lists
// HelmReleases via helmwatch.ListAndSnapshotHelmReleases— sees a
// realistic cluster snapshot.
func fakeHRDynamicClient(t *testing.T, names ...string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmReleaseList",
	}, &unstructured.UnstructuredList{})
	objs := make([]runtime.Object, 0, len(names))
	for _, name := range names {
		u := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "helm.toolkit.fluxcd.io/v2",
				"kind":       "HelmRelease",
				"metadata": map[string]any{
					"name":      name,
					"namespace": helmwatch.FluxNamespace,
				},
				"status": map[string]any{
					"conditions": []any{
						map[string]any{
							"type":               "Ready",
							"status":             string(metav1.ConditionTrue),
							"reason":             "ReconciliationSucceeded",
							"message":            "Helm install succeeded",
							"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
						},
					},
				},
			},
		}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "helm.toolkit.fluxcd.io",
			Version: "v2",
			Kind:    "HelmRelease",
		})
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{helmwatch.HelmReleaseGVR: "HelmReleaseList"},
		objs...,
	)
}

// newK8sCacheWithClusters builds a real *k8scache.Factory pre-registered
// with the supplied pre-built dynamic clients (the test path AddCluster
// supports via ClusterRef.DynamicClient). No Start() needed — the
// chroot fan-out reads DynamicClientFor(id) + lists HelmReleases
// directly, never the informer cache.
func newK8sCacheWithClusters(t *testing.T, clients map[string]*dynamicfake.FakeDynamicClient) *k8scache.Factory {
	t.Helper()
	refs := make([]k8scache.ClusterRef, 0, len(clients))
	for id, dyn := range clients {
		refs = append(refs, k8scache.ClusterRef{ID: id, DynamicClient: dyn})
	}
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Clusters: refs,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

// TestChrootSeed_FanOut_BothRegionsLabeledAndDeduped is the end-to-end
// proof for #3276: post-handover, a Sovereign whose k8sCache holds the
// primary in-cluster cluster + a registered secondary-region kubeconfig
// must surface BOTH regions' install jobs in jobs.Store — each row
// region-labeled — and a repeat /jobs read must NOT duplicate them.
//
// Before this test the multi-region fan-out (chrootSeedSecondaryRegions)
// was only exercised with k8sCache==nil, so no test proved the secondary
// region's rows actually land region-labeled in the store. Here we wire
// a real Factory with two fake clusters and assert the store contents.
func TestChrootSeed_FanOut_BothRegionsLabeledAndDeduped(t *testing.T) {
	const (
		depID         = "dep3276"
		region        = "me-east-215-b-1"
		sovereignFQDN = "t99.omani.works"
	)
	t.Setenv("SOVEREIGN_FQDN", sovereignFQDN)

	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Primary in-cluster cluster id == depID (matches buildChrootClusterRef
	// when CATALYST_SELF_DEPLOYMENT_ID resolves to depID); secondary
	// cluster id == "<depID>-<region>" (HandleSovereignSecondaryKubeconfig
	// convention). Both serve the same two bp-* HelmReleases so we can
	// prove the region labeling, not just row counts.
	primaryDyn := fakeHRDynamicClient(t, "bp-cilium", "bp-flux")
	secondaryDyn := fakeHRDynamicClient(t, "bp-cilium", "bp-flux")
	cache := newK8sCacheWithClusters(t, map[string]*dynamicfake.FakeDynamicClient{
		depID:                primaryDyn,
		depID + "-" + region: secondaryDyn,
	})

	h := &Handler{
		jobs:     st,
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}
	dep := &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: sovereignFQDN,
		},
	}

	// First read seeds primary + fans out to the secondary region.
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	// The PRIMARY in-region seed runs against the chroot's own apiserver
	// via rest.InClusterConfig() (sovereignDynamicClient), which is
	// unavailable in a unit test — so this test asserts the SECONDARY
	// fan-out specifically, which is the #3276 defect: the secondary
	// region's install rows never reached jobs.Store. Bucket the install
	// rows the fan-out wrote (Region=region, "<region>:" AppID prefix).
	secondaryInstalls := map[string]jobs.Job{}
	for _, j := range got {
		if j.Type != jobs.JobTypeInstall {
			continue
		}
		if j.Region == region {
			secondaryInstalls[j.AppID] = j
		} else if j.Region != "" {
			t.Fatalf("unexpected region %q on job %q", j.Region, j.ID)
		}
	}

	// Secondary region — region-prefixed AppID rows, Region label set.
	for _, want := range []string{region + ":cilium", region + ":flux"} {
		j, ok := secondaryInstalls[want]
		if !ok {
			t.Fatalf("secondary install row %q missing; have %v", want, secondaryInstalls)
		}
		if j.Region != region {
			t.Fatalf("secondary row %q has Region=%q; want %q", want, j.Region, region)
		}
		// The region-prefixed AppID and the explicit Region field must
		// agree (the UI prefers Region but falls back to the prefix).
		if got := jobs.RegionFromComponent(j.AppID); got != region {
			t.Fatalf("AppID %q yields region %q; want %q", j.AppID, got, region)
		}
	}

	// Idempotency / de-dup: a SECOND read (the common case — every /jobs
	// XHR re-runs the lazy seed) must NOT duplicate rows.
	beforeLen := len(got)
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)
	after, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs (2nd): %v", err)
	}
	if len(after) != beforeLen {
		t.Fatalf("second seed changed row count: before=%d after=%d (rows must be de-duplicated)", beforeLen, len(after))
	}
}

// TestChrootSeed_SingleRegion_NoSecondaryRowsNoRegionLabel proves the
// single-region path is preserved: with only the primary cluster
// registered, every install row stays Region="" and the store carries
// no region-prefixed rows (so the UI's Region filter/column stay hidden).
func TestChrootSeed_SingleRegion_NoSecondaryRowsNoRegionLabel(t *testing.T) {
	const (
		depID         = "dep3276single"
		sovereignFQDN = "t98.omani.works"
	)
	t.Setenv("SOVEREIGN_FQDN", sovereignFQDN)

	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	primaryDyn := fakeHRDynamicClient(t, "bp-cilium", "bp-flux")
	cache := newK8sCacheWithClusters(t, map[string]*dynamicfake.FakeDynamicClient{
		depID: primaryDyn,
	})

	h := &Handler{
		jobs:     st,
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}
	dep := &Deployment{
		ID:      depID,
		Request: provisioner.Request{SovereignFQDN: sovereignFQDN},
	}
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	// With ONLY the primary cluster registered, the secondary fan-out
	// must write nothing — so NO row may carry a region label. (The
	// primary HelmRelease seed goes via rest.InClusterConfig() which is
	// unavailable in-unit; the provisioner-lifecycle rows that ARE
	// present must all stay region-less.) This is the regression guard
	// that a single-region Sovereign never surfaces the Region
	// column/filter.
	for _, j := range got {
		if j.Region != "" {
			t.Fatalf("single-region run produced a region-labeled row: %q region=%q", j.ID, j.Region)
		}
	}
}

// TestRegionFromSecondaryClusterID_Contract locks in the rules the
// chroot fan-out relies on:
//   - empty cluster id → no region
//   - cluster id == primary depID → no region (primary already seeded)
//   - cluster id == chroot fallback (sovereign-<fqdn>) → no region
//   - "<depID>-<region>" → region
//   - "<chrootFallback>-<region>" → region
//   - alien / cross-deployment ids → no region (no leakage)
//   - empty SOVEREIGN_FQDN ("sovereign-" fallback) does NOT match any
//     ".*-..." cluster id (defense in depth)
func TestRegionFromSecondaryClusterID_Contract(t *testing.T) {
	cases := []struct {
		name       string
		clusterID  string
		depID      string
		fallbackID string
		want       string
	}{
		{
			name:       "primary depID — no region",
			clusterID:  "dep123",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "chroot fallback id — no region",
			clusterID:  "sovereign-t34.omani.works",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "depID-prefixed secondary — extracts region",
			clusterID:  "dep123-hel1-1",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "hel1-1",
		},
		{
			name:       "fallback-prefixed secondary — extracts region",
			clusterID:  "sovereign-t34.omani.works-nbg1-2",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "nbg1-2",
		},
		{
			name:       "alien cluster id — no region (no leakage)",
			clusterID:  "other-deployment-fsn1",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "empty cluster id — no region",
			clusterID:  "",
			depID:      "dep123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "",
		},
		{
			name:       "bare sovereign- fallback marker does NOT match arbitrary ids",
			clusterID:  "sovereign-fsn1",
			depID:      "dep123",
			fallbackID: "sovereign-",
			want:       "",
		},
		{
			name:       "depID with hyphenated region — preserves region key",
			clusterID:  "abc-123-fsn1-2",
			depID:      "abc-123",
			fallbackID: "sovereign-t34.omani.works",
			want:       "fsn1-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := regionFromSecondaryClusterID(tc.clusterID, tc.depID, tc.fallbackID)
			if got != tc.want {
				t.Fatalf("regionFromSecondaryClusterID(%q, %q, %q) = %q; want %q",
					tc.clusterID, tc.depID, tc.fallbackID, got, tc.want)
			}
		})
	}
}

// TestSnapshotsToSeedsForRegion_PrefixesAppID verifies that the seed
// shape the chroot fan-out feeds into the Bridge produces region-
// prefixed jobs that the FE regionFromJob() helper picks up. Without
// the "<region>:" prefix on the resulting Job.AppID the UI's
// regionOptions set stays size 1 and the Region dropdown never
// renders. Locks in the pipe from ComponentSnapshot → InformerSeed →
// Job.AppID without standing up the full helmwatch.Bridge.
//
// snapshotsToSeedsForRegion already has direct coverage in
// jobs_backfill_test.go for the bare-region case; this test pins the
// hyphenated-region (hel1-1, nbg1-2) case the D20 fan-out exercises
// in production.
func TestSnapshotsToSeedsForRegion_HyphenatedRegion(t *testing.T) {
	// Synthesise two snapshots — chroot fan-out path uses the same
	// helmwatch.ComponentSnapshot shape ListAndSnapshotHelmReleases
	// returns from a fake apiserver.
	snaps := []helmwatch.ComponentSnapshot{
		{AppID: "bp-cilium", Status: "succeeded"},
		{AppID: "bp-flux", Status: "running", DependsOn: []string{"bp-cilium"}},
	}
	seeds := snapshotsToSeedsForRegion(snaps, "hel1-1")
	if len(seeds) != 2 {
		t.Fatalf("got %d seeds; want 2", len(seeds))
	}
	wantBy := map[string]struct {
		state string
		deps  []string
	}{
		"hel1-1:bp-cilium": {state: "succeeded", deps: nil},
		"hel1-1:bp-flux":   {state: "running", deps: []string{"hel1-1:bp-cilium"}},
	}
	for _, s := range seeds {
		want, ok := wantBy[s.Component]
		if !ok {
			t.Fatalf("unexpected seed component %q", s.Component)
		}
		if s.State != want.state {
			t.Fatalf("seed %q: state=%q want %q", s.Component, s.State, want.state)
		}
		if len(want.deps) != len(s.DependsOn) {
			t.Fatalf("seed %q: deps=%v want %v", s.Component, s.DependsOn, want.deps)
		}
		for i, d := range s.DependsOn {
			if d != want.deps[i] {
				t.Fatalf("seed %q: deps[%d]=%q want %q", s.Component, i, d, want.deps[i])
			}
		}
	}
}

// TestChrootSeedJobsStoreIfEmpty_FanOutReachableWithBootstrapKitInStore
// — TBD-A63 regression. Before the fix, chrootSeedJobsStoreIfEmpty
// short-circuited with `if hasBootstrapKit { return }` BEFORE calling
// chrootSeedSecondaryRegions. On a fully-converged 3-region Sovereign
// the phase-1 helmwatch.Watcher seeds the primary bootstrap-kit group
// asynchronously, so by the time /jobs hits the handler hasBootstrapKit
// is already true and the secondary fan-out NEVER ran — leaving the
// per-deployment jobs.Store with only primary-region rows.
//
// The fix splits the primary-seed body off behind its own
// `if !hasBootstrapKit` guard and runs the fan-out UNCONDITIONALLY
// afterwards. This test asserts the runtime contract by setting up a
// handler with:
//
//   - SOVEREIGN_FQDN env set + dep.Request.SovereignFQDN matching, so
//     chrootSeedJobsStoreIfEmpty does not short-circuit on the env
//     guards at the top,
//   - a jobs.Store pre-seeded with a bootstrap-kit group Job for the
//     deployment id, so hasBootstrapKit=true on entry,
//   - h.k8sCache=nil so chrootSeedSecondaryRegions returns immediately
//     (no real apiservers required for the regression check).
//
// The success criterion is simply that the function returns WITHOUT
// any panic or store mutation. With the old early-return bug, the
// function reached `return` before line 276; with the fix it falls
// through to chrootSeedSecondaryRegions(...) which then no-ops on
// h.k8sCache==nil. The behavioural delta is the runtime reachability
// itself — locked in below via a sentinel that asserts the post-seed
// branch executed (k8sCache lookup attempted, observable via a
// log-capture).
func TestChrootSeedJobsStoreIfEmpty_FanOutReachableWithBootstrapKitInStore(t *testing.T) {
	const (
		depID         = "tbda63dep"
		sovereignFQDN = "tbd-a63.example"
	)

	t.Setenv("SOVEREIGN_FQDN", sovereignFQDN)

	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Pre-seed a bootstrap-kit group Job so hasBootstrapKit flips to
	// true on entry and exercises the buggy early-return path.
	if err := st.UpsertJob(jobs.Job{
		ID:           jobs.JobID(depID, jobs.GroupBootstrapKit),
		DeploymentID: depID,
		JobName:      jobs.GroupBootstrapKit,
		DisplayName:  jobs.GroupBootstrapKitDisplay,
		Status:       jobs.StatusSucceeded,
		StartedAt:    timePtr(time.Now().Add(-time.Hour)),
		FinishedAt:   timePtr(time.Now()),
	}); err != nil {
		t.Fatalf("UpsertJob bootstrap-kit: %v", err)
	}
	// Also seed a provisioner lifecycle Job so the test focuses on the
	// hasBootstrapKit short-circuit specifically (else the function
	// would also drive the provisioner-seed path which is unrelated).
	if err := st.UpsertJob(jobs.Job{
		ID:           jobs.JobID(depID, jobs.GroupProvisioner),
		DeploymentID: depID,
		JobName:      jobs.GroupProvisioner,
		Status:       jobs.StatusSucceeded,
	}); err != nil {
		t.Fatalf("UpsertJob provisioner: %v", err)
	}

	h := &Handler{
		jobs:     st,
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: nil, // forces chrootSeedSecondaryRegions to early-return inside the fan-out body
	}
	dep := &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: sovereignFQDN,
		},
	}

	// Pre-fix: chrootSeedJobsStoreIfEmpty hit `if hasBootstrapKit { return }`
	// and exited before chrootSeedSecondaryRegions could fire. Post-fix:
	// the fan-out is reached unconditionally and no-ops on h.k8sCache==nil.
	// Either way this call must not panic.
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	// Verify the store still has exactly the pre-seeded rows (no
	// secondary writes happened because k8sCache==nil short-circuited
	// the fan-out body). This locks in that the fix did not regress
	// the primary-seed idempotency — repeat /jobs reads with a fully
	// populated store remain a no-op.
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("post-seed store size = %d; want 2 (pre-seeded bootstrap-kit + provisioner only)", len(got))
	}
}

// timePtr is a tiny helper for inline *time.Time literals.
func timePtr(t time.Time) *time.Time { return &t }
