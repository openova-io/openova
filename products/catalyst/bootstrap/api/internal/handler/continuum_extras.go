// Package handler — continuum_extras.go: EPIC-6 iter-6 target-state
// Continuum DR endpoints that the qa-loop matrix expects beyond the
// original U-DR-1 surface. These are NOT new features — they are the
// rest of the contract documented in the test matrix: per-CR PUT for
// RPO/RTO updates, switchover preview, status SSE, fleet roll-up, and
// per-Sovereign DR summary.
//
// REST surface added on top of continuum.go's U-DR-1 routes:
//
//	GET    /api/v1/sovereigns/{id}/continuum/{name}              — singular alias of /continuums/{name}, returns enriched status
//	PUT    /api/v1/sovereigns/{id}/continuum/{name}              — patch spec.rpoSeconds + spec.rtoSeconds (operator+)
//	GET    /api/v1/sovereigns/{id}/continuum/{name}/stream       — SSE: status snapshot + walLagSeconds tick every 5s
//	POST   /api/v1/sovereigns/{id}/continuum/{name}/switchover/preview — dry-run: estimatedDuration + currentLag + blockingChecks[]
//	GET    /api/v1/fleet/continuum                               — fleet-wide list of Continuum CRs (items envelope)
//	GET    /api/v1/fleet/sovereigns/{id}/dr-summary              — per-Sov DR rollup: continuumCount, healthyCount, lastSwitchoverAge
//
// Plus singular aliases for the other 4 U-DR-1 routes so the matrix's
// `/continuum/{name}/switchover` etc all resolve. The original `/continuums/`
// (plural) routes stay live for back-compat — both shapes work.
//
// Per ADR-0001 §2.7 the Continuum CR remains the source of truth — PUT
// patches `spec.rpoSeconds` + `spec.rtoSeconds` and the controller
// reconciles. The preview endpoint is read-only — it computes
// estimatedDuration from observed walLagSeconds + RTO and surfaces the
// 7-step health probe results without committing anything.
//
// Per INVIOLABLE-PRINCIPLES #4 every URL is env-derived; nothing
// hardcoded. Per #5 the PUT requires operator tier on the Application
// (REUSES applicationInstallCallerAuthorized — same gate as POST
// switchover). Preview is read-only with the same gate as GET (no
// claims check — viewers see DR posture).
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

)

// ── Wire shapes ──────────────────────────────────────────────────────

// continuumEnrichedGetResponse — body of GET /continuum/{name}.
// Extends the original continuumGetResponse with the test-matrix-required
// flat fields (`currentPrimary`, `walLagSeconds`, `lastSwitchoverDuration`,
// `dnsObservation`, `rpoSeconds`, `rtoSeconds`) so the UI + matrix asserts
// resolve without parsing nested status.
type continuumEnrichedGetResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	UID       string                 `json:"uid"`
	Spec      map[string]interface{} `json:"spec,omitempty"`
	Status    map[string]interface{} `json:"status,omitempty"`

	// Convenience flat fields — duplicated from spec/status for the
	// matrix + UI's StatusPanel; the canonical source remains the CR.
	CurrentPrimary          string  `json:"currentPrimary"`
	PrimaryRegion           string  `json:"primaryRegion"`
	WALLagSeconds           float64 `json:"walLagSeconds"`
	LastSwitchoverDuration  float64 `json:"lastSwitchoverDurationSeconds"`
	LastSwitchoverDurationS string  `json:"lastSwitchoverDuration,omitempty"`
	DNSObservation          string  `json:"dnsObservation,omitempty"`
	RPOSeconds              int64   `json:"rpoSeconds"`
	RTOSeconds              int64   `json:"rtoSeconds"`
	Replicas                []continuumReplicaInfo `json:"replicas"`

	// Desired-state projection (desiredPrimaryRegion, converged,
	// switchoverInProgress, step, lastSwitchover, convergeReason/Message).
	// See continuum_convergence.go.
	continuumConvergence
}

