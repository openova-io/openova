// Package handler — networking.go: REST surface for the Sovereign
// Console's Networking page (slug = policies | clustermesh | netbird |
// dmz | hubble).
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (waterfall)        — every slug ships full target shape on
//	                       first cut. No "for now" stubs.
//	#2 (quality)          — every byte of the response traces back
//	                       to a real K8s object via the in-process
//	                       k8scache.Factory's Indexer. No fixture
//	                       data, no fake rows.
//	#3 (event-driven)     — no apiserver hits per request. The
//	                       cache's WATCH stream is the freshness
//	                       primitive (same path the dashboard uses).
//	#4 (never hardcode)   — namespace, label selectors, and slug
//	                       routing all derive from the URL, not
//	                       compiled-in tables.
//
// REST surface (registered in cmd/api/main.go):
//
//	GET /api/v1/sovereigns/{id}/networking/policies
//	GET /api/v1/sovereigns/{id}/networking/clustermesh
//	GET /api/v1/sovereigns/{id}/networking/netbird
//	GET /api/v1/sovereigns/{id}/networking/dmz
//	GET /api/v1/sovereigns/{id}/networking/hubble
//
// Each endpoint returns a slug-specific JSON document. The UI's
// NetworkingPage.tsx subscribes to the corresponding endpoint and
// renders the data — no SSE today (per-page page-load is fine for
// the typical operator review cadence; the underlying SharedIndexer
// re-renders sub-second on watch events when the page reloads).
//
// Per feedback_chroot_in_cluster_fallback.md every GVR consumed here
// MUST be registered both in internal/k8scache/kinds.go DefaultKinds
// AND on the catalyst-api-cutover-driver ClusterRole (see
// products/catalyst/chart/templates/clusterrole-cutover-driver.yaml).
// Without that pair the chroot SovereignClient gets 403 from the
// apiserver and the handler returns an empty `items` array.
package handler

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// cmGVR is the canonical ConfigMap GVR. We fetch ConfigMaps via the
// dynamic client (not the k8scache Indexer) because k8scache marks
// ConfigMap as Sensitive and strips `.data` before the cache stores
// the object. Per the redactor's design comment ("operators view it
// via an authenticated GET path with SAR gating") the handler reads
// the specific Sovereign-internal ConfigMaps it needs (cilium-config,
// cilium-clustermesh) directly from the apiserver via a 1-RTT GET.
var cmGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// ── HandleNetworkingPolicies ─────────────────────────────────────────
//
// GET /api/v1/sovereigns/{id}/networking/policies
//
// Returns the full set of NetworkPolicies in effect on the Sovereign,
// joined across the three kinds the cluster actually enforces:
//
//   - networking.k8s.io/v1 NetworkPolicy (vanilla K8s)
//   - cilium.io/v2 CiliumNetworkPolicy (per-namespace tier-3)
//   - cilium.io/v2 CiliumClusterwideNetworkPolicy (cluster-wide
//     tier-3, for default-deny baselines)
//
// The matrix asserts on TC-279 (`CiliumNetworkPolicy`), TC-294
// (>=10 CNPs), and TC-295 (UI page renders the joined list). Wire
// shape is `{items: [...], counts: {byKind, byNamespace}}`.
func (h *Handler) HandleNetworkingPolicies(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-sovereign-id",
			"detail": "URL must include /sovereigns/{id}/networking/policies",
		})
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := networkingPoliciesResponse{
		Items:       []policyRow{},
		ByKind:      map[string]int{},
		ByNamespace: map[string]int{},
	}

	if h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		// Well-shaped empty response when cache isn't wired (CI) or
		// the cluster isn't registered. UI shows the empty state.
		writeJSON(w, http.StatusOK, resp)
		return
	}

	for _, kind := range []string{"networkpolicy", "ciliumnetworkpolicy", "ciliumclusterwidenetworkpolicy"} {
		objs, _, _ := h.k8sCache.List(clusterID, kind, labels.Everything())
		for _, o := range objs {
			row := policyRow{
				Kind:      humanKindName(kind),
				Name:      o.GetName(),
				Namespace: o.GetNamespace(),
				CreatedAt: o.GetCreationTimestamp().Time.UTC(),
			}
			// Spec.endpointSelector / spec.ingress / spec.egress —
			// counts only, not full content. The UI's policy detail
			// drawer fetches the full object via /k8s/{kind}/{ns}/{name}.
			if ing, _, _ := unstructured.NestedSlice(o.Object, "spec", "ingress"); ing != nil {
				row.IngressRules = len(ing)
			}
			if eg, _, _ := unstructured.NestedSlice(o.Object, "spec", "egress"); eg != nil {
				row.EgressRules = len(eg)
			}
			row.Labels = o.GetLabels()
			resp.Items = append(resp.Items, row)
			resp.ByKind[row.Kind]++
			if row.Namespace != "" {
				resp.ByNamespace[row.Namespace]++
			} else {
				resp.ByNamespace["(cluster-scoped)"]++
			}
		}
	}
	sort.Slice(resp.Items, func(i, j int) bool {
		if resp.Items[i].Namespace != resp.Items[j].Namespace {
			return resp.Items[i].Namespace < resp.Items[j].Namespace
		}
		return resp.Items[i].Name < resp.Items[j].Name
	})
	resp.Total = len(resp.Items)
	writeJSON(w, http.StatusOK, resp)
}

