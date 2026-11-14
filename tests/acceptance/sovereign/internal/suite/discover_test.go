package suite

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
)

// The discovery steps that only the journeys need (D0.3, D0.4 and D0.5) are
// left out of a run that requests no journey. The tests run the discovery
// against a fake console and the fake kubectl of this package.
//
// A full console holds the own Organization, its Environment over both
// regions and the Blueprint. A bare console holds none of the three, like a
// Sovereign that has not been given its own Organization yet: there step D0.3
// fails when it is run.

func init() {
	fakeAnswers["discover"] = map[string]string{
		"a configmaps flux-system":      kubeList(fakeVariables("huawei", "region-a")),
		"b configmaps flux-system":      kubeList(fakeVariables("huawei", "region-b")),
		"* deployments catalyst-system": kubeList(),
		"* ciliumnodes":                 kubeList(),
		"* pods kube-system":            kubeList(),
	}
}

// journeyObjectPaths are the paths that steps D0.3, D0.4 and D0.5 ask for.
var journeyObjectPaths = []string{
	"/api/v1/organizations",
	"/api/v1/sovereigns/dep/environments",
	"/api/v1/catalog/sample-app",
	"/api/v1/catalog/sample-app/versions/1.0.0",
}

// fakeConsole answers the questions of the discovery and notes each path.
type fakeConsole struct {
	bare  bool
	mu    sync.Mutex
	asked []string
}

func (c *fakeConsole) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c.mu.Lock()
	c.asked = append(c.asked, req.URL.Path)
	c.mu.Unlock()
	answers := map[string]string{
		"/api/v1/whoami":                                  `{"tier":"owner","deploymentId":"dep","sovereignFQDN":"pack.example","mode":"sovereign"}`,
		"/api/v1/sovereigns/dep/regions":                  `["region-a","region-b"]`,
		"/api/v1/deployments/dep/infrastructure/topology": `{"topology":{"regions":[{"name":"region-a"},{"name":"region-b"}]}}`,
		"/api/v1/organizations":                           `{"items":[]}`,
	}
	if !c.bare {
		answers["/api/v1/organizations"] = `{"items":[{"subdomain":"own","kind":"internal","state":"active"}]}`
		answers["/api/v1/sovereigns/dep/environments"] = `{"items":[{"name":"own-cp","organizationRef":"own","placement":"multi-region","regions":[{},{}]}]}`
		answers["/api/v1/catalog/sample-app"] = `{"name":"bp-sample-app","version":"1.0.0"}`
		answers["/api/v1/catalog/sample-app/versions/1.0.0"] = `{"raw":{"spec":{"topology":{"supported":["active-hot-standby"]}}}}`
	}
	body, found := answers[req.URL.Path]
	if !found {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// askedFor returns the journey-object paths that the console was asked for.
func (c *fakeConsole) askedFor() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{}
	for _, p := range c.asked {
		if slices.Contains(journeyObjectPaths, p) {
			out = append(out, p)
		}
	}
	return out
}

// stepLine prints the steps of a run as "<journey> <id> <status>", in order.
func stepLine(run *Run) string {
	parts := []string{}
	for _, s := range run.Steps {
		parts = append(parts, s.Journey+" "+s.ID+" "+s.Status)
	}
	return strings.Join(parts, ", ")
}

// discoverOn runs the discovery of a run against a console. It returns the
// runner, the result of the discovery and what the run printed.
func discoverOn(t *testing.T, console *fakeConsole, noJourney bool) (*runner, bool, string) {
	t.Helper()
	t.Setenv(fakeKubectlEnv, "discover")
	srv := httptest.NewServer(console)
	defer srv.Close()
	regions := fakeRegions(t)
	out := &bytes.Buffer{}
	r := &runner{cfg: Config{Blueprint: "bp-sample-app"}, out: out, api: api.New(srv.URL, "session"),
		run: &Run{Mode: "run", Subject: Evidence{}}, A: regions[0], B: regions[1], noJourney: noJourney}
	r.A.ID, r.B.ID = "", "" // the discovery reads each region's identity itself
	ok := r.discover(context.Background())
	return r, ok, out.String()
}

const (
	everyStep     = "D0 D0.1 PASS, D0 D0.2 PASS, D0 D0.3 PASS, D0 D0.4 PASS, D0 D0.5 PASS, D0 D0.6 PASS, D0 D0.10 PASS"
	withoutThree  = "D0 D0.1 PASS, D0 D0.2 PASS, D0 D0.6 PASS, D0 D0.10 PASS"
	stopsAtTheOrg = "D0 D0.1 PASS, D0 D0.2 PASS, D0 D0.3 FAIL"
)

// A run that requests a journey runs every step, as before.
func TestDiscoverWithAJourneyRunsEveryStep(t *testing.T) {
	console := &fakeConsole{}
	r, ok, _ := discoverOn(t, console, false)
	if !ok || stepLine(r.run) != everyStep {
		t.Fatalf("want every step passed, got ok=%v steps: %s", ok, stepLine(r.run))
	}
	if _, listed := r.run.Subject[notRequestedKey]; listed {
		t.Errorf("a run with a journey names no step as not requested, got %v", r.run.Subject[notRequestedKey])
	}
	if r.run.Subject["org"] != "own" || r.run.Subject["environment"] != "own-cp" || r.run.Subject["blueprint"] != "bp-sample-app@1.0.0" {
		t.Errorf("the subject does not hold the three journey objects: %v", r.run.Subject)
	}
	if got := console.askedFor(); !slices.Equal(got, journeyObjectPaths) {
		t.Errorf("the console was asked for %v, want %v", got, journeyObjectPaths)
	}
}

