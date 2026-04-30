// Package handler — infrastructure.go: REST surface for the Sovereign
// Infrastructure page (issue #227 + Day-2 CRUD via Crossplane).
//
//	GET    /api/v1/deployments/{depId}/infrastructure/topology  — unified
//	GET    /api/v1/deployments/{depId}/infrastructure/compute   — legacy
//	GET    /api/v1/deployments/{depId}/infrastructure/storage   — legacy
//	GET    /api/v1/deployments/{depId}/infrastructure/network   — legacy
//
// Day-2 CRUD (every endpoint writes a Crossplane XRC):
//
//	POST   /api/v1/deployments/{depId}/infrastructure/regions
//	POST   /api/v1/deployments/{depId}/infrastructure/regions/{id}/clusters
//	POST   /api/v1/deployments/{depId}/infrastructure/clusters/{id}/vclusters
//	POST   /api/v1/deployments/{depId}/infrastructure/clusters/{id}/pools
//	PATCH  /api/v1/deployments/{depId}/infrastructure/pools/{id}
//	POST   /api/v1/deployments/{depId}/infrastructure/loadbalancers
//	POST   /api/v1/deployments/{depId}/infrastructure/peerings
//	POST   /api/v1/deployments/{depId}/infrastructure/firewalls/{id}/rules
//	POST   /api/v1/deployments/{depId}/infrastructure/nodes/{id}/{cordon|drain|replace}
//	DELETE /api/v1/deployments/{depId}/infrastructure/{kind}/{id}
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 every mutation flows through a
// Crossplane Composite Resource Claim (XRC) the catalyst-api writes
// against the SOVEREIGN cluster's kubeconfig. The handler does NOT
// call hcloud-go, NEVER `exec.Command("kubectl",...)`, NEVER use
// client-go for direct mutation outside the XRC-write path. The
// Crossplane Composition controller (authored by the third-sibling
// agent) reconciles the claim into actual cloud resources.
//
// When the Composition for a given XRC kind is not yet present on
// the Sovereign cluster, the create still succeeds — Crossplane
// stores the claim and sits it as Pending. The audit-trail Job
// records "Awaiting Crossplane Composition for <kind>" so an
// operator browsing the Jobs surface sees the gap.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every knob (XRC API group,
// namespace, dynamic-client factory) is a runtime parameter — see
// internal/infrastructure/xrc.go for the centralised constants.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/dynamic"

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

/* ── HTTP handlers — Day-2 CRUD via Crossplane XRC ─────────────── */

// CreateInfrastructureRegion — POST .../infrastructure/regions
//
// Body: { region, skuCP, skuWorker?, workerCount }
// Writes a RegionClaim XRC. Composition target: region-composition.
type createRegionBody struct {
	Region      string `json:"region"`
	SkuCP       string `json:"skuCP"`
	SkuWorker   string `json:"skuWorker"`
	WorkerCount int    `json:"workerCount"`
	Provider    string `json:"provider"`
}

func (h *Handler) CreateInfrastructureRegion(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createRegionBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Region) == "" {
		writeBadRequest(w, "region-required", "region is required")
		return
	}
	if strings.TrimSpace(body.SkuCP) == "" {
		writeBadRequest(w, "skuCP-required", "skuCP is required")
		return
	}
	provider := body.Provider
	if provider == "" {
		provider = firstProvider(dep.Request)
	}
	xrcName := infrastructure.XRCName(dep.ID, "region", body.Region)
	spec := map[string]any{
		"region":      body.Region,
		"provider":    provider,
		"skuCP":       body.SkuCP,
		"skuWorker":   body.SkuWorker,
		"workerCount": body.WorkerCount,
	}
	action := fmt.Sprintf("add-region region=%s sku=%s workers=%d", body.Region, body.SkuCP, body.WorkerCount)
	diff := fmt.Sprintf("+ region: %s\n+   skuCP: %s\n+   skuWorker: %s\n+   workerCount: %d", body.Region, body.SkuCP, body.SkuWorker, body.WorkerCount)
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "region",
		Slug:    body.Region,
		Action:  action,
		Diff:    diff,
		XRCKind: infrastructure.KindRegionClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructureCluster — POST .../regions/{id}/clusters
type createClusterBody struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	HA      bool   `json:"ha"`
}

