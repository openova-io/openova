// Package v1alpha1 — Blueprint topology Go types for G117 EPIC.
//
// These types are the in-tree, statically-typed representation of the
// new `spec.topology`, `spec.endpoints`, `spec.sso`, and
// `spec.multiInstance` fields the G117 wave adds to the Blueprint CRD.
//
// The existing blueprint-controller (core/controllers/blueprint/...)
// uses the dynamic client + unstructured.Unstructured to round-trip
// Blueprint CRs, so this package is consumed primarily by:
//
//  1. The admission webhook (validates incoming Blueprint manifests
//     against the locked schema in platform/_schemas/blueprint-topology.json)
//  2. The application-controller (G117.6) when fanning HelmReleases
//     across host clusters per Blueprint.spec.topology + perTopology
//     placement
//  3. catalyst-api (G117.2-G117.4) when serving GET /catalog/{bp}
//     and computing default + supported topologies for the new-instance
//     dialog
//
// Schema shape (locked architectural decision #1):
//
//	spec:
//	  topology:
//	    supported: [active-active, active-hot-standby, active-passive, singleton]
//	    defaults:
//	      multi-region: active-hot-standby
//	      single-region: singleton
//	    perTopology:
//	      active-hot-standby:
//	        replication: { backend: cnpg-pair, mode: sync, lagSloSeconds: 5 }
//	        switchover:  { mechanism: bp-continuum, rtoSeconds: 30, rpoSeconds: 0 }
//	        placement:   { clusters: [mgmt-A, mgmt-B], roles: { mgmt-A: active, mgmt-B: passive } }
//
// The Topology struct is intentionally permissive on the perTopology map
// because each variant uses different replication/switchover knobs.
// The structural JSON schema (platform/_schemas/blueprint-topology.json)
// is the SINGLE source of truth for required-vs-optional per variant —
// Go code here only carries the shape, not the per-variant required
// fields.
//
// Source-of-truth document for which Blueprint takes which topology:
//
//	docs/sessions/2026-06-02-per-blueprint-topology-audit.md
//
// Reference: docs/ARCHITECTURE.md §3 (Blueprint CRD) and §8 (multi-region).
package v1alpha1

// BcpTopology is the enum of supported BCP (Business Continuity Plan)
// topology choices a Blueprint may declare. See ARCHITECTURE §8 +
// `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` legend.
//
// LOCKED enum — do not extend without an ADR.
type BcpTopology string

const (
	// BcpActiveActive — both regions serve live traffic simultaneously
	// (e.g. bp-strimzi MirrorMaker2, bp-clickhouse, stateless APIs).
	BcpActiveActive BcpTopology = "active-active"

	// BcpActiveHotStandby — primary serves traffic; secondary is fully
	// provisioned and kept in-sync (sync-replication where the data
	// store supports it; see PerTopology.Replication for the knobs).
	// Switchover RTO target: ≤30s.
	BcpActiveHotStandby BcpTopology = "active-hot-standby"

	// BcpActivePassive — primary serves traffic; secondary is in cold
	// or warm standby (e.g. backup/restore-based; no in-flight sync).
	BcpActivePassive BcpTopology = "active-passive"

	// BcpSingleton — single instance, no cross-region HA. Used for
	// per-cluster infrastructure (bp-cilium, bp-flux, bp-cert-manager,
	// etc.) where each cluster owns its own copy.
	BcpSingleton BcpTopology = "singleton"
)

// AllBcpTopologies returns the LOCKED enum slice for admission
// validation and tests. Mirrors the JSON schema's bcpTopology enum.
func AllBcpTopologies() []BcpTopology {
	return []BcpTopology{
		BcpActiveActive,
		BcpActiveHotStandby,
		BcpActivePassive,
		BcpSingleton,
	}
}

// IsValid reports whether s is a recognised topology choice.
func (b BcpTopology) IsValid() bool {
	switch b {
	case BcpActiveActive, BcpActiveHotStandby, BcpActivePassive, BcpSingleton:
		return true
	}
	return false
}

