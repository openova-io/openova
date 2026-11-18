package operations

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// Messages the run reconciler writes into status.targets[].message.
const (
	MessageOutsidePolicy    = "outside-policy"
	MessageNotStarted       = "not-started"
	MessageNotEligible      = "not-eligible"
	MessageBaselineNotFound = "baseline-not-found"
	MessageDeadline         = "deadline-exceeded"
	MessageNoMarker         = "completion-marker-missing"
	MessageNoSBOM           = "no-sbom-in-result"
	MessageNoResultsFile    = "no-results-file-in-result"
	MessageResultsInvalid   = "results-file-invalid"
	maxMessage              = 1024
)

// RunReconciler brings one MachineRun to its end. It starts one runner Job per target,
// at most spec.maxParallel of the policy at a time, reads the result document of every
// finished Job, writes the hardening report and Machine.status.sbom, and deletes the run
// when it has been finished for Settings.Retention. When the policy has the profile
// vulnerability, the run ends with one match Job over the bills of materials of its
// targets, and the vulnerability reports follow from it. A rematch run starts no Job on a
// machine: it has the match Job only. The Jobs themselves write no object.
type RunReconciler struct {
	Client   client.Client
	Reader   client.Reader // reads Jobs and their Pods from the API server, not from a cache
	Store    Store
	Settings Settings
	Now      func() time.Time
}

// SetupWithManager registers the controller: one reconcile when a MachineRun is created,
// one for every event of a runner Job, and one at each time the reconcile asked for.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("machine-run").
		For(newObject(v1alpha1.MachineRunKind), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(JobRun)).
		Complete(r)
}

// JobRun maps a runner Job to its MachineRun, named by the annotation machines.openova.io/run.
func JobRun(_ context.Context, o client.Object) []reconcile.Request {
	ns, name, ok := strings.Cut(o.GetAnnotations()[AnnotationRun], "/")
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}

// Offset is the stable start offset of one target inside the jitter of its policy: the
// first eight bytes of the SHA-256 of the target, <namespace>/<name>, modulo the jitter
// in seconds.
func Offset(target string, jitter time.Duration) time.Duration {
	seconds := uint64(jitter / time.Second) // #nosec G115 -- PlanOf refuses a negative jitter
	if seconds == 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(target))
	return time.Duration(binary.BigEndian.Uint64(sum[:8])%seconds) * time.Second // #nosec G115 -- less than the jitter
}

