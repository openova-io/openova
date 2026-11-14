package suite

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures of what step D0.11 reads. fakeNode is in check_nodes_test.go.

func fakeVariables(provider, regionKey string) obj {
	return obj{"metadata": kubeMeta("flux-system", "sovereign-variables"),
		"data": obj{"PROVIDER": provider, "SOVEREIGN_REGION_KEY": regionKey, "CILIUM_LB_IPAM_CIDRS": "192.0.2.10/32"}}
}

func fakeBootstrapKit(regionKey string) obj {
	return obj{"metadata": kubeMeta("flux-system", "bootstrap-kit"), "spec": obj{"postBuild": obj{"substitute": obj{
		"PROVIDER": "huawei", "SOVEREIGN_REGION_KEY": regionKey, "CILIUM_LB_IPAM_CIDRS": "192.0.2.20/32"}}}}
}

func fakeKubeSystem(created string) string {
	meta := kubeMeta("", "kube-system")
	meta["creationTimestamp"] = created
	return kubeList(obj{"metadata": meta})
}

func fakeHandover(data obj) string {
	return kubeList(obj{"metadata": kubeMeta("catalyst", "self-sovereign-cutover-status"), "data": data})
}

func init() {
	// Installed since SOV-130: the bootstrap variables are a ConfigMap.
	installed := map[string]string{
		"a configmaps flux-system":   kubeList(fakeVariables("huawei", "region-a")),
		"b configmaps flux-system":   kubeList(fakeVariables("huawei", "region-b")),
		"a namespaces":               fakeKubeSystem("2026-11-20T06:22:00Z"),
		"b namespaces":               fakeKubeSystem("2026-11-20T06:20:00Z"),
		"a nodes":                    kubeList(fakeNode("a-1", "True"), fakeNode("a-2", "True"), fakeNode("a-3", "True")),
		"b nodes":                    kubeList(fakeNode("b-1", "True"), fakeNode("b-2", "False")),
		"a configmaps catalyst":      fakeHandover(obj{"cutoverComplete": "true", "cutoverFinishedAt": "2026-11-20T08:05:00Z"}),
		"* " + kustomizationResource: kubeList(),
	}
	with := func(changes map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range installed {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
			if v == "" {
				delete(out, k) // nothing answers this question
			}
		}
		return out
	}
	fakeAnswers["provision-installed"] = installed
	// Standing since September, before SOV-130: the variables are substitutes of bootstrap-kit.
	fakeAnswers["provision-standing"] = with(map[string]string{
		"a configmaps flux-system": kubeList(), "b configmaps flux-system": kubeList(), "* " + kustomizationResource: "",
		"a " + kustomizationResource: kubeList(fakeBootstrapKit("region-a")),
		"b " + kustomizationResource: kubeList(fakeBootstrapKit("region-b")),
		"a namespaces":               fakeKubeSystem("2026-09-02T10:00:00Z"),
		"b namespaces":               fakeKubeSystem("2026-09-02T10:04:00Z"),
		"a configmaps catalyst":      fakeHandover(obj{"cutoverComplete": "true", "cutoverFinishedAt": "2026-09-02T13:00:00Z"}),
	})
	fakeAnswers["provision-chain-failed"] = with(map[string]string{"a configmaps catalyst": fakeHandover(obj{
		"cutoverComplete": "false", "failedStep": "harbor-prewarm", "step.harbor-prewarm.finishedAt": "2026-11-20T07:40:00Z"})})
	fakeAnswers["provision-chain-running"] = with(map[string]string{"a configmaps catalyst": fakeHandover(obj{
		"cutoverComplete": "false", "failedStep": "", "currentStep": "gitea-mirror"})})
	fakeAnswers["provision-chain-absent"] = with(map[string]string{"a configmaps catalyst": kubeList()})
	fakeAnswers["provision-chain-no-time"] = with(map[string]string{"a configmaps catalyst": fakeHandover(obj{"cutoverComplete": "true"})})
	fakeAnswers["provision-two-providers"] = with(map[string]string{"b configmaps flux-system": kubeList(fakeVariables("hetzner", "region-b"))})
	fakeAnswers["provision-no-provider"] = with(map[string]string{"b configmaps flux-system": kubeList(fakeVariables("", ""))})
	fakeAnswers["provision-no-variables"] = with(map[string]string{"b configmaps flux-system": kubeList()})
	fakeAnswers["provision-bad-birth"] = with(map[string]string{"b namespaces": fakeKubeSystem("yesterday")})
	fakeAnswers["provision-region-b-unreachable"] = with(map[string]string{"b configmaps flux-system": "", "b namespaces": "", "b nodes": ""})
}

