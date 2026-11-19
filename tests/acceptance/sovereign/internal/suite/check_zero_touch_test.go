package suite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
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

// zeroTouchRecord is a run context whose console answers GET
// /api/v1/deployments/dep with status and body: the Sovereign's record of the
// deployment, the source of the retry-call clause.
func zeroTouchRecord(t *testing.T, status int, body string) *CheckContext {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/deployments/dep" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &CheckContext{API: api.New(srv.URL, "session"), Deploy: &DeploymentInfo{ID: "dep"}}
}

// zeroTouchNoRetry is a record whose list of retry calls is read and empty.
const zeroTouchNoRetry = `{"id":"dep","status":"ready","retryCalls":[]}`

// zeroTouchRun takes the first look in one scenario and runs the check in
// another, against a record without retry calls.
func zeroTouchRun(t *testing.T, start, end string) *runner {
	t.Helper()
	return zeroTouchRunWith(t, start, end, zeroTouchRecord(t, http.StatusOK, zeroTouchNoRetry))
}

// zeroTouchRunWith is zeroTouchRun with the run context cc.
func zeroTouchRunWith(t *testing.T, start, end string, cc *CheckContext) *runner {
	t.Helper()
	regions := fakeRegions(t)
	early := &runner{out: io.Discard, run: &Run{Mode: "run"}, A: regions[0], B: regions[1]}
	var first []*Step
	if start != "" {
		t.Setenv(fakeKubectlEnv, start)
		first = zeroTouchStart(context.Background(), early, []Check{zeroTouchCheck{}})
	}
	return runCheck(t, end, zeroTouchCheck{}, cc, first...)
}

func TestZeroTouchCheck(t *testing.T) {
	if _, ok := registry[zeroTouchID]; !ok || zeroTouchID != "FND-G02" {
		t.Fatalf("check %s is not registered", zeroTouchID)
	}
	// Clean at both looks and a record without retry calls: the check passes.
	r := zeroTouchRun(t, "zero-touch-clean", "zero-touch-clean")
	got := r.run.Checks[0]
	if got.Status != Pass || len(failedSteps(r)) != 0 {
		t.Fatalf("clean at both looks, no retry call: want PASS, got %+v %v", got, failedSteps(r))
	}
	// 2 steps of the first look, then per region one step for each look, then the retry clause.
	if len(r.run.Steps) != 7 || r.run.Steps[0].ID != "FND-G02.start.1" || r.run.Steps[6].Status != Pass {
		t.Fatalf("want 7 steps ending in the passed retry clause, got %d", len(r.run.Steps))
	}
	if ev := r.run.Steps[6].Evidence; ev["source"] != "GET /api/v1/deployments/dep (retryCalls)" {
		t.Errorf("the retry clause does not name its source: %+v", ev)
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

// TestZeroTouchRetryCalls: the retry clause passes only on a record that was
// read and lists no retry call. Everything else fails, and nothing is skipped.
func TestZeroTouchRetryCalls(t *testing.T) {
	const one = `{"id":"dep","retryCalls":[{"at":"2026-11-19T20:00:00Z","route":"POST /api/v1/deployments/{id}/phases/{phase}/retry","target":"phase phase1-watch","by":"owner@example.com"}]}`
	for name, c := range map[string]struct {
		status int
		body   string
		adjust func(cc *CheckContext) // nil leaves the context as it is
		want   string
	}{
		"one retry call, named": {http.StatusOK, one, nil,
			"1 retry calls: 2026-11-19T20:00:00Z POST /api/v1/deployments/{id}/phases/{phase}/retry phase phase1-watch by owner@example.com"},
		"the field is absent":     {http.StatusOK, `{"id":"dep","status":"ready"}`, nil, "not measured: the deployment record carries no retryCalls list"},
		"the field is null":       {http.StatusOK, `{"id":"dep","retryCalls":null}`, nil, "not measured: the deployment record carries no retryCalls list"},
		"the field is not a list": {http.StatusOK, `{"id":"dep","retryCalls":{}}`, nil, "not measured: retryCalls of the deployment record is not a list"},
		"the record answers 500": {http.StatusInternalServerError, `{}`, nil,
			"not measured: the deployment record could not be read: GET /api/v1/deployments/dep: HTTP 500"},
		"the record is not found": {http.StatusNotFound, "deployment not found", nil,
			"not measured: the deployment record could not be read: GET /api/v1/deployments/dep: HTTP 404"},
		"the record is not JSON": {http.StatusOK, "<html>", nil, "not measured: the deployment record could not be read"},
		"no console session": {http.StatusOK, zeroTouchNoRetry, func(cc *CheckContext) { cc.API = nil },
			"not measured: the run has no console session"},
		"no deployment named": {http.StatusOK, zeroTouchNoRetry, func(cc *CheckContext) { cc.Deploy = nil },
			"not measured: the run names no deployment"},
	} {
		cc := zeroTouchRecord(t, c.status, c.body)
		if c.adjust != nil {
			c.adjust(cc)
		}
		r := zeroTouchRunWith(t, "zero-touch-clean", "zero-touch-clean", cc)
		got := r.run.Checks[0]
		last := r.run.Steps[len(r.run.Steps)-1]
		if got.Status != Fail || !strings.Contains(got.Reason, c.want) || last.Status != Fail || !strings.Contains(last.Error, c.want) {
			t.Errorf("%s: want FAIL of the retry clause with %q, got %+v, last step %+v", name, c.want, got, last)
		}
		for _, s := range r.run.Steps {
			if s.Status == Skip {
				t.Errorf("%s: step %s is SKIP; the retry clause is measured or it fails", name, s.ID)
			}
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
