// release.go — Sovereign-side decommission surface (issue #319).
//
// Two new endpoints sit on top of the existing canonical release seam
// (DELETE /api/v1/pool/{domain}/release in handler.go), which already
// drops the child PowerDNS zone, removes the parent-zone NS delegation,
// and deletes the allocation row. The seam is the same; this file
// adds two convenience entry points for the post-handover lifecycle:
//
//	POST /api/v1/release
//	  Body: {"sovereignFQDN": "omantel.omani.works"}
//	  Looks up the (poolDomain, subdomain) pair by splitting at the
//	  first dot, validates that pool is managed, and delegates to the
//	  existing Allocator.Release. Status codes mirror the canonical
//	  DELETE handler. The Sovereign's catalyst-api calls this from its
//	  customer-side decommission flow once the customer types their
//	  FQDN to confirm.
//
//	POST /api/v1/force-release
//	  Body: {"sovereignFQDN": "...", "reason": "...", "graceConfirmed": true}
//	  Header: X-Force-Release-Confirm: yes
//	  Operator-only safety hatch for orphaned Sovereigns whose customer
//	  cluster never executed the proper decommission flow. Refuses if:
//	    1. allocation does not exist (404)
//	    2. graceConfirmed is false (422)
//	    3. allocation is younger than the configured grace period (412)
//	    4. the FQDN still resolves in DNS (412 — the Sovereign isn't
//	       actually orphaned; investigate before force-releasing)
//	  Logs the operator's reason on success. Audit trail per the issue
//	  body. Default grace period is 30 days, configurable via
//	  POOL_FORCE_RELEASE_GRACE_HOURS env var (per INVIOLABLE-PRINCIPLES
//	  #4 — runtime configuration, not hardcoded).
//
// Anti-duplication: the existing canonical seam already handles
// PowerDNS teardown + parent-zone NS revert + allocation row delete.
// This file is purely an FQDN-shaped wrapper + the operator-confirmed
// force-release safety logic. NO PowerDNS, NO Dynadot, NO store calls
// are duplicated here — every mutation flows through Allocator.Release.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

// defaultForceReleaseGraceHours mirrors the issue body's "30 days" rule.
// Operators can override via env var per docs/INVIOLABLE-PRINCIPLES.md
// #4 — the grace period is configuration, not code.
const defaultForceReleaseGraceHours = 24 * 30

// dnsResolver is package-level so tests can swap it for a stub. Default
// is the host net resolver, which honours /etc/resolv.conf inside the
// PDM Pod. The Sovereign FQDN must NOT resolve for force-release to
// succeed (that's the entire safety guarantee).
var dnsResolver = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// SovereignReleaseRequest is the FQDN-shaped body for /api/v1/release.
type SovereignReleaseRequest struct {
	SovereignFQDN string `json:"sovereignFQDN"`
}

// ForceReleaseRequest is the body for /api/v1/force-release.
type ForceReleaseRequest struct {
	SovereignFQDN  string `json:"sovereignFQDN"`
	Reason         string `json:"reason"`
	GraceConfirmed bool   `json:"graceConfirmed"`
}

// addReleaseRoutes wires the two new endpoints into the chi router.
// Called from Routes(); kept as a separate function so the route table
// stays readable.
func (h *Handler) addReleaseRoutes() {
	// no-op placeholder — routes are registered in Routes() below.
}

// SovereignRelease handles POST /api/v1/release.
//
// This is a pure convenience wrapper around the canonical
// Allocator.Release seam. The wrapper exists so the customer-side
// decommission flow can call PDM with just its FQDN — without having
// to know whether its FQDN is `<sub>.<pool>` or `<pool>` (BYO).
func (h *Handler) SovereignRelease(w http.ResponseWriter, r *http.Request) {
	var req SovereignReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	pool, sub, ok := splitFQDN(req.SovereignFQDN)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-fqdn",
			"detail": "sovereignFQDN must be of the form <subdomain>.<pool-domain>",
		})
		return
	}
	if !dynadot.IsManagedDomain(pool) {
		// Per the issue body's BYO happy-path: BYO Sovereigns return 404
		// from this endpoint because PDM has no allocation to release.
		// The customer's decommission flow treats 404 as "nothing to do
		// on PDM" and proceeds with the local tofu destroy.
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "not-managed",
			"detail": "pool domain " + pool + " is not managed by PDM (BYO Sovereign — no allocation to release)",
		})
		return
	}
	alloc, err := h.Alloc.Release(r.Context(), pool, sub)
	if err != nil {
		writeReleaseError(w, h, pool, sub, alloc, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sovereignFQDN": sub + "." + pool,
		"freed":         allocationResponse(alloc),
		"releasedAt":    time.Now().UTC().Format(time.RFC3339),
	})
}

