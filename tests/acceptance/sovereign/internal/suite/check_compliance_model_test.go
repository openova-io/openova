package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// modelFixture is one region's cluster and the API, as the check reads them.
type modelFixture struct {
	models     map[string]string // region -> the object default, as JSON; absent: NotFound
	legacy     string            // the ClusterPolicy list of every region
	validating string            // the ValidatingPolicy list of every region
	reports    string            // the policy reports of the first region
	policies   []map[string]any  // the items of the policies route
	readErr    error             // the answer to every read of region-b
}

const (
	// One check weighs its severity (high: 4), one states its weight, one is no kyverno check.
	fixtureModel = `{"spec": {"version": "1.0.0", "severityWeights": {"critical": 8, "high": 4, "medium": 2, "low": 1},
	  "checks": [{"id": "probes-present", "source": "kyverno", "severity": "high"},
	             {"id": "image-tag-pinned", "source": "kyverno", "severity": "high", "weight": 7},
	             {"id": "hubble-flows-seen", "source": "evaluator", "severity": "low"}]}}`
	fixtureLegacy = `{"items": [
	  {"metadata": {"name": "probes-present"}, "spec": {"rules": [{"name": "r", "validate": {}}]}},
	  {"metadata": {"name": "app-label-mutate"}, "spec": {"rules": [{"name": "r", "mutate": {}}]}}]}`
	fixtureValidating = `{"items": [{"metadata": {"name": "image-tag-pinned"}, "spec": {"validationActions": ["Deny"]}}]}`
	fixtureReports    = `{"items": [
	  {"results": [{"policy": "probes-present", "result": "pass"}, {"policy": "probes-present", "result": "fail"}]},
	  {"results": [{"policy": "app-label-mutate", "result": "pass"}]}]}`
)

func goodModelFixture() *modelFixture {
	return &modelFixture{
		models:     map[string]string{"region-a": fixtureModel, "region-b": fixtureModel},
		legacy:     fixtureLegacy,
		validating: fixtureValidating,
		reports:    fixtureReports,
		policies: []map[string]any{
			{"name": "probes-present", "weight": 4, "source": "kyverno"},
			{"name": "image-tag-pinned", "weight": 7, "source": "kyverno"},
			{"name": "app-label-mutate", "weight": 0, "source": "kyverno"}, // an engine object without a check
		},
	}
}

func (f *modelFixture) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	if r.ID == "region-b" && f.readErr != nil {
		return nil, f.readErr
	}
	switch args[1] {
	case modelResource:
		m, ok := f.models[r.ID]
		if !ok {
			return nil, errors.New(`Error from server (NotFound): compliancemodels.compliance.openova.io "default" not found`)
		}
		return []byte(m), nil
	case legacyPolicies:
		return []byte(f.legacy), nil
	case validatingPolicies:
		return []byte(f.validating), nil
	case policyReports:
		return []byte(f.reports), nil
	}
	return nil, errors.New("unexpected kubectl " + strings.Join(args, " "))
}

func (f *modelFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/compliance/policies") {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"items": f.policies})
}

// runModelCheck runs the check against the fixture in two regions the way the
// runner does and returns the result.
func runModelCheck(t *testing.T, f *modelFixture) (CheckResult, []*Step) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	cc := &CheckContext{API: api.New(srv.URL, "session"), Deploy: &DeploymentInfo{ID: "dep"},
		Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := complianceModelCheck{kubectl: f.kubectl}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, cc, true)
	return r.run.Checks[0], r.run.Steps
}

func TestComplianceModelCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(f *modelFixture)
		want string // empty: PASS
	}{
		"the model, the engine and the API agree": {},
		"a region without the object": {func(f *modelFixture) { delete(f.models, "region-b") },
			`region-b: compliancemodels.compliance.openova.io default: Error from server (NotFound)`},
		"a model with no kyverno check": {func(f *modelFixture) {
			f.models["region-a"] = `{"spec": {"version": "1.0.0", "checks": [{"id": "x", "source": "evaluator"}]}}`
		}, "nothing was measured: the model of region-a holds no check of source kyverno"},
		"a check whose severity has no weight": {func(f *modelFixture) {
			f.models["region-a"] = strings.Replace(fixtureModel, `"severity": "high"}`, `"severity": "urgent"}`, 1)
		}, `region-a: the check probes-present has no weight and its severity "urgent" has none`},
		"a validating policy without a check": {func(f *modelFixture) {
			f.validating = `{"items": [{"metadata": {"name": "image-tag-pinned"}}, {"metadata": {"name": "new-policy"}}]}`
		}, "region-a: the policy new-policy has no check in the model"},
		"a check without a policy object": {func(f *modelFixture) { f.validating = `{"items": []}` },
			"region-a: the check image-tag-pinned has no validating policy object"},
		"the second region differs": {func(f *modelFixture) {
			f.models["region-b"] = strings.Replace(fixtureModel, `"id": "probes-present"`, `"id": "probes-gone"`, 1)
		}, "region-b: the policy probes-present has no check in the model; region-b: the check probes-gone has no validating policy object"},
		"a region that cannot be read": {func(f *modelFixture) { f.readErr = errors.New("connection refused") }, "connection refused"},
		"the API scores without the model": {func(f *modelFixture) {
			f.policies = []map[string]any{{"name": "probes-present", "weight": 1, "source": "kyverno"}, {"name": "image-tag-pinned", "weight": 1, "source": "kyverno"}}
		}, "image-tag-pinned: the API weighs it 1, the model 7; probes-present: the API weighs it 1, the model 4"},
		"the API does not list a check": {func(f *modelFixture) { f.policies = f.policies[:1] },
			"image-tag-pinned: the API does not list it as a kyverno policy"},
		"the API weighs a policy the model lacks": {func(f *modelFixture) {
			f.policies = append(f.policies, map[string]any{"name": "stray", "weight": 1, "source": "kyverno"})
		}, "stray: the API weighs it 1 and the model has no such check"},
		"no check has a result": {func(f *modelFixture) { f.reports = `{"items": [{"results": [{"policy": "app-label-mutate"}]}]}` },
			"nothing was measured: no kyverno check of the model has a result in the 1 policy reports of region-a"},
	} {
		t.Run(name, func(t *testing.T) {
			f := goodModelFixture()
			if tc.edit != nil {
				tc.edit(f)
			}
			res, _ := runModelCheck(t, f)
			if (tc.want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, tc.want) {
				t.Fatalf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, tc.want)
			}
		})
	}
}

// TestComplianceModelCheckEvidence: a check with no result is listed as no
// subject, and the evidence names the count of results of every check.
func TestComplianceModelCheckEvidence(t *testing.T) {
	res, steps := runModelCheck(t, goodModelFixture())
	var last *Step
	for _, s := range steps {
		if s.ID == complianceModelID+".4" {
			last = s
		}
	}
	if res.Status != Pass || last == nil {
		t.Fatalf("status %s, steps %+v", res.Status, steps)
	}
	b, _ := json.Marshal(last.Evidence)
	for _, want := range []string{`"no_subject":["image-tag-pinned"]`, `"results_per_check":{"image-tag-pinned":0,"probes-present":2}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("evidence %s does not hold %s", b, want)
		}
	}
}

func TestComplianceModelRegistered(t *testing.T) {
	c, ok := registry[complianceModelID].(complianceModelCheck)
	if !ok || c.kubectl == nil {
		t.Fatalf("the registry must hold %s with kube.Region.Run", complianceModelID)
	}
}
