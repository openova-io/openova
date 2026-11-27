package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// engineRegion is what the fake kubectl of TestPolicyEngine answers in one
// region: the JSON of each question (a resource, or "raw <path>" for kubectl
// get --raw). engineNotServed and engineRefused fail the question.
type engineRegion map[string]string

const (
	engineNotServed, engineRefused = "NOT-SERVED", "REFUSED"
	engineMetricsPath              = "raw /api/v1/namespaces/kyverno/services/kyverno-svc-metrics:metrics-port/proxy/metrics"
	engineReady                    = `{"conditionStatus": {"ready": true}}`
	engineNotReady                 = `{"conditionStatus": {"ready": false, "message": "webhook not configured"}}`
	engineVerifier                 = `{"metadata": {"name": "cosign-verified"}, "spec": {"rules": [{"name": "verify", "verifyImages": [{}]}]}, "status": {"conditions": [{"type": "Ready", "status": "True"}]}}`
	engineInfo                     = "# HELP kyverno_info Kyverno version information\n# TYPE kyverno_info gauge\n" +
		`kyverno_info{otel_scope_name="kyverno",version="v1.19.2"} 1` + "\n" + `kyverno_admission_requests_total{resource_kind="Pod"} 12` + "\n"
	engineVerifierRule = `kyverno_policy_rule_info_total{policy_name="cosign-verified",rule_name="verify",status_ready="true"} 1` + "\n"
	engineOldPolicy    = `{"metadata": {"name": "old-policy"}, "spec": {"rules": [{"name": "r", "validate": {}}]}}`
	engineMetricsSvc   = `{"metadata": {"namespace": "kyverno", "name": "kyverno-svc-metrics"}, "spec": {"ports": [{"name": "metrics-port"}]}}`
)

func engineList(items ...string) string { return `{"items": [` + strings.Join(items, ",") + `]}` }

// engineVP is a new policy object; key is a name or namespace/name.
func engineVP(key, status string) string {
	ns, name, found := strings.Cut(key, "/")
	if !found {
		ns, name = "", key
	}
	return fmt.Sprintf(`{"metadata": {"name": %q, "namespace": %q}, "status": %s}`, name, ns, status)
}

// engineResults is a policy report with one result per policy:result pair.
func engineResults(pairs ...string) string {
	out := []string{}
	for _, p := range pairs {
		policy, result, _ := strings.Cut(p, ":")
		out = append(out, fmt.Sprintf(`{"policy": %q, "result": %q}`, policy, result))
	}
	return engineList(`{"results": [` + strings.Join(out, ",") + `]}`)
}

// engineClean is a region with nothing to refuse: three ValidatingPolicy
// objects and a MutatingPolicy, ready; a model of four kyverno checks, one of
// them (absent) without a policy, and a check of another source; results for
// alpha and beta, none for gamma; the two Services of the admission controller.
func engineClean() engineRegion {
	return engineRegion{
		legacyPolicies: engineList(), legacyNsPolicies: engineList(), validatingNsPolicy: engineList(), mutatingNsPolicy: engineList(),
		validatingPolicies: engineList(engineVP("alpha", engineReady), engineVP("beta", engineReady), engineVP("gamma", engineReady)),
		mutatingPolicies:   engineList(engineVP("stamp", engineReady)),
		engineModel: `{"spec": {"checks": [{"id": "alpha", "source": "kyverno"}, {"id": "beta", "source": "kyverno"},
		  {"id": "gamma", "source": "kyverno"}, {"id": "absent", "source": "kyverno"}, {"id": "judged", "source": "evaluator"}]}}`,
		engineReports:     engineResults("alpha:pass", "alpha:fail", "beta:skip"),
		"services":        engineList(`{"metadata": {"namespace": "kyverno", "name": "kyverno-svc"}, "spec": {"ports": [{"name": "https"}]}}`, engineMetricsSvc),
		engineMetricsPath: engineInfo,
	}
}

// engineRun runs the check on two regions with the answers of world.
func engineRun(world map[string]engineRegion) *Run {
	kubectl := func(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		q := args[1]
		if q == "--raw" && len(args) == 3 {
			q = "raw " + args[2]
		}
		switch answer, ok := world[r.ID][q]; {
		case args[0] != "get" || !ok:
			return nil, fmt.Errorf("no answer for kubectl %q in %s", args, r.ID)
		case answer == engineNotServed:
			return nil, errors.New(`error: the server doesn't have a resource type "x"`)
		case answer == engineRefused:
			return nil, errors.New("connection refused")
		default:
			return []byte(answer), nil
		}
	}
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(policyEngineID, StateRequired)}}, []Check{policyEngineCheck{kubectl: kubectl}}, cc, true)
	return r.run
}

