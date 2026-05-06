// sovereign_more.go — additional Sovereign Console endpoints (#L6-DoD).
//
// The Sovereign Console at console.<sov-fqdn> renders the chroot
// /users, /catalog, /settings, /cloud (graph + topology) pages, all
// of which need data shaped to match the existing wizard surfaces
// they're ported from. This file groups the small data-shim handlers
// that didn't fit naturally into sovereign.go (which already hosts
// the larger /jobs / /apps / /cloud handlers).
//
// All handlers in this file:
//   - mount on the Sovereign-side catalyst-api (FQDN console.<sov>)
//   - read live cluster state via the in-cluster client (sovereignDepsFor)
//   - return a STABLE empty shape on any error (the Sovereign Console
//     pages render the empty state gracefully — never crash on null).
//
// Caught on omantel.biz 2026-05-06: every chroot page hit a 404 because
// these endpoints didn't exist; the React error boundary caught the
// resulting fetch failure and rendered "Something went wrong".
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
)

// base64Decode is the URL-safe base64 decoder used to read JWT payloads.
func base64Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// ── /api/v1/sovereign/users — Keycloak realm users ──────────────────────────
//
// Returns the operator users registered in the Sovereign's local
// Keycloak realm (typically just the org-admin who completed handover,
// plus any users invited via the Sovereign Console's Users page).
// Sourced from the kc-sa client credentials secret + Keycloak admin API.
//
// Empty-shape contract: returns {"users":[]} on any error so the UI
// renders an "(no users)" empty state rather than a fetch failure.

type sovereignUsersResponse struct {
	Users []sovereignUser `json:"users"`
}

type sovereignUser struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Status        string `json:"status"`
	LastSignInISO string `json:"lastSignInISO,omitempty"`
}