// Topology captures the per-Blueprint multi-region declaration. The
// admission webhook enforces that `defaults.multi-region` and
// `defaults.single-region` are each members of `supported`, and that
// every key in `perTopology` is in `supported` too.
type Topology struct {
	// Supported lists every BCP topology this Blueprint can be
	// installed with. Must be non-empty.
	Supported []BcpTopology `json:"supported"`

	// Defaults selects the topology used by the application-controller
	// when the Sovereign exposes multi-region vs single-region to the
	// installing org. The console / API surfaces these as the
	// pre-selected radio button on the new-instance dialog.
	Defaults TopologyDefaults `json:"defaults"`

	// PerTopology carries the replication/switchover/placement knobs
	// per variant. Keys MUST be present in Supported.
	//
	// The value shape is variant-specific; the structural schema in
	// platform/_schemas/blueprint-topology.json gates required fields.
	PerTopology map[BcpTopology]TopologyVariant `json:"perTopology,omitempty"`
}

// TopologyDefaults binds a default BcpTopology to each Sovereign
// shape. The application-controller picks the multi-region default
// when len(Sovereign.spec.regions) > 1, else the single-region default.
// (Locked architectural decision #7.)
type TopologyDefaults struct {
	MultiRegion  BcpTopology `json:"multi-region"`
	SingleRegion BcpTopology `json:"single-region"`
}

// TopologyVariant is the per-BCP-topology shape. All sub-blocks are
// optional at the Go level; the JSON schema gates which are required
// per BCP variant (e.g. active-hot-standby requires Replication +
// Switchover; singleton requires only Placement).
type TopologyVariant struct {
	Replication *ReplicationSpec `json:"replication,omitempty"`
	Switchover  *SwitchoverSpec  `json:"switchover,omitempty"`
	Placement   *PlacementSpec   `json:"placement,omitempty"`
}

// ReplicationSpec — how the Blueprint preserves state across the
// active/passive pair. Backend strings are conventional (cnpg-pair,
// raft, mirrormaker2, s3-bucket-replication, ccr, none).
type ReplicationSpec struct {
	// Backend is the canonical name of the replication mechanism.
	// See docs/sessions/2026-06-02-per-blueprint-topology-audit.md
	// "State preservation" column for the per-Blueprint convention.
	Backend string `json:"backend"`

	// Mode is the consistency model. Common values:
	//   - sync    — synchronous (zero-tx-loss; bp-cnpg-pair `remote_apply`)
	//   - async   — asynchronous (eventually-consistent; S3 bucket repl)
	//   - none    — n/a (DaemonSet, stateless, etc.)
	Mode string `json:"mode,omitempty"`

	// LagSloSeconds is the maximum acceptable replication lag in
	// seconds, used by the Continuum SLO checks. 0 = sync mode (no lag).
	LagSloSeconds *int `json:"lagSloSeconds,omitempty"`
}

// SwitchoverSpec — how an active→passive flip is orchestrated.
type SwitchoverSpec struct {
	// Mechanism names the controller that orchestrates the switchover.
	// Conventional: bp-continuum, manual, bp-velero-restore.
	Mechanism string `json:"mechanism"`

	// RtoSeconds — recovery-time objective. Target time from "primary
	// loss detected" to "passive promoted and serving".
	RtoSeconds *int `json:"rtoSeconds,omitempty"`

	// RpoSeconds — recovery-point objective. Acceptable data loss in
	// the worst case. 0 means sync replication.
	RpoSeconds *int `json:"rpoSeconds,omitempty"`
}

// PlacementSpec — which host clusters this Blueprint is installed on.
type PlacementSpec struct {
	// Tier is one of the canonical cluster tiers used by
	// `Blueprint.spec.placementSchema.vcluster`:
	//   - "mgmt" — Catalyst control-plane cluster
	//   - "dmz"  — DMZ tenant workload cluster
	//   - "rtz"  — Regulated tenant workload cluster
	//   - ""     — no vCluster placement; installs into the host cluster
	Tier string `json:"tier,omitempty"`

	// Clusters lists the canonical cluster IDs this Blueprint is
	// installed on. Used by the application-controller's fan-out
	// renderer (G117.6) to write per-cluster HelmReleases.
	Clusters []string `json:"clusters,omitempty"`

	// Roles maps cluster ID → role ("active" | "passive" | "singleton").
	// The application-controller emits the active-role HR with full
	// traffic-facing config; passive HRs are rendered identically but
	// the Continuum lease prevents them from serving.
	Roles map[string]string `json:"roles,omitempty"`
}

