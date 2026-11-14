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

// goodPreflight is a value of --preflight that passes step FND-G13.2 in the
// scenario provision-installed, whose environment was created at 06:20.
const goodPreflight = "2026-11-20T05:55:00Z;servers=1/-/0,volumes=38/400/0,addresses=1/10/0,load_balancers=0/-/0," +
	"nat_gateways=0/-/0,networks=1/5/0,security_groups=3/-/0,keypairs=1/-/0,peerings=0/-/0,buckets=41/100/0,dns_records=-/-/0"

func mustPreflight(t *testing.T, v string) *Preflight {
	t.Helper()
	pre, err := parsePreflight(v)
	if err != nil || pre == nil {
		t.Fatalf("%q was not read: %v", v, err)
	}
	return pre
}

// wipeStep returns the step of check FND-G13 with that identifier.
func wipeStep(t *testing.T, r *runner, id string) *Step {
	t.Helper()
	for _, s := range r.run.Steps {
		if s.Journey == wipeID && s.ID == id {
			return s
		}
	}
	t.Fatalf("the check recorded no step %s", id)
	return nil
}

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
		got := wipeStep(t, runCheck(t, "none", wipeCheck{}, &CheckContext{Deploy: c.deploy}), "FND-G13.1")
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Error, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	r := runCheck(t, "none", wipeCheck{}, &CheckContext{Deploy: &DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 3}})
	first := wipeStep(t, r, "FND-G13.1")
	if first.Evidence["deployment_id"] != "d2" || first.Evidence["previous_deployment_id"] != "d1" || first.Evidence["previous_wipe_orphans"] != 3 {
		t.Errorf("the step does not hold the evidence: %+v", first)
	}
	// The check passes only when both steps pass.
	clean := &DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 0, Preflight: mustPreflight(t, goodPreflight)}
	if got := runCheck(t, "provision-installed", wipeCheck{}, &CheckContext{Deploy: clean}).run.Checks[0]; got.Status != Pass {
		t.Errorf("a clean wipe with its numbers: want PASS, got %+v", got)
	}
	clean.Preflight = nil
	if got := runCheck(t, "provision-installed", wipeCheck{}, &CheckContext{Deploy: clean}).run.Checks[0]; got.Status != Fail ||
		!strings.Contains(got.Reason, "did not give the numbers of the flight checklist") {
		t.Errorf("a clean wipe without its numbers: want FAIL, got %+v", got)
	}
}

