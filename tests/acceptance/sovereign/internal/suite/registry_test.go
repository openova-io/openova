package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCheck is a check whose behaviour the test sets.
type fakeCheck struct {
	id  string
	run func(cc *CheckContext, rec StepRecorder) error
	ran *bool
}

func (f fakeCheck) ID() string { return f.id }

func (f fakeCheck) Run(_ context.Context, cc *CheckContext, rec StepRecorder) error {
	if f.ran != nil {
		*f.ran = true
	}
	return f.run(cc, rec)
}

func stepHolds(_ *CheckContext, rec StepRecorder) error {
	rec.Step("1", "it holds", func(ev Evidence) error { ev["seen"] = 1; return nil })
	return nil
}

func stepBreaks(_ *CheckContext, rec StepRecorder) error {
	rec.Step("1", "it holds", func(Evidence) error { return nil })
	rec.Step("2", "it also holds", func(Evidence) error { return errors.New("it does not") })
	rec.Step("3", "a later step", func(Evidence) error { return errors.New("later") })
	return nil
}

// emptyRegistry gives the test a registry of its own.
func emptyRegistry(t *testing.T) {
	t.Helper()
	saved := registry
	registry = map[string]Check{}
	t.Cleanup(func() { registry = saved })
}

// entry is a manifest entry whose implementation is a Go file of the suite.
func entry(id, state string, runsIn ...string) ManifestCheck {
	return ManifestCheck{ID: id, State: state, RunsIn: runsIn, Needs: []string{},
		Implementation: suiteRoot + "internal/suite/check_fake.go"}
}

func TestRegisterCheck(t *testing.T) {
	emptyRegistry(t)
	RegisterCheck(fakeCheck{id: "FND-G03"})
	RegisterCheck(fakeCheck{id: "FND-G01"})
	got := RegisteredChecks()
	if len(got) != 2 || got[0].ID() != "FND-G01" || got[1].ID() != "FND-G03" {
		t.Fatalf("RegisteredChecks is not sorted by identifier: %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a duplicate identifier did not panic")
		}
	}()
	RegisterCheck(fakeCheck{id: "FND-G03"})
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", `{"schema":"openova.io/gates/v1","pass_rule":{"consecutive":2},"checks":[{"id":"FND-G03","state":"required","runs_in":["local","cloud"],"destructive":false,"needs":["multi-region"],"implementation":"tests/acceptance/sovereign/internal/suite/check_nodes.go"}]}`)
	m, err := LoadManifest(good)
	if err != nil || len(m.Checks) != 1 || m.Checks[0].Needs[0] != "multi-region" || m.Checks[0].RunsIn[1] != "cloud" ||
		m.Checks[0].Implementation != "tests/acceptance/sovereign/internal/suite/check_nodes.go" {
		t.Fatalf("a good manifest was not read: %v %+v", err, m)
	}
	for name, p := range map[string]string{
		"a missing file": filepath.Join(dir, "none.json"),
		"not JSON":       write("bad.json", "schema: x\n"),
		"another schema": write("other.json", `{"schema":"openova.io/gates/v0","checks":[]}`),
	} {
		if _, err := LoadManifest(p); !errors.Is(err, ErrManifest) {
			t.Errorf("%s: want an ErrManifest error, got %v", name, err)
		}
	}
}

func TestSelectChecks(t *testing.T) {
	emptyRegistry(t)
	for _, id := range []string{"FND-G01", "FND-G02", "FND-G04", "FND-G05", "FND-G06"} {
		RegisterCheck(fakeCheck{id: id})
	}
	wipe := entry("FND-G06", StateRequired, "cloud", "self")
	wipe.Destructive = true
	m := &Manifest{Schema: ManifestSchema, Checks: []ManifestCheck{
		entry("FND-G01", StateRequired, "local", "cloud", "self"),
		entry("FND-G02", StateExpectedFail, "cloud"),
		entry("FND-G03", "not-built", "local", "cloud"), // not selected, so it needs no registered check
		entry("FND-G04", StateRequired, "local"),
		entry("FND-G05", "not-built", "cloud"),
		wipe,
	}}
	for runIn, want := range map[RunIn]string{RunInCloud: "FND-G01 FND-G02 FND-G06", RunInLocal: "FND-G01 FND-G04", RunInSelf: "FND-G01"} {
		got, err := SelectChecks(m, runIn)
		ids := []string{}
		for _, c := range got {
			ids = append(ids, c.ID())
		}
		if err != nil || strings.Join(ids, " ") != want {
			t.Errorf("%s: selected %v (error %v), want %s", runIn, ids, err, want)
		}
	}
	m.Checks[2].State = StateExpectedFail
	_, err := SelectChecks(m, RunInCloud)
	if !errors.Is(err, ErrManifest) || !strings.HasSuffix(err.Error(), "no registered check for FND-G03") {
		t.Fatalf("a selected entry without a registered check must be an error, got %v", err)
	}
}

