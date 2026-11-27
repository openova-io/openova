package suite

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packRoot is a checkout whose PackHoldFile holds text; an empty text writes no file.
func packRoot(t *testing.T, text string) string {
	t.Helper()
	root := t.TempDir()
	if text == "" {
		return root
	}
	p := filepath.Join(root, filepath.FromSlash(PackHoldFile))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// packManifest has a required check of local and cloud (FND-G01), a required
// check of local (FND-G02), an expected-fail and a not-built check of local,
// and a required check of the cloud only.
func packManifest() *Manifest {
	return &Manifest{Checks: []ManifestCheck{
		entry("FND-G01", StateRequired, "local", "cloud"), entry("FND-G02", StateRequired, "local"),
		entry("FND-G03", StateExpectedFail, "local"), entry("FND-G04", "not-built", "local"),
		entry("FND-G05", StateRequired, "cloud"),
	}}
}

const packFile = "# the reason of the hold\n\nFND-G01 the image of the change is not pinned yet\n"

// runHeld runs FND-G01 with outcome and FND-G02 passing, after discovery
// passed, and returns the finished run.
func runHeld(m *Manifest, outcome func(*CheckContext, StepRecorder) error) *Run {
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: []*Step{{Journey: "D0", ID: "D0.1", Status: Pass}}}}
	checks := []Check{fakeCheck{id: "FND-G01", run: outcome}, fakeCheck{id: "FND-G02", run: stepHolds}}
	r.runChecks(context.Background(), m, checks, &CheckContext{}, true)
	r.run.Finalize(nil)
	return r.run
}

// A held check runs at expected-fail: the run passes when the check fails and
// when it passes, so the hold cannot turn the pack red once the check starts
// to pass by itself; the check that is not held stays required.
func TestPackHoldToleratesBothOutcomes(t *testing.T) {
	for _, c := range []struct {
		name    string
		outcome func(*CheckContext, StepRecorder) error
		status  string
	}{{"a held check that fails", stepBreaks, Fail}, {"a held check that passes", stepHolds, Pass}} {
		m := packManifest()
		holds, err := HoldForPack(m, RunInLocal, packRoot(t, packFile))
		if err != nil {
			t.Fatal(err)
		}
		if len(holds) != 1 || holds[0] != (Hold{ID: "FND-G01", Reason: "the image of the change is not pinned yet"}) {
			t.Fatalf("holds: %+v", holds)
		}
		if m.Checks[0].State != StateExpectedFail || m.Checks[1].State != StateRequired || m.Checks[4].State != StateRequired {
			t.Fatalf("states after the hold: %+v", m.Checks)
		}
		run := runHeld(m, c.outcome)
		got := run.Checks[0]
		if run.Result != Pass || got.ID != "FND-G01" || got.StateAtRun != StateExpectedFail || got.Status != c.status {
			t.Errorf("%s: result %s, check %+v; want PASS, FND-G01 %s at expected-fail", c.name, run.Result, got, c.status)
		}
		if second := run.Checks[1]; second.StateAtRun != StateRequired || second.Status != Pass {
			t.Errorf("%s: the check that is not held: %+v", c.name, second)
		}
	}
	// Control: the same failure without the hold fails the run.
	if run := runHeld(packManifest(), stepBreaks); run.Result != Fail || run.Checks[0].StateAtRun != StateRequired {
		t.Errorf("without the hold a failing required check must fail the run: result %s, %+v", run.Result, run.Checks[0])
	}
}

// A run in cloud or in self holds nothing: the gate of a Sovereign keeps
// every check at its state of the manifest.
func TestPackHoldOnlyInLocal(t *testing.T) {
	root := packRoot(t, packFile)
	for _, r := range []RunIn{RunInCloud, RunInSelf} {
		m := packManifest()
		holds, err := HoldForPack(m, r, root)
		if err != nil || len(holds) != 0 || m.Checks[0].State != StateRequired {
			t.Errorf("run in %s: holds %+v, error %v, FND-G01 %s; want none, nil, required", r, holds, err, m.Checks[0].State)
		}
	}
}

// No file, or a file of comments only, holds nothing.
func TestPackHoldNoFile(t *testing.T) {
	for name, text := range map[string]string{"no file": "", "comments only": "# nothing is held\n\n"} {
		m := packManifest()
		holds, err := HoldForPack(m, RunInLocal, packRoot(t, text))
		if err != nil || len(holds) != 0 || m.Checks[0].State != StateRequired {
			t.Errorf("%s: holds %+v, error %v, FND-G01 %s", name, holds, err, m.Checks[0].State)
		}
	}
}

// A line that cannot be held stops the run with ErrManifest, which writes no record.
func TestPackHoldRefuses(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"a line without a reason":      {"FND-G01\n", "line 1: FND-G01 has no reason"},
		"a check named twice":          {"FND-G01 one\nFND-G02 two\nFND-G01 three\n", "line 3: FND-G01 is named twice"},
		"a check not in the manifest":  {"FND-G99 gone\n", "FND-G99 is not a check of the manifest"},
		"an expected-fail check":       {"FND-G03 already\n", "FND-G03 is expected-fail, and only a required check is held"},
		"a not-built check":            {"FND-G04 later\n", "FND-G04 is not-built, and only a required check is held"},
		"a check that never runs here": {"# a comment\nFND-G05 cloud\n", "line 2: FND-G05 does not run in local"},
	} {
		holds, err := HoldForPack(packManifest(), RunInLocal, packRoot(t, c.text))
		if !errors.Is(err, ErrManifest) || holds != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: holds %+v, error %v; want ErrManifest saying %q", name, holds, err, c.want)
		}
	}
}