// EndpointSpec — a single named external endpoint surfaced by a
// Blueprint. The Endpoints tab UI (G117.3) renders one card per entry;
// the Launch button UI (G117.4) starts from the entry tagged
// `launchDefault: true` (else the first SSO-enabled entry).
type EndpointSpec struct {
	// Name is a stable identifier per Blueprint (e.g. "ui", "api",
	// "metrics"). Must be unique within a Blueprint.
	Name string `json:"name"`

	// HostnameTemplate is a Go-text/template fragment evaluated by
	// catalyst-api against the per-Instance / per-Org / per-Sovereign
	// context. Tokens MUST resolve to hostnames matching RFC-1123.
	//
	// Supported tokens (catalyst-api resolveHostnameTemplate):
	// `{{.SovereignFQDN}}` `{{.OrgSlug}}` `{{.AppName}}` `{{.OrgDomain}}`.
	//
	// A PER-ORG app MUST name its host with `{{.OrgDomain}}` — e.g.
	// `agenity.{{.OrgDomain}}`. Composing `{{.OrgSlug}}` with
	// `{{.SovereignFQDN}}` was the #5389 defect: per-Org apps are served on
	// the Organization's pool domain (agenity.uatco.omani.homes), and
	// `<app>.<org>.<SovereignFQDN>` has no HTTPRoute, no DNS and no
	// certificate — a well-formed, dead Open button. `{{.OrgDomain}}` falls
	// back to `<slug>.<SovereignFQDN>` for a single-domain Org, so it is a
	// strict replacement.
	//
	// A SOVEREIGN-SINGLETON app names only `{{.SovereignFQDN}}` — e.g.
	// `grafana.{{.SovereignFQDN}}`.
	//
	// Since #5389 an unsupported token, or a token that substitutes to the
	// empty string, is a hard resolution FAILURE: the endpoint is reported
	// with no hostname and no launchURL rather than emitting a URL with the
	// literal braces still in it.
	HostnameTemplate string `json:"hostnameTemplate"`

	// Port — service port exposed by the Blueprint's Service. Default
	// 443 for `protocol: https`, 80 for `http`. The Gateway/HTTPRoute
	// rendered by the application-controller uses this value.
	Port *int `json:"port,omitempty"`

	// Protocol — https | http | grpc | tcp | udp. Drives Gateway
	// listener selection and Coraza WAF attach.
	Protocol string `json:"protocol,omitempty"`

	// TLS — does the endpoint terminate TLS at the Gateway? Default
	// true. If false, the endpoint is internal-only and the Launch
	// button is hidden.
	TLS *bool `json:"tls,omitempty"`

	// Visibility — public | private | internal. `internal` endpoints
	// are not exposed by Gateway and only resolvable within the
	// host cluster. `private` is mTLS-only (no Coraza WAF).
	Visibility string `json:"visibility,omitempty"`

	// LaunchDefault — when true, the console's Launch button on the
	// Application detail page navigates to this endpoint by default.
	// Exactly one EndpointSpec per Blueprint MAY set this true; if
	// none do, catalyst-api picks the first SSO-enabled https endpoint.
	LaunchDefault bool `json:"launchDefault,omitempty"`

	// SSOEnabled — whether this endpoint sits behind the Sovereign's
	// SSO chain (Catalyst KC realm `sovereign` for Tier-1/2 apps;
	// per-Org KC realm with sovereign-broker federation for Tier-3).
	// If true, the Launch button uses silent OIDC
	// `prompt=none&kc_idp_hint=catalyst-pin` per G117.4.
	SSOEnabled bool `json:"ssoEnabled,omitempty"`
}

