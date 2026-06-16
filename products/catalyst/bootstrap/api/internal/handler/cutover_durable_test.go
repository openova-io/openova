// cutover_durable_test.go — #3379 cutover-PROOF integrity tests.
//
// Covers the Go-side faces of #3379:
//   - Face 1 (#3667): the durable, revert-immune sovereignty fact sealed in
//     OpenBao gates re-fire + resume even when the status ConfigMap was
//     reverted to cutoverComplete:"false" by a chart upgrade; runCutover
//     seals it in the success tail; HandleCutoverStatus backfills from it.
//   - Face 2 (#3671): runCutover refuses cutoverComplete=true while
//     registriesYamlActive != "v2".
//   - Face 4 (#3681): cutoverStartedAt is written ONCE (first run) and not
//     re-stamped on resume; cutoverLastAttemptStartedAt advances per attempt.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// fakeKVStore is a tiny in-memory KV-v2 backend supporting the GET + PUT
// calls openbao.Client.{GetKVv2,PutKVv2} make, so a test can assert the
// durable cutover-complete seal is written + read back. The path under test
// is /v1/secret/data/<secretPath>.
type fakeKVStore struct {
	mu   sync.Mutex
	data map[string]map[string]any // secretPath -> blob
}

func newFakeOpenbao(t *testing.T) (*openbao.Client, *fakeKVStore) {
	t.Helper()
	store := &fakeKVStore{data: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path shape: /v1/<mount>/data/<secretPath...>
		const prefix = "/v1/secret/data/"
		w.Header().Set("Content-Type", "application/json")
		if len(r.URL.Path) <= len(prefix) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := r.URL.Path[len(prefix):]
		store.mu.Lock()
		defer store.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			blob, ok := store.data[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			env := map[string]any{"data": map[string]any{"data": blob}}
			_ = json.NewEncoder(w).Encode(env)
		case http.MethodPost, http.MethodPut:
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			store.data[key] = body.Data
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	client := &openbao.Client{Addr: srv.URL, Token: "test-token", HTTP: srv.Client()}
	return client, store
}

// minimalPodSpec returns a one-container PodSpec for a job-mode cutover step.
func minimalPodSpec(t *testing.T) *corev1.PodSpec {
	t.Helper()
	return &corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{
			{Name: "step", Image: "busybox:1.36", Command: []string{"true"}},
		},
	}
}

// seedCutoverSeal writes the durable cutover-complete fact directly into the
// fake store, simulating a Sovereign that has ALREADY sealed it.
func (s *fakeKVStore) seedCutoverSeal(startedAt, finishedAt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[cutoverCompleteSecretPath] = map[string]any{
		"cutoverComplete":   "true",
		"cutoverStartedAt":  startedAt,
		"cutoverFinishedAt": finishedAt,
		"sealedAt":          finishedAt,
	}
}

func (s *fakeKVStore) hasCutoverSeal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[cutoverCompleteSecretPath]
	return ok
}

// ── Face 1 (#3667) ──────────────────────────────────────────────────────────

// TestResumeInterruptedCutover_NoOpWhenSealedDespiteRevertedCM proves the
// core durability guarantee: the status ConfigMap reads cutoverComplete:"false"
// AND cutoverStartedAt:"" (exactly the shape a `helm upgrade` revert produces),
// yet because the OpenBao seal is present, the resume hook fires NOTHING and
// backfills the CM to true.
func TestResumeInterruptedCutover_NoOpWhenSealedDespiteRevertedCM(t *testing.T) {
	// CM reverted by a chart upgrade: false + empty start (template defaults).
	revertedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data: map[string]string{
			"cutoverComplete":  "false",
			"cutoverStartedAt": "",
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		revertedCM,
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	ob, store := newFakeOpenbao(t)
	h.openbao = ob
	store.seedCutoverSeal("2026-06-16T13:36:31Z", "2026-06-16T14:11:12Z")

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	h.ResumeInterruptedCutover(context.Background())
	time.Sleep(100 * time.Millisecond)

	if jobCreates != 0 {
		t.Errorf("resume created %d Jobs despite a sealed cutover (reverted CM must NOT re-fire), want 0", jobCreates)
	}
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status CM: %v", err)
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q after resume with seal present, want backfilled to true", cm.Data["cutoverComplete"])
	}
}

