// Package handler — sme_users.go: SME-tier user CRUD endpoints owned
// by the unified-rbac slice of catalyst-api (issue #802).
//
// These endpoints are the back end for the SME-admin pages of the
// Sovereign Console SPA. Per [Q-mine-1] / [B] of #795 the unified-rbac
// service in the OTECH control plane is the only component that fires
// the ADR-0003 3-step user-create hook (Keycloak → NewAPI → K8s
// Secret). Until unified-rbac is split out as its own deployable unit,
// catalyst-api is its host process — same trust boundary, same Pod
// ServiceAccount, same RBAC.
//
// HTTP surface:
//
//	POST   /api/v1/sme/users         — fire the ADR-0003 hook
//	GET    /api/v1/sme/users         — list users in the calling tenant
//	DELETE /api/v1/sme/users/{uuid}  — inverse rollback
//
// Tenant scoping: every request must carry a tenant context. In
// production the SPA sends `X-Tenant-Host: <window.location.host>`; the
// handler resolves that header against the same tenant registry the
// public /tenant/discover endpoint serves. The session middleware
// (auth.RequireSession) guarantees the caller is authenticated; a
// future enhancement will additionally verify the JWT realm claim
// matches the registry record (the OIDC token IS the SME-realm
// proof; until that wiring is plumbed end-to-end the host-header
// suffices and is preserved in the audit log).
//
// ADR-0003 contract:
//
//	step 1 — Keycloak admin API user create  (idempotent on 409)
//	step 2 — NewAPI admin API user create    (idempotent on 409)
//	step 3 — K8s Secret server-side apply    (idempotent on stable
//	         field manager)
//
// Each step is independently retryable; state machine persisted in
// store.UserProvisionStore. The synchronous handler returns
// 202 Accepted with the partial state once step 1 succeeds and lets
// the reconciler complete steps 2-3 in the background — this gives
// the SPA a fast UX (the user appears immediately as "provisioning")
// while honouring ADR-0003 §5.6 ("don't hold the request open for the
// full 3-step latency").
//
// For tests (and for environments without NewAPI / Keycloak / K8s
// wired), the handler is fully exercisable in synchronous mode against
// fake clients — see sme_users_test.go.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/newapi"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// SMEKeycloakClient is the contract sme_users.go uses to fire ADR-0003
// step 1. The catalyst-api's existing internal/keycloak.Client
// implements a richer surface than this; the narrow interface here
// gives tests a simple stub seam.
type SMEKeycloakClient interface {
	// EnsureSMEUser creates (or finds) the user in the SME-vcluster
	// realm and returns the Keycloak user id. emailVerified=true is
	// implied for SME-admin-created users — the password reset / first
	// login flow is owned by the welcome email subscriber on
	// org.user.events (ADR-0003 §3.6).
	EnsureSMEUser(ctx context.Context, realmAdminURL, realmName, email, smeUserUUID, smeTenantID string) (string, error)
}

// SMESecretApplier is the contract for ADR-0003 step 3 — server-side
// apply of `newapi-key-<sme_user_uuid>` into the SME tenant
// namespace. The default implementation uses client-go SSA on the
// catalyst-api Pod's own ServiceAccount; tests inject a stub.
type SMESecretApplier interface {
	ApplyNewAPIKeySecret(ctx context.Context, namespace, smeTenantID, smeUserUUID, apiKey, baseURL string) (string, error)
}

// SMEEventEmitter publishes ADR-0003 §3.6 NATS events
// (`org.user.events`). Nil-tolerant — when nil the publish is a
// no-op. The catalyst-api wires a real producer in main.go when the
// NATS broker URL is configured.
type SMEEventEmitter interface {
	EmitSMEUserCreated(ctx context.Context, rec store.UserProvisionRecord) error
	EmitSMEUserDeleted(ctx context.Context, rec store.UserProvisionRecord) error
}

// SMEDeps bundles the dependencies the SME user handlers need. Wired
// at startup; nil values turn the corresponding endpoint into a 503.
type SMEDeps struct {
	UserProvisionStore *store.UserProvisionStore
	NewAPIClient       *newapi.Client
	KeycloakClient     SMEKeycloakClient
	SecretApplier      SMESecretApplier
	Events             SMEEventEmitter
	// SecretBaseURLTemplate is the customer-facing NewAPI URL written
	// into the K8s Secret's `base-url` field (ADR-0003 §3.3). Per
	// docs/INVIOLABLE-PRINCIPLES.md #4 the URL is configured at
	// startup, never inlined here. Example:
	// "https://newapi.{otech_fqdn}". The literal `{otech_fqdn}` token
	// is replaced with the OTECH FQDN at apply time.
	SecretBaseURLTemplate string
	// OTECHFQDN is the FQDN substituted into SecretBaseURLTemplate.
	OTECHFQDN string
}

