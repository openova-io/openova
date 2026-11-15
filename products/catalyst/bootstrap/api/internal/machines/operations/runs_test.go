package operations

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

func rule(id, result string) runner.RuleResult {
	return runner.RuleResult{ID: "xccdf_org.ssgproject.content_rule_" + id, Result: result}
}

func nodesPolicy(profiles ...interface{}) *unstructured.Unstructured {
	return scanPolicy(ns, policySpec("", profiles...))
}

func (w *world) report(machine string) *unstructured.Unstructured {
	w.t.Helper()
	for _, r := range w.list(PolicyReportKind, ns) {
		if r.GetName() == HardeningReportName(machine) {
			return &r
		}
	}
	return nil
}

func target(run *v1alpha1.MachineRun, machine string) v1alpha1.RunTarget {
	for _, t := range run.Status.Targets {
		if t.Machine == ns+"/"+machine {
			return t
		}
	}
	return v1alpha1.RunTarget{}
}

// One run over two nodes with maxParallel 1: one Job at a time, each as the runner
// contract says; the report, the bill-of-materials reference and the run's end follow
// from the result documents; the finished run is deleted after its retention time.
func TestRunScansEachNodeAndRecordsTheResult(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory", "hardening"), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", false), runObject("r1"))
	run, _ := w.step("r1")
	jobs := w.jobs()
	if run.Status.Phase != v1alpha1.RunRunning || len(run.Status.Targets) != 2 || len(jobs) != 1 || target(run, "cp-1").JobName != jobs[0].Name || target(run, "cp-2").JobName != "" {
		t.Fatalf("after the first step: phase %s, targets %+v, %d Jobs", run.Status.Phase, run.Status.Targets, len(jobs))
	}
	job, pod := jobs[0], jobs[0].Spec.Template.Spec
	if job.Name != JobName(ns+"/r1", ns+"/cp-1") || job.Namespace != jobNS || job.Annotations[AnnotationRun] != ns+"/r1" ||
		*job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 3600 || *job.Spec.TTLSecondsAfterFinished != 86400 {
		t.Errorf("the Job is %s/%s with spec %+v", job.Namespace, job.Name, job.Spec)
	}
	if pod.NodeName != "cp-1" || pod.ServiceAccountName != "mr-node" || *pod.AutomountServiceAccountToken || pod.HostNetwork || pod.HostPID || pod.HostIPC ||
		pod.RestartPolicy != corev1.RestartPolicyNever || len(pod.Tolerations) != 1 || pod.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Errorf("the pod is not a node-mode runner pod: %+v", pod)
	}
	if v := pod.Volumes; len(v) != 4 || v[0].HostPath.Path != "/" || v[1].CSI.Driver != "secrets-store.csi.k8s.io" || v[1].CSI.VolumeAttributes["secretProviderClass"] != "evidence-store" ||
		v[2].EmptyDir.Medium != corev1.StorageMediumMemory || v[3].EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Errorf("the volumes are %+v", pod.Volumes)
	}
	common := "--mode=node --machine=catalyst-infra/cp-1 --run=catalyst-infra/r1 --os-family=ubuntu --os-version=22.04 --evidence-endpoint=https://s3.example " +
		"--evidence-region=region-1 --evidence-bucket=machine-evidence --evidence-prefix=catalyst-infra/cp-1/r1/ --address=10.0.0.1"
	wantArgs := map[string]string{
		"inventory": "--task=inventory " + common,
		"hardening": "--task=hardening " + common + " --profile-id=" + profile + " --content-release=0.1.82 " +
			"--disabled-rules=xccdf_org.ssgproject.content_rule_a,xccdf_org.ssgproject.content_rule_b",
	}
	if len(pod.Containers) != 2 {
		t.Fatalf("the pod has %d containers, want inventory and hardening", len(pod.Containers))
	}
	for _, c := range pod.Containers {
		sc := c.SecurityContext
		if strings.Join(c.Args, " ") != wantArgs[c.Name] || c.Image != w.runs.Settings.RunnerImage || *sc.RunAsUser != 0 || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || sc.Privileged != nil {
			t.Errorf("container %s: args %q, security %+v", c.Name, strings.Join(c.Args, " "), sc)
		}
		if _, err := runner.ParseArgs(c.Args); err != nil {
			t.Errorf("container %s: the runner refuses its arguments: %v", c.Name, err)
		}
		m := c.VolumeMounts
		if len(m) != 3 || m[0].MountPath != "/host" || !m[0].ReadOnly || m[1].MountPath != "/run/machine-runner" || m[1].Name != "work-"+c.Name || m[2].MountPath != "/var/run/secrets/evidence-store" || !m[2].ReadOnly {
			t.Errorf("container %s: mounts %+v", c.Name, m)
		}
	}
	if run, _ = w.step("r1"); len(w.jobs()) != 1 || run.Status.Phase != v1alpha1.RunRunning {
		t.Fatalf("a second step with the Job still running: %d Jobs, phase %s", len(w.jobs()), run.Status.Phase)
	}

	w.now = t0.Add(10 * time.Minute)
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1"),
		"hardening": w.hardeningDone("r1", "cp-1", true, rule("pass_b", "pass"), rule("na", "notapplicable"), rule("fail_b", "fail"), rule("err", "error"), rule("fail_a", "fail"), rule("pass_a", "fixed"))})
	run, _ = w.step("r1")
	first := target(run, "cp-1")
	wantEvidence := []string{"catalyst-infra/cp-1/r1/sbom.cdx.json", "catalyst-infra/cp-1/r1/hardening.results.json"}
	if first.Outcome != "Succeeded" || first.Message != "" || first.ReportName != "machine-cp-1-hardening" || !reflect.DeepEqual(first.Evidence, wantEvidence) || first.FinishedAt == nil || len(w.jobs()) != 2 {
		t.Fatalf("after the first Job: target %+v, %d Jobs", first, len(w.jobs()))
	}
	rep := w.report("cp-1")
	if rep == nil {
		t.Fatal("no hardening report was written")
	}
	var got []string
	results, _, _ := unstructured.NestedSlice(rep.Object, "results")
	for _, r := range results {
		m := r.(map[string]interface{})
		got = append(got, strings.TrimPrefix(m["rule"].(string), "xccdf_org.ssgproject.content_rule_")+"="+m["result"].(string))
		props, _ := m["properties"].(map[string]interface{})
		if m["source"] != "machine-hardening" || m["policy"] != profile || props["runName"] != "r1" || props["contentRelease"] != "0.1.82" || props["evidenceKey"] != wantEvidence[1] {
			t.Errorf("result %v", m)
		}
	}
	if want := "fail_a=fail fail_b=fail err=error na=skip pass_a=pass pass_b=pass"; strings.Join(got, " ") != want {
		t.Errorf("the results are %q, want worst first: %q", strings.Join(got, " "), want)
	}
	summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
	scope, _, _ := unstructured.NestedMap(rep.Object, "scope")
	owners := rep.GetOwnerReferences()
	if !reflect.DeepEqual(summary, map[string]interface{}{"pass": int64(2), "fail": int64(2), "warn": int64(0), "error": int64(1), "skip": int64(1)}) ||
		scope["kind"] != "Machine" || scope["name"] != "cp-1" || len(owners) != 1 || owners[0].Kind != "Machine" || owners[0].UID != "uid-cp-1" ||
		rep.GetLabels()[LabelReportMachine] != "cp-1" || rep.GetLabels()[LabelReport] != "hardening" || rep.GetAnnotations()[AnnotationResultsTruncated] != "" {
		t.Errorf("the report is %v", rep.Object)
	}
	mu := newObject(v1alpha1.MachineKind)
	if err := w.c.Get(context.Background(), key("cp-1").NamespacedName, mu); err != nil {
		t.Fatal(err)
	}
	m, _ := v1alpha1.MachineFromUnstructured(mu)
	if m.Status.Sbom == nil || m.Status.Sbom.Key != wantEvidence[0] || m.Status.Sbom.SHA256 != "aa" || !m.Status.Sbom.CollectedAt.Time.Equal(w.now) || m.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("Machine.status is %+v", m.Status)
	}

	w.end("r1", "cp-2", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-2"), "hardening": w.hardeningDone("r1", "cp-2", true, rule("x", "pass"))})
	run, wait := w.step("r1")
	if run.Status.Phase != v1alpha1.RunSucceeded || run.Status.Counts["Succeeded"] != 2 || !run.Status.FinishedAt.Time.Equal(w.now) || wait != 720*time.Hour {
		t.Fatalf("at the end: phase %s, counts %v, wake-up in %s", run.Status.Phase, run.Status.Counts, wait)
	}
	w.now = w.now.Add(720*time.Hour - time.Minute)
	if run, wait = w.step("r1"); run == nil || wait != time.Minute {
		t.Fatalf("one minute before the retention time: run %v, wake-up in %s", run, wait)
	}
	w.now = w.now.Add(time.Minute)
	if run, _ = w.step("r1"); run != nil {
		t.Fatal("the finished run was not deleted after its retention time")
	}
}

