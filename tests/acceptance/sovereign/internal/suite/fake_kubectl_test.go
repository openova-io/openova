package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The fake kubectl of the check tests.
//
// A check reads a cluster only through kube.Region, which starts the binary
// named in Region.Kubectl. The tests name the test binary itself there and
// put the name of a scenario in the environment variable fakeKubectlEnv.
// TestMain then answers as kubectl would: it prints the JSON list that the
// scenario holds for the region (the value of --kubeconfig) and the resource
// asked for. A question the scenario has no answer for fails with exit code 1,
// as a cluster that cannot be reached does.
//
// Like kubectl, the fake prints metadata.managedFields only when it is asked
// with --show-managed-fields.

const fakeKubectlEnv = "SUITE_FAKE_KUBECTL"

// fakeAnswers holds the scenarios: scenario -> question -> JSON list. A
// question is "<kubeconfig> <resource>" or "<kubeconfig> <resource> <namespace>".
// The kubeconfig "*" answers for every region. Each test file adds its
// scenarios in init(), so that the fake has them when it runs as kubectl.
var fakeAnswers = map[string]map[string]string{}

func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeKubectlEnv); scenario != "" && len(os.Args) > 1 && os.Args[1] == "--kubeconfig" {
		os.Exit(fakeKubectl(scenario, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeKubectl(scenario string, args []string) int {
	var kubeconfig, resource, namespace string
	managed := false
	for i := 0; i < len(args); i++ {
		value := ""
		if i+1 < len(args) {
			value = args[i+1]
		}
		switch args[i] {
		case "--kubeconfig":
			kubeconfig = value
			i++
		case "get":
			resource = value
			i++
		case "-n":
			namespace = value
			i++
		case "--show-managed-fields":
			managed = true
		}
	}
	answers := fakeAnswers[scenario]
	for _, question := range []string{
		kubeconfig + " " + resource + " " + namespace, kubeconfig + " " + resource,
		"* " + resource + " " + namespace, "* " + resource,
	} {
		text, ok := answers[question]
		if !ok {
			continue
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			fmt.Fprintln(os.Stderr, "fake kubectl: the answer is not JSON:", err)
			return 1
		}
		for _, item := range list.Items {
			if meta, isMap := item["metadata"].(map[string]any); isMap && !managed {
				delete(meta, "managedFields")
			}
		}
		if list.Items == nil {
			list.Items = []map[string]any{}
		}
		if err := json.NewEncoder(os.Stdout).Encode(list); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: scenario %s cannot reach %q for %q in namespace %q\n", scenario, kubeconfig, resource, namespace)
	return 1
}

// obj is one JSON object of a fixture.
type obj = map[string]any

// kubeList is the output of `kubectl get -o json` for a list of objects.
func kubeList(list ...obj) string {
	b, err := json.Marshal(obj{"items": list})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// kubeMeta is the metadata of a fixture object. Each manager becomes one entry of
// managedFields.
func kubeMeta(namespace, name string, managers ...string) obj {
	fields := []obj{}
	for _, m := range managers {
		fields = append(fields, obj{"manager": m, "operation": "Update"})
	}
	return obj{"namespace": namespace, "name": name, "managedFields": fields}
}

// kubeConditions is status.conditions: pairs of type and status.
func kubeConditions(pairs ...string) []obj {
	out := []obj{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, obj{"type": pairs[i], "status": pairs[i+1], "reason": "Fixture"})
	}
	return out
}

// fakeRegions are two regions whose kubectl is the test binary.
func fakeRegions(t *testing.T) []kube.Region {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []kube.Region{
		{ID: "region-a", Kubeconfig: "a", Kubectl: self},
		{ID: "region-b", Kubeconfig: "b", Kubectl: self},
	}
}

// runCheck runs one check against a scenario the way the runner does and
// returns the runner. cc.Regions is set to the two fake regions.
func runCheck(t *testing.T, scenario string, c Check, cc *CheckContext, before ...*Step) *runner {
	t.Helper()
	t.Setenv(fakeKubectlEnv, scenario)
	cc.Regions = fakeRegions(t)
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: before}}
	m := &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}
	r.runChecks(context.Background(), m, []Check{c}, cc, true)
	return r
}

// failedSteps returns the errors of the failed steps of a run, in order.
func failedSteps(r *runner) []string {
	out := []string{}
	for _, s := range r.run.Steps {
		if s.Status == Fail {
			out = append(out, s.Error)
		}
	}
	return out
}