const freshCandidate = "20261120T060000Z-4be7a11" // cut at 06:00; the installation was created at 06:20

// measureScenario runs check FND-G02 with its first look in scenario start and the
// check in scenario end (no step of it when both are empty), then measures
// the provision in scenario. It returns the installation and step D0.11.
func measureScenario(t *testing.T, scenario, start, end, candidate string) (*Installation, *Step) {
	t.Helper()
	var prior []*Step
	if start != "" || end != "" {
		prior = zeroTouchRun(t, start, end).run.Steps
	}
	t.Setenv(fakeKubectlEnv, scenario)
	regions := fakeRegions(t)
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: prior, Candidate: Candidate{ID: candidate}}, A: regions[0], B: regions[1]}
	r.measureProvision(context.Background(), map[string]int{"region-a": 3, "region-b": 2})
	last := r.run.Steps[len(r.run.Steps)-1]
	if last.Journey != "D0" || last.ID != provisionStepID {
		t.Fatalf("the last step is %s %s, not D0 %s", last.Journey, last.ID, provisionStepID)
	}
	return r.run.Provision.Installation, last
}

func rfc3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestProvisionFreshAndStanding(t *testing.T) {
	inst, step := measureScenario(t, "provision-installed", "zero-touch-clean", "zero-touch-clean", freshCandidate)
	want := Installation{Provider: "huawei", CreatedAt: rfc3339("2026-11-20T06:20:00Z"), ReadyAt: rfc3339("2026-11-20T08:05:00Z"), Fresh: true,
		Regions: []ProvisionRegion{{ID: "region-a", NodesRequested: 3, NodesReady: 3}, {ID: "region-b", NodesRequested: 2, NodesReady: 1}}}
	if step.Status != Pass || inst.Provider != want.Provider || !inst.CreatedAt.Equal(want.CreatedAt) || !inst.ReadyAt.Equal(want.ReadyAt) ||
		!inst.Fresh || len(inst.Regions) != 2 || inst.Regions[0] != want.Regions[0] || inst.Regions[1] != want.Regions[1] {
		t.Fatalf("a fresh installation: got %s %q %+v, want %+v", step.Status, step.Error, *inst, want)
	}
	// Born in September and updated in place to a candidate cut in November.
	inst, step = measureScenario(t, "provision-standing", "zero-touch-clean", "zero-touch-clean", freshCandidate)
	if step.Status != Pass || inst.Provider != "huawei" || !inst.CreatedAt.Equal(rfc3339("2026-09-02T10:00:00Z")) ||
		!inst.ReadyAt.Equal(rfc3339("2026-09-02T13:00:00Z")) || inst.Fresh ||
		!strings.Contains(step.Evidence["fresh_reason"].(string), "before candidate "+freshCandidate+" was cut") ||
		step.Evidence["variables"].([]string)[1] != "Kustomization flux-system/bootstrap-kit spec.postBuild.substitute" {
		t.Fatalf("a standing environment: got %s %q %+v %v", step.Status, step.Error, *inst, step.Evidence)
	}
}

