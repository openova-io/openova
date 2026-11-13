// Package handler — continuum_dr_extras.go: qa-loop iter-1 prefetch
// Fix #110 (Continuum DR third batch). Adds the rest of the DR contract
// the matrix (and the SovereignConsole DR page) is expected to call:
//
//	GET    /api/v1/sovereigns/{id}/continuum/{name}/replication-status
//	GET    /api/v1/sovereigns/{id}/continuum/{name}/switchover/history
//	GET    /api/v1/sovereigns/{id}/dr/quorum/status
//	GET    /api/v1/sovereigns/{id}/dr/replication-status
//	GET    /api/v1/sovereigns/{id}/continuum/{name}/settings
//	PUT    /api/v1/sovereigns/{id}/continuum/{name}/settings
//
// The quorum endpoint surfaces the DNS-quorum witness state the
// Continuum controller reports. Per ADR-0001 §2.7 every CR
// remains the source of truth — these handlers READ from CRs (Continuum,
// CNPGPair, PDM, Cluster) + the audit lister and SYNTHESIZE realistic
// shapes when the in-cluster client is bootstrapping (mirrors the Fix
// #63 / Fix #102 fallback pattern so chroot Sovereigns lacking a working
// kubeconfig still surface the contract the SovereignConsole renders).
//
// Per INVIOLABLE-PRINCIPLES #4 every URL is env-derived (no hardcoded
// hostnames). Per #5 the read endpoints gate on viewer (any
// authenticated tier).
// Per #16 (canonical seam first) the handlers reuse the already-existing
// continuumDynamicClient + getContinuumCR + audit helpers; no new
// k8s-client wiring.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)


// ── Wire shapes ──────────────────────────────────────────────────────

// continuumReplicationStatus — body of GET /continuum/{name}/replication-status.
//
// Aggregates the per-region replication telemetry the matrix +
// SovereignConsole DR page render. `replicaPromotable` is the bool the
// switchover preview also surfaces; `walLagSeconds` mirrors what the
// CNPGPair status reports for the active replica.
type continuumReplicationStatus struct {
	Continuum         string                  `json:"continuum"`
	Namespace         string                  `json:"namespace"`
	PrimaryRegion     string                  `json:"primaryRegion"`
	CurrentPrimary    string                  `json:"currentPrimary"`
	WALLagSeconds     float64                 `json:"walLagSeconds"`
	WALLagBytes       int64                   `json:"walLagBytes"`
	ReplicaPromotable bool                    `json:"replicaPromotable"`
	StreamingState    string                  `json:"streamingState"`
	SyncState         string                  `json:"syncState"`
	LastHeartbeat     string                  `json:"lastHeartbeat"`
	// StandbyAvailable is the TRI-STATE standby-leg verdict (#4923/#4901):
	// true  — the standby half was verified reachable+following off the live
	//         cnpg cluster-pair;
	// false — the required hot-standby is ABSENT (region-kill / outage) —
	//         the explicit honest condition, never masked by a green shape;
	// nil (omitted) — no cnpg pair resolvable, so the standby leg cannot be
	//         verified from live cluster state; reported as unknown, NOT as
	//         a fabricated healthy.
	StandbyAvailable *bool                   `json:"standbyAvailable,omitempty"`
	// Phase / LeaseHolder / LeaseExpiresAt are the CONTINUUM'S OWN reconciled
	// status, relayed verbatim so the DR panel can render the live Continuum
	// state instead of a static badge (UAT row 62). The controller writes all
	// three (continuum_controller.go patchStatus: status.phase,
	// status.leaseHolder, status.leaseExpiresAt) and this endpoint already READ
	// leaseHolder — it just consumed it internally (to correct CurrentPrimary)
	// and then dropped it, so the console had no way to show WHO holds the
	// witness lease or whether the Continuum reconciled Healthy. Omitted when
	// the CR does not report them — never defaulted to a green-looking value.
	Phase             string                  `json:"phase,omitempty"`
	LeaseHolder       string                  `json:"leaseHolder,omitempty"`
	LeaseExpiresAt    string                  `json:"leaseExpiresAt,omitempty"`
	Replicas          []continuumReplicaInfo  `json:"replicas"`
	HealthGates       []continuumHealthGate   `json:"healthGates"`
	ObservedAt        string                  `json:"observedAt"`
	Source            string                  `json:"source"` // "live" | "pending"

	// Desired-state projection — desiredPrimaryRegion, converged,
	// switchoverInProgress, step, lastSwitchover, convergeReason/Message
	// (continuum_convergence.go). `currentPrimary` above is the OBSERVED
	// writer (status.primaryRegion); the console renders the two side by side.
	continuumConvergence
}

// continuumHealthGate — one row in the replication status health-gate list.
type continuumHealthGate struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // "Pass" | "Warn" | "Fail"
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity"` // "info" | "warning" | "critical"
}

// continuumSwitchoverHistoryItem — one row in the switchover history
// audit trail. Mirrors the rbac-audit envelope shape (TC-325 pattern).
type continuumSwitchoverHistoryItem struct {
	AuditType        string `json:"auditType"`
	Timestamp        string `json:"ts"`
	Actor            string `json:"actor"`
	Sovereign        string `json:"sovereignId"`
	Continuum        string `json:"continuum"`
	FromRegion       string `json:"fromRegion"`
	ToRegion         string `json:"toRegion"`
	Reason           string `json:"reason"`
	Result           string `json:"result"` // "completed" | "failed" | "rolled-back"
	DurationSeconds  int64  `json:"durationSeconds"`
	RPOObservedSec   int64  `json:"rpoObservedSeconds"`
	RTOObservedSec   int64  `json:"rtoObservedSeconds"`
	AuditEventID     string `json:"auditEventId"`
}

// continuumSwitchoverHistoryResponse — body of
// GET /continuum/{name}/switchover/history.
type continuumSwitchoverHistoryResponse struct {
	Items  []continuumSwitchoverHistoryItem `json:"items"`
	Schema []string                         `json:"schema"`
	Total  int                              `json:"total"`
}

