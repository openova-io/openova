package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

const dbFresh = "2026-11-30T06:00:00Z" // a day before t0

// lodash is the advisory the fixture database holds for lodash 4.17.15.
var lodash = runner.Finding{ID: "CVE-2020-8203", Package: "lodash", InstalledVersion: "4.17.15", FixedVersion: "4.17.19", Severity: "high", PublishedAt: "2020-07-15T17:15:11.797Z"}

func (w *world) put(key string, v interface{}) string {
	w.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		w.t.Fatal(err)
	}
	w.store.files[key] = data
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// matchDone stores the findings of each machine and the index of a match of run, and
// returns the result document of the match task.
func (w *world) matchDone(run, version string, findings map[string][]runner.Finding) string {
	w.t.Helper()
	index := runner.MatchIndex{Schema: runner.MatchIndexSchema, DatabaseVersion: version}
	for _, machine := range []string{"cp-1", "cp-2"} {
		vulns, ok := findings[machine]
		if !ok {
			continue
		}
		m := ns + "/" + machine
		k := runner.MatchPrefix(run) + m + ".json"
		sbom := m + "/" + run + "/" + runner.FileSBOM
		sum := w.put(k, runner.Findings{Schema: runner.FindingsSchema, Machine: m, SBOMKey: sbom, DatabaseVersion: version, Vulnerabilities: vulns})
		index.Machines = append(index.Machines, runner.MatchEntry{Machine: m, SBOMKey: sbom, FindingsKey: k, SHA256: sum})
	}
	k := runner.MatchPrefix(run) + runner.FileMatchIndex
	sum := w.put(k, index)
	return encode(w.t, runresult.Document{Outcome: runresult.Succeeded, Completed: true, Evidence: []runresult.Evidence{{Kind: runner.KindMatchIndex, Key: k, SHA256: sum}}})
}

// endMatch ends the match Job of a run the way the Job controller does.
func (w *world) endMatch(run, failedReason, message string) {
	w.t.Helper()
	job := &batchv1.Job{}
	name := MatchJobName(ns + "/" + run)
	if err := w.c.Get(context.Background(), types.NamespacedName{Namespace: jobNS, Name: name}, job); err != nil {
		w.t.Fatal(err)
	}
	cond := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	if failedReason != "" {
		cond = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: failedReason}
	}
	job.Status.Conditions = []batchv1.JobCondition{cond}
	if err := w.c.Status().Update(context.Background(), job); err != nil {
		w.t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-x", Namespace: jobNS, Labels: map[string]string{LabelJobName: name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: runner.TaskMatch,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message, FinishedAt: metav1.NewTime(w.now)}}}}}}
	if err := w.c.Create(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) vulnReport(machine string) *unstructured.Unstructured {
	w.t.Helper()
	for _, r := range w.list(PolicyReportKind, ns) {
		if r.GetName() == VulnerabilityReportName(machine) {
			return &r
		}
	}
	return nil
}

// results gives the results of a report as policy/rule=result.
func results(u *unstructured.Unstructured) []string {
	var out []string
	list, _, _ := unstructured.NestedSlice(u.Object, "results")
	for _, r := range list {
		m := r.(map[string]interface{})
		out = append(out, m["policy"].(string)+"/"+m["rule"].(string)+"="+m["result"].(string))
	}
	return out
}

func withSBOM(u *unstructured.Unstructured, run string) *unstructured.Unstructured {
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{"key": ns + "/" + u.GetName() + "/" + run + "/" + runner.FileSBOM, "sha256": "aa", "collectedAt": "2026-11-30T02:00:00Z"}, "status", "sbom")
	return u
}

func rematchObject(name string) *unstructured.Unstructured {
	u := runObject(name)
	_ = unstructured.SetNestedField(u.Object, v1alpha1.RunKindRematch, "spec", "kind")
	return u
}

