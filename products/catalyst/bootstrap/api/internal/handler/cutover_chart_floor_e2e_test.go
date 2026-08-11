// End-to-end coverage for the #5919 cutover chart-version floor.
//
// cutover_chart_floor_test.go tests assertCutoverChartFloor in isolation. This
// file drives the REAL HTTP handler, because a floor that is correct in
// isolation but never consulted by the trigger would be a guard on a surface
// that cannot fail. It also pins the placement decision (an already-complete
// cutover is never retro-failed) and proves the shipping chart really emits
// the label the floor reads.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// withHandoverArchiveNotYetCutOver wires an OpenBao stub that models a
// Sovereign which HAS been handed over but has NOT yet cut over.
//
// The existing withHandoverArchive helper answers EVERY path with the same
// non-empty KV-v2 envelope, including catalyst/cutover-complete. That makes
// sovereignCutoverComplete() report true, so spawnCutoverEngine short-circuits
// on the durable seal and never reaches step discovery — the engine never runs
// and the seal branch back-fills cutoverComplete=true into the ConfigMap. A
// test using it therefore asserts nothing about the engine, and a floor test
// using it would be measuring a surface that cannot fail.
//
// This variant answers 404 for the cutover-complete path (which the client maps
// to ErrSecretNotFound → "not sealed") and the archive envelope for everything
// else, so the trigger actually walks the gate.
func withHandoverArchiveNotYetCutOver(t *testing.T, h *Handler) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "cutover-complete") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"archive":"dGVzdA==","sovereignFQDN":"t99.omani.works"}}}`))
	}))
	t.Cleanup(srv.Close)
	h.openbao = &openbao.Client{Addr: srv.URL, Token: "test-token", HTTP: srv.Client()}
}

// makeCutoverStepCMAtChartVersion is makeCutoverStepCM with the chart-version
// label overridden, so a test can model a Sovereign running an old chart.
// An empty version DELETES the label, modelling a chart too old to stamp it.
func makeCutoverStepCMAtChartVersion(name, stepName string, order int, version string) *corev1.ConfigMap {
	cm := makeCutoverStepCM(name, stepName, order, cutoverModeJob, minimalPodSpecYAML, "")
	if version == "" {
		delete(cm.Labels, cutoverChartLabel)
	} else {
		cm.Labels[cutoverChartLabel] = helmLabel(version)
	}
	return cm
}

// TestInternalTriggerRefusesChartBelowFloor is the end-to-end proof: a POST to
// the real trigger handler, against a Sovereign whose step ConfigMaps carry the
// hw292 chart version, must answer 412 and must NOT start the engine.
func TestInternalTriggerRefusesChartBelowFloor(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCMAtChartVersion("cutover-step-01-gitea-mirror", "gitea-mirror", 1, "0.1.159"),
		makeCutoverStepCMAtChartVersion("cutover-step-06-helmrepository-patches", "helmrepository-patches", 6, "0.1.159"),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	withHandoverArchiveNotYetCutOver(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body=%s", rec.Code, rec.Body.String())
	}

	// writeJSON also stamps numeric `status`/`code` fields, so decode loosely
	// and read the strings we care about.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, rec.Body.String())
	}
	str := func(k string) string {
		s, _ := body[k].(string)
		return s
	}
	// Assert on the VALUES, not on key presence. A body carrying
	// observedVersion:"" would name no diagnosis and must not count as a pass.
	if got := str("error"); got != "cutover-chart-below-floor" {
		t.Errorf("error = %q, want %q", got, "cutover-chart-below-floor")
	}
	if got := str("observedVersion"); got != "0.1.159" {
		t.Errorf("observedVersion = %q, want %q — the operator must be told WHICH chart is installed", got, "0.1.159")
	}
	if got := str("minChartVersion"); got != cutoverMinChartVersion {
		t.Errorf("minChartVersion = %q, want %q", got, cutoverMinChartVersion)
	}
	if d := str("detail"); !strings.Contains(d, "#5919") || !strings.Contains(d, cutoverMinChartVersion) {
		t.Errorf("detail must cite the issue and the floor; got %q", d)
	}

	// The engine must not have run: no cutover Jobs, and cutoverComplete
	// must still be false. This is the real payload of the fix — 0.1.159
	// reaching step-08 is what put a 600s deny-egress hold on hw292 and then
	// certified it as sovereign.
	jobs, err := client.BatchV1().Jobs(cutoverTestNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("engine started: %d Jobs created, want 0", len(jobs.Items))
	}
	// The status ConfigMap is written by the engine, so on a clean refusal it
	// legitimately does not exist yet. Absent counts as "not complete";
	// present-and-true is the #5919 failure.
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(
		context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{},
	)
	switch {
	case apierrors.IsNotFound(err):
		// nothing was written — the engine never ran, which is the point
	case err != nil:
		t.Fatalf("get status ConfigMap: %v", err)
	case cm.Data["cutoverComplete"] == "true":
		t.Error("cutoverComplete = true — a below-floor chart certified a cutover, which is exactly #5919")
	}
}

// TestInternalTriggerRefusesChartWithNoVersionLabel is the positive-evidence
// case at the HTTP edge: deleting the label must not become a bypass.
func TestInternalTriggerRefusesChartWithNoVersionLabel(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCMAtChartVersion("cutover-step-01-gitea-mirror", "gitea-mirror", 1, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	withHandoverArchiveNotYetCutOver(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412 when the chart version is unknowable; body=%s", rec.Code, rec.Body.String())
	}
	jobs, err := client.BatchV1().Jobs(cutoverTestNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("engine started on an unknown chart version: %d Jobs, want 0", len(jobs.Items))
	}
}

// TestInternalTriggerAcceptsChartAtFloor is the CONTROL for the tests above:
// the same handler, same fixtures, same auth — differing ONLY in the chart
// version label. It must reach the engine (200). Without this, a handler that
// 412'd unconditionally would pass every test above.
func TestInternalTriggerAcceptsChartAtFloor(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCMAtChartVersion("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverMinChartVersion),
		makeCutoverStepCMAtChartVersion("cutover-step-06-helmrepository-patches", "helmrepository-patches", 6, cutoverMinChartVersion),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	withHandoverArchiveNotYetCutOver(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("control: status = %d, want 200 at the floor version; body=%s", rec.Code, rec.Body.String())
	}

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
		t.Errorf("control: cutoverComplete = %q, want true — the floor must not block a compliant chart", cm.Data["cutoverComplete"])
	}
}

// TestAlreadyCompleteCutoverIsNotRetroFailedByTheFloor pins the placement
// decision. hw292 has ALREADY cut over on 0.1.159. Raising a floor afterwards
// must not turn its idempotent 200 into a 412 — the floor governs whether a
// cutover may START, never whether a finished one is revoked.
func TestAlreadyCompleteCutoverIsNotRetroFailedByTheFloor(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCMAtChartVersion("cutover-step-01-gitea-mirror", "gitea-mirror", 1, "0.1.159"),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installTokenReviewReactor(t, client, "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner")
	// The DURABLE seal is present — this Sovereign has already cut over, as
	// hw292 has. withHandoverArchive answers every OpenBao path non-empty,
	// which is exactly the sealed state here (unlike the tests above, where
	// that would have masked the gate).
	withHandoverArchive(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/cutover/trigger", nil)
	req.Header.Set("Authorization", "Bearer fake-sa-token-bytes")
	h.HandleCutoverInternalTrigger(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent no-op); a completed cutover must not be retro-failed by the floor. body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestRealChartEmitsVersionLabelAndMeetsItsOwnFloor reads the ACTUAL chart in
// this repo. Two things must hold, and both are invisible to a fixture-only
// test:
//
//  1. templates/_helpers.tpl must still emit helm.sh/chart. If a refactor drops
//     it, every step ConfigMap loses its version and the floor would refuse
//     EVERY cutover on every Sovereign. That failure would otherwise surface
//     only on a live prov.
//  2. The chart version shipping today must be >= the floor. A floor above the
//     shipping chart bricks the cutover; this catches it at build time.
func TestRealChartEmitsVersionLabelAndMeetsItsOwnFloor(t *testing.T) {
	root := "../../../../../../platform/self-sovereign-cutover/chart"

	helpers, err := os.ReadFile(filepath.Join(root, "templates", "_helpers.tpl"))
	if err != nil {
		t.Skipf("chart helpers not readable from this working dir (%v) — skipping repo-coupled check", err)
	}
	if !strings.Contains(string(helpers), cutoverChartLabel) {
		t.Errorf("templates/_helpers.tpl no longer emits %s — every step ConfigMap would lose its version and the #5919 floor would refuse every cutover",
			cutoverChartLabel)
	}
	if !strings.Contains(string(helpers), ".Chart.Version") {
		t.Errorf("templates/_helpers.tpl no longer interpolates .Chart.Version into %s", cutoverChartLabel)
	}

	chartYAML, err := os.ReadFile(filepath.Join(root, "Chart.yaml"))
	if err != nil {
		t.Skipf("Chart.yaml not readable from this working dir (%v)", err)
	}
	var shipping string
	for _, line := range strings.Split(string(chartYAML), "\n") {
		if strings.HasPrefix(line, "version:") {
			shipping = strings.TrimSpace(strings.TrimPrefix(line, "version:"))
			break
		}
	}
	if shipping == "" {
		t.Fatal("could not read `version:` from the real Chart.yaml")
	}
	cmp, err := compareChartVersions(shipping, cutoverMinChartVersion)
	if err != nil {
		t.Fatalf("comparing shipping chart %q against floor %q: %v", shipping, cutoverMinChartVersion, err)
	}
	if cmp < 0 {
		t.Fatalf("the shipping chart %s is BELOW the floor %s — this build would refuse every cutover",
			shipping, cutoverMinChartVersion)
	}
	t.Logf("shipping chart %s >= floor %s", shipping, cutoverMinChartVersion)
}
