package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/tenant/catalog"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// tenantSlugRE mirrors the guard in services/provisioning/handlers/consumer.go
// so bad slugs are rejected at the tenant-service input boundary (CreateOrg)
// rather than only downstream in provisioning. Security-critical: the slug
// becomes a filesystem path component in clusters/.../tenants/<slug>/ so
// anything but [a-z0-9-] opens a path-traversal vector. Issue #105 (extended).
var tenantSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// validTenantSlug returns true iff s is a safe tenant slug.
func validTenantSlug(s string) bool {
	return tenantSlugRE.MatchString(s)
}

// Handler holds dependencies for tenant HTTP handlers.
type Handler struct {
	Store    *store.Store
	Producer *events.Producer
	// Catalog is optional; when unset the day-2 app install/uninstall
	// endpoints return 501. Provisioning-time creation does not need it
	// because the marketplace already validated capacity at checkout.
	Catalog *catalog.Client
	// ProvisioningURL is the internal base URL for provisioning-service
	// (e.g. http://provisioning.sme.svc.cluster.local:8084). Tenant calls
	// it directly for day-2 install/uninstall so the pipeline works even
	// when the event bus is unavailable.
	ProvisioningURL string

	// DayTwoLocks serializes day-2 install/uninstall on a given tenant so
	// concurrent callers see consistent tenant.Apps reads. Issue #110.
	// Callers MUST pre-populate via NewTenantLocks(); nil is not safe.
	DayTwoLocks *tenantLocks
}

// NewTenantLocks returns a fresh tenantLocks for Handler.DayTwoLocks.
// Exposed so main.go can wire it at construction.
func NewTenantLocks() *tenantLocks { return newTenantLocks() }

// requireMembership checks that the calling user is a member of the tenant
// and returns the role. Returns empty string and writes an error response if
// the user is not a member.
func (h *Handler) requireMembership(w http.ResponseWriter, r *http.Request, tenantID string) (string, bool) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return "", false
	}
	role, err := h.Store.GetMemberRole(r.Context(), tenantID, userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check membership")
		return "", false
	}
	if role == "" {
		respond.Error(w, http.StatusForbidden, "not a member of this organization")
		return "", false
	}
	return role, true
}

// requireOwnerOrAdmin checks that the calling user has owner or admin role in the tenant.
func (h *Handler) requireOwnerOrAdmin(w http.ResponseWriter, r *http.Request, tenantID string) (string, bool) {
	role, ok := h.requireMembership(w, r, tenantID)
	if !ok {
		return "", false
	}
	if role != "owner" && role != "admin" {
		respond.Error(w, http.StatusForbidden, "owner or admin role required")
		return "", false
	}
	return role, true
}

// requireOwner checks that the calling user has the owner role in the tenant.
func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	role, ok := h.requireMembership(w, r, tenantID)
	if !ok {
		return false
	}
	if role != "owner" {
		respond.Error(w, http.StatusForbidden, "owner role required")
		return false
	}
	return true
}

// requireSuperadmin checks that the request was made by a superadmin.
func requireSuperadmin(r *http.Request) bool {
	return middleware.RoleFromContext(r.Context()) == "superadmin"
}

