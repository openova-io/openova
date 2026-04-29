// Package handler — registrar.go: thin proxy to PDM's registrar adapter
// surface, used by the wizard's BYO Flow B (#169).
//
// The wizard never calls PDM directly — it always goes through catalyst-api
// so a single CORS-allowlist + auth posture covers everything the React
// app talks to. This file is the proxy seam:
//
//	POST /api/v1/registrar/{registrar}/validate
//	  Body:  {"domain": "...", "token": "..."}
//	  Reply: {"valid": true, "registrar": "...", "domain": "..."}
//
//	POST /api/v1/registrar/{registrar}/set-ns
//	  (Same shape as PDM's /set-ns; we forward as-is.)
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the Token
// field never enters a struct that gets logged. We forward bytes verbatim
// to PDM and stream the response back; the only thing that lands in
// catalyst-api logs is {registrar, domain, outcome}.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// registrarValidateRequest is what the wizard POSTs to catalyst-api.
//
// IMPORTANT: do not add struct tags that mark Token as loggable. The
// proxy never logs this struct directly — it forwards the bytes to PDM
// and decodes only the response.
type registrarValidateRequest struct {
	Domain string `json:"domain"`
	Token  string `json:"token"`
}

// supportedRegistrars lists every adapter the wizard's BYO Flow B
// dropdown should accept. Keep in sync with the model.ts REGISTRAR_OPTIONS
// list and PDM's compiled-in adapter set.
var supportedRegistrars = map[string]struct{}{
	"cloudflare": {},
	"namecheap":  {},
	"godaddy":    {},
	"ovh":        {},
	"dynadot":    {},
}

// ValidateRegistrar handles POST /api/v1/registrar/{registrar}/validate.
//
// We don't decode the body to a struct here — we read it once, validate
// the path param + body shape, then forward the bytes to PDM. This keeps
// the token's lifetime as short as possible: it never enters a long-lived
// struct and is GC'd as soon as the http.Client.Do call returns.
func (h *Handler) ValidateRegistrar(w http.ResponseWriter, r *http.Request) {
	registrarName := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "registrar")))
	if _, ok := supportedRegistrars[registrarName]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":  "unsupported-registrar",
			"detail": "registrar adapter not available",
		})
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<14))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "request-too-large",
			"detail": "validation request body must be under 16KB",
		})
		return
	}

	// Sanity check the JSON shape — we want {domain, token} both non-empty.
	var probe registrarValidateRequest
	if err := json.Unmarshal(raw, &probe); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "request body must be JSON {domain, token}",
		})
		return
	}
	if strings.TrimSpace(probe.Domain) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-domain",
			"detail": "domain is required",
		})
		return
	}
	if probe.Token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-token",
			"detail": "registrar API credentials are required",
		})
		return
	}

	pdmBase := pdmBaseURL()
	if pdmBase == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "pdm-unavailable",
			"detail": "POOL_DOMAIN_MANAGER_URL is not configured",
		})
		return
	}

	target := fmt.Sprintf("%s/api/v1/registrar/%s/validate",
		strings.TrimRight(pdmBase, "/"),
		url.PathEscape(registrarName),
	)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pdmReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		h.log.Error("build pdm validate request", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy-build-failed"})
		return
	}
	pdmReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(pdmReq)
	if err != nil {
		h.log.Info("pdm validate proxy: network error",
			"registrar", registrarName,
			"domain", probe.Domain,
			"outcome", "pdm-unreachable",
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "pdm-unreachable",
			"detail": "pool-domain-manager is temporarily unreachable",
		})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Stream PDM's status + body verbatim so the wizard's error mapping
	// has the same vocabulary it would see talking to PDM directly.
	h.log.Info("pdm validate proxy: complete",
		"registrar", registrarName,
		"domain", probe.Domain,
		"status", resp.StatusCode,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// SetNSRegistrar handles POST /api/v1/registrar/{registrar}/set-ns.
//
// Mirror of ValidateRegistrar but forwards to PDM's /set-ns. Used by the
// CreateDeployment branch when sovereignDomainMode == "byo-api". The
// wizard does NOT call this directly — only catalyst-api does, on submit.
func (h *Handler) SetNSRegistrar(w http.ResponseWriter, r *http.Request) {
	registrarName := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "registrar")))
	if _, ok := supportedRegistrars[registrarName]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":  "unsupported-registrar",
			"detail": "registrar adapter not available",
		})
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<14))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "request-too-large",
			"detail": "set-ns request body must be under 16KB",
		})
		return
	}

	pdmBase := pdmBaseURL()
	if pdmBase == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "pdm-unavailable",
			"detail": "POOL_DOMAIN_MANAGER_URL is not configured",
		})
		return
	}

	target := fmt.Sprintf("%s/api/v1/registrar/%s/set-ns",
		strings.TrimRight(pdmBase, "/"),
		url.PathEscape(registrarName),
	)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	pdmReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		h.log.Error("build pdm set-ns request", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy-build-failed"})
		return
	}
	pdmReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(pdmReq)
	if err != nil {
		h.log.Info("pdm set-ns proxy: network error",
			"registrar", registrarName,
			"outcome", "pdm-unreachable",
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "pdm-unreachable",
			"detail": "pool-domain-manager is temporarily unreachable",
		})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// pdmBaseURL is read every call so a config-map rotation propagates
// without a Pod restart. Same default as handler.New() so the proxy and
// the rest of the package agree.
func pdmBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("POOL_DOMAIN_MANAGER_URL")); v != "" {
		return v
	}
	return "http://pool-domain-manager.openova-system.svc.cluster.local:8080"
}
