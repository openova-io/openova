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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"

	// Side-effect import: registers every CloudProvider adapter so
	// providers.Get(...) returns a live value in this package's tests
	// (which build without cmd/api's main.go).
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
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
	Status     string // pending | provisioning | tofu-applying | flux-bootstrapping | phase1-watching | ready | failed | partial-failure
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

	// AdoptedAt — set by the handover finalisation flow (issue #317) once
	// the customer has accepted operational ownership of their Sovereign.
	// Until set, console.openova.io/sovereign/<id> renders the normal
	// Catalyst-Zero admin shell. Once set, the UI's beforeLoad redirect
	// (issue #319) sends the operator to https://console.<sovereign-fqdn>
	// instead — the customer's Sovereign is now self-sufficient and
	// Catalyst-Zero retains only the minimum-retention surface (PDM
	// allocation row + parent-zone NS delegation for pool subdomains).
	//
	// This field is the contract between #317 (writer) and #319
	// (reader). #317's handover handler stamps it; #319's redirect +
	// decommission UI consume it.
	AdoptedAt *time.Time `json:"adoptedAt,omitempty"`

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

	// OwnerEmail — the email address of the operator who created this
	// deployment (issue #689). Stamped at CreateDeployment time from the
	// authenticated session (X-User-Email injected by RequireSession).
	// Read by checkOwnership() on every /deployments/{id}* handler so a
	// signed-in operator cannot read or mutate another operator's
	// deployment. Empty for legacy records persisted before #689 — the
	// check skips with a log warning when empty so an in-place upgrade
	// doesn't lock operators out of pre-existing rows.
	OwnerEmail string

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

	// Multi-region kubeconfig PUT-back (operator mandate, 2026-05-12).
	// secondaryKubeconfigPaths maps region key (e.g. "nbg1-1",
	// "hel1-2") → on-disk path of the per-region kubeconfig file.
	// Populated by PutKubeconfig when ?region=<k> is set; consumed
	// by runPhase1Watch to spawn one helmwatch.Bridge per region so
	// the canvas surfaces install-* HRs from EVERY region, not just
	// primary. The primary's kubeconfig stays on Result.KubeconfigPath.
	//
	// secondaryWatchers parallels the map — one running helmwatch
	// .Watcher per region whose Snapshot is composed into the flow
	// snapshot. Cleared on wipe.
	secondaryKubeconfigPaths map[string]string
	secondaryWatchers        map[string]*helmwatch.Watcher
}

// SlimForHandover returns a copy of the receiver retaining ONLY the
// fields a post-handover record needs:
//
//   - id, sovereignFQDN, createdAt (StartedAt), createdBy (Request.OrgEmail)
//     for audit;
//   - AdoptedAt (= adoptedAt) for the redirect contract consumed by
//     UI/router.tsx::maybeRedirectToCustomerConsole (issue #319).
//
// All operational/runtime state (Result, TofuState, kubeconfig path on
// Result.KubeconfigPath, PDM bearer hash, pdm reservation token,
// component states, error string, finishedAt) is zeroed. The slim
// shape is what is persisted by FinaliseHandover (issue #453) instead
// of deleting the record entirely — deletion would 404 the redirect
// fetch and the customer console would never be reached.
//
// Per docs/INVIOLABLE-PRINCIPLES.md §0 minimum-retention: Catalyst-Zero
// keeps ONLY the structural breadcrumb required for the redirect to
// fire; everything else lives on the Sovereign side post-handover.
//
// The returned Deployment carries closed eventsCh + done channels so
// any leftover SSE consumer attempts on the slim record terminate
// immediately (matching the fromRecord rehydration shape).
func (d *Deployment) SlimForHandover(adoptedAt time.Time) *Deployment {
	closedCh := make(chan provisioner.Event)
	closedDone := make(chan struct{})
	close(closedCh)
	close(closedDone)

	stamped := adoptedAt.UTC()

	d.mu.Lock()
	id := d.ID
	startedAt := d.StartedAt
	orgName := d.Request.OrgName
	orgEmail := d.Request.OrgEmail
	sovereignFQDN := d.Request.SovereignFQDN
	d.mu.Unlock()

	return &Deployment{
		ID:     id,
		Status: "adopted",
		// Request retains ONLY the audit-minimum context (orgName,
		// orgEmail, sovereignFQDN). All credentials + sizing + region
		// detail are intentionally zeroed — they belong to the
		// Sovereign side now.
		Request: provisioner.Request{
			OrgName:       orgName,
			OrgEmail:      orgEmail,
			SovereignFQDN: sovereignFQDN,
		},
		StartedAt: startedAt,
		// FinishedAt is also "adoptedAt" semantically — set to the same
		// stamp so the wizard's terminal-state surfaces show a coherent
		// timeline.
		FinishedAt: stamped,
		AdoptedAt:  &stamped,
		eventsCh:   closedCh,
		done:       closedDone,
	}
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
		AdoptedAt:            d.AdoptedAt,
		OwnerEmail:           d.OwnerEmail,
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
		AdoptedAt:            rec.AdoptedAt,
		OwnerEmail:           rec.OwnerEmail,
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

	// Phase-0 in-flight at restart time → failed. The wizard's
	// FailureCard is the right surface for this state — operator must
	// purge the orphaned cloud resources by hand because catalyst-api
	// can't resume an OpenTofu run mid-apply (state-lock + the workdir
	// on /tmp emptyDir died with the previous Pod).
	//
	// "phase1-watching" is deliberately EXCLUDED from this rewrite (issue
	// #830 Bug 3). Phase 1 is purely observational — Phase 0 has already
	// committed its tofu state, the Hetzner resources exist and are
	// reachable, the kubeconfig is on the PVC, and the bootstrap-kit
	// HelmReleases keep reconciling on the Sovereign cluster regardless
	// of whether catalyst-api's in-memory watcher is alive. The new Pod
	// can re-attach the helmwatch informer and continue streaming
	// per-component state. The on-disk record retains Status=
	// "phase1-watching" so /deployments/{id} keeps reporting the truth
	// (the watch is in-flight, not failed) until the resumed watcher
	// terminates and markPhase1Done flips Status to ready/failed.
	//
	// Without this carve-out, a transient catalyst-api roll mid-Phase-1
	// (image bump, OOM, node maintenance) latches the deployment record
	// to status=failed even though the Sovereign cluster reaches Ready=
	// True for every HelmRelease seconds later — the auto-fire handover
	// then never triggers and the operator is stranded on the wizard
	// page. otech102 incident, 2026-05-04. The downstream resume happens
	// in restoreFromStore via shouldResumePhase1 + resumePhase1Watch.
	if isPhase0InFlightStatus(rec.Status) {
		dep.Status = "failed"
		dep.Error = "catalyst-api restarted during provisioning — this deployment was abandoned mid-apply. Hetzner resources tagged with `catalyst-deployment-id=" + rec.ID + "` are orphans and must be purged manually (hcloud server, lb, network, firewall, ssh-key) before retrying. The wizard cannot resume — start a new deployment."
		dep.FinishedAt = time.Now()
	}
	return dep
}

// isInFlightStatus reports whether a deployment is operationally
// still running. Used by the orphan-PDM-release decision logic to
// avoid releasing a slot that's still active.
func isInFlightStatus(s string) bool {
	switch s {
	case "pending", "provisioning", "tofu-applying", "flux-bootstrapping", "phase1-watching":
		return true
	}
	return false
}

