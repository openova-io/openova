// Package handler — infrastructure.go: read-only REST surface for the
// Sovereign Infrastructure page (issue #227).
//
//	GET    /api/v1/deployments/{depId}/infrastructure/topology  — unified
//	GET    /api/v1/deployments/{depId}/infrastructure/compute   — legacy
//	GET    /api/v1/deployments/{depId}/infrastructure/storage   — legacy
//	GET    /api/v1/deployments/{depId}/infrastructure/network   — legacy
//
// The endpoints compose from the deployment record and the live
// cluster. They write nothing. The file also holds request helpers
// that other handlers of this package share.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/infrastructure"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

/* ── Wire shapes — JSON tags must match the TS contract verbatim ─── */
//
// These shapes back the LEGACY GET endpoints (compute/storage/network).
// The unified topology endpoint emits infrastructure.TopologyResponse.
// The legacy endpoints will be deprecated once the FE migrates to the
// unified shape; per the task spec we keep them working until then.

type infraClusterItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ControlPlane string `json:"controlPlane"`
	Version      string `json:"version"`
	Region       string `json:"region"`
	NodeCount    int    `json:"nodeCount"`
	Status       string `json:"status"`
}

type infraNodeItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SKU    string `json:"sku"`
	Region string `json:"region"`
	Role   string `json:"role"`
	IP     string `json:"ip"`
	Status string `json:"status"`
}

type infraComputeResponse struct {
	Clusters []infraClusterItem `json:"clusters"`
	Nodes    []infraNodeItem    `json:"nodes"`
}

type infraPVCItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Capacity     string `json:"capacity"`
	Used         string `json:"used"`
	StorageClass string `json:"storageClass"`
	Status       string `json:"status"`
}

type infraBucketItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Endpoint      string `json:"endpoint"`
	Capacity      string `json:"capacity"`
	Used          string `json:"used"`
	RetentionDays string `json:"retentionDays"`
}

type infraVolumeItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Capacity   string `json:"capacity"`
	Region     string `json:"region"`
	AttachedTo string `json:"attachedTo"`
	Status     string `json:"status"`
}

type infraStorageResponse struct {
	PVCs    []infraPVCItem    `json:"pvcs"`
	Buckets []infraBucketItem `json:"buckets"`
	Volumes []infraVolumeItem `json:"volumes"`
}

type infraLBItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PublicIP     string `json:"publicIP"`
	Ports        string `json:"ports"`
	TargetHealth string `json:"targetHealth"`
	Region       string `json:"region"`
	Status       string `json:"status"`
}

type infraDRGItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	CIDR   string `json:"cidr"`
	Region string `json:"region"`
	Peers  string `json:"peers"`
	Status string `json:"status"`
}

type infraPeeringItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	VPCPair string `json:"vpcPair"`
	Subnets string `json:"subnets"`
	Status  string `json:"status"`
}

type infraNetworkResponse struct {
	LoadBalancers []infraLBItem      `json:"loadBalancers"`
	DRGs          []infraDRGItem     `json:"drgs"`
	Peerings      []infraPeeringItem `json:"peerings"`
}

/* ── HTTP handlers — read endpoints ────────────────────────────── */

// GetInfrastructureTopology — the UNIFIED topology endpoint. Returns
// the whole hierarchical tree (cloud → topology.regions[*] → clusters
// → vclusters | pools | nodes | LBs + storage). The four FE tabs all
// derive their views off this one response.
//
// Today the loader composes from the deployment record + the live
// cluster informer cache; the legacy endpoints below remain wired
// until the FE cuts over to the unified shape (per the task spec's
// "keep existing read endpoints working until unified deploys").
func (h *Handler) GetInfrastructureTopology(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "deployment-not-found",
			"detail": "no deployment with id " + depID,
		})
		return
	}
	in := h.loaderInputFor(dep)
	resp := infrastructure.Load(r.Context(), in)
	writeJSON(w, http.StatusOK, resp)
}

