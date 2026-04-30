// Package handler — HTTP handlers wired to the OpenTofu-based provisioner.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #3 + docs/ARCHITECTURE.md §10:
// Phase 0 cloud provisioning is OpenTofu's job, NOT bespoke Go code. This
// handler invokes `tofu apply` against the canonical infra/hetzner/ module
// and streams the output to the wizard via SSE.
//
// Phase 1 — bootstrap-kit installation (Cilium → cert-manager → Flux →
// Crossplane → ... → bp-catalyst-platform) — runs INSIDE the new
// Sovereign cluster via Flux reconciling clusters/<sovereign-fqdn>/ in
// the public OpenOva monorepo. catalyst-api does NOT orchestrate that
// (per Lesson #24, never call helm/kubectl), but it DOES OBSERVE Phase
// 1's HelmRelease state via a read-only client-go dynamic informer
// against the new Sovereign's kubeconfig (internal/helmwatch). The
// observed state flows back through the same SSE buffer Phase 0 used,
// surfacing per-component pills ("cilium installing → installed") for
// the Sovereign Admin's app cards.
package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
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
//   - eventsCh — the live SSE channel. runProvisioning closes this when
//     BOTH the Phase-0 OpenTofu provisioning AND (when launched) the
//     Phase-1 HelmRelease watch have finished. StreamLogs ranges over
//     it; when it closes, the SSE stream emits `event: done`.
//   - eventsBuf — a bounded, mutex-guarded slice of every event ever
//     emitted for this deployment. StreamLogs reads this on first
//     connection so a browser that lands on the page AFTER
//     provisioning finished still renders the full history. GET
//     /events surfaces the same slice as JSON for any client that
//     wants a one-shot snapshot.
//
// done is closed once runProvisioning has finished AND the Phase-1
// watch has either terminated or was never launched (Phase-0 failure).
// StreamLogs uses it to know when a deployment is fully complete
// (replay-then-emit-done) versus still running (replay-then-tail-
// channel).
type Deployment struct {
	ID         string
	Status     string // pending | provisioning | tofu-applying | flux-bootstrapping | phase1-watching | ready | failed
	Request    provisioner.Request
	Result     *provisioner.Result
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time

	// eventsCh carries live events to the active SSE consumer.
	// runProvisioning + the Phase-1 watch goroutine both emit through
	// it; closed once both have finished.
	eventsCh chan provisioner.Event

	// eventsBuf is the durable history every emitted event lands in. Mutex
	// guarded by mu. Bounded at EventBufferCap with FIFO eviction.
	eventsBuf []provisioner.Event

	// done is closed when runProvisioning's full lifecycle (Phase 0 +
	// optional Phase 1 watch) has finished and the terminal fields
	// (Status, Result, Error, FinishedAt, ComponentStates,
	// Phase1FinishedAt) are committed under mu.
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

	// kubeconfigBearerHash — hex-encoded SHA-256 of the 32-byte bearer
	// token templated into the new Sovereign's cloud-init (issue #183).
	// Persisted on the on-disk record so a Pod restart can still
	// verify a delayed cloud-init PUT after the original Pod died.
	// The plaintext bearer NEVER lives on this struct — it is
	// generated in CreateDeployment, stamped onto the
	// provisioner.Request just long enough for writeTfvars to render
	// it, and then GC'd. The only durable copies are the cloud-init
	// template inside the Sovereign's user_data and the SHA-256 hash
	// here.
	kubeconfigBearerHash string

	// phase1Started gates the at-most-once launch of the Phase-1
	// helmwatch goroutine. Two callers race to start it:
	//   - runProvisioning, after `tofu apply` finishes
	//   - PutKubeconfig, after cloud-init posts back the kubeconfig
	// In the typical happy path cloud-init reaches healthz before
	// `tofu apply` returns (the LB reconcile is the slowest part of
	// Phase 0), so PutKubeconfig wins. A guard ensures the loser is
	// a no-op rather than racing two informers against the same
	// HelmRelease list.
	phase1Started bool

	// jobsBridge — per-deployment helmwatch → Job/Execution/LogLine
	// bridge (issue #205, sub of epic #204). Allocated on first
	// component event in emitWatchEvent. Nil-tolerant: the emit path
	// no-ops the forward when bridge is nil OR the Handler's jobs
	// store is nil (tests without persistence).
	jobsBridge *jobs.Bridge

	// liveWatcher — pointer to the helmwatch.Watcher currently
	// driving Phase-1 events for this deployment, populated by
	// runPhase1Watch / resumePhase1Watch / refreshWatch. The
	// /components/state endpoint reads SnapshotComponents() off it
	// to return the in-memory informer cache as JSON; the
	// /refresh-watch endpoint short-circuits to "already running"
	// when this is non-nil. Cleared after Watch() returns so a
	// subsequent /refresh-watch can spin up a fresh informer (the
	// previous Watcher's GVR informer is single-shot).
	//
	// Holding a pointer here is safe — Watcher is goroutine-safe
	// for SnapshotComponents() and the GC reclaims the old one
	// once the field is overwritten.
	liveWatcher *helmwatch.Watcher
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
		ID:                   d.ID,
		Status:               d.Status,
		Request:              store.Redact(d.Request),
		Result:               d.Result,
		Error:                d.Error,
		StartedAt:            d.StartedAt,
		FinishedAt:           d.FinishedAt,
		Events:               append([]provisioner.Event(nil), d.eventsBuf...),
		PDMReservationToken:  d.pdmReservationToken,
		PDMPoolDomain:        d.pdmPoolDomain,
		PDMSubdomain:         d.pdmSubdomain,
		KubeconfigBearerHash: d.kubeconfigBearerHash,
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
		ID:                   rec.ID,
		Status:               rec.Status,
		Request:              rec.Request.ToProvisionerRequest(),
		Result:               rec.Result,
		Error:                rec.Error,
		StartedAt:            rec.StartedAt,
		FinishedAt:           rec.FinishedAt,
		eventsCh:             closedCh,
		eventsBuf:            append([]provisioner.Event(nil), rec.Events...),
		done:                 closedDone,
		pdmReservationToken:  rec.PDMReservationToken,
		pdmPoolDomain:        rec.PDMPoolDomain,
		pdmSubdomain:         rec.PDMSubdomain,
		kubeconfigBearerHash: rec.KubeconfigBearerHash,
	}

	// Kubeconfig file lost across restart → mark as failed. If the
	// record claims a path but the file is missing, the helmwatch
	// goroutine has nothing to watch with. Per the spec for #183 we
	// surface this distinctly so the operator can investigate (PVC
	// unmount, accidental delete, fs corruption) instead of seeing a
	// stuck progress bar. The deployment record's Status was already
	// "ready" or "failed" pre-restart; we leave that alone, only
	// stamping a stricter Error message when KubeconfigPath has
	// drifted from the file system.
	if rec.Result != nil && rec.Result.KubeconfigPath != "" {
		if _, err := os.Stat(rec.Result.KubeconfigPath); err != nil {
			// Don't downgrade a healthy "ready" to "failed" silently;
			// only flag this when the deployment is otherwise
			// observable. The error message gives the operator
			// enough to grep the PVC.
			dep.Error = "kubeconfig file lost across catalyst-api restart: " + rec.Result.KubeconfigPath + " — " + err.Error()
		}
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
	case "pending", "provisioning", "tofu-applying", "flux-bootstrapping", "phase1-watching":
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

	resumed := 0
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

		// Resume the Phase-1 helmwatch goroutine after a Pod restart
		// when the rehydrated deployment carries a KubeconfigPath
		// pointing at an existing file (issue #183 spec gate #6).
		// The previous Pod died mid-watch — the kubeconfig file
		// survived on the PVC, so the new Pod can re-attach the
		// informer and continue streaming per-component events to
		// any wizard that polls /events or reopens the SSE stream.
		//
		// We skip resume for deployments whose Phase-1 already
		// terminated (Result.Phase1FinishedAt != nil) — re-running
		// the watcher on a finished deployment would just emit
		// duplicate events. We also skip when the file is missing
		// (fromRecord already stamped a clear Error on dep) and
		// when Status is the rewritten-to-failed phase1-watching
		// case — those are operator-actionable failures, not
		// resumable runs.
		if h.shouldResumePhase1(dep, rec) {
			resumed++
			h.resumePhase1Watch(dep)
		}
	}
	h.log.Info("restored deployments from PVC",
		"count", len(records),
		"resumed", resumed,
		"dir", h.store.Dir(),
	)
}

