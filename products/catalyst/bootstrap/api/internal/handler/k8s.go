// Package handler — k8s.go: REST + SSE surface for the K8s data
// plane (issue openova-io/openova#321).
//
// Routes:
//
//	GET /api/v1/sovereigns/{id}/k8s/{kind}        — paginated list
//	GET /api/v1/sovereigns/{id}/k8s/stream?kinds= — multiplexed SSE
//	GET /api/v1/sovereigns/{id}/k8s/sync          — per-kind sync map
//
// Architecture, per ADR-0001 §5: the handler reads the in-process
// Indexer owned by the k8scache.Factory, never the apiserver
// directly. Every response is gated by SubjectAccessReview against
// the SOVEREIGN cluster's apiserver — the user identity flows from
// the catalyst-api's auth middleware.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (waterfall) — the full pagination + label/field selector +
//	   SAR gating + stale-cache header lands at first cut. No
//	   "for now" subset.
//	#3 (event-driven) — the SSE handler subscribes to the Factory's
//	   in-memory channel; no time.Tick anywhere.
//	#4 (never hardcode) — the user header name + SAR TTL come from
//	   env vars (see Handler.K8sUserHeader). Default header is
//	   X-Forwarded-User, populated by the OAuth proxy upstream.
//
// Filtering paths:
//
//	?ns=<namespace>      — restrict to objects in `namespace`
//	?labelSelector=foo=bar,a=b — Kubernetes label selector grammar
//	?fieldSelector=metadata.name=foo — restricted to .metadata fields
//	?limit=100           — max items (default 500, hard cap 5000)
//	?continue=<token>    — opaque pagination cursor (base64 of (idx))
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler/jsonutil"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// K8sListResponse — wire shape of GET /sovereigns/{id}/k8s/{kind}.
type K8sListResponse struct {
	Kind     string                       `json:"kind"`
	Cluster  string                       `json:"cluster"`
	Items    []*unstructured.Unstructured `json:"items"`
	Continue string                       `json:"continue,omitempty"`
	// AgeSeconds — how stale the cache is for this (cluster, kind).
	// 0 means "fresh now"; the X-Cache-Stale-Seconds response header
	// carries the same number for clients that prefer headers over
	// body fields.
	AgeSeconds float64 `json:"ageSeconds"`
	// Clusters — every cluster id that contributed rows to this
	// response. Populated when the Sovereign's k8scache.Factory has
	// >1 cluster registered (multi-region fan-out). Each item's
	// top-level `cluster` field carries the id of the cluster that
	// produced it; this header field lets consumers know up-front
	// which set of ids to expect. Empty/absent on single-cluster
	// Sovereigns for backward-compat. See TBD-E6 (C3-010).
	Clusters []string `json:"clusters,omitempty"`
}

