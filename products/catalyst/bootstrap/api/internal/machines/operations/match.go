package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// The fixed parts of the match Job and the messages of a match.
const (
	LabelTask           = "machines.openova.io/task"
	MatchUser           = 65534
	MatchWorkLimit      = "4Gi"
	MessageNoSBOMStored = "no-sbom-stored"
	MessageNoIndex      = "no-index-in-result"
	MessageNoFindings   = "no-findings-file"
)

// NeedsMatch reports whether the runs of a policy end with a match: its profiles
// contain vulnerability.
func NeedsMatch(p *v1alpha1.ScanPolicy) bool {
	return p != nil && slices.Contains(p.Spec.Profiles, v1alpha1.ProfileVulnerability)
}

// MatchJobName is the name of the one match Job of a run.
func MatchJobName(run string) string { return JobName(run, runner.TaskMatch) }

// MatchJob builds the match Job of a run: one container that runs the task match on the
// given bills of materials, as user 65534 with a read-only root file system, no host
// mount, no node and no token of the Kubernetes API; a disk volume of its own as the
// work folder and the evidence binding as a CSI volume. Its Pods carry the label
// machines.openova.io/task=match, by which a network policy of the namespace selects
// them: they need the object store, DNS and the host of the database repository only.
func (s Settings) MatchJob(run *v1alpha1.MachineRun, keys []string) *batchv1.Job {
	no, yes, user := false, true, int64(MatchUser)
	backoff, deadline, ttl := int32(0), int64(JobDeadlineSeconds), int32(JobTTLSeconds)
	limit := resource.MustParse(MatchWorkLimit)
	labels := map[string]string{"app.kubernetes.io/name": "machine-runner", LabelTask: runner.TaskMatch}
	annotations := map[string]string{AnnotationRun: run.Namespace + "/" + run.Name}
	args := []string{
		"--task=" + runner.TaskMatch, "--run=" + run.Namespace + "/" + run.Name, "--sbom-keys=" + strings.Join(keys, ","),
		"--evidence-endpoint=" + s.EvidenceEndpoint, "--evidence-region=" + s.EvidenceRegion, "--evidence-bucket=" + s.EvidenceBucket,
		"--evidence-prefix=" + runner.MatchPrefix(run.Name),
	}
	if s.VulnerabilityDB != "" {
		args = append(args, "--db-repository="+s.VulnerabilityDB)
	}
	pod := corev1.PodSpec{
		ServiceAccountName:           NodeServiceAccount,
		AutomountServiceAccountToken: &no,
		RestartPolicy:                corev1.RestartPolicyNever,
		EnableServiceLinks:           &no,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &user, RunAsGroup: &user, FSGroup: &user,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Volumes: []corev1.Volume{
			{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}},
			{Name: "evidence-store", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
				Driver: "secrets-store.csi.k8s.io", ReadOnly: &yes,
				VolumeAttributes: map[string]string{"secretProviderClass": EvidenceProviderClass},
			}}},
		},
		Containers: []corev1.Container{{
			Name: runner.TaskMatch, Image: s.RunnerImage, Args: args,
			Env:                    []corev1.EnvVar{{Name: "HOME", Value: WorkMountPath}, {Name: "TMPDIR", Value: WorkMountPath}},
			TerminationMessagePath: corev1.TerminationMessagePathDefault,
			SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: &yes, AllowPrivilegeEscalation: &no,
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "work", MountPath: WorkMountPath},
				{Name: "evidence-store", MountPath: EvidenceMountPath, ReadOnly: true},
			},
		}},
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: MatchJobName(run.Namespace + "/" + run.Name), Namespace: s.JobNamespace, Labels: labels, Annotations: annotations},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}, Spec: pod},
		},
	}
}

// sbomKey is the bill of materials a scan target stored in this run, or "".
func sbomKey(t *v1alpha1.RunTarget) string {
	for _, k := range t.Evidence {
		if m, ok := runner.SBOMMachine(k); ok && m == t.Machine {
			return k
		}
	}
	return ""
}

// matched reports whether a target is part of the match of its run: a scan target that
// stored a bill of materials, or a rematch target that has not ended.
func matched(run *v1alpha1.MachineRun, t *v1alpha1.RunTarget) bool {
	if run.Spec.Kind == v1alpha1.RunKindRematch {
		return t.Outcome == ""
	}
	return sbomKey(t) != ""
}

