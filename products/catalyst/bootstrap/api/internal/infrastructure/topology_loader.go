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

// K8sCacheReader — narrow read-only view of the catalyst-api k8sCache
// Factory the loader uses to fan out across multi-region clusters in
// the chroot post-cutover fallback (G14, Refs #2551). Defined as an
// interface so the loader doesn't import the k8scache package (would
// create a cycle: handler→infrastructure→k8scache→handler-types).
type K8sCacheReader interface {
	Clusters() []string
	DynamicClientFor(clusterID string) (dynamic.Interface, error)
}

// LoaderInput — the deployment-shaped data the handler hands to the
// loader. The loader does not import the handler package (would
// create a cycle); the handler unwraps Deployment fields onto this
// struct and calls Load.
type LoaderInput struct {
	DeploymentID     string
	Status           string // canonical UI status
	SovereignFQDN    string
	Provider         string
	Region           string
	Regions          []provisioner.RegionSpec
	WorkerCount      int
	WorkerSize       string
	CPSize           string
	Result           *provisioner.Result
	HetznerProjectID string

	// DynamicClient — Sovereign cluster dynamic client, built from
	// the persisted kubeconfig by the live-watcher. Nil when the
	// kubeconfig hasn't been postedback yet — the loader emits empty
	// arrays for live-source fields in that case.
	DynamicClient dynamic.Interface

	// K8sCache — multi-region fan-out source for the chroot post-
	// cutover fallback (G14, Refs #2551). When set + the chroot has
	// >1 registered cluster (primary + secondary kubeconfigs both
	// loaded), the loader emits one Region per cluster instead of
	// the single in-cluster region. Nil → falls back to DynamicClient
	// single-cluster behaviour (legacy path).
	K8sCache K8sCacheReader
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
	// #5515: pattern is derived AFTER the regions are built — it needs their
	// liveness (Clusters), which does not exist at function entry. The call
	// previously sat here, above the build loop, which is structurally why it
	// could only ever see declared specs.
	regions := []Region{}
	if len(in.Regions) > 0 {
		for i, rs := range in.Regions {
			// Multi-region live-source fix (fix/console-topology-both-
			// regions): the mothership path (Request.Regions populated)
			// previously read EVERY region's live data — vClusters,
			// peerings, XRCs — through the single primary kubeconfig
			// (in.DynamicClient = Result.KubeconfigPath = <depID>.yaml).
			// The secondary region row therefore mirrored the primary's
			// live contents (or showed empty), so the operator saw an
			// effectively single-region topology on a 2-region Sovereign.
			//
			// The k8sCache already loads BOTH the primary <depID>.yaml AND
			// each secondary <depID>-<regionKey>.yaml as distinct clusters
			// (k8scache.LoadClustersFromDir, filename-stem id). Resolve the
			// per-region dynamic client here and build each Region against
			// its OWN cluster. Nil-tolerant: when no k8sCache cluster
			// matches the declared region, perRegionIn falls back to the
			// primary in.DynamicClient — identical to the prior behaviour.
			perRegionIn := in
			if dc := perRegionDynamicClient(in, rs); dc != nil {
				perRegionIn.DynamicClient = dc
			} else if i > 0 && in.K8sCache != nil && len(in.K8sCache.Clusters()) > 0 {
				// Secondary declared region (Regions[0] is primary) with
				// NO live kubeconfig registered in a POPULATED k8sCache =
				// standby-region-absent (#4811). (When K8sCache is nil /
				// empty — legacy single-cluster mode — the primary-client
				// fallback below is still correct; only guard the
				// multi-cluster case.) Falling back to the primary client
				// here made
				// buildRegion read the primary's Nodes and emit THIS
				// region's DECLARED worker count as present — the #4814
				// fabrication ("Region 2/2 · WorkerNode 24/24" on a
				// region-b-absent 2-region prov, 6 live nodes). Emit a
				// degraded, live-empty region so the console counts stay
				// honest (0 live vs declared → renders as degraded/partial).
				regions = append(regions, buildAbsentRegion(rs))
				continue
			}
			regions = append(regions, buildRegion(ctx, perRegionIn, rs))
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
	} else if in.K8sCache != nil && len(in.K8sCache.Clusters()) > 0 {
		// Chroot post-cutover path with multi-cluster k8sCache (G14,
		// Refs #2551). When the chroot has BOTH primary + secondary
		// kubeconfigs registered (via the cloud-init secondary-PUT-
		// back posting to /api/v1/sovereign/secondary-kubeconfig +
		// the k8sCache rescan loop), fan out across every cluster so
		// the operator sees one Region per region. Previously this
		// path called buildRegionFromLiveNodes ONCE against the
		// in-cluster client, hiding the secondary region entirely.
		for _, cid := range in.K8sCache.Clusters() {
			dc, err := in.K8sCache.DynamicClientFor(cid)
			if err != nil || dc == nil {
				continue
			}
			perClusterIn := in
			perClusterIn.DynamicClient = dc
			// Hint the region by parsing the secondary suffix off the
			// k8sCache cluster ID. Format: "<depID>" for primary,
			// "<depID>-<region>" for secondaries. The hint flows
			// through to buildRegionFromLiveNodes' regionName when no
			// Node carries an explicit region label.
			if strings.HasPrefix(cid, in.DeploymentID+"-") {
				perClusterIn.Region = strings.TrimPrefix(cid, in.DeploymentID+"-")
			} else if cid != in.DeploymentID {
				perClusterIn.Region = cid
			}
			if rs, ok := buildRegionFromLiveNodes(ctx, perClusterIn); ok {
				regions = append(regions, rs)
			}
		}
	} else if in.DynamicClient != nil && in.SovereignFQDN != "" {
		// Single-cluster chroot path: no k8sCache (test / legacy) but
		// a DynamicClient is wired. Probe the live cluster's Nodes
		// via the dynamic client, group by `node.kubernetes.io/
		// instance-type`, and emit one Region with one Cluster
		// carrying all real Nodes + derived NodePools.
		if rs, ok := buildRegionFromLiveNodes(ctx, in); ok {
			regions = append(regions, rs)
		}
	}
	pattern := derivePattern(in, regions)
	// Pre-existing correction, retained: a build path that produced regions but
	// declared nothing (chroot / live-nodes paths) must not report "unknown".
	if len(regions) > 0 && pattern == "unknown" {
		pattern = "solo"
	}

	return TopologyData{
		Pattern: pattern,
		Regions: regions,
	}
}

// perRegionDynamicClient resolves the live dynamic.Interface for a
// single declared region off the k8sCache, so the mothership topology
// path (Request.Regions populated) reads each region's vClusters/
// peerings/XRCs from that region's OWN cluster instead of always the
// primary kubeconfig.
//
// Cluster-id convention (k8scache.LoadClustersFromDir filename stems +
// handler.HandleSovereignSecondaryKubeconfig writer):
//
//	primary    → "<depID>"                  (file <depID>.yaml)
//	secondary  → "<depID>-<regionKey>"       (file <depID>-<regionKey>.yaml)
//
// The declared RegionSpec.CloudRegion (e.g. "me-east-215-b") is the
// PREFIX of the materialised regionKey (e.g. "me-east-215-b-1"), so we
// match the primary region (Regions[0] / in.Region) to the bare depID
// cluster and every other region to the registered cluster whose id is
// "<depID>-<CloudRegion>" or "<depID>-<CloudRegion>-*".
//
// Returns nil when: k8sCache is unset (tests / legacy), the declared
// region is the primary (caller keeps in.DynamicClient = primary), or
// no registered cluster matches. nil → caller falls back to the
// primary in.DynamicClient, preserving the pre-fix behaviour exactly.
func perRegionDynamicClient(in LoaderInput, rs provisioner.RegionSpec) dynamic.Interface {
	if in.K8sCache == nil || in.DeploymentID == "" {
		return nil
	}
	region := strings.TrimSpace(rs.CloudRegion)
	if region == "" {
		return nil
	}
	// The primary region resolves to the bare-depID cluster, which IS
	// in.DynamicClient already; returning nil lets the caller keep it
	// (and avoids a redundant client fetch).
	primaryRegion := strings.TrimSpace(in.Region)
	if primaryRegion == "" && len(in.Regions) > 0 {
		primaryRegion = strings.TrimSpace(in.Regions[0].CloudRegion)
	}
	if region == primaryRegion {
		return nil
	}

	want := in.DeploymentID + "-" + region
	for _, cid := range in.K8sCache.Clusters() {
		// Exact "<depID>-<region>" or suffixed "<depID>-<region>-N"
		// (materialisation index, e.g. "...-me-east-215-b-1").
		if cid == want || strings.HasPrefix(cid, want+"-") {
			return clientForCluster(in, cid)
		}
	}

	// #5274 fallback — the declared CloudRegion can DIVERGE from the
	// kubeconfig-id region token. The chroot's region list comes from
	// SOVEREIGN_REGIONS_JSON (bare cloud code, e.g. "me-east-215-b" — the
	// exact/prefix match above) OR, when that ConfigMap key isn't wired,
	// from the SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION fallback
	// (handler.chrootRegionsFromPrimaryReplicaEnv), which carries the FULL
	// 4-segment cluster slug (e.g. "hw-me-east-215-b-rtz-prod", live hw101).
	// The secondary kubeconfig is registered under the bare cloud code
	// ("<depID>-me-east-215-b-1"), so "<depID>-hw-me-east-215-b-rtz-prod"
	// never prefix-matches → region-b was rendered as buildAbsentRegion
	// (Clusters: nil) → the Cloud graph showed "Region 2/2 · Cluster 1/1"
	// on a fully-live 2-region Sovereign. Both strings still contain the
	// same cloud-region code as a contiguous hyphen-delimited token run, so
	// match on that. No fabrication: this only resolves a client for a
	// secondary cluster that is GENUINELY registered in the k8sCache; a
	// truly-absent region still matches nothing and stays buildAbsentRegion.
	for _, cid := range in.K8sCache.Clusters() {
		if cid == in.DeploymentID || !strings.HasPrefix(cid, in.DeploymentID+"-") {
			continue // primary / chroot-self / a different deployment's cluster
		}
		idRegion := trimMaterialisationIndex(strings.TrimPrefix(cid, in.DeploymentID+"-"))
		if regionTokensOverlap(region, idRegion) {
			return clientForCluster(in, cid)
		}
	}
	return nil
}

// clientForCluster fetches the k8sCache dynamic client for cid, returning
// nil on error / absence so callers fall back to buildAbsentRegion (or the
// primary client) rather than dereferencing a nil interface.
func clientForCluster(in LoaderInput, cid string) dynamic.Interface {
	dc, err := in.K8sCache.DynamicClientFor(cid)
	if err != nil || dc == nil {
		return nil
	}
	return dc
}

// trimMaterialisationIndex strips a trailing "-<digits>" materialisation
// index off a region key ("me-east-215-b-1" → "me-east-215-b"). Keys with
// no numeric tail are returned unchanged, so a bare "me-east-215-b" is a
// no-op.
func trimMaterialisationIndex(s string) string {
	i := strings.LastIndex(s, "-")
	if i <= 0 || i == len(s)-1 {
		return s
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s
		}
	}
	return s[:i]
}