// callProvisioning POSTs a JSON payload to the provisioning service. Used by
// day-2 app install/uninstall so the pipeline works when RedPanda is down.
// Returns nil on any 2xx; logs and returns an error otherwise. A 5s timeout
// keeps the tenant API responsive if provisioning is slow.
func (h *Handler) callProvisioning(ctx context.Context, path string, payload any) error {
	if h.ProvisioningURL == "" {
		return fmt.Errorf("provisioning URL not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.ProvisioningURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("provisioning %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tenant CRUD
// ---------------------------------------------------------------------------

// CreateOrg creates a new organization for the authenticated user.
func (h *Handler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}

	var body struct {
		Slug     string   `json:"slug"`
		Name     string   `json:"name"`
		OrgType  string   `json:"org_type"`
		Industry string   `json:"industry"`
		PlanID   string   `json:"plan_id"`
		Apps     []string `json:"apps"`
		AddOns   []string `json:"addons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.Slug == "" || body.Name == "" {
		respond.Error(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	// Slug becomes a DNS subdomain AND a filesystem path component in
	// clusters/.../tenants/<slug>/ — it MUST match a tight regex or we
	// open a path-traversal vector (slug="../etc/passwd" would have the
	// provisioning consumer write outside the tenants directory). Same
	// regex the provisioning-side guard in #105 enforces. Security fix
	// caught by dod-chaos scenario1_apiBoundaries test 1b.
	if !validTenantSlug(body.Slug) {
		respond.Error(w, http.StatusBadRequest,
			"slug must be 3-31 chars, lowercase alphanumeric with hyphens, starting with a letter (e.g. 'acme-co')")
		return
	}

	// Check slug uniqueness.
	available, err := h.Store.CheckSlugAvailable(r.Context(), body.Slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check slug availability")
		return
	}
	if !available {
		respond.Error(w, http.StatusConflict, "slug is already taken")
		return
	}

	tenant := &store.Tenant{
		Slug:      body.Slug,
		Name:      body.Name,
		OrgType:   body.OrgType,
		Industry:  body.Industry,
		OwnerID:   userID,
		PlanID:    body.PlanID,
		Apps:      body.Apps,
		AddOns:    body.AddOns,
		Subdomain: body.Slug,
		Status:    "provisioning",
	}

	if err := h.Store.CreateTenant(r.Context(), tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	// Add the creator as owner member.
	member := &store.Member{
		TenantID: tenant.ID,
		UserID:   userID,
		Role:     "owner",
		JoinedAt: time.Now().UTC(),
	}
	if err := h.Store.AddMember(r.Context(), member); err != nil {
		slog.Error("failed to add owner as member", "tenant_id", tenant.ID, "error", err)
		// Tenant was created; don't fail the response, but log the error.
	}

	// Publish tenant.created event (non-blocking — don't let broker outage delay the response).
	evt, err := events.NewEvent("tenant.created", "tenant-service", tenant.ID, tenant)
	if err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer pubCancel()
		if pubErr := h.Producer.Publish(pubCtx, "sme.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.created event", "tenant_id", tenant.ID, "error", pubErr)
		}
	}

	respond.JSON(w, http.StatusCreated, tenant)
}

// ListOrgs returns all organizations where the authenticated user is a member.
func (h *Handler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}

	tenants, err := h.Store.ListTenantsByOwner(r.Context(), userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list organizations")
		return
	}
	respond.OK(w, tenants)
}

// GetOrg returns a single organization by ID (membership required).
func (h *Handler) GetOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireMembership(w, r, id); !ok {
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}
	respond.OK(w, tenant)
}

// UpdateOrg updates an organization (owner/admin only).
func (h *Handler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	existing, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if existing == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	var body struct {
		Name          *string  `json:"name"`
		OrgType       *string  `json:"org_type"`
		Industry      *string  `json:"industry"`
		PlanID        *string  `json:"plan_id"`
		Apps          []string `json:"apps"`
		AddOns        []string `json:"addons"`
		Subdomain     *string  `json:"subdomain"`
		CustomDomains []string `json:"custom_domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Apply partial updates.
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.OrgType != nil {
		existing.OrgType = *body.OrgType
	}
	if body.Industry != nil {
		existing.Industry = *body.Industry
	}
	if body.PlanID != nil {
		existing.PlanID = *body.PlanID
	}
	if body.Apps != nil {
		existing.Apps = body.Apps
	}
	if body.AddOns != nil {
		existing.AddOns = body.AddOns
	}
	if body.Subdomain != nil {
		existing.Subdomain = *body.Subdomain
	}
	if body.CustomDomains != nil {
		existing.CustomDomains = body.CustomDomains
	}

	if err := h.Store.UpdateTenant(r.Context(), id, existing); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update organization")
		return
	}
	respond.OK(w, existing)
}

// DeleteOrg soft-deletes an organization (owner only).
func (h *Handler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !h.requireOwner(w, r, id) {
		return
	}

	existing, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get organization")
		return
	}
	if existing == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	// Soft delete: set status to "deleted".
	existing.Status = "deleted"
	if err := h.Store.UpdateTenant(r.Context(), id, existing); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete organization")
		return
	}

	// Publish tenant.deleted event (non-blocking). Slug is required by the
	// provisioning consumer so it can locate the tenant's GitOps directory.
	evt, err := events.NewEvent("tenant.deleted", "tenant-service", id, map[string]string{
		"id":   id,
		"slug": existing.Subdomain,
	})
	if err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer pubCancel()
		if pubErr := h.Producer.Publish(pubCtx, "sme.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.deleted event", "tenant_id", id, "error", pubErr)
		}
	}

	respond.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

// ListMembers returns all members of an organization (membership required).
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireMembership(w, r, id); !ok {
		return
	}

	members, err := h.Store.ListMembers(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list members")
		return
	}
	respond.OK(w, members)
}

