// cutover_snapshot_steps_test.go — #5437 guards.
//
// The defect these lock down (live hw290, 2026-07-27): a cutover re-attempt
// separated in time SKIPPED step-01 gitea-mirror on its recorded success,
// re-ran step-03 harbor-prewarm fresh, then pointed Flux at the 14-hour-stale
// mirror. The mirror pinned `catalyst-api:b1b472d`, the fresh prewarm had
// pushed only what the cluster was running, and the local registry answered
// `NotFound` — control plane down (0/1 ImagePullBackOff, /healthz 503).
//
// Every test below FAILS against the pre-fix engine (skip-on-success applied
// uniformly). Negative proof is recorded on PR #5437's body.
package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// ── Fixtures ────────────────────────────────────────────────────────────────

// cutoverJobCreateCounter records how many Jobs the engine mints per step
// slug (read off the cutover.openova.io/step label createCutoverJob stamps).
// A step that was SKIPPED mints zero.
func cutoverJobCreateCounter(t *testing.T, client *fakek8s.Clientset) func(string) int {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		job, ok := ca.GetObject().(*batchv1.Job)
		if !ok {
			return false, nil, nil
		}
		mu.Lock()
		counts[job.Labels[cutoverStepLabelKey]]++
		mu.Unlock()
		return false, nil, nil // let the default tracker create it
	})
	return func(step string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[step]
	}
}

// hw290Chain is the shipped chain trimmed to the steps this defect involves:
// the snapshot (01), a genuinely idempotent action (02), the artifact push
// that consumes the snapshot (03), and the pivot boundary (05).
func hw290Chain(t *testing.T) []cutoverStep {
	t.Helper()
	return []cutoverStep{
		{stepName: cutoverStepGiteaMirror, order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: "harbor-projects", order: 2, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: cutoverStepHarborPrewarm, order: 3, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: cutoverStepFluxGitRepoPatch, order: 5, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}
}

// statusCMWith builds the durable status ConfigMap from arbitrary rows.
func statusCMWith(rows map[string]string) *corev1.ConfigMap {
	data := map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v2"}
	for k, v := range rows {
		data[k] = v
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       data,
	}
}

// completedJobForStep fabricates the leftover Complete Job a prior attempt
// left behind, carrying the step label the idempotency check queries by.
func completedJobForStep(stepName string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cutover-" + stepName + "-1753542122",
			Namespace:         cutoverTestNS,
			Labels:            map[string]string{cutoverStepLabelKey: stepName},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-14 * time.Hour)),
		},
		Status: batchv1.JobStatus{
			CompletionTime: &metav1.Time{Time: time.Now().Add(-14 * time.Hour)},
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

// ── Engine behaviour ────────────────────────────────────────────────────────

// TestRunCutover_ReRunsMirrorWhenSnapshotPredatesAttempt is the headline guard:
// the exact hw290 durable state (step-01 + step-02 recorded success 14 hours
// ago, everything downstream unrun) must re-take the mirror snapshot rather
// than skip it, and must invalidate the consumer steps that were recorded
// against the OLD snapshot.
func TestRunCutover_ReRunsMirrorWhenSnapshotPredatesAttempt(t *testing.T) {
	yesterday := time.Now().Add(-14 * time.Hour).UTC().Format(time.RFC3339)
	objs := []k8sruntime.Object{statusCMWith(map[string]string{
		"cutoverStartedAt":                               yesterday,
		"step." + cutoverStepGiteaMirror + ".result":     "success",
		"step." + cutoverStepGiteaMirror + ".finishedAt": yesterday,
		"step.harbor-projects.result":                    "success",
		"step.harbor-projects.finishedAt":                yesterday,
	})}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t), false)

	if got := created(cutoverStepGiteaMirror); got != 1 {
		t.Errorf("gitea-mirror Jobs created = %d, want 1 — a snapshot of a moving upstream recorded 14h ago must be RE-TAKEN, not skipped (#5437)", got)
	}
	// The cascade: harbor-projects was ALSO recorded against the old snapshot,
	// so re-taking the snapshot must invalidate it. Without this the fix would
	// merely invert the drift (fresh mirror, stale consumers).
	if got := created("harbor-projects"); got != 1 {
		t.Errorf("harbor-projects Jobs created = %d, want 1 — re-taking the snapshot must invalidate the steps recorded against the old one (#5437)", got)
	}

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status CM: %v", err)
	}
	// cutoverStartedAt is the first-attempt anchor and must survive (#3681).
	if cm.Data["cutoverStartedAt"] != yesterday {
		t.Errorf("cutoverStartedAt = %q, want the preserved first-attempt anchor %q", cm.Data["cutoverStartedAt"], yesterday)
	}
	if cm.Data["step."+cutoverStepGiteaMirror+".finishedAt"] == yesterday {
		t.Errorf("step.gitea-mirror.finishedAt is still the stale %q — the durable row must carry THIS attempt's snapshot", yesterday)
	}
}