// drQuorumStatus — body of GET /dr/quorum/status.
//
// Reflects the DNS-quorum lease holder + per-PDM agreement reported by
// the Continuum controller's witness. The matrix's TC-319/320/321 dig
// against pdm-{1,2,3} for the same TXT record; this endpoint surfaces
// the same lease state via HTTP for the SovereignConsole.
type drQuorumStatus struct {
	Sovereign     string              `json:"sovereign"`
	Zone          string              `json:"zone"`
	RecordName    string              `json:"recordName"`
	LeaseHolder   string              `json:"leaseHolder"`
	LeaseExpires  string              `json:"leaseExpires"`
	Quorum        string              `json:"quorum"` // "in-quorum" | "split" | "lost"
	QuorumOf      string              `json:"quorumOf"` // "2of3" | "1of3" etc
	PrimaryRegion string              `json:"primaryRegion"`
	Witnesses     []drQuorumWitness   `json:"witnesses"`
	ObservedAt    string              `json:"observedAt"`
	Source        string              `json:"source"`
}

// drQuorumWitness — per-PDM witness state.
type drQuorumWitness struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Phase       string `json:"phase"` // "Healthy" | "Degraded" | "Lost"
	Agreement   bool   `json:"agreement"`
	LastProbed  string `json:"lastProbed"`
	TXTRecord   string `json:"txtRecord,omitempty"`
}

// continuumSettings — body of GET / PUT /continuum/{name}/settings.
//
// Per-Application DR knobs the SovereignConsole settings page exposes.
// Mirrors a subset of Continuum spec — the source of truth remains the
// CR. PUT uses RFC-7396 merge-patch semantics; only the fields the
// caller supplies are mutated.
type continuumSettings struct {
	Continuum            string  `json:"continuum"`
	Namespace            string  `json:"namespace"`
	RPOSeconds           int64   `json:"rpoSeconds"`
	RTOSeconds           int64   `json:"rtoSeconds"`
	AutoFailover         bool    `json:"autoFailover"`
	AutoFailoverThreshold float64 `json:"autoFailoverThresholdSeconds"`
	HotStandbyRegions    []string `json:"hotStandbyRegions"`
	NotificationChannels []string `json:"notificationChannels"`
	MaintenanceWindow    string   `json:"maintenanceWindow,omitempty"`
	UpdatedAt            string   `json:"updatedAt"`
	UpdatedBy            string   `json:"updatedBy,omitempty"`
}

// ── Handlers ─────────────────────────────────────────────────────────

// HandleContinuumReplicationStatus — GET
// /api/v1/sovereigns/{id}/continuum/{name}/replication-status
//
// Aggregates the per-region replication telemetry from the Continuum CR
// + linked CNPGPair status. Falls back to a synthesized realistic shape
// when the in-cluster client is bootstrapping or the CRs are missing
// (mirrors Fix #63's switchover-preview fallback).
func (h *Handler) HandleContinuumReplicationStatus(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	resp := pendingReplicationStatus(name, ns)
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if !apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":  "continuum-get-failed",
				"detail": getErr.Error(),
			})
			return
		}
		// #4551 — the name lookup missed. The frontend computes the
		// Continuum name as `dr-<app>` (controller convention), but a live
		// CR is frequently named differently (e.g.
		// `cnpg-pair-bp-cnpg-pair-continuum`) and instead carries
		// `spec.applicationRef: <app>`. Resolve by applicationRef before
		// giving up, so the panel reads the REAL CR (source:live, true lag)
		// instead of the synthesized Hetzner/2s shape.
		appName := appNameFromContinuumName(name)
		if matched := findContinuumCRByApplicationRef(r.Context(), client, appName); matched != nil {
			cr = matched
		} else {
			// No CR by name OR applicationRef — ADR-0015 D7: no object, no
			// status. The pending response stands (the former live-derived
			// record from a cnpg pair is deleted).
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	resp = enrichReplicationStatus(cr)
	// Look up the linked CNPGPair for the live WAL lag reading.
	if cnpgPairName, _, _ := unstructured.NestedString(cr.Object, "spec", "cnpgPair", "name"); cnpgPairName != "" {
		cnpgPairNS, _, _ := unstructured.NestedString(cr.Object, "spec", "cnpgPair", "namespace")
		if cnpgPairNS == "" {
			cnpgPairNS = cr.GetNamespace()
		}
		gvr := schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "cnpgpairs"}
		if pair, perr := client.Resource(gvr).Namespace(cnpgPairNS).Get(r.Context(), cnpgPairName, metav1.GetOptions{}); perr == nil {
			// #5601 — consume the lag key a producer actually writes.
			// `walLagSeconds` is the CNPGPair-CRD spelling (stamped by the QA
			// fixture); the live controllers publish the same axis as
			// `replicationLagSeconds`. Probing only the first spelling left the
			// panel's lag reading stuck on the CR-derived value.
			lag := readNumericNested(pair.Object, "status", "walLagSeconds")
			if lag == 0 {
				lag = readNumericNested(pair.Object, "status", "replicationLagSeconds")
			}
			if lag > 0 {
				resp.WALLagSeconds = lag
				// #6114 — RE-DERIVE the gate from the reading we just adopted.
				//
				// The gate above came from enrichReplicationStatus ->
				// walLagHealthGate, which reads the CONTINUUM CR. For
				// dr-shared-pg that CR carries no lag key at all, so the gate
				// is Warn/"not reported by the Continuum CR; unverified" —
				// correct for the CR, and STALE the instant a real producer
				// (this CNPGPair) hands us a measurement. Leaving it made the
				// response ship a genuine numeric lag next to a Warn saying no
				// measurement exists, and the console believes the gate: it
				// renders an em-dash over the live number. That is what UAT
				// rows R12/R13 and the topology block assert must never happen.
				//
				// Guarded by `lag > 0`, so this can only ever act on a POSITIVE
				// reading a producer actually wrote. An absent lag never
				// reaches here and keeps the "unverified" Warn, preserving the
				// #4901/#4923 invariant that absence is unknown, not a healthy
				// zero — never a verdict from absent evidence.
				upsertHealthGate(&resp, walLagGateFromMeasurement(lag, cnpgPairName))
			}
			lagBytes := int64(readNumericNested(pair.Object, "status", "walLagBytes"))
			if lagBytes > 0 {
				resp.WALLagBytes = lagBytes
			}
			if streaming, _, _ := unstructured.NestedString(pair.Object, "status", "streamingState"); streaming != "" {
				resp.StreamingState = streaming
			}
			if sync, _, _ := unstructured.NestedString(pair.Object, "status", "syncState"); sync != "" {
				resp.SyncState = sync
			}
			if hb, _, _ := unstructured.NestedString(pair.Object, "status", "lastHeartbeat"); hb != "" {
				resp.LastHeartbeat = hb
			}
			// #5601 — `status.replicaPromotable` has NO live producer (the QA
			// fixture is its only writer), so probing for it alone silently
			// kept the zero-value false and the Switchover control rendered
			// "no caught-up standby to promote" against a healthy caught-up
			// pair. Honor an explicit reading when present; otherwise derive
			// promotability from the keys the CNPGPair CRD contract actually
			// carries — replica streaming AND lag under the 30s threshold
			// (the CRD's own stated switchover-safety predicate, same shape
			// as liveReplicationStatusFromCNPGPair). A pair reporting neither
			// key leaves the CR-derived verdict untouched.
			// augmentReplicationStandbyStatus still runs after this and
			// forces non-promotable when the live standby leg is verified
			// absent (#4901), so a lag-0-during-outage reading cannot arm
			// the control.
			if promotable, found, _ := unstructured.NestedBool(pair.Object, "status", "replicaPromotable"); found {
				resp.ReplicaPromotable = promotable
			} else if streaming, sFound, _ := unstructured.NestedBool(pair.Object, "status", "streaming"); sFound {
				resp.ReplicaPromotable = streaming && lag <= 30
			}
		}
	}
	// #4923 — the Continuum CR / linked CNPGPair status frequently omit
	// syncState (and a spine CR carries none), so enrich defaulted it to a
	// misleading "async". Read the AUTHORITATIVE synchronous posture straight
	// off the live CNPG cluster-pair backing the app: CNPG renders
	// `synchronous_standby_names = FIRST N (...)` (sync_state=sync, RPO=0) only
	// when the primary half declares spec.postgresql.synchronous. No-op when no
	// 2-region pair resolves (e.g. openbao-raft spine continuums).
	h.augmentReplicationSyncFromLivePair(
		r.Context(), client, appNameFromContinuumName(cr.GetName()), cr.GetNamespace(), &resp)
	// #4923/#4901 — VERIFY the standby leg off the live cnpg cluster-pair and
	// surface the tri-state verdict (available / ABSENT / unverifiable). The
	// continuum-controller's stored status stays green through a hot-standby
	// outage (it derives phase from lease-held-ness alone), so the endpoint
	// must cross-check the replica half itself — never relay a fabricated
	// Healthy.
	h.augmentReplicationStandbyStatus(r.Context(), depID, client, cr, &resp)
	resp.Source = "live"
	resp.ObservedAt = h.continuumNow().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, resp)
}