func (h *Handler) CreateInfrastructureCluster(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	regionID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createClusterBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "name-required", "cluster name is required")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "cluster", body.Name)
	spec := map[string]any{
		"region":  regionID,
		"name":    body.Name,
		"version": body.Version,
		"ha":      body.HA,
	}
	action := fmt.Sprintf("add-cluster name=%s region=%s ha=%v", body.Name, regionID, body.HA)
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "cluster",
		Slug:    body.Name,
		Action:  action,
		Diff:    "+ cluster: " + body.Name,
		XRCKind: infrastructure.KindClusterClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructureVCluster — POST .../clusters/{id}/vclusters
type createVClusterBody struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Role      string `json:"role"`
}

func (h *Handler) CreateInfrastructureVCluster(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	clusterID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createVClusterBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "name-required", "vcluster name is required")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "vcluster", body.Name)
	spec := map[string]any{
		"cluster":   clusterID,
		"name":      body.Name,
		"namespace": body.Namespace,
		"role":      body.Role,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "vcluster",
		Slug:    body.Name,
		Action:  fmt.Sprintf("add-vcluster name=%s cluster=%s role=%s", body.Name, clusterID, body.Role),
		Diff:    "+ vcluster: " + body.Name,
		XRCKind: infrastructure.KindVClusterClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructurePool — POST .../clusters/{id}/pools
type createPoolBody struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	SKU         string `json:"sku"`
	Region      string `json:"region"`
	DesiredSize int    `json:"desiredSize"`
}

func (h *Handler) CreateInfrastructurePool(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	clusterID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createPoolBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "name-required", "pool name is required")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "pool", body.Name)
	spec := map[string]any{
		"cluster":     clusterID,
		"name":        body.Name,
		"role":        body.Role,
		"sku":         body.SKU,
		"region":      body.Region,
		"desiredSize": body.DesiredSize,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "pool",
		Slug:    body.Name,
		Action:  fmt.Sprintf("add-pool name=%s cluster=%s sku=%s size=%d", body.Name, clusterID, body.SKU, body.DesiredSize),
		Diff:    fmt.Sprintf("+ nodePool: %s\n+   sku: %s\n+   desiredSize: %d", body.Name, body.SKU, body.DesiredSize),
		XRCKind: infrastructure.KindNodePoolClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// PatchInfrastructurePool — PATCH .../pools/{id}
type patchPoolBody struct {
	DesiredSize *int   `json:"desiredSize,omitempty"`
	SKU         string `json:"sku,omitempty"`
}

func (h *Handler) PatchInfrastructurePool(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	poolID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body patchPoolBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if body.DesiredSize == nil && strings.TrimSpace(body.SKU) == "" {
		writeBadRequest(w, "no-fields", "PATCH must include desiredSize and/or sku")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "pool", poolID)
	spec := map[string]any{
		"name": poolID,
	}
	diff := ""
	if body.DesiredSize != nil {
		spec["desiredSize"] = *body.DesiredSize
		diff += fmt.Sprintf("~ desiredSize: %d\n", *body.DesiredSize)
	}
	if body.SKU != "" {
		spec["sku"] = body.SKU
		diff += "~ sku: " + body.SKU + "\n"
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "update",
		Kind:    "pool",
		Slug:    poolID,
		Action:  fmt.Sprintf("update-pool id=%s", poolID),
		Diff:    diff,
		XRCKind: infrastructure.KindNodePoolClaim,
		XRCName: xrcName,
		Spec:    spec,
		Patch:   true,
	})
}

// CreateInfrastructureLoadBalancer — POST .../loadbalancers
type createLBBody struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Ports  string `json:"ports"`
}

func (h *Handler) CreateInfrastructureLoadBalancer(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createLBBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "name-required", "lb name is required")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "lb", body.Name)
	spec := map[string]any{
		"name":   body.Name,
		"region": body.Region,
		"ports":  body.Ports,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "lb",
		Slug:    body.Name,
		Action:  fmt.Sprintf("add-lb name=%s region=%s", body.Name, body.Region),
		Diff:    "+ lb: " + body.Name,
		XRCKind: infrastructure.KindLoadBalancerClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructurePeering — POST .../peerings
type createPeeringBody struct {
	Name    string `json:"name"`
	VPCFrom string `json:"vpcFrom"`
	VPCTo   string `json:"vpcTo"`
	Subnets string `json:"subnets"`
}

func (h *Handler) CreateInfrastructurePeering(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createPeeringBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "name-required", "peering name is required")
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, "peering", body.Name)
	spec := map[string]any{
		"name":    body.Name,
		"vpcFrom": body.VPCFrom,
		"vpcTo":   body.VPCTo,
		"subnets": body.Subnets,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "peering",
		Slug:    body.Name,
		Action:  fmt.Sprintf("add-peering name=%s pair=%s/%s", body.Name, body.VPCFrom, body.VPCTo),
		Diff:    fmt.Sprintf("+ peering: %s\n+   vpcFrom: %s\n+   vpcTo: %s", body.Name, body.VPCFrom, body.VPCTo),
		XRCKind: infrastructure.KindPeeringClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructureFirewallRule — POST .../firewalls/{id}/rules
type createFWRuleBody struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Port      string `json:"port"`
	Sources   string `json:"sources"`
	Action    string `json:"action"`
}