// isPhase0InFlightStatus reports whether a deployment was abandoned
// mid-Phase-0 (a state catalyst-api cannot resume — tofu workdir lives
// on /tmp emptyDir which dies with the Pod). These statuses get
// rewritten to "failed" on restart so the wizard renders FailureCard.
//
// "phase1-watching" is deliberately EXCLUDED — Phase 1 is observational
// and resumable across Pod restarts. See fromRecord's comment for the
// full rationale (issue #830 Bug 3).
func isPhase0InFlightStatus(s string) bool {
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

	resumed := 0
	meshReconciles := 0
	meshRetries := 0
	spineReconciles := 0
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
			// Pod-restart-orphan PDM release (issue #489). The previous
			// catalyst-api Pod owned the runProvisioning goroutine that
			// would have called pdm.Release on Phase-0 failure. The
			// goroutine died with the Pod; the reservation is now
			// orphaned in PDM, locking the subdomain forever. fromRecord
			// rewrites the status to "failed" precisely BECAUSE the
			// previous Pod was killed mid-apply — the same condition
			// means we MUST release the PDM slot here, otherwise a
			// retry under the same subdomain returns 409 conflict and
			// the franchise customer is forced to pick `acmeN+1` for
			// what should be a routine retry.
			//
			// Best-effort + asynchronous: a slow PDM at startup must
			// NOT block the rest of the rehydration loop (other
			// deployments need to land in sync.Map quickly so /events
			// poll works).
			if dep.Status == "failed" && rec.PDMReservationToken != "" &&
				rec.PDMPoolDomain != "" && rec.PDMSubdomain != "" &&
				dep.AdoptedAt == nil && h.pdm != nil {
				go h.releaseOrphanedReservation(dep.ID, rec.PDMPoolDomain, rec.PDMSubdomain)
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
		} else if h.shouldStartupClusterMeshReconcile(dep) {
			// #3241 — level-triggered ClusterMesh reconcile on startup.
			// A ready multi-region deployment whose Phase-1 terminated
			// on a previous Pod gets the same retry loop markPhase1Done
			// fires, so a partially-meshed Sovereign (hw126 shape: the
			// one-shot fan-out raced LB-IPAM and gave up forever) heals
			// zero-touch on the next catalyst-api roll. Every establish
			// step is idempotent, and the loop's first attempt on an
			// already-fully-meshed Sovereign confirms + exits. Resumed
			// Phase-1 watches are excluded — their markPhase1Done fires
			// the loop itself on termination. Deployments with missing
			// kubeconfigs are warn-and-skipped inside the should-helper.
			meshReconciles++
			go h.runAutoEstablishClusterMesh(dep)
		} else if h.clusterMeshReconcileStatusGate(dep) {
			// #4811 — the deployment IS a ready multi-region mesh candidate but
			// shouldStartupClusterMeshReconcile returned false ONLY because the
			// primary kubeconfig was unresolved at restore (the k8scache
			// dir-load race: restoreFromStore runs during New(), sometimes
			// milliseconds before the kubeconfig file lands on the PVC, so the
			// resolvePrimaryKubeconfigPath os.Stat misses it). The old one-shot
			// dropped the reconcile forever, so the stale cilium-clustermesh
			// endpoint (:2379 KINE port instead of the :12379 proxy dial port) —
			// regenerated only by the establish loop's steady-state heal — never
			// healed and region-b's mesh stayed 0/1 across every OOM restart.
			// Start a bounded background retry that re-checks and fires the
			// establish the moment the kubeconfig resolves; it self-bounds so a
			// genuinely-lost kubeconfig still stops.
			meshRetries++
			go h.retryStartupClusterMeshReconcile(dep)
		}
		// #3319 (founder, 2026-06-12) — converged-late handover. MUST be
		// an INDEPENDENT hook, not the else-arm above: #3317 made the
		// mesh branch accept failed+TIMEOUT records too, so as an
		// else-if this never evaluated (witnessed live: mothership
		// logged clusterMeshReconciles=1 for hw130 and no rescue). The
		// two are complementary — mesh reconcile AND rescue both fire
		// for a converged-late record; each is idempotent.
		if h.shouldConvergedLateRescue(dep) {
			go h.runConvergedLateRescue(dep)
		}

		// #4212 — level-triggered spine Application-CR enrollment on startup.
		// The spine producer (runPostHandoverSpineApplications) is otherwise
		// one-shot (Phase-1 OutcomeReady + converged-late only), so a
		// Sovereign that handed over before the producer shipped never
		// enrolls its DR-capable spine into the object model and mints no
		// Continuum CR. This re-fires the idempotent producer (force:true
		// server-side-apply) for any ready, handed-over deployment, healing
		// the seam zero-touch on the next catalyst-api roll. Independent of
		// the rescue hook above — a normally-handed-over record never enters
		// the converged-late branch, so the spine would otherwise stay
		// unenrolled forever. No-op on an already-enrolled Sovereign.
		if h.shouldStartupSpineReconcile(dep) {
			spineReconciles++
			go h.runPostHandoverSpineApplications(dep)
		}

		// #1907 — bake-time top-up of the canonical .omani.X org-pool.
		// On a Pod restart the import already ran on a prior Pod and
		// the on-disk record may still be short of 4 entries (the
		// situation #1907 caught on t31 — mother stamped 2 entries,
		// catalyst-api restarted, /parent-domains kept returning 2).
		// No-op on the mothership (SOVEREIGN_FQDN unset); idempotent
		// when the pool is already full. See
		// chroot_parent_domains_seed.go for the full rationale.
		h.chrootEnsureOrgPoolSeed(dep)
	}
	h.log.Info("restored deployments from PVC",
		"count", len(records),
		"resumed", resumed,
		"clusterMeshReconciles", meshReconciles,
		"clusterMeshRetries", meshRetries,
		"spineReconciles", spineReconciles,
		"dir", h.store.Dir(),
	)
}

// releaseOrphanedReservation calls pdm.Release for a deployment whose
// status was rewritten to "failed" because the catalyst-api Pod died
// mid-provisioning (issue #489). Best-effort: any failure is logged
// at warn but does not block other rehydration work. Idempotent — a
// pdm.ErrNotFound response (the reservation already expired or was
// previously released) is treated as success. The dep entry's PDM
// pointers are cleared on success so a follow-up Cancel & Wipe doesn't
// double-release.
//
// Run as a goroutine from restoreFromStore: the rehydration loop must
// not block on PDM. A 30s timeout caps each call.
func (h *Handler) releaseOrphanedReservation(deploymentID, poolDomain, subdomain string) {
	if h.pdm == nil || poolDomain == "" || subdomain == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := h.pdm.Release(ctx, poolDomain, subdomain)
	if err != nil && !errors.Is(err, pdm.ErrNotFound) {
		h.log.Warn("orphan-release: pdm release failed; subdomain may stay locked until TTL",
			"id", deploymentID,
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"err", err,
		)
		return
	}
	h.log.Info("orphan-release: released PDM allocation for pod-restart-orphaned deployment",
		"id", deploymentID,
		"poolDomain", poolDomain,
		"subdomain", subdomain,
	)
	if val, ok := h.deployments.Load(deploymentID); ok {
		dep := val.(*Deployment)
		dep.mu.Lock()
		dep.pdmPoolDomain = ""
		dep.pdmSubdomain = ""
		dep.pdmReservationToken = ""
		dep.mu.Unlock()
		h.persistDeployment(dep)
	}
}