// augmentReplicationStandbyStatus relays the Continuum CR's OWN standby
// verdict — status.standbyAvailable, published by the continuum-controller's
// standby probe (pgprobe, #4901/#5311) — verbatim into the response.
//
// ADR-0015 D7: the read-side synthesis that used to live here (a cross-check
// of the cnpg pair's replica half from region A, a cross-cluster informer
// read, and a three-way precedence between them) is DELETED. It was a second
// status derivation for one object. Absent a controller verdict the gate is
// Warn/unknown — never a fabricated Pass.
func (h *Handler) augmentReplicationStandbyStatus(
	_ context.Context,
	_ string,
	_ dynamic.Interface,
	cr *unstructured.Unstructured,
	resp *continuumReplicationStatus,
) {
	crStandby, crFound, _ := unstructured.NestedBool(cr.Object, "status", "standbyAvailable")
	if !crFound {
		upsertHealthGate(resp, continuumHealthGate{
			Name: "standby-available", Status: "Warn", Severity: "warning",
			Message: "the Continuum controller has not published a standby verdict (status.standbyAvailable absent); reporting unknown, not healthy",
		})
		return
	}
	resp.StandbyAvailable = &crStandby
	if !crStandby {
		resp.ReplicaPromotable = false
		resp.StreamingState = "interrupted"
		upsertHealthGate(resp, continuumHealthGate{
			Name: "standby-available", Status: "Fail", Severity: "critical",
			Message: "the Continuum controller's standby probe reports the required hot-standby is unreachable; replication has no standby leg and RPO=0 durability is at risk",
		})
		return
	}
	upsertHealthGate(resp, continuumHealthGate{
		Name: "standby-available", Status: "Pass", Severity: "info",
		Message: "standby leg reported available by the Continuum controller's standby probe",
	})
}

// upsertHealthGate replaces the gate of the same name or appends it.
func upsertHealthGate(resp *continuumReplicationStatus, gate continuumHealthGate) {
	for i := range resp.HealthGates {
		if resp.HealthGates[i].Name == gate.Name {
			resp.HealthGates[i] = gate
			return
		}
	}
	resp.HealthGates = append(resp.HealthGates, gate)
}

// HandleContinuumSwitchoverHistory — GET
// /api/v1/sovereigns/{id}/continuum/{name}/switchover/history
//
// Returns the audit trail filtered to switchover events for THIS
// Continuum CR. Mirrors the rbac-audit envelope (TC-325 pattern). Falls
// back to a synthesized last-switchover row when no live audit events
// are visible.
func (h *Handler) HandleContinuumSwitchoverHistory(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	items := h.collectContinuumSwitchoverHistory(depID, name)
	// #4930 (follow-up to #4923/#4927) — a Sovereign that has not exercised a
	// switchover has an EMPTY switchover history. The prior code fabricated a
	// single "last switchover" row (fromRegion hz-fsn-rtz-prod → toRegion
	// hz-hel-rtz-prod — Hetzner regions on a Huawei-only Sovereign — plus an
	// invented 47s duration / 3s RPO) purely so the panel rendered a non-empty
	// audit trail. That is exactly the synthesized-data anti-theater the founder
	// bans. Return the honest (possibly empty) trail; the SovereignConsole
	// renders "no switchovers recorded" instead of a fabricated event.
	// Newest first for the UI.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})
	writeJSON(w, http.StatusOK, continuumSwitchoverHistoryResponse{
		Items: items,
		Schema: []string{
			"auditType", "ts", "actor", "sovereignId", "continuum",
			"fromRegion", "toRegion", "reason", "result",
			"durationSeconds", "rpoObservedSeconds", "rtoObservedSeconds",
			"auditEventId",
		},
		Total: len(items),
	})
}

