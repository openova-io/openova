package suite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// proofsHR is a HelmRelease of a fixture: settled, with spec.test.enable true
// and one revision whose test hooks have the given phases. Ready is False and
// status.failures is 1, which the check must not read.
func proofsHR(namespace, name, chart string, hooks map[string]string) obj {
	testHooks := obj{}
	for hook, phase := range hooks {
		testHooks[hook] = obj{"phase": phase}
	}
	return obj{
		"metadata": obj{"namespace": namespace, "name": name, "generation": 2},
		"spec":     obj{"test": obj{"enable": true, "ignoreFailures": true}, "chart": obj{"spec": obj{"chart": chart}}},
		"status": obj{"observedGeneration": 2, "failures": 1, "conditions": kubeConditions("Ready", "False"),
			"history": []obj{{"chartName": chart, "status": "deployed", "testHooks": testHooks}}},
	}
}

// proofsWith returns a copy of o with change applied to it.
func proofsWith(o obj, change func(o obj)) obj {
	b, _ := json.Marshal(o)
	out := obj{}
	_ = json.Unmarshal(b, &out)
	change(out)
	return out
}

func proofsStatus(o obj) obj { return o["status"].(obj) }

var (
	proofsCilium  = proofsHR("flux-system", "bp-cilium", "bp-cilium", map[string]string{"bp-cilium-function-test": "Succeeded"})
	proofsSealed  = proofsWith(proofsHR("flux-system", "bp-sealed-secrets", "bp-sealed-secrets", nil), proofsNoHooks("True", "no test hooks"))
	proofsShared  = proofsHR("flux-system", "bp-postgres-shared", "bp-postgres", map[string]string{"bp-postgres-shared-function-test": "Succeeded"})
	proofsOtelOp  = proofsHR("flux-system", "bp-opentelemetry-operator", "bp-opentelemetry-operator", map[string]string{"t": "Failed"})
	proofsShop    = proofsWith(proofsHR("acme", "shop", "bp-shop", nil), func(o obj) { delete(o, "spec") })
	proofsCilFail = proofsHR("flux-system", "bp-cilium", "bp-cilium", map[string]string{"bp-cilium-function-test": "Failed"})
)

// proofsNoHooks empties testHooks and sets the condition TestSuccess.
func proofsNoHooks(status, message string) func(o obj) {
	return func(o obj) {
		s := proofsStatus(o)
		s["history"] = []any{obj{"chartName": "x", "status": "deployed", "testHooks": obj{}}}
		s["conditions"] = []any{obj{"type": "TestSuccess", "status": status, "reason": "Fixture", "message": message}}
	}
}

// proofsSlots are the slot files of the fixture candidate. Slot 19c is not of
// the foundation ring although slot 19 is. Slot 01 also holds a HelmRepository
// that has the name of the release of slot 19c: only a HelmRelease counts.
var proofsSlots = map[string]string{
	"01-cilium.yaml":                  "apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: bp-opentelemetry-operator\n  namespace: flux-system\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-cilium\n  namespace: flux-system\n",
	"05-sealed-secrets.yaml":          "kind: HelmRelease\nmetadata:\n  name: bp-sealed-secrets\n  namespace: flux-system\n",
	"16a-bp-postgres-shared.yaml":     "kind: HelmRelease\nmetadata:\n  name: bp-postgres-shared\n  namespace: flux-system\n",
	"19c-opentelemetry-operator.yaml": "kind: HelmRelease\nmetadata:\n  name: bp-opentelemetry-operator\n  namespace: flux-system\n",
	"kustomization.yaml":              "resources: []\n",
}

func init() {
	hr := " " + hrResource
	base := kubeList(proofsCilium, proofsSealed, proofsShared, proofsOtelOp, proofsShop)
	with := func(extra ...obj) string {
		return kubeList(append([]obj{proofsSealed, proofsShared, proofsOtelOp, proofsShop}, extra...)...)
	}
	fakeAnswers["proofs-pass"] = map[string]string{"*" + hr: base}
	fakeAnswers["proofs-cilium-failed"] = map[string]string{"a" + hr: base, "b" + hr: with(proofsCilFail)}
	fakeAnswers["proofs-cilium-awaiting"] = map[string]string{"a" + hr: base,
		"b" + hr: with(proofsWith(proofsCilium, proofsNoHooks("Unknown", "")))}
	fakeAnswers["proofs-cilium-other-ring"] = map[string]string{"a" + hr: base,
		"b" + hr: with(proofsWith(proofsCilFail, func(o obj) { o["metadata"].(obj)["labels"] = obj{ringLabel: "provider"} }))}
	fakeAnswers["proofs-labelled-foundation"] = map[string]string{"a" + hr: base, "b" + hr: kubeList(proofsCilium, proofsSealed, proofsShared, proofsShop,
		proofsWith(proofsOtelOp, func(o obj) { o["metadata"].(obj)["labels"] = obj{ringLabel: "foundation"} }))}
	fakeAnswers["proofs-sealed-failed"] = map[string]string{"a" + hr: base, "b" + hr: kubeList(proofsCilium, proofsShared, proofsOtelOp, proofsShop,
		proofsHR("flux-system", "bp-sealed-secrets", "bp-sealed-secrets", map[string]string{"t": "Failed"}))}
	fakeAnswers["proofs-no-foundation"] = map[string]string{"a" + hr: base, "b" + hr: kubeList(proofsOtelOp, proofsShop)}
	fakeAnswers["proofs-region-b-unreachable"] = map[string]string{"a" + hr: base}
}