// shouldResumePhase1 returns true when a rehydrated deployment is a
// candidate for re-attaching the Phase-1 helmwatch goroutine. The
// criteria are:
//   - Result.KubeconfigPath is non-empty AND points at a readable file
//   - Phase 1 has NOT already terminated (Phase1FinishedAt == nil)
//   - Status was not rewritten by fromRecord — i.e. the original
//     status was NOT phase1-watching (which fromRecord rewrote to
//     failed). A genuinely-finished "ready" or "failed" deployment
//     is not resumed; only one that survived a restart with the
//     watch still owing work.
//
// Why we resume even when rec.Status is "ready" but Phase1FinishedAt
// is nil: that combination is a contract violation — markPhase1Done
// would have set Phase1FinishedAt before flipping Status to ready. If
// we ever observe it, it's a residual bug or hand-edited record;
// resume is the safer-default action because the helmwatch is
// idempotent (it just observes HelmRelease.status).
func (h *Handler) shouldResumePhase1(dep *Deployment, rec store.Record) bool {
	if dep.Result == nil || dep.Result.KubeconfigPath == "" {
		return false
	}
	if dep.Result.Phase1FinishedAt != nil {
		return false
	}
	// fromRecord rewrites in-flight statuses (including
	// "phase1-watching") to "failed" — those are not resumable.
	if isInFlightStatus(rec.Status) {
		return false
	}
	if _, err := os.Stat(dep.Result.KubeconfigPath); err != nil {
		return false
	}
	return true
}