// HandleDRQuorumStatus — GET /api/v1/sovereigns/{id}/dr/quorum/status
//
// Surfaces the DNS-quorum witness state for the Sovereign's Continuum
// controller. Reads from the PDM CRs + the Continuum CR's
// status.lastLuaRecord. Falls back to a synthesized 3-PDM 2-of-3
// in-quorum shape when the CRs are missing.
func (h *Handler) HandleDRQuorumStatus(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	zone := strings.TrimSpace(r.URL.Query().Get("zone"))
	if zone == "" {
		zone = "openova.io"
	}
	contName := strings.TrimSpace(r.URL.Query().Get("continuum"))
	if contName == "" {
		contName = "cont-omantel"
	}
	resp := pendingQuorumStatus(depID, contName, zone)
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	pdmGVR := schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "pdms"}
	list, lerr := client.Resource(pdmGVR).Namespace("").List(r.Context(), metav1.ListOptions{})
	if lerr != nil || len(list.Items) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	witnesses := []drQuorumWitness{}
	healthy := 0
	for i := range list.Items {
		pdm := &list.Items[i]
		// ADR-0015 D7 — the PDM's own phase, verbatim; absent stays empty
		// (unknown), never a defaulted "Pending".
		phase, _, _ := unstructured.NestedString(pdm.Object, "status", "phase")
		endpoint, _, _ := unstructured.NestedString(pdm.Object, "spec", "endpoint")
		region, _, _ := unstructured.NestedString(pdm.Object, "spec", "region")
		quorum, _, _ := unstructured.NestedString(pdm.Object, "status", "leaseQuorum")
		probed, _, _ := unstructured.NestedString(pdm.Object, "status", "lastProbedAt")
		agreement := quorum == "in-quorum" && phase == "Healthy"
		if agreement {
			healthy++
		}
		witnesses = append(witnesses, drQuorumWitness{
			Name:       pdm.GetName(),
			Endpoint:   endpoint,
			Region:     region,
			Phase:      phase,
			Agreement:  agreement,
			LastProbed: probed,
		})
	}
	sort.SliceStable(witnesses, func(i, j int) bool { return witnesses[i].Name < witnesses[j].Name })
	resp.Witnesses = witnesses
	switch {
	case healthy >= 2:
		resp.Quorum = "in-quorum"
	case healthy == 1:
		resp.Quorum = "split"
	default:
		resp.Quorum = "lost"
	}
	resp.QuorumOf = fmt.Sprintf("%dof%d", healthy, len(witnesses))
	resp.Source = "live"
	resp.ObservedAt = h.continuumNow().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, resp)
}

// HandleDRReplicationStatus — GET /api/v1/sovereigns/{id}/dr/replication-status
//
// Sovereign-wide replication roll-up: walks every Continuum CR and
// produces an aggregate. Mirrors HandleContinuumReplicationStatus shape
// but without the {name} path param — the SovereignConsole's DR
// dashboard renders this on the overview tile.
func (h *Handler) HandleDRReplicationStatus(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	out := struct {
		Sovereign         string                       `json:"sovereign"`
		ContinuumCount    int                          `json:"continuumCount"`
		ReplicasHealthy   int                          `json:"replicasHealthy"`
		ReplicasDegraded  int                          `json:"replicasDegraded"`
		// ReplicasUnknown counts continuums whose standby leg could not be
		// verified from live cluster state (no cnpg pair resolvable — e.g.
		// dr-spine/raft continuums). Reported honestly as UNKNOWN instead of
		// being folded into healthy (#4923 hw242 evidence: all four dr-spine-*
		// read Healthy while region-B ran zero workloads).
		ReplicasUnknown   int                          `json:"replicasUnknown"`
		MaxWALLagSeconds  float64                      `json:"maxWalLagSeconds"`
		Continuums        []continuumReplicationStatus `json:"continuums"`
		ObservedAt        string                       `json:"observedAt"`
		Source            string                       `json:"source"`
	}{
		Sovereign:  depID,
		Continuums: []continuumReplicationStatus{},
		ObservedAt: h.continuumNow().UTC().Format(time.RFC3339),
		Source:     "pending",
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// #4923 — honest empty roll-up (source:pending). NEVER a fabricated
		// 1-healthy / 2s-lag / Hetzner row that misreports the Sovereign's DR
		// posture when the live status is simply not observable yet.
		writeJSON(w, http.StatusOK, out)
		return
	}
	list, lerr := client.Resource(ContinuumGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
	if lerr != nil || len(list.Items) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for i := range list.Items {
		cr := &list.Items[i]
		row := enrichReplicationStatus(cr)
		// #4923 — verify the standby leg off the live cnpg pair per row (the
		// stored CR status alone stays green through a standby outage). Rows
		// whose standby cannot be verified count as UNKNOWN — never healthy.
		h.augmentReplicationStandbyStatus(r.Context(), depID, client, cr, &row)
		row.Source = "live"
		out.Continuums = append(out.Continuums, row)
		out.ContinuumCount++
		switch {
		case row.StandbyAvailable != nil && *row.StandbyAvailable:
			out.ReplicasHealthy++
		case row.StandbyAvailable != nil:
			out.ReplicasDegraded++
		default:
			out.ReplicasUnknown++
		}
		if row.WALLagSeconds > out.MaxWALLagSeconds {
			out.MaxWALLagSeconds = row.WALLagSeconds
		}
	}
	out.Source = "live"
	writeJSON(w, http.StatusOK, out)
}

// HandleContinuumSettingsGet — GET /api/v1/sovereigns/{id}/continuum/{name}/settings
//
// Returns the per-Application DR knobs the SovereignConsole settings
// page renders. Mirrors a subset of Continuum spec.
func (h *Handler) HandleContinuumSettingsGet(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	settings := defaultContinuumSettings(name, "qa-omantel")
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeJSON(w, http.StatusOK, settings)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		writeJSON(w, http.StatusOK, settings)
		return
	}
	settings = settingsFromContinuumCR(cr)
	writeJSON(w, http.StatusOK, settings)
}

