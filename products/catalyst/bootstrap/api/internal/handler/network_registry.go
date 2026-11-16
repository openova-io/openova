// network_registry.go — the parent keeps the network registry of a ready
// Sovereign in every region, also for a Sovereign handed over before the
// registry existed (SOV-175, #7938).
//
// THE GAP. The registry ConfigMap flux-system/sovereign-network-registry is
// written by lifecycle.DeliverNetworkRegistry, whose one caller is the
// adoption step of the hand-over (post_handover_adoption_apply.go), fired from
// a Phase-1 watch's OutcomeReady or the converged-late rescue. restoreFromStore
// resumes the secondary-kubeconfig delivery and the spine on every start, but
// not the registry. So a Sovereign handed over by a parent that predates the
// registry (hw307, handed over 15 Nov) never gets the ConfigMap, and the writer
// of slot 12b never fills status.network of sovereigns/sovereign-self, short of
// re-running the whole hand-over chain with POST .../phases/phase1-watch/retry.
//
// ONE PRODUCER, TWO TRIGGERS. Both call lifecycle.ReconcileNetworkRegistry
// with the deployment's OpenTofu state and the parent's cluster cache
// (k8scache): in every region, read the ConfigMap; write it with the hand-over's
// server-side apply only when it is absent or carries another document.
//
//  1. Start. SetK8sCache runs one pass over every ready record. That is the
//     first moment the parent holds both inputs: restoreFromStore loads the
//     records inside New(), before main.go starts the cache and calls
//     SetK8sCache, so a pass in restoreFromStore would find no client.
//  2. POST /api/v1/deployments/{id}/network-registry, for one deployment and
//     nothing else; owner-checked like its neighbours, and mothership-only.
//
// The writes use the region's kubeconfig in the cache: ServiceAccount
// kube-system/parent-bootstrap of that region (cluster-admin). No new loop,
// no new client.
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// errNoOpenTofuState: the parent holds no OpenTofu state for the deployment.
var errNoOpenTofuState = errors.New("the parent holds no OpenTofu state for the deployment")

// networkRegistryResponse is the body of the route.
type networkRegistryResponse struct {
	DeploymentID string                   `json:"deploymentId"`
	Revision     string                   `json:"revision"`
	Regions      []lifecycle.RegionResult `json:"regions"`
}

// reconcileNetworkRegistry reads the deployment's OpenTofu state (the file
// the adoption step reads) and runs the reconcile with clusters.
func (h *Handler) reconcileNetworkRegistry(ctx context.Context, depID string, clusters lifecycle.Clusters) (string, []lifecycle.RegionResult, error) {
	state, err := os.ReadFile(filepath.Clean(filepath.Join(provisioner.New().WorkDir, depID, "terraform.tfstate")))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errNoOpenTofuState, err)
	}
	return lifecycle.ReconcileNetworkRegistry(ctx, state, depID, clusters)
}

// networkRegistryLog is h.log, or the default logger for a Handler built
// without one (many tests build &Handler{} and call SetK8sCache).
func (h *Handler) networkRegistryLog() *slog.Logger {
	if h.log != nil {
		return h.log
	}
	return slog.Default()
}

// reconcileNetworkRegistries is the start pass: every ready record, one after
// the other, with one log line each. It returns the ids it judged. On a
// Sovereign (SOVEREIGN_FQDN set) it does nothing: the parent is the writer,
// and a Sovereign holds no OpenTofu state.
func (h *Handler) reconcileNetworkRegistries(ctx context.Context, clusters lifecycle.Clusters) []string {
	if isChroot() {
		return nil
	}
	var ids []string
	h.deployments.Range(func(_, v any) bool {
		dep, ok := v.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		id, ready := dep.ID, dep.Status == "ready"
		dep.mu.Unlock()
		if ready {
			ids = append(ids, id)
		}
		return true
	})
	sort.Strings(ids)
	log := h.networkRegistryLog()
	for _, id := range ids {
		rev, regions, err := h.reconcileNetworkRegistry(ctx, id, clusters)
		if err != nil {
			log.Warn("network-registry", "id", id, "trigger", "start", "err", err.Error())
			continue
		}
		log.Info("network-registry", "id", id, "trigger", "start", "revision", rev, "regions", regions)
	}
	return ids
}

// HandleNetworkRegistry handles POST /api/v1/deployments/{id}/network-registry:
// the reconcile for one ready deployment, nothing else.
//
//	200 every region was read; regions[].written says where it wrote
//	404 unknown id, or a session that does not own the deployment
//	409 the deployment is not ready
//	422 the parent holds no OpenTofu state or no registry in it, or no
//	    cluster of the deployment in its cache
//	502 a region could not be read or written; regions[].error says why
//	503 the parent runs without its cluster cache
func (h *Handler) HandleNetworkRegistry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return
	}
	dep := val.(*Deployment)
	if !h.checkOwnership(w, r, dep) {
		return
	}
	dep.mu.Lock()
	status := dep.Status
	dep.mu.Unlock()
	if status != "ready" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "deployment-not-ready",
			"status": status,
			"detail": "the network registry is written into the regions of a ready deployment only",
		})
		return
	}
	if h.k8sCache == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cluster-cache-not-wired", "detail": lifecycle.ErrNoClusterCache.Error()})
		return
	}
	rev, regions, err := h.reconcileNetworkRegistry(r.Context(), dep.ID, h.k8sCache)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if errors.Is(err, lifecycle.ErrNoClusterCache) {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]string{"error": "network-registry-not-delivered", "detail": err.Error()})
		return
	}
	h.networkRegistryLog().Info("network-registry", "id", dep.ID, "trigger", "route", "revision", rev, "regions", regions)
	code := http.StatusOK
	for _, region := range regions {
		if region.Error != "" {
			code = http.StatusBadGateway
		}
	}
	writeJSON(w, code, networkRegistryResponse{DeploymentID: dep.ID, Revision: rev, Regions: regions})
}
