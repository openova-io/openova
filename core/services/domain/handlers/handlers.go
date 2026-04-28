package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/domain/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for domain HTTP handlers.
type Handler struct {
	Store       *store.Store
	Producer    *events.Producer
	CNAMETarget string // e.g., sme.openova.io
	// TenantURL is the internal base URL for the tenant service
	// (e.g., http://tenant.sme.svc.cluster.local:8083). Used for cross-service
	// membership checks. When empty, authorization helpers fall back to
	// deny-by-default so we never IDOR silently.
	TenantURL string
	// TenantClient is the HTTP client used to call the tenant service. Set in
	// tests to avoid real network calls.
	TenantClient *http.Client
}

// tenantHTTPClient returns the configured HTTP client or a sensible default.
func (h *Handler) tenantHTTPClient() *http.Client {
	if h.TenantClient != nil {
		return h.TenantClient
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// checkTenantMembership asks the tenant service whether the caller (as
// identified by the forwarded Authorization header) is a member of tenantID.
// Returns (true, "") when the tenant service returns 200, (false, "") on 403,
// and (false, reason) on any other error (caller should treat as 500).
func (h *Handler) checkTenantMembership(ctx context.Context, authHeader, tenantID string) (bool, string) {
	if h.TenantURL == "" {
		return false, "tenant service URL not configured"
	}
	if authHeader == "" {
		return false, "missing authorization header"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := strings.TrimRight(h.TenantURL, "/") + "/tenant/orgs/" + tenantID
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false, "build membership request: " + err.Error()
	}
	req.Header.Set("Authorization", authHeader)
	resp, err := h.tenantHTTPClient().Do(req)
	if err != nil {
		return false, "tenant service unreachable: " + err.Error()
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, ""
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound:
		return false, ""
	default:
		return false, fmt.Sprintf("tenant service returned %d", resp.StatusCode)
	}
}

// authorizeTenantAccess enforces that the caller may act on resources owned
// by tenantID. Superadmin role (from the JWT) always passes; otherwise the
// tenant service is consulted for membership. Writes the appropriate error
// response and returns false when access is denied.
func (h *Handler) authorizeTenantAccess(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	if tenantID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant id is required")
		return false
	}
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return false
	}
	if middleware.RoleFromContext(r.Context()) == "superadmin" {
		return true
	}
	ok, reason := h.checkTenantMembership(r.Context(), r.Header.Get("Authorization"), tenantID)
	if ok {
		return true
	}
	if reason != "" {
		slog.Error("membership check failed", "tenant_id", tenantID, "reason", reason)
		respond.Error(w, http.StatusInternalServerError, "failed to verify tenant membership")
		return false
	}
	respond.Error(w, http.StatusForbidden, "not a member of this organization")
	return false
}

// subdomainRe validates subdomain format: lowercase alphanumeric + hyphens, 2-32 chars,
// must start and end with alphanumeric.
var subdomainRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// validateSubdomain checks that a subdomain label is well-formed.
func validateSubdomain(s string) error {
	if len(s) < 2 || len(s) > 32 {
		return fmt.Errorf("subdomain must be between 2 and 32 characters")
	}
	if !subdomainRe.MatchString(s) {
		return fmt.Errorf("subdomain must be lowercase alphanumeric and hyphens, starting and ending with a letter or digit")
	}
	if strings.Contains(s, "--") {
		return fmt.Errorf("subdomain must not contain consecutive hyphens")
	}
	return nil
}

// ---------------------------------------------------------------------------
// POST /domain/subdomains — register a free subdomain
// ---------------------------------------------------------------------------

type registerSubdomainRequest struct {
	TenantID  string `json:"tenant_id"`
	Subdomain string `json:"subdomain"`
	TLD       string `json:"tld"`
}