// shouldResumePhase1 returns true when a rehydrated deployment is a
// candidate for re-attaching the Phase-1 helmwatch goroutine. The
// criteria are:
//   - Result.KubeconfigPath is non-empty AND points at a readable file
//   - Phase 1 has NOT already terminated (Phase1FinishedAt == nil)
//   - Original on-disk status was NOT a Phase-0 in-flight state (those
//     are unrecoverable — fromRecord rewrites them to "failed")
//
// Resume IS triggered for the following original statuses:
//   - "phase1-watching" — the canonical durable-watcher case (issue
//     #830 Bug 3). Phase 0 already committed its tofu state, the
//     Sovereign cluster is healthy, and the new Pod re-attaches the
//     helmwatch informer to continue streaming per-component events.
//   - Terminal states ("ready", "failed", "adopted") with
//     Phase1FinishedAt == nil — contract-violation residual; resume is
//     the safer-default action because the helmwatch is idempotent
//     (it just observes HelmRelease.status — no patches/applies).
//
// Why we resume even when rec.Status is "ready" but Phase1FinishedAt
// is nil: that combination is a contract violation — markPhase1Done
// would have set Phase1FinishedAt before flipping Status to ready. If
// we ever observe it, it's a residual bug or hand-edited record;
// resume is the safer-default action because the helmwatch is
// idempotent (it just observes HelmRelease.status).
func (h *Handler) shouldResumePhase1(dep *Deployment, rec store.Record) bool {
	if dep.Result != nil && dep.Result.Phase1FinishedAt != nil {
		return false
	}
	// Phase-0 in-flight statuses are rewritten to "failed" by
	// fromRecord — those are not resumable (tofu workdir died with
	// the previous Pod, Hetzner resources are orphaned).
	// "phase1-watching" is NOT in this set per issue #830 Bug 3 — the
	// watcher IS resumable, and resuming is the whole point of the
	// durable-watcher fix.
	if isPhase0InFlightStatus(rec.Status) {
		return false
	}
	// #3153: resolve the kubeconfig path with a CONVENTIONAL fallback.
	// Result.KubeconfigPath carries json `omitempty` and can be lost on a
	// rehydrated record when a mothership roll persists the deployment
	// before PutKubeconfig stamped the path — but the kubeconfig FILE itself
	// survives on the PVC at <kubeconfigsDir>/<id>.yaml. Without this
	// fallback the resume is skipped and the operator goes permanently blind
	// to convergence after ANY catalyst-api roll (image bump, OOM, node
	// maintenance). Resuming read-only is harmless; skipping it is the bug.
	//
	// The resolved path is stamped back onto dep.Result.KubeconfigPath so
	// the resumed watch + StreamLogs see it. The stamp lives HERE, not in
	// resolvePrimaryKubeconfigPath: this path runs once at startup
	// (restoreFromStore, before traffic), while the helper is also called
	// from the mesh-reconcile goroutine where a Result write would race the
	// lock-free JSON marshal of the *Result pointer State() hands out
	// (#3241 -race regression). #3241 lifted the resolution into the shared
	// helper so the startup ClusterMesh reconcile path resolves the primary
	// kubeconfig identically.
	path, ok := h.resolvePrimaryKubeconfigPath(dep)
	if ok {
		dep.mu.Lock()
		if dep.Result == nil {
			dep.Result = &provisioner.Result{}
		}
		dep.Result.KubeconfigPath = path
		dep.mu.Unlock()
	}
	return ok
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

	// phase1WatchWG (test-only; nil in production) lets a test await this
	// resumed watch before returning, exactly as PutKubeconfig does for the
	// first-launch spawn (#4932). restoreFromStore fires this on a
	// rehydrated phase1-watching record, and its terminal markPhase1Done →
	// persistDeployment write into the store dir races Go's testing
	// RemoveAll cleanup when the store is a t.TempDir() (the #4934 restore
	// leak, e.g. TestPodRestart_Phase1WatchingResumesWithKubeconfig). Add(1)
	// runs before `go` (the WaitGroup contract); Done fires when the
	// goroutine fully unwinds.
	if h.phase1WatchWG != nil {
		h.phase1WatchWG.Add(1)
	}
	go func() {
		if h.phase1WatchWG != nil {
			defer h.phase1WatchWG.Done()
		}
		h.runPhase1Watch(dep)
		// runPhase1Watch -> markPhase1Done flips Status terminal,
		// but does NOT close the channels (the original
		// runProvisioning closes them on the first-launch path).
		// On resume we own them, so close here to release any
		// SSE consumers waiting on `event: done`.
		//
		// Wave 5.140 (hw30 #16 fix-forward 2026-05-27): defense for
		// `close of nil channel` panic — a concurrent wipe handler
		// (wipe.go:649) sets dep.eventsCh = nil after close. Snapshot
		// channels under dep.mu, nil-check, defer recover. Pod crashed
		// at 2026-05-27T02:33:34Z in this site during hw30 #16's
		// provisioning, abandoning the active prov mid-apply.
		defer func() { _ = recover() }()
		dep.mu.Lock()
		evCh := dep.eventsCh
		doneCh := dep.done
		dep.mu.Unlock()
		select {
		case <-doneCh:
			// Already closed (defensive).
		default:
			if evCh != nil {
				close(evCh)
			}
			if doneCh != nil {
				close(doneCh)
			}
		}
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
		// ownerEmail — the operator who created this deployment (issue #689).
		// Always emitted so the UI can compare against the current session;
		// only operators authorised by checkOwnership() ever reach this path,
		// so the email shown is necessarily either the current user's or
		// blank (legacy record).
		"ownerEmail": d.OwnerEmail,
	}
	// C8-001 (2026-05-17 t143): lift the Sovereign-provisioning request
	// fields that the chroot's /sovereign/settings page renders so the
	// page works on a fresh chroot session (where the operator's
	// browser-side wizard-store is empty). The fields are non-secret
	// projections of the wizard submit (control-plane size, pool
	// subdomain, BYO domain) — they live on the deployment record's
	// RedactedRequest already, the gap was only that State() never
	// surfaced them. Founder caught on t136 2026-05-17 — Settings page
	// shows four em-dash placeholders for Capacity / CP size / Pool
	// subdomain / BYO domain on the chroot Sovereign console because
	// the chroot has no localStorage'd wizard store to read from.
	if v := d.Request.ControlPlaneSize; v != "" {
		out["controlPlaneSize"] = v
	}
	// #4057 — surface the EFFECTIVE default StorageClass so the chroot
	// Settings page renders the operator's choice (or the per-provider CSI
	// default when they accepted it) instead of an em-dash. deriveStorageClass
	// fills the per-provider durable class (hcloud-volumes / evs-ssd) when the
	// stored value is empty, so the page always shows a real durable class
	// (never empty, never local-path).
	out["storageClass"] = provisioner.DeriveStorageClass(d.Request)
	if v := d.Request.SovereignPoolDomain; v != "" {
		out["sovereignPoolDomain"] = v
	}
	if v := d.Request.SovereignSubdomain; v != "" {
		out["sovereignSubdomain"] = v
	}
	if v := d.Request.SovereignDomainMode; v != "" {
		out["sovereignDomainMode"] = v
	}
	// BYO-domain is encoded on RedactedRequest only when domainMode
	// is `byo`; we still emit when present so the chroot Settings page
	// can render it. Pool-mode deployments leave this empty.
	if v := d.Request.SovereignFQDN; v != "" && d.Request.SovereignDomainMode == "byo" {
		out["sovereignByoDomain"] = v
	}
	// Per-region control-plane sizes (multi-region Sovereigns). The
	// Settings page falls back to controlPlaneSize when the array is
	// empty; surface both so future per-region renderings need no
	// API extension.
	if len(d.Request.Regions) > 0 {
		sizes := make([]string, 0, len(d.Request.Regions))
		for _, r := range d.Request.Regions {
			sizes = append(sizes, r.ControlPlaneSize)
		}
		out["regionControlPlaneSizes"] = sizes
	}
	// Org-profile fields (non-secret). Same rationale as the sovereign
	// fields above — the chroot Settings page would render four
	// em-dashes for Name / Billing email / Industry / Headquarters
	// otherwise.
	if v := d.Request.OrgName; v != "" {
		out["orgName"] = v
	}
	if v := d.Request.OrgEmail; v != "" {
		out["orgEmail"] = v
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
		// Issue #519 — lift phase1Outcome too so the wizard's
		// `markFailedTerminal` reducer can read it without unwrapping
		// result. The presence of any non-empty value is the durable
		// proof that runPhase1Watch reached its terminal classification,
		// which in turn proves Phase 0 finished — even if the
		// `tofu-output` event was lost in producer-channel overflow.
		if d.Result.Phase1Outcome != "" {
			out["phase1Outcome"] = d.Result.Phase1Outcome
		}
		// G117 #2840 — surface the per-region materialisation breakdown
		// at the top level so the operator-console deployments table can
		// render "1 of 2 regions materialised: me-east-215-a (missing:
		// me-east-215-b)" without unwrapping Result. Emitted whenever
		// the slice is non-empty (i.e. for every multi-region prov, not
		// just partial-failure rows — operator-console builds the
		// missing-set diff against the declared Regions[]).
		if len(d.Result.MaterializedRegions) > 0 {
			out["materializedRegions"] = d.Result.MaterializedRegions
		}
		// Issue #923 — lift the live Phase-1 substate to the top
		// level so the Sovereign Admin's wizard banner can render
		// "reconnecting…" / "watching…" while Status itself stays
		// "phase1-watching". Empty string is omitted so a UI client
		// that pre-dates the substate field never sees a stray key.
		if d.Result.Phase1Substate != "" {
			out["phase1Substate"] = d.Result.Phase1Substate
		}
		// Issues #764 + #768 — lift the handover-fire surface to the
		// top level so the wizard's provision-page reducer can drive
		// the "Open your Sovereign console →" button + auto-redirect
		// without unwrapping result. handoverURL is the canonical
		// post-mint redirect; handoverFiredAt is the proof-of-fire
		// timestamp the UI uses to render the toast exactly once
		// even on a poll-after-SSE-reconnect.
		if d.Result.HandoverURL != "" {
			out["handoverURL"] = d.Result.HandoverURL
		}
		if d.Result.HandoverFiredAt != nil {
			out["handoverFiredAt"] = d.Result.HandoverFiredAt.UTC().Format(time.RFC3339)
		}
		// #3611 — per-region HelmRelease health, lifted to the top level
		// so the operator-console deployments/jobs view can badge a
		// degraded secondary on a "ready" deployment without unwrapping
		// result. While the Phase-1 watchers are still attached (the
		// pre-handover polling window) we RECOMPUTE the census live off
		// the in-memory informer caches so the counts track convergence;
		// once the watchers are torn down at handover we fall back to the
		// snapshot markPhase1Done persisted onto Result.Regions. Both
		// paths are surface-only — neither changes d.Status.
		regions, secondaryDegraded := d.regionHealthForStateLocked()
		if len(regions) > 0 {
			out["regions"] = regions
			out["secondaryDegraded"] = secondaryDegraded
		}
	}
	// adoptedAt — handover-finalisation flag (issue #317) lifted to the
	// top level so the UI's beforeLoad redirect (issue #319) reads it
	// without walking nested result fields. Nil until #317's handover
	// handler stamps it; once set, the Sovereign is operationally
	// self-sufficient and console.openova.io/sovereign/<id> redirects
	// to console.<sovereign-fqdn>.
	if d.AdoptedAt != nil {
		out["adoptedAt"] = d.AdoptedAt.UTC().Format(time.RFC3339)
	}
	// OpenovaFlow integration (Agent #3, PR #1389/#1390 follow-up). Lit
	// up on every new deployment so the FlowPage canvas can swap from
	// the legacy bridge to the openova-flow-server-driven adapter. The
	// flag is server-driven (not request-driven) so a future
	// blueprint-side flip stays a one-line change here, not a
	// migration of every stored deployment record. Per docs/INVIOLABLE-
	// PRINCIPLES.md #1 (target-state) the canvas is the real consumer
	// from first cut; legacy deployments without bp-openova-flow-server
	// running degrade gracefully (the bridge still renders).
	out["openovaFlowEnabled"] = true
	return out
}

// regionHealthForStateLocked returns the per-region HelmRelease health
// census + the secondaryDegraded roll-up for State() (#3611). The CALLER
// MUST hold d.mu.
//
// While the Phase-1 watchers are still attached (liveWatcher for the
// primary, secondaryWatchers for the rest) it recomputes the census LIVE
// off the in-memory informer caches so a poll during convergence tracks
// the real counts. Once the watchers are torn down (handover, or a record
// restored from disk where no watcher is attached) it returns the snapshot
// markPhase1Done persisted onto Result.Regions. Both paths are surface-
// only — this never touches d.Status.
//
// SnapshotComponents() on each watcher takes only that watcher's own lock,
// never d.mu, so calling it under d.mu is safe.
func (d *Deployment) regionHealthForStateLocked() (regions []provisioner.RegionHealth, secondaryDegraded bool) {
	primaryAttached := d.liveWatcher != nil
	secondariesAttached := len(d.secondaryWatchers) > 0
	if !primaryAttached && !secondariesAttached {
		// No live watchers — surface the persisted snapshot verbatim.
		if d.Result == nil {
			return nil, false
		}
		return d.Result.Regions, d.Result.SecondaryDegraded
	}

	// Primary states: prefer the live informer cache; fall back to the
	// persisted terminal map when only secondaries are attached (a state
	// that does not normally occur but keeps the census honest).
	var primaryStates map[string]string
	if primaryAttached {
		primaryStates = make(map[string]string)
		for _, cs := range d.liveWatcher.SnapshotComponents() {
			primaryStates[cs.AppID] = cs.Status
		}
	} else if d.Result != nil {
		primaryStates = d.Result.ComponentStates
	}

	primaryRegion := primaryRegionKey(&d.Request)
	// #4086 — pass the deployment provider so provider-inapplicable HRs (the
	// hcloud control-plane HRs on a Huawei Sovereign, which are suspended and
	// never go Ready) are excluded from the census, instead of permanently
	// degrading a healthy non-Hetzner Sovereign.
	return provisioner.ComputeRegionHealth(d.Request.Provider, primaryRegion, primaryStates, snapshotSecondaryStatesLocked(d))
}

