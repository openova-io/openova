// topology_loader.go — composes the unified TopologyResponse from the
// three available data sources:
//
//  1. The deployment record's Phase-0 OpenTofu outputs (provisioner.
//     Result + Request) — always available post-Phase-0; carries
//     control-plane IP, load-balancer IP, declared region SKUs, and
//     declared worker counts.
//
//  2. The live Sovereign cluster's dynamic informer cache — populated
//     by the helmwatch.Watcher attached to this deployment. Reads
//     vcluster.io/v1alpha1 VClusters when the operator is installed
//     plus core/v1 PVCs from the live cluster.
//
//  3. The Crossplane managed-resource list — surfaces XRCs the
//     catalyst-api itself wrote. Populated by the same dynamic
//     client; empty when no claims exist.
//
// Per docs/INVIOLABLE-PRINCIPLES.md (no placeholder data) every
// per-source query that fails or returns empty results in an empty
// slice on the response — never a synthesised row.
package infrastructure

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// LoaderInput — the deployment-shaped data the handler hands to the
// loader. The loader does not import the handler package (would
// create a cycle); the handler unwraps Deployment fields onto this
// struct and calls Load.
type LoaderInput struct {
	DeploymentID  string
	Status        string // canonical UI status
	SovereignFQDN string
	Provider      string
	Region        string
	Regions       []provisioner.RegionSpec
	WorkerCount   int
	WorkerSize    string
	CPSize        string
	Result        *provisioner.Result
	HetznerProjectID string

	// DynamicClient — Sovereign cluster dynamic client, built from
	// the persisted kubeconfig by the live-watcher. Nil when the
	// kubeconfig hasn't been postedback yet — the loader emits empty
	// arrays for live-source fields in that case.
	DynamicClient dynamic.Interface
}

// Load composes the unified TopologyResponse. The function is
// allocation-light by design — every slice is pre-sized off the
// request shape so the typical 1-region happy-path emits a single
// allocation per per-region child.
func Load(ctx context.Context, in LoaderInput) TopologyResponse {
	if ctx == nil {
		ctx = context.Background()
	}
	cloud := buildCloud(in)
	topology := buildTopology(ctx, in)
	storage := buildStorage(ctx, in)
	return TopologyResponse{
		Cloud:    cloud,
		Topology: topology,
		Storage:  storage,
	}
}

// buildCloud — one tenant per cloud provider. Today every Sovereign
// runs against exactly one Hetzner project; multi-cloud will add
// per-provider entries.
func buildCloud(in LoaderInput) []CloudTenant {
	provider := in.Provider
	if provider == "" {
		provider = "hetzner"
	}
	tenant := CloudTenant{
		ID:        "cloud-" + provider,
		Provider:  provider,
		Name:      provider,
		Status:    in.Status,
		ProjectID: in.HetznerProjectID,
	}
	return []CloudTenant{tenant}
}

// buildTopology — pattern + per-region build-out. One Region row per
// Regions[*] entry; legacy single-region path uses the singular
// Request fields.
func buildTopology(ctx context.Context, in LoaderInput) TopologyData {
	pattern := derivePattern(in)

	regions := []Region{}
	if len(in.Regions) > 0 {
		for _, rs := range in.Regions {
			regions = append(regions, buildRegion(ctx, in, rs))
		}
	} else if in.Region != "" {
		// Legacy singular path — pre-multi-region wizard payload.
		legacy := provisioner.RegionSpec{
			Provider:         in.Provider,
			CloudRegion:      in.Region,
			ControlPlaneSize: in.CPSize,
			WorkerSize:       in.WorkerSize,
			WorkerCount:      in.WorkerCount,
		}
		regions = append(regions, buildRegion(ctx, in, legacy))
	}
	return TopologyData{
		Pattern: pattern,
		Regions: regions,
	}
}

func derivePattern(in LoaderInput) string {
	switch {
	case len(in.Regions) > 1:
		return "multi-region"
	case len(in.Regions) == 1 && in.Regions[0].WorkerCount >= 3:
		return "ha-pair"
	case len(in.Regions) == 1:
		return "solo"
	case in.Region != "":
		return "solo"
	default:
		return "unknown"
	}
}