// resumePhase1Watch re-attaches a Phase-1 helmwatch goroutine to a
// rehydrated deployment. The fromRecord path constructs the
// Deployment with closed eventsCh + done channels (because the
// previous Pod's runProvisioning goroutine no longer exists); we
// allocate fresh ones here so emitWatchEvent + StreamLogs see a
// live channel pair. A goroutine then runs runPhase1Watch which
// closes them when the watch terminates.
func (h *Handler) resumePhase1Watch(dep *Deployment) {
	dep.mu.Lock()
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.done = make(chan struct{})
	// The watcher will set phase1Started=true inside runPhase1Watch
	// under the same lock; we leave it false here so the launch is
	// correctly gated.
	dep.phase1Started = false
	// Re-flag the deployment as in-flight so isDone()/StreamLogs
	// behave correctly while the resumed watch runs. Status flips
	// back to "ready"/"failed" inside markPhase1Done when the
	// watch terminates.
	dep.Status = "phase1-watching"
	dep.mu.Unlock()

	h.log.Info("resuming phase 1 watch after pod restart",
		"id", dep.ID,
		"kubeconfigPath", dep.Result.KubeconfigPath,
	)

	go func() {
		h.runPhase1Watch(dep)
		// runPhase1Watch -> markPhase1Done flips Status terminal,
		// but does NOT close the channels (the original
		// runProvisioning closes them on the first-launch path).
		// On resume we own them, so close here to release any
		// SSE consumers waiting on `event: done`.
		dep.mu.Lock()
		select {
		case <-dep.done:
			// Already closed (defensive).
		default:
			close(dep.eventsCh)
			close(dep.done)
		}
		dep.mu.Unlock()
	}()
}

