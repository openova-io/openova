// Tests for the internal cutover trigger endpoint (issue #935 Bug 2).
//
// Coverage gates:
//
//  1. HandleCutoverInternalTrigger — happy path with a valid SA token
//     for the canonical cutover-runner SA runs the engine to
//     completion and persists cutoverComplete=true.
//  2. HandleCutoverInternalTrigger — missing Authorization header →
//     401 missing-bearer.
//  3. HandleCutoverInternalTrigger — token-review rejects the token
//     (Authenticated=false) → 502 token-review-failed.
//  4. HandleCutoverInternalTrigger — token-review accepts the token
//     but the resolved username is NOT the cutover-runner SA →
//     403 unauthorized-sa.
//  5. HandleCutoverInternalTrigger — idempotent when the durable
//     status ConfigMap reports cutoverComplete=true (no Jobs created).
//  6. HandleCutoverInternalTrigger — wrong HTTP method → 405.
//
// All tests inject a fake clientset and prepend a reactor on the
// `tokenreviews` resource so the apiserver round-trip is mocked.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// withHandoverArchive points the Handler's OpenBao client at a fake
// server that serves the tofu-phase0-archive secret — i.e. simulates a
// Sovereign that HAS been handed over, so the cutover handover-gate
// (sovereignHandoverComplete) opens. Torn down at test end.
func withHandoverArchive(t *testing.T, h *Handler) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// KV-v2 read envelope: {"data":{"data":{...}}}.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"archive":"dGVzdA==","sovereignFQDN":"hw-test.example.com"}}}`))
	}))
	t.Cleanup(srv.Close)
	h.openbao = &openbao.Client{Addr: srv.URL, Token: "test-token", HTTP: srv.Client()}
}

// installTokenReviewReactor wires a reactor that approves any
// TokenReview create call by returning Authenticated=true with the
// supplied username. If username is empty the reactor rejects the
// review (Authenticated=false) — used for the rejection-path test.
func installTokenReviewReactor(t *testing.T, client *fakek8s.Clientset, username string) {
	t.Helper()
	client.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		tr, ok := ca.GetObject().(*authnv1.TokenReview)
		if !ok {
			return false, nil, nil
		}
		out := tr.DeepCopy()
		if username == "" {
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: false,
				Error:         "test reactor: token rejected",
			}
		} else {
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: true,
				User:          authnv1.UserInfo{Username: username},
			}
		}
		return true, out, nil
	})
}

func TestHandleCutoverInternalTrigger_HappyPath(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	// The default expected username for ns="catalyst" is
	// system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner
	// — matches what TokenReview returns from the chart's mounted
	// SA token in production.
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	// A handed-over Sovereign: the tofu-phase0-archive is sealed, so the
	// handover-completion gate opens and the auto-trigger runs the engine.
	withHandoverArchive(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Wait for the engine goroutine to flip the running flag back to
	// false — same shape as TestHandleCutoverStart_RunsAllStepsAndPersistsCompleted.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(
		context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true (data=%v)", cm.Data["cutoverComplete"], cm.Data)
	}
}

func TestHandleCutoverInternalTrigger_MissingBearerReturns401(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	// No Authorization header.
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCutoverInternalTrigger_TokenReviewRejectsReturns502(t *testing.T) {
	h, client := fakeHandlerWithCutover(t)
	installTokenReviewReactor(t, client, "") // empty username = reject

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer expired-token")
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCutoverInternalTrigger_WrongSAReturns403(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	// A token from some other namespace's default SA — not the
	// cutover-runner. Should be refused even though authenticated.
	installTokenReviewReactor(t, client, "system:serviceaccount:default:default")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer wrong-sa-token")
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCutoverInternalTrigger_IdempotentWhenComplete(t *testing.T) {
	preComplete := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":          "true",
			"cutoverFinishedAt":        "2026-05-04T10:00:00Z",
			"step.gitea-mirror.result": "success",
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		preComplete,
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent); body=%s", rec.Code, rec.Body.String())
	}
	if jobCreates != 0 {
		t.Errorf("created %d Jobs on idempotent /trigger call, want 0", jobCreates)
	}

	var resp cutoverStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.CutoverComplete {
		t.Errorf("response.cutoverComplete = false, want true")
	}
}

func TestHandleCutoverInternalTrigger_WrongMethodReturns405(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/cutover/trigger", nil)
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow header = %q, want POST", got)
	}
}

// TestHandleCutoverInternalTrigger_DefersWhenNotHandedOver is the core
// fresh-prov regression guard: a valid auto-trigger on a Sovereign that
// has NOT been handed over (no tofu-phase0-archive in OpenBao) must
// REFUSE to start the engine — 425 Too Early, zero Jobs created. Without
// this gate the bootstrap auto-trigger half-pivots the registry and
// wedges bootstrap-kit (hw93 2026-06-04).
func TestHandleCutoverInternalTrigger_DefersWhenNotHandedOver(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	// h.openbao is nil → sovereignHandoverComplete returns false → gate closed.

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(_ clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)

	if rec.Code != http.StatusTooEarly {
		t.Fatalf("status = %d, want 425 (handover-incomplete); body=%s", rec.Code, rec.Body.String())
	}
	if jobCreates != 0 {
		t.Errorf("created %d Jobs while handover incomplete, want 0 — that is the registry half-pivot", jobCreates)
	}
}

// TestHandleCutoverInternalTrigger_EnvOverrideBypassesGate proves the
// CATALYST_CUTOVER_REQUIRE_HANDOVER=false escape hatch lets a deliberate
// forced-demo cutover run on a converged-but-not-handed-over prov.
func TestHandleCutoverInternalTrigger_EnvOverrideBypassesGate(t *testing.T) {
	t.Setenv(envRequireHandoverForCutover, "false")
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	// openbao nil + env override=false → gate skipped, engine runs.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gate bypassed by env); body=%s", rec.Code, rec.Body.String())
	}

	// Drain the engine goroutine so it doesn't outlive the test.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