// TestRunCutover_KeepsMirrorSuccessTakenWithinThisAttempt proves the fix does
// not throw away idempotency wholesale: a snapshot recorded at/after the
// attempt start is still honoured (this is the mid-run resume path — a
// catalyst-api roll must not re-mirror work it just did).
func TestRunCutover_KeepsMirrorSuccessTakenWithinThisAttempt(t *testing.T) {
	// A timestamp the attempt-start comparison can only read as "taken during
	// this attempt" (runCutover stamps attemptStart at time.Now()).
	inAttempt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	objs := []k8sruntime.Object{statusCMWith(map[string]string{
		"step." + cutoverStepGiteaMirror + ".result":     "success",
		"step." + cutoverStepGiteaMirror + ".finishedAt": inAttempt,
	})}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t), false)

	if got := created(cutoverStepGiteaMirror); got != 0 {
		t.Errorf("gitea-mirror Jobs created = %d, want 0 — a snapshot taken within this attempt is still valid and must be skipped", got)
	}
	if got := created(cutoverStepFluxGitRepoPatch); got != 1 {
		t.Errorf("flux-gitrepository-patch Jobs created = %d, want 1 — a fresh snapshot must let the pivot proceed", got)
	}
}

// TestRunCutover_SuppressesMirrorRerunPastPivotBoundary proves the fix does not
// re-create the hw86 incident: once flux-gitrepository-patch has succeeded the
// mirrored repo is the live GitOps source and steps 06/07/10/11 commit
// sovereign-local changes onto its main branch. step-01's
// `git push --force refs/heads/*` would revert them, so the stale snapshot is
// KEPT there.
func TestRunCutover_SuppressesMirrorRerunPastPivotBoundary(t *testing.T) {
	yesterday := time.Now().Add(-14 * time.Hour).UTC().Format(time.RFC3339)
	objs := []k8sruntime.Object{statusCMWith(map[string]string{
		"step." + cutoverStepGiteaMirror + ".result":          "success",
		"step." + cutoverStepGiteaMirror + ".finishedAt":      yesterday,
		"step.harbor-projects.result":                         "success",
		"step." + cutoverStepHarborPrewarm + ".result":        "success",
		"step." + cutoverStepFluxGitRepoPatch + ".result":     "success",
		"step." + cutoverStepFluxGitRepoPatch + ".finishedAt": yesterday,
	})}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t), false)

	if got := created(cutoverStepGiteaMirror); got != 0 {
		t.Errorf("gitea-mirror Jobs created = %d, want 0 — re-mirroring past the pivot boundary force-pushes upstream over the sovereign-local pivot commits (hw86)", got)
	}
}

// TestRunCutover_StaleSnapshotDoesNotAdoptPriorCompleteJob closes the SECOND
// skip path. runCutoverStep's idempotency check adopts any Complete Job
// carrying the step label and returns success without re-running — which would
// silently undo the re-run decision made one layer up.
func TestRunCutover_StaleSnapshotDoesNotAdoptPriorCompleteJob(t *testing.T) {
	yesterday := time.Now().Add(-14 * time.Hour).UTC().Format(time.RFC3339)
	objs := []k8sruntime.Object{
		statusCMWith(map[string]string{
			"step." + cutoverStepGiteaMirror + ".result":     "success",
			"step." + cutoverStepGiteaMirror + ".finishedAt": yesterday,
		}),
		completedJobForStep(cutoverStepGiteaMirror),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t), false)

	if got := created(cutoverStepGiteaMirror); got != 1 {
		t.Errorf("gitea-mirror Jobs created = %d, want 1 — yesterday's Complete Job must NOT be re-adopted for a snapshot step (#5437)", got)
	}
}