// SSOSpec — Blueprint-level SSO declaration. Tier-1 (in-band CP), Tier-2
// (CP-adjacent), Tier-3 (tenant Apps) follow the locked SSO fan-out
// plan (decision #5) — `realm` here selects which KC realm the
// Blueprint federates to.
type SSOSpec struct {
	// Realm — `sovereign` for Tier-1/2 (Catalyst CP realm); `<org>`
	// for Tier-3 (per-Org realm with `Keycloak OIDC` IdP federated to
	// the sovereign-realm broker — see decision #6).
	Realm string `json:"realm"`

	// ProtocolMapper — `oidc` (default) | `saml`. Used by application-
	// controller when rendering the per-instance Secret for the
	// downstream chart (bp-grafana, bp-gitea, bp-harbor, …).
	ProtocolMapper string `json:"protocolMapper,omitempty"`

	// GroupsClaim — name of the OIDC claim carrying the user's groups
	// for RBAC. Default "groups" (per G91 + G113).
	GroupsClaim string `json:"groupsClaim,omitempty"`

	// RolesFromGroup — map of OIDC group name → application-level role.
	// e.g. {"sovereign-admins": "admin", "tenant-admins": "editor"}.
	RolesFromGroup map[string]string `json:"rolesFromGroup,omitempty"`

	// SilentLogin — when true, the Launch button URL appends
	// `prompt=none&kc_idp_hint=catalyst-pin` to silently SSO-bounce
	// through KC. Default true on Tier-1/2; false on Tier-3 until
	// per-Org realm federation is verified.
	SilentLogin *bool `json:"silentLogin,omitempty"`

	// AdminGroup — the Sovereign-realm GROUP whose members are elevated
	// to the application's admin role. This is the STANDARD, app-agnostic
	// admin-bootstrap declaration (#3648): every Blueprint that needs a
	// first admin seeded via the Sovereign SSO names the group HERE,
	// instead of burying it in a chart-bespoke values key (newapi's
	// `adminSeed.adminGroup`, harbor's `sso.adminGroup`, gitea's
	// `sso.adminGroup`, …). Elevation ALWAYS keys on the group, never on
	// a per-user identity (#3374 law §5). Default `sovereign-admins`.
	//
	// HOW each Blueprint honours it is the chart's business and varies by
	// the upstream app's admin model — Pattern A apps map the OIDC
	// `groups` claim → admin role natively (grafana `role_attribute_path`,
	// harbor `oidc_admin_group`); Pattern B apps with no group→role
	// mechanism seed/promote DB-direct (newapi, guacamole). The PLATFORM
	// contract is the uniform DECLARATION of the group; the seeding
	// mechanism is the chart's, never keyed on the app name in the engine.
	AdminGroup string `json:"adminGroup,omitempty"`

	// Bootstrap — optional standard sso-bootstrap stanza: when set, the
	// Blueprint declares that the platform should seed its FIRST admin via
	// the Sovereign SSO (Pattern B — apps whose upstream has no
	// group→role config knob). See SSOBootstrapSpec. Absent → no admin
	// seed (Pattern A apps, or apps that don't need a seeded admin).
	Bootstrap *SSOBootstrapSpec `json:"bootstrap,omitempty"`
}

// SSOBootstrapSpec — the STANDARD, app-agnostic "seed the first admin via
// the Sovereign SSO" contract (#3648). It generalises the formerly
// newapi-specific `adminSeed` stanza into a declaration ANY Blueprint can
// carry. The platform honours the SAME fields uniformly; the chart
// supplies the engine-specific seeding mechanism (a DB-direct Job for
// apps like newapi/guacamole whose upstream has no group→role config, or
// a native config map for apps like grafana/harbor that map the group
// claim themselves) — there is NO app-name literal in how the platform
// interprets these fields.
//
// The declaration answers three app-agnostic questions:
//
//	sso:
//	  adminGroup: sovereign-admins   # WHO becomes admin (the group)
//	  bootstrap:
//	    enabled: true                # seed a first admin at all?
//	    providerSlug: sovereign      # the OIDC provider id the app trusts
//	    enforceAdminGroup: false     # also DENY non-members at the app edge?
type SSOBootstrapSpec struct {
	// Enabled — seed a first admin for this app via the Sovereign SSO.
	// Default true when the Bootstrap stanza is present at all.
	Enabled *bool `json:"enabled,omitempty"`

	// ProviderSlug — the in-app OIDC provider identifier the seeded admin
	// authenticates through (e.g. `sovereign`). Maps to the app's OIDC
	// client/provider id and the KC realm segment. Default `sovereign`.
	ProviderSlug string `json:"providerSlug,omitempty"`

	// EnforceAdminGroup — when true, also GATE the app's OIDC login on
	// AdminGroup membership (deny non-members at the app boundary).
	// Default false: the Sovereign realm's catalyst-pin IdP already
	// restricts WHO can authenticate, and a strict claim-format gate must
	// never break the owner's first landing (the #3150 wire-proof≠walk
	// lesson).
	EnforceAdminGroup bool `json:"enforceAdminGroup,omitempty"`
}