// State returns a JSON-safe snapshot for the GET endpoint.
//
// numEvents surfaces the buffer size so callers polling /deployments/{id}
// can confirm the catalyst-api is recording progress even before they open
// the SSE stream. ProvisionPage uses this in its diagnostic readout.
//
// componentStates + phase1FinishedAt surface the Phase-1 HelmRelease
// watch outcome to the Sovereign Admin shell so its top-level pill
// can render "X of Y components installed" without having to walk
// the full event buffer.
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
		// Lift the Phase-1 fields to the top level too — the
		// Sovereign Admin polls /deployments/<id> and reads them
		// without unwrapping result.
		if len(d.Result.ComponentStates) > 0 {
			out["componentStates"] = d.Result.ComponentStates
		}
		if d.Result.Phase1FinishedAt != nil {
			out["phase1FinishedAt"] = d.Result.Phase1FinishedAt.UTC().Format(time.RFC3339)
		}
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

	// Stamp the GHCR pull token from CATALYST_GHCR_PULL_TOKEN onto the
	// Request BEFORE Validate() so a missing-secret misconfiguration
	// surfaces here as 400 with a clear pointer to docs/SECRET-ROTATION.md
	// rather than 5 minutes into the runProvisioning goroutine. The
	// provisioner.New() inside runProvisioning re-stamps the same env
	// var as a defense-in-depth: if the env was missing here but the
	// Pod was rolled in between, the late stamp picks it up.
	//
	// The wizard payload NEVER carries this field — Request.GHCRPullToken
	// is `json:"-"` precisely so the wire format cannot inject it.
	if tok := os.Getenv("CATALYST_GHCR_PULL_TOKEN"); tok != "" {
		req.GHCRPullToken = tok
	}

	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	id := newID()

	// Mint the cloud-init kubeconfig postback bearer token (issue
	// #183, Option D) BEFORE kicking off the provisioner so
	// writeTfvars renders the plaintext into the Sovereign's
	// cloud-init. The plaintext lives on the provisioner.Request
	// only — never on the Deployment struct, never in the on-disk
	// JSON record. The hash is what we persist; constant-time
	// compared on PUT to verify the inbound bearer.
	bearerToken, bearerHash, err := newBearerToken()
	if err != nil {
		// crypto/rand failure is exceptional — the standard library
		// only fails this when the OS RNG is unavailable. Surface as
		// 500 so the wizard sees a coherent failure instead of a
		// silent crash.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "bearer-token-generation-failed",
			"detail": err.Error(),
		})
		return
	}
	req.DeploymentID = id
	req.KubeconfigBearerToken = bearerToken

	dep := &Deployment{
		ID:                   id,
		Status:               "provisioning",
		Request:              req,
		StartedAt:            time.Now(),
		eventsCh:             make(chan provisioner.Event, 256),
		done:                 make(chan struct{}),
		kubeconfigBearerHash: bearerHash,
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
	// Tee — provisioner.Provision writes events into producer; this
	// goroutine records every event in the durable buffer AND forwards
	// it to the live SSE channel. recordEvent is the single emit path,
	// so the buffer and the live stream cannot diverge. The Phase-1
	// watch (when launched) shares the same emit path via
	// h.emitWatchEvent so per-component events flow through identical
	// plumbing.
	producer := make(chan provisioner.Event, 256)
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		for ev := range producer {
			h.emitWatchEvent(dep, ev)
		}
	}()

	prov := provisioner.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := prov.Provision(ctx, dep.Request, producer)
	close(producer)
	<-teeDone

	// Capture Phase-0 outcome under the lock.
	dep.mu.Lock()
	if err != nil {
		dep.FinishedAt = time.Now()
		dep.Status = "failed"
		dep.Error = err.Error()
		h.log.Error("provision failed", "id", dep.ID, "err", err)
	} else {
		dep.Status = "phase1-watching"
		dep.Result = result
		h.log.Info("phase 0 complete; phase 1 watch starting",
			"id", dep.ID,
			"sovereignFQDN", result.SovereignFQDN,
			"controlPlaneIP", result.ControlPlaneIP,
			"loadBalancerIP", result.LoadBalancerIP,
		)
	}
	dep.mu.Unlock()
	// Persist the Phase-0 terminal state (ready or failed). This is
	// the line that guarantees a `status: phase1-watching` (or
	// `failed`) deployment on disk before the Phase-1 watch starts,
	// so a Pod kill in the gap between Phase 0 and Phase 1 can be
	// resumed/diagnosed correctly.
	h.persistDeployment(dep)

	// Phase 1 — HelmRelease watch. Only runs on Phase-0 success and
	// only when a kubeconfig is available. The watch emits per-
	// component events into the same SSE buffer + live channel; when
	// it terminates, it writes ComponentStates + Phase1FinishedAt
	// onto dep.Result and flips Status to ready (or leaves failed
	// alone if Phase 0 already failed).
	if err == nil && result != nil {
		h.runPhase1Watch(dep)
	}

	// Close the SSE live channel + done signal AFTER both phases
	// have settled. Existing tests that drive runProvisioning's
	// failure-fast path (no real tofu) still hit close because the
	// Phase-0 error skips the watch.
	close(dep.eventsCh)
	close(dep.done)

	// Final persist — captures Phase 1 terminal state when the watch
	// ran, or is a no-op for the Phase 0 failure path (already
	// persisted above).
	h.persistDeployment(dep)

	// PDM lifecycle: on success, /commit with the LB IP; on failure,
	// /release so the reservation TTL doesn't have to expire to free
	// the name. PDM is the single owner of the Dynadot side-effect
	// (it is also responsible for AddSovereignRecords on commit;
	// catalyst-api never writes DNS itself). The commit happens
	// post-Phase-0 because the LB IP is the only data PDM needs;
	// the Phase-1 watch outcome does NOT change DNS routing.
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

