package suite

import (
	"strings"
	"testing"
)

func fakeMachine(name, class string) obj {
	return obj{"metadata": kubeMeta("catalyst-infra", name), "spec": obj{"class": class}}
}

func fakeMachineRun(name, kind, phase, finished string) obj {
	return obj{"metadata": kubeMeta("catalyst-infra", name), "spec": obj{"kind": kind, "policyRef": obj{"kind": "ScanPolicy", "name": "nodes"}},
		"status": obj{"phase": phase, "finishedAt": finished}}
}

// fakeVulnReport is a vulnerability report: the rule database-age with the given
// result, and one fail result; version "" leaves databaseVersion out of the fail result.
func fakeVulnReport(node, age, version string) obj {
	fail := obj{"source": "machine-vulnerability", "policy": "CVE-2020-8203", "rule": "lodash", "result": "fail", "properties": obj{"fixedVersion": "4.17.19"}}
	if version != "" {
		fail["properties"].(obj)["databaseVersion"] = version
	}
	return obj{"metadata": kubeMeta("catalyst-infra", "machine-"+node+"-vulnerability"), "results": []obj{
		{"source": "machine-vulnerability", "policy": "vulnerability-database", "rule": "database-age", "result": age, "properties": obj{"databaseVersion": "2026-11-30T06:00:00Z"}}, fail}}
}

func init() {
	const db = "2026-11-30T06:00:00Z"
	machines := kubeList(fakeMachine("cp-1", "node"), fakeMachine("cp-2", "node"), fakeMachine("vm-1", "virtual-machine"))
	runs := kubeList(fakeMachineRun("nodes-a", "scan", "Failed", "2026-12-01T02:40:00Z"), fakeMachineRun("nodes-b", "scan", "Succeeded", "2026-11-30T02:40:00Z"),
		fakeMachineRun("nodes-c", "rematch", "Succeeded", "2026-12-01T03:40:00Z"))
	reports := kubeList(fakeVulnReport("cp-1", "pass", db), fakeVulnReport("cp-2", "pass", db))
	good := map[string]string{"* " + g11Machines + " catalyst-infra": machines, "* " + g11Runs + " catalyst-infra": runs, "* " + g11Reports + " catalyst-infra": reports}
	with := func(question, answer string) map[string]string {
		out := map[string]string{}
		for k, v := range good {
			out[k] = v
		}
		if answer == "" {
			delete(out, question)
		} else {
			out[question] = answer
		}
		return out
	}
	fakeAnswers["g11-good"] = good
	fakeAnswers["g11-no-node"] = with("* "+g11Machines+" catalyst-infra", kubeList(fakeMachine("vm-1", "virtual-machine")))
	fakeAnswers["g11-no-scan"] = with("* "+g11Runs+" catalyst-infra", kubeList(fakeMachineRun("nodes-a", "scan", "Failed", "2026-12-01T02:40:00Z"),
		fakeMachineRun("nodes-c", "rematch", "Succeeded", "2026-12-01T03:40:00Z")))
	fakeAnswers["g11-no-report"] = with("* "+g11Reports+" catalyst-infra", kubeList(fakeVulnReport("cp-1", "pass", db)))
	fakeAnswers["g11-no-version"] = with("* "+g11Reports+" catalyst-infra", kubeList(fakeVulnReport("cp-1", "pass", db), fakeVulnReport("cp-2", "pass", "")))
	fakeAnswers["g11-stale"] = with("* "+g11Reports+" catalyst-infra", kubeList(fakeVulnReport("cp-1", "pass", db), fakeVulnReport("cp-2", "warn", db)))
	noAge := fakeVulnReport("cp-2", "pass", db)
	noAge["results"] = noAge["results"].([]obj)[1:]
	fakeAnswers["g11-no-age"] = with("* "+g11Reports+" catalyst-infra", kubeList(fakeVulnReport("cp-1", "pass", db), noAge))
	fakeAnswers["g11-not-served"] = with("* "+g11Machines+" catalyst-infra", "")
}

func TestVulnReportsCheck(t *testing.T) {
	if _, ok := registry[infG11ID]; !ok || infG11ID != "INF-G11" {
		t.Fatalf("check %s is not registered", infG11ID)
	}
	for scenario, want := range map[string]string{
		"g11-good":       "",
		"g11-no-node":    "no Machine of class node in catalyst-infra",
		"g11-no-scan":    "no MachineRun of kind scan of the policy nodes has the phase Succeeded",
		"g11-no-report":  "node cp-2 has no PolicyReport machine-cp-2-vulnerability",
		"g11-no-version": "1 results of machine-cp-2-vulnerability have no databaseVersion",
		"g11-stale":      "the rule database-age of machine-cp-2-vulnerability is warn",
		"g11-no-age":     "machine-cp-2-vulnerability has no result of the rule database-age",
		"g11-not-served": "kubectl get " + g11Machines,
	} {
		r := runCheck(t, scenario, vulnReportsCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case want == "" && (got.Status != Pass || len(r.run.Steps) != 2):
			t.Errorf("%s: want PASS in two regions, got %+v", scenario, got)
		case want != "" && (got.Status != Fail || !strings.Contains(got.Reason, want)):
			t.Errorf("%s: want FAIL with %q, got %+v", scenario, want, got)
		}
	}
	r := runCheck(t, "g11-good", vulnReportsCheck{}, &CheckContext{})
	ev := r.run.Steps[0].Evidence
	if ev["scan_run"] != "nodes-b" || strings.Join(ev["nodes"].([]string), ",") != "cp-1,cp-2" || ev["database_versions"].(map[string]string)["cp-2"] != "2026-11-30T06:00:00Z" {
		t.Errorf("the evidence is %+v", ev)
	}
}