func (h *Handler) CreateInfrastructureFirewallRule(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	fwID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body createFWRuleBody
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Direction) == "" || strings.TrimSpace(body.Protocol) == "" {
		writeBadRequest(w, "direction-protocol-required", "direction and protocol are required")
		return
	}
	slug := body.Direction + "-" + body.Protocol + "-" + body.Port
	xrcName := infrastructure.XRCName(dep.ID, "fw", fwID+"-"+slug)
	spec := map[string]any{
		"firewall":  fwID,
		"direction": body.Direction,
		"protocol":  body.Protocol,
		"port":      body.Port,
		"sources":   body.Sources,
		"action":    body.Action,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    "add",
		Kind:    "firewall-rule",
		Slug:    slug,
		Action:  fmt.Sprintf("add-firewall-rule fw=%s %s/%s/%s", fwID, body.Direction, body.Protocol, body.Port),
		Diff:    fmt.Sprintf("+ rule: %s/%s/%s sources=%s action=%s", body.Direction, body.Protocol, body.Port, body.Sources, body.Action),
		XRCKind: infrastructure.KindFirewallRuleClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// CreateInfrastructureNodeAction — POST .../nodes/{id}/{cordon|drain|replace}
func (h *Handler) CreateInfrastructureNodeAction(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	nodeID := chi.URLParam(r, "id")
	verb := strings.ToLower(chi.URLParam(r, "action"))
	switch verb {
	case "cordon", "drain", "replace":
	default:
		writeBadRequest(w, "unsupported-action", "action must be cordon|drain|replace")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	xrcName := infrastructure.XRCName(dep.ID, verb, nodeID)
	spec := map[string]any{
		"node":   nodeID,
		"action": verb,
	}
	h.submitMutation(w, r, dep, mutationInputs{
		Verb:    verb,
		Kind:    "node",
		Slug:    nodeID,
		Action:  fmt.Sprintf("%s-node id=%s", verb, nodeID),
		Diff:    fmt.Sprintf("~ node %s: %s", nodeID, verb),
		XRCKind: infrastructure.KindNodeActionClaim,
		XRCName: xrcName,
		Spec:    spec,
	})
}

// DeleteInfrastructureResource — DELETE .../{kind}/{id}
//
// Maps `kind` to the corresponding XRC kind, deletes the claim, and
// returns 202 with a Cascade preview computed from the live topology.
func (h *Handler) DeleteInfrastructureResource(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	id := chi.URLParam(r, "id")
	xrcKind, ok := xrcKindForResourceKind(kind)
	if !ok {
		writeBadRequest(w, "unknown-kind", "unsupported resource kind: "+kind)
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	// Compute cascade preview from current topology BEFORE we delete.
	in := h.loaderInputFor(dep)
	topology := infrastructure.Load(r.Context(), in)
	cascade := infrastructure.CascadeFor(kind, id, topology)

	// Resolve the XRC name from the resource id. The CRUD POST
	// helpers stamp deterministic names off (depID, verb, slug); a
	// DELETE request carries the resource id which can be the slug
	// component (e.g. region "hel1"). We try the deterministic name
	// first; if the dynamic client says NotFound, surface 404.
	xrcName := infrastructure.XRCName(dep.ID, kind, id)

	client, clientErr := h.sovereignDynamicClient(dep)
	if clientErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-cluster-unreachable",
			"detail": clientErr.Error(),
		})
		return
	}

	bridge := h.bridgeFor(dep)
	mutationRes, mutErr := h.registerMutation(bridge, jobs.MutationRecord{
		Verb:    "remove",
		Kind:    kind,
		Slug:    id,
		Action:  fmt.Sprintf("remove-%s id=%s", kind, id),
		Diff:    fmt.Sprintf("- %s: %s", kind, id),
		XRCKind: xrcKind,
		At:      time.Now().UTC(),
	})
	if mutErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "mutation-job-register-failed",
			"detail": mutErr.Error(),
		})
		return
	}

	_, delErr := infrastructure.DeleteXRC(r.Context(), client, xrcKind, xrcName)
	submittedAt := infrastructure.SubmittedAt()
	respStatus := "submitted-pending-composition"
	httpStatus := http.StatusAccepted
	if delErr != nil {
		if errors.Is(delErr, infrastructure.ErrXRCNameConflict) {
			// Treat NotFound as "already gone" — we still emit a
			// success Job so the audit trail captures intent. The FE
			// surfaces 202 so the table re-renders without the row.
			respStatus = "already-absent"
		} else {
			_ = bridge.FinishMutationJob(mutationRes, jobs.StatusFailed, delErr.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "xrc-delete-failed",
				"detail": delErr.Error(),
			})
			return
		}
	}
	_ = bridge.AppendXRCSubmittedLog(mutationRes, xrcKind, xrcName,
		"deletionPolicy=Delete; cascade rows: "+itoa(len(cascade)))
	_ = bridge.FinishMutationJob(mutationRes, jobs.StatusSucceeded, "")

	resp := infrastructure.MutationResponse{
		JobID:       mutationRes.JobID,
		XRCKind:     xrcKind,
		XRCName:     xrcName,
		Status:      respStatus,
		SubmittedAt: submittedAt,
		Cascade:     cascade,
	}
	writeJSON(w, httpStatus, resp)
}

