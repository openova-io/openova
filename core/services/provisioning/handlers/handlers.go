package handlers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for provisioning HTTP handlers.
type Handler struct {
	Store        *store.Store
	Producer     *events.Producer
	Generator    *gitops.ManifestGenerator
	GitHubClient *ghclient.Client
	CatalogURL   string // internal URL to catalog service

	// day2Cancels tracks in-flight day-2 job wait contexts so tenant.deleted
	// can preempt them (issue #99). Zero value is ready to use.
	day2Cancels day2CancelRegistry
}

// startRequest is the JSON body for manually starting a provision.
type startRequest struct {
	TenantID  string   `json:"tenant_id"`
	OrderID   string   `json:"order_id"`
	PlanID    string   `json:"plan_id"`
	Apps      []string `json:"apps"`
	Subdomain string   `json:"subdomain"`
}

// GetStatus returns the provision status by ID.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.Store.GetProvision(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get provision")
		return
	}
	if p == nil {
		respond.Error(w, http.StatusNotFound, "provision not found")
		return
	}
	respond.OK(w, p)
}

// GetByTenant returns the provision status for a given tenant.
func (h *Handler) GetByTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	p, err := h.Store.GetProvisionByTenant(r.Context(), tenantID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get provision")
		return
	}
	if p == nil {
		respond.Error(w, http.StatusNotFound, "provision not found for tenant")
		return
	}
	respond.OK(w, p)
}

// Start manually triggers provisioning (admin endpoint).
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.TenantID == "" || req.OrderID == "" || req.PlanID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id, order_id, and plan_id are required")
		return
	}

	provision, err := h.startProvisioning(r.Context(), req.TenantID, req.OrderID, req.PlanID, req.Apps, req.Subdomain)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to start provisioning")
		return
	}

	respond.JSON(w, http.StatusCreated, provision)
}

// ApplyAppInstall is the HTTP equivalent of the tenant.app_install_requested
// event. The tenant service calls this directly after persisting tenant.Apps,
// which keeps day-2 working when RedPanda is offline. Returns 202 and runs
// the apply/wait flow in a goroutine so the tenant service isn't blocked.
// The async worker drives the shared Job lifecycle so the Jobs page renders
// the same shape regardless of which transport (HTTP / event-bus) was used.
func (h *Handler) ApplyAppInstall(w http.ResponseWriter, r *http.Request) {
	var data appChangeData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if data.TenantID == "" || data.TenantSlug == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id and tenant_slug are required")
		return
	}
	go func() {
		if err := h.runInstallJob(context.Background(), data); err != nil {
			slog.Error("day-2 install (http)", "tenant", data.TenantSlug, "error", err)
		}
	}()
	respond.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ApplyAppUninstall is the HTTP twin of ApplyAppInstall for uninstall.
func (h *Handler) ApplyAppUninstall(w http.ResponseWriter, r *http.Request) {
	var data appChangeData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if data.TenantID == "" || data.TenantSlug == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id and tenant_slug are required")
		return
	}
	go func() {
		if err := h.runUninstallJob(context.Background(), data); err != nil {
			slog.Error("day-2 uninstall (http)", "tenant", data.TenantSlug, "error", err)
		}
	}()
	respond.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// List returns all provisions with pagination (admin endpoint).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	limit := 50

	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		}
	}
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	provisions, err := h.Store.ListProvisions(r.Context(), offset, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list provisions")
		return
	}
	respond.OK(w, provisions)
}

// --- catalog resolution helpers ---

// catalogAppResp mirrors the /catalog/apps response shape we care about.
// DependencyIDs is the resolved canonical-ID view of Dependencies that the
// catalog service computes once per request (see #89). Keying provisioning
// logic by ID lets us drop the slug↔ID translation maps that used to live
// here and in computePurgeRetention — the two services now agree on a single
// identifier kind.
type catalogAppResp struct {
	ID            string   `json:"id"`
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	Dependencies  []string `json:"dependencies"`   // slugs (admin-friendly)
	DependencyIDs []string `json:"dependency_ids"` // canonical UUIDs — preferred
}

// fetchCatalogApps is the single place we call GET /catalog/apps. Every
// translation helper (name lookup, slug lookup, dependency walk) derives
// from the same response so we don't fan out N requests on a single event.
// Returns (nil, false) on any non-success so callers can fall back cleanly
// without duplicating the error-handling boilerplate.
func (h *Handler) fetchCatalogApps(ctx context.Context) ([]catalogAppResp, bool) {
	if h.CatalogURL == "" {
		return nil, false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.CatalogURL+"/catalog/apps", nil)
	if err != nil {
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, false
	}
	var apps []catalogAppResp
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, false
	}
	return apps, true
}

