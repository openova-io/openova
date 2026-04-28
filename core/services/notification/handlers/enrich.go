package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Enricher looks up supplementary fields (tenant name, owner email) that
// are not carried in cross-service event payloads. Provisioning day-2
// events (provision.app_ready / app_removed / app_failed) only carry
// tenant_id + app_slug; the email template needs the human-readable org
// name and the recipient address.
//
// Implementation calls the tenant and auth services over the internal
// cluster network using a short-lived superadmin JWT minted with the
// shared JWT_SECRET. Only services inside the sme namespace can hit
// these endpoints; the gateway does not expose /tenant/admin or
// /auth/admin.
type Enricher struct {
	TenantURL string // e.g. http://tenant.sme.svc.cluster.local:8083
	AuthURL   string // e.g. http://auth.sme.svc.cluster.local:8081
	JWTSecret []byte
	HTTP      *http.Client
}

// NewEnricher constructs an Enricher. Leave URLs empty to disable — in
// that case Lookup returns zero values without error and the caller
// will skip the email.
func NewEnricher(tenantURL, authURL string, jwtSecret []byte) *Enricher {
	return &Enricher{
		TenantURL: tenantURL,
		AuthURL:   authURL,
		JWTSecret: jwtSecret,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
	}
}

// TenantInfo is the subset of tenant+owner fields needed for email bodies.
type TenantInfo struct {
	TenantID     string
	OrgName      string
	Subdomain    string
	WorkspaceURL string
	OwnerEmail   string
	OwnerName    string
}

// Lookup resolves a tenant_id into TenantInfo. Returns (nil, nil) if
// enrichment is disabled (no URLs configured) so callers can fall back
// to a skip-the-email path without treating missing config as an error.
func (e *Enricher) Lookup(ctx context.Context, tenantID string) (*TenantInfo, error) {
	if e == nil || e.TenantURL == "" || e.AuthURL == "" {
		return nil, nil
	}
	if tenantID == "" {
		return nil, fmt.Errorf("enrich: empty tenant_id")
	}

	token, err := e.serviceToken()
	if err != nil {
		return nil, fmt.Errorf("enrich: mint token: %w", err)
	}

	tenant, err := e.getTenant(ctx, tenantID, token)
	if err != nil {
		return nil, err
	}
	owner, err := e.getUser(ctx, tenant.OwnerID, token)
	if err != nil {
		return nil, err
	}
	return &TenantInfo{
		TenantID:     tenant.ID,
		OrgName:      tenant.Name,
		Subdomain:    tenant.Subdomain,
		WorkspaceURL: "https://" + tenant.Subdomain + ".openova.io",
		OwnerEmail:   owner.Email,
		OwnerName:    owner.Name,
	}, nil
}

// serviceToken mints a short-lived superadmin JWT that the tenant and
// auth services trust via the shared JWT_SECRET. Lifetime is kept
// deliberately short (2 minutes) — long enough for an event-handler
// chain but too short to be useful if exfiltrated from logs.
func (e *Enricher) serviceToken() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  "svc:notification",
		"role": "superadmin",
		"iat":  now.Unix(),
		"exp":  now.Add(2 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(e.JWTSecret)
}

// tenantRecord mirrors services/tenant/store.Tenant for the fields we read.
type tenantRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id"`
	Subdomain string `json:"subdomain"`
}

func (e *Enricher) getTenant(ctx context.Context, tenantID, token string) (*tenantRecord, error) {
	url := e.TenantURL + "/tenant/admin/tenants/" + tenantID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("enrich: build tenant req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enrich: tenant req: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrich: tenant lookup %s returned %d", tenantID, resp.StatusCode)
	}
	var out tenantRecord
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("enrich: decode tenant: %w", err)
	}
	return &out, nil
}

// userResponse mirrors the wrapper produced by auth's AdminGetUser.
type userResponse struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

// userRecord is the unwrapped user we care about.
type userRecord struct {
	ID    string
	Email string
	Name  string
}

func (e *Enricher) getUser(ctx context.Context, userID, token string) (*userRecord, error) {
	url := e.AuthURL + "/auth/admin/users/" + userID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("enrich: build user req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enrich: user req: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrich: user lookup %s returned %d", userID, resp.StatusCode)
	}
	var out userResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("enrich: decode user: %w", err)
	}
	return &userRecord{ID: out.User.ID, Email: out.User.Email, Name: out.User.Name}, nil
}