// A Job that ends without a parsable result document is Failed with no-result, and so
// is a Job that is gone. Nothing is written for it.
func TestRunWithoutAResultDocumentIsFailed(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory", "hardening"), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", false), runObject("r1"))
	w.step("r1")
	w.end("r1", "cp-1", "BackoffLimitExceeded", map[string]string{"inventory": "", "hardening": `{"outcome":"Succeeded"}`})
	run, _ := w.step("r1")
	if got := target(run, "cp-1"); got.Outcome != "Failed" || got.Message != "inventory: no-result; hardening: no-result" || got.ReportName != "" || len(got.Evidence) != 0 {
		t.Fatalf("a Job without a result document gave %+v", got)
	}
	job := &batchv1.Job{}
	if err := w.c.Get(context.Background(), types.NamespacedName{Namespace: jobNS, Name: target(run, "cp-2").JobName}, job); err != nil {
		t.Fatal(err)
	}
	if err := w.c.Delete(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	run, _ = w.step("r1")
	if got := target(run, "cp-2"); got.Outcome != "Failed" || got.Message != "no-result" || run.Status.Phase != v1alpha1.RunFailed || run.Status.Counts["Failed"] != 2 {
		t.Fatalf("a Job that is gone gave %+v, phase %s", got, run.Status.Phase)
	}
	if w.report("cp-1") != nil || w.report("cp-2") != nil {
		t.Fatal("a report was written for a run without a result")
	}
}

