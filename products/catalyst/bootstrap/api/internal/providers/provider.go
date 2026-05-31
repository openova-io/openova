// Package providers defines the canonical cloud-provider abstraction for
// catalyst-api Phase-0 provisioning + Phase-1 lifecycle operations.
//
// Background (founder mandate, 2026-05-23): Phase-0 was implemented
// Hetzner-first; the OpenTofu module at infra/hetzner/, the orphan-purge
// fallback at internal/hetzner/, and the object-storage bucket lifecycle
// at internal/objectstorage/hetzner/ all hard-code Hetzner-specific
// concepts (hcloud server / lb / network / firewall / minio_s3_bucket).
// When the platform expands to other clouds — Huawei (active customer
// pipeline, Issue #1841), AWS, GCP, Azure — every Phase-0 entry point
// would have needed a branch. This package interposes a thin interface
// at the boundary so each new cloud is "implement CloudProvider once",
// not "scatter changes across N handlers".
//
// Architectural pattern: Ports + Adapters (hexagonal). The catalyst-api
// handlers (the "core" of the hexagon) speak only to this package's
// CloudProvider interface (the "port"). Each provider package under
// providers/<name>/ is an "adapter" that translates the interface to
// the provider's native APIs + OpenTofu module.
//
// Scope of Wave 2 (this PR): introduce the interface + types + registry
// + Hetzner adapter (delegates to existing packages — byte-equivalent
// behavior) + Huawei stub. NO existing call sites are migrated yet; the
// seam exists and is wired into the registry so future PRs can switch
// individual handlers (Provision, Wipe, ListServers, ValidateCreds) to
// use providers.Get(dep.Provider) without touching the underlying
// hetzner/provisioner/objectstorage code.
//
// See providers/PROVIDER-INTERFACE.md for the cross-cutting contract
// each provider adapter MUST honour (variable/output contract on the
// OpenTofu module, cloud-init template variables, credential shape,
// idempotency guarantees on Wipe).
package providers

import (
	"context"
	"time"
)

// CloudProvider is the canonical seam between catalyst-api's
// cloud-agnostic Phase-0 / Phase-1 code paths and the per-cloud
// implementation.
//
// Every method is context-aware so callers can enforce timeouts +
// cancellation (e.g. a 30-minute cap on Provision, 5-minute cap on
// Wipe's per-resource sweep).
//
// All methods MUST be safe to call concurrently against DIFFERENT
// deployments. Concurrency on the SAME deployment is the caller's
// responsibility (catalyst-api serialises via the per-deployment
// mutex in handler.Deployment).
type CloudProvider interface {
	// Name returns the canonical provider name. MUST match the
	// `Provider` field on the deployment record + the provider enum
	// in core/controllers/environment/api/v1/types.go ("hetzner",
	// "huawei", "aws", "gcp", "azure", "oci", "contabo").
	Name() string

	// Provision creates the cloud infrastructure for one deployment
	// per the supplied spec. It runs `tofu apply` against this
	// provider's canonical module (infra/providers/<name>/), emits
	// progress events on the supplied channel, and returns the live
	// result (control-plane IP, LB IP, kubeconfig path) on success.
	//
	// MUST be idempotent: re-running Provision on a deployment whose
	// tofu workdir already holds state returns the existing outputs
	// without re-creating cloud resources.
	Provision(ctx context.Context, spec ProvisionSpec, events chan<- ProvisionEvent) (*ProvisionResult, error)

	// Wipe destroys every cloud resource bound to the deployment.
	// Idempotent — re-calling on an already-wiped deployment returns
	// a report with zero deletions and no error.
	//
	// Implementations MUST run the canonical purge sequence:
	//   1. `tofu destroy` against the per-deployment workdir
	//   2. Provider-side orphan sweep (labels + name prefixes)
	//   3. Object-storage bucket purge (if the provider's tofu
	//      module created any)
	// Step 2 + 3 are belt-and-braces against tofu state corruption.
	Wipe(ctx context.Context, spec WipeSpec, progress func(msg string)) (*WipeResult, error)

	// ListServers returns the current cloud-side inventory of
	// servers (control-plane + worker) for one deployment. Used by
	// the wizard's "infrastructure" tab + by the wipe handler's
	// pre-flight check.
	//
	// Returns nil + nil error when the deployment has no live
	// infrastructure (already wiped, or never provisioned).
	ListServers(ctx context.Context, deploymentID, sovereignFQDN string, creds ProviderCreds) ([]ServerInfo, error)

	// ValidateCreds checks that the supplied provider credentials
	// are usable BEFORE the deployment record is persisted. Called
	// by the wizard's POST /deployments handler so the operator
	// gets a fast 400 on a bad token instead of a 30-minute tofu
	// failure.
	//
	// Returns nil when the creds authenticate against the
	// provider's API surface; an error with a human-readable
	// message otherwise.
	ValidateCreds(ctx context.Context, creds ProviderCreds) error
}

