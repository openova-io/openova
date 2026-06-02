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
	// context. Example: `{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}`.
	// Tokens MUST resolve to hostnames matching RFC-1123.
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
	Topology      *Topology          `json:"topology,omitempty"`
	Endpoints     []EndpointSpec     `json:"endpoints,omitempty"`
	SSO           *SSOSpec           `json:"sso,omitempty"`
	MultiInstance *MultiInstanceSpec `json:"multiInstance,omitempty"`
}
