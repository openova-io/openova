// capabilities.go — the optional capability interfaces of a provider.
//
// An adapter implements only those it needs. A caller reaches one with
// As[T](name): ok is false when the adapter does not implement T. An area
// that needs a new capability adds its interface here, with its reader;
// the registry needs no change.

package providers

import "context"

// Preflighter runs before `tofu plan`. Replaces VPCQuotaHook and VPCReclaimHook.
type Preflighter interface {
	Preflight(ctx context.Context, in PreflightInput) (PreflightResult, error)
}

// PreflightInput is the input of Preflighter.Preflight.
type PreflightInput struct {
	Spec         ProvisionSpec
	ProtectedIDs map[string]struct{} // 8-character deployment-id prefixes that must not be touched
	// InState holds the resource instance addresses that the deployment's
	// OpenTofu state already holds; empty on a new install. An adapter
	// creates no object for an address in it: the state owns that one.
	InState  map[string]bool
	Progress func(msg string)
}

// PreflightResult is the result of Preflighter.Preflight.
type PreflightResult struct {
	// Imports are the objects the adapter created in the cloud before the
	// plan, for resources of the module to own. The engine imports each
	// into the state before `tofu plan`, so the plan uses the object instead
	// of creating its own, and the state knows every object of the install.
	Imports []StateImport
}

// StateImport is one object for `tofu import`.
type StateImport struct {
	Address string `json:"address"` // the module's resource instance address
	ID      string `json:"id"`      // the cloud's id of the object
	Label   string `json:"label"`   // what the object is (an IP address, a name), never a secret
}

// PostApplier runs after a successful `tofu apply`. Replaces NATEIPPreflightHook.
// An object that a PostApply step creates or replaces in the cloud is not in
// the state; such a step must return ReapplyNeeded instead, or the object
// must be made before the plan (PreflightResult.Imports).
type PostApplier interface {
	PostApply(ctx context.Context, in PostApplyInput) (PostApplyResult, error)
}

// PostApplyInput is the input of PostApplier.PostApply.
type PostApplyInput struct {
	Spec     ProvisionSpec
	Outputs  map[string]any // contract outputs, decoded JSON
	Progress func(msg string)
}

// PostApplyResult is the result of PostApplier.PostApply.
type PostApplyResult struct {
	ReapplyNeeded bool // true = the engine runs one more `tofu apply`
}

// ApplyErrorClassifier turns tofu error text into a decision. Replaces the
// provider string match of the provisioning engine.
type ApplyErrorClassifier interface {
	ClassifyApplyError(stderr string) ApplyErrorClass
}

// ApplyErrorClass is the decision of ApplyErrorClassifier.
type ApplyErrorClass struct {
	Retryable bool
	Reason    string // shown to the user; "" = use the engine's text
	// Addresses are the resource addresses the failure names. Across
	// consecutive retryable failures the engine counts the failures of each
	// address; an address that a failure does not name starts again at 0.
	Addresses []string
	// StopOnRepeat stops the retries when an address of this failure has now
	// failed three consecutive times: more attempts cannot help.
	StopOnRepeat bool
}

// OrphanSweeper replaces the five SweepOrphan* methods.
type OrphanSweeper interface {
	SweepKinds() []string // stable kind names, for logs and reports
	SweepOrphans(ctx context.Context, in SweepInput) (SweepResult, error)
}

// SweepInput is the input of OrphanSweeper.SweepOrphans.
type SweepInput struct {
	Creds        ProviderCreds
	CloudRegion  string
	ProtectedIDs map[string]struct{}
	Destructive  bool
	Progress     func(msg string)
}

// SweepResult is the result of OrphanSweeper.SweepOrphans.
type SweepResult struct{ Deleted map[string]int } // kind -> count

// LivenessProber replaces HasServersByNamePrefix and the janitor's
// provider-name branch. It answers "does this deployment still own cloud
// resources".
type LivenessProber interface {
	HasLiveInfrastructure(ctx context.Context, deploymentID, sovereignFQDN string, creds ProviderCreds) (bool, error)
}

// FrontDoorReconciler replaces the gateway and console load-balancer member
// reconcilers. It is called by the in-Sovereign cloud controller, not by
// handlers.
type FrontDoorReconciler interface {
	ReconcileFrontDoor(ctx context.Context, in FrontDoorInput) (changed int, err error)
}

// FrontDoorInput is the input of FrontDoorReconciler.ReconcileFrontDoor.
type FrontDoorInput struct {
	Creds         ProviderCreds
	CloudRegion   string
	SovereignFQDN string
	Door          string // "gateway" | "console"
	PrimaryIPs    []string
	PeerIPs       []string
	HTTPSPort     int
	HTTPPort      int
	SweepStale    bool
	Progress      func(msg string)
}

// LegacyAliaser lets an adapter declare old JSON field names that still
// arrive on the wire while the console is being moved to the credentials
// map. Key = legacy JSON field, value = credential key. Transitional.
type LegacyAliaser interface {
	LegacyCredentialAliases() map[string]string
}

// AdoptionMapper maps this provider's OpenTofu resource types to the neutral
// adoption kinds (loadbalancer, server, network, subnet, firewall, eip,
// peering). Transitional.
type AdoptionMapper interface {
	AdoptionKinds() map[string]string // OpenTofu resource type -> neutral kind
}

// ObjectStorageValidator replaces the second registry of package
// objectstorage. Same two methods, same signatures.
type ObjectStorageValidator interface {
	ObjectStorageEndpoint(region string) string
	ValidateObjectStorage(ctx context.Context, region, accessKey, secretKey string) (bool, error)
}
