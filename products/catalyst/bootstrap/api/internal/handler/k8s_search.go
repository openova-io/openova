// Package handler — k8s_search.go: cross-kind name-substring search.
//
// REST surface:
//
//	GET /api/v1/sovereigns/{id}/k8s/search?q=<substr>&kinds=<csv>
//
// Returns the canonical items envelope across every registered kind on
// the Sovereign cluster matching the case-insensitive name substring.
// Used by the Sovereign Console's command-palette + the qa-loop
// matrix's "find-this-Pod / Deployment / ConfigMap" rows (TC-265).
//
// Architecture rules:
//
//   - Per ADR-0001 §2.7 the underlying source is the per-cluster
//     Indexer cache the catalyst-api already maintains via k8scache —
//     no new informers, no apiserver round-trips per request.
//   - Per INVIOLABLE-PRINCIPLES.md #5 the SAR gate (canonical seam in
//     handler/k8s.go's HandleK8sList) is REUSED so a viewer-tier
//     caller never sees a resource they can't `get`.
//   - Result cap is intentionally low (default 100, max 500) so a wide
//     match doesn't dump megabytes; callers narrow via `kinds=` or by
//     adding more characters to `q=`.

package handler

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/labels"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// k8sSearchHit — one row in the search response.
type k8sSearchHit struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// k8sSearchResponse — body of GET /sovereigns/{id}/k8s/search. Canonical
// `{items, total}` envelope per the matrix contract.
type k8sSearchResponse struct {
	Items []k8sSearchHit `json:"items"`
	Total int            `json:"total"`
	Query string         `json:"query,omitempty"`
}

// HandleK8sSearch — GET /api/v1/sovereigns/{id}/k8s/search?q=<substr>
//
// qa-loop iter-9 Fix #43, Cluster-B (TC-265).
func (h *Handler) HandleK8sSearch(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		writeJSON(w, http.StatusOK, k8sSearchResponse{Items: []k8sSearchHit{}})
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		writeBadRequest(w, "missing-id", "sovereign id is required")
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		// Empty query → empty items envelope (matches the matrix
		// contract — no 400 for an empty search).
		writeJSON(w, http.StatusOK, k8sSearchResponse{
			Items: []k8sSearchHit{},
			Query: "",
		})
		return
	}

	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	// Restrict to a kinds CSV when supplied; default = every
	// registered kind on the cluster. Iterating every kind is fine
	// because the Indexer cache is in-memory and the loop is bounded
	// by the registry size (low tens).
	registry := h.k8sCache.Registry()
	var kinds []string
	if kindsQ := strings.TrimSpace(r.URL.Query().Get("kinds")); kindsQ != "" {
		for _, k := range strings.Split(kindsQ, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, ok := registry.Get(k); !ok {
				continue
			}
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		kinds = registry.Names()
	}

	user := h.k8sUser(r)

	hits := make([]k8sSearchHit, 0, limit)
	for _, kindName := range kinds {
		items, _, err := h.k8sCache.List(clusterID, kindName, labels.Everything())
		if err != nil {
			continue
		}
		for _, it := range items {
			name := it.GetName()
			if !strings.Contains(strings.ToLower(name), q) {
				continue
			}
			ns := it.GetNamespace()
			// SAR gate per (user, kind, namespace) — REUSE the canonical
			// seam from HandleK8sList. Anonymous (empty user) callers
			// fall through (SAR cache is bypassed on empty user).
			if user != "" && h.sarCache != nil {
				if !h.sarCache.Allowed(r.Context(), h.k8sCache, user, clusterID, kindName, ns, "get") {
					continue
				}
			}
			hits = append(hits, k8sSearchHit{
				Kind:      kindName,
				Name:      name,
				Namespace: ns,
			})
			if len(hits) >= limit {
				break
			}
		}
		if len(hits) >= limit {
			break
		}
	}

	// Stable ordering by (kind, namespace, name) so the response is
	// deterministic for repeat queries.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		if hits[i].Namespace != hits[j].Namespace {
			return hits[i].Namespace < hits[j].Namespace
		}
		return hits[i].Name < hits[j].Name
	})

	writeJSON(w, http.StatusOK, k8sSearchResponse{
		Items: hits,
		Total: len(hits),
		Query: q,
	})
}