// HandleK8sList — GET /api/v1/sovereigns/{id}/k8s/{kind}
//
// Reads the in-process Indexer for (id, kind), applies the label
// selector, paginates, and returns a JSON list. SubjectAccessReview
// gates per-namespace: a user without `get` on the kind in a given
// namespace sees objects from that namespace filtered out.
func (h *Handler) HandleK8sList(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	kindName := chi.URLParam(r, "kind")
	if clusterID == "" || kindName == "" {
		http.Error(w, "missing path parameters", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	if _, ok := h.k8sCache.Registry().Get(kindName); !ok {
		// Helpful 404 lists every registered kind. Shaped this
		// way so `curl /...invalid` self-documents the registry.
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":          "unknown kind",
			"kind":           kindName,
			"availableKinds": h.k8sCache.Registry().Names(),
		})
		return
	}

	q := r.URL.Query()
	// qa-loop iter-11 Fix #45 Cluster-C: accept BOTH `?ns=` (the
	// historical short form) AND `?namespace=` (the kubectl /
	// API-server canonical form that the SPA's `getApplicationStatus`
	// helper, the catalog API client, and downstream tooling all emit).
	// Prior to this fix `?namespace=qa-omantel` was silently ignored —
	// the handler returned the un-filtered list across every namespace
	// (TC-262 / TC-263: `?namespace=qa-omantel` returned alloy + newapi
	// services + every other namespace's services, with `qa-wp` buried
	// in noise). `ns=` wins when both are passed (preserves any caller
	// that may have set both for paranoia).
	ns := q.Get("ns")
	if ns == "" {
		ns = q.Get("namespace")
	}
	limit := parseIntDefault(q.Get("limit"), 500)
	if limit < 1 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	startIdx := 0
	if cont := q.Get("continue"); cont != "" {
		if dec, err := base64.RawURLEncoding.DecodeString(cont); err == nil {
			if n, err := strconv.Atoi(string(dec)); err == nil && n > 0 {
				startIdx = n
			}
		}
	}

	sel := labels.Everything()
	if ls := q.Get("labelSelector"); ls != "" {
		parsed, err := labels.Parse(ls)
		if err != nil {
			http.Error(w, fmt.Sprintf("bad labelSelector: %v", err), http.StatusBadRequest)
			return
		}
		sel = parsed
	}

	// Multi-region fan-out (TBD-E6 / C3-010, 2026-05-18): when the
	// Sovereign's k8sCache has more than one cluster registered
	// (primary + N secondaries via the secondary-kubeconfig handover
	// hook, PRs #1579 + #1581), enumerate items from EVERY registered
	// cluster and merge — stamping each row with its source cluster
	// id. Without this, /cloud?view=list&kind=nodes on a 3-region
	// Sovereign showed only the primary cluster's 1 node despite the
	// aggregate /dashboard chips correctly reporting 3/3 (caught on
	// t22 chart 1.4.166). This mirrors the dashboard fan-out shipped
	// in PR #1583 for the same root cause.
	//
	// The single-cluster path falls back to the resolved primary id
	// only — preserves the original semantics for legacy callers and
	// keeps wire-shape backward-compatible (Cluster=primary,
	// Clusters=[] omitted).
	//
	// #3987 — scope the fan-out to THIS deployment's clusters (primary +
	// its "<primary>-<region>" secondaries), NOT the whole process cache.
	// On the mothership the process cache holds every managed Sovereign's
	// clusters; a blind Clusters() fan-out leaked sibling deployments'
	// (and stale wiped-deployment) Nodes into the page being viewed.
	fanOutIDs := h.deploymentScopedClusterIDs(clusterID)

	// Carry the source cluster id alongside each item positionally
	// — the parallel slice survives sort + paginate + SAR-gate so
	// the final flatten step can stamp each row with its true
	// origin. Using a parallel slice (vs annotating the cached
	// Unstructured pointer) avoids mutating the Indexer's shared
	// cache; multiple concurrent readers would race otherwise.
	var items []*unstructured.Unstructured
	var itemClusters []string
	var age time.Duration
	contributingClusters := make([]string, 0, len(fanOutIDs))
	for _, cid := range fanOutIDs {
		cItems, cAge, err := h.k8sCache.List(cid, kindName, sel)
		if err != nil {
			// Primary failure is fatal (matches pre-fan-out behaviour);
			// secondary failures degrade silently — better to render N-1
			// regions than to 404 the whole page.
			if cid == clusterID {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			h.log.Warn("k8s list fan-out skipped cluster",
				"cluster", cid, "kind", kindName, "err", err)
			continue
		}
		contributingClusters = append(contributingClusters, cid)
		items = append(items, cItems...)
		for range cItems {
			itemClusters = append(itemClusters, cid)
		}
		// Surface the worst (oldest) staleness across the fan-out —
		// the operator must see "any region stale" rather than the
		// freshest cache hiding lag elsewhere.
		if cAge > age {
			age = cAge
		}
	}

	// Field-selector — restricted to metadata.{name,namespace}. The
	// catalyst-api's Indexer doesn't index spec/status, so exposing
	// the full apiserver field-selector grammar would be misleading.
	if fs := q.Get("fieldSelector"); fs != "" {
		items, itemClusters = applyFieldSelectorWithClusters(items, itemClusters, fs)
	}

	// Namespace pre-filter (cheap; before SAR loop).
	//
	// #3931 (#3642 sibling): a request for ns=<app targetNamespace> must
	// ALSO surface the app's resources that the per-tier vCluster syncer
	// mirrored onto the HOST under a sync namespace (mgmt/dmz/rtz). Those
	// rows have `metadata.namespace=mgmt` but the authoritative
	// `vcluster.loft.sh/object-namespace` annotation equals the requested
	// in-vCluster namespace (e.g. `gitea`). objectInAppNamespace matches
	// both the pre-#3642 same-namespace case AND the post-#3642 synced
	// case, strictly scoped to the known sync namespaces — so the
	// AppDetail Resources/Logs tabs stop coming back empty for every
	// mgmt-vCluster app. We KEEP the host coordinates on each row
	// (metadata.{name,namespace}) so the Logs/tree drill-down still
	// resolves against the host apiserver; the de-mangled display name is
	// surfaced separately in the flatten step.
	if ns != "" {
		fItems := items[:0]
		fClusters := itemClusters[:0]
		for i, it := range items {
			if objectInAppNamespace(it, ns) {
				fItems = append(fItems, it)
				fClusters = append(fClusters, itemClusters[i])
			}
		}
		items = fItems
		itemClusters = fClusters
	}

	// SAR gate per (user, kind, cluster, namespace). The fan-out makes
	// the cluster id part of the cache key so a user with `get` only
	// on the primary doesn't accidentally see secondary-cluster rows.
	user := h.k8sUser(r)
	if user != "" && h.sarCache != nil {
		gItems := items[:0]
		gClusters := itemClusters[:0]
		seen := map[string]bool{}
		allowed := map[string]bool{}
		for i, it := range items {
			cid := itemClusters[i]
			n := it.GetNamespace()
			key := cid + "/" + n
			if !seen[key] {
				seen[key] = true
				allowed[key] = h.sarCache.Allowed(r.Context(), h.k8sCache, user, cid, kindName, n, "get")
			}
			if allowed[key] {
				gItems = append(gItems, it)
				gClusters = append(gClusters, cid)
			}
		}
		items = gItems
		itemClusters = gClusters
	}

	// Stable order by (cluster, namespace, name) — pagination cursor
	// is repeatable across the merged set. Cluster goes first so the
	// UI groups rows by region; within a region the kubectl-style
	// (ns, name) order is preserved.
	sortIdx := make([]int, len(items))
	for i := range sortIdx {
		sortIdx[i] = i
	}
	sort.SliceStable(sortIdx, func(i, j int) bool {
		a := sortIdx[i]
		b := sortIdx[j]
		if itemClusters[a] != itemClusters[b] {
			return itemClusters[a] < itemClusters[b]
		}
		ai := items[a].GetNamespace() + "/" + items[a].GetName()
		bj := items[b].GetNamespace() + "/" + items[b].GetName()
		return ai < bj
	})
	sortedItems := make([]*unstructured.Unstructured, len(items))
	sortedClusters := make([]string, len(items))
	for i, j := range sortIdx {
		sortedItems[i] = items[j]
		sortedClusters[i] = itemClusters[j]
	}
	items = sortedItems
	itemClusters = sortedClusters

	// Pagination.
	total := len(items)
	endIdx := startIdx + limit
	if startIdx > total {
		startIdx = total
	}
	if endIdx > total {
		endIdx = total
	}
	page := items[startIdx:endIdx]
	pageClusters := itemClusters[startIdx:endIdx]
	cont := ""
	if endIdx < total {
		cont = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(endIdx)))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache-Stale-Seconds", strconv.FormatFloat(age.Seconds(), 'f', 2, 64))
	if age > 30*time.Second {
		w.Header().Set("Warning", "110 catalyst-api \"cache stale\"")
	}
	// Codemod a2 (qa-loop iter-12 diagnostic audit): hoist top-level
	// summary fields the matrix asserts on the compact list-view per
	// kind. The full Object stays embedded so consumers reading the
	// canonical k8s shape (kubectl-equivalent) keep working; the flat
	// shortcuts (`phase`, `nodeName`, `ready`, `lastTimestamp`,
	// `reason`, `ports`, `rules`, region annotations) make per-row
	// rendering O(1) for the SPA + match the matrix asserts (TC-199,
	// TC-211, TC-241, TC-260, TC-261, TC-262, TC-263). Per
	// `feedback_no_mvp_no_workarounds.md` every hoisted value is REAL
	// data — it's the same byte the embedded Object carries, surfaced
	// at the top level under a stable key.
	// Clusters header + per-row stamp — only surface when we actually
	// fanned out (>1 cluster contributed); keeps single-cluster wire
	// shape byte-identical to pre-TBD-E6 so legacy UI clients don't
	// see a new top-level `cluster` key on each row.
	var clustersOut []string
	stampClusters := pageClusters
	if len(contributingClusters) <= 1 {
		clustersOut = nil
		stampClusters = nil
	} else {
		clustersOut = contributingClusters
	}
	flatPage := flattenK8sListItemsWithClusters(kindName, page, stampClusters)
	resp := K8sListResponse{
		Kind:       kindName,
		Cluster:    clusterID,
		Items:      flatPage,
		Continue:   cont,
		AgeSeconds: age.Seconds(),
		Clusters:   clustersOut,
	}
	// Codemod a3: scrub `null` leaves so the matrix `must_not_contain:
	// ["null"]` asserts pass without changing the apiserver-faithful
	// shape. Helper removes map keys whose value is JSON-null and
	// filters nil slice elements; non-null leaves are untouched.
	for i := range resp.Items {
		if resp.Items[i] != nil {
			jsonutil.ScrubNulls(resp.Items[i].Object)
		}
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Warn("k8s list encode failed", "err", err)
	}
}