// TestSpawnCutoverEngine_NoOpWhenSealedDespiteRevertedCM proves the auto-trigger
// / operator spawn path ALSO no-ops on a reverted CM when the seal exists — so
// the Helm post-upgrade auto-trigger Job cannot re-run the 600s hold.
func TestSpawnCutoverEngine_NoOpWhenSealedDespiteRevertedCM(t *testing.T) {
	revertedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "cutoverStartedAt": ""},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		revertedCM,
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	ob, store := newFakeOpenbao(t)
	h.openbao = ob
	store.seedCutoverSeal("2026-06-16T13:36:31Z", "2026-06-16T14:11:12Z")

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	res, err := h.spawnCutoverEngine(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, "internal")
	if err != nil {
		t.Fatalf("spawnCutoverEngine: %v", err)
	}
	if res.outcome != cutoverSpawnAlreadyComplete {
		t.Errorf("outcome = %v, want cutoverSpawnAlreadyComplete (sealed)", res.outcome)
	}
	time.Sleep(50 * time.Millisecond)
	if jobCreates != 0 {
		t.Errorf("spawn created %d Jobs on a sealed (reverted-CM) cutover, want 0", jobCreates)
	}
}

// TestRunCutover_SealsDurableFactOnSuccess proves the success tail seals the
// durable fact in OpenBao in the same run that flips the CM to true.
func TestRunCutover_SealsDurableFactOnSuccess(t *testing.T) {
	statusCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		// registriesYamlActive=v2 so the Face-2 invariant gate passes and the
		// run reaches the seal (a DaemonSet-less job-only chain here).
		Data: map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v2"},
	}
	objs := []k8sruntime.Object{statusCM}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	ob, store := newFakeOpenbao(t)
	h.openbao = ob

	steps := []cutoverStep{
		{stepName: "only-step", order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}
	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, steps, false)

	if !store.hasCutoverSeal() {
		t.Errorf("durable cutover-complete seal was NOT written on success")
	}
	cm, _ := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true", cm.Data["cutoverComplete"])
	}
}

