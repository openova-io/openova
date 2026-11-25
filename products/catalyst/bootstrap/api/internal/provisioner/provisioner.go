// Package provisioner is a thin wrapper around `tofu` (OpenTofu) — the
// canonical Phase 0 IaC layer per docs/ARCHITECTURE.md §10 and
// docs/SOVEREIGN-PROVISIONING.md §3.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #3: OpenTofu provisions Phase 0
// cloud resources, Crossplane is the ONLY day-2 IaC, Flux is the ONLY GitOps
// reconciler, Blueprints are the ONLY install unit. This package therefore
// does NOT call cloud APIs directly, does NOT exec helm/kubectl, does NOT
// construct cloud-init inline. All of that lives in the OpenTofu module at
// infra/hetzner/ and in Crossplane Compositions / Flux Kustomizations the
// module bootstraps into the cluster.
//
// What this package DOES:
//   - validate the wizard's request (well-formed inputs)
//   - write a tofu.auto.tfvars.json file for the OpenTofu module
//   - exec `tofu init && tofu apply -auto-approve` and stream stdout to the
//     wizard via SSE events
//   - return tofu output values (control_plane_ip, load_balancer_ip)
//     as the Result the wizard's success screen consumes. The
//     kubeconfig is NOT a tofu output — the new Sovereign's cloud-init
//     PUTs it back over the bearer-token endpoint (issue #183), and
//     the handler writes the path onto Result.KubeconfigPath.
//
// Crossplane adoption (Phase 1 hand-off) and bootstrap-kit installation
// (Cilium → cert-manager → Flux → Crossplane → ... → bp-catalyst-platform)
// happen INSIDE the cluster via Flux reconciling clusters/<sovereign-fqdn>/
// in this monorepo — NOT from this Go process. By the time `tofu apply`
// returns, the cluster is bootstrapping itself.
package provisioner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the API image holds no zoneinfo files: Validate checks sovereignTimeZone against this copy

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// ActiveDepPrefixesHook returns the do-not-touch allowlist of 8-char
// deployment-ID prefixes for EVERY live (non-`wiped`) deployment. nil by
// default — the handler registers it (→ Handler.buildActivePrefixes, the
// #4454 fail-safe). The in-band VPC-quota reclaim (#4614) consults it so
// the reclaim can never reap a live Sovereign's VPC that merely shares
// the project — the production-delete fault of 2026-06-28, where the
// reclaim's protect-set held only THIS prov's own prefix and so treated
// the live omantel.biz VPC as a reclaimable orphan.
var ActiveDepPrefixesHook func() map[string]struct{}

// reclaimProtectSet builds the do-not-touch allowlist for the in-band
// VPC-quota reclaim (#4614): EVERY live (non-`wiped`) deployment's 8-char
// prefix from ActiveDepPrefixesHook (the handler's #4454 fail-safe), plus
// this prov's own (not-yet-created) deployment prefix. SweepOrphanVPCs
// reaps any catalyst-* VPC NOT in this set, so a missing live prefix here
// means a live Sovereign's VPC gets deleted — the production-delete fault.
// nil hook (no handler wired, e.g. CI) degrades to protecting only the
// firing prov, the pre-#4614 behaviour.
func reclaimProtectSet(deploymentID string) map[string]struct{} {
	protect := map[string]struct{}{}
	if ActiveDepPrefixesHook != nil {
		for p := range ActiveDepPrefixesHook() {
			protect[p] = struct{}{}
		}
	}
	if len(deploymentID) >= 8 {
		protect[deploymentID[:8]] = struct{}{}
	}
	return protect
}

// ParentDomain is one entry in Request.ParentDomains — a registered
// parent zone the Sovereign-side PowerDNS becomes authoritative for
// after NS-flip lands at the registrar.
//
// Issue #826 (sub-1 of epic #825 — Multi-domain Sovereign) introduces
// this shape so a single Sovereign can own N parent domains rather
// than the legacy one-FQDN model. Day-1 (wizard provision) populates
// the slice with exactly ONE entry carrying Role="primary"; Day-2
// add-domain calls (issue #829) append additional entries with
// Role="org-pool". The provisioning pipeline iterates over the slice
// applying the same per-domain steps to each (NS-flip via registrar,
// PowerDNS zone create, cert-manager Certificate create) — see
// ProvisionParentDomain below.
//
// The wizard UI stays single-FQDN (per the SCOPE CORRECTION on issue
// #826: multi-domain capability is a Day-2 admin-console action). The
// catalyst-api translates the wizard's single FQDN into a 1-element
// ParentDomains array internally — operators never see this struct in
// the wizard.
//
// Field semantics:
//
//   - Name is the registered domain at the registrar
//     (e.g. "omani.works"). Must match parentDomainNamePattern.
//   - Role is one of ParentDomainRolePrimary | ParentDomainRoleOrgPool.
//     Exactly one entry in the slice carries the primary role; zero or
//     more carry org-pool. Validation enforces this at Validate() time.
//   - RegistrarKind is the adapter id used to flip the NS records at
//     the registrar (today: "dynadot"; future: "namecheap" / "godaddy"
//     / etc). Inviolable Principle #4 ("never hardcode") requires this
//     to be a runtime value, not a compile-time switch.
//   - RegistrarCredsRef is the name of a SealedSecret in
//     catalyst-system holding the registrar credentials. The
//     catalyst-api resolves this at provision time via the K8s API
//     (issue #829 wires the resolver). Empty means "fall back to the
//     same operator-supplied env-mounted credentials the catalyst-api
//     uses for the primary domain" — the typical case where one
//     Dynadot account holds every domain in the operator's portfolio.
//   - AddedAt is the UTC timestamp the domain was added to the pool.
//     Day-1 entries carry the deployment's StartedAt; Day-2 entries
//     carry the moment the admin console add-domain handler was
//     called. Stored on the persisted Record so the admin console can
//     render "added 3 days ago" on the parent-domains panel.
type ParentDomain struct {
	Name              string    `json:"name"`
	Role              string    `json:"role"`
	RegistrarKind     string    `json:"registrarKind,omitempty"`
	RegistrarCredsRef string    `json:"registrarCredsRef,omitempty"`
	AddedAt           time.Time `json:"addedAt,omitempty"`
}

// ParentDomain role constants. Exactly one ParentDomain in
// Request.ParentDomains carries ParentDomainRolePrimary; zero or more
// carry ParentDomainRoleOrgPool. The wizard's Day-1 path always
// produces a single primary entry — org-pool entries are appended
// post-handover via the admin console (issue #829).
const (
	// ParentDomainRolePrimary marks the unique parent domain that
	// hosts the Sovereign's own URLs (console.<primary>,
	// api.<primary>, marketplace.<primary>, ...). Its zone is the
	// authoritative source for the Sovereign's bootstrap-kit
	// HTTPRoutes.
	ParentDomainRolePrimary = "primary"

	// ParentDomainRoleOrgPool marks a parent domain offered to Organization
	// tenants for free-subdomain allocation. When an Organization signs up
	// under a Sovereign, they pick from the org-pool entries to
	// receive a free console.<org>.<org-pool> subdomain.
	ParentDomainRoleOrgPool = "org-pool"
)

