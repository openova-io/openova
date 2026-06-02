// Package v1alpha1 — Application Go types for G117 W2.C2 multi-instance.
//
// These types are the in-tree, statically-typed representation of the
// `Application.apps.openova.io/v1` CR with the new multi-instance fields
// the G117.2 wave adds:
//
//   - spec.instanceId      — stable per-instance identifier (immutable
//     after creation; defaults to first 8 chars of metadata.uid)
//   - spec.isolationLevel  — enum `namespace` (default) | `vcluster`
//   - spec.namingTemplate  — Go-template (default `{{.AppName}}-{{.InstanceID}}`
//     for namespace isolation, `{{.AppName}}` for vcluster)
//
// The existing application-controller (core/controllers/application/...)
// uses the dynamic client + unstructured.Unstructured to round-trip
// Application CRs, so this package is consumed primarily by:
//
//  1. The admission package (validates incoming Application manifests
//     against the multi-instance gates exported by the Blueprint typed
//     companion at core/pkg/apis/blueprint/v1alpha1)
//  2. catalyst-api (POST /apps/instances) when pre-checking gates
//     before creating the Application CR
//  3. application-controller fan-out helpers (G117 W2.C1) when
//     resolving the per-instance namespace target via the naming
//     template
//
// Schema shape (extends the existing CRD at
// products/catalyst/chart/crds/application.yaml — backwards-compatible:
// every new field is optional with sensible defaults):
//
//	spec:
//	  blueprintRef: { name: bp-grafana, version: 1.0.5 }
//	  environmentRef: acme-prod
//	  regions: [hz-fsn-rtz-prod]
//	  # ── G117.2 W2.C2 additions ────────────────────────────────────
//	  instanceId: a3f7c91d        # immutable after creation
//	  isolationLevel: namespace   # or `vcluster`
//	  namingTemplate: "{{.AppName}}-{{.InstanceID}}"
//
// The IsolationLevel enum is intentionally minimal — only the two
// values currently honoured by application-controller fan-out (W2.C1).
// New isolation modes (e.g. `cluster` for full cluster-per-instance)
// require an ADR.
//
// Backwards-compat invariant: every legacy Application CR (without
// instanceId/isolationLevel/namingTemplate) MUST keep reconciling. The
// admission webhook + the migration script
// (tools/migrate-applications-instanceId.py) backfill the new fields
// on first reconcile / on cluster upgrade.
//
// Reference: docs/ARCHITECTURE.md §3 (Application CRD) and
// docs/ADR-... (per-Blueprint multi-instance via G117.2 brief).
package v1alpha1

// IsolationLevel enumerates the supported per-Application isolation
// modes. LOCKED enum — extending requires an ADR.
type IsolationLevel string

const (
	// IsolationNamespace — every Application instance is a distinct
	// Kubernetes Namespace within the Org's host cluster(s). The
	// rendered HelmReleases target that namespace. This is the default
	// for backwards-compat (legacy single-instance Applications all
	// landed in the Org's default namespace).
	IsolationNamespace IsolationLevel = "namespace"

	// IsolationVCluster — every Application instance lands inside the
	// Org's existing vCluster (one vCluster per Org per host cluster).
	// The HR names differ per instance (via NamingTemplate), the
	// underlying namespace in the vCluster is shared.
	IsolationVCluster IsolationLevel = "vcluster"
)

// DefaultNamingTemplate returns the Go-template the application-controller
// renders to compute the per-instance namespace (for namespace isolation)
// or the HelmRelease name (for vcluster isolation).
//
// The template context exposes:
//
//	{{.AppName}}     — Application.metadata.name
//	{{.InstanceID}}  — Application.spec.instanceId (8-char hex)
//	{{.OrgSlug}}     — extracted from labels[catalyst.openova.io/organization]
//	{{.Blueprint}}   — Blueprint name (without `bp-` prefix)
//
// We split the default by isolation level because vCluster scoping
// already provides the per-Org boundary; appending the instanceId to
// the chart-release name is sufficient for HR uniqueness, and keeping
// the namespace static makes intra-vCluster Service discovery simpler.
func DefaultNamingTemplate(isolation IsolationLevel) string {
	switch isolation {
	case IsolationVCluster:
		return "{{.AppName}}"
	default:
		// namespace isolation OR unknown / unset → default to the
		// namespace pattern which embeds InstanceID for uniqueness.
		return "{{.AppName}}-{{.InstanceID}}"
	}
}

// MultiInstanceSpec — Application-side projection of the per-instance
// fields. Mirrors the JSON keys in the CRD schema (additive to the
// existing Application.spec).
//
// Every field is optional on the wire — the admission webhook (via the
// admission package) defaults them, and the migration script backfills
// them on the existing CR set.
type MultiInstanceSpec struct {
	// InstanceID — stable per-instance identifier. Immutable after
	// creation (admission rejects updates). Defaults to first 8 chars
	// of metadata.uid when unset.
	InstanceID string `json:"instanceId,omitempty"`

	// IsolationLevel — `namespace` (default) | `vcluster`. Determines
	// how the application-controller targets HelmReleases across
	// clusters.
	IsolationLevel IsolationLevel `json:"isolationLevel,omitempty"`

	// NamingTemplate — Go-template syntax. Default depends on
	// IsolationLevel: `{{.AppName}}-{{.InstanceID}}` for namespace
	// isolation, `{{.AppName}}` for vcluster.
	NamingTemplate string `json:"namingTemplate,omitempty"`
}

// IsValidIsolationLevel returns true when v is a recognized
// IsolationLevel (or empty, which means "default to namespace").
func IsValidIsolationLevel(v string) bool {
	switch IsolationLevel(v) {
	case "", IsolationNamespace, IsolationVCluster:
		return true
	}
	return false
}

// ApplyDefaults fills in the unset fields of an Application's
// multi-instance projection using the locked defaults. The caller is
// responsible for assigning back to the CR.
//
//   - InstanceID: keep as-is (caller supplies first 8 chars of UID on
//     create; admission webhook rejects updates that change it).
//   - IsolationLevel: empty → `namespace`.
//   - NamingTemplate: empty → DefaultNamingTemplate(IsolationLevel).
//
// This function is intentionally pure (no time/UUID side effects) so
// admission tests + migration tests share the same defaulting code.
func (m *MultiInstanceSpec) ApplyDefaults() {
	if m == nil {
		return
	}
	if m.IsolationLevel == "" {
		m.IsolationLevel = IsolationNamespace
	}
	if m.NamingTemplate == "" {
		m.NamingTemplate = DefaultNamingTemplate(m.IsolationLevel)
	}
}

// FirstUIDChars returns the first 8 chars of a UID string. Used by the
// admission webhook + the migration script to derive InstanceID from
// metadata.uid when the CR doesn't carry one. Returns the input
// verbatim when shorter than 8 chars.
func FirstUIDChars(uid string) string {
	const n = 8
	if len(uid) <= n {
		return uid
	}
	return uid[:n]
}