func (h *Handler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	// TBD-V4 (issue #1968, 2026-05-19) — pre-initialise Request defaults
	// BEFORE json.Decode so a body that OMITS `marketplaceEnabled`
	// (legacy API caller, hand-crafted curl, future automation that
	// doesn't know about the field) lands with MarketplaceEnabled=true.
	// The encoding/json package only assigns fields present in the body,
	// so an explicit `"marketplaceEnabled": false` from the wizard still
	// wins (canonical pointer-bool-emulation pattern for default-true
	// bool fields without changing the struct shape). Paired with the
	// chart-side `${MARKETPLACE_ENABLED:-true}` slot fallback (PR #1967)
	// and the matching variables.tf default flip.
	//
	// EnableSharedPostgres=true (Refs #3370) — same default-true idiom:
	// the ADR-0010 reusable shared-Postgres model (bootstrap-kit slot
	// 16a/16c/16d) is the founder North-Star-2 target state ('3 shared
	// PG instances → 3 cards, 6-7 apps many-to-many'). It is ALSO the
	// only path that lands an Application CR per shared-pg instance on a
	// fresh prov (the bp-postgres chart self-registers it ONLY when
	// `enabled != "false"`, the master gate fed by SOVEREIGN_ENABLE_
	// SHARED_PG); without it `/api/v1/sovereign/apps` projects ZERO
	// instance cards + ZERO Contexts (the #3370 surface is unreachable).
	// Leaving the default OFF made #3370 invisible on every fresh prov
	// (hw138). The deadlock that historically blocked SHARED_PG=true
	// (#3283) is closed; the chart is at 0.1.10. An explicit
	// `"enableSharedPostgres": false` in the body still wins for the
	// byte-identical dedicated-cluster path.
	req := provisioner.Request{MarketplaceEnabled: true, EnableSharedPostgres: true}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Issue #748 — orgEmail MUST equal the authenticated session's email.
	// Background: until this fix landed, a signed-in user could POST a
	// deployment whose req.OrgEmail belonged to someone ELSE — the catalyst-
	// api accepted the body verbatim and stamped the wrong identity onto
	// the Sovereign-admin / Catalyst-Organization owner. Any subsequent
	// session-scoped list (GET /deployments — issue #747) would then hide
	// the deployment from its real creator and leak it to the typo'd
	// recipient. The session JWT is the load-bearing identity claim; the
	// request body's OrgEmail is no more trustworthy than any other
	// client-provided field.
	//
	// Per docs/INVIOLABLE-PRINCIPLES.md #1 (never trust the client) the
	// server-side check is the load-bearing fix. The wizard's read-only
	// orgEmail input is defense-in-depth only.
	//
	// We use ClaimsFromContext (populated by auth.RequireSession) as the
	// canonical source — when it's nil we fall back to the X-User-Email
	// header so existing tests that build a Handler{} directly without
	// the middleware in the chain still exercise the orgEmail-match
	// check. When BOTH are absent (off-prod / Sovereign-side bootstrap
	// with no auth wired), the check is skipped — the deployment row is
	// stamped with empty OwnerEmail and the legacy passthrough applies.
	claims := auth.ClaimsFromContext(r.Context())
	sessionEmail := ""
	if claims != nil {
		sessionEmail = strings.TrimSpace(claims.Email)
	} else {
		sessionEmail = strings.TrimSpace(r.Header.Get("X-User-Email"))
	}
	if sessionEmail != "" {
		if !strings.EqualFold(strings.TrimSpace(req.OrgEmail), sessionEmail) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "orgEmail must match the authenticated session's email",
			})
			return
		}
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
	// Same fail-fast pattern for the Harbor proxy-cache robot token
	// (issue #557). Empty token here = Validate() rejects the deployment
	// at /api/v1/deployments POST, which is the right time to surface
	// the missing CATALYST_HARBOR_ROBOT_TOKEN env var on the catalyst-
	// api Pod. Falling through to docker.io is forbidden architecturally;
	// the alternative is 5+ min of `tofu apply` followed by a stuck CP
	// at Init:0/6 (caught live during otech24).
	if tok := os.Getenv("CATALYST_HARBOR_ROBOT_TOKEN"); tok != "" {
		req.HarborRobotToken = tok
	}

	// Stamp Huawei IAM credentials from env vars when the deployment
	// targets the Huawei provider. SAME pattern as GHCRPullToken /
	// HarborRobotToken above — operator AK/SK lives in a Kubernetes
	// Secret (`huawei-operator-creds` in the catalyst ns) projected
	// into the catalyst-api Pod as CATALYST_HUAWEI_ACCESS_KEY /
	// _SECRET_KEY / _PROJECT_ID / _REGION env vars. The wizard
	// payload NEVER carries these — Request.Huawei* fields are
	// `json:"-"` precisely so credential-exfiltration via wire body
	// is structurally impossible. Validate() (called below) still
	// requires them populated when provider=huawei; empty env =
	// fail-fast 400 with a clear pointer to the missing K8s Secret,
	// not a 5-minute tofu apply that crashes mid-flight.
	if req.Provider == "huawei" {
		if v := os.Getenv("CATALYST_HUAWEI_ACCESS_KEY"); v != "" {
			req.HuaweiAccessKey = v
		}
		if v := os.Getenv("CATALYST_HUAWEI_SECRET_KEY"); v != "" {
			req.HuaweiSecretKey = v
		}
		if v := os.Getenv("CATALYST_HUAWEI_PROJECT_ID"); v != "" {
			req.HuaweiProjectID = v
		}
		if v := os.Getenv("CATALYST_HUAWEI_REGION"); v != "" {
			req.HuaweiRegion = v
		}
		// Wave 5.121 (#2467): historical minWorkers=8 for s7n.large.4
		// (2vCPU/8GB) Huawei stack — needed because the smaller worker
		// flavor couldn't fit bp-catalyst-platform's Pod requests with
		// fewer nodes.
		//
		// Wave 5.146 (2026-05-27): CP flavor flipped to m7n.large.8
		// (2vCPU/16GB), worker flavor m7n.xlarge.8 (4vCPU/32GB).
		// SUPERSEDED by #4055 (2026-06-21) — the "fits with headroom"
		// claim did NOT hold once the console + both vClusters + cnpg-pair
		// landed together; 2vCPU CP / 4vCPU workers wedged on CPU. See the
		// resource-floor block below for the corrected sizing.
		//
		// Refs #2536 (2026-05-28, founder direction): symmetric
		// MGMT+RTZ+DMZ vClusters (Refs #2537) on 2 regions × 3 workers
		// = 6 ECS workers Sovereign-wide. That's the HCS Tier-B cost-fit
		// target and what the wizard's default sends. Honour
		// `workerCount=3` end-to-end (do not silently clamp up to 8).
		minWorkers := 3
		if req.WorkerCount < minWorkers {
			req.WorkerCount = minWorkers
		}
		// Per-region override too — Regions[i].WorkerCount is what
		// the tofu module actually iterates.
		for i := range req.Regions {
			if req.Regions[i].Provider == "huawei" && req.Regions[i].WorkerCount < minWorkers {
				req.Regions[i].WorkerCount = minWorkers
			}
		}
		// Wave 5.146 → #4055 (founder direction 2026-06-21): ENFORCE an
		// adequate resource FLOOR — stop shipping under-provisioned
		// Sovereigns. The full Catalyst platform (console + cnpg-pair +
		// mgmt/dmz vClusters + keycloak/harbor/gitea/openbao/guacamole/
		// newapi + trivy/falco/crossplane) does NOT fit on 2vCPU nodes.
		// hw182 came up with m7n.large.8 (2vCPU/16GB) for BOTH the CP and
		// all 5 workers and wedged: "0/6 nodes available: Insufficient
		// cpu" → bp-catalyst-platform's post-install hook timed out →
		// install/uninstall oscillation → the console never scheduled.
		//
		// The OLD guard only rewrote the deprecated s7n.large.4 + empty,
		// so a 2vCPU m7n.large.8 (the CP flavor wrongly sent as the
		// worker flavor) slipped straight through un-bumped. The floor:
		//   control-plane >= m7n.xlarge.8  (4 vCPU / 32 GB)
		//   workers       >= m7n.2xlarge.8 (8 vCPU / 64 GB)
		// Every known-undersized Huawei flavor is now rewritten UP to the
		// floor; a larger explicit flavor is honoured untouched. The m7n
		// family is unconstrained in the SKU matrix (sku_availability.go),
		// so these pass IsSkuAvailableInRegion in every kom4dc region.
		const (
			hwCPFloor     = "m7n.xlarge.8"  // 4 vCPU / 32 GB
			hwWorkerFloor = "m7n.2xlarge.8" // 8 vCPU / 64 GB
		)
		// undersizedCP/undersizedWorker = flavors strictly below the floor
		// for that role (plus "" = unset). m7n.large.8 (2vCPU) is too
		// small for BOTH roles; m7n.xlarge.8 (4vCPU) meets the CP floor
		// but is still below the worker floor.
		undersizedCP := map[string]bool{"": true, "s7n.large.4": true, "m7n.large.8": true}
		undersizedWorker := map[string]bool{"": true, "s7n.large.4": true, "m7n.large.8": true, "m7n.xlarge.8": true}
		if undersizedCP[req.ControlPlaneSize] {
			req.ControlPlaneSize = hwCPFloor
		}
		if undersizedWorker[req.WorkerSize] {
			req.WorkerSize = hwWorkerFloor
		}
		for i := range req.Regions {
			if req.Regions[i].Provider != "huawei" {
				continue
			}
			if undersizedCP[req.Regions[i].ControlPlaneSize] {
				req.Regions[i].ControlPlaneSize = hwCPFloor
			}
			if undersizedWorker[req.Regions[i].WorkerSize] {
				req.Regions[i].WorkerSize = hwWorkerFloor
			}
		}
		// Issue #2528: Huawei OBS (Object Storage Service) on HCS Kom4DC
		// uses the same IAM AK/SK as the compute API. Auto-derive the OBS
		// credentials from the already-stamped Huawei creds so callers
		// (wizard, automation, direct API) don't need to repeat them.
		// Only fills empty fields — an explicit objectStorageAccessKey in
		// the POST body wins (for non-Kom4DC deployments that have
		// separate OBS keys).
		if req.ObjectStorageAccessKey == "" {
			req.ObjectStorageAccessKey = req.HuaweiAccessKey
		}
		if req.ObjectStorageSecretKey == "" {
			req.ObjectStorageSecretKey = req.HuaweiSecretKey
		}
		if req.ObjectStorageRegion == "" {
			req.ObjectStorageRegion = req.HuaweiRegion
		}
	}

	// Mint the deployment ID NOW (before bucket-name derivation) so the
	// per-Sovereign Object Storage bucket name (issue #371, Fix #111) can
	// take a deterministic suffix from it. This fixes a real Hetzner
	// global-namespace collision: bucket names are GLOBAL across every
	// Hetzner Object Storage tenant, so two CreateDeployment calls for
	// the same FQDN (re-provision, race, two operators on adjacent pools)
	// would have collided on `catalyst-<fqdn>` and the second `tofu apply`
	// would have failed with `BucketAlreadyExists`. Suffix derivation lives
	// in hetzner.BucketNameForSovereign so the wipe handler can reproduce
	// the same name without re-deriving here.
	id := newID()

	// Derive the per-Sovereign Object Storage bucket name (issue #371,
	// Fix #111 suffix). The wizard never surfaces this as a free-form
	// input — it's plumbing, not user-facing.
	//
	// Pattern: catalyst-<fqdn-with-dots-replaced-by-dashes>-<8-hex>.
	// `catalyst-omantel-omani-works-b3b837a2` for the reference deployment
	// with id `b3b837a22d7a8e5c`. The downstream Validate() re-checks the
	// resulting string against the S3 bucket-naming RFC; if a future FQDN
	// format breaks the rule the validator emits a clear error rather
	// than letting tofu apply fail 5 minutes in.
	if strings.TrimSpace(req.ObjectStorageBucket) == "" && strings.TrimSpace(req.SovereignFQDN) != "" {
		// Resolve via the providers.CloudProvider seam so a non-Hetzner
		// deployment dispatches to its own ObjectStorageNamer impl
		// (Huawei's bucket-prefix on OBS follows the same `catalyst-
		// <fqdn-dashed>-<id-prefix>` shape but the namer lives in
		// providers/huawei). Wave 4 (#2140) wires this off req.Provider
		// so a POST with provider=huawei lands on the Huawei adapter's
		// BucketNameForDeployment; empty/unset Provider falls back to
		// "hetzner" for the back-compat path.
		providerName := strings.ToLower(strings.TrimSpace(req.Provider))
		if providerName == "" {
			providerName = "hetzner"
		}
		if cp, perr := providers.Get(providerName); perr == nil {
			if namer, ok := cp.(providers.ObjectStorageNamer); ok {
				req.ObjectStorageBucket = namer.BucketNameForDeployment(req.SovereignFQDN, id)
			}
		}
	}

	// #4706 (founder 2026-07-03: "the fundamental requirement of 2 region
	// mimicking agreement") — the BCP agreement default is MULTI-REGION.
	// A POST with <2 regions and no explicit bcpTopology is rejected at
	// ADMISSION: a single-region prov must be a DELIBERATE act (the payload
	// says bcpTopology="single-region" in so many words — the
	// quota-constrained validation shape). This floor deliberately lives
	// HERE and not in Request.Validate(): Validate doubles as the store's
	// legacy-record rehydration/migration path, where pre-floor
	// single-region records must keep loading.
	if strings.TrimSpace(req.BcpTopology) == "" && len(req.Regions) < 2 {
		http.Error(w,
			fmt.Sprintf("deployment has %d region(s) and no explicit bcpTopology — the BCP agreement default is multi-region (>=2 regions, Pillar 2). Add a second region, or set bcpTopology=%q explicitly for a deliberate single-region validation prov",
				len(req.Regions), provisioner.BcpTopologySingleRegion),
			http.StatusBadRequest)
		return
	}

	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Pre-fire resource preflight gate (#3889). The provisioning
	// goroutine runs INSIDE this mothership catalyst-api process; firing
	// a fresh prov into a disk- or memory-starved mothership node is the
	// "take off with the tank empty" mistake that cascaded hw168
	// (DiskPressure → pod evictions → dead pdm ghcr token → un-persisted
	// deployment record → orphaned Huawei infra). REFUSE the create with
	// an actionable error instead of starting a prov that dies mid-flight.
	// Fail-open: when the in-cluster client is unavailable (CI / dev) the
	// gate allows the create, matching every other sovereignDeps path.
	if err := h.checkResourcePreflight(r.Context()); err != nil {
		h.log.Warn("deployment create refused by resource preflight", "id", id, "reason", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "mothership-resource-pressure",
			"detail": err.Error(),
		})
		return
	}

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

	// Stamp the Catalyst-Zero handover-JWT public JWK so cloud-init can
	// write it to /var/lib/catalyst/handover-jwt-public.jwk on the new
	// Sovereign (issue #605, Phase-8b). When the signer is unavailable
	// (CATALYST_HANDOVER_KEY_PATH unset) we leave the field empty — the
	// OpenTofu module's variables.tf default ("") preserves backwards
	// compatibility, the file gets written empty, and the handover flow
	// is simply not yet wired on that Sovereign.
	if h.handoverSigner != nil {
		if jwk, jerr := h.handoverSigner.PublicJWK(); jerr == nil {
			req.HandoverJWTPublicKey = string(jwk)
		} else {
			h.log.Warn("handover signer present but PublicJWK failed; provisioning without JWK on new Sovereign", "err", jerr)
		}
	}

	// Stamp the owner email from the authenticated session (issue #689).
	// auth.RequireSession sets X-User-Email after validating the session
	// cookie AND injects Claims into the context; if both are missing,
	// the deployment was created on a catalyst-api instance running
	// without the auth middleware (legacy CI / Sovereign-side bootstrap)
	// and we leave the field empty. The ownership-check helper treats
	// empty as "legacy — skip".
	//
	// Issue #748: the orgEmail-match check above already guaranteed
	// req.OrgEmail equals the session email when a session is present,
	// so OwnerEmail is necessarily the same identity. We persist the
	// session-derived value (not req.OrgEmail) so a future client-side
	// bug that bypasses the check still cannot poison the durable
	// owner field.
	ownerEmail := sessionEmail

	dep := &Deployment{
		ID:                   id,
		Status:               "provisioning",
		Request:              req,
		StartedAt:            time.Now(),
		eventsCh:             make(chan provisioner.Event, 256),
		done:                 make(chan struct{}),
		kubeconfigBearerHash: bearerHash,
		OwnerEmail:           ownerEmail,
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

// ListDeployments returns the slim state of every deployment owned by the
// authenticated session, optionally filtered by ?owner=<email> (issue #747).
//
// Why a list endpoint:
//
//	The wizard's index route (/sovereign/wizard, /sovereign/) needs to
//	know whether the signed-in operator already has an in-flight
//	deployment so it can auto-redirect to /sovereign/provision/<id>
//	instead of presenting Step 1 of an empty wizard. Without this
//	redirect, an operator who refreshes the tab during a 15-minute
//	provisioning run loses the progress page and lands on the org
//	form — which is the exact bug the founder hit live with otech90
//	on 2026-05-04.
//
// Filtering rules:
//
//  1. ?owner=<email> — restrict to deployments whose OwnerEmail
//     matches (case-insensitive).
//  2. When auth.RequireSession is wired, X-User-Email is always set
//     and we ALSO restrict to that session's email regardless of
//     the ?owner= value, so an operator can't list someone else's
//     deployments by passing a different ?owner=. The server-side
//     check is the security boundary; ?owner= is effectively a
//     hint that mirrors the session.
//  3. When the middleware is bypassed (CI / tests / catalyst-api
//     running without CATALYST_KC_ADDR), the ?owner= filter is the
//     only filter and an empty ?owner= returns every deployment —
//     same passthrough policy as checkOwnership() to keep existing
//     tests working unchanged.
//  4. Issue #4683 — a session that holds the catalyst-owner realm
//     role (or the flattened tier=owner claim) is a sovereign-admin
//     with FLEET-WIDE visibility: the session-email boundary of
//     rule 2 does not apply. Owner-scoping is a marketplace-CUSTOMER
//     concern; the operator console must show every deployment
//     (including legacy rows with empty OwnerEmail) or the page
//     renders empty the moment a prov was fired under a different
//     operator identity — caught live on hw206 (console.openova.io
//     /sovereign/deployments rendered empty for the operator). For
//     such a session ?owner= becomes a genuine filter over ANY
//     owner instead of a self-assertion.
//
// Adopted deployments (AdoptedAt != nil) are EXCLUDED from the list —
// post-handover the customer's Sovereign is operationally self-
// sufficient and the Catalyst-Zero record is a minimum-retention
// breadcrumb, not an active session. The wizard-redirect logic only
// cares about live or recently-failed deployments.
//
// The response is a slim shape (id, status, sovereignFQDN, region,
// startedAt, finishedAt, ownerEmail, adoptedAt, error) so a tab full
// of deployments stays cheap to render — the full event buffer is
// fetched per-deployment via /deployments/{id}/events when the
// operator opens the progress page.
func (h *Handler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	ownerFilter := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("owner")))
	// Issue #748 — read the canonical session identity from the context
	// claims first (auth.RequireSession injects them), falling back to
	// X-User-Email so existing tests that build a Handler{} directly
	// continue to exercise the session-bound filter without rewiring.
	claims := auth.ClaimsFromContext(r.Context())
	sessionEmail := ""
	if claims != nil {
		sessionEmail = strings.TrimSpace(strings.ToLower(claims.Email))
	}
	if sessionEmail == "" {
		sessionEmail = strings.TrimSpace(strings.ToLower(r.Header.Get("X-User-Email")))
	}

	// Issue #4683 — sovereign-admin fleet visibility. A catalyst-owner
	// session is not owner-scoped: rule 4 of the filtering table above.
	fleetVisible := deploymentsListFleetVisible(claims)

	// Issue #748 — when a session is present AND a ?owner= query param
	// is also supplied AND ?owner != session.email, return an empty list
	// rather than silently collapsing to session-only rows. Returning
	// rows for a query that asked for a different identity is the kind
	// of subtle UX bug that hides the security boundary; "200 + empty"
	// makes the boundary explicit without leaking existence (we don't
	// emit 403 — the response shape MUST NOT differentiate "exists but
	// not yours" from "doesn't exist", same posture as the issue #689
	// 404-not-403 rule on /deployments/{id}). Does not apply to a
	// fleet-visible session (#4683) — there ?owner= is a real filter.
	if !fleetVisible && sessionEmail != "" && ownerFilter != "" && ownerFilter != sessionEmail {
		writeJSON(w, http.StatusOK, map[string]any{
			"deployments": []any{},
		})
		return
	}

	// Effective owner filter — when the session is set (production behind
	// RequireSession), it always defines the boundary so the ?owner=
	// query is at most a redundant assertion. In tests / CI without the
	// middleware, fall back to whatever the caller passed. A fleet-
	// visible session (#4683) has no session boundary: the ?owner=
	// query alone decides (empty ?owner= → every deployment).
	effectiveOwner := sessionEmail
	if fleetVisible || effectiveOwner == "" {
		effectiveOwner = ownerFilter
	}

	type listEntry struct {
		ID            string  `json:"id"`
		Status        string  `json:"status"`
		SovereignFQDN string  `json:"sovereignFQDN,omitempty"`
		Region        string  `json:"region,omitempty"`
		StartedAt     string  `json:"startedAt,omitempty"`
		FinishedAt    string  `json:"finishedAt,omitempty"`
		OwnerEmail    string  `json:"ownerEmail,omitempty"`
		AdoptedAt     *string `json:"adoptedAt,omitempty"`
		Error         string  `json:"error,omitempty"`
		// SecondaryDegraded (#3611) — surface-only flag so the
		// deployments list can badge a "ready" multi-region prov whose
		// secondary region is significantly behind the primary, instead
		// of a flat green pill. Omitted (false) for single-region and
		// fully-converged provs. The per-region HR breakdown lives on the
		// GET /deployments/{id} response (Result.Regions); the list keeps
		// just the roll-up to stay lightweight.
		SecondaryDegraded bool `json:"secondaryDegraded,omitempty"`
	}

	out := make([]listEntry, 0)
	h.deployments.Range(func(_, val any) bool {
		dep, ok := val.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		entry := listEntry{
			ID:            dep.ID,
			Status:        dep.Status,
			SovereignFQDN: dep.Request.SovereignFQDN,
			Region:        dep.Request.Region,
			OwnerEmail:    dep.OwnerEmail,
			Error:         dep.Error,
		}
		// #3611 — roll-up the per-region census so the list can badge a
		// degraded secondary on a "ready" deployment. Same live-or-
		// persisted source as State(); surface-only.
		_, entry.SecondaryDegraded = dep.regionHealthForStateLocked()
		if !dep.StartedAt.IsZero() {
			entry.StartedAt = dep.StartedAt.UTC().Format(time.RFC3339)
		}
		if !dep.FinishedAt.IsZero() {
			entry.FinishedAt = dep.FinishedAt.UTC().Format(time.RFC3339)
		}
		if dep.AdoptedAt != nil {
			s := dep.AdoptedAt.UTC().Format(time.RFC3339)
			entry.AdoptedAt = &s
		}
		owner := strings.TrimSpace(strings.ToLower(dep.OwnerEmail))
		dep.mu.Unlock()

		// Owner filter — when an effective owner is set, only emit
		// deployments whose OwnerEmail matches case-insensitively.
		// Legacy records with empty OwnerEmail are treated the same way
		// checkOwnership treats them on per-id reads: passthrough when
		// the request has no session, but skipped when the session does
		// not match (so a signed-in operator does not see legacy rows
		// owned by someone else).
		if effectiveOwner != "" {
			if owner == "" {
				// Legacy record — only include it when the caller has
				// no session AND no ?owner= filter, which collapses to
				// "no effective owner". Since effectiveOwner != "" here,
				// we skip the legacy row.
				return true
			}
			if owner != effectiveOwner {
				return true
			}
		}

		// Skip adopted deployments — the customer's Sovereign owns
		// these post-handover, so the wizard redirect should not pull
		// the operator back into the Catalyst-Zero shell.
		if entry.AdoptedAt != nil {
			return true
		}

		out = append(out, entry)
		return true
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"deployments": out,
	})
}

