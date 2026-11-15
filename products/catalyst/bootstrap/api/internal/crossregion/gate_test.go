package crossregion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// gateRig is a two-region deployment for the gate: a kubeconfig file per
// region whose CONTENTS name the region, and a fake cluster per region that
// both the gate's Lists and its watchers read.
type gateRig struct {
	gate     Gate
	landed   *Signal
	primary  dynamic.Interface
	follower dynamic.Interface
	mu       sync.Mutex
	events   []string
	census   map[string]int
}

func newGateRig(t *testing.T, primaryObjs, followerObjs []*unstructured.Unstructured) *gateRig {
	t.Helper()
	dir := t.TempDir()
	primaryPath, followerPath := filepath.Join(dir, "dep.yaml"), filepath.Join(dir, "dep-"+testRegion+".yaml")
	for path, content := range map[string]string{primaryPath: "primary", followerPath: "follower"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	r := &gateRig{landed: &Signal{}, primary: fakeRegion(t, primaryObjs...), follower: fakeRegion(t, followerObjs...), census: map[string]int{}}
	byPath := map[string]dynamic.Interface{primaryPath: r.primary, followerPath: r.follower}
	byContent := map[string]dynamic.Interface{"primary": r.primary, "follower": r.follower}
	r.gate = Gate{
		PrimaryLabel: "fsn1",
		PrimaryPath:  primaryPath,
		Followers:    map[string]string{testRegion: followerPath},
		Provider:     "hetzner",
		Budget:       20 * time.Second,
		Landed:       r.landed.Done(),
		Dynamic:      func(path string) (dynamic.Interface, error) { return byPath[path], nil },
		WatchConfig: func(kubeconfig string) helmwatch.Config {
			return helmwatch.Config{
				KubeconfigYAML:         kubeconfig,
				DynamicFactory:         func(y string) (dynamic.Interface, error) { return byContent[y], nil },
				FirstSeenTimeout:       30 * time.Second,
				MinBootstrapKitHRs:     1,
				RecensusInterval:       30 * time.Millisecond,
				LatePollTimeout:        100 * time.Millisecond,
				LatePollInterval:       20 * time.Millisecond,
				ReadySentinelComponent: "catalyst-platform",
			}
		},
		Emit: func(level, message string) {
			r.mu.Lock()
			r.events = append(r.events, level+": "+message)
			r.mu.Unlock()
		},
		OnCensus: func(region string, _ []helmwatch.ComponentSnapshot) {
			r.mu.Lock()
			r.census[region]++
			r.mu.Unlock()
		},
		CallTimeout: testTimeout,
		Respin:      20 * time.Millisecond,
	}
	return r
}

func primaryReleases() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{readyRelease("bp-cilium"), readyRelease("bp-catalyst-platform")}
}

// TestGate_WaitsForEveryStepThenConverges drives the gate through each
// thing it waits for, one at a time, and ends on the first moment all hold.
func TestGate_WaitsForEveryStepThenConverges(t *testing.T) {
	ctx := context.Background()
	ks := kitKustomizationObject()
	ks.SetAnnotations(map[string]string{helmwatch.AnnotationReconcileRequestedAt: "2026-11-15T06:23:10Z"})
	rig := newGateRig(t, primaryReleases(), []*unstructured.Unstructured{
		ks,
		readyRelease("bp-cilium"),
		release("bp-keycloak", cond("Ready", "Unknown", "Progressing", time.Now())),
	})

	var verdict *Verdict
	done := make(chan struct{})
	go func() {
		verdict = rig.gate.Await(ctx)
		close(done)
	}()
	stillWaiting := func(why string) {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("the gate ended (%+v) while %s", verdict, why)
		case <-time.After(300 * time.Millisecond):
		}
	}

	// 1. The cross-region work has not landed: every release may still be
	// rendered again by the database-pair switch.
	stillWaiting("the cross-region work had not landed")
	rig.landed.Set()

	// 2. The follower's kit Kustomization has a reconcile request (the one
	// the switch stamps) that kustomize-controller has not handled.
	stillWaiting("the follower's kit Kustomization had not applied the switch")
	cur, err := rig.follower.Resource(kustomizationGVR).Namespace(helmwatch.FluxNamespace).Get(ctx, kitKustomization, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Kustomization: %v", err)
	}
	_ = unstructured.SetNestedField(cur.Object, "2026-11-15T06:23:10Z", "status", "lastHandledReconcileAt")
	if _, err := rig.follower.Resource(kustomizationGVR).Namespace(helmwatch.FluxNamespace).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update Kustomization: %v", err)
	}

	// 3. A follower release is still installing.
	stillWaiting("a follower release was installing")
	hr := getRelease(t, rig.follower, "bp-keycloak")
	_ = unstructured.SetNestedSlice(hr.Object, []any{cond("Ready", "True", "InstallSucceeded", time.Now())}, "status", "conditions")
	updateRelease(t, rig.follower, hr)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("the gate did not end although every region is installed and settled")
	}
	if verdict == nil || !verdict.Converged || !verdict.CrossRegionLanded {
		t.Fatalf("verdict = %+v, want converged", verdict)
	}
	if got := verdict.States[testRegion]["keycloak"]; got != helmwatch.StateInstalled {
		t.Errorf("verdict.States[%s][keycloak] = %q, want installed: the record's census is built from it", testRegion, got)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.census[""] == 0 || rig.census[testRegion] == 0 {
		t.Errorf("census callbacks = %v, want both regions (the caller writes its job rows from them)", rig.census)
	}
}