func buildRegion(ctx context.Context, in LoaderInput, rs provisioner.RegionSpec) Region {
	provider := rs.Provider
	if provider == "" {
		provider = "hetzner"
	}
	regionID := "region-" + rs.CloudRegion

	cluster := buildCluster(ctx, in, rs)
	networks := buildNetworks(ctx, in, rs)

	return Region{
		ID:             regionID,
		Name:           rs.CloudRegion,
		Provider:       provider,
		ProviderRegion: rs.CloudRegion,
		SkuCP:          rs.ControlPlaneSize,
		SkuWorker:      rs.WorkerSize,
		WorkerCount:    rs.WorkerCount,
		Status:         in.Status,
		Clusters:       []Cluster{cluster},
		Networks:       networks,
	}
}

func buildCluster(ctx context.Context, in LoaderInput, rs provisioner.RegionSpec) Cluster {
	clusterName := in.SovereignFQDN
	if clusterName == "" {
		dep := in.DeploymentID
		if len(dep) > 8 {
			dep = dep[:8]
		}
		clusterName = "cluster-" + dep
	}
	clusterID := "cluster-" + in.DeploymentID + "-" + rs.CloudRegion
	if rs.CloudRegion == "" {
		clusterID = "cluster-" + in.DeploymentID
	}

	nodes := buildNodes(in, rs)
	pools := buildNodePools(in, rs)
	lbs := buildLBs(in, rs)
	vclusters := loadVClusters(ctx, in)

	return Cluster{
		ID:            clusterID,
		Name:          clusterName,
		Version:       "v1.30",
		Status:        in.Status,
		NodeCount:     len(nodes),
		VClusters:     vclusters,
		LoadBalancers: lbs,
		NodePools:     pools,
		Nodes:         nodes,
	}
}

func buildNodes(in LoaderInput, rs provisioner.RegionSpec) []Node {
	out := []Node{}

	cpIP := ""
	if in.Result != nil {
		cpIP = in.Result.ControlPlaneIP
	}
	cpID := "node-cp-" + rs.CloudRegion
	if rs.CloudRegion == "" {
		cpID = "node-cp-" + in.DeploymentID
	}
	out = append(out, Node{
		ID:         cpID,
		Name:       "control-plane-" + rs.CloudRegion,
		SKU:        rs.ControlPlaneSize,
		Region:     rs.CloudRegion,
		Role:       "control-plane",
		IP:         cpIP,
		Status:     in.Status,
		NodePoolID: "pool-cp-" + rs.CloudRegion,
	})

	for i := 0; i < rs.WorkerCount; i++ {
		wID := "node-w-" + itoa(i) + "-" + rs.CloudRegion
		if rs.CloudRegion == "" {
			wID = "node-w-" + itoa(i) + "-" + in.DeploymentID
		}
		out = append(out, Node{
			ID:         wID,
			Name:       "worker-" + itoa(i+1) + "-" + rs.CloudRegion,
			SKU:        rs.WorkerSize,
			Region:     rs.CloudRegion,
			Role:       "worker",
			IP:         "",
			Status:     in.Status,
			NodePoolID: "pool-worker-" + rs.CloudRegion,
		})
	}
	return out
}

func buildNodePools(in LoaderInput, rs provisioner.RegionSpec) []NodePool {
	pools := []NodePool{
		{
			ID:          "pool-cp-" + rs.CloudRegion,
			Name:        "control-plane-" + rs.CloudRegion,
			Role:        "control-plane",
			SKU:         rs.ControlPlaneSize,
			Region:      rs.CloudRegion,
			DesiredSize: 1,
			CurrentSize: 1,
			Status:      in.Status,
		},
	}
	if rs.WorkerCount > 0 {
		pools = append(pools, NodePool{
			ID:          "pool-worker-" + rs.CloudRegion,
			Name:        "worker-" + rs.CloudRegion,
			Role:        "worker",
			SKU:         rs.WorkerSize,
			Region:      rs.CloudRegion,
			DesiredSize: rs.WorkerCount,
			CurrentSize: rs.WorkerCount,
			Status:      in.Status,
		})
	}
	return pools
}

func buildLBs(in LoaderInput, rs provisioner.RegionSpec) []LoadBalancer {
	if in.Result == nil || in.Result.LoadBalancerIP == "" {
		return []LoadBalancer{}
	}
	name := in.SovereignFQDN
	if name == "" {
		name = "ingress-lb"
	}
	return []LoadBalancer{{
		ID:           "lb-" + in.DeploymentID,
		Name:         name,
		PublicIP:     in.Result.LoadBalancerIP,
		Ports:        "80,443,6443",
		TargetHealth: "—",
		Region:       rs.CloudRegion,
		Status:       in.Status,
	}}
}