// A result that does not say completed writes no report and no bill-of-materials
// reference, whatever outcome it claims; the report of an earlier run stays.
func TestRunThatDidNotCompleteWritesNoReport(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory", "hardening"), machineObject("cp-1", "22.04", false), runObject("r1"), runObject("r2"), runObject("r3"))
	w.step("r1")
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1"), "hardening": w.hardeningDone("r1", "cp-1", true, rule("x", "fail"))})
	w.step("r1")
	before := w.report("cp-1")

	w.step("r2")
	claimed := strings.Replace(inventoryDone(t, "r2", "cp-1"), `"completed":true`, `"completed":false`, 1)
	w.end("r2", "cp-1", "", map[string]string{"inventory": claimed, "hardening": w.hardeningDone("r2", "cp-1", false, rule("x", "pass"))})
	run, _ := w.step("r2")
	if got := target(run, "cp-1"); got.Outcome != "Incomplete" || got.Message != "inventory: completion-marker-missing; hardening: completion-marker-missing" || got.ReportName != "" || len(got.Evidence) != 0 {
		t.Fatalf("a result without completed gave %+v", got)
	}
	w.step("r3")
	incomplete := encode(t, runresult.Document{Outcome: runresult.Incomplete, Message: "oscap-chroot was stopped: signal: killed"})
	w.end("r3", "cp-1", "BackoffLimitExceeded", map[string]string{"inventory": inventoryDone(t, "r3", "cp-1"), "hardening": incomplete})
	run, _ = w.step("r3")
	if got := target(run, "cp-1"); got.Outcome != "Incomplete" || got.Message != "hardening: oscap-chroot was stopped: signal: killed" || run.Status.Phase != v1alpha1.RunFailed {
		t.Fatalf("an Incomplete hardening task gave %+v, phase %s", got, run.Status.Phase)
	}
	after := w.report("cp-1")
	if after == nil || !reflect.DeepEqual(before.Object["results"], after.Object["results"]) {
		t.Fatal("a result that did not complete changed the hardening report")
	}
	mu := newObject(v1alpha1.MachineKind)
	_ = w.c.Get(context.Background(), key("cp-1").NamespacedName, mu)
	if k, _, _ := unstructured.NestedString(mu.Object, "status", "sbom", "key"); k != "catalyst-infra/cp-1/r3/sbom.cdx.json" {
		t.Fatalf("status.sbom.key = %q, want the one of the last complete inventory", k)
	}
}