// TestGate_EndsShortAndNamesTheRegion: a follower release that stays failed
// ends the gate at its budget with a verdict that names region and release.
func TestGate_EndsShortAndNamesTheRegion(t *testing.T) {
	rig := newGateRig(t, primaryReleases(), []*unstructured.Unstructured{
		readyRelease("bp-cilium"),
		release("bp-keycloak", cond("Ready", "False", "InstallFailed", time.Now()), cond("Stalled", "True", "RetriesExceeded", time.Now())),
		release("bp-gitea", cond("Ready", "False", "DependencyNotReady", time.Now())),
	})
	rig.gate.Budget = 400 * time.Millisecond
	rig.landed.Set()

	v := rig.gate.Await(context.Background())
	if v.Converged {
		t.Fatalf("converged with a failed follower release")
	}
	if !v.CrossRegionLanded {
		t.Errorf("CrossRegionLanded = false although the signal was set")
	}
	summary := v.Summary()
	for _, want := range []string{"region " + testRegion, "bp-keycloak (failed)", "bp-gitea"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary does not name %q: %s", want, summary)
		}
	}
	if len(v.Pending["fsn1"]) != 0 {
		t.Errorf("the primary region is reported short: %v", v.Pending["fsn1"])
	}
	if v.Waited < rig.gate.Budget {
		t.Errorf("the gate gave up after %s, before its budget of %s", v.Waited, rig.gate.Budget)
	}
}

// TestGate_EndsShortWhenTheCrossRegionWorkNeverLands: every release reads
// Ready, the regions are not meshed. Not ready, and the verdict says why.
func TestGate_EndsShortWhenTheCrossRegionWorkNeverLands(t *testing.T) {
	rig := newGateRig(t, primaryReleases(), []*unstructured.Unstructured{readyRelease("bp-cilium")})
	rig.gate.Budget = 300 * time.Millisecond

	v := rig.gate.Await(context.Background())
	if v.Converged || v.CrossRegionLanded {
		t.Fatalf("verdict = %+v, want not converged and not landed", v)
	}
	if !strings.Contains(v.Summary(), "not meshed yet or the cross-region database pair is not switched on") {
		t.Errorf("summary does not say what is missing: %s", v.Summary())
	}
}

