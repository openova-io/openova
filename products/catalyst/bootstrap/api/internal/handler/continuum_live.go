// Package handler — continuum_live.go: the GENERIC live-DR-record layer
// (#3492 / #3375 / #3648).
//
// Problem (founder review #7, 2026-06-16): the AppDetail "Disaster
// Recovery → Switchover" panel showed, for EVERY app, the placeholder
// "No live Continuum record for <app> yet". The cross-region DATA path
// is proven (region-kill on hw128: flipping the cnpg-pair Cluster
// spec.replica.enabled promotes the replica in ~3s, zero data loss), but
// nothing produced a *live DR record* the panel could render, and nothing
// drove the operator-facing switchover off that record. The Continuum CR
// is only ever minted by a chart (platform/cnpg-pair, default-OFF) or a
// QA fixture — so for an arbitrary app like bp-grafana, GET
// /continuums/dr-<app> returned 404 and the panel fell to the placeholder.
//
// This file closes that gap GENERICALLY — keyed on the Application's
// declared topology (active-hot-standby) + switchover mechanism
// (cnpg-pair), NEVER on the app name (founder #4: "you cannot hardcode
// things per application, all concepts are applicable for all"):
//
//   - deriveLiveContinuumRecord() builds a Continuum record from the LIVE
//     cnpg cluster-pair backing the app — real primaryRegion /
//     replicaRegion (read off the two Cluster CRs' openova.io/region
//     labels), real replicationHealthy (the replica Cluster's Ready
//     condition), real lag, real currentPrimary. It is the same shape the
//     real Continuum CR returns, so the panel's StatusPanel renders it
//     unchanged. When there is genuinely no 2-region cnpg-pair for the
//     app, it returns (nil, false) and the honest 404 / placeholder stands.
//
//   - The former liveSwitchoverViaCNPGPair() (an imperative cnpg flip
//     behind the Switchover button) is REMOVED — desired-state model,
//     2026-11-07: the operator declares the Primary in placement and the
//     continuum-controller converges. catalyst-api only READS DR state.
//
// Why a local cnpg reader rather than importing the controller's:
// catalyst-api intentionally avoids importing core/controllers/continuum/...
// (it drags in the whole controller-runtime dep tree — see the same note
// at continuum.go:80). The cnpg Cluster contract is a small, stable set
// of GVR + label + field paths; we mirror exactly the subset
// core/controllers/continuum/internal/cnpg/status.go uses (ClusterGVR,
// the two pair labels, status.currentPrimary, spec.replica.enabled,
// status.conditions[Ready], the openova.io/region label, and the
// cnpg.io/cluster.primary cordon annotation). If the contract drifts the
// dynamic client returns NotFound on first call — fail-closed, never a
// fabricated record.
package handler