// RegisterSubdomain registers a free subdomain under one of the allowed TLDs.
func (h *Handler) RegisterSubdomain(w http.ResponseWriter, r *http.Request) {
	var req registerSubdomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.TenantID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id is required")
		return
	}

	// Authz: only members of the named tenant may register subdomains for it.
	if !h.authorizeTenantAccess(w, r, req.TenantID) {
		return
	}

	req.Subdomain = strings.ToLower(strings.TrimSpace(req.Subdomain))
	req.TLD = strings.ToLower(strings.TrimSpace(req.TLD))

	if err := validateSubdomain(req.Subdomain); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	if !store.IsAllowedTLD(req.TLD) {
		respond.Error(w, http.StatusBadRequest, fmt.Sprintf("TLD %q is not available; choose from: %s",
			req.TLD, strings.Join(store.AllowedTLDs, ", ")))
		return
	}

	// Check availability.
	available, err := h.Store.CheckSubdomainAvailable(r.Context(), req.Subdomain, req.TLD)
	if err != nil {
		slog.Error("failed to check subdomain availability", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to check availability")
		return
	}
	if !available {
		respond.Error(w, http.StatusConflict, fmt.Sprintf("subdomain %s.%s is already taken", req.Subdomain, req.TLD))
		return
	}

	d := &store.Domain{
		TenantID:  req.TenantID,
		Domain:    req.Subdomain + "." + req.TLD,
		Type:      "subdomain",
		TLD:       req.TLD,
		Subdomain: req.Subdomain,
		DNSStatus: "verified",
		TLSReady:  true,
	}

	if err := h.Store.CreateDomain(r.Context(), d); err != nil {
		slog.Error("failed to create subdomain", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to register subdomain")
		return
	}

	// Publish domain.registered event.
	h.publishEvent(r, "domain.registered", d.TenantID, d)

	respond.JSON(w, http.StatusCreated, d)
}

// ---------------------------------------------------------------------------
// POST /domain/byod — register a BYOD (Bring Your Own Domain) domain
// ---------------------------------------------------------------------------

type registerBYODRequest struct {
	TenantID string `json:"tenant_id"`
	Domain   string `json:"domain"`
}

type registerBYODResponse struct {
	Domain       *store.Domain `json:"domain"`
	CNAMETarget  string        `json:"cname_target"`
	Registrar    string        `json:"registrar"`
	Instructions string        `json:"instructions"`
}

// RegisterBYOD registers a custom domain (Bring Your Own Domain).
func (h *Handler) RegisterBYOD(w http.ResponseWriter, r *http.Request) {
	var req registerBYODRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.TenantID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id is required")
		return
	}

	// Authz: only members of the named tenant may BYOD for it.
	if !h.authorizeTenantAccess(w, r, req.TenantID) {
		return
	}

	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if req.Domain == "" {
		respond.Error(w, http.StatusBadRequest, "domain is required")
		return
	}

	// Check if domain is already registered.
	existing, err := h.Store.FindDomainByName(r.Context(), req.Domain)
	if err != nil {
		slog.Error("failed to check existing domain", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to check domain")
		return
	}
	if existing != nil {
		respond.Error(w, http.StatusConflict, fmt.Sprintf("domain %s is already registered", req.Domain))
		return
	}

	// Detect registrar via WHOIS.
	registrar, err := detectRegistrar(req.Domain)
	if err != nil {
		slog.Warn("WHOIS lookup failed", "domain", req.Domain, "error", err)
		registrar = "unknown"
	}

	d := &store.Domain{
		TenantID:  req.TenantID,
		Domain:    req.Domain,
		Type:      "byod",
		Registrar: registrar,
		DNSStatus: "pending",
		TLSReady:  false,
	}

	if err := h.Store.CreateDomain(r.Context(), d); err != nil {
		slog.Error("failed to create BYOD domain", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to register domain")
		return
	}

	// Publish domain.registered event.
	h.publishEvent(r, "domain.registered", d.TenantID, d)

	resp := registerBYODResponse{
		Domain:       d,
		CNAMETarget:  h.CNAMETarget,
		Registrar:    registrar,
		Instructions: dnsInstructions(registrar, h.CNAMETarget),
	}

	respond.JSON(w, http.StatusCreated, resp)
}

// ---------------------------------------------------------------------------
// GET /domain/domains/{tenantId} — list domains for a tenant
// ---------------------------------------------------------------------------

// ListDomains returns all domains for a tenant.
func (h *Handler) ListDomains(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	if tenantID == "" {
		respond.Error(w, http.StatusBadRequest, "tenantId is required")
		return
	}

	// Authz: only members of the tenant (or a superadmin) may list its domains.
	if !h.authorizeTenantAccess(w, r, tenantID) {
		return
	}

	domains, err := h.Store.ListDomainsByTenant(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to list domains", "tenant_id", tenantID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to list domains")
		return
	}

	respond.OK(w, domains)
}

// ---------------------------------------------------------------------------
// DELETE /domain/domains/{id} — delete a domain
// ---------------------------------------------------------------------------

// DeleteDomain removes a domain by ID.
//
// Authorization model (IDOR fix, issue #79):
//  1. The domain is loaded so we know which tenant owns it.
//  2. The caller's JWT identifies the user + role.
//  3. If the caller is a superadmin they may delete any domain.
//  4. Otherwise we ask the tenant service whether the caller is a member of
//     the domain's owning tenant — 403 if not.
//
// Returning 404 for a missing domain BEFORE the authz check would leak the
// existence/absence of domain IDs to any authenticated user; returning 403
// when the caller isn't a member is the least-leaky behaviour.
func (h *Handler) DeleteDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "id is required")
		return
	}

	d, err := h.Store.GetDomain(r.Context(), id)
	if err != nil {
		slog.Error("failed to load domain for delete", "id", id, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to load domain")
		return
	}
	if d == nil {
		// 404 for a missing record is fine regardless of caller (no IDOR leak
		// beyond "this ID does not exist" — delete is always a no-op in that
		// case). We still require the caller to be authenticated, which the
		// JWT middleware guarantees before this handler runs.
		if middleware.UserIDFromContext(r.Context()) == "" {
			respond.Error(w, http.StatusUnauthorized, "missing user identity")
			return
		}
		respond.Error(w, http.StatusNotFound, "domain not found")
		return
	}

	if !h.authorizeTenantAccess(w, r, d.TenantID) {
		return
	}

	if err := h.Store.DeleteDomain(r.Context(), id); err != nil {
		slog.Error("failed to delete domain", "id", id, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}

	respond.JSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// POST /domain/verify/{id} — verify BYOD DNS configuration
// ---------------------------------------------------------------------------

type verifyResponse struct {
	ID        string `json:"id"`
	Domain    string `json:"domain"`
	DNSStatus string `json:"dns_status"`
	CNAME     string `json:"cname,omitempty"`
	Message   string `json:"message"`
}

// VerifyDNS checks whether a BYOD domain's CNAME record points to the expected target.
func (h *Handler) VerifyDNS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "id is required")
		return
	}

	d, err := h.Store.GetDomain(r.Context(), id)
	if err != nil {
		slog.Error("failed to get domain", "id", id, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to get domain")
		return
	}
	if d == nil {
		respond.Error(w, http.StatusNotFound, "domain not found")
		return
	}

	// Authz: only members of the owning tenant may trigger re-verification.
	if !h.authorizeTenantAccess(w, r, d.TenantID) {
		return
	}

	if d.Type != "byod" {
		respond.Error(w, http.StatusBadRequest, "DNS verification is only applicable to BYOD domains")
		return
	}

	// Look up the CNAME record.
	cname, err := net.LookupCNAME(d.Domain)
	if err != nil {
		d.DNSStatus = "failed"
		h.Store.UpdateDomain(r.Context(), id, d)

		respond.OK(w, verifyResponse{
			ID:        d.ID,
			Domain:    d.Domain,
			DNSStatus: "failed",
			Message:   fmt.Sprintf("DNS lookup failed: %v", err),
		})
		return
	}

	// Normalize: strip trailing dot.
	cname = strings.TrimSuffix(cname, ".")
	target := strings.TrimSuffix(h.CNAMETarget, ".")

	if cname == target {
		d.DNSStatus = "verified"
		d.TLSReady = true
		h.Store.UpdateDomain(r.Context(), id, d)

		respond.OK(w, verifyResponse{
			ID:        d.ID,
			Domain:    d.Domain,
			DNSStatus: "verified",
			CNAME:     cname,
			Message:   "DNS is correctly configured",
		})
		return
	}

	d.DNSStatus = "failed"
	h.Store.UpdateDomain(r.Context(), id, d)

	respond.OK(w, verifyResponse{
		ID:        d.ID,
		Domain:    d.Domain,
		DNSStatus: "failed",
		CNAME:     cname,
		Message:   fmt.Sprintf("CNAME points to %s, expected %s", cname, target),
	})
}

