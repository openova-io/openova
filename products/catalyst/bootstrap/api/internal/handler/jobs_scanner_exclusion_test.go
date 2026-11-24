// jobs_scanner_exclusion_test.go — end-to-end proof of the Day-2 scanner
// COLLAPSE (#3925, jobs-convergence-monitor-model.md §4 Surface B). It drives
// the FULL ingestion pipeline a live /jobs read uses —
//
//	helmwatch.ListReconcilerObservations  (the cluster-wide GVR list)
//	  → reconcilerObsToBridge             (wire-shape translation)
//	  → jobs.Bridge.SeedReconcilerObservations (project into jobs.Store)
//	  → Handler.flowSnapshotFromJobs      (build the flow-diagram DAG)
//
// and asserts that a 100s-strong trivy-operator scan flood collapses to
// EXACTLY ONE identity-keyed LEAF in jobs.Store — never a per-run row, never
// excluded — and therefore exactly one scanner NODE (no per-run nodes) in the
// DAG, while every genuine reconciler Job survives as its own node and a
// CronJob's runs fold onto that CronJob's own cron leaf. Trivy is the one
// matcher the platform runs (ADR-0031 I13), so it is the one scanner that
// collapses. The flow diagram is derived strictly from the leaves in
// jobs.Store, so collapsing the scanner at ingestion is what keeps the graph
// to one node for it — this test pins that the collapse holds all the way
// through the graph builder, not just at the observation layer.
package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
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

// TestFlowDiagram_CollapsesDay2Scanners is the DAG-level guard: a trivy scan
// flood collapses to EXACTLY ONE node (no per-run nodes), a CronJob's runs
// fold onto its own cron node, and real reconciler Jobs each become their own
// node.
func TestFlowDiagram_CollapsesDay2Scanners(t *testing.T) {
	const depID = "dep-scan-collapse"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), st)

	objs := []runtime.Object{}

	// --- the scanner flood (must collapse to ONE node) ---
	// trivy-operator scan Jobs across all three detection paths.
	const trivyRuns = 120
	for i := 0; i < trivyRuns; i++ {
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

	// --- a recurring CronJob: its runs fold onto ITS cron leaf, no scanner rule ---
	const cronRuns = 12
	objs = append(objs, mkCron("openbao", "openbao-snapshot-save"))
	for i := 0; i < cronRuns; i++ {
		objs = append(objs, mkJob("openbao", "openbao-snapshot-save-2900"+strconv.Itoa(i), "openbao-snapshot-save", nil))
	}

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

	// EXACTLY ONE collapsed trivy node, ONE cron node — and ZERO per-run nodes.
	trivyNodeID := depID + ":" + jobs.TaskJobPrefix + "trivy-security-scan"
	cronNodeID := depID + ":" + jobs.CronJobPrefix + "openbao-snapshot-save"
	trivyNodes, cronNodes, perRunNodes := 0, 0, 0
	for _, n := range msg.Nodes {
		switch n.ID {
		case trivyNodeID:
			trivyNodes++
		case cronNodeID:
			cronNodes++
		default:
			if perRunLooksLikeScanner(n.ID) || perRunLooksLikeScanner(n.Label) {
				perRunNodes++
				t.Errorf("a per-run node leaked into the DAG: id=%q label=%q — runs must fold onto their leaf", n.ID, n.Label)
			}
		}
	}
	if trivyNodes != 1 {
		t.Errorf("expected exactly 1 collapsed trivy node (%s); got %d (flood was %d runs)", trivyNodeID, trivyNodes, trivyRuns)
	}
	if cronNodes != 1 {
		t.Errorf("expected exactly 1 cron node (%s); got %d", cronNodeID, cronNodes)
	}
	if perRunNodes != 0 {
		t.Errorf("%d per-run nodes leaked — runs must fold onto one node each", perRunNodes)
	}

	// The collapsed scanner leaf and the cron leaf each carry their
	// run-history (run-count = Executions) in jobs.Store, proving the runs
	// are NOT lost.
	if _, execs, gerr := st.GetJob(depID, jobs.TaskJobPrefix+"trivy-security-scan"); gerr != nil {
		t.Errorf("collapsed trivy leaf missing from store: %v", gerr)
	} else if len(execs) != trivyRuns {
		t.Errorf("collapsed trivy leaf should carry %d runs as run-history; got %d Executions", trivyRuns, len(execs))
	}
	if _, execs, gerr := st.GetJob(depID, jobs.CronJobPrefix+"openbao-snapshot-save"); gerr != nil {
		t.Errorf("cron leaf missing from store: %v", gerr)
	} else if len(execs) != cronRuns {
		t.Errorf("cron leaf should carry %d runs as run-history; got %d Executions", cronRuns, len(execs))
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
			t.Errorf("real reconciler node %q missing from the flow DAG — collapse must not drop genuine reconcilers", suf)
		}
	}
}

// perRunLooksLikeScanner reports whether a node id/label references a PER-RUN
// artifact (an individual trivy scan-* Job, a single openbao-snapshot-save-<n>
// run, or the retired "security-scans" summary). The collapsed identity node
// (trivy-security-scan) and the cron node are expected and are NOT flagged.
func perRunLooksLikeScanner(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "scan-vulnerabilityreport") ||
		strings.Contains(l, "openbao-snapshot-save-2900") ||
		strings.Contains(l, "security-scans")
}

// UAT row 176, the control half. trivy-security-scan aggregates one scan Job
// per workload and has no single object to re-run, so a retry on it must
// answer 422 with the actionable code. A real scan Job is seeded so the
// lookup has something to find: its presence must not make the aggregate row
// retryable.
func TestRetryJob_Task_TrivyIdentity_Stays422_Row176(t *testing.T) {
	scan := mkJob("trivy-system", "scan-vulnerabilityreport-1", "",
		map[string]string{"app.kubernetes.io/managed-by": "trivy-operator"})
	r, _, depID := installRetryHarness(t, "owner@t99.omani.works",
		"task-trivy-security-scan", jobs.StatusFailed, scan)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "task-trivy-security-scan",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("trivy aggregate must stay 422 (nothing to re-run), got %d; body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not-directly-retryable") {
		t.Fatalf("422 body must keep the actionable code: %s", rec.Body.String())
	}
}
