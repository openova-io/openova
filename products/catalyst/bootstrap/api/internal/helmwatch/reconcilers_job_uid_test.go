package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestReconcilerObservationsCarryTheJobUID pins that every run read from a
// Job carries the Job's metadata.uid, the key the Job watch closes the run
// by: a CronJob run, a standalone task run and an install-hook run.
func TestReconcilerObservationsCarryTheJobUID(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cronRun := makeBatchJob("openbao", "snapshot-29000000", "snapshot", "", "", now)
	cronRun.SetUID(types.UID("uid-cron-run"))
	task := makeBatchJob("org-a", "wp-admin-user", "", "Complete", metav1.ConditionTrue, now)
	task.SetUID(types.UID("uid-task"))
	hook := makeBatchJob("openbao", "bp-openbao-init", "", "", "", now)
	hook.SetUID(types.UID("uid-hook"))
	hook.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "bp-openbao", "helm.sh/hook": "post-install"})

	obs, err := ListReconcilerObservations(context.Background(),
		reconcilerFakeClient(makeCronJob("openbao", "snapshot"), cronRun, task, hook))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range obs {
		switch {
		case o.Kind == ReconcilerKindCron && o.Name == "snapshot" && len(o.Executions) == 1:
			got["cron"] = o.Executions[0].UID
		case o.Kind == ReconcilerKindTask && o.OwnerInstallChart == "openbao":
			got["hook"] = o.JobUID
		case o.Kind == ReconcilerKindTask && o.Name == "wp-admin-user" && len(o.Executions) == 1:
			got["task"] = o.Executions[0].UID
		}
	}
	want := map[string]string{"cron": "uid-cron-run", "task": "uid-task", "hook": "uid-hook"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s run uid %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