// deploymentsListFleetVisible reports whether the authenticated session
// may list EVERY deployment regardless of OwnerEmail (issue #4683).
//
// True for the sovereign-admin/operator tier only: the catalyst-owner
// realm role (canonical, stamped by Keycloak realm config or the G97
// operator enrichment in auth.RequireSession) OR the flattened
// tier=owner claim. Both signals project the SAME owner tier — the
// dual check absorbs the documented session-shape drift where some
// mint paths carry only one of the two (hw86 2026-06-01: OIDC session
// with empty realm_access.roles; PIN sessions carry tier only).
//
// An Org-scoped customer session (tier=org-admin, #4110) is NEVER
// fleet-visible — checked first so realm-config drift that leaks a
// privileged role onto a customer token cannot widen their view.
//
// nil claims (middleware bypassed: CI / tests / catalyst-api without
// CATALYST_KC_ADDR) → false, preserving the legacy ?owner= passthrough
// semantics unchanged.
func deploymentsListFleetVisible(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(claims.Tier), orgScopedTier) {
		return false
	}
	if claims.HasRealmRole("catalyst-owner") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(claims.Tier), "owner")
}

// GetDeployment returns the current state of a deployment for polling.
func (h *Handler) GetDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	var dep *Deployment
	if ok {
		dep = val.(*Deployment)
	} else if dep = h.chrootEnsureDeployment(id); dep == nil {
		// Match StreamLogs (deployments.go:1247-1254) — on the Sovereign
		// chroot the in-memory store is empty across catalyst-api Pod
		// restarts; chrootEnsureDeployment synthesises from SOVEREIGN_FQDN
		// env + the populated Result/Request fields (PR #1567, D22).
		// Without this fallback the Settings page renders 404 on every
		// post-restart load until some OTHER handler (StreamLogs, jobs
		// list, etc.) primes the store via its own synth call.
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	// Issue #689 — ownership check. Returns 404 (not 403) on mismatch so
	// the existence of someone else's deployment is never leaked.
	if !h.checkOwnership(w, r, dep) {
		return
	}
	// D0 (handover-redirect) — caught on t132 Playwright walkthrough
	// 2026-05-17: the handoverURL persisted at handover-fire time
	// carries a JWT that expires per DefaultTTL (5min). Operators who
	// click /jobs hours later get the stale token → Sovereign-side
	// /auth/handover rejects with raw JSON {"error":"invalid token"}.
	// Re-mint the JWT on every GetDeployment when the deployment is
	// ready+handover-fired so the URL the wizard reads is always
	// freshly-signed. Best-effort: on mint failure, leave the existing
	// URL in place so a transient signer error doesn't break polling.
	h.remintHandoverURLForReadyDeployment(dep)
	state := dep.State()
	// #3925 surface D — enrich with `operationInProgress` so the
	// Convergence-Monitor top-bar chip can render OPERATION-IN-PROGRESS
	// distinctly from initial CONVERGING. Computed off the jobs store
	// (cutover / DR-switchover group non-terminal), which lives on the
	// Handler — so it's injected here rather than inside Deployment.State().
	state["operationInProgress"] = h.operationInProgress(dep.ID)
	writeJSON(w, http.StatusOK, state)
}