// TestSelectChecksByImplementation: only an entry whose implementation is a Go
// file of the suite is selected. An entry implemented by a script or by a
// folder of browser specs is proven by its own job and is left out, even when
// a check of the same identifier is registered. A Go entry without a
// registered check still stops the run.
func TestSelectChecksByImplementation(t *testing.T) {
	emptyRegistry(t)
	RegisterCheck(fakeCheck{id: "FND-G01"})
	RegisterCheck(fakeCheck{id: "CLD-G03"})
	with := func(id, state, implementation string) ManifestCheck {
		e := entry(id, state, "local", "cloud")
		e.Implementation = implementation
		return e
	}
	m := &Manifest{Schema: ManifestSchema, Checks: []ManifestCheck{
		with("FND-G01", StateRequired, suiteRoot+"internal/suite/check_candidate.go"),
		with("CLD-G02", StateRequired, "scripts/check-provider-leaks.py"),
		with("CLD-G03", StateExpectedFail, "scripts/check-provider-contract.sh"),
		with("FND-G07", StateExpectedFail, "scripts/sso-zero-click-probe.mjs"),
		with("ACC-G03", StateExpectedFail, "products/catalyst/bootstrap/ui/e2e/gate/apps"),
		with("ACC-G05", StateRequired, "core/marketplace/e2e/gate/redeem"),
		with("ACC-G13", StateRequired, ""),
		with("ACC-G14", StateRequired, suiteRoot+"../../../scripts/probe.go"),
	}}
	for _, r := range []RunIn{RunInLocal, RunInCloud} {
		got, err := SelectChecks(m, r)
		if err != nil || len(got) != 1 || got[0].ID() != "FND-G01" {
			t.Errorf("%s: want only FND-G01 selected, got %v (error %v)", r, got, err)
		}
	}
	m.Checks = append(m.Checks, with("ACC-G15", StateExpectedFail, suiteRoot+"internal/suite/check_none.go"))
	for _, r := range []RunIn{RunInLocal, RunInCloud} {
		_, err := SelectChecks(m, r)
		if !errors.Is(err, ErrManifest) || !strings.HasSuffix(err.Error(), "no registered check for ACC-G15") {
			t.Errorf("%s: a Go entry without a registered check must stop the run, got %v", r, err)
		}
	}
}

func TestRunChecksAndResult(t *testing.T) {
	ranSkipped, ranUndiscovered := false, false
	sawPrior := -1
	checks := []Check{
		fakeCheck{id: "FND-G01", run: stepHolds},
		fakeCheck{id: "FND-G02", run: stepBreaks},
		fakeCheck{id: "FND-G03", run: func(*CheckContext, StepRecorder) error { return errors.New("no kubeconfig") }},
		fakeCheck{id: "FND-G04", run: func(*CheckContext, StepRecorder) error { return nil }},
		fakeCheck{id: "FND-G05", run: stepHolds, ran: &ranSkipped},
		fakeCheck{id: "FND-G06", run: func(_ *CheckContext, rec StepRecorder) error {
			rec.Skip("1", "the wipe report", "no wipe before this run")
			return nil
		}},
		fakeCheck{id: "FND-G07", run: func(cc *CheckContext, rec StepRecorder) error {
			sawPrior = len(cc.Prior)
			return stepHolds(cc, rec)
		}},
	}
	m := &Manifest{Checks: []ManifestCheck{
		entry("FND-G01", StateRequired), entry("FND-G02", StateExpectedFail), entry("FND-G03", StateExpectedFail),
		entry("FND-G04", StateExpectedFail), {ID: "FND-G05", State: StateRequired, Needs: []string{"multi-region", "gpu"}},
		entry("FND-G06", StateExpectedFail), entry("FND-G07", StateRequired),
	}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: []*Step{{Journey: "D0", ID: "D0.1", Status: Pass}}}}
	r.runChecks(context.Background(), m, checks, &CheckContext{Caps: map[string]bool{"gpu": true}}, true)
	want := []CheckResult{
		{ID: "FND-G01", StateAtRun: StateRequired, Status: Pass},
		{ID: "FND-G02", StateAtRun: StateExpectedFail, Status: Fail, Reason: "it does not"},
		{ID: "FND-G03", StateAtRun: StateExpectedFail, Status: Fail, Reason: "no kubeconfig"},
		{ID: "FND-G04", StateAtRun: StateExpectedFail, Status: Fail, Reason: "the check recorded no step"},
		{ID: "FND-G05", StateAtRun: StateRequired, Status: Skip, Reason: "the run does not declare multi-region", unmet: true},
		{ID: "FND-G06", StateAtRun: StateExpectedFail, Status: Skip, Reason: "no wipe before this run"},
		{ID: "FND-G07", StateAtRun: StateRequired, Status: Pass},
	}
	if len(r.run.Checks) != len(want) {
		t.Fatalf("got %d results, want %d", len(r.run.Checks), len(want))
	}
	for i, w := range want {
		if r.run.Checks[i] != w {
			t.Errorf("result %d: got %+v, want %+v", i, r.run.Checks[i], w)
		}
	}
	if ranSkipped || sawPrior != 1 {
		t.Errorf("a check with unmet needs ran (%v), or Prior did not hold the 1 earlier step (%d)", ranSkipped, sawPrior)
	}
	if last := r.run.Steps[len(r.run.Steps)-1]; last.Journey != "FND-G07" || last.Evidence["seen"] != 1 {
		t.Errorf("the step of a check must carry the check identifier and its evidence: %+v", last)
	}
	// The result: expected-fail failures and a skip for unmet needs do not fail the run.
	r.run.Finalize(nil)
	if r.run.Result != Pass {
		t.Fatalf("result is %s: only required checks may fail a run", r.run.Result)
	}
	for name, bad := range map[string]CheckResult{
		"a required check that failed":                 {ID: "X", StateAtRun: StateRequired, Status: Fail},
		"a required check skipped although it applied": {ID: "X", StateAtRun: StateRequired, Status: Skip},
	} {
		run := &Run{Mode: "run", Checks: []CheckResult{bad}}
		if run.Finalize(nil); run.Result != Fail {
			t.Errorf("%s must fail the run", name)
		}
	}
	// When discovery failed no check runs, and a required check fails the run.
	r = &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), m, []Check{fakeCheck{id: "FND-G01", run: stepHolds, ran: &ranUndiscovered}}, &CheckContext{}, false)
	r.run.Finalize(nil)
	if got := r.run.Checks[0]; ranUndiscovered || got.Status != Skip || got.Reason != "discovery (D0) failed" || r.run.Result != Fail {
		t.Errorf("after a failed discovery: ran=%v result=%+v run=%s", ranUndiscovered, got, r.run.Result)
	}
}

