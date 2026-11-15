// Tests for #7902 defect 1: a HelmRelease whose first install was cut (a
// node reboot killed helm-controller during `pending-install`), was continued
// as an upgrade, failed, and is now `Stalled=True MissingRollbackTarget`.
// Flux never retries it. The watcher resets it once per stall and does not
// count it as terminally failed meanwhile.
package helmwatch

import (
	"context"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgotesting "k8s.io/client-go/testing"
)

// makeRollbackStalledHelmRelease is the object the fresh installation of 15
// November 2026 left in its primary region: the only history entry is the
// failed revision 2, and the release is stalled on a rollback with no target.
func makeRollbackStalledHelmRelease(name string, history ...string) *unstructured.Unstructured {
	u := makeHelmRelease(name, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "UpgradeFailed", Message: "Helm upgrade failed for release openbao/openbao with chart bp-openbao@1.2.78: post-upgrade hooks failed: 1 error occurred: * timed out waiting for the condition"},
		{Type: "Stalled", Status: metav1.ConditionTrue, Reason: ReasonMissingRollbackTarget, Message: "Failed to perform remediation: missing target release for rollback: cannot remediate failed release"},
	})
	if len(history) == 0 {
		history = []string{"failed"}
	}
	entries := make([]any, 0, len(history))
	for i, status := range history {
		entries = append(entries, map[string]any{
			"name":    "openbao",
			"version": int64(len(history) + 1 - i),
			"status":  status,
		})
	}
	_ = unstructured.SetNestedSlice(u.Object, entries, "status", "history")
	return u
}

// patchCounter counts the PATCH calls the watcher sends per HelmRelease and
// keeps the last body.
type patchCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func countPatches(client *dynamicfake.FakeDynamicClient) *patchCounter {
	pc := &patchCounter{counts: map[string]int{}}
	client.PrependReactor("patch", "helmreleases", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		pa := action.(clientgotesting.PatchAction)
		pc.mu.Lock()
		pc.counts[pa.GetName()]++
		pc.mu.Unlock()
		return false, nil, nil // let the tracker apply it
	})
	return pc
}

func (p *patchCounter) get(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[name]
}

func stallTestWatcher(t *testing.T, client *dynamicfake.FakeDynamicClient, rec *recorder, timeout time.Duration) *Watcher {
	t.Helper()
	w, err := NewWatcher(Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       timeout,
		FirstSeenTimeout:   30 * time.Second,
		MinBootstrapKitHRs: 2,
		DynamicFactory:     fakeFactory(client),
		RecensusInterval:   40 * time.Millisecond,
		LatePollTimeout:    200 * time.Millisecond,
		LatePollInterval:   50 * time.Millisecond,
	}, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	return w
}

func getHR(t *testing.T, client *dynamicfake.FakeDynamicClient, name string) *unstructured.Unstructured {
	t.Helper()
	u, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return u
}

// TestWatch_NeverDeployedRollbackStall_ResetOnce: the stalled release gets
// exactly one reset over many re-census cycles, with one token on the three
// annotations, and the watch does not conclude "failed" on it.
func TestWatch_NeverDeployedRollbackStall_ResetOnce(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newFakeScheme(),
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeRollbackStalledHelmRelease("bp-openbao"),
	)
	patches := countPatches(client)
	rec := &recorder{}
	w := stallTestWatcher(t, client, rec, 600*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got := patches.get("bp-openbao"); got != 1 {
		t.Fatalf("resets sent to bp-openbao = %d, want exactly 1 (0: Flux never retries a stalled release, so it stays down for good; more than 1: the re-census patches the same stall every cycle)", got)
	}
	if got := patches.get("bp-cilium"); got != 0 {
		t.Errorf("a healthy release was patched %d time(s)", got)
	}
	ann := getHR(t, client, "bp-openbao").GetAnnotations()
	token := ann[AnnotationReconcileRequestedAt]
	if token == "" || ann[AnnotationReconcileResetAt] != token || ann[AnnotationReconcileForceAt] != token {
		t.Errorf("annotations = %v, want one token on requestedAt, resetAt and forceAt (helm-controller ignores resetAt and forceAt whose value differs from requestedAt)", ann)
	}
	if got := w.Outcome(); got == OutcomeFailed {
		t.Errorf("Outcome() = %q: a release that was just reset is in flight, not failed", got)
	}
	if got := final["openbao"]; got != StateInstalling {
		t.Errorf("final[openbao] = %q, want %q", got, StateInstalling)
	}
}