type continuumReplicaInfo struct {
	Region        string  `json:"region"`
	Role          string  `json:"role"`
	LagSeconds    float64 `json:"lagSeconds"`
	LastHeartbeat string  `json:"lastHeartbeat,omitempty"`
}

// continuumFleetItem — one row in /fleet/continuum.
type continuumFleetItem struct {
	Sovereign      string  `json:"sovereign"`
	Name           string  `json:"name"`
	Namespace      string  `json:"namespace"`
	PrimaryRegion  string  `json:"primaryRegion"`
	CurrentPrimary string  `json:"currentPrimary"`
	Phase          string  `json:"phase"`
	WALLagSeconds  float64 `json:"walLagSeconds"`
	LeaseHolder    string  `json:"leaseHolder,omitempty"`
	LastSwitchover string  `json:"lastSwitchover,omitempty"`
	Healthy        bool    `json:"healthy"`

	// Desired-state projection (continuum_convergence.go). Flattened field
	// by field — the struct is not embedded because this row's
	// `lastSwitchover` is already the bare timestamp.
	DesiredPrimaryRegion string                   `json:"desiredPrimaryRegion,omitempty"`
	Converged            bool                     `json:"converged"`
	SwitchoverInProgress bool                     `json:"switchoverInProgress"`
	Step                 string                   `json:"step,omitempty"`
	LastSwitchoverDetail *continuumLastSwitchover `json:"lastSwitchoverDetail,omitempty"`
	ConvergeReason       string                   `json:"convergeReason,omitempty"`
	ConvergeMessage      string                   `json:"convergeMessage,omitempty"`
}

// continuumFleetResponse — items envelope per the matrix's TC-326.
//
// #5731 — `unreachable` names every Sovereign whose Continuums could
// NOT be listed. Without it an empty `items` is ambiguous between "no
// DR is configured anywhere" and "we could read nothing", and the fleet
// page would render the same calm empty state for both.
type continuumFleetResponse struct {
	Items       []continuumFleetItem `json:"items"`
	Total       int                  `json:"total"`
	Unreachable []string             `json:"unreachable,omitempty"`
}

// continuumDRSummary — body of /fleet/sovereigns/{id}/dr-summary.
type continuumDRSummary struct {
	Sovereign         string  `json:"sovereign"`
	ContinuumCount    int     `json:"continuumCount"`
	HealthyCount      int     `json:"healthyCount"`
	DegradedCount     int     `json:"degradedCount"`
	LastSwitchoverAge string  `json:"lastSwitchoverAge,omitempty"`
	LastSwitchoverAt  string  `json:"lastSwitchoverAt,omitempty"`
	MaxWALLagSeconds  float64 `json:"maxWalLagSeconds"`
}

// ── Handlers ─────────────────────────────────────────────────────────

// HandleContinuumGetEnriched — GET /api/v1/sovereigns/{id}/continuum/{name}
// (singular path; matrix-required). Returns the enriched shape with flat
// status fields the matrix's TC-329/TC-335 assert on.
func (h *Handler) HandleContinuumGetEnriched(w http.ResponseWriter, r *http.Request) {
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
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// #5728 — client init failed: 503. Never a fabricated
		// phase/lag for a cluster we could not read.
		writeContinuumUnreachable(w, name, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			// #5728 — CR absent: 404. The prior code answered 200
			// with phase:Healthy + walLagSeconds:2 + Hetzner
			// regions + a mothership hostname for a Continuum that
			// does not exist.
			writeContinuumNotFound(w, name)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-get-failed",
			"detail": getErr.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, enrichContinuumResponse(cr))
}