// Quarantined reports whether a Machine carries the condition Quarantined with status True.
func Quarantined(u *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conditions {
		if m, ok := c.(map[string]interface{}); ok && m["type"] == "Quarantined" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// Eligible reports whether a run may start a Job for a Machine: class node, phase Active
// and not quarantined.
func Eligible(u *unstructured.Unstructured, m *v1alpha1.Machine) bool {
	return m.Spec.Class == v1alpha1.ClassNode && m.Status.Phase == v1alpha1.PhaseActive && !Quarantined(u)
}

func succeeded(o runresult.Outcome) bool { return o == runresult.Succeeded || o == runresult.NoChange }

// Summary gives the phase of a run whose targets have all finished, and the number of
// targets per outcome: Succeeded when none failed, Failed when none succeeded,
// PartiallyFailed otherwise.
func Summary(targets []v1alpha1.RunTarget) (string, map[string]int32) {
	counts := map[string]int32{}
	good, bad := 0, 0
	for _, t := range targets {
		counts[t.Outcome]++
		if succeeded(runresult.Outcome(t.Outcome)) {
			good++
		} else {
			bad++
		}
	}
	switch {
	case bad == 0:
		return v1alpha1.RunSucceeded, counts
	case good == 0:
		return v1alpha1.RunFailed, counts
	}
	return v1alpha1.RunPartiallyFailed, counts
}

func final(phase string) bool {
	return phase == v1alpha1.RunSucceeded || phase == v1alpha1.RunPartiallyFailed || phase == v1alpha1.RunFailed
}

// Reconcile moves one MachineRun one step forward.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := newObject(v1alpha1.MachineRunKind)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	run, err := v1alpha1.MachineRunFromUnstructured(u)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.now()
	if final(run.Status.Phase) {
		due := now
		if run.Status.FinishedAt != nil {
			due = run.Status.FinishedAt.Add(r.Settings.Retention)
		}
		if now.Before(due) {
			return ctrl.Result{RequeueAfter: due.Sub(now)}, nil
		}
		uid := u.GetUID()
		return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, u, client.Preconditions{UID: &uid}))
	}
	policy, plan, outside, err := r.policyOf(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	stamp := metav1.NewTime(now)
	if run.Status.StartedAt == nil {
		run.Status.StartedAt = &stamp
		if err := r.freeze(ctx, run, policy, plan, outside, stamp); err != nil {
			return ctrl.Result{}, err
		}
	}
	scheduled, _ := time.Parse(time.RFC3339, run.Annotations[v1alpha1.AnnotationScheduledFor])
	dueAt := func(t *v1alpha1.RunTarget) time.Time {
		if scheduled.IsZero() {
			return time.Time{}
		}
		return scheduled.Add(Offset(t.Machine, plan.Jitter))
	}
	running := 0
	var waiting []*v1alpha1.RunTarget
	for i := range run.Status.Targets {
		t := &run.Status.Targets[i]
		switch {
		case t.Outcome != "":
		case t.JobName == "" && outside != "":
			t.Outcome, t.Message, t.FinishedAt = string(runresult.Refused), MessageOutsidePolicy, &stamp
		case run.Spec.Kind == v1alpha1.RunKindRematch:
			// A rematch target starts no Job: it ends with the match.
		case t.JobName == "":
			waiting = append(waiting, t)
		default:
			done, err := r.collect(ctx, run, t, stamp)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				running++
			}
		}
	}
	sort.SliceStable(waiting, func(i, j int) bool { return dueAt(waiting[i]).Before(dueAt(waiting[j])) })
	var wake time.Time
	for _, t := range waiting {
		if at := dueAt(t); at.After(now) {
			wake = at
			break
		}
		if running >= plan.MaxParallel {
			break
		}
		started, err := r.start(ctx, run, policy, t, stamp)
		if err != nil {
			return ctrl.Result{}, err
		}
		if started {
			running++
		}
	}
	nodeWork := false
	for _, t := range run.Status.Targets {
		nodeWork = nodeWork || (t.Outcome == "" && run.Spec.Kind != v1alpha1.RunKindRematch)
	}
	matching := false
	if !final(run.Status.Phase) && outside == "" && !nodeWork && NeedsMatch(policy) {
		done, err := r.match(ctx, run, stamp)
		if err != nil {
			return ctrl.Result{}, err
		}
		matching = !done
	}
	open, begun := 0, run.Status.Match != nil
	for _, t := range run.Status.Targets {
		if t.Outcome == "" {
			open++
		}
		begun = begun || t.Outcome != "" || t.JobName != ""
	}
	switch {
	case final(run.Status.Phase):
	case open == 0 && !matching:
		run.Status.Phase, run.Status.Counts = Summary(run.Status.Targets)
		run.Status.FinishedAt = &stamp
	case !begun:
		run.Status.Phase = v1alpha1.RunPending
	default:
		run.Status.Phase = v1alpha1.RunRunning
	}
	obj, err := v1alpha1.MachineRunToUnstructured(run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !reflect.DeepEqual(obj.Object["status"], u.Object["status"]) {
		if err := r.Client.Status().Update(ctx, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("update the status of MachineRun %s/%s: %w", run.Namespace, run.Name, err)
		}
	}
	switch {
	case final(run.Status.Phase):
		return ctrl.Result{RequeueAfter: r.Settings.Retention}, nil
	case !wake.IsZero():
		return ctrl.Result{RequeueAfter: wake.Sub(now)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *RunReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// policyOf reads the ScanPolicy of a run. outside is not empty when the run is outside
// its policy: its kind does not fit, the policy is absent, invalid or suspended, or the
// run is a rematch run and the policy has no profile vulnerability.
func (r *RunReconciler) policyOf(ctx context.Context, run *v1alpha1.MachineRun) (p *v1alpha1.ScanPolicy, plan PolicyPlan, outside string, err error) {
	if (run.Spec.Kind != v1alpha1.RunKindScan && run.Spec.Kind != v1alpha1.RunKindRematch) || run.Spec.PolicyRef.Kind != v1alpha1.ScanPolicyKind.Kind {
		return nil, plan, "the kind of the run does not fit the kind of its policy", nil
	}
	u := newObject(v1alpha1.ScanPolicyKind)
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.PolicyRef.Name}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, plan, "the policy does not exist", nil
		}
		return nil, plan, "", err
	}
	if p, err = v1alpha1.ScanPolicyFromUnstructured(u); err != nil {
		return nil, plan, "", err
	}
	if plan, err = PlanOf(p); err != nil {
		return p, plan, err.Error(), nil
	}
	if p.Spec.Suspended {
		return p, plan, "the policy is suspended", nil
	}
	if run.Spec.Kind == v1alpha1.RunKindRematch && !NeedsMatch(p) {
		return p, plan, "a rematch run needs a policy with the profile vulnerability", nil
	}
	return p, plan, "", nil
}