// GetInfrastructureCompute — legacy compute view. Kept while the FE
// migrates to the unified topology endpoint. Composes from the same
// deployment record fields the topology loader does, but emits the
// pre-existing flat shape.
func (h *Handler) GetInfrastructureCompute(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "deployment-not-found",
			"detail": "no deployment with id " + depID,
		})
		return
	}
	writeJSON(w, http.StatusOK, buildInfraCompute(dep))
}

// GetInfrastructureStorage — legacy storage view. Today returns the
// well-shaped empty response; the unified topology endpoint sources
// PVCs from the live cluster informer when reachable.
func (h *Handler) GetInfrastructureStorage(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	_, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "deployment-not-found",
			"detail": "no deployment with id " + depID,
		})
		return
	}
	writeJSON(w, http.StatusOK, infraStorageResponse{
		PVCs:    []infraPVCItem{},
		Buckets: []infraBucketItem{},
		Volumes: []infraVolumeItem{},
	})
}

// GetInfrastructureNetwork — legacy network view. Surfaces the LB
// from the deployment record + empty arrays for DRGs / peerings.
func (h *Handler) GetInfrastructureNetwork(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "deployment-not-found",
			"detail": "no deployment with id " + depID,
		})
		return
	}
	writeJSON(w, http.StatusOK, buildInfraNetwork(dep))
}

/* ── Internal helpers ──────────────────────────────────────────── */

