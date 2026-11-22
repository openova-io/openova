package openova

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// A finished pod bills once, up to the moment it finished.
//
// A pod in phase Succeeded or Failed outlives its run: a CronJob keeps its
// last runs, a Job without a TTL keeps its pod, an evicted pod stays until it
// is collected. The collector used to stamp such a pod finished at the moment
// it SAW it. Once the tracked entry was pruned (two passes after it
// finished), the hourly informer resync observed the pod again as a new
// resource finished "now", and the emit billed it from the last collection to
// now; a restart of the service did the same at once for every finished pod.
// These tests pin that the finish instant comes from the pod object, so no
// pass, resync or restart bills a pod that is not running.

// finishedPod is testPod run to completion: phase Succeeded, its container
// terminated at finished.
func finishedPod(ns, name, uid string, created, finished time.Time, cpu, mem string) *corev1.Pod {
	p := testPod(ns, name, uid, created, cpu, mem)
	p.Status.Phase = corev1.PodSucceeded
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0, StartedAt: metav1.Time{Time: created}, FinishedAt: metav1.Time{Time: finished},
		}},
	}}
	return p
}

func podRecords(repo *fakeRepo, sourceID, uid string) []store.UsageRecord {
	var out []store.UsageRecord
	for _, r := range repo.usageRecords(sourceID) {
		if r.ResourceID == "pod/"+uid {
			out = append(out, r)
		}
	}
	return out
}

// TestFinishedPodIsNotBilledAgainAfterARestart: the service restarts at
// 12:40; the source was last collected at 12:00. A CronJob pod that finished
// three days ago is still in the namespace. The new process must write
// nothing for it — on main it wrote 40 minutes of its requests.
func TestFinishedPodIsNotBilledAgainAfterARestart(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	ctx := context.Background()
	noon := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	// The previous process: the source collected up to 12:00.
	now := noon
	before := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	before.ObserveNamespace(orgNamespace("acme"))
	if _, err := before.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	// The restarted process at 12:40 lists the namespace and the old pod.
	now = noon.Add(40 * time.Minute)
	ranFrom := noon.AddDate(0, 0, -3).Add(-time.Hour)
	ranTo := noon.AddDate(0, 0, -3)
	after := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	after.ObserveNamespace(orgNamespace("acme"))
	after.ObservePod(finishedPod("acme", "backup-28871", "job-pod-1", ranFrom, ranTo, "2", "4Gi"))
	if _, err := after.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(cust.ID)[0]
	if got := podRecords(repo, src.ID, "job-pod-1"); len(got) != 0 {
		t.Fatalf("a pod that finished three days ago was billed again after the restart: %+v", got)
	}
}

// TestFinishedPodIsNotBilledAgainOnResync: one process. The pod runs from
// 09:00 and finishes at 10:20; the event arrives at 10:25. The hourly passes
// go on, prune drops the pod, and the informer's resync at 13:05 hands the
// same finished pod to ObservePod again. The pod is billed for 09:00–10:20
// and not a minute more: 1 h 20 min × 1 vCPU = 1.333333 vCPU-hours.
func TestFinishedPodIsNotBilledAgainOnResync(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 11, 20, 10, 20, 0, 0, time.UTC)

	now := start
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))
	c.ObservePod(testPod("acme", "report-1", "pod-uid-9", start, "1", "1Gi"))
	emit := func(at time.Time) {
		t.Helper()
		now = at
		if _, err := c.EmitOrg(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
	}
	emit(start.Add(30 * time.Minute))

	now = finished.Add(5 * time.Minute)
	c.ObservePod(finishedPod("acme", "report-1", "pod-uid-9", start, finished, "1", "1Gi"))
	emit(now)
	emit(time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))
	emit(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	emit(time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)) // prunes the pod

	// The resync re-delivers the unchanged pod object.
	now = time.Date(2026, 11, 20, 13, 5, 0, 0, time.UTC)
	c.ObservePod(finishedPod("acme", "report-1", "pod-uid-9", start, finished, "1", "1Gi"))
	emit(now)
	emit(time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC))

	src := repo.sourcesOf(cust.ID)[0]
	recs := podRecords(repo, src.ID, "pod-uid-9")
	if got := billed(recs, SKUVCPU).FloatString(6); got != "1.333333" {
		t.Fatalf("vCPU-hours billed = %s, want 1.333333 (09:00–10:20); records %+v", got, sortedRecords(recs))
	}
	for _, r := range recs {
		if r.WindowEnd.After(finished) {
			t.Fatalf("billing ran past the finish at 10:20: %s %s–%s", r.SKU, r.WindowStart.Format("15:04"), r.WindowEnd.Format("15:04"))
		}
	}
}

// TestFinishedPodWithoutAFinishTime keeps the two fallbacks: a pod this
// collector tracked running stops when it is seen finished; a pod first seen
// already finished, with no finish time on the object, bills nothing.
func TestFinishedPodWithoutAFinishTime(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	now := start
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))
	c.ObservePod(testPod("acme", "seen-running", "uid-a", start, "1", "1Gi"))

	now = start.Add(30 * time.Minute)
	bare := testPod("acme", "seen-running", "uid-a", start, "1", "1Gi")
	bare.Status.Phase = corev1.PodFailed
	c.ObservePod(bare)
	never := testPod("acme", "never-seen", "uid-b", start.Add(-48*time.Hour), "1", "1Gi")
	never.Status.Phase = corev1.PodFailed
	c.ObservePod(never)
	if _, err := c.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(cust.ID)[0]
	if got := billed(podRecords(repo, src.ID, "uid-a"), SKUVCPU).FloatString(6); got != "0.500000" {
		t.Fatalf("tracked pod billed %s vCPU-hours, want 0.500000 (09:00 to the 09:30 it was seen finished)", got)
	}
	if got := podRecords(repo, src.ID, "uid-b"); len(got) != 0 {
		t.Fatalf("a pod first seen finished with no finish time was billed: %+v", got)
	}
}
