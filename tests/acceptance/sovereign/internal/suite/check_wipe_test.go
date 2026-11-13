package suite

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWipeCheck(t *testing.T) {
	if _, ok := registry[wipeID]; !ok || wipeID != "FND-G13" {
		t.Fatalf("check %s is not registered", wipeID)
	}
	for name, c := range map[string]struct {
		deploy *DeploymentInfo
		want   string // "" = PASS, else a part of the reason of the FAIL
	}{
		"the wipe left nothing":             {&DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 0}, ""},
		"the wipe left three orphans":       {&DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 3}, "the wipe of deployment d1 left 3 orphans"},
		"the run did not name the wipe":     {&DeploymentInfo{ID: "d2", PreviousWipeOrphans: -1}, "the run did not name the previous deployment"},
		"the run did not count the orphans": {&DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: -1}, "the run did not say how many orphans the wipe of deployment d1 left"},
		"the wiped deployment is this one":  {&DeploymentInfo{ID: "d1", PreviousDeploymentID: "d1", PreviousWipeOrphans: 0}, "the previous deployment d1 is the deployment under test"},
		"the run has no deployment":         {nil, "the run did not name the previous deployment"},
	} {
		r := runCheck(t, "none", wipeCheck{}, &CheckContext{Deploy: c.deploy})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	r := runCheck(t, "none", wipeCheck{}, &CheckContext{Deploy: &DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 3}})
	last := r.run.Steps[len(r.run.Steps)-1]
	if last.ID != "FND-G13.1" || last.Evidence["deployment_id"] != "d2" || last.Evidence["previous_deployment_id"] != "d1" ||
		last.Evidence["previous_wipe_orphans"] != 3 {
		t.Errorf("the step does not hold the evidence: %+v", last)
	}
}

func TestParsePreviousWipe(t *testing.T) {
	if id, n, err := parsePreviousWipe(" d1 ", " 0 "); id != "d1" || n != 0 || err != nil {
		t.Fatalf("a good pair was not read: %q %d %v", id, n, err)
	}
	if id, n, err := parsePreviousWipe("d1", "12"); id != "d1" || n != 12 || err != nil {
		t.Fatalf("a good pair was not read: %q %d %v", id, n, err)
	}
	if id, n, err := parsePreviousWipe("", " "); id != "" || n != -1 || err != nil {
		t.Fatalf("no value must give \"\", -1 and no error, got %q %d %v", id, n, err)
	}
	for _, bad := range [][2]string{{"d1", ""}, {"", "0"}, {"d1", "x"}, {"d1", "-1"}, {"d1", "1.5"}} {
		if id, n, err := parsePreviousWipe(bad[0], bad[1]); err == nil || id != "" || n != -1 || !strings.HasPrefix(err.Error(), "--previous-") {
			t.Errorf("%q must be refused, got %q %d %v", bad, id, n, err)
		}
	}
}

// TestWipeSampleRecord runs the suite with the two values of a wipe. The
// console is a closed port of this machine, so the discovery fails at once
// and no request leaves the machine. The record must carry the two values in
// its object provision. When ACCEPT_SAMPLE_RECORD_DIR is set the record stays
// there, so that `scripts/acceptance-gate.py validate-record` can prove that
// the fields and the schema of that tool agree.
func TestWipeSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	cfg := Config{Env: "wipe", Console: "http://127.0.0.1:9", Session: "x", KubeconfigA: "a", KubeconfigB: "b",
		RunIn: string(RunInLocal), RepoRoot: ".", PreviousDeploymentID: "d1", PreviousWipeOrphans: "0",
		Record: true, RecordDir: dir}
	run, err := Execute(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if run.Provision == nil || run.Provision.PreviousDeploymentID != "d1" || run.Provision.PreviousWipeOrphans != 0 {
		t.Fatalf("the run does not carry the wipe: %+v", run.Provision)
	}
	files, err := filepath.Glob(filepath.Join(dir, "wipe-"+run.RunID+".json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("no record was written: %v %v", files, err)
	}
	b, err := os.ReadFile(filepath.Clean(files[0]))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Provision map[string]any `json:"provision"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	// 0 orphans is written, not left out.
	if len(rec.Provision) != 2 || rec.Provision["previous_deployment_id"] != "d1" || rec.Provision["previous_wipe_orphans"] != float64(0) {
		t.Errorf("the record holds provision %v", rec.Provision)
	}
	// Without the values the record has no object provision.
	cfg.PreviousDeploymentID, cfg.PreviousWipeOrphans, cfg.RecordDir = "", "", t.TempDir()
	if run, err := Execute(context.Background(), cfg, io.Discard); err != nil || run.Provision != nil {
		t.Errorf("a run without the values: want no provision, got %+v %v", run, err)
	}
	// One value without the other stops the run before it starts.
	cfg.PreviousDeploymentID = "d1"
	if _, err := Execute(context.Background(), cfg, io.Discard); err == nil ||
		err.Error() != "--previous-deployment-id is given without --previous-wipe-orphans" {
		t.Errorf("one value alone: want the run refused, got %v", err)
	}
}