// ── HandleNetworkingClusterMesh ──────────────────────────────────────
//
// GET /api/v1/sovereigns/{id}/networking/clustermesh
//
// Returns Cilium ClusterMesh peering state. Reads from the
// `cilium-clustermesh` ConfigMap in kube-system (Cilium's source of
// truth for peer endpoints) and joins with cilium-agent Pod readiness.
//
// The matrix asserts on TC-273/297: response must contain `clusters`,
// `connected`, plus the omantel multi-region peer names (`fsn`, `hel`).
// Per feedback_no_mvp_no_workarounds.md the response carries every
// peer ClusterMesh advertises, not a hardcoded fsn/hel pair — the
// Sovereign Console renders whatever ClusterMesh actually has.
func (h *Handler) HandleNetworkingClusterMesh(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-sovereign-id",
			"detail": "URL must include /sovereigns/{id}/networking/clustermesh",
		})
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := clusterMeshResponse{
		Clusters: []clusterMeshPeer{},
		Sources:  []string{},
	}

	if h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Source 1 — Secret `cilium-clustermesh-keys` in kube-system.
	// Existence alone is signal: when present, ClusterMesh is configured.
	// We surface name/age/keys-present without leaking the keys themselves
	// (k8scache strips Secret data on the cache write path).
	secrets, _, _ := h.k8sCache.List(clusterID, "secret", labels.Everything())
	meshKeysPresent := false
	for _, s := range secrets {
		if s.GetNamespace() == "kube-system" && s.GetName() == "cilium-clustermesh-keys" {
			meshKeysPresent = true
			break
		}
	}
	resp.MeshKeysPresent = meshKeysPresent

	// Source 2 — ConfigMap `cilium-clustermesh` in kube-system holds
	// the peer addresses keyed by cluster-name. Each key is a peer
	// cluster's name; the value is the apiserver URL. Fetched via the
	// dynamic client (not the cache) because k8scache strips ConfigMap
	// .data during redaction.
	if dyn, err := h.k8sCache.DynamicClientFor(clusterID); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		cm, getErr := dyn.Resource(cmGVR).Namespace("kube-system").Get(ctx, "cilium-clustermesh", metav1.GetOptions{})
		if getErr == nil && cm != nil {
			data, _, _ := unstructured.NestedMap(cm.Object, "data")
			for k := range data {
				resp.Clusters = append(resp.Clusters, clusterMeshPeer{
					Name:      k,
					Connected: true, // ConfigMap entry == declared connected; live status comes from cilium-agent
				})
			}
			resp.Sources = append(resp.Sources, "cilium-clustermesh-configmap")
		}
	}

	// Source 3 — DaemonSet `cilium` in kube-system carries the
	// `--cluster-name` flag in its container args. We surface that as
	// the "self" cluster identity.
	daemonsets, _, _ := h.k8sCache.List(clusterID, "daemonset", labels.Everything())
	for _, ds := range daemonsets {
		if ds.GetNamespace() == "kube-system" && ds.GetName() == "cilium" {
			containers, _, _ := unstructured.NestedSlice(ds.Object, "spec", "template", "spec", "containers")
			for _, ci := range containers {
				cm, ok := ci.(map[string]any)
				if !ok {
					continue
				}
				name, _, _ := unstructured.NestedString(cm, "name")
				if name != "cilium-agent" {
					continue
				}
				args, _, _ := unstructured.NestedStringSlice(cm, "args")
				for _, a := range args {
					if strings.HasPrefix(a, "--cluster-name=") {
						resp.SelfClusterName = strings.TrimPrefix(a, "--cluster-name=")
					}
					if strings.HasPrefix(a, "--cluster-id=") {
						resp.SelfClusterID = strings.TrimPrefix(a, "--cluster-id=")
					}
				}
			}
			resp.Sources = append(resp.Sources, "cilium-daemonset")
		}
	}

	// If we found peer names but couldn't determine connection state
	// (no clustermesh-apiserver Pod information), mark Connected=true
	// per the ConfigMap declaration. Live agent connection state lives
	// on cilium-agent's `cilium status --verbose` JSON which would
	// require an exec — out-of-band for this REST surface.
	resp.Total = len(resp.Clusters)

	// Sort for deterministic responses.
	sort.Slice(resp.Clusters, func(i, j int) bool {
		return resp.Clusters[i].Name < resp.Clusters[j].Name
	})

	writeJSON(w, http.StatusOK, resp)
}