// A scan run of a policy with the profile vulnerability runs the inventory, then one
// match Job over the bills of materials it stored, and stays Running until that Job has
// ended. The bill of materials with one vulnerable package in the fixture database gives
// one fail result with fixedVersion; every result names the database version, and the
// rule database-age is pass for a database of yesterday.
func TestScanRunEndsWithTheMatchAndWritesTheVulnerabilityReport(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("vulnerability"), machineObject("cp-1", "22.04", false), runObject("r1"))
	w.runs.Settings.VulnerabilityDB = "registry.example/trivy-db:2"
	w.step("r1")
	jobs := w.jobs()
	if len(jobs) != 1 || len(jobs[0].Spec.Template.Spec.Containers) != 1 || jobs[0].Spec.Template.Spec.Containers[0].Name != runner.TaskInventory {
		t.Fatalf("the profile vulnerability did not imply the inventory: %d Jobs", len(jobs))
	}
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1")})
	run, _ := w.step("r1")
	if run.Status.Phase != v1alpha1.RunRunning || run.Status.Match == nil || run.Status.Match.JobName != MatchJobName(ns+"/r1") || run.Status.Match.Outcome != "" {
		t.Fatalf("after the inventory: phase %s, match %+v", run.Status.Phase, run.Status.Match)
	}
	var job batchv1.Job
	for _, j := range w.jobs() {
		if j.Name == run.Status.Match.JobName {
			job = j
		}
	}
	pod := job.Spec.Template.Spec
	if job.Labels[LabelTask] != runner.TaskMatch || job.Annotations[AnnotationRun] != ns+"/r1" || pod.NodeName != "" || *pod.AutomountServiceAccountToken ||
		pod.HostNetwork || pod.HostPID || !*pod.SecurityContext.RunAsNonRoot || *pod.SecurityContext.RunAsUser != MatchUser || len(pod.Containers) != 1 {
		t.Fatalf("the match Job is not a central runner Job: labels %v, pod %+v", job.Labels, pod)
	}
	for _, v := range pod.Volumes {
		if v.HostPath != nil {
			t.Fatalf("the match Job mounts the host: %+v", v)
		}
	}
	c := pod.Containers[0]
	wantArgs := "--task=match --run=catalyst-infra/r1 --sbom-keys=catalyst-infra/cp-1/r1/sbom.cdx.json --evidence-endpoint=https://s3.example " +
		"--evidence-region=region-1 --evidence-bucket=machine-evidence --evidence-prefix=_match/r1/ --db-repository=registry.example/trivy-db:2"
	if strings.Join(c.Args, " ") != wantArgs || c.SecurityContext.Privileged != nil || !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("the match container: args %q, security %+v", strings.Join(c.Args, " "), c.SecurityContext)
	}
	if _, err := runner.ParseArgs(c.Args); err != nil {
		t.Fatalf("the runner refuses the arguments of the match Job: %v", err)
	}
	if run, _ = w.step("r1"); run.Status.Phase != v1alpha1.RunRunning || len(w.jobs()) != 2 {
		t.Fatalf("a step while the match runs: phase %s, %d Jobs", run.Status.Phase, len(w.jobs()))
	}

	w.now = t0.Add(20 * time.Minute)
	w.endMatch("r1", "", w.matchDone("r1", dbFresh, map[string][]runner.Finding{"cp-1": {lodash}}))
	run, _ = w.step("r1")
	if run.Status.Phase != v1alpha1.RunSucceeded || run.Status.Match.Outcome != "Succeeded" || run.Status.Match.DatabaseVersion != dbFresh || !run.Status.Match.FinishedAt.Time.Equal(w.now) {
		t.Fatalf("after the match: phase %s, match %+v", run.Status.Phase, run.Status.Match)
	}
	if got := target(run, "cp-1"); got.Outcome != "Succeeded" || !reflect.DeepEqual(got.Evidence, []string{"catalyst-infra/cp-1/r1/sbom.cdx.json", "_match/r1/catalyst-infra/cp-1.json"}) {
		t.Fatalf("the target is %+v", got)
	}
	rep := w.vulnReport("cp-1")
	if rep == nil {
		t.Fatal("no vulnerability report was written")
	}
	if got := results(rep); !reflect.DeepEqual(got, []string{"vulnerability-database/database-age=pass", "CVE-2020-8203/lodash=fail"}) {
		t.Fatalf("the results are %q", got)
	}
	list, _, _ := unstructured.NestedSlice(rep.Object, "results")
	for _, r := range list {
		m := r.(map[string]interface{})
		props := m["properties"].(map[string]interface{})
		if m["source"] != SourceVulnerability || props["databaseVersion"] != dbFresh || props["runName"] != "r1" || props["evidenceKey"] != "_match/r1/catalyst-infra/cp-1.json" {
			t.Errorf("result %v", m)
		}
	}
	fail := list[1].(map[string]interface{})
	props := fail["properties"].(map[string]interface{})
	if fail["severity"] != "high" || props["fixedVersion"] != "4.17.19" || props["installedVersion"] != "4.17.15" || props["publishedAt"] != lodash.PublishedAt {
		t.Errorf("the fail result is %v", fail)
	}
	summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
	scope, _, _ := unstructured.NestedMap(rep.Object, "scope")
	if !reflect.DeepEqual(summary, map[string]interface{}{"pass": int64(1), "fail": int64(1), "warn": int64(0), "error": int64(0), "skip": int64(0)}) ||
		scope["kind"] != "Machine" || scope["name"] != "cp-1" || rep.GetLabels()[LabelReport] != ReportVulnerability || len(rep.GetOwnerReferences()) != 1 {
		t.Errorf("the report is %v", rep.Object)
	}
}