// bridgeFor — returns the per-deployment jobs.Bridge, allocating one
// when the deployment doesn't have one yet (a rehydrated deployment
// whose helmwatch hasn't started). The bridge is stored on the
// Deployment so later writers append to the same audit-trail surface.
func (h *Handler) bridgeFor(dep *Deployment) *jobs.Bridge {
	if h.jobs == nil {
		// Tests without persistence — surface a bridge backed by a
		// transient store.
		return jobs.NewBridge(noopJobsStore(), dep.ID)
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.jobsBridge != nil {
		return dep.jobsBridge
	}
	bridge := jobs.NewBridge(h.jobs, dep.ID)
	dep.jobsBridge = bridge
	return bridge
}

// noopJobsStore — in-test fallback when h.jobs is nil. Returns a
// jobs.Store rooted at os.TempDir() so the in-test bridge still has
// a coherent backing store. Per docs/INVIOLABLE-PRINCIPLES.md #2 we
// don't fabricate data — the Store is real, just transient.
func noopJobsStore() *jobs.Store {
	dir, err := os.MkdirTemp("", "catalyst-jobs-noop-")
	if err != nil {
		dir = "/tmp/catalyst-jobs-noop"
	}
	st, err := jobs.NewStore(dir)
	if err != nil {
		return nil
	}
	return st
}

// sovereignDynamicClient — builds a dynamic.Interface for the
// Sovereign cluster owning the given Deployment. Resolution order:
//
//  1. If catalyst-api is running ON the Sovereign cluster itself
//     (chroot mode — SOVEREIGN_FQDN env matches dep.Request.SovereignFQDN),
//     use rest.InClusterConfig(). The chroot's catalyst-api lives in
//     the same cluster as the Sovereign workloads it serves.
//  2. Otherwise (mother mode), read the deployment's persisted
//     kubeconfig that cloud-init posted back during provisioning.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 this is the ONLY path through
// which catalyst-api obtains a mutation-capable client against the
// Sovereign cluster.
func (h *Handler) sovereignDynamicClient(dep *Deployment) (dynamic.Interface, error) {
	dep.mu.Lock()
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	depFQDN := ""
	if dep.Request.SovereignFQDN != "" {
		depFQDN = dep.Request.SovereignFQDN
	}
	dep.mu.Unlock()

	if selfFQDN := os.Getenv("SOVEREIGN_FQDN"); selfFQDN != "" && selfFQDN == depFQDN {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("chroot in-cluster config: %w", err)
		}
		return dynamic.NewForConfig(cfg)
	}

	// #3153 conventional-path fallback — Result.KubeconfigPath carries json
	// `omitempty` and is LOST when a mothership Pod roll persists a
	// rehydrated deployment record before PutKubeconfig re-stamps it, even
	// though the kubeconfig FILE itself survives on the PVC at
	// <kubeconfigsDir>/<id>.yaml. Without this, a converged Sovereign's
	// mother-side catalyst-api can't build a client (record field null) and
	// the /jobs install+reconcile re-seed (#4731), retry, and reconciler
	// projections all silently no-op even though the cluster is fully
	// reachable. resolvePrimaryKubeconfigPath stat-guards the file, so a
	// record whose kubeconfig was genuinely never posted (provisioning in
	// flight) still returns the honest "not yet posted back" error below.
	if kubeconfigPath == "" {
		if resolved, ok := h.resolvePrimaryKubeconfigPath(dep); ok {
			kubeconfigPath = resolved
		}
	}

	if kubeconfigPath == "" {
		return nil, errors.New("sovereign cluster kubeconfig not yet posted back — cloud-init in flight or PUT /kubeconfig missed; retry once the wizard's success screen reaches Phase-1 ready")
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	if h.dynamicFactory != nil {
		return h.dynamicFactory(string(raw))
	}
	return helmwatch.NewDynamicClientFromKubeconfig(string(raw))
}

// loaderInputFor — projects the Deployment's fields onto the
// infrastructure.LoaderInput shape. Pulled out so the test path can
// reuse the projection without rebuilding the struct.
func (h *Handler) loaderInputFor(dep *Deployment) infrastructure.LoaderInput {
	dep.mu.Lock()
	defer dep.mu.Unlock()
	in := infrastructure.LoaderInput{
		DeploymentID:     dep.ID,
		Status:           statusForDeployment(dep),
		SovereignFQDN:    dep.Request.SovereignFQDN,
		Provider:         firstProvider(dep.Request),
		Region:           firstRegion(dep.Request),
		Regions:          append([]provisioner.RegionSpec(nil), dep.Request.Regions...),
		WorkerCount:      dep.Request.WorkerCount,
		WorkerSize:       dep.Request.WorkerSize,
		CPSize:           dep.Request.ControlPlaneSize,
		Result:           dep.Result,
		HetznerProjectID: dep.Request.HetznerProjectID,
		DynamicClient:    h.tryDynamicClientLocked(dep),
	}
	// G14 (Refs #2551): wire k8sCache so the chroot post-cutover
	// fallback in topology_loader.buildTopology can fan out across
	// every registered cluster (primary + secondary kubeconfigs)
	// instead of synthesising a single Region from the in-cluster
	// client. Nil-tolerant — tests without k8sCache fall through to
	// the single-cluster DynamicClient path.
	if h.k8sCache != nil {
		in.K8sCache = h.k8sCache
	}
	return in
}

// tryDynamicClientLocked — best-effort dynamic client for live-source
// reads. Caller MUST hold dep.mu (loaderInputFor is the only caller
// today). A failure (kubeconfig missing, parse error) returns nil
// without surfacing through to the loader; the loader treats nil
// as "no live data" and returns empty arrays.
//
// Chroot fallback: when catalyst-api runs ON the Sovereign cluster
// (SOVEREIGN_FQDN env matches dep.Request.SovereignFQDN), use the
// in-cluster ServiceAccount instead of looking for a posted-back
// kubeconfig — the chroot is its own kubeconfig.
func (h *Handler) tryDynamicClientLocked(dep *Deployment) dynamic.Interface {
	if selfFQDN := os.Getenv("SOVEREIGN_FQDN"); selfFQDN != "" && selfFQDN == dep.Request.SovereignFQDN {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil
		}
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil
		}
		return c
	}
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	if kubeconfigPath == "" {
		return nil
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil
	}
	if h.dynamicFactory != nil {
		c, err := h.dynamicFactory(string(raw))
		if err != nil {
			return nil
		}
		return c
	}
	c, err := helmwatch.NewDynamicClientFromKubeconfig(string(raw))
	if err != nil {
		return nil
	}
	return c
}

