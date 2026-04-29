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
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
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

// toRecord serializes the deployment for the on-disk store. Caller MUST
// hold dep.mu — every persistence path (recordEventAndPersist on append,
// runProvisioning on terminal state, CreateDeployment on row creation,
// retry handlers on retry-init) calls this under the lock so the
// snapshot is internally consistent.
//
// The credential fields are dropped via store.Redact; see
// internal/store/store.go for the redaction list. The PDM reservation
// token is preserved (per-deployment opaque identifier) so a Pod
// restart in the gap between `tofu apply` returning and PDM /commit
// can still complete the commit on rehydration.
func (d *Deployment) toRecord() store.Record {
	return store.Record{
		ID:                  d.ID,
		Status:              d.Status,
		Request:             store.Redact(d.Request),
		Result:              d.Result,
		Error:               d.Error,
		StartedAt:           d.StartedAt,
		FinishedAt:          d.FinishedAt,
		Events:              append([]provisioner.Event(nil), d.eventsBuf...),
		PDMReservationToken: d.pdmReservationToken,
		PDMPoolDomain:       d.pdmPoolDomain,
		PDMSubdomain:        d.pdmSubdomain,
	}
}

// fromRecord rehydrates a Deployment from an on-disk record. Used by
// Handler.restoreFromStore at startup.
//
// The eventsCh and done channels are created closed: the runProvisioning
// goroutine no longer exists for a deployment loaded from disk (the
// catalyst-api Pod that ran it died), so the SSE replay path must
// behave as if the deployment is finished. StreamLogs.isDone() is the
// branch that distinguishes "in-flight, tail the channel" from
// "completed, replay then emit done"; loaded deployments take the
// completed branch unconditionally.
//
// If the on-disk Status is in-flight ("provisioning" / "tofu-applying"
// / "pending"), it is rewritten to "failed" with an explanatory error
// per the architectural requirement: a Pod restart during `tofu apply`
// orphans real Hetzner resources, and the wizard's FailureCard MUST
// surface that to the operator instead of showing a stuck progress
// bar forever. The orphaned resources are listed in the error message
// so the operator knows where to clean up.
func fromRecord(rec store.Record) *Deployment {
	closedCh := make(chan provisioner.Event)
	closedDone := make(chan struct{})
	close(closedCh)
	close(closedDone)

	dep := &Deployment{
		ID:                  rec.ID,
		Status:              rec.Status,
		Request:             rec.Request.ToProvisionerRequest(),
		Result:              rec.Result,
		Error:               rec.Error,
		StartedAt:           rec.StartedAt,
		FinishedAt:          rec.FinishedAt,
		eventsCh:            closedCh,
		eventsBuf:           append([]provisioner.Event(nil), rec.Events...),
		done:                closedDone,
		pdmReservationToken: rec.PDMReservationToken,
		pdmPoolDomain:       rec.PDMPoolDomain,
		pdmSubdomain:        rec.PDMSubdomain,
	}

	// In-flight at restart time → failed. The wizard's FailureCard is
	// the right surface for this state — operator must purge the
	// orphaned cloud resources by hand because catalyst-api can't
	// resume an OpenTofu run mid-apply (state-lock + the workdir on
	// /tmp emptyDir died with the previous Pod).
	if isInFlightStatus(rec.Status) {
		dep.Status = "failed"
		dep.Error = "catalyst-api restarted during provisioning — this deployment was abandoned mid-apply. Hetzner resources tagged with `catalyst-deployment-id=" + rec.ID + "` are orphans and must be purged manually (hcloud server, lb, network, firewall, ssh-key) before retrying. The wizard cannot resume — start a new deployment."
		dep.FinishedAt = time.Now()
	}
	return dep
}

func isInFlightStatus(s string) bool {
	switch s {
	case "pending", "provisioning", "tofu-applying", "flux-bootstrapping":
		return true
	}
	return false
}