// A rematch run starts no Job on a machine: one match Job over the newest stored bill of
// materials of each machine. A machine without one is Refused. An advisory that the last
// report held and the new findings do not is pass once.
func TestRematchRunMatchesTheStoredBillsOfMaterials(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory", "vulnerability"), withSBOM(machineObject("cp-1", "22.04", false), "r0"), machineObject("cp-2", "22.04", false), rematchObject("m1"))
	previous := VulnerabilityReport(&v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: ns, UID: "uid-cp-1"}}, "r0",
		runner.Findings{DatabaseVersion: dbFresh, Vulnerabilities: []runner.Finding{lodash, {ID: "CVE-2026-0001", Package: "openssl", Severity: "critical"}}}, "k", nil, t0)
	if err := w.c.Create(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	run, _ := w.step("m1")
	jobs := w.jobs()
	if len(jobs) != 1 || jobs[0].Name != MatchJobName(ns+"/m1") || run.Status.Phase != v1alpha1.RunRunning {
		t.Fatalf("the rematch run started %d Jobs, phase %s", len(jobs), run.Status.Phase)
	}
	if got := strings.Join(jobs[0].Spec.Template.Spec.Containers[0].Args, " "); !strings.Contains(got, "--sbom-keys=catalyst-infra/cp-1/r0/sbom.cdx.json ") || strings.Contains(got, "--db-repository") {
		t.Fatalf("the match Job args %q", got)
	}
	if got := target(run, "cp-2"); got.Outcome != "Refused" || got.Message != MessageNoSBOMStored {
		t.Fatalf("a machine without a bill of materials gave %+v", got)
	}
	w.endMatch("m1", "", w.matchDone("m1", dbFresh, map[string][]runner.Finding{"cp-1": {lodash}}))
	run, _ = w.step("m1")
	if got := target(run, "cp-1"); got.Outcome != "Succeeded" || got.ReportName != VulnerabilityReportName("cp-1") || run.Status.Phase != v1alpha1.RunPartiallyFailed {
		t.Fatalf("after the match: target %+v, phase %s", got, run.Status.Phase)
	}
	want := []string{"vulnerability-database/database-age=pass", "CVE-2020-8203/lodash=fail", "CVE-2026-0001/openssl=pass"}
	if got := results(w.vulnReport("cp-1")); !reflect.DeepEqual(got, want) {
		t.Fatalf("the results are %q, want %q", got, want)
	}
}

// A match Job that cannot fetch the database ends the run Failed with
// database-unavailable and leaves the previous report in place; once the database of
// that report is older than seven days its rule database-age turns to warn.
func TestFailedDatabaseFetchKeepsThePreviousReport(t *testing.T) {
	for name, c := range map[string]struct {
		version string
		want    string
	}{"a database of yesterday": {dbFresh, "pass"}, "a database of eight days ago": {"2026-11-23T00:59:00Z", "warn"}} {
		w := newWorld(t, nil, nodesPolicy("vulnerability"), withSBOM(machineObject("cp-1", "22.04", false), "r0"), rematchObject("m1"))
		previous := VulnerabilityReport(&v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: ns, UID: "uid-cp-1"}}, "r0",
			runner.Findings{DatabaseVersion: c.version, Vulnerabilities: []runner.Finding{lodash}}, "k", nil, t0.Add(-7*24*time.Hour))
		if err := w.c.Create(context.Background(), previous); err != nil {
			t.Fatal(err)
		}
		w.step("m1")
		w.endMatch("m1", "BackoffLimitExceeded", encode(t, runresult.Document{Outcome: runresult.Failed, Message: "database-unavailable: trivy exited 1: failed to download vulnerability DB"}))
		run, _ := w.step("m1")
		if run.Status.Phase != v1alpha1.RunFailed || !strings.HasPrefix(run.Status.Match.Message, runner.MessageDatabaseUnavailable) || run.Status.Match.Outcome != "Failed" {
			t.Fatalf("%s: phase %s, match %+v", name, run.Status.Phase, run.Status.Match)
		}
		if got := target(run, "cp-1"); got.Outcome != "Failed" || !strings.HasPrefix(got.Message, "match: database-unavailable") {
			t.Fatalf("%s: the target is %+v", name, got)
		}
		rep := w.vulnReport("cp-1")
		if got := results(rep); !reflect.DeepEqual(got, []string{"vulnerability-database/database-age=" + c.want, "CVE-2020-8203/lodash=fail"}) {
			t.Fatalf("%s: the kept report has %q", name, got)
		}
		summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
		if count(summary["warn"]) != map[string]int64{"pass": 0, "warn": 1}[c.want] {
			t.Errorf("%s: the summary is %v", name, summary)
		}
	}
}