// ProvisionSpec is the cloud-agnostic input to CloudProvider.Provision.
// Per-cloud specifics (Hetzner project ID, Huawei AK/SK, etc.) live in
// the Creds map so the interface itself stays neutral.
type ProvisionSpec struct {
	// DeploymentID — opaque 16-byte hex identifier assigned by
	// catalyst-api at POST /deployments time. Stamped on every
	// cloud resource (label `catalyst.openova.io/deployment-id`)
	// so Wipe can scope its orphan sweep.
	DeploymentID string

	// SovereignFQDN — the new Sovereign's fully-qualified domain
	// (e.g. "omantel.omani.works"). Used by the cloud-init
	// template + the LE certificate issuance flow.
	SovereignFQDN string

	// Regions — per-region sizing + role assignment. Length ≥ 1.
	// Regions[0] is the primary; Regions[1:] are secondaries.
	Regions []RegionSpec

	// ParentDomains — pool + BYO parent domains the Sovereign owns
	// (e.g. omani.homes / omani.rest / omani.trade). The
	// pool-domain-manager + ParentDomain controller use these for
	// per-Org subdomain allocation.
	ParentDomains []ParentDomain

	// Creds — provider-specific credentials (Hetzner token, S3
	// access key, etc.). Shape MUST match what the provider
	// adapter expects; see providers/<name>/README.md for the
	// per-provider key list.
	Creds ProviderCreds

	// GitOpsRepoURL — per-Sovereign Gitea / GitHub repo URL the
	// Sovereign's Flux source pulls from after handover.
	GitOpsRepoURL string

	// HandoverJWTPublic — RSA public key (PEM) the new Sovereign's
	// /auth/handover endpoint uses to validate the bearer JWT the
	// wizard mints on Phase-1 completion.
	HandoverJWTPublic string

	// Raw — provider-agnostic per-deployment knobs the adapter may
	// thread through to its OpenTofu module. Each adapter
	// documents which keys it consumes. Kept as a map so the
	// interface doesn't need bumping for every new flag.
	Raw map[string]string
}

// RegionSpec — per-region sizing + role for one provisioning request.
type RegionSpec struct {
	// Code — the provider's region identifier ("fsn1", "hel1" for
	// Hetzner; "cn-north-4" for Huawei).
	Code string

	// Role — "primary" or "secondary". The primary region runs the
	// Catalyst control plane + the first CNPG cluster; secondary
	// regions host vClusters + CNPG ReplicaCluster mirrors.
	Role string

	// ControlPlaneSize — provider-specific instance type for the
	// control-plane node (e.g. "cpx52" on Hetzner).
	ControlPlaneSize string

	// WorkerSize — provider-specific instance type for worker
	// nodes. Ignored when WorkerCount=0 (combined CP+worker).
	WorkerSize string

	// WorkerCount — number of dedicated worker nodes. 0 = combined
	// CP+worker (the canonical multi-region topology, per
	// feedback_multiregion_topology_1_cpx52_per_region.md).
	WorkerCount int
}

// ParentDomain — one parent domain the Sovereign owns. Mirrors the
// shape provisioner.ParentDomain consumes; replicated here so the
// providers package doesn't need to import the provisioner package
// (and create an import cycle).
type ParentDomain struct {
	Name       string `json:"name"`
	Role       string `json:"role,omitempty"`
	Registrar  string `json:"registrar,omitempty"`
	ProvisionedAt string `json:"provisionedAt,omitempty"`
}

// ProviderCreds — opaque per-provider credential bag. Each provider
// adapter publishes the expected keys + their meaning. Kept as a map
// so adding a new provider does not require an interface bump.
//
// Hetzner uses: hcloud_token, hcloud_project_id, object_storage_access_key,
//               object_storage_secret_key, object_storage_region
// Huawei (planned) will use: ak, sk, project_id, region
type ProviderCreds struct {
	Raw map[string]string
}

// Get is a small ergonomic helper — returns the value for `key` (empty
// string when absent). Wraps the underlying map so callers don't
// repeat the nil-check.
func (c ProviderCreds) Get(key string) string {
	if c.Raw == nil {
		return ""
	}
	return c.Raw[key]
}

// ProvisionEvent — a single progress emit on the Provision channel.
// Shape mirrors provisioner.Event so callers can convert without
// re-formatting.
type ProvisionEvent struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
}

// ProvisionResult — what CloudProvider.Provision returns on success.
// The cloud-agnostic subset of provisioner.Result; provider-specific
// extras (Hetzner project ID echo, etc.) stay in the persisted
// deployment record, not on this struct.
type ProvisionResult struct {
	SovereignFQDN  string
	ControlPlaneIP string
	LoadBalancerIP string
	ConsoleURL     string
	GitOpsRepoURL  string
	KubeconfigPath string
}