// A quarantined machine is no target: the policy selects it by its labels, and no Job is
// created for it. A run that names it is outside its policy and starts nothing.
func TestQuarantinedMachineGetsNoJob(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory"), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", true),
		runObject("r1"), runObject("r2", ns+"/cp-1", ns+"/cp-2"))
	run, _ := w.step("r1")
	if len(run.Status.Targets) != 1 || run.Status.Targets[0].Machine != ns+"/cp-1" || len(w.jobs()) != 1 || w.jobs()[0].Spec.Template.Spec.NodeName != "cp-1" {
		t.Fatalf("targets %+v, %d Jobs", run.Status.Targets, len(w.jobs()))
	}
	run, wait := w.step("r2")
	if run.Status.Phase != v1alpha1.RunFailed || target(run, "cp-2").Outcome != "Refused" || target(run, "cp-2").Message != MessageOutsidePolicy ||
		target(run, "cp-1").Message != MessageNotStarted || run.Status.Counts["Refused"] != 2 || len(w.jobs()) != 1 || wait != 720*time.Hour {
		t.Fatalf("a run that names a quarantined machine: phase %s, targets %+v, %d Jobs", run.Status.Phase, run.Status.Targets, len(w.jobs()))
	}
}

// A machine that is quarantined between the start of the run and its own turn is refused
// then, and a run whose policy is absent, suspended or deleted starts nothing more.
func TestRunStaysInsideItsPolicy(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("inventory"), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", false), runObject("r1"),
		object(v1alpha1.MachineRunKind, ns, "r2", map[string]interface{}{"spec": map[string]interface{}{"kind": "scan", "policyRef": map[string]interface{}{"kind": "ScanPolicy", "name": "absent"}, "requestedBy": "a@example.org"}}))
	w.step("r1")
	mu := newObject(v1alpha1.MachineKind)
	_ = w.c.Get(context.Background(), key("cp-2").NamespacedName, mu)
	_ = unstructured.SetNestedSlice(mu.Object, []interface{}{map[string]interface{}{"type": "Quarantined", "status": "True"}}, "status", "conditions")
	if err := w.c.Status().Update(context.Background(), mu); err != nil {
		t.Fatal(err)
	}
	w.end("r1", "cp-1", "", map[string]string{"inventory": inventoryDone(t, "r1", "cp-1")})
	run, _ := w.step("r1")
	if got := target(run, "cp-2"); got.Outcome != "Refused" || got.Message != MessageNotEligible || run.Status.Phase != v1alpha1.RunPartiallyFailed || len(w.jobs()) != 1 {
		t.Fatalf("a machine quarantined during the run gave %+v, phase %s, %d Jobs", got, run.Status.Phase, len(w.jobs()))
	}
	if run, _ = w.step("r2"); run.Status.Phase != v1alpha1.RunFailed || len(run.Status.Targets) != 0 || run.Status.FinishedAt == nil {
		t.Fatalf("a run of an absent policy: %+v", run.Status)
	}

	w = newWorld(t, nil, scanPolicy(ns, func() map[string]interface{} {
		s := policySpec("", "inventory")
		s["suspended"] = true
		return s
	}()), machineObject("cp-1", "22.04", false), runObject("r3", ns+"/cp-1"))
	if run, _ = w.step("r3"); run.Status.Phase != v1alpha1.RunFailed || target(run, "cp-1").Message != MessageOutsidePolicy || len(w.jobs()) != 0 {
		t.Fatalf("a run of a suspended policy: %+v", run.Status)
	}

	w = newWorld(t, nil, nodesPolicy("inventory"), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", false), runObject("r4"))
	w.step("r4")
	_ = w.c.Delete(context.Background(), scanPolicy(ns, nil))
	if run, _ = w.step("r4"); target(run, "cp-2").Message != MessageOutsidePolicy || target(run, "cp-1").Outcome != "" || len(w.jobs()) != 1 {
		t.Fatalf("a run whose policy was deleted while it was open: %+v", run.Status)
	}
}