// recordEventAndPersist appends ev to the durable history, persists the
// deployment to disk under the lock, and returns the event. This is the
// hot path — every emit goes through here. Persistence is best-effort:
// if disk write fails (full PVC, unmount), we log and continue. The
// in-memory state remains authoritative for the live SSE consumer; the
// on-disk gap is reported through h.log so an operator can spot it.
//
// We persist under d.mu so concurrent emits can't tear the on-disk
// record. The store's own mutex serializes the temp-file rename
// against any concurrent Save (e.g. the terminal-state Save in
// runProvisioning racing the last emitted event).
func (h *Handler) recordEventAndPersist(d *Deployment, ev provisioner.Event) provisioner.Event {
	d.mu.Lock()
	if len(d.eventsBuf) >= EventBufferCap {
		copy(d.eventsBuf, d.eventsBuf[1:])
		d.eventsBuf = d.eventsBuf[:len(d.eventsBuf)-1]
	}
	d.eventsBuf = append(d.eventsBuf, ev)
	rec := d.toRecord()
	d.mu.Unlock()

	if h.store != nil {
		if err := h.store.Save(rec); err != nil {
			h.log.Warn("persist deployment after event failed",
				"id", d.ID,
				"err", err,
			)
		}
	}
	return ev
}

// persistDeployment serializes the current state under the lock and
// writes it. Used at terminal state (runProvisioning end) and at
// row creation (CreateDeployment). Same best-effort policy as
// recordEventAndPersist.
func (h *Handler) persistDeployment(d *Deployment) {
	if h.store == nil {
		return
	}
	d.mu.Lock()
	rec := d.toRecord()
	d.mu.Unlock()
	if err := h.store.Save(rec); err != nil {
		h.log.Warn("persist deployment failed",
			"id", d.ID,
			"err", err,
		)
	}
}

// restoreFromStore reads every record from h.store and registers each
// in h.deployments. Called from New() after the store wires up so the
// catalyst-api process restored from a Pod restart still answers
// /api/v1/deployments/<id> for every deployment the previous Pod knew
// about. Per-file decode failures are logged but do not abort the
// load — the user-facing requirement is "as many deployments as
// possible recovered" not "all-or-nothing".
func (h *Handler) restoreFromStore() {
	if h.store == nil {
		return
	}
	records, err := h.store.LoadAll(func(path string, e error) {
		h.log.Warn("skipping unreadable deployment record",
			"path", path,
			"err", e,
		)
	})
	if err != nil {
		h.log.Error("restoreFromStore: walk failed; running with empty in-memory state",
			"err", err,
		)
		return
	}

	for _, rec := range records {
		dep := fromRecord(rec)
		h.deployments.Store(dep.ID, dep)
		// If the load step rewrote a stuck in-flight status to failed,
		// re-persist so the on-disk state matches what we serve. This
		// is the architectural promise: the wizard reading
		// /deployments/<id> after a Pod restart sees a coherent
		// terminal state, not a ghost still labelled "provisioning".
		if dep.Status != rec.Status && h.store != nil {
			if err := h.store.Save(dep.toRecord()); err != nil {
				h.log.Warn("re-persist rewritten in-flight record failed",
					"id", dep.ID,
					"err", err,
				)
			}
		}
	}
	h.log.Info("restored deployments from PVC",
		"count", len(records),
		"dir", h.store.Dir(),
	)
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

	// Persist the freshly-created deployment row before kicking off
	// the goroutine. If the catalyst-api Pod is killed in the gap
	// between Store and the first emit, the next Pod's restoreFromStore
	// rehydrates it as "failed: catalyst-api restarted during
	// provisioning" so the wizard's FailureCard renders. Without this
	// initial Save, the row wouldn't exist on disk yet and the wizard
	// would see a 404.
	h.persistDeployment(dep)

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
			// recordEventAndPersist appends to the durable buffer AND
			// rewrites the on-disk record. The user-reported regression
			// (deployment id 5cd1bceaaacb71f6 disappeared after a Pod
			// restart at 12:57) is closed by this single line: every
			// event the wizard sees has also hit ext4 by the time the
			// goroutine moves to the next iteration, so a Pod kill
			// loses at most the in-flight event the producer hadn't
			// emitted yet.
			recorded := h.recordEventAndPersist(dep, ev)
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

	// Terminal-state persist. The recordEventAndPersist loop above
	// already wrote the last per-event snapshot, but the terminal
	// status / Result / FinishedAt mutations happen after the producer
	// channel closed — so we need one more Save to capture them. This
	// is the line that guarantees a `status: ready` deployment on
	// disk after a clean run, instead of "still provisioning" frozen
	// at the last emitted event.
	h.persistDeployment(dep)

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
