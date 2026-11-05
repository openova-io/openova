package handler

// Epic #6703 — "Run now" on a CronJob must materialise the Job the way
// `kubectl create job --from=cronjob/<name>` does.
//
// Measured on hw307 (2026-11-05): every bootstrap-kit CronJob's jobTemplate
// carries `app.kubernetes.io/managed-by: flux` (that is how the CronJob
// controller's children pass the Kyverno `flux-managed` ClusterPolicy, which
// is Enforce and matches Job). The old one-off Job carried ONLY the two
// catalyst.openova.io labels, so the admission webhook denied it in any
// namespace outside the policy's allow-list — "Run now" on a Blueprint cron
// could not work. It also carried no ownerReference, so the run never showed
// in the CronJob's run history (childRunsOfCron keys on ownerReference
// kind=CronJob) and was never GC'd with its CronJob.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

func TestRetryJob_Cron_RunNow_MirrorsKubectlCreateJobFromCronJob_6703(t *testing.T) {
	cj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata": map[string]any{
			"name":      "openbao-snapshot-save",
			"namespace": "openbao",
			"uid":       "cj-uid-6703",
			// Labels on the CronJob itself are NOT what the controller copies
			// onto a child Job — the template's are. Put a decoy here so the
			// test can tell the two apart.
			"labels": map[string]any{"helm.toolkit.fluxcd.io/name": "bp-openbao"},
		},
		"spec": map[string]any{
			"schedule": "*/10 * * * *",
			"jobTemplate": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{
						"app.kubernetes.io/managed-by":  "flux",
						"catalyst.openova.io/blueprint": "bp-openbao",
					},
					"annotations": map[string]any{
						"catalyst.openova.io/template-note": "kept",
					},
				},
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"restartPolicy": "Never",
							"containers":    []any{map[string]any{"name": "snap", "image": "busybox"}},
						},
					},
				},
			},
		},
	}}
	r, h, depID := installRetryHarness(t, "owner@t99.omani.works", "cron-openbao-snapshot-save", jobs.StatusFailed, cj)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "cron-openbao-snapshot-save",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	dyn, _ := h.dynamicFactory("")
	list, err := dyn.Resource(helmwatch.JobGVR).Namespace("openbao").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("want exactly one one-off Job, got %d", len(list.Items))
	}
	job := list.Items[0]

	labels := job.GetLabels()
	// The template labels — the ones that admit the Job past Kyverno.
	if labels["app.kubernetes.io/managed-by"] != "flux" {
		t.Fatalf("one-off Job must carry the jobTemplate label app.kubernetes.io/managed-by=flux (Kyverno flux-managed admits on it); labels=%v", labels)
	}
	if labels["catalyst.openova.io/blueprint"] != "bp-openbao" {
		t.Fatalf("template label catalyst.openova.io/blueprint not carried; labels=%v", labels)
	}
	// The CronJob's own metadata labels are NOT what a child carries.
	if _, leaked := labels["helm.toolkit.fluxcd.io/name"]; leaked {
		t.Fatalf("CronJob metadata labels must not leak onto the child (only jobTemplate.metadata.labels do); labels=%v", labels)
	}
	// Our own markers stay.
	if labels["catalyst.openova.io/manual-run"] != "true" || labels["catalyst.openova.io/from-cronjob"] != "openbao-snapshot-save" {
		t.Fatalf("catalyst manual-run markers missing; labels=%v", labels)
	}

	ann := job.GetAnnotations()
	if ann[manualRunInstantiateAnnotation] != "manual" {
		t.Fatalf("one-off Job must carry %s=manual (the kubectl marker); annotations=%v", manualRunInstantiateAnnotation, ann)
	}
	if ann["catalyst.openova.io/template-note"] != "kept" {
		t.Fatalf("template annotations must be carried; annotations=%v", ann)
	}

	refs := job.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "CronJob" || refs[0].Name != "openbao-snapshot-save" || string(refs[0].UID) != "cj-uid-6703" {
		t.Fatalf("one-off Job must own-reference its CronJob (non-controller, like kubectl) so it shows in run history and is GC'd with it; got %+v", refs)
	}
	if refs[0].Controller != nil && *refs[0].Controller {
		t.Fatalf("ownerReference must NOT claim controller=true — the CronJob controller did not create this run")
	}
}