// ---------------------------------------------------------------------------
// GET /domain/check/{subdomain}/{tld} — check subdomain availability (public)
// ---------------------------------------------------------------------------

type checkResponse struct {
	Subdomain string `json:"subdomain"`
	TLD       string `json:"tld"`
	Domain    string `json:"domain"`
	Available bool   `json:"available"`
}

// CheckAvailability checks whether a subdomain is available under a TLD.
func (h *Handler) CheckAvailability(w http.ResponseWriter, r *http.Request) {
	subdomain := r.PathValue("subdomain")
	tld := r.PathValue("tld")

	subdomain = strings.ToLower(strings.TrimSpace(subdomain))
	tld = strings.ToLower(strings.TrimSpace(tld))

	if !store.IsAllowedTLD(tld) {
		respond.Error(w, http.StatusBadRequest, fmt.Sprintf("TLD %q is not available", tld))
		return
	}

	available, err := h.Store.CheckSubdomainAvailable(r.Context(), subdomain, tld)
	if err != nil {
		slog.Error("failed to check availability", "subdomain", subdomain, "tld", tld, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to check availability")
		return
	}

	respond.OK(w, checkResponse{
		Subdomain: subdomain,
		TLD:       tld,
		Domain:    subdomain + "." + tld,
		Available: available,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// publishEvent publishes a domain event to RedPanda. Failures are logged but not fatal.
// Uses a short timeout so a broker outage doesn't block the HTTP response.
func (h *Handler) publishEvent(_ *http.Request, eventType, tenantID string, data any) {
	evt, err := events.NewEvent(eventType, "domain-service", tenantID, data)
	if err != nil {
		slog.Error("failed to create event", "type", eventType, "error", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Producer.Publish(pubCtx, "domain-events", evt); err != nil {
		slog.Error("failed to publish event", "type", eventType, "error", err)
	}
}
