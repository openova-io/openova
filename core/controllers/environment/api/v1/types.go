// Package v1 holds the Go types that mirror the
// Environment.catalyst.openova.io/v1 CRD shipped at
// products/catalyst/chart/crds/environment.yaml.
//
// Per docs/EPICS-1-6-unified-design.md §3.2.2 + docs/NAMING-CONVENTION.md
// §11, an Environment is the user-facing scope where Applications are
// installed. Logical concept; one Environment is realized by N vClusters
// across regions × building blocks. The Environment name is the
// composition `{organizationRef}-{envType}` — DR is a Placement on the
// Application, NEVER an envType (per NAMING §11.1, Inviolable Principle
// against quietly substituting design).
//
// These types are intentionally hand-written rather than generated via
// kubebuilder so the controller has zero non-K8s build-time tooling
// dependencies (per Inviolable Principle #3 — match the documented
// architecture exactly, no ad-hoc tool sprawl).
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the canonical GVK group + version for the
// Environment CRD. Mirrors the spec in
// `products/catalyst/chart/crds/environment.yaml`.
var GroupVersion = schema.GroupVersion{Group: "catalyst.openova.io", Version: "v1"}

// EnvironmentGVK is the literal GVK for typed clients.
var EnvironmentGVK = GroupVersion.WithKind("Environment")

// EnvironmentRegion describes one host-cluster realization slot of an
// Environment. Per the CRD schema:
//
//   - provider:      enum {hetzner|huawei|oci|aws|gcp|azure|contabo}
//   - region:        ^[a-z]{3}[a-z0-9]?$ (NAMING §4.1 region code)
//   - buildingBlock: enum {rtz|dmz|mgt}
//   - hostCluster:   optional explicit override; when empty the
//     controller derives `{provider}-{region}-{bb}-{envType}` per
//     NAMING §4.1.
type EnvironmentRegion struct {
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	BuildingBlock string `json:"buildingBlock"`
	HostCluster   string `json:"hostCluster,omitempty"`
}

// EnvironmentSpec is the desired state of an Environment.
type EnvironmentSpec struct {
	OrganizationRef string              `json:"organizationRef"`
	EnvType         string              `json:"envType"`
	Placement       string              `json:"placement"`
	Regions         []EnvironmentRegion `json:"regions"`
	PolicyRef       string              `json:"policyRef,omitempty"`
}

// EnvironmentVClusterStatus is the per-region rollout summary surfaced
// in `status.vclusters[]`.
type EnvironmentVClusterStatus struct {
	Host               string      `json:"host"`
	Name               string      `json:"name"`
	Phase              string      `json:"phase,omitempty"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// EnvironmentGiteaRepoRef encodes the per-Org Gitea-side coordinates
// for this Environment (NAMING §11.2 item 1: branch per env_type, repo
// is per-Application — controller tracks the canonical Org + branch
// pair here).
type EnvironmentGiteaRepoRef struct {
	Org    string `json:"org,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// EnvironmentCondition captures one slice of the controller's view on
// reconciliation health. Type names are stable across versions.
type EnvironmentCondition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"` // True | False | Unknown
	Reason             string      `json:"reason,omitempty"`
	Message            string      `json:"message,omitempty"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// EnvironmentStatus is the observed state of an Environment.
type EnvironmentStatus struct {
	Phase                  string                      `json:"phase,omitempty"`
	RegionCount            int32                       `json:"regionCount,omitempty"`
	VClusters              []EnvironmentVClusterStatus `json:"vclusters,omitempty"`
	GiteaRepoRef           EnvironmentGiteaRepoRef     `json:"giteaRepoRef,omitempty"`
	JetstreamSubjectPrefix string                      `json:"jetstreamSubjectPrefix,omitempty"`
	Conditions             []EnvironmentCondition      `json:"conditions,omitempty"`
	ObservedGeneration     int64                       `json:"observedGeneration,omitempty"`
}

// Phase constants for status.phase.
const (
	PhasePending      = "Pending"
	PhaseProvisioning = "Provisioning"
	PhaseReady        = "Ready"
	PhaseDegraded     = "Degraded"
	PhaseFailed       = "Failed"
)

// Standard condition Types surfaced by environment-controller.
const (
	ConditionGiteaOrgReady       = "GiteaOrgReady"
	ConditionGitRepositoryWritten = "GitRepositoryWritten"
	ConditionReady               = "Ready"
)

// Standard condition statuses (string form per the CRD enum).
const (
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"
)

// Environment is the cluster-scoped CR.
//
// +kubebuilder:object:root=true
type Environment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnvironmentSpec   `json:"spec,omitempty"`
	Status EnvironmentStatus `json:"status,omitempty"`
}

// EnvironmentList wraps a slice of Environment.
//
// +kubebuilder:object:root=true
type EnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Environment `json:"items"`
}

// DeepCopyInto, DeepCopy, DeepCopyObject — minimal hand-written
// implementations. We avoid pulling in code-generation tooling at
// build time; controller-runtime needs only DeepCopyObject to satisfy
// runtime.Object.

func (in *Environment) DeepCopyInto(out *Environment) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = *in.Status.DeepCopy()
}

func (in *Environment) DeepCopy() *Environment {
	if in == nil {
		return nil
	}
	out := new(Environment)
	in.DeepCopyInto(out)
	return out
}

func (in *Environment) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *EnvironmentList) DeepCopyInto(out *EnvironmentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Environment, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *EnvironmentList) DeepCopy() *EnvironmentList {
	if in == nil {
		return nil
	}
	out := new(EnvironmentList)
	in.DeepCopyInto(out)
	return out
}

func (in *EnvironmentList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *EnvironmentSpec) DeepCopy() *EnvironmentSpec {
	if in == nil {
		return nil
	}
	out := new(EnvironmentSpec)
	*out = *in
	if in.Regions != nil {
		out.Regions = make([]EnvironmentRegion, len(in.Regions))
		copy(out.Regions, in.Regions)
	}
	return out
}

func (in *EnvironmentStatus) DeepCopy() *EnvironmentStatus {
	if in == nil {
		return nil
	}
	out := new(EnvironmentStatus)
	*out = *in
	if in.VClusters != nil {
		out.VClusters = make([]EnvironmentVClusterStatus, len(in.VClusters))
		for i := range in.VClusters {
			out.VClusters[i] = in.VClusters[i]
			out.VClusters[i].LastTransitionTime = *in.VClusters[i].LastTransitionTime.DeepCopy()
		}
	}
	if in.Conditions != nil {
		out.Conditions = make([]EnvironmentCondition, len(in.Conditions))
		for i := range in.Conditions {
			out.Conditions[i] = in.Conditions[i]
			out.Conditions[i].LastTransitionTime = *in.Conditions[i].LastTransitionTime.DeepCopy()
		}
	}
	return out
}

// SchemeBuilder registers the Environment kinds.
var (
	SchemeBuilder runtime.SchemeBuilder
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(addKnownTypes)
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Environment{},
		&EnvironmentList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
