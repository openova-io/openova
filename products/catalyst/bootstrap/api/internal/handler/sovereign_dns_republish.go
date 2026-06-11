// sovereign_dns_republish.go — idempotent DNS re-publish for live Sovereigns.
//
// Why this file exists
// ────────────────────
// upsertSovereignParentZoneRecordsFromResult (sovereign_dns_records.go) is
// called ONCE — at Phase-0 success — and writes the A records for every
// CanonicalSovereignSubdomain into the parent zone.  When the canonical
// subdomain list grows (e.g. #3265 added "newapi" and "pdns-admin"), Sovereigns
// that were provisioned BEFORE the change stay with the old, shorter list.
// hw126 (deployment id c986326a77d391d4) was provisioned with 14 subdomains;
// #3265 raised it to 16.  The gap manifests as NXDOMAIN on newapi.<fqdn> and
// pdns-admin.<fqdn> even though the Cilium Gateway HTTPRoute is Accepted and
// the LB is healthy — the ONLY missing piece is the A record in the parent
// PowerDNS zone.
//
// This endpoint provides an operator-callable re-trigger so the operator does
// NOT need to re-provision (wipe + re-create) a live Sovereign just to pick up
// the new subdomain list.
//
// Endpoint
// ────────
//   POST /internal/deployments/{id}/republish-dns
//
// Auth model
// ──────────
// Sits inside the RequireSession chi.Group (same as every other /internal/*
// route).  No separate bearer token required — the signed-in mothership
// operator calling this already owns the deployment.  checkOwnership is called
// inside the handler per the pattern every other deployments/{id} endpoint
// follows.
//
// Input reconstruction
// ────────────────────
// The handler reads dep.Result.SovereignFQDN and dep.Result.LoadBalancerIP
// from the persisted Deployment record (on-disk store, survived Pod restarts)
// and dep.Request.ParentDomains for the parent zone list.  No external API
// calls are needed — every input survived in the deployment record exactly as
// it was written by the original provision run.
//
// Idempotency
// ──────────
// PowerDNS PatchRRSets with ChangeType=REPLACE is unconditionally idempotent:
// re-running the call with the same IP replaces the existing records byte-for-
// byte; re-running after an IP change (LB reprovisioned) correctly overwrites
// the stale record.  Safe to call multiple times.
//
// Response
// ────────
//   200 JSON:
//     {
//       "deploymentId": "c986326a77d391d4",
//       "sovereignFQDN": "hw126.omantel.biz",
//       "lbIP": "1.2.3.4",
//       "parentZones": ["omantel.biz"],
//       "published": ["console","auth",...,"newapi","pdns-admin"],
//       "publishedCount": 16
//     }
//
// Error responses follow the standard writeJSON shape (see handler.go).
package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// dnsRepublishResponse is the JSON body returned on success.
type dnsRepublishResponse struct {
	DeploymentID   string   `json:"deploymentId"`
	SovereignFQDN  string   `json:"sovereignFQDN"`
	LBIP           string   `json:"lbIP"`
	ParentZones    []string `json:"parentZones"`
	Published      []string `json:"published"`
	PublishedCount int      `json:"publishedCount"`
}

