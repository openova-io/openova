package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/openova-io/openova-private/website/marketplace-api/store"
)

type Handler struct {
	Store       *store.MemoryStore
	JWTSecret   []byte
	AllowOrigin string
	GitRepoPath string
	SMTPHost    string
	SMTPPort    string
	FromEmail   string
}

// CORS wraps a handler with CORS headers.
func (h *Handler) CORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", h.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]string{"error": msg})
}

// generateJWT creates a tenant JWT token.
func (h *Handler) generateJWT(tenantID, email string) (string, error) {
	claims := jwt.MapClaims{
		"sub":       tenantID,
		"email":     email,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.JWTSecret)
}

// authenticateTenant extracts and validates the tenant JWT.
func (h *Handler) authenticateTenant(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", jwt.ErrTokenMalformed
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		return h.JWTSecret, nil
	})
	if err != nil {
		return "", err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", jwt.ErrTokenInvalidClaims
	}

	sub, _ := claims["sub"].(string)
	return sub, nil
}

// --- Catalog Endpoints ---

func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "catalog served from static site"})
}

func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/api/marketplace/apps/")
	if slug == "" {
		h.writeError(w, http.StatusBadRequest, "slug required")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "message": "catalog served from static site"})
}

func (h *Handler) ListBundles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "bundles served from static site"})
}

// --- Provisioning Endpoints ---

type ProvisionRequest struct {
	CompanyName string            `json:"companyName"`
	Email       string            `json:"email"`
	Subdomain   string            `json:"subdomain"`
	Size        string            `json:"size"`
	Apps        []string          `json:"apps"`
	AddOns      []string          `json:"addOns"`
	Config      map[string]string `json:"config"`
}

func (h *Handler) CreateProvision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CompanyName == "" || req.Email == "" || req.Subdomain == "" || req.Size == "" || len(req.Apps) == 0 {
		h.writeError(w, http.StatusBadRequest, "companyName, email, subdomain, size, and apps are required")
		return
	}

	provisionID := uuid.New().String()
	tenantID := uuid.New().String()
	now := time.Now()

	// Build provisioning steps
	steps := []store.ProvisionStep{
		{Name: "Creating virtual cluster", Status: store.StatusPending},
		{Name: "Configuring networking", Status: store.StatusPending},
	}
	for _, app := range req.Apps {
		steps = append(steps, store.ProvisionStep{
			Name:   "Deploying " + app,
			Status: store.StatusPending,
		})
	}
	steps = append(steps,
		store.ProvisionStep{Name: "Configuring TLS certificates", Status: store.StatusPending},
		store.ProvisionStep{Name: "Running health checks", Status: store.StatusPending},
	)

	// Generate JWT for the new tenant
	token, err := h.generateJWT(tenantID, req.Email)
	if err != nil {
		log.Printf("JWT generation error: %v", err)
		h.writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	provision := &store.Provision{
		ID:          provisionID,
		TenantID:    tenantID,
		CompanyName: req.CompanyName,
		Email:       req.Email,
		Subdomain:   req.Subdomain,
		Size:        req.Size,
		Apps:        req.Apps,
		AddOns:      req.AddOns,
		Status:      store.StatusProvisioning,
		Steps:       steps,
		CreatedAt:   now,
		UpdatedAt:   now,
		JWTToken:    token,
	}

	h.Store.CreateProvision(provision)

	// Start async provisioning
	go h.runProvisioning(provision)

	log.Printf("Provision started: id=%s tenant=%s apps=%v size=%s", provisionID, tenantID, req.Apps, req.Size)

	h.writeJSON(w, http.StatusCreated, map[string]string{
		"provisionId": provisionID,
		"tenantId":    tenantID,
		"status":      string(store.StatusProvisioning),
		"token":       token,
	})
}

func (h *Handler) GetProvisionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/marketplace/provisions/")
	parts := strings.SplitN(id, "/", 2)
	provisionID := parts[0]

	if provisionID == "" {
		h.writeError(w, http.StatusBadRequest, "provision ID required")
		return
	}

	provision := h.Store.GetProvision(provisionID)
	if provision == nil {
		h.writeError(w, http.StatusNotFound, "provision not found")
		return
	}

	// Calculate progress percentage
	total := len(provision.Steps)
	done := 0
	for _, step := range provision.Steps {
		if step.Status == store.StatusCompleted {
			done++
		}
	}
	pct := 0
	if total > 0 {
		pct = (done * 100) / total
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"id":       provision.ID,
		"tenantId": provision.TenantID,
		"status":   provision.Status,
		"steps":    provision.Steps,
		"progress": pct,
	})
}

// --- Tenant Endpoints ---

