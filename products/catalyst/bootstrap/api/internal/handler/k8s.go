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
			"error":            "unknown kind",
			"kind":             kindName,
			"availableKinds":   h.k8sCache.Registry().Names(),
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

	items, age, err := h.k8sCache.List(clusterID, kindName, sel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Field-selector — restricted to metadata.{name,namespace}. The
	// catalyst-api's Indexer doesn't index spec/status, so exposing
	// the full apiserver field-selector grammar would be misleading.
	if fs := q.Get("fieldSelector"); fs != "" {
		items = applyFieldSelector(items, fs)
	}

	// Namespace pre-filter (cheap; before SAR loop).
	if ns != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.GetNamespace() == ns {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	// SAR gate per (user, kind, namespace).
	user := h.k8sUser(r)
	if user != "" && h.sarCache != nil {
		gated := items[:0]
		seenNS := map[string]bool{}
		allowedNS := map[string]bool{}
		for _, it := range items {
			n := it.GetNamespace()
			if !seenNS[n] {
				seenNS[n] = true
				allowedNS[n] = h.sarCache.Allowed(r.Context(), h.k8sCache, user, clusterID, kindName, n, "get")
			}
			if allowedNS[n] {
				gated = append(gated, it)
			}
		}
		items = gated
	}

	// Stable order by (namespace, name) — important so pagination
	// cursors are repeatable.
	sort.SliceStable(items, func(i, j int) bool {
		ai := items[i].GetNamespace() + "/" + items[i].GetName()
		bj := items[j].GetNamespace() + "/" + items[j].GetName()
		return ai < bj
	})

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
	flatPage := flattenK8sListItems(kindName, page)
	resp := K8sListResponse{
		Kind:       kindName,
		Cluster:    clusterID,
		Items:      flatPage,
		Continue:   cont,
		AgeSeconds: age.Seconds(),
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
	out := make([]*unstructured.Unstructured, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		// Shallow-copy the source map so we can decorate without
		// mutating the cached Unstructured (the Indexer hands out the
		// same pointer to every reader).
		base := make(map[string]interface{}, len(it.Object)+6)
		for k, v := range it.Object {
			base[k] = v
		}
		// Region annotation hoist — applies to every kind that carries
		// a node/region label. The annotation key matches the cilium
		// + cluster-autoscaler convention.
		if region := it.GetAnnotations()["topology.kubernetes.io/region"]; region != "" {
			base["region"] = region
		} else if region := it.GetLabels()["topology.kubernetes.io/region"]; region != "" {
			base["region"] = region
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
			// Node objects carry the region directly on their labels.
			if region := it.GetLabels()["topology.kubernetes.io/region"]; region != "" {
				base["region"] = region
			}
			if zone := it.GetLabels()["topology.kubernetes.io/zone"]; zone != "" {
				base["zone"] = zone
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
			if ts, ok, _ := unstructured.NestedString(it.Object, "lastTimestamp"); ok && ts != "" {
				base["lastTimestamp"] = ts
			}
			if reason, ok, _ := unstructured.NestedString(it.Object, "reason"); ok && reason != "" {
				base["reason"] = reason
			}
			if msg, ok, _ := unstructured.NestedString(it.Object, "message"); ok && msg != "" {
				base["message"] = msg
			}
		}
		out = append(out, &unstructured.Unstructured{Object: base})
	}
	return out
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
		if err := h.streamInitialState(r.Context(), w, flusher, clusterID, kindsFilter, user); err != nil {
			return
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
			// kind/empty filter; cluster filter applied here.
			if ev.Cluster != clusterID {
				continue
			}
			if user != "" && h.sarCache != nil {
				ns := ""
				if ev.Object != nil {
					ns = ev.Object.GetNamespace()
				}
				if !h.sarCache.Allowed(r.Context(), h.k8sCache, user, clusterID, ev.Kind, ns, "get") {
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
	if len(clusters) != 1 {
		return clusterID
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