// match brings the match of a run one step forward and reports whether it has ended. The
// first step creates the Job; a rematch target whose Machine has no stored bill of
// materials ends Refused with no-sbom-stored, and a run with nothing to match ends
// without a Job. The step after the Job has ended writes the reports.
func (r *RunReconciler) match(ctx context.Context, run *v1alpha1.MachineRun, at metav1.Time) (bool, error) {
	m := run.Status.Match
	if m == nil {
		var keys []string
		for i := range run.Status.Targets {
			t := &run.Status.Targets[i]
			if run.Spec.Kind != v1alpha1.RunKindRematch {
				if k := sbomKey(t); k != "" {
					keys = append(keys, k)
				}
				continue
			}
			if t.Outcome != "" {
				continue
			}
			mach, _, err := r.machine(ctx, t.Machine)
			if err != nil {
				return false, err
			}
			if mach == nil || mach.Status.Sbom == nil || mach.Status.Sbom.Key == "" {
				t.Outcome, t.Message, t.StartedAt, t.FinishedAt = string(runresult.Refused), MessageNoSBOMStored, &at, &at
				continue
			}
			keys, t.StartedAt = append(keys, mach.Status.Sbom.Key), &at
		}
		if len(keys) == 0 {
			return true, nil
		}
		job := r.Settings.MatchJob(run, keys)
		if err := r.Client.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, fmt.Errorf("create Job %s/%s: %w", job.Namespace, job.Name, err)
		}
		run.Status.Match = &v1alpha1.RunMatch{JobName: job.Name, StartedAt: &at}
		return false, nil
	}
	if m.Outcome != "" {
		return true, nil
	}
	job := &batchv1.Job{}
	doc := runresult.Document{Outcome: runresult.Failed, Message: runresult.MessageNoResult}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: r.Settings.JobNamespace, Name: m.JobName}, job)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return false, err
	default:
		ended, deadline := jobEnded(job)
		if !ended {
			return false, nil
		}
		terminated, err := r.terminated(ctx, job)
		if err != nil {
			return false, err
		}
		if state := terminated[runner.TaskMatch]; state != nil {
			doc = runresult.OrNoResult([]byte(state.Message))
		}
		switch {
		case deadline && !succeeded(doc.Outcome):
			doc = runresult.Document{Outcome: runresult.Timeout, Message: MessageDeadline}
		case succeeded(doc.Outcome) && !doc.Completed:
			doc = runresult.Document{Outcome: runresult.Incomplete, Message: MessageNoMarker}
		}
	}
	return true, r.applyMatch(ctx, run, doc, at)
}

// readChecked reads one file of the evidence bucket and decodes it when its SHA-256 is
// sum. A file that cannot be fetched is an error, tried again; a file that does not
// match its checksum or does not decode gives false.
func (r *RunReconciler) readChecked(ctx context.Context, key, sum string, into interface{}) (bool, error) {
	data, err := r.Store.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("fetch %s: %w", key, err)
	}
	got := sha256.Sum256(data)
	return hex.EncodeToString(got[:]) == sum && json.Unmarshal(data, into) == nil, nil
}

// applyMatch records the end of a match. A match that succeeded writes the vulnerability
// report of every machine that has a findings file. A machine without one keeps its
// previous report, whose result database-age turns to warn once its database is older
// than MaxDatabaseAge; its target ends Failed with the reason after "match: ".
func (r *RunReconciler) applyMatch(ctx context.Context, run *v1alpha1.MachineRun, doc runresult.Document, at metav1.Time) error {
	var index runner.MatchIndex
	if succeeded(doc.Outcome) {
		e := evidenceOf(&doc, runner.KindMatchIndex)
		ok := e != nil
		if ok {
			var err error
			if ok, err = r.readChecked(ctx, e.Key, e.SHA256, &index); err != nil {
				return err
			}
			ok = ok && index.Schema == runner.MatchIndexSchema
		}
		switch {
		case e == nil:
			doc = runresult.Document{Outcome: runresult.Failed, Message: MessageNoIndex}
		case !ok:
			doc = runresult.Document{Outcome: runresult.Failed, Message: MessageResultsInvalid}
		}
	}
	entries := map[string]runner.MatchEntry{}
	if succeeded(doc.Outcome) {
		for _, e := range index.Machines {
			entries[e.Machine] = e
		}
	}
	for i := range run.Status.Targets {
		t := &run.Status.Targets[i]
		if !matched(run, t) {
			continue
		}
		mach, _, err := r.machine(ctx, t.Machine)
		if err != nil {
			return err
		}
		problem, outcome := doc.Message, doc.Outcome
		if problem == "" {
			problem = string(doc.Outcome)
		}
		if succeeded(doc.Outcome) {
			outcome = runresult.Failed
			if problem, err = r.writeVulnerability(ctx, run, mach, t, entries[t.Machine], at); err != nil {
				return err
			}
		}
		if problem == "" {
			if run.Spec.Kind == v1alpha1.RunKindRematch {
				t.Outcome, t.FinishedAt = string(runresult.Succeeded), &at
			}
			continue
		}
		if mach != nil {
			if err := r.keepReport(ctx, mach, at); err != nil {
				return err
			}
		}
		message := strings.TrimPrefix(t.Message+"; ", "; ") + "match: " + problem
		if len(message) > maxMessage {
			message = message[:maxMessage]
		}
		t.Message, t.FinishedAt = strings.ToValidUTF8(message, ""), &at
		if t.Outcome == "" || succeeded(runresult.Outcome(t.Outcome)) {
			t.Outcome = string(outcome)
		}
	}
	message := strings.Join(strings.Fields(doc.Message), " ")
	if len(message) > maxMessage {
		message = message[:maxMessage]
	}
	m := run.Status.Match
	m.Outcome, m.Message, m.DatabaseVersion, m.FinishedAt = string(doc.Outcome), strings.ToValidUTF8(message, ""), index.DatabaseVersion, &at
	return nil
}

