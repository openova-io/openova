// Package infrastructure carries the wire-contract types + Crossplane
// XRC writer helpers + topology loader the catalyst-api Sovereign
// Infrastructure surface emits.
//
// # Architectural rule (docs/INVIOLABLE-PRINCIPLES.md #3)
//
// All Day-2 mutations MUST go through Crossplane. The catalyst-api
// writes a Crossplane Composite Resource Claim (XRC) into the Sovereign
// cluster (NOT contabo-mkt) and returns 202. The Crossplane provider
// does the cloud work. The UI watches the Job that wraps the XRC
// submission via the existing Jobs/Executions surface (issue #205).
//
// catalyst-api MUST NOT call hcloud-go, NEVER `exec.Command("kubectl",
// ...)` for mutation, NEVER use client-go for direct mutation of cluster
// resources outside the XRC-write path. The dynamic client created from
// the deployment's persisted kubeconfig is the ONLY mutation seam.
//
// # Wire contract — TopologyResponse
//
// The unified GET /api/v1/deployments/{id}/infrastructure/topology
// returns the WHOLE hierarchical tree in ONE shape so the four
// frontend tabs (Topology / Compute / Storage / Network) all render
// filtered views off a single response — no per-tab fetches, no
// cross-fetch coordination state on the client side.
//
// # Empty fallback (founder principle: never placeholder data)
//
// When live data isn't available (vCluster CRs not present, peerings
// not provisioned yet), the loader returns empty arrays — never
// placeholder rows. The frontend's empty-card UX is the canonical
// surface for that state.
package infrastructure

import "time"

// TopologyResponse — unified hierarchical view of the Sovereign's
// infrastructure. The four tabs filter views off this single shape.
type TopologyResponse struct {
	// Cloud — list of cloud-provider tenants behind this Sovereign.
	// Today we model one Hetzner project per deployment; future
	// multi-cloud Sovereigns will surface multiple tenants here.
	Cloud []CloudTenant `json:"cloud"`

	// Topology — pattern + per-region layout. The wizard's BYO Flow B
	// + the multi-region per-provider rework make this the canonical
	// shape: a Sovereign is N regions × clusters × node-pools wide.
	Topology TopologyData `json:"topology"`

	// Storage — Persistent Volume Claims, S3-compatible buckets, and
	// raw block Volumes attached across the topology. Aggregated here
	// so the Storage tab renders without a second round-trip.
	Storage StorageData `json:"storage"`
}

// CloudTenant — one cloud-provider account/project this Sovereign
// runs against. The catalyst-api derives this from the deployment
// record's Request (e.g. HetznerProjectID) — credentials never flow
// into the response.
type CloudTenant struct {
	ID       string `json:"id"`
	Provider string `json:"provider"` // hetzner | oci | aws | ...
	Name     string `json:"name"`     // human label

	// ProjectID — opaque cloud-side identifier (e.g. Hetzner project
	// number). Read-only metadata; never treated as a credential.
	ProjectID string `json:"projectID,omitempty"`

	// Status mirrors the deployment's overall status — healthy when
	// the Sovereign is ready, unknown while provisioning, failed on
	// terminal failure.
	Status string `json:"status"`
}

// TopologyData — pattern + regions list. Pattern derives from the
// number of regions and HA flag: solo (1 region, 1 cluster), ha-pair
// (1 region, HA), multi-region (N>1 regions), air-gap (BYO + isolated).
type TopologyData struct {
	Pattern string   `json:"pattern"`
	Regions []Region `json:"regions"`
}

// Region — one cloud region within the Sovereign's deployment. Carries
// the per-region SKU + worker count plus the live cluster + node-pool
// + network state read from the informer cache when available.
type Region struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	ProviderRegion string `json:"providerRegion"` // e.g. fsn1, hel1, ash

	// SkuCP / SkuWorker — Hetzner server type slug (cpx21, cpx41).
	// Empty when the deployment hasn't reached Validate yet.
	SkuCP     string `json:"skuCP"`
	SkuWorker string `json:"skuWorker"`

	// WorkerCount — declared worker count for this region. The
	// frontend renders this on the region card; the live node count
	// comes from len(Clusters[*].Nodes).
	WorkerCount int `json:"workerCount"`

	// Status — healthy | degraded | failed | unknown. Pre-Phase-0
	// deployments emit unknown.
	Status string `json:"status"`

	Clusters []Cluster `json:"clusters"`
	Networks []Network `json:"networks"`
}