// parentDomainNamePattern is the FQDN regex applied to ParentDomain.Name.
// Matches the wizard's isValidDomain helper (RFC 1035 labels, ≥ 2
// labels). Lower-cased before the test runs.
var parentDomainNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?(\.[a-z]([a-z0-9-]*[a-z0-9])?)+$`)

// defaultRegistrarKind is the registrar adapter used when a Day-1
// ParentDomain entry is synthesised from the legacy SovereignFQDN
// payload (the wizard captures a single FQDN + relies on the
// catalyst-api's env-mounted Dynadot credentials). Centralised here
// rather than scattered as a magic string — Inviolable Principle #4.
//
// Future operators on registrars other than Dynadot override this via
// CATALYST_DEFAULT_REGISTRAR_KIND on the catalyst-api Pod. The
// wizard's StepDomain stays unchanged: the operator ships their
// registrar credentials as env vars + sets this env to the matching
// adapter id.
const defaultRegistrarKind = "dynadot"

// s3BucketNamePattern enforces RFC-compliant S3 bucket naming on
// Request.ObjectStorageBucket per the rules at
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
// (Hetzner Object Storage applies the same lexical rules — they're a
// superset of the original S3 spec). Used by Validate(); also documented
// in infra/hetzner/variables.tf §object_storage_bucket_name's validation
// block so the same rule applies whether OpenTofu rejects it client-side
// or the catalyst-api does so server-side.
var s3BucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// BCP topology constants — the operator-visible enum for
// Request.BcpTopology. See the doc comment on the field for the full
// semantic contract.
const (
	BcpTopologySingleRegion     = "single-region"
	BcpTopologyActiveHotStandby = "active-hotstandby"
	BcpTopologyActiveActive     = "active-active"
)

// validBcpTopologies is the wire-allowed set the validator checks
// against. This is the operator-signup PLACEMENT-EDITOR DIALECT — kept in
// sync with the Sovereign CRD enum (`spec.bcpTopology` in
// products/catalyst/chart/crds/sovereign.yaml) and the
// applications_update.go placement.mode set. NOTE (#3648): the separate
// cnpg-pair DR CRD (`cnpgpairs.dr.openova.io` spec.topology) uses the
// CANONICAL hyphenated vocabulary (`active-hot-standby`); do NOT "resync"
// this dialect set to it — they are intentionally different axes (signup
// dialect vs canonical DR-contract). The dialect→canonical mapping lives
// in endpoint_handler.go::canonicalizeTopology.
var validBcpTopologies = map[string]struct{}{
	BcpTopologySingleRegion:     {},
	BcpTopologyActiveHotStandby: {},
	BcpTopologyActiveActive:     {},
}

// deriveBcpTopology applies the auto-derivation rule documented on
// Request.BcpTopology:
//
//   - Empty + len(Regions) >= 2  → "active-hotstandby"
//   - Empty + len(Regions) <  2  → "single-region"
//   - Explicit value preserved verbatim
//
// Pulled into a helper so the unit tests and writeTfvars share one
// source of truth — there is exactly one place that decides what the
// effective topology is for a given Request.
func deriveBcpTopology(req Request) string {
	t := strings.TrimSpace(req.BcpTopology)
	if t != "" {
		return t
	}
	if len(req.Regions) >= 2 {
		return BcpTopologyActiveHotStandby
	}
	return BcpTopologySingleRegion
}

// bcpTopologyEnableHotStandby maps the effective topology to the
// `enable_hot_standby` tofu var (string "true"/"false"). The
// `active-active` topology is treated as a superset of
// `active-hotstandby` at the cnpg-pair layer (active-active still
// requires a primary + replica CNPG pair under the hood per docs/
// SRE.md §2 sync-replica matrix); the symmetric routing knob is a
// separate workstream (G93.4 Refs #2669).
func bcpTopologyEnableHotStandby(topology string) string {
	switch topology {
	case BcpTopologyActiveHotStandby, BcpTopologyActiveActive:
		return "true"
	default:
		return "false"
	}
}

// consoleIsolationEnabled resolves the #4053 console-isolation toggle (Refs
// #4431 #4212) for a Request, applying the default-TRUE rule:
//
//   - Request.ConsoleIsolationEnabled == nil (the POST omitted the field — the
//     common case + every legacy/automation payload) → true (the canonical
//     production posture: dedicated console ELB + isolated cilium-gateway-
//     console, byte-identical to pre-#4431 behaviour).
//   - Request.ConsoleIsolationEnabled == &false → false (the 3-EIP single-
//     region validation shape: no console ELB, console front doors collapse
//     onto elb_primary + the shared cilium-gateway).
//   - Request.ConsoleIsolationEnabled == &true → true (explicit production).
//
// One source of truth shared by writeTfvars() and the unit tests.
func consoleIsolationEnabled(req Request) bool {
	if req.ConsoleIsolationEnabled == nil {
		return true
	}
	return *req.ConsoleIsolationEnabled
}

// DeriveStorageClass applies the #4057 defaulting rule for Request.StorageClass:
//
//   - Explicit non-empty value (operator chose a class in the wizard or via
//     the API) → preserved verbatim, trimmed of surrounding whitespace.
//   - Empty (the common case) → spec.storage.defaultClass of the effective
//     cloud profile of the request's provider (EffectiveProfile).
//
// Empty when the class is empty and the profile cannot be read. One source of
// truth shared by writeTfvars(), the deployment view and the unit tests.
func DeriveStorageClass(req Request) string {
	sc, _ := storageClass(req)
	return sc
}

// storageClass is DeriveStorageClass with the error of the profile read.
func storageClass(req Request) (string, error) {
	if sc := strings.TrimSpace(req.StorageClass); sc != "" {
		return sc, nil
	}
	profile, err := EffectiveProfile(req.Provider, req.Installation)
	if err != nil {
		return "", err
	}
	return profile.Spec.Storage.DefaultClass, nil
}

// ProvidersDir holds the provider packs (contract C2): the cloud profiles
// the engine reads. The catalyst-api image carries them at
// cloudprofile.DefaultDir; tests repoint it.
var ProvidersDir = cloudprofile.DefaultDir

// ReadInstallation returns an installation document by its metadata.name.
// Nil: a request that names an installation is refused.
var ReadInstallation cloudprofile.InstallationReader

// EffectiveProfile returns the cloud profile of the pack named provider,
// merged with the installation document named installation when that is not
// empty. The pack is read from ProvidersDir and the document through
// ReadInstallation.
func EffectiveProfile(provider, installation string) (*cloudprofile.Profile, error) {
	name := strings.ToLower(strings.TrimSpace(provider))
	profiles, err := cloudprofile.Load(ProvidersDir)
	if err != nil {
		return nil, fmt.Errorf("cloud profiles: %w", err)
	}
	pack, ok := profiles[name]
	if !ok {
		return nil, fmt.Errorf("cloud profile: no pack %q in %s", name, ProvidersDir)
	}
	installation = strings.TrimSpace(installation)
	if installation == "" {
		return pack, nil
	}
	if ReadInstallation == nil {
		return nil, fmt.Errorf("cloud profile: installation %q cannot be read here", installation)
	}
	data, err := ReadInstallation(context.Background(), installation)
	if err != nil {
		return nil, fmt.Errorf("cloud profile: installation %q: %w", installation, err)
	}
	doc, err := cloudprofile.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("cloud profile: installation %q: %w", installation, err)
	}
	if doc.Metadata.Name != installation {
		return nil, fmt.Errorf("cloud profile: installation %q: the document is %q", installation, doc.Metadata.Name)
	}
	return cloudprofile.Merge(pack, doc)
}

// RegionSpec is one entry in Request.Regions — the per-region sizing
// payload the wizard's StepProvider produces. Each topology slot has its
// own provider, its own cloud-region, and its own SKU vocabulary, so the
// canonical request shape carries one of these per slot.
//
// SKU strings are the provider's NATIVE instance-type identifier (cx32,
// VM.Standard.E5.Flex.4.32, m6i.xlarge, Standard_D4s_v5, ...). The
// OpenTofu module receives them verbatim via tofu.auto.tfvars.json and
// the provider's API validates them at apply time. The wizard reads
// every legal id from products/catalyst/bootstrap/ui/src/shared/constants/
// providerSizes.ts (PROVIDER_NODE_SIZES) — there is no SKU literal
// anywhere else in the wizard.
type RegionSpec struct {
	Provider         string `json:"provider"`
	CloudRegion      string `json:"cloudRegion"`
	ControlPlaneSize string `json:"controlPlaneSize"`
	WorkerSize       string `json:"workerSize"`
	WorkerCount      int    `json:"workerCount"`

	// Code, Zone and Role — the region fields of module contract version 1
	// (infra/providers/_contract/contract.json, CLD-090). Code is the region
	// code: every module keys its per-region resources and its output
	// control_plane_ips_per_region by it. Empty means the value of
	// CloudRegion. Zone is the availability zone, written to the module as
	// availability_zone; empty means the module's own choice. Role is kept
	// when a request or a stored record carries it and is never read:
	// writeTfvars takes the role from the position (index 0 is "primary",
	// every other entry is "secondary").
	Code string `json:"code,omitempty"`
	Zone string `json:"zone,omitempty"`
	Role string `json:"role,omitempty"`

	// StorageClass — optional per-region StorageClass carried on the wire
	// for forward-compatibility (#4057). Today the effective class is a
	// single per-Sovereign value: Validate() folds Regions[0].StorageClass
	// into the umbrella Request.StorageClass when the umbrella value is
	// empty (mirroring the ControlPlaneSize/WorkerSize per-region→singular
	// derivation), and writeTfvars threads that one umbrella value into the
	// `default_storage_class` tofu var → the cloud-init
	// `SOVEREIGN_CNPG_STORAGE_CLASS` substitute. Per-region cloud-init
	// rendering of distinct classes (the mixed-provider multi-region case)
	// would also require extending the tofu `regions` object type and the
	// template substitute, which is out of scope here — so a value set on a
	// non-primary region is currently accepted but not independently
	// rendered. Kept on the struct so that extension is additive.
	StorageClass string `json:"storageClass,omitempty"`

	// ClusterMeshName — Cilium ClusterMesh peer name override for this
	// secondary region. Empty = auto-derive as
	// `<sovereign-stem>-<region-code-no-digits>` (e.g. omantel + hel1
	// -> omantel-hel) when the umbrella Request.ClusterMeshName is set.
	// Allocated via docs/CLUSTERMESH-CLUSTER-IDS.md. Per
	// docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the operator
	// MAY override per-region when the auto-derive convention conflicts
	// with an existing peer name in the registry.
	ClusterMeshName string `json:"clusterMeshName,omitempty"`
}

// Request carries the wizard inputs the OpenTofu module needs.
type Request struct {
	OrgName  string `json:"orgName"`
	OrgEmail string `json:"orgEmail"`

	SovereignFQDN       string `json:"sovereignFQDN"`
	SovereignDomainMode string `json:"sovereignDomainMode"` // pool | byo
	SovereignPoolDomain string `json:"sovereignPoolDomain"`
	SovereignSubdomain  string `json:"sovereignSubdomain"`

	// SovereignTimeZone is the Sovereign's own IANA time zone (the wizard
	// proposes the browser's). childVariables writes it as SOVEREIGN_TIME_ZONE
	// of the variables document, UTC when it is empty; kured's default reboot
	// span, 01:00 to 05:00, is read in it (kit slot 04b).
	SovereignTimeZone string `json:"sovereignTimeZone,omitempty"`

	// ParentDomains — the canonical multi-domain payload introduced by
	// issue #826 (sub-1 of epic #825 — Multi-domain Sovereign). The
	// Sovereign-side PowerDNS becomes authoritative for every entry
	// here; exactly one entry carries Role="primary" (hosts
	// console/api/marketplace), zero-or-more carry Role="org-pool"
	// (offered to Organization tenants for free subdomains).
	//
	// Backward compatibility: when the wizard ships a payload without
	// this field (every wizard payload as of issue #826 — the wizard
	// stays single-FQDN per the scope correction), Validate()
	// synthesises a single primary entry from SovereignPoolDomain (or
	// SovereignFQDN when domain_mode is byo). Existing on-disk
	// deployment records persisted under the legacy single-FQDN shape
	// thus deserialize cleanly + on next Save() round-trip the
	// synthesised array, achieving the migration the issue's DoD calls
	// for without a one-shot migration step.
	//
	// Day-2 add-domain (issue #829) appends to this slice via the same
	// per-domain abstraction the Day-1 path uses — see
	// ProvisionParentDomain.
	ParentDomains []ParentDomain `json:"parentDomains,omitempty"`

	// Provider — the provider pack and CloudProvider adapter of the
	// deployment, one of providers.Names(). Required: Validate() returns a
	// *providers.FieldError on "provider" for an empty value and
	// `unsupported provider "<x>"; known: <names>` for an unknown one. It is
	// never defaulted and never inferred from the credentials present.
	Provider string `json:"provider,omitempty"`

	// Installation — the installation document laid over the pack profile of
	// Provider (its metadata.name). Empty: the pack profile alone.
	Installation string `json:"installation,omitempty"`

	// Credentials — the credential map of the provider, keyed by the
	// credential fields of its cloud profile (spec.credentials.fields[].key).
	// UnmarshalJSON also fills it from the legacy JSON fields an adapter
	// names (providers.LegacyAliaser), and syncLegacyCredentials keeps its
	// Object Storage keys equal to the Object Storage fields. It is the only
	// place a request carries a cloud credential (CLD-104). The operator's
	// own keys (ServerOnlyCredentialKeys) are stamped server-side, never
	// taken from a body. The stored record keeps the keys and redacts every
	// value.
	Credentials map[string]string `json:"credentials,omitempty"`

	// Legacy singular fields. When Regions is non-empty Validate()
	// derives these from Regions[0] so writeTfvars()'s single-region
	// apply path keeps working unchanged. When Regions is empty the
	// wizard is from before the per-provider rework migrated, or the
	// payload is hand-crafted (e.g. handler/load_test.go), and these
	// carry the request directly.
	Region           string `json:"region"`
	ControlPlaneSize string `json:"controlPlaneSize"`
	WorkerSize       string `json:"workerSize"`
	WorkerCount      int    `json:"workerCount"`

	// StorageClass — the operator-chosen default StorageClass for this
	// Sovereign's stateful workloads (founder point #1: "storage class is
	// an INPUT that needs to be chosen by the user and like all the other
	// choosable options it needs to have its defaults as well"). Issue
	// #4057.
	//
	// This is an OPTIONAL input with a per-provider default. When empty
	// (the common case — the wizard pre-selects the per-provider CSI class
	// and most operators accept it, and every legacy/automation payload
	// omits the field) DeriveStorageClass() fills in the durable
	// cloud-block-storage CSI class for the chosen provider: Hetzner →
	// `hcloud-volumes` (bp-hcloud-csi slot 55a), Huawei → `evs-ssd`
	// (bp-huawei-evs-csi slot 55b). The k3s ephemeral local-path
	// provisioner is FORBIDDEN (`--disable=local-storage` + the K23 Kyverno
	// ENFORCE deny, #3971/#892) so the default is ALWAYS a durable cloud
	// class — there is no ephemeral fallback. An operator MAY override with
	// any class their CSI installs (e.g. a second hcloud `hcloud-volumes-
	// xfs` profile) but the value is passed verbatim to the cloud — a class
	// the cluster does not install leaves PVCs Pending, the same as any
	// other unknown class name.
	//
	// Threading mirrors the BcpTopology / EnableSharedPostgres seam
	// exactly: this Request field → writeTfvars `default_storage_class`
	// tofu var (the per-provider literal when empty) → the cloud-init Flux
	// Kustomization postBuild.substitute `SOVEREIGN_CNPG_STORAGE_CLASS`
	// (which previously HARDCODED the per-provider literal) → the
	// host-shared CNPG Cluster CRs in slots 10/19/52 + the bp-cnpg /
	// bp-mgmt-vcluster default class. The matching string variable
	// `default_storage_class` is declared in
	// infra/providers/{hetzner,huawei}/variables.tf.
	StorageClass string `json:"storageClass,omitempty"`

	HAEnabled bool `json:"haEnabled"`

	// BcpTopology — Business-Continuity-Planning topology the operator
	// chose at provision time. One of:
	//
	//   - "single-region"     — one region, no cross-region DR shape. The
	//                           Sovereign renders single-Cluster CNPG on
	//                           every CNPG-backed tenant Application.
	//   - "active-hotstandby" — primary + replica region pair. The
	//                           Sovereign renders the bp-cnpg-pair shape
	//                           on every CNPG-backed tenant Application
	//                           (Pillar 3 zero-tx-loss claim).
	//   - "active-active"     — symmetric multi-region; reserved for
	//                           future work (Refs #2669 G93.4). Today
	//                           validates as "active-hotstandby" at this
	//                           layer; the symmetric routing knob is
	//                           rendered by a follow-up workstream.
	//
	// Threaded into tofu via var.enable_hot_standby + the existing
	// var-derived primary/replica region labels, then into the cloud-init
	// Flux Kustomization postBuild.substitute as SOVEREIGN_ENABLE_HOT_STANDBY
	// which the bp-catalyst-platform chart slot 13 reads via
	// `${SOVEREIGN_ENABLE_HOT_STANDBY:-}`. Pre-G93.1 the cloud-init template
	// HARDCODED this envsubst key to empty regardless of the operator's
	// intent (and the Huawei port lacked it entirely) — every multi-region
	// prov silently landed single-Cluster CNPG, defeating Pillar 3.
	//
	// Default-derivation rule (Validate()):
	//   - Empty + len(Regions) >= 2  → "active-hotstandby"   (target-state)
	//   - Empty + len(Regions) <  2  → "single-region"
	//   - Explicit value preserved verbatim
	//
	// Multi-layer RCA (Refs #2666 G93.1, docs/PRINCIPLES.md Principle 16):
	//   - Trigger: HARDCODED `SOVEREIGN_ENABLE_HOT_STANDBY: ""` in the
	//     hetzner cloud-init template; the Huawei port missed it entirely
	//     so the chart's `${SOVEREIGN_ENABLE_HOT_STANDBY:-}` envsubst
	//     evaluated to literal empty → chart-side fallback `false` always
	//     won. Every fresh multi-region prov landed Pillar 3 broken.
	//   - Defense: BcpTopology is the declarative seam on the wire, with
	//     auto-derivation for the common case. The cloud-init template
	//     reads `${enable_hot_standby}` from the tofu var instead of
	//     hardcoded "".
	//   - Containment: tofu var validation `["true","false"]` makes a
	//     typo at tfvars-write time a `tofu plan` failure (not a silent
	//     wrong default). The provisioner.Request.Validate() rejects
	//     unknown topology strings at /api/v1/deployments POST time so
	//     the operator gets a 400 instead of a wrong-by-default prov.
	BcpTopology string `json:"bcpTopology,omitempty"`

	// AdminCIDRs — the IPv4 ranges the sovereign-admin reaches the new
	// Sovereign's Kubernetes API from (NET-011). Optional: when absent, the
	// entries of CATALYST_DEFAULT_ADMIN_CIDRS are used. writeTfvars writes
	// them to the OpenTofu variable admin_cidrs. A range of prefix length
	// 0, such as 0.0.0.0/0, is refused with HTTP 422 (control_plane_sources.go).
	AdminCIDRs []string `json:"adminCIDRs,omitempty"`

	// MarketplaceEnabled — when true, bp-catalyst-platform 1.3.0+ renders the
	// marketplace + tenant-wildcard HTTPRoutes (issue #710). Threaded into
	// tofu via var.marketplace_enabled, then into the cloud-init Flux
	// Kustomization postBuild.substitute.MARKETPLACE_ENABLED. Default false
	// — operator opts in via wizard's "Enable Marketplace" component
	// checkbox.
	MarketplaceEnabled bool `json:"marketplaceEnabled"`

	// ConsoleIsolationEnabled — #4053 dedicated-console-gateway toggle (Refs
	// #4431 #4212). When true (the canonical default, set in NewRequest /
	// applyDefaults below), the Sovereign provisions the dedicated console ELB
	// + its own public EIP so the console./api./marketplace. front doors ride
	// an isolated cilium-gateway-console whose CEC can never be poisoned by a
	// half-converged app on the shared gateway (#4053). The catalyst-ui /
	// catalyst-api HTTPRoutes parent that dedicated gateway.
	//
	// When false, the dedicated console ELB stack is NOT created — the
	// Sovereign consumes ONE FEWER public EIP (cp + nat + elb_primary = 3
	// instead of 4), the console_load_balancer_ip tofu output resolves EMPTY
	// (the DNS-writer recordTargetIP collapses console/api/marketplace onto
	// elb_primary, sovereign_dns_records.go, test-covered), and the
	// SOVEREIGN_CONSOLE_GATEWAY cloud-init substitute re-parents the console
	// HTTPRoutes onto the SHARED cilium-gateway so the console still resolves
	// (no #4070-shape 404-with-TLS-green). This is the seam that lets a
	// single-region validation prov fit a 3-free-EIP kom4dc pool.
	//
	// Threaded through the EXACT same seam as MarketplaceEnabled: this Request
	// field → tofu var `console_isolation_enabled` (string "true"/"false") →
	// the IaC console-ELB `count` gate + the cloud-init SOVEREIGN_CONSOLE_GATEWAY
	// substitute → bootstrap-kit slot 13 ingress.gateway.parentRef.name.
	//
	// Default TRUE (production posture). A POST that omits this field lands the
	// isolated-console production shape, byte-identical to today. A pointer so
	// "omitted" (nil → defaulted true) is distinguishable from an explicit
	// false; applyConsoleIsolationDefault() resolves it.
	ConsoleIsolationEnabled *bool `json:"consoleIsolationEnabled,omitempty"`

	// EnableSharedPostgres — opt-in switch for the ADR-0010 reusable,
	// shareable backing-services model (#3188). When true, bootstrap-kit
	// slot 16a (16a-bp-postgres-shared.yaml) renders the shared `shared-pg`
	// CNPG engine + the per-consumer Database CRs/roles/reflected Secrets;
	// when false (the safe default) slot 16a installs an EMPTY-but-Ready
	// release that satisfies the unconditional bp-gitea/bp-harbor
	// `dependsOn` edge WITHOUT deploying an unused engine. Single-region /
	// non-sharing provs stay byte-identical to today.
	//
	// Threaded through the EXACT same seam as BcpTopology →
	// SOVEREIGN_ENABLE_HOT_STANDBY: catalyst-api Request → tofu var
	// `enable_shared_pg` (string "true"/"false") → cloud-init Flux
	// Kustomization postBuild.substitute as SOVEREIGN_ENABLE_SHARED_PG →
	// slot 16a reads `${SOVEREIGN_ENABLE_SHARED_PG:=false}`. Before this
	// field NOTHING set the substitute var, so the chart's envsubst
	// fallback `false` always won and the #3188 model was dormant +
	// unreachable even on a fresh prov.
	//
	// Default false (the master gate stays OFF). This is the OPT-IN seam —
	// the default is unchanged. NOTE: to actually run two consumers off the
	// shared engine the operator ALSO flips the per-consumer
	// SOVEREIGN_{GITEA,HARBOR}_PG_OWN_CLUSTER=false overlays (the two-flag
	// contract documented in slot 16a); this field controls ONLY whether
	// slot 16a renders the engine at all.
	EnableSharedPostgres bool `json:"enableSharedPostgres"`

	// QATestEnabled — when true, bp-catalyst-platform's qaFixtures stack
	// (qa-<sov> namespace + qa-wp Application + Continuum CR + CNPGPair
	// + PDM CRs + ScheduledBackup + status-seeder Jobs + tier-bound
	// UserAccess seeder) renders so the qa-loop matrix Test Executor
	// finds every Sovereign-side fixture it asserts on. Default false —
	// customer-facing Sovereigns NEVER auto-enable this. QA Sovereigns
	// (omantel.biz, qa.<anything>) provision with `qaTestEnabled: true`.
	//
	// Threaded into tofu via var.qa_fixtures_enabled +
	// var.qa_test_session_enabled + var.qa_fixtures_namespace +
	// var.qa_organization, then into the bootstrap-kit Flux Kustomization
	// postBuild.substitute as QA_FIXTURES_ENABLED / QA_TEST_SESSION_ENABLED
	// / QA_FIXTURES_NAMESPACE / QA_ORGANIZATION (the four envsubst
	// placeholders the chart reads at clusters/_template/bootstrap-kit/
	// 13-bp-catalyst-platform.yaml lines 496/510/511/519).
	//
	// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the namespace
	// and organization names DERIVE from SovereignFQDN's first label at
	// writeTfvars() time — `qa-<label>` and `<label>-platform`. There is
	// no chart-side default of "qa-omantel" / "omantel-platform" leaking
	// onto a non-omantel QA Sovereign.
	//
	// Fix #73 (qa-loop bounded-cycle iter-16): provision #7 came up
	// zero-touch but qaFixtures defaulted false because the provisioner
	// never threaded the toggle. ~140 matrix TCs were inherently fixture-
	// blocked. This field is the canonical seam.
	QATestEnabled bool `json:"qaTestEnabled"`

	// FireCutoverOnHandover — the North Star toggle that auto-fires the
	// self-sovereign cutover engine the moment handover seals the
	// tofu-phase0-archive, INDEPENDENT of QATestEnabled. Default false
	// (omitted = false) — #4061 keeps the cutover an operator-gated BSS
	// action for customer Sovereigns that set neither flag.
	//
	// The coupling this DECOUPLES: before this field, the only way to
	// auto-fire the cutover on handover was QATestEnabled=true, which ALSO
	// renders the qaFixtures stack + LE-STAGING wildcard certs (browser-
	// unusable console). A PROD-cert prov (QATestEnabled=false) could never
	// auto-cutover. The North Star needs BOTH prod-certs AND auto-cutover, so
	// this is the independent seam: set FireCutoverOnHandover=true with
	// QATestEnabled=false to drive a browser-trusted Sovereign to
	// cutoverComplete with ZERO manual steps.
	//
	// Threading (SOV-131): this Request field → the key HANDOVER_AUTO
	// (string "true"/"false") of the variables document that writeTfvars
	// passes as the module input `variables` → the cloud-init control-plane
	// template's FIRE_CUTOVER_ON_HANDOVER substitute → bootstrap-kit slot-13
	// catalyst-api env CATALYST_FIRE_CUTOVER_ON_HANDOVER. The final env value
	// is the OR of qa_fixtures_enabled and this field (the qa path is
	// preserved). infra/providers/{hetzner,huawei}/variables.tf refuse a
	// HANDOVER_AUTO other than "true" or "false" at `tofu plan`. The chart's
	// api-deployment.yaml already ORs `.Values.catalystApi.fireCutoverOnHandover`
	// with `.Values.qaFixtures.enabled` (#4648) — this closes the wire→tfvars
	// →cloud-init half that #4648 left coupled to qaTestEnabled.
	//
	// ZERO-TOUCH DEFAULT (founder 2026-11-06): nil — the field omitted —
	// means TRUE. Every Sovereign cuts over on handover with no operator
	// click; a prov must send `"fireCutoverOnHandover": false` explicitly to
	// keep the cutover an operator-gated action.
	FireCutoverOnHandover *bool `json:"fireCutoverOnHandover,omitempty"`

	// MailRelay — set on a cloud where a mail server inside the Sovereign
	// cannot send: its sign-in codes then go through this relay (MAIL_MODE
	// relay of the variables document, SOV-323). Nil keeps MAIL_MODE local.
	MailRelay *MailRelay `json:"mailRelay,omitempty"`

	// QAFixturesNamespace — explicit override for the qa-fixtures
	// namespace name. Empty (default) → derived from SovereignFQDN's
	// first label as "qa-<label>" at writeTfvars() time. Operator may
	// override when a Sovereign hosts multiple isolated QA tenants
	// (extremely rare; reserved for future use). Ignored when
	// QATestEnabled=false.
	QAFixturesNamespace string `json:"qaFixturesNamespace,omitempty"`

	// QAOrganization — explicit override for the qa-fixtures Organization
	// name (Organization.metadata.name in the qa-fixtures stack). Empty
	// (default) → derived from SovereignFQDN's first label as
	// "<label>-platform" at writeTfvars() time. Ignored when
	// QATestEnabled=false.
	QAOrganization string `json:"qaOrganization,omitempty"`

	// ClusterMeshName + ClusterMeshID — Cilium ClusterMesh per-Sovereign
	// peer anchors (#1101 EPIC-6 multi-region DR). Both empty/zero =
	// single-cluster Sovereign (not in a mesh). When set, must match the
	// allocation in docs/CLUSTERMESH-CLUSTER-IDS.md (every PR that adds
	// a new peer claims a row in that registry). Threaded through:
	// catalyst-api Request → tofu vars cluster_mesh_name/cluster_mesh_id
	// → cloudinit postBuild.substitute (CLUSTER_MESH_NAME / CLUSTER_MESH_ID)
	// → bootstrap-kit slot 01-cilium.yaml's spec.values.cilium.cluster.{name,id}.
	// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), there is NO
	// chart-side default — operator request OR per-Sovereign overlay must
	// supply the values when ClusterMesh is enabled.
	ClusterMeshName string `json:"clusterMeshName,omitempty"`
	ClusterMeshID   int    `json:"clusterMeshId,omitempty"`

	// Per-region sizing payload — canonical from the per-provider rework
	// onwards. The wizard always emits this. Multi-region tofu wiring is
	// structural-correct (variables.tf and the cloud-init templates
	// accept the per-region SKU values), but only Regions[0] is end-to-end
	// exercised today against a real Hetzner project: writeTfvars()
	// renders the singular fields below, mirrored from Regions[0]. The
	// for_each iteration that activates the rest lives in the OpenTofu
	// module — this Go struct's role is to carry the data, intact, for
	// that iteration to pick up.
	Regions []RegionSpec `json:"regions,omitempty"`

	SSHPublicKey string `json:"sshPublicKey"`

	// Dynadot DNS credentials are passed through to the OpenTofu module as
	// variables when SovereignDomainMode is "pool" (the module only writes
	// DNS for managed pool domains; BYO Sovereigns require the customer to
	// point their own CNAME at the LB IP shown in the success screen).
	DynadotAPIKey    string `json:"-"`
	DynadotAPISecret string `json:"-"`

	// GHCRPullToken is a long-lived GHCR pull token (GitHub PAT or fine-
	// grained token with `packages:read` on `openova-io`) handed to the
	// new Sovereign at cloud-init time so it can pull the private bp-*
	// OCI artifacts from `ghcr.io/openova-io/`. Without this, every
	// HelmRepository CR in clusters/<sovereign-fqdn>/bootstrap-kit/
	// (each carrying `secretRef: name: ghcr-pull`) errors with
	// `failed to get authentication secret 'flux-system/ghcr-pull':
	// secrets "ghcr-pull" not found` on a fresh Sovereign — Phase 1
	// stalls at bp-cilium and the bootstrap kit never lands.
	//
	// Source of truth: the catalyst-api Pod mounts this from the
	// `catalyst-ghcr-pull-token` Kubernetes Secret in the `catalyst`
	// namespace as the env var CATALYST_GHCR_PULL_TOKEN. New() reads
	// the env var at startup; Provision() stamps it onto the Request
	// before writing tofu.auto.tfvars.json so OpenTofu can render the
	// cloud-init template with the token interpolated into the
	// flux-system/ghcr-pull Secret manifest.
	//
	// json:"-" is load-bearing: the field MUST NOT serialize to disk.
	// Persisted deployment records (internal/store) are redacted of
	// every credential, but redaction only fires on fields the store's
	// RedactedRequest projection knows about — keeping this off the
	// wire entirely is the simpler invariant. The value is only ever
	// in memory between New()'s env read and the tofu.auto.tfvars.json
	// write, both of which sit on the catalyst-api Pod's local FS at
	// 0o600.
	GHCRPullToken string `json:"-"`

	// HarborRobotToken — central Harbor proxy-cache robot account secret
	// (issue #557). Stamped server-side from Provisioner.HarborRobotToken
	// (env CATALYST_HARBOR_ROBOT_TOKEN). Interpolated by
	// cloudinit-control-plane.tftpl into /etc/rancher/k3s/registries.yaml
	// so containerd authenticates against harbor.openova.io's proxy
	// projects (proxy-dockerhub, proxy-gcr, proxy-quay, proxy-k8s,
	// proxy-ghcr) on every image pull. Without this, containerd falls
	// through to the upstream registry on a fresh Hetzner IP — Docker Hub
	// returns rate-limit HTML and pods stick at Init:0/6 (caught live
	// during otech24). json:"-" — never accepted from the wizard payload.
	HarborRobotToken string `json:"-"`

	// PowerDNSAPIKey — contabo PowerDNS API key (PR #681 followup).
	// The Sovereign-side bp-cert-manager-powerdns-webhook calls
	// pdns.openova.io to write DNS-01 challenge TXT records into
	// contabo's authoritative omani.works zone. The webhook reads its
	// API key from cert-manager/powerdns-api-credentials Secret on the
	// Sovereign — that Secret MUST be created by cloud-init at boot
	// (mirroring the harbor-robot-token pattern from PR #680) because
	// Reflector cannot bridge across clusters. Caught live on otech47.
	// Stamped server-side from Provisioner.PowerDNSAPIKey (env
	// CATALYST_POWERDNS_API_KEY). json:"-" — never accepted from
	// wizard payload.
	PowerDNSAPIKey string `json:"-"`

	// PDMBasicAuthUser / PDMBasicAuthPass — credentials for the public
	// PDM ingress at pool.openova.io (issue #879 Bug 2). cloudinit-
	// control-plane.tftpl writes them into the Sovereign's `flux-system/
	// pdm-basicauth` Secret so the catalyst-api Pod (mounted via
	// Reflector mirror into catalyst-system) can `Authorization: Basic …`
	// against the Traefik basicAuth Middleware in front of PDM.
	// Stamped server-side from Provisioner.PDMBasicAuthUser /
	// PDMBasicAuthPass (envs CATALYST_PDM_BASIC_AUTH_USER /
	// CATALYST_PDM_BASIC_AUTH_PASS). json:"-" — never accepted from
	// wizard payload. Empty falls through to a Secret with empty values;
	// the Sovereign's catalyst-api skips SetBasicAuth and degrades to
	// PDM 401 (clear log line) instead of crashlooping.
	PDMBasicAuthUser string `json:"-"`
	PDMBasicAuthPass string `json:"-"`

	// DeploymentID — catalyst-api's per-deployment identifier (16-char
	// hex). Stamped onto the Request by the handler before tfvars are
	// emitted so the OpenTofu cloud-init template can render the URL
	// the new Sovereign's control plane PUTs its kubeconfig to:
	//
	//   PUT https://console.openova.io/sovereign/api/v1/deployments/{id}/kubeconfig
	//
	// json:"-" so the wizard's create-payload never carries this from
	// the browser; the handler owns it. Persisting it on disk happens
	// via the store.Record's own ID field — we don't duplicate the
	// value into the redacted request.
	DeploymentID string `json:"-"`

	// KubeconfigBearerToken — 32-byte cryptographic-random token
	// generated at CreateDeployment time, stamped here by the handler
	// before tfvars are written. The plaintext value flows ONLY into
	// the OpenTofu workdir (encrypted PVC, deleted on `tofu destroy`)
	// and into the new Sovereign's cloud-init runcmd. The catalyst-api
	// side persists ONLY the SHA-256 hash on the deployment record;
	// the plaintext NEVER lands in the JSON store, never gets logged.
	//
	// On the new Sovereign, cloud-init renders this as a Bearer header
	// on a single PUT to /api/v1/deployments/{id}/kubeconfig. The
	// handler verifies SHA-256 of the received bearer matches the
	// persisted hash via constant-time compare, then writes the
	// kubeconfig file to /var/lib/catalyst/kubeconfigs/<id>.yaml.
	//
	// json:"-" so even if a future logging line marshals the Request
	// wholesale, the token does not leak.
	KubeconfigBearerToken string `json:"-"`

	// HandoverJWTPublicKey — RFC 7517 JWK JSON of the Catalyst-Zero
	// RS256 handover-JWT public key. Stamped by the handler from
	// h.handoverSigner.PublicJWK() before tfvars are written. Cloud-init
	// writes the JWK to /var/lib/catalyst/handover-jwt-public.jwk on the
	// new Sovereign control-plane (issue #605, Phase-8b) so Agent-C can
	// verify the one-time handover JWT without a cross-cluster RPC back
	// to Catalyst-Zero. Empty when CATALYST_HANDOVER_KEY_PATH is unset
	// (variables.tf default ""). json:"-" to keep it out of API request
	// JSON — it's stamped server-side, never accepted from the client.
	HandoverJWTPublicKey string `json:"-"`

	// ── Hetzner Object Storage (Phase 0b — issue #371) ──────────────────
	//
	// Per-Sovereign S3 backing for Harbor (#383) and Velero (#384). The
	// wizard's StepCredentials Object-Storage section captures these from
	// the operator (one-time-issued in the Hetzner Console — there is no
	// Cloud API to mint them; see infra/hetzner/variables.tf §Object
	// Storage for the constraint analysis).
	//
	// Region: fsn1 / nbg1 / hel1 (Object Storage availability is
	// European-only as of 2026-04). Independent of compute region;
	// ash/hil compute Sovereigns pick a European Object Storage region.
	//
	// AccessKey + SecretKey: standard AWS-S3-style credentials. The
	// catalyst-api validates them via S3 ListBuckets BEFORE
	// CreateDeployment returns 201 so the operator sees a typo'd key at
	// the wizard step, not 5 minutes into `tofu apply`.
	//
	// BucketName: deterministic per-Sovereign — the catalyst-api derives
	// it from the FQDN slug. We compute and stamp this in
	// CreateDeployment (writeTfvars never sees an empty bucket name);
	// the wizard does not surface it as a free-form input. Hetzner bucket
	// names share a global namespace across all Hetzner Object Storage
	// tenants, so a deterministic per-FQDN slug minimises collision risk.
	//
	// All four fields carry `json:"objectStorage*"` because the wizard's
	// browser ships them in the deployment-create POST body. The plaintext
	// lives in the per-deployment OpenTofu workdir (encrypted PVC, mode
	// 0600) until `tofu destroy` removes the workdir; the durable copy
	// is the K8s Secret cloud-init writes into the new Sovereign's
	// flux-system namespace. The catalyst-api's on-disk deployment
	// record is redacted via store.Redact.
	ObjectStorageRegion    string `json:"objectStorageRegion"`
	ObjectStorageAccessKey string `json:"objectStorageAccessKey"`
	ObjectStorageSecretKey string `json:"objectStorageSecretKey"`
	ObjectStorageBucket    string `json:"objectStorageBucket"`

	// GitopsBranch is the Git ref the new Sovereign follows. Empty means the
	// value of CATALYST_GITOPS_BRANCH, or "main" when that is unset. Any
	// other value must match gitopsBranchPattern (see resolveGitopsBranch).
	GitopsBranch string `json:"gitopsBranch,omitempty"`
}

// gitopsBranchPattern lists the Git refs a deployment request may name:
// "main", or a release-candidate ref rc/<UTC yyyymmddThhmmssZ>-<first 7 hex
// characters of the commit>. No other branch name is accepted.
var gitopsBranchPattern = regexp.MustCompile(`^(main|rc/[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7})$`)

// resolveGitopsBranch returns the Git ref the new Sovereign follows. An empty
// field gives the value of CATALYST_GITOPS_BRANCH, or "main" when that is
// unset, as before the field existed. A value that matches
// gitopsBranchPattern is returned as it is. Any other value is an error.
func resolveGitopsBranch(req Request) (string, error) {
	if req.GitopsBranch == "" {
		return env("CATALYST_GITOPS_BRANCH", "main"), nil
	}
	if !gitopsBranchPattern.MatchString(req.GitopsBranch) {
		return "", errors.New("invalid gitopsBranch")
	}
	return req.GitopsBranch, nil
}