// HandleRepublishDNS handles
//
//	POST /internal/deployments/{id}/republish-dns
//
// It re-runs upsertSovereignParentZoneRecords against an EXISTING deployment's
// persisted Result so any subdomains added to CanonicalSovereignSubdomains
// after the original provision are written into the parent zone without
// requiring a full re-provision.
//
// Inputs are sourced ENTIRELY from the persisted Deployment record:
//   - dep.Result.SovereignFQDN  → sovereignFQDN for the upsert
//   - dep.Result.LoadBalancerIP → lbIP for the A records
//   - dep.Request.ParentDomains → parent zone list (with backstop derivation
//     from FQDN if empty, matching the original prov-time logic exactly)
//
// The PowerDNS upsert is REPLACE-idempotent: re-running is safe.
func (h *Handler) HandleRepublishDNS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "deployment-not-found",
			"detail": "no deployment with that id is loaded in-memory; restart catalyst-api to reload from disk if the deployment was persisted",
		})
		return
	}
	dep := val.(*Deployment)

	// Ownership check — same gate every other /deployments/{id}* handler uses.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	// Read inputs under the mutex — same pattern as upsertSovereignParentZoneRecordsFromResult.
	dep.mu.Lock()
	result := dep.Result
	dep.mu.Unlock()

	if result == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "no-result",
			"detail": "deployment has no Phase-0 result yet (still provisioning or failed before tofu completed); cannot republish DNS without an LB IP",
		})
		return
	}
	if result.SovereignFQDN == "" || result.LoadBalancerIP == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "incomplete-result",
			"detail": "deployment result is missing sovereignFQDN or loadBalancerIP; cannot reconstruct DNS inputs",
		})
		return
	}

	if h.powerdnsZoneClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "powerdns-not-wired",
			"detail": "CATALYST_POWERDNS_API_KEY is unset on this catalyst-api Pod; DNS republish is only possible on a Sovereign-side Pod with PowerDNS wired",
		})
		return
	}

	// Collect parent zones — mirror the logic in upsertSovereignParentZoneRecordsFromResult
	// verbatim so both paths agree on which zones are written.
	dep.mu.Lock()
	parents := make([]string, 0, len(dep.Request.ParentDomains))
	for _, pd := range dep.Request.ParentDomains {
		if pd.Name != "" {
			parents = append(parents, pd.Name)
		}
	}
	dep.mu.Unlock()

	if len(parents) == 0 {
		// Backstop: derive from FQDN tail labels (same as prov-time path).
		parts := strings.SplitN(result.SovereignFQDN, ".", 2)
		if len(parts) == 2 {
			parents = []string{parts[1]}
		}
	}
	if len(parents) == 0 {
		// Single-label FQDN edge-case — use the FQDN itself as zone.
		parents = []string{result.SovereignFQDN}
	}

	// Re-use the existing idempotent upsert.  The same 404-fallback logic that
	// upsertSovereignParentZoneRecordsFromResult applies is reproduced here:
	// when the synthesised ParentDomain equals the Sovereign FQDN itself, the
	// first PATCH returns 404 because the authoritative zone is the FQDN's
	// tail labels, not the FQDN itself.  We retry against the derived parent.
	var errs []string
	resolvedParents := make([]string, 0, len(parents))
	for _, parent := range parents {
		if err := h.upsertSovereignParentZoneRecords(r.Context(), result.SovereignFQDN, parent, result.LoadBalancerIP); err == nil {
			resolvedParents = append(resolvedParents, parent)
			continue
		} else if strings.Contains(err.Error(), "status 404") && parent == result.SovereignFQDN {
			if i := strings.Index(result.SovereignFQDN, "."); i > 0 {
				derivedParent := result.SovereignFQDN[i+1:]
				h.log.Info("republish-dns: parent zone 404 — retrying with derived parent",
					"id", id,
					"originalParent", parent,
					"derivedParent", derivedParent,
				)
				if err2 := h.upsertSovereignParentZoneRecords(r.Context(), result.SovereignFQDN, derivedParent, result.LoadBalancerIP); err2 == nil {
					resolvedParents = append(resolvedParents, derivedParent)
					continue
				} else {
					errs = append(errs, "zone "+parent+": original="+err.Error()+"; derived-fallback="+err2.Error())
					continue
				}
			}
			errs = append(errs, "zone "+parent+": "+err.Error())
		} else {
			errs = append(errs, "zone "+parent+": "+err.Error())
		}
	}

	if len(errs) > 0 {
		h.log.Warn("republish-dns: one or more parent zone patches failed",
			"id", id,
			"errors", errs,
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":         "partial-failure",
			"detail":        "one or more parent zone patches failed; see errors field",
			"errors":        errs,
			"deploymentId":  id,
			"sovereignFQDN": result.SovereignFQDN,
		})
		return
	}

	published := CanonicalSovereignSubdomains
	writeJSON(w, http.StatusOK, dnsRepublishResponse{
		DeploymentID:   id,
		SovereignFQDN:  result.SovereignFQDN,
		LBIP:           result.LoadBalancerIP,
		ParentZones:    resolvedParents,
		Published:      published,
		PublishedCount: len(published),
	})
}
