// G117.6 — per-cluster HelmRelease fan-out.
//
// FanoutHRs turns one resolved (Application × Topology variant) pair
// into N HelmReleases — one per cluster in the variant's
// PlacementSpec.Clusters[]. Each HR carries:
//
//   - LabelRole = active | passive | singleton (per the variant's
//     PlacementSpec.Roles map; when the map is empty, defaults to
//     singleton for a 1-cluster variant and otherwise to the role the
//     DECLARED TOPOLOGY implies for that cluster — see roleFor)
//   - LabelStandbyDelivery = remote | local-undelivered, on passive
//     legs only: whether the standby actually reached its own cluster
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
// when set; falls back to RoleSingleton (single-cluster) or, for a
// multi-cluster variant with no Roles map, to the topology-derived role
// (roleFor).
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
	// scale-down now lives HERE — the SOLE topology fan-out. The marker
	// `_openova_standby: true` is the canonical Openova standby signal
	// (docs/EPICS-1-6-unified-design.md §5) for operators that need a
	// boolean rather than an integer count (CNPG `replica.enabled`).
	//
	// #6268 — the scale-down is no longer UNCONDITIONAL. `replicas: 0`
	// is the COLD-standby semantic (rebuild-on-failover, active-passive);
	// applying it to a HOT standby contradicts the posture the operator
	// chose, because a hot standby is defined by streaming and a replica
	// with zero replicas cannot stream. Which one a passive leg gets is
	// decided by standbyIsHot() — see its doc for why DELIVERY is half
	// of that decision and not a defensive extra.
	//
	// The kubeConfig pivot is resolved FIRST because the values overlay
	// depends on it: an undelivered leg renders into the same cluster as
	// its active peer, and that changes what booting it "hot" would mean.
	role := roleFor(cluster, in.Variant.Placement, in.Topology)

	// G92.1 #2674 kubeConfig pivot — Flux v2 helm-controller installs
	// INTO the cluster whose kubeconfig is in the referenced Secret.
	// When KubeConfigSecretFor is nil, or resolves this cluster to the
	// empty string, the field is omitted so the HR targets the LOCAL
	// cluster (legacy / substrate Blueprints; the clusterregistry
	// split-side default for an unwired remote region).
	secretName, secretNamespace := "", ""
	if in.KubeConfigSecretFor != nil {
		secretName, secretNamespace = in.KubeConfigSecretFor(cluster)
	}
	delivered := secretName != ""

	values := in.Values
	if role == RolePassive {
		values = withStandbyOverlay(in.Values, standbyIsHot(in.Topology, delivered))
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
	// #6268 — record WHERE a standby leg actually landed. Stamped only
	// on passive HRs (an active / singleton HR has no standby leg), so
	// the label's presence is itself meaningful and no existing HR shape
	// gains a field it cannot explain.
	if role == RolePassive {
		if delivered {
			labels[LabelStandbyDelivery] = StandbyDeliveryRemote
		} else {
			labels[LabelStandbyDelivery] = StandbyDeliveryLocal
		}
	}
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

	// Emit the kubeConfig block for a leg that resolved one (resolution
	// itself happened above, before the values overlay).
	if delivered {
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
	// HelmRelease names are k8s object names → must be RFC-1123 DNS
	// labels: lowercase only. The cluster token can carry an
	// uppercase region marker (e.g. clusterregistry.RegionA="A" →
	// "rtz-A"), which would render an invalid name like
	// `agenity-rtz-A`. Lowercase the composed name (#4079). Labels
	// keep their original case (label values permit uppercase); only
	// the object NAME is constrained.
	combined := strings.ToLower(fmt.Sprintf("%s-%s", app, cluster))
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

// withStandbyOverlay returns a COPY of `user` with the standby overlay
// applied. #3375 DoD-2 — this is the placement.go `Standby` semantic,
// relocated into the topology fan-out so the fanned-out passive HR is
// not byte-identical to the active HR. The input map is NEVER mutated
// (it is the Blueprint-derived shared `in.Values`); a shallow copy is
// sufficient because we only set top-level keys.
//
// Both standby kinds carry `_openova_standby: true` — the marker says
// "this leg is the standby", which is true of a hot standby and a cold
// one alike, and charts whose standby semantic is a boolean rather than
// an integer count (CNPG `replica.enabled`) key off exactly that.
//
// They differ in ONE key. A COLD standby is additionally pinned to
// `replicas: 0`: it runs no process and is rebuilt from the bucket on
// failover, so a chart keying off `.Values.replicas` must not start it.
// A HOT standby is left at the declared replica count, because a hot
// standby is DEFINED by streaming from the primary and a workload scaled
// to zero cannot stream. Zeroing it produced an active-hot-standby
// Application whose standby was, in fact, cold — the posture the
// operator picked in the Catalog and the posture the platform ran
// disagreed, silently, with `status.phase: Ready` over the top (#6268).
func withStandbyOverlay(user map[string]interface{}, hot bool) map[string]interface{} {
	out := make(map[string]interface{}, len(user)+2)
	for k, v := range user {
		out[k] = v
	}
	if !hot {
		// int64 (NOT a bare Go int): the rendered HR is an
		// unstructured.Unstructured that gets DeepCopy'd on the
		// dynamic-client write path. k8s.io/apimachinery's
		// DeepCopyJSONValue only accepts JSON-compatible scalars
		// (int64/float64/string/bool) and panics on a bare `int`
		// ("cannot deep copy int"). The text/template per-region path
		// can use a plain int because it serialises to YAML, but this
		// path must stay JSON-deep-copyable.
		out["replicas"] = int64(0)
	}
	out[StandbyMarker] = true
	return out
}

// standbyIsHot reports whether a passive leg must boot HOT (a live,
// streaming replica) rather than COLD (`replicas: 0`, rebuilt on
// failover). BOTH conditions are required:
//
//  1. the resolved topology declares a hot standby — `active-hot-standby`
//     is the only posture in the enum whose standby streams;
//     `active-passive` is the cold one by definition, and a singleton /
//     active-active has no standby leg to classify.
//
//  2. the leg is actually DELIVERED to its own cluster — i.e. the
//     fan-out resolved a kubeConfig secretRef for it.
//
// Condition 2 is not defensive padding, and it is the reason this
// function takes an argument that looks unrelated to "hot". When no
// kubeConfig resolves, the renderer omits `spec.kubeConfig` and Flux
// installs the HR into the cluster the controller itself runs in — the
// SAME cluster, and the same namespace, as the active peer. Booting
// that leg "hot" would not produce a standby; it would install a second
// full primary next to the first, under a name that says standby. So
// while delivery is unmet the leg stays cold, which is inert, and
// LabelStandbyDelivery records the shortfall on the object rather than
// letting a duplicate be mistaken for a replica.
//
// The consequence worth stating plainly: turning an undelivered standby
// hot requires FIXING DELIVERY, not relaxing this predicate.
func standbyIsHot(topology bpv1alpha1.BcpTopology, delivered bool) bool {
	return topology == bpv1alpha1.BcpActiveHotStandby && delivered
}

// roleFor picks the per-cluster role from the variant's Roles map,
// falling back to RoleSingleton (single-cluster variant) or, for a
// multi-cluster variant with no usable Roles entry, to the role the
// DECLARED TOPOLOGY implies for that cluster.
//
// The declared roles always win. The fallbacks are NOT a substitute for
// a well-formed Blueprint — the admission webhook enforces that an
// active-hot-standby variant declares roles for every cluster in
// Placement.Clusters — they exist so a singleton variant (which needs no
// roles map) doesn't crash the controller and a legacy / multi-cluster
// Blueprint missing roles still renders a coherent placement.
//
// #6268 — that last clause is the change. The previous fallback returned
// RolePassive for EVERY cluster of a multi-cluster variant with no roles
// map, on the reasoning that "everything passive" is conservative and
// would look obviously wrong in the console. It is not conservative and
// it does not look wrong: passive is the role that triggers the standby
// overlay, so an active-hot-standby Application whose Blueprint omitted
// the map rendered TWO standbys and NO primary — an app with nothing
// serving, reported with a per-cluster status that reads as a normal
// multi-region rollout. A fallback that can produce a placement with no
// active member is not a safe default; it is a different failure.
//
// The topology already states the answer, so the fallback reads it
// instead of guessing:
//
//	active-active           → every cluster active (no standby exists)
//	active-hot-standby      → first declared cluster active, rest passive
//	active-passive          → first declared cluster active, rest passive
//	singleton / unset       → passive (unchanged; a multi-cluster
//	                          singleton is malformed and the caller's
//	                          topology resolution is the thing to fix)
//
// "First declared cluster is the primary" is the same convention the
// Blueprint catalog already authors to (bp-postgres declares
// `clusters: [rtz-A, rtz-B]` with `roles: {rtz-A: active, rtz-B:
// passive}`), so the derived answer agrees with the declared one
// wherever both exist.
func roleFor(cluster string, p *bpv1alpha1.PlacementSpec, topology bpv1alpha1.BcpTopology) string {
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
	switch topology {
	case bpv1alpha1.BcpActiveActive:
		return RoleActive
	case bpv1alpha1.BcpActiveHotStandby, bpv1alpha1.BcpActivePassive:
		if len(p.Clusters) > 0 && p.Clusters[0] == cluster {
			return RoleActive
		}
		return RolePassive
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

	// StandbyDelivery mirrors LabelStandbyDelivery for the passive legs
	// (empty for active / singleton, which have no standby leg). #6268 —
	// without it, a standby that never left the primary region is
	// reported here identically to one that landed in its own region:
	// same cluster ID, same role, same Ready status, because the HR IS
	// Ready — it installed successfully, just not where the placement
	// says. Readers that need "is this Application actually in two
	// regions?" cannot answer it from cluster+role+status alone.
	StandbyDelivery string `json:"standbyDelivery,omitempty"`
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
			Cluster:         labels[LabelCluster],
			Role:            labels[LabelRole],
			HR:              hr.GetName(),
			StandbyDelivery: labels[LabelStandbyDelivery],
		})
	}
	return out
}