// Validate ensures the wizard payload is complete enough for OpenTofu to run.
//
// Pointer receiver: when Regions is non-empty, Validate mirrors Regions[0]
// into the legacy singular fields so writeTfvars() can keep using them
// without conditional logic. Callers (handler.CreateDeployment) operate
// on the *Request anyway because the same instance is stored on
// Deployment.Request, so the mutation is intentional and persistent.
func (r *Request) Validate() error {
	if _, err := resolveGitopsBranch(*r); err != nil {
		return err
	}
	if tz := strings.TrimSpace(r.SovereignTimeZone); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
			return fmt.Errorf("sovereignTimeZone %q is not a time zone of the IANA database", tz)
		}
	}
	if len(r.Regions) > 0 {
		// Each region must carry a provider + cloudRegion. The sizes are
		// checked after normalize, because the adapter may fill an empty
		// size from the profile's floors.
		for i, rs := range r.Regions {
			if strings.TrimSpace(rs.Provider) == "" {
				return fmt.Errorf("region[%d] provider is required", i)
			}
			if strings.TrimSpace(rs.CloudRegion) == "" {
				return fmt.Errorf("region[%d] cloudRegion is required", i)
			}
			if rs.WorkerCount < 0 {
				return fmt.Errorf("region[%d] workerCount must be non-negative", i)
			}
		}

		// Mirror Regions[0] into the legacy singular fields for the
		// single-region apply path inside writeTfvars().
		r.Region = r.Regions[0].CloudRegion
		r.ControlPlaneSize = r.Regions[0].ControlPlaneSize
		r.WorkerSize = r.Regions[0].WorkerSize
		r.WorkerCount = r.Regions[0].WorkerCount
		// #4057 — mirror the primary region's StorageClass into the
		// umbrella singular field ONLY when the umbrella value is empty, so
		// an operator who set the class at the top level (the common wizard
		// shape) keeps it, while a per-region-only payload still surfaces a
		// class to writeTfvars. Empty here flows through DeriveStorageClass
		// to the default class of the cloud profile.
		if strings.TrimSpace(r.StorageClass) == "" {
			r.StorageClass = r.Regions[0].StorageClass
		}
	}

	// The adapter of the provider validates and normalises the request
	// (CLD-103): the required credentials, the region capabilities and every
	// provider rule are applied by its Normalize, read from the effective
	// cloud profile.
	if err := r.normalize(); err != nil {
		return err
	}
	// A control-plane SKU per region, and a worker SKU when WorkerCount > 0:
	// what the adapter's Normalize left in place.
	for i, rs := range r.Regions {
		if strings.TrimSpace(rs.ControlPlaneSize) == "" {
			return fmt.Errorf("region[%d] controlPlaneSize is required", i)
		}
		if rs.WorkerCount > 0 && strings.TrimSpace(rs.WorkerSize) == "" {
			return fmt.Errorf("region[%d] workerSize is required when workerCount > 0", i)
		}
	}

	if strings.TrimSpace(r.Region) == "" {
		return errors.New("region is required (runtime parameter, never hardcoded)")
	}
	if strings.TrimSpace(r.SovereignFQDN) == "" {
		return errors.New("sovereign FQDN is required")
	}
	if err := r.MailRelay.validate(); err != nil {
		return err
	}

	// BCP topology (Refs #2666 G93.1) — validate explicit value if
	// the operator supplied one; auto-derive when empty so every
	// multi-region payload lands on the target-state Pillar 3 shape
	// without a per-overlay write. Place this check AFTER the Regions
	// mirror above so deriveBcpTopology() sees the canonical
	// len(Regions) count. Normalisation is whitespace-trim + lowercase
	// so the operator can send "Active-HotStandby" / " single-region "
	// and still hit the enum.
	if explicit := strings.ToLower(strings.TrimSpace(r.BcpTopology)); explicit != "" {
		if _, ok := validBcpTopologies[explicit]; !ok {
			return fmt.Errorf(
				"bcpTopology %q is not one of: single-region, active-hotstandby, active-active",
				r.BcpTopology,
			)
		}
		r.BcpTopology = explicit
	} else {
		// NOTE (#4706): the multi-region POST floor ("no implicit
		// single-region provs") lives in the HANDLER admission path
		// (CreateDeployment, deployments.go), NOT here — Validate() is
		// ALSO the on-restart legacy-record migration path (the store
		// rehydrates pre-floor single-region records through it, see
		// TestLegacyRecord_NoParentDomainsKey_LoadsCleanly), and a
		// policy floor here would brick existing deployments on the
		// next catalyst-api roll. Rehydrated legacy records keep the
		// historical auto-derivation.
		r.BcpTopology = deriveBcpTopology(*r)
	}
	// Mirror invariant: an EXPLICIT single-region declaration with >=2
	// regions is contradictory — reject rather than guess which half the
	// operator meant.
	if r.BcpTopology == BcpTopologySingleRegion && len(r.Regions) >= 2 {
		return fmt.Errorf(
			"bcpTopology=%q contradicts len(regions)=%d — drop the extra regions or choose a multi-region topology",
			BcpTopologySingleRegion, len(r.Regions),
		)
	}
	// Cross-field invariant: active-hotstandby and active-active both
	// require >=2 regions on the wire — a single-region prov cannot
	// satisfy Pillar 3 zero-tx-loss. Fail-fast at /api/v1/deployments
	// POST time so the operator gets a 400 instead of an apply that
	// boots single-region with the chart still rendering single-cluster
	// CNPG (which would be a wrong-by-default silent regression).
	if (r.BcpTopology == BcpTopologyActiveHotStandby || r.BcpTopology == BcpTopologyActiveActive) && len(r.Regions) < 2 {
		return fmt.Errorf(
			"bcpTopology=%q requires len(regions)>=2 (got %d) — Pillar 3 zero-transactions-lost cannot be honoured single-region",
			r.BcpTopology, len(r.Regions),
		)
	}

	// ParentDomains migration (issue #826 — backward compat with the
	// legacy single-FQDN payload). When the slice is empty, synthesise
	// a single primary entry from SovereignPoolDomain (managed-pool
	// mode) or SovereignFQDN (BYO mode) so the downstream provisioner
	// always operates on the array shape regardless of how the request
	// arrived. This is the migration step for in-flight wizard
	// deployments and for on-disk records persisted under the old
	// shape — the next Save() round-trip emits the array form. New
	// wizard payloads (none today; #826 scope correction holds the
	// wizard at single-FQDN) would populate the slice directly.
	//
	// When the slice IS supplied, validate every entry: name, role,
	// and "exactly one primary" invariant. The catalyst-api's
	// add-domain handler (#829) calls Validate() on the same request
	// shape after appending — re-running the check here is idempotent.
	if len(r.ParentDomains) == 0 {
		// Pick the synthesis source: pool-domain mode uses
		// SovereignPoolDomain (the operator-owned registered domain;
		// SovereignFQDN is the per-Sovereign sub-zone like
		// "omantel.omani.works"), BYO-or-empty falls back to
		// SovereignFQDN. The role is always "primary" — Day-1 has
		// exactly one entry.
		name := strings.TrimSpace(r.SovereignPoolDomain)
		if name == "" {
			name = strings.TrimSpace(r.SovereignFQDN)
		}
		name = strings.ToLower(name)
		// AddedAt is left zero on the synthesis path so the migration
		// is observable from the on-disk record (a non-zero
		// AddedAt indicates the entry was created via the explicit
		// admin add-domain flow). Callers stamp a real timestamp
		// when they construct a ParentDomain themselves — see
		// internal/handler's add-domain handler in #829.
		r.ParentDomains = []ParentDomain{{
			Name:          name,
			Role:          ParentDomainRolePrimary,
			RegistrarKind: defaultRegistrarKindFromEnv(),
		}}
	}
	if err := validateParentDomains(&r.ParentDomains); err != nil {
		return err
	}

	if strings.TrimSpace(r.OrgName) == "" {
		return errors.New("organisation name is required")
	}
	if strings.TrimSpace(r.OrgEmail) == "" {
		return errors.New("organisation email is required (becomes initial sovereign-admin)")
	}
	if strings.TrimSpace(r.SSHPublicKey) == "" {
		return errors.New("SSH public key is required (sovereign-admin break-glass + cluster bootstrap)")
	}

	// GHCR pull token: required for managed-pool deployments. Phase 1
	// (Flux reconciling clusters/<sovereign-fqdn>/bootstrap-kit) pulls
	// the bp-* OCI artifacts from `ghcr.io/openova-io/` which is a
	// PRIVATE registry path; without a token the source-controller
	// errors `secrets "ghcr-pull" not found` and bp-cilium never
	// installs.
	//
	// Why scoped to domain_mode=pool: managed-pool Sovereigns are the
	// catalyst-platform's reference deployment shape (omani.works,
	// follow-on franchise pool domains). BYO Sovereigns go through
	// the same Phase 1, but the BYO registrar proxy + Flow B path
	// (issue #169) is the orthogonal track for that work; the
	// requirement here is to land the durable-secret fix without
	// blocking BYO catalyst-api Pod startup when the operator has
	// not yet created `catalyst-ghcr-pull-token`. BYO deployments
	// will still fail Phase 1 if the token is missing — the failure
	// surfaces from the new cluster's Flux logs instead of from this
	// validator. The BYO+token requirement gets enforced once the
	// Flow B work also lands a token-on-Request stamp. See section F
	// in the fix-cloudinit-ghcr-pull-secret-durable rollout notes.
	//
	// We surface the pool-mode error from Validate() rather than from
	// runProvisioning() so a misconfigured catalyst-api Pod fails
	// fast at /api/v1/deployments POST time instead of after 5 min
	// of `tofu apply`.
	if r.SovereignDomainMode == "pool" && strings.TrimSpace(r.GHCRPullToken) == "" {
		return errors.New("GHCR pull token is required for managed-pool deployments (CATALYST_GHCR_PULL_TOKEN missing on catalyst-api — see docs/SECRET-ROTATION.md)")
	}
	// Harbor robot token (issue #557) — REQUIRED, no exceptions. The
	// architecture mandate is that every Sovereign image pull goes
	// through harbor.openova.io's proxy projects (proxy-dockerhub,
	// proxy-gcr, proxy-quay, proxy-k8s, proxy-ghcr). An empty token
	// means containerd will fail authentication against Harbor and
	// fall through to upstream registries — Docker Hub then
	// rate-limits a fresh Hetzner IP and pods stick at Init:0/6
	// forever (caught live during otech24). Fail fast at /api/v1/
	// deployments POST so a misconfigured catalyst-api Pod surfaces
	// the missing CATALYST_HARBOR_ROBOT_TOKEN env immediately
	// instead of after 5 min of tofu apply.
	if strings.TrimSpace(r.HarborRobotToken) == "" {
		return errors.New("Harbor robot token is required (CATALYST_HARBOR_ROBOT_TOKEN missing on catalyst-api — every Sovereign image pull MUST go through harbor.openova.io; falling through to docker.io is not allowed)")
	}

	// Hetzner Object Storage (issue #371) — Phase 0b. All four fields are
	// required for any Hetzner-backed Sovereign: the bucket exists at
	// `tofu apply` time (minio_s3_bucket in main.tf) so Harbor (#383) and
	// Velero (#384) find their backing store ready when Phase 1
	// reconciles their HelmReleases. The bucket name is computed by the
	// handler from the Sovereign FQDN slug — wizards never surface it.
	//
	// Validation here is fail-fast at /api/v1/deployments POST time so a
	// missing/typo'd credential pair surfaces as 400 with a clear pointer
	// rather than 5 minutes into `tofu apply`. The catalyst-api's
	// /api/v1/credentials/object-storage/validate endpoint is the wizard's
	// upstream gate — by the time the deployment payload arrives here,
	// the keys SHOULD already have been validated against ListBuckets.
	if strings.TrimSpace(r.ObjectStorageRegion) == "" {
		return errors.New("object storage region is required (a region of the cloud's Object Storage, as its cloud profile lists it)")
	}
	if strings.TrimSpace(r.ObjectStorageAccessKey) == "" {
		return errors.New("object storage access key is required (issued by the cloud's Object Storage)")
	}
	if strings.TrimSpace(r.ObjectStorageSecretKey) == "" {
		return errors.New("object storage secret key is required (paired with the access key)")
	}
	if strings.TrimSpace(r.ObjectStorageBucket) == "" {
		return errors.New("object storage bucket name is required (catalyst-api derives this deterministically from the Sovereign FQDN; an empty value here is a wizard or handler bug)")
	}
	// Bucket name validity mirrors the S3 RFC: 3-63 chars, lowercase
	// alphanumeric + hyphens, must start and end alphanumeric. The
	// wizard derives it from the FQDN slug so the rule should always
	// pass — but a hand-crafted POST (e.g. load test) could violate it,
	// so we re-enforce here.
	if !s3BucketNamePattern.MatchString(r.ObjectStorageBucket) {
		return fmt.Errorf("object storage bucket name %q does not match S3 naming rules (3-63 chars, lowercase alphanumeric + hyphens, start/end alphanumeric)", r.ObjectStorageBucket)
	}
	return nil
}

// regionsProvider is the lower-case provider every entry of regions names, or
// "" when there are no regions, an entry names none, or two entries differ.
func regionsProvider(regions []RegionSpec) string {
	name := ""
	for _, rs := range regions {
		p := strings.ToLower(strings.TrimSpace(rs.Provider))
		if p == "" || (name != "" && p != name) {
			return ""
		}
		name = p
	}
	return name
}

// ResolveProvider returns the lower-case provider of r: the top-level
// Provider, else the one provider every regions[] entry names (the wizard
// names it per region, StepReview.tsx), else "". The create handler calls it
// before it stamps any server-side value, so both request shapes take one
// path; normalize calls it too.
func ResolveProvider(r *Request) string {
	if name := strings.ToLower(strings.TrimSpace(r.Provider)); name != "" {
		return name
	}
	return regionsProvider(r.Regions)
}

// normalize resolves the adapter of r.Provider and runs its Normalize on the
// spec of r, with the effective cloud profile (CLD-103). r.Provider becomes the
// canonical lower-case name. The sizes and worker counts that Normalize sets
// are copied back into r. Region codes, cloud regions and zones are not:
// writeTfvars takes them from the adapter's Normalize of its own (moduleSpec),
// and the rest of the engine keeps the request's.
func (r *Request) normalize() error {
	name := ResolveProvider(r)
	if name == "" {
		return &providers.FieldError{Field: "provider", Message: "required; known: " + strings.Join(providers.Names(), ", ")}
	}
	adapter, err := providers.Get(name)
	if err != nil {
		return fmt.Errorf("unsupported provider %q; known: %s", name, strings.Join(providers.Names(), ", "))
	}
	r.Provider = name
	profile, err := EffectiveProfile(name, r.Installation)
	if err != nil {
		return err
	}
	syncLegacyCredentials(r)
	spec := r.providerSpec()
	if err := adapter.Normalize(&spec, profile); err != nil {
		return err
	}
	if len(r.Regions) == 0 {
		r.ControlPlaneSize = spec.Regions[0].ControlPlaneSize
		r.WorkerSize = spec.Regions[0].WorkerSize
		r.WorkerCount = spec.Regions[0].WorkerCount
		return nil
	}
	for i := range r.Regions {
		r.Regions[i].ControlPlaneSize = spec.Regions[i].ControlPlaneSize
		r.Regions[i].WorkerSize = spec.Regions[i].WorkerSize
		r.Regions[i].WorkerCount = spec.Regions[i].WorkerCount
	}
	r.ControlPlaneSize = r.Regions[0].ControlPlaneSize
	r.WorkerSize = r.Regions[0].WorkerSize
	r.WorkerCount = r.Regions[0].WorkerCount
	return nil
}

// providerSpec is what the adapter of r sees of the request (section 4.1 of
// the port): the credentials map, the Object Storage fields and one RegionSpec
// per entry of Regions, or one built from the legacy singular fields when
// Regions is empty. Code is the entry's code, or its cloudRegion when that is
// empty; Role comes from the position.
func (r *Request) providerSpec() providers.ProvisionSpec {
	creds := make(map[string]string, len(r.Credentials))
	for k, v := range r.Credentials {
		creds[k] = v
	}
	regions := r.Regions
	if len(regions) == 0 {
		regions = []RegionSpec{{CloudRegion: r.Region, ControlPlaneSize: r.ControlPlaneSize, WorkerSize: r.WorkerSize, WorkerCount: r.WorkerCount}}
	}
	spec := providers.ProvisionSpec{
		DeploymentID:  r.DeploymentID,
		SovereignFQDN: r.SovereignFQDN,
		Installation:  r.Installation,
		Creds:         providers.ProviderCreds{Raw: creds},
		StorageClass:  r.StorageClass,
		ObjectStorage: providers.ObjectStorageSpec{
			Region:    r.ObjectStorageRegion,
			Bucket:    r.ObjectStorageBucket,
			AccessKey: r.ObjectStorageAccessKey,
			SecretKey: r.ObjectStorageSecretKey,
		},
	}
	for i, rs := range regions {
		role := "secondary"
		if i == 0 {
			role = "primary"
		}
		spec.Regions = append(spec.Regions, providers.RegionSpec{
			Code:             regionCode(rs),
			CloudRegion:      rs.CloudRegion,
			Zone:             rs.Zone,
			Role:             role,
			ControlPlaneSize: rs.ControlPlaneSize,
			WorkerSize:       rs.WorkerSize,
			WorkerCount:      rs.WorkerCount,
		})
	}
	return spec
}

// legacyCredentialFields are the Object Storage fields of Request, by the
// credential key the credentials map holds each under. They stay as the
// source of ProvisionSpec.ObjectStorage and of the Object Storage variables.
func (r *Request) legacyCredentialFields() []struct {
	key   string
	field *string
} {
	return []struct {
		key   string
		field *string
	}{
		{"object_storage_access_key", &r.ObjectStorageAccessKey},
		{"object_storage_secret_key", &r.ObjectStorageSecretKey},
		{"object_storage_region", &r.ObjectStorageRegion},
	}
}

// ServerOnlyCredentialKeys are the credential keys of the operator's own cloud
// account (Provisioner.OperatorCredentials): only the server's configuration
// sets them, never a request body.
var ServerOnlyCredentialKeys = []string{"access_key", "secret_key", "project_id", "region"}

// RefuseServerOnlyCredentials returns a *providers.FieldError on
// "credentials.<key>" when the credentials map of r names a server-only key
// (ServerOnlyCredentialKeys). The create handler calls it on the decoded body
// before it stamps anything, so a body never chooses the keys, or the region
// and with it the endpoints, that the operator's credentials are used against.
func RefuseServerOnlyCredentials(r *Request) *providers.FieldError {
	for _, key := range ServerOnlyCredentialKeys {
		if _, named := r.Credentials[key]; named {
			return &providers.FieldError{Field: "credentials." + key, Message: "server-only: set from the operator's configuration, never from a request"}
		}
	}
	return nil
}

// RedactedMarker is the value a stored deployment record holds in place of
// every secret credential (internal/store). A rehydrated request carries it
// in Credentials; Credential reads it as empty.
const RedactedMarker = "<redacted>"

// StoredCredentialKeys returns the credential keys of provider whose value a
// stored deployment record keeps as it is (internal/store): the fields that
// the cloud profile of its pack marks secret: false, except the server-only
// keys, which the server's configuration sets on every request. It returns
// nil, so that the record redacts every value, when the profile cannot be
// read. A pack is read once per ProvidersDir; the caller must not change the
// map.
func StoredCredentialKeys(provider string) map[string]bool {
	name := strings.ToLower(strings.TrimSpace(provider))
	cacheKey := ProvidersDir + "\x00" + name
	if keys, ok := storedCredentialKeys.Load(cacheKey); ok {
		return keys.(map[string]bool)
	}
	profiles, err := cloudprofile.Load(ProvidersDir)
	if err != nil || profiles[name] == nil {
		return nil
	}
	keys := map[string]bool{}
	for _, f := range profiles[name].Spec.Credentials.Fields {
		if !f.Secret && !slices.Contains(ServerOnlyCredentialKeys, f.Key) {
			keys[f.Key] = true
		}
	}
	storedCredentialKeys.Store(cacheKey, keys)
	return keys
}

// storedCredentialKeys caches StoredCredentialKeys by ProvidersDir and
// provider: the store redacts on every persisted event.
var storedCredentialKeys sync.Map

// Credential returns the credential key of r: "" when the key is absent or
// holds RedactedMarker, so a value read back from a stored record is never
// used as a credential.
func (r Request) Credential(key string) string {
	if v := r.Credentials[key]; v != RedactedMarker {
		return v
	}
	return ""
}

// WithCredential returns a copy of r whose credentials map is a copy of r's
// with key set to value, so r is not changed.
func (r Request) WithCredential(key, value string) Request {
	r.Credentials = maps.Clone(r.Credentials)
	r.SetCredential(key, value)
	return r
}

// SetCredential sets the credential key of r to value, creating the map.
func (r *Request) SetCredential(key, value string) {
	if r.Credentials == nil {
		r.Credentials = map[string]string{}
	}
	r.Credentials[key] = value
}

// syncLegacyCredentials makes the credentials map and the Object Storage
// fields agree: a non-empty field is written into the map under its key, and
// an empty field is filled from the map. A field set server-side (the Object
// Storage keys the create handler derives) therefore wins.
func syncLegacyCredentials(r *Request) {
	for _, l := range r.legacyCredentialFields() {
		if v := *l.field; strings.TrimSpace(v) != "" {
			if r.Credentials == nil {
				r.Credentials = map[string]string{}
			}
			r.Credentials[l.key] = v
		} else if v, ok := r.Credentials[l.key]; ok {
			*l.field = v
		}
	}
}

// UnmarshalJSON decodes a request and then, for its provider (ResolveProvider:
// the top-level field, else the one its regions name), copies every
// legacy JSON field that the adapter names (providers.LegacyAliaser) into
// Credentials, unless the map already holds that key. An unknown or empty
// provider is left to Validate.
func (r *Request) UnmarshalJSON(data []byte) error {
	type plain Request
	if err := json.Unmarshal(data, (*plain)(r)); err != nil {
		return err
	}
	aliaser, ok, err := providers.As[providers.LegacyAliaser](ResolveProvider(r))
	if err != nil || !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for legacy, key := range aliaser.LegacyCredentialAliases() {
		raw, present := fields[legacy]
		if !present {
			continue
		}
		var v string
		if json.Unmarshal(raw, &v) != nil || v == "" {
			continue
		}
		if _, set := r.Credentials[key]; set {
			continue
		}
		if r.Credentials == nil {
			r.Credentials = map[string]string{}
		}
		r.Credentials[key] = v
	}
	return nil
}

