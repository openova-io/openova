// wipe.go — pre-handover wipe surface (issue #318).
//
// When a provisioning attempt fails (mid-tofu-apply, mid-Phase-1, or just
// because the operator decided to abandon), the wizard's failed-state
// banner exposes a "Cancel & Wipe" button that POSTs here. This handler
// runs the canonical purge sequence:
//
//   1. Cancel any in-flight context (helmwatch informer, current Phase-0
//      runner) for this deployment.
//   2. Run `tofu destroy -auto-approve` against the per-deployment
//      workdir. Idempotent — re-runs on partial state are safe.
//   3. Run a Hetzner force-purge of any resources tagged with
//      `catalyst-deployment-id=<id>` so anything tofu missed (or anything
//      created out-of-band) is removed. Belt + braces; tofu destroy is
//      the primary path, Hetzner API the safety net.
//   4. Release the PDM allocation row (pool subdomain only). Best-effort:
//      a PDM outage doesn't block local cleanup, the pool-domain-manager
//      operator can force-release later via `pdm-cli` (#319).
//   5. Delete the on-disk record + kubeconfig + tofu workdir.
//   6. Mark the in-memory Deployment Status="wiped" so subsequent GETs
//      return 410 Gone (per the founder's minimum-retention principle —
//      Catalyst-Zero retains nothing operational about a wiped
//      deployment).
//
// All progress streams as SSE events on the same channel as the original
// provisioning + Phase-1 watch, so the wizard's banner can render the
// purge live without a second stream.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase 0): tofu
// destroy is the primary purge mechanism; the Hetzner direct-API call in
// step 3 is fallback ONLY for orphans tofu can't see (corrupt state,
// resources created out-of-band by a half-completed cloud-init, etc.).
// We never use the direct API for new resource creation.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// wipeRequest is the body of POST /api/v1/deployments/{id}/wipe.
//
// HetznerToken is required ONLY when the on-disk Deployment record's
// Request.HetznerToken has been GC'd (the field is intentionally cleared
// after writeTfvars per the credential-hygiene principle). The wizard
// re-prompts the operator for the token in the Cancel & Wipe modal, so
// the value survives just long enough to drive `tofu destroy` + the
// Hetzner orphan purge, then is forgotten.
type wipeRequest struct {
	HetznerToken string `json:"hetznerToken"`
}

// wipeResponse summarises what was actually purged. The wizard renders
// the counts in a "Wipe complete — N servers, M load balancers, …
// removed" success banner.
type wipeResponse struct {
	DeploymentID  string                `json:"deploymentId"`
	SovereignFQDN string                `json:"sovereignFQDN"`
	TofuDestroyed bool                  `json:"tofuDestroyed"`
	HetznerPurge  hetzner.PurgeReport   `json:"hetznerPurge"`
	PDMReleased   bool                  `json:"pdmReleased"`
	LocalCleaned  bool                  `json:"localCleaned"`
	Errors        []string              `json:"errors"`
	WipedAt       string                `json:"wipedAt"`
}