// A scheduled run starts each target at its own offset inside the jitter, and the
// reconciler asks to be woken exactly at the next offset.
func TestScheduledRunStartsEachTargetAtItsOffset(t *testing.T) {
	spec := policySpec("0 2 * * *", "inventory")
	spec["maxParallel"] = int64(5)
	r := runObject("r1")
	r.SetAnnotations(map[string]string{v1alpha1.AnnotationScheduledFor: "2026-12-01T02:00:00Z"})
	w := newWorld(t, nil, scanPolicy(ns, spec), machineObject("cp-1", "22.04", false), machineObject("cp-2", "22.04", false), machineObject("cp-3", "22.04", false), r)
	slot := at("2026-12-01 02:00")
	offsets := map[string]time.Duration{}
	var order []string
	for _, n := range []string{"cp-1", "cp-2", "cp-3"} {
		offsets[n] = Offset(ns+"/"+n, time.Hour)
		if offsets[n] != Offset(ns+"/"+n, time.Hour) || offsets[n] < 0 || offsets[n] >= time.Hour {
			t.Fatalf("Offset of %s is %s", n, offsets[n])
		}
		order = append(order, n)
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if offsets[order[j]] < offsets[order[i]] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	if offsets[order[0]] == offsets[order[1]] || offsets[order[1]] == offsets[order[2]] || offsets[order[0]] == 0 {
		t.Fatalf("the three test machines need three different offsets above zero, got %v", offsets)
	}
	w.now = slot
	run, wait := w.step("r1")
	if run.Status.Phase != v1alpha1.RunPending || len(w.jobs()) != 0 || wait != offsets[order[0]] {
		t.Fatalf("at the scheduled time: phase %s, %d Jobs, wake-up in %s, want %s", run.Status.Phase, len(w.jobs()), wait, offsets[order[0]])
	}
	w.now = slot.Add(offsets[order[0]])
	run, wait = w.step("r1")
	if run.Status.Phase != v1alpha1.RunRunning || len(w.jobs()) != 1 || target(run, order[0]).JobName == "" || wait != offsets[order[1]]-offsets[order[0]] {
		t.Fatalf("at the first offset: phase %s, %d Jobs, wake-up in %s", run.Status.Phase, len(w.jobs()), wait)
	}
	w.now = slot.Add(time.Hour)
	if _, wait = w.step("r1"); len(w.jobs()) != 3 || wait != 0 {
		t.Fatalf("after the jitter: %d Jobs, wake-up in %s", len(w.jobs()), wait)
	}
	if Offset("a/b", 0) != 0 {
		t.Fatal("a jitter of zero gave an offset")
	}
}

// A machine whose release has no profile in the baseline is reported as skip with the
// reason no-profile-for-release and never as pass. A missing baseline refuses the target.
func TestHardeningWithoutAProfileIsSkipNotPass(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("hardening"), machineObject("cp-1", "20.04", false), runObject("r1"))
	run, _ := w.step("r1")
	got := target(run, "cp-1")
	if run.Status.Phase != v1alpha1.RunSucceeded || got.Outcome != "Succeeded" || got.Message != "hardening: no-profile-for-release" || got.ReportName != "machine-cp-1-hardening" || got.JobName != "" || len(w.jobs()) != 0 {
		t.Fatalf("a machine without a profile gave %+v, %d Jobs", got, len(w.jobs()))
	}
	rep := w.report("cp-1")
	results, _, _ := unstructured.NestedSlice(rep.Object, "results")
	summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
	if len(results) != 1 || results[0].(map[string]interface{})["result"] != "skip" || results[0].(map[string]interface{})["rule"] != "no-profile-for-release" || summary["pass"] != int64(0) || summary["skip"] != int64(1) {
		t.Fatalf("the report of a machine without a profile is %v", rep.Object)
	}

	spec := policySpec("", "inventory", "hardening")
	spec["hardeningBaselineRef"] = map[string]interface{}{"name": "absent"}
	w = newWorld(t, nil, scanPolicy(ns, spec), machineObject("cp-1", "22.04", false), runObject("r1"))
	run, _ = w.step("r1")
	if got := target(run, "cp-1"); got.Outcome != "Refused" || got.Message != MessageBaselineNotFound || len(w.jobs()) != 0 {
		t.Fatalf("a missing baseline gave %+v, %d Jobs", got, len(w.jobs()))
	}
}