// proofsRun runs check SVC-G06 against a scenario with the fixture candidate.
func proofsRun(t *testing.T, scenario string, before ...*Step) *runner {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(pinsDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, text := range proofsSlots {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runCheck(t, scenario, proofsCheck{}, &CheckContext{RepoRoot: root}, before...)
}

func TestProofsCheck(t *testing.T) {
	if _, ok := registry[proofsID]; !ok || proofsID != "SVC-G06" {
		t.Fatalf("check %s is not registered", proofsID)
	}
	r := proofsRun(t, "proofs-pass")
	if got := r.run.Checks[0]; got.Status != Pass || len(r.run.Steps) != 3 {
		t.Fatalf("want PASS in 3 steps, got %+v in %d steps: %v", got, len(r.run.Steps), failedSteps(r))
	}
	ev := r.run.Steps[1].Evidence
	if ev["foundation"] != 3 || ev["judged"] != 2 || len(ev["exempt"].([]string)) != 1 {
		t.Errorf("three foundation releases, one exempt: %+v", ev)
	}
	if want := map[string]int{"pass": 2, "fail": 1, "none": 2}; !proofsJSONEqual(ev["results"], want) {
		t.Errorf("results: got %v, want %v", ev["results"], want)
	}
	for name, c := range map[string]struct{ scenario, want string }{
		"a foundation test failed":           {"proofs-cilium-failed", "1 of 2 foundation HelmReleases have no passing function test: flux-system/bp-cilium (bp-cilium) fail: test hook bp-cilium-function-test failed"},
		"a foundation test has not finished": {"proofs-cilium-awaiting", "flux-system/bp-cilium (bp-cilium) pending: no test hook is recorded and TestSuccess is Unknown (Fixture)"},
		"a ring label that says foundation":  {"proofs-labelled-foundation", "flux-system/bp-opentelemetry-operator (bp-opentelemetry-operator) fail"},
		"no foundation release":              {"proofs-no-foundation", "no HelmRelease of the foundation ring was found: nothing was measured"},
		"a region that cannot be reached":    {"proofs-region-b-unreachable", "[region-b] kubectl get " + hrResource},
	} {
		got := proofsRun(t, c.scenario).run.Checks[0]
		if got.Status != Fail || !strings.Contains(got.Reason, c.want) {
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	for name, scenario := range map[string]string{
		"a ring label that is not foundation": "proofs-cilium-other-ring",
		"an exempt chart whose test failed":   "proofs-sealed-failed",
	} {
		if got := proofsRun(t, scenario).run.Checks[0]; got.Status != Pass {
			t.Errorf("%s is not judged: want PASS, got %+v", name, got)
		}
	}
	r = runCheck(t, "proofs-pass", proofsCheck{}, &CheckContext{RepoRoot: t.TempDir()})
	if got := failedSteps(r); len(got) != 3 || !strings.Contains(got[0], "no HelmRelease was found in a foundation slot file") || got[1] != "the foundation HelmReleases were not read" {
		t.Errorf("without slot files: got %v", got)
	}
}

func TestProofsResult(t *testing.T) {
	cilium := func(change func(o obj)) kube.Object {
		var o kube.Object
		b, _ := json.Marshal(proofsWith(proofsCilium, change))
		_ = json.Unmarshal(b, &o)
		return o
	}
	history := func(phases ...map[string]string) func(o obj) {
		return func(o obj) {
			list := []any{}
			for _, p := range phases {
				hooks := obj{}
				for k, v := range p {
					hooks[k] = obj{"phase": v}
				}
				list = append(list, obj{"chartName": "bp-cilium", "testHooks": hooks})
			}
			proofsStatus(o)["history"] = list
		}
	}
	for name, c := range map[string]struct {
		change      func(o obj)
		result, why string
	}{
		"every hook succeeded; Ready and failures are not read": {func(obj) {}, proofPass, ""},
		"a hook failed":                     {history(map[string]string{"a": "Succeeded", "b": "Failed"}), proofFail, "test hook b failed"},
		"a hook still runs":                 {history(map[string]string{"a": "Succeeded", "b": "Running"}), proofPending, "test hook b has phase Running"},
		"the newest revision counts":        {history(map[string]string{"a": "Succeeded"}, map[string]string{"a": "Failed"}), proofPass, ""},
		"the newest revision failed":        {history(map[string]string{"a": "Failed"}, map[string]string{"a": "Succeeded"}), proofFail, "test hook a failed"},
		"no revision":                       {func(o obj) { delete(proofsStatus(o), "history") }, proofPending, "status.history holds no revision"},
		"the chart has no test hook":        {proofsNoHooks("True", "no test hooks"), proofNone, "the chart has no test hook"},
		"awaiting tests is not none":        {proofsNoHooks("Unknown", ""), proofPending, "no test hook is recorded and TestSuccess is Unknown (Fixture)"},
		"TestSuccess False and no hook yet": {proofsNoHooks("False", "pod failed"), proofPending, "no test hook is recorded and TestSuccess is False (Fixture)"},
		"no hook and no TestSuccess": {func(o obj) {
			proofsNoHooks("True", "")(o)
			proofsStatus(o)["conditions"] = []any{}
		}, proofPending, "no test hook is recorded and there is no TestSuccess condition"},
		"test not enabled":  {func(o obj) { delete(o["spec"].(obj), "test") }, proofNone, "spec.test.enable is not true"},
		"not settled":       {func(o obj) { o["metadata"].(obj)["generation"] = 3 }, proofPending, "status.observedGeneration 2 is not metadata.generation 3"},
		"still reconciling": {func(o obj) { proofsStatus(o)["conditions"] = kubeConditions("Reconciling", "True") }, proofPending, "the release has a Reconciling condition"},
	} {
		if result, why := proofOf(cilium(c.change)); result != c.result || why != c.why {
			t.Errorf("%s: got %s %q, want %s %q", name, result, why, c.result, c.why)
		}
	}
	if got := proofsBlueprint(cilium(func(o obj) { delete(o["spec"].(obj), "chart") })); got != "bp-cilium" {
		t.Errorf("without spec.chart the Blueprint is the chart of the newest revision: got %q", got)
	}
}

// proofsArtifacts is the artifacts step of check FND-G01 for the fixture: the
// release of region-b is pending in scenario proofs-cilium-awaiting.
func proofsArtifacts() *Step {
	art := &Artifacts{Images: []ImageArtifact{{Region: "region-a", Ref: "r", Digest: "sha256:" + strings.Repeat("a", 64), Containers: 1}}}
	for _, region := range []string{"region-a", "region-b"} {
		art.Charts = append(art.Charts,
			ChartArtifact{Region: region, Namespace: "acme", Release: "shop", Chart: "bp-shop", Version: "3.0.0"},
			ChartArtifact{Region: region, Namespace: "flux-system", Release: "bp-cilium", Chart: "bp-cilium", Version: "1.4.20", Pinned: "1.4.20"})
	}
	return &Step{Journey: candidateID, ID: "FND-G01.8", Status: Pass, Evidence: Evidence{ArtifactsKey: art}}
}

func TestProofsFinalize(t *testing.T) {
	r := proofsRun(t, "proofs-cilium-awaiting", proofsArtifacts())
	r.run.Finalize(nil)
	b, err := json.Marshal(r.run.Artifacts.Charts)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"region":"region-a","namespace":"acme","release":"shop","chart":"bp-shop","version":"3.0.0","pinned":"","blueprint":"bp-shop","functionTest":"none"},` +
		`{"region":"region-a","namespace":"flux-system","release":"bp-cilium","chart":"bp-cilium","version":"1.4.20","pinned":"1.4.20","blueprint":"bp-cilium","functionTest":"pass"},` +
		`{"region":"region-b","namespace":"acme","release":"shop","chart":"bp-shop","version":"3.0.0","pinned":"","blueprint":"bp-shop","functionTest":"none"},` +
		`{"region":"region-b","namespace":"flux-system","release":"bp-cilium","chart":"bp-cilium","version":"1.4.20","pinned":"1.4.20"}]`
	if string(b) != want {
		t.Errorf("charts:\n got %s\nwant %s", b, want)
	}
	if _, kept := r.run.Steps[2].Evidence[ProofsKey].(map[string]ChartProof); !kept {
		t.Error("the results stay in the evidence of their step")
	}
}

// TestProofsSampleRecord writes one version 2 record whose charts carry the
// results of check SVC-G06. When ACCEPT_SAMPLE_RECORD_DIR is set the record
// stays there, so that `scripts/acceptance-gate.py validate-record` can prove
// that the record and the schema of that tool agree.
func TestProofsSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	now := time.Date(2026, 11, 20, 8, 15, 0, 0, time.UTC)
	run := proofsRun(t, "proofs-pass", proofsArtifacts()).run
	run.Schema, run.RunID, run.Env, run.Console = SchemaVersion, NewRunID(now), "proofs", "https://console.sample.omani.works"
	run.RunIn, run.Capabilities, run.SuiteGitSHA = string(RunInLocal), []string{}, candidateCommit
	run.StartedAt, run.FinishedAt = now, now.Add(time.Minute)
	run.Candidate, run.Producer, run.Subject = Candidate{Commit: candidateCommit}, Producer{Kind: "manual"}, Evidence{}
	run.Finalize(nil)
	if run.Result != Pass || run.Artifacts.Charts[3].FunctionTest != proofPass {
		t.Fatalf("the record does not hold the results: result %s, charts %+v", run.Result, run.Artifacts.Charts)
	}
	if _, err := run.Write(dir); err != nil {
		t.Fatal(err)
	}
}

// proofsJSONEqual compares two values by their JSON form.
func proofsJSONEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
