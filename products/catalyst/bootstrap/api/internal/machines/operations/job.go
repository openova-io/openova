package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

// Settings are the facts the operations controller gets from its arguments.
type Settings struct {
	JobNamespace     string        // the namespace of the runner Jobs: the controller's own
	RunnerImage      string        // the machine-runner image, by digest
	EvidenceEndpoint string        // the S3 endpoint of the evidence bucket, https://host[:port]
	EvidenceRegion   string        // its region
	EvidenceBucket   string        // the bucket
	Retention        time.Duration // how long a finished run is kept
}

// The fixed parts of a node-mode runner Job.
const (
	NodeServiceAccount    = "mr-node"
	EvidenceProviderClass = "evidence-store"
	EvidenceMountPath     = "/var/run/secrets/evidence-store"
	WorkMountPath         = "/run/machine-runner"
	HostMountPath         = "/host"
	JobDeadlineSeconds    = 3600
	JobTTLSeconds         = 86400
	AnnotationRun         = "machines.openova.io/run"
	AnnotationMachine     = "machines.openova.io/machine"
	LabelJobName          = "batch.kubernetes.io/job-name" // set by the Job controller on the Pods of a Job
)

// Task is one container of a runner Job: the task name and the arguments of the runner.
type Task struct {
	Name string
	Args []string
}

// JobName is the name of the Job of one run and one target: mr- and the first 16 hex
// digits of the SHA-256 of <run namespace>/<run name>/<machine namespace>/<machine name>.
// The same run and target give the same name, so a target never gets two Jobs.
func JobName(run, machine string) string {
	sum := sha256.Sum256([]byte(run + "/" + machine))
	return "mr-" + hex.EncodeToString(sum[:8])
}

// TaskArgs are the arguments of the runner for one task on one node. profile is the
// hardening profile and baseline its HardeningBaseline; both are used only for the task
// hardening.
func (s Settings) TaskArgs(task string, run *v1alpha1.MachineRun, m *v1alpha1.Machine, profile string, baseline *v1alpha1.HardeningBaseline) []string {
	family, version := "", ""
	if m.Status.OS != nil {
		family, version = m.Status.OS.Family, m.Status.OS.Version
	}
	args := []string{
		"--task=" + task, "--mode=" + runner.ModeNode,
		"--machine=" + m.Namespace + "/" + m.Name, "--run=" + run.Namespace + "/" + run.Name,
		"--os-family=" + family, "--os-version=" + version,
		"--evidence-endpoint=" + s.EvidenceEndpoint, "--evidence-region=" + s.EvidenceRegion, "--evidence-bucket=" + s.EvidenceBucket,
		"--evidence-prefix=" + m.Namespace + "/" + m.Name + "/" + run.Name + "/",
	}
	if m.Status.PrimaryAddress != "" {
		args = append(args, "--address="+m.Status.PrimaryAddress)
	}
	if task == runner.TaskHardening {
		args = append(args, "--profile-id="+profile, "--content-release="+baseline.Spec.Content.Release)
		if baseline.Spec.Tailoring != nil && len(baseline.Spec.Tailoring.DisabledRules) > 0 {
			args = append(args, "--disabled-rules="+strings.Join(baseline.Spec.Tailoring.DisabledRules, ","))
		}
	}
	return args
}

// NodeJob builds the Job of one run on one Machine of class node: one container per
// task, on the target's Node, as root with a read-only root file system, the Node's file
// system read-only at /host, a memory volume of its own as the work folder and the
// evidence binding as a CSI volume. The pod gets no token of the Kubernetes API.
func (s Settings) NodeJob(run *v1alpha1.MachineRun, m *v1alpha1.Machine, tasks []Task) *batchv1.Job {
	no, yes, root := false, true, int64(0)
	backoff, deadline, ttl := int32(0), int64(JobDeadlineSeconds), int32(JobTTLSeconds)
	labels := map[string]string{"app.kubernetes.io/name": "machine-runner", "machines.openova.io/mode": runner.ModeNode}
	annotations := map[string]string{AnnotationRun: run.Namespace + "/" + run.Name, AnnotationMachine: m.Namespace + "/" + m.Name}
	pod := corev1.PodSpec{
		ServiceAccountName:           NodeServiceAccount,
		AutomountServiceAccountToken: &no,
		NodeName:                     m.Spec.ExternalID,
		RestartPolicy:                corev1.RestartPolicyNever,
		EnableServiceLinks:           &no,
		Tolerations:                  []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
		Volumes: []corev1.Volume{
			{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}},
			{Name: "evidence-store", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
				Driver: "secrets-store.csi.k8s.io", ReadOnly: &yes,
				VolumeAttributes: map[string]string{"secretProviderClass": EvidenceProviderClass},
			}}},
		},
	}
	for _, t := range tasks {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "work-" + t.Name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}})
		pod.Containers = append(pod.Containers, corev1.Container{
			Name: t.Name, Image: s.RunnerImage, Args: t.Args,
			TerminationMessagePath: corev1.TerminationMessagePathDefault,
			SecurityContext:        &corev1.SecurityContext{RunAsUser: &root, ReadOnlyRootFilesystem: &yes, AllowPrivilegeEscalation: &no},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "host", MountPath: HostMountPath, ReadOnly: true},
				{Name: "work-" + t.Name, MountPath: WorkMountPath},
				{Name: "evidence-store", MountPath: EvidenceMountPath, ReadOnly: true},
			},
		})
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: JobName(run.Namespace+"/"+run.Name, m.Namespace+"/"+m.Name), Namespace: s.JobNamespace, Labels: labels, Annotations: annotations},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}, Spec: pod},
		},
	}
}