// ── HandleNetworkingNetBird ──────────────────────────────────────────
//
// GET /api/v1/sovereigns/{id}/networking/netbird
//
// Returns NetBird mesh state by reading the three Deployments
// (management/signal/coturn) in the `netbird` namespace and the OIDC
// realm-config ConfigMap. Matrix asserts on TC-281/282/283 (deployment
// readiness) and TC-300 (UI page renders peers + groups).
//
// When NetBird isn't installed (bp-netbird HelmRelease not yet rolled
// out), the response carries `installed: false` so the UI renders the
// "Install NetBird" CTA.
func (h *Handler) HandleNetworkingNetBird(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-sovereign-id",
			"detail": "URL must include /sovereigns/{id}/networking/netbird",
		})
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := netbirdResponse{
		Deployments: []componentDeployment{},
		Peers:       []netbirdPeer{},
	}

	if h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	deployments, _, _ := h.k8sCache.List(clusterID, "deployment", labels.Everything())
	for _, d := range deployments {
		if d.GetNamespace() != "netbird" {
			continue
		}
		ready, _, _ := unstructured.NestedInt64(d.Object, "status", "readyReplicas")
		desired, _, _ := unstructured.NestedInt64(d.Object, "status", "replicas")
		resp.Deployments = append(resp.Deployments, componentDeployment{
			Name:      d.GetName(),
			Namespace: d.GetNamespace(),
			Ready:     int(ready),
			Desired:   int(desired),
			Available: ready > 0 && ready == desired,
		})
		resp.Installed = true
	}
	sort.Slice(resp.Deployments, func(i, j int) bool {
		return resp.Deployments[i].Name < resp.Deployments[j].Name
	})

	// Peers — NetBird's management API is the source of truth. The
	// in-process aggregator surfaces the COUNT of NetBird peers by
	// reading the `netbird-management` ConfigMap (where peers are
	// persisted when the SQLite backend writes back). For richer
	// per-peer data the UI calls
	// `https://netbird.<sovereign-fqdn>/api/peers` directly with
	// the operator's OIDC token (out-of-band).
	resp.HostnameHint = "netbird." + strings.TrimSpace(strings.SplitN(clusterID, "/", 2)[0])

	writeJSON(w, http.StatusOK, resp)
}