// resolveAppNames fetches app names from the catalog service, keyed by ID.
func (h *Handler) resolveAppNames(ctx context.Context) map[string]string {
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return nil
	}
	m := make(map[string]string, len(apps))
	for _, a := range apps {
		m[a.ID] = a.Name
	}
	return m
}

// resolveAppSlugs resolves app UUIDs to slugs via the catalog.
func (h *Handler) resolveAppSlugs(ctx context.Context, appIDs []string) []string {
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return appIDs
	}
	idToSlug := make(map[string]string, len(apps))
	for _, a := range apps {
		idToSlug[a.ID] = a.Slug
	}
	slugs := make([]string, len(appIDs))
	for i, id := range appIDs {
		if slug, ok := idToSlug[id]; ok {
			slugs[i] = slug
		} else {
			slugs[i] = id // fallback to ID
		}
	}
	return slugs
}

// resolveAppDependencies returns the catalog-defined dependency slugs for the
// given app slugs. These are installed alongside the user-selected apps
// (e.g. WordPress → mysql). The existing NeedsDB mechanism in gitops still
// handles DB creation; this surface is here so the UI can show what's being
// installed and future non-DB deps can use the same path.
//
// #89: uses the shared fetchCatalogApps helper instead of re-implementing
// the request + decode. Output keyed by slug because the caller
// (startProvisioning) names provisioning steps by slug.
func (h *Handler) resolveAppDependencies(ctx context.Context, appSlugs []string) map[string][]string {
	deps := make(map[string][]string, len(appSlugs))
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return deps
	}
	bySlug := make(map[string][]string, len(apps))
	for _, a := range apps {
		bySlug[a.Slug] = a.Dependencies
	}
	for _, slug := range appSlugs {
		if d := bySlug[slug]; len(d) > 0 {
			deps[slug] = d
		}
	}
	return deps
}

// resolvePlanSlug fetches the plan slug for a plan UUID from the catalog.
func (h *Handler) resolvePlanSlug(ctx context.Context, planID string) string {
	if h.CatalogURL == "" {
		return "s" // default
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.CatalogURL+"/catalog/plans", nil)
	if err != nil {
		return "s"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "s"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return "s"
	}
	var plans []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&plans); err != nil {
		return "s"
	}
	for _, p := range plans {
		if p.ID == planID {
			return p.Slug
		}
	}
	return "s"
}

// appDisplayName returns a human-readable name for an app.
func appDisplayName(names map[string]string, id string) string {
	if names != nil {
		if n, ok := names[id]; ok {
			return n
		}
	}
	if len(id) > 8 {
		return fmt.Sprintf("app-%s", id[:8])
	}
	return id
}

// --- K8s API helpers for monitoring deployment status ---