// freeze writes the targets of a run once: the Machines the policy selects now, or
// spec.targets when every one of them is selected. A run outside its policy gets every
// requested target as Refused and the phase Failed, and starts nothing.
func (r *RunReconciler) freeze(ctx context.Context, run *v1alpha1.MachineRun, policy *v1alpha1.ScanPolicy, plan PolicyPlan, reason string, at metav1.Time) error {
	names, outside := run.Spec.Targets, map[string]bool{}
	if reason != "" {
		ctrl.LoggerFrom(ctx).Info("the run is outside its policy", "reason", reason)
		for _, n := range names {
			outside[n] = true
		}
	} else {
		selector, _ := metav1.LabelSelectorAsSelector(&policy.Spec.MachineSelector)
		selected := map[string]bool{}
		for _, ns := range plan.Namespaces {
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(v1alpha1.MachineListKind)
			if err := r.Client.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
				return fmt.Errorf("list Machines of %s: %w", ns, err)
			}
			for i := range list.Items {
				if m, err := v1alpha1.MachineFromUnstructured(&list.Items[i]); err == nil && Eligible(&list.Items[i], m) {
					selected[m.Namespace+"/"+m.Name] = true
				}
			}
		}
		if len(names) == 0 {
			for n := range selected {
				names = append(names, n)
			}
			sort.Strings(names)
		}
		for _, n := range names {
			if !selected[n] {
				outside[n] = true
			}
		}
	}
	run.Status.Targets = nil
	for _, n := range names {
		t := v1alpha1.RunTarget{Machine: n}
		if len(outside) > 0 {
			t.Outcome, t.Message, t.FinishedAt = string(runresult.Refused), MessageNotStarted, &at
			if outside[n] {
				t.Message = MessageOutsidePolicy
			}
		}
		run.Status.Targets = append(run.Status.Targets, t)
	}
	if reason != "" || len(outside) > 0 {
		_, run.Status.Counts = Summary(run.Status.Targets)
		run.Status.Phase, run.Status.FinishedAt = v1alpha1.RunFailed, &at
	}
	return nil
}

// machine reads one Machine named <namespace>/<name>. A Machine that is gone gives nil.
func (r *RunReconciler) machine(ctx context.Context, target string) (*v1alpha1.Machine, *unstructured.Unstructured, error) {
	ns, name, _ := strings.Cut(target, "/")
	u := newObject(v1alpha1.MachineKind)
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		return nil, nil, client.IgnoreNotFound(err)
	}
	m, err := v1alpha1.MachineFromUnstructured(u)
	return m, u, err
}