// ── Pre-pivot invariant (defence in depth) ──────────────────────────────────

func TestAssertMirrorSnapshotFresh(t *testing.T) {
	attemptStart := time.Date(2026, 7, 27, 4, 56, 51, 0, time.UTC)
	chain := hw290Chain(t)

	t.Run("stale snapshot refuses the pivot", func(t *testing.T) {
		err := assertMirrorSnapshotFresh(chain, map[string]string{
			"step." + cutoverStepGiteaMirror + ".finishedAt": "2026-07-26T15:02:02Z",
		}, attemptStart)
		if err == nil {
			t.Fatal("want an error: pivoting Flux onto a snapshot older than this attempt is the hw290 outage")
		}
	})

	t.Run("unrecorded snapshot fails closed", func(t *testing.T) {
		if err := assertMirrorSnapshotFresh(chain, map[string]string{}, attemptStart); err == nil {
			t.Fatal("want an error: an unprovable snapshot age must not be treated as fresh")
		}
	})

	t.Run("snapshot from this attempt passes", func(t *testing.T) {
		err := assertMirrorSnapshotFresh(chain, map[string]string{
			"step." + cutoverStepGiteaMirror + ".finishedAt": "2026-07-27T05:01:15Z",
		}, attemptStart)
		if err != nil {
			t.Fatalf("want nil for a snapshot taken during this attempt; got %v", err)
		}
	})

	t.Run("chain without a mirror step has nothing to assert", func(t *testing.T) {
		partial := []cutoverStep{{stepName: cutoverStepFluxGitRepoPatch, order: 5, mode: cutoverModeJob}}
		if err := assertMirrorSnapshotFresh(partial, map[string]string{}, attemptStart); err != nil {
			t.Fatalf("want nil when the chain carries no gitea-mirror step; got %v", err)
		}
	})
}

// ── Predicate unit tests ────────────────────────────────────────────────────

func TestDecideSnapshotRerun(t *testing.T) {
	attemptStart := time.Date(2026, 7, 27, 4, 56, 51, 0, time.UTC)
	chain := hw290Chain(t)
	mirror := chain[0]

	t.Run("hw290 shape re-runs", func(t *testing.T) {
		d := decideSnapshotRerun(mirror, chain, map[string]string{
			"step." + cutoverStepGiteaMirror + ".result":     "success",
			"step." + cutoverStepGiteaMirror + ".finishedAt": "2026-07-26T15:02:02Z",
		}, attemptStart, nil)
		if !d.rerun {
			t.Fatalf("want rerun for a success recorded 14h before the attempt; got %+v", d)
		}
	})

	t.Run("no recorded success is not a re-run decision", func(t *testing.T) {
		if d := decideSnapshotRerun(mirror, chain, map[string]string{}, attemptStart, nil); d.rerun {
			t.Errorf("a step with no recorded success runs normally; want rerun=false, got %+v", d)
		}
	})

	t.Run("non-snapshot step is never re-run", func(t *testing.T) {
		prewarm := chain[2]
		d := decideSnapshotRerun(prewarm, chain, map[string]string{
			"step." + cutoverStepHarborPrewarm + ".result":     "success",
			"step." + cutoverStepHarborPrewarm + ".finishedAt": "2026-07-26T15:40:00Z",
		}, attemptStart, nil)
		if d.rerun {
			t.Errorf("harbor-prewarm is invalidated by the cascade, never by its own time predicate; got %+v", d)
		}
	})

	t.Run("past the pivot boundary the re-run is suppressed", func(t *testing.T) {
		d := decideSnapshotRerun(mirror, chain, map[string]string{
			"step." + cutoverStepGiteaMirror + ".result":      "success",
			"step." + cutoverStepGiteaMirror + ".finishedAt":  "2026-07-26T15:02:02Z",
			"step." + cutoverStepFluxGitRepoPatch + ".result": "success",
		}, attemptStart, nil)
		if d.rerun || !d.suppressed {
			t.Errorf("want suppressed (hw86 clobber guard), got %+v", d)
		}
	})
}