// ForceRelease handles POST /api/v1/force-release.
//
// Operator-confirmed safety hatch for orphaned Sovereigns. Refuses
// unless every gate is satisfied (see file header). On success: the
// allocation is gone, the parent-zone NS delegation is reverted, and
// the audit log records the operator's reason.
//
// Status codes:
//   - 200 OK              — released; body carries freed allocation + audit
//   - 400 Bad Request     — invalid JSON
//   - 401 Unauthorized    — missing X-Force-Release-Confirm: yes header
//   - 404 Not Found       — no allocation for the FQDN (already released?)
//   - 412 Precondition    — grace not elapsed OR DNS still resolves
//   - 422 Unprocessable   — invalid FQDN, unmanaged pool, missing reason,
//                           or graceConfirmed=false
//   - 500 Internal        — store / PowerDNS error during teardown
func (h *Handler) ForceRelease(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Force-Release-Confirm") != "yes" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-confirm-header",
			"detail": "force-release requires X-Force-Release-Confirm: yes",
		})
		return
	}
	var req ForceReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-reason",
			"detail": "force-release requires a non-empty operator reason",
		})
		return
	}
	if !req.GraceConfirmed {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "grace-not-confirmed",
			"detail": "force-release requires graceConfirmed=true (operator attests the grace period has elapsed)",
		})
		return
	}
	pool, sub, ok := splitFQDN(req.SovereignFQDN)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-fqdn",
			"detail": "sovereignFQDN must be of the form <subdomain>.<pool-domain>",
		})
		return
	}
	if !dynadot.IsManagedDomain(pool) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "unsupported-pool",
			"detail": "pool domain " + pool + " is not managed by PDM",
		})
		return
	}

	// Lookup before doing anything destructive so we can return 404 cleanly
	// without invoking the Allocator's PowerDNS teardown path.
	alloc, err := h.Store.Get(r.Context(), pool, sub)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "not-found",
				"detail": "no allocation exists for " + sub + "." + pool + " (already released?)",
			})
			return
		}
		h.Log.Error("force-release lookup failed", "fqdn", req.SovereignFQDN, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Gate 1: grace period elapsed?
	graceHours := graceHoursFromEnv()
	graceElapsed := time.Since(alloc.ReservedAt) >= time.Duration(graceHours)*time.Hour
	if !graceElapsed {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error":  "grace-not-elapsed",
			"detail": fmt.Sprintf("allocation reserved at %s; grace period is %dh — wait until %s",
				alloc.ReservedAt.Format(time.RFC3339),
				graceHours,
				alloc.ReservedAt.Add(time.Duration(graceHours)*time.Hour).Format(time.RFC3339)),
		})
		return
	}

	// Gate 2: FQDN must NOT resolve. If a Sovereign's IP is still in DNS
	// the cluster is probably alive; force-release would be premature.
	resolveCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ips, dnsErr := dnsResolver(resolveCtx, req.SovereignFQDN); dnsErr == nil && len(ips) > 0 {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error":  "fqdn-still-resolves",
			"detail": "subdomain still resolves; investigate before force-releasing",
		})
		return
	}

	// All gates passed — drive the canonical release seam. Audit log
	// emits BEFORE the destructive call so a partial-failure shows up
	// in the log alongside the operator's reason.
	h.Log.Info("force-release authorised — proceeding with canonical release",
		"sovereignFQDN", req.SovereignFQDN,
		"poolDomain", pool,
		"subdomain", sub,
		"reason", req.Reason,
		"reservedAt", alloc.ReservedAt.Format(time.RFC3339),
		"graceHours", graceHours,
	)

	freed, err := h.Alloc.Release(r.Context(), pool, sub)
	if err != nil {
		writeReleaseError(w, h, pool, sub, freed, err)
		return
	}
	h.Log.Info("force-release complete",
		"sovereignFQDN", req.SovereignFQDN,
		"reason", req.Reason,
		"previousState", string(freed.State),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"sovereignFQDN": sub + "." + pool,
		"freed":         allocationResponse(freed),
		"releasedAt":    time.Now().UTC().Format(time.RFC3339),
		"audit": map[string]any{
			"reason":     req.Reason,
			"graceHours": graceHours,
			"reservedAt": alloc.ReservedAt.Format(time.RFC3339),
		},
	})
}

// splitFQDN parses a Sovereign FQDN into its (poolDomain, subdomain)
// pair. Returns ok=false on a single-label or empty input.
//
// Examples:
//
//	splitFQDN("omantel.omani.works")    -> ("omani.works", "omantel", true)
//	splitFQDN("acme.example.openova.io") -> ("example.openova.io", "acme", true)
//	splitFQDN("openova.io")             -> ("openova.io", "", false)
func splitFQDN(fqdn string) (poolDomain, subdomain string, ok bool) {
	fqdn = strings.ToLower(strings.TrimSpace(fqdn))
	if fqdn == "" {
		return "", "", false
	}
	idx := strings.IndexByte(fqdn, '.')
	if idx <= 0 || idx == len(fqdn)-1 {
		return "", "", false
	}
	return fqdn[idx+1:], fqdn[:idx], true
}

// graceHoursFromEnv reads POOL_FORCE_RELEASE_GRACE_HOURS, falling back
// to the 30-day default. Negative or zero values fall through to the
// default — this is a safety guard, not a tuning knob.
func graceHoursFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("POOL_FORCE_RELEASE_GRACE_HOURS"))
	if raw == "" {
		return defaultForceReleaseGraceHours
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultForceReleaseGraceHours
	}
	return v
}

// writeReleaseError maps Allocator.Release errors onto HTTP responses.
// Mirrors the canonical Release handler's status-code mapping so the
// FQDN wrapper and the path-shaped seam return identical shapes for
// the same failure modes.
func writeReleaseError(w http.ResponseWriter, h *Handler, pool, sub string, alloc *store.Allocation, err error) {
	if alloc != nil && (strings.Contains(err.Error(), "powerdns delete zone") || strings.Contains(err.Error(), "powerdns remove delegation")) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"warning": "row deleted but PowerDNS teardown failed; re-run release to clean up DNS (idempotent)",
			"detail":  err.Error(),
			"freed":   allocationResponse(alloc),
		})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "not-found",
			"detail": "no allocation exists for " + sub + "." + pool,
		})
	case errors.Is(err, dynadot.ErrUnmanagedDomain):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "unsupported-pool",
			"detail": "pool domain " + pool + " is not managed by PDM",
		})
	default:
		h.Log.Error("release failed", "poolDomain", pool, "subdomain", sub, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