// InviteMember adds a new member to an organization (owner/admin only).
func (h *Handler) InviteMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	var body struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.Email == "" {
		respond.Error(w, http.StatusBadRequest, "email is required")
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	// Prevent adding a second owner.
	if body.Role == "owner" {
		respond.Error(w, http.StatusBadRequest, "cannot assign owner role via invitation")
		return
	}
	if body.Role != "admin" && body.Role != "member" && body.Role != "viewer" {
		respond.Error(w, http.StatusBadRequest, "role must be admin, member, or viewer")
		return
	}

	member := &store.Member{
		TenantID: id,
		UserID:   body.UserID,
		Email:    body.Email,
		Role:     body.Role,
		JoinedAt: time.Now().UTC(),
	}
	if err := h.Store.AddMember(r.Context(), member); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	respond.JSON(w, http.StatusCreated, member)
}

// RemoveMember removes a member from an organization (owner/admin only, can't remove owner).
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	targetUserID := r.PathValue("userId")

	if _, ok := h.requireOwnerOrAdmin(w, r, id); !ok {
		return
	}

	// Check the target's role — cannot remove the owner.
	targetRole, err := h.Store.GetMemberRole(r.Context(), id, targetUserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check member role")
		return
	}
	if targetRole == "" {
		respond.Error(w, http.StatusNotFound, "member not found")
		return
	}
	if targetRole == "owner" {
		respond.Error(w, http.StatusForbidden, "cannot remove the owner")
		return
	}

	if err := h.Store.RemoveMember(r.Context(), id, targetUserID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to remove member")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ---------------------------------------------------------------------------
// Slug check (public)
// ---------------------------------------------------------------------------

// CheckSlug returns whether a slug is available.
func (h *Handler) CheckSlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		respond.Error(w, http.StatusBadRequest, "slug is required")
		return
	}
	available, err := h.Store.CheckSlugAvailable(r.Context(), slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to check slug")
		return
	}
	respond.OK(w, map[string]bool{"available": available})
}

// ---------------------------------------------------------------------------
// Admin endpoints
// ---------------------------------------------------------------------------

// AdminListTenants returns a paginated list of all tenants (superadmin only).
func (h *Handler) AdminListTenants(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	q := r.URL.Query().Get("q")
	if q != "" {
		tenants, err := h.Store.SearchTenants(r.Context(), q)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to search tenants")
			return
		}
		respond.OK(w, map[string]any{"tenants": tenants, "total": len(tenants)})
		return
	}

	tenants, total, err := h.Store.ListAllTenants(r.Context(), offset, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	respond.OK(w, map[string]any{"tenants": tenants, "total": total, "offset": offset, "limit": limit})
}

// AdminGetTenant returns any tenant by ID (superadmin only).
func (h *Handler) AdminGetTenant(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.OK(w, tenant)
}

// AdminUpdateStatus changes a tenant's status (superadmin only).
func (h *Handler) AdminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	validStatuses := map[string]bool{"active": true, "suspended": true, "provisioning": true, "deleted": true}
	if !validStatuses[body.Status] {
		respond.Error(w, http.StatusBadRequest, "status must be active, suspended, provisioning, or deleted")
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	tenant.Status = body.Status
	if err := h.Store.UpdateTenant(r.Context(), id, tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update tenant status")
		return
	}
	respond.OK(w, tenant)
}

// AdminDeleteTenant soft-deletes any tenant and publishes tenant.deleted
// (superadmin only, no membership check).
func (h *Handler) AdminDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}

	id := r.PathValue("id")
	tenant, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	// Already soft-deleted — a subsequent DELETE request should 404, not
	// return 'deleted' status again. Caught by dod-chaos scenario8_negativeOps
	// on 2026-04-20: a second admin delete was quietly returning 200 which
	// would cause duplicate tenant.deleted events to fire and confuse audit
	// trails.
	if tenant.Status == "deleted" {
		respond.Error(w, http.StatusNotFound, "tenant already deleted")
		return
	}

	tenant.Status = "deleted"
	if err := h.Store.UpdateTenant(r.Context(), id, tenant); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}

	evt, err := events.NewEvent("tenant.deleted", "tenant-service", id, map[string]string{
		"id":   id,
		"slug": tenant.Subdomain,
	})
	if err == nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer pubCancel()
		if pubErr := h.Producer.Publish(pubCtx, "sme.tenant.events", evt); pubErr != nil {
			slog.Error("failed to publish tenant.deleted event", "tenant_id", id, "error", pubErr)
		}
	}

	respond.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// InternalGetSubdomain returns the tenant's subdomain by ID. No auth — this
// route is only registered at the cluster-internal service IP and is used
// by billing to enrich order.placed events with the subdomain that
// store.Order doesn't carry. Returning just id+subdomain (no other
// sensitive fields) keeps the blast radius small even if the path were
// ever exposed at a gateway by accident. Issue #105.
func (h *Handler) InternalGetSubdomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	t, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch tenant")
		return
	}
	if t == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{
		"id":        t.ID,
		"subdomain": t.Subdomain,
	})
}