// HandleContinuumStream — GET /api/v1/sovereigns/{id}/continuum/{name}/stream
// SSE: emits the enriched status every 5s. Each frame is a `data: {json}`
// line carrying walLagSeconds + currentPrimary so the UI graph can tick.
func (h *Handler) HandleContinuumStream(w http.ResponseWriter, r *http.Request) {
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
	// #5728 — the client MUST be resolved before any SSE header is
	// written, so an unreachable cluster answers 503 rather than
	// streaming fabricated frames. The prior code discarded this error
	// and emitted synthesized "phase:Healthy, walLagSeconds:2" frames
	// forever, so the live-streaming DR panel read fiction.
	client, clientErr := h.sovereignDynamicClient(dep)
	if clientErr != nil || client == nil {
		writeContinuumUnreachable(w, name, clientErr)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))

	// Initial frame: synchronous read so the client sees status
	// immediately rather than waiting up to one tick.
	//
	// #5728 — a frame is emitted for EVERY tick, but a tick that could
	// not read the CR emits the honest unavailable envelope (no phase,
	// no lag, no region), never a synthesized healthy record. The
	// stream stays open because the CR may appear later.
	emitFrame := func() {
		cr, getErr := getContinuumCR(r.Context(), client, name, ns)
		if getErr != nil {
			writeSSEFrame(w, continuumStreamUnavailableFrame(name, getErr))
			flusher.Flush()
			return
		}
		writeSSEFrame(w, enrichContinuumResponse(cr))
		flusher.Flush()
	}
	emitFrame()

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			emitFrame()
		}
	}
}

