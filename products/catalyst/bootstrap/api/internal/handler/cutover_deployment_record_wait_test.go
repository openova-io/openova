package handler

// The cutover's pre-step secondary-kubeconfigs waits for the deployment record
// instead of failing because it has not arrived yet
// (cutover_deployment_record_wait.go).
//
// Measured 20 Nov 2026 on a fresh two-region install: the hand-over seal fired
// the cutover at 15:21:53, the parent's POST /api/v1/internal/deployments/import
// stored the record at 15:22:09, the first attempt failed with "the resolved
// deployment record carries an EMPTY deployment id ... expects 1 secondary
// region(s)" and the first automatic repetition passed at 15:28:10.
//
// These tests drive runCutover itself, the way the seal does, on a chroot
// whose region-B kubeconfig is delivered but whose record is not yet stored.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cutoverreconcile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

const (
	recordWaitFQDN  = "hw307.omani.works"
	recordWaitDepID = "dep20261120"
	recordWaitKey   = "me-east-215-b-1"
)

func recordWaitRegions() []provisioner.RegionSpec {
	return []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
}

// recordWaitFixture is a two-region chroot right after the hand-over seal: the
// region-B kubeconfig is on disk as <depID>-<regionKey>.yaml, the primary
// <depID>.yaml is not (so no prefix can be derived from disk), and no
// deployment record is stored. budget is CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS.
func recordWaitFixture(t *testing.T, budget string) (*Handler, *fakek8s.Clientset, *cutoverDeps, []cutoverStep) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", recordWaitFQDN)
	t.Setenv("CATALYST_OTECH_FQDN", recordWaitFQDN)
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod")
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", budget)

	// A usable document pointing at a local port nothing listens on, so no
	// code path of the run can reach a real address.
	kc := strings.ReplaceAll(completeKubeconfigSameCluster, "https://212.72.24.6:6443", "https://127.0.0.1:1")
	if err := os.WriteFile(filepath.Join(dir, recordWaitDepID+"-"+recordWaitKey+".yaml"), []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}

	statusCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
		Data:       map[string]string{"cutoverComplete": "false", "registriesYamlActive": "v2"},
	}
	h, client := fakeHandlerWithCutover(t, statusCM)
	installJobReactor(t, client, batchv1.JobComplete)
	ob, kv := newFakeOpenbao(t)
	h.openbao = ob
	kv.seedHandoverSeal()
	retryCallStore(t, h)
	if h.resolveCutoverDeployment() != nil {
		t.Fatal("fixture already holds a deployment record")
	}
	steps := []cutoverStep{{stepName: "only-step", order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)}}
	return h, client, &cutoverDeps{core: client, ns: cutoverTestNS}, steps
}

// failedStepWrites records every status patch that sets failedStep.
func failedStepWrites(client *fakek8s.Clientset) func() []string {
	var mu sync.Mutex
	var seen []string
	client.PrependReactor("patch", "configmaps", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		var body struct {
			Data map[string]string `json:"data"`
		}
		if pa, ok := action.(clienttesting.PatchAction); ok && json.Unmarshal(pa.GetPatch(), &body) == nil && body.Data["failedStep"] != "" {
			mu.Lock()
			seen = append(seen, body.Data["failedStep"])
			mu.Unlock()
		}
		return false, nil, nil
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func cutoverStatusOf(t *testing.T, client *fakek8s.Clientset) map[string]string {
	t.Helper()
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	return cm.Data
}

// runCutoverWithin runs one attempt and fails the test if it does not end
// within limit (a pre-step that hangs, or waits on a record that arrived).
func runCutoverWithin(t *testing.T, h *Handler, deps *cutoverDeps, steps []cutoverStep, operatorRetry bool, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runCutover(context.Background(), deps, steps, operatorRetry)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("the cutover attempt did not end within %s", limit)
	}
	return time.Since(start)
}

// The seal arrives before the import: the first attempt waits, and when the
// import lands it goes on. Before the wait it failed at once with
// failedStep=secondary-kubeconfigs, which SOV-242 then counted as repetition 1.
func TestCutoverPreStep_SealBeforeImport_FirstAttemptWaitsForTheRecord(t *testing.T) {
	h, client, deps, steps := recordWaitFixture(t, "60")
	failedWrites := failedStepWrites(client)
	events, _, cancel := h.cutoverBusFor().Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runCutover(context.Background(), deps, steps, false)
	}()

	limit := time.After(30 * time.Second)
waitAnnounced:
	for {
		select {
		case <-done:
			s := cutoverStatusOf(t, client)
			t.Fatalf("the first attempt ended before the import landed: failedStep=%q lastError=%q", s["failedStep"], s["lastError"])
		case ev := <-events:
			if strings.Contains(ev.Message, "waiting for the deployment record") {
				break waitAnnounced
			}
		case <-limit:
			t.Fatal("the pre-step neither waited nor ended within 30s")
		}
	}

	// The parent's import lands.
	importRecord(t, h, store.Record{
		ID:      recordWaitDepID,
		Request: store.RedactedRequest{SovereignFQDN: recordWaitFQDN, Regions: recordWaitRegions()},
	}, "")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the attempt did not go on within 30s after the record arrived")
	}

	s := cutoverStatusOf(t, client)
	if s["failedStep"] != "" || s["cutoverComplete"] != "true" {
		t.Fatalf("first attempt: failedStep=%q cutoverComplete=%q lastError=%q, want no failed step and a complete cutover",
			s["failedStep"], s["cutoverComplete"], s["lastError"])
	}
	if w := failedWrites(); len(w) != 0 {
		t.Fatalf("the first attempt wrote failedStep %v; a record that has not arrived yet is a wait, not a failed step", w)
	}
	if s[cutoverreconcile.StepKey] != "" || s[cutoverreconcile.CountKey] != "" {
		t.Fatalf("a repetition record exists (%s=%q %s=%q); none may be opened",
			cutoverreconcile.StepKey, s[cutoverreconcile.StepKey], cutoverreconcile.CountKey, s[cutoverreconcile.CountKey])
	}
	sec, err := client.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the secondary-kubeconfigs Secret was not materialized after the record arrived: %v", err)
	}
	if _, ok := sec.Data[recordWaitKey+".yaml"]; !ok {
		t.Fatalf("Secret keys %v, want %s.yaml", secretDataKeys(sec), recordWaitKey)
	}
	if got := sec.Annotations["catalyst.openova.io/deployment-id"]; got != recordWaitDepID {
		t.Fatalf("Secret deployment-id annotation %q, want %q (from the arrived record)", got, recordWaitDepID)
	}
}

