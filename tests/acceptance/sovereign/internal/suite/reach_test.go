package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/retry"
)

// The discovery retries a read that got no answer and stops at a real answer;
// a run whose discovery could not reach a region is NOT-MEASURED and writes no
// record (reach.go). The errors are those of gate run 3 on hw307 (15 Nov 2026),
// from a runner in another cloud, while the same API answered in 4 ms from the
// operator host: step D0.10's exec stream broke, and step D0.11's TLS handshake
// timed out. That run wrote a FAIL record without an artefact list, and the
// pass rule then formed no pair with the next run.
const (
	run3ExecStream = `E1115 19:35:12.419799    4495 v2.go:167] "Unhandled Error" err="read message: %!w(<nil>)"
E1115 19:35:12.419833    4495 v2.go:150] "Unhandled Error" err="read message: %!w(<nil>)"
error: error reading from error stream: read message: %!w(<nil>)`
	run3Handshake = "Unable to connect to the server: net/http: TLS handshake timeout"
)

// The tests of this package retry without waiting; the bound itself is proven
// in package retry.
func init() {
	newRetryPolicy = func() *retry.Policy {
		p := retry.Default()
		p.Sleep = func(context.Context, time.Duration) error { return nil }
		return p
	}
}

// fakeFaultEnv names a JSON file with a fault of the path to one question of
// the fake kubectl: it fails with stderr for the first times calls (-1: every
// call) and counts the calls in the file counter.
const fakeFaultEnv = "SUITE_FAKE_KUBECTL_FAULT"

type fakeFault struct {
	Question string `json:"question"`
	Times    int    `json:"times"`
	Stderr   string `json:"stderr"`
	Counter  string `json:"counter"`
}

// fakeReach is called by the fake kubectl before it answers a question. It
// applies the fault of fakeFaultEnv, and it answers `exec`, which the fake
// otherwise does not know: the question of `kubectl exec -n <ns> <pod> …` is
// "<kubeconfig> exec <pod>" and its answer is printed as it is.
func fakeReach(scenario, kubeconfig, resource, namespace string, args []string) (int, bool) {
	question := kubeconfig + " " + resource + " " + namespace
	exec := false
	for i, a := range args {
		if a == "exec" && i+3 < len(args) && args[i+1] == "-n" {
			question, exec = kubeconfig+" exec "+args[i+3], true
			break
		}
	}
	if b, err := os.ReadFile(filepath.Clean(os.Getenv(fakeFaultEnv))); err == nil {
		var f fakeFault
		if json.Unmarshal(b, &f) == nil && f.Question == question {
			fh, err := os.OpenFile(filepath.Clean(f.Counter), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return 3, true
			}
			_, _ = fh.WriteString(question + "\n")
			_ = fh.Close()
			seen, _ := os.ReadFile(filepath.Clean(f.Counter))
			if n := strings.Count(string(seen), "\n"); f.Times < 0 || n <= f.Times {
				fmt.Fprintln(os.Stderr, f.Stderr)
				return 1, true
			}
		}
	}
	if !exec {
		return 0, false
	}
	if out, ok := fakeAnswers[scenario][question]; ok {
		fmt.Print(out)
		return 0, true
	}
	fmt.Fprintf(os.Stderr, "error: scenario %s cannot reach %q\n", scenario, question)
	return 1, true
}

// withFault sets a fault for the rest of the test and returns how often its
// question was asked.
func withFault(t *testing.T, question string, times int, stderr string) func() int {
	t.Helper()
	dir := t.TempDir()
	f := fakeFault{Question: question, Times: times, Stderr: stderr, Counter: filepath.Join(dir, "calls")}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fault.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeFaultEnv, filepath.Join(dir, "fault.json"))
	return func() int {
		seen, _ := os.ReadFile(filepath.Clean(f.Counter))
		return strings.Count(string(seen), "\n")
	}
}

