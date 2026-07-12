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
//   - liveSwitchoverViaCNPGPair() drives the SAME promotion mechanism the
//     region-kill walk proved — flip spec.replica.enabled on the two
//     Cluster halves (+ the cordon annotation) — so the Switchover button
//     actually does something when no Continuum CR exists yet but a live
//     pair does. This REPLACES the synthesized "completed in 60s" theater
//     for the no-CR case (anti-pattern the brief explicitly bans).
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
	"fmt"
	"sort"
	"strings"
	"time"

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

// cnpgPairStandbyForContinuum resolves the standby (replica) availability of
// the cnpg cluster-pair a Continuum CR names in spec.cnpgPair. It returns
// (state, true) ONLY when the CR references a cnpg pair AND that pair's
// replica-role Cluster half is present on the cluster (so a determination
// can be made). It returns (_, false) — leave the observed status untouched
// — when:
//
//   - the CR carries no spec.cnpgPair: the dr-spine / openbao-raft continuums
//     have no synchronous cnpg pair, so the SyncRep standby-absent branch is
//     not theirs (their own standby posture is surfaced elsewhere), or
//   - the replica-role Cluster half cannot be listed/found: provisioning in
//     flight, or the client cannot see the pair — being conservative here
//     avoids false-alarming a half-provisioned pair.
//
// The replica-role Cluster PRESENT-but-not-Ready is the proven region-kill
// state (#4901) — that is exactly the case this returns Available=false for.
func (h *Handler) cnpgPairStandbyForContinuum(
	ctx context.Context,
	client dynamic.Interface,
	cr *unstructured.Unstructured,
) (cnpgStandbyState, bool) {
	pairName, _, _ := unstructured.NestedString(cr.Object, "spec", "cnpgPair", "name")
	if strings.TrimSpace(pairName) == "" {
		return cnpgStandbyState{}, false
	}
	pairNS, _, _ := unstructured.NestedString(cr.Object, "spec", "cnpgPair", "namespace")
	if strings.TrimSpace(pairNS) == "" {
		pairNS = cr.GetNamespace()
	}
	list, err := client.Resource(cnpgClusterGVR).Namespace(pairNS).List(ctx, metav1.ListOptions{
		LabelSelector: cnpgPairLabel + "=" + pairName,
	})
	if err != nil || list == nil {
		return cnpgStandbyState{}, false
	}
	var replica *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetLabels()[cnpgRoleLabel] == cnpgRoleReplica {
			replica = &list.Items[i]
			break
		}
	}
	if replica == nil {
		// No replica-role half resolvable — cannot judge; leave untouched.
		return cnpgStandbyState{}, false
	}
	return cnpgStandbyState{
		PairName:       pairName,
		ReplicaCluster: replica.GetName(),
		ReplicaRegion:  replica.GetLabels()[cnpgRegionLabel],
		Available:      cnpgStandbyAvailable(replica),
	}, true
}

