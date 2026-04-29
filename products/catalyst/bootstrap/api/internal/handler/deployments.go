// Package handler — HTTP handlers wired to the OpenTofu-based provisioner.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #3 + docs/ARCHITECTURE.md §10:
// Phase 0 cloud provisioning is OpenTofu's job, NOT bespoke Go code. This
// handler invokes `tofu apply` against the canonical infra/hetzner/ module
// and streams the output to the wizard via SSE.
//
// Phase 1 hand-off (Crossplane adopting day-2 management) and bootstrap-kit
// installation (Cilium → cert-manager → Flux → Crossplane → ... → bp-catalyst-platform)
// happen INSIDE the cluster via Flux reconciling clusters/<sovereign-fqdn>/
// in the public OpenOva monorepo. The handler does not orchestrate that.
package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// EventBufferCap bounds the per-deployment in-memory event slice. A long-
// running multi-region `tofu apply` can emit thousands of stdout lines; if
// the buffer ever reaches this cap we drop the oldest entry (FIFO) so a
// runaway producer cannot OOM the catalyst-api Pod. 10,000 events ≈ 1MB at
// typical event size — well within the Pod's memory budget.
const EventBufferCap = 10000

// Deployment captures provisioning state for a single Sovereign run.
//
// Events flow lives in two parallel structures:
//
//   - eventsCh — the live SSE channel. runProvisioning closes this when the
//     provisioning goroutine finishes, which is what the existing StreamLogs
//     loop watches for `event: done`.
//   - eventsBuf — a bounded, mutex-guarded slice of every event ever emitted
//     for this deployment. StreamLogs reads this on first connection so a
//     browser that lands on the page AFTER provisioning finished still
//     renders the full history. GET /events surfaces the same slice as JSON
//     for any client that wants a one-shot snapshot.
//
// done is closed once runProvisioning has finished and the terminal state
// (Status, Result, Error, FinishedAt) is committed. StreamLogs uses it to
// know when a deployment is already complete (replay-then-emit-done) versus
// still running (replay-then-tail-channel).
type Deployment struct {
	ID         string
	Status     string // pending | provisioning | tofu-applying | flux-bootstrapping | ready | failed
	Request    provisioner.Request
	Result     *provisioner.Result
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time

	// eventsCh carries live events to the active SSE consumer. runProvisioning
	// emits to this channel; StreamLogs ranges over it. Closed by
	// runProvisioning when the goroutine finishes.
	eventsCh chan provisioner.Event

	// eventsBuf is the durable history every emitted event lands in. Mutex
	// guarded by mu. Bounded at EventBufferCap with FIFO eviction.
	eventsBuf []provisioner.Event

	// done is closed when runProvisioning has finished and the terminal
	// fields (Status, Result, Error, FinishedAt) are committed under mu.
	done chan struct{}

	mu sync.Mutex

	// PDM reservation captured before `tofu apply` for managed-pool
	// deployments. The reservationToken is held until `tofu apply`
	// returns the LB IP, at which point we POST it to PDM /commit. On
	// `tofu destroy` (or a phase-0 retry that decides to abandon) we
	// DELETE /release.
	//
	// Empty for BYO deployments — those keep their own DNS off-platform.
	pdmReservationToken string
	pdmPoolDomain       string
	pdmSubdomain        string
}

// recordEvent appends ev to the durable history under the mutex, evicting
// the oldest entry when the buffer is at cap. Returns the event back so
// callers can fluently send it down the live channel.
func (d *Deployment) recordEvent(ev provisioner.Event) provisioner.Event {
	d.mu.Lock()
	if len(d.eventsBuf) >= EventBufferCap {
		// FIFO eviction — drop oldest, keep newest. Allocate a fresh slice
		// so the underlying array doesn't keep growing without bound when
		// the cap is hit repeatedly (a copy is O(n) but the cap is bounded
		// so the amortised cost is fine for the single emit-per-line rate).
		copy(d.eventsBuf, d.eventsBuf[1:])
		d.eventsBuf = d.eventsBuf[:len(d.eventsBuf)-1]
	}
	d.eventsBuf = append(d.eventsBuf, ev)
	d.mu.Unlock()
	return ev
}

// snapshotEvents returns a copy of the durable history for safe iteration
// outside the mutex. Used by StreamLogs (replay-on-connect) and the
// /events endpoint.
func (d *Deployment) snapshotEvents() []provisioner.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]provisioner.Event, len(d.eventsBuf))
	copy(out, d.eventsBuf)
	return out
}

// isDone reports whether the runProvisioning goroutine has finished. Used
// by StreamLogs to distinguish "completed deployment, replay everything
// then send done" from "in-flight, replay then tail channel".
func (d *Deployment) isDone() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