func init() {
	// A Sovereign whose mesh is whole: one Cilium agent per region, each holding
	// the peer region's gateway IP as reserved:ingress (identity 8), and what
	// step D0.11 reads.
	ciliumNode := func(name, ip string) obj {
		return obj{"metadata": kubeMeta("", name), "spec": obj{"ingress": obj{"ipv4": ip}}}
	}
	reach := map[string]string{
		"a ciliumnodes":                 kubeList(ciliumNode("a-1", "10.1.0.1")),
		"b ciliumnodes":                 kubeList(ciliumNode("b-1", "10.2.0.1")),
		"a pods kube-system":            kubeList(obj{"metadata": kubeMeta("kube-system", "cilium-crdb5")}),
		"b pods kube-system":            kubeList(obj{"metadata": kubeMeta("kube-system", "cilium-b1")}),
		"a exec cilium-crdb5":           "10.2.0.1/32 identity=8 encryptkey=0 tunnelendpoint=0.0.0.0\n",
		"b exec cilium-b1":              "10.1.0.1/32 identity=8 encryptkey=0 tunnelendpoint=0.0.0.0\n",
		"* deployments catalyst-system": kubeList(),
	}
	for k, v := range fakeAnswers["provision-installed"] {
		reach[k] = v
	}
	fakeAnswers["reach"] = reach
}

// reachOn runs the discovery of a run that requests a journey against the
// scenario reach and the full fake console, as discoverOn does for its scenario.
func reachOn(t *testing.T) (*runner, bool) {
	t.Helper()
	t.Setenv(fakeKubectlEnv, "reach")
	srv := httptest.NewServer(&fakeConsole{})
	defer srv.Close()
	regions := fakeRegions(t)
	r := &runner{cfg: Config{Blueprint: "bp-sample-app"}, out: &bytes.Buffer{}, api: api.New(srv.URL, "session"),
		run: &Run{Mode: "run", Subject: Evidence{}}, A: regions[0], B: regions[1]}
	r.A.ID, r.B.ID = "", "" // the discovery reads each region's identity itself
	ok := r.discover(context.Background())
	return r, ok
}

// executeReach runs the suite in the cloud against the scenario reach, with
// the fake as the kubectl of the PATH, and returns the run, the record
// directory and what it printed.
func executeReach(t *testing.T, console string) (*Run, string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeKubectlEnv, "reach")
	dir, out := t.TempDir(), &bytes.Buffer{}
	cfg := Config{Env: "reach", Console: console, Session: "x", KubeconfigA: "a", KubeconfigB: "b",
		RunIn: string(RunInCloud), RepoRoot: ".", Blueprint: "bp-sample-app", Journeys: "J1", Record: true, RecordDir: dir}
	run, err := Execute(context.Background(), cfg, out)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return run, dir, out.String()
}