// Event is a single progress event streamed back to the wizard via SSE.
//
// Component / State are populated for Phase-1 component events emitted by
// the HelmRelease watch loop (internal/helmwatch). For Phase-0 OpenTofu
// events these stay empty so the existing wire format is unchanged — no
// existing field is removed or renamed; only two optional fields are
// added. The Admin shell's "logs filtered by event.component === id"
// path keys off Component; the per-app status pill keys off State.
//
// State semantics (Phase-1 watch only):
//
//   - "pending"    — HelmRelease appeared in the cluster but Ready
//     condition not yet observed, OR Ready=False with a
//     `dependency 'X' is not ready` message (the
//     component is waiting upstream of itself)
//   - "installing" — Ready=Unknown, or Ready=False with reason
//     `Progressing` / message `Reconciliation in progress`
//   - "installed"  — Ready=True
//   - "degraded"   — Ready=True transitioned to Ready=False without
//     InstallFailed/UpgradeFailed (a healthy install
//     that lost readiness post-install)
//   - "failed"     — Ready=False with reason InstallFailed /
//     UpgradeFailed / ChartPullError /
//     ArtifactFailed (the install actually broke,
//     not waiting on deps)
//
// For phase: "component-log" events, Component is set, State is empty,
// Level carries the helm-controller log level, and Message is the raw
// log line.
type Event struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`

	// Component is the normalised component id for Phase-1 events
	// ("bp-cilium" → "cilium"). Empty for Phase-0 OpenTofu events.
	Component string `json:"component,omitempty"`

	// State is one of pending|installing|installed|degraded|failed for
	// phase: "component" events; empty for Phase-0 events and for
	// phase: "component-log" events (which carry the original log
	// level instead).
	State string `json:"state,omitempty"`

	// DependsOn carries the HelmRelease's spec.dependsOn[].name list
	// (with the "bp-" prefix stripped) on every phase:"component"
	// event. Without this, the bridge writes Jobs with DependsOn=[]
	// for every HR observed AFTER the watcher's initial-list sync —
	// i.e. on every fresh provision, where Flux installs the 45 bp-*
	// HRs over ~10 min AFTER the watcher attaches with an empty
	// initial list. PR #1431 and PR #1470 patched the seed + persist
	// paths but did not close this gap; the per-event emit at
	// helmwatch.go:1525 silently dropped spec.dependsOn. Caught on
	// prov t102.omani.works (22af2b1120158239, 2026-05-15). For
	// secondary regions, the entries are region-prefixed
	// ("gitea" → "hel1-2:gitea") by spawnSecondaryRegionWatchers' emit
	// callback so intra-region sibling edges land in the
	// install-<region>:<chart> namespace.
	DependsOn []string `json:"dependsOn,omitempty"`
}

// Result captures the OpenTofu outputs the wizard's success screen needs
// PLUS the Phase-1 component watch terminal state.
//
// ComponentStates and Phase1FinishedAt are populated by the HelmRelease
// watch loop in internal/helmwatch. They are the durable per-component
// outcome the Admin shell renders ("X of Y components installed") long
// after the live SSE stream has closed.
//
// KubeconfigPath holds the absolute path to the new Sovereign's k3s
// kubeconfig file on the catalyst-api PVC (typically
// /var/lib/catalyst/kubeconfigs/<id>.yaml, mode 0600). It is populated
// when the new Sovereign's cloud-init PUTs the kubeconfig back via the
// bearer-token endpoint (issue #183, Option D). The HelmRelease watch
// loop, the wizard's "Download kubeconfig" button, and the operator's
// GET /api/v1/deployments/<id>/kubeconfig all read from this file.
//
// The plaintext kubeconfig contents NEVER land in the JSON record on
// disk — only the file path is persisted. Per
// docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the file is
// chmod 0600 and the directory is owned by the catalyst-api process
// UID on the PVC.
type Result struct {
	SovereignFQDN  string `json:"sovereignFQDN"`
	ControlPlaneIP string `json:"controlPlaneIP"`
	LoadBalancerIP string `json:"loadBalancerIP"`
	// ConsoleLoadBalancerIP — #4053. tofu output console_load_balancer_ip:
	// the dedicated console LB that fronts the isolated cilium-gateway-console.
	// console./api.<fqdn> DNS point here; the wildcard keeps pointing at
	// LoadBalancerIP. Empty when the tofu module pre-dates #4053.
	ConsoleLoadBalancerIP string `json:"consoleLoadBalancerIP,omitempty"`
	ConsoleURL            string `json:"consoleURL"`
	GitOpsRepoURL         string `json:"gitopsRepoURL"`

	// KubeconfigPath — absolute path to the kubeconfig YAML on the
	// catalyst-api PVC. Empty until cloud-init PUTs the bytes back
	// over the bearer-token endpoint. Persisted to the per-deployment
	// store record so a catalyst-api Pod restart can re-read the file
	// from disk and resume the Phase-1 HelmRelease watch.
	KubeconfigPath string `json:"kubeconfigPath,omitempty"`

	// ComponentStates — final state of every bp-* HelmRelease the
	// Phase-1 watch observed, keyed by normalised component id. Set
	// when the watch loop terminates (all-installed, all-installed-or-
	// failed, or timeout).
	ComponentStates map[string]string `json:"componentStates,omitempty"`

	// Phase1FinishedAt — UTC timestamp the watch loop terminated.
	// nil while Phase 1 is in flight or has not started.
	Phase1FinishedAt *time.Time `json:"phase1FinishedAt,omitempty"`

	// Phase1Substate — live sub-status while Phase 1 is in flight
	// (issue #923). Set by helmwatch.Watcher.OnSubstate as the watch
	// progresses through pre-flight reachability + cache-sync. Cleared
	// (set to "") once the watch terminates and Phase1Outcome is
	// stamped. The Sovereign Admin's wizard banner reads this to
	// render a granular status pill while Status itself stays
	// "phase1-watching":
	//
	//   - "watcher-reconnecting" — apiserver was unreachable and the
	//     pre-flight probe is retrying with backoff (typical after a
	//     catalyst-api Pod restart while the LB / kube-vip warms up)
	//   - "watcher-watching"     — apiserver reachable, informer
	//     attached, observing per-component HelmRelease transitions
	//
	// Empty while Phase 1 has not started, has terminated, or the
	// build of catalyst-api predates the substate field — the wizard
	// falls back to rendering the bare Status pill.
	Phase1Substate string `json:"phase1Substate,omitempty"`

	// Phase1Outcome — terminal classification of the Phase-1 watch.
	// One of:
	//
	//   - "ready"                — all observed components installed,
	//                              ≥ MinBootstrapKitHRs were observed
	//   - "failed"                — all observed components terminal
	//                              AND ≥ MinBootstrapKitHRs were
	//                              observed, but at least one failed
	//   - "timeout"               — overall WatchTimeout elapsed with
	//                              partial state (≥1 HR observed)
	//   - "flux-not-reconciling"  — overall WatchTimeout elapsed with
	//                              ZERO HelmReleases ever observed.
	//                              The bootstrap-kit Kustomization on
	//                              the new Sovereign isn't reconciling.
	//                              Operator playbook in
	//                              docs/RUNBOOK-PROVISIONING.md
	//                              §"Phase 1 watch shows 0 HelmReleases".
	//
	// Empty while Phase 1 is in flight or was skipped (no kubeconfig).
	// The Sovereign Admin's wizard banner reads this to render the
	// right operator-actionable diagnostic instead of an opaque
	// "stuck" pill.
	Phase1Outcome string `json:"phase1Outcome,omitempty"`

	// HandoverFiredAt — UTC timestamp the catalyst-api auto-fired the
	// handover JWT mint after the Phase-1 watch terminated with
	// OutcomeReady (issues #764 + #768). Nil until the auto-fire
	// happens; non-nil afterward. Tests + the wizard's provision page
	// gate on (status=="ready" && HandoverURL != "" && HandoverFiredAt
	// != nil) to render the "Open your Sovereign console →" button +
	// the 5-second auto-redirect timer.
	HandoverFiredAt *time.Time `json:"handoverFiredAt,omitempty"`

	// HandoverCompletedAt — UTC time the hand-over of the Sovereign to its
	// owner completed; fireHandover sets it. From then on the wipe route
	// refuses a wipe whose header X-Confirm-Sovereign-FQDN does not carry
	// the Sovereign's FQDN (internal/lifecycle). Nil until then.
	HandoverCompletedAt *time.Time `json:"handoverCompletedAt,omitempty"`

	// MaterializedRegions — list of region codes that actually came up
	// after `tofu apply` returned. Sourced from the per-provider
	// per-region output map (Huawei: `control_plane_ips_per_region`).
	// Populated for every multi-region prov; for single-region provs
	// it is either nil (single-region path doesn't read the map) or a
	// 1-entry slice (when the operator submitted Regions[0] only). The
	// catalyst-api uses len(MaterializedRegions) vs len(Request.Regions)
	// to detect the G117 #2840 partial-region cascade where the HCS
	// scheduler refused the secondary region (typically Common.0021 /
	// quota cap) and the prov silently degraded from active-hotstandby
	// to single-region.
	MaterializedRegions []string `json:"materializedRegions,omitempty"`

	// PreCreated — the objects the cloud adapter created before the plan,
	// each imported into the state at its resource address
	// (providers.PreflightResult.Imports, precreated.go): the record names
	// what the engine put in the cloud outside OpenTofu's own allocation.
	PreCreated []providers.StateImport `json:"preCreated,omitempty"`

	// HandoverURL — fully-qualified handover redirect URL (issues
	// #764 + #768). Shape:
	//
	//   https://console.<sovereignFqdn>/auth/handover?token=<jwt>
	//
	// The token is RS256-signed by catalyst-api's handoverjwt.Signer
	// (claims contract documented in internal/handoverjwt/signer.go);
	// the Sovereign-side /auth/handover handler validates it, mints a
	// session, and 302s to /console/dashboard. The URL is durable on
	// the deployment record so a Pod restart between the mint and the
	// browser-side redirect still surfaces the same URL on the next
	// /deployments/{id} poll. Empty until the auto-fire happens; the
	// JWT inside expires after 5 minutes per
	// handoverjwt.DefaultTTL — re-mint is the operator's manual
	// "Open Sovereign console" path (POST
	// /deployments/{id}/mint-handover-token).
	HandoverURL string `json:"handoverURL,omitempty"`

	// Regions — per-region HelmRelease health census (#3611). For a
	// multi-region deployment this is the ONLY honest read of how each
	// region actually converged: the flat ComponentStates map above
	// collapses every region together, and dep.Status="ready" is driven
	// solely by the PRIMARY region's Phase-1 watch. A degraded secondary
	// (hw145: region-a 60/63 HRs, region-b 48/63) would otherwise hide
	// behind a bare green "ready" — this slice surfaces "region-b 48/63,
	// degraded" instead. Computed at Phase-1 termination (a snapshot of
	// the secondary watchers taken while they are still attached) and
	// recomputed live in Deployment.State() while the watchers remain up,
	// so the console/jobs view tracks convergence and then freezes on the
	// handover picture. Empty for a single-region prov; for a multi-
	// region prov the primary entry is emitted even before any secondary
	// kubeconfig lands. Primary first, secondaries in sorted region-key
	// order. See internal/provisioner/region_health.go for the gate.
	//
	// Since #7902 Status="ready" is NOT driven by the primary region alone
	// any more: the every-region gate (internal/crossregion/gate.go) holds
	// the deployment in phase1-watching until every region that reported in
	// is installed. This census stays the per-region read of it.
	Regions []RegionHealth `json:"regions,omitempty"`

	// SecondaryDegraded — surface-only roll-up of Regions: true when ANY
	// secondary region is significantly short of the primary's Ready-HR
	// count (#3611). This is intentionally NOT a gate — markPhase1Done
	// keeps flipping Status="ready" off the primary watcher alone so a
	// legitimately-slow secondary never hangs the prov (Flux keeps
	// reconciling long past the watch budget and the doctrine forbids
	// wiping such envs). It exists so the operator console can badge a
	// "ready" deployment as "secondary degraded" and so catalyst-api logs
	// the condition at Phase-1 termination. Always false for a single-
	// region prov.
	//
	// #7902: this flag is still not what decides ready (it is a ratio
	// against the primary and tolerates a few releases). What decides is
	// the every-region gate, which wants every release of every region
	// that reported in installed. On a record that gate made ready this
	// flag is false; it is true on a record whose declared region never
	// reported in (standby-region-absent) and on records of older builds.
	SecondaryDegraded bool `json:"secondaryDegraded,omitempty"`

	// SecondaryFluxCRDAbsentRegions — the region keys of SECONDARY control
	// planes whose cloud-init flux-install stage silently did not land: the
	// cluster is HEALTHY (its kubeconfig was PUT back) but the Flux
	// HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) is ABSENT, so a
	// helmwatch informer there would observe zero HelmReleases until
	// stopSecondaries cancels it — an invisible 0/0 with no named diagnostic
	// (#5012, the secondary-region mirror of the primary's #5042
	// OutcomeFluxCRDsAbsent). Populated by spawnSecondaryRegionWatchers'
	// per-region probe. This is a NAMED, greppable, surface-only signal: like
	// SecondaryDegraded it NEVER gates "ready" (a broken/absent secondary must
	// never hang or fail the whole prov — the surface-not-gate discipline), it
	// only lets the operator console + catalyst-api logs name the exact region
	// + root cause (cloud-init's flux-install stage) instead of the prov
	// silently idling. Empty on a healthy prov. The region also surfaces in
	// the #3611 Regions census as a 0/0 degraded secondary because the probe
	// registers a nil watcher slot for it (skipping the doomed real watcher).
	SecondaryFluxCRDAbsentRegions []string `json:"secondaryFluxCRDAbsentRegions,omitempty"`

	// ConsoleDegraded — surface-only flag (#5253, family #4486/#4706): the
	// PRIMARY region fully converged (Phase1Outcome=="ready" — every primary
	// HelmRelease installed) but the #4706 external console-reachability
	// probe did not observe https://console.<fqdn>/ serving within its budget
	// at Phase-1 termination. Like SecondaryDegraded this is intentionally
	// NOT a gate — markPhase1Done keeps Status="ready" and fires the full
	// producer chain (handover, ClusterMesh establish, spine/adoption/policy
	// hooks) off the converged primary, because latching Status="failed" here
	// left the ENTIRE cross-region topology permanently inert (hw276: no
	// mesh → no CNPG-pair flip → hub secrets never sync → keycloak + the SSO
	// charts wedge on region-b). A background re-probe clears the flag the
	// moment the front door answers; the operator console can badge a
	// "ready" deployment as console-degraded off this field meanwhile.
	ConsoleDegraded bool `json:"consoleDegraded,omitempty"`

	// ConsoleDegradedDetail — the human diagnostic behind ConsoleDegraded
	// (the last probe error, e.g. "https://console.<fqdn>/ returned HTTP
	// 404"). Updated by each background re-probe attempt; cleared together
	// with ConsoleDegraded when the console answers. Deliberately NOT
	// stamped on the deployment's top-level Error field — /deployments/{id}
	// renders a non-empty Error as a hard failure (the #4486 NB), and this
	// condition is non-fatal by design.
	ConsoleDegradedDetail string `json:"consoleDegradedDetail,omitempty"`
}

// PartialRegionMaterialisationError is returned by Provision when
// `tofu apply` completed without a Go-side error but the per-provider
// per-region readback shows fewer regions came up than the wizard
// declared (len(Result.MaterializedRegions) < len(req.Regions)).
//
// The typed error carries the partial Result so the catalyst-api can
// stamp Deployment.status = "partial-failure" with a useful breakdown
// — it does NOT auto-`tofu destroy`, which would abandon a partial
// infra the operator may want to retain for forensic / cost / quota
// reasons (per G117 #2840: "auto-rollback is destructive and may
// abandon a partial-prov the operator wants to keep").
//
// The handler-side flow on encountering this error is documented in
// handler/deployments.go::runProvisioning (search for
// PartialRegionMaterialisationError).
type PartialRegionMaterialisationError struct {
	// DeclaredRegions — the region codes the operator submitted via
	// Request.Regions[].CloudRegion, in declaration order.
	DeclaredRegions []string
	// MaterializedRegions — the region codes that have a non-empty
	// primary-CP EIP after `tofu apply`, in declaration order.
	MaterializedRegions []string
	// MissingRegions — DeclaredRegions \ MaterializedRegions, in
	// declaration order. The operator console renders this list.
	MissingRegions []string
	// Result — non-nil; the partial-success Result with whatever
	// outputs DID land (so the catalyst-api still has the LB IP, the
	// primary CP IP, the materialized region list, etc.). The caller
	// MUST NOT proceed to commitPDMWithRetry / runPhase1Watch on this
	// Result because the topology promise (active-hotstandby) is
	// broken — but the data is useful for the operator-console
	// PartialFailure render.
	Result *Result
}

func (e *PartialRegionMaterialisationError) Error() string {
	return fmt.Sprintf(
		"partial-region materialisation: declared %d regions %v, only %d materialised %v (missing: %v) — "+
			"likely HCS Common.0021 / quota cascade. Sovereign violates Pillar 2 (multi-region BCP) + Pillar 3 "+
			"(zero-tx-loss). Operator action required: inspect HCS quotas + decide whether to retain the partial "+
			"prov (single-region usable) or wipe + retry. Auto-destroy was NOT performed (operator decision per G117 #2840).",
		len(e.DeclaredRegions), e.DeclaredRegions,
		len(e.MaterializedRegions), e.MaterializedRegions,
		e.MissingRegions,
	)
}

// detectPartialRegionMaterialisation compares the operator's declared
// regions (the region code of each req.Regions[] entry, regionCode) against
// the per-region readback
// from tofu (out.ControlPlaneIPsPerRegion keys) and returns the
// materialised + missing sets.
//
// Returns the materialised-regions slice (always non-nil — empty when
// the readback was empty) and the missing-regions slice (nil when
// every declared region came up).
//
// Per G117 #2840 the post-condition is: every region declared in
// Request.Regions[] MUST have a corresponding entry in
// control_plane_ips_per_region with a non-empty EIP. Anything less is
// a partial-failure that breaks the topology promise.
// normalisePerRegionKeys collapses the on-the-wire shapes of
// `control_plane_ips_per_region` into the shape keyed by region code
// (regionCode) that detectPartialRegionMaterialisation expects:
//
//   - Contract version 1 (CLD-090): every module keys the output by region
//     code (e.g. "me-east-215-a", "fsn1"); passed through unchanged.
//   - A Hetzner working directory applied before contract version 1: keys
//     are `<cloudRegion>-<index>` for secondaries (e.g. "fsn1-1",
//     "hel1-2") + literal "primary" for the primary CP. "primary" is
//     mapped to the code of declaredRegions[0]; each suffixed key is
//     mapped to the leading segment when that is a declared code.
//
// Duplicate cloudRegions (legal Hetzner same-region-stacking case —
// fsn1-mgmt + fsn1-dataplane) collapse with first-non-empty-wins so a
// healthy stack reads as "materialised" without false-negative.
// Empty-string values are skipped (an empty EIP is not a working CP).
//
// Pure function; safe to unit-test against literal maps. Refs #2840.
func normalisePerRegionKeys(declaredRegions []RegionSpec, perRegion map[string]string) map[string]string {
	if len(perRegion) == 0 {
		return perRegion
	}
	out := make(map[string]string, len(perRegion))
	primaryCode := ""
	if len(declaredRegions) > 0 {
		primaryCode = strings.TrimSpace(regionCode(declaredRegions[0]))
	}
	// #5381: the suffix-strip below MUST be declared-set-aware. Huawei
	// cloud-region codes END IN DIGITS ("me-east-215",
	// "me-east-215-b-1"), so an unconditional "<region>-<index>" strip
	// corrupted them into "me-east" / "me-east-215-b", detection found
	// neither declared code, and a fully-materialised 2-region Sovereign
	// was stamped partial-failure (hw289 live). A key that already IS a
	// declared cloudRegion is final — only a key that is NOT declared may
	// be a Hetzner "<cloudRegion>-<index>" form.
	declaredSet := make(map[string]struct{}, len(declaredRegions))
	for _, r := range declaredRegions {
		if code := strings.TrimSpace(regionCode(r)); code != "" {
			declaredSet[code] = struct{}{}
		}
	}
	for k, v := range perRegion {
		if strings.TrimSpace(v) == "" {
			continue
		}
		code := k
		_, keyIsDeclared := declaredSet[k]
		if k == "primary" && primaryCode != "" {
			code = primaryCode
		} else if idx := strings.LastIndexByte(k, '-'); idx > 0 && !keyIsDeclared {
			// Hetzner secondaries: "<cloudRegion>-<index>" — strip the
			// numeric trailing segment if it parses as an int. Keep the
			// raw key when the suffix isn't numeric (defensive against
			// future key formats) OR when stripping would not yield a
			// declared region (so a digit-tailed provider code is never
			// truncated into a code nobody declared).
			suffix := k[idx+1:]
			allDigit := suffix != ""
			for _, r := range suffix {
				if r < '0' || r > '9' {
					allDigit = false
					break
				}
			}
			if allDigit {
				if _, strippedIsDeclared := declaredSet[k[:idx]]; strippedIsDeclared || len(declaredSet) == 0 {
					code = k[:idx]
				}
			}
		}
		if existing, ok := out[code]; !ok || existing == "" {
			out[code] = v
		}
	}
	return out
}

func detectPartialRegionMaterialisation(declaredRegions []RegionSpec, perRegion map[string]string) (materialised []string, missing []string) {
	materialised = make([]string, 0, len(perRegion))
	declared := make(map[string]struct{}, len(declaredRegions))
	for _, r := range declaredRegions {
		code := strings.TrimSpace(regionCode(r))
		if code == "" {
			continue
		}
		declared[code] = struct{}{}
		if ip, ok := perRegion[code]; ok && strings.TrimSpace(ip) != "" {
			materialised = append(materialised, code)
		} else {
			missing = append(missing, code)
		}
	}
	// Also append any materialised codes that weren't in the declared
	// set (defensive — tofu shouldn't produce these, but if it did we
	// want them visible in the operator-console PartialFailure view).
	for code, ip := range perRegion {
		if strings.TrimSpace(ip) == "" {
			continue
		}
		if _, ok := declared[code]; !ok {
			materialised = append(materialised, code)
		}
	}
	return materialised, missing
}

// ReasonStandbyRegionAbsent is the canonical Phase-1 failure reason
// stamped when a deployment requested an active-hot-standby (or
// active-active) topology but the standby region's cluster never
// materialised in the watch — no secondary control-plane was ever
// observed (#3375 §3(e)/DoD-7). It is the DR sibling of the
// "did not PUT kubeconfig" primary-region failure class: a topology the
// Sovereign cannot honour must NOT be stamped `ready`.
const ReasonStandbyRegionAbsent = "standby-region-absent"

// ReasonDefaultStorageClassEphemeral is the canonical Phase-1 failure
// reason stamped when an otherwise-ready Sovereign's resolved DEFAULT
// StorageClass is still the k3s ephemeral local-path provisioner
// (rancher.io/local-path) — meaning no cloud block-storage CSI flipped
// the default to a durable volume (#3971). Every PVC (prod Postgres
// 3×100Gi, openbao secrets, customer-Org DB, …) would land on node-local
// disk and be destroyed by any node replacement, invalidating the BCP/DR
// model. It is the storage sibling of ReasonStandbyRegionAbsent: a
// durability guarantee the Sovereign cannot honour must NOT be stamped
// `ready`.
const ReasonDefaultStorageClassEphemeral = "default-storageclass-ephemeral"

// DeclaredDRStandbyIntegrity reports whether a multi-region DR topology's
// standby half is structurally PRESENT, given:
//
//   - bcpTopology: the effective Request.BcpTopology (one of
//     single-region | active-hotstandby | active-active).
//   - declaredRegions: len(Request.Regions) — how many regions the
//     operator chose at signup.
//   - observedSecondaryRegions: how many SECONDARY regions the Phase-1
//     watch actually observed (the count of distinct keys in the
//     secondary-watcher census — a region whose kubeconfig never arrived
//     is NOT counted, which is exactly the hw150 absent-standby signal).
//
// It returns ok=true when the topology's standby requirement is met (or
// when the topology is single-region, where there is no standby to
// check). It returns ok=false + a human reason when active-hot-standby /
// active-active was requested but FEWER than (declaredRegions-1)
// secondary regions came up — i.e. a declared standby region is missing.
//
// This is the INTEGRITY half of #3375's DoD-7: it refuses to let a
// Sovereign claim active-hot-standby when its standby region is absent.
// It is DISTINCT from the surface-only "secondary degraded" census
// (#3611): a SLOW-but-present secondary has an observed watcher and so is
// counted here (it does not trip this gate — the prov is not hung); only
// a GENUINELY-ABSENT region (no cluster, no kubeconfig, zero observed
// secondary) trips it. It is also additive to the tofu-apply-time
// partial-region post-condition (#2840), which fires earlier when the
// region's EIP itself never materialised; this gate catches the case
// where the EIP came up but the region-B cluster never formed.
//
// Pure function — no I/O, no locks. Generic by construction: it keys on
// the declared topology + region counts, never on any app or blueprint
// name. Refs #3375.
func DeclaredDRStandbyIntegrity(bcpTopology string, declaredRegions, observedSecondaryRegions int) (ok bool, reason string) {
	switch strings.ToLower(strings.TrimSpace(bcpTopology)) {
	case BcpTopologyActiveHotStandby, BcpTopologyActiveActive:
		// Multi-region DR requested. Need at least (declaredRegions-1)
		// secondaries observed; a declared 2-region prov needs >=1.
		wantSecondary := declaredRegions - 1
		if wantSecondary < 1 {
			// Defensive: a multi-region topology with <2 declared regions
			// is rejected at Validate() (provisioner.go ~830); if we ever
			// reach here, treat the missing standby as absent.
			wantSecondary = 1
		}
		if observedSecondaryRegions < wantSecondary {
			return false, fmt.Sprintf(
				"topology %q was requested but %d of %d standby region(s) did not provision — the standby region's cluster never came up (no secondary control-plane observed). Disaster-recovery is INACTIVE; this Sovereign is running single-region. The deployment is NOT ready: a Sovereign cannot claim active-hot-standby when its standby region is absent (Pillar 2/3, #3375).",
				bcpTopology, wantSecondary-observedSecondaryRegions, declaredRegions-1)
		}
		return true, ""
	default:
		// single-region (or empty / unknown — Validate() gates those):
		// no standby to assert.
		return true, ""
	}
}

// Provisioner runs `tofu init && tofu apply` against the canonical
// infra/hetzner/ module.
type Provisioner struct {
	// ModulePath is the absolute path to the OpenTofu module directory.
	// In the deployed catalyst-api container this is /infra/hetzner/.
	ModulePath string
	// WorkDir is where per-deployment tofu state is kept. In production
	// this is a per-Sovereign subdirectory, persisted via the catalyst-api
	// PVC so re-runs (`tofu apply` again with same vars) are idempotent.
	WorkDir string
	// GHCRPullToken is the long-lived GHCR pull token mounted from the
	// `catalyst-ghcr-pull-token` Secret in the catalyst namespace as the
	// env var CATALYST_GHCR_PULL_TOKEN. Stamped onto every Request in
	// Provision() before writeTfvars(). Empty when the env var is
	// missing — Validate() rejects such deployments with a fail-fast
	// error so the operator notices the misconfiguration before
	// `tofu apply` runs.
	GHCRPullToken string

	// HarborRobotToken is the central Harbor proxy-cache robot account
	// secret (`robot$openova-bot` on harbor.openova.io). Mounted from
	// the Reflector-mirrored `harbor-robot-token` K8s Secret in the
	// catalyst namespace as env CATALYST_HARBOR_ROBOT_TOKEN.
	// cloudinit-control-plane.tftpl interpolates it into the new
	// Sovereign's /etc/rancher/k3s/registries.yaml so containerd
	// authenticates against harbor.openova.io's docker.io / gcr / quay /
	// k8s / ghcr proxy projects on every image pull (issue #557).
	// Empty falls through to anonymous Harbor pulls; if the proxy is
	// configured for public access this still works, but rate-limited
	// upstream (Docker Hub) pulls will fail when the proxy can't
	// authenticate either. Stamped onto every Request before tfvars.
	HarborRobotToken string

	// PowerDNSAPIKey is the contabo PowerDNS API key — the same value
	// living in the contabo cluster's `openova-system/powerdns-api-
	// credentials` Secret (key `api-key`). The catalyst-api Pod mounts
	// it via a Reflector-mirrored copy in the `catalyst` namespace as
	// env CATALYST_POWERDNS_API_KEY. cloudinit-control-plane.tftpl
	// interpolates it into the Sovereign's `cert-manager/powerdns-api-
	// credentials` Secret so bp-cert-manager-powerdns-webhook can write
	// DNS-01 challenge TXT records to contabo's authoritative omani.works
	// zone. PR #681 followup — without this the wildcard cert never
	// issues and the Sovereign Console TLS handshake fails (caught
	// live on otech47).
	PowerDNSAPIKey string

	// PDMBasicAuthUser / PDMBasicAuthPass — credentials for the public
	// PDM ingress at pool.openova.io (issue #879 Bug 2). Mounted from
	// the Reflector-mirrored `pdm-basicauth` Secret as envs
	// CATALYST_PDM_BASIC_AUTH_USER / CATALYST_PDM_BASIC_AUTH_PASS.
	// cloudinit-control-plane.tftpl interpolates them into the new
	// Sovereign's flux-system/pdm-basicauth Secret so its catalyst-api
	// inherits the same auth posture (Reflector mirrors them into
	// catalyst-system). Empty values render an empty Secret and the
	// Sovereign-side pdmFlipNS skips SetBasicAuth — same degradation
	// posture as the harbor-robot-token Empty-Token path.
	PDMBasicAuthUser string
	PDMBasicAuthPass string

	// OperatorCredentials — per provider name, the credentials of the
	// operator's own cloud account by credential key, read once at New()
	// (operatorCredentialsFromEnv). The handler stamps the same values onto
	// each Request before Validate() (deployments.go); Provision() fills every
	// key a Request leaves empty from here (#3716), the same pattern as
	// GHCRPullToken / HarborRobotToken above, so the pre-flight hooks always
	// have signing credentials.
	OperatorCredentials map[string]map[string]string

	// TofuPluginCacheDir is the OpenTofu provider plugin cache directory
	// (#3126). Set as TF_PLUGIN_CACHE_DIR on every `tofu` exec so the
	// provider binaries (huaweicloud, hetznercloud, dynadot, ...) are
	// downloaded from github release-assets ONCE and reused by every
	// subsequent provision regardless of region/provider.
	//
	// Why this matters: OpenTofu providers are hosted as binaries on
	// github release-assets (302 → release-assets.githubusercontent.com,
	// a Fastly/Azure-Blob CDN). `tofu init` fetches the zip + SHA256SUMS
	// + .sig from there. The catalyst-api uses a FRESH per-deployment
	// workdir, so without a shared plugin cache EVERY prov re-downloads
	// every provider from scratch — any transient github/CDN 504 then
	// kills a full 2-region prov at init (caught live: the CDN edge
	// returned intermittent 504s to the mothership; `tofu init` retries
	// only 2× internally then aborts).
	//
	// Mounted on the PERSISTENT catalyst-api-deployments PVC (same volume
	// the per-deployment workdir lives on) so the cache survives Pod
	// rolls and is shared across deployments. Read once at New() from
	// CATALYST_TF_PLUGIN_CACHE_DIR (default /var/lib/catalyst/tofu-plugin-cache);
	// runtime-overridable per docs/PRINCIPLES.md #4. Empty disables the
	// cache (falls back to per-workdir download — the pre-#3126
	// behaviour) so air-gapped operators pinning a provider mirror are
	// unaffected.
	TofuPluginCacheDir string
}

// New returns a Provisioner with paths read from environment.
//
// CATALYST_GHCR_PULL_TOKEN is read here once at startup. If the env var is
// missing the Provisioner is still constructed (so the catalyst-api Pod
// comes up cleanly and the wizard endpoints not requiring it — the BYO
// flows that do not invoke Phase-1 bootstrap-kit, and the diagnostic
// endpoints — keep working). Provision() stamps the token onto every
// Request, and Validate() rejects deployments where the field is empty
// with a clear pointer to docs/SECRET-ROTATION.md.
func New() *Provisioner {
	return &Provisioner{
		// Default reflects the providers/ refactor (Wave 2, Issue #1841):
		// per-cloud tofu modules live under infra/providers/<name>/.
		// The Containerfile keeps a /infra/hetzner -> /infra/providers/hetzner
		// symlink so any explicit CATALYST_TOFU_MODULE_PATH=/infra/hetzner
		// overrides set on older Sovereigns still resolve.
		//
		// Wave 4 (#2140): ModulePath is now the FALLBACK shared across every
		// provider — Provision()/Destroy() override it per-request via
		// resolveModulePath() which swaps the trailing directory to match
		// req.Provider. The env-mounted override remains authoritative when
		// set (air-gap operators pinning a custom module location).
		ModulePath: env("CATALYST_TOFU_MODULE_PATH", "/infra/providers/hetzner"),
		WorkDir:    env("CATALYST_TOFU_WORKDIR", "/var/lib/catalyst/tofu"),
		// #3126 — provider plugin cache on the persistent deployments PVC
		// (sibling of WorkDir's /var/lib/catalyst/tofu). First prov fills
		// it; every later prov reuses the cached providers with zero
		// github release-asset fetches. See the field doc on Provisioner.
		TofuPluginCacheDir: env("CATALYST_TF_PLUGIN_CACHE_DIR", "/var/lib/catalyst/tofu-plugin-cache"),
		GHCRPullToken:      os.Getenv("CATALYST_GHCR_PULL_TOKEN"),
		HarborRobotToken:   os.Getenv("CATALYST_HARBOR_ROBOT_TOKEN"),
		PowerDNSAPIKey:     os.Getenv("CATALYST_POWERDNS_API_KEY"),
		PDMBasicAuthUser:   os.Getenv("CATALYST_PDM_BASIC_AUTH_USER"),
		PDMBasicAuthPass:   os.Getenv("CATALYST_PDM_BASIC_AUTH_PASS"),
		// #3716 — the operator credentials backstop (see field docs).
		OperatorCredentials: operatorCredentialsFromEnv(),
	}
}