// TestGate_StopsWhenTheDeploymentLeftItsWatch: a wipe during the wait ends
// the gate at its next step instead of at the budget.
func TestGate_StopsWhenTheDeploymentLeftItsWatch(t *testing.T) {
	rig := newGateRig(t, primaryReleases(), []*unstructured.Unstructured{
		readyRelease("bp-cilium"),
		release("bp-keycloak", cond("Ready", "False", "InstallFailed", time.Now()), cond("Stalled", "True", "RetriesExceeded", time.Now())),
	})
	rig.gate.Budget = time.Minute
	rig.gate.Watching = func() bool { return false }
	rig.landed.Set()

	done := make(chan *Verdict, 1)
	go func() { done <- rig.gate.Await(context.Background()) }()
	select {
	case v := <-done:
		if v.Converged {
			t.Errorf("converged although a follower release is failed")
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the gate kept waiting for a deployment that left its watch")
	}
}

// TestReadCensus covers what counts as "settled with every release
// installed".
func TestReadCensus(t *testing.T) {
	ctx := context.Background()
	pending := func(t *testing.T, provider string, objs ...*unstructured.Unstructured) []string {
		t.Helper()
		c, err := ReadCensus(ctx, fakeRegion(t, objs...), provider, testTimeout)
		if err != nil {
			t.Fatalf("ReadCensus: %v", err)
		}
		return c.Pending
	}

	t.Run("every release installed", func(t *testing.T) {
		if got := pending(t, "hetzner", readyRelease("bp-a"), release("not-a-blueprint", cond("Ready", "False", "InstallFailed", time.Now()))); len(got) != 0 {
			t.Errorf("pending = %v, want none", got)
		}
	})
	t.Run("suspended and provider-gated releases do not hold a region back", func(t *testing.T) {
		suspended := release("bp-velero", cond("Ready", "False", "InstallFailed", time.Now()))
		_ = unstructured.SetNestedField(suspended.Object, true, "spec", "suspend")
		got := pending(t, "huawei", suspended, release("bp-hcloud-ccm", cond("Ready", "Unknown", "Progressing", time.Now())), readyRelease("bp-a"))
		if len(got) != 0 {
			t.Errorf("pending = %v, want none", got)
		}
	})
	t.Run("installing, failed and stalled releases are named", func(t *testing.T) {
		got := pending(t, "hetzner",
			release("bp-gitea", cond("Ready", "Unknown", "Progressing", time.Now())),
			release("bp-harbor", cond("Ready", "False", "InstallFailed", time.Now())),
			release("bp-keycloak", cond("Ready", "False", "RetriesExceeded", time.Now()), cond("Stalled", "True", "RetriesExceeded", time.Now())))
		want := "bp-gitea (installing)|bp-harbor (failed)|bp-keycloak (failed)"
		if strings.Join(got, "|") != want {
			t.Errorf("pending = %v, want %s", got, want)
		}
	})
	t.Run("a Ready release whose new generation is not observed is not installed", func(t *testing.T) {
		hr := readyRelease("bp-postgres-shared")
		hr.SetGeneration(3)
		_ = unstructured.SetNestedField(hr.Object, int64(2), "status", "observedGeneration")
		got := pending(t, "hetzner", hr)
		if len(got) != 1 || !strings.Contains(got[0], "bp-postgres-shared") {
			t.Errorf("pending = %v, want the release whose new desired state is not applied", got)
		}
	})
	t.Run("the kit Kustomization must have applied its current desired state", func(t *testing.T) {
		ks := kitKustomizationObject()
		ks.SetGeneration(5)
		_ = unstructured.SetNestedField(ks.Object, int64(4), "status", "observedGeneration")
		got := pending(t, "hetzner", ks, readyRelease("bp-a"))
		if len(got) != 1 || !strings.HasPrefix(got[0], "kustomization/"+kitKustomization) {
			t.Errorf("pending = %v, want the Kustomization", got)
		}
	})
}

func TestFollowerKubeconfigs(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	write("dep.yaml")
	inDir := write("dep-region-b.yaml")
	write("dep-cloudinit.log")
	write("dep-region-b.nodeip")
	write("other-region-b.yaml")
	elsewhere := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(elsewhere, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := FollowerKubeconfigs(dir, "dep", map[string]string{"region-c": elsewhere, "region-gone": filepath.Join(dir, "gone.yaml")})
	if len(got) != 2 || got["region-b"] != inDir || got["region-c"] != elsewhere {
		t.Errorf("FollowerKubeconfigs = %v, want region-b from the folder and region-c from the record, nothing else", got)
	}
	if got := FollowerKubeconfigs("", "dep", nil); len(got) != 0 {
		t.Errorf("FollowerKubeconfigs without a folder or a record = %v, want none", got)
	}
}

func TestSignal(t *testing.T) {
	var s Signal
	if s.IsSet() {
		t.Fatalf("a new Signal is set")
	}
	done := s.Done()
	s.Set()
	s.Set()
	select {
	case <-done:
	default:
		t.Fatalf("Done() taken before Set() is not closed after it")
	}
	if !s.IsSet() {
		t.Errorf("IsSet() = false after Set()")
	}
}

func TestBudget(t *testing.T) {
	if got := Budget(3 * time.Second); got != 3*time.Second {
		t.Errorf("Budget(override) = %s", got)
	}
	t.Setenv(BudgetEnv, "45m")
	if got := Budget(0); got != 45*time.Minute {
		t.Errorf("Budget from %s = %s, want 45m", BudgetEnv, got)
	}
	t.Setenv(BudgetEnv, "not-a-duration")
	if got := Budget(0); got != DefaultBudget {
		t.Errorf("Budget with a malformed value = %s, want the default %s", got, DefaultBudget)
	}
}

func TestShortfall(t *testing.T) {
	whole, short := fakeRegion(t, readyRelease("bp-a")), fakeRegion(t, release("bp-keycloak", cond("Ready", "False", "InstallFailed", time.Now())))
	dynFor := func(path string) (dynamic.Interface, error) {
		if path == "short" {
			return short, nil
		}
		return whole, nil
	}
	got := Shortfall(context.Background(), map[string]string{"region-b": "whole", "region-c": "short"}, "hetzner", dynFor, testTimeout)
	if len(got) != 1 || !strings.Contains(got["region-c"], "bp-keycloak (failed)") {
		t.Errorf("Shortfall = %v, want region-c with its failed release only", got)
	}
}