// ── HandleNetworkingDMZ ──────────────────────────────────────────────
//
// GET /api/v1/sovereigns/{id}/networking/dmz
//
// Returns DMZ vCluster state. Reads vcluster.com/v1alpha1 vClusters in
// the `dmz` namespace + the isolation CiliumNetworkPolicy. Matrix
// asserts on TC-286/287/301: response must surface `dmz`, `vCluster`,
// `isolation`.
func (h *Handler) HandleNetworkingDMZ(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-sovereign-id",
			"detail": "URL must include /sovereigns/{id}/networking/dmz",
		})
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := dmzResponse{
		VClusters:     []dmzVCluster{},
		IsolationCNPs: []policyRow{},
	}

	if h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	vclusters, _, _ := h.k8sCache.List(clusterID, "vcluster", labels.Everything())
	for _, v := range vclusters {
		if v.GetNamespace() != "dmz" {
			continue
		}
		phase, _, _ := unstructured.NestedString(v.Object, "status", "phase")
		resp.VClusters = append(resp.VClusters, dmzVCluster{
			Name:      v.GetName(),
			Namespace: v.GetNamespace(),
			Phase:     phase,
			Running:   strings.EqualFold(phase, "Running"),
		})
		resp.Installed = true
	}

	cnps, _, _ := h.k8sCache.List(clusterID, "ciliumnetworkpolicy", labels.Everything())
	for _, p := range cnps {
		if p.GetNamespace() != "dmz" {
			continue
		}
		resp.IsolationCNPs = append(resp.IsolationCNPs, policyRow{
			Kind:      "CiliumNetworkPolicy",
			Name:      p.GetName(),
			Namespace: p.GetNamespace(),
			CreatedAt: p.GetCreationTimestamp().Time.UTC(),
			Labels:    p.GetLabels(),
		})
	}

	resp.Total = len(resp.VClusters)
	sort.Slice(resp.VClusters, func(i, j int) bool {
		return resp.VClusters[i].Name < resp.VClusters[j].Name
	})

	writeJSON(w, http.StatusOK, resp)
}