// regionTokensOverlap reports whether the hyphen-delimited token run of one
// region string appears as a contiguous subsequence of the other's (either
// direction). It lets a bare cloud code (me-east-215-b) match a full cluster
// slug (hw-me-east-215-b-rtz-prod) that embeds it, WITHOUT the false
// positives a raw substring test would allow ("me-east-215-b" vs
// "me-east-215-ba" differ as tokens). Cloud-region codes are unique, so the
// embedded code resolves each declared region to exactly one cluster.
func regionTokensOverlap(a, b string) bool {
	at := strings.Split(a, "-")
	bt := strings.Split(b, "-")
	return tokenSubsequence(at, bt) || tokenSubsequence(bt, at)
}

// tokenSubsequence reports whether needle appears as a contiguous run inside
// haystack. Empty needle never matches (guards against an empty region
// string pairing with everything).
func tokenSubsequence(needle, haystack []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		match := true
		for j := range needle {
			if haystack[start+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// nodeGVR — schema reference for core/v1 Node listing via dynamic client.
var nodeGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}

// buildRegionFromLiveNodes synthesises a Region from the live cluster's
// Node list when the deployment record has no declared Regions (the
// chroot post-cutover case). Groups Nodes by SKU label to derive
// NodePools; emits one Cluster carrying all real Nodes. Returns
// (zero, false) when the Node list is empty or unreachable.
func buildRegionFromLiveNodes(ctx context.Context, in LoaderInput) (Region, bool) {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := in.DynamicClient.Resource(nodeGVR).List(listCtx, metav1.ListOptions{})
	if err != nil || list == nil || len(list.Items) == 0 {
		return Region{}, false
	}

	// Pick a region from a Node label if any node carries one; else
	// fall back to the in.Region hint (set by the k8sCache fan-out
	// caller per-cluster), then the SovereignFQDN as last resort.
	// `openova.io/region` is the catalyst-canonical label written by
	// cloud-init (e.g., "hw-me-east-215-a-rtz-prod") — preferred over
	// the upstream k8s topology labels because it carries the full
	// catalyst region slug.
	regionName := ""
	for _, n := range list.Items {
		for _, k := range []string{"openova.io/region", "topology.kubernetes.io/region", "failure-domain.beta.kubernetes.io/region"} {
			if v, ok := n.GetLabels()[k]; ok && v != "" {
				regionName = v
				break
			}
		}
		if regionName != "" {
			break
		}
	}
	if regionName == "" {
		regionName = in.Region
	}
	if regionName == "" {
		regionName = in.SovereignFQDN
	}

	type poolKey struct {
		role string // control-plane or worker
		sku  string
	}
	poolNodes := map[poolKey][]Node{}
	cpSKU := ""
	workerSKU := ""
	cpCount, workerCount := 0, 0
	for _, n := range list.Items {
		labels := n.GetLabels()
		role := "worker"
		if _, ok := labels["node-role.kubernetes.io/control-plane"]; ok {
			role = "control-plane"
		} else if _, ok := labels["node-role.kubernetes.io/master"]; ok {
			role = "control-plane"
		}
		sku := labels["node.kubernetes.io/instance-type"]
		if sku == "" {
			sku = labels["beta.kubernetes.io/instance-type"]
		}
		// Read the first InternalIP for display.
		ip := ""
		if addrs, found, _ := unstructuredAddresses(n.Object); found {
			for _, a := range addrs {
				m, ok := a.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := m["type"].(string); t == "InternalIP" {
					ip, _ = m["address"].(string)
					break
				}
			}
		}
		key := poolKey{role: role, sku: sku}
		// Use the bare K8s node name as the topology node id so the
		// architecture-graph adapter's WorkerNode composite id
		// matches the k8sAdapter's `WorkerNode:<name>` exactly. With
		// the legacy "node-" prefix the IDs diverged and mergeGraphs
		// couldn't dedupe → 8 WorkerNodes rendered for 4 real Nodes.
		// Caught on omantel.biz 2026-05-07.
		nodeID := n.GetName()
		statusReady := nodeReadyStatus(n.Object)
		nodeStatus := in.Status
		if statusReady == "True" {
			nodeStatus = "healthy"
		} else if statusReady == "False" {
			nodeStatus = "degraded"
		}
		poolNodes[key] = append(poolNodes[key], Node{
			ID:         nodeID,
			Name:       n.GetName(),
			SKU:        sku,
			Region:     regionName,
			Role:       role,
			IP:         ip,
			Status:     nodeStatus,
			NodePoolID: "pool-" + role + "-" + regionName,
		})
		if role == "control-plane" {
			cpCount++
			if cpSKU == "" {
				cpSKU = sku
			}
		} else {
			workerCount++
			if workerSKU == "" {
				workerSKU = sku
			}
		}
	}

	allNodes := make([]Node, 0, len(list.Items))
	pools := make([]NodePool, 0, len(poolNodes))
	for k, ns := range poolNodes {
		allNodes = append(allNodes, ns...)
		pools = append(pools, NodePool{
			ID:          "pool-" + k.role + "-" + regionName,
			Name:        k.role + "-" + regionName,
			Role:        k.role,
			SKU:         k.sku,
			Region:      regionName,
			DesiredSize: len(ns),
			CurrentSize: len(ns),
			Status:      in.Status,
		})
	}

	clusterName := in.SovereignFQDN
	if clusterName == "" {
		clusterName = "cluster-" + in.DeploymentID
	}
	// Per-region clusterID: when the caller hinted a Region (k8sCache
	// fan-out, G14 Refs #2551), suffix it so primary + secondary
	// rows have distinct IDs. The mothership topology path's
	// buildRegion does the same via `cluster-<depID>-<cloudRegion>`.
	clusterID := "cluster-" + in.DeploymentID
	if in.Region != "" {
		clusterID = "cluster-" + in.DeploymentID + "-" + in.Region
	}
	// Provider selection: prefer the wizard-declared provider; if the
	// chroot's synthesised deployment has no Provider (G14, Refs
	// #2551), sniff the first Node's provider hints before defaulting.
	// k3s sets node.spec.providerID="<provider>://<id>" (e.g.,
	// "huawei://..." / "hcloud://...") + sometimes a label like
	// "topology.kubernetes.io/cloud-provider". Default "hetzner" is
	// kept only as the absolute last resort.
	//
	// Resolved BEFORE the cluster is built so frontDoorLBs can pick the
	// correct FrontDoorKind (gateway-eip on Huawei vs cloud-lb elsewhere).
	provider := in.Provider
	if provider == "" {
		for _, n := range list.Items {
			if v, ok := n.GetLabels()["topology.kubernetes.io/cloud-provider"]; ok && v != "" {
				provider = v
				break
			}
			if spec, ok := n.Object["spec"].(map[string]any); ok {
				if pid, _ := spec["providerID"].(string); pid != "" {
					if idx := strings.Index(pid, "://"); idx > 0 {
						scheme := pid[:idx]
						switch scheme {
						case "hcloud":
							provider = "hetzner"
						default:
							provider = scheme
						}
						break
					}
				}
			}
		}
	}
	if provider == "" {
		provider = "hetzner"
	}
	// Carry the resolved provider so frontDoorLBs reads the real value
	// even when the synthesised deployment had no declared Provider.
	lbIn := in
	lbIn.Provider = provider
	cluster := Cluster{
		ID:        clusterID,
		Name:      clusterName,
		Version:   "v1.31",
		Status:    in.Status,
		NodeCount: len(allNodes),
		VClusters: loadVClusters(ctx, in),
		// Refs #3998: source the front-door LB from the deployment record
		// (Result.LoadBalancerIP), NOT the empty XRC layer — so the live
		// chroot path (the one every real Sovereign hits, since catalyst-api
		// runs in-cluster) shows the real EIP/Gateway or cloud LB instead
		// of 0/0.
		LoadBalancers: frontDoorLBs(lbIn, regionName),
		NodePools:     pools,
		Nodes:         allNodes,
	}
	region := Region{
		ID:             "region-" + regionName,
		Name:           regionName,
		Provider:       provider,
		ProviderRegion: regionName,
		SkuCP:          cpSKU,
		SkuWorker:      workerSKU,
		WorkerCount:    workerCount,
		Status:         in.Status,
		Clusters:       []Cluster{cluster},
		Networks:       []Network{},
	}
	_ = cpCount
	return region, true
}

// unstructuredAddresses returns status.addresses[] from a Node's
// unstructured object payload.
func unstructuredAddresses(obj map[string]any) ([]any, bool, error) {
	st, ok := obj["status"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	addrs, ok := st["addresses"].([]any)
	if !ok {
		return nil, false, nil
	}
	return addrs, true, nil
}

// nodeReadyStatus returns the Ready condition status string ("True" /
// "False" / "Unknown") from the Node's status.conditions[]. Returns
// empty string when not found.
func nodeReadyStatus(obj map[string]any) string {
	st, ok := obj["status"].(map[string]any)
	if !ok {
		return ""
	}
	conds, ok := st["conditions"].([]any)
	if !ok {
		return ""
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Ready" {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

// derivePattern — the topology pattern the console renders.
//
// #5515: this MUST be derived from the BUILT regions, which carry liveness via
// their Clusters slice, and NOT from in.Regions (the DECLARED wizard specs). A
// declared secondary region that never converged is emitted by buildAbsentRegion
// with Clusters:nil (#4811/#4814); counting declared specs therefore reported
// "multi-region" for a deployment with exactly ONE live region — the console
// presented a DR topology that did not exist.
//
// The previous signature took only LoaderInput, so liveness was not merely
// mis-weighted, it was UNREACHABLE — no argument carried it. Reordering the
// switch could not have fixed it.
//
// In-flight guard: while a fresh prov is still converging, NO region has clusters
// yet. Counting live regions alone would report "unknown" for every provision in
// progress — a regression of the normal path in the name of fixing the degraded
// one. So when nothing has converged, fall back to the DECLARED shape: reporting
// intent is honest when there is no live state to contradict it. The defect this
// fixes is specifically the MIXED case — some regions live, others declared-dead —
// where live state exists and disagrees with the declaration.
func derivePattern(in LoaderInput, built []Region) string {
	live := 0
	liveWorkers := 0
	for _, r := range built {
		if len(r.Clusters) == 0 {
			continue
		}
		if live == 0 {
			liveWorkers = r.WorkerCount
		}
		live++
	}

	// Nothing converged yet (fresh prov in flight, or a legacy path that builds
	// no clusters) — report the declared shape rather than "unknown".
	if live == 0 {
		switch {
		case len(in.Regions) > 1:
			return "multi-region"
		case len(in.Regions) == 1 && in.Regions[0].WorkerCount >= 3:
			return "ha-pair"
		case len(in.Regions) == 1, in.Region != "":
			return "solo"
		default:
			return "unknown"
		}
	}

	switch {
	case live > 1:
		return "multi-region"
	case liveWorkers >= 3:
		return "ha-pair"
	default:
		return "solo"
	}
}

// buildAbsentRegion renders a DECLARED secondary region that has no live
// kubeconfig registered in the k8sCache (standby-region-absent — the #4811
// gap on a 2-region prov where region-b never converged). It emits a
// degraded, live-empty Region (no Clusters → 0 live nodes) carrying only
// the declared SKUs/WorkerCount, so the console counts reflect reality
// (0 live vs declared) instead of falling back to the primary region's
// client and rendering this region's declared worker count as present
// (#4814). The frontend derives the live node count from
// len(Clusters[*].Nodes), so an empty Clusters slice yields an honest
// "0 live / N declared" and a degraded region badge.
func buildAbsentRegion(rs provisioner.RegionSpec) Region {
	return Region{
		ID:             "region-" + rs.CloudRegion,
		Name:           rs.CloudRegion,
		Provider:       rs.Provider,
		ProviderRegion: rs.CloudRegion,
		SkuCP:          rs.ControlPlaneSize,
		SkuWorker:      rs.WorkerSize,
		WorkerCount:    rs.WorkerCount, // declared total; live=0 (empty Clusters)
		Status:         "degraded",
		Clusters:       nil,
		Networks:       nil,
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
	return frontDoorLBs(in, rs.CloudRegion)
}

// frontDoorLBs composes the cloud front-door LoadBalancer row from the
// DEPLOYMENT RECORD (Result.LoadBalancerIP) — the source of truth that is
// always populated post-Phase-0 — rather than the empty Crossplane XRC
// layer (Refs #3998). On Hetzner the IP is a real `hcloud_load_balancer`;
// on Huawei it is an EIP whose ELB targets node:443/:80 — the cilium-envoy
// hostNetwork host port (§854 / #4765, NOT a nodePort)
// (FrontDoorKind distinguishes them so the UI can explain the datapath).
//
// Both the declared (mothership) path and the live chroot path call this,
// so the LB page renders the real front door on EVERY Sovereign instead
// of 0/0. Returns an empty slice (never a synthesised row) when the
// record has no LoadBalancerIP yet, per the no-placeholder principle.
func frontDoorLBs(in LoaderInput, region string) []LoadBalancer {
	if in.Result == nil || in.Result.LoadBalancerIP == "" {
		return []LoadBalancer{}
	}
	name := in.SovereignFQDN
	if name == "" {
		name = "ingress-lb"
	}
	// The platform's standard front door terminates HTTPS (443) + HTTP
	// (80) at the Cilium Gateway and exposes the k3s/k8s apiserver on
	// 6443. These are the listeners every Sovereign provisions; sourced
	// from the deploy topology, not fabricated per-cluster runtime state.
	listeners := []LoadBalancerListener{
		{Port: 80, Protocol: "tcp"},
		{Port: 443, Protocol: "tcp"},
		{Port: 6443, Protocol: "tcp"},
	}
	// FrontDoorKind: on Huawei the gateway ELB targets node:443/:80 — the
	// cilium-envoy hostNetwork host port (§854 / #4765, NOT a nodePort) —
	// so model it as gateway-eip; every other provider provisions a real
	// cloud LB.
	frontDoor := "cloud-lb"
	if strings.EqualFold(in.Provider, "huawei") {
		frontDoor = "gateway-eip"
	}
	return []LoadBalancer{{
		ID:            "lb-" + in.DeploymentID,
		Name:          name,
		PublicIP:      in.Result.LoadBalancerIP,
		Listeners:     listeners,
		Targets:       []LoadBalancerTarget{},
		Ports:         "80,443,6443",
		TargetHealth:  "—",
		Region:        region,
		Status:        in.Status,
		FrontDoorKind: frontDoor,
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

	// First-source: vcluster.io/v1alpha1 VCluster CRs. Returned when the
	// vcluster-platform / loft-platform operator is installed (provides
	// a VCluster CRD that aggregates pod+service+secret behind a single
	// resource). Our bootstrap topology ships loft-sh/vcluster as a
	// plain Helm chart (StatefulSet+Service, no CRD), so the CR list
	// is empty on a converged Sovereign.
	gvr := schema.GroupVersionResource{
		Group:    "vcluster.io",
		Version:  "v1alpha1",
		Resource: "vclusters",
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	list, err := in.DynamicClient.Resource(gvr).Namespace("").List(cctx, metav1.ListOptions{})
	cancel()
	if err == nil && list != nil {
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
	}
	if len(out) > 0 {
		return out
	}

	// Fallback: enumerate Namespaces carrying our canonical role label
	// `catalyst.openova.io/vcluster-role`. bp-{mgmt,dmz,rtz}-vcluster
	// stamps this on the host namespace; the loft-sh/vcluster
	// StatefulSet renders pods inside that namespace. One Namespace
	// per vCluster instance — name = label value (mgmt/dmz/rtz).
	// Caught on t129 2026-05-16: canvas chip showed `vCluster 0/0`
	// despite vCluster Pods Running because the CR-first path returned
	// empty (no vcluster.io CRD) and there was no fallback. Refs DoD D15.
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	nsCtx, nsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer nsCancel()
	nsList, nsErr := in.DynamicClient.Resource(nsGVR).List(nsCtx, metav1.ListOptions{
		LabelSelector: "catalyst.openova.io/vcluster-role",
	})
	if nsErr != nil || nsList == nil {
		return out
	}
	for _, ns := range nsList.Items {
		name := ns.GetName()
		role := ns.GetLabels()["catalyst.openova.io/vcluster-role"]
		if role == "" {
			role = vclusterRole(ns.GetLabels())
		}
		out = append(out, VCluster{
			ID:        "vcluster-" + name,
			Name:      name,
			Namespace: name,
			Role:      role,
			Status:    "healthy",
		})
	}

	// Third-source: loft-sh/vcluster StatefulSets (label app=vcluster). The
	// per-Org vcluster (bp-rtz-vcluster / org-controller) renders a StatefulSet
	// named `vcluster` in the Org boundary ns WITHOUT a vcluster.io/.com CR AND
	// without the catalyst.openova.io/vcluster-role ns label, so BOTH sources
	// above miss it → "vCluster 0/0" despite a Running 1/1 vcluster (live hw225
	// uat225wp 2026-07-05, #936 class). Enumerate app=vcluster StatefulSets,
	// deduping against namespaces already emitted above. Refs #4739.
	seen := map[string]bool{}
	for _, v := range out {
		seen[v.Namespace] = true
	}
	stsGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	stsCtx, stsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer stsCancel()
	stsList, stsErr := in.DynamicClient.Resource(stsGVR).List(stsCtx, metav1.ListOptions{
		LabelSelector: "app=vcluster",
	})
	if stsErr == nil && stsList != nil {
		for _, sts := range stsList.Items {
			ns := sts.GetNamespace()
			if seen[ns] {
				continue
			}
			seen[ns] = true
			out = append(out, VCluster{
				ID:        "vcluster-" + ns,
				Name:      sts.GetName(),
				Namespace: ns,
				Role:      vclusterRole(sts.GetLabels()),
				Status:    "healthy",
			})
		}
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

// buildStorage — PVCs + block Volumes from the live cluster. Empty
// slices when sources aren't reachable.
//
// #5611: Volumes was previously a hardcoded `[]Volume{}` (the comment
// claimed "buckets/volumes from the Crossplane managed-resource list"
// but no query was ever issued), so the cloud Volumes page rendered a
// positive "Volumes 0 / No volumes yet" on a Sovereign carrying 50 EVS
// block volumes. A PersistentVolume IS the Kubernetes projection of the
// cloud block volume attached to a node (Hetzner Volume via
// hcloud-csi, Huawei EVS via evs.csi.huaweicloud.com), so loadVolumes
// reads PVs directly — the provider-agnostic source that is populated
// on every Sovereign, not just the hcloud mothership. Buckets stay []
// until an object-storage source is wired (honest empty, not a false
// count — the Buckets page renders the "not collected" empty-state).
func buildStorage(ctx context.Context, in LoaderInput) StorageData {
	return StorageData{
		PVCs:    loadPVCs(ctx, in),
		Buckets: []Bucket{},
		Volumes: loadVolumes(ctx, in),
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

// loadVolumes — the live block-Volume source for the cloud Volumes page
// (#5611). A PersistentVolume is the Kubernetes projection of the cloud
// block volume attached to a node (hcloud-csi Volume on Hetzner, EVS on
// Huawei via evs.csi.huaweicloud.com), so PVs are the provider-agnostic
// source that is populated on every Sovereign — unlike the hcloud-only
// Crossplane `volume.hcloud` XRC, which returns nothing on a Huawei
// Sovereign and produced the false "Volumes 0". Mirrors loadPVCs: reads
// the same in.DynamicClient (the region this loader is scoped to), so
// the count matches that region's live PVs and never fabricates a row.
func loadVolumes(ctx context.Context, in LoaderInput) (out []Volume) {
	out = []Volume{}
	defer func() {
		if r := recover(); r != nil {
			out = []Volume{}
		}
	}()
	if in.DynamicClient == nil {
		return out
	}
	// PVs are cluster-scoped — list without a namespace.
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := in.DynamicClient.Resource(gvr).List(cctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return out
	}
	for _, item := range list.Items {
		spec, _, _ := nestedMap(item.Object, "spec")
		status, _, _ := nestedMap(item.Object, "status")
		out = append(out, Volume{
			ID:         item.GetName(),
			Name:       item.GetName(),
			Capacity:   stringField(stringMapField(spec, "capacity"), "storage"),
			Region:     pvRegion(spec),
			AttachedTo: pvClaimRef(spec),
			Status:     pvPhaseToTopologyStatus(stringField(status, "phase")),
		})
	}
	return out
}

// pvClaimRef — the "namespace/name" of the PVC bound to this PV, the
// honest "what claims this volume" the Volumes page's Attachment column
// surfaces. Empty (→ "detached" in the UI) for an unbound PV.
func pvClaimRef(spec map[string]any) string {
	ref, _, _ := nestedMap(spec, "claimRef")
	if ref == nil {
		return ""
	}
	ns := stringField(ref, "namespace")
	name := stringField(ref, "name")
	switch {
	case ns != "" && name != "":
		return ns + "/" + name
	case name != "":
		return name
	default:
		return ""
	}
}

// pvRegion — best-effort region/zone from the PV's CSI node-affinity
// topology term (topology.kubernetes.io/{region,zone} or a driver zone
// key). Empty when the PV carries no topology constraint — honest empty,
// never a synthesised region. Fully defensive against unstructured shape
// surprises (any type mismatch → "").
func pvRegion(spec map[string]any) string {
	na, _, _ := nestedMap(spec, "nodeAffinity", "required")
	if na == nil {
		return ""
	}
	terms, ok := na["nodeSelectorTerms"].([]any)
	if !ok {
		return ""
	}
	for _, t := range terms {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		exprs, ok := tm["matchExpressions"].([]any)
		if !ok {
			continue
		}
		for _, e := range exprs {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			key := stringField(em, "key")
			if !strings.Contains(key, "zone") && !strings.Contains(key, "region") {
				continue
			}
			vals, ok := em["values"].([]any)
			if !ok || len(vals) == 0 {
				continue
			}
			if s, ok := vals[0].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// pvPhaseToTopologyStatus — maps a PV's status.phase onto the
// TopologyStatus vocabulary the Volumes page's StatusPill renders
// (healthy | degraded | failed | unknown).
func pvPhaseToTopologyStatus(phase string) string {
	switch phase {
	case "Bound":
		return "healthy"
	case "Released":
		return "degraded"
	case "Failed":
		return "failed"
	default:
		// "Available" (unbound but usable) and "Pending"/"" all read as
		// unknown — present, not-yet-attached, no false health claim.
		return "unknown"
	}
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
