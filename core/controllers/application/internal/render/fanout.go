// G117.6 — per-cluster HelmRelease fan-out.
//
// FanoutHRs turns one resolved (Application × Topology variant) pair
// into N HelmReleases — one per cluster in the variant's
// PlacementSpec.Clusters[]. Each HR carries:
//
//   - LabelRole = active | passive | singleton (per the variant's
//     PlacementSpec.Roles map; defaults to singleton when the map is
//     empty AND len(Clusters)==1, else passive)
//   - LabelTopology = the resolved BCP topology choice
//   - LabelApp = the parent Application name (back-pointer for delete
//     + observability rollup)
//   - LabelCluster = the canonical cluster ID
//   - spec.kubeConfig.secretRef following the G92.1 pivot pattern
//     (PR #2674) so Flux helm-controller pivots into the per-cluster
//     vCluster apiserver
//
// The output is the per-cluster `*unstructured.Unstructured` slice; the
// reconciler ranges over it and upserts each HR via the dynamic
// client (matching the existing application_controller.go style).
//
// Hard rule #9 (worker brief): use the existing dynamic-client path.
// We do NOT introduce a typed helmv2 SDK dependency — the
// application-controller has historically rendered HRs via unstructured
// + the dynamic client (see ensureHostFluxBootstrap()), and that idiom
// stays intact here.
//
// Refs #2745 (G117.6) + #2737 (G117 EPIC).

package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// HRName63 is the K8s DNS-1123 label cap that HelmRelease.metadata.name
// must respect. We compose `<app>-<cluster>` and truncate + hash-suffix
// when the combined length exceeds the cap.
const HRName63 = 63

// FanoutInputs carries everything FanoutHRs needs to produce per-cluster
// HelmReleases. Kept as a struct (rather than positional args) so the
// signature can grow without touching every caller — Wave-2 will likely
// add per-instance / per-Org realm knobs (G117.2 / G117.4 scope).
type FanoutInputs struct {
	// AppName — Application CR metadata.name. The per-cluster HR
	// name is composed as `<AppName>-<clusterShort>`; truncation
	// rules apply per HRName63.
	AppName string

	// AppNamespace — Application CR metadata.namespace. The
	// rendered HR's metadata.namespace defaults to this when
	// PlacementSpec doesn't override it via a cluster-specific
	// mapping. (Per the existing controller's vCluster-pivot logic
	// the host-side HR may live in a different namespace; that's
	// stamped by the caller via WriteNamespace.)
	AppNamespace string

	// WriteNamespace — when non-empty, overrides AppNamespace on
	// the rendered HR's metadata.namespace. The G92.1 vCluster
	// pivot path (PR #2674) uses this to land each HR in the
	// vCluster's host-side namespace so Flux v2 helm-controller
	// resolves spec.kubeConfig.secretRef from the same namespace.
	WriteNamespace string

	// Topology — the resolved BCP topology key (returned by
	// ResolveTopology). Stamped on LabelTopology.
	Topology bpv1alpha1.BcpTopology

	// Variant — the resolved per-topology variant
	// (PlacementSpec + Replication + Switchover). Required —
	// FanoutHRs returns an error when nil or when its Placement is
	// nil / has zero clusters.
	Variant *bpv1alpha1.TopologyVariant

	// KubeConfigSecretFor — function returning the per-cluster
	// kubeConfig Secret name (Flux v2's
	// `spec.kubeConfig.secretRef.name`). When nil the renderer
	// emits no `spec.kubeConfig` block — i.e. the HR targets the
	// SAME cluster the reconciler runs in (legacy / mgmt-cluster-
	// local Applications). This is the G92.1 pivot seam.
	KubeConfigSecretFor func(cluster string) (secretName, secretNamespace string)

	// KubeConfigSecretKey — the data key inside the kubeConfig
	// Secret (Flux v2 `spec.kubeConfig.secretRef.key`). The loft-sh
	// vcluster exportKubeConfig convention is "config" (matches the
	// hand-proven bootstrap-kit slots 22/23/24/35/19a and the
	// per-region render path, core/controllers/pkg/render
	// manifests.go). Empty = field omitted (Flux default key
	// lookup). #3373.
	KubeConfigSecretKey string

	// Chart + ChartVersion + SourceRefName + SourceRefKind +
	// SourceRefNamespace + Values — passed through verbatim to
	// every rendered HR's spec.chart + spec.values. The caller
	// (reconciler) flattens these from the Blueprint manifest +
	// Application parameters; this package is a pure renderer.
	Chart              string
	ChartVersion       string
	SourceRefName      string
	SourceRefKind      string
	SourceRefNamespace string
	Values             map[string]interface{}

	// IntervalSeconds — HR.spec.interval. 0 means leave unset
	// (helm-controller default ~10m applies).
	IntervalSeconds int

	// OwnerLabels — extra labels merged onto every HR. The
	// reconciler typically passes org / env-type / app-uid here
	// for traceability + cascade-delete (matches
	// application_controller.go's commonLabels block).
	OwnerLabels map[string]string

	// DependsOnFor — #3370 backing-service wiring. When non-nil, the
	// renderer stamps Flux `spec.dependsOn` on each per-cluster HR
	// with the entries this function returns for that cluster. The
	// caller (reconciler) resolves the Application-level
	// `spec.dependsOn[]` refs to concrete HelmRelease names — a
	// bootstrap-owned backing instance resolves to its slot HR
	// (spec.helmRelease); a controller-managed one resolves to its
	// per-cluster `HRNameFor(<backing>, cluster)` HR. nil / empty =
	// no dependsOn block (legacy shape, byte-identical).
	DependsOnFor func(cluster string) []HRDependsOn
}