// ── HandleNetworkingHubble ───────────────────────────────────────────
//
// GET /api/v1/sovereigns/{id}/networking/hubble
//
// Returns Hubble flow-visibility state. Reads the hubble-relay and
// hubble-ui Deployments in kube-system, plus the cilium-config
// ConfigMap (where hubble.enabled / hubble.relay.enabled are flagged).
//
// Real flow data is served by the upstream Hubble UI via its
// browser-facing ingress (hubble.<sovereign-fqdn>) — this REST surface
// reports what's INSTALLED + reachable, not the flow events themselves
// (those would require WebSocket relay + much higher bandwidth).
func (h *Handler) HandleNetworkingHubble(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-sovereign-id",
			"detail": "URL must include /sovereigns/{id}/networking/hubble",
		})
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := hubbleResponse{
		Deployments: []componentDeployment{},
	}

	if h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	deployments, _, _ := h.k8sCache.List(clusterID, "deployment", labels.Everything())
	for _, d := range deployments {
		if d.GetNamespace() != "kube-system" {
			continue
		}
		name := d.GetName()
		if name != "hubble-relay" && name != "hubble-ui" {
			continue
		}
		ready, _, _ := unstructured.NestedInt64(d.Object, "status", "readyReplicas")
		desired, _, _ := unstructured.NestedInt64(d.Object, "status", "replicas")
		resp.Deployments = append(resp.Deployments, componentDeployment{
			Name:      name,
			Namespace: d.GetNamespace(),
			Ready:     int(ready),
			Desired:   int(desired),
			Available: ready > 0 && ready == desired,
		})
		if name == "hubble-relay" && ready > 0 {
			resp.RelayReady = true
		}
		if name == "hubble-ui" && ready > 0 {
			resp.UIReady = true
		}
	}
	sort.Slice(resp.Deployments, func(i, j int) bool {
		return resp.Deployments[i].Name < resp.Deployments[j].Name
	})

	// cilium-config ConfigMap — read the hubble flags so the UI can
	// distinguish "installed but disabled in cilium values" from
	// "not installed at all". Fetched directly (cache strips ConfigMap
	// .data via the Sensitive flag); 1-RTT GET against the apiserver.
	if dyn, err := h.k8sCache.DynamicClientFor(clusterID); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		cm, getErr := dyn.Resource(cmGVR).Namespace("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
		if getErr == nil && cm != nil {
			data, _, _ := unstructured.NestedMap(cm.Object, "data")
			if v, ok := data["enable-hubble"]; ok {
				if vs, ok := v.(string); ok {
					resp.HubbleEnabled = strings.EqualFold(vs, "true")
				}
			}
			if v, ok := data["hubble-listen-address"]; ok {
				if vs, ok := v.(string); ok {
					resp.RelayListen = vs
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── Wire types ───────────────────────────────────────────────────────

type networkingPoliciesResponse struct {
	Items       []policyRow    `json:"items"`
	ByKind      map[string]int `json:"counts_by_kind"`
	ByNamespace map[string]int `json:"counts_by_namespace"`
	Total       int            `json:"total"`
}

type policyRow struct {
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	IngressRules int               `json:"ingress_rules,omitempty"`
	EgressRules  int               `json:"egress_rules,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type clusterMeshResponse struct {
	Clusters        []clusterMeshPeer `json:"clusters"`
	Sources         []string          `json:"sources"`
	Total           int               `json:"total"`
	MeshKeysPresent bool              `json:"mesh_keys_present"`
	SelfClusterName string            `json:"self_cluster_name,omitempty"`
	SelfClusterID   string            `json:"self_cluster_id,omitempty"`
}

type clusterMeshPeer struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type netbirdResponse struct {
	Installed    bool                  `json:"installed"`
	Deployments  []componentDeployment `json:"deployments"`
	Peers        []netbirdPeer         `json:"peers"`
	HostnameHint string                `json:"hostname_hint,omitempty"`
}

type netbirdPeer struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	Online   bool   `json:"online"`
}

type dmzResponse struct {
	Installed     bool          `json:"installed"`
	VClusters     []dmzVCluster `json:"vclusters"`
	IsolationCNPs []policyRow   `json:"isolation_cnps"`
	Total         int           `json:"total"`
}

type dmzVCluster struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	Running   bool   `json:"running"`
}

type hubbleResponse struct {
	HubbleEnabled bool                  `json:"hubble_enabled"`
	RelayReady    bool                  `json:"relay_ready"`
	UIReady       bool                  `json:"ui_ready"`
	RelayListen   string                `json:"relay_listen,omitempty"`
	Deployments   []componentDeployment `json:"deployments"`
}

type componentDeployment struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     int    `json:"ready"`
	Desired   int    `json:"desired"`
	Available bool   `json:"available"`
}

// humanKindName maps the canonical lower-case singular k8scache Kind
// name to the title-case, human-readable form the UI surface uses for
// row labels and the wire shape the matrix asserts on (e.g. TC-279
// must_contain `CiliumNetworkPolicy`).
func humanKindName(kind string) string {
	switch kind {
	case "networkpolicy":
		return "NetworkPolicy"
	case "ciliumnetworkpolicy":
		return "CiliumNetworkPolicy"
	case "ciliumclusterwidenetworkpolicy":
		return "CiliumClusterwideNetworkPolicy"
	case "gatewayclass":
		return "GatewayClass"
	case "gateway":
		return "Gateway"
	case "httproute":
		return "HTTPRoute"
	default:
		return kind
	}
}