// WipeDeployment handles POST /api/v1/deployments/{id}/wipe.
//
// Response codes:
//   - 200 OK on full or partial success (errors in the body)
//   - 400 Bad Request when the body cannot be parsed
//   - 404 Not Found when the deployment id is unknown
//   - 409 Conflict if a wipe is already in progress for this deployment
//   - 500 on a fatal local-state error (workdir non-removable, etc.)
//
// The endpoint is idempotent: re-running on a partially-wiped deployment
// returns the same shape with empty deltas. The wizard treats a 200 with
// non-empty Errors as "investigate the log; some cleanup may be manual".
func (h *Handler) WipeDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	// Parse body — the wizard re-prompts for the Hetzner token because
	// catalyst-api intentionally GCs it from the in-memory Request after
	// writeTfvars. Without it tofu destroy + the orphan purge can't
	// authenticate.
	var body wipeRequest
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.HetznerToken) == "" {
		http.Error(w, "hetznerToken is required (re-prompt the operator before calling this endpoint)", http.StatusBadRequest)
		return
	}

	// Single-flight guard: if Status is already "wiping", refuse.
	dep.mu.Lock()
	if dep.Status == "wiping" {
		dep.mu.Unlock()
		http.Error(w, "wipe already in progress for this deployment", http.StatusConflict)
		return
	}
	prevStatus := dep.Status
	dep.Status = "wiping"
	dep.mu.Unlock()

	// Re-open the events channel if the previous one is closed, so the
	// wizard's banner can render purge progress on the same SSE stream
	// it used for provisioning.
	dep.mu.Lock()
	if dep.eventsCh == nil {
		dep.eventsCh = make(chan provisioner.Event, 256)
	}
	dep.mu.Unlock()

	// Note: any live Phase-1 watcher for this deployment will exit
	// naturally as `tofu destroy` removes the API server it's watching
	// (the watch reconnect will fail with "no route to host" / "EOF" and
	// the watcher's own context-deadline-exceeded path takes over).
	// We don't need to explicitly cancel here.
	_ = dep.liveWatcher

	report := wipeResponse{
		DeploymentID:  id,
		SovereignFQDN: dep.Request.SovereignFQDN,
		WipedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	emit := func(phase, level, msg string) {
		ev := provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   phase,
			Level:   level,
			Message: msg,
		}
		dep.recordEvent(ev)
		select {
		case dep.eventsCh <- ev:
		default:
		}
	}
	emit("wipe", "info", "Wipe initiated for "+dep.Request.SovereignFQDN+" (was: "+prevStatus+")")

	// Step 1 — tofu destroy. Pass the freshly-prompted Hetzner token via
	// the Request so writeTfvars renders it for the destroy run.
	wipeReq := dep.Request
	wipeReq.HetznerToken = body.HetznerToken

	prov := provisioner.New()
	tofuCtx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	if err := prov.Destroy(tofuCtx, wipeReq, dep.eventsCh); err != nil {
		report.Errors = append(report.Errors, "tofu destroy: "+err.Error())
		emit("wipe", "warn", "tofu destroy did not complete cleanly: "+err.Error()+" — falling back to direct Hetzner orphan purge")
	} else {
		report.TofuDestroyed = true
		emit("wipe", "info", "tofu destroy complete")
	}

	// Step 2 — Hetzner orphan purge (always runs as belt-and-braces, even
	// when tofu destroy succeeded — catches resources tofu didn't track,
	// e.g. a half-failed cloud-init that created a worker manually, or
	// resources the operator created in the same project for testing).
	purge, err := hetzner.Purge(tofuCtx, body.HetznerToken, id, func(msg string) {
		emit("wipe", "info", "hetzner: "+msg)
	})
	report.HetznerPurge = purge
	if err != nil {
		report.Errors = append(report.Errors, "hetzner purge: "+err.Error())
	}
	if purge.Total() > 0 {
		emit("wipe", "info", "Hetzner orphan purge removed "+itoa(purge.Total())+" resource(s) (servers: "+itoa(len(purge.Servers))+", lbs: "+itoa(len(purge.LoadBalancers))+", networks: "+itoa(len(purge.Networks))+", firewalls: "+itoa(len(purge.Firewalls))+", ssh-keys: "+itoa(len(purge.SSHKeys))+")")
	} else if len(purge.Errors) == 0 {
		emit("wipe", "info", "Hetzner orphan purge: nothing to remove (clean account)")
	}

	// Step 3 — PDM release (pool-subdomain only). Best-effort. Resolve pool
	// + subdomain from either the Deployment record (set during the
	// reservation step) or from the FQDN as a fallback.
	poolDomain, subdomain := dep.pdmPoolDomain, dep.pdmSubdomain
	if poolDomain == "" || subdomain == "" {
		// Fallback: split sovereignFQDN at the first dot.
		if idx := strings.IndexByte(dep.Request.SovereignFQDN, '.'); idx > 0 {
			subdomain = dep.Request.SovereignFQDN[:idx]
			poolDomain = dep.Request.SovereignFQDN[idx+1:]
		}
	}
	if dep.Request.SovereignDomainMode == "pool" && poolDomain != "" && subdomain != "" {
		releaseCtx, releaseCancel := context.WithTimeout(r.Context(), 30*time.Second)
		if err := h.pdm.Release(releaseCtx, poolDomain, subdomain); err != nil {
			report.Errors = append(report.Errors, "pdm release: "+err.Error())
			emit("wipe", "warn", "PDM release failed (operator must run pdm-cli force-release later): "+err.Error())
		} else {
			report.PDMReleased = true
			emit("wipe", "info", "PDM allocation released for "+subdomain+"."+poolDomain)
		}
		releaseCancel()
	} else {
		emit("wipe", "info", "BYO or unresolvable pool — no PDM allocation to release")
	}

	// Step 4 — local state cleanup. Three things to clean:
	//   - kubeconfig file (mode 0600 per file)
	//   - tofu workdir (already removed by Destroy on success, but be
	//     defensive in case Destroy returned an error and left it)
	//   - on-disk deployment record JSON
	if h.kubeconfigsDir != "" {
		kcPath := filepath.Join(h.kubeconfigsDir, id+".yaml")
		if err := os.Remove(kcPath); err != nil && !os.IsNotExist(err) {
			report.Errors = append(report.Errors, "remove kubeconfig: "+err.Error())
		} else if err == nil {
			emit("wipe", "info", "kubeconfig file removed: "+kcPath)
		}
	}

	tofuWorkDir := filepath.Join(prov.WorkDir, deploymentSovereignName(dep.Request.SovereignFQDN))
	if err := os.RemoveAll(tofuWorkDir); err != nil {
		report.Errors = append(report.Errors, "remove tofu workdir: "+err.Error())
	}

	if h.store != nil {
		if err := h.store.Delete(id); err != nil {
			report.Errors = append(report.Errors, "store delete: "+err.Error())
		} else {
			report.LocalCleaned = true
			emit("wipe", "info", "deployment record removed from on-disk store")
		}
	} else {
		report.LocalCleaned = true
	}

	// Step 5 — finalize. Mark the deployment "wiped" in memory and close
	// the events channel so the SSE stream terminates with a clean
	// boundary. The next GET /events returns the full purge log; any
	// future GET on the deployment id returns 410 Gone (handled in
	// GetDeployment).
	dep.mu.Lock()
	dep.Status = "wiped"
	dep.FinishedAt = time.Now().UTC()
	dep.mu.Unlock()

	// Don't immediately remove from sync.Map — we want StreamLogs
	// reconnects within ~30s to see the final wipe summary frames. A
	// background goroutine reaps after a TTL.
	go func() {
		time.Sleep(60 * time.Second)
		h.deployments.Delete(id)
	}()

	emit("wipe", "info", "Wipe complete. Start a fresh deployment from /sovereign.")

	// Close the events channel so SSE consumers get a clean EOF after
	// replaying the purge log.
	dep.mu.Lock()
	if dep.eventsCh != nil {
		close(dep.eventsCh)
		dep.eventsCh = nil
	}
	dep.mu.Unlock()

	writeJSON(w, http.StatusOK, report)
}

// deploymentSovereignName mirrors provisioner.Request.sovereignName() —
// dot-to-dash so the workdir lookup matches what Provision/Destroy use.
// Duplicated here (vs exported on the package) to keep that field
// internal to the provisioner; the handler only needs this for cleanup.
func deploymentSovereignName(fqdn string) string {
	return strings.ReplaceAll(fqdn, ".", "-")
}

// decodeJSONBody is a thin error-wrapping helper for request bodies. Other
// handlers in this package use json.NewDecoder directly; we wrap to
// produce a consistent 400 message.
func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}