// State returns a JSON-safe snapshot for the GET endpoint.
//
// numEvents surfaces the buffer size so callers polling /deployments/{id}
// can confirm the catalyst-api is recording progress even before they open
// the SSE stream. ProvisionPage uses this in its diagnostic readout.
func (d *Deployment) State() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]any{
		"id":            d.ID,
		"status":        d.Status,
		"startedAt":     d.StartedAt.Format(time.RFC3339),
		"finishedAt":    nil,
		"sovereignFQDN": d.Request.SovereignFQDN,
		"region":        d.Request.Region,
		"numEvents":     len(d.eventsBuf),
	}
	if !d.FinishedAt.IsZero() {
		out["finishedAt"] = d.FinishedAt.Format(time.RFC3339)
	}
	if d.Error != "" {
		out["error"] = d.Error
	}
	if d.Result != nil {
		out["result"] = d.Result
	}
	return out
}

func (h *Handler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req provisioner.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Inject Dynadot credentials when the customer chose a pool domain so the
	// OpenTofu module can write DNS records via the dynadot variables.
	// Credentials come from environment variables mounted from the
	// dynadot-api-credentials K8s secret in the openova-system namespace.
	if req.SovereignDomainMode == "pool" {
		req.DynadotAPIKey = h.dynadotAPIKey
		req.DynadotAPISecret = h.dynadotAPISecret
	}

	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	id := newID()
	dep := &Deployment{
		ID:        id,
		Status:    "provisioning",
		Request:   req,
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
	}

	// Reserve the pool subdomain via PDM BEFORE we kick off `tofu apply`.
	// PDM holds the name with a TTL — if `tofu apply` fails or this catalyst-
	// api Pod crashes, the TTL expires and the name is freed automatically.
	// On the success path the runProvisioning goroutine calls /commit with
	// the LB IP, which flips the reservation to ACTIVE and writes the
	// Dynadot DNS records.
	//
	// For BYO deployments (the customer owns the DNS zone) we skip PDM
	// entirely — the customer points their own CNAME at the LB IP shown
	// on the success screen.
	if req.SovereignDomainMode == "pool" && pdm.IsManagedDomain(req.SovereignPoolDomain) {
		if h.pdm == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "pool-domain-manager client is not configured (POOL_DOMAIN_MANAGER_URL)",
			})
			return
		}
		reserveCtx, reserveCancel := context.WithTimeout(r.Context(), 10*time.Second)
		reservation, reserveErr := h.pdm.Reserve(reserveCtx, req.SovereignPoolDomain, req.SovereignSubdomain, "catalyst-api/deployment-"+id)
		reserveCancel()
		if reserveErr != nil {
			if errors.Is(reserveErr, pdm.ErrConflict) {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error":  "subdomain-conflict",
					"detail": "this subdomain has been reserved or activated for the chosen pool — pick a different name",
				})
				return
			}
			h.log.Error("pdm reserve failed", "id", id, "err", reserveErr)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "pdm-unavailable",
				"detail": "pool-domain-manager is temporarily unreachable: " + reserveErr.Error(),
			})
			return
		}
		dep.pdmReservationToken = reservation.ReservationToken
		dep.pdmPoolDomain = reservation.PoolDomain
		dep.pdmSubdomain = reservation.Subdomain
	}

	h.deployments.Store(id, dep)

	// Capture status before launching the goroutine — runProvisioning races
	// with this read otherwise (the goroutine takes dep.mu before mutating
	// Status, but the response writer here reads it without the lock).
	initialStatus := dep.Status
	go h.runProvisioning(dep)

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":        id,
		"status":    initialStatus,
		"streamURL": fmt.Sprintf("/api/v1/deployments/%s/logs", id),
	})
}

// GetDeployment returns the current state of a deployment for polling.
func (h *Handler) GetDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	writeJSON(w, http.StatusOK, dep.State())
}

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Replay-on-connect: emit every event already in the durable buffer
	// before tailing the live channel. This is what makes navigating to
	// `/sovereign/provision/<completed-id>` render the full history instead
	// of an empty shell — a browser that connects after `event: done`
	// arrived at an already-closed channel previously got nothing.
	for _, ev := range dep.snapshotEvents() {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
	}
	flusher.Flush()

	// If the deployment is already complete, the live channel is closed and
	// the buffer above is the authoritative history. Emit `event: done`
	// with the terminal state and return — the browser won't try to
	// reconnect because the EventSource handler closes on `done`.
	if dep.isDone() {
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", mustJSON(dep.State()))
		flusher.Flush()
		return
	}

	// In-flight: tail the live channel. Any event arriving here also got
	// recorded into the buffer (recordEvent is the single emit path), so
	// the next reconnect after a flake will replay the same history plus
	// whatever arrived in between.
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-dep.eventsCh:
			if !open {
				fmt.Fprintf(w, "event: done\ndata: %s\n\n", mustJSON(dep.State()))
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
			flusher.Flush()
		}
	}
}

