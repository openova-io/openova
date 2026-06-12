// Package handler — handover_jwt.go: JWT handover signing endpoint (issue #605,
// Phase-8b).
//
// # Endpoint
//
//	POST /api/v1/deployments/{id}/mint-handover-token
//
// Requires an authenticated session (the OIDC proxy upstream sets
// X-Forwarded-User = sub and X-Forwarded-Email = email before requests
// reach catalyst-api). The handler reads both headers and mints a
// short-lived RS256 JWT whose claims satisfy the Agent-C contract.
//
// # JWT contract (Agent C must accept exactly this shape)
//
//	{
//	  "iss":            "https://console.openova.io",
//	  "aud":            ["https://console.<sovereignFqdn>"],
//	  "sub":            "<openova-user-sub>",
//	  "email":          "<verified-email>",
//	  "email_verified": true,
//	  "sovereign_fqdn": "<deployment.SovereignFQDN>",
//	  "deployment_id":  "<deployment.ID>",
//	  "role":           "sovereign-admin",
//	  "jti":            "<uuid-v4>",
//	  "iat":            <unix>,
//	  "exp":            <unix + 300>
//	}
//
// # Response
//
//	200 OK:  { "token": "<jwt>", "redirectURL": "https://console.<fqdn>/auth/handover?token=<jwt>" }
//	401:     signer not configured — CATALYST_HANDOVER_KEY_PATH missing
//	404:     deployment not found
//	409:     deployment not in handover-ready state (not adopted, not phase1-done)
//	500:     signing failure
//
// # Public-key endpoint
//
//	GET /api/v1/handover/public-key
//
// Returns the RS256 public key as a JSON Web Key (RFC 7517). This endpoint
// lets the Sovereign-side Agent-C bootstrap its trust anchor at provision time
// in addition to the cloud-init distribution path.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   - #10 (credential hygiene): private key never logged, only the jti goes
//     into the info-level log line.
//   - #4 (never hardcode): issuer URL and TTL come from the Signer config
//     (set by main.go from CATALYST_HANDOVER_JWT_ISSUER / _TTL_SECONDS).
package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// MintHandoverToken handles
//
//	POST /api/v1/deployments/{id}/mint-handover-token
//
// See package-level comment for the full contract.
func (h *Handler) MintHandoverToken(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "signer-not-configured",
			"detail": "CATALYST_HANDOVER_KEY_PATH is not set or the key file could not be loaded — catalyst-api cannot mint handover tokens on this instance",
		})
		return
	}

	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	// Issue #689 — CRITICAL ownership check before minting a handover
	// JWT. Without this, a different signed-in operator could mint a
	// handover for someone else's Sovereign and impersonate the original
	// owner during the cross-domain redirect. 404 (not 403) on mismatch
	// preserves the never-leak-existence contract.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	dep.mu.Lock()
	fqdn := dep.Request.SovereignFQDN
	status := dep.Status
	dep.mu.Unlock()

	// Only mint a token for deployments that have successfully reached the
	// handover-ready state. Accept both "ready" (Phase-1 complete, not yet
	// finalised) and "adopted" (finalised, browser already redirected once
	// but may retry) as valid states.
	if status != "ready" && status != "adopted" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-handover-ready",
			"detail": "deployment status is " + status + "; handover token can only be minted when status is ready or adopted",
		})
		return
	}

	if strings.TrimSpace(fqdn) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-fqdn",
			"detail": "deployment has no SovereignFQDN; cannot mint handover token",
		})
		return
	}

	// Read user identity. Two valid sources, in order:
	//   1. X-User-Sub / X-User-Email — injected by catalyst-api's own
	//      RequireSession middleware (auth/middleware.go) when the
	//      PIN-auth (issue #688) / Keycloak session cookie is validated.
	//      This is the canonical Catalyst-Zero auth path.
	//   2. X-Forwarded-User / X-Forwarded-Email — set by an upstream
	//      OIDC proxy when one is deployed. Backwards-compat with the
	//      original handler design.
	//
	// Falling back to "unknown" gave the JWT bogus claims, and the
	// Sovereign-side handover handler couldn't pre-provision the
	// operator account — landing the operator on Keycloak's bare
	// login screen instead of the seamless first-session flow
	// (Phase-8b agreement). Caught live on otech46.
	sub := r.Header.Get("X-User-Sub")
	if sub == "" {
		sub = h.k8sUser(r) // X-Forwarded-User or h.k8sUserHeader override
	}
	if sub == "" {
		sub = "unknown"
	}
	email := r.Header.Get("X-User-Email")
	if email == "" {
		email = r.Header.Get("X-Forwarded-Email")
	}
	if email == "" {
		email = r.Header.Get("X-Auth-Request-Email") // Oauth2-proxy alt
	}
	if email == "" {
		// No identity source — log loud and fall back to sub. Result
		// will be the bare-Keycloak handover that #20 was supposed to
		// solve; this branch should never fire in production.
		email = sub
		h.log.Warn("handover-jwt: no identity header found (X-User-Email, X-Forwarded-Email, X-Auth-Request-Email all empty) — using sub as email",
			"sub", sub,
			"deploymentId", id,
		)
	}

	tokenStr, err := h.handoverSigner.MintToken(fqdn, id, sub, email)
	if err != nil {
		h.log.Error("handover-jwt: mint failed",
			"deploymentId", id,
			"fqdn", fqdn,
			"err", err,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "mint-failed",
			"detail": "could not sign handover JWT: " + err.Error(),
		})
		return
	}

	// Build redirect URL: https://console.<fqdn>/auth/handover?token=<jwt>
	redirectURL := "https://console." + fqdn + "/auth/handover?token=" + url.QueryEscape(tokenStr)

	h.log.Info("handover-jwt: token minted",
		"deploymentId", id,
		"fqdn", fqdn,
		"sub", sub,
	)

	// TBD-A10 / #1760 — fire the D16 secondary-kubeconfig export here as
	// a recovery path for operators who hit a missed auto-fire (e.g.
	// catalyst-api restarted before secondary CPs PUT their kubeconfigs).
	// The chroot endpoint is idempotent (re-POSTing a {depID, region}
	// pair overwrites the file + AddCluster on duplicate ID is a no-op),
	// so unconditional re-fire is safe. Fire-and-forget like fireHandover.
	go h.exportSecondaryKubeconfigsToChild(dep, fqdn, id)

	// #3263 #3277 — re-fire the handover jobs export for the same
	// recovery reason: an env whose handover predates the jobs wire
	// (hw130) or whose handover-time export was lost to a catalyst-api
	// roll receives its region-b job rows on the next operator re-mint.
	// The chroot's import is idempotent (ImportSnapshot never regresses
	// a terminal row), so unconditional re-fire is safe.
	go h.exportJobsToChild(id, fqdn)

	writeJSON(w, http.StatusOK, map[string]string{
		"token":       tokenStr,
		"redirectURL": redirectURL,
	})
}

// GetHandoverPublicKey handles
//
//	GET /api/v1/handover/public-key
//
// Returns the RS256 public key as a JSON Web Key (RFC 7517). The Sovereign-side
// Agent-C can call this at provision time to bootstrap its trust anchor as an
// alternative to the cloud-init distribution path.
func (h *Handler) GetHandoverPublicKey(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "signer-not-configured",
			"detail": "handover signer not initialised on this catalyst-api instance",
		})
		return
	}

	jwk, err := h.handoverSigner.PublicJWK()
	if err != nil {
		h.log.Error("handover-jwt: PublicJWK failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "jwk-encode-failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(jwk)
}

// SetHandoverSigner wires the handover JWT signer into the Handler.
// Called by main.go after loading/generating the keypair.
// Tests can inject a test Signer via this method.
func (h *Handler) SetHandoverSigner(s *handoverjwt.Signer) {
	h.handoverSigner = s
}