// HandleFleetContinuum — GET /api/v1/fleet/continuum
// Aggregates Continuum CRs across every Sovereign known to this
// catalyst-api instance.
//
// #5731 — this is the page a sovereign-admin SCANS to find which
// Sovereign needs attention, so a row here is a health claim. It used
// to append a synthesized `cont-omantel` row with `Phase:"Healthy"`,
// `Healthy:true`, `WALLagSeconds:2` whenever no live CR was visible —
// including when every Sovereign was unreadable. An estate that cannot
// be read now reports ZERO rows, and every Sovereign whose Continuums
// could not be listed is named in `unreachable` so an empty list is
// never mistaken for "no DR configured".
func (h *Handler) HandleFleetContinuum(w http.ResponseWriter, r *http.Request) {
	out := continuumFleetResponse{Items: []continuumFleetItem{}}
	sovs := h.collectFleetSovereigns(r.Context())
	for _, s := range sovs {
		dep, ok := h.lookupDeploymentForInfra(s.ID)
		if !ok {
			out.Unreachable = append(out.Unreachable, s.ID)
			continue
		}
		client, err := h.sovereignDynamicClient(dep)
		if err != nil {
			out.Unreachable = append(out.Unreachable, s.ID)
			continue
		}
		list, err := client.Resource(ContinuumGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
		if err != nil {
			out.Unreachable = append(out.Unreachable, s.ID)
			continue
		}
		for i := range list.Items {
			cr := &list.Items[i]
			out.Items = append(out.Items, continuumItemFromCR(s.ID, cr))
		}
	}
	out.Total = len(out.Items)
	writeJSON(w, http.StatusOK, out)
}

// HandleFleetSovereignDRSummary — GET /api/v1/fleet/sovereigns/{id}/dr-summary
func (h *Handler) HandleFleetSovereignDRSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(id)
	if !ok {
		writeNotFound(w, id)
		return
	}
	out := continuumDRSummary{Sovereign: id}
	client, err := h.sovereignDynamicClient(dep)
	if err == nil {
		list, lerr := client.Resource(ContinuumGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
		if lerr == nil {
			var lastSwitchover time.Time
			for i := range list.Items {
				cr := &list.Items[i]
				out.ContinuumCount++
				phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
				switch phase {
				case "Healthy", "Pending", "":
					out.HealthyCount++
				case "Degraded", "FailedOver", "Failed", "SwitchingOver":
					out.DegradedCount++
				}
				lag := readNumericNested(cr.Object, "status", "walLagSeconds")
				if lag > out.MaxWALLagSeconds {
					out.MaxWALLagSeconds = lag
				}
				if at, _, _ := unstructured.NestedString(cr.Object, "status", "lastSwitchover", "at"); at != "" {
					if ts, err := time.Parse(time.RFC3339, at); err == nil && ts.After(lastSwitchover) {
						lastSwitchover = ts
					}
				}
			}
			if !lastSwitchover.IsZero() {
				out.LastSwitchoverAt = lastSwitchover.UTC().Format(time.RFC3339)
				age := time.Since(lastSwitchover).Round(time.Second)
				out.LastSwitchoverAge = age.String()
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Helpers ──────────────────────────────────────────────────────────

func enrichContinuumResponse(cr *unstructured.Unstructured) continuumEnrichedGetResponse {
	resp := continuumEnrichedGetResponse{
		Name:      cr.GetName(),
		Namespace: cr.GetNamespace(),
		UID:       string(cr.GetUID()),
		Replicas:  []continuumReplicaInfo{},
	}
	if specObj, ok, _ := unstructured.NestedMap(cr.Object, "spec"); ok {
		resp.Spec = specObj
	}
	if statusObj, ok, _ := unstructured.NestedMap(cr.Object, "status"); ok {
		resp.Status = statusObj
	}
	resp.PrimaryRegion, _, _ = unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	// The OBSERVED writer (status.primaryRegion) wins; spec.primaryRegion is
	// the pair's static home region and only a pre-reconcile fallback.
	resp.CurrentPrimary = continuumObservedPrimary(cr)
	if resp.CurrentPrimary == "" {
		resp.CurrentPrimary, _, _ = unstructured.NestedString(cr.Object, "status", "currentPrimary")
	}
	if resp.CurrentPrimary == "" {
		resp.CurrentPrimary = resp.PrimaryRegion
	}
	resp.continuumConvergence = continuumConvergenceFromCR(cr, resp.CurrentPrimary)
	resp.WALLagSeconds = readNumericNested(cr.Object, "status", "walLagSeconds")
	resp.LastSwitchoverDuration = readNumericNested(cr.Object, "status", "lastSwitchoverDurationSeconds")
	resp.LastSwitchoverDurationS, _, _ = unstructured.NestedString(cr.Object, "status", "lastSwitchover", "rtoObserved")
	resp.DNSObservation, _, _ = unstructured.NestedString(cr.Object, "status", "dnsObservation")
	if resp.DNSObservation == "" {
		// Synthesize from the lua-record record if present.
		if recs, ok, _ := unstructured.NestedSlice(cr.Object, "status", "lastLuaRecord", "records"); ok && len(recs) > 0 {
			if rec, ok := recs[0].(map[string]interface{}); ok {
				if h, ok := rec["hostname"].(string); ok {
					resp.DNSObservation = "lua:" + h
				}
			}
		}
	}
	if rpoSec := readNumericNested(cr.Object, "spec", "rpoSeconds"); rpoSec > 0 {
		resp.RPOSeconds = int64(rpoSec)
	} else if rpoStr, _, _ := unstructured.NestedString(cr.Object, "spec", "rpo"); rpoStr != "" {
		if n, err := parseDurationSecondsLocal(rpoStr); err == nil {
			resp.RPOSeconds = int64(n)
		}
	}
	if rtoSec := readNumericNested(cr.Object, "spec", "rtoSeconds"); rtoSec > 0 {
		resp.RTOSeconds = int64(rtoSec)
	} else if rtoStr, _, _ := unstructured.NestedString(cr.Object, "spec", "rto"); rtoStr != "" {
		if n, err := parseDurationSecondsLocal(rtoStr); err == nil {
			resp.RTOSeconds = int64(n)
		}
	}
	// Replicas: walk status.replicationLag map to surface lag per region.
	if rl, ok, _ := unstructured.NestedMap(cr.Object, "status", "replicationLag"); ok {
		for region, raw := range rl {
			ri := continuumReplicaInfo{Region: region, Role: "replica"}
			switch v := raw.(type) {
			case string:
				if n, err := parseDurationSecondsLocal(v); err == nil {
					ri.LagSeconds = float64(n)
				}
			case float64:
				ri.LagSeconds = v
			case int64:
				ri.LagSeconds = float64(v)
			}
			resp.Replicas = append(resp.Replicas, ri)
		}
	}
	if resp.CurrentPrimary != "" {
		resp.Replicas = append(resp.Replicas, continuumReplicaInfo{
			Region: resp.CurrentPrimary,
			Role:   "primary",
		})
	}
	return resp
}

func continuumItemFromCR(sov string, cr *unstructured.Unstructured) continuumFleetItem {
	item := continuumFleetItem{
		Sovereign: sov,
		Name:      cr.GetName(),
		Namespace: cr.GetNamespace(),
	}
	item.PrimaryRegion, _, _ = unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	// Observed writer first (status.primaryRegion); never spec.primaryRegion
	// while the controller has published an observation.
	item.CurrentPrimary = continuumObservedPrimary(cr)
	if item.CurrentPrimary == "" {
		item.CurrentPrimary, _, _ = unstructured.NestedString(cr.Object, "status", "currentPrimary")
	}
	if item.CurrentPrimary == "" {
		item.CurrentPrimary = continuumActingPrimary(cr)
	}
	conv := continuumConvergenceFromCR(cr, item.CurrentPrimary)
	item.DesiredPrimaryRegion = conv.DesiredPrimaryRegion
	item.Converged = conv.Converged
	item.SwitchoverInProgress = conv.SwitchoverInProgress
	item.Step = conv.Step
	item.LastSwitchoverDetail = conv.LastSwitchoverRecord
	item.ConvergeReason = conv.ConvergeReason
	item.ConvergeMessage = conv.ConvergeMessage
	item.Phase, _, _ = unstructured.NestedString(cr.Object, "status", "phase")
	item.WALLagSeconds = readNumericNested(cr.Object, "status", "walLagSeconds")
	item.LeaseHolder, _, _ = unstructured.NestedString(cr.Object, "status", "leaseHolder")
	item.LastSwitchover, _, _ = unstructured.NestedString(cr.Object, "status", "lastSwitchover", "at")
	item.Healthy = item.Phase == "" || item.Phase == "Healthy" || item.Phase == "Pending"
	return item
}

func writeSSEFrame(w http.ResponseWriter, payload interface{}) {
	if _, err := w.Write([]byte("data: ")); err != nil {
		return
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(payload)
	_, _ = w.Write([]byte("\n"))
}

// readNumericNested reads a numeric field that may be int64, float64, or
// numeric-string. unstructured.NestedFloat64 errors on int values which
// is the common case from a real CR.
func readNumericNested(obj map[string]interface{}, fields ...string) float64 {
	cur := interface{}(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return 0
		}
		v, ok := m[f]
		if !ok {
			return 0
		}
		cur = v
	}
	switch v := cur.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		// Accept "12s" or plain digits.
		if n, err := parseDurationSecondsLocal(v); err == nil {
			return float64(n)
		}
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f
		}
	}
	return 0
}

// parseDurationSecondsLocal — handler-local copy of the controller's
// duration parser. Accepts `[0-9]+(s|m|h)` or plain digits → seconds.
func parseDurationSecondsLocal(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	last := s[len(s)-1]
	if last >= '0' && last <= '9' {
		// Plain digits = seconds.
		n := 0
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("non-digit")
			}
			n = n*10 + int(c-'0')
		}
		return n, nil
	}
	digits := s[:len(s)-1]
	n := 0
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit")
		}
		n = n*10 + int(c-'0')
	}
	switch last {
	case 's':
		return n, nil
	case 'm':
		return n * 60, nil
	case 'h':
		return n * 3600, nil
	}
	return 0, fmt.Errorf("unknown unit")
}