func TestProvisionFreshOnlyWhenEveryConditionHolds(t *testing.T) {
	for name, c := range map[string]struct{ start, end, candidate, why string }{
		"no candidate":                     {"zero-touch-clean", "zero-touch-clean", "", "the run names no candidate"},
		"a candidate id without a time":    {"zero-touch-clean", "zero-touch-clean", "x-4be7a11", "does not start with the time it was cut"},
		"cut after the installation":       {"zero-touch-clean", "zero-touch-clean", "20261120T070000Z-4be7a11", "so it was updated in place"},
		"check FND-G02 did not run":        {"", "", freshCandidate, "no passing look at region region-a at the start"},
		"a change by hand at the end":      {"zero-touch-clean", "zero-touch-restarted", freshCandidate, "check FND-G02 failed in step"},
		"a change by hand at the start":    {"zero-touch-restarted", "zero-touch-clean", freshCandidate, "check FND-G02 failed in step"},
		"the first look could not measure": {"zero-touch-region-b-unreachable", "zero-touch-clean", freshCandidate, "check FND-G02 failed in step"},
	} {
		inst, step := measureScenario(t, "provision-installed", c.start, c.end, c.candidate)
		if inst.Fresh || step.Status != Pass || !strings.Contains(step.Evidence["fresh_reason"].(string), c.why) {
			t.Errorf("%s: want not fresh because %q, got fresh=%v %s %v", name, c.why, inst.Fresh, step.Status, step.Evidence["fresh_reason"])
		}
	}
	// A look at only one end of the run is not enough.
	onlyEnd := []*Step{{Journey: zeroTouchID, ID: "FND-G02.2", Status: Pass, Evidence: Evidence{"region": "region-a"}}}
	onlyStart := []*Step{{Journey: zeroTouchID, ID: "FND-G02.start.1", Status: Pass, Evidence: Evidence{"region": "region-a"}},
		{Journey: zeroTouchID, ID: "FND-G02.1", Status: Pass, Evidence: Evidence{"region": "region-a", "start_step": "FND-G02.start.1"}}}
	for want, steps := range map[string][]*Step{"at the start": onlyEnd, "at the end": onlyStart} {
		if held, why := zeroTouchHeld(steps, fakeRegions(t)); held || !strings.Contains(why, "region region-a "+want) {
			t.Errorf("no look %s: held=%v %q", want, held, why)
		}
	}
}

func TestProvisionValuesThatCannotBeRead(t *testing.T) {
	for scenario, want := range map[string]string{
		"provision-chain-failed":         "",
		"provision-chain-running":        `ready_at: [region-a] the hand-over chain has not ended (current step: "gitea-mirror")`,
		"provision-chain-absent":         "ready_at: [region-a] the ConfigMap catalyst/self-sovereign-cutover-status does not exist",
		"provision-chain-no-time":        `ready_at: [region-a] cutoverFinishedAt of the ConfigMap catalyst/self-sovereign-cutover-status is not a time: ""`,
		"provision-two-providers":        "provider: region region-b reports hetzner and region region-a reports huawei",
		"provision-no-provider":          "provider: the bootstrap variables of region region-b (ConfigMap flux-system/sovereign-variables) have no PROVIDER",
		"provision-no-variables":         "provider: [region-b] neither the ConfigMap flux-system/sovereign-variables nor the Kustomization flux-system/bootstrap-kit exists",
		"provision-bad-birth":            `created_at: [region-b] the creation time of the namespace kube-system is not a time: "yesterday"`,
		"provision-region-b-unreachable": "created_at: [region-b] kubectl get namespaces",
	} {
		inst, step := measureScenario(t, scenario, "zero-touch-clean", "zero-touch-clean", freshCandidate)
		if want == "" { // a failed chain ended at its failed step
			if step.Status != Pass || !inst.ReadyAt.Equal(rfc3339("2026-11-20T07:40:00Z")) {
				t.Errorf("%s: got %s %q %+v", scenario, step.Status, step.Error, *inst)
			}
			continue
		}
		if step.Status != Fail || !strings.HasPrefix(step.Error, "not read: ") || !strings.Contains(step.Error, want) {
			t.Errorf("%s: want FAIL naming %q, got %s %q", scenario, want, step.Status, step.Error)
		}
		unread := strings.HasPrefix(want, "created_at") == inst.CreatedAt.IsZero() && strings.HasPrefix(want, "ready_at") == inst.ReadyAt.IsZero()
		if !unread || inst.Provider != "huawei" || len(inst.Regions) != 2 {
			t.Errorf("%s: a value that was read is missing, or one that was not read is set: %+v", scenario, *inst)
		}
		if why := step.Evidence["fresh_reason"].(string); inst.CreatedAt.IsZero() && (inst.Fresh || !strings.Contains(why, "creation time of the environment was not read")) {
			t.Errorf("%s: an installation whose creation was not read: fresh=%v %q", scenario, inst.Fresh, why)
		}
	}
}