// MultiInstanceSpec — when present, a Blueprint may have >1
// Application instance per Organization. Catalog cards (G117.2) show
// the instance-count badge and "+ New instance" button when this is
// declared. When absent, a Blueprint is singleton-per-Org.
type MultiInstanceSpec struct {
	// Enabled — true to permit multiple instances per Organization.
	Enabled bool `json:"enabled"`

	// MaxPerOrg — optional cap. 0 / unset = unlimited.
	MaxPerOrg *int `json:"maxPerOrg,omitempty"`

	// NamingTemplate — how the application-controller names per-instance
	// Namespaces and Helm releases. Default `{{.AppName}}-{{.InstanceID}}`.
	NamingTemplate string `json:"namingTemplate,omitempty"`

	// IsolationLevel — `namespace` (default) | `vcluster`. When
	// `vcluster`, each instance gets its own vCluster under the Org's
	// tier-cluster (rtz or dmz).
	IsolationLevel string `json:"isolationLevel,omitempty"`
}

// ProducesInstancesSpec — declares that THIS Blueprint is an OPERATOR /
// engine that PRODUCES shareable data-instances of another Blueprint. It
// is the DECLARED, app-agnostic replacement for the former hardcoded
// "postgres operator" knowledge: any operator Blueprint (CNPG, a future
// Redis/Valkey operator, a Kafka operator, …) that carries this stanza
// gets the engine-card / instance-creation treatment uniformly — NO
// engine-name literal anywhere in the platform engine.
//
// The contract pairs an OPERATOR Blueprint (this one, typically
// `visibility: unlisted`) with the INSTANCE Blueprint it provisions:
//
//	# platform/cnpg/blueprint.yaml (the OPERATOR / engine):
//	spec:
//	  visibility: unlisted
//	  producesInstances:
//	    kind: postgres            # the engine-class id (db engine kind)
//	    instanceBlueprint: bp-postgres   # the listed instance card
//
// The instance Blueprint (bp-postgres) keeps declaring `multiInstance`
// + `contextSchema` (the per-instance + per-Context machinery). What
// `producesInstances` adds is the explicit operator→instance edge the
// engine reads instead of inferring it from a `card.family: postgres`
// label or a `Type == "postgres"` string check.
//
// Consumed by:
//   - the backing-service generator (core/controllers/pkg/backingservice):
//     it resolves a consumer's `backingServices[].type: <kind>` to the
//     instance Blueprint HR prefix via the producer registry built from
//     these declarations, instead of the hardcoded `bp-postgres-` literal.
//   - the catalog projector / console engine-card surface: an operator
//     Blueprint with this stanza is the "engine class" shown once; the
//     `instanceBlueprint`'s installs are the data-instance cards.
type ProducesInstancesSpec struct {
	// Kind — the engine-class identifier the instances are OF
	// (e.g. `postgres`, `redis`, `kafka`). This is the value a consumer
	// Blueprint names in `backingServices[].type` and the label the
	// console renders on the engine-class card. REQUIRED.
	Kind string `json:"kind"`

	// InstanceBlueprint — the id of the INSTANCE Blueprint this operator
	// provisions (e.g. `bp-postgres`). Each install of that Blueprint is
	// one shareable data-instance; the Flux HR a consumer `dependsOn` is
	// `<instanceBlueprint>-<instanceName>`. REQUIRED.
	InstanceBlueprint string `json:"instanceBlueprint"`
}

// BackingServiceMode is the binding mode a consumer requests for a
// backing service: a dedicated private instance, or a share of a named
// existing instance. See ADR-0010.
type BackingServiceMode string

const (
	// BackingServiceModePrivate — the consumer gets its OWN dedicated
	// data-instance (one CNPG Cluster per consumer). This is the legacy
	// 1:1 shape and the default when `mode` is omitted.
	BackingServiceModePrivate BackingServiceMode = "private"

	// BackingServiceModeShared — the consumer SHARES a named existing
	// data-instance (referenced via InstanceRef). The generator adds an
	// isolated Database + a per-consumer role + a reflected Secret to
	// that instance, and points the consumer's dependsOn at it.
	BackingServiceModeShared BackingServiceMode = "shared"
)