// start creates the Job of one target, or finishes the target when no Job is needed. It
// reports whether a Job now runs.
func (r *RunReconciler) start(ctx context.Context, run *v1alpha1.MachineRun, policy *v1alpha1.ScanPolicy, t *v1alpha1.RunTarget, at metav1.Time) (bool, error) {
	refuse := func(message string) (bool, error) {
		t.Outcome, t.Message, t.FinishedAt = string(runresult.Refused), message, &at
		return false, nil
	}
	m, u, err := r.machine(ctx, t.Machine)
	if err != nil {
		return false, err
	}
	if m == nil || !Eligible(u, m) {
		return refuse(MessageNotEligible)
	}
	var tasks []Task
	for _, profile := range []string{v1alpha1.ProfileInventory, v1alpha1.ProfileHardening} {
		if !slices.Contains(policy.Spec.Profiles, profile) && (profile != v1alpha1.ProfileInventory || !NeedsMatch(policy)) {
			continue
		}
		if profile == v1alpha1.ProfileInventory {
			tasks = append(tasks, Task{Name: runner.TaskInventory, Args: r.Settings.TaskArgs(runner.TaskInventory, run, m, "", nil)})
			continue
		}
		bu := newObject(v1alpha1.HardeningBaselineKind)
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: policy.Spec.HardeningBaselineRef.Name}, bu); err != nil {
			if apierrors.IsNotFound(err) {
				return refuse(MessageBaselineNotFound)
			}
			return false, err
		}
		baseline, err := v1alpha1.HardeningBaselineFromUnstructured(bu)
		if err != nil {
			return false, err
		}
		if id := ProfileFor(baseline, m); id != "" {
			tasks = append(tasks, Task{Name: runner.TaskHardening, Args: r.Settings.TaskArgs(runner.TaskHardening, run, m, id, baseline)})
			continue
		}
		res := runner.HardeningResults{ContentRelease: baseline.Spec.Content.Release, Reason: runner.MessageNoProfile}
		if err := r.putReport(ctx, HardeningReport(m, run.Name, res, "", at.Time)); err != nil {
			return false, err
		}
		t.ReportName, t.Message = HardeningReportName(m.Name), runner.TaskHardening+": "+runner.MessageNoProfile
	}
	t.StartedAt = &at
	if len(tasks) == 0 {
		t.Outcome, t.FinishedAt = string(runresult.Succeeded), &at
		return false, nil
	}
	job := r.Settings.NodeJob(run, m, tasks)
	if err := r.Client.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("create Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	t.JobName = job.Name
	return true, nil
}

// ProfileFor returns the profile of a baseline for the operating-system release of a
// Machine: the entry whose family and version equal the Machine's. No entry gives "".
func ProfileFor(b *v1alpha1.HardeningBaseline, m *v1alpha1.Machine) string {
	if m.Status.OS == nil {
		return ""
	}
	for _, p := range b.Spec.Profiles {
		if p.OSFamily == m.Status.OS.Family && p.OSVersion == m.Status.OS.Version {
			return p.ProfileID
		}
	}
	return ""
}

// collect reads the Job of a started target. While the Job runs it reports false. When
// the Job has ended, or is gone, it fills the target from the result document of every
// container and reports true. A container without a parsable document is Failed with
// no-result; a document that says success without completed is Incomplete.
func (r *RunReconciler) collect(ctx context.Context, run *v1alpha1.MachineRun, t *v1alpha1.RunTarget, at metav1.Time) (bool, error) {
	finish := func(outcome runresult.Outcome, messages []string) (bool, error) {
		message := strings.Join(strings.Fields(strings.Join(messages, "; ")), " ")
		if len(message) > maxMessage {
			message = message[:maxMessage]
		}
		t.Outcome, t.Message, t.FinishedAt = string(outcome), strings.ToValidUTF8(message, ""), &at
		return true, nil
	}
	job := &batchv1.Job{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: r.Settings.JobNamespace, Name: t.JobName}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return finish(runresult.Failed, []string{runresult.MessageNoResult})
		}
		return false, err
	}
	ended, deadline := jobEnded(job)
	if !ended {
		return false, nil
	}
	terminated, err := r.terminated(ctx, job)
	if err != nil {
		return false, err
	}
	m, _, err := r.machine(ctx, t.Machine)
	if err != nil {
		return false, err
	}
	outcome, messages := runresult.Succeeded, []string{}
	if t.Message != "" {
		messages = append(messages, t.Message)
	}
	for _, c := range job.Spec.Template.Spec.Containers {
		doc, collected := runresult.OrNoResult(nil), at
		if state := terminated[c.Name]; state != nil {
			doc, collected = runresult.OrNoResult([]byte(state.Message)), state.FinishedAt
		}
		switch {
		case deadline && !succeeded(doc.Outcome):
			doc = runresult.Document{Outcome: runresult.Timeout, Message: MessageDeadline}
		case succeeded(doc.Outcome) && !doc.Completed:
			doc = runresult.Document{Outcome: runresult.Incomplete, Message: MessageNoMarker}
		case succeeded(doc.Outcome) && c.Name == runner.TaskInventory:
			err = r.writeSBOM(ctx, m, &doc, collected)
		case succeeded(doc.Outcome) && c.Name == runner.TaskHardening:
			err = r.writeHardening(ctx, run, m, t, &doc, at)
		}
		if err != nil {
			return false, err
		}
		if succeeded(doc.Outcome) {
			for _, e := range doc.Evidence {
				t.Evidence = append(t.Evidence, e.Key)
			}
		} else if succeeded(outcome) {
			outcome = doc.Outcome
		}
		if doc.Message != "" {
			messages = append(messages, c.Name+": "+doc.Message)
		}
	}
	return finish(outcome, messages)
}