func TestRegionIdentityReadsBothPlaces(t *testing.T) {
	for scenario, want := range map[string]string{"provision-installed": "192.0.2.10", "provision-standing": "192.0.2.20",
		"provision-no-provider": "have no SOVEREIGN_REGION_KEY", "provision-no-variables": "neither the ConfigMap"} {
		t.Setenv(fakeKubectlEnv, scenario)
		key, ips, err := regionIdentity(context.Background(), fakeRegions(t)[1])
		switch {
		case err != nil && !strings.Contains(err.Error(), want):
			t.Errorf("%s: want an error naming %q, got %v", scenario, want, err)
		case err == nil && (key != "region-b" || strings.Join(ips, ",") != want):
			t.Errorf("%s: got %q %v, want region-b [%s]", scenario, key, ips, want)
		}
	}
}

// TestExecuteMeasuresProvisionInTheCloud runs the suite with the fake as the
// kubectl of the PATH. The console is a closed port of this machine, so the
// discovery fails at once and no request leaves the machine.
func TestExecuteMeasuresProvisionInTheCloud(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeKubectlEnv, "provision-standing")
	cfg := Config{Env: "standing", Console: "http://127.0.0.1:9", Session: "x", KubeconfigA: "a", KubeconfigB: "b",
		RunIn: string(RunInCloud), RepoRoot: ".", Record: true, RecordDir: t.TempDir()}
	run, err := Execute(context.Background(), cfg, io.Discard)
	if err != nil || run.Provision == nil || run.Provision.Installation == nil || run.Provision.PreviousWipe != nil ||
		run.Provision.Provider != "huawei" || run.Provision.Fresh || run.Result != Fail {
		t.Fatalf("a cloud run: want the installation only, in a failed run, got %+v %v", run, err)
	}
	// A local run measures nothing: TestWipeSampleRecord finds only the two values of the wipe there.
	if last := run.Steps[len(run.Steps)-1]; last.ID != provisionStepID || last.Status != Pass {
		t.Fatalf("want step %s last and passed, got %s %s", provisionStepID, last.ID, last.Status)
	}
}

// TestCloudSampleRecord writes two records of cloud runs: a fresh installation
// after a wipe (fresh-<run_id>.json) and a standing environment
// (standing-<run_id>.json). When ACCEPT_SAMPLE_RECORD_DIR is set they stay
// there, so that `scripts/acceptance-gate.py validate-record` can prove that
// the object provision and the schema of that tool agree.
func TestCloudSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	commit := strings.Repeat("4be7a11c0d", 4)
	for env, keys := range map[string]int{"fresh": 7, "standing": 5} {
		zero := zeroTouchRun(t, "zero-touch-clean", "zero-touch-clean")
		t.Setenv(fakeKubectlEnv, map[string]string{"fresh": "provision-installed", "standing": "provision-standing"}[env])
		regions, now := fakeRegions(t), rfc3339("2026-11-20T08:15:00Z")
		r := &runner{out: io.Discard, A: regions[0], B: regions[1], run: &Run{Schema: SchemaVersion, RunID: NewRunID(now),
			Env: env, Console: "https://console." + env + ".omani.works", DeploymentID: "deployment-" + env, Mode: "run",
			RunIn: string(RunInCloud), Capabilities: []string{"multi-region"}, SuiteGitSHA: commit, StartedAt: now,
			FinishedAt: now.Add(time.Minute), Candidate: Candidate{ID: freshCandidate, Commit: commit},
			Producer: producerFromEnv(func(string) string { return "" }), Subject: Evidence{}, Checks: zero.run.Checks, Steps: zero.run.Steps}}
		if env == "fresh" {
			r.run.Provision = &Provision{PreviousWipe: &PreviousWipe{PreviousDeploymentID: "deployment-before"}}
		}
		r.measureProvision(context.Background(), map[string]int{"region-a": 3, "region-b": 2})
		r.run.Finalize(nil)
		p, err := r.run.Write(dir)
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Result    string         `json:"result"`
			Provision map[string]any `json:"provision"`
		}
		if b, err := os.ReadFile(filepath.Clean(p)); err != nil || json.Unmarshal(b, &rec) != nil {
			t.Fatalf("%s: the record cannot be read back: %v", env, err)
		}
		if len(rec.Provision) != keys || rec.Provision["fresh"] != (env == "fresh") || rec.Result != Pass {
			t.Errorf("%s: the record holds result %s and provision %v", env, rec.Result, rec.Provision)
		}
	}
}