// HRDependsOn is one Flux HelmRelease spec.dependsOn entry (#3370).
type HRDependsOn struct {
	Name      string
	Namespace string
}

// FanoutHRs returns one `*unstructured.Unstructured` HelmRelease per
// cluster in the variant's PlacementSpec.Clusters[]. Order is stable:
// the input slice order. Role-stamping follows variant.Placement.Roles
// when set; falls back to RoleSingleton (single-cluster) or
// RolePassive (multi-cluster with no Roles map) defensively.
//
// Errors:
//   - in.Variant == nil
//   - in.Variant.Placement == nil
//   - len(in.Variant.Placement.Clusters) == 0
//   - in.AppName == ""
//
// All other shapes (missing Roles map, missing KubeConfigSecretFor)
// are tolerated — the renderer picks safe defaults.
func FanoutHRs(in FanoutInputs) ([]*unstructured.Unstructured, error) {
	if in.AppName == "" {
		return nil, fmt.Errorf("fanout: AppName required")
	}
	if in.Variant == nil {
		return nil, fmt.Errorf("fanout: Variant required")
	}
	if in.Variant.Placement == nil {
		return nil, fmt.Errorf("fanout: Variant.Placement required")
	}
	clusters := in.Variant.Placement.Clusters
	if len(clusters) == 0 {
		return nil, fmt.Errorf("fanout: Variant.Placement.Clusters must be non-empty")
	}

	out := make([]*unstructured.Unstructured, 0, len(clusters))
	for _, cluster := range clusters {
		hr := renderOneHR(in, cluster)
		out = append(out, hr)
	}
	return out, nil
}