func evidenceOf(doc *runresult.Document, kind string) *runresult.Evidence {
	for i := range doc.Evidence {
		if doc.Evidence[i].Kind == kind {
			return &doc.Evidence[i]
		}
	}
	return nil
}

// writeSBOM sets Machine.status.sbom to the bill of materials of a finished inventory
// task. A document without one turns to Failed.
func (r *RunReconciler) writeSBOM(ctx context.Context, m *v1alpha1.Machine, doc *runresult.Document, at metav1.Time) error {
	e := evidenceOf(doc, runner.KindSBOM)
	if e == nil {
		*doc = runresult.Document{Outcome: runresult.Failed, Message: MessageNoSBOM}
		return nil
	}
	if m == nil {
		return nil
	}
	patch, err := json.Marshal(map[string]interface{}{"status": map[string]interface{}{"sbom": v1alpha1.SBOM{Key: e.Key, SHA256: e.SHA256, CollectedAt: at}}})
	if err != nil {
		return err
	}
	u := newObject(v1alpha1.MachineKind)
	u.SetNamespace(m.Namespace)
	u.SetName(m.Name)
	if err := r.Client.Status().Patch(ctx, u, client.RawPatch(types.MergePatchType, patch)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("patch the status of Machine %s/%s: %w", m.Namespace, m.Name, err)
	}
	return nil
}

// writeHardening writes the hardening report of a finished hardening task from the file
// hardening.results.json in the evidence bucket. A document without that file, or a file
// that does not match its checksum or its schema, turns the document to Failed and
// leaves the report as it was. A file that cannot be fetched is an error, tried again.
func (r *RunReconciler) writeHardening(ctx context.Context, run *v1alpha1.MachineRun, m *v1alpha1.Machine, t *v1alpha1.RunTarget, doc *runresult.Document, at metav1.Time) error {
	e := evidenceOf(doc, runner.KindHardeningResults)
	if e == nil {
		*doc = runresult.Document{Outcome: runresult.Failed, Message: MessageNoResultsFile}
		return nil
	}
	data, err := r.Store.Get(ctx, e.Key)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", e.Key, err)
	}
	var res runner.HardeningResults
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != e.SHA256 || json.Unmarshal(data, &res) != nil || res.Schema != runner.HardeningResultsSchema {
		*doc = runresult.Document{Outcome: runresult.Failed, Message: MessageResultsInvalid}
		return nil
	}
	if m == nil {
		return nil
	}
	if err := r.putReport(ctx, HardeningReport(m, run.Name, res, e.Key, at.Time)); err != nil {
		return err
	}
	t.ReportName = HardeningReportName(m.Name)
	return nil
}

// putReport creates a report or replaces the one of the same name.
func (r *RunReconciler) putReport(ctx context.Context, report *unstructured.Unstructured) error {
	old := newObject(PolicyReportKind)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(report), old)
	switch {
	case apierrors.IsNotFound(err):
		err = r.Client.Create(ctx, report)
	case err == nil:
		report.SetResourceVersion(old.GetResourceVersion())
		err = r.Client.Update(ctx, report)
	}
	if err != nil {
		return fmt.Errorf("write PolicyReport %s/%s: %w", report.GetNamespace(), report.GetName(), err)
	}
	return nil
}
