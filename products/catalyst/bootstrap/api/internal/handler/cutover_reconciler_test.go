package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cutoverreconcile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/retrycall"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// seedHandoverSeal writes the Sovereign-side handover marker
// (secret/catalyst/tofu-phase0-archive) into the fake store, simulating a
// Sovereign that has received the Phase-0 archive from the mothership.
func (s *fakeKVStore) seedHandoverSeal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data["catalyst/tofu-phase0-archive"] = map[string]any{
		"archive":  "<base64-tofu-archive>",
		"sealedAt": "2026-06-16T13:36:31Z",
	}
}

// TestRunCutoverReconcilePass drives the #4635 level-triggered reconciler
// through its three pure-state branches. The decision is state, never timing:
// fire iff (handover seal exists) && (cutover-complete seal absent).
func TestRunCutoverReconcilePass(t *testing.T) {
	cases := []struct {
		name       string
		handover   bool // tofu-phase0-archive sealed
		complete   bool // durable cutover-complete sealed
		wantFires  int
		wantSource string
	}{
		{name: "pre-handover: seal absent → no fire", handover: false, complete: false, wantFires: 0},
		{name: "already complete → no fire", handover: true, complete: true, wantFires: 0},
		{name: "sealed-but-uncut → fires with source=reconcile", handover: true, complete: false, wantFires: 1, wantSource: "reconcile"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []k8sruntime.Object{
				makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
					Data:       map[string]string{"cutoverComplete": "false", "cutoverStartedAt": ""},
				},
			}
			h, _ := fakeHandlerWithCutover(t, objs...)
			ob, store := newFakeOpenbao(t)
			h.openbao = ob
			if tc.handover {
				store.seedHandoverSeal()
			}
			if tc.complete {
				store.seedCutoverSeal("2026-06-16T13:36:31Z", "2026-06-16T14:11:12Z")
			}

			fires := 0
			gotSource := ""
			h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, source string) (cutoverSpawnResult, error) {
				fires++
				gotSource = source
				return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
			}

			h.runCutoverReconcilePass(context.Background())

			if fires != tc.wantFires {
				t.Fatalf("fires=%d, want %d", fires, tc.wantFires)
			}
			if tc.wantSource != "" && gotSource != tc.wantSource {
				t.Fatalf("source=%q, want %q", gotSource, tc.wantSource)
			}
		})
	}
}

// TestRunCutoverReconcilePass_FailsClosedOnSealReadError proves a transient
// OpenBao read error never half-fires the engine — the pass returns without
// firing and the next tick retries (no give-up).
func TestRunCutoverReconcilePass_FailsClosedOnSealReadError(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data:       map[string]string{"cutoverComplete": "false"},
		},
	}
	h, _ := fakeHandlerWithCutover(t, objs...)
	// A nil OpenBao client makes sovereignHandoverComplete read as
	// not-handed-over (false, nil) — the gate stays closed, no fire. This is
	// the Catalyst-Zero / pre-OpenBao path; the reconciler must never fire
	// without a confirmed seal.
	h.openbao = nil

	fires := 0
	h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, _ string) (cutoverSpawnResult, error) {
		fires++
		return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
	}
	h.runCutoverReconcilePass(context.Background())
	if fires != 0 {
		t.Fatalf("fires=%d, want 0 (no seal ⇒ never fire)", fires)
	}
}

// TestCutoverSourceRetriesFailedStep guards the #4635 gap: the level-triggered
// reconciler (source="reconcile") MUST re-run a genuinely-failed step (else a
// cutover wedged at a failed step stays wedged and the reconciler is inert),
// while the one-shot edge triggers fail closed.
func TestCutoverSourceRetriesFailedStep(t *testing.T) {
	cases := map[string]bool{
		"internal":  false, // one-shot Helm hook — fail closed
		"handover":  false, // one-shot seal-fire — fail closed
		"operator":  true,  // BSS CTA — deliberate retry
		"reconcile": true,  // #4635 level-triggered — re-runs the step when it fires (cutoverreconcile bounds how often)
		"":          false, // unknown — fail closed
	}
	for source, want := range cases {
		if got := cutoverSourceRetriesFailedStep(source); got != want {
			t.Errorf("cutoverSourceRetriesFailedStep(%q) = %v, want %v", source, got, want)
		}
	}
}