// TestWatch_NeverDeployedRollbackStall_RecoversToReady: once helm-controller
// acts on the reset and the release installs, the watch concludes ready.
func TestWatch_NeverDeployedRollbackStall_RecoversToReady(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newFakeScheme(),
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeRollbackStalledHelmRelease("bp-openbao"),
	)
	patches := countPatches(client)
	rec := &recorder{}
	w := stallTestWatcher(t, client, rec, 20*time.Second)

	// Stand-in for helm-controller: when the reset arrives, install.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if patches.get("bp-openbao") > 0 {
				updateHR(t, client, "bp-openbao", []metav1.Condition{
					{Type: "Ready", Status: metav1.ConditionTrue, Reason: "UpgradeSucceeded", Message: "Helm upgrade succeeded"},
				})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := w.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got := w.Outcome(); got != OutcomeReady {
		t.Errorf("Outcome() = %q, want %q after the reset release installed", got, OutcomeReady)
	}
}

// TestWatch_RollbackStallOfDeployedRelease_NotTouched: a release that was
// deployed once has a rollback target in principle; the same reason on it is
// another defect and stays visible as a failure.
func TestWatch_RollbackStallOfDeployedRelease_NotTouched(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newFakeScheme(),
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeRollbackStalledHelmRelease("bp-openbao", "failed", "superseded"),
	)
	patches := countPatches(client)
	rec := &recorder{}
	w := stallTestWatcher(t, client, rec, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := w.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got := patches.get("bp-openbao"); got != 0 {
		t.Errorf("resets sent = %d, want 0 for a release that was deployed before", got)
	}
	if got := w.Outcome(); got != OutcomeFailed {
		t.Errorf("Outcome() = %q, want %q", got, OutcomeFailed)
	}
}

// TestHealNeverDeployedStalls_DroppedRequestIsSentAgain: a reset that
// arrives while a dependency is not ready is consumed by helm-controller
// without effect. It is sent again when the dependency is ready, and not
// before.
func TestHealNeverDeployedStalls_DroppedRequestIsSentAgain(t *testing.T) {
	stalled := makeRollbackStalledHelmRelease("bp-openbao")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newFakeScheme(),
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"}, stalled)
	patches := countPatches(client)
	rec := &recorder{}
	w := stallTestWatcher(t, client, rec, time.Second)
	ctx := context.Background()
	list := func() *unstructured.UnstructuredList {
		l, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return l
	}

	w.healNeverDeployedStalls(ctx, client, list())
	if got := patches.get("bp-openbao"); got != 1 {
		t.Fatalf("first pass: resets = %d, want 1", got)
	}
	token := getHR(t, client, "bp-openbao").GetAnnotations()[AnnotationReconcileRequestedAt]

	// helm-controller saw the request (lastHandledReconcileAt) and returned
	// early on the dependency: reset and force are not handled.
	u := getHR(t, client, "bp-openbao")
	_ = unstructured.SetNestedField(u.Object, token, "status", "lastHandledReconcileAt")
	setReady(u, metav1.ConditionFalse, "DependencyNotReady", "dependency 'flux-system/bp-cert-manager' is not ready")
	if _, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	w.healNeverDeployedStalls(ctx, client, list())
	if got := patches.get("bp-openbao"); got != 1 {
		t.Fatalf("while the dependency is not ready: resets = %d, want still 1 (a request sent now is dropped again)", got)
	}

	// Dependency ready; the release is back on its stall. Send again.
	u = getHR(t, client, "bp-openbao")
	setReady(u, metav1.ConditionFalse, "UpgradeFailed", "Helm upgrade failed")
	if _, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	w.healNeverDeployedStalls(ctx, client, list())
	if got := patches.get("bp-openbao"); got != 2 {
		t.Fatalf("after the dependency became ready: resets = %d, want 2", got)
	}
	second := getHR(t, client, "bp-openbao").GetAnnotations()[AnnotationReconcileRequestedAt]
	if second == token {
		t.Errorf("the second request reused the token %q, which helm-controller has already consumed", token)
	}

	// Handled this time: nothing more is sent for the same stall.
	u = getHR(t, client, "bp-openbao")
	for _, f := range []string{"lastHandledReconcileAt", "lastHandledResetAt", "lastHandledForceAt"} {
		_ = unstructured.SetNestedField(u.Object, second, "status", f)
	}
	if _, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	w.healNeverDeployedStalls(ctx, client, list())
	if got := patches.get("bp-openbao"); got != 2 {
		t.Errorf("after the request was handled: resets = %d, want still 2", got)
	}
}