// writeVulnerability writes the vulnerability report of one matched machine from its
// entry of the index. It returns the reason when the machine has no usable findings file.
func (r *RunReconciler) writeVulnerability(ctx context.Context, run *v1alpha1.MachineRun, mach *v1alpha1.Machine, t *v1alpha1.RunTarget, e runner.MatchEntry, at metav1.Time) (string, error) {
	switch {
	case e.Machine == "":
		return MessageNoFindings, nil
	case e.Error != "":
		return e.Error, nil
	}
	var f runner.Findings
	ok, err := r.readChecked(ctx, e.FindingsKey, e.SHA256, &f)
	if err != nil {
		return "", err
	}
	if !ok || f.Schema != runner.FindingsSchema || f.Machine != t.Machine {
		return MessageResultsInvalid, nil
	}
	t.Evidence = append(t.Evidence, e.FindingsKey)
	if mach == nil {
		return "", nil
	}
	previous := newObject(PolicyReportKind)
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: mach.Namespace, Name: VulnerabilityReportName(mach.Name)}, previous)
	switch {
	case apierrors.IsNotFound(err):
		previous = nil
	case err != nil:
		return "", err
	}
	if err := r.putReport(ctx, VulnerabilityReport(mach, run.Name, f, e.FindingsKey, previous, at.Time)); err != nil {
		return "", err
	}
	if run.Spec.Kind == v1alpha1.RunKindRematch {
		t.ReportName = VulnerabilityReportName(mach.Name)
	}
	return "", nil
}

// keepReport leaves the vulnerability report of a Machine in place and turns its result
// database-age to warn once its database is older than MaxDatabaseAge.
func (r *RunReconciler) keepReport(ctx context.Context, mach *v1alpha1.Machine, at metav1.Time) error {
	previous := newObject(PolicyReportKind)
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: mach.Namespace, Name: VulnerabilityReportName(mach.Name)}, previous); err != nil {
		return client.IgnoreNotFound(err)
	}
	aged := AgedReport(previous, at.Time)
	if aged == nil {
		return nil
	}
	if err := r.Client.Update(ctx, aged); err != nil {
		return fmt.Errorf("update PolicyReport %s/%s: %w", aged.GetNamespace(), aged.GetName(), err)
	}
	return nil
}

// jobEnded reports whether a Job has the condition Complete or Failed, and whether it
// failed on its deadline.
func jobEnded(job *batchv1.Job) (ended, deadline bool) {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			ended, deadline = true, c.Type == batchv1.JobFailed && c.Reason == batchv1.JobReasonDeadlineExceeded
		}
	}
	return ended, deadline
}

// terminated gives the end state of every container of the Pods of a Job, by name.
func (r *RunReconciler) terminated(ctx context.Context, job *batchv1.Job) (map[string]*corev1.ContainerStateTerminated, error) {
	pods := &corev1.PodList{}
	if err := r.Reader.List(ctx, pods, client.InNamespace(job.Namespace), client.MatchingLabels{LabelJobName: job.Name}); err != nil {
		return nil, err
	}
	out := map[string]*corev1.ContainerStateTerminated{}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.State.Terminated != nil {
				out[cs.Name] = cs.State.Terminated
			}
		}
	}
	return out, nil
}
