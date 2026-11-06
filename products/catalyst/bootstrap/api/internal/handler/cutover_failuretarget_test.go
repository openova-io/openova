package handler

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// A Job marked FailureTarget is dying; an operator retry must not attach to it.
func TestJobHasFailureTarget(t *testing.T) {
	dying := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue}}}}
	if !jobHasFailureTarget(dying) || jobHasFailureTarget(&batchv1.Job{}) {
		t.Fatal("FailureTarget detection wrong")
	}
}