// A run that requests no journey leaves the three steps out. On a bare console
// its discovery passes, and the subject of the run lists the three steps.
func TestDiscoverWithoutAJourneyLeavesOutTheJourneyObjects(t *testing.T) {
	console := &fakeConsole{bare: true}
	r, ok, printed := discoverOn(t, console, true)
	if !ok || stepLine(r.run) != withoutThree {
		t.Fatalf("want the discovery to pass without the three steps, got ok=%v steps: %s", ok, stepLine(r.run))
	}
	left, _ := r.run.Subject[notRequestedKey].([]string)
	if !slices.Equal(left, []string{"D0.3", "D0.4", "D0.5"}) {
		t.Errorf("subject.%s is %v, want [D0.3 D0.4 D0.5]", notRequestedKey, r.run.Subject[notRequestedKey])
	}
	for _, key := range []string{"org", "environment", "blueprint"} {
		if v, held := r.run.Subject[key]; held {
			t.Errorf("subject.%s is %v, but the step that reads it did not run", key, v)
		}
	}
	if got := console.askedFor(); len(got) != 0 {
		t.Errorf("the console was asked for %v, but the three steps must not run", got)
	}
	for _, id := range []string{"D0.3", "D0.4", "D0.5"} {
		if !strings.Contains(printed, "D0 "+id+"   not requested: the run requests no journey") {
			t.Errorf("the output has no line that says %s was not requested:\n%s", id, printed)
		}
	}
	// A step that was not run is not a step of the record: nothing says PASS for it.
	if r.run.Finalize(nil); r.run.Result != Pass {
		t.Errorf("the run is %s, want PASS", r.run.Result)
	}
}

// The control of the test above: on the same bare console a run that requests
// a journey fails at step D0.3.
func TestDiscoverOnABareConsoleFailsWhenAJourneyIsRequested(t *testing.T) {
	r, ok, _ := discoverOn(t, &fakeConsole{bare: true}, false)
	if ok || stepLine(r.run) != stopsAtTheOrg {
		t.Fatalf("want the discovery to fail at D0.3, got ok=%v steps: %s", ok, stepLine(r.run))
	}
	if _, listed := r.run.Subject[notRequestedKey]; listed {
		t.Errorf("a failed step is not a step that was not requested: %v", r.run.Subject[notRequestedKey])
	}
	if r.run.Finalize([]string{"J1"}); r.run.Result != Fail {
		t.Errorf("the run is %s, want FAIL", r.run.Result)
	}
}

// journeyStep by itself: it runs the step unless the run requests no journey.
func TestJourneyStep(t *testing.T) {
	ran := 0
	fn := func(Evidence) error { ran++; return nil }
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	if !r.journeyStep("D0.3", "a step", fn) || ran != 1 || stepLine(r.run) != "D0 D0.3 PASS" || r.run.Subject != nil {
		t.Fatalf("a run with a journey: ran=%d steps=%q subject=%v", ran, stepLine(r.run), r.run.Subject)
	}
	r = &runner{out: io.Discard, run: &Run{Mode: "run"}, noJourney: true}
	if !r.journeyStep("D0.3", "a step", fn) || !r.journeyStep("D0.4", "another step", fn) || ran != 1 || len(r.run.Steps) != 0 {
		t.Fatalf("a run without a journey: ran=%d steps=%q", ran, stepLine(r.run))
	}
	if left, _ := r.run.Subject[notRequestedKey].([]string); !slices.Equal(left, []string{"D0.3", "D0.4"}) {
		t.Fatalf("subject.%s is %v, want [D0.3 D0.4]", notRequestedKey, r.run.Subject[notRequestedKey])
	}
}

// Execute decides which kind of run it is: only a run (not a discover run)
// that requests no journey leaves the three steps out. The fake is the kubectl
// of the PATH, as in TestExecuteMeasuresProvisionInTheCloud.
func TestExecuteLeavesOutTheJourneyObjectsOnlyWithoutAJourney(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeKubectlEnv, "discover")
	for _, c := range []struct {
		name     string
		readOnly bool
		journeys string
		result   string
		steps    string // every step of a run; the first steps of a discover run, which goes on to probe
		left     []string
	}{
		{"a run without a journey", false, "", Pass, withoutThree, []string{"D0.3", "D0.4", "D0.5"}},
		{"a run whose journey list holds only blanks", false, " , ", Pass, withoutThree, []string{"D0.3", "D0.4", "D0.5"}},
		{"a run that requests a journey", false, "J1", Fail, stopsAtTheOrg + ", J1 - SKIP", nil},
		{"a discover run", true, "", Fail, stopsAtTheOrg + ", D0 D0.7 ", nil},
	} {
		console := &fakeConsole{bare: true}
		srv := httptest.NewServer(console)
		cfg := Config{Env: "pack", Console: srv.URL, Session: "x", KubeconfigA: "a", KubeconfigB: "b",
			RunIn: string(RunInLocal), RepoRoot: ".", Blueprint: "bp-sample-app", Journeys: c.journeys, ReadOnly: c.readOnly}
		run, err := Execute(context.Background(), cfg, io.Discard)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := stepLine(run)
		asWanted := got == c.steps
		if c.readOnly {
			asWanted = strings.HasPrefix(got, c.steps)
		}
		if run.Result != c.result || !asWanted {
			t.Errorf("%s: result %s, steps: %s; want result %s and steps: %s", c.name, run.Result, got, c.result, c.steps)
		}
		left, _ := run.Subject[notRequestedKey].([]string)
		if !slices.Equal(left, c.left) {
			t.Errorf("%s: subject.%s is %v, want %v", c.name, notRequestedKey, left, c.left)
		}
		if asked := console.askedFor(); (len(asked) == 0) != (c.left != nil) {
			t.Errorf("%s: the console was asked for %v", c.name, asked)
		}
	}
}
