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
	Preflight(ctx context.Context, in PreflightInput) error
}

// PreflightInput is the input of Preflighter.Preflight.
type PreflightInput struct {
	Spec         ProvisionSpec
	ProtectedIDs map[string]struct{} // 8-character deployment-id prefixes that must not be touched
	Progress     func(msg string)
}

// PostApplier runs after a successful `tofu apply`. Replaces NATEIPPreflightHook.
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