// TestExecuteStopsOnManifest: the manifest is read and the checks are selected
// before anything runs, so both errors come back before the first request.
func TestExecuteStopsOnManifest(t *testing.T) {
	emptyRegistry(t)
	dir := t.TempDir()
	unregistered := filepath.Join(dir, "gates.json")
	text := `{"schema":"openova.io/gates/v1","checks":[{"id":"FND-G03","state":"required","runs_in":["cloud"],"destructive":false,"needs":[],"implementation":"tests/acceptance/sovereign/internal/suite/check_nodes.go"}]}`
	if err := os.WriteFile(unregistered, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Env: "sample", Console: "http://127.0.0.1:9", Session: "x", KubeconfigA: "a", KubeconfigB: "b", RunIn: "cloud"}
	for name, path := range map[string]string{"a missing manifest": filepath.Join(dir, "none.json"), "an entry without a registered check": unregistered} {
		cfg.Manifest = path
		if run, err := Execute(context.Background(), cfg, io.Discard); run != nil || !errors.Is(err, ErrManifest) {
			t.Errorf("%s: want no run and an ErrManifest error, got %v, %v", name, run, err)
		}
	}
}

func TestProducerFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if p := producerFromEnv(get); p != (Producer{Kind: "manual"}) {
		t.Fatalf("outside a workflow the producer must be manual, got %+v", p)
	}
	env = map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_RUN_ID": "11873344556", "GITHUB_RUN_ATTEMPT": "2",
		"GITHUB_WORKFLOW_REF": "openova-io/openova/.github/workflows/foundation-gate.yaml@refs/heads/main"}
	want := Producer{Kind: "workflow", Workflow: "foundation-gate.yaml", RunID: "11873344556", RunAttempt: 2}
	if p := producerFromEnv(get); p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}
}

// TestSampleRecord writes one version 2 record of a local run. When
// ACCEPT_SAMPLE_RECORD_DIR is set the record stays there, so that
// `scripts/acceptance-gate.py validate-record` can prove that this type and
// the schema of that tool agree.
func TestSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	now := time.Date(2026, 11, 20, 8, 15, 0, 0, time.UTC)
	commit := strings.Repeat("4be7a11c0d", 4)
	run := &Run{Schema: SchemaVersion, RunID: NewRunID(now), Env: "sample", Console: "https://console.sample.omani.works",
		Mode: "run", RunIn: string(RunInLocal), Capabilities: splitList(" multi-region , gpu,"), SuiteGitSHA: commit,
		StartedAt: now, FinishedAt: now.Add(time.Minute), Candidate: Candidate{Commit: commit},
		Producer: producerFromEnv(func(string) string { return "" }), Subject: Evidence{},
		Checks: []CheckResult{{ID: "FND-G03", StateAtRun: StateRequired, Status: Fail, Reason: "5 of 6 nodes are Ready"}},
		Steps:  []*Step{{Journey: "FND-G03", ID: "1", Name: "ready nodes", Status: Fail, StartedAt: now, FinishedAt: now, Error: "5 of 6"}}}
	run.Finalize(nil)
	if _, err := run.Write(dir); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["schema"] != "openova.io/acceptance-sovereign/v2" || got["run_in"] != "local" || got["result"] != Fail ||
		len(got["capabilities"].([]any)) != 2 || got["candidate"].(map[string]any)["id"] != "" ||
		got["producer"].(map[string]any)["kind"] != "manual" || len(got["checks"].([]any)[0].(map[string]any)) != 4 {
		t.Fatalf("the record is not a version 2 record: %s", b)
	}
}
