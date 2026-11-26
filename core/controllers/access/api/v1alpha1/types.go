package v1alpha1

import (
	"fmt"
	"maps"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// KindPod is the one target kind of this version: a set of pods entered
// through the Kubernetes API.
const KindPod = "pod"

// Condition types of an AccessTarget.
const (
	ConditionReady               = "Ready"
	ConditionServiceAccountReady = "ServiceAccountReady"
)

// Phases of an AccessGrant, and the reasons of phase Invalid.
const (
	PhasePending   = "Pending"
	PhaseScheduled = "Scheduled"
	PhaseActive    = "Active"
	PhaseExpired   = "Expired"
	PhaseSuspended = "Suspended"
	PhaseInvalid   = "Invalid"

	ReasonTargetNotFound    = "TargetNotFound"
	ReasonAccountNotAllowed = "AccountNotAllowed"
	ReasonWindowTooLong     = "WindowTooLong"
	ReasonSelfApproval      = "SelfApproval"
	ReasonDuplicateApprover = "DuplicateApprover"
)

// AccessTarget says what a person may reach in a session, and under which rules.
type AccessTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessTargetSpec   `json:"spec"`
	Status AccessTargetStatus `json:"status,omitempty"`
}

// AccessTargetSpec is the desired state of a target.
type AccessTargetSpec struct {
	Kind        string       `json:"kind"`
	DisplayName string       `json:"displayName,omitempty"`
	Pod         *PodTarget   `json:"pod,omitempty"`
	Policy      TargetPolicy `json:"policy,omitempty"`
}

// PodTarget names the pods of the target's own namespace that may be entered.
type PodTarget struct {
	Selector map[string]string `json:"selector"`
}

// TargetPolicy holds the rules of a target that this version reads.
type TargetPolicy struct {
	Approval    ApprovalPolicy `json:"approval,omitempty"`
	MaxDuration string         `json:"maxDuration,omitempty"`
}

// ApprovalPolicy says whether a grant needs approvals, and how many.
type ApprovalPolicy struct {
	Required *bool `json:"required,omitempty"`
	Count    int32 `json:"count,omitempty"`
}

// AccessTargetStatus is written by the target reconciler.
type AccessTargetStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// The definition defaults policy.approval.required to true, policy.approval.count
// to 1 and policy.maxDuration to 1h, and the API server writes them into every
// object it stores. These three methods read the field and return that same
// default when it is absent, for an object built in memory.

// ApprovalRequired reports whether a grant on the target needs approvals.
func (t *AccessTarget) ApprovalRequired() bool {
	return t.Spec.Policy.Approval.Required == nil || *t.Spec.Policy.Approval.Required
}

// ApprovalCount is the number of approvals a grant on the target needs.
func (t *AccessTarget) ApprovalCount() int {
	if t.Spec.Policy.Approval.Count == 0 {
		return 1
	}
	return int(t.Spec.Policy.Approval.Count)
}

// MaxDuration is the longest window a grant on the target may have.
func (t *AccessTarget) MaxDuration() (time.Duration, error) {
	if t.Spec.Policy.MaxDuration == "" {
		return time.Hour, nil
	}
	d, err := time.ParseDuration(t.Spec.Policy.MaxDuration)
	if err != nil {
		return 0, fmt.Errorf("target %s/%s: policy.maxDuration: %w", t.Namespace, t.Name, err)
	}
	return d, nil
}

// AccessTargetList is a list of targets.
type AccessTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessTarget `json:"items"`
}

// AccessGrant is one person's request for a target, and once approved their
// time-bound right to it. It lives in the namespace of its target.
type AccessGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessGrantSpec   `json:"spec"`
	Status AccessGrantStatus `json:"status,omitempty"`
}

// AccessGrantSpec is the desired state of a grant.
type AccessGrantSpec struct {
	TargetRef     LocalRef   `json:"targetRef"`
	Subject       Subject    `json:"subject"`
	Account       string     `json:"account,omitempty"`
	Reason        string     `json:"reason"`
	Ticket        string     `json:"ticket,omitempty"`
	Window        Window     `json:"window"`
	Approvals     []Approval `json:"approvals,omitempty"`
	Suspended     bool       `json:"suspended,omitempty"`
	SuspendReason string     `json:"suspendReason,omitempty"`
}

// LocalRef names an object in the same namespace.
type LocalRef struct {
	Name string `json:"name"`
}

// Subject is the identity the gate presents.
type Subject struct {
	Email string `json:"email"`
}

// Window is the time in which a grant may be used.
type Window struct {
	NotBefore metav1.Time `json:"notBefore"`
	NotAfter  metav1.Time `json:"notAfter"`
}

// Approval is one approver's entry.
type Approval struct {
	By string      `json:"by"`
	At metav1.Time `json:"at"`
}

// AccessGrantStatus is written by the grant reconciler.
type AccessGrantStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	Phase              string       `json:"phase,omitempty"`
	Reason             string       `json:"reason,omitempty"`
	ActivatedAt        *metav1.Time `json:"activatedAt,omitempty"`
	EndedAt            *metav1.Time `json:"endedAt,omitempty"`
}

// AccessGrantList is a list of grants.
type AccessGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessGrant `json:"items"`
}

// DeepCopyInto copies the target into out.
func (in *AccessTarget) DeepCopyInto(out *AccessTarget) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if in.Spec.Pod != nil {
		p := *in.Spec.Pod
		p.Selector = maps.Clone(p.Selector)
		out.Spec.Pod = &p
	}
	if r := in.Spec.Policy.Approval.Required; r != nil {
		v := *r
		out.Spec.Policy.Approval.Required = &v
	}
	out.Status.Conditions = slices.Clone(in.Status.Conditions)
}

// DeepCopy returns a copy of the target.
func (in *AccessTarget) DeepCopy() *AccessTarget {
	if in == nil {
		return nil
	}
	out := new(AccessTarget)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *AccessTarget) DeepCopyObject() runtime.Object { return in.DeepCopy() }

// DeepCopyInto copies the grant into out.
func (in *AccessGrant) DeepCopyInto(out *AccessGrant) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Approvals = slices.Clone(in.Spec.Approvals)
	out.Status.ActivatedAt, out.Status.EndedAt = in.Status.ActivatedAt.DeepCopy(), in.Status.EndedAt.DeepCopy()
}

// DeepCopy returns a copy of the grant.
func (in *AccessGrant) DeepCopy() *AccessGrant {
	if in == nil {
		return nil
	}
	out := new(AccessGrant)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *AccessGrant) DeepCopyObject() runtime.Object { return in.DeepCopy() }

// DeepCopyInto copies the list into out.
func (in *AccessTargetList) DeepCopyInto(out *AccessTargetList) {
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = copyItems(in.Items, (*AccessTarget).DeepCopyInto)
}

// DeepCopyObject implements runtime.Object.
func (in *AccessTargetList) DeepCopyObject() runtime.Object {
	out := new(AccessTargetList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the list into out.
func (in *AccessGrantList) DeepCopyInto(out *AccessGrantList) {
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = copyItems(in.Items, (*AccessGrant).DeepCopyInto)
}

// DeepCopyObject implements runtime.Object.
func (in *AccessGrantList) DeepCopyObject() runtime.Object {
	out := new(AccessGrantList)
	in.DeepCopyInto(out)
	return out
}

func copyItems[T any](in []T, copyInto func(*T, *T)) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i := range in {
		copyInto(&in[i], &out[i])
	}
	return out
}
