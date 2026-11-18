package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The kinds of the operations controller.
var (
	ScanPolicyKind        = GroupVersion.WithKind("ScanPolicy")
	HardeningBaselineKind = GroupVersion.WithKind("HardeningBaseline")
	MachineRunKind        = GroupVersion.WithKind("MachineRun")
)

// Profiles of a ScanPolicy. ProfileVulnerability matches the bill of materials of a
// machine against the vulnerability database; it implies ProfileInventory.
const (
	ProfileInventory     = "inventory"
	ProfileHardening     = "hardening"
	ProfileVulnerability = "vulnerability"
)

// The kinds of a MachineRun. A scan run starts one Job per machine; a rematch run starts
// none and matches the newest stored bill of materials of each machine again.
// RequestedByController is spec.requestedBy of a run the operations controller created.
const (
	RunKindScan           = "scan"
	RunKindRematch        = "rematch"
	RequestedByController = "controller"
)

// Phases of a MachineRun. The last three are final.
const (
	RunPending         = "Pending"
	RunRunning         = "Running"
	RunSucceeded       = "Succeeded"
	RunPartiallyFailed = "PartiallyFailed"
	RunFailed          = "Failed"
)

// Defaults of ScanPolicy.spec.
const (
	DefaultJitter      = "1h"
	DefaultMaxParallel = 5
)

// AnnotationScheduledFor is set on a MachineRun that the schedule of its policy created.
// Its value is the scheduled time (RFC 3339, UTC); each target starts at its own offset
// inside spec.jitter after that time. A run without it starts its targets at once.
const AnnotationScheduledFor = "machines.openova.io/scheduled-for"

// SBOM names the newest complete bill of materials of a Machine: Machine.status.sbom.
type SBOM struct {
	Key         string      `json:"key"`
	SHA256      string      `json:"sha256"`
	CollectedAt metav1.Time `json:"collectedAt"`
}

// LocalRef names an object in the namespace of the object that holds the reference.
type LocalRef struct {
	Name string `json:"name"`
}

// ScanPolicy says which machines are scanned, with which profiles and when.
type ScanPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ScanPolicySpec   `json:"spec"`
	Status            ScanPolicyStatus `json:"status,omitempty"`
}

// ScanPolicySpec is the desired state of a ScanPolicy. An empty Jitter and a nil
// MaxParallel take the default of the definition.
type ScanPolicySpec struct {
	Suspended            bool                 `json:"suspended,omitempty"`
	Namespaces           []string             `json:"namespaces,omitempty"`
	MachineSelector      metav1.LabelSelector `json:"machineSelector"`
	Profiles             []string             `json:"profiles"`
	HardeningBaselineRef *LocalRef            `json:"hardeningBaselineRef,omitempty"`
	Schedule             string               `json:"schedule,omitempty"`
	Jitter               string               `json:"jitter,omitempty"`
	MaxParallel          *int32               `json:"maxParallel,omitempty"`
}

// ScanPolicyStatus is what the scheduler last did.
type ScanPolicyStatus struct {
	LastRunName    string             `json:"lastRunName,omitempty"`
	NextScheduleAt *metav1.Time       `json:"nextScheduleAt,omitempty"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
}

// HardeningBaseline names the hardening content and the profile per release.
type HardeningBaseline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HardeningBaselineSpec `json:"spec"`
}

// HardeningBaselineSpec is the desired state of a HardeningBaseline.
type HardeningBaselineSpec struct {
	Content   HardeningContent   `json:"content"`
	Profiles  []HardeningProfile `json:"profiles"`
	Tailoring *Tailoring         `json:"tailoring,omitempty"`
}

// HardeningContent names the content and its release.
type HardeningContent struct {
	Source  string `json:"source"`
	Release string `json:"release"`
}

// HardeningProfile is the profile of one operating-system release.
type HardeningProfile struct {
	OSFamily  string `json:"osFamily"`
	OSVersion string `json:"osVersion"`
	ProfileID string `json:"profileId"`
}

// Tailoring changes the profile for this Sovereign.
type Tailoring struct {
	DisabledRules []string `json:"disabledRules,omitempty"`
}

// MachineRun is the record of one operation on the machines a policy selects.
type MachineRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineRunSpec   `json:"spec"`
	Status            MachineRunStatus `json:"status,omitempty"`
}

// MachineRunSpec says what runs. It is immutable.
type MachineRunSpec struct {
	Kind        string    `json:"kind"`
	PolicyRef   PolicyRef `json:"policyRef"`
	Targets     []string  `json:"targets,omitempty"`
	RequestedBy string    `json:"requestedBy"`
	Reason      string    `json:"reason,omitempty"`
}

// PolicyRef names the policy of a run, in the namespace of the run.
type PolicyRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// MachineRunStatus is the progress and the result of a run.
type MachineRunStatus struct {
	Phase      string           `json:"phase,omitempty"`
	StartedAt  *metav1.Time     `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time     `json:"finishedAt,omitempty"`
	Counts     map[string]int32 `json:"counts,omitempty"`
	Targets    []RunTarget      `json:"targets,omitempty"`
	Match      *RunMatch        `json:"match,omitempty"`
}

// RunMatch is the one match Job of a run: it matches the bills of materials of the run's
// machines against the vulnerability database. Outcome is empty until the Job has ended.
// DatabaseVersion is the update time of the database the Job used (RFC 3339).
type RunMatch struct {
	JobName         string       `json:"jobName"`
	Outcome         string       `json:"outcome,omitempty"`
	Message         string       `json:"message,omitempty"`
	DatabaseVersion string       `json:"databaseVersion,omitempty"`
	StartedAt       *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt      *metav1.Time `json:"finishedAt,omitempty"`
}

// RunTarget is one machine of a run. Machine is <namespace>/<name>. Outcome is empty
// until the target has finished; then it is an outcome of the runner's result document.
type RunTarget struct {
	Machine    string       `json:"machine"`
	Outcome    string       `json:"outcome,omitempty"`
	Message    string       `json:"message,omitempty"`
	JobName    string       `json:"jobName,omitempty"`
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	Evidence   []string     `json:"evidence,omitempty"`
	ReportName string       `json:"reportName,omitempty"`
}

// ScanPolicyFromUnstructured converts an unstructured object of kind ScanPolicy. Like
// the two converters below, it returns an error for an object of another kind.
func ScanPolicyFromUnstructured(u *unstructured.Unstructured) (*ScanPolicy, error) {
	p := &ScanPolicy{}
	return p, fromUnstructured(u, ScanPolicyKind, p)
}

// ScanPolicyToUnstructured converts a ScanPolicy and sets its apiVersion and kind.
func ScanPolicyToUnstructured(p *ScanPolicy) (*unstructured.Unstructured, error) {
	return toUnstructured(p, ScanPolicyKind)
}

// HardeningBaselineFromUnstructured converts an unstructured object of kind HardeningBaseline.
func HardeningBaselineFromUnstructured(u *unstructured.Unstructured) (*HardeningBaseline, error) {
	b := &HardeningBaseline{}
	return b, fromUnstructured(u, HardeningBaselineKind, b)
}

// MachineRunFromUnstructured converts an unstructured object of kind MachineRun.
func MachineRunFromUnstructured(u *unstructured.Unstructured) (*MachineRun, error) {
	r := &MachineRun{}
	return r, fromUnstructured(u, MachineRunKind, r)
}

// MachineRunToUnstructured converts a MachineRun and sets its apiVersion and kind.
func MachineRunToUnstructured(r *MachineRun) (*unstructured.Unstructured, error) {
	return toUnstructured(r, MachineRunKind)
}