// operatorCredentialsFromEnv reads the operator's own cloud credentials, by
// provider name and credential key, from the environment of the Pod (the
// Secret huawei-operator-creds projects them; CLD-106 moves them to mounted
// files). An empty variable sets nothing.
func operatorCredentialsFromEnv() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, v := range []struct{ provider, key, env string }{
		{"huawei", "access_key", "CATALYST_HUAWEI_ACCESS_KEY"},
		{"huawei", "secret_key", "CATALYST_HUAWEI_SECRET_KEY"},
		{"huawei", "project_id", "CATALYST_HUAWEI_PROJECT_ID"},
		{"huawei", "region", "CATALYST_HUAWEI_REGION"},
	} {
		if value := os.Getenv(v.env); value != "" {
			if out[v.provider] == nil {
				out[v.provider] = map[string]string{}
			}
			out[v.provider][v.key] = value
		}
	}
	return out
}

// resolveModulePath returns the OpenTofu module directory for the
// supplied provider name. Wave 4 (#2140) wired this in so the same
// *Provisioner instance can drive a Hetzner provision AND a Huawei
// provision in the same process — no per-provider Provisioner
// duplication, no env mutation between calls.
//
// Resolution rules, in order:
//
//  1. If CATALYST_TOFU_MODULE_PATH was set explicitly via env, honour
//     it verbatim AND swap the trailing directory to match the
//     provider so an air-gapped operator pinning the root layout
//     (e.g. "/mnt/iac/infra/providers/hetzner") still picks up the
//     Huawei sibling at "/mnt/iac/infra/providers/huawei". This keeps
//     custom mount points working while still supporting per-provider
//     dispatch.
//  2. Otherwise fall back to the canonical layout shipped by the
//     catalyst-api container image: /infra/providers/<provider>/.
//
// The provider name is lower-cased and trimmed; Validate() guarantees
// it's one of the registered names by the time this is called, so an
// empty / unknown value here is a programmer error — log + fall back
// to hetzner to preserve byte-equivalent behaviour for the back-compat
// path.
func (p *Provisioner) resolveModulePath(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "hetzner"
	}
	base := p.ModulePath
	if base == "" {
		base = "/infra/providers/hetzner"
	}
	// Strip the trailing directory and re-append the provider name.
	// filepath.Dir handles trailing-slash variants ("/a/b" and "/a/b/")
	// equivalently.
	parent := filepath.Dir(strings.TrimRight(base, "/"))
	if parent == "" || parent == "." {
		// Unexpected — env-supplied path was a single segment.
		// Fall through to the canonical layout.
		return "/infra/providers/" + provider
	}
	return filepath.Join(parent, provider)
}

// Provision runs the full sequence. Emits events into the channel; returns
// Result on success.
func (p *Provisioner) Provision(ctx context.Context, req Request, events chan<- Event) (*Result, error) {
	// Stamp the GHCR pull token from the Provisioner (read once from
	// CATALYST_GHCR_PULL_TOKEN at New()) onto every Request BEFORE
	// validation. The handler does not — and must not — accept this
	// from the wizard payload, so the env-loaded value is the only
	// source. Stamping before Validate() lets the validator reject
	// the deployment with a clear error when the env var is missing.
	if strings.TrimSpace(req.GHCRPullToken) == "" {
		req.GHCRPullToken = p.GHCRPullToken
	}
	if strings.TrimSpace(req.HarborRobotToken) == "" {
		req.HarborRobotToken = p.HarborRobotToken
	}
	if strings.TrimSpace(req.PowerDNSAPIKey) == "" {
		req.PowerDNSAPIKey = p.PowerDNSAPIKey
	}
	if strings.TrimSpace(req.PDMBasicAuthUser) == "" {
		req.PDMBasicAuthUser = p.PDMBasicAuthUser
	}
	if req.PDMBasicAuthPass == "" {
		req.PDMBasicAuthPass = p.PDMBasicAuthPass
	}
	// #3716 — backstop the operator credentials of the request's provider
	// (loaded once at New()) onto the Request, mirroring the GHCR/Harbor
	// pattern above: every key the request leaves empty is filled, so the
	// pre-flight hooks still receive signing credentials on any code path
	// that reaches Provision() without the handler's stamp. The map is
	// copied first, so the caller's request is not changed.
	req.Credentials = maps.Clone(req.Credentials)
	for key, value := range p.OperatorCredentials[strings.ToLower(strings.TrimSpace(req.Provider))] {
		if strings.TrimSpace(req.Credential(key)) == "" {
			req.SetCredential(key, value)
		}
	}

	if err := req.Validate(); err != nil {
		return nil, err
	}
	// NET-011: refuse to start when the new cluster's API port would be
	// closed to this provisioner (control_plane_sources.go).
	if err := CheckControlPlaneSources(req); err != nil {
		return nil, err
	}

	emit := func(phase, level, msg string) {
		select {
		case events <- Event{Time: time.Now().UTC().Format(time.RFC3339), Phase: phase, Level: level, Message: msg}:
		default:
		}
	}

	// Per-deployment workdir keyed by Sovereign FQDN — re-running with the
	// same FQDN is idempotent (tofu apply on existing state).
	deployDir := filepath.Join(p.WorkDir, req.workdirKey())
	if err := os.MkdirAll(deployDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workdir: %w", err)
	}

	// Stage the module by symlinking — keeps state isolated per deployment
	// while sharing the canonical module source.
	//
	// Wave 4 (#2140): the per-Request Provider field selects the per-cloud
	// tofu module under infra/providers/<provider>/. The Provisioner's
	// ModulePath default ("/infra/providers/hetzner") covers the
	// back-compat path when Provider is empty/hetzner; a non-hetzner
	// Provider swaps to the matching directory under the same root.
	// Validate() guarantees req.Provider is one of the registered names
	// at this point.
	modulePath := p.resolveModulePath(req.Provider)
	if err := stageModule(modulePath, deployDir); err != nil {
		return nil, fmt.Errorf("stage tofu module: %w", err)
	}

	// Write tofu.auto.tfvars.json — OpenTofu auto-loads any *.auto.tfvars.json
	// in the working directory at apply time.
	if err := writeTfvars(deployDir, req); err != nil {
		return nil, fmt.Errorf("write tfvars: %w", err)
	}

	// The adapter's checks before `tofu init` (providers.Preflighter): a limit
	// of the cloud that cannot be raised from code is refused here, before any
	// resource exists, instead of minutes into `tofu apply`. ProtectedIDs holds
	// every live deployment (#4614, reclaimProtectSet), so an in-band reclaim
	// never touches a live Sovereign. The objects the adapter creates for the
	// plan are imported after `tofu init` (precreated.go).
	inState, err := stateAddresses(deployDir)
	if err != nil {
		return nil, err
	}
	var preCreated []providers.StateImport
	if pf, ok, _ := providers.As[providers.Preflighter](req.Provider); ok {
		in := providers.PreflightInput{
			Spec:         capabilitySpec(req),
			ProtectedIDs: reclaimProtectSet(req.DeploymentID),
			InState:      inState,
			Progress:     func(msg string) { emit("tofu-init", "info", msg) },
		}
		res, err := pf.Preflight(ctx, in)
		if err != nil {
			return nil, err
		}
		preCreated = res.Imports
	}

	emit("tofu-init", "info", "Initialising OpenTofu working directory")
	if err := p.runTofuInitWithRetry(ctx, deployDir, emit); err != nil {
		return nil, fmt.Errorf("tofu init: %w", err)
	}
	preCreated, err = p.importPreCreated(ctx, deployDir, preCreated, inState, emit)
	if err != nil {
		return nil, err
	}

	emit("tofu-plan", "info", "Planning cloud resources (network, firewall, servers, load balancers, DNS)")
	if err := p.runTofu(ctx, deployDir, []string{"plan", "-input=false", "-no-color", "-out=tfplan"}, emit); err != nil {
		return nil, fmt.Errorf("tofu plan: %w", err)
	}

	emit("tofu-apply", "info", "Applying — this provisions real cloud resources, please wait")
	// Wave 5.129 (hw30 fix-forward 2026-05-27): cap tofu parallelism at 2.
	// Default 10 fanned out 8+ worker ECS creates simultaneously, which
	// overloaded the HCS scheduler's CollectInfoTask and returned
	// `Common.0021: CollectInfoTask-fail: Sub job fail!` on 4 of 8
	// workers on hw30 #4.
	//
	// Wave 5.141 (hw30 #17) experimented with parallelism=1. Wave 5.142
	// REVERTED to parallelism=2 — empirical data on hw30 #18
	// (02fe531cb7ab20e0) showed parallelism=1 made things WORSE: 0 ECSs
	// created across 4 attempts because the CP itself hit Common.0021
	// every time when scheduled alone. Under parallelism=2 the CP +
	// first worker create together and the CP reliably succeeds (then
	// 5 of 8 workers ACTIVE, only odd-numbered worker indices fail).
	// Net: parallelism=2 is the sweet spot.
	applyArgs := []string{"apply", "-input=false", "-no-color", "-auto-approve", "-parallelism=2", "tfplan"}
	// Wave 5.132 (hw30 fix-forward 2026-05-27): retry up to 3 times on
	// transient HCS scheduler failures (Common.0021 CollectInfoTask-fail).
	// This error fires when HCS's per-worker metadata-collection step
	// times out — independent of tofu parallelism. The workers get
	// scheduled and create the ECS instance, but the post-create info
	// task fails before tofu can record state. Re-plan + re-apply is
	// idempotent (the failed workers will be re-created with same names)
	// and reliably succeeds on the next attempt.
	// Wave 5.133 (hw30 fix-forward 2026-05-27): bumped retry count 3→6
	// and initial backoff 30s→90s with exponential growth (90s, 180s,
	// 360s, 720s, 1440s, 2880s). Observed on hw30 #7 that the SAME
	// worker indices (w3, w4, w5, w6) repeatedly hit Common.0021 across
	// 3 retries with 30s backoff. HCS scheduler cell-health appears to
	// recover on minutes-scale, not seconds. Total max wait is ~95 min
	// worst-case (6 retries × exponential), but most provs settle by
	// retry #3 (~10 min). The 30m wipe-min-life-protection threshold
	// (handler/wipe.go) is bumped via env override per #914 if needed.
	const maxApplyRetries = 6
	const baseBackoff = 90 * time.Second
	var lastErr error
	// Wave 5.145 (hw30 #21 fix-forward 2026-05-27): the consecutive failures
	// of each resource address (applyFailures), so a prov whose address keeps
	// failing despite the per-retry name salt stops after the third attempt
	// instead of spending the remaining backoffs.
	failed := applyFailures{}
	for attempt := 1; attempt <= maxApplyRetries; attempt++ {
		err := p.runTofu(ctx, deployDir, applyArgs, emit)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		// Only a failure that providers.ApplyErrorClassifier (or OpenTofu's own
		// read-after-create text, classifyApplyError) calls transient is
		// retried; a configuration error does not fix itself on retry.
		errStr := err.Error()
		class := classifyApplyError(req.Provider, errStr)
		if attempt < maxApplyRetries && class.Retryable {
			// Wave 5.145 — stop early when the same resource address keeps
			// failing despite the per-retry name salt: the cloud's capacity for
			// it is unhealthy, and the remaining backoffs would be spent on a
			// doomed prov.
			if stuck := failed.record(class); len(stuck) > 0 {
				return nil, fmt.Errorf("%s: %d resource address(es) failed in 3 or more consecutive attempts despite the per-retry name salt — %v. The cloud's capacity for that zone and flavor is unhealthy. Retry later, or try another zone or flavor (deployment_id=%s)", class.Reason, len(stuck), stuck, req.DeploymentID)
			}
			backoff := baseBackoff * time.Duration(1<<(attempt-1))
			emit("tofu-apply", "warn", fmt.Sprintf("%s — attempt %d/%d, re-planning + retrying in %s", class.Reason, attempt, maxApplyRetries, backoff))
			time.Sleep(backoff)
			// Wave 5.139 — bump the retry_attempt salt so any worker
			// NOT yet in state (i.e. the ones the prior attempt hit
			// Common.0021 on) gets a NEW name on this retry's plan.
			// HCS scheduler picks a fresh cell for the new name,
			// dodging the bad cell. Existing ACTIVE workers are
			// protected by lifecycle.ignore_changes=[name] in the
			// huawei worker resource block.
			if berr := bumpRetryAttempt(deployDir, attempt); berr != nil {
				emit("tofu-apply", "warn", fmt.Sprintf("retry_attempt bump failed (continuing without name-salt rotation): %v", berr))
			}
			// Re-plan so the next attempt builds a fresh tfplan
			// that captures any state mutations from the partial apply.
			if perr := p.runTofu(ctx, deployDir, []string{"plan", "-input=false", "-no-color", "-out=tfplan"}, emit); perr != nil {
				return nil, fmt.Errorf("tofu plan (retry): %w", perr)
			}
			continue
		}
		return nil, fmt.Errorf("tofu apply: %w", err)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("tofu apply: %w", lastErr)
	}

	// The adapter's step after a successful `tofu apply` (providers.PostApplier),
	// before cloud-init needs the network. An error fails the prov; a step that
	// may fail without harm reports through Progress and returns nil.
	if pa, ok, _ := providers.As[providers.PostApplier](req.Provider); ok {
		res, err := pa.PostApply(ctx, providers.PostApplyInput{
			Spec:     capabilitySpec(req),
			Progress: func(msg string) { emit("post-apply", "info", msg) },
		})
		if err != nil {
			return nil, fmt.Errorf("post-apply: %w", err)
		}
		if res.ReapplyNeeded {
			emit("tofu-apply", "info", "Applying once more, as the cloud adapter asked after its post-apply step")
			if err := p.runTofu(ctx, deployDir, []string{"apply", "-input=false", "-no-color", "-auto-approve", "-parallelism=2"}, emit); err != nil {
				return nil, fmt.Errorf("tofu apply (after post-apply): %w", err)
			}
		}
	}

	emit("tofu-output", "info", "Reading OpenTofu outputs")
	out, err := p.readOutputs(ctx, deployDir)
	if err != nil {
		return nil, fmt.Errorf("read tofu outputs: %w", err)
	}

	// ── Crossplane adoption (Phase-1 hand-off) — ADR-0011 / Refs #4002 ──
	// THE PRODUCER that was missing. Read the OpenTofu state and emit one
	// Observe-first CloudAdoption per real cloud resource (loadbalancer /
	// server / network / subnet / eip / firewall), each annotated with the
	// cloud resource id, into adoption-claims.yaml in the deploy workdir.
	// Crossplane's provider-opentofu then OBSERVES the live infra by
	// external-name — so Crossplane finally OWNS what OpenTofu bootstrapped,
	// instead of OpenTofu silently owning everything and Crossplane sitting
	// inert. Best-effort + non-fatal: a generation failure must never fail
	// an otherwise-successful apply (the bootstrap-kit's adoption-claims.yaml
	// placeholder keeps the infrastructure Kustomization valid until the
	// next reconcile picks up the generated file).
	if err := p.writeAdoptionClaims(deployDir, req, emit); err != nil {
		emit("crossplane-adoption", "warn", "adoption-claim generation failed (non-fatal — Crossplane will adopt on the next reconcile once the file lands): "+err.Error())
	}

	emit("flux-bootstrap", "info", "Cloud-init has bootstrapped Flux + Crossplane in the new cluster — Flux will now reconcile clusters/"+req.SovereignFQDN+"/ from the public OpenOva monorepo, installing the 11-component bootstrap kit and bp-catalyst-platform umbrella in dependency order. The wizard's progress page will poll Flux Kustomizations on the new cluster for steady-state.")

	// G117 #2840 — post-condition partial-region rollback defense.
	//
	// `tofu apply` returning nil-error guarantees the OpenTofu state is
	// internally consistent — NOT that every declared region produced
	// a working primary CP node. On HCS Kom4DC the scheduler can
	// silently refuse a secondary region when a per-AZ/per-flavor cell
	// is unhealthy (Common.0021 / quota cascade); the per-region
	// resource block then evaluates to an empty `for` expression, the
	// per-region EIP map output ends up with fewer entries than
	// expected, and tofu happily reports SUCCESS.
	//
	// Without this gate the deployment proceeds to commitPDMWithRetry +
	// runPhase1Watch on a Sovereign that violates Pillar 2 (multi-region
	// BCP) + Pillar 3 (zero-tx-loss) — exactly what happened on hw86
	// (Refs #2840). Operator sees a green "ready" wizard pill but the
	// Sovereign is single-region under the hood.
	//
	// Detection runs only when the operator declared >=2 regions AND
	// the per-provider readback populated a per-region map. Huawei
	// emits `control_plane_ips_per_region` keyed by cloudRegion verbatim;
	// Hetzner emits the same output name but keyed by `<cloudRegion>-<index>`
	// for secondaries + literal `primary` for the primary CP (the
	// same-region-duplicates test constraint — fsn1-mgmt + fsn1-dataplane
	// are legal). normalisePerRegionKeys() collapses both shapes to the
	// cloudRegion form before detection runs. When the post-condition
	// fails we return a typed PartialRegionMaterialisationError carrying
	// the partial Result — the handler stamps Deployment.status =
	// "partial-failure" but does NOT auto-`tofu destroy`. The operator
	// decides.
	normalisedPerRegion := normalisePerRegionKeys(req.Regions, out.ControlPlaneIPsPerRegion)
	materialised, missing := detectPartialRegionMaterialisation(req.Regions, normalisedPerRegion)
	declared := make([]string, 0, len(req.Regions))
	for _, r := range req.Regions {
		if code := strings.TrimSpace(regionCode(r)); code != "" {
			declared = append(declared, code)
		}
	}

	result := &Result{
		SovereignFQDN:         req.SovereignFQDN,
		ControlPlaneIP:        out.ControlPlaneIP,
		LoadBalancerIP:        out.LoadBalancerIP,
		ConsoleLoadBalancerIP: out.ConsoleLoadBalancerIP, // #4053 — "" when module pre-dates #4053
		ConsoleURL:            fmt.Sprintf("https://console.%s", req.SovereignFQDN),
		GitOpsRepoURL:         fmt.Sprintf("https://gitea.%s", req.SovereignFQDN),
		MaterializedRegions:   materialised,
		PreCreated:            preCreated,
	}

	// Only enforce when the operator declared >=2 regions AND the
	// per-provider readback was populated (single-region Hetzner and
	// any pre-G117 readback that didn't surface the map skip the
	// check). The admission gate at Validate() already rejects a
	// SUBMISSION of bcpTopology=active-hotstandby + len(regions)<2;
	// this gate catches the downstream tofu-apply partial.
	if len(declared) >= 2 && len(out.ControlPlaneIPsPerRegion) > 0 && len(missing) > 0 {
		emit("tofu-apply", "error", fmt.Sprintf(
			"G117 #2840 partial-region materialisation detected — declared %d regions %v, only %d materialised %v (missing %v). "+
				"Marking deployment as partial-failure; auto-destroy was NOT performed so the operator can inspect the partial infra "+
				"and decide whether to retain it (single-region usable) or wipe + retry against a different AZ/flavor.",
			len(declared), declared, len(materialised), materialised, missing,
		))
		return result, &PartialRegionMaterialisationError{
			DeclaredRegions:     declared,
			MaterializedRegions: materialised,
			MissingRegions:      missing,
			Result:              result,
		}
	}

	return result, nil
}

// Destroy runs `tofu destroy -auto-approve` against the per-deployment
// workdir for req.SovereignFQDN. Idempotent — re-running on a partially-
// destroyed state cleans up whatever's left. Streams stdout/stderr as
// Events to the wizard so the operator sees progress.
//
// On success the per-deployment workdir is REMOVED so the next
// re-provision starts fresh. On failure the workdir is preserved so the
// operator can inspect state — they MUST then run a force-purge against
// the cloud account directly to remove orphans, since `tofu destroy`
// failing partway leaves resources behind.
func (p *Provisioner) Destroy(ctx context.Context, req Request, events chan<- Event) error {
	if strings.TrimSpace(req.GHCRPullToken) == "" {
		req.GHCRPullToken = p.GHCRPullToken
	}
	if strings.TrimSpace(req.HarborRobotToken) == "" {
		req.HarborRobotToken = p.HarborRobotToken
	}
	if strings.TrimSpace(req.PowerDNSAPIKey) == "" {
		req.PowerDNSAPIKey = p.PowerDNSAPIKey
	}
	if strings.TrimSpace(req.PDMBasicAuthUser) == "" {
		req.PDMBasicAuthUser = p.PDMBasicAuthUser
	}
	if req.PDMBasicAuthPass == "" {
		req.PDMBasicAuthPass = p.PDMBasicAuthPass
	}

	emit := func(phase, level, msg string) {
		select {
		case events <- Event{Time: time.Now().UTC().Format(time.RFC3339), Phase: phase, Level: level, Message: msg}:
		default:
		}
	}

	deployDir := filepath.Join(p.WorkDir, req.workdirKey())

	// If the workdir doesn't exist, there's no tofu state to destroy —
	// either the deployment never made it past CreateDeployment, or it
	// was already cleaned up. Nothing to do; let the caller continue
	// with the post-tofu cleanup steps (Hetzner orphan purge, PDM
	// release, local state cleanup).
	if _, err := os.Stat(deployDir); os.IsNotExist(err) {
		emit("tofu-destroy", "info", "no tofu workdir for "+req.SovereignFQDN+" — nothing to destroy")
		return nil
	} else if err != nil {
		return fmt.Errorf("stat tofu workdir: %w", err)
	}

	// Re-stage the module + tfvars so a partially-cleaned workdir still
	// has what tofu needs to destroy. Same provider-aware resolution as
	// Provision() — a Huawei deployment destroys against
	// infra/providers/huawei/, not the default Hetzner module.
	modulePath := p.resolveModulePath(req.Provider)
	if err := stageModule(modulePath, deployDir); err != nil {
		return fmt.Errorf("stage tofu module: %w", err)
	}
	// #3140: the wipe-time req is often reconstructed without the topology
	// (regions/ssh_public_key/org_email/obs_bucket_name). Overwriting the
	// complete provision-time tofu.auto.tfvars.json with it makes `tofu destroy`
	// fail variable-validation, forcing the providerPurge fallback. For Huawei
	// (creds are env-injected via CATALYST_HUAWEI_*, not the tfvars file), when
	// req lacks the regions AND the complete file is already present, PRESERVE
	// it instead of clobbering. Hetzner keeps the re-write (its token is
	// re-prompted into req at wipe time). A partially-cleaned workdir (file
	// removed) still falls through to writeTfvars.
	tfvarsPath := filepath.Join(deployDir, "tofu.auto.tfvars.json")
	_, tfvarsStatErr := os.Stat(tfvarsPath)
	preserveExistingTfvars := strings.EqualFold(strings.TrimSpace(req.Provider), "huawei") &&
		len(req.Regions) == 0 && tfvarsStatErr == nil
	if preserveExistingTfvars {
		emit("tofu-destroy", "info", "#3140: preserving complete provision-time tofu.auto.tfvars.json (wipe req lacks topology)")
		// SOV-131, SOV-338: a kept file may lack the module input
		// `variables` or hold a document of an older contract, and
		// `tofu destroy` needs that input with exactly the contract keys.
		if err := addVariablesIfMissing(tfvarsPath, req); err != nil {
			return fmt.Errorf("add variables to tfvars: %w", err)
		}
		// CLD-090: a kept file written before module contract version 1
		// has `regions` entries without cloud_region, and the module no
		// longer accepts them.
		if err := addCloudRegionIfMissing(tfvarsPath, req); err != nil {
			return fmt.Errorf("add cloud_region to tfvars: %w", err)
		}
	} else if err := writeTfvars(deployDir, req); err != nil {
		return fmt.Errorf("write tfvars: %w", err)
	}

	emit("tofu-init", "info", "Re-initialising OpenTofu working directory for destroy")
	if err := p.runTofu(ctx, deployDir, []string{"init", "-input=false", "-no-color"}, emit); err != nil {
		return fmt.Errorf("tofu init: %w", err)
	}

	emit("tofu-destroy", "info", "Destroying cloud resources for "+req.SovereignFQDN+" (network, firewall, ssh-key, servers, load balancers)")
	if err := p.runTofu(ctx, deployDir, []string{"destroy", "-input=false", "-no-color", "-auto-approve"}, emit); err != nil {
		// Don't remove the workdir — operator may want to inspect.
		return fmt.Errorf("tofu destroy: %w", err)
	}

	// Remove the workdir on success — next re-provision starts fresh.
	if err := os.RemoveAll(deployDir); err != nil {
		emit("tofu-destroy", "warn", "could not remove workdir "+deployDir+": "+err.Error())
		// Non-fatal — destroy itself succeeded.
	}

	emit("tofu-destroy", "info", "Tofu destroy complete; workdir removed")
	return nil
}