// decodeMutationBody reads + decodes the body. Returns false (after writing
// a 400) when decode fails so the caller can early-return.
func decodeMutationBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeBadRequest(w, "empty-body", "request body is required")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeBadRequest(w, "invalid-body", err.Error())
		return false
	}
	return true
}

// readMutationBody reads the request body for callers that need to do
// dual-shape decoding (e.g. applications install accepting both the
// canonical struct shape AND the simplified UI/matrix shape per
// applications_wire_compat.go).
//
// Returns (rawBody, true) when a 4xx was already written and the
// caller MUST early-return; (rawBody, false) on success. The boolean
// inversion vs decodeMutationBody is intentional — callers
// pattern-match `if errd { return }`.
func readMutationBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		writeBadRequest(w, "empty-body", "request body is required")
		return nil, true
	}
	const maxBody = int64(1 << 16)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeBadRequest(w, "body-read-failed", err.Error())
		return nil, true
	}
	if len(body) == 0 {
		writeBadRequest(w, "empty-body", "request body is required")
		return nil, true
	}
	return body, false
}

func writeNotFound(w http.ResponseWriter, depID string) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error":  "deployment-not-found",
		"detail": "no deployment with id " + depID,
	})
}

func writeBadRequest(w http.ResponseWriter, code, detail string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{
		"error":  code,
		"detail": detail,
	})
}

/* ── Legacy helpers (read endpoints) ───────────────────────────── */

// lookupDeploymentForInfra resolves a deployment by id from the
// in-memory map, mirroring GetDeployment's lookup. Returns nil + false
// on miss so the caller can write a 404 with a contextual error body.
func (h *Handler) lookupDeploymentForInfra(id string) (*Deployment, bool) {
	val, ok := h.deployments.Load(id)
	if ok {
		dep, ok := val.(*Deployment)
		if ok {
			return dep, true
		}
	}
	// Chroot post-cutover: synthesise an in-memory Deployment from
	// SOVEREIGN_FQDN so the topology loader gets a non-nil dep with
	// SovereignFQDN set; the loader's chroot fallback then probes the
	// live cluster for Nodes via the in-cluster dynamic client.
	if dep := h.chrootEnsureDeployment(id); dep != nil {
		return dep, true
	}
	return nil, false
}