// waitForDeployment polls the K8s API until the deployment has at least one
// ready replica or the timeout expires.
func (h *Handler) waitForDeployment(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := h.checkDeploymentReady(namespace, name)
		if err == nil && ready {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("deployment %s/%s not ready after %s", namespace, name, timeout)
}

// waitForAnyPod waits until at least one pod is Running in the namespace.
func (h *Handler) waitForAnyPod(ctx context.Context, namespace string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := h.checkAnyPodRunning(namespace)
		if err == nil && running {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("no running pods in %s after %s", namespace, timeout)
}

// checkDeploymentReady uses the in-cluster K8s API to check deployment readiness.
func (h *Handler) checkDeploymentReady(namespace, name string) (bool, error) {
	body, err := h.k8sGet(fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", namespace, name))
	if err != nil {
		return false, err
	}
	var dep struct {
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &dep); err != nil {
		return false, err
	}
	return dep.Status.ReadyReplicas > 0, nil
}

// checkAnyPodRunning checks if any pod in the namespace is Running.
func (h *Handler) checkAnyPodRunning(namespace string) (bool, error) {
	body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
	if err != nil {
		return false, err
	}
	var podList struct {
		Items []struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &podList); err != nil {
		return false, err
	}
	for _, pod := range podList.Items {
		if pod.Status.Phase == "Running" {
			return true, nil
		}
	}
	return false, nil
}

// waitForVclusterDNSOrKick polls the host NS for the synced kube-dns service
// that vcluster's syncer creates (named kube-dns-x-kube-system-x-vcluster).
// If it doesn't appear within 60s, delete vcluster-0 to force the syncer to
// re-initialize, then poll for another 60s. Returns nil on success, error if
// DNS is still missing after the kick. Issue #103.
//
// Why this matters: without kube-dns synced, pods inside the vcluster stay
// Pending with "waiting for DNS service IP" — every app install that follows
// times out at 10 min. In today's harness run tenant e2e90689b hit this on
// provisioning and only recovered when an operator manually restarted
// vcluster-0. Folding that workaround into the provisioning flow removes the
// operator-in-the-loop requirement.
func (h *Handler) waitForVclusterDNSOrKick(ctx context.Context, hostNS string) error {
	dnsSvc := "/api/v1/namespaces/" + hostNS + "/services/kube-dns-x-kube-system-x-vcluster"
	poll := func(timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if _, err := h.k8sGet(dnsSvc); err == nil {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(5 * time.Second):
			}
		}
		return false
	}

	if poll(60 * time.Second) {
		slog.Info("vcluster dns synced", "ns", hostNS)
		return nil
	}

	slog.Warn("vcluster dns not synced after 60s — kicking vcluster-0", "ns", hostNS)
	// Delete vcluster-0 via the DELETE pod endpoint; the StatefulSet will
	// recreate it and the fresh syncer usually publishes kube-dns within ~12s.
	if err := h.k8sDelete("/api/v1/namespaces/" + hostNS + "/pods/vcluster-0"); err != nil {
		slog.Warn("vcluster dns kick: delete vcluster-0 failed — may still recover",
			"ns", hostNS, "error", err)
	}

	if poll(90 * time.Second) {
		slog.Info("vcluster dns synced after kick", "ns", hostNS)
		return nil
	}
	return fmt.Errorf("kube-dns service still missing in %s after vcluster-0 restart", hostNS)
}

// waitForHelmRelease polls Flux's HelmRelease resource until its Ready condition
// is True or the timeout expires. Used to gate on vCluster being online.
func (h *Handler) waitForHelmRelease(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/apis/helm.toolkit.fluxcd.io/v2/namespaces/%s/helmreleases/%s", namespace, name))
		if err == nil {
			var hr struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
						Reason string `json:"reason"`
					} `json:"conditions"`
				} `json:"status"`
			}
			if jerr := json.Unmarshal(body, &hr); jerr == nil {
				for _, c := range hr.Status.Conditions {
					if c.Type == "Ready" && c.Status == "True" {
						slog.Info("helmrelease ready", "namespace", namespace, "name", name)
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("helmrelease %s/%s not ready after %s", namespace, name, timeout)
}

// waitForVclusterApp waits until a vCluster-synced pod for the given app slug
// is Running+Ready in the host namespace. vCluster syncs pods using the name
// pattern <pod>-x-<inner-ns>-x-<vcluster-name> — the inner ns is "apps" and the
// vcluster helm release name is "vcluster", so we look for `<appSlug>-...-x-apps-x-vcluster`.
func (h *Handler) waitForVclusterApp(ctx context.Context, namespace, appSlug string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	prefix := appSlug + "-"
	suffix := "-x-apps-x-vcluster"
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
		if err == nil {
			var podList struct {
				Items []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Status struct {
						Phase      string `json:"phase"`
						Conditions []struct {
							Type   string `json:"type"`
							Status string `json:"status"`
						} `json:"conditions"`
					} `json:"status"`
				} `json:"items"`
			}
			if jerr := json.Unmarshal(body, &podList); jerr == nil {
				for _, pod := range podList.Items {
					name := pod.Metadata.Name
					if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
						continue
					}
					if pod.Status.Phase != "Running" {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == "Ready" && c.Status == "True" {
							slog.Info("vcluster app pod ready", "namespace", namespace, "pod", name)
							return nil
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("app %s not ready in %s after %s", appSlug, namespace, timeout)
}

// waitForCertificate polls cert-manager's Certificate resource until its Ready
// condition is True. Returns nil on ready, error on timeout — callers can
// decide whether a still-issuing cert is fatal.
func (h *Handler) waitForCertificate(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificates/%s", namespace, name))
		if err == nil {
			var cert struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			}
			if jerr := json.Unmarshal(body, &cert); jerr == nil {
				for _, c := range cert.Status.Conditions {
					if c.Type == "Ready" && c.Status == "True" {
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("certificate %s/%s not ready after %s", namespace, name, timeout)
}

// k8sGet makes a GET request to the in-cluster Kubernetes API.
func (h *Handler) k8sGet(path string) ([]byte, error) {
	return h.k8sRequest(http.MethodGet, path, nil)
}

// k8sDelete issues a DELETE against the in-cluster API. Used for tenant
// teardown to explicitly drop Flux Kustomization / HelmRelease CRs so their
// finalizers don't strand the namespace.
func (h *Handler) k8sDelete(path string) error {
	body, err := h.k8sRequest(http.MethodDelete, path, nil)
	if err != nil {
		// 404 on a delete is success (already gone).
		if strings.Contains(err.Error(), "status 404") {
			return nil
		}
		return err
	}
	_ = body
	return nil
}

// k8sPatchRemoveFinalizers strips all .metadata.finalizers from a CR so
// Kubernetes can garbage-collect it. Used as last-resort when a finalizer
// is blocking namespace deletion for longer than the timeout.
func (h *Handler) k8sPatchRemoveFinalizers(path string) error {
	patch := []byte(`{"metadata":{"finalizers":null}}`)
	_, err := h.k8sRequest(http.MethodPatch, path, patch)
	if err != nil && strings.Contains(err.Error(), "status 404") {
		return nil
	}
	return err
}

func (h *Handler) k8sRequest(method, path string, body []byte) ([]byte, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in cluster")
	}

	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}

	url := fmt.Sprintf("https://%s:%s%s", host, port, path)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(tokenBytes))
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("k8s %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// mirrorVClusterKubeconfig copies the `vc-vcluster` Secret from the tenant
// namespace to flux-system as `tenant-<slug>-kubeconfig`. The per-tenant
// Flux Kustomization CR (which lives in flux-system per issue #97) references
// this mirror to reconcile resources into the vCluster. We mirror rather than
// place the CR in the tenant NS because:
//
//  1. Flux Kustomization.spec.kubeConfig.secretRef has no `namespace` field —
//     the secret must live in the CR's own namespace.
//  2. Placing the CR in tenant-<slug> re-introduces the finalizer-blocks-NS-GC
//     defect this whole fix is solving.
//
// Idempotent: if the mirror already exists it's updated in place (handles
// password rotation or kubeconfig CA rotation). Call this after the vcluster
// HelmRelease reaches Ready so the source secret definitely exists.
func (h *Handler) mirrorVClusterKubeconfig(ctx context.Context, tenantSlug string) error {
	srcNS := "tenant-" + tenantSlug
	srcName := "vc-vcluster"
	dstNS := "flux-system"
	dstName := "tenant-" + tenantSlug + "-kubeconfig"

	srcBody, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", srcNS, srcName))
	if err != nil {
		return fmt.Errorf("read source secret %s/%s: %w", srcNS, srcName, err)
	}
	var src struct {
		Data map[string]string `json:"data"`
		Type string            `json:"type"`
	}
	if err := json.Unmarshal(srcBody, &src); err != nil {
		return fmt.Errorf("parse source secret: %w", err)
	}

	// Build the destination secret payload. Copy .data verbatim (base64 values
	// survive the round trip) and carry over the type so opaque stays opaque.
	//
	// NB: K8s label values are restricted to [A-Za-z0-9-_.], so the source-ns
	// reference goes in an annotation (unconstrained) rather than a label.
	dst := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      dstName,
			"namespace": dstNS,
			"labels": map[string]string{
				"openova.io/tenant":     tenantSlug,
				"openova.io/managed-by": "provisioning",
			},
			"annotations": map[string]string{
				"openova.io/mirror-of": srcNS + "/" + srcName,
			},
		},
		"data": src.Data,
		"type": src.Type,
	}
	payload, err := json.Marshal(dst)
	if err != nil {
		return fmt.Errorf("marshal mirror secret: %w", err)
	}

	// Try create first; fall back to PUT (full replace) if it already exists.
	_, err = h.k8sRequest(http.MethodPost, fmt.Sprintf("/api/v1/namespaces/%s/secrets", dstNS), payload)
	if err == nil {
		slog.Info("mirrored vCluster kubeconfig", "src", srcNS+"/"+srcName, "dst", dstNS+"/"+dstName)
		return nil
	}
	// 409 conflict → already exists. Update via PUT to keep data fresh.
	if !strings.Contains(err.Error(), "status 409") {
		return fmt.Errorf("create mirror secret: %w", err)
	}
	_, err = h.k8sRequest(http.MethodPut, fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", dstNS, dstName), payload)
	if err != nil {
		return fmt.Errorf("update mirror secret: %w", err)
	}
	slog.Info("updated mirrored vCluster kubeconfig", "src", srcNS+"/"+srcName, "dst", dstNS+"/"+dstName)
	return nil
}

// deleteVClusterKubeconfigMirror removes the flux-system mirror secret during
// tenant teardown. 404 is treated as success (already gone).
func (h *Handler) deleteVClusterKubeconfigMirror(ctx context.Context, tenantSlug string) error {
	return h.k8sDelete(fmt.Sprintf(
		"/api/v1/namespaces/flux-system/secrets/tenant-%s-kubeconfig", tenantSlug))
}