// tofuInitMaxAttempts / tofuInitBaseBackoff control the bounded
// retry-with-exponential-backoff applied to `tofu init` (#3126). 4
// attempts at 5s/15s/45s (base 5s, ×3 growth) ride out a short github
// release-asset CDN blip even on a COLD plugin cache; on a warm cache the
// first attempt needs zero github fetches and never sleeps. Package-level
// so the unit test can reference the same growth contract.
const (
	tofuInitMaxAttempts   = 4
	tofuInitBaseBackoff   = 5 * time.Second
	tofuInitBackoffGrowth = 3
)

// isTransientInitFailure reports whether a `tofu init` error string looks
// like a provider-install / network-class failure that a retry could
// clear (#3126), as opposed to a deterministic configuration error
// (bad provider constraint, malformed backend block) where retrying is
// pointless. The markers are the substrings OpenTofu emits when the
// github release-asset CDN flakes or a registry/network hop times out:
//
//   - "Failed to install provider"      — the umbrella init error
//   - "Failed to query available provider packages"
//   - "could not query provider registry"
//   - "504" / "Gateway Timeout"         — the actual CDN edge status
//   - "502" / "Bad Gateway" / "503" / "Service Unavailable"
//   - "failed to retrieve" / "error fetching" — checksum/zip fetch leg
//   - "TLS handshake timeout" / "i/o timeout" / "connection reset"
//   - "timeout" / "temporary failure" / "no such host" (DNS blip)
//   - "EOF" / "unexpected EOF"          — truncated CDN response
//
// Matching is case-insensitive so "gateway timeout" and "Gateway Timeout"
// both qualify. A real config error (e.g. "Invalid provider requirements"
// without any network marker) does NOT match and is surfaced immediately.
func isTransientInitFailure(errStr string) bool {
	s := strings.ToLower(errStr)
	markers := []string{
		"failed to install provider",
		"failed to query available provider",
		"could not query provider registry",
		"failed to retrieve",
		"error fetching",
		"504",
		"gateway timeout",
		"502",
		"bad gateway",
		"503",
		"service unavailable",
		"tls handshake timeout",
		"i/o timeout",
		"connection reset",
		"connection refused",
		"temporary failure",
		"no such host",
		"timeout",
		"unexpected eof",
		"eof",
	}
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// retryTofuInit runs `runOnce` (a single `tofu init`) with bounded
// retry-with-exponential-backoff, retrying ONLY on transient
// provider-install/network failures per isTransientInitFailure (#3126).
// `sleep` is injected so the unit test can pass a no-op instead of
// actually waiting 5s+15s+45s. Returns nil on the first success; the
// last error otherwise. A non-transient (config) error returns
// immediately without consuming the remaining attempts.
func retryTofuInit(ctx context.Context, runOnce func() error, sleep func(time.Duration), emit func(string, string, string)) error {
	var lastErr error
	for attempt := 1; attempt <= tofuInitMaxAttempts; attempt++ {
		err := runOnce()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt >= tofuInitMaxAttempts || !isTransientInitFailure(err.Error()) {
			break
		}
		// Stop early if the caller's context is already done — no point
		// sleeping through a backoff we'll abort anyway.
		if ctx.Err() != nil {
			break
		}
		backoff := tofuInitBaseBackoff
		for i := 1; i < attempt; i++ {
			backoff *= tofuInitBackoffGrowth
		}
		emit("tofu-init", "warn", fmt.Sprintf(
			"transient provider-install/network failure on tofu init (likely github release-asset CDN blip) — attempt %d/%d, retrying in %s: %v",
			attempt, tofuInitMaxAttempts, backoff, err,
		))
		sleep(backoff)
	}
	return lastErr
}

// runTofuInitWithRetry wraps the real `tofu init` exec in retryTofuInit
// so a short github/CDN outage at provider-download time no longer fails
// the whole prov (#3126). Layer 2 of the #3126 fix; layer 1 is the
// TF_PLUGIN_CACHE_DIR wired in runTofu (which means attempt 1 usually
// needs ZERO github fetches once the cache is warm).
func (p *Provisioner) runTofuInitWithRetry(ctx context.Context, deployDir string, emit func(string, string, string)) error {
	return retryTofuInit(ctx, func() error {
		return p.runTofu(ctx, deployDir, []string{"init", "-input=false", "-no-color"}, emit)
	}, time.Sleep, emit)
}

// runTofu executes `tofu <args>` in deployDir, streaming stdout/stderr lines
// as Events to the wizard.
func (p *Provisioner) runTofu(ctx context.Context, deployDir string, args []string, emit func(string, string, string)) error {
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = deployDir
	cmd.Env = append(os.Environ(),
		// HCLOUD_TOKEN must be in the environment for the hcloud provider —
		// OpenTofu's variable system does NOT pass tfvars to the provider's
		// auth flow, only to the module's variable references. So we duplicate
		// it as both a tfvar (module references it) AND env (provider auth).
		// The tfvar value is what gets serialized; we keep it short-lived.
		"TF_INPUT=false",
		"TF_IN_AUTOMATION=true",
	)
	// #3126 — point OpenTofu at the shared, persistent provider plugin
	// cache so providers are fetched from github release-assets ONCE and
	// reused on every subsequent prov (the per-deployment workdir is
	// fresh each time, so without this every prov cold-downloads every
	// provider and a transient github/CDN 504 fails the whole apply at
	// init). MkdirAll is idempotent + cheap; OpenTofu refuses to use a
	// non-existent TF_PLUGIN_CACHE_DIR (it errors rather than creating
	// it), so we must create it ourselves. On MkdirAll failure we log +
	// continue WITHOUT the cache var (degrade to per-workdir download —
	// the pre-#3126 behaviour) rather than failing the prov outright.
	if cacheDir := strings.TrimSpace(p.TofuPluginCacheDir); cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			emit("tofu-init", "warn", fmt.Sprintf("provider plugin cache dir %q unavailable (%v) — falling back to per-workdir provider download", cacheDir, err))
		} else {
			cmd.Env = append(cmd.Env, "TF_PLUGIN_CACHE_DIR="+cacheDir)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	// Wave 5.136 (hw30 #12 fix-forward 2026-05-27): also capture stderr to
	// an in-memory buffer so the caller's retry-decision can substring-
	// match against actual tofu error text (e.g. "Common.0021",
	// "CollectInfoTask-fail"). cmd.Wait() returns only "exit status 1"
	// for tofu failures; the retry loop in Provision (provisioner.go:1231)
	// was therefore never engaging because the error string lacked the
	// transient-failure markers. hw30 #12 failed in one attempt at
	// 2026-05-27T00:30Z despite the 6-attempt exponential-backoff loop
	// being present in the code (Wave 5.132/5.133) — the matcher just
	// couldn't see the markers it needed.
	var stderrBuf bytes.Buffer
	const stderrCapMax = 32 * 1024 // bound memory; head of stderr is enough for the marker
	streamDone := make(chan struct{}, 2)
	go func() {
		streamLines(stdout, "tofu", "info", emit)
		streamDone <- struct{}{}
	}()
	go func() {
		streamLinesTee(stderr, "tofu", "warn", emit, &stderrBuf, stderrCapMax)
		streamDone <- struct{}{}
	}()

	waitErr := cmd.Wait()
	<-streamDone
	<-streamDone

	if waitErr != nil {
		stderrTail := strings.TrimSpace(stderrBuf.String())
		if stderrTail != "" {
			return fmt.Errorf("tofu %s failed: %w | stderr: %s", strings.Join(args, " "), waitErr, stderrTail)
		}
		return fmt.Errorf("tofu %s failed: %w", strings.Join(args, " "), waitErr)
	}
	return nil
}

// streamLinesTee streams scanner lines through emit() AND appends them to buf
// (bounded by capMax bytes) so the caller can inspect tofu stderr for
// substring markers after cmd.Wait() returns only the exit status.
func streamLinesTee(r io.Reader, phase, level string, emit func(string, string, string), buf *bytes.Buffer, capMax int) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		emit(phase, level, line)
		if buf.Len()+len(line)+1 <= capMax {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
}

func streamLines(r io.Reader, phase, level string, emit func(string, string, string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		emit(phase, level, line)
	}
}

type tofuOutputs struct {
	ControlPlaneIP string `json:"control_plane_ip"`
	LoadBalancerIP string `json:"load_balancer_ip"`
	// ConsoleLoadBalancerIP — #4053. Both providers emit
	// `console_load_balancer_ip` (the dedicated console LB for the isolated
	// cilium-gateway-console). asString() returns "" when the key is absent
	// (a tofu module that pre-dates #4053), and the DNS layer falls back to
	// LoadBalancerIP for the console records — byte-identical legacy behaviour.
	ConsoleLoadBalancerIP string `json:"console_load_balancer_ip"`
	// ControlPlaneIPsPerRegion — map<region-code, primary-CP-EIP>.
	// Populated by BOTH the Huawei module's `control_plane_ips_per_region`
	// output AND (as of 2026-06-03 / #2840 follow-up) the Hetzner module's
	// same-named output. Keyed by the operator-supplied cloudRegion
	// value verbatim on both providers, one entry per region in
	// var.regions including the primary. Empty for legacy single-region
	// payloads that never declared a multi-region intent. G117 #2840
	// reads this to detect partial-region materialisation: a 2-region
	// declaration that produces only 1 map entry means the cloud
	// scheduler refused to allocate the secondary region (HCS:
	// Common.0021 / quota cascade; Hetzner: project quota / capacity
	// cascade) and the prov has silently degraded to single-region —
	// which violates Pillar 2 (multi-region BCP) + Pillar 3
	// (zero-tx-loss). Hetzner's legacy `control_plane_ips_by_region`
	// (secondaries-only, primary excluded) is still emitted for any
	// consumer that already reads it.
	ControlPlaneIPsPerRegion map[string]string `json:"control_plane_ips_per_region"`
}

func (p *Provisioner) readOutputs(ctx context.Context, deployDir string) (*tofuOutputs, error) {
	cmd := exec.CommandContext(ctx, "tofu", "output", "-json", "-no-color")
	cmd.Dir = deployDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// tofu output -json wraps each output in {"value": ..., "type": ..., "sensitive": ...}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse tofu output: %w", err)
	}
	asString := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		if s, ok := v.Value.(string); ok {
			return s
		}
		return ""
	}
	// asStringMap decodes a tofu `map(string)` output into a Go
	// map[string]string. Missing-key, wrong-type, and nil-value entries
	// are silently skipped so a partially-materialised apply still
	// surfaces the keys that DID land (which is the whole point of the
	// G117 #2840 partial-region detection — we need to enumerate what
	// came up before we can name what didn't).
	asStringMap := func(key string) map[string]string {
		v, ok := raw[key]
		if !ok || v.Value == nil {
			return nil
		}
		m, ok := v.Value.(map[string]any)
		if !ok {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, val := range m {
			if s, ok := val.(string); ok && s != "" {
				out[k] = s
			}
		}
		return out
	}
	return &tofuOutputs{
		ControlPlaneIP:           asString("control_plane_ip"),
		LoadBalancerIP:           asString("load_balancer_ip"),
		ConsoleLoadBalancerIP:    asString("console_load_balancer_ip"), // #4053 — "" when module pre-dates #4053
		ControlPlaneIPsPerRegion: asStringMap("control_plane_ips_per_region"),
	}, nil
}

// writeTfvars renders tofu.auto.tfvars.json from the wizard request. The
// module's variables.tf declares every key here; the JSON format is auto-
// loaded by tofu without any -var-file flag.
func writeTfvars(deployDir string, req Request) error {
	gitopsBranch, err := resolveGitopsBranch(req)
	if err != nil {
		return err
	}
	defaultStorageClass, err := storageClass(req)
	if err != nil {
		return fmt.Errorf("default storage class: %w", err)
	}
	// The address the new Sovereign calls back to. The variables document
	// names it as its parent's API address.
	apiURL := env(
		"CATALYST_API_PUBLIC_URL",
		"https://console.openova.io/sovereign",
	)
	doc, err := childVariables(req, apiURL)
	if err != nil {
		return err
	}
	// `regions` in the shape of module contract version 1 (CLD-090), each
	// region placed by the adapter (moduleSpec).
	adapter, profile, spec, err := moduleSpec(req)
	if err != nil {
		return err
	}
	regions := contractRegions(req, spec)
	vars := map[string]any{
		// Identity
		"sovereign_fqdn":      req.SovereignFQDN,
		"sovereign_subdomain": req.SovereignSubdomain,
		"org_name":            req.OrgName,
		"org_email":           req.OrgEmail,

		// Marketplace exposure toggle (issue #710). Stringified for the tofu
		// var (declared as type string with "true"/"false" validation) so
		// drone-envsubst can interpolate it directly into the Flux
		// Kustomization postBuild.substitute block at cloud-init render
		// time without quoting surprises.
		"marketplace_enabled": map[bool]string{true: "true", false: "false"}[req.MarketplaceEnabled],

		// #4053 console-isolation toggle (Refs #4431 #4212). Stringified
		// "true"/"false" for the same envsubst-passthrough reason as
		// marketplace_enabled. true (the default, resolved by
		// consoleIsolationEnabled when the field is omitted) → the IaC
		// console-ELB `count` gate provisions the dedicated console ELB + EIP
		// and the cloud-init SOVEREIGN_CONSOLE_GATEWAY substitute keeps the
		// console HTTPRoutes on cilium-gateway-console (#4053 isolation).
		// false → the console ELB stack is dropped (one fewer EIP) and the
		// console re-parents onto the shared cilium-gateway. The matching
		// infra/providers/{hetzner,huawei}/variables.tf carry the
		// `["true","false"]` validation so a typo fails at `tofu plan`.
		"console_isolation_enabled": map[bool]string{true: "true", false: "false"}[consoleIsolationEnabled(req)],

		// QA fixtures auto-enable (Fix #73 — qa-loop bounded-cycle iter-16).
		// Stringified for symmetric reasons as marketplace_enabled. When
		// QATestEnabled=true the bp-catalyst-platform qaFixtures stack
		// renders on the Sovereign so the qa-loop matrix executor finds
		// every fixture (qa-<label> ns + qa-wp Application + Continuum CR
		// + CNPGPair + PDM CRs + tier-bound UserAccess seeder).
		// Customer-facing Sovereigns provision with QATestEnabled=false
		// (default) → no fixture artifacts.
		"qa_fixtures_enabled":     map[bool]string{true: "true", false: "false"}[req.QATestEnabled],
		"qa_test_session_enabled": map[bool]string{true: "true", false: "false"}[req.QATestEnabled],

		// The variables document (contract C5), the module input
		// `variables`. Its HANDOVER_AUTO replaces fire_cutover_on_handover.
		"variables": doc,

		// Wildcard cert staging-LE selector (Fix #123 — qa-loop iter-1 LE
		// rate-limit unblock). When QATestEnabled=true the per-Sovereign
		// overlay sets WILDCARD_CERT_USE_STAGING=true → bp-catalyst-platform
		// 1.4.136+ renders the sovereign-wildcard-tls Certificate(s) with
		// `issuerRef.name: letsencrypt-dns01-staging-powerdns` instead of
		// the production issuer. Staging hits LE's separate ACME directory
		// with generous rate limits, so the wipe + re-provision cadence
		// of QA Sovereigns no longer trips production's 5-certs/168h
		// ceiling per registered domain. Customer Sovereigns
		// (QATestEnabled=false) provision real-trusted production certs.
		// Stringified for the same envsubst-passthrough reason as
		// qa_fixtures_enabled.
		"wildcard_cert_use_staging": map[bool]string{true: "true", false: "false"}[req.QATestEnabled],

		// BCP topology threading (Refs #2666 G93.1). Pre-G93.1 the
		// cloud-init Kustomization substitute SOVEREIGN_ENABLE_HOT_STANDBY
		// was hardcoded to "" on Hetzner and absent on Huawei → the
		// bp-catalyst-platform chart's slot-13
		// `${SOVEREIGN_ENABLE_HOT_STANDBY:-}` always evaluated to empty,
		// so the chart-side fallback `false` won on EVERY multi-region
		// Sovereign. The org_tenant_gitops writer reads the same env at
		// catalyst-api runtime to decide whether to inject
		// `pg.activeHotStandby` into every tenant bp-wordpress-tenant
		// HelmRelease — empty meant single-Cluster CNPG always, even on
		// a 2-region prov. Pillar 3 zero-tx-loss was silently impossible.
		//
		// Both topology values that imply a primary+replica CNPG pair
		// (active-hotstandby, active-active) flip `enable_hot_standby`
		// to "true"; single-region stays "false". The Hetzner +
		// Huawei cloud-init templates now interpolate this tofu var
		// into the Kustomization substitute map (G93.1 PR), replacing
		// the previously-hardcoded literal. See
		// infra/providers/{hetzner,huawei}/variables.tf for the matching
		// "true"/"false" validation block — a typo (e.g. "True") fails
		// at `tofu plan` rather than landing a wrong-by-default prov.
		//
		// The effective topology is computed by deriveBcpTopology, the
		// same helper Validate() calls — there is exactly one source of
		// truth for "is this an active-hotstandby Sovereign". Tests in
		// provisioner_bcp_topology_test.go pin every branch of the
		// derivation + the resulting tfvars emission.
		"enable_hot_standby": bcpTopologyEnableHotStandby(deriveBcpTopology(req)),
		// Mirror the canonical effective string into the tfvars so an
		// operator inspecting tofu.auto.tfvars.json on the catalyst-api
		// PVC can answer "what topology did this Sovereign provision
		// under?" without re-reading the deployment record. The Hetzner
		// + Huawei variables.tf declare a matching string variable
		// `bcp_topology`. The catalyst-api ledger + the Sovereign-side
		// status surfaces use this same value to render the BSS-menu DR
		// posture chip and the Settings page Continuity Plan row.
		"bcp_topology": deriveBcpTopology(req),

		// Shared-Postgres opt-in threading (ADR-0010 / #3188). Mirrors the
		// enable_hot_standby seam exactly: the boolean Request field maps
		// to a stringified "true"/"false" tofu var the cloud-init template
		// interpolates verbatim into the bootstrap-kit Kustomization
		// postBuild.substitute as SOVEREIGN_ENABLE_SHARED_PG. slot 16a
		// (16a-bp-postgres-shared.yaml) reads `${SOVEREIGN_ENABLE_SHARED_PG
		// :=false}` as its master gate — OFF (the safe default) renders an
		// empty-but-Ready release. Before this var NOTHING set the
		// substitute, so the chart fallback `false` always won and the
		// reuse model was dormant + unreachable even on a fresh prov.
		// infra/providers/{hetzner,huawei}/variables.tf carry the matching
		// `["true","false"]` validation so a typo fails at `tofu plan`.
		"enable_shared_pg": map[bool]string{true: "true", false: "false"}[req.EnableSharedPostgres],

		// Operator-chosen default StorageClass (#4057 — founder point #1:
		// "storage class is an INPUT chosen by the user, with defaults").
		// storageClass returns the operator's explicit class verbatim, or
		// the default class of the provider's cloud profile. On a cloud pack
		// NEVER local-path (which is FORBIDDEN by --disable=local-storage +
		// the K23 Kyverno ENFORCE deny). The Hetzner + Huawei cloud-init templates interpolate
		// this var into the bootstrap-kit Kustomization postBuild.substitute
		// `SOVEREIGN_CNPG_STORAGE_CLASS` — which previously HARDCODED the
		// per-provider literal — so the host-shared CNPG Cluster CRs (slots
		// 10/19/52) + the bp-cnpg / bp-mgmt-vcluster default class all name the
		// chosen class. infra/providers/{hetzner,huawei}/variables.tf declare
		// the matching string variable; a fresh prov that omits the wizard
		// field lands byte-identical to today (the per-provider default).
		"default_storage_class": defaultStorageClass,

		// QA namespace + Organization names — derived from the Sovereign
		// FQDN's first label at provision time per principle #4 (never
		// hardcode). The chart's defaults (qa-omantel / omantel-platform)
		// are correct ONLY for omantel.biz and would leak onto every other
		// QA Sovereign without this derivation. Operator may override
		// via Request.QAFixturesNamespace / QAOrganization for the rare
		// multi-tenant-per-Sovereign QA case.
		"qa_fixtures_namespace": deriveQAFixturesNamespace(req),
		"qa_organization":       deriveQAOrganization(req),

		// Cilium ClusterMesh per-Sovereign peer anchors (#1101 EPIC-6).
		// Empty + 0 = not in a mesh. Tofu validates id ∈ [0, 255].
		//
		// Auto-derivation for zero-touch multi-region provs: when the
		// operator omits ClusterMeshName/ClusterMeshID AND len(Regions)>1,
		// derive both deterministically so the mesh comes up by default.
		// Without this, every multi-region prov lands with cluster.id=0
		// and Cilium kvstoremesh refuses to start: "ClusterID 0 is
		// reserved". Operator may still override; auto-derive only kicks
		// in when both fields are zero/empty (the omit-from-POST default).
		// Caught on prov t104.omani.works (98395b3d9bd9c1aa, 2026-05-15):
		// all 3 regions had cluster.id=0, clustermesh-apiserver
		// CrashLoopBackOff 16 restarts, no inter-region mesh ever
		// formed.
		"cluster_mesh_name": deriveClusterMeshName(req),
		"cluster_mesh_id":   deriveClusterMeshID(req),

		// NET-011: who may reach the new Sovereign's API port 6443 besides
		// its own addresses (control_plane_sources.go). Both are lists and
		// never null; an empty list opens the port to nobody else.
		"admin_cidrs":       AdminCIDRs(req),
		"provisioner_cidrs": EgressCIDRs(),

		"region": req.Region,

		// Topology — singular fields drive today's solo apply path. The
		// per-region payload (regions, below) is structurally available
		// to the OpenTofu module's for_each iteration when the multi-
		// region wiring is activated; collapsing it back to single-SKU
		// here would break the architectural shape the wizard intends.
		//
		// IMPORTANT: control_plane_size / worker_size are conditionally
		// inserted below (after the literal map) when non-empty. An empty
		// string written to tofu.auto.tfvars.json OVERRIDES the variables.tf
		// default ("cpx21" / "cpx31") with "" — and "" fails the SKU regex
		// validator at plan time ("control_plane_size must match Hetzner
		// server-type naming"). Writing the keys only when set lets the
		// default-cost-optimized variables.tf defaults take effect for
		// zero-override request bodies.
		"worker_count": req.WorkerCount,
		"ha_enabled":   req.HAEnabled,

		// Per-region payload — `regions` in the shape of module contract
		// version 1, the same for every provider (contractRegions,
		// CLD-090). Index 0 is the primary region. The list is never
		// null and never empty: a request without Regions gives one
		// entry built from the singular fields above.
		"regions": regions,

		// SSH key — module creates an hcloud_ssh_key from this and attaches
		// to all servers. We never generate keys here; sovereign-admin
		// supplies the public half from their secrets manager.
		"ssh_public_key": req.SSHPublicKey,

		// Domain
		// The wizard exposes three modes (pool / byo-manual / byo-api) so
		// the StepDomain UX can branch the operator into the right
		// flow. The OpenTofu module only cares about the binary
		// pool/byo distinction (pool means "we own the parent zone via
		// Dynadot"; byo means "operator is bringing their own
		// domain"). Collapse byo-manual + byo-api → "byo" before
		// writing the tfvars. Caught live on omantel.biz 2026-05-07
		// — provisioning failed at `tofu plan` with
		// `Invalid value for variable: var.domain_mode is "byo-manual" — Domain mode must be 'pool' or 'byo'`.
		"domain_mode":    mapDomainModeForTofu(req.SovereignDomainMode),
		"pool_domain":    req.SovereignPoolDomain,
		"dynadot_key":    req.DynadotAPIKey, // empty when domain_mode != "pool"
		"dynadot_secret": req.DynadotAPISecret,

		// Multi-domain payload (issue #826) — emitted as a list of
		// objects so the OpenTofu module can iterate per-domain.
		// At Day-1 the slice has length 1 (the primary entry). The
		// OpenTofu module's `variable "parent_domains"` declaration
		// (when added in a follow-up) consumes this shape directly;
		// today the field is structural-correct + harmless if the
		// module's variables.tf has not yet declared it (OpenTofu
		// ignores unknown variables in tofu.auto.tfvars.json with a
		// warning, not an error). Emitting it now lets the Day-2
		// add-domain flow (#829) ship the same shape for every
		// re-render.
		//
		// IMPORTANT: emit empty slice [] (not nil) to mirror the
		// regions field's failure mode — `for r in var.parent_domains`
		// in a future module would fail on null.
		"parent_domains": coalesceParentDomains(req.ParentDomains),

		// Multi-domain YAML literal (issue #1772 — D30b org-pool listener
		// fix). infra/hetzner/variables.tf declares `variable
		// "parent_domains_yaml"` (type=string, default="") and
		// infra/hetzner/main.tf locals.parent_domains_decoded calls
		// `yamldecode()` on its value to materialise the per-zone
		// Cilium Gateway listeners (one HTTPS/HTTP pair per parent
		// zone, hostnamed `*.<zone>`). When this tfvar is empty the
		// module falls back to a single-entry list derived from
		// sovereign_fqdn — which silently DROPS every org-pool zone
		// the operator added.
		//
		// Symptom on t22 (Wave 32 D27-D31 verifier): tfvars.parent_domains
		// listed both `{omantel.biz, primary}` + `{omani.homes,
		// org-pool}` (this Go-side field) but the live Gateway
		// advertised only `*.t22.omantel.biz` — the listener for
		// `*.omani.homes` never rendered because the terraform local
		// never saw the second zone. tenants on omani.homes hit the
		// envoy default fallback cert and could not get TLS.
		//
		// Render parent_domains_yaml as a JSON-flow array literal
		// (`[{name: "x", role: "y"}, ...]`). JSON-flow is a YAML
		// superset — yamldecode() in the module accepts it natively,
		// and json.Marshal produces output that's safe inside
		// tofu.auto.tfvars.json without quoting hell. Empty
		// ParentDomains → emit "" so the module's single-zone
		// fallback (derived from sovereign_fqdn) kicks in cleanly.
		"parent_domains_yaml": parentDomainsYAMLLiteral(req.ParentDomains),

		// Contabo PowerDNS API key — interpolated by
		// cloudinit-control-plane.tftpl into the Sovereign's
		// `cert-manager/powerdns-api-credentials` Secret so
		// bp-cert-manager-powerdns-webhook can write DNS-01 challenge
		// TXT records to contabo's authoritative omani.works zone.
		// PR #681 followup. Empty here = wildcard cert never issues
		// (caught live on otech47).
		"powerdns_api_key": req.PowerDNSAPIKey,

		// GitOps source — Flux on the new cluster watches this for
		// clusters/<sovereign-fqdn>/. Defaults to the public OpenOva monorepo;
		// override for air-gap (operator-mirrored Gitea).
		"gitops_repo_url": env("CATALYST_GITOPS_REPO_URL", "https://github.com/openova-io/openova"),
		"gitops_branch":   gitopsBranch,

		// GHCR pull token — interpolated into the cloud-init template so
		// the new Sovereign's Flux source-controller can pull private
		// bp-* OCI artifacts from `ghcr.io/openova-io/`. Marked sensitive
		// in variables.tf; OpenTofu never logs the value to stdout. The
		// tofu.auto.tfvars.json file containing it is 0o600 on the
		// catalyst-api Pod's local FS and is wiped when the deployment
		// directory is purged. Per docs/SECRET-ROTATION.md the token
		// rotates yearly and is stored in 1Password — never in git.
		"ghcr_pull_token": req.GHCRPullToken,

		// Harbor proxy-cache robot token (issue #557). Stamped server-
		// side. cloudinit-control-plane.tftpl writes it into
		// /etc/rancher/k3s/registries.yaml so containerd authenticates
		// against harbor.openova.io's proxy projects. Empty falls
		// through to anonymous Harbor pulls.
		"harbor_robot_token": req.HarborRobotToken,

		// PDM basic-auth credentials (issue #879 Bug 2). Stamped server-
		// side. cloudinit-control-plane.tftpl writes them into the
		// new Sovereign's flux-system/pdm-basicauth Secret so its
		// catalyst-api can call PDM via Authorization: Basic ….
		// Empty falls through to a Secret with empty values; the
		// Sovereign's pdmFlipNS skips SetBasicAuth and degrades to
		// PDM 401 with a clear log line, matching the harbor-robot-
		// token degradation posture.
		"pdm_basic_auth_user": req.PDMBasicAuthUser,
		"pdm_basic_auth_pass": req.PDMBasicAuthPass,

		// Cloud-init kubeconfig postback (issue #183, Option D). The
		// catalyst-api stamps deployment_id + kubeconfig_bearer_token
		// onto the Request before writeTfvars is called: deployment_id
		// keys the URL path /api/v1/deployments/<id>/kubeconfig the
		// new Sovereign PUTs to; kubeconfig_bearer_token is the
		// 32-byte random secret the new Sovereign attaches in the
		// Authorization header. The catalyst-api persists ONLY the
		// SHA-256 hash on the on-disk record; the plaintext lives in
		// the per-deployment OpenTofu workdir (encrypted PVC) until
		// `tofu destroy` removes it.
		//
		// catalyst_api_url is the public URL the new Sovereign PUTs
		// back to — runtime variable per docs/INVIOLABLE-PRINCIPLES.md
		// #4 so air-gapped franchises override without code changes.
		"deployment_id":           req.DeploymentID,
		"kubeconfig_bearer_token": req.KubeconfigBearerToken,
		// OpenovaFlow integration (Agent #3, PR #1389/#1390 follow-up).
		// Same value as deployment_id, distinct variable name so the
		// cloud-init template's postBuild.substitute (SOVEREIGN_DEPLOYMENT_ID)
		// reads from a semantically named knob. bp-openova-flow-emitter
		// uses this as the FlowID so the openova-flow-server keys all
		// FlowNodes (one per HelmRelease per region) under the same id
		// the catalyst-api proxy /api/v1/flows/{deploymentId}/* queries.
		"sovereign_deployment_id": req.DeploymentID,
		"catalyst_api_url":        apiURL,

		// Phase-8b handover JWK (issue #605). Stamped server-side from
		// h.handoverSigner.PublicJWK() in CreateDeployment. cloud-init
		// renders it into /var/lib/catalyst/handover-jwt-public.jwk on
		// the new Sovereign so Agent-C verifies the one-time handover
		// JWT without a cross-cluster RPC. variables.tf default "" keeps
		// the var optional — if the signer is unavailable the file lands
		// empty and the handover flow is simply not yet wired on that
		// Sovereign (caught at /auth/handover, not at provision time).
		"handover_jwt_public_key": req.HandoverJWTPublicKey,

		// ── Hetzner Object Storage (issue #371) ─────────────────────────
		// Per-Sovereign S3 backing for Harbor + Velero. variables.tf in
		// infra/hetzner/ declares all four keys; main.tf creates the bucket
		// idempotently via the aminueza/minio provider; cloudinit-control-
		// plane.tftpl writes the credentials into a flux-system Secret on
		// the new Sovereign so Phase 1 (Flux reconciling bp-harbor +
		// bp-velero) finds them already present.
		//
		// Persistence boundary: the tofu.auto.tfvars.json file containing
		// these values is mode 0600 on the catalyst-api Pod's encrypted
		// PVC and is wiped by the Destroy() flow on `tofu destroy`. The
		// in-cluster K8s Secret on the new Sovereign is the only durable
		// destination. The credentials NEVER live in the public openova
		// monorepo — that would violate docs/INVIOLABLE-PRINCIPLES.md #10.
		"object_storage_region":      req.ObjectStorageRegion,
		"object_storage_access_key":  req.ObjectStorageAccessKey,
		"object_storage_secret_key":  req.ObjectStorageSecretKey,
		"object_storage_bucket_name": req.ObjectStorageBucket,
	}

	// Conditionally include singular SKU fields. variables.tf in
	// infra/hetzner/ declares "cpx21" / "cpx31" defaults for the
	// cost-optimized 1× CP + 2× worker topology; writing an empty
	// string here would override the default with "" and fail the
	// SKU regex validator at `tofu plan`. Only emit when set.
	if strings.TrimSpace(req.ControlPlaneSize) != "" {
		vars["control_plane_size"] = req.ControlPlaneSize
	}
	if strings.TrimSpace(req.WorkerSize) != "" {
		vars["worker_size"] = req.WorkerSize
	}

	// Provider — emit the normalised lower-case name so downstream
	// terraform (and any future fan-out logic) can branch on
	// `var.provider`. Validate() guarantees the value is one of the
	// known names; an empty string is impossible here.
	vars["provider"] = req.Provider

	// Wave 5.139 — initial value for the retry-loop salt; the catalyst-
	// api Provision retry loop (provisioner.go:1220) bumps this between
	// attempts via bumpRetryAttempt(). Tofu uses it as one input to the
	// worker-name hash (infra/providers/huawei/main.tf:742) so unhealthy
	// workers get a fresh name on each retry and HCS scheduler picks a
	// fresh cell, dodging the bad-cell affinity that returned
	// Common.0021 on the prior attempt.
	vars["retry_attempt"] = 0

	// The adapter's own variables (class 3 of module contract version 1):
	// the engine writes no variable that belongs to one cloud.
	own, err := adapter.TfVars(spec, profile)
	if err != nil {
		return fmt.Errorf("%s variables: %w", req.Provider, err)
	}
	if err := mergeAdapterVariables(vars, own); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(deployDir, "tofu.auto.tfvars.json"), raw, 0o600)
}

