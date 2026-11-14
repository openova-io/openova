// Package v1alpha1 holds the Go form of the kinds Machine and MachineSource of the
// group machines.openova.io. The kinds belong to the Blueprint
// bp-machine-operations; their definitions are the files in
// platform/machine-operations/chart/crds/. A field exists here only when it
// exists in the definition, and the other way round.
//
// The controllers of the machine plane work on unstructured.Unstructured objects
// and convert them with the functions of convert.go. There is no deep-copy code.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group and Version name the API of the machine plane.
const (
	Group   = "machines.openova.io"
	Version = "v1alpha1"
)

// GroupVersion is machines.openova.io/v1alpha1.
var GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

// The kinds and their list kinds.
var (
	MachineKind           = GroupVersion.WithKind("Machine")
	MachineListKind       = GroupVersion.WithKind("MachineList")
	MachineSourceKind     = GroupVersion.WithKind("MachineSource")
	MachineSourceListKind = GroupVersion.WithKind("MachineSourceList")
)

// The five labels the inventory controller writes on every Machine. No other label
// is kept on a Machine.
const (
	LabelClass     = "machines.openova.io/class"
	LabelOwnership = "machines.openova.io/ownership"
	LabelOSFamily  = "machines.openova.io/os-family"
	LabelPhase     = "machines.openova.io/phase"
	LabelSource    = "machines.openova.io/source"
)

// Machine classes.
const (
	ClassNode           = "node"
	ClassVirtualMachine = "virtual-machine"
	ClassHost           = "host"
	ClassAppliance      = "appliance"
)

// Machine phases. The label machines.openova.io/phase carries the same value in
// lower case.
const (
	PhaseActive  = "Active"
	PhaseStale   = "Stale"
	PhaseRetired = "Retired"
)

// Ownership values.
const (
	OwnershipDeclared  = "declared"
	OwnershipAdopted   = "adopted"
	OwnershipUnmanaged = "unmanaged"
)

// SourceTypeNodes is the one source type of this version: the Nodes of the cluster.
const SourceTypeNodes = "nodes"

// Condition types of a MachineSource.
const (
	ConditionReady     = "Ready"
	ConditionComplete  = "Complete"
	ConditionStaleHold = "StaleHold"
)

// Defaults of MachineSource.spec.stale.
const (
	DefaultHoldPercent = 20
	DefaultHoldMinimum = 10
	DefaultRetireAfter = "720h"
	DefaultPurgeAfter  = "2160h"
)

// Machine is one machine that a MachineSource saw. It is an observation: only the
// inventory controller creates, changes and deletes it.
type Machine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineSpec   `json:"spec"`
	Status            MachineStatus `json:"status,omitempty"`
}

// MachineSpec is the identity of a Machine. Every field is immutable.
type MachineSpec struct {
	SourceRef  SourceRef `json:"sourceRef"`
	ExternalID string    `json:"externalId"`
	Class      string    `json:"class"`
}

// SourceRef names the MachineSource of a Machine, in the Machine's namespace.
type SourceRef struct {
	Name string `json:"name"`
}

// MachineStatus is what the source last reported.
type MachineStatus struct {
	Phase             string       `json:"phase,omitempty"`
	Ownership         string       `json:"ownership,omitempty"`
	OwnerRef          *OwnerRef    `json:"ownerRef,omitempty"`
	ProviderID        string       `json:"providerId,omitempty"`
	DisplayName       string       `json:"displayName,omitempty"`
	PrimaryAddress    string       `json:"primaryAddress,omitempty"`
	Addresses         []Address    `json:"addresses,omitempty"`
	Fingerprint       *Fingerprint `json:"fingerprint,omitempty"`
	OS                *OS          `json:"os,omitempty"`
	FirstSeen         *metav1.Time `json:"firstSeen,omitempty"`
	LastSeen          *metav1.Time `json:"lastSeen,omitempty"`
	LastEnumerationID string       `json:"lastEnumerationId,omitempty"`
	StaleSince        *metav1.Time `json:"staleSince,omitempty"`
}

// OwnerRef names the object that owns a declared or adopted Machine.
type OwnerRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// Address is one address of a Machine. Type is Private or Public.
type Address struct {
	Type    string `json:"type"`
	IP      string `json:"ip"`
	MAC     string `json:"mac,omitempty"`
	Network string `json:"network,omitempty"`
}

// Fingerprint holds the identifiers that tell one machine from another. An empty
// string means unknown.
type Fingerprint struct {
	FirmwareUUID string `json:"firmwareUUID,omitempty"`
	InstanceUUID string `json:"instanceUUID,omitempty"`
	Serial       string `json:"serial,omitempty"`
	MachineID    string `json:"machineID,omitempty"`
}

// OS is the operating system of a Machine.
type OS struct {
	Family  string `json:"family,omitempty"`
	Version string `json:"version,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
}

// MachineSource is one place the inventory controller reads machines from.
type MachineSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineSourceSpec   `json:"spec"`
	Status            MachineSourceStatus `json:"status,omitempty"`
}

// MachineSourceSpec says what to read and how missing machines are handled.
type MachineSourceSpec struct {
	Type      string     `json:"type"`
	Suspended bool       `json:"suspended,omitempty"`
	Stale     *StaleSpec `json:"stale,omitempty"`
}

// StaleSpec holds the rules for machines that a source no longer lists. A nil
// pointer or an empty string takes the default of the definition.
type StaleSpec struct {
	HoldPercent    *int32 `json:"holdPercent,omitempty"`
	HoldMinimum    *int32 `json:"holdMinimum,omitempty"`
	RetireAfter    string `json:"retireAfter,omitempty"`
	PurgeAfter     string `json:"purgeAfter,omitempty"`
	ReleaseHoldFor string `json:"releaseHoldFor,omitempty"`
}

// MachineSourceStatus is the result of the last enumeration.
type MachineSourceStatus struct {
	LastEnumeration *LastEnumeration   `json:"lastEnumeration,omitempty"`
	Held            *Held              `json:"held,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// LastEnumeration describes the newest enumeration of a source.
type LastEnumeration struct {
	ID          string        `json:"id"`
	StartedAt   metav1.Time   `json:"startedAt"`
	FinishedAt  metav1.Time   `json:"finishedAt"`
	Complete    bool          `json:"complete"`
	Sightings   int32         `json:"sightings"`
	Created     int32         `json:"created"`
	MarkedStale int32         `json:"markedStale"`
	Scopes      []ScopeStatus `json:"scopes,omitempty"`
}

// ScopeStatus is the result of reading one scope of a source.
type ScopeStatus struct {
	Name     string `json:"name"`
	Readable bool   `json:"readable"`
	Error    string `json:"error,omitempty"`
	Count    int32  `json:"count"`
}

// Held is present while a sweep that would mark too many machines is held.
type Held struct {
	EnumerationID  string      `json:"enumerationId"`
	WouldMarkStale int32       `json:"wouldMarkStale"`
	Live           int32       `json:"live"`
	Since          metav1.Time `json:"since"`
}