// remintHandoverURLForReadyDeployment re-signs the handover JWT on a
// ready deployment whose original token has expired. Idempotent + best-
// effort. See GetDeployment caller comment for D0 context.
func (h *Handler) remintHandoverURLForReadyDeployment(dep *Deployment) {
	if dep == nil || h.handoverSigner == nil {
		return
	}
	dep.mu.Lock()
	if dep.Result == nil || dep.Result.HandoverFiredAt == nil || dep.Status != "ready" {
		dep.mu.Unlock()
		return
	}
	fqdn := dep.Request.SovereignFQDN
	depID := dep.ID
	owner := dep.OwnerEmail
	dep.mu.Unlock()
	if fqdn == "" || depID == "" || owner == "" {
		return
	}
	tokenStr, err := h.handoverSigner.MintToken(fqdn, depID, owner, owner)
	if err != nil {
		h.log.Warn("handover re-mint failed", "id", depID, "err", err)
		return
	}
	freshURL := "https://console." + fqdn + "/auth/handover?token=" + url.QueryEscape(tokenStr)
	dep.mu.Lock()
	if dep.Result != nil {
		dep.Result.HandoverURL = freshURL
	}
	dep.mu.Unlock()
}

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// chroot fallback: empty until deployment record settles.
	// On the chroot (SOVEREIGN_FQDN env set), the cutover does NOT
	// import the mother's deployment record into the chroot's
	// in-memory map. The first dashboard load after handover races
	// the synthesis path so without this fallback the wizard's SSE
	// reconnect loop sees a transient 404 (TC-229).
	// chrootEnsureDeployment synthesises a record with pre-closed
	// channels, so the "completed deployment, replay buffer + emit
	// done" branch fires below — empty buffer + done frame is the
	// right shape for a freshly-handed-over Sovereign. Once the
	// chroot lazy-seed populates jobs/events the next reconnect
	// returns the real history. On the mother (no SOVEREIGN_FQDN)
	// chrootEnsureDeployment returns nil and the legacy 404 stands.
	val, ok := h.deployments.Load(id)
	var dep *Deployment
	if ok {
		dep = val.(*Deployment)
	} else if dep = h.chrootEnsureDeployment(id); dep == nil {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	// Issue #689 — ownership check BEFORE any SSE bytes are written. Once
	// the response is hijacked into text/event-stream mode the helper's
	// JSON 404 path can't fire, so this MUST happen before the SSE headers.
	if !h.checkOwnership(w, r, dep) {
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

	// Replay-on-connect: emit every event already in the durable buffer
	// before tailing the live channel. This is what makes navigating to
	// `/sovereign/provision/<completed-id>` render the full history instead
	// of an empty shell — a browser that connects after `event: done`
	// arrived at an already-closed channel previously got nothing.
	for _, ev := range dep.snapshotEvents() {
		writeSSEEvent(w, ev)
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
			writeSSEEvent(w, ev)
			flusher.Flush()
		}
	}
}

