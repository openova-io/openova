// jobs_scanner_exclusion_test.go — end-to-end proof of the Day-2 scanner
// exclusion (issue #3919, founder mandate). It drives the FULL ingestion
// pipeline a live /jobs read uses —
//
//	helmwatch.ListReconcilerObservations  (the cluster-wide GVR list)
//	  → reconcilerObsToBridge             (wire-shape translation)
//	  → jobs.Bridge.SeedReconcilerObservations (project into jobs.Store)
//	  → Handler.flowSnapshotFromJobs      (build the flow-diagram DAG)
//
// and asserts that trivy-operator + syft-grype scanner Jobs produce ZERO
// task/cron LEAVES in jobs.Store AND therefore ZERO scanner NODES + EDGES in
// the convergence DAG, while every genuine reconciler Job survives as its own
// node. The flow diagram is derived strictly from the leaves in jobs.Store, so
// excluding scanners at ingestion is what keeps them out of the graph — this
// test pins that the exclusion holds all the way through the graph builder, not
// just at the observation layer.
package handler

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// scannerExclusionListScheme registers the *List GVKs the dynamic fake needs
// so List(...) resolves for every GVR ListReconcilerObservations enumerates.
func scannerExclusionListScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJobList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "JobList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DeploymentList"},
		&unstructured.UnstructuredList{})
	return scheme
}

func scannerExclusionFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scannerExclusionListScheme(),
		map[schema.GroupVersionResource]string{
			helmwatch.KustomizationGVR: "KustomizationList",
			helmwatch.CronJobGVR:       "CronJobList",
			helmwatch.JobGVR:           "JobList",
			helmwatch.DeploymentGVR:    "DeploymentList",
		},
		objs...,
	)
}

// mkJob builds a batch/v1 Job; ownerCron non-empty stamps a CronJob owner ref.
func mkJob(namespace, name, ownerCron string, labels map[string]string) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": namespace}
	if ownerCron != "" {
		meta["ownerReferences"] = []any{map[string]any{
			"apiVersion": "batch/v1", "kind": "CronJob", "name": ownerCron, "uid": "uid-" + ownerCron,
		}}
	}
	if len(labels) > 0 {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		meta["labels"] = l
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   meta,
		"status": map[string]any{
			"startTime": "2026-06-20T01:00:00Z",
			"conditions": []any{map[string]any{
				"type": "Complete", "status": "True", "lastTransitionTime": "2026-06-20T01:01:00Z",
			}},
		},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
	return u
}

func mkCron(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"schedule": "0 * * * *"},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"})
	return u
}

// TestFlowDiagram_ExcludesDay2Scanners is the DAG-level guard: scanner Jobs
// never become flow-diagram nodes or edges, while real reconciler Jobs do.
func TestFlowDiagram_ExcludesDay2Scanners(t *testing.T) {
	const depID = "dep-scan-excl"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), st)

	objs := []runtime.Object{}

	// --- the scanner flood (must be invisible to the DAG) ---
	// trivy-operator scan Jobs across all three detection paths.
	for i := 0; i < 120; i++ {
		var labels map[string]string
		switch i % 3 {
		case 0:
			labels = map[string]string{"app.kubernetes.io/managed-by": "trivy-operator"}
		case 1:
			labels = map[string]string{"trivy-operator.resource.kind": "Pod"}
		default:
			labels = nil // scan-* name in trivy-system
		}
		objs = append(objs, mkJob("trivy-system", "scan-vulnerabilityreport-"+strconv.Itoa(i), "", labels))
	}
	// syft-grype: the CronJob + its spawned Jobs (owner ref) + a label-only
	// Job + a bare namespace-only Job.
	objs = append(objs, mkCron("syft-grype", "syft-grype"))
	for i := 0; i < 12; i++ {
		objs = append(objs, mkJob("syft-grype", "syft-grype-run-"+strconv.Itoa(i), "syft-grype", nil))
	}
	objs = append(objs, mkJob("syft-grype", "syft-grype-orphan", "",
		map[string]string{"catalyst.openova.io/blueprint": "bp-syft-grype"}))
	objs = append(objs, mkJob("syft-grype", "stray-job-no-labels", "", nil))

	// --- the real convergence set (must each become a DAG node) ---
	objs = append(objs,
		mkJob("cnpg", "cnpg-pair-primary-join", "", nil),
		mkJob("openbao", "openbao-init-29000111", "", nil),
		mkJob("gitea", "gitea-sso-configure", "", nil),
	)

	obs, err := helmwatch.ListReconcilerObservations(context.Background(), scannerExclusionFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	bridge := jobs.NewBridge(st, depID)
	if _, _, err := bridge.SeedReconcilerObservations(reconcilerObsToBridge(obs)); err != nil {
		t.Fatalf("SeedReconcilerObservations: %v", err)
	}

	// Build the flow-diagram DAG exactly as the snapshot/stream endpoints do.
	msg, ok := h.flowSnapshotFromJobs(depID)
	if !ok || msg == nil {
		t.Fatal("flowSnapshotFromJobs returned no snapshot")
	}

	// ZERO scanner nodes. Any node whose id/label references a scanner is a leak.
	scannerNodeIDs := map[string]bool{}
	for _, n := range msg.Nodes {
		if nameLooksLikeScanner(n.ID) || nameLooksLikeScanner(n.Label) {
			t.Errorf("scanner leaked into the flow DAG as a node: id=%q label=%q status=%q",
				n.ID, n.Label, n.Status)
			scannerNodeIDs[n.ID] = true
		}
	}

	// ZERO scanner edges — neither endpoint may reference a scanner node.
	for _, r := range msg.Relationships {
		if scannerNodeIDs[r.FromID] || scannerNodeIDs[r.ToID] ||
			nameLooksLikeScanner(r.FromID) || nameLooksLikeScanner(r.ToID) {
			t.Errorf("scanner leaked into the flow DAG as an edge: %s --%s--> %s", r.FromID, r.Type, r.ToID)
		}
	}

	// The 3 real reconciler Jobs each ARE nodes (as Reconcilers-group task leaves).
	wantNodeSuffixes := []string{
		jobs.TaskJobPrefix + "cnpg-pair-primary-join",
		jobs.TaskJobPrefix + "openbao-init", // run-suffix stripped
		jobs.TaskJobPrefix + "gitea-sso-configure",
	}
	for _, suf := range wantNodeSuffixes {
		found := false
		for _, n := range msg.Nodes {
			if strings.HasSuffix(n.ID, ":"+suf) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("real reconciler node %q missing from the flow DAG — exclusion must not drop genuine reconcilers", suf)
		}
	}
}

// nameLooksLikeScanner reports whether a node id/label references a Day-2
// scanner artifact (trivy scan-*, syft-grype, or the retired collapsed
// "security-scans" summary). Used to detect any scanner leak into the DAG.
func nameLooksLikeScanner(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "scan-vulnerabilityreport") ||
		strings.Contains(l, "syft-grype") ||
		strings.Contains(l, "security-scans") ||
		strings.Contains(l, "trivy")
}
