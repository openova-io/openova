package v1alpha1

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestScanPolicyAndMachineRunRoundTrip(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 12, 1, 2, 0, 0, 0, time.UTC).Local()) // a decoded time is local
	parallel := int32(3)
	policy := &ScanPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: "catalyst-infra"},
		Spec: ScanPolicySpec{
			Namespaces: []string{"acme"}, MachineSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelClass: ClassNode}},
			Profiles: []string{ProfileInventory, ProfileHardening}, HardeningBaselineRef: &LocalRef{Name: "nodes"},
			Schedule: "0 2 * * *", Jitter: "2h", MaxParallel: &parallel,
		},
		Status: ScanPolicyStatus{LastRunName: "nodes-01", NextScheduleAt: &at},
	}
	pu, err := ScanPolicyToUnstructured(policy)
	if err != nil || pu.GetAPIVersion() != "machines.openova.io/v1alpha1" || pu.GetKind() != "ScanPolicy" {
		t.Fatalf("apiVersion %q kind %q: %v", pu.GetAPIVersion(), pu.GetKind(), err)
	}
	policyOut, err := ScanPolicyFromUnstructured(pu)
	if err != nil {
		t.Fatal(err)
	}
	policy.TypeMeta = policyOut.TypeMeta
	if !reflect.DeepEqual(policy, policyOut) {
		t.Fatalf("round trip changed the ScanPolicy:\n in %+v\nout %+v", policy, policyOut)
	}
	if _, err := MachineRunFromUnstructured(pu); err == nil {
		t.Fatal("a ScanPolicy was converted as a MachineRun")
	}
	run := &MachineRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes-01", Namespace: "catalyst-infra", Annotations: map[string]string{AnnotationScheduledFor: "2026-12-01T02:00:00Z"}},
		Spec:       MachineRunSpec{Kind: RunKindScan, PolicyRef: PolicyRef{Kind: "ScanPolicy", Name: "nodes"}, Targets: []string{"catalyst-infra/cp-1"}, RequestedBy: RequestedByController, Reason: "schedule 0 2 * * *"},
		Status: MachineRunStatus{Phase: RunPartiallyFailed, StartedAt: &at, FinishedAt: &at, Counts: map[string]int32{"Succeeded": 1, "Failed": 1},
			Targets: []RunTarget{{Machine: "catalyst-infra/cp-1", Outcome: "Succeeded", JobName: "mr-0", StartedAt: &at, FinishedAt: &at, Evidence: []string{"k"}, ReportName: "machine-cp-1-hardening"}},
			Match:   &RunMatch{JobName: "mr-1", Outcome: "Failed", Message: "database-unavailable", DatabaseVersion: "2026-11-30T06:00:00Z", StartedAt: &at, FinishedAt: &at}},
	}
	ru, err := MachineRunToUnstructured(run)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(ru.Object, "spec", "policyRef", "kind"); got != "ScanPolicy" || ru.GetKind() != "MachineRun" {
		t.Fatalf("kind %q, spec.policyRef.kind %q", ru.GetKind(), got)
	}
	runOut, err := MachineRunFromUnstructured(ru)
	if err != nil {
		t.Fatal(err)
	}
	run.TypeMeta = runOut.TypeMeta
	if !reflect.DeepEqual(run, runOut) {
		t.Fatalf("round trip changed the MachineRun:\n in %+v\nout %+v", run, runOut)
	}
}

func TestHardeningBaselineAndSbomFromUnstructured(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "machines.openova.io/v1alpha1", "kind": "HardeningBaseline", "metadata": map[string]interface{}{"name": "nodes", "namespace": "catalyst-infra"},
		"spec": map[string]interface{}{
			"content":   map[string]interface{}{"source": "complianceascode", "release": "0.1.82"},
			"profiles":  []interface{}{map[string]interface{}{"osFamily": "ubuntu", "osVersion": "24.04", "profileId": "xccdf_org.ssgproject.content_profile_cis_level1_server"}},
			"tailoring": map[string]interface{}{"disabledRules": []interface{}{"xccdf_org.ssgproject.content_rule_a"}},
		},
	}}
	want := HardeningBaselineSpec{
		Content:   HardeningContent{Source: "complianceascode", Release: "0.1.82"},
		Profiles:  []HardeningProfile{{OSFamily: "ubuntu", OSVersion: "24.04", ProfileID: "xccdf_org.ssgproject.content_profile_cis_level1_server"}},
		Tailoring: &Tailoring{DisabledRules: []string{"xccdf_org.ssgproject.content_rule_a"}},
	}
	if b, err := HardeningBaselineFromUnstructured(u); err != nil || !reflect.DeepEqual(b.Spec, want) {
		t.Fatalf("HardeningBaselineFromUnstructured = %+v, %v; want spec %+v", b, err, want)
	}
	at := metav1.NewTime(time.Date(2026, 12, 1, 2, 0, 0, 0, time.UTC).Local())
	m := &Machine{Spec: MachineSpec{SourceRef: SourceRef{Name: "nodes"}, ExternalID: "cp-1", Class: ClassNode}, Status: MachineStatus{Sbom: &SBOM{Key: "k", SHA256: "aa", CollectedAt: at}}}
	mu, err := MachineToUnstructured(m)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(mu.Object, "status", "sbom", "collectedAt"); got != "2026-12-01T02:00:00Z" {
		t.Fatalf("status.sbom.collectedAt = %q", got)
	}
	if back, err := MachineFromUnstructured(mu); err != nil || !reflect.DeepEqual(back.Status.Sbom, m.Status.Sbom) {
		t.Fatalf("status.sbom after the round trip: %+v, %v", back, err)
	}
}