// BackingServiceSpec — a consumer Blueprint's declared need for a
// backing service (ADR-0010). The catalyst-layer generator
// (core/controllers/pkg/backingservice) translates each entry into the
// declarative binding YAML: a CNPG `Database` CR + a
// `Cluster.spec.managed.roles[]` entry + a reflected connection Secret +
// a Flux `dependsOn` edge from the consumer HR to the data-instance HR.
//
// NO controller and NO Crossplane are involved — the generated objects
// are CNPG's own declarative CRDs, applied by Flux.
type BackingServiceSpec struct {
	// Type — the backing-service engine. Postgres first; the model
	// generalises (redis, clickhouse) via the same field later.
	Type string `json:"type"`

	// Mode — private (dedicated instance, default) | shared (share a
	// named instance via InstanceRef).
	Mode BackingServiceMode `json:"mode,omitempty"`

	// InstanceRef — the data-instance (bp-postgres install) name to
	// share. REQUIRED when Mode is shared; ignored when private.
	InstanceRef string `json:"instanceRef,omitempty"`

	// Database — the isolated database name to provision for this
	// consumer on the instance. Defaults to the consumer's app name.
	Database string `json:"database,omitempty"`

	// Role — the per-consumer login role. Defaults to the database name.
	Role string `json:"role,omitempty"`

	// SecretName — the connection Secret name reflected into the
	// consumer's namespace. Defaults to `<database>-database-secret`.
	SecretName string `json:"secretName,omitempty"`
}

// ── #3373 PLACEMENT — instance-level placement types ────────────────
//
// Founder-ratified law (#3373 §2): "Placement is an INSTANCE property
// (`Application.spec.placement`), NOT a class mandate. The blueprint
// declares `defaultPlacement` + `allowedPlacements`; dmz/mgmt/rtz
// defaults are SUGGESTIONS the user accepts silently — or changes in
// advanced options."
//
// InstancePlacement is the object form of `Application.spec.placement`
// and the value type of `Blueprint.spec.defaultPlacement`. The legacy
// string form of `Application.spec.placement` (the BCP-posture enum
// single-region | active-active | active-hotstandby) remains accepted
// on the wire; the object form carries that posture in `mode` plus the
// WHERE: vcluster / regions / clusters.

// VCluster tier names — the canonical vCluster targets an instance can
// land in. "host" (or "") means the host k3s cluster itself, which is
// reserved for the HOST-MINIMUM substrate set (#3373 §4).
const (
	VClusterHost = "host"
	VClusterMgmt = "mgmt"
	VClusterDmz  = "dmz"
	VClusterRtz  = "rtz"
)

// KnownVClusters returns the canonical vCluster tier names accepted in
// InstancePlacement.VCluster (admission + UI selector source).
func KnownVClusters() []string {
	return []string{VClusterHost, VClusterMgmt, VClusterDmz, VClusterRtz}
}

// NormalizeVCluster maps the user-facing "host" sentinel (and "") to
// the internal empty-string host placement used by the render paths
// (G92.1: empty = no kubeConfig pivot, HR targets the host cluster).
func NormalizeVCluster(v string) string {
	if v == VClusterHost {
		return ""
	}
	return v
}

// IsKnownVCluster reports whether v is an accepted placement target.
// Both the "host" sentinel and "" (host) are accepted.
func IsKnownVCluster(v string) bool {
	switch v {
	case "", VClusterHost, VClusterMgmt, VClusterDmz, VClusterRtz:
		return true
	}
	return false
}