func records(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// refusingConsole is a console of this machine that answers 401 to every
// request: a real answer, so the discovery fails at once at step D0.1 and the
// run is a measured FAIL.
func refusingConsole(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// CONTROL: on the whole Sovereign every discovery step passes, D0.10 included,
// and D0.10 checked one peer gateway IP in each region.
func TestReachControlDiscoveryPasses(t *testing.T) {
	r, ok := reachOn(t)
	if !ok || stepLine(r.run) != everyStep {
		t.Fatalf("want every step passed, got ok=%v steps: %s", ok, stepLine(r.run))
	}
	if mesh := r.run.Steps[len(r.run.Steps)-1]; mesh.Evidence["checked"] != 2 {
		t.Errorf("step D0.10 checked %v peer gateway IPs, want 2", mesh.Evidence["checked"])
	}
}

// Two TLS handshake timeouts of region a, then an answer: the read is retried
// and the discovery passes.
func TestDiscoveryRetriesATransientFailureAndPasses(t *testing.T) {
	calls := withFault(t, "a configmaps flux-system", 2, run3Handshake)
	r, ok := reachOn(t)
	if !ok || stepLine(r.run) != everyStep {
		t.Fatalf("two handshake timeouts then an answer: ok=%v steps: %s; want every step passed", ok, stepLine(r.run))
	}
	if calls() != 3 {
		t.Errorf("the bootstrap variables of region a were asked %d times, want 3 (two failures and the answer)", calls())
	}
	// The same for a broken exec stream of step D0.10.
	calls = withFault(t, "a exec cilium-crdb5", 1, run3ExecStream)
	if r, ok = reachOn(t); !ok || stepLine(r.run) != everyStep || calls() != 2 {
		t.Fatalf("one broken stream then an answer: ok=%v steps: %s after %d calls", ok, stepLine(r.run), calls())
	}
	// The retries end with the discovery: a check makes its calls once.
	if r.A.Retry != nil || r.B.Retry != nil || r.api.Retry != nil {
		t.Errorf("the retry policies are still set after the discovery: %v %v %v", r.A.Retry, r.B.Retry, r.api.Retry)
	}
}

// A region whose exec stream keeps breaking, as in gate run 3: the discovery
// fails at D0.10 after 5 attempts, the run is NOT-MEASURED with exit code 4
// and a line that says so, and no record is written.
func TestDiscoveryThatCannotReachARegionIsNotMeasured(t *testing.T) {
	calls := withFault(t, "a exec cilium-crdb5", -1, run3ExecStream)
	srv := httptest.NewServer(&fakeConsole{})
	defer srv.Close()
	run, dir, printed := executeReach(t, srv.URL)
	if run.Result != NotMeasured {
		t.Fatalf("result %s, want %s; steps: %s", run.Result, NotMeasured, stepLine(run))
	}
	if calls() != 5 {
		t.Errorf("the exec was attempted %d times, want 5", calls())
	}
	if files := records(t, dir); len(files) != 0 {
		t.Errorf("a NOT-MEASURED run wrote %v", files)
	}
	var mesh *Step
	for _, s := range run.Steps {
		if s.Journey == "D0" && s.ID == "D0.10" {
			mesh = s
		}
	}
	if mesh == nil || mesh.Status != Fail || !strings.Contains(mesh.Error, "error reading from error stream") ||
		!strings.Contains(mesh.Error, "no answer after 5 attempt(s)") {
		t.Errorf("step D0.10 does not say what failed and how often: %+v", mesh)
	}
	for _, want := range []string{"NOT MEASURED: the discovery could not reach the Sovereign after its retries (step D0.10).",
		"RESULT NOT-MEASURED", "not recorded: a NOT-MEASURED run writes no record"} {
		if !strings.Contains(printed, want) {
			t.Errorf("the output has no line %q:\n%s", want, printed)
		}
	}
	// Step D0.11 still measured the installation: only region a's exec failed.
	if last := run.Steps[len(run.Steps)-1]; last.ID != provisionStepID || last.Status != Pass {
		t.Errorf("want step %s last and passed, got %s %s %s", provisionStepID, last.ID, last.Status, last.Error)
	}
}

// Step D0.11 at the end of a run, whose region a gives no answer: the run is NOT-MEASURED.
func TestProvisionThatCannotReachARegionIsNotMeasured(t *testing.T) {
	calls := withFault(t, "a configmaps flux-system", -1, run3Handshake)
	t.Setenv(fakeKubectlEnv, "reach")
	regions := fakeRegions(t)
	r := &runner{out: &bytes.Buffer{}, run: &Run{Mode: "run", Steps: []*Step{{Journey: "D0", ID: "D0.1", Status: Pass}}}, A: regions[0], B: regions[1]}
	r.measureProvision(context.Background(), map[string]int{"region-a": 3, "region-b": 2})
	last := r.run.Steps[len(r.run.Steps)-1]
	if last.Status != Fail || !strings.Contains(last.Error, "provider: [region-a] kubectl get configmaps") || !strings.Contains(last.Error, "TLS handshake timeout") {
		t.Fatalf("step %s: %s %q", last.ID, last.Status, last.Error)
	}
	if calls() != 5 {
		t.Errorf("the bootstrap variables of region a were asked %d times, want 5", calls())
	}
	if r.run.Finalize(nil); r.run.Result != NotMeasured {
		t.Errorf("result %s, want %s", r.run.Result, NotMeasured)
	}
	if _, err := r.run.Write(t.TempDir()); err == nil {
		t.Errorf("Write wrote the record of a NOT-MEASURED run")
	}
}

// A real answer is never retried, and a run that failed on it is a measured
// FAIL with a record.
func TestDiscoveryNeverRetriesARealAnswer(t *testing.T) {
	calls := withFault(t, "a configmaps flux-system", -1, `Error from server (Forbidden): configmaps is forbidden: User "gate" cannot list resource "configmaps" in the namespace "flux-system"`)
	r, ok := reachOn(t)
	if ok || stepLine(r.run) != "D0 D0.1 PASS, D0 D0.2 FAIL" || calls() != 1 {
		t.Fatalf("a Forbidden: ok=%v steps: %s after %d calls; want D0.2 failed after one call", ok, stepLine(r.run), calls())
	}
	// The whole run: D0.2 and, at the end, D0.11 each ask once.
	srv := httptest.NewServer(&fakeConsole{})
	defer srv.Close()
	run, dir, _ := executeReach(t, srv.URL)
	if run.Result != Fail || calls() != 1+2 {
		t.Fatalf("a Forbidden: result %s after %d calls in all, want FAIL after 3; steps: %s", run.Result, calls(), stepLine(run))
	}
	if files := records(t, dir); len(files) != 1 {
		t.Errorf("a measured FAIL must be recorded, got %v", files)
	}
}

// A discovery step that failed on an answer makes the run a measured FAIL,
// even when another step could not reach a region.
func TestAnAnswerInTheDiscoveryOutweighsAnUnreachableStep(t *testing.T) {
	withFault(t, "a exec cilium-crdb5", -1, run3ExecStream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/v1/catalog/sample-app/versions/1.0.0" { // D0.5 fails on an answer
			_, _ = w.Write([]byte(`{"raw":{"spec":{"topology":{"supported":["singleton"]}}}}`))
			return
		}
		(&fakeConsole{}).ServeHTTP(w, req)
	}))
	defer srv.Close()
	run, dir, _ := executeReach(t, srv.URL)
	if run.Result != Fail || !strings.Contains(stepLine(run), "D0 D0.5 FAIL") || !strings.Contains(stepLine(run), "D0 D0.10 FAIL") {
		t.Fatalf("result %s, steps: %s; want FAIL with D0.5 and D0.10 failed", run.Result, stepLine(run))
	}
	if files := records(t, dir); len(files) != 1 {
		t.Errorf("a measured FAIL must be recorded, got %v", files)
	}
}

// A console of this machine that does not listen: unreachable, so NOT-MEASURED.
func TestAConsoleThatDoesNotListenIsNotMeasured(t *testing.T) {
	run, dir, _ := executeReach(t, "http://127.0.0.1:9")
	if run.Result != NotMeasured || !strings.HasPrefix(stepLine(run), "D0 D0.1 FAIL") {
		t.Fatalf("result %s, steps: %s; want NOT-MEASURED at D0.1", run.Result, stepLine(run))
	}
	if files := records(t, dir); len(files) != 0 {
		t.Errorf("a NOT-MEASURED run wrote %v", files)
	}
	// The control: a console that answers 401 is a measured FAIL, and is recorded.
	run, dir, _ = executeReach(t, refusingConsole(t))
	if run.Result != Fail || len(records(t, dir)) != 1 {
		t.Fatalf("a console that refuses the session: result %s, records %v; want FAIL and one record", run.Result, records(t, dir))
	}
}