// augmentContinuumStandbyStatus cross-checks the live cnpg-pair standby for a
// cnpg-pair-backed Continuum CR and reflects a lost REQUIRED synchronous
// hot-standby into the OBSERVED status the DR panel renders (#4901).
//
// THE GAP THIS CLOSES. The continuum-controller owns the CR's stored status
// (ADR-0001 §2.7) and tracks the witness lease / primary correctly, but it
// derives phase purely from lease-held-ness (patchStatusFromCR). A lost
// required-sync standby — region-b replica unreachable, writes stalling on
// SyncRep — therefore leaves the cnpg-pair Continuum pinned phase=Healthy,
// replicationLagSeconds=0, switchoverInProgress=false (proven in the G12
// region-kill drill on hw232). An operator watching the cnpg-pair Continuum
// would not see that RPO=0 durability is at risk. This augments the READ
// projection (it does NOT write the CR — no second status writer fighting
// the controller) so the panel surfaces the standby-absent condition,
// reusing the same live replica-readiness signal deriveLiveContinuumRecord
// already computes for the no-CR path.
//
// Invariants:
//   - ONLY cnpg-pair-backed continuums (spec.cnpgPair present) with a
//     resolvable replica half are touched — dr-spine/raft continuums are
//     left untouched.
//   - lease/primary tracking (leaseHolder / currentPrimary /
//     replicationLagSeconds) is preserved verbatim.
//   - phase is only nudged Healthy→Degraded — never clobbering a FailedOver /
//     SwitchingOver / already-Degraded phase.
//   - a Ready-but-lagging standby is NOT flagged (lag rides its own field).
func (h *Handler) augmentContinuumStandbyStatus(
	ctx context.Context,
	client dynamic.Interface,
	cr *unstructured.Unstructured,
	status map[string]interface{},
) map[string]interface{} {
	st, ok := h.cnpgPairStandbyForContinuum(ctx, client, cr)
	if !ok {
		return status
	}
	if status == nil {
		status = map[string]interface{}{}
	}
	now := h.continuumNow().UTC().Format(time.RFC3339)
	status["standbyAvailable"] = st.Available
	if st.Available {
		status["hotStandbyAbsent"] = false
		setContinuumStatusCondition(status, now, "StandbyAvailable", "True", "StandbyReachable",
			fmt.Sprintf("required synchronous hot-standby %s (cnpg-pair replica cluster %q) is reachable and following the primary",
				standbyRegionLabel(st.ReplicaRegion), st.ReplicaCluster))
		return status
	}
	// Standby unreachable — surface the Degraded / standby-absent posture.
	if phase, _ := status["phase"].(string); phase == "" || phase == "Healthy" {
		status["phase"] = "Degraded"
	}
	status["hotStandbyAbsent"] = true
	setContinuumStatusCondition(status, now, "StandbyAvailable", "False", "StandbyUnreachable",
		fmt.Sprintf("required synchronous hot-standby %s (cnpg-pair replica cluster %q) is unreachable; synchronous replication has no standby to acknowledge commits so writes stall and RPO=0 durability is at risk",
			standbyRegionLabel(st.ReplicaRegion), st.ReplicaCluster))
	return status
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

// setContinuumStatusCondition upserts a K8s-style condition into
// status.conditions[] (append, or replace the existing entry of the same
// type). It mutates the OBSERVED status map (a deep copy of the CR's status)
// — never the CR itself.
func setContinuumStatusCondition(status map[string]interface{}, now, condType, condStatus, reason, message string) {
	cond := map[string]interface{}{
		"type":               condType,
		"status":             condStatus,
		"reason":             reason,
		"message":            message,
		"lastTransitionTime": now,
	}
	conds, _ := status["conditions"].([]interface{})
	for i := range conds {
		if m, ok := conds[i].(map[string]interface{}); ok {
			if t, _ := m["type"].(string); t == condType {
				conds[i] = cond
				status["conditions"] = conds
				return
			}
		}
	}
	status["conditions"] = append(conds, cond)
}

// deriveLiveContinuumRecord builds a Continuum DR record from the live
// cnpg cluster-pair backing the app, in the EXACT continuumGetResponse
// shape the AppDetail DR panel reads. Returns (record, true) when a real
// 2-region pair exists; (nil, false) otherwise (honest 404 / placeholder).
//
// The record is entirely derived from live cluster state — it is NOT a
// fabricated/placeholder document. spec mirrors what a chart-seeded
// Continuum CR would carry (applicationRef, primaryRegion,
// hotStandbyRegions, cnpgPair ref, mechanism: cnpg-pair); status carries
// the live phase / primaryRegion / replicaRegion / replicationHealthy /
// replicationLagSeconds the panel renders. The record's `name` follows
// the same dr-<app> convention the UI derives so a later chart-seeded CR
// is a drop-in replacement.
func (h *Handler) deriveLiveContinuumRecord(
	ctx context.Context,
	client dynamic.Interface,
	appName, namespace string,
) (*continuumGetResponse, bool) {
	st, err := h.findCNPGPairForApp(ctx, client, appName, namespace)
	if err != nil || st == nil {
		return nil, false
	}
	// A genuine 2-region active-hot-standby pair requires two DISTINCT
	// regions. If both halves report the same region label (or both empty)
	// we cannot honestly claim cross-region DR — leave the placeholder.
	// (A kom4dc 2-VPC mimic still pins distinct openova.io/region labels
	// per zone, so this holds there too.)
	if st.PrimaryRegion == "" || st.ReplicaRegion == "" || st.PrimaryRegion == st.ReplicaRegion {
		return nil, false
	}

	phase := "Healthy"
	if !st.ReplicationHealthy {
		// Replica not Ready, or already promoted (mid/post-switchover).
		if !st.ReplicaEnabled {
			phase = "FailedOver"
		} else {
			phase = "Degraded"
		}
	}

	rec := &continuumGetResponse{
		Name:      "dr-" + appName,
		Namespace: st.Namespace,
		// No UID — this is a derived (live) record, not a stored CR. The
		// empty UID is the honest signal to any consumer that this is the
		// live projection, replaced verbatim once a Continuum CR is seeded.
		UID: "",
		Spec: map[string]interface{}{
			"applicationRef":    appName,
			"primaryRegion":     st.PrimaryRegion,
			"hotStandbyRegions": []interface{}{st.ReplicaRegion},
			"cnpgPair": map[string]interface{}{
				"name":      st.PairName,
				"namespace": st.Namespace,
			},
			"switchover": map[string]interface{}{
				"mechanism": "cnpg-pair",
			},
			// Marks the record as the live projection (vs a reconciled CR)
			// so the FE / future consumers can distinguish without guessing
			// off the empty UID.
			"source": "live-cnpg-pair",
		},
		Status: map[string]interface{}{
			"phase":                 phase,
			"primaryRegion":         st.PrimaryRegion,
			"replicaRegion":         st.ReplicaRegion,
			"replicationHealthy":    st.ReplicationHealthy,
			"replicationLagSeconds": int64(st.LagSeconds),
			"currentPrimary":        st.CurrentPrimary,
			"cnpgPair":              st.PairName,
			// #4923 — the live synchronous posture (RPO=0 when true), read off
			// the primary half's spec.postgresql.synchronous. Consumed by
			// liveReplicationStatusFromCNPGPair to report syncState honestly.
			"syncReplication": st.SyncReplication,
			// #4923/#4901 — the VERIFIED standby-leg availability (replica half
			// Ready + >=1 ready instance). false = the explicit standby-absent
			// condition; consumed by liveReplicationStatusFromCNPGPair.
			"standbyAvailable": st.StandbyAvailable,
			"observedAt":       h.continuumNow().UTC().Format(time.RFC3339),
		},
	}
	return rec, true
}

// liveSwitchoverViaCNPGPair drives the proven region-kill promotion on the
// live cnpg cluster-pair backing the app, when no Continuum CR exists but a
// real 2-region pair does. It performs the SAME two patches the
// continuum-controller's cnpgPromoter (and the hw128 region-kill walk)
// perform:
//
//  1. cordon the old primary (annotation cnpg.io/cluster.primary =
//     "switchover-pending") so it stops accepting writes,
//  2. flip spec.replica.enabled — true on the old primary (it becomes the
//     follower), false on the replica (it is promoted).
//
// targetRegion selects which half becomes primary; when empty the replica
// half is promoted (the only standby). Returns the resolved from/to
// regions on success. This is a REAL state change, not a synthesized
// response.
func (h *Handler) liveSwitchoverViaCNPGPair(
	ctx context.Context,
	client dynamic.Interface,
	appName, namespace, targetRegion string,
) (fromRegion, toRegion string, err error) {
	st, ferr := h.findCNPGPairForApp(ctx, client, appName, namespace)
	if ferr != nil {
		return "", "", fmt.Errorf("find cnpg-pair: %w", ferr)
	}
	if st == nil {
		return "", "", errNoLivePair
	}
	if st.PrimaryRegion == "" || st.ReplicaRegion == "" || st.PrimaryRegion == st.ReplicaRegion {
		return "", "", errNoLivePair
	}
	// Resolve direction. The replica half is the promotion target; the
	// primary half is the current primary. A targetRegion that names the
	// current primary is a no-op (the caller surfaces 409 upstream).
	from := st.PrimaryRegion
	to := st.ReplicaRegion
	if strings.TrimSpace(targetRegion) != "" && targetRegion != to {
		if targetRegion == from {
			return from, to, errSwitchoverNoop
		}
		// targetRegion names neither half — reject rather than guess.
		return from, to, fmt.Errorf("targetRegion %q matches neither the primary (%s) nor the standby (%s) region of cnpg-pair %q", targetRegion, from, to, st.PairName)
	}
	// Idempotency guard: if the standby half is already promoted (its
	// spec.replica.enabled is false — it stopped following WAL), the pair
	// is mid/post-switchover. Promoting again would ping-pong the primary
	// back. Treat a bare or to-targeted switchover as a no-op so a
	// double-click can't silently double-switch (cleanup-review finding).
	if !st.ReplicaEnabled {
		return from, to, errSwitchoverNoop
	}

	ns := st.Namespace
	// Step 1 — cordon the old primary.
	if err := h.cnpgSetPrimaryAnnotation(ctx, client, ns, st.PrimaryClusterName, "switchover-pending"); err != nil {
		return from, to, fmt.Errorf("cordon old primary: %w", err)
	}
	// Step 2a — old primary becomes the follower.
	if err := h.cnpgSetReplicaEnabled(ctx, client, ns, st.PrimaryClusterName, true); err != nil {
		// Roll back the cordon so we don't strand the old primary
		// cordoned-but-still-primary (a blocked writer = write outage).
		_ = h.cnpgClearPrimaryAnnotation(ctx, client, ns, st.PrimaryClusterName)
		return from, to, fmt.Errorf("demote old primary: %w", err)
	}
	// Step 2b — replica is promoted (stops following WAL).
	if err := h.cnpgSetReplicaEnabled(ctx, client, ns, st.ReplicaClusterName, false); err != nil {
		// Best-effort rollback: restore the old primary as the writer
		// (replica.enabled=false) AND clear its cordon, so the pair is left
		// with a functioning, writable primary rather than stranded
		// cordoned-but-primary (correctness-review finding).
		_ = h.cnpgSetReplicaEnabled(ctx, client, ns, st.PrimaryClusterName, false)
		_ = h.cnpgClearPrimaryAnnotation(ctx, client, ns, st.PrimaryClusterName)
		return from, to, fmt.Errorf("promote standby: %w", err)
	}
	// Clear the cordon on the (now-former) primary; idempotent.
	_ = h.cnpgClearPrimaryAnnotation(ctx, client, ns, st.PrimaryClusterName)
	return from, to, nil
}

// errNoLivePair is returned when there is no live 2-region cnpg-pair to
// act on — the honest signal to fall through to a real error (NOT a
// fabricated "completed" response).
var errNoLivePair = fmt.Errorf("no live 2-region cnpg-pair backing this application")

// errSwitchoverNoop — the target region is already primary.
var errSwitchoverNoop = fmt.Errorf("target region is already primary")

// cnpgSetReplicaEnabled patches spec.replica.enabled on a Cluster CR.
// Mirrors cnpg.Reader.SetReplicaEnabled.
func (h *Handler) cnpgSetReplicaEnabled(
	ctx context.Context,
	client dynamic.Interface,
	namespace, name string,
	enabled bool,
) error {
	ri := client.Resource(cnpgClusterGVR).Namespace(namespace)
	cr, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", namespace, name, err)
	}
	if err := unstructured.SetNestedField(cr.Object, enabled, "spec", "replica", "enabled"); err != nil {
		return fmt.Errorf("set replica.enabled: %w", err)
	}
	if _, err := ri.Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s/%s: %w", namespace, name, err)
	}
	return nil
}

