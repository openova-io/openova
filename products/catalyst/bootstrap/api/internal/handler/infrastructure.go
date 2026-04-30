// Package handler — infrastructure.go: REST surface for the Sovereign
// Infrastructure page (issue #227).
//
//	GET /api/v1/deployments/{depId}/infrastructure/topology
//	GET /api/v1/deployments/{depId}/infrastructure/compute
//	GET /api/v1/deployments/{depId}/infrastructure/storage
//	GET /api/v1/deployments/{depId}/infrastructure/network
//
// Each endpoint reads from two data sources, merging in this order:
//
//  1. The deployment record's `provisioner.Result` — every Phase-0
//     output that the OpenTofu module persisted (control-plane IP,
//     load-balancer IP, region, etc.). This is always available the
//     moment Phase 0 finishes; no live cluster is needed.
//
//  2. (Future) The new Sovereign's POST-back kubeconfig — used to query
//     metrics-server / kubectl-equivalent state for live PVCs, services,
//     nodes, etc. The kubeconfig path lives at `Result.KubeconfigPath`
//     and is set by the cloud-init postback (issue #183).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target-state shape):
// the JSON response shapes here are the FINAL shapes the UI consumes.
// When a piece of live data is unavailable today (live PVC list, live
// metrics) the handler returns a well-shaped EMPTY response — not
// placeholder data. The UI handles empty state gracefully via its
// "Provisioning…" overlay.
//
// Per #4 (never hardcode): the handler reads region / IP / FQDN from
// the deployment record's Request + Result; nothing is inlined.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

/* ── Wire shapes — JSON tags must match the TS contract verbatim ─── */

type infraTopologyNode struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
}

type infraTopologyEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

type infraTopologyResponse struct {
	Nodes []infraTopologyNode `json:"nodes"`
	Edges []infraTopologyEdge `json:"edges"`
}

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
	ID       string `json:"id"`
	Name     string `json:"name"`
	VPCPair  string `json:"vpcPair"`
	Subnets  string `json:"subnets"`
	Status   string `json:"status"`
}

type infraNetworkResponse struct {
	LoadBalancers []infraLBItem      `json:"loadBalancers"`
	DRGs          []infraDRGItem     `json:"drgs"`
	Peerings      []infraPeeringItem `json:"peerings"`
}

/* ── HTTP handlers ────────────────────────────────────────────── */

// GetInfrastructureTopology handles
// GET /api/v1/deployments/{depId}/infrastructure/topology.
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
	writeJSON(w, http.StatusOK, buildInfraTopology(dep))
}

// GetInfrastructureCompute handles
// GET /api/v1/deployments/{depId}/infrastructure/compute.
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

// GetInfrastructureStorage handles
// GET /api/v1/deployments/{depId}/infrastructure/storage.
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
	// Storage queries require the new Sovereign's kubeconfig + a live
	// kubectl call. Per the file-header contract, until that integration
	// lands we return the well-shaped empty response so the UI's empty
	// state activates instead of placeholder data.
	writeJSON(w, http.StatusOK, infraStorageResponse{
		PVCs:    []infraPVCItem{},
		Buckets: []infraBucketItem{},
		Volumes: []infraVolumeItem{},
	})
}

// GetInfrastructureNetwork handles
// GET /api/v1/deployments/{depId}/infrastructure/network.
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

/* ── Helpers ─────────────────────────────────────────────────── */

// lookupDeploymentForInfra resolves a deployment by id from the
// in-memory map, mirroring GetDeployment's lookup. Returns nil + false
// on miss so the caller can write a 404 with a contextual error body.
func (h *Handler) lookupDeploymentForInfra(id string) (*Deployment, bool) {
	val, ok := h.deployments.Load(id)
	if !ok {
		return nil, false
	}
	dep, ok := val.(*Deployment)
	if !ok {
		return nil, false
	}
	return dep, true
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
		// pending / provisioning / tofu-applying / phase1-watching all
		// surface as unknown — the topology renderer paints these grey.
		return "unknown"
	}
}

// firstRegion returns the cloud region of the first regional spec, or
// the legacy singular region if Regions is empty. Empty when neither
// is set (e.g. a freshly-created deployment that hasn't reached
// Validate() yet).
func firstRegion(req provisioner.Request) string {
	if len(req.Regions) > 0 {
		return req.Regions[0].CloudRegion
	}
	return req.Region
}

// firstProvider returns the cloud provider of the first regional spec.
// Defaults to "hetzner" when no Regions slot is set (the legacy
// singular path is Hetzner-only).
func firstProvider(req provisioner.Request) string {
	if len(req.Regions) > 0 && req.Regions[0].Provider != "" {
		return req.Regions[0].Provider
	}
	return "hetzner"
}

// totalWorkerCount sums every Regions slot's WorkerCount, falling back
// to the legacy singular field. Used for the Cluster card's nodeCount.
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