// emitWatchEvent — single emit path for Phase 0 + Phase 1 events.
// Records into the durable buffer (which persists every event to
// disk) and forwards onto the live SSE channel. Non-blocking send to
// eventsCh: if no consumer is attached, the buffer (256) absorbs the
// burst; once full we drop on the LIVE side only — the durable
// buffer still has the event so the next /events poll or SSE
// reconnect replays it.
//
// The same call also forwards Phase-1 component events to the per-
// deployment jobs.Bridge, which materialises Jobs / Executions / LogLines
// into the new /api/v1/deployments/{id}/jobs surface (issue #205, sub
// of epic #204). The bridge write is best-effort: a failure does NOT
// abort the SSE feed (the durable buffer is the contract for
// /api/v1/deployments/{id}/events; the jobs surface is a parallel
// projection). Errors are logged at warn so an operator can spot
// drift.
func (h *Handler) emitWatchEvent(dep *Deployment, ev provisioner.Event) {
	recorded := h.recordEventAndPersist(dep, ev)

	// Synchronise channel send with the close() path in
	// resumePhase1Watch (which closes dep.eventsCh under dep.mu after
	// the watch loop returns). Without this guard a helmwatch
	// goroutine still in-flight when resumePhase1Watch closes
	// eventsCh races the close — the race detector flags the read
	// of eventsCh against the close write. Holding dep.mu here makes
	// the close-vs-send linearisation point unambiguous, and the
	// `done` short-circuit prevents a panic-on-closed-channel send.
	dep.mu.Lock()
	closed := false
	select {
	case <-dep.done:
		closed = true
	default:
	}
	if !closed {
		select {
		case dep.eventsCh <- recorded:
		default:
		}
	}
	bridge := dep.jobsBridge
	if h.jobs != nil && bridge == nil {
		bridge = jobs.NewBridge(h.jobs, dep.ID)
		dep.jobsBridge = bridge
	}
	dep.mu.Unlock()

	// Forward Phase-1 component events to the jobs bridge. Phase-0
	// OpenTofu events have no Job analogue and are silently dropped
	// inside the bridge (it filters on Phase=="component" + non-
	// empty Component). The bridge write is best-effort: a failure
	// does NOT abort the SSE feed.
	if bridge == nil {
		return
	}
	if err := bridge.OnProvisionerEvent(recorded); err != nil {
		h.log.Warn("jobs bridge: forward failed",
			"id", dep.ID,
			"phase", recorded.Phase,
			"component", recorded.Component,
			"err", err,
		)
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newBearerToken mints the cloud-init kubeconfig postback bearer
// (issue #183, Option D). Returns (plaintextHex, sha256Hex, error).
//
// 32 bytes from crypto/rand → 64 hex chars. The plaintext NEVER
// lands on disk on the catalyst-api side — it flows out via tfvars
// into the new Sovereign's cloud-init user_data and is consumed
// exactly once when the new control plane PUTs back its kubeconfig.
// The SHA-256 hash is what we persist on the deployment record;
// the PUT handler constant-time compares the inbound bearer's hash
// to that value.
//
// Returns an error only when crypto/rand fails — the standard
// library surfaces that exclusively when the OS RNG is unavailable,
// which is catastrophic for the whole process. The
// CreateDeployment caller translates the error into HTTP 500.
func newBearerToken() (plaintext, hashHex string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("crypto/rand: %w", err)
	}
	plaintext = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plaintext))
	hashHex = hex.EncodeToString(sum[:])
	return plaintext, hashHex, nil
}

// hashBearerToken returns the hex-encoded SHA-256 of the supplied
// bearer plaintext. Used by the PUT /kubeconfig handler to compare
// the inbound bearer to the persisted hash.
func hashBearerToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
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