func buildNetworks(ctx context.Context, in LoaderInput, rs provisioner.RegionSpec) []Network {
	// Per-region VPC stamped by the Phase-0 module; follow-on
	// Day-2 PeeringClaim XRCs bind regions together. Today we
	// surface one Network per region with empty Peerings until the
	// Crossplane Composition lands and Peering objects exist.
	netID := "net-" + rs.CloudRegion + "-" + in.DeploymentID
	if rs.CloudRegion == "" {
		netID = "net-" + in.DeploymentID
	}
	return []Network{{
		ID:       netID,
		Name:     "vpc-" + rs.CloudRegion,
		CIDR:     "",
		Region:   rs.CloudRegion,
		Peerings: loadPeerings(ctx, in, rs),
		Firewall: nil,
		Status:   in.Status,
	}}
}

// loadVClusters — query the Sovereign cluster's vcluster.io/v1alpha1
// CRs. Returns an empty slice when the operator isn't installed
// (Crd doesn't exist) or when no vclusters have been provisioned.
//
// The recover guard tolerates fake-client panics in unit tests
// (k8s.io/client-go/dynamic/fake panics on unregistered list-kinds);
// production never hits this path because the real apiserver
// returns 404 instead of panicking.
func loadVClusters(ctx context.Context, in LoaderInput) (out []VCluster) {
	out = []VCluster{}
	defer func() {
		if r := recover(); r != nil {
			out = []VCluster{}
		}
	}()
	if in.DynamicClient == nil {
		return out
	}
	gvr := schema.GroupVersionResource{
		Group:    "vcluster.io",
		Version:  "v1alpha1",
		Resource: "vclusters",
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := in.DynamicClient.Resource(gvr).Namespace("").List(cctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return out
	}
	for _, item := range list.Items {
		role := vclusterRole(item.GetLabels())
		out = append(out, VCluster{
			ID:        "vcluster-" + item.GetNamespace() + "-" + item.GetName(),
			Name:      item.GetName(),
			Namespace: item.GetNamespace(),
			Role:      role,
			Status:    statusFromUnstructured(item.Object),
		})
	}
	return out
}

func vclusterRole(labels map[string]string) string {
	if v, ok := labels["catalyst.openova.io/role"]; ok && v != "" {
		return v
	}
	if v, ok := labels["building-block"]; ok && v != "" {
		return v
	}
	return "other"
}

// loadPeerings — query Crossplane PeeringClaim XRCs scoped to this
// deployment via the LabelDeploymentID selector.
//
// The recover guard tolerates fake-client panics in unit tests as
// described on loadVClusters.
func loadPeerings(ctx context.Context, in LoaderInput, rs provisioner.RegionSpec) (out []Peering) {
	out = []Peering{}
	defer func() {
		if r := recover(); r != nil {
			out = []Peering{}
		}
	}()
	if in.DynamicClient == nil {
		return out
	}
	gvr := gvrForKind(KindPeeringClaim)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := in.DynamicClient.Resource(gvr).Namespace(XRCNamespace).List(cctx, metav1.ListOptions{
		LabelSelector: LabelDeploymentID + "=" + in.DeploymentID,
	})
	if err != nil || list == nil {
		return out
	}
	for _, item := range list.Items {
		spec, _, _ := nestedMap(item.Object, "spec")
		out = append(out, Peering{
			ID:      string(item.GetUID()),
			Name:    item.GetName(),
			VPCPair: stringField(spec, "vpcPair"),
			Subnets: stringField(spec, "subnets"),
			Status:  statusFromUnstructured(item.Object),
		})
	}
	return out
}

// buildStorage — PVCs from the live cluster + buckets/volumes from
// the Crossplane managed-resource list. Empty slices when sources
// aren't reachable.
func buildStorage(ctx context.Context, in LoaderInput) StorageData {
	return StorageData{
		PVCs:    loadPVCs(ctx, in),
		Buckets: []Bucket{},
		Volumes: []Volume{},
	}
}

func loadPVCs(ctx context.Context, in LoaderInput) (out []PVC) {
	out = []PVC{}
	defer func() {
		if r := recover(); r != nil {
			out = []PVC{}
		}
	}()
	if in.DynamicClient == nil {
		return out
	}
	gvr := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "persistentvolumeclaims",
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := in.DynamicClient.Resource(gvr).Namespace("").List(cctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return out
	}
	for _, item := range list.Items {
		spec, _, _ := nestedMap(item.Object, "spec")
		status, _, _ := nestedMap(item.Object, "status")
		capacity := stringField(stringMapField(status, "capacity"), "storage")
		out = append(out, PVC{
			ID:           string(item.GetUID()),
			Name:         item.GetName(),
			Namespace:    item.GetNamespace(),
			Capacity:     capacity,
			Used:         "",
			StorageClass: stringField(spec, "storageClassName"),
			Status:       stringField(status, "phase"),
		})
	}
	return out
}

// CascadeFor — given a delete target (kind + id) and the current
// topology, lists the child resources that would be reaped. Used by
// the DELETE handler to populate the 202 response's Cascade slice.
func CascadeFor(kind, id string, topology TopologyResponse) []CascadeImpact {
	out := []CascadeImpact{}
	switch strings.ToLower(kind) {
	case "region":
		for _, rg := range topology.Topology.Regions {
			if rg.ID != id {
				continue
			}
			for _, c := range rg.Clusters {
				out = append(out, CascadeImpact{
					Kind: "cluster", ID: c.ID, Name: c.Name,
					Note: "cluster will drain + be reaped",
				})
				for _, np := range c.NodePools {
					out = append(out, CascadeImpact{
						Kind: "nodePool", ID: np.ID, Name: np.Name,
						Note: "node pool will be deleted",
					})
				}
				for _, n := range c.Nodes {
					out = append(out, CascadeImpact{
						Kind: "node", ID: n.ID, Name: n.Name,
						Note: "workloads will be drained",
					})
				}
				for _, lb := range c.LoadBalancers {
					out = append(out, CascadeImpact{
						Kind: "lb", ID: lb.ID, Name: lb.Name,
						Note: "load balancer will be released",
					})
				}
			}
			for _, n := range rg.Networks {
				out = append(out, CascadeImpact{
					Kind: "network", ID: n.ID, Name: n.Name,
					Note: "VPC will be released; peerings disconnected",
				})
				for _, p := range n.Peerings {
					out = append(out, CascadeImpact{
						Kind: "peering", ID: p.ID, Name: p.Name,
						Note: "peering will be torn down",
					})
				}
			}
		}
	case "cluster":
		for _, rg := range topology.Topology.Regions {
			for _, c := range rg.Clusters {
				if c.ID != id {
					continue
				}
				for _, np := range c.NodePools {
					out = append(out, CascadeImpact{Kind: "nodePool", ID: np.ID, Name: np.Name})
				}
				for _, n := range c.Nodes {
					out = append(out, CascadeImpact{Kind: "node", ID: n.ID, Name: n.Name})
				}
				for _, lb := range c.LoadBalancers {
					out = append(out, CascadeImpact{Kind: "lb", ID: lb.ID, Name: lb.Name})
				}
			}
		}
	case "nodepool", "pool":
		for _, rg := range topology.Topology.Regions {
			for _, c := range rg.Clusters {
				for _, np := range c.NodePools {
					if np.ID != id {
						continue
					}
					for _, n := range c.Nodes {
						if n.NodePoolID == np.ID {
							out = append(out, CascadeImpact{Kind: "node", ID: n.ID, Name: n.Name,
								Note: "node will be drained + cordoned"})
						}
					}
				}
			}
		}
	}
	// Always emit at least one descriptor so the FE confirm dialog
	// can render a row even when no children are observable.
	if len(out) == 0 {
		out = append(out, CascadeImpact{
			Kind: kind,
			ID:   id,
			Name: id,
			Note: "no observable child resources — proceeding will reap the underlying cloud resources",
		})
	}
	return out
}

/* ─── Helpers (no client-go mutation here; reads only) ─── */

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

func nestedMap(obj map[string]any, path ...string) (map[string]any, bool, error) {
	cur := obj
	for _, p := range path {
		v, ok := cur[p]
		if !ok {
			return nil, false, nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur = m
	}
	return cur, true, nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func stringMapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	return nil
}

func statusFromUnstructured(obj map[string]any) string {
	status, _, _ := nestedMap(obj, "status")
	if status == nil {
		return "unknown"
	}
	if phase := stringField(status, "phase"); phase != "" {
		return phase
	}
	if cs, ok := status["conditions"].([]any); ok {
		for _, c := range cs {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if stringField(cm, "type") == "Ready" {
				if stringField(cm, "status") == "True" {
					return "healthy"
				}
				return strings.ToLower(stringField(cm, "reason"))
			}
		}
	}
	return "unknown"
}