import (
	"context"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ── cnpg Cluster contract (mirrors core/controllers/continuum/internal/cnpg) ──

// cnpgClusterGVR is the CNPG Cluster CR GroupVersionResource. Namespaced.
// Mirrors cnpg.ClusterGVR.
var cnpgClusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

const (
	// cnpgPairLabel names the cluster-pair every half carries. Mirrors
	// cnpg.PairLabel. The bp-cnpg-pair chart stamps it from the chart
	// fullname; the bp-postgres-shared chart does the same for shared PG.
	cnpgPairLabel = "catalyst.openova.io/cnpg-pair"

	// cnpgRoleLabel + values — primary | replica. Mirrors cnpg.PairRoleLabel.
	cnpgRoleLabel   = "openova.io/cnpg-role"
	cnpgRolePrimary = "primary"
	cnpgRoleReplica = "replica"

	// cnpgRegionLabel — the host region each Cluster half is pinned to
	// (node-affinity term). Both halves carry it (primary-cluster.yaml:29,
	// replica-cluster.yaml:45). This is how we derive primaryRegion /
	// replicaRegion generically — from live cluster placement, not config.
	cnpgRegionLabel = "openova.io/region"

	// cnpgAppLabel — when a cnpg-pair is provisioned as the state store of
	// a specific Application (the Organization / per-app path), the pair
	// carries this label so we can associate it back without a name match.
	// Optional: pairs that don't carry it are matched by name convention.
	cnpgAppLabel = "openova.io/application"

	// cnpgPrimaryAnnotation — the cordon annotation the CNPG operator
	// inspects. Mirrors cnpg.PrimaryAnnotation.
	cnpgPrimaryAnnotation = "cnpg.io/cluster.primary"
)

// cnpgPairState is the parsed live view of a cnpg cluster-pair, used to
// build the Continuum DR record and to drive the switchover flip.
type cnpgPairState struct {
	// PairName is the value of the cnpgPairLabel shared by both halves.
	PairName  string
	Namespace string

	PrimaryClusterName string
	ReplicaClusterName string

	// PrimaryRegion / ReplicaRegion derived from each half's
	// openova.io/region label. Empty when the label is absent (single-node
	// dev pairs) — the record still renders, the region badge shows "—".
	PrimaryRegion string
	ReplicaRegion string

	// CurrentPrimary is the Pod inside the primary Cluster currently
	// serving writes (status.currentPrimary). Surfaced for parity with the
	// controller's status; not load-bearing for the panel.
	CurrentPrimary string

	// ReplicaEnabled reflects spec.replica.enabled on the replica half —
	// true in steady state (it follows WAL), false after a switchover
	// promoted it. The inverse holds on the primary half.
	ReplicaEnabled bool

	// ReplicationHealthy = the replica Cluster reports Ready=True AND it is
	// in replica mode (actively following). This is the honest health
	// signal the panel's lag bar / phase pill key off.
	ReplicationHealthy bool

	// LagSeconds — best-effort replication lag. CNPG does not expose a flat
	// integer in every version; 0 when unavailable (same caveat as the
	// controller's cnpg.parseStatus).
	LagSeconds int

	// PrimaryReady / ReplicaReady — each half's Ready condition.
	PrimaryReady bool
	ReplicaReady bool

	// SyncReplication reflects whether the PRIMARY half declares synchronous
	// replication (spec.postgresql.synchronous present) — the RPO=0 durability
	// posture that makes CNPG render `synchronous_standby_names = FIRST N (...)`
	// (sync_state=sync in pg_stat_replication). False for async/lab pairs. #4923.
	SyncReplication bool

	// StandbyAvailable is the cnpgStandbyAvailable verdict on the replica
	// half: Ready=True AND (when reported) >=1 ready instance. Distinct from
	// ReplicationHealthy — a promoted (post-switchover) standby is available
	// but no longer following. False = the standby-absent condition the
	// replication-status endpoint must surface explicitly (#4923/#4901).
	StandbyAvailable bool
}

// findCNPGPairForApp discovers the cnpg cluster-pair backing an
// Application, generically. Resolution order (all label-driven, no
// hardcoded app names):
//
//  1. A pair labelled openova.io/application=<app> (the per-app state-store
//     path) — the strongest association, valid even cluster-wide.
//  2. Otherwise, WITHIN the app's namespace only, the lone cnpg-pair
//     present (the common case: one Org Environment → one shared
//     cnpg-pair). When more than one pair lives in the namespace and none
//     is app-labelled we pick the lexically-first deterministically.
//
// 🔒 TENANT ISOLATION: the lexically-first fallback is applied ONLY when
// the list was scoped to a single namespace. When namespace is empty we
// list cluster-wide (chroot-friendly) but then accept ONLY an
// app-labelled match — we NEVER pick a lexically-first pair across
// namespaces, because that could resolve to a DIFFERENT Organization's
// cnpg-pair and (on the switchover WRITE path) flip another tenant's
// database. A cross-org guess is a correctness + isolation breach, so we
// return (nil,nil) instead.
//
// Returns (nil, nil) — NOT an error — when no pair can be SAFELY resolved;
// the caller then leaves the honest 404 in place.
func (h *Handler) findCNPGPairForApp(
	ctx context.Context,
	client dynamic.Interface,
	appName, namespace string,
) (*cnpgPairState, error) {
	// List candidate Cluster CRs. When namespace is known, scope to it;
	// else list cluster-wide (chroot-friendly, mirrors getContinuumCR) —
	// but cluster-wide results are only used for an app-labelled match.
	nsScoped := strings.TrimSpace(namespace) != ""
	var (
		list *unstructured.UnstructuredList
		err  error
	)
	ri := client.Resource(cnpgClusterGVR)
	if nsScoped {
		list, err = ri.Namespace(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: cnpgPairLabel,
		})
	} else {
		list, err = ri.Namespace("").List(ctx, metav1.ListOptions{
			LabelSelector: cnpgPairLabel,
		})
	}
	if err != nil {
		// A missing CRD (no cnpg installed) surfaces as NotFound/NoMatch —
		// treat as "no pair", not a hard error, so the panel placeholder
		// stands rather than the panel erroring.
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if list == nil || len(list.Items) == 0 {
		return nil, nil
	}

	// Group halves by pair name.
	type halves struct{ primary, replica *unstructured.Unstructured }
	byPair := map[string]*halves{}
	appLabelled := map[string]bool{}
	pairNames := []string{}
	for i := range list.Items {
		it := &list.Items[i]
		labels := it.GetLabels()
		pair := labels[cnpgPairLabel]
		if pair == "" {
			continue
		}
		if _, ok := byPair[pair]; !ok {
			byPair[pair] = &halves{}
			pairNames = append(pairNames, pair)
		}
		switch labels[cnpgRoleLabel] {
		case cnpgRolePrimary:
			byPair[pair].primary = it
		case cnpgRoleReplica:
			byPair[pair].replica = it
		}
		if appName != "" && labels[cnpgAppLabel] == appName {
			appLabelled[pair] = true
		}
	}
	sort.Strings(pairNames)

	// Prefer an app-labelled pair (safe even cluster-wide). Only when the
	// list was namespace-scoped do we fall back to the lone /
	// lexically-first pair — a cluster-wide lexical pick could cross
	// Organization boundaries (see the TENANT ISOLATION note above).
	var chosen string
	for _, p := range pairNames {
		if appLabelled[p] {
			chosen = p
			break
		}
	}
	if chosen == "" && nsScoped && len(pairNames) > 0 {
		chosen = pairNames[0]
	}
	if chosen == "" {
		return nil, nil
	}
	hv := byPair[chosen]
	// Require BOTH halves — a 2-region active-hot-standby pair. A lone
	// primary (single-region) is NOT a DR record; honest 404 stands.
	if hv == nil || hv.primary == nil || hv.replica == nil {
		return nil, nil
	}

	st := &cnpgPairState{
		PairName:           chosen,
		Namespace:          hv.primary.GetNamespace(),
		PrimaryClusterName: hv.primary.GetName(),
		ReplicaClusterName: hv.replica.GetName(),
		PrimaryRegion:      hv.primary.GetLabels()[cnpgRegionLabel],
		ReplicaRegion:      hv.replica.GetLabels()[cnpgRegionLabel],
	}
	st.CurrentPrimary, _, _ = unstructured.NestedString(hv.primary.Object, "status", "currentPrimary")
	st.ReplicaEnabled, _, _ = unstructured.NestedBool(hv.replica.Object, "spec", "replica", "enabled")
	st.PrimaryReady = cnpgClusterReady(hv.primary)
	st.ReplicaReady = cnpgClusterReady(hv.replica)
	st.LagSeconds = cnpgClusterLag(hv.replica)
	st.SyncReplication = cnpgClusterSynchronous(hv.primary)
	st.StandbyAvailable = cnpgStandbyAvailable(hv.replica)
	// Healthy = the standby half is Ready and still in replica mode
	// (actively following WAL). If replica.enabled flipped to false the
	// pair is mid/post-switchover — not "healthy steady-state".
	st.ReplicationHealthy = st.ReplicaReady && st.ReplicaEnabled
	return st, nil
}

// cnpgClusterReady reports the Cluster's Ready condition == True.
// Mirrors cnpg.parseStatus's conditions scan.
func cnpgClusterReady(cr *unstructured.Unstructured) bool {
	conds, found, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _, _ := unstructured.NestedString(cm, "type")
		s, _, _ := unstructured.NestedString(cm, "status")
		if t == "Ready" {
			return s == "True"
		}
	}
	return false
}