func (h *Handler) TenantRouter(w http.ResponseWriter, r *http.Request) {
	// Extract tenant ID and sub-path from URL
	path := strings.TrimPrefix(r.URL.Path, "/api/marketplace/tenants/")
	parts := strings.SplitN(path, "/", 2)
	tenantID := parts[0]

	if tenantID == "" {
		h.writeError(w, http.StatusBadRequest, "tenant ID required")
		return
	}

	// Authenticate
	authTenantID, err := h.authenticateTenant(r)
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if authTenantID != tenantID {
		h.writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	subPath := ""
	if len(parts) > 1 {
		subPath = parts[1]
	}

	switch {
	case subPath == "" && r.Method == http.MethodGet:
		h.GetTenant(w, r, tenantID)
	case subPath == "" && r.Method == http.MethodDelete:
		h.DeleteTenant(w, r, tenantID)
	case subPath == "scale" && r.Method == http.MethodPost:
		h.ScaleTenant(w, r, tenantID)
	case subPath == "suspend" && r.Method == http.MethodPost:
		h.SuspendTenant(w, r, tenantID)
	case subPath == "resume" && r.Method == http.MethodPost:
		h.ResumeTenant(w, r, tenantID)
	case subPath == "backup" && r.Method == http.MethodPost:
		h.BackupTenant(w, r, tenantID)
	case subPath == "apps" && r.Method == http.MethodPost:
		h.AddApp(w, r, tenantID)
	case subPath == "domains" && r.Method == http.MethodPost:
		h.AddDomain(w, r, tenantID)
	case strings.HasPrefix(subPath, "apps/") && strings.HasSuffix(subPath, "/restart") && r.Method == http.MethodPost:
		appSlug := strings.TrimPrefix(subPath, "apps/")
		appSlug = strings.TrimSuffix(appSlug, "/restart")
		h.RestartApp(w, r, tenantID, appSlug)
	default:
		h.writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) GetTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	h.writeJSON(w, http.StatusOK, tenant)
}

func (h *Handler) DeleteTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// In production: commit deletion to Git, Flux removes vCluster
	log.Printf("Tenant deletion requested: %s", tenantID)

	tenant.VClusterStatus = "deleting"
	h.Store.UpdateTenant(tenant)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleting"})
}

type ScaleRequest struct {
	Size string `json:"size"`
}

func (h *Handler) ScaleTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	var req ScaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	validSizes := map[string]string{"xs": "XS", "s": "S", "m": "M", "l": "L"}
	label, ok := validSizes[req.Size]
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid size: must be xs, s, m, or l")
		return
	}

	log.Printf("Tenant %s scaling from %s to %s", tenantID, tenant.Size, req.Size)

	tenant.Size = req.Size
	tenant.SizeLabel = label
	h.Store.UpdateTenant(tenant)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "scaling", "size": req.Size})
}

func (h *Handler) SuspendTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	tenant.VClusterStatus = "suspended"
	h.Store.UpdateTenant(tenant)
	log.Printf("Tenant %s suspended", tenantID)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "suspended"})
}

func (h *Handler) ResumeTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	tenant.VClusterStatus = "running"
	h.Store.UpdateTenant(tenant)
	log.Printf("Tenant %s resumed", tenantID)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

func (h *Handler) BackupTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	log.Printf("Backup triggered for tenant %s", tenantID)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "backup_started"})
}

type AddAppRequest struct {
	Slug string `json:"slug"`
}

func (h *Handler) AddApp(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	var req AddAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// Check if app already deployed
	for _, app := range tenant.Apps {
		if app.Slug == req.Slug {
			h.writeError(w, http.StatusConflict, "app already deployed")
			return
		}
	}

	log.Printf("Adding app %s to tenant %s", req.Slug, tenantID)

	h.writeJSON(w, http.StatusAccepted, map[string]string{"status": "deploying", "app": req.Slug})
}

type AddDomainRequest struct {
	Domain string `json:"domain"`
}

func (h *Handler) AddDomain(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	var req AddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if req.Domain == "" {
		h.writeError(w, http.StatusBadRequest, "domain required")
		return
	}

	log.Printf("Adding domain %s to tenant %s", req.Domain, tenantID)

	h.writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "configuring",
		"domain": req.Domain,
		"cname":  tenant.Subdomain + ".openova.cloud",
	})
}

func (h *Handler) RestartApp(w http.ResponseWriter, r *http.Request, tenantID, appSlug string) {
	tenant := h.Store.GetTenant(tenantID)
	if tenant == nil {
		h.writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	found := false
	for _, app := range tenant.Apps {
		if app.Slug == appSlug {
			found = true
			break
		}
	}
	if !found {
		h.writeError(w, http.StatusNotFound, "app not found")
		return
	}

	log.Printf("Restarting app %s for tenant %s", appSlug, tenantID)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "restarting", "app": appSlug})
}
