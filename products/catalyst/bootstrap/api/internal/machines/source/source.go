// Package source holds what a MachineSource can see, as plain values, and the
// interface every kind of source implements. The inventory controller turns an
// Enumeration into Machine objects; a source never writes to the cluster.
package source

import (
	"context"
	"time"
)

// Class is the class of a machine: "node", "virtual-machine", "host" or "appliance".
type Class string

// PowerState is "Running", "Stopped" or "Unknown".
type PowerState string

// The classes and power states.
const (
	ClassNode           Class      = "node"
	ClassVirtualMachine Class      = "virtual-machine"
	ClassHost           Class      = "host"
	ClassAppliance      Class      = "appliance"
	PowerRunning        PowerState = "Running"
	PowerStopped        PowerState = "Stopped"
	PowerUnknown        PowerState = "Unknown"
)

// Address is one address of a machine. Type is "Private" or "Public".
type Address struct {
	Type    string
	IP      string
	MAC     string
	Network string
}

// Fingerprint holds the identifiers that tell one machine from another.
type Fingerprint struct {
	FirmwareUUID string
	InstanceUUID string
	Serial       string
	MachineID    string
}

// Sighting is one machine as a source lists it.
type Sighting struct {
	ExternalID  string // required, stable at the source
	DisplayName string
	Class       Class
	PowerState  PowerState
	ProviderID  string // "<pack name>:<cloud id>" or ""
	Addresses   []Address
	Fingerprint Fingerprint
	OSFamily    string
	OSVersion   string
	Arch        string
	Kernel      string
	VCPU        int
	MemoryMiB   int
	Flavor      string
	Tags        map[string]string
	Parent      string // ExternalID of the host it runs on, or ""
}

// ScopeResult is the result of reading one scope of a source.
type ScopeResult struct {
	Name     string
	Readable bool
	Error    string
	Count    int
}

// Enumeration is one full listing of a source.
type Enumeration struct {
	ID         string // ULID, set by the caller
	StartedAt  time.Time
	FinishedAt time.Time
	Complete   bool // true only when every ScopeResult is Readable
	Scopes     []ScopeResult
	Sightings  []Sighting
}

// Source lists what one MachineSource can see. Implementations: nodes, declared, cloudaccount.
type Source interface {
	Type() string
	// Enumerate never returns a partial list as complete. An error means nothing was read.
	Enumerate(ctx context.Context) (Enumeration, error)
}

// ChangeType is "Upsert" or "Gone".
type ChangeType string

// The change types.
const (
	ChangeUpsert ChangeType = "Upsert"
	ChangeGone   ChangeType = "Gone"
)

// Change is one change that a Watcher reports.
type Change struct {
	Type     ChangeType
	Sighting Sighting
}

// Watcher is optional. Watch blocks until ctx ends; a returned error makes the caller fall back to the interval.
type Watcher interface {
	Watch(ctx context.Context, sink func(Change)) error
}