// The import never arrives: the step fails once the bound has passed, naming
// the import. A later attempt (an automatic repetition) is past the bound, which
// counts from the cutover's first start, and fails at once, as before the wait.
func TestCutoverPreStep_ImportNeverArrives_FailsAfterTheBoundNamingTheImport(t *testing.T) {
	const bound = 2 * time.Second
	h, client, deps, steps := recordWaitFixture(t, "2")

	elapsed := runCutoverWithin(t, h, deps, steps, false, 30*time.Second)
	s := cutoverStatusOf(t, client)
	if s["failedStep"] != "secondary-kubeconfigs" {
		t.Fatalf("failedStep=%q lastError=%q, want secondary-kubeconfigs", s["failedStep"], s["lastError"])
	}
	if elapsed < bound {
		t.Fatalf("the step failed after %s, before the bound of %s", elapsed, bound)
	}
	for _, want := range []string{"no deployment record for this Sovereign has arrived", "POST /api/v1/internal/deployments/import", "waited until"} {
		if !strings.Contains(s["lastError"], want) {
			t.Fatalf("lastError does not name %q: %s", want, s["lastError"])
		}
	}

	elapsed = runCutoverWithin(t, h, deps, steps, true, 30*time.Second)
	s = cutoverStatusOf(t, client)
	if s["failedStep"] != "secondary-kubeconfigs" || !strings.Contains(s["lastError"], "POST /api/v1/internal/deployments/import") {
		t.Fatalf("repetition: failedStep=%q lastError=%q, want the same failure naming the import", s["failedStep"], s["lastError"])
	}
	if elapsed >= bound {
		t.Fatalf("a repetition past the bound waited %s again; it must fail at once", elapsed)
	}
}

// A record that HAS arrived with an empty id is not a wait: the step fails at
// once with the #5488 message, as it did before.
func TestCutoverPreStep_ArrivedRecordWithEmptyIDFailsAtOnce(t *testing.T) {
	h, client, deps, steps := recordWaitFixture(t, "600")
	h.deployments.Store("", &Deployment{
		ID:      "",
		Request: provisioner.Request{SovereignFQDN: recordWaitFQDN, Regions: recordWaitRegions()},
	})

	runCutoverWithin(t, h, deps, steps, false, 30*time.Second)
	s := cutoverStatusOf(t, client)
	if s["failedStep"] != "secondary-kubeconfigs" || !strings.Contains(s["lastError"], "EMPTY deployment id") {
		t.Fatalf("failedStep=%q lastError=%q, want secondary-kubeconfigs with the EMPTY deployment id failure", s["failedStep"], s["lastError"])
	}
	if strings.Contains(s["lastError"], "has not landed") {
		t.Fatalf("an arrived record was reported as not arrived: %s", s["lastError"])
	}
}