// cnpgSetPrimaryAnnotation sets the cordon annotation. Mirrors
// cnpg.Reader.SetPrimaryAnnotation.
func (h *Handler) cnpgSetPrimaryAnnotation(
	ctx context.Context,
	client dynamic.Interface,
	namespace, name, value string,
) error {
	ri := client.Resource(cnpgClusterGVR).Namespace(namespace)
	cr, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", namespace, name, err)
	}
	ann := cr.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[cnpgPrimaryAnnotation] = value
	cr.SetAnnotations(ann)
	if _, err := ri.Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s/%s: %w", namespace, name, err)
	}
	return nil
}

// cnpgClearPrimaryAnnotation removes the cordon annotation. Idempotent.
// Mirrors cnpg.Reader.ClearPrimaryAnnotation.
func (h *Handler) cnpgClearPrimaryAnnotation(
	ctx context.Context,
	client dynamic.Interface,
	namespace, name string,
) error {
	ri := client.Resource(cnpgClusterGVR).Namespace(namespace)
	cr, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", namespace, name, err)
	}
	ann := cr.GetAnnotations()
	if ann == nil {
		return nil
	}
	if _, ok := ann[cnpgPrimaryAnnotation]; !ok {
		return nil
	}
	delete(ann, cnpgPrimaryAnnotation)
	cr.SetAnnotations(ann)
	if _, err := ri.Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s/%s: %w", namespace, name, err)
	}
	return nil
}

// appNameFromContinuumName strips the conventional `dr-` prefix the UI
// applies (TopologyTab passes continuumName = `dr-<applicationName>` when
// AppDetail doesn't supply an explicit name). Used to map the panel's CR
// name back to the Application when deriving the live record / driving the
// live switchover. Returns the input unchanged when it carries no prefix.
func appNameFromContinuumName(continuumName string) string {
	return strings.TrimPrefix(continuumName, "dr-")
}