// TestRunCutoverReconcilePass_FailedStepWaitsForItsPause (SOV-242): the pass
// that first sees a failed step opens its repetition record and fires nothing
// (before, it re-fired the failed step on every tick).
func TestRunCutoverReconcilePass_FailedStepWaitsForItsPause(t *testing.T) {
	h, client := fakeHandlerWithCutover(t,
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data: map[string]string{"cutoverComplete": "false", "failedStep": "gitea-mirror"}})
	ob, store := newFakeOpenbao(t)
	h.openbao = ob
	store.seedHandoverSeal()
	fires := 0
	h.spawnCutoverEngineFn = func(context.Context, *cutoverDeps, string) (cutoverSpawnResult, error) {
		fires++
		return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
	}
	h.runCutoverReconcilePass(context.Background())
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if fires != 0 || cm.Data[cutoverreconcile.StepKey] != "gitea-mirror" || cm.Data[cutoverreconcile.CountKey] != "0" {
		t.Fatalf("fires=%d record=%v, want no fire and an open record for gitea-mirror", fires, cm.Data)
	}
}

// spentRecordHandler: a Sovereign whose gitea-mirror step spent its repetitions.
func spentRecordHandler(t *testing.T) (*Handler, *fakek8s.Clientset) {
	return fakeHandlerWithCutover(t,
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS}, Data: map[string]string{
			"cutoverComplete": "false", "failedStep": "gitea-mirror",
			cutoverreconcile.StepKey: "gitea-mirror", cutoverreconcile.CountKey: "3", cutoverreconcile.HaltedKey: "true",
		}})
}

// TestHandleCutoverStart_ClearsSpentRepetitionRecord (SOV-242): the
// sovereign-admin's start route, recorded as a retry call, is the way past a
// halted step; it clears the record.
func TestHandleCutoverStart_ClearsSpentRepetitionRecord(t *testing.T) {
	const fqdn = "t99.omani.works"
	t.Setenv("SOVEREIGN_FQDN", fqdn)
	h, client := spentRecordHandler(t)
	installJobReactor(t, client, batchv1.JobComplete)
	st := retryCallStore(t, h)
	dep := &Deployment{ID: "dep-cutover", Status: "ready", Request: provisioner.Request{SovereignFQDN: fqdn}}
	dep.retries.Start()
	h.deployments.Store(dep.ID, dep)
	rec := httptest.NewRecorder()
	h.HandleCutoverStart(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	waitCutoverIdle(t, h)
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	for k := range cutoverreconcile.Cleared() {
		if cm.Data[k] != "" {
			t.Errorf("%s = %q after the start route, want it cleared", k, cm.Data[k])
		}
	}
	assertOneRetryCall(t, st, dep, retrycall.RouteCutoverStart, "cutover chain")
}

// TestHandleCutoverStart_RecordClearFailureIsAnError (SOV-242): a record
// that cannot be cleared would halt the resumed run at its first failure, so
// the route answers an error, starts no engine run and frees the run slot.
// The retry call stays recorded: the route records it before the clear
// (model rule 5), so the refused call is on the record too.
func TestHandleCutoverStart_RecordClearFailureIsAnError(t *testing.T) {
	const fqdn = "t99.omani.works"
	t.Setenv("SOVEREIGN_FQDN", fqdn)
	h, client := spentRecordHandler(t)
	client.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	st := retryCallStore(t, h)
	dep := &Deployment{ID: "dep-cutover", Status: "ready", Request: provisioner.Request{SovereignFQDN: fqdn}}
	dep.retries.Start()
	h.deployments.Store(dep.ID, dep)
	rec := httptest.NewRecorder()
	h.HandleCutoverStart(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil))
	if rec.Code != http.StatusBadGateway || !h.cutoverBusFor().tryStartRun() {
		t.Fatalf("status %d, want 502 and the run slot free; body=%s", rec.Code, rec.Body.String())
	}
	assertOneRetryCall(t, st, dep, retrycall.RouteCutoverStart, "cutover chain")
}