// TestPreflightStep proves each rule of step FND-G13.2 with a value that
// breaks it. The scenario provision-installed created the environment at
// 2026-11-20T06:20:00Z.
func TestPreflightStep(t *testing.T) {
	change := func(old, new string) string {
		if !strings.Contains(goodPreflight, old) {
			t.Fatalf("goodPreflight does not hold %q", old)
		}
		return strings.Replace(goodPreflight, old, new, 1)
	}
	for name, c := range map[string]struct {
		scenario, value string
		want            string // "" = PASS, else a part of the reason of the FAIL
	}{
		"every number is there":               {"provision-installed", goodPreflight, ""},
		"the run gave no numbers":             {"provision-installed", "", "the run did not give the numbers of the flight checklist (--preflight)"},
		"a volume was left":                   {"provision-installed", change("volumes=38/400/0", "volumes=38/400/2"), "volumes: the wipe left 2"},
		"a kind the entry does not name left": {"provision-installed", change("nat_gateways=0/-/0", "nat_gateways=1/-/3"), "nat_gateways: the wipe left 3"},
		"the DNS records were not counted":    {"provision-installed", change("dns_records=-/-/0", "dns_records=-/-/-"), "dns_records: the leftovers were not counted"},
		"a DNS record was left":               {"provision-installed", change("dns_records=-/-/0", "dns_records=-/-/1"), "dns_records: the wipe left 1"},
		"the buckets are not in the value":    {"provision-installed", change(",buckets=41/100/0", ""), "buckets: the leftovers were not counted"},
		"the networks have no allowed count":  {"provision-installed", change("networks=1/5/0", "networks=1/-/0"), "networks: the used and the allowed count were not both measured"},
		"the volumes have no used count":      {"provision-installed", change("volumes=38/400/0", "volumes=-/400/0"), "volumes: the used and the allowed count were not both measured"},
		"more used than allowed":              {"provision-installed", change("volumes=38/400/0", "volumes=401/400/0"), "volumes: 401 used of 400 allowed"},
		"read after the environment was born": {"provision-installed", change("2026-11-20T05:55:00Z", "2026-11-20T06:21:00Z"), "not before the environment was created at 2026-11-20T06:20:00Z"},
		"read when the environment was born":  {"provision-installed", change("2026-11-20T05:55:00Z", "2026-11-20T06:20:00Z"), "not before the environment was created at 2026-11-20T06:20:00Z"},
		"the creation time cannot be read":    {"provision-region-b-unreachable", goodPreflight, "the creation of the environment was not read: [region-b] kubectl get namespaces"},
		"the creation time is not a time":     {"provision-bad-birth", goodPreflight, "the creation of the environment was not read"},
		"two rules are broken, both are named": {"provision-installed", strings.Replace(change("servers=1/-/0", "servers=1/-/4"), "networks=1/5/0", "networks=1/-/0", 1),
			"servers: the wipe left 4; networks: the used and the allowed count were not both measured"},
	} {
		deploy := &DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", PreviousWipeOrphans: 0}
		if c.value != "" {
			deploy.Preflight = mustPreflight(t, c.value)
		}
		got := wipeStep(t, runCheck(t, c.scenario, wipeCheck{}, &CheckContext{Deploy: deploy}), "FND-G13.2")
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %s %q", name, got.Status, got.Error)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Error, c.want)):
			t.Errorf("%s: want FAIL with %q, got %s %q", name, c.want, got.Status, got.Error)
		}
	}
	// The evidence holds what was judged.
	deploy := &DeploymentInfo{ID: "d2", PreviousDeploymentID: "d1", Preflight: mustPreflight(t, strings.Replace(goodPreflight, "servers=1/-/0", "servers=1/-/4", 1))}
	step := wipeStep(t, runCheck(t, "provision-installed", wipeCheck{}, &CheckContext{Deploy: deploy}), "FND-G13.2")
	left, _ := step.Evidence["leftovers"].(map[string]int)
	free, _ := step.Evidence["free"].(map[string]int)
	if step.Evidence["measured_at"] != "2026-11-20T05:55:00Z" || step.Evidence["environment_created_at"] != "2026-11-20T06:20:00Z" ||
		step.Evidence["leftover_count"] != 4 || left["servers"] != 4 || len(left) != 11 ||
		len(free) != 4 || free["networks"] != 4 || free["volumes"] != 362 || free["buckets"] != 59 || free["addresses"] != 9 {
		t.Errorf("the step does not hold the evidence: %+v", step.Evidence)
	}
	// A run without a deployment has no numbers either.
	if got := wipeStep(t, runCheck(t, "provision-installed", wipeCheck{}, &CheckContext{}), "FND-G13.2"); got.Status != Fail {
		t.Errorf("no deployment: want FAIL, got %s", got.Status)
	}
}

