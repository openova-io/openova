/*
jobs_retry_syft_identity_row176_test.go — UAT row 176, the 422 half.

THE DEFECT. The Jobs view collapses every scanner run onto ONE stable identity
leaf (#3925): all trivy-operator scan Jobs fold onto `task-trivy-security-scan`
and all bp-syft-grype runs fold onto `task-syft-sbom`. Those identities are
SYNTHETIC — no Kubernetes object is named `syft-sbom`. So the retry dispatch
(jobs_retry.go, case jobs.KindTask) asked resolveObjectNamespace for a Job by
that name, found nothing, and returned errNotDirectlyRetryable → HTTP 422.

#5496 (8367b627a) added a `<row-name>-<digits>` fallback for
controller-generated Jobs. It cannot reach this row: the real object is
`syft-grype-bp-syft-grype-29773110`, and `syft-sbom` is not a prefix of it, so
the suffix rule never matches. That fix is deployed and the row still 422s.

THE DISTINCTION THIS FILE PINS — and why the two scanners must NOT be treated
alike:

  - syft-grype HAS a single real backing object: the CronJob `syft-grype` in
    namespace `syft-grype` (helmwatch/reconcilers.go declares both, and the
    collapsed row is seeded FROM that CronJob). "Run now" on that CronJob is a
    truthful re-run, and it is the same mechanism the CronJob-owned task branch
    and case jobs.KindCron already use.
  - trivy-security-scan has NO single backing object. The trivy-operator spawns
    one scan Job per workload; there is nothing to re-run that means "redo the
    scan". Its 422 is CORRECT and must stay.

Hence the control below. A fix that made every collapsed identity row "work" —
by inventing a target, or by relaxing the aggregate check — would turn the
first test green and the control red. Only a fix that re-runs a real object
that actually exists passes both.
*/
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// syftGrypeCronJob — the bp-syft-grype CronJob exactly as slot-33 installs it
// (release name `syft-grype`, targetNamespace `syft-grype`). This is the
// object the collapsed `task-syft-sbom` row is seeded from.
func syftGrypeCronJob() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": "syft-grype", "namespace": "syft-grype"},
		"spec": map[string]any{
			"schedule": "0 3 * * *",
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"restartPolicy": "Never",
							"containers": []any{map[string]any{
								"name": "syft", "image": "anchore/syft:latest",
							}},
						},
					},
				},
			},
		},
	}}
}

// THE ROW-176 GUARD. Clicking re-run on the collapsed Syft SBOM row must
// actually re-run it — 200, via the owning CronJob.
func TestRetryJob_Task_SyftIdentity_RunsViaOwningCronJob_Row176(t *testing.T) {
	r, h, depID := installRetryHarness(t, "owner@t99.omani.works",
		"task-syft-sbom", jobs.StatusFailed, syftGrypeCronJob())

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "task-syft-sbom",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (re-run via the owning CronJob), got %d; body=%s — "+
			"the operator clicks re-run on the Syft SBOM row and nothing happens (UAT row 176)",
			rec.Code, rec.Body.String())
	}

	// The 200 must correspond to a REAL new run, not a bare acknowledgement.
	// Asserting only the status code would pass on a handler that returned
	// 200 and did nothing at all — the exact fake-green this row is about.
	dyn, _ := h.dynamicFactory("")
	list, err := dyn.Resource(helmwatch.JobGVR).Namespace("syft-grype").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	created := ""
	for _, it := range list.Items {
		if v, _, _ := unstructured.NestedString(it.Object, "metadata", "labels",
			"catalyst.openova.io/from-cronjob"); v == "syft-grype" {
			created = it.GetName()
		}
	}
	if created == "" {
		t.Fatalf("no one-off Job was created from CronJob syft-grype; a 200 without a "+
			"new run is a fake green. Jobs present: %d", len(list.Items))
	}
}

// THE CONTROL. trivy-security-scan aggregates per-workload scan Jobs and has
// no single object to re-run, so it must STILL 422. This is what stops the fix
// above from being "make aggregate rows return 200".
func TestRetryJob_Task_TrivyIdentity_Stays422_Row176(t *testing.T) {
	// Seed the syft CronJob too: it exists on a real Sovereign, and its
	// presence must not make the UNRELATED trivy row suddenly retryable.
	r, _, depID := installRetryHarness(t, "owner@t99.omani.works",
		"task-trivy-security-scan", jobs.StatusFailed, syftGrypeCronJob())

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

// The syft row must not become retryable by NAME alone — if the backing
// CronJob is genuinely absent (bp-syft-grype not installed), the honest answer
// is still 422. Otherwise the fix would report a successful re-run of an
// object that does not exist, which is the same class of defect as the
// original silent failure.
func TestRetryJob_Task_SyftIdentity_NoCronJob_Stays422_Row176(t *testing.T) {
	r, _, depID := installRetryHarness(t, "owner@t99.omani.works",
		"task-syft-sbom", jobs.StatusFailed) // no CronJob seeded

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "task-syft-sbom",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("absent backing CronJob must stay 422, got %d; body=%s",
			rec.Code, rec.Body.String())
	}
}
