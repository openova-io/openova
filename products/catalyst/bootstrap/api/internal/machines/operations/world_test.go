package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

const (
	ns      = PlatformNamespace
	jobNS   = "machine-ops"
	profile = "xccdf_org.ssgproject.content_profile_cis_level1_server"
)

var t0 = at("2026-12-01 01:00")

// memStore is an evidence bucket in memory. fail makes every Get fail.
type memStore struct {
	files map[string][]byte
	fail  bool
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	if data, ok := s.files[key]; ok && !s.fail {
		return data, nil
	}
	return nil, errors.New("the object cannot be read")
}

// world is a fake API server (controller-runtime's fake client; no server binary) with
// the baseline "nodes" and the policy "nodes", which selects the Machines of class node.
type world struct {
	t     *testing.T
	c     client.Client
	now   time.Time
	store *memStore
	sched *Scheduler
	runs  *RunReconciler
}

func object(gvk schema.GroupVersionKind, namespace, name string, content map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: content}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(types.UID("uid-" + name))
	u.SetGeneration(1)
	return u
}

func policySpec(schedule string, profiles ...interface{}) map[string]interface{} {
	return map[string]interface{}{
		"machineSelector": map[string]interface{}{"matchLabels": map[string]interface{}{v1alpha1.LabelClass: v1alpha1.ClassNode}},
		"profiles":        profiles, "hardeningBaselineRef": map[string]interface{}{"name": "nodes"},
		"schedule": schedule, "jitter": "1h", "maxParallel": int64(1),
	}
}

func machineObject(name, version string, quarantined bool) *unstructured.Unstructured {
	status := map[string]interface{}{"phase": v1alpha1.PhaseActive, "primaryAddress": "10.0.0.1", "os": map[string]interface{}{"family": "ubuntu", "version": version}}
	if quarantined {
		status["conditions"] = []interface{}{map[string]interface{}{"type": "Quarantined", "status": "True", "reason": "DuplicateIdentity"}}
	}
	u := object(v1alpha1.MachineKind, ns, name, map[string]interface{}{
		"spec":   map[string]interface{}{"sourceRef": map[string]interface{}{"name": "nodes"}, "externalId": name, "class": v1alpha1.ClassNode},
		"status": status,
	})
	u.SetLabels(map[string]string{v1alpha1.LabelClass: v1alpha1.ClassNode})
	return u
}

func runObject(name string, targets ...interface{}) *unstructured.Unstructured {
	spec := map[string]interface{}{"kind": "scan", "policyRef": map[string]interface{}{"kind": "ScanPolicy", "name": "nodes"}, "requestedBy": "controller"}
	if len(targets) > 0 {
		spec["targets"] = targets
	}
	return object(v1alpha1.MachineRunKind, ns, name, map[string]interface{}{"spec": spec})
}

func newWorld(t *testing.T, funcs *interceptor.Funcs, objects ...client.Object) *world {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kinds := []schema.GroupVersionKind{v1alpha1.MachineKind, v1alpha1.ScanPolicyKind, v1alpha1.HardeningBaselineKind, v1alpha1.MachineRunKind, PolicyReportKind}
	for _, k := range kinds {
		scheme.AddKnownTypeWithName(k, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(k.GroupVersion().WithKind(k.Kind+"List"), &unstructured.UnstructuredList{})
	}
	baseline := object(v1alpha1.HardeningBaselineKind, ns, "nodes", map[string]interface{}{"spec": map[string]interface{}{
		"content":   map[string]interface{}{"source": "complianceascode", "release": "0.1.82"},
		"profiles":  []interface{}{map[string]interface{}{"osFamily": "ubuntu", "osVersion": "22.04", "profileId": profile}},
		"tailoring": map[string]interface{}{"disabledRules": []interface{}{"xccdf_org.ssgproject.content_rule_a", "xccdf_org.ssgproject.content_rule_b"}},
	}})
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline).WithObjects(objects...).
		WithStatusSubresource(newObject(v1alpha1.MachineKind), newObject(v1alpha1.ScanPolicyKind), newObject(v1alpha1.MachineRunKind))
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	w := &world{t: t, c: b.Build(), now: t0, store: &memStore{files: map[string][]byte{}}}
	clock := func() time.Time { return w.now }
	w.sched = &Scheduler{Client: w.c, Now: clock}
	w.runs = &RunReconciler{Client: w.c, Reader: w.c, Store: w.store, Now: clock, Settings: Settings{
		JobNamespace: jobNS, RunnerImage: "registry.example/machine-runner@sha256:0", EvidenceEndpoint: "https://s3.example",
		EvidenceRegion: "region-1", EvidenceBucket: "machine-evidence", Retention: 720 * time.Hour,
	}}
	return w
}

