// Package sandboxapi defines the Go types mirroring the
// sandbox.openova.io/v1 Sandbox CRD that sandbox-controller reconciles.
// These mirror products/catalyst/chart/crds/sandbox.yaml 1:1; if either
// side changes, both sides must change in the same PR.
//
// Per products/sandbox/docs/architecture.md §7 the Sandbox is the
// per-user, per-Organization coding-agent plane. Wave 1 (this slice)
// only materializes the namespace + RBAC + PVCs + placeholder Secret —
// pty-server + openova-sandbox-mcp + preview HTTPRoutes land in Waves
// 2/3. The CRD lives at sandbox.openova.io/v1 — a SIBLING group to
// orgs.openova.io/v1 so RBAC + watch caches stay per-domain isolated
// (ADR-0001 §6: `catalyst.<domain>.<event>` convention).
package sandboxapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group/version for the Sandbox kind.
var GroupVersion = schema.GroupVersion{Group: "sandbox.openova.io", Version: "v1"}

// SchemeBuilder is used to add the Sandbox types to a runtime.Scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme registers Sandbox{,List} with the supplied Scheme so the
// controller-runtime manager and its caches can serialize the kind.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Sandbox{}, &SandboxList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// Sandbox is the namespace-scoped CR. See architecture.md §7 +
// products/catalyst/chart/crds/sandbox.yaml.
//
// +kubebuilder:resource:scope=Namespaced
type Sandbox struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxSpec   `json:"spec"`
	Status SandboxStatus `json:"status,omitempty"`
}

// SandboxList is the standard list wrapper.
type SandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sandbox `json:"items"`
}

// SandboxSpec mirrors the CRD's spec block.
type SandboxSpec struct {
	Owner          SandboxOwner   `json:"owner"`
	Quota          SandboxQuota   `json:"quota"`
	Repos          []SandboxRepo  `json:"repos,omitempty"`
	AgentCatalogue []string       `json:"agentCatalogue,omitempty"`
	PreviewDomain  string         `json:"previewDomain,omitempty"`

	// PlanID is the catalog plan slug the customer selected
	// (`sandbox-free` | `sandbox-pro` | `sandbox-ent`). Stamped by the
	// tenant-service orchestrator from the `tenant.sandbox_requested`
	// event. The sandbox-controller falls back to this when
	// spec.capabilities is empty so the operator gets the plan's
	// default capability allowlist without an explicit overlay.
	PlanID string `json:"planId,omitempty"`

	// Capabilities is the MCP capability allowlist the
	// sandbox-controller stamps onto every minted NewAPI token. The
	// MCP server gates every tool call against these strings via
	// Claims.HasCapability (architecture.md §3 RBAC). Empty list →
	// the controller derives the list from spec.planId via
	// CapabilitiesForPlan. Wildcards (`sandbox.db.*`) are honoured
	// by the matcher so the plan map can carry coarse grants.
	Capabilities []string `json:"capabilities,omitempty"`
}

// SandboxOwner is spec.owner.
type SandboxOwner struct {
	Email  string          `json:"email"`
	OrgRef SandboxOrgRef   `json:"orgRef"`
}

// SandboxOrgRef references the parent Organization by slug.
type SandboxOrgRef struct {
	Slug string `json:"slug"`
}

// SandboxQuota mirrors the ResourceQuota the reconciler renders.
type SandboxQuota struct {
	CPU                string `json:"cpu"`
	Memory             string `json:"memory"`
	Storage            string `json:"storage"`
	ConcurrentSessions int    `json:"concurrentSessions"`
}

// SandboxRepo names a Gitea repo to auto-clone (PVC + initContainer in
// Wave 2; Wave 1 only writes the PVC manifest).
type SandboxRepo struct {
	GiteaRepo string `json:"giteaRepo"`
}

// SandboxStatus is controller-managed.
type SandboxStatus struct {
	Phase              string             `json:"phase,omitempty"` // Pending|Provisioning|Ready|Failed
	Sessions           int                `json:"sessions,omitempty"`
	StorageUsed        string             `json:"storageUsed,omitempty"`
	Spend30d           string             `json:"spend30d,omitempty"`
	Previews           []SandboxPreview   `json:"previews,omitempty"`
	GitopsPath         string             `json:"gitopsPath,omitempty"`
	Conditions         []SandboxCondition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// SandboxPreview is one element of status.previews.
type SandboxPreview struct {
	PRNumber int    `json:"prNumber"`
	URL      string `json:"url"`
	SHA      string `json:"sha"`
}

// SandboxCondition is the standard K8s-style condition.
type SandboxCondition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"` // True|False|Unknown
	Reason             string      `json:"reason,omitempty"`
	Message            string      `json:"message,omitempty"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (s *Sandbox) DeepCopyObject() runtime.Object {
	if s == nil {
		return nil
	}
	out := &Sandbox{}
	s.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (s *Sandbox) DeepCopyInto(out *Sandbox) {
	*out = *s
	out.TypeMeta = s.TypeMeta
	s.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	s.Spec.DeepCopyInto(&out.Spec)
	s.Status.DeepCopyInto(&out.Status)
}

// DeepCopyObject for SandboxList.
func (l *SandboxList) DeepCopyObject() runtime.Object {
	if l == nil {
		return nil
	}
	out := &SandboxList{}
	out.TypeMeta = l.TypeMeta
	l.ListMeta.DeepCopyInto(&out.ListMeta)
	if l.Items != nil {
		out.Items = make([]Sandbox, len(l.Items))
		for i := range l.Items {
			l.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
	return out
}

// DeepCopyInto for SandboxSpec.
func (s *SandboxSpec) DeepCopyInto(out *SandboxSpec) {
	*out = *s
	if s.Repos != nil {
		out.Repos = make([]SandboxRepo, len(s.Repos))
		copy(out.Repos, s.Repos)
	}
	if s.AgentCatalogue != nil {
		out.AgentCatalogue = make([]string, len(s.AgentCatalogue))
		copy(out.AgentCatalogue, s.AgentCatalogue)
	}
	if s.Capabilities != nil {
		out.Capabilities = make([]string, len(s.Capabilities))
		copy(out.Capabilities, s.Capabilities)
	}
}

// DeepCopyInto for SandboxStatus.
func (s *SandboxStatus) DeepCopyInto(out *SandboxStatus) {
	*out = *s
	if s.Conditions != nil {
		out.Conditions = make([]SandboxCondition, len(s.Conditions))
		for i := range s.Conditions {
			out.Conditions[i] = s.Conditions[i]
			out.Conditions[i].LastTransitionTime = *s.Conditions[i].LastTransitionTime.DeepCopy()
		}
	}
	if s.Previews != nil {
		out.Previews = make([]SandboxPreview, len(s.Previews))
		copy(out.Previews, s.Previews)
	}
}