// The results file is trusted only when it matches the checksum of the result document.
// A file that cannot be fetched is tried again; a Job over its deadline is Timeout.
func TestRunChecksTheResultsFileAndTheDeadline(t *testing.T) {
	w := newWorld(t, nil, nodesPolicy("hardening"), machineObject("cp-1", "22.04", false), runObject("r1"), runObject("r2"), runObject("r3"))
	w.step("r1")
	doc := w.hardeningDone("r1", "cp-1", true, rule("x", "pass"))
	w.end("r1", "cp-1", "", map[string]string{"hardening": doc})
	w.store.fail = true
	if _, err := w.runs.Reconcile(context.Background(), key("r1")); err == nil {
		t.Fatal("a results file that cannot be fetched was not an error")
	}
	w.store.fail = false
	w.store.files["catalyst-infra/cp-1/r1/hardening.results.json"] = []byte(`{"schema":"openova.io/machine-hardening-results/v1","rules":[]}`)
	run, _ := w.step("r1")
	if got := target(run, "cp-1"); got.Outcome != "Failed" || got.Message != "hardening: results-file-invalid" || got.ReportName != "" || w.report("cp-1") != nil {
		t.Fatalf("a results file that does not match its checksum gave %+v", got)
	}
	w.step("r2")
	w.end("r2", "cp-1", "", map[string]string{"hardening": encode(t, runresult.Document{Outcome: runresult.Succeeded, Completed: true})})
	if run, _ = w.step("r2"); target(run, "cp-1").Message != "hardening: no-results-file-in-result" || target(run, "cp-1").Outcome != "Failed" {
		t.Fatalf("a hardening result without a results file gave %+v", target(run, "cp-1"))
	}
	w.step("r3")
	w.end("r3", "cp-1", batchv1.JobReasonDeadlineExceeded, nil)
	if run, _ = w.step("r3"); target(run, "cp-1").Outcome != "Timeout" || target(run, "cp-1").Message != "hardening: deadline-exceeded" {
		t.Fatalf("a Job over its deadline gave %+v", target(run, "cp-1"))
	}
}

func TestHardeningReportKeepsTheWorstTwoHundred(t *testing.T) {
	var res runner.HardeningResults
	for i := 0; i < 150; i++ {
		res.Rules = append(res.Rules, rule("p"+strings.Repeat("x", i), "pass"), rule("f"+strings.Repeat("x", i), "fail"))
	}
	m := &v1alpha1.Machine{}
	m.Namespace, m.Name = ns, "cp-1"
	rep := HardeningReport(m, "r1", res, "k", t0)
	results, _, _ := unstructured.NestedSlice(rep.Object, "results")
	fails := 0
	for _, r := range results {
		if r.(map[string]interface{})["result"] == "fail" {
			fails++
		}
	}
	summary, _, _ := unstructured.NestedMap(rep.Object, "summary")
	if len(results) != MaxReportResults || fails != 150 || rep.GetAnnotations()[AnnotationResultsTruncated] != "true" || summary["pass"] != int64(150) || summary["fail"] != int64(150) {
		t.Fatalf("%d results, %d of them fail, annotations %v, summary %v", len(results), fails, rep.GetAnnotations(), summary)
	}
}

func TestSummary(t *testing.T) {
	for want, outcomes := range map[string][]string{"Succeeded": {"Succeeded", "NoChange"}, "PartiallyFailed": {"Succeeded", "Timeout"}, "Failed": {"Refused", "Failed"}} {
		targets := []v1alpha1.RunTarget{{Outcome: outcomes[0]}, {Outcome: outcomes[1]}}
		if got, counts := Summary(targets); got != want || counts[outcomes[0]] != 1 || counts[outcomes[1]] != 1 {
			t.Errorf("Summary(%v) = %s, %v; want %s", outcomes, got, counts, want)
		}
	}
	if got, counts := Summary(nil); got != "Succeeded" || len(counts) != 0 {
		t.Errorf("Summary of no target = %s, %v", got, counts)
	}
}