func key(name string) ctrl.Request { return ctrlKey(ns, name) }

func ctrlKey(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

// schedule reconciles the policy "nodes" and returns the time the scheduler asked for.
func (w *world) schedule() time.Duration {
	w.t.Helper()
	res, err := w.sched.Reconcile(context.Background(), key("nodes"))
	if err != nil {
		w.t.Fatalf("reconcile the policy: %v", err)
	}
	return res.RequeueAfter
}

// step reconciles one run and returns it with the time the reconciler asked for.
func (w *world) step(name string) (*v1alpha1.MachineRun, time.Duration) {
	w.t.Helper()
	res, err := w.runs.Reconcile(context.Background(), key(name))
	if err != nil {
		w.t.Fatalf("reconcile the run: %v", err)
	}
	for _, u := range w.list(v1alpha1.MachineRunKind, ns) {
		if u.GetName() == name {
			run, err := v1alpha1.MachineRunFromUnstructured(&u)
			if err != nil {
				w.t.Fatal(err)
			}
			return run, res.RequeueAfter
		}
	}
	return nil, res.RequeueAfter
}

// policy returns the one ScanPolicy of a namespace.
func (w *world) policy(namespace string) *v1alpha1.ScanPolicy {
	w.t.Helper()
	p, err := v1alpha1.ScanPolicyFromUnstructured(&w.list(v1alpha1.ScanPolicyKind, namespace)[0])
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) list(kind schema.GroupVersionKind, namespace string) []unstructured.Unstructured {
	w.t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(kind.GroupVersion().WithKind(kind.Kind + "List"))
	if err := w.c.List(context.Background(), list, client.InNamespace(namespace)); err != nil {
		w.t.Fatal(err)
	}
	return list.Items
}

func (w *world) jobs() []batchv1.Job {
	w.t.Helper()
	list := &batchv1.JobList{}
	if err := w.c.List(context.Background(), list, client.InNamespace(jobNS)); err != nil {
		w.t.Fatal(err)
	}
	return list.Items
}

// end ends the Job of one target the way the Job controller does: the condition
// Complete or Failed (with a reason), and one Pod whose containers ended with the given
// termination messages. A nil messages map leaves the Job without a Pod.
func (w *world) end(run, machine string, failedReason string, messages map[string]string) {
	w.t.Helper()
	job := &batchv1.Job{}
	name := JobName(ns+"/"+run, ns+"/"+machine)
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
	if messages == nil {
		return
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-x", Namespace: jobNS, Labels: map[string]string{LabelJobName: name}}}
	for container, message := range messages {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: container,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message, FinishedAt: metav1.NewTime(w.now)}}})
	}
	if err := w.c.Create(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
}

func encode(t *testing.T, d runresult.Document) string {
	t.Helper()
	out, err := runresult.Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// inventoryDone is the document of an inventory task that stored its bill of materials.
func inventoryDone(t *testing.T, run, machine string) string {
	return encode(t, runresult.Document{Outcome: runresult.Succeeded, Completed: true,
		Evidence: []runresult.Evidence{{Kind: runner.KindSBOM, Key: ns + "/" + machine + "/" + run + "/" + runner.FileSBOM, SHA256: "aa"}}})
}

// hardeningDone puts a results file with the given rules into the bucket and returns the
// document of the hardening task that stored it.
func (w *world) hardeningDone(run, machine string, completed bool, rules ...runner.RuleResult) string {
	w.t.Helper()
	k := ns + "/" + machine + "/" + run + "/" + runner.FileHardeningResults
	data, err := json.Marshal(runner.HardeningResults{Schema: runner.HardeningResultsSchema, ProfileID: profile, ContentRelease: "0.1.82", Rules: rules})
	if err != nil {
		w.t.Fatal(err)
	}
	w.store.files[k] = data
	sum := sha256.Sum256(data)
	return encode(w.t, runresult.Document{Outcome: runresult.Succeeded, Completed: completed,
		Hardening: &runresult.Hardening{ProfileID: profile, ContentRelease: "0.1.82", ResultsKey: k},
		Evidence:  []runresult.Evidence{{Kind: runner.KindHardeningResults, Key: k, SHA256: hex.EncodeToString(sum[:])}}})
}
