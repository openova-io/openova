package suite

import (
	"strings"
	"testing"
)

// fakeNode is a fixture node with its Ready condition.
func fakeNode(name, ready string) obj {
	return obj{"metadata": kubeMeta("", name), "status": obj{"conditions": kubeConditions("MemoryPressure", "False", "Ready", ready)}}
}

func init() {
	three := kubeList(fakeNode("n1", "True"), fakeNode("n2", "True"), fakeNode("n3", "True"))
	fakeAnswers["nodes-as-requested"] = map[string]string{"* nodes": three}
	fakeAnswers["nodes-one-not-ready"] = map[string]string{
		"a nodes": three,
		"b nodes": kubeList(fakeNode("n1", "True"), fakeNode("n2", "False"), fakeNode("n3", "True")),
	}
	fakeAnswers["nodes-one-more"] = map[string]string{
		"a nodes": three,
		"b nodes": kubeList(fakeNode("n1", "True"), fakeNode("n2", "True"), fakeNode("n3", "True"), fakeNode("n4", "True")),
	}
	fakeAnswers["nodes-region-b-unreachable"] = map[string]string{"a nodes": three}
}

func TestNodesCheck(t *testing.T) {
	if _, ok := registry[nodesID]; !ok || nodesID != "FND-G03" {
		t.Fatalf("check %s is not registered", nodesID)
	}
	both := map[string]int{"region-a": 3, "region-b": 3}
	for name, c := range map[string]struct {
		scenario  string
		requested map[string]int
		want      string // "" = PASS, else a part of the reason of the FAIL
	}{
		"ready equals requested in both regions": {"nodes-as-requested", both, ""},
		"one node is not Ready":                  {"nodes-one-not-ready", both, "2 nodes are Ready in region region-b and 3 were requested"},
		"one node more than requested":           {"nodes-one-more", both, "4 nodes are Ready in region region-b and 3 were requested"},
		"the run did not say":                    {"nodes-as-requested", nil, "the run did not say how many nodes region region-a was asked for"},
		"one region was not said":                {"nodes-as-requested", map[string]int{"region-a": 3}, "the run did not say how many nodes region region-b was asked for"},
		"a region cannot be reached":             {"nodes-region-b-unreachable", both, "[region-b] kubectl get nodes"},
		"a requested region is not reached":      {"nodes-as-requested", map[string]int{"region-a": 3, "region-b": 3, "region-c": 3}, "region region-c was requested with 3 nodes and the run has no kubeconfig for it"},
	} {
		r := runCheck(t, c.scenario, nodesCheck{}, &CheckContext{Deploy: &DeploymentInfo{ID: "d", NodesRequested: c.requested}})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// The evidence holds what was found and what was requested.
	r := runCheck(t, "nodes-one-not-ready", nodesCheck{}, &CheckContext{Deploy: &DeploymentInfo{NodesRequested: both}})
	last := r.run.Steps[len(r.run.Steps)-1]
	if last.Journey != nodesID || last.ID != "FND-G03.2" || last.Evidence["requested"] != 3 || last.Evidence["ready"] != 2 ||
		last.Evidence["nodes"] != 3 || strings.Join(last.Evidence["not_ready"].([]string), ",") != "n2" {
		t.Errorf("the step of region-b does not hold the evidence: %+v", last)
	}
	// Without a deployment the check fails; it never passes on nothing.
	if got := runCheck(t, "nodes-as-requested", nodesCheck{}, &CheckContext{}).run.Checks[0]; got.Status != Fail {
		t.Errorf("without a deployment: want FAIL, got %+v", got)
	}
}

func TestParseNodesRequested(t *testing.T) {
	got, err := parseNodesRequested(" region-a=6 , region-b = 3,")
	if err != nil || len(got) != 2 || got["region-a"] != 6 || got["region-b"] != 3 {
		t.Fatalf("a good value was not read: %v %v", got, err)
	}
	if got, err := parseNodesRequested("  "); got != nil || err != nil {
		t.Fatalf("an empty value must give nil and no error, got %v %v", got, err)
	}
	for _, bad := range []string{"region-a", "region-a=", "=3", "region-a=three", "region-a=0", "region-a=-1", "region-a=3,region-a=3"} {
		if got, err := parseNodesRequested(bad); err == nil || got != nil || !strings.HasPrefix(err.Error(), "--nodes-requested: ") {
			t.Errorf("%q must be refused, got %v %v", bad, got, err)
		}
	}
}