// TestHandleCutoverStatus_BackfillsFromSeal proves /status answers
// cutoverComplete=true (state=sovereign) when the CM was reverted but the seal
// exists — so the SovereigntyCard never flickers back to the CTA.
func TestHandleCutoverStatus_BackfillsFromSeal(t *testing.T) {
	revertedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "cutoverStartedAt": ""},
	}
	objs := []k8sruntime.Object{revertedCM}
	h, _ := fakeHandlerWithCutover(t, objs...)
	ob, store := newFakeOpenbao(t)
	h.openbao = ob
	store.seedCutoverSeal("2026-06-16T13:36:31Z", "2026-06-16T14:11:12Z")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/cutover/status", nil)
	h.HandleCutoverStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		State            string `json:"state"`
		CutoverComplete  bool   `json:"cutoverComplete"`
		CutoverStartedAt string `json:"cutoverStartedAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.CutoverComplete || resp.State != "sovereign" {
		t.Errorf("backfill failed: cutoverComplete=%v state=%q, want true/sovereign", resp.CutoverComplete, resp.State)
	}
	if resp.CutoverStartedAt != "2026-06-16T13:36:31Z" {
		t.Errorf("cutoverStartedAt = %q, want recovered from seal 2026-06-16T13:36:31Z", resp.CutoverStartedAt)
	}
}

// ── Face 2 (#3671) ──────────────────────────────────────────────────────────

// TestRunCutover_RefusesCompleteWhenRegistriesYamlV1 proves the invariant gate:
// every step succeeds, but registriesYamlActive is still "v1" → the run FAILS
// rather than sealing a false sovereignty fact over a still-tethered node.
func TestRunCutover_RefusesCompleteWhenRegistriesYamlV1(t *testing.T) {
	statusCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v1"},
	}
	objs := []k8sruntime.Object{statusCM}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	ob, store := newFakeOpenbao(t)
	h.openbao = ob

	// A chain that CONTAINS registry-pivot (so the invariant gate applies) but
	// NO harbor-prewarm step, so the engine never flips registriesYamlActive to
	// v2 → it stays v1 → the invariant must fail. registry-pivot is run in
	// job-mode here so the gate (which keys on step NAME) fires without
	// invoking the DaemonSet ack-wait path.
	steps := []cutoverStep{
		{stepName: cutoverStepRegistryPivot, order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}
	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, steps, false)

	cm, _ := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if cm.Data["cutoverComplete"] == "true" {
		t.Errorf("cutoverComplete = true with registriesYamlActive=v1 — invariant gate failed to block")
	}
	if cm.Data["failedStep"] != "registry-pivot" {
		t.Errorf("failedStep = %q, want registry-pivot", cm.Data["failedStep"])
	}
	if store.hasCutoverSeal() {
		t.Errorf("durable seal was written despite registriesYamlActive=v1 — must NOT seal a still-tethered node")
	}
}

// TestRunCutover_FlipsRegistriesYamlV2AfterHarborPrewarm proves the engine sets
// registriesYamlActive=v2 the moment harbor-prewarm succeeds (keyed on step
// NAME, not index), and then the invariant gate is satisfied so the run
// completes.
func TestRunCutover_FlipsRegistriesYamlV2AfterHarborPrewarm(t *testing.T) {
	statusCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v1"},
	}
	objs := []k8sruntime.Object{statusCM}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	ob, _ := newFakeOpenbao(t)
	h.openbao = ob

	steps := []cutoverStep{
		{stepName: cutoverStepHarborPrewarm, order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}
	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, steps, false)

	cm, _ := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if cm.Data["registriesYamlActive"] != "v2" {
		t.Errorf("registriesYamlActive = %q, want v2 (flipped after harbor-prewarm)", cm.Data["registriesYamlActive"])
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true (v2 set → invariant satisfied)", cm.Data["cutoverComplete"])
	}
}

// TestCountRegistryPivotV2Acks counts only node ack keys whose value is "v2".
func TestCountRegistryPivotV2Acks(t *testing.T) {
	status := map[string]string{
		"node.cp-1.registriesYaml":     "v2",
		"node.worker-1.registriesYaml": "v2",
		"node.worker-2.registriesYaml": "v1", // not yet acked
		"registriesYamlActive":         "v2", // not a node ack
		"step.harbor-prewarm.result":   "success",
	}
	if got := countRegistryPivotV2Acks(status); got != 2 {
		t.Errorf("countRegistryPivotV2Acks = %d, want 2 (only v2 node acks)", got)
	}
}

// ── Face 4 (#3681) ──────────────────────────────────────────────────────────

// TestRunCutover_StartedAtWrittenOnceAcrossResume proves the audit-fidelity
// guard: two sequential runCutover calls (a resume after a catalyst-api roll)
// leave cutoverStartedAt BYTE-IDENTICAL — the first run's value — while
// cutoverLastAttemptStartedAt advances on the second.
func TestRunCutover_StartedAtWrittenOnceAcrossResume(t *testing.T) {
	statusCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v2"},
	}
	objs := []k8sruntime.Object{statusCM}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	ob, _ := newFakeOpenbao(t)
	h.openbao = ob

	steps := []cutoverStep{
		{stepName: "s1", order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}

	// First run.
	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, steps, false)
	cm1, _ := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	firstStart := cm1.Data["cutoverStartedAt"]
	firstAttempt := cm1.Data["cutoverLastAttemptStartedAt"]
	if firstStart == "" {
		t.Fatalf("cutoverStartedAt empty after first run")
	}

	// Reset cutoverComplete to false (simulate a chart-less mid-run resume that
	// re-enters runCutover) and ensure a measurable time delta.
	_ = patchCutoverStatus(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS},
		map[string]string{"cutoverComplete": "false"})
	time.Sleep(1100 * time.Millisecond)

	// Second run (resume / re-fire).
	h.runCutover(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, steps, false)
	cm2, _ := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})

	if cm2.Data["cutoverStartedAt"] != firstStart {
		t.Errorf("cutoverStartedAt changed on resume: %q -> %q (must be byte-identical, #3681)",
			firstStart, cm2.Data["cutoverStartedAt"])
	}
	if cm2.Data["cutoverLastAttemptStartedAt"] == firstAttempt {
		t.Errorf("cutoverLastAttemptStartedAt did NOT advance on resume (%q) — must carry the latest attempt time",
			cm2.Data["cutoverLastAttemptStartedAt"])
	}
}