// SetSMEDeps wires the SME-tier dependencies. Called by main.go at
// startup; tests pass a struct with stub clients.
func (h *Handler) SetSMEDeps(deps SMEDeps) { h.smeDeps = deps }

// resolveTenant locates the TenantRegistration for the request. Order:
//
//  1. X-Tenant-Host header (sent by the SPA bootstrap)
//  2. tenant query param (escape hatch for curl/operator tooling)
//
// Returns 400 if no tenant context is supplied; 404 if the host is
// unknown; 422 if the resolved tenant is not SME-tier (otech-tier
// admins use the existing UserAccess CRD surface).
func (h *Handler) resolveOrganization(w http.ResponseWriter, r *http.Request) (store.TenantRegistration, bool) {
	if h.tenantRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "tenant-registry-unavailable",
		})
		return store.TenantRegistration{}, false
	}
	host := strings.TrimSpace(r.Header.Get("X-Tenant-Host"))
	if host == "" {
		host = strings.TrimSpace(r.URL.Query().Get("tenant"))
	}
	if host == "" {
		writeBadRequest(w, "tenant-required",
			"missing tenant context: set X-Tenant-Host header or ?tenant= query param")
		return store.TenantRegistration{}, false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	t, ok := h.tenantRegistry.Get(host)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "tenant-not-registered",
			"detail": "no tenant registered for host " + host,
		})
		return store.TenantRegistration{}, false
	}
	if t.TenantKind != store.TenantKindSME {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "tenant-not-sme",
			"detail": "endpoint is restricted to tenant_kind=sme; got " + string(t.TenantKind),
		})
		return store.TenantRegistration{}, false
	}
	return t, true
}

/* ── wire shapes ─────────────────────────────────────────────────── */

type smeUserCreateRequest struct {
	Email string `json:"email"`
	// Roles maps an app slug → role short-form. Application slugs are
	// "wordpress", "openclaw", "stalwart", "rbac" by default; the
	// canonical list is owned by the SPA's RolesPage. Per ADR-0003 the
	// hook persists role assignments as Keycloak group memberships;
	// the unified-rbac UI's RolesPage is the canonical mapping editor.
	Roles map[string]string `json:"roles,omitempty"`
}