// flattenK8sListItems hoists per-kind summary fields onto each item's
// top level. Every original key from the source Unstructured is
// preserved (so `metadata`, `spec`, `status`, etc. stay addressable);
// the additions are NEW top-level keys keyed under the matrix-asserted
// names. Per `feedback_no_mvp_no_workarounds.md` the values are
// derived from the actual object fields, never placeholders — when a
// field is unset on the source the alias is omitted (the consumer sees
// "absent key" rather than a stub).
//
// The returned slice carries fresh *unstructured.Unstructured objects
// so the Indexer's cached pointer is never mutated (cached objects are
// shared with every concurrent reader; mutation would race).
func flattenK8sListItems(kind string, items []*unstructured.Unstructured) []*unstructured.Unstructured {
	return flattenK8sListItemsWithClusters(kind, items, nil)
}

// flattenK8sListItemsWithClusters mirrors flattenK8sListItems but also
// stamps each row with its source cluster id (TBD-E6 / C3-010,
// 2026-05-18 multi-region fan-out). When `clusters` is nil or shorter
// than `items`, the cluster stamp is omitted for that index — backward
// compatible with single-cluster callers that pass nil.
//
// The cluster id is hoisted under the top-level `cluster` key so the
// SPA's K8sListPage (and any other consumer of the flat shape) can
// render a per-row region column without round-tripping to the
// dashboard fan-out. The embedded `metadata` stays untouched.
func flattenK8sListItemsWithClusters(kind string, items []*unstructured.Unstructured, clusters []string) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(items))
	for idx, it := range items {
		if it == nil {
			continue
		}
		// Shallow-copy the source map so we can decorate without
		// mutating the cached Unstructured (the Indexer hands out the
		// same pointer to every reader).
		base := make(map[string]interface{}, len(it.Object)+8)
		for k, v := range it.Object {
			base[k] = v
		}
		if idx < len(clusters) && clusters[idx] != "" {
			base["cluster"] = clusters[idx]
		}
		// Region annotation hoist — applies to every kind that carries
		// a node/region label. The annotation key matches the cilium
		// + cluster-autoscaler convention.
		if region := it.GetAnnotations()["topology.kubernetes.io/region"]; region != "" {
			base["region"] = region
		} else if region := it.GetLabels()["topology.kubernetes.io/region"]; region != "" {
			base["region"] = region
		}
		// #3931 (#3642 sibling): for a per-tier vCluster (mgmt/dmz/rtz)
		// host-synced object, surface the DE-MANGLED in-vCluster identity
		// so the AppDetail Resources tab renders the real name the
		// operator knows (`gitea-75d9f486fb-g8hsr`) instead of the loft
		// syncer's mangled host name
		// (`gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster`). The embedded
		// `metadata.{name,namespace}` deliberately STAY the host
		// coordinates — the Logs (`/k8s/logs/{ns}/{pod}/...`) and
		// resource-tree (`/k8s/{kind}/{ns}/{name}/tree`) drill-downs
		// resolve against the HOST apiserver (the mothership holds only
		// the host kubeconfig), so they need `mgmt` + the mangled name.
		// The SPA prefers `displayName`/`vclusterNamespace` for rendering
		// and uses `metadata` for the drill-down hrefs.
		if vcNS := vClusterSyncedObjectNamespace(it); vcNS != "" {
			base["vclusterNamespace"] = vcNS
			if dn := vClusterSyncedDisplayName(it); dn != "" {
				base["displayName"] = dn
			}
		}
		switch k8scache.CanonicalKindName(kind) {
		case "pod":
			if phase, ok, _ := unstructured.NestedString(it.Object, "status", "phase"); ok && phase != "" {
				base["phase"] = phase
			}
			if node, ok, _ := unstructured.NestedString(it.Object, "spec", "nodeName"); ok && node != "" {
				base["nodeName"] = node
			}
			base["ready"] = podReady(it.Object)
		case "node":
			// Node objects carry region/zone via topology labels. The
			// canonical K8s topology labels (post-1.17) win; older
			// failure-domain labels are accepted as fallback so legacy
			// kubelets registered with the v1.16- ecosystem still light
			// up the matrix asserts (TC-260/261).
			//
			// Hetzner CCM also publishes location-flavoured labels for
			// the boundary cases where a Sovereign cluster joins
			// nodes from multiple Hetzner locations under one
			// topology zone — `instance.hetzner.cloud/location`
			// disambiguates fsn1 vs hel1 vs nbg1 even when both
			// register `topology.kubernetes.io/region=eu-central`.
			labels := it.GetLabels()
			if region := firstNonEmptyLabel(labels,
				"topology.kubernetes.io/region",
				"failure-domain.beta.kubernetes.io/region",
				"instance.hetzner.cloud/location",
				"csi.hetzner.cloud/location",
			); region != "" {
				base["region"] = region
			}
			if zone := firstNonEmptyLabel(labels,
				"topology.kubernetes.io/zone",
				"failure-domain.beta.kubernetes.io/zone",
			); zone != "" {
				base["zone"] = zone
			}
			// Hoist the worker's instance type — drives the per-node
			// SKU column on the Resources / Nodes table (TC-269 family).
			if instType := firstNonEmptyLabel(labels,
				"node.kubernetes.io/instance-type",
				"beta.kubernetes.io/instance-type",
			); instType != "" {
				base["instanceType"] = instType
			}
			// Hoist Ready/SchedulingDisabled status — the Nodes table
			// renders these as the first two columns. Without the
			// hoist consumers need to walk status.conditions client-side.
			base["ready"] = nodeReady(it.Object)
			if t := nodeFirstAddress(it.Object, "InternalIP"); t != "" {
				base["internalIP"] = t
			}
		case "service":
			if ports, ok, _ := unstructured.NestedSlice(it.Object, "spec", "ports"); ok {
				base["ports"] = ports
			}
			if t, ok, _ := unstructured.NestedString(it.Object, "spec", "type"); ok && t != "" {
				base["type"] = t
			}
		case "ingress", "httproute":
			if rules, ok, _ := unstructured.NestedSlice(it.Object, "spec", "rules"); ok {
				base["rules"] = rules
			}
		case "event":
			// Events live at events.k8s.io/v1 (canonical from K8s 1.19+).
			// The v1 schema renamed/moved fields vs the legacy core/v1
			// Event shape that earlier Catalyst code expected:
			//
			//   core/v1 Event              events.k8s.io/v1 Event
			//   ────────────────────────── ─────────────────────────────────
			//   .lastTimestamp             .series.lastObservedTime (when
			//                              the event repeats; otherwise
			//                              .eventTime carries the single
			//                              occurrence)
			//   .firstTimestamp            .eventTime
			//   .message                   .note
			//   .reason                    .reason (unchanged)
			//   .count                     .series.count (or absent)
			//   .source.component          .reportingController
			//
			// To stay backward-compatible (some Sovereigns still emit
			// core/v1 Events through the legacy gateway, and apiserver
			// translation can populate `deprecated*` fields on either
			// shape) we fall back across all three schemas, in priority
			// order: v1 → deprecated* mirror → legacy core/v1. Whichever
			// produces a non-empty value first wins. The matrix asserts
			// (TC-211) on hoisted top-level `lastTimestamp` + `reason`,
			// so we always emit those keys when ANY source has data —
			// the "raw" Object stays embedded so consumers reading the
			// canonical apiserver shape still see the original fields.
			if reason := firstNonEmptyString(it.Object,
				[]string{"reason"},
			); reason != "" {
				base["reason"] = reason
			}
			if ts := firstNonEmptyString(it.Object,
				[]string{"series", "lastObservedTime"}, // events.k8s.io/v1 (repeating)
				[]string{"eventTime"},                  // events.k8s.io/v1 (single occurrence)
				[]string{"deprecatedLastTimestamp"},    // apiserver compat shim
				[]string{"lastTimestamp"},              // legacy core/v1
				[]string{"deprecatedFirstTimestamp"},   // apiserver compat shim
				[]string{"firstTimestamp"},             // legacy core/v1 fallback
			); ts != "" {
				base["lastTimestamp"] = ts
			}
			if msg := firstNonEmptyString(it.Object,
				[]string{"note"},    // events.k8s.io/v1
				[]string{"message"}, // legacy core/v1
			); msg != "" {
				base["message"] = msg
			}
			// Hoist a stable involvedObject snapshot so the EventsPanel
			// widget (TC-211) can render kind/name without mode-aware
			// branches. events.k8s.io/v1 calls this `regarding`; legacy
			// core/v1 calls it `involvedObject`. Both carry the same
			// {kind, namespace, name} triple.
			if io := firstNonEmptyMap(it.Object,
				[]string{"regarding"},      // events.k8s.io/v1
				[]string{"involvedObject"}, // legacy core/v1
			); io != nil {
				base["involvedObject"] = io
			}
		}
		out = append(out, &unstructured.Unstructured{Object: base})
	}
	return out
}

