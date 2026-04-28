package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// StartKubeconfigReconciler periodically walks every tenant-* namespace with a
// Ready vcluster HelmRelease and ensures flux-system/tenant-<slug>-kubeconfig
// exists. Without this loop, a provisioning pod killed between
// waitForVclusterDNSOrKick and mirrorVClusterKubeconfig (CI deploy, OOM, node
// drain) strands the tenant — the new pod's event consumer is past the
// tenant.provisioning_requested offset and never re-runs the mirror, so Flux
// sits forever with "secret not found". Issue #104.
//
// The reconciler is intentionally cheap and stateless:
//   - Lists namespaces matching "tenant-*".
//   - For each, checks whether vcluster HelmRelease is Ready AND whether
//     flux-system/tenant-<slug>-kubeconfig already exists.
//   - Calls mirrorVClusterKubeconfig for the ones that are Ready-but-missing.
//
// Cadence: every 60s. Fast enough that a stranded tenant recovers within a
// minute of the next pod being up, slow enough that steady-state load is
// negligible (one LIST + a handful of GETs per cycle).
func (h *Handler) StartKubeconfigReconciler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		// Run one pass at startup so a pod that restarted mid-provision
		// self-heals immediately instead of waiting 60s for the first tick.
		h.reconcileMirrors(ctx)
		for {
			select {
			case <-ctx.Done():
				slog.Info("kubeconfig reconciler stopping")
				return
			case <-ticker.C:
				h.reconcileMirrors(ctx)
			}
		}
	}()
	slog.Info("kubeconfig reconciler started")
}

// reconcileMirrors is one pass of the self-heal loop. Exposed (unexported to
// the package) so tests can invoke it directly.
func (h *Handler) reconcileMirrors(ctx context.Context) {
	body, err := h.k8sGet("/api/v1/namespaces?labelSelector=openova.io/managed-by=provisioning")
	if err != nil {
		slog.Debug("kubeconfig reconciler: list tenant namespaces failed", "error", err)
		return
	}
	var nsList struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &nsList); err != nil {
		slog.Debug("kubeconfig reconciler: decode namespace list", "error", err)
		return
	}
	mirrored := 0
	for _, ns := range nsList.Items {
		name := ns.Metadata.Name
		if !strings.HasPrefix(name, "tenant-") {
			continue
		}
		slug := strings.TrimPrefix(name, "tenant-")
		if slug == "" {
			continue
		}
		if !h.vclusterHelmReleaseReady(name) {
			continue
		}
		if h.kubeconfigMirrorExists(slug) {
			continue
		}
		// Ready vcluster + missing mirror = the exact drop we're healing.
		slog.Warn("kubeconfig reconciler: mirror missing for ready tenant — healing",
			"slug", slug)
		if err := h.mirrorVClusterKubeconfig(ctx, slug); err != nil {
			slog.Error("kubeconfig reconciler: mirror failed",
				"slug", slug, "error", err)
			continue
		}
		mirrored++
	}
	if mirrored > 0 {
		slog.Info("kubeconfig reconciler: healed stranded tenants", "count", mirrored)
	}
}

// vclusterHelmReleaseReady returns true if the HelmRelease named "vcluster"
// in the given namespace has a Ready=True condition. Unknown/absent → false.
func (h *Handler) vclusterHelmReleaseReady(namespace string) bool {
	body, err := h.k8sGet("/apis/helm.toolkit.fluxcd.io/v2/namespaces/" + namespace + "/helmreleases/vcluster")
	if err != nil {
		return false
	}
	var hr struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &hr); err != nil {
		return false
	}
	for _, c := range hr.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

// kubeconfigMirrorExists returns true iff flux-system/tenant-<slug>-kubeconfig
// already exists.
func (h *Handler) kubeconfigMirrorExists(slug string) bool {
	_, err := h.k8sGet("/api/v1/namespaces/flux-system/secrets/tenant-" + slug + "-kubeconfig")
	return err == nil
}
