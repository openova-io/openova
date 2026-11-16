package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The tests of a run in self. Its kubectl gets no --kubeconfig argument, so
// the fake of fake_kubectl_test.go, which needs one, cannot stand in for it.

const selfDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// selfKubectl writes the stand-in kubectl into dir. It logs every call in
// dir/calls, answers `get <resource>` with dir/<resource>.json and keeps the
// input of `apply` in dir/applied.json.
func selfKubectl(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
d='` + dir + `'
printf '%s\n' "$*" >> "$d/calls"
if [ "$1" = apply ]; then cat > "$d/applied.json"; exit 0; fi
if [ "$1" = get ] && [ -f "$d/$2.json" ]; then cat "$d/$2.json"; exit 0; fi
echo "error: no answer for $*" >&2
exit 1
`
	hostFile(t, dir, "kubectl", 0o700, []byte(script))
	return filepath.Join(dir, "kubectl")
}

// writeAnswer stores the answer of `get <resource>`.
func writeAnswer(t *testing.T, dir, resource, list string) {
	t.Helper()
	hostFile(t, dir, resource+".json", 0o600, []byte(list))
}

// pinnedRelease is a fixture HelmRelease whose spec names want and whose
// newest deployed history entry is got ("" for none).
func pinnedRelease(namespace, name, want, got string) obj {
	hr := fakeRelease(namespace, name, false, "Ready", "True")
	hr["spec"] = obj{"chart": obj{"spec": obj{"chart": name, "version": want}}}
	if got != "" {
		hr["status"].(obj)["history"] = []obj{{"status": "deployed", "chartVersion": got}}
	}
	return hr
}

func TestReleasesBehind(t *testing.T) {
	suspended := pinnedRelease("flux-system", "bp-velero", "1.0.0", "0.9.0")
	suspended["spec"].(obj)["suspend"] = true
	unpinned := pinnedRelease("flux-system", "bp-oci", "", "2.0.0")
	var list struct{ Items []kube.Object }
	if err := json.Unmarshal([]byte(kubeList(
		pinnedRelease("flux-system", "bp-cilium", "1.4.20", "1.4.20"),
		pinnedRelease("flux-system", "bp-harbor", "1.2.0", "1.1.0"),
		pinnedRelease("gitea", "bp-gitea", "3.0.0", ""),
		suspended, unpinned)), &list); err != nil {
		t.Fatal(err)
	}
	compared, behind := releasesBehind(list.Items)
	want := []string{"flux-system/bp-harbor runs 1.1.0 and its spec names 1.2.0", "gitea/bp-gitea runs no deployed version and its spec names 3.0.0"}
	if compared != 3 || strings.Join(behind, "|") != strings.Join(want, "|") {
		t.Fatalf("compared %d, behind %q; want 3, %q", compared, behind, want)
	}
}

func TestWaitForReleases(t *testing.T) {
	for _, tc := range []struct {
		name, list, status string
	}{
		{"every release runs its version", kubeList(pinnedRelease("flux-system", "bp-cilium", "1.4.20", "1.4.20")), Pass},
		{"a release runs another version", kubeList(pinnedRelease("flux-system", "bp-cilium", "1.4.20", "1.4.19")), Fail},
		{"no release is found", kubeList(), Fail},
		{"the releases cannot be read", "", Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			region := kube.Region{ID: "region-a", Kubectl: selfKubectl(t, dir)}
			if tc.list != "" {
				writeAnswer(t, dir, hrResource, tc.list)
			}
			r := &runner{out: io.Discard, run: &Run{}}
			r.waitForReleases(context.Background(), region, 0)
			if len(r.run.Steps) != 1 || r.run.Steps[0].ID != "D0.12" || r.run.Steps[0].Status != tc.status {
				t.Fatalf("steps %+v, want one step D0.12 with status %s", r.run.Steps, tc.status)
			}
		})
	}
}

func TestBuildReport(t *testing.T) {
	finished := time.Date(2026, 11, 20, 10, 15, 0, 0, time.UTC)
	run := &Run{RunID: "20261120T101500Z-9c0d", FinishedAt: finished, Checks: []CheckResult{
		{ID: "FND-G04", StateAtRun: StateRequired, Status: Pass},
		{ID: "FND-G05", StateAtRun: StateRequired, Status: Fail, Reason: "a node port"},
		{ID: "FND-G01", StateAtRun: StateExpectedFail, Status: Fail, Reason: "a digest"},
		{ID: "FND-G08", StateAtRun: StateExpectedFail, Status: Skip, Reason: "the run does not declare multi-region"},
	}}
	b, err := json.Marshal(BuildReport("conformance", run))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		APIVersion, Kind string
		Metadata         struct {
			Name   string
			Labels map[string]string
		}
		Summary map[string]int
		Results []struct {
			Source, Policy, Rule, Result, Severity, Message string
			Timestamp                                       struct{ Seconds int64 }
			Properties                                      map[string]string
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	l, want := got.Metadata.Labels, []string{"FND-G04 pass high", "FND-G05 fail high", "FND-G01 warn low", "FND-G08 skip low"}
	if got.APIVersion != "wgpolicyk8s.io/v1alpha2" || got.Kind != "ClusterPolicyReport" || got.Metadata.Name != "conformance" ||
		len(l) != 2 || l["app.kubernetes.io/managed-by"] != "catalyst-conformance" || l["catalyst.openova.io/run-id"] != run.RunID ||
		len(got.Results) != len(want) || fmt.Sprint(got.Summary) != "map[error:0 fail:1 pass:1 skip:1 warn:1]" {
		t.Fatalf("the report is %s", b)
	}
	for i, res := range got.Results {
		if res.Policy+" "+res.Result+" "+res.Severity != want[i] || res.Rule != res.Policy || res.Source != "catalyst-conformance" ||
			res.Timestamp.Seconds != finished.Unix() || res.Properties["state"] != run.Checks[i].StateAtRun || res.Message == "" {
			t.Fatalf("result %d is %+v, want %s", i, res, want[i])
		}
	}
}

func TestSelfProducer(t *testing.T) {
	env := map[string]string{"ACCEPT_JOB_NAMESPACE": "catalyst-conformance", "ACCEPT_JOB_NAME": "conformance-1"}
	if p := selfProducer(func(k string) string { return env[k] }); p.Kind != "manual" || p.Job != nil {
		t.Fatalf("a Job without its uid gave %+v", p)
	}
}

// selfManifest writes a manifest in which FND-G04 lists self, FND-G05 lists
// cloud only and FND-G13 lists self but is destructive.
func selfManifest(t *testing.T, dir string) string {
	t.Helper()
	m := Manifest{Schema: ManifestSchema, Checks: []ManifestCheck{
		{ID: "FND-G04", State: StateExpectedFail, RunsIn: []string{"self"}, Needs: []string{}, Implementation: suiteRoot + "internal/suite/check_releases.go"},
		{ID: "FND-G05", State: StateExpectedFail, RunsIn: []string{"cloud"}, Needs: []string{}, Implementation: suiteRoot + "internal/suite/check_nodeport.go"},
		{ID: "FND-G13", State: StateExpectedFail, RunsIn: []string{"self"}, Destructive: true, Needs: []string{}, Implementation: suiteRoot + "internal/suite/check_wipe.go"},
	}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	hostFile(t, dir, "gates.json", 0o600, b)
	return filepath.Join(dir, "gates.json")
}

// TestExecuteSelf runs the suite in self against the stand-in kubectl. When
// ACCEPT_SAMPLE_RECORD_DIR is set the record stays there, so that
// `scripts/acceptance-gate.py validate-record` can prove that a record of a run
// in self and the schema of that tool agree.
func TestExecuteSelf(t *testing.T) {
	dir := t.TempDir()
	selfKubectl(t, dir) // found on the PATH as kubectl: the region of a run in self names no binary
	writeAnswer(t, dir, hrResource, kubeList(pinnedRelease("flux-system", "bp-cilium", "1.4.20", "1.4.20")))
	writeAnswer(t, dir, "namespaces", kubeList())
	writeAnswer(t, dir, "pods", kubeList(fakePod("kube-system", "cilium-1", 0, "")))
	for k, v := range map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"ACCEPT_JOB_NAMESPACE": "catalyst-conformance", "ACCEPT_JOB_NAME": "conformance-1", "ACCEPT_JOB_UID": "u-1",
		"SUITE_GIT_SHA": strings.Repeat("4be7a11c0d", 4)} {
		t.Setenv(k, v)
	}
	records := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if records == "" {
		records = t.TempDir()
	}
	cfg := Config{Env: "sample", RunIn: string(RunInSelf), Manifest: selfManifest(t, dir), RegionA: "region-a",
		SuiteImageDigest: selfDigest, ReportName: "conformance", Journeys: "J1,J2,J3,J4,J5",
		Record: true, RecordDir: records}
	run, err := Execute(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunIn != "self" || run.SuiteImageDigest != selfDigest || run.Candidate != (Candidate{}) || run.Result != Pass ||
		run.Producer.Kind != "job" || run.Producer.Job.UID != "u-1" || run.SuiteGitSHA != strings.Repeat("4be7a11c0d", 4) {
		t.Fatalf("the record of a run in self is %+v", run)
	}
	if len(run.Checks) != 1 || run.Checks[0].ID != "FND-G04" || run.Checks[0].Status != Pass || len(run.Journeys) != 0 {
		t.Fatalf("checks %+v, journeys %v: want FND-G04 alone and no journey", run.Checks, run.Journeys)
	}
	if run.Steps[0].ID != "D0.12" || run.Steps[0].Status != Pass {
		t.Fatalf("the first step is %+v, want D0.12 PASS", run.Steps[0])
	}
	calls, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "calls")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "--kubeconfig") || !strings.HasSuffix(string(calls), "apply -f -\n") {
		t.Fatalf("kubectl was called with\n%s", calls)
	}
	var report struct {
		Kind    string
		Results []struct{ Policy, Result string }
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "applied.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &report); err != nil || report.Kind != "ClusterPolicyReport" || len(report.Results) != 1 ||
		report.Results[0].Policy != "FND-G04" || report.Results[0].Result != "pass" {
		t.Fatalf("the applied report is %s (%v)", b, err)
	}
	files, err := filepath.Glob(filepath.Join(records, "sample-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no record was written in %s (%v)", records, err)
	}
}

func TestExecuteSelfRefuses(t *testing.T) {
	dir := t.TempDir()
	good := Config{Env: "sample", RunIn: string(RunInSelf), Manifest: selfManifest(t, dir), RegionA: "region-a",
		SuiteImageDigest: selfDigest, ReportName: "conformance"}
	for _, tc := range []struct {
		name, want string
		change     func(*Config)
	}{
		{"a digest that is a tag", "--suite-image-digest", func(c *Config) { c.SuiteImageDigest = "latest" }},
		{"no region", "--region-a", func(c *Config) { c.RegionA = "" }},
		{"no manifest", "--manifest", func(c *Config) { c.Manifest = "" }},
		{"no report name", "--report-name", func(c *Config) { c.ReportName = "" }},
		{"a negative wait", "--wait-for-release", func(c *Config) { c.WaitForReleaseMinutes = -1 }},
		{"a discover run", "discover", func(c *Config) { c.ReadOnly = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.change(&cfg)
			if _, err := Execute(context.Background(), cfg, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error about %s", err, tc.want)
			}
		})
	}
}