// PhaseHandoverReady — Phase value set on a synthesized provisioner.Event
// when catalyst-api auto-fires the handover JWT mint after the Phase-1
// watch terminates with OutcomeReady (issues #764 + #768). The event's
// Message carries a JSON object `{"handoverURL": "...", "expiresAt":
// "..."}` (RFC 3339 expiry, RS256 token contract — see
// internal/handoverjwt/signer.go).
//
// StreamLogs renders this Event as a typed SSE event:
//
//	event: handover-ready
//	data: {"handoverURL": "...", "expiresAt": "..."}
//
// The wizard's useDeploymentEvents hook listens via
// EventSource.addEventListener('handover-ready', ...) so the typed
// channel is independent of the default `message` event reducer.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is built from the
// Sovereign FQDN persisted on the deployment record + the JWT minted
// by handoverjwt.Signer.MintToken — no hardcoded host.
const PhaseHandoverReady = "handover-ready"

// writeSSEEvent renders a single provisioner.Event onto an SSE stream.
//
// Default rendering is the legacy shape:
//
//	data: <json-of-event>
//
// (no `event:` prefix → the browser dispatches it as the default
// `message` event, which the existing reducer consumes).
//
// The single typed-event branch is `Phase == PhaseHandoverReady`,
// which renders:
//
//	event: handover-ready
//	data: <Event.Message verbatim>
//
// Event.Message for the handover-ready event is already a JSON object
// — see Handler.fireHandover in phase1_watch.go. The wizard's
// addEventListener('handover-ready', ...) parses it directly without
// the Event wrapper.
func writeSSEEvent(w http.ResponseWriter, ev provisioner.Event) {
	if ev.Phase == PhaseHandoverReady {
		// The Message field is the canonical JSON payload —
		// emit it verbatim under the typed event channel. No
		// double-wrapping in the Event struct, so the Sovereign UI's
		// addEventListener handler receives the exact shape #768
		// specifies: {"handoverURL": "...", "expiresAt": "..."}.
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", PhaseHandoverReady, ev.Message)
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
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
	// chroot fallback: empty until deployment record settles.
	// On the chroot (SOVEREIGN_FQDN env set), the cutover does NOT
	// import the mother's deployment record. The first dashboard
	// load races the in-memory synthesis path so without this
	// fallback the wizard sees a transient 404 (TC-229) on
	// /api/v1/deployments/<id>/events. Synthesise a minimal record
	// — snapshotEvents() returns an empty slice on the synthetic
	// record so the wizard renders its empty state and reconnects;
	// once Phase-1 watch starts emitting (chroot lazy-seed path),
	// subsequent reads return the populated buffer. On the mother
	// (no SOVEREIGN_FQDN) chrootEnsureDeployment returns nil and
	// the legacy 404 path is preserved.
	val, ok := h.deployments.Load(id)
	var dep *Deployment
	if ok {
		dep = val.(*Deployment)
	} else if dep = h.chrootEnsureDeployment(id); dep == nil {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	// Issue #689 — ownership check (404 on mismatch — never leak existence).
	if !h.checkOwnership(w, r, dep) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":  dep.State(),
		"events": dep.snapshotEvents(),
		"done":   dep.isDone(),
	})
}