// renderOneHR produces a single HelmRelease (helm.toolkit.fluxcd.io/v2)
// for one (Application × cluster) pair. Pure function.
func renderOneHR(in FanoutInputs, cluster string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetName(HRNameFor(in.AppName, cluster))
	if in.WriteNamespace != "" {
		hr.SetNamespace(in.WriteNamespace)
	} else {
		hr.SetNamespace(in.AppNamespace)
	}

	// #3375 DoD-2 — UNIFY the placement model. Before this, the fan-out
	// path rendered the passive HR BYTE-IDENTICAL to the active one
	// (differing only by the LabelRole label) while a SEPARATE,
	// parallel render path (placement.Resolve → render.mergeValues)
	// computed the `replicas:0` standby scale-down. Two divergent
	// manifest sets ran every reconcile (issue §3(b)). The standby
	// scale-down now lives HERE — the SOLE topology fan-out — so the
	// fanned-out passive HR carries `replicas:0` + the `_openova_standby`
	// marker, exactly the semantic placement.go:151's `Standby` flag
	// carried on the now-removed second path. The Continuum lease
	// governs whether the passive replica serves; the scale-down ensures
	// the standby boots cold (replicas:0) so a backend whose chart keys
	// off `.Values.replicas` does NOT run a hot second copy. The marker
	// `_openova_standby: true` is the canonical Openova standby signal
	// (docs/EPICS-1-6-unified-design.md §5) for operators that need a
	// boolean rather than an integer count (CNPG `replica.enabled`).
	role := roleFor(cluster, in.Variant.Placement)
	values := in.Values
	if role == RolePassive {
		values = withStandbyOverlay(in.Values)
	}

	// Labels — start from OwnerLabels, then overlay the
	// G117.6-mandated four (role, topology, app, cluster) so a
	// caller that accidentally seeds one of these in OwnerLabels
	// can't subvert the canonical contract.
	labels := map[string]string{}
	for k, v := range in.OwnerLabels {
		labels[k] = v
	}
	labels[LabelApp] = in.AppName
	labels[LabelTopology] = string(in.Topology)
	labels[LabelCluster] = cluster
	labels[LabelRole] = role
	hr.SetLabels(labels)

	// Spec.
	spec := map[string]interface{}{}

	// chart block.
	chartSpec := map[string]interface{}{
		"chart": in.Chart,
	}
	if in.ChartVersion != "" {
		chartSpec["version"] = in.ChartVersion
	}
	if in.SourceRefName != "" {
		ref := map[string]interface{}{
			"name": in.SourceRefName,
		}
		if in.SourceRefKind != "" {
			ref["kind"] = in.SourceRefKind
		}
		if in.SourceRefNamespace != "" {
			ref["namespace"] = in.SourceRefNamespace
		}
		chartSpec["sourceRef"] = ref
	}
	spec["chart"] = map[string]interface{}{
		"spec": chartSpec,
	}

	// Interval.
	if in.IntervalSeconds > 0 {
		spec["interval"] = fmt.Sprintf("%ds", in.IntervalSeconds)
	}

	// Values — `values` is `in.Values` for active/singleton, or the
	// standby-overlaid copy for a passive cluster (#3375 DoD-2).
	if len(values) > 0 {
		spec["values"] = values
	}

	// #3370 — Flux dependsOn wiring to the backing instance(s). The
	// edge points at the BACKING APPLICATION's HelmRelease (resolved
	// per-cluster by the caller), never at an operator chart — a
	// consumer depends on `shared-pg`, not on bp-cnpg.
	if in.DependsOnFor != nil {
		if deps := in.DependsOnFor(cluster); len(deps) > 0 {
			arr := make([]interface{}, 0, len(deps))
			for _, d := range deps {
				if d.Name == "" {
					continue
				}
				m := map[string]interface{}{"name": d.Name}
				if d.Namespace != "" {
					m["namespace"] = d.Namespace
				}
				arr = append(arr, m)
			}
			if len(arr) > 0 {
				spec["dependsOn"] = arr
			}
		}
	}

	// G92.1 #2674 kubeConfig pivot — Flux v2 helm-controller installs
	// INTO the cluster whose kubeconfig is in the referenced Secret.
	// When KubeConfigSecretFor is nil the field is omitted so the HR
	// targets the local cluster (legacy / substrate Blueprints).
	if in.KubeConfigSecretFor != nil {
		secretName, secretNamespace := in.KubeConfigSecretFor(cluster)
		if secretName != "" {
			secretRef := map[string]interface{}{
				"name": secretName,
			}
			// #3373: stamp the Secret data key when the caller
			// declares it (vcluster exportKubeConfig convention is
			// "config"; without the key Flux looks up its default
			// key and the pivot silently fails against vc-* Secrets).
			if in.KubeConfigSecretKey != "" {
				secretRef["key"] = in.KubeConfigSecretKey
			}
			kc := map[string]interface{}{
				"secretRef": secretRef,
			}
			// Flux v2 SecretReference contract: namespace is implied
			// from the HR's own namespace. We DO NOT stamp the
			// namespace into the secretRef block (Flux rejects the
			// nested field). The caller is responsible for setting
			// WriteNamespace = secretNamespace.
			_ = secretNamespace
			spec["kubeConfig"] = kc
		}
	}

	hr.Object["spec"] = spec

	// Bare creationTimestamp suppression — unstructured.SetLabels
	// already touches metadata; we leave the rest of the metadata as
	// the dynamic client + the API server fill in.
	_ = metav1.ObjectMeta{}

	return hr
}

// HRNameFor composes the per-cluster HR name. K8s DNS-1123 caps names
// at 63 chars; we truncate the combined `<app>-<cluster>` to 57 chars
// and append a 5-char hash suffix when the combined would overflow,
// matching the brief's "`<app-name>-<cluster-short>` (≤ 63 chars;
// truncate + 5-char suffix hash on overflow)" spec.
//
// The hash is sha256(app+"\x00"+cluster) hex-encoded first 5 chars —
// cheap, stable across reconciles, and collision-resistant within a
// single Sovereign.
func HRNameFor(app, cluster string) string {
	combined := fmt.Sprintf("%s-%s", app, cluster)
	if len(combined) <= HRName63 {
		return combined
	}
	h := sha256.Sum256([]byte(app + "\x00" + cluster))
	suffix := hex.EncodeToString(h[:])[:5]
	// Reserve 6 chars for "-" + 5-hash; truncate combined to 57.
	maxBody := HRName63 - 6
	if maxBody < 1 {
		// Pathological — should never happen at HRName63=63.
		maxBody = 1
	}
	truncated := combined[:maxBody]
	truncated = strings.TrimRight(truncated, "-")
	return fmt.Sprintf("%s-%s", truncated, suffix)
}

// StandbyMarker is the canonical top-level Openova standby signal
// (docs/EPICS-1-6-unified-design.md §5). Charts whose standby semantic is
// a boolean rather than a `replicas` integer (CNPG `replica.enabled`,
// Cassandra, …) read this marker. Stable contract — mirrored from
// core/controllers/pkg/render.mergeValues so the SOLE topology fan-out
// emits the SAME standby shape the now-removed parallel path did.
const StandbyMarker = "_openova_standby"