// moduleK3sVersion is the default of variable k3s_version in both provider
// modules. writeTfvars does not set k3s_version, so it is what a new
// Sovereign runs. TestModuleK3sVersionIsTheModuleDefault fails when they differ.
const moduleK3sVersion = "v1.31.14+k3s1"

// legacyOwnVariables returns the name and the variables document of a parent
// without a ConfigMap flux-system/sovereign-variables of its own (every
// mothership installed before contract C5). The name is the host of apiURL
// without a leading "console.". The registry values are those its children
// use today: charts from ghcr.io under openova-io, images through the proxy
// projects below. Work package SOV-940 deletes this function.
func legacyOwnVariables(apiURL string) (string, upstream.Variables, error) {
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || u.Hostname() == "" {
		return "", upstream.Variables{}, fmt.Errorf("variables document: the callback address %q has no host", apiURL)
	}
	parentFQDN := strings.TrimPrefix(strings.ToLower(u.Hostname()), "console.")
	own := upstream.Variables{
		Mode:              "mothership",
		LocalRegistryHost: "ghcr.io",
		PathCharts:        "openova-io",
		PathRelease:       "openova-io/release",
		PathProviderPacks: "openova-io/provider-packs",
		PathXpkg:          "proxy-xpkg",
		PathTofuProviders: "tofu-providers",
		PathNodeArtifacts: "node-artifacts",
		ProxyProjects: map[string]string{
			"docker.io":            "proxy-dockerhub",
			"registry-1.docker.io": "proxy-dockerhub",
			"quay.io":              "proxy-quay",
			"gcr.io":               "proxy-gcr",
			"registry.k8s.io":      "proxy-k8s",
			"ghcr.io":              "proxy-ghcr",
			// SOV-135: the two names below have an empty path, so a registry
			// serves them at its root. The kit names the images of the
			// platform under the first. No registry of the parent serves a
			// proxy project for the second (legacyOriginByName).
			"harbor.openova.io": "",
			"xpkg.upbound.io":   "",
		},
		ReleaseChannel: "stable",
	}
	return parentFQDN, own, nil
}

// legacyOriginRegistry is the address of the registry of the parent that
// legacyOwnVariables describes. A new Sovereign has it as the origin address
// of every proxy name, unless OriginRegistryByProvider or legacyOriginByName
// names another. Work package SOV-940 deletes it.
const legacyOriginRegistry = "https://harbor.openova.io"

// legacyOriginByName holds the proxy names that no registry of the parent
// serves. Each has its own public registry as its origin address, whatever
// the provider. Work package SOV-940 deletes it.
var legacyOriginByName = map[string]string{
	"xpkg.upbound.io": "https://xpkg.upbound.io",
}

// OriginRegistryByProvider holds, per provider name, the address at which a
// new Sovereign of that provider reaches the registry of the parent, where
// that is not legacyOriginRegistry. A provider adapter adds its address from
// init(). Work package SOV-940 deletes it.
var OriginRegistryByProvider = map[string]string{}

// childVariables returns the variables document (contract C5) of a new
// Sovereign: what the parent serves to a child (upstream.Served), with the
// keys of this child filled in. Every proxy name gets its origin address in
// ORIGIN_PROXY_ENDPOINTS_JSON (SOV-135). It fails when the document breaks
// the contract.
func childVariables(req Request, apiURL string) (map[string]string, error) {
	parentFQDN, own, err := legacyOwnVariables(apiURL)
	if err != nil {
		return nil, err
	}
	v := upstream.Served(parentFQDN, own)
	v.ParentAPIURL = strings.TrimSpace(apiURL)
	v.HandoverAuto = req.FireCutoverOnHandover == nil || *req.FireCutoverOnHandover
	v.LocalRegistryHost = "registry." + strings.ToLower(strings.TrimSpace(req.SovereignFQDN))
	v.K3sVersion = moduleK3sVersion
	if tz := strings.TrimSpace(req.SovereignTimeZone); tz != "" {
		v.TimeZone = tz
	}
	req.MailRelay.apply(&v)
	v.CloudCredentialMode = "supplied"
	originRegistry := legacyOriginRegistry
	if address := OriginRegistryByProvider[strings.ToLower(strings.TrimSpace(req.Provider))]; address != "" {
		originRegistry = address
	}
	for name := range v.ProxyProjects {
		v.OriginProxyEndpoints[name] = originRegistry
		if address, ok := legacyOriginByName[name]; ok {
			v.OriginProxyEndpoints[name] = address
		}
	}
	doc := v.ToMap()
	if _, err := upstream.FromMap(doc); err != nil {
		return nil, fmt.Errorf("variables document: %w", err)
	}
	if errs := v.Validate(req.SovereignFQDN); len(errs) > 0 {
		return nil, fmt.Errorf("variables document: %w", errors.Join(errs...))
	}
	return doc, nil
}

// addVariablesIfMissing brings the module input `variables` of a kept
// tofu.auto.tfvars.json to exactly the keys of contract C5, the only key set
// the provider modules accept. A missing key gets the value of a new document,
// built for req with the file's own catalyst_api_url. A key the contract no
// longer has is dropped. Held values stay. A file without `variables` (one
// written before SOV-131) gets the whole new document. A file whose document
// already has exactly the contract keys is not written.
func addVariablesIfMissing(path string, req Request) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		return fmt.Errorf("decode tfvars: %w", err)
	}
	kept, isObject := vars["variables"].(map[string]any)
	if !isObject && vars["variables"] != nil {
		return fmt.Errorf("decode tfvars: variables is not a JSON object")
	}
	doc := map[string]any{}
	var fresh map[string]string
	for key := range (upstream.Variables{}).ToMap() {
		if value, ok := kept[key]; ok {
			doc[key] = value
			continue
		}
		if fresh == nil {
			apiURL, _ := vars["catalyst_api_url"].(string)
			if fresh, err = childVariables(req, apiURL); err != nil {
				return err
			}
		}
		doc[key] = fresh[key]
	}
	if fresh == nil && len(kept) == len(doc) {
		return nil
	}
	vars["variables"] = doc
	out, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// classifyApplyError decides on one failed `tofu apply`: the adapter's
// providers.ApplyErrorClassifier, and OpenTofu's own read-after-create
// inconsistency (#3142), which a re-plan reconciles on every cloud.
func classifyApplyError(provider, stderr string) providers.ApplyErrorClass {
	var class providers.ApplyErrorClass
	if c, ok, _ := providers.As[providers.ApplyErrorClassifier](provider); ok {
		class = c.ClassifyApplyError(stderr)
	}
	if strings.Contains(stderr, "Provider produced inconsistent result after apply") {
		class.Retryable = true
	}
	if class.Retryable && class.Reason == "" {
		class.Reason = "transient apply failure"
	}
	return class
}

// applyFailures counts, per resource address, the consecutive retryable
// `tofu apply` failures that named it (Wave 5.145).
type applyFailures map[string]int

// record counts the addresses of one retryable failure and forgets every
// address the failure does not name, so a transient failure does not count
// for ever. It returns the addresses that have now failed three consecutive
// times when the failure's class stops on a repeat.
func (f applyFailures) record(class providers.ApplyErrorClass) []string {
	named := map[string]bool{}
	var stuck []string
	for _, a := range class.Addresses {
		named[a] = true
		f[a]++
		if f[a] >= 3 && class.StopOnRepeat {
			stuck = append(stuck, a)
		}
	}
	for a := range f {
		if !named[a] {
			delete(f, a)
		}
	}
	return stuck
}

// capabilitySpec is the spec the engine hands an adapter's capabilities: the
// request's, without a credential that a stored record holds as
// RedactedMarker (as Request.Credential reads it).
func capabilitySpec(req Request) providers.ProvisionSpec {
	spec := req.providerSpec()
	for k, v := range spec.Creds.Raw {
		if v == RedactedMarker {
			delete(spec.Creds.Raw, k)
		}
	}
	return spec
}