// Cluster — one Kubernetes cluster within a region. The OWNING
// Sovereign always has exactly one host cluster today; future
// multi-cluster Sovereigns will surface additional entries here.
type Cluster struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`

	// NodeCount — total nodes (control-plane + workers) across all
	// node-pools on this cluster. Computed from Nodes when the live
	// cluster informer cache is populated; falls back to the
	// declared count from the deployment record otherwise.
	NodeCount int `json:"nodeCount"`

	VClusters     []VCluster     `json:"vclusters"`
	LoadBalancers []LoadBalancer `json:"loadBalancers"`
	NodePools     []NodePool     `json:"nodePools"`
	Nodes         []Node         `json:"nodes"`
}

// VCluster — a vcluster.io v1alpha1 virtual cluster running on the
// host cluster. Used by Catalyst's DMZ / RTZ / MGMT building-block
// layout. Populated only when the vcluster operator is installed AND
// at least one VCluster CR exists; otherwise the slice is empty.
type VCluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Role      string `json:"role"` // dmz | rtz | mgmt | other
	Status    string `json:"status"`
}

// LoadBalancer — Hetzner cloud LB (or future multi-cloud equivalent)
// attached to the cluster. Today a Sovereign has exactly one LB
// fronting the ingress controller; future multi-LB topologies surface
// here.
type LoadBalancer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PublicIP     string `json:"publicIP"`
	Ports        string `json:"ports"`
	TargetHealth string `json:"targetHealth"`
	Region       string `json:"region"`
	Status       string `json:"status"`
}

// NodePool — a logical group of identically-sized worker nodes the
// catalyst-environment-controller can scale up/down via the
// NodePoolClaim XRC. The Phase-0 OpenTofu module emits one
// control-plane pool + one worker pool per region; Day-2 mutations
// add additional pools.
type NodePool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`        // control-plane | worker
	SKU         string `json:"sku"`         // server type slug
	Region      string `json:"region"`
	DesiredSize int    `json:"desiredSize"` // declared size
	CurrentSize int    `json:"currentSize"` // observed from informer
	Status      string `json:"status"`
}

// Node — one Kubernetes node. Surfaced from the live cluster's
// informer cache; pre-cluster deployments synthesise nodes from the
// declared topology for the canvas to render.
type Node struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SKU        string `json:"sku"`
	Region     string `json:"region"`
	Role       string `json:"role"`
	IP         string `json:"ip"`
	Status     string `json:"status"`
	NodePoolID string `json:"nodePoolID,omitempty"`
}

// Network — one cloud network / VPC plus its peerings + firewalls.
// Multi-region Sovereigns peer their per-region networks through this
// shape; the third-sibling Crossplane PeeringClaim Composition writes
// the actual peering object.
type Network struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	CIDR     string         `json:"cidr"`
	Region   string         `json:"region"`
	Peerings []Peering      `json:"peerings"`
	Firewall *FirewallRules `json:"firewall,omitempty"`
	Status   string         `json:"status"`
}

// Peering — one VPC-to-VPC peering edge. Status mirrors the cloud
// provider's terminal state (active | degraded | failed | unknown).
type Peering struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	VPCPair string `json:"vpcPair"`
	Subnets string `json:"subnets"`
	Status  string `json:"status"`
}

// FirewallRules — collected ingress/egress rules a cloud firewall
// applies to its attached resources. Surfaced as a dedicated child of
// Network so the Network tab renders rule chips inline with the VPC
// card.
type FirewallRules struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Rules []FirewallRule `json:"rules"`
}

// FirewallRule — a single allow/deny rule. The IP-list field is
// rendered as a comma-separated string in the UI; a slice would tempt
// the React grid to scroll horizontally for /32 enumerations.
type FirewallRule struct {
	ID        string `json:"id"`
	Direction string `json:"direction"` // in | out
	Protocol  string `json:"protocol"`  // tcp | udp | icmp
	Port      string `json:"port"`      // empty for icmp
	Sources   string `json:"sources"`   // CIDR list (CSV)
	Action    string `json:"action"`    // accept | drop
}

// StorageData — aggregate storage view across the whole topology.
// PVCs come from the live cluster (when reachable); buckets +
// volumes come from cloud-provider state via Crossplane managed-
// resource status.
type StorageData struct {
	PVCs    []PVC    `json:"pvcs"`
	Buckets []Bucket `json:"buckets"`
	Volumes []Volume `json:"volumes"`
}

// PVC — one Kubernetes PersistentVolumeClaim. Fields mirror the
// existing infraPVCItem shape so the UI's PVC card renders unchanged.
type PVC struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Capacity     string `json:"capacity"`
	Used         string `json:"used"`
	StorageClass string `json:"storageClass"`
	Status       string `json:"status"`
}

// Bucket — one S3-compatible object bucket (Hetzner Object Storage,
// SeaweedFS volume server, etc.).
type Bucket struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Endpoint      string `json:"endpoint"`
	Capacity      string `json:"capacity"`
	Used          string `json:"used"`
	RetentionDays string `json:"retentionDays"`
}

// Volume — one cloud block-storage volume (Hetzner Volume).
type Volume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Capacity   string `json:"capacity"`
	Region     string `json:"region"`
	AttachedTo string `json:"attachedTo"`
	Status     string `json:"status"`
}

// MutationResponse — uniform 202 Accepted shape every CRUD endpoint
// emits. The frontend keys off jobId to deep-link to the
// GitLab-style log viewer in the Jobs surface.
type MutationResponse struct {
	JobID    string `json:"jobId"`
	XRCKind  string `json:"xrcKind"`
	XRCName  string `json:"xrcName"`
	Status   string `json:"status"`
	SubmittedAt time.Time `json:"submittedAt"`

	// Cascade — populated only on DELETE. Lists the child resources
	// the cascade would affect so the FE confirm dialog can render
	// "deleting region X will drain Y workloads, remove Z PVCs".
	Cascade []CascadeImpact `json:"cascade,omitempty"`
}

// CascadeImpact — one row in a delete cascade preview.
type CascadeImpact struct {
	Kind  string `json:"kind"`  // region | cluster | nodePool | node | pvc | volume | lb | peering
	ID    string `json:"id"`
	Name  string `json:"name"`
	Note  string `json:"note,omitempty"` // operator-readable detail
}