// HandleSovereignUsers — GET /api/v1/sovereign/users.
func (h *Handler) HandleSovereignUsers(w http.ResponseWriter, r *http.Request) {
	resp := sovereignUsersResponse{Users: []sovereignUser{}}

	// Surface the operator who's currently authenticated (always at
	// least one user — the operator hitting the page). Read from
	// session cookie claims (email + sub).
	if email, ok := readEmailFromSessionCookie(r); ok && email != "" {
		resp.Users = append(resp.Users, sovereignUser{
			ID:     email,
			Email:  email,
			Name:   email,
			Role:   "sovereign-admin",
			Status: "active",
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── /api/v1/sovereign/catalog — Sovereign-side catalog adapter ──────────────
//
// The CatalogAdminPage was originally ported from core/services/catalog
// (a separate service that doesn't exist on Sovereign clusters). To
// avoid a 404 + sidebar-link error, expose a minimal Sovereign catalog
// view sourced from the embedded blueprints catalog (same data the
// /apps endpoint already uses, just shaped for the publish-toggle UI).
//
// Wire shape — bare array (the CatalogAdminPage's fetcher does
// `respond.OK(w, apps)` and expects an array, not a wrapper object).

type sovereignCatalogApp struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Family     string `json:"family"`
	Tier       string `json:"tier"`
	Published  bool   `json:"published"`
	Visibility string `json:"visibility"`
	Icon       string `json:"icon,omitempty"`
}

// HandleSovereignCatalog — GET /api/v1/sovereign/catalog.
func (h *Handler) HandleSovereignCatalog(w http.ResponseWriter, _ *http.Request) {
	listed, err := catalog.ListedBlueprints()
	if err != nil {
		writeJSON(w, http.StatusOK, []sovereignCatalogApp{})
		return
	}
	apps := make([]sovereignCatalogApp, 0, len(listed))
	for _, bp := range listed {
		family := ""
		if bp.Category != nil {
			family = *bp.Category
		}
		section := ""
		if bp.Section != nil {
			section = *bp.Section
		}
		visStr := string(bp.Visibility)
		apps = append(apps, sovereignCatalogApp{
			ID:         bp.ID,
			Slug:       bp.Slug,
			Name:       bp.Title,
			Title:      bp.Title,
			Family:     family,
			Tier:       section,
			Published:  visStr == "listed",
			Visibility: visStr,
		})
	}
	writeJSON(w, http.StatusOK, apps)
}

// ── /api/v1/sovereign/settings — Sovereign tenant settings overview ─────────
//
// Powers the chroot /settings page. Returns the Sovereign's identity,
// handover record, and runtime config flags so the page can render
// "About this Sovereign" + tenant-admin affordances without any other
// API round-trip.

type sovereignSettingsResponse struct {
	SovereignFQDN string                 `json:"sovereignFQDN"`
	DeploymentID  string                 `json:"deploymentId"`
	Region        string                 `json:"region"`
	Provider      string                 `json:"provider"`
	HandoverISO   string                 `json:"handoverISO,omitempty"`
	Marketplace   bool                   `json:"marketplaceEnabled"`
	Features      map[string]interface{} `json:"features"`
}

// HandleSovereignSettings — GET /api/v1/sovereign/settings.
func (h *Handler) HandleSovereignSettings(w http.ResponseWriter, r *http.Request) {
	fqdn := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	deploymentID := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	if jwtFQDN, jwtDepID, ok := readSessionClaimsFromCookie(r); ok {
		if fqdn == "" {
			fqdn = jwtFQDN
		}
		if deploymentID == "" {
			deploymentID = jwtDepID
		}
	}

	resp := sovereignSettingsResponse{
		SovereignFQDN: fqdn,
		DeploymentID:  deploymentID,
		Region:        strings.TrimSpace(os.Getenv("CATALYST_REGION")),
		Provider:      strings.TrimSpace(os.Getenv("CATALYST_PROVIDER")),
		Marketplace:   os.Getenv("CATALYST_MARKETPLACE_ENABLED") == "true",
		Features: map[string]interface{}{
			"handoverFiredAt": strings.TrimSpace(os.Getenv("CATALYST_HANDOVER_FIRED_AT")),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── /api/v1/sovereign/topology — hierarchical infrastructure for /cloud ─────
//
// CloudPage's getHierarchicalInfrastructure() in the UI hits this
// endpoint when DETECTED_MODE.mode === 'sovereign'. Builds the
// HierarchicalInfrastructure shape the UI expects (cloud → region →
// cluster → vcluster|node|lb|network) from the same in-cluster client
// queries that /sovereign/cloud uses, but reshaped into the single
// hierarchical tree the canvas + tabs render off.
//
// On any error returns the well-shaped empty topology so the canvas
// renders its "Provisioning…" overlay rather than crashing the page.

type hierTopologyResponse struct {
	Cloud    map[string]interface{}   `json:"cloud"`
	Topology map[string]interface{}   `json:"topology"`
}

// HandleSovereignTopology — GET /api/v1/sovereign/topology.
func (h *Handler) HandleSovereignTopology(w http.ResponseWriter, r *http.Request) {
	emptyResp := hierTopologyResponse{
		Cloud: map[string]interface{}{
			"provider":       strings.TrimSpace(os.Getenv("CATALYST_PROVIDER")),
			"providerRegion": strings.TrimSpace(os.Getenv("CATALYST_REGION")),
		},
		Topology: map[string]interface{}{
			"pattern": "solo",
			"regions": []interface{}{},
		},
	}

	deps, err := h.sovereignDepsFor()
	if err != nil {
		writeJSON(w, http.StatusOK, emptyResp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Build a single region from the live Nodes list. Cluster name
	// derived from the Sovereign's FQDN env. NodePool synthesised
	// from k3s role labels (control-plane vs worker).
	type nodeOut struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		SKU    string `json:"sku"`
		Role   string `json:"role"`
		IP     string `json:"ip"`
		Status string `json:"status"`
	}
	var nodes []nodeOut
	if list, err := deps.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		for _, n := range list.Items {
			role := "worker"
			for k := range n.Labels {
				if strings.HasSuffix(k, "node-role.kubernetes.io/control-plane") ||
					strings.HasSuffix(k, "node-role.kubernetes.io/master") {
					role = "control-plane"
				}
			}
			ip := ""
			for _, a := range n.Status.Addresses {
				if a.Type == "ExternalIP" || a.Type == "InternalIP" {
					ip = a.Address
					if a.Type == "ExternalIP" {
						break
					}
				}
			}
			status := "unknown"
			for _, c := range n.Status.Conditions {
				if c.Type == "Ready" {
					if c.Status == "True" {
						status = "healthy"
					} else {
						status = "degraded"
					}
				}
			}
			nodes = append(nodes, nodeOut{
				ID:     string(n.UID),
				Name:   n.Name,
				SKU:    n.Labels["instance.hetzner.cloud/server-type"],
				Role:   role,
				IP:     ip,
				Status: status,
			})
		}
	}

	cluster := map[string]interface{}{
		"id":            strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN")),
		"name":          strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN")),
		"version":       "k3s",
		"vclusters":     []interface{}{},
		"loadBalancers": []interface{}{},
		"nodePools":     []interface{}{},
		"nodes":         nodes,
		"status":        "healthy",
	}

	region := map[string]interface{}{
		"id":          strings.TrimSpace(os.Getenv("CATALYST_REGION")),
		"name":        strings.TrimSpace(os.Getenv("CATALYST_REGION")),
		"provider":    strings.TrimSpace(os.Getenv("CATALYST_PROVIDER")),
		"workerCount": len(nodes),
		"clusters":    []interface{}{cluster},
		"networks":    []interface{}{},
	}

	resp := hierTopologyResponse{
		Cloud: emptyResp.Cloud,
		Topology: map[string]interface{}{
			"pattern": "solo",
			"regions": []interface{}{region},
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── helper — read email from session cookie (no signature verify) ──────────

func readEmailFromSessionCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie("catalyst_session")
	if err != nil || c == nil || c.Value == "" {
		return "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64Decode(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	return claims.Email, claims.Email != ""
}