func TestParsePreflight(t *testing.T) {
	if pre, err := parsePreflight("  "); pre != nil || err != nil {
		t.Fatalf("no value must give nil and no error, got %+v %v", pre, err)
	}
	pre := mustPreflight(t, " 2026-11-20T05:55:00Z; networks=1/5/0 ,volumes=38/-/0,dns_records=-/-/- ")
	if !pre.MeasuredAt.Equal(time.Date(2026, 11, 20, 5, 55, 0, 0, time.UTC)) || len(pre.Kinds) != 3 {
		t.Fatalf("the value was not read: %+v", pre)
	}
	n, v, d := pre.Kinds[0], pre.Kinds[1], pre.Kinds[2]
	if n.Kind != "networks" || *n.Used != 1 || *n.Allowed != 5 || *n.Leftovers != 0 ||
		v.Kind != "volumes" || *v.Used != 38 || v.Allowed != nil || *v.Leftovers != 0 ||
		d.Kind != "dns_records" || d.Used != nil || d.Allowed != nil || d.Leftovers != nil {
		t.Fatalf("the numbers were not read: %+v", pre.Kinds)
	}
	for _, bad := range []string{
		"networks=1/5/0",                                     // no time
		"2026-11-20;networks=1/5/0",                          // a date is not a time
		"2026-11-20T05:55:00+04:00;networks=1/5/0",           // not UTC
		"2026-11-20T05:55:00Z;",                              // no kind
		"2026-11-20T05:55:00Z;networks",                      // no numbers
		"2026-11-20T05:55:00Z;networks=1/5",                  // two numbers
		"2026-11-20T05:55:00Z;networks=1/5/0/0",              // four numbers
		"2026-11-20T05:55:00Z;networks=1/5/x",                // not a number
		"2026-11-20T05:55:00Z;networks=-1/5/0",               // below 0
		"2026-11-20T05:55:00Z;networks=1/05/0",               // not written as a whole number
		"2026-11-20T05:55:00Z;networks=1/5/",                 // an empty number
		"2026-11-20T05:55:00Z;Networks=1/5/0",                // not a lower-case name
		"2026-11-20T05:55:00Z;networks=1/5/0,networks=1/5/0", // a kind twice
		"2026-11-20T05:55:00Z;networks=1/5/0,",               // an empty item
	} {
		if pre, err := parsePreflight(bad); err == nil || pre != nil || !strings.HasPrefix(err.Error(), "--preflight: ") {
			t.Errorf("%q must be refused, got %+v %v", bad, pre, err)
		}
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
	// The numbers of the flight checklist are carried too, with or without a wipe.
	cfg.PreviousDeploymentID, cfg.Preflight, cfg.RecordDir = "", goodPreflight, t.TempDir()
	if run, err := Execute(context.Background(), cfg, io.Discard); err != nil || run.Provision == nil || run.Provision.PreviousWipe != nil ||
		run.Provision.Preflight == nil || len(run.Provision.Preflight.Kinds) != 11 {
		t.Errorf("a run with --preflight: want the numbers in the record, got %+v %v", run, err)
	}
	// A value in another form stops the run before it starts.
	cfg.Preflight = "networks=1/5/0"
	if _, err := Execute(context.Background(), cfg, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "--preflight: ") {
		t.Errorf("a value in another form: want the run refused, got %v", err)
	}
}

// TestPreflightSampleRecord writes the record of a cloud run that follows a
// wipe and was given --preflight (preflight-<run_id>.json). When
// ACCEPT_SAMPLE_RECORD_DIR is set it stays there, so that
// `scripts/acceptance-gate.py validate-record` can prove that the object
// provision.preflight and the schema of that tool agree.
func TestPreflightSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	commit := strings.Repeat("4be7a11c0d", 4)
	zero := zeroTouchRun(t, "zero-touch-clean", "zero-touch-clean")
	t.Setenv(fakeKubectlEnv, "provision-installed")
	regions, now := fakeRegions(t), rfc3339("2026-11-20T08:15:00Z")
	r := &runner{out: io.Discard, A: regions[0], B: regions[1], run: &Run{Schema: SchemaVersion, RunID: NewRunID(now),
		Env: "preflight", Console: "https://console.preflight.omani.works", DeploymentID: "deployment-preflight", Mode: "run",
		RunIn: string(RunInCloud), Capabilities: []string{"multi-region", "previous-wipe"}, SuiteGitSHA: commit, StartedAt: now,
		FinishedAt: now.Add(time.Minute), Candidate: Candidate{ID: freshCandidate, Commit: commit},
		Producer: producerFromEnv(func(string) string { return "" }), Subject: Evidence{}, Checks: zero.run.Checks, Steps: zero.run.Steps}}
	r.run.Provision = &Provision{PreviousWipe: &PreviousWipe{PreviousDeploymentID: "deployment-before"}, Preflight: mustPreflight(t, goodPreflight)}
	r.measureProvision(context.Background(), map[string]int{"region-a": 3, "region-b": 2})
	r.run.Finalize(nil)
	p, err := r.run.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Result    string `json:"result"`
		Provision struct {
			Fresh     bool `json:"fresh"`
			Preflight struct {
				MeasuredAt string           `json:"measured_at"`
				Kinds      []map[string]any `json:"kinds"`
			} `json:"preflight"`
		} `json:"provision"`
	}
	if b, err := os.ReadFile(filepath.Clean(p)); err != nil || json.Unmarshal(b, &rec) != nil {
		t.Fatalf("the record cannot be read back: %v", err)
	}
	pre := rec.Provision.Preflight
	if rec.Result != Pass || !rec.Provision.Fresh || pre.MeasuredAt != "2026-11-20T05:55:00Z" || len(pre.Kinds) != 11 {
		t.Fatalf("the record holds result %s and provision %+v", rec.Result, rec.Provision)
	}
	// A number that was measured is written, 0 too; one that was not is left out.
	volumes, dns := pre.Kinds[1], pre.Kinds[10]
	if len(volumes) != 4 || volumes["kind"] != "volumes" || volumes["used"] != float64(38) || volumes["allowed"] != float64(400) || volumes["leftovers"] != float64(0) ||
		len(dns) != 2 || dns["kind"] != "dns_records" || dns["leftovers"] != float64(0) {
		t.Errorf("the record holds the kinds %v and %v", volumes, dns)
	}
}