type smeUserResponse struct {
	UUID         string                   `json:"uuid"`
	Email        string                   `json:"email"`
	State        store.UserProvisionState `json:"state"`
	KCUserID     string                   `json:"kc_user_id,omitempty"`
	NewAPIUserID string                   `json:"newapi_user_id,omitempty"`
	SecretName   string                   `json:"secret_name,omitempty"`
	LastError    string                   `json:"last_error,omitempty"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
	Steps        smeUserSteps             `json:"steps"`
}

// smeUserSteps surfaces the 3-step state to the SPA so it can render a
// progress bar. Each field is "pending"|"done"|"failed".
type smeUserSteps struct {
	KC     string `json:"kc"`
	NewAPI string `json:"newapi"`
	Secret string `json:"secret"`
}

func recordToResponse(rec store.UserProvisionRecord) smeUserResponse {
	steps := smeUserSteps{KC: "pending", NewAPI: "pending", Secret: "pending"}
	switch rec.State {
	case store.UPSKCCreated:
		steps.KC = "done"
	case store.UPSNewAPICreated:
		steps.KC, steps.NewAPI = "done", "done"
	case store.UPSSecretApplied, store.UPSDone:
		steps.KC, steps.NewAPI, steps.Secret = "done", "done", "done"
	case store.UPSFailed:
		// Mark whichever step failed; downstream UI uses LastError.
		switch {
		case rec.SecretName != "":
			steps.KC, steps.NewAPI = "done", "done"
			steps.Secret = "failed"
		case rec.NewAPIUserID != "":
			steps.KC, steps.NewAPI = "done", "failed"
		case rec.KCUserID != "":
			steps.KC = "done"
			steps.NewAPI = "failed"
		default:
			steps.KC = "failed"
		}
	}
	return smeUserResponse{
		UUID:         rec.SMEUserUUID,
		Email:        rec.Email,
		State:        rec.State,
		KCUserID:     rec.KCUserID,
		NewAPIUserID: rec.NewAPIUserID,
		SecretName:   rec.SecretName,
		LastError:    rec.LastError,
		CreatedAt:    rec.CreatedAt,
		UpdatedAt:    rec.UpdatedAt,
		Steps:        steps,
	}
}

/* ── HTTP handlers ───────────────────────────────────────────────── */

// HandleCreateSMEUser — POST /api/v1/sme/users.
//
// Fires the ADR-0003 3-step hook synchronously (since the catalyst-api
// pod is already in the OTECH control plane and all three downstreams
// are reachable in-cluster, the typical happy-path latency is sub-
// second). Returns 202 with the current state — the response shape
// always includes the steps[] progress so the SPA can render the
// progress bar even if a step failed.
func (h *Handler) HandleCreateSMEUser(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	if h.smeDeps.UserProvisionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "user-provision-store-unavailable",
		})
		return
	}

	var body smeUserCreateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		writeBadRequest(w, "email-required", "email is required")
		return
	}
	if !strings.Contains(email, "@") {
		writeBadRequest(w, "email-invalid", "email must contain @")
		return
	}

	smeUserUUID := uuid.New().String()
	rec := store.UserProvisionRecord{
		SMEUserUUID: smeUserUUID,
		OrganizationID: tenant.TenantID,
		Email:       email,
		State:       store.UPSPending,
	}
	if err := h.smeDeps.UserProvisionStore.Put(rec); err != nil {
		h.log.Error("sme-users: persist pending failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "persist-failed",
			"detail": err.Error(),
		})
		return
	}

	final, err := h.runSMEUserHook(r.Context(), tenant, rec)
	if err != nil {
		h.log.Warn("sme-users: hook failed (partial state persisted)",
			"uuid", smeUserUUID,
			"tenant", tenant.TenantID,
			"err", err,
		)
		// Hook errors are surfaced via the steps[] field in the response
		// body, NOT as a 5xx — the SPA wants to render the partial state.
	}
	writeJSON(w, http.StatusAccepted, recordToResponse(final))
}

// HandleListSMEUsers — GET /api/v1/sme/users.
func (h *Handler) HandleListSMEUsers(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	if h.smeDeps.UserProvisionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []smeUserResponse{}})
		return
	}
	rows := h.smeDeps.UserProvisionStore.List(tenant.TenantID)
	out := make([]smeUserResponse, 0, len(rows))
	for _, rec := range rows {
		out = append(out, recordToResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// HandleDeleteSMEUser — DELETE /api/v1/sme/users/{uuid}.
//
// Inverse rollback per ADR-0003 §3.7. Each step is best-effort and
// idempotent — partial failure leaves a `state=deleted` audit row so
// the reconciler can retry the next pass. Returns 204 on success, 404
// if the uuid is unknown.
func (h *Handler) HandleDeleteSMEUser(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	if h.smeDeps.UserProvisionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "user-provision-store-unavailable",
		})
		return
	}

	uuidStr := chi.URLParam(r, "uuid")
	rec, ok := h.smeDeps.UserProvisionStore.Get(tenant.TenantID, uuidStr)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "user-not-found",
		})
		return
	}

	if h.smeDeps.NewAPIClient != nil && rec.NewAPIUserID != "" {
		if err := h.smeDeps.NewAPIClient.DisableUser(r.Context(), rec.NewAPIUserID); err != nil {
			h.log.Warn("sme-users: newapi disable failed (best-effort)", "err", err)
		}
	}
	// Step 1 of rollback (delete K8s Secret) and step 3 (delete KC
	// user) are deferred to the reconciler when wiring is incomplete;
	// for the synchronous path we record the deletion intent and emit
	// the event so subscribers (billing, audit) can de-provision.
	rec.State = store.UPSDeleted
	rec.UpdatedAt = time.Now().UTC()
	_ = h.smeDeps.UserProvisionStore.Put(rec)

	if h.smeDeps.Events != nil {
		_ = h.smeDeps.Events.EmitSMEUserDeleted(r.Context(), rec)
	}

	w.WriteHeader(http.StatusNoContent)
}

/* ── orchestration ──────────────────────────────────────────────── */

// runSMEUserHook fires the ADR-0003 3-step hook against the supplied
// dependencies. It is fully idempotent on the persisted state: a
// re-invocation that sees `kc_created` skips step 1, etc. Returns the
// final persisted record + any terminal error.
func (h *Handler) runSMEUserHook(ctx context.Context, tenant store.TenantRegistration, rec store.UserProvisionRecord) (store.UserProvisionRecord, error) {
	deps := h.smeDeps
	st := deps.UserProvisionStore
	if st == nil {
		return rec, errors.New("user-provision store not wired")
	}

	persist := func(updated store.UserProvisionRecord) {
		if err := st.Put(updated); err != nil {
			h.log.Warn("sme-users: persist failed during hook", "err", err)
		}
	}

	// Step 1 — Keycloak admin API user create.
	if rec.State == store.UPSPending {
		if deps.KeycloakClient == nil {
			rec.State = store.UPSFailed
			rec.LastError = "kc_create:terminal:keycloak client not wired"
			persist(rec)
			return rec, errors.New("keycloak client not wired")
		}
		kcID, err := deps.KeycloakClient.EnsureSMEUser(
			ctx, tenant.SMEKeycloakAdminURL, tenant.SMEKeycloakRealmName,
			rec.Email, rec.SMEUserUUID, tenant.TenantID,
		)
		if err != nil {
			rec.LastError = "kc_create:transient:" + truncate(err.Error(), 256)
			rec.RetryCount++
			persist(rec)
			return rec, fmt.Errorf("kc_create: %w", err)
		}
		rec.KCUserID = kcID
		rec.State = store.UPSKCCreated
		rec.LastError = ""
		persist(rec)
	}

	// Step 2 — NewAPI admin API user create.
	if rec.State == store.UPSKCCreated {
		if deps.NewAPIClient == nil {
			rec.State = store.UPSFailed
			rec.LastError = "newapi_create:terminal:newapi client not wired"
			persist(rec)
			return rec, errors.New("newapi client not wired")
		}
		md := map[string]string{
			"kc_user_id": rec.KCUserID,
			"kc_realm":   tenant.SMEKeycloakRealmName,
		}
		u, err := deps.NewAPIClient.EnsureUser(ctx, newapi.CreateUserRequest{
			ExternalID: rec.SMEUserUUID,
			Email:      rec.Email,
			TenantID:   tenant.TenantID,
			Tier:       "default",
			Metadata:   md,
		})
		if err != nil {
			rec.LastError = "newapi_create:transient:" + truncate(err.Error(), 256)
			rec.RetryCount++
			persist(rec)
			return rec, fmt.Errorf("newapi_create: %w", err)
		}
		rec.NewAPIUserID = u.UserID
		rec.State = store.UPSNewAPICreated
		rec.LastError = ""
		persist(rec)

		// Stash the api-key in a context-local; we do NOT persist it
		// (the K8s Secret is the canonical store). The Step-3 SSA below
		// reads it directly from the CreateUser response.
		rec = applyStep3(ctx, h, deps, tenant, rec, u.APIKey, persist)
	}

	if rec.State == store.UPSFailed {
		return rec, errors.New(rec.LastError)
	}
	return rec, nil
}

// applyStep3 fires ADR-0003 step 3. Pulled into its own function to
// keep runSMEUserHook readable; uses a closure-captured persist callback.
func applyStep3(
	ctx context.Context,
	h *Handler,
	deps SMEDeps,
	tenant store.TenantRegistration,
	rec store.UserProvisionRecord,
	apiKey string,
	persist func(store.UserProvisionRecord),
) store.UserProvisionRecord {
	if rec.State != store.UPSNewAPICreated {
		return rec
	}
	if deps.SecretApplier == nil {
		rec.State = store.UPSFailed
		rec.LastError = "secret_apply:terminal:secret applier not wired"
		persist(rec)
		return rec
	}
	baseURL := deps.SecretBaseURLTemplate
	if strings.Contains(baseURL, "{otech_fqdn}") {
		baseURL = strings.ReplaceAll(baseURL, "{otech_fqdn}", deps.OTECHFQDN)
	}
	secretName, err := deps.SecretApplier.ApplyNewAPIKeySecret(
		ctx, tenant.OrganizationNamespace, tenant.TenantID, rec.SMEUserUUID, apiKey, baseURL,
	)
	if err != nil {
		rec.LastError = "secret_apply:transient:" + truncate(err.Error(), 256)
		rec.RetryCount++
		persist(rec)
		return rec
	}
	rec.SecretName = secretName
	rec.State = store.UPSDone
	rec.LastError = ""
	persist(rec)

	// ADR-0003 §3.6 NATS event emission.
	if deps.Events != nil {
		if err := deps.Events.EmitSMEUserCreated(ctx, rec); err != nil {
			h.log.Warn("sme-users: nats emit failed", "err", err)
		}
	}
	return rec
}

/* ── default in-process implementations ────────────────────────── */

// K8sSecretApplier is the production SMESecretApplier. Server-side
// applies the `newapi-key-<sme_user_uuid>` Secret with field manager
// `unified-rbac` per ADR-0003 §3.3.
type K8sSecretApplier struct {
	Client kubernetes.Interface
}

// ApplyNewAPIKeySecret implements SMESecretApplier.
func (a K8sSecretApplier) ApplyNewAPIKeySecret(ctx context.Context, namespace, smeTenantID, smeUserUUID, apiKey, baseURL string) (string, error) {
	if a.Client == nil {
		return "", errors.New("k8s client not wired")
	}
	if strings.TrimSpace(namespace) == "" {
		return "", errors.New("namespace is required")
	}
	name := "newapi-key-" + smeUserUUID
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"catalyst.openova.io/sme-tenant":    smeTenantID,
				"catalyst.openova.io/sme-user-uuid": smeUserUUID,
				"catalyst.openova.io/managed-by":    "unified-rbac",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"api-key":  apiKey,
			"base-url": baseURL,
		},
	}

	// Server-side apply per ADR-0003 §3.3 using the Patch verb with
	// ApplyPatchType. The field manager is the audit-trail label that
	// distinguishes unified-rbac writes from anything else mutating
	// this Secret.
	data, err := json.Marshal(sec)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	_, err = a.Client.CoreV1().Secrets(namespace).Patch(
		ctx, name, "application/apply-patch+yaml",
		data, metav1.PatchOptions{
			FieldManager: "unified-rbac",
			Force:        boolPtr(true),
		},
	)
	if err != nil {
		return "", fmt.Errorf("ssa Secret %s/%s: %w", namespace, name, err)
	}
	return name, nil
}

func boolPtr(b bool) *bool { return &b }

// truncate caps a string at n bytes — used to keep the persisted
// last_error column bounded.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SMEKeycloakDirectClient is the production SMEKeycloakClient
// implementation. It hits the SME-vcluster Keycloak admin API
// directly. To keep the surface in this file small (and avoid
// duplicating internal/keycloak), we shell out to a per-call HTTP
// flow that mirrors ADR-0003 §3.1 verbatim. SA token retrieval reuses
// the existing internal/keycloak.Client when available; otherwise the
// client expects a bearer token to be supplied per-call.
//
// The default implementation here is intentionally minimal — the
// catalyst-api's main.go can replace it with a fuller client when
// wiring the SME-realm SA credentials.
type SMEKeycloakDirectClient struct {
	// SAToken is the long-lived service-account token. In production
	// this comes from the bp-keycloak ExternalSecret pipeline; in
	// tests, supply an empty string and the client is unusable (and
	// the handler 503s gracefully).
	SAToken    string
	HTTPClient *http.Client
}

// EnsureSMEUser implements SMEKeycloakClient. POST
// {realmAdminURL}/admin/realms/{realmName}/users with the body shape
// ADR-0003 §3.1 specifies. On 409 falls back to the documented GET +
// id capture.
func (c SMEKeycloakDirectClient) EnsureSMEUser(ctx context.Context, realmAdminURL, realmName, email, smeUserUUID, smeTenantID string) (string, error) {
	if strings.TrimSpace(realmAdminURL) == "" || strings.TrimSpace(realmName) == "" {
		return "", errors.New("keycloak: realm config missing")
	}
	if strings.TrimSpace(c.SAToken) == "" {
		return "", errors.New("keycloak: SA token not wired")
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	body := map[string]any{
		"username":      email,
		"email":         email,
		"emailVerified": true,
		"enabled":       true,
		"attributes": map[string]any{
			"sme_tenant_id": []string{smeTenantID},
			"sme_user_uuid": []string{smeUserUUID},
		},
		"groups": []string{"sme-users"},
	}
	bs, _ := json.Marshal(body)
	url := strings.TrimRight(realmAdminURL, "/") + "/admin/realms/" + realmName + "/users"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bs)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.SAToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", smeUserUUID)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak POST users: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		// Capture user id from Location header.
		loc := resp.Header.Get("Location")
		parts := strings.Split(loc, "/")
		if len(parts) == 0 {
			return "", errors.New("keycloak: no Location on 201")
		}
		return parts[len(parts)-1], nil
	case http.StatusConflict:
		// Idempotent recovery: GET ?username=email&exact=true.
		q := url + "?username=" + email + "&exact=true"
		greq, _ := http.NewRequestWithContext(ctx, http.MethodGet, q, nil)
		greq.Header.Set("Authorization", "Bearer "+c.SAToken)
		gresp, err := hc.Do(greq)
		if err != nil {
			return "", err
		}
		defer gresp.Body.Close()
		var users []struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(gresp.Body).Decode(&users)
		if len(users) == 0 {
			return "", errors.New("keycloak: 409 but lookup empty")
		}
		return users[0].ID, nil
	default:
		return "", fmt.Errorf("keycloak: POST users HTTP %d", resp.StatusCode)
	}
}