// engineSet sets answers of a region: question, answer, question, answer, ...
func engineSet(pairs ...string) func(engineRegion) {
	return func(r engineRegion) {
		for i := 0; i+1 < len(pairs); i += 2 {
			r[pairs[i]] = pairs[i+1]
		}
	}
}

func TestPolicyEngine(t *testing.T) {
	for name, tc := range map[string]struct {
		region string               // the region the case changes: region-b unless set
		change func(r engineRegion) // the fault of that region; nil changes nothing
		want   string               // the error of a failed step of that region; empty: the check passes
	}{
		"both regions clean":                    {},
		"the image verifier, ready and counted": {change: engineSet(legacyPolicies, engineList(engineVerifier), engineMetricsPath, engineInfo+engineVerifierRule)},
		"a series of value 0 is a deleted rule": {change: engineSet(engineMetricsPath, engineInfo+`kyverno_policy_rule_info_total{policy_name="old-policy"} 0`)},
		"a legacy ClusterPolicy in the second region": {change: engineSet(legacyPolicies, engineList(engineOldPolicy)),
			want: "the ClusterPolicy old-policy is a legacy policy object"},
		"a legacy ClusterPolicy in the first region": {region: "region-a", change: engineSet(legacyPolicies, engineList(engineOldPolicy)),
			want: "the ClusterPolicy old-policy is a legacy policy object"},
		"a legacy Policy in a namespace": {change: engineSet(legacyNsPolicies, engineList(`{"metadata": {"namespace": "org-a", "name": "scope"}}`)),
			want: "the Policy org-a/scope is a legacy policy object"},
		"the image verifier also validates": {change: engineSet(legacyPolicies, engineList(strings.Replace(engineVerifier, `"verifyImages": [{}]`, `"verifyImages": [{}], "validate": {}`, 1))),
			want: "the ClusterPolicy cosign-verified has the rule verify, which does more than verify images"},
		"the image verifier has no rule": {change: engineSet(legacyPolicies, engineList(`{"metadata": {"name": "cosign-verified"}, "spec": {}}`)),
			want: "the ClusterPolicy cosign-verified has no rule"},
		"a ValidatingPolicy is not ready": {change: engineSet(validatingPolicies, engineList(engineVP("alpha", engineReady), engineVP("beta", engineNotReady))),
			want: "validatingpolicies.policies.kyverno.io beta is not ready: webhook not configured"},
		"a MutatingPolicy carries no status": {change: engineSet(mutatingPolicies, engineList(`{"metadata": {"name": "stamp"}}`)),
			want: "mutatingpolicies.policies.kyverno.io stamp carries no status.conditionStatus.ready"},
		"a namespaced ValidatingPolicy is not ready": {change: engineSet(validatingNsPolicy, engineList(engineVP("org-a/local", engineNotReady))),
			want: "namespacedvalidatingpolicies.policies.kyverno.io org-a/local is not ready"},
		"the image verifier is not ready": {change: engineSet(legacyPolicies, engineList(strings.Replace(engineVerifier, `"status": "True"`, `"status": "False", "reason": "Failed"`, 1)),
			engineMetricsPath, engineInfo+engineVerifierRule), want: `clusterpolicies.kyverno.io cosign-verified has Ready "False" (Failed: )`},
		"the image verifier carries no condition Ready": {change: engineSet(legacyPolicies, engineList(strings.Replace(engineVerifier, `"type": "Ready"`, `"type": "Other"`, 1)),
			engineMetricsPath, engineInfo+engineVerifierRule), want: `clusterpolicies.kyverno.io cosign-verified has Ready ""`},
		"no ValidatingPolicy": {change: engineSet(validatingPolicies, engineList()), want: "nothing was measured: the region holds no ValidatingPolicy"},
		"ValidatingPolicy is not served": {change: engineSet(validatingPolicies, engineNotServed),
			want: "the region does not serve validatingpolicies.policies.kyverno.io"},
		"no model": {change: engineSet(engineModel, engineRefused), want: "read compliancemodels.compliance.openova.io default: connection refused"},
		"the model holds no kyverno check": {change: engineSet(engineModel, `{"spec": {"checks": [{"id": "judged", "source": "evaluator"}]}}`),
			want: "nothing was measured: the model holds no check of source kyverno"},
		"every result of a check is error": {change: engineSet(engineReports, engineResults("alpha:pass", "beta:error")),
			want: "the engine evaluated no object for beta (1 results, each error)"},
		"no check has a result": {change: engineSet(engineReports, engineResults("other:pass")),
			want: "nothing was measured: no kyverno check of the model has a result in the 1 policy reports"},
		"the counter names another legacy policy": {change: engineSet(engineMetricsPath, engineInfo+`kyverno_policy_rule_info_total{rule_name="r",policy_name="old-policy"} 1`),
			want: `kyverno_policy_rule_info_total counts loaded rules of the legacy policies "old-policy" (1 rules)`},
		"the metrics hold no kyverno_info": {change: engineSet(engineMetricsPath, "# HELP kyverno_info Kyverno version information\n"),
			want: "nothing was measured: the metrics of kyverno/kyverno-svc-metrics hold no sample of kyverno_info"},
		"the verifier exists and the counter does not see it": {change: engineSet(legacyPolicies, engineList(engineVerifier)),
			want: "the image verifier cosign-verified exists and kyverno_policy_rule_info_total counts no rule of it"},
		"no metrics Service": {change: engineSet("services", engineList()),
			want: "the region has 0 Services app.kubernetes.io/name=kyverno-admission-controller with a port metrics-port"},
		"two metrics Services": {change: engineSet("services", engineList(engineMetricsSvc, strings.Replace(engineMetricsSvc, `"kyverno"`, `"other"`, 1))),
			want: "the region has 2 Services app.kubernetes.io/name=kyverno-admission-controller with a port metrics-port (kyverno/kyverno-svc-metrics, other/kyverno-svc-metrics); want one"},
		"a series of the counter without policy_name": {change: engineSet(engineMetricsPath, engineInfo+`kyverno_policy_rule_info_total{rule_name="r"} 1`),
			want: "line 5 of the metrics cannot be read"},
		"a series of the counter without a value": {change: engineSet(engineMetricsPath, engineInfo+`kyverno_policy_rule_info_total{policy_name="old-policy"} one`),
			want: "line 5 of the metrics cannot be read"},
		"the region cannot be read": {change: engineSet(legacyPolicies, engineRefused), want: "connection refused"},
	} {
		t.Run(name, func(t *testing.T) {
			world := map[string]engineRegion{"region-a": engineClean(), "region-b": engineClean()}
			faulty := tc.region
			if faulty == "" {
				faulty = "region-b"
			}
			if tc.change != nil {
				tc.change(world[faulty])
			}
			run := engineRun(world)
			if len(run.Steps) != 8 {
				t.Fatalf("%d steps; want four in each of the two regions", len(run.Steps))
			}
			failed, held := []string{}, false
			for _, s := range run.Steps {
				if s.Status != Fail {
					continue
				}
				failed = append(failed, s.Name+": "+s.Error)
				if !strings.HasSuffix(s.Name, " region "+faulty) && !strings.Contains(s.Name, " region "+faulty+" ") {
					t.Errorf("a step of the region the case leaves clean failed: %s: %s", s.Name, s.Error)
				}
				held = held || strings.Contains(s.Error, tc.want)
			}
			switch res := run.Checks[0]; {
			case tc.want == "" && (res.Status != Pass || len(failed) > 0):
				t.Fatalf("status %s, failed steps %q; want PASS", res.Status, failed)
			case tc.want != "" && (res.Status != Fail || !held):
				t.Fatalf("status %s, failed steps %q; want a failed step of %s that holds %q", res.Status, failed, faulty, tc.want)
			}
		})
	}
}