// withStandbyOverlay returns a COPY of `user` with the standby scale-down
// overlaid: `replicas: 0` (the universal Helm-chart standby signal for
// Deployment / StatefulSet kinds) + `_openova_standby: true` (the
// boolean marker for operators that key off a flag). #3375 DoD-2 — this
// is the placement.go `Standby` semantic, relocated into the topology
// fan-out so the fanned-out passive HR carries the scale-down instead of
// being byte-identical to the active HR. The input map is NEVER mutated
// (it is the Blueprint-derived shared `in.Values`); a shallow copy is
// sufficient because we only set top-level keys.
func withStandbyOverlay(user map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(user)+2)
	for k, v := range user {
		out[k] = v
	}
	// int64 (NOT a bare Go int): the rendered HR is an
	// unstructured.Unstructured that gets DeepCopy'd on the dynamic-client
	// write path. k8s.io/apimachinery's DeepCopyJSONValue only accepts
	// JSON-compatible scalars (int64/float64/string/bool) and panics on a
	// bare `int` ("cannot deep copy int"). The text/template per-region
	// path can use a plain int because it serialises to YAML, but this
	// path must stay JSON-deep-copyable.
	out["replicas"] = int64(0)
	out[StandbyMarker] = true
	return out
}

// roleFor picks the per-cluster role from the variant's Roles map,
// falling back to RoleSingleton (single-cluster variant) or
// RolePassive (multi-cluster, no map).
//
// The defensive fallbacks are NOT a substitute for a well-formed
// Blueprint — the admission webhook enforces that an
// active-hot-standby variant declares roles for every cluster in
// Placement.Clusters. These fallbacks exist so a singleton-variant
// (no roles map needed) doesn't crash the controller and a
// legacy/multi-cluster Blueprint missing roles gets a conservative
// "everything passive" treatment that surfaces in the operator
// console as obviously wrong (rather than a silent crash).
func roleFor(cluster string, p *bpv1alpha1.PlacementSpec) string {
	if p == nil {
		return RoleSingleton
	}
	if p.Roles != nil {
		if role, ok := p.Roles[cluster]; ok && role != "" {
			return role
		}
	}
	// No explicit role for this cluster.
	if len(p.Clusters) == 1 {
		return RoleSingleton
	}
	return RolePassive
}

// SortHRsForReconcile orders a fan-out slice so active HRs come
// first, then passive, then singleton. The reconciler applies HRs in
// this order so the active replica becomes Ready before the passive
// boots (avoids the cnpg-pair race where the passive replica boots
// before the active primary has WAL streaming up — brief §4 "Honor
// reconcile order").
//
// Stable within each role bucket: preserves input order for callers
// that want deterministic per-cluster ordering (e.g. for byte-equal
// reconcile idempotency).
func SortHRsForReconcile(hrs []*unstructured.Unstructured) {
	sort.SliceStable(hrs, func(i, j int) bool {
		return roleSortRank(hrs[i]) < roleSortRank(hrs[j])
	})
}

// roleSortRank — active=0, passive=1, singleton=2, unknown=3. Lower
// reconciles first.
func roleSortRank(hr *unstructured.Unstructured) int {
	if hr == nil {
		return 3
	}
	labels := hr.GetLabels()
	switch labels[LabelRole] {
	case RoleActive:
		return 0
	case RolePassive:
		return 1
	case RoleSingleton:
		return 2
	default:
		return 3
	}
}

// PerClusterStatus is the typed shape FanoutHRs callers populate when
// they want to seed `Application.status.perCluster[]` per the brief.
// We intentionally don't bind this to a typed Application status
// struct here because the application-controller's status writer is
// already on the unstructured path; the typed view is most useful in
// tests + when catalyst-api reads the status back to serve
// GET /apps/{id}.
type PerClusterStatus struct {
	Cluster string `json:"cluster"`
	Role    string `json:"role"`
	HR      string `json:"hr"`
	Status  string `json:"status,omitempty"`
}

// PerClusterStatusesFor returns a templated `perCluster` slice from a
// fan-out result, with empty `Status` fields the reconciler later
// fills in by observing each per-cluster HR's `status.conditions[
// type=Ready]`. Pure helper to keep the reconciler thin.
func PerClusterStatusesFor(hrs []*unstructured.Unstructured) []PerClusterStatus {
	out := make([]PerClusterStatus, 0, len(hrs))
	for _, hr := range hrs {
		labels := hr.GetLabels()
		out = append(out, PerClusterStatus{
			Cluster: labels[LabelCluster],
			Role:    labels[LabelRole],
			HR:      hr.GetName(),
		})
	}
	return out
}