func TestSnapshotRerunSuppressedBy(t *testing.T) {
	chain := hw290Chain(t)

	t.Run("pre-pivot successes do not suppress", func(t *testing.T) {
		got := snapshotRerunSuppressedBy(chain, map[string]string{
			"step.harbor-projects.result":                  "success",
			"step." + cutoverStepHarborPrewarm + ".result": "success",
		}, 1, nil)
		if got != "" {
			t.Errorf("suppressed by %q; steps before the pivot boundary are re-runnable", got)
		}
	})

	t.Run("a surviving Job past the boundary suppresses even with a blanked row", func(t *testing.T) {
		// ResumeInterruptedCutover blanks the result row of a step that was in
		// flight when catalyst-api died, so the durable record alone cannot see
		// that the chain reached step-08. The surviving Job can — and must, or
		// the cascade re-runs the 10-minute deny-egress hold (t40 / TBD-V56).
		withEgress := append(hw290Chain(t),
			cutoverStep{stepName: cutoverStepEgressBlockTest, order: 8, mode: cutoverModeJob})
		got := snapshotRerunSuppressedBy(withEgress, map[string]string{
			"step." + cutoverStepEgressBlockTest + ".result": "",
		}, 1, func(name string) bool { return name == cutoverStepEgressBlockTest })
		if got != cutoverStepEgressBlockTest {
			t.Errorf("suppressor = %q, want %q — a surviving Job is the only surviving evidence the chain got that far", got, cutoverStepEgressBlockTest)
		}
	})

	t.Run("chain with no pivot boundary falls back to conservative", func(t *testing.T) {
		partial := []cutoverStep{
			{stepName: cutoverStepGiteaMirror, order: 1},
			{stepName: "helmrepository-patches", order: 6},
		}
		got := snapshotRerunSuppressedBy(partial, map[string]string{
			"step.helmrepository-patches.result": "success",
		}, 1, nil)
		if got != "helmrepository-patches" {
			t.Errorf("suppressor = %q, want helmrepository-patches — with no boundary step the engine cannot prove which later steps push to the mirror", got)
		}
	})
}

func TestSnapshotRerunCascade(t *testing.T) {
	chain := hw290Chain(t)
	prior := map[string]string{
		"step." + cutoverStepGiteaMirror + ".result":     "success",
		"step." + cutoverStepGiteaMirror + ".finishedAt": "2026-07-26T15:02:02Z",
		"step.harbor-projects.result":                    "success",
		"cutoverStartedAt":                               "2026-07-26T14:56:49Z",
	}
	clear := snapshotRerunCascade(chain, prior, 0)

	for _, key := range []string{
		"step." + cutoverStepGiteaMirror + ".result",
		"step." + cutoverStepGiteaMirror + ".finishedAt",
		"step.harbor-projects.result",
	} {
		if v, ok := clear[key]; !ok || v != "" {
			t.Errorf("cascade must blank %q in the durable patch; got (%q, present=%v)", key, v, ok)
		}
		if _, still := prior[key]; still {
			t.Errorf("cascade must also drop %q from the in-memory prior status so the same run's skip check agrees", key)
		}
	}
	if _, ok := clear["cutoverStartedAt"]; ok {
		t.Error("cascade must not touch the first-attempt anchor cutoverStartedAt (#3681)")
	}

	// The cascade must stop AT the pivot boundary — reaching past it would
	// blank step-08's row and repeat the 10-minute deny-egress hold.
	withEgress := append(hw290Chain(t),
		cutoverStep{stepName: cutoverStepEgressBlockTest, order: 8, mode: cutoverModeJob})
	prior2 := map[string]string{
		"step." + cutoverStepGiteaMirror + ".result":      "success",
		"step." + cutoverStepHarborPrewarm + ".result":    "success",
		"step." + cutoverStepFluxGitRepoPatch + ".result": "success",
		"step." + cutoverStepEgressBlockTest + ".result":  "success",
	}
	clear2 := snapshotRerunCascade(withEgress, prior2, 0)
	for _, key := range []string{
		"step." + cutoverStepFluxGitRepoPatch + ".result",
		"step." + cutoverStepEgressBlockTest + ".result",
	} {
		if _, ok := clear2[key]; ok {
			t.Errorf("cascade blanked %q — it must stop at the pivot boundary (t40 / TBD-V56)", key)
		}
	}
	if _, ok := clear2["step."+cutoverStepHarborPrewarm+".result"]; !ok {
		t.Error("cascade must still invalidate the pre-pivot consumer harbor-prewarm")
	}
}
