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

// Deployment captures provisioning state for a single Sovereign run.
type Deployment struct {
	ID         string
	Status     string // pending | provisioning | tofu-applying | flux-bootstrapping | ready | failed
	Request    provisioner.Request
	Result     *provisioner.Result
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	Events     chan provisioner.Event
	mu         sync.Mutex

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

// State returns a JSON-safe snapshot for the GET endpoint.
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
		Events:    make(chan provisioner.Event, 256),
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

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-dep.Events:
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

func (h *Handler) runProvisioning(dep *Deployment) {
	defer close(dep.Events)

	prov := provisioner.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := prov.Provision(ctx, dep.Request, dep.Events)

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