// WipeSpec — input to CloudProvider.Wipe. Carries everything the
// adapter needs to (a) destroy the tofu workdir, (b) sweep
// cloud-side orphans, (c) purge the object-storage bucket.
type WipeSpec struct {
	DeploymentID  string
	SovereignFQDN string

	// Creds — same shape as ProvisionSpec.Creds. Re-prompted by
	// the wizard on the Cancel & Wipe modal because catalyst-api
	// GCs the original credentials post-provision (credential
	// hygiene, see handler/wipe.go for rationale).
	Creds ProviderCreds
}

// WipeResult — generic, provider-agnostic summary of what Wipe
// actually destroyed. Per-resource-kind counts live in
// ProviderPurge so the shape stays neutral (Hetzner has
// servers/lbs/networks/firewalls/ssh-keys/volumes/primary-ips;
// Huawei will have ECS / ELB / VPC / SecurityGroup; the shape
// must accommodate both without per-provider fields on the
// public struct).
type WipeResult struct {
	DeploymentID  string
	SovereignFQDN string

	// TofuDestroyed — true when `tofu destroy` ran to completion
	// (zero remaining state). False when destroy failed; the
	// provider-side orphan sweep then becomes the primary
	// cleanup path.
	TofuDestroyed bool

	// ProviderPurge — keyed by resource-kind ("servers",
	// "load_balancers", "networks", "firewalls", "ssh_keys",
	// "s3_buckets", "volumes", "primary_ips"…). Each value
	// is the slice of names/IDs the adapter actually deleted.
	// Empty kinds may be omitted.
	ProviderPurge map[string][]string

	// S3Buckets — names of object-storage buckets that were
	// purged + deleted. Provider-specific but always reported
	// the same way (slice of bucket names).
	S3Buckets []string

	// Errors — human-readable error messages from each step
	// that failed. A non-empty slice does NOT necessarily mean
	// total failure (the canonical handler treats partial
	// success as 200 with a non-empty errors body).
	Errors []string

	// G103 (Refs #2670) — Post-wipe orphan-verification residuals.
	// Keyed by resource-kind (same vocab as ProviderPurge); each value
	// is the slice of resource names that the post-wipe scan found
	// STILL PRESENT with a catalyst-* prefix (bastion-* hard-excluded).
	// A non-empty map means the wipe contract was violated:
	// downstream prov attempts on the same account will hit quota +
	// name-collision failures.
	//
	// The canonical wipe contract (per G103 issue acceptance):
	//   - 0 VPCs (besides bastion)
	//   - 0 EIPs (besides bastion)
	//   - 0 NAT gateways (besides bastion)
	//   - 0 ECSs (besides bastion)
	//   - 0 keypairs (besides bastion-openova-kp)
	//
	// Providers that implement orphan verification populate this map;
	// providers that don't leave it nil. The wipe handler logs any
	// non-empty entries at error level and surfaces them in the
	// SSE event stream so the operator sees real residual.
	ResidualOrphans map[string][]string

	// G103 (Refs #2670) — VerifiedZeroOrphans is true when the
	// provider ran a post-wipe orphan scan AND the scan found zero
	// catalyst-* resources (bastion-* excluded). Providers that don't
	// implement verification leave this false. The operator-facing
	// audit-hcs verification + the CI zero-orphan gate read this
	// boolean as the canonical "wipe was complete" signal.
	VerifiedZeroOrphans bool

	// WipedAt — wall-clock timestamp the Wipe call returned.
	WipedAt time.Time
}

// ObjectStorageNamer is an OPTIONAL extension implemented by providers
// that mint object-storage buckets at Phase-0 (Hetzner today, future AWS
// and GCP). Handlers that need to derive a per-deployment bucket name
// type-assert the provider to this interface; providers that don't mint
// object storage (e.g. an on-prem provider with pre-existing storage)
// simply don't implement it.
//
// The methods are pure (no I/O, no API calls) — they exist on the
// abstraction surface so handlers don't have to import provider-specific
// packages just to compute a bucket name.
type ObjectStorageNamer interface {
	// BucketNameForDeployment returns the canonical bucket name for
	// one (SovereignFQDN, deploymentID) pair. MUST be deterministic
	// so the wipe handler can reproduce the same name post-restart.
	BucketNameForDeployment(sovereignFQDN, deploymentID string) string

	// BucketNamePrefixForSovereign returns the prefix every bucket
	// for this Sovereign shares. Used by the wipe handler's
	// orphan-bucket sweep to grep across deployment re-provisions.
	BucketNamePrefixForSovereign(sovereignFQDN string) string
}

// ServerInfo — one cloud-side server in the deployment's inventory.
// Provider-agnostic shape; per-cloud fields (Hetzner image ID,
// Huawei AZ) live in Labels.
type ServerInfo struct {
	ID        string
	Name      string
	Region    string
	PublicIP  string
	PrivateIP string
	Status    string // "running", "stopped", "creating", "deleting", "error"
	Labels    map[string]string
}