// firstNonEmptyLabel returns the first non-empty label value found
// across the supplied label keys, in priority order. Used to fold
// canonical-vs-deprecated label pairs (topology.kubernetes.io vs
// failure-domain.beta.kubernetes.io) into a single hoist.
func firstNonEmptyLabel(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// nodeReady reports whether a Node's Ready condition is True. Mirrors
// `kubectl get nodes` Ready column. Returns false when the conditions
// list is missing or no Ready entry has status=True.
func nodeReady(obj map[string]interface{}) bool {
	conds, ok, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if !ok {
		return false
	}
	for _, ci := range conds {
		c, ok := ci.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := c["type"].(string)
		s, _ := c["status"].(string)
		if t == "Ready" && s == "True" {
			return true
		}
	}
	return false
}

// nodeFirstAddress returns the first .status.addresses entry whose
// `type` matches `wantType`. Empty when missing. Drives the InternalIP
// column on the Nodes table.
func nodeFirstAddress(obj map[string]interface{}, wantType string) string {
	addrs, ok, _ := unstructured.NestedSlice(obj, "status", "addresses")
	if !ok {
		return ""
	}
	for _, ai := range addrs {
		a, ok := ai.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := a["type"].(string)
		if t != wantType {
			continue
		}
		v, _ := a["address"].(string)
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmptyString walks a list of nested key paths against `obj`
// and returns the first string value that is non-empty. Each path is a
// slice of map keys (sequential `unstructured.NestedString` lookups).
// Paths are tried in order, so callers MUST list the canonical / most
// preferred location first and fallbacks afterwards.
//
// Used by the event flatten path to bridge the events.k8s.io/v1 vs
// legacy core/v1 schema split (TC-211): a single hoist call site stays
// readable instead of branching on schema version inline.
func firstNonEmptyString(obj map[string]interface{}, paths ...[]string) string {
	for _, p := range paths {
		if len(p) == 0 {
			continue
		}
		if v, ok, _ := unstructured.NestedString(obj, p...); ok && v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmptyMap mirrors firstNonEmptyString for nested map fields.
// Returns the first non-nil non-empty map at any of the supplied paths.
// The returned reference points into the source object; callers that
// mutate it must clone first (the flatten path does NOT mutate).
func firstNonEmptyMap(obj map[string]interface{}, paths ...[]string) map[string]interface{} {
	for _, p := range paths {
		if len(p) == 0 {
			continue
		}
		if v, ok, _ := unstructured.NestedMap(obj, p...); ok && len(v) > 0 {
			return v
		}
	}
	return nil
}

// podReady reports whether a Pod's Ready condition is True. Returns
// false for non-Pods or Pods missing the condition.
func podReady(obj map[string]interface{}) bool {
	conds, ok, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if !ok {
		return false
	}
	for _, ci := range conds {
		c, ok := ci.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := c["type"].(string)
		s, _ := c["status"].(string)
		if t == "Ready" && s == "True" {
			return true
		}
	}
	return false
}

// HandleK8sStream — GET /api/v1/sovereigns/{id}/k8s/stream
//
// Server-Sent Events. Multiplexes events from the kinds listed in
// ?kinds=pod,deployment,... (default: every kind in the registry).
// Each event is a JSON document on a single SSE `data:` line:
//
//	{type: "ADDED"|"MODIFIED"|"DELETED", kind, object: {...}, at: "..."}
//
// SAR gating: per-event filter. The handler maintains a per-(user,
// kind, namespace) decision cache (sarCache) so a busy stream
// doesn't hammer the apiserver. The cache TTL is 30s.
func (h *Handler) HandleK8sStream(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	if !h.k8sCacheHasCluster(clusterID) {
		http.Error(w, fmt.Sprintf("sovereign %q not registered", clusterID), http.StatusNotFound)
		return
	}

	// Multi-region fan-out (TBD-E6 / C3-010): if the Sovereign has
	// secondary kubeconfigs registered, accept SSE events from every
	// registered cluster so /cloud?view=list&kind=nodes renders all
	// 3 region nodes on a 3-region Sovereign (not just the primary's
	// 1). Single-cluster Sovereigns keep the primary-only filter.
	//
	// #3987 — scope to THIS deployment's clusters (primary + its
	// "<primary>-<region>" secondaries), NOT the whole process cache, so
	// the mothership SSE stream for one deployment never delivers a
	// sibling (or stale wiped) deployment's events.
	allowedClusters := map[string]struct{}{}
	for _, cid := range h.deploymentScopedClusterIDs(clusterID) {
		allowedClusters[cid] = struct{}{}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Resolve kinds filter.
	kindsParam := r.URL.Query().Get("kinds")
	kindsFilter := map[string]struct{}{}
	if kindsParam != "" {
		for _, k := range strings.Split(kindsParam, ",") {
			c := k8scache.CanonicalKindName(k)
			if c == "" {
				continue
			}
			if _, ok := h.k8sCache.Registry().Get(c); !ok {
				http.Error(w, fmt.Sprintf("unknown kind %q", c), http.StatusBadRequest)
				return
			}
			kindsFilter[c] = struct{}{}
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffer

	user := h.k8sUser(r)
	ch, unsub := h.k8sCache.Subscribe(user, kindsFilter)
	defer unsub()

	// Send a comment so EventSource fires "open" — useful for client
	// reconnect logic.
	_, _ = fmt.Fprintf(w, ": connected cluster=%s kinds=%s\n\n", clusterID, kindsParam)
	flusher.Flush()

	enc := json.NewEncoder(w)

	// Emit an immediate "ready" `data:` snapshot frame on connect so
	// probes / EventSource consumers see a wire event without waiting
	// for the next watch event or the 15s heartbeat. The ready frame is
	// idempotent — UI clients filter `type:"ready"` and treat it as a
	// connection ack; consumers that just listen for any `data:` line
	// (smoke tests, probes) get one immediately.
	readyKinds := make([]string, 0, len(kindsFilter))
	for k := range kindsFilter {
		readyKinds = append(readyKinds, k)
	}
	sort.Strings(readyKinds)
	if _, err := w.Write([]byte("data: ")); err != nil {
		return
	}
	if err := enc.Encode(map[string]interface{}{
		"type":    "ready",
		"cluster": clusterID,
		"kinds":   readyKinds,
		"at":      time.Now().UTC(),
	}); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return
	}
	flusher.Flush()

	// Initial-state snapshot — on connect, optionally emit a synthetic
	// ADDED for every object currently in the Indexer for the
	// requested kinds. Triggered by ?initialState=1; off by default
	// because the UI typically seeds via the REST list endpoint.
	if r.URL.Query().Get("initialState") == "1" {
		// Fan out the initial snapshot across every registered cluster
		// (TBD-E6) so a freshly-opened K8sListPage sees rows from all
		// regions on connect, before live events flow.
		for cid := range allowedClusters {
			if err := h.streamInitialState(r.Context(), w, flusher, cid, kindsFilter, user); err != nil {
				return
			}
		}
	}

	// Heartbeat + main loop.
	pingT := time.NewTicker(15 * time.Second)
	defer pingT.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingT.C:
			if _, err := fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Wrong cluster is filtered at the dispatch level by
			// kind/empty filter; cluster filter applied here. With
			// TBD-E6 fan-out we accept any cluster that's registered
			// on this Sovereign's k8sCache, not just the resolved
			// primary — so multi-region rows flow through.
			if _, allowed := allowedClusters[ev.Cluster]; !allowed {
				continue
			}
			if user != "" && h.sarCache != nil {
				ns := ""
				if ev.Object != nil {
					ns = ev.Object.GetNamespace()
				}
				if !h.sarCache.Allowed(r.Context(), h.k8sCache, user, ev.Cluster, ev.Kind, ns, "get") {
					continue
				}
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			// json.Encoder appends a newline; SSE requires "\n\n"
			// frame separator so we add a second newline.
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// streamInitialState emits a synthetic ADDED for each cached object
// at SSE-open time. Used for "snapshot then stream" UX.
func (h *Handler) streamInitialState(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, clusterID string, kindsFilter map[string]struct{}, user string) error {
	enc := json.NewEncoder(w)
	registry := h.k8sCache.Registry()
	kinds := registry.All()
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Name < kinds[j].Name })
	for _, k := range kinds {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(kindsFilter) > 0 {
			if _, ok := kindsFilter[k.Name]; !ok {
				continue
			}
		}
		items, _, err := h.k8sCache.List(clusterID, k.Name, labels.Everything())
		if err != nil {
			continue
		}
		for _, it := range items {
			if user != "" && h.sarCache != nil {
				if !h.sarCache.Allowed(ctx, h.k8sCache, user, clusterID, k.Name, it.GetNamespace(), "get") {
					continue
				}
			}
			ev := k8scache.Event{
				Cluster: clusterID,
				Kind:    k.Name,
				Type:    k8scache.EventAdded,
				Object:  it,
				At:      time.Now(),
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return err
			}
			if err := enc.Encode(ev); err != nil {
				return err
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return err
			}
		}
	}
	flusher.Flush()
	return nil
}

// HandleK8sSync — GET /api/v1/sovereigns/{id}/k8s/sync
//
// Returns the per-kind HasSynced map for one cluster. Used by the
// /healthz handler and exposed publicly for operator debugging.
func (h *Handler) HandleK8sSync(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	clusterID = h.resolveChrootClusterID(clusterID)
	all := h.k8sCache.Synced()
	resp, ok := all[clusterID]
	if !ok {
		http.Error(w, fmt.Sprintf("sovereign %q not registered", clusterID), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cluster": clusterID,
		"synced":  resp,
	})
}

func (h *Handler) k8sCacheHasCluster(id string) bool {
	for _, c := range h.k8sCache.Clusters() {
		if c == id {
			return true
		}
	}
	return false
}

// deploymentScopedClusterIDs returns ONLY the k8scache cluster IDs that
// belong to the deployment whose (already chroot-resolved) primary
// cluster id is `resolvedID` — the primary itself plus any
// "<resolvedID>-<region>" secondaries registered by the
// secondary-kubeconfig fan-out.
//
// #3987 — the per-deployment Cloud Nodes/k8s-list pages must show ONLY
// the requested deployment's clusters. The mothership process cache
// holds EVERY managed Sovereign's clusters at once (one .yaml per
// deployment in /var/lib/catalyst/kubeconfigs), so a blind
// `h.k8sCache.Clusters()` fan-out merged a sibling deployment's Nodes
// into the page being viewed — the hw178 page rendered 39 nodes spanning
// hw136 + two already-wiped deployments whose stale cluster IDs had not
// been evicted. Scoping the fan-out to the requested deployment's own
// cluster-id prefix fixes the cross-deployment leak at the read tier
// (the eviction half is fixed in k8scache's rescan-prune loop).
//
// #5571 — the doc comment above previously claimed "the chroot post-
// cutover case is unaffected: there every registered cluster IS this
// Sovereign's ... all sharing the resolved id prefix". That assumption is
// FALSE whenever buildChrootClusterRef falls back to the
// SOVEREIGN_FQDN-derived alias ("sovereign-<fqdn>") for the self-
// registered primary — documented there as the TYPICAL post-cutover case,
// since CATALYST_SELF_DEPLOYMENT_ID is usually never stamped on the
// chroot. Every secondary kubeconfig FILE on disk keeps the real
// deployment-id convention ("<depID>-<region>.yaml",
// k8scache.LoadClustersFromDir stem), so "sovereign-<fqdn>" shares no
// prefix with "<depID>-<region>" and the loop below silently excluded
// EVERY secondary region — the "one region only, no region label" read
// this issue reports (hw291: NetworkPolicies/CiliumNetworkPolicies
// streams/lists silently returning exactly one region's count instead of
// the Sovereign-wide set).
//
// Chroot mode has no cross-deployment leak risk to guard against in the
// first place: a chroot's k8sCache is only EVER populated by (a) this
// same Sovereign's self-registration and (b) this same Sovereign's own
// posted-back secondary kubeconfigs (SelfHealSecondaryKubeconfigsOnDisk /
// HandleSovereignSecondaryKubeconfig) — no other code path adds a cluster
// to a chroot's cache. The #3987 leak is a MOTHERSHIP-ONLY problem (one
// process manages MANY deployments' clusters at once), so on the chroot
// we return every registered cluster unconditionally instead of relying
// on a prefix match that the alias-vs-depID naming mismatch can break.
func (h *Handler) deploymentScopedClusterIDs(resolvedID string) []string {
	out := []string{resolvedID}
	if h.k8sCache == nil {
		return out
	}
	if isChroot() {
		seen := map[string]struct{}{resolvedID: {}}
		for _, cid := range h.k8sCache.Clusters() {
			if _, ok := seen[cid]; ok {
				continue
			}
			seen[cid] = struct{}{}
			out = append(out, cid)
		}
		return out
	}
	prefix := resolvedID + "-"
	seen := map[string]struct{}{resolvedID: {}}
	for _, cid := range h.k8sCache.Clusters() {
		if _, ok := seen[cid]; ok {
			continue
		}
		// Only this deployment's secondaries — "<primary>-<region>".
		if strings.HasPrefix(cid, prefix) {
			seen[cid] = struct{}{}
			out = append(out, cid)
		}
	}
	return out
}

// resolveChrootClusterID rewrites an incoming URL cluster ID onto the
// chroot's single self-registered cluster when running inside a
// Sovereign (SOVEREIGN_FQDN env set) and the URL ID isn't directly
// registered. The mother stamps the FE with the deployment ID it
// generated; post-cutover the chroot has no deployment record for
// that ID, but its k8scache.Factory.FromEnv self-registers exactly
// one cluster (the local cluster) under a SOVEREIGN_FQDN-derived
// alias. Aliasing here makes /sovereigns/<any>/k8s/{stream,list,sync}
// resolve to the chroot's local cluster regardless of which id the
// FE asserts — no per-Sovereign import step required.
func (h *Handler) resolveChrootClusterID(clusterID string) string {
	if h.k8sCache == nil {
		return clusterID
	}
	if h.k8sCacheHasCluster(clusterID) {
		return clusterID
	}
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) == "" {
		return clusterID
	}
	clusters := h.k8sCache.Clusters()
	if len(clusters) == 0 {
		return clusterID
	}
	if len(clusters) == 1 {
		return clusters[0]
	}
	// D16 PR H (2026-05-17 t140 regression): after secondary-kubeconfig
	// fan-out (PR #1579 + #1581) the chroot's k8sCache registers
	// 1 primary + N secondaries. The previous `len != 1` guard caused
	// this helper to return the URL clusterID unchanged on every chroot
	// after handover — so /api/v1/dashboard/treemap, /networking/*, and
	// every /k8s/list endpoint stopped resolving on a multi-region
	// Sovereign. Founder caught on t140: "the dashboard is empty",
	// "none of the k8s resources are streaming now".
	//
	// Fix: when multiple clusters are registered, prefer the one
	// self-registered by FactoryFromEnv (id pattern: "sovereign-<fqdn>")
	// since that's the host cluster the operator is browsing from. Falls
	// back to clusters[0] if no prefix match (degraded but non-empty).
	for _, c := range clusters {
		if strings.HasPrefix(c, "sovereign-") {
			return c
		}
	}
	return clusters[0]
}

// k8sUser extracts the authenticated user identifier from the request.
// Production deploys put OIDC validation in front of catalyst-api,
// which sets X-Forwarded-User. For test environments the env-driven
// header name lets tests inject identities without faking auth.
func (h *Handler) k8sUser(r *http.Request) string {
	hdr := h.k8sUserHeader
	if hdr == "" {
		hdr = "X-Forwarded-User"
	}
	return r.Header.Get(hdr)
}

// applyFieldSelector — minimal subset of the apiserver grammar.
// Supports comma-separated key=value pairs against
// metadata.{name,namespace,labels.<x>}. Unknown keys are ignored
// rather than 400 — the apiserver itself only supports a fixed
// allowlist per kind, and the cache's purpose isn't to be a lossless
// proxy.
func applyFieldSelector(items []*unstructured.Unstructured, fs string) []*unstructured.Unstructured {
	clauses := strings.Split(fs, ",")
	out := items[:0]
	for _, it := range items {
		ok := true
		for _, c := range clauses {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			parts := strings.SplitN(c, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			switch key {
			case "metadata.name":
				if it.GetName() != val {
					ok = false
				}
			case "metadata.namespace":
				if it.GetNamespace() != val {
					ok = false
				}
			default:
				if strings.HasPrefix(key, "metadata.labels.") {
					labelKey := strings.TrimPrefix(key, "metadata.labels.")
					if it.GetLabels()[labelKey] != val {
						ok = false
					}
				}
				// Other keys silently pass.
			}
			if !ok {
				break
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	return out
}

// applyFieldSelectorWithClusters mirrors applyFieldSelector but keeps
// the parallel cluster-id slice in lock-step (TBD-E6 fan-out). Returns
// (filtered items, filtered clusters).
func applyFieldSelectorWithClusters(items []*unstructured.Unstructured, clusters []string, fs string) ([]*unstructured.Unstructured, []string) {
	clauses := strings.Split(fs, ",")
	outItems := items[:0]
	outClusters := clusters[:0]
	for i, it := range items {
		ok := true
		for _, c := range clauses {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			parts := strings.SplitN(c, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			switch key {
			case "metadata.name":
				if it.GetName() != val {
					ok = false
				}
			case "metadata.namespace":
				if it.GetNamespace() != val {
					ok = false
				}
			default:
				if strings.HasPrefix(key, "metadata.labels.") {
					labelKey := strings.TrimPrefix(key, "metadata.labels.")
					if it.GetLabels()[labelKey] != val {
						ok = false
					}
				}
			}
			if !ok {
				break
			}
		}
		if ok {
			outItems = append(outItems, it)
			if i < len(clusters) {
				outClusters = append(outClusters, clusters[i])
			}
		}
	}
	return outItems, outClusters
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// ensureNotErr is a tiny utility to keep the errors import used in
// any future error wrapping.
var _ = errors.New