// bumpRetryAttempt rewrites the on-disk tofu.auto.tfvars.json with an
// incremented `retry_attempt` value. Called by Provision's retry loop
// between attempts. Wave 5.139.
func bumpRetryAttempt(deployDir string, newAttempt int) error {
	path := filepath.Join(deployDir, "tofu.auto.tfvars.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		return fmt.Errorf("decode tfvars for retry bump: %w", err)
	}
	vars["retry_attempt"] = newAttempt
	out, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// stageModule copies the canonical module's *.tf files into the per-deployment
// workdir. We copy rather than symlink so each deployment can have its own
// .terraform/ + state, and so OpenTofu's working-directory model works as
// expected.
func stageModule(src, dst string) error {
	if err := copyTfFiles(src, dst); err != nil {
		return err
	}
	// #3145: the cloud-agnostic cloud-init lives in infra/providers/_shared/
	// and each provider module references it via
	// templatefile("${path.module}/../_shared/cloudinit-control-plane.tftpl").
	// In-repo that resolves (sibling dir), but the per-deployment tofu workdir
	// stages ONLY the provider module, so `../_shared/` would be missing at
	// apply time (caught live on hw119: "no file exists at ./../_shared/..."").
	// Stage _shared as a sibling of the workdir so the cross-module reference
	// resolves identically in-repo and at prov time.
	sharedSrc := filepath.Join(filepath.Dir(src), "_shared")
	if fi, err := os.Stat(sharedSrc); err == nil && fi.IsDir() {
		sharedDst := filepath.Join(filepath.Dir(dst), "_shared")
		if err := os.MkdirAll(sharedDst, 0o700); err != nil {
			return err
		}
		if err := copyTfFiles(sharedSrc, sharedDst); err != nil {
			return err
		}
	}
	// SEC-150: the seal hook of a provider pack is the child module
	// <module>/seal, and the pack's main.tf calls it with source "./seal"
	// (contract C8, docs/contracts/c8-seal-provider.md). copyTfFiles does not
	// descend into directories, so stage that one directory beside the
	// module's own files. Without it `tofu init` fails in the workdir with an
	// unreadable module directory, although it passes in the repository.
	sealSrc := filepath.Join(src, "seal")
	if fi, err := os.Stat(sealSrc); err == nil && fi.IsDir() {
		sealDst := filepath.Join(dst, "seal")
		if err := os.MkdirAll(sealDst, 0o700); err != nil {
			return err
		}
		if err := copyTfFiles(sealSrc, sealDst); err != nil {
			return err
		}
	}
	return nil
}

// copyTfFiles copies the .tf/.tftpl files from src into dst (non-recursive),
// skipping unchanged files. Shared by the provider-module + _shared staging
// (#3145) so both go through one code path.
func copyTfFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") && !strings.HasSuffix(e.Name(), ".tftpl") {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		// Skip if already there and unchanged — re-runs of the same wizard
		// flow shouldn't re-copy module files.
		srcInfo, _ := os.Stat(from)
		dstInfo, _ := os.Stat(to)
		if dstInfo != nil && dstInfo.ModTime() == srcInfo.ModTime() && dstInfo.Size() == srcInfo.Size() {
			continue
		}
		raw, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// workdirKey returns the per-deployment tofu workdir name. Keyed by
// DeploymentID (not FQDN) so concurrent or sequential reprovisions of the
// SAME SovereignFQDN never share state — each POST /deployments gets a
// unique workdir because CreateDeployment generates a fresh DeploymentID
// on every call (deployments.go:CreateDeployment).
//
// History: this was originally keyed by `strings.ReplaceAll(FQDN, ".", "-")`
// so wizard-resume on the same FQDN would re-enter the same workdir and
// idempotently `tofu apply` on existing state. The downside surfaced on
// prov #82 (omani.works, 2026-05-14): a force-wipe whose `tofu destroy`
// failed (because of a stale-tftpl bug in the workdir) left tfstate
// referencing destroyed-via-Hetzner-purge cloud resources. The NEXT
// reprov of the same FQDN inherited the dirty tfstate and `tofu apply`
// failed with "Saved plan is stale" / "resource already exists". By
// keying on DeploymentID every reprov is hermetic; wizard-resume can
// re-use the same DeploymentID via an explicit retry endpoint instead.
//
// Tests-load-bearing: the workdir name is referenced from wipe.go and
// handover.go via dep.ID directly (no shared helper). The on-disk
// kubeconfig naming was ALREADY keyed by DeploymentID, so this brings
// the tofu workdir into alignment.
func (r Request) workdirKey() string {
	if r.DeploymentID != "" {
		return r.DeploymentID
	}
	// Fallback only used by the legacy Destroy code-path that was called
	// without a DeploymentID set on Request. Modern paths always set it
	// in CreateDeployment before invoking Provision/Destroy.
	return strings.ReplaceAll(r.SovereignFQDN, ".", "-")
}

// sovereignName is the legacy name retained for documentation references
// in handler/wipe.go and hetzner/purge.go comments. New callers use
// workdirKey() above.
func (r Request) sovereignName() string {
	return r.workdirKey()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ParentDomainStep is one named operation in the per-domain
// provisioning pipeline. Day-1 (wizard signup) runs every registered
// step against the single primary domain; Day-2 (admin add-domain
// from issue #829) runs the same steps against each newly-added
// org-pool domain.
//
// Today the steps are:
//
//  1. registrar-flip — point the registered parent zone at the
//     Sovereign's PowerDNS by writing ns1/ns2/ns3 NS records (and a
//     glue A record for ns1) at the registrar's API.
//  2. powerdns-zone-create — POST /api/v1/servers/localhost/zones to
//     the Sovereign's PowerDNS so it serves the zone authoritatively.
//  3. cert-manager-cert — apply a Certificate CR for *.<domain>
//     against the Sovereign so cert-manager issues a wildcard.
//
// At Day-1 these are wired structurally inside the OpenTofu cloud-
// init template (the catalyst-api's role is to write tofu.auto.tfvars.
// json with the parent-domains list; the cloud-init renders the
// per-domain Job manifests). At Day-2 (#829) the catalyst-api
// implements them in-process — running each step against a freshly-
// added domain post-handover. Same interface, same semantics; only
// the executor differs.
//
// The interface lives here so #829's add-domain handler imports
// THIS package's contract — not a parallel one. Issue #826's DoD
// calls out "reusable per-domain function/interface ready for #829";
// this is that contract.
type ParentDomainStep interface {
	// Name is a stable identifier emitted in SSE events
	// (event.phase = "parent-domain:" + Name). Examples:
	// "registrar-flip", "powerdns-zone-create", "cert-manager-cert".
	Name() string

	// Apply executes the step against pd. Implementations MUST be
	// idempotent: re-running with the same (pd, lbIP) is a no-op
	// when the desired state is already in place. Idempotency is the
	// load-bearing property — the wipe + restart loop documented in
	// the Catalyst-Zero memory ("FAILURE = WIPE + RESTART FROM
	// ZERO") relies on every per-domain step being safe to re-run.
	//
	// The lbIP is the Sovereign's load-balancer IPv4, returned by
	// the Phase-0 OpenTofu apply. Day-1 has it on the Result struct;
	// Day-2 reads it from the persisted Record. Threading it through
	// the interface keeps the steps independent of how the IP got
	// computed.
	Apply(ctx context.Context, pd ParentDomain, lbIP string) error
}

// ProvisionParentDomain runs the full per-domain pipeline (every
// supplied ParentDomainStep, in order) against pd. Used by:
//
//   - Day-1 (wizard signup): the catalyst-api's runProvisioning
//     iterates Request.ParentDomains and calls this once per entry
//     (the slice is length 1 today). The OpenTofu module handles the
//     same fan-out structurally inside cloud-init; this Go-side path
//     exists for any domain step the catalyst-api owns directly
//     (e.g. a future "verify NS propagation" probe that runs
//     post-handover).
//   - Day-2 (#829 admin add-domain): the catalyst-api's
//     /api/v1/sovereign/parent-domains POST handler iterates the
//     freshly-appended entries and calls this for each. Same steps
//     as Day-1 — the abstraction is the entire point of #826.
//
// Per-step events are emitted via the supplied emit function so the
// SSE stream surfaces "parent-domain:registrar-flip start
// omani.works" → "parent-domain:registrar-flip ok omani.works" etc.
// The event shape supports per-domain emission so #829's add-domain
// flow surfaces progress per added domain.
//
// Stops on the first step error; subsequent steps are skipped
// (failing partway through is the operator's signal to wipe and
// restart per the wipe-and-rerun rule in the Catalyst-Zero memory).
// The error wraps the step name + domain name so the operator's log
// readout names the failure unambiguously.
func ProvisionParentDomain(ctx context.Context, pd ParentDomain, lbIP string, steps []ParentDomainStep, emit func(phase, level, msg string)) error {
	if emit == nil {
		emit = func(string, string, string) {}
	}
	for _, step := range steps {
		phase := "parent-domain:" + step.Name()
		emit(phase, "info", fmt.Sprintf("%s start %s", step.Name(), pd.Name))
		if err := step.Apply(ctx, pd, lbIP); err != nil {
			emit(phase, "error", fmt.Sprintf("%s failed for %s: %s", step.Name(), pd.Name, err.Error()))
			return fmt.Errorf("parent-domain step %q for %q: %w", step.Name(), pd.Name, err)
		}
		emit(phase, "info", fmt.Sprintf("%s ok %s", step.Name(), pd.Name))
	}
	return nil
}

// ProvisionParentDomains is the slice-flavoured ProvisionParentDomain.
// Iterates pds applying every step against each entry, in order.
// Day-1 (length 1) and Day-2 (length 1+) take the same path — the
// Day-1 caller passes Request.ParentDomains, the Day-2 caller passes
// a single-element slice with the freshly-added entry.
//
// Stops on first per-domain failure (the per-domain ProvisionParentDomain
// call). The caller (catalyst-api handler) decides whether the whole
// deployment should fail or whether to mark the failed entry and
// continue — neither policy lives here.
func ProvisionParentDomains(ctx context.Context, pds []ParentDomain, lbIP string, steps []ParentDomainStep, emit func(phase, level, msg string)) error {
	for i := range pds {
		if err := ProvisionParentDomain(ctx, pds[i], lbIP, steps, emit); err != nil {
			return err
		}
	}
	return nil
}

// validateParentDomains enforces the parent-domain pool invariants
// expressed by the multi-domain epic (#825):
//
//   - every entry's Name is a syntactically valid FQDN ≥ 2 labels
//     (parentDomainNamePattern, mirroring the wizard's isValidDomain
//     helper)
//   - every entry's Role is one of the two known roles
//     (ParentDomainRolePrimary | ParentDomainRoleOrgPool)
//   - exactly ONE entry carries the primary role — neither zero nor
//     two-or-more is acceptable
//   - duplicate Names within the slice are rejected (an operator
//     adding the same domain twice via the admin console is a wizard
//     bug; surfacing the error from Validate() lets #829's handler
//     reject with a clear 400 instead of a half-applied NS-flip)
//
// Pure function — no side effects, called by Request.Validate after
// the migration synthesis path runs so it sees the canonical array
// shape on every request.
// validateParentDomains uses a *[]ParentDomain so it can normalise
// each entry's Name in place (lowercase + trim) — the on-disk record
// then carries the canonical form, and downstream consumers (the
// registrar adapter, the CRD projection, the Organization-signup pool
// dropdown) all see the same string. Mutating callers' slice
// through a pointer mirrors how Validate() already mutates the
// per-region singular fields on the same Request.
func validateParentDomains(pds *[]ParentDomain) error {
	if pds == nil || len(*pds) == 0 {
		return errors.New("parent domains list is empty (migration path failed to synthesise an entry — check SovereignFQDN / SovereignPoolDomain)")
	}
	primaryCount := 0
	seen := make(map[string]struct{}, len(*pds))
	for i := range *pds {
		pd := &(*pds)[i]
		name := strings.ToLower(strings.TrimSpace(pd.Name))
		if name == "" {
			return fmt.Errorf("parent domain[%d] name is required", i)
		}
		if !parentDomainNamePattern.MatchString(name) {
			return fmt.Errorf("parent domain[%d] name %q is not a valid FQDN (must be lowercase, ≥ 2 labels, RFC 1035)", i, pd.Name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("parent domain[%d] name %q is a duplicate of an earlier entry", i, pd.Name)
		}
		seen[name] = struct{}{}
		// Normalise in-place so downstream consumers see the
		// canonical lowercase form.
		pd.Name = name

		switch pd.Role {
		case ParentDomainRolePrimary:
			primaryCount++
		case ParentDomainRoleOrgPool:
			// OK — zero-or-more allowed.
		case "":
			return fmt.Errorf("parent domain[%d] role is required (one of %q | %q)", i, ParentDomainRolePrimary, ParentDomainRoleOrgPool)
		default:
			return fmt.Errorf("parent domain[%d] role %q is not recognised (one of %q | %q)", i, pd.Role, ParentDomainRolePrimary, ParentDomainRoleOrgPool)
		}
	}
	if primaryCount != 1 {
		return fmt.Errorf("parent domains must contain exactly one entry with role=%q (found %d)", ParentDomainRolePrimary, primaryCount)
	}
	return nil
}

// PrimaryParentDomain returns the single entry in r.ParentDomains
// carrying ParentDomainRolePrimary, or nil if the slice has not yet
// been validated/synthesised. Callers use this to read the primary
// domain name when threading it through the OpenTofu module without
// duplicating the "find the primary" loop at every call site. After
// Validate() has run successfully, this is guaranteed non-nil.
func (r Request) PrimaryParentDomain() *ParentDomain {
	for i := range r.ParentDomains {
		if r.ParentDomains[i].Role == ParentDomainRolePrimary {
			return &r.ParentDomains[i]
		}
	}
	return nil
}

// OrgPoolParentDomains returns every entry in r.ParentDomains carrying
// ParentDomainRoleOrgPool. Day-1 always returns an empty slice; Day-2
// add-domain (issue #829) populates the result.
//
// The order matches r.ParentDomains so callers iterating with the
// catalyst-api's "add-order" semantics (oldest first) get a stable
// list — useful for the admin console's "added 3 days ago" rendering
// + for the Organization wizard's parent-domain dropdown choosing the most
// recently added pool domain when nothing is selected.
func (r Request) OrgPoolParentDomains() []ParentDomain {
	out := make([]ParentDomain, 0, len(r.ParentDomains))
	for _, pd := range r.ParentDomains {
		if pd.Role == ParentDomainRoleOrgPool {
			out = append(out, pd)
		}
	}
	return out
}

// TenantParentDomain returns the single org-pool TLD new Organizations
// default to for their per-Org console + app subdomains
// (console.<slug>.<pool-tld>, <app>.<slug>.<pool-tld>) — the FIRST entry in
// r.ParentDomains carrying ParentDomainRoleOrgPool, lower-cased + trimmed, or
// "" when the Sovereign brought no org-pool zone (single-zone / BYO).
//
// This is the Go mirror of the cloud-init TENANT_PARENT_DOMAIN substitute
// (infra/providers/_shared/cloudinit-control-plane.tftpl, bootstrap-kit
// block), which derives the same "first org-pool zone" from
// var.parent_domains_yaml. bootstrap-kit slot-13 threads that substitute into
// bp-catalyst-platform's orgServices.provisioning.tenantParentDomain, and the
// per-Org provisioning consumer stamps spec.tenantPublic ONLY when it is set —
// so the value MUST come from the same ParentDomains source that feeds the
// parentZones substitute (and therefore bp-keycloak #6504's
// console.*.<zone>/* catalyst-ui callbacks), or the two diverge and a
// customer login at console.<slug>.<pool-tld> 400s Invalid parameter:
// redirect_uri against the shared sovereign realm (#3374 #3988, live hw301).
// Kept in lockstep with that template; both derive from ParentDomains by
// construction.
func (r Request) TenantParentDomain() string {
	for _, pd := range r.ParentDomains {
		if pd.Role == ParentDomainRoleOrgPool {
			return strings.ToLower(strings.TrimSpace(pd.Name))
		}
	}
	return ""
}

// defaultRegistrarKindFromEnv returns the registrar adapter id used
// when synthesising a Day-1 ParentDomain entry from the legacy
// single-FQDN payload. Reads CATALYST_DEFAULT_REGISTRAR_KIND with
// fallback to defaultRegistrarKind ("dynadot"). Inviolable Principle
// #4: never hardcode the registrar kind in a code path; let
// operators on registrars other than Dynadot override via env.
func defaultRegistrarKindFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_DEFAULT_REGISTRAR_KIND")); v != "" {
		return strings.ToLower(v)
	}
	return defaultRegistrarKind
}

// regionCode is the region code of rs in module contract version 1: Code, or
// the value of CloudRegion when Code is empty.
func regionCode(rs RegionSpec) string {
	if strings.TrimSpace(rs.Code) != "" {
		return rs.Code
	}
	return rs.CloudRegion
}

// cloudRegionVariable is the adapter variable that carried the cloud region
// of a Sovereign whose cloud names its region once, before module contract
// version 1. The module reads regions[].cloud_region now; that adapter still
// returns the variable from TfVars, because the wipe and the janitor read it
// from the stored variables file. The engine only reads it from a kept file
// (addCloudRegionIfMissing).
const cloudRegionVariable = "huawei_region"

// contractRegions returns the module input `regions` in the shape of module
// contract version 1 (infra/providers/_contract/contract.json), the one shape
// every provider module declares (CLD-090). Each entry has:
//
//   - code: the entry's code in spec (regionCode of the entry by default);
//   - cloud_region: the cloud's own region identifier in spec;
//   - availability_zone: the entry's zone in spec;
//   - role: "primary" for index 0 and "secondary" for every other entry. A
//     role in the request is not read;
//   - control_plane_size, worker_size and worker_count as the request has them.
//
// A request without Regions (the legacy singular fields) gives one entry built
// from Region, ControlPlaneSize, WorkerSize and WorkerCount. The result is
// never nil and never empty: the contract refuses an empty list, and a nil
// slice would be written as JSON null.
//
// The place of each entry (code, cloud_region, availability_zone) is the one
// the adapter's Normalize gave spec (moduleSpec): a cloud that names its
// region once for the whole Sovereign sets it there. The sizes and the count
// are the request's, which Validate set from the same Normalize.
func contractRegions(req Request, spec providers.ProvisionSpec) []map[string]any {
	regions := req.Regions
	if len(regions) == 0 {
		regions = []RegionSpec{{
			CloudRegion:      req.Region,
			ControlPlaneSize: req.ControlPlaneSize,
			WorkerSize:       req.WorkerSize,
			WorkerCount:      req.WorkerCount,
		}}
	}
	out := make([]map[string]any, 0, len(regions))
	for i, r := range regions {
		role := "secondary"
		if i == 0 {
			role = "primary"
		}
		place := providers.RegionSpec{Code: regionCode(r), CloudRegion: r.CloudRegion, Zone: r.Zone}
		if i < len(spec.Regions) {
			place = spec.Regions[i]
		}
		out = append(out, map[string]any{
			"code":               place.Code,
			"cloud_region":       place.CloudRegion,
			"availability_zone":  place.Zone,
			"role":               role,
			"control_plane_size": r.ControlPlaneSize,
			"worker_size":        r.WorkerSize,
			"worker_count":       r.WorkerCount,
		})
	}
	return out
}

// moduleSpec returns the adapter of req's provider, its effective cloud
// profile and the adapter's view of req after the adapter's Normalize: the
// place of each region (code, cloud region, zone) that writeTfvars hands the
// module is the adapter's. writeTfvars writes the variables of the request it
// is given and is not a validator: Provision runs Validate before it, and
// Destroy writes the variables of a wipe request that nothing validates. So a
// refusal of Normalize is not returned here; the spec is used as Normalize
// left it.
func moduleSpec(req Request) (providers.CloudProvider, *cloudprofile.Profile, providers.ProvisionSpec, error) {
	name := strings.ToLower(strings.TrimSpace(req.Provider))
	adapter, err := providers.Get(name)
	if err != nil {
		return nil, nil, providers.ProvisionSpec{}, err
	}
	profile, err := EffectiveProfile(name, req.Installation)
	if err != nil {
		return nil, nil, providers.ProvisionSpec{}, err
	}
	req.Credentials = maps.Clone(req.Credentials)
	syncLegacyCredentials(&req)
	spec := req.providerSpec()
	_ = adapter.Normalize(&spec, profile) // the refusal is Validate's, see above
	return adapter, profile, spec, nil
}

// contractVariableNames returns the names an adapter's TfVars must not use:
// the common variables and the engine extras of module contract version 1
// (_contract/contract.json below ProvidersDir).
func contractVariableNames() (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(ProvidersDir, "_contract", "contract.json")))
	if err != nil {
		return nil, fmt.Errorf("module contract: %w", err)
	}
	var contract struct {
		Variables []struct {
			Name string `json:"name"`
		} `json:"variables"`
		EngineExtras []string `json:"engine_extras"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, fmt.Errorf("module contract: %w", err)
	}
	names := map[string]bool{}
	for _, v := range contract.Variables {
		names[v.Name] = true
	}
	for _, name := range contract.EngineExtras {
		names[name] = true
	}
	return names, nil
}

// mergeAdapterVariables adds the variables of the adapter (class 3 of module
// contract version 1) to vars. A key that the engine wrote, or that names a
// common variable or an engine extra of the contract, is refused with
// providers.ErrTfVarCollision.
func mergeAdapterVariables(vars, own map[string]any) error {
	reserved, err := contractVariableNames()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(own))
	for key := range own {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, written := vars[key]; written || reserved[key] {
			return fmt.Errorf("%w: %s", providers.ErrTfVarCollision, key)
		}
		vars[key] = own[key]
	}
	return nil
}

// addCloudRegionIfMissing brings the module input `regions` of a kept
// tofu.auto.tfvars.json to the shape of module contract version 1 (CLD-090).
// A file written before that contract has entries without cloud_region, and
// the Huawei module no longer accepts them. Such an entry gets the file's own
// cloudRegionVariable, or the cloud region the adapter gives the request's
// primary region (moduleSpec) when the file has none.
// Nothing else changes. A file whose entries all have cloud_region is not written.
func addCloudRegionIfMissing(path string, req Request) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		return fmt.Errorf("decode tfvars: %w", err)
	}
	regions, isList := vars["regions"].([]any)
	if !isList {
		return nil
	}
	cloudRegion, _ := vars[cloudRegionVariable].(string)
	if strings.TrimSpace(cloudRegion) == "" {
		if _, _, spec, err := moduleSpec(req); err == nil && len(spec.Regions) > 0 {
			cloudRegion = spec.Regions[0].CloudRegion
		}
	}
	changed := false
	for _, entry := range regions {
		region, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		if have, _ := region["cloud_region"].(string); strings.TrimSpace(have) != "" {
			continue
		}
		region["cloud_region"] = cloudRegion
		changed = true
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// coalesceParentDomains normalises a nil ParentDomain slice to an empty
// slice, so that JSON marshalling emits `[]` and not `null`: an OpenTofu
// module iterating `var.parent_domains` gets `[]` (validator-friendly)
// instead of `null` (validator-fatal). Issue #826.
func coalesceParentDomains(pds []ParentDomain) []ParentDomain {
	if pds == nil {
		return []ParentDomain{}
	}
	return pds
}

// parentDomainsYAMLLiteral renders the parent-domain pool as a YAML
// inline-array literal suitable for the OpenTofu module's
// `var.parent_domains_yaml` (declared in infra/hetzner/variables.tf,
// consumed by locals.parent_domains_decoded via yamldecode()).
//
// Issue #1772 (TBD-D30b — Cilium Gateway missing *.omani.homes listener
// on t22): catalyst-api previously emitted only the structural
// `parent_domains` JSON array — never the YAML-string variable the
// terraform module actually reads. As a result the module's
// listener-rendering local fell through to the single-zone fallback
// `[{name: "<sovereign_fqdn>", role: "primary"}]` and every org-pool
// zone the operator added (e.g. omani.homes) was silently dropped
// from the Cilium Gateway listeners. Tenant TLS on the missing zone
// then failed with NET::ERR_CERT_COMMON_NAME_INVALID.
//
// Output shape — JSON-flow array, which is valid YAML (yamldecode()
// accepts it natively). Each entry carries name + role; we
// deliberately omit registrarKind/credsRef from the YAML so the
// terraform module never sees per-domain secrets (those belong in
// the catalyst-api persistent record only). Empty / nil slice → ""
// so the module's single-zone fallback (derived from sovereign_fqdn)
// kicks in cleanly.
//
// JSON-encoding via encoding/json guarantees correctly quoted strings
// for any zone name + escapes any unexpected character; the resulting
// scalar is embedded inside the tofu.auto.tfvars.json string value
// without quoting hell.
func parentDomainsYAMLLiteral(pds []ParentDomain) string {
	if len(pds) == 0 {
		return ""
	}
	type yamlEntry struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	entries := make([]yamlEntry, 0, len(pds))
	for _, pd := range pds {
		name := strings.ToLower(strings.TrimSpace(pd.Name))
		if name == "" {
			continue
		}
		role := strings.TrimSpace(pd.Role)
		if role == "" {
			role = ParentDomainRolePrimary
		}
		entries = append(entries, yamlEntry{Name: name, Role: role})
	}
	if len(entries) == 0 {
		return ""
	}
	out, err := json.Marshal(entries)
	if err != nil {
		// json.Marshal on []yamlEntry can only fail on cycles / unsupported
		// types — none possible here. Defensive fallback to empty so the
		// terraform single-zone path takes over rather than tripping
		// yamldecode() on a malformed literal.
		return ""
	}
	return string(out)
}

// mapDomainModeForTofu collapses the wizard's three-mode domain
// selector (`pool` / `byo-manual` / `byo-api`) into the binary
// pool/byo enum the OpenTofu module's `variable "domain_mode"`
// validation accepts.
//
// The wizard's tri-state exists for UX branching — pool flows through
// the OpenOva PDM + Dynadot path; byo-manual asks the operator to
// paste the NS records into their registrar manually; byo-api drives
// the registrar API automatically. From the cloud-infrastructure
// layer (Hetzner servers, network, LB) NONE of those distinctions
// matter — the only thing tofu needs to know is "do I need to call
// Dynadot at apply time?" which is `pool` only.
//
// An empty string maps to "byo" so the test path that leaves
// SovereignDomainMode unset doesn't accidentally trigger the pool
// branch.
func mapDomainModeForTofu(wizardMode string) string {
	if wizardMode == "pool" {
		return "pool"
	}
	return "byo"
}

// deriveQAFixturesNamespace returns the qa-fixtures namespace name to thread
// into the bootstrap-kit Kustomization's QA_FIXTURES_NAMESPACE substitute
// envvar. Resolution order:
//
//  1. req.QAFixturesNamespace if non-empty (operator override).
//  2. "qa-<first-label-of-FQDN>" derived from req.SovereignFQDN — e.g.
//     "omantel.biz" → "qa-omantel", "qa.example.com" → "qa-qa".
//  3. "qa-default" fallback when SovereignFQDN is empty (defensive — the
//     Validate() pass already guarantees non-empty FQDN at provision time).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the chart's default
// of "qa-omantel" is correct ONLY for omantel.biz; deriving from FQDN here
// guarantees every QA Sovereign gets its own unambiguous namespace name
// without relying on operator-set values.
//
// When req.QATestEnabled=false this value still computes (cheap) but is
// inert: the chart's `qaFixtures.enabled: false` short-circuits before
// the namespace name is materialised.
func deriveQAFixturesNamespace(req Request) string {
	if s := strings.TrimSpace(req.QAFixturesNamespace); s != "" {
		return s
	}
	label := firstFQDNLabel(req.SovereignFQDN)
	if label == "" {
		return "qa-default"
	}
	return "qa-" + label
}

// deriveQAOrganization returns the qa-fixtures Organization CR name (the
// `organization` chart value at clusters/_template/bootstrap-kit/
// 13-bp-catalyst-platform.yaml line 519). Same resolution shape as
// deriveQAFixturesNamespace: explicit override → derived "<label>-platform"
// → "default-platform" defensive fallback.
//
// "default-platform" is the safe degenerate value (validates against the
// Organization CRD's name regex `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` and
// avoids any cross-Sovereign collision because Validate() blocks
// SovereignFQDN-empty requests upstream).
func deriveQAOrganization(req Request) string {
	if s := strings.TrimSpace(req.QAOrganization); s != "" {
		return s
	}
	label := firstFQDNLabel(req.SovereignFQDN)
	if label == "" {
		return "default-platform"
	}
	return label + "-platform"
}

// firstFQDNLabel returns the first DNS label of an FQDN — "omantel" from
// "omantel.biz", "qa" from "qa.example.com", "" from "" or a single-label
// input (single-label inputs are caught upstream by the FQDN regex
// validator at variables.tf line 14, but the helper degrades gracefully
// here so unit tests against partial requests don't panic).
func firstFQDNLabel(fqdn string) string {
	s := strings.ToLower(strings.TrimSpace(fqdn))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "."); i > 0 {
		return s[:i]
	}
	// No dot — return as-is. The Validate() FQDN regex rejects single-
	// label inputs upstream so this branch is unreachable in normal
	// operation; kept as a non-panicking fallback for unit tests.
	return s
}

// deriveClusterMeshName returns the canonical Cilium ClusterMesh name
// for this Sovereign. Operator may override via Request.ClusterMeshName;
// otherwise auto-derived from the FQDN's first label suffixed with
// "-mesh". For single-region provs (len(Regions) <= 1) returns empty
// string — single-cluster Sovereigns don't need a mesh.
//
// Caught on prov t104.omani.works (2026-05-15): operator submitted the
// canonical multi-region request without ClusterMeshName, defaulted to
// "" → cilium-config rendered cluster.name="default" on all 3 regions
// → kvstoremesh refused to start. Auto-derive closes the gap.
func deriveClusterMeshName(req Request) string {
	return DeriveClusterMeshName(req)
}

// DeriveClusterMeshName is the exported wrapper around deriveClusterMeshName
// so the handler package can derive a consistent default at orchestrator
// time without duplicating the firstFQDNLabel logic. Same semantics as
// the unexported version: operator override > FQDN-based default >
// empty string for single-region provs.
func DeriveClusterMeshName(req Request) string {
	if s := strings.TrimSpace(req.ClusterMeshName); s != "" {
		return s
	}
	if len(req.Regions) <= 1 {
		return ""
	}
	label := firstFQDNLabel(req.SovereignFQDN)
	if label == "" {
		return ""
	}
	return label + "-mesh"
}

// DeriveSecondaryClusterMeshName returns the per-secondary cluster name
// MATCHING tofu's `secondary_region_cluster_mesh_name` local at
// infra/hetzner/main.tf:389: `<sovereign-stem>-<region-stem-no-digits>`
// e.g. for FQDN=t129.omani.works + CloudRegion=nbg1 → `t129-nbg`.
//
// IMPORTANT — different shape from DeriveClusterMeshName (primary uses
// `<stem>-mesh`, secondaries use `<stem>-<region-no-digits>`). The
// asymmetry is baked into tofu; the orchestrator MUST match the names
// cilium-config carries on each region's cluster, otherwise the agent
// queries `cilium/cluster-config/v1/<peer-name>` against a key the
// peer's etcd doesn't have → "failed to retrieve cluster configuration:
// not found" and the cluster stays not-ready.
//
// Caught on t129 (6cddff7ef4432bdc, 2026-05-16): orchestrator used
// `<primary-name>-<region-key>` (e.g. `t129-mesh-nbg1-1`) but actual
// cilium-config rendered `<stem>-<region-stem>` (`t129-nbg`).
//
// Operator override via RegionSpec.ClusterMeshName takes precedence.
// Returns empty for single-region or when the FQDN can't be parsed.
func DeriveSecondaryClusterMeshName(req Request, rs RegionSpec) string {
	if s := strings.TrimSpace(rs.ClusterMeshName); s != "" {
		return s
	}
	if len(req.Regions) <= 1 {
		return ""
	}
	stem := firstFQDNLabel(req.SovereignFQDN)
	if stem == "" {
		return ""
	}
	regionStem := stripDigitRuns(rs.CloudRegion)
	if regionStem == "" {
		return ""
	}
	return stem + "-" + regionStem
}

// stripDigitRuns removes EVERY digit run from a region name — `nbg1` →
// `nbg`, `hel1` → `hel`, `me-east-215-b` → `me-east--b`. This must
// match tofu's `replace(r.code, "/[0-9]+/", "")` (infra/providers/*
// secondary cluster.name derivation) EXACTLY: the predecessor
// (stripTrailingDigits) only removed TRAILING runs, which agreed with
// tofu on Hetzner regions (`fsn1`, `hel1` — digits at the end) but
// diverged on kom4dc (`me-east-215-b` — interior run). Live on hw128
// (#3241 layer 5) the orchestrator wrote peer entries / hostAliases /
// etcd lookups under `hw128-me-east-215-b` while region-b's cilium
// registered as `hw128-me-east--b` → region-a polled a cluster-config
// key that never existed and sat at `remote configuration:
// retrieved=false` forever, despite a healthy etcd session.
func stripDigitRuns(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			b.WriteByte(s[i])
		}
	}
	// A digit run at a segment boundary ("me-east-215" → "me-east-", or the
	// interior run in "me-east-215-b" → "me-east--b") leaves a trailing "-"
	// or a "--" once the digits are gone. Cilium's cluster.name validator
	// (validate.yaml) rejects BOTH ("must start and end with an alphanumeric
	// character") → the CNI install fails → nodes NotReady → 0 HRs (the
	// hw209-215 kom4dc wedge, live-diagnosed on 38e4191f). Collapse repeated
	// dashes + trim leading/trailing so the derived name is always a valid
	// cilium cluster.name. MUST stay byte-identical to the tofu counterpart
	// (huawei/main.tf:906/915) per the #3241 lockstep contract.
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// deriveClusterMeshID returns the canonical Cilium ClusterMesh peer ID
// for this Sovereign's PRIMARY region. Operator may override via
// Request.ClusterMeshID; otherwise auto-derived deterministically from
// the deployment ID hash, modulo 252, plus 1 (range 1..252; leaves
// 253-255 as a 3-slot pad for secondaries which main.tf computes as
// primary+1, primary+2, etc.).
//
// For single-region provs (len(Regions) <= 1) returns 0 (the
// "not-in-mesh" sentinel that variables.tf documents). The tofu module
// at infra/hetzner/main.tf has matching logic that emits secondaries
// at 0 when the primary is 0.
//
// Caught on prov t104.omani.works (2026-05-15): operator submitted
// multi-region request without ClusterMeshID, defaulted to 0 →
// cilium-config rendered cluster.id=0 on all 3 regions → Cilium
// reserves 0 → kvstoremesh CrashLoopBackOff with "ClusterID 0 is
// reserved" → no mesh ever formed → cross-region observability
// permanently broken.
func deriveClusterMeshID(req Request) int {
	return DeriveClusterMeshID(req)
}

// DeriveClusterMeshID is the exported wrapper around deriveClusterMeshID
// so the handler package can derive a consistent default at orchestrator
// time. Same semantics: operator override > deterministic hash(DepID|FQDN)
// % 252 + 1 > 0 for single-region.
func DeriveClusterMeshID(req Request) int {
	if req.ClusterMeshID != 0 {
		return req.ClusterMeshID
	}
	if len(req.Regions) <= 1 {
		return 0
	}
	src := strings.TrimSpace(req.DeploymentID)
	if src == "" {
		src = strings.TrimSpace(req.SovereignFQDN)
	}
	if src == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(src))
	// Take a 32-bit window of the hash and reduce to [1, 252]. The
	// primary uses this value; main.tf increments for secondaries so
	// the max id any region sees is primary+(N-1) ≤ 252+2 = 254.
	// 255 is intentionally avoided — Cilium uses it as a sentinel
	// in some configs.
	v := int(binary.BigEndian.Uint32(sum[:4]))
	return (v % 252) + 1
}