func setReady(u *unstructured.Unstructured, status metav1.ConditionStatus, reason, message string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for i, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != "Ready" {
			continue
		}
		c["status"] = string(status)
		c["reason"] = reason
		c["message"] = message
		conds[i] = c
	}
	_ = unstructured.SetNestedSlice(u.Object, conds, "status", "conditions")
}

func TestRetryRequestDropped(t *testing.T) {
	build := func(requested, reset, force, handledReq, handledReset, handledForce string) *unstructured.Unstructured {
		u := makeReadyHelmRelease("bp-x")
		ann := map[string]string{}
		if requested != "" {
			ann[AnnotationReconcileRequestedAt] = requested
		}
		if reset != "" {
			ann[AnnotationReconcileResetAt] = reset
		}
		if force != "" {
			ann[AnnotationReconcileForceAt] = force
		}
		u.SetAnnotations(ann)
		for f, v := range map[string]string{"lastHandledReconcileAt": handledReq, "lastHandledResetAt": handledReset, "lastHandledForceAt": handledForce} {
			if v != "" {
				_ = unstructured.SetNestedField(u.Object, v, "status", f)
			}
		}
		return u
	}
	cases := []struct {
		name string
		u    *unstructured.Unstructured
		want bool
	}{
		{"no request", build("", "", "", "", "", ""), false},
		{"request not seen yet", build("t1", "t1", "t1", "", "", ""), false},
		{"seen, reset and force not handled", build("t1", "t1", "t1", "t1", "", ""), true},
		{"seen, an older reset handled", build("t2", "t2", "t2", "t2", "t1", "t1"), true},
		{"handled", build("t1", "t1", "t1", "t1", "t1", "t1"), false},
		{"plain reconcile request, no reset", build("t1", "", "", "t1", "", ""), false},
	}
	for _, c := range cases {
		if got := RetryRequestDropped(c.u); got != c.want {
			t.Errorf("%s: RetryRequestDropped = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestHealNeverDeployedStalls_OneResetAcrossWatchers: a region can have two
// watchers at once (the Phase-1 watch and the every-region gate), and a watch
// can be re-attached. The release's own annotation says the stall was reset;
// a second watcher does not reset it again.
func TestHealNeverDeployedStalls_OneResetAcrossWatchers(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newFakeScheme(),
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"}, makeRollbackStalledHelmRelease("bp-openbao"))
	patches := countPatches(client)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		w := stallTestWatcher(t, client, &recorder{}, time.Second)
		l, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		w.healNeverDeployedStalls(ctx, client, l)
	}
	if got := patches.get("bp-openbao"); got != 1 {
		t.Errorf("resets from two watchers = %d, want 1", got)
	}
}
