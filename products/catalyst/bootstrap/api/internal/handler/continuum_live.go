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
			"observedAt":            h.continuumNow().UTC().Format(time.RFC3339),
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