// ── qa-loop iter-15 Fix #63 — target-state synthesizers ──────────────
//
// When the live Continuum CR or the in-cluster client is not yet
// available (the fleet fixture chart 1.4.128 has not yet rolled, OR
// the Sovereign chroot is bootstrapping), these helpers surface an
// HONEST "state could not be read" response — never an invented one.
//
// #5728 / #5731 — this block used to hold four synthesizers that
// asserted measurements nobody took:
//
//	synthesizedEnrichedContinuum   phase:Healthy, walLagSeconds:2
//	synthesizedFleetItem           Phase:Healthy, Healthy:true
//	synthesizedSwitchoverPreview   Promotable:true, BlockingChecks:[]
//	synthesizedSwitchoverCompleted status:completed, duration 60s (continuum.go)
//
// They composed a false-confidence loop: preview said "safe to fail
// over" → switchover said "completed" → fleet said "healthy" → the
// enriched GET said "healthy, 2s lag". Each was fabricated
// independently and they AGREED with each other, so cross-checking one
// against another could not detect it. All four are deleted.
//
// The rule they violated, which every future fallback must obey:
// a synthesized/fallback response may never carry a health verdict, a
// lag figure, a duration, a completion status, or an empty
// blocking-checks list. If the state could not be read, say so.
//
// The Hetzner/QA constants they leaned on (`fsn1`, `hz-hel-rtz-prod`,
// `qa-omantel`, `lua:pdm-1.openova.io`) are deleted with them — a
// cut-over Huawei Sovereign must never emit another cloud's region
// names or a mothership hostname as its own DR facts.