// buildInfraTopology composes a layered topology graph from the
// deployment record. The layers (cloud → region → cluster → node | lb)
// are deterministic so the UI's force-free layered layout reads
// top-down without guesswork.
func buildInfraTopology(dep *Deployment) infraTopologyResponse {
	dep.mu.Lock()
	defer dep.mu.Unlock()

	provider := firstProvider(dep.Request)
	region := firstRegion(dep.Request)
	status := statusForDeployment(dep)
	fqdn := dep.Request.SovereignFQDN

	cloudID := "cloud-" + provider
	regionID := "region-" + region
	clusterID := "cluster-" + dep.ID
	lbID := "lb-" + dep.ID

	nodes := []infraTopologyNode{
		{
			ID:    cloudID,
			Kind:  "cloud",
			Label: provider,
			Status: status,
			Metadata: map[string]string{
				"provider": provider,
			},
		},
	}
	if region != "" {
		nodes = append(nodes, infraTopologyNode{
			ID:    regionID,
			Kind:  "region",
			Label: region,
			Status: status,
			Metadata: map[string]string{
				"cloudRegion": region,
				"provider":    provider,
			},
		})
	}
	clusterMeta := map[string]string{
		"sovereignFQDN": fqdn,
		"deploymentID":  dep.ID,
	}
	if dep.Result != nil {
		if dep.Result.ControlPlaneIP != "" {
			clusterMeta["controlPlaneIP"] = dep.Result.ControlPlaneIP
		}
		if dep.Result.ConsoleURL != "" {
			clusterMeta["consoleURL"] = dep.Result.ConsoleURL
		}
	}
	clusterLabel := fqdn
	if clusterLabel == "" {
		clusterLabel = "cluster-" + dep.ID[:minLen(dep.ID, 8)]
	}
	nodes = append(nodes, infraTopologyNode{
		ID:       clusterID,
		Kind:     "cluster",
		Label:    clusterLabel,
		Status:   status,
		Metadata: clusterMeta,
	})

	// Edges that always exist.
	edges := []infraTopologyEdge{}
	if region != "" {
		edges = append(edges, infraTopologyEdge{From: cloudID, To: regionID, Relation: "contains"})
		edges = append(edges, infraTopologyEdge{From: regionID, To: clusterID, Relation: "contains"})
	} else {
		edges = append(edges, infraTopologyEdge{From: cloudID, To: clusterID, Relation: "contains"})
	}

	// Worker nodes: synthesise one per Regions slot's WorkerCount + the
	// control-plane SKU. The OpenTofu module names them deterministically
	// via cloud-init; until the kubeconfig postback exposes the actual
	// node list, we surface the requested topology so the canvas mirrors
	// what was provisioned.
	for ri, rg := range dep.Request.Regions {
		// Control plane node for this region.
		cpID := "node-cp-" + rg.CloudRegion
		nodes = append(nodes, infraTopologyNode{
			ID:    cpID,
			Kind:  "node",
			Label: cpID,
			Status: status,
			Metadata: map[string]string{
				"role":   "control-plane",
				"sku":    rg.ControlPlaneSize,
				"region": rg.CloudRegion,
			},
		})
		edges = append(edges, infraTopologyEdge{From: clusterID, To: cpID, Relation: "contains"})

		for i := 0; i < rg.WorkerCount; i++ {
			nID := "node-w" + itoa(ri) + "-" + itoa(i) + "-" + rg.CloudRegion
			nodes = append(nodes, infraTopologyNode{
				ID:    nID,
				Kind:  "node",
				Label: "worker-" + itoa(i+1),
				Status: status,
				Metadata: map[string]string{
					"role":   "worker",
					"sku":    rg.WorkerSize,
					"region": rg.CloudRegion,
				},
			})
			edges = append(edges, infraTopologyEdge{From: clusterID, To: nID, Relation: "contains"})
		}
	}
	// Legacy singular path — when Regions is empty.
	if len(dep.Request.Regions) == 0 {
		cpID := "node-cp-" + dep.ID
		nodes = append(nodes, infraTopologyNode{
			ID:    cpID,
			Kind:  "node",
			Label: "control-plane",
			Status: status,
			Metadata: map[string]string{
				"role":   "control-plane",
				"sku":    dep.Request.ControlPlaneSize,
				"region": region,
			},
		})
		edges = append(edges, infraTopologyEdge{From: clusterID, To: cpID, Relation: "contains"})
		for i := 0; i < dep.Request.WorkerCount; i++ {
			nID := "node-w-" + itoa(i) + "-" + dep.ID
			nodes = append(nodes, infraTopologyNode{
				ID:    nID,
				Kind:  "node",
				Label: "worker-" + itoa(i+1),
				Status: status,
				Metadata: map[string]string{
					"role":   "worker",
					"sku":    dep.Request.WorkerSize,
					"region": region,
				},
			})
			edges = append(edges, infraTopologyEdge{From: clusterID, To: nID, Relation: "contains"})
		}
	}

	// Load balancer — surface when the OpenTofu module has reported its
	// public IP. Pre-LB-reconcile deployments will simply not have an LB
	// node on the canvas yet.
	if dep.Result != nil && dep.Result.LoadBalancerIP != "" {
		nodes = append(nodes, infraTopologyNode{
			ID:    lbID,
			Kind:  "lb",
			Label: "ingress-lb",
			Status: status,
			Metadata: map[string]string{
				"publicIP": dep.Result.LoadBalancerIP,
				"region":   region,
			},
		})
		edges = append(edges, infraTopologyEdge{From: clusterID, To: lbID, Relation: "attached-to"})
	}

	return infraTopologyResponse{Nodes: nodes, Edges: edges}
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
		NodeCount:    totalWorkerCount(dep.Request) + 1, // +1 for control-plane
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

	// DRGs and peerings require live cloud-API state — the OpenTofu
	// module records them but we don't surface them through the
	// catalyst-api persistence today. Per the file-header contract we
	// return well-shaped empty arrays rather than placeholder data.
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

// itoa avoids strconv just for the int→string formatting in id
// composition (handler is small, fmt.Sprintf would be fine but this
// keeps the hot path allocation-free).
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