// ── Helpers ──────────────────────────────────────────────────────────

// continuumNow lives in continuum.go — this file reuses it (h.continuumNow()).

// continuumPhaseReady — the Continuum phases that count as ready.
//
// FailedOver is deliberately included: it is the post-switchover STEADY state,
// and treating it as not-ready would make a DR preflight block the failback of
// a Sovereign that has already failed over — the one state where an operator
// cannot recover without it (the second half #5601/PR #5780 closed).
func continuumPhaseReady(phase string) bool {
	return phase == "Healthy" || phase == "FailedOver"
}

func (h *Handler) collectContinuumSwitchoverHistory(depID, contName string) []continuumSwitchoverHistoryItem {
	out := []continuumSwitchoverHistoryItem{}
	if h.auditBus == nil {
		return out
	}
	predicate := func(t string) bool {
		// Match the canonical switchover audit-type names emitted by
		// the continuum-controller (converging on spec.desiredPrimaryRegion).
		return strings.HasPrefix(t, "continuum-switchover-")
	}
	events := h.auditBus.List(depID, predicate, 100)
	for _, e := range events {
		// e.Detail is a free-form string per the canonical schema —
		// reach for the structured fields when present, fall back to
		// best-effort substring scan otherwise.
		cName := contName
		// Best-effort: when continuum name is in the detail line, prefer it.
		if strings.Contains(e.Detail, contName) || contName == "" {
			cName = contName
		}
		out = append(out, continuumSwitchoverHistoryItem{
			AuditType:       e.AuditType,
			Timestamp:       e.Timestamp.UTC().Format(time.RFC3339),
			Actor:           e.Actor,
			Sovereign:       e.SovereignID,
			Continuum:       cName,
			FromRegion:      "",
			ToRegion:        "",
			Reason:          e.Detail,
			Result:          firstNonEmpty(e.Result, "completed"),
			DurationSeconds: 0,
			RPOObservedSec:  0,
			RTOObservedSec:  0,
			AuditEventID:    fmt.Sprintf("%s-%d", e.AuditType, e.Timestamp.UnixNano()),
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func enrichReplicationStatus(cr *unstructured.Unstructured) continuumReplicationStatus {
	out := continuumReplicationStatus{
		Continuum: cr.GetName(),
		Namespace: cr.GetNamespace(),
		Replicas:  []continuumReplicaInfo{},
		Source:    "live",
	}
	out.PrimaryRegion, _, _ = unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	out.CurrentPrimary, _, _ = unstructured.NestedString(cr.Object, "status", "currentPrimary")
	// #4886 — the Continuum controller records the ACTIVE region as the
	// witness-lease holder in `status.leaseHolder`, NOT `status.currentPrimary`
	// (that field is CNPGPair-derived and only present when a cnpg pair backs
	// the app). Prefer leaseHolder so the DR panel's active region is correct
	// for the spine continuums (openbao raft, keycloak/gitea/harbor) and stays
	// correct after a switchover flips the lease off the original primaryRegion.
	if leaseHolder, _, _ := unstructured.NestedString(cr.Object, "status", "leaseHolder"); leaseHolder != "" {
		out.CurrentPrimary = leaseHolder
	}
	// The continuum-controller's status.primaryRegion is the OBSERVED writer
	// and beats both the lease holder and the static spec.primaryRegion.
	if observed := continuumObservedPrimary(cr); observed != "" {
		out.CurrentPrimary = observed
	}
	if out.CurrentPrimary == "" {
		out.CurrentPrimary = out.PrimaryRegion
	}
	out.continuumConvergence = continuumConvergenceFromCR(cr, out.CurrentPrimary)
	out.WALLagSeconds = readNumericNested(cr.Object, "status", "walLagSeconds")
	// #4886 — the Continuum controller writes the numeric lag as
	// `status.replicationLagSeconds` (int64); `walLagSeconds` is the
	// CNPGPair-only spelling. Fall back to replicationLagSeconds so the spine
	// continuums surface their real lag instead of a hardcoded 0. (A linked
	// CNPGPair reading, when present, still overrides this in the caller.)
	if out.WALLagSeconds == 0 {
		out.WALLagSeconds = readNumericNested(cr.Object, "status", "replicationLagSeconds")
	}
	out.WALLagBytes = int64(readNumericNested(cr.Object, "status", "walLagBytes"))
	// #4923 — NO fabricated defaults. The prior code defaulted streamingState
	// to "streaming" and syncState to "async" when the CR omitted them —
	// synthesized telemetry the operator could not distinguish from a live
	// reading (and flatly wrong on a synchronous RPO=0 pair). Empty = the CR
	// does not report it; the live cnpg-pair augmentation fills syncState when
	// a real pair resolves, and the UI renders "—" otherwise.
	out.StreamingState, _, _ = unstructured.NestedString(cr.Object, "status", "streamingState")
	out.SyncState, _, _ = unstructured.NestedString(cr.Object, "status", "syncState")
	// #4923 — replicaPromotable needs POSITIVE evidence that a standby exists
	// and follows, not just "lag number is small": an ABSENT standby also
	// reads lag=0 (#4901 — health/lag stayed green for the whole hot-standby
	// outage), so the old `lag <= 30` marked a lost standby promotable. The
	// live-pair standby augmentation upgrades this when it verifies the
	// standby off the cnpg cluster-pair.
	//
	// #5601 — the Continuum controller NEVER writes status.replicationHealthy.
	// Its status writer (core/controllers/continuum patchStatus) publishes the
	// standby posture as status.standbyAvailable (#4901) with replica health
	// folded into status.phase. Probing only the absent key silently kept the
	// zero-value false, so a fully healthy caught-up pair on hw292
	// (standbyAvailable=true, phase=Healthy, replicationLagSeconds=0,
	// StandbyAvailable condition True/StandbyReachable) rendered the
	// Switchover control disabled with "no caught-up standby to promote" —
	// asserting the opposite of the CR it claims to read. Consume the keys
	// the controller actually writes, deriving promotability identically to
	// liveReplicationStatusFromCNPGPair: positive standby evidence AND
	// healthy AND lag under the 30s threshold. Healthy/FailedOver are the two
	// reconciled-ready phases (phaseToReady in the controller) — FailedOver is
	// the post-switchover steady state and must keep the failback path armed.
	// A CR carrying NEITHER key still yields false: never promotable without
	// positive evidence.
	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	replHealthy, replHealthyFound, _ := unstructured.NestedBool(cr.Object, "status", "replicationHealthy")
	standbyAvailable, standbyAvailableFound, _ := unstructured.NestedBool(cr.Object, "status", "standbyAvailable")
	switch {
	case replHealthyFound:
		out.ReplicaPromotable = replHealthy && out.WALLagSeconds <= 30
	case standbyAvailableFound:
		// continuumPhaseReady == Healthy || FailedOver — the FailedOver clause
		// is guarded by continuum_dr_extras_5601_test.go, which fails if it is
		// dropped (deleting it used to leave every test green).
		out.ReplicaPromotable = standbyAvailable && continuumPhaseReady(phase) && out.WALLagSeconds <= 30
	}
	// #4923 — health gates DERIVED from the observed CR status, never a
	// hardcoded all-Pass wall (the prior shape reported "streaming-replication
	// Pass" during a proven standby outage).
	leaseHolder, _, _ := unstructured.NestedString(cr.Object, "status", "leaseHolder")
	// Row 62 — relay the Continuum's OWN reconciled status so the DR panel can
	// render live state (phase + who holds the witness lease + when it expires)
	// rather than a static badge. Read-only passthrough; empty when unreported.
	out.Phase = phase
	out.LeaseHolder = leaseHolder
	out.LeaseExpiresAt, _, _ = unstructured.NestedString(cr.Object, "status", "leaseExpiresAt")
	out.HealthGates = []continuumHealthGate{
		replicationHealthGate(replHealthy, replHealthyFound, standbyAvailable, standbyAvailableFound, phase),
		walLagHealthGate(cr.Object, out.WALLagSeconds),
		leaseWitnessHealthGate(leaseHolder),
	}
	// The configured standby regions from spec — placement config, labelled as
	// such via the honest lastHeartbeat handling: only a heartbeat the CR
	// actually reports is surfaced, never a fabricated now() stamp.
	hb, _, _ := unstructured.NestedString(cr.Object, "status", "lastHeartbeat")
	out.LastHeartbeat = hb
	standbys, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	for _, region := range standbys {
		out.Replicas = append(out.Replicas, continuumReplicaInfo{
			Region:        region,
			Role:          "replica",
			LagSeconds:    out.WALLagSeconds,
			LastHeartbeat: hb,
		})
	}
	return out
}

// replicationHealthGate derives the streaming-replication gate from the
// observed status. Tri-state honest: Pass only on POSITIVE evidence; Fail on an
// explicit negative or a Degraded/FailedOver phase; Warn (unverified) when the
// CR reports neither — NEVER a default Pass. #4923.
//
// UAT row 71 — THE GATE COULD NEVER PASS. `status.replicationHealthy` has ZERO
// producers: grep across core/controllers/continuum and core/pkg/apis returns
// nothing, and all 8 live Continuums on hw292 omit it. So `found` was false on
// every real CR and this gate returned Warn "unverified" unconditionally,
// forever, on a fully healthy pair. Downstream that is not cosmetic: since
// #5508 the console treats a streaming-replication Warn as "the lag reading is
// not a measurement" and renders the replication lag as "—", which is exactly
// the clause row 71 fails on ("a live replication-lag number").
//
// The fix is the one #5601 already applied to ReplicaPromotable in this same
// file and did not carry across to the gate: consume the key the controller
// ACTUALLY writes. `status.standbyAvailable` is the continuum-controller's own
// standby-probe verdict (patchStatus line ~1396; the #4901/#5311 probe), so a
// true reading alongside a reconciled-ready phase IS positive evidence that
// replication is streaming, and a false reading is a verified fault. A CR
// carrying neither key still yields Warn — never a Pass without evidence.
// Degraded/FailedOver are already short-circuited to Fail below, so the only
// phase that can carry the standby-probe evidence to a Pass is Healthy.
func replicationHealthGate(healthy, found, standbyAvailable, standbyFound bool, phase string) continuumHealthGate {
	phaseReady := phase == "Healthy"
	switch {
	case found && healthy:
		return continuumHealthGate{Name: "streaming-replication", Status: "Pass", Severity: "info"}
	case found && !healthy:
		return continuumHealthGate{Name: "streaming-replication", Status: "Fail", Severity: "critical",
			Message: "replication reported unhealthy on the Continuum CR"}
	case phase == "Degraded" || phase == "FailedOver":
		return continuumHealthGate{Name: "streaming-replication", Status: "Fail", Severity: "critical",
			Message: fmt.Sprintf("Continuum phase is %s", phase)}
	case standbyFound && !standbyAvailable:
		return continuumHealthGate{Name: "streaming-replication", Status: "Fail", Severity: "critical",
			Message: "the Continuum's standby probe reports no standby leg; replication is not streaming"}
	case standbyFound && standbyAvailable && phaseReady:
		return continuumHealthGate{Name: "streaming-replication", Status: "Pass", Severity: "info",
			Message: fmt.Sprintf("standby probe reports the standby leg available; Continuum phase is %s", phase)}
	default:
		return continuumHealthGate{Name: "streaming-replication", Status: "Warn", Severity: "warning",
			Message: "replication health not reported by the Continuum CR; unverified"}
	}
}

// walLagHealthGate derives the wal-lag gate: Pass only when the CR actually
// REPORTS a lag reading under the 30s promotability threshold; Warn when the
// reading is over threshold or absent entirely (absent lag is unknown, not a
// healthy zero — #4901's outage kept lag pinned at 0). #4923.
func walLagHealthGate(obj map[string]interface{}, lag float64) continuumHealthGate {
	_, foundWAL, _ := unstructured.NestedFieldNoCopy(obj, "status", "walLagSeconds")
	_, foundRepl, _ := unstructured.NestedFieldNoCopy(obj, "status", "replicationLagSeconds")
	if !foundWAL && !foundRepl {
		return continuumHealthGate{Name: "wal-lag-under-rpo", Status: "Warn", Severity: "warning",
			Message: "replication lag not reported by the Continuum CR; unverified"}
	}
	if lag > 30 {
		return continuumHealthGate{Name: "wal-lag-under-rpo", Status: "Warn", Severity: "warning",
			Message: fmt.Sprintf("replication lag %.0fs exceeds the 30s promotability threshold", lag)}
	}
	return continuumHealthGate{Name: "wal-lag-under-rpo", Status: "Pass", Severity: "info"}
}

// walLagGateFromMeasurement derives the wal-lag gate from a lag reading that a
// real producer PUBLISHED, as opposed to walLagHealthGate which derives it from
// whichever keys a given CR happens to carry.
//
// The distinction is the whole point (#6114). walLagHealthGate must treat an
// absent key as unverified, because absence is not a healthy zero (#4901 kept
// lag pinned at 0 straight through an outage). But once a producer has handed
// us an actual positive measurement, continuing to report "no measurement" is
// simply false — and the console, which trusts the gate over the number,
// suppresses the live value behind an em-dash.
//
// Callers MUST only invoke this with a measurement they positively observed.
// It has no absent-key branch by design: there is no input to this function
// that means "unknown", so it can never manufacture a Pass out of nothing.
func walLagGateFromMeasurement(lag float64, source string) continuumHealthGate {
	if lag > 30 {
		return continuumHealthGate{Name: "wal-lag-under-rpo", Status: "Warn", Severity: "warning",
			Message: fmt.Sprintf("replication lag %.0fs exceeds the 30s promotability threshold (measured by CNPGPair %s)", lag, source)}
	}
	return continuumHealthGate{Name: "wal-lag-under-rpo", Status: "Pass", Severity: "info",
		Message: fmt.Sprintf("replication lag %.1fs is under the 30s promotability threshold (measured by CNPGPair %s)", lag, source)}
}

// leaseWitnessHealthGate derives the witness-lease gate from the observed
// lease holder: Pass when a region holds the lease, Warn when no lease is
// observed. #4923.
func leaseWitnessHealthGate(leaseHolder string) continuumHealthGate {
	if strings.TrimSpace(leaseHolder) != "" {
		return continuumHealthGate{Name: "lease-witness-quorum", Status: "Pass", Severity: "info"}
	}
	return continuumHealthGate{Name: "lease-witness-quorum", Status: "Warn", Severity: "warning",
		Message: "no witness-lease holder observed on the Continuum CR"}
}

// findContinuumCRByApplicationRef lists every Continuum CR cluster-wide
// and returns the first whose `spec.applicationRef` equals appName. This is
// the #4551 fix for the frontend's `dr-<app>` name guess: the live CR is
// frequently named differently but tags the app via applicationRef. Returns
// nil when no match (the caller then tries the live cnpg-pair derivation).
func findContinuumCRByApplicationRef(
	ctx context.Context,
	client dynamic.Interface,
	appName string,
) *unstructured.Unstructured {
	if strings.TrimSpace(appName) == "" {
		return nil
	}
	list, err := client.Resource(ContinuumGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return nil
	}
	for i := range list.Items {
		ref, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "applicationRef")
		if ref == appName {
			return list.Items[i].DeepCopy()
		}
	}
	return nil
}

// augmentReplicationSyncFromLivePair upgrades a replication-status response
// with the REAL synchronous posture (+ regions when the CR left them blank)
// read straight off the live CNPG cluster-pair backing the app. #4923.
//
// CNPG renders `synchronous_standby_names = FIRST N (...)` — sync_state=sync,
// replay_lag=0, RPO=0 — ONLY when the primary half declares
// spec.postgresql.synchronous (the bp-cnpg-pair chart sets it when
// replication.mode=sync). That is the authoritative sync/async signal; the
// Continuum CR and the dr.openova.io CNPGPair status often omit syncState, so
// enrichReplicationStatus otherwise defaults it to a misleading "async" on a
// genuinely synchronous pair. No-op when no 2-region pair resolves (e.g. the
// openbao-raft spine continuums carry no cnpg pair) so their observed status
// is left untouched.
func (h *Handler) augmentReplicationSyncFromLivePair(
	ctx context.Context,
	client dynamic.Interface,
	appName, ns string,
	resp *continuumReplicationStatus,
) {
	st, err := h.findCNPGPairForApp(ctx, client, appName, ns)
	if err != nil || st == nil {
		return
	}
	if st.SyncReplication {
		resp.SyncState = "sync"
	} else {
		resp.SyncState = "async"
	}
	// Fill regions the CR-derived enrich left blank from the live cluster
	// placement labels (never a Hetzner placeholder).
	if resp.PrimaryRegion == "" && st.PrimaryRegion != "" {
		resp.PrimaryRegion = st.PrimaryRegion
	}
	if resp.CurrentPrimary == "" {
		resp.CurrentPrimary = resp.PrimaryRegion
	}
}

// gatePass maps a bool to the health-gate status vocabulary.
func gatePass(ok bool) string {
	if ok {
		return "Pass"
	}
	return "Warn"
}

// pendingReplicationStatus is the HONEST shape returned when the live
// cnpg-pair replication status is not yet observable (dynamic client
// bootstrapping, no Continuum CR, no resolvable 2-region pair). #4923.
//
// It NEVER fabricates data. The prior `synthesizedReplicationStatus`
// hardcoded Hetzner regions (`hz-fsn-rtz-prod` / `hz-hel-rtz-prod`) on a
// Huawei-only Sovereign, a byte-identical `walLagSeconds:2 / walLagBytes:8192`
// for EVERY app, and `syncState:async` — flatly contradicting the live truth
// (`pg_stat_replication`: `sync_state=sync`, `replay_lag=0`,
// `synchronous_standby_names=FIRST 1`). That misled the operator into reading
// async / wrong-cloud / 2s-lag when reality was synchronous RPO=0.
//
// This replacement carries ONLY facts we actually know at fallback time: the
// Sovereign's REAL configured regions (from SOVEREIGN_PRIMARY_REGION /
// SOVEREIGN_REPLICA_REGION, threaded in by bp-catalyst-platform from the
// sovereign-fqdn ConfigMap — empty when genuinely unknown, NEVER a Hetzner
// placeholder), zero lag (unknown, not a fake 2s), empty sync/streaming state
// (unknown), and `source:"pending"` so the UI's "NOT LIVE" gate holds without
// drawing wrong-cloud regions or invented lag. Region derivation is the same
// env pair `chrootRegionsFromPrimaryReplicaEnv` reads.
func pendingReplicationStatus(name, ns string) continuumReplicationStatus {
	primary := strings.TrimSpace(os.Getenv("SOVEREIGN_PRIMARY_REGION"))
	replica := strings.TrimSpace(os.Getenv("SOVEREIGN_REPLICA_REGION"))
	replicas := []continuumReplicaInfo{}
	if replica != "" && replica != primary {
		replicas = append(replicas, continuumReplicaInfo{
			Region: replica,
			Role:   "replica",
		})
	}
	return continuumReplicationStatus{
		Continuum:         name,
		Namespace:         ns,
		PrimaryRegion:     primary,
		CurrentPrimary:    primary,
		WALLagSeconds:     0,
		WALLagBytes:       0,
		ReplicaPromotable: false,
		StreamingState:    "",
		SyncState:         "",
		Replicas:          replicas,
		HealthGates: []continuumHealthGate{
			{Name: "live-status", Status: "Warn", Severity: "info",
				Message: "live cnpg-pair replication status not yet available; awaiting region-b convergence"},
		},
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
		Source:     "pending",
	}
}

// sovereignRegionsFromEnv returns the Sovereign's REAL configured primary +
// replica regions from SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION
// (threaded in by bp-catalyst-platform from the sovereign-fqdn ConfigMap — the
// same env pair pendingReplicationStatus / chrootRegionsFromPrimaryReplicaEnv
// read). Both are empty when genuinely unknown — NEVER a Hetzner (or any cloud)
// placeholder. replica is blanked when it equals primary (a single-region prov
// has no distinct standby). #4930 (follow-up to #4923/#4927).
func sovereignRegionsFromEnv() (primary, replica string) {
	primary = strings.TrimSpace(os.Getenv("SOVEREIGN_PRIMARY_REGION"))
	replica = strings.TrimSpace(os.Getenv("SOVEREIGN_REPLICA_REGION"))
	if replica == primary {
		replica = ""
	}
	return primary, replica
}

// pendingQuorumStatus is the HONEST fallback for GET /dr/quorum/status when the
// PDM witness CRs are not observable (dynamic client bootstrapping, PDM CRD
// absent, or no PDM CRs present). #4930 (follow-up to #4923/#4927).
//
// It NEVER fabricates witness state. The prior synthesizedQuorumStatus
// hardcoded a 3-PDM "2of3 in-quorum" shape with Hetzner regions
// (hz-fsn-rtz-prod / hz-hel-rtz-prod) on a Huawei-only Sovereign and invented
// pdm-1 as an in-quorum lease holder — a false "healthy quorum" claim the
// operator could not distinguish from a live reading. This replacement carries
// ONLY the Sovereign's real primary region (from SOVEREIGN_PRIMARY_REGION,
// empty when unknown, never a cloud placeholder), no fabricated witnesses, no
// invented lease holder, Quorum:"pending", and source:"pending" so the UI's
// NOT-LIVE gate holds. RecordName is a derived DNS name (not telemetry), so it
// stays.
func pendingQuorumStatus(depID, contName, zone string) drQuorumStatus {
	primary, _ := sovereignRegionsFromEnv()
	return drQuorumStatus{
		Sovereign:     depID,
		Zone:          zone,
		RecordName:    fmt.Sprintf("_continuum-quorum.%s.%s", contName, zone),
		LeaseHolder:   "",
		LeaseExpires:  "",
		Quorum:        "pending",
		QuorumOf:      "",
		PrimaryRegion: primary,
		Witnesses:     []drQuorumWitness{},
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
		Source:        "pending",
	}
}

// defaultContinuumSettings is the HONEST fallback for GET
// /continuum/{name}/settings when no live Continuum CR is readable. #4930
// (follow-up to #4923/#4927).
//
// It returns the platform's genuine per-Application DR policy DEFAULTS (the
// RPO/RTO knobs a fresh Continuum carries) — those are configured defaults for
// the settings form, NOT fabricated telemetry. The prior
// synthesizedContinuumSettings additionally hardcoded
// HotStandbyRegions:["hz-hel-rtz-prod"] (a Hetzner region on a Huawei-only
// Sovereign) plus an invented NotificationChannels:["sre-pager"] and
// UpdatedBy:"qa-fixture-seed". This replacement derives the hot-standby region
// from the Sovereign's REAL configured replica (SOVEREIGN_REPLICA_REGION, empty
// when unknown, never a cloud placeholder) and carries no fabricated channel or
// actor.
func defaultContinuumSettings(name, ns string) continuumSettings {
	_, replica := sovereignRegionsFromEnv()
	hotStandby := []string{}
	if replica != "" {
		hotStandby = append(hotStandby, replica)
	}
	return continuumSettings{
		Continuum:             name,
		Namespace:             ns,
		RPOSeconds:            30,
		RTOSeconds:            60,
		AutoFailover:          false,
		AutoFailoverThreshold: 120,
		HotStandbyRegions:     hotStandby,
		NotificationChannels:  []string{},
		MaintenanceWindow:     "",
		UpdatedAt:             time.Now().UTC().Format(time.RFC3339),
		UpdatedBy:             "",
	}
}

func settingsFromContinuumCR(cr *unstructured.Unstructured) continuumSettings {
	out := continuumSettings{
		Continuum: cr.GetName(),
		Namespace: cr.GetNamespace(),
	}
	out.RPOSeconds = int64(readNumericNested(cr.Object, "spec", "rpoSeconds"))
	out.RTOSeconds = int64(readNumericNested(cr.Object, "spec", "rtoSeconds"))
	out.AutoFailover, _, _ = unstructured.NestedBool(cr.Object, "spec", "autoFailover")
	out.AutoFailoverThreshold = readNumericNested(cr.Object, "spec", "autoFailoverThresholdSeconds")
	out.HotStandbyRegions, _, _ = unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	out.NotificationChannels, _, _ = unstructured.NestedStringSlice(cr.Object, "spec", "notificationChannels")
	out.MaintenanceWindow, _, _ = unstructured.NestedString(cr.Object, "spec", "maintenanceWindow")
	out.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return out
}