// InstancePlacement — WHERE one Application instance runs (#3373).
//
// As `Application.spec.placement` (object form): written by the
// provisioning journey's advanced view OR edited directly in Git by an
// advanced user — both equally valid, the Git-committed IaC is the
// single source of truth and the platform reconciles to it.
//
// As `Blueprint.spec.defaultPlacement`: the class-level SUGGESTION the
// instance inherits when its own placement omits a field.
type InstancePlacement struct {
	// VCluster — which vCluster the instance lands in: "mgmt" | "dmz"
	// | "rtz" | "host" (or "" = host). Drives the ONE generic render
	// mechanism: the Flux `spec.kubeConfig.secretRef` pivot
	// (fanout.go G92.1 / render.Inputs.VCluster). Never per-app code.
	VCluster string `json:"vcluster,omitempty"`

	// Regions — host clusters this instance targets, in priority
	// order. When set on an Application it overrides the legacy
	// top-level `spec.regions[]` for placement resolution.
	Regions []string `json:"regions,omitempty"`

	// Clusters — canonical cluster IDs (Sovereign.spec.clusters
	// names, e.g. mgmt-A / mgmt-B) the topology fan-out targets.
	// When set it narrows/overrides the Blueprint topology variant's
	// PlacementSpec.Clusters for THIS instance.
	Clusters []string `json:"clusters,omitempty"`

	// Mode — DEPRECATED (#3969). The legacy BCP-posture enum carried by
	// the object form (single-region | active-active | active-hotstandby).
	// Superseded by Targets[] (the desired-state target list, each with a
	// Primary|Standby data-role). The string is still parsed off the wire
	// for back-compat with older CRs, but the canonical desired state is
	// now Targets — the pattern is DERIVED via DerivePattern, never stored.
	Mode string `json:"mode,omitempty"`

	// Targets — #3969: the canonical desired-state placement. A list of
	// targets, each = region × cluster × vCluster × data-role
	// (Primary|Standby, with a Standby type Hot|Cold). The pattern
	// (singleton / active-passive / active-hot-standby / active-active) is
	// DERIVED from these via DerivePattern; it is never stored, never
	// selected. When set, Targets is the source of truth and supersedes
	// the deprecated Mode posture enum.
	Targets []PlacementTarget `json:"targets,omitempty"`

	// OwnedDependencies — #3969: per-owned-dependency cascade overrides.
	// An owned (private) backing service follows the consumer's placement
	// by DEFAULT; an entry here with Follow:false decouples it (the user
	// pins it). Absent ⇒ every owned dep follows.
	OwnedDependencies []OwnedDependencyOverride `json:"ownedDependencies,omitempty"`
}

// BlueprintSpecExtension — the new fields G117 grafts onto the existing
// Blueprint CR's spec. Production code merges these into the existing
// dynamic-client-driven blueprint-controller by reading them out of
// the unstructured.Unstructured via NestedMap; this typed shape is
// the canonical contract every other consumer (catalyst-api,
// application-controller, admission webhook, console) imports.
//
// IMPORTANT: this type intentionally does NOT alias the legacy
// `BlueprintSpec` (which lives only inside the OpenAPI CRD schema
// today, not in Go). When the project later promotes the CRD to
// typed kubebuilder types, BlueprintSpecExtension's fields fold into
// the parent struct verbatim (same JSON keys).
type BlueprintSpecExtension struct {
	Topology        *Topology            `json:"topology,omitempty"`
	Endpoints       []EndpointSpec       `json:"endpoints,omitempty"`
	SSO             *SSOSpec             `json:"sso,omitempty"`
	MultiInstance   *MultiInstanceSpec   `json:"multiInstance,omitempty"`
	BackingServices []BackingServiceSpec `json:"backingServices,omitempty"`

	// ProducesInstances — when set, declares this Blueprint is the
	// OPERATOR/engine that provisions shareable instances of another
	// (instance) Blueprint. The DECLARED replacement for the hardcoded
	// "postgres operator" knowledge: any operator Blueprint that sets it
	// gets the engine-card / instance-creation treatment uniformly. See
	// ProducesInstancesSpec.
	ProducesInstances *ProducesInstancesSpec `json:"producesInstances,omitempty"`

	// DefaultPlacement — #3373: the class-level placement SUGGESTION
	// every instance of this Blueprint inherits unless its own
	// `Application.spec.placement` overrides a field. Replaces the
	// scattered hand-maintained slot-file placement truth.
	DefaultPlacement *InstancePlacement `json:"defaultPlacement,omitempty"`

	// AllowedPlacements — #3373: the vCluster tiers an instance of
	// this Blueprint MAY be placed in ("host"|"mgmt"|"dmz"|"rtz").
	// Empty = unrestricted. The application-controller fails an
	// Application whose effective vCluster is not in this list; the
	// provisioning journey's generic advanced selector renders its
	// options from this declaration (#3370 pattern — no per-app UI).
	AllowedPlacements []string `json:"allowedPlacements,omitempty"`

	// PlacementCapability — #3969: the GATE on how many targets may be
	// Primary. "multi-primary" allows ≥2 live Primary targets
	// (active-active); "primary+standby" (the default when empty) allows
	// exactly one Primary, the rest Standby. This is the only stored
	// capability gate — the pattern label is always derived from the
	// resolved targets. Replaces the per-Blueprint `topology.supported[]`
	// posture-enum list.
	PlacementCapability PlacementCapability `json:"placementCapability,omitempty"`
}