// cnpgClusterLag returns best-effort replication lag seconds. CNPG does
// not expose a uniform flat integer across versions; we try the same
// sub-paths cnpg.parseStatus does and return 0 when absent.
func cnpgClusterLag(cr *unstructured.Unstructured) int {
	if lag, found, _ := unstructured.NestedInt64(cr.Object, "status", "lag"); found {
		return int(lag)
	}
	if lag, found, _ := unstructured.NestedInt64(cr.Object, "status", "replication", "lagSeconds"); found {
		return int(lag)
	}
	return 0
}

// cnpgClusterSynchronous reports whether the PRIMARY half of a cnpg-pair
// declares synchronous replication. CNPG renders
// `synchronous_standby_names = FIRST N ("<replica>")` — the RPO=0 durability
// posture (sync_state=sync in pg_stat_replication) — ONLY when
// spec.postgresql.synchronous is present. The bp-cnpg-pair chart sets that
// block (method:first + number:N + standbyNamesPre:[<replica>]) when
// replication.mode=sync and OMITS it for the forensic "async" mode. Mirrors
// the chart contract at platform/cnpg-pair/chart/templates/primary-cluster.yaml.
// #4923.
func cnpgClusterSynchronous(primary *unstructured.Unstructured) bool {
	if primary == nil {
		return false
	}
	sync, found, err := unstructured.NestedMap(primary.Object, "spec", "postgresql", "synchronous")
	if err != nil || !found || len(sync) == 0 {
		return false
	}
	// A synchronous block with a positive `number` forces N synchronous
	// standbys — the unambiguous sync signal.
	if n, ok, _ := unstructured.NestedInt64(sync, "number"); ok && n > 0 {
		return true
	}
	// A `method` (first | any) present is likewise sufficient.
	if m, _, _ := unstructured.NestedString(sync, "method"); strings.TrimSpace(m) != "" {
		return true
	}
	// Any non-empty synchronous block declares synchronous intent.
	return true
}