func (h *Handler) runProvisioning(dep *Deployment) {
	// Pre-seed the 5 Phase-0 lifecycle Job rows ("provisioner" group)
	// BEFORE prov.Provision starts. This guarantees:
	//
	//   1. Deep-links to /jobs/tofu-init|tofu-plan|tofu-apply|
	//      tofu-output|cluster-bootstrap resolve from the very first
	//      wizard render, not just after the corresponding emit.
	//   2. The JobsTable can never "drop" the Phase-0 rows when
	//      bootstrap-kit children land later (they're durable in
	//      jobs.Store, not derived from the live event stream).
	//
	// The bridge initialiser is the same path emitWatchEvent uses;
	// pre-allocating it here means SeedProvisionerJobs fires once per
	// deployment regardless of whether the first emit beats this
	// goroutine.
	if h.jobs != nil {
		dep.mu.Lock()
		if dep.jobsBridge == nil {
			dep.jobsBridge = jobs.NewBridge(h.jobs, dep.ID)
		}
		bridge := dep.jobsBridge
		dep.mu.Unlock()
		// Stamp the deployment's cloud provider so the Phase-0 lifecycle
		// parent group renders "Provision <Provider>" (e.g. "Provision
		// Huawei") instead of a hardcoded Hetzner label (#3895).
		bridge.SetProvider(firstProvider(dep.Request))
		if seedErr := bridge.SeedProvisionerJobs(); seedErr != nil {
			h.log.Warn("jobs bridge: seed Phase-0 lifecycle jobs failed",
				"id", dep.ID, "err", seedErr)
		}
	}

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
	// Wave 5.137 (hw30 #13 fix-forward 2026-05-27): bump Provision
	// timeout 30m→180m. Wave 5.132/5.133 added a 6-attempt × exponential-
	// backoff retry loop for HCS Common.0021 transient failures
	// (provisioner.go:1220). Worst-case backoff envelope alone is
	// 90s+180s+360s+720s+1440s+2880s = ~95 min, plus ~5 min per attempt
	// for plan+apply = ~125 min. The previous 30m ceiling truncated the
	// loop at attempt 4 — hw30 #13 (76864b8cfcd5961a) failed at
	// 2026-05-27T01:12:31Z with "tofu plan (retry): context deadline
	// exceeded" while waiting between attempts 4 and 5, despite the
	// retry mechanism working correctly. 180m gives the loop full room
	// to exhaust (max ~125 min) plus 55 min Phase-1 Helm-watch + safety
	// margin. CATALYST_PHASE1_WATCH_TIMEOUT is independent (120m,
	// runProvisioning path) and has its own ceiling.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Minute)
	defer cancel()

	result, err := prov.Provision(ctx, dep.Request, producer)
	close(producer)
	<-teeDone

	// G117 #2840 — partial-region materialisation post-condition.
	//
	// The provisioner returns a typed PartialRegionMaterialisationError
	// when `tofu apply` succeeded internally but the per-region readback
	// shows fewer regions came up than the wizard declared. The error
	// carries a non-nil Result with whatever DID land so the catalyst-
	// api can render a meaningful "partial-failure" state instead of an
	// opaque "failed" pill.
	//
	// We deliberately do NOT auto-`tofu destroy` here — that's destructive
	// and may abandon a partial-prov the operator wants to retain (a
	// single-region Sovereign with usable workers may be cheaper to keep
	// than to wipe + retry against an unhealthy HCS cell). Instead we
	// stamp Status = "partial-failure", populate Result.MaterializedRegions
	// for the operator console, and emit a clear event. PDM commit + the
	// Phase-1 watch are SKIPPED — the topology promise (active-hotstandby)
	// is broken so promoting DNS or watching helm-releases would lie about
	// the Sovereign's actual posture.
	var partialErr *provisioner.PartialRegionMaterialisationError
	isPartialFailure := errors.As(err, &partialErr)
	if isPartialFailure && result == nil && partialErr.Result != nil {
		// Defensive — partialErr.Result is the source of truth even if
		// the provisioner ever changed the (result, err) wire shape.
		result = partialErr.Result
	}

	// Stamp the Phase-0 lifecycle Jobs into terminal state so the
	// JobsTable's Started/Finished/Duration columns populate without
	// waiting for Phase-1 events. Failure stamps the in-flight phase
	// as Failed (with the error captured as a final LogLine);
	// success rolls every phase to Succeeded.
	now := time.Now().UTC()
	if h.jobs != nil && dep.jobsBridge != nil {
		if err != nil {
			if mErr := dep.jobsBridge.MarkProvisionerFailed(err.Error(), now); mErr != nil {
				h.log.Warn("jobs bridge: mark Phase-0 failed",
					"id", dep.ID, "err", mErr)
			}
		} else {
			if mErr := dep.jobsBridge.MarkProvisionerComplete(now); mErr != nil {
				h.log.Warn("jobs bridge: mark Phase-0 complete",
					"id", dep.ID, "err", mErr)
			}
		}
	}

	// Capture Phase-0 outcome under the lock.
	dep.mu.Lock()
	switch {
	case isPartialFailure:
		// G117 #2840 — operator-actionable terminal state. Renders a
		// "partial-failure" pill in the wizard + operator-console
		// deployments table. The detailed missing/materialised
		// breakdown lives on the Result so the BSS console can build a
		// drill-down view.
		dep.FinishedAt = time.Now()
		dep.Status = "partial-failure"
		dep.Error = err.Error()
		dep.Result = result
		h.log.Error("partial-region materialisation — Pillar 2+3 violated, operator action required",
			"id", dep.ID,
			"sovereignFQDN", result.SovereignFQDN,
			"declaredRegions", partialErr.DeclaredRegions,
			"materializedRegions", partialErr.MaterializedRegions,
			"missingRegions", partialErr.MissingRegions,
		)
	case err != nil:
		dep.FinishedAt = time.Now()
		dep.Status = "failed"
		dep.Error = err.Error()
		h.log.Error("provision failed", "id", dep.ID, "err", err)
	default:
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

	// PDM /commit fires HERE — between Phase 0 and Phase 1 — for two
	// architectural reasons:
	//
	//   1. The Phase-0 LB IP is the ONLY data PDM needs to write the
	//      child-zone records (NS delegation + console/api/wildcard
	//      A records). Phase-1 outcome does NOT change DNS routing,
	//      so deferring the commit to AFTER Phase 1 just delays the
	//      moment console.<sub>.<pool> resolves (and previously did
	//      so with the SSE channel already closed — failures were
	//      invisible to the wizard).
	//   2. The PDM reservation TTL is 10 minutes by default; Phase-0
	//      routinely takes 8-12 min on a fresh Hetzner cluster, so
	//      the commit can race the sweeper. CommitWithRetry handles
	//      that by re-Reserving on 404/410/403 — and it does so while
	//      the wizard SSE stream is still open, surfacing each
	//      attempt as an event. See pdm/client.go:CommitWithRetry.
	//
	// On Phase-0 failure (no LB IP), Release is called instead so the
	// reservation TTL doesn't have to expire to free the name.
	//
	// G117 #2840 — partial-failure ALSO releases the PDM reservation
	// and skips the parent-zone write. The Sovereign's topology promise
	// is broken; publishing DNS would point the public FQDN at a
	// single-region cluster claiming to be multi-region, which is worse
	// than no DNS at all. The operator decides whether to retain the
	// partial prov + re-publish DNS manually, or wipe + retry.
	if err == nil && result != nil {
		h.commitPDMWithRetry(dep, result)
		// Parent-zone A records — BYO Sovereigns (operator-owned
		// parent like omani.works, sovereign FQDN like
		// t111.omani.works) need console.<fqdn>, auth.<fqdn>, etc.
		// written into the parent zone so browsers resolve them at
		// the primary LB. commitPDMWithRetry handles pool-allocated
		// FQDNs; this handles every other shape. Best-effort: log +
		// continue on failure so the Sovereign still reaches
		// phase1-watching even if PowerDNS is briefly unavailable.
		h.upsertSovereignParentZoneRecordsFromResult(context.Background(), dep, result)
	} else {
		h.releasePDMReservation(dep)
	}

	// Phase 1 — HelmRelease watch. Only runs on Phase-0 success and
	// only when a kubeconfig is available. The watch emits per-
	// component events into the same SSE buffer + live channel; when
	// it terminates, it writes ComponentStates + Phase1FinishedAt
	// onto dep.Result and flips Status to ready (or leaves failed
	// alone if Phase 0 already failed).
	//
	// G117 #2840 — partial-failure ALSO skips the Phase-1 watch. The
	// chart's bp-* HRs render under the assumption of N regions; with
	// only 1 region materialised the install would either fail or
	// (worse) silently downgrade to single-region CNPG. The Sovereign
	// is operator-owned at this point — no automatic retry.
	if err == nil && result != nil {
		h.runPhase1Watch(dep)
	}

	// Close the SSE live channel + done signal AFTER both phases
	// have settled. Existing tests that drive runProvisioning's
	// failure-fast path (no real tofu) still hit close because the
	// Phase-0 error skips the watch.
	//
	// Wave 5.135: if a concurrent Wipe handler has taken ownership
	// (Status == "wiping") it has already replaced dep.eventsCh with
	// a fresh channel and is sending events through it from its own
	// goroutines. Closing the (new) channel here races those sends
	// and panics "send on closed channel" inside the huawei wipe
	// callback (wipe.go:442). The Wipe handler closes its own
	// channel at wipe.go:649 on terminal exit; skip the close here.
	// Wave 5.156 (2026-05-27): also skip when Status=="wiped" — wipe.go
	// flips Status from "wiping" → "wiped" at line 643 BEFORE closing
	// the channel at line 661. If runProvisioning's defer fires after
	// the Status flip but before wipe.go's close, the original Wave 5.135
	// guard (which only checked "wiping") let the close through here,
	// then wipe.go's close panicked "close of closed channel".
	// Witnessed on hw33 zero-touch wipe #2 of 3 (2026-05-27T12:41:59Z).
	dep.mu.Lock()
	skipClose := dep.Status == "wiping" || dep.Status == "wiped"
	dep.mu.Unlock()
	if !skipClose {
		// Defense-in-depth: even with the status guard, a third concurrent
		// path could close between the check and the close. Wrap in a
		// func+recover so a double-close doesn't crash the catalyst-api
		// process.
		func() {
			defer func() { _ = recover() }()
			close(dep.eventsCh)
		}()
	}
	func() {
		defer func() { _ = recover() }()
		close(dep.done)
	}()

	// Final persist — captures Phase 1 terminal state when the watch
	// ran, or is a no-op for the Phase 0 failure path (already
	// persisted above).
	h.persistDeployment(dep)
}

// commitPDMWithRetry promotes the deployment's PDM reservation to
// ACTIVE and triggers the per-Sovereign zone write inside PDM. It runs
// while the wizard SSE stream is still open so each attempt + failure
// surfaces as an event; on final exhaustion the human-readable error
// is appended to dep.Error so the wizard FailureCard renders it.
//
// The retry loop is delegated to pdm.Client.CommitWithRetry (5
// attempts, 1s → 16s backoff). Re-Reserve on 404/410/403 is done
// inline so a TTL-expiry — the actual symptom that broke otech41+ —
// recovers automatically and the persisted deployment row is updated
// to the fresh token (so a Pod restart between re-Reserve and
// re-Commit picks up the new token instead of the stale one).
//
// No-op when the deployment isn't using PDM (BYO domain mode, or
// h.pdm == nil in tests).
func (h *Handler) commitPDMWithRetry(dep *Deployment, result *provisioner.Result) {
	if dep.pdmReservationToken == "" || h.pdm == nil {
		return
	}

	// Up to 5 minutes for the full retry window — 5 attempts at
	// 1s/2s/4s/8s/16s backoff plus per-attempt HTTP timeouts (15s
	// each from Client.HTTP) plus a re-Reserve every other attempt
	// in the worst case = ~3 min ceiling, with a safety margin.
	pdmCtx, pdmCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer pdmCancel()

	emitInfo := func(msg string) {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "pdm-commit",
			Level:   "info",
			Message: msg,
		})
	}
	emitErr := func(msg string) {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "pdm-commit",
			Level:   "error",
			Message: msg,
		})
	}

	emitInfo(fmt.Sprintf("Publishing DNS records for %s via pool-domain-manager (LB %s)…",
		result.SovereignFQDN, result.LoadBalancerIP))

	commitErr := h.pdm.CommitWithRetry(
		pdmCtx,
		dep.pdmPoolDomain,
		pdm.CommitInput{
			Subdomain:             dep.pdmSubdomain,
			ReservationToken:      dep.pdmReservationToken,
			SovereignFQDN:         result.SovereignFQDN,
			LoadBalancerIP:        result.LoadBalancerIP,
			ConsoleLoadBalancerIP: result.ConsoleLoadBalancerIP, // #4053 — isolated console LB (empty-safe)
		},
		func(ctx context.Context) (*pdm.Reservation, error) {
			emitInfo(fmt.Sprintf("PDM reservation expired during Phase 0 — re-Reserving %s.%s",
				dep.pdmSubdomain, dep.pdmPoolDomain))
			return h.pdm.Reserve(ctx, dep.pdmPoolDomain, dep.pdmSubdomain, "catalyst-api/deployment-"+dep.ID)
		},
		func(newToken string) {
			// Token rotated — persist the new token onto the row so a
			// Pod restart between this re-Reserve and the next
			// Commit attempt doesn't lose it.
			dep.mu.Lock()
			dep.pdmReservationToken = newToken
			dep.mu.Unlock()
			h.persistDeployment(dep)
		},
		pdm.CommitRetryConfig{}, // production defaults
	)
	if commitErr != nil {
		// Final exhaustion — surface to the wizard. The Sovereign is
		// live (cluster, LB, kubeconfig all good) but DNS is stale,
		// so we annotate dep.Error rather than flipping Status to
		// failed (which would imply the cluster itself is broken).
		// The wizard's FailureCard checks dep.Error before rendering
		// "ready" so the operator sees this regardless.
		h.log.Error("pdm commit failed after retry; sovereign is live but DNS records are stale — operator action required",
			"id", dep.ID,
			"poolDomain", dep.pdmPoolDomain,
			"subdomain", dep.pdmSubdomain,
			"err", commitErr,
		)
		emitErr(fmt.Sprintf("DNS publish FAILED after retry: %v. The Sovereign cluster is live (LB %s) but %s will not resolve until an operator runs PDM /commit manually or re-runs the deployment.",
			commitErr, result.LoadBalancerIP, result.SovereignFQDN))
		dep.mu.Lock()
		errMsg := fmt.Sprintf("DNS commit failed: %v — sovereign cluster is live but %s does not resolve. Re-run `curl -X POST $POOL_DOMAIN_MANAGER_URL/api/v1/pool/%s/commit` with a fresh reservation, or re-trigger the deployment.",
			commitErr, result.SovereignFQDN, dep.pdmPoolDomain)
		if dep.Error == "" {
			dep.Error = errMsg
		} else {
			dep.Error = dep.Error + " | " + errMsg
		}
		dep.mu.Unlock()
		h.persistDeployment(dep)
		return
	}

	h.log.Info("pdm commit complete",
		"id", dep.ID,
		"poolDomain", dep.pdmPoolDomain,
		"subdomain", dep.pdmSubdomain,
		"loadBalancerIP", result.LoadBalancerIP,
	)
	emitInfo(fmt.Sprintf("DNS published — %s now resolves to %s.",
		result.SovereignFQDN, result.LoadBalancerIP))
}

// releasePDMReservation releases the PDM reservation on Phase-0
// failure so the reservation TTL doesn't have to expire to free the
// name. Best-effort: a release failure logs a warning but does not
// block the failure-cleanup path. ErrNotFound is silently ignored
// because the row may have been swept already.
func (h *Handler) releasePDMReservation(dep *Deployment) {
	if dep.pdmReservationToken == "" || h.pdm == nil {
		return
	}
	// Refs #3728 — ReleaseWithRetry so a transient PDM failure on the
	// Phase-0-failure cleanup doesn't strand the reservation until its TTL.
	// (A reserved row WOULD expire on its 10m TTL, but if it was already
	// committed to `active` before the failure, only an explicit release
	// frees it — retrying closes that window.)
	pdmCtx, pdmCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer pdmCancel()
	releaseErr := h.pdm.ReleaseWithRetry(pdmCtx, dep.pdmPoolDomain, dep.pdmSubdomain, pdm.CommitRetryConfig{})
	if releaseErr != nil && !errors.Is(releaseErr, pdm.ErrNotFound) {
		h.log.Error("pdm release failed; reservation will expire on TTL",
			"id", dep.ID,
			"poolDomain", dep.pdmPoolDomain,
			"subdomain", dep.pdmSubdomain,
			"err", releaseErr,
		)
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
	provider := firstProvider(dep.Request)
	dep.mu.Unlock()
	// Stamp the provider on the bridge so a lifecycle event that lazily
	// materialises the Phase-0 group still labels it "Provision
	// <Provider>" rather than the Hetzner default (#3895).
	if bridge != nil {
		bridge.SetProvider(provider)
	}

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