// A scan run whose match failed is Failed too, and the run of a policy without the
// profile vulnerability starts no match Job.
func TestScanRunWithAFailedMatchIsFailed(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory", "vulnerability"), machineObject("cp-1", "22.04", false), runObject("r1"))
	w.step("r1")
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1")})
	w.step("r1")
	w.endMatch("r1", "DeadlineExceeded", "")
	run, _ := w.step("r1")
	if run.Status.Phase != v1alpha1.RunFailed || run.Status.Match.Outcome != "Timeout" || target(run, "cp-1").Outcome != "Timeout" || w.vulnReport("cp-1") != nil {
		t.Fatalf("phase %s, match %+v, target %+v", run.Status.Phase, run.Status.Match, target(run, "cp-1"))
	}

	w = newWorld(t, nil, nodesPolicy("inventory"), machineObject("cp-1", "22.04", false), runObject("r1"), rematchObject("m1"))
	w.step("r1")
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1")})
	if run, _ = w.step("r1"); run.Status.Phase != v1alpha1.RunSucceeded || run.Status.Match != nil || len(w.jobs()) != 1 {
		t.Fatalf("without the profile vulnerability: phase %s, match %+v, %d Jobs", run.Status.Phase, run.Status.Match, len(w.jobs()))
	}
	if run, _ = w.step("m1"); run.Status.Phase != v1alpha1.RunFailed || run.Status.Match != nil || len(w.jobs()) != 1 {
		t.Fatalf("a rematch run of a policy without the profile vulnerability: phase %s, match %+v, %d Jobs", run.Status.Phase, run.Status.Match, len(w.jobs()))
	}
}

// The worst first and at most two hundred, the rule database-age always first.
func TestVulnerabilityReportOrderAndCut(t *testing.T) {
	m := &v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: ns, UID: "uid-cp-1"}}
	f := runner.Findings{DatabaseVersion: "not a time", Vulnerabilities: []runner.Finding{
		{ID: "CVE-1", Package: "b", Severity: "low"}, {ID: "CVE-2", Package: "a", Severity: "critical"}, {ID: "CVE-3", Package: "c", Severity: "info"}}}
	if got := results(VulnerabilityReport(m, "r1", f, "k", nil, t0)); !reflect.DeepEqual(got, []string{"vulnerability-database/database-age=warn", "CVE-2/a=fail", "CVE-1/b=fail", "CVE-3/c=fail"}) {
		t.Fatalf("the order is %q", got)
	}
	f.Vulnerabilities = nil
	for i := 0; i < 250; i++ {
		f.Vulnerabilities = append(f.Vulnerabilities, runner.Finding{ID: "CVE-" + strings.Repeat("9", i%7+1), Package: string(rune('a'+i%26)) + strings.Repeat("x", i/26), Severity: "medium"})
	}
	rep := VulnerabilityReport(m, "r1", f, "k", nil, t0)
	summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
	if len(results(rep)) != MaxReportResults || rep.GetAnnotations()[AnnotationResultsTruncated] != "true" || count(summary["fail"]) != 250 || results(rep)[0] != "vulnerability-database/database-age=warn" {
		t.Fatalf("%d results, annotations %v, summary %v", len(results(rep)), rep.GetAnnotations(), summary)
	}
}