// statusForDeployment maps the Deployment.Status string to the canonical
// TopologyStatus vocabulary the UI consumes (healthy / degraded / failed
// / unknown). Pre-Phase-0 deployments return "unknown".
func statusForDeployment(dep *Deployment) string {
	switch dep.Status {
	case "ready":
		return "healthy"
	case "failed":
		return "failed"
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}

// firstRegion returns the cloud region of the first regional spec, or
// the legacy singular region if Regions is empty.
func firstRegion(req provisioner.Request) string {
	if len(req.Regions) > 0 {
		return req.Regions[0].CloudRegion
	}
	return req.Region
}

// firstProvider returns the cloud provider of the first regional spec.
// Defaults to "hetzner" when no Regions slot is set.
func firstProvider(req provisioner.Request) string {
	if len(req.Regions) > 0 && req.Regions[0].Provider != "" {
		return req.Regions[0].Provider
	}
	return "hetzner"
}

// totalWorkerCount sums every Regions slot's WorkerCount, falling back
// to the legacy singular field.
func totalWorkerCount(req provisioner.Request) int {
	if len(req.Regions) > 0 {
		n := 0
		for _, rg := range req.Regions {
			n += rg.WorkerCount
		}
		return n
	}
	return req.WorkerCount
}

func buildInfraCompute(dep *Deployment) infraComputeResponse {
	dep.mu.Lock()
	defer dep.mu.Unlock()

	region := firstRegion(dep.Request)
	status := statusForDeployment(dep)
	fqdn := dep.Request.SovereignFQDN

	clusterName := fqdn
	if clusterName == "" {
		clusterName = "cluster-" + dep.ID[:minLen(dep.ID, 8)]
	}
	cluster := infraClusterItem{
		ID:           "cluster-" + dep.ID,
		Name:         clusterName,
		ControlPlane: "k3s",
		Version:      "v1.30",
		Region:       region,
		NodeCount:    totalWorkerCount(dep.Request) + 1,
		Status:       status,
	}

	nodes := []infraNodeItem{}
	if len(dep.Request.Regions) > 0 {
		for ri, rg := range dep.Request.Regions {
			cpIP := ""
			if dep.Result != nil && ri == 0 {
				cpIP = dep.Result.ControlPlaneIP
			}
			nodes = append(nodes, infraNodeItem{
				ID:     "node-cp-" + rg.CloudRegion,
				Name:   "control-plane-" + rg.CloudRegion,
				SKU:    rg.ControlPlaneSize,
				Region: rg.CloudRegion,
				Role:   "control-plane",
				IP:     cpIP,
				Status: status,
			})
			for i := 0; i < rg.WorkerCount; i++ {
				nodes = append(nodes, infraNodeItem{
					ID:     "node-w" + itoa(ri) + "-" + itoa(i) + "-" + rg.CloudRegion,
					Name:   "worker-" + itoa(i+1) + "-" + rg.CloudRegion,
					SKU:    rg.WorkerSize,
					Region: rg.CloudRegion,
					Role:   "worker",
					IP:     "",
					Status: status,
				})
			}
		}
	} else {
		cpIP := ""
		if dep.Result != nil {
			cpIP = dep.Result.ControlPlaneIP
		}
		nodes = append(nodes, infraNodeItem{
			ID:     "node-cp-" + dep.ID,
			Name:   "control-plane",
			SKU:    dep.Request.ControlPlaneSize,
			Region: region,
			Role:   "control-plane",
			IP:     cpIP,
			Status: status,
		})
		for i := 0; i < dep.Request.WorkerCount; i++ {
			nodes = append(nodes, infraNodeItem{
				ID:     "node-w-" + itoa(i) + "-" + dep.ID,
				Name:   "worker-" + itoa(i+1),
				SKU:    dep.Request.WorkerSize,
				Region: region,
				Role:   "worker",
				IP:     "",
				Status: status,
			})
		}
	}

	return infraComputeResponse{
		Clusters: []infraClusterItem{cluster},
		Nodes:    nodes,
	}
}

func buildInfraNetwork(dep *Deployment) infraNetworkResponse {
	dep.mu.Lock()
	defer dep.mu.Unlock()

	region := firstRegion(dep.Request)
	status := statusForDeployment(dep)
	fqdn := dep.Request.SovereignFQDN

	lbs := []infraLBItem{}
	if dep.Result != nil && dep.Result.LoadBalancerIP != "" {
		lbName := fqdn
		if lbName == "" {
			lbName = "ingress-lb"
		}
		lbs = append(lbs, infraLBItem{
			ID:           "lb-" + dep.ID,
			Name:         lbName,
			PublicIP:     dep.Result.LoadBalancerIP,
			Ports:        "80,443,6443",
			TargetHealth: "—",
			Region:       region,
			Status:       status,
		})
	}

	return infraNetworkResponse{
		LoadBalancers: lbs,
		DRGs:          []infraDRGItem{},
		Peerings:      []infraPeeringItem{},
	}
}

func minLen(s string, max int) int {
	if len(s) < max {
		return len(s)
	}
	return max
}

// itoa avoids strconv just for the int→string formatting.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
