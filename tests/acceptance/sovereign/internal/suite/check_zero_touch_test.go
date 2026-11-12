package suite

import (
	"context"
	"io"
	"strings"
	"testing"
)

// fakeWorkload is a fixture Deployment, DaemonSet or StatefulSet. restartedAt,
// when not empty, is the restart annotation on its pod template.
func fakeWorkload(kind, namespace, name, restartedAt string, managers ...string) obj {
	annotations := obj{"checksum/config": "abc"}
	if restartedAt != "" {
		annotations[restartedAtAnnotation] = restartedAt
	}
	return obj{"kind": kind, "metadata": kubeMeta(namespace, name, managers...),
		"spec": obj{"template": obj{"metadata": obj{"annotations": annotations}}}}
}

func init() {
	releases := kubeList(fakeRelease("flux-system", "bp-cilium", false, "Ready", "True"))
	clean := map[string]string{
		"* " + hrResource: releases,
		"* deployments,daemonsets,statefulsets catalyst-system": kubeList(
			fakeWorkload("Deployment", "catalyst-system", "catalyst-api", "", "helm-controller"),
			// Field managers of a StatefulSet are not judged.
			fakeWorkload("StatefulSet", "catalyst-system", "store", "", "kubectl-patch")),
		"* deployments,daemonsets,statefulsets cnpg-system": kubeList(),
		"* deployments,daemonsets,statefulsets kube-system": kubeList(fakeWorkload("DaemonSet", "kube-system", "cilium", "", "helm")),
		// Field managers in flux-system are not judged: the bootstrap applies Flux with kubectl.
		"* deployments,daemonsets,statefulsets flux-system": kubeList(
			fakeWorkload("Deployment", "flux-system", "source-controller", "", "kubectl-client-side-apply")),
	}
	with := func(question, answer string) map[string]string {
		out := map[string]string{question: answer}
		for k, v := range clean {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
		return out
	}
	fakeAnswers["zero-touch-clean"] = clean
	fakeAnswers["zero-touch-release-patched"] = with("b "+hrResource, kubeList(
		obj{"kind": "HelmRelease", "metadata": kubeMeta("flux-system", "bp-harbor", "helm-controller", "kubectl-patch")}))
	fakeAnswers["zero-touch-server-side-apply"] = with("b deployments,daemonsets,statefulsets kube-system", kubeList(
		fakeWorkload("Deployment", "kube-system", "coredns", "", "kubectl")))
	fakeAnswers["zero-touch-restarted"] = with("b deployments,daemonsets,statefulsets flux-system", kubeList(
		fakeWorkload("Deployment", "flux-system", "source-controller", "2026-11-20T08:00:00Z", "helm")))
	fakeAnswers["zero-touch-statefulset-restarted"] = with("b deployments,daemonsets,statefulsets catalyst-system", kubeList(
		fakeWorkload("StatefulSet", "catalyst-system", "store", "2026-11-20T08:00:00Z", "helm")))
	fakeAnswers["zero-touch-no-field-managers"] = with("b "+hrResource, kubeList(
		obj{"kind": "HelmRelease", "metadata": kubeMeta("flux-system", "bp-harbor")}))
	fakeAnswers["zero-touch-nothing"] = map[string]string{"* " + hrResource: kubeList(),
		"* deployments,daemonsets,statefulsets": kubeList()}
	fakeAnswers["zero-touch-region-b-unreachable"] = map[string]string{"a " + hrResource: releases,
		"a deployments,daemonsets,statefulsets": kubeList()}
}

// zeroTouchRun takes the first look in one scenario and runs the check in another.
func zeroTouchRun(t *testing.T, start, end string) *runner {
	t.Helper()
	regions := fakeRegions(t)
	early := &runner{out: io.Discard, run: &Run{Mode: "run"}, A: regions[0], B: regions[1]}
	var first []*Step
	if start != "" {
		t.Setenv(fakeKubectlEnv, start)
		first = zeroTouchStart(context.Background(), early, []Check{zeroTouchCheck{}})
	}
	return runCheck(t, end, zeroTouchCheck{}, &CheckContext{}, first...)
}

func TestZeroTouchCheck(t *testing.T) {
	if _, ok := registry[zeroTouchID]; !ok || zeroTouchID != "FND-G02" {
		t.Fatalf("check %s is not registered", zeroTouchID)
	}
	// Clean at both looks: no step fails. The retry clause is not measured, so the check is SKIP, never PASS.
	r := zeroTouchRun(t, "zero-touch-clean", "zero-touch-clean")
	got := r.run.Checks[0]
	if got.Status != Skip || !strings.HasPrefix(got.Reason, "not measured: ") || len(failedSteps(r)) != 0 {
		t.Fatalf("clean at both looks: want SKIP for the retry clause and no failed step, got %+v %v", got, failedSteps(r))
	}
	// 2 steps of the first look, then per region one step for each look, then the skipped step.
	if len(r.run.Steps) != 7 || r.run.Steps[0].ID != "FND-G02.start.1" || r.run.Steps[6].Status != Skip {
		t.Fatalf("want 7 steps ending in a SKIP, got %d", len(r.run.Steps))
	}
	if ev := r.run.Steps[3].Evidence; ev["inspected"] != 5 || ev["region"] != "region-a" {
		t.Errorf("the look does not hold the evidence: %+v", ev)
	}
	if ev := r.run.Steps[2].Evidence; ev["start_step"] != "FND-G02.start.1" || ev["inspected"] != 5 {
		t.Errorf("the step for the first look does not point at it: %+v", ev)
	}
	for name, c := range map[string]struct{ start, end, want string }{
		"a HelmRelease patched by hand, seen at the end":     {"zero-touch-clean", "zero-touch-release-patched", "HelmRelease flux-system/bp-harbor: field manager kubectl-patch (Update)"},
		"a HelmRelease patched by hand, seen at the start":   {"zero-touch-release-patched", "zero-touch-clean", "HelmRelease flux-system/bp-harbor: field manager kubectl-patch (Update)"},
		"a server-side apply with kubectl":                   {"zero-touch-clean", "zero-touch-server-side-apply", "Deployment kube-system/coredns: field manager kubectl (Update)"},
		"a restart by hand in flux-system":                   {"zero-touch-clean", "zero-touch-restarted", "Deployment flux-system/source-controller: annotation kubectl.kubernetes.io/restartedAt=2026-11-20T08:00:00Z"},
		"a restart by hand, seen at the start only":          {"zero-touch-restarted", "zero-touch-clean", "annotation kubectl.kubernetes.io/restartedAt"},
		"a StatefulSet restarted by hand":                    {"zero-touch-clean", "zero-touch-statefulset-restarted", "StatefulSet catalyst-system/store: annotation"},
		"objects that come back without field managers":      {"zero-touch-clean", "zero-touch-no-field-managers", "1 objects came back without field managers, so they were not measured: HelmRelease flux-system/bp-harbor"},
		"no tracked object at all":                           {"zero-touch-clean", "zero-touch-nothing", "no tracked object was found: nothing was measured"},
		"a region that cannot be reached":                    {"zero-touch-clean", "zero-touch-region-b-unreachable", "[region-b] kubectl get " + hrResource},
		"no first look was taken":                            {"", "zero-touch-clean", "no look was taken at region region-a at the start of the run"},
		"the first look could not reach a region":            {"zero-touch-region-b-unreachable", "zero-touch-clean", "[region-b] kubectl get " + hrResource},
		"the first look found nothing to inspect anywhere":   {"zero-touch-nothing", "zero-touch-clean", "no tracked object was found"},
		"field managers missing at the first look as well":   {"zero-touch-no-field-managers", "zero-touch-clean", "came back without field managers"},
		"a server-side apply with kubectl at the first look": {"zero-touch-server-side-apply", "zero-touch-clean", "field manager kubectl (Update)"},
	} {
		got := zeroTouchRun(t, c.start, c.end).run.Checks[0]
		if got.Status != Fail || !strings.Contains(got.Reason, c.want) {
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
}

// TestZeroTouchStart: the first look is taken only when the check is selected.
func TestZeroTouchStart(t *testing.T) {
	regions := fakeRegions(t)
	t.Setenv(fakeKubectlEnv, "zero-touch-clean")
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}, A: regions[0], B: regions[1]}
	if first := zeroTouchStart(context.Background(), r, []Check{nodesCheck{}}); first != nil {
		t.Fatalf("the first look was taken although the check is not selected: %d steps", len(first))
	}
	first := zeroTouchStart(context.Background(), r, []Check{nodesCheck{}, zeroTouchCheck{}})
	if len(first) != 2 || first[1].Journey != zeroTouchID || first[1].ID != "FND-G02.start.2" || first[1].Status != Pass {
		t.Fatalf("want one passed step per region under the identifier of the check, got %+v", first)
	}
	// The first look adds no step to the run by itself: the journeys must not see it.
	if len(r.run.Steps) != 0 {
		t.Fatalf("the first look added %d steps to the run", len(r.run.Steps))
	}
	// A first look that fails does not change the result of the run by itself.
	t.Setenv(fakeKubectlEnv, "zero-touch-restarted")
	r.run.Steps = zeroTouchStart(context.Background(), r, []Check{zeroTouchCheck{}})
	if r.run.Finalize(nil); r.run.Result != Pass || len(failedSteps(r)) != 1 {
		t.Fatalf("a failed first look must be recorded and must not fail the run: result %s, failed %v", r.run.Result, failedSteps(r))
	}
}