func TestPolicyEngineListsChecksWithoutResult(t *testing.T) {
	run := engineRun(map[string]engineRegion{"region-a": engineClean(), "region-b": engineClean()})
	for _, i := range []int{2, 6} { // step 3 of each region
		s := run.Steps[i]
		got := []any{s.Status, s.Evidence["no_subject"], s.Evidence["not_installed"], s.Evidence["results_per_check"]}
		want := []any{Pass, []string{"gamma"}, []string{"absent"}, map[string]int{"alpha": 2, "beta": 1}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("step %s: status, no subject, not installed and results %v; want %v", s.ID, got, want)
		}
	}
}

func TestPolicyEngineLegacyRules(t *testing.T) {
	rules, info, err := legacyRules(`kyverno_policy_rule_info_total{policy_name="a\"b",rule_name="r"} 1 1700000000` + "\nkyverno_info 1\nkyverno_policy_rule_info_totals 7\n")
	if want := map[string]int{`a"b`: 1}; err != nil || info != 1 || !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules %v, info %d, error %v; want %v, 1 and none", rules, info, err, want)
	}
}

func TestPolicyEngineRegistered(t *testing.T) {
	if c, ok := registry[policyEngineID].(policyEngineCheck); !ok || c.kubectl == nil {
		t.Fatalf("the registry must hold %s with kube.Region.Run", policyEngineID)
	}
}