// GetDeploymentEvents returns the buffered event history + state JSON for
// a one-shot snapshot. ProvisionPage calls this on mount to seed the
// `applyEventToContext` reducer before opening the SSE stream — the SSE
// stream's replay-on-connect serves the same purpose, but a stateless GET
// is easier to test and gives the wizard a fast-path that doesn't need to
// hold an SSE socket open. Both paths read the same `eventsBuf`, so they
// agree by construction.
func (h *Handler) GetDeploymentEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	writeJSON(w, http.StatusOK, map[string]any{
		"state":  dep.State(),
		"events": dep.snapshotEvents(),
		"done":   dep.isDone(),
	})
}

func (h *Handler) runProvisioning(dep *Deployment) {
	// Tee — provisioner.Provision writes events into producer; this goroutine
	// records every event in the durable buffer AND forwards it to the live
	// SSE channel. recordEvent is the single emit path, so the buffer and
	// the live stream cannot diverge.
	producer := make(chan provisioner.Event, 256)
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		for ev := range producer {
			recorded := dep.recordEvent(ev)
			// Non-blocking send to the live channel — if no SSE consumer is
			// attached, the eventsCh buffer (256) absorbs the burst; once
			// full we drop on the live side ONLY (the durable buffer still
			// has the event, so the next reconnect replays it). This
			// preserves the existing channel-buffer-overflow semantics
			// while guaranteeing history retention.
			select {
			case dep.eventsCh <- recorded:
			default:
			}
		}
		close(dep.eventsCh)
	}()

	prov := provisioner.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := prov.Provision(ctx, dep.Request, producer)
	close(producer)
	<-teeDone

	dep.mu.Lock()
	dep.FinishedAt = time.Now()
	if err != nil {
		dep.Status = "failed"
		dep.Error = err.Error()
		h.log.Error("provision failed", "id", dep.ID, "err", err)
	} else {
		dep.Status = "ready"
		dep.Result = result
		h.log.Info("provision complete",
			"id", dep.ID,
			"sovereignFQDN", result.SovereignFQDN,
			"controlPlaneIP", result.ControlPlaneIP,
			"loadBalancerIP", result.LoadBalancerIP,
		)
	}
	dep.mu.Unlock()
	close(dep.done)

	// PDM lifecycle: on success, /commit with the LB IP; on failure, /release
	// so the reservation TTL doesn't have to expire to free the name. PDM is
	// the single owner of the Dynadot side-effect (it is also responsible for
	// AddSovereignRecords on commit; catalyst-api never writes DNS itself).
	if dep.pdmReservationToken != "" && h.pdm != nil {
		pdmCtx, pdmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer pdmCancel()
		if err == nil && result != nil {
			commitErr := h.pdm.Commit(pdmCtx, dep.pdmPoolDomain, pdm.CommitInput{
				Subdomain:        dep.pdmSubdomain,
				ReservationToken: dep.pdmReservationToken,
				SovereignFQDN:    result.SovereignFQDN,
				LoadBalancerIP:   result.LoadBalancerIP,
			})
			if commitErr != nil {
				h.log.Error("pdm commit failed; sovereign is live but DNS records may be stale",
					"id", dep.ID,
					"poolDomain", dep.pdmPoolDomain,
					"subdomain", dep.pdmSubdomain,
					"err", commitErr,
				)
			} else {
				h.log.Info("pdm commit complete",
					"id", dep.ID,
					"poolDomain", dep.pdmPoolDomain,
					"subdomain", dep.pdmSubdomain,
					"loadBalancerIP", result.LoadBalancerIP,
				)
			}
		} else {
			releaseErr := h.pdm.Release(pdmCtx, dep.pdmPoolDomain, dep.pdmSubdomain)
			if releaseErr != nil && !errors.Is(releaseErr, pdm.ErrNotFound) {
				h.log.Error("pdm release failed; reservation will expire on TTL",
					"id", dep.ID,
					"poolDomain", dep.pdmPoolDomain,
					"subdomain", dep.pdmSubdomain,
					"err", releaseErr,
				)
			}
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		fallback, _ := json.Marshal(map[string]string{
			"level": "error",
			"msg":   "failed to encode event: " + err.Error(),
		})
		return string(fallback)
	}
	return string(b)
}