// continuumUnavailableResponse — the honest body for every Continuum
// read whose state could NOT be observed. It carries the identity of
// what was asked for and why it could not be answered, and NOTHING
// else: no phase, no lag, no duration, no region, no health verdict.
type continuumUnavailableResponse struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Error   string `json:"error"`
	Detail  string `json:"detail,omitempty"`
	Message string `json:"message"`
}

// writeContinuumUnreachable — 503. The in-cluster client could not be
// built, so nothing about this Continuum is known. Distinct from
// "no such Continuum" because it implies a different operator action.
func writeContinuumUnreachable(w http.ResponseWriter, name string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	writeJSON(w, http.StatusServiceUnavailable, continuumUnavailableResponse{
		Name:    name,
		Found:   false,
		Error:   "sovereign-unreachable",
		Detail:  detail,
		Message: "cannot reach the Sovereign cluster — DR state is UNKNOWN, not healthy",
	})
}

// writeContinuumNotFound — 404. The cluster answered and has no such
// Continuum CR. An absent DR record is not a healthy DR record.
func writeContinuumNotFound(w http.ResponseWriter, name string) {
	writeJSON(w, http.StatusNotFound, continuumUnavailableResponse{
		Name:    name,
		Found:   false,
		Error:   "continuum-not-found",
		Message: "no Continuum CR of that name exists on this Sovereign — no DR state to report",
	})
}

// continuumStreamUnavailableFrame — the honest SSE frame for a tick
// that could not read the Continuum CR. Same rule as the one-shot GET:
// no phase, no lag, no region — an unreadable DR record is unknown,
// never healthy.
func continuumStreamUnavailableFrame(name string, err error) continuumUnavailableResponse {
	detail := ""
	code := "continuum-read-failed"
	if err != nil {
		detail = err.Error()
		if apierrors.IsNotFound(err) {
			code = "continuum-not-found"
		}
	}
	return continuumUnavailableResponse{
		Name:    name,
		Found:   false,
		Error:   code,
		Detail:  detail,
		Message: "DR state could not be read on this tick — UNKNOWN, not healthy",
	}
}

// Compile-time assertions that we don't shadow names from continuum.go.
var _ = (*Handler).HandleContinuumGet

// continuumActingPrimary is the region serving writes NOW. The controller
// publishes the observed writer as status.primaryRegion (the failover
// target once the declared primary half is a replica). The preview used to
// read status.currentPrimary — a field nothing writes — and fell back to
// spec.primaryRegion, so after a switch to region B the console said
// "region A already primary" and offered no way back (hw307 2026-11-07).
func continuumActingPrimary(cr *unstructured.Unstructured) string {
	if s, _, _ := unstructured.NestedString(cr.Object, "status", "primaryRegion"); s != "" {
		return s
	}
	s, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	return s
}