// cnpgClusterReadyInstances reports status.readyInstances (the count of a
// CNPG Cluster's Pods currently Ready) and whether the field was present.
// CNPG drops it to 0 when every instance of a Cluster is down — the
// region-kill drill (#4901: region-b nodes cordoned, replica Pods deleted →
// 0 ready). A present-but-zero reading is a standby-absent signal; an absent
// field means the Cluster status is too old/partial to judge on this axis
// (the caller then falls back to the Ready condition alone).
func cnpgClusterReadyInstances(cr *unstructured.Unstructured) (int, bool) {
	v, found, err := unstructured.NestedFieldNoCopy(cr.Object, "status", "readyInstances")
	if err != nil || !found {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// cnpgStandbyAvailable reports whether the replica (standby) half of a cnpg
// cluster-pair is genuinely serving as a hot-standby: its Cluster reports
// Ready=True AND (when the field is present) at least one instance is Ready.
//
// A Ready-but-LAGGING standby is still AVAILABLE — replication lag is a
// separate axis (status.replicationLagSeconds) and must NEVER raise a
// standby-absent alarm (the issue's explicit no-false-alarm rule). Only an
// unreachable / down standby (not Ready, or zero ready instances) counts as
// absent.
func cnpgStandbyAvailable(replica *unstructured.Unstructured) bool {
	if !cnpgClusterReady(replica) {
		return false
	}
	if inst, found := cnpgClusterReadyInstances(replica); found {
		return inst >= 1
	}
	return true
}

// cnpgStandbyState is the resolved availability of the REQUIRED synchronous
// hot-standby a cnpg-pair Continuum CR references via spec.cnpgPair.
type cnpgStandbyState struct {
	PairName       string
	ReplicaCluster string
	ReplicaRegion  string
	Available      bool
}

// standbyRegionLabel renders a human region for the condition message,
// falling back to a generic label when the replica Cluster carries no
// openova.io/region label (single-node dev pairs).
func standbyRegionLabel(region string) string {
	if strings.TrimSpace(region) == "" {
		return "the standby region"
	}
	return region
}

// appNameFromContinuumName strips the conventional `dr-` prefix the UI
// applies (TopologyTab passes continuumName = `dr-<applicationName>` when
// AppDetail doesn't supply an explicit name). Used to map the panel's CR
// name back to the Application when deriving the live record / driving the
// live switchover. Returns the input unchanged when it carries no prefix.
func appNameFromContinuumName(continuumName string) string {
	return strings.TrimPrefix(continuumName, "dr-")
}
