// Package handler — HTTP handlers wired to the real Hetzner provisioner.
//
// Endpoints:
//   POST /api/v1/deployments         — start a real Hetzner provisioning run
//   GET  /api/v1/deployments/{id}/logs — SSE stream of provisioning events
//   GET  /api/v1/deployments/{id}      — current deployment state (polled by wizard)
//
// Per docs/PROVISIONING-PLAN.md and the user's "no mocks" directive: this
// handler delegates to internal/hetzner.Provisioner which makes real Hetzner
// Cloud API calls. The token + project ID + region come from the wizard
// payload (StepCredentials + StepInfrastructure / StepOrg).
package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
)

// Deployment captures provisioning state for a single Sovereign run.
type Deployment struct {
	ID         string
	Status     string // pending | provisioning | bootstrapping | ready | failed
	Request    hetzner.ProvisionRequest
	Result     *hetzner.Result
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	Events     chan hetzner.Event
	mu         sync.Mutex
}

// State returns a JSON-safe snapshot of the deployment for the GET endpoint.
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
	var req hetzner.ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
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
		Events:    make(chan hetzner.Event, 256),
	}
	h.deployments.Store(id, dep)

	go h.runProvisioning(dep)

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":        id,
		"status":    dep.Status,
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

	prov := hetzner.New()
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