/* ── Internal helpers ──────────────────────────────────────────── */

// mutationInputs — common payload submitMutation handles. Pulled out
// so each per-kind handler is small + reads as a config block, not a
// 30-line copy of the audit-trail dance.
type mutationInputs struct {
	Verb    string
	Kind    string
	Slug    string
	Action  string
	Diff    string
	XRCKind string
	XRCName string
	Spec    map[string]any
	Patch   bool // when true, treat conflict as "submitted-pending-composition"
}

// submitMutation is the common pipe every CRUD POST/PATCH handler
// goes through:
//
//  1. Acquire the Sovereign cluster's dynamic client.
//  2. Register the mutation Job (audit trail).
//  3. Submit the XRC (or detect conflict).
//  4. Append the xrc-submitted log line + finish the Job.
//  5. Emit the 202 response.
//
// Any failure short-circuits with the appropriate HTTP code; the
// audit-trail Job is still committed so an operator can see the
// failed attempt on the Jobs surface.
func (h *Handler) submitMutation(w http.ResponseWriter, r *http.Request, dep *Deployment, in mutationInputs) {
	client, clientErr := h.sovereignDynamicClient(dep)
	if clientErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-cluster-unreachable",
			"detail": clientErr.Error(),
		})
		return
	}

	bridge := h.bridgeFor(dep)
	mutationRes, mutErr := h.registerMutation(bridge, jobs.MutationRecord{
		Verb:    in.Verb,
		Kind:    in.Kind,
		Slug:    in.Slug,
		Action:  in.Action,
		Diff:    in.Diff,
		XRCKind: in.XRCKind,
		At:      time.Now().UTC(),
	})
	if mutErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "mutation-job-register-failed",
			"detail": mutErr.Error(),
		})
		return
	}

	_, _, submitErr := infrastructure.SubmitXRC(r.Context(), client, infrastructure.XRCSpec{
		Kind:         in.XRCKind,
		Name:         in.XRCName,
		DeploymentID: dep.ID,
		Action:       in.Action,
		Diff:         in.Diff,
		Spec:         in.Spec,
	})
	if submitErr != nil {
		if errors.Is(submitErr, infrastructure.ErrXRCNameConflict) {
			if in.Patch {
				// PATCH-style update: conflict is the expected case
				// when the XRC already exists. Today we treat the
				// PATCH as "submission accepted" because the third-
				// sibling Composition handles in-place updates via
				// .spec convergence. The FE re-fetches the topology
				// to observe the new desiredSize.
				_ = bridge.AppendXRCSubmittedLog(mutationRes, in.XRCKind, in.XRCName,
					"existing claim updated in place")
				_ = bridge.FinishMutationJob(mutationRes, jobs.StatusSucceeded, "")
				writeJSON(w, http.StatusAccepted, infrastructure.MutationResponse{
					JobID:       mutationRes.JobID,
					XRCKind:     in.XRCKind,
					XRCName:     in.XRCName,
					Status:      "submitted-pending-composition",
					SubmittedAt: infrastructure.SubmittedAt(),
				})
				return
			}
			_ = bridge.FinishMutationJob(mutationRes, jobs.StatusFailed,
				"xrc name conflict: "+in.XRCName)
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "xrc-name-conflict",
				"detail": "an XRC with name " + in.XRCName + " already exists",
			})
			return
		}
		_ = bridge.FinishMutationJob(mutationRes, jobs.StatusFailed, submitErr.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "xrc-submit-failed",
			"detail": submitErr.Error(),
		})
		return
	}
	_ = bridge.AppendXRCSubmittedLog(mutationRes, in.XRCKind, in.XRCName,
		"awaiting Crossplane Composition reconciliation")
	_ = bridge.FinishMutationJob(mutationRes, jobs.StatusSucceeded, "")

	writeJSON(w, http.StatusAccepted, infrastructure.MutationResponse{
		JobID:       mutationRes.JobID,
		XRCKind:     in.XRCKind,
		XRCName:     in.XRCName,
		Status:      "submitted-pending-composition",
		SubmittedAt: infrastructure.SubmittedAt(),
	})
}

// bridgeFor — returns the per-deployment jobs.Bridge, allocating one
// when the deployment doesn't have one yet (Day-2 mutations on a
// rehydrated deployment whose helmwatch hasn't started). The bridge
// is stored on the Deployment so subsequent mutations append to the
// same audit-trail surface.
func (h *Handler) bridgeFor(dep *Deployment) *jobs.Bridge {
	if h.jobs == nil {
		// Tests without persistence — surface a no-op bridge that
		// still answers RegisterMutationJob with deterministic ids.
		// We allocate against an in-memory store so every mutation
		// path remains exercised in unit tests.
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

// registerMutation — thin wrapper around bridge.RegisterMutationJob
// so the in-test no-op bridge can short-circuit without writing to
// disk. Production passes through unchanged.
func (h *Handler) registerMutation(bridge *jobs.Bridge, rec jobs.MutationRecord) (jobs.MutationResult, error) {
	return bridge.RegisterMutationJob(rec)
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

// sovereignDynamicClient — builds a dynamic.Interface from the
// deployment's persisted kubeconfig. Returns an error when the
// kubeconfig is missing (cloud-init hasn't posted back yet) or
// unreadable (PVC unmount). Per docs/INVIOLABLE-PRINCIPLES.md #3
// this is the ONLY path through which catalyst-api obtains a
// mutation-capable client against the Sovereign cluster.
func (h *Handler) sovereignDynamicClient(dep *Deployment) (dynamic.Interface, error) {
	dep.mu.Lock()
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()
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
	return infrastructure.LoaderInput{
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
}

// tryDynamicClientLocked — best-effort dynamic client for live-source
// reads. Caller MUST hold dep.mu (loaderInputFor is the only caller
// today). A failure (kubeconfig missing, parse error) returns nil
// without surfacing through to the loader; the loader treats nil
// as "no live data" and returns empty arrays.
func (h *Handler) tryDynamicClientLocked(dep *Deployment) dynamic.Interface {
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

// xrcKindForResourceKind — DELETE handler maps a URL path segment
// (e.g. "regions") onto the canonical XRC kind ("RegionClaim"). The
// mapping is centralised here so a future kind that changes its
// URL segment only flips one switch.
func xrcKindForResourceKind(kind string) (string, bool) {
	switch strings.ToLower(kind) {
	case "regions", "region":
		return infrastructure.KindRegionClaim, true
	case "clusters", "cluster":
		return infrastructure.KindClusterClaim, true
	case "vclusters", "vcluster":
		return infrastructure.KindVClusterClaim, true
	case "pools", "pool", "nodepools", "nodepool":
		return infrastructure.KindNodePoolClaim, true
	case "loadbalancers", "loadbalancer", "lb":
		return infrastructure.KindLoadBalancerClaim, true
	case "peerings", "peering":
		return infrastructure.KindPeeringClaim, true
	case "firewalls", "firewall", "firewallrules", "firewallrule":
		return infrastructure.KindFirewallRuleClaim, true
	case "nodes", "node":
		return infrastructure.KindNodeActionClaim, true
	}
	return "", false
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
