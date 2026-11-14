// marketplace.go — real handlers for marketplace.domain.byod +
// marketplace.domain.subdomain.
//
// Scope (architecture.md §3 + line 164): an agent shipping an app from
// inside a Sandbox routinely needs to bind a public hostname to the
// per-Org workload — either a custom domain the operator already owns
// (BYOD) or a free subdomain under one of the Sovereign-managed pools
// (omani.homes / omani.rest / omani.trade / omani.works). The canonical implementation
// lives in `core/services/domain/handlers/handlers.go`:
//
//   - POST /domain/byod        registerBYODRequest{tenant_id, domain}
//                              → 201 {domain, cname_target, registrar, instructions}
//   - POST /domain/subdomains  registerSubdomainRequest{tenant_id, subdomain, tld}
//                              → 201 *store.Domain
//
// The Sandbox MCP server is a thin proxy: validate args, look up the
// Sandbox's tenant_id from env.TenantID, forward the call with the
// SANDBOX_TOKEN as the Authorization bearer, and shape the response
// into a structured envelope the agent can paste into its app config.
//
// This is consistent with sandbox.auth.* (which proxies Keycloak admin)
// and sandbox.db.* (which manipulates k8s CRs) — every cross-service
// surface is a thin wrapper over the canonical service handler.
//
// Wire contract:
//
//   - marketplace.domain.byod {fqdn} → POST <DomainAPIURL>/domain/byod
//     with body {tenant_id: env.TenantID, domain: fqdn}. Response carries
//     CNAMETarget the agent points its registrar at.
//
//   - marketplace.domain.subdomain {subdomain, parent_zone?} →
//     POST <DomainAPIURL>/domain/subdomains with body
//     {tenant_id: env.TenantID, subdomain, tld: parent_zone}. The
//     parent_zone defaults to the Sovereign's primary pool (typically
//     `omani.homes` / `omani.rest` / `omani.trade` / `omani.works`); the canonical allow-list
//     lives in core/services/domain/store.AllowedTLDs).
//
// Auth: same shape as sandbox.db.* — claims.OrgID must match env.OrgID,
// RequiredCapability = "marketplace", and every call requires
// env.DomainAPIURL + env.TenantID + env.SandboxToken to be set or the
// handler returns a clear "controller didn't wire marketplace" error.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// mpHTTPTimeout caps every marketplace-proxy call. The domain service
// runs in-cluster and a healthy WHOIS / DB write completes in <300ms;
// anything slower is a degraded backend and the agent wants a fast
// error, not a 60s hang.
const mpHTTPTimeout = 15 * time.Second

// mpFQDNRE constrains the `fqdn` arg of marketplace.domain.byod.
// Mirrors core/services/domain's validation surface (lowercase
// alnum + hyphen labels separated by dots, at least one dot, no
// leading/trailing dot or hyphen). Strict enough to reject obvious
// junk before we burn an HTTP roundtrip on the domain service.
var mpFQDNRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// mpSubdomainRE matches the same surface core/services/domain's
// subdomainRe enforces server-side. We pre-validate here so a bad
// argument doesn't cost a 400 round-trip.
var mpSubdomainRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// mpParentZoneRE constrains the `parent_zone` arg — must look like a
// public DNS suffix (`omani.homes`, `omani.rest`, `omani.trade`, `omani.works`). Same shape as
// mpFQDNRE.
var mpParentZoneRE = mpFQDNRE

// requireMarketplaceProxy is the canonical gate every marketplace.*
// handler runs first: the controller must have wired BOTH the domain
// service URL AND the tenant id. If either is empty we surface a
// well-typed error rather than letting an unauthenticated POST leak
// past as a confusing 401/404 from the domain service.
func requireMarketplaceProxy(env *Env) error {
	if env == nil {
		return errors.New("marketplace: server env missing on context")
	}
	if strings.TrimSpace(env.DomainAPIURL) == "" {
		return errors.New("marketplace: SANDBOX_DOMAIN_API_URL not set (sandbox-controller didn't wire the domain service URL)")
	}
	if strings.TrimSpace(env.TenantID) == "" {
		return errors.New("marketplace: SANDBOX_TENANT_ID not set (sandbox-controller didn't wire the tenant id)")
	}
	if strings.TrimSpace(env.SandboxToken) == "" {
		return errors.New("marketplace: SANDBOX_TOKEN not set (no bearer available to proxy to the domain service)")
	}
	return nil
}

// mpHTTPClient returns the per-process HTTP client used for every
// marketplace-proxy call. Centralised so the timeout + the Authorization
// header are applied uniformly.
func mpHTTPClient() *http.Client {
	return &http.Client{Timeout: mpHTTPTimeout}
}

// mpDo runs a JSON POST against the domain service. Body is closed
// before return; the response payload is returned verbatim so the
// caller can decode into a per-endpoint envelope.
func mpDo(ctx context.Context, env *Env, urlStr string, payload any) ([]byte, int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+env.SandboxToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := mpHTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("domain %s: %w", urlStr, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}

// --- marketplace.domain.byod ------------------------------------------

type marketplaceDomainBYODArgs struct {
	// FQDN — the operator-owned domain to bind (`acme.com`,
	// `app.example.org`). Must look like a public FQDN (at least one
	// dot, valid TLD-shaped suffix). The domain service performs WHOIS
	// + uniqueness checks server-side; we pre-validate the shape.
	FQDN string `json:"fqdn"`
}

// marketplaceDomainBYODResponse is the structured envelope the BYOD
// proxy returns. Mirrors `registerBYODResponse` in
// core/services/domain/handlers but renames the wire keys to a
// stable snake_case surface decoupled from any future domain-service
// refactor.
type marketplaceDomainBYODResponse struct {
	Status       string         `json:"status"`
	FQDN         string         `json:"fqdn"`
	CNAMETarget  string         `json:"cname_target"`
	Registrar    string         `json:"registrar,omitempty"`
	Instructions string         `json:"instructions,omitempty"`
	Domain       map[string]any `json:"domain,omitempty"`
	Note         string         `json:"note,omitempty"`
}

func marketplaceDomainBYOD(ctx context.Context, raw json.RawMessage) (any, error) {
	var args marketplaceDomainBYODArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("marketplace.domain.byod: invalid arguments: %w", err)
	}
	args.FQDN = strings.ToLower(strings.TrimSpace(args.FQDN))
	if args.FQDN == "" {
		return nil, errors.New("marketplace.domain.byod: `fqdn` is required")
	}
	if !mpFQDNRE.MatchString(args.FQDN) {
		return nil, fmt.Errorf("marketplace.domain.byod: `fqdn` must be a valid lowercase FQDN (matched against %s)", mpFQDNRE.String())
	}
	env := EnvFrom(ctx)
	if err := requireMarketplaceProxy(env); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"tenant_id": env.TenantID,
		"domain":    args.FQDN,
	}
	urlStr := strings.TrimRight(env.DomainAPIURL, "/") + "/domain/byod"
	body, status, err := mpDo(ctx, env, urlStr, payload)
	if err != nil {
		return nil, fmt.Errorf("marketplace.domain.byod: %w", err)
	}
	switch status {
	case http.StatusCreated:
		// Decode the canonical response. We surface the same fields back
		// to the agent under stable, snake_case keys so the wire shape
		// is decoupled from any future domain-service refactor.
		var resp struct {
			Domain       map[string]any `json:"domain"`
			CNAMETarget  string         `json:"cname_target"`
			Registrar    string         `json:"registrar"`
			Instructions string         `json:"instructions"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("marketplace.domain.byod: decode response: %w", err)
		}
		return marketplaceDomainBYODResponse{
			Status:       "Registered",
			FQDN:         args.FQDN,
			CNAMETarget:  resp.CNAMETarget,
			Registrar:    resp.Registrar,
			Instructions: resp.Instructions,
			Domain:       resp.Domain,
			Note:         "Set a CNAME at your registrar pointing the FQDN at `cname_target`, then call POST /domain/verify/{id} (or wait for the auto-verify cron).",
		}, nil
	case http.StatusConflict:
		// Idempotent re-call: surface the same shape with a non-Created
		// status so the agent can branch.
		return map[string]any{
			"status": "AlreadyExists",
			"fqdn":   args.FQDN,
			"note":   "Domain is already registered (idempotent no-op). Inspect via the domain service for the existing record.",
		}, nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, fmt.Errorf("marketplace.domain.byod: domain service rejected the Sandbox bearer (HTTP %d): %s", status, body)
	default:
		return nil, fmt.Errorf("marketplace.domain.byod: domain service returned %d: %s", status, body)
	}
}

func schemaMarketplaceDomainBYOD() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"fqdn"},
		"properties": map[string]any{
			"fqdn": map[string]any{
				"type":        "string",
				"description": "Operator-owned fully-qualified domain (e.g. `acme.com`, `app.example.org`). Lowercased; must contain at least one dot.",
				"pattern":     mpFQDNRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// --- marketplace.domain.subdomain --------------------------------------

type marketplaceDomainSubdomainArgs struct {
	// Subdomain — the per-pool label (`acme`, `dashboard`, `staging`).
	// 2-32 chars, lowercase alnum + hyphen. Must not contain
	// consecutive hyphens.
	Subdomain string `json:"subdomain"`

	// ParentZone — optional override for the parent pool zone
	// (`omani.homes`, `omani.rest`, `omani.trade`, `omani.works`). When empty we leave the
	// `tld` field off the proxied request and let the domain service
	// apply its own AllowedTLDs default.
	ParentZone string `json:"parent_zone,omitempty"`
}

// marketplaceDomainSubdomainResponse mirrors the canonical
// store.Domain shape returned by POST /domain/subdomains but adds
// the convenience `fqdn` field the agent typically wants
// (`<subdomain>.<parent_zone>`) without re-concatenating client-side.
type marketplaceDomainSubdomainResponse struct {
	Status     string         `json:"status"`
	FQDN       string         `json:"fqdn"`
	Subdomain  string         `json:"subdomain"`
	ParentZone string         `json:"parent_zone"`
	Domain     map[string]any `json:"domain,omitempty"`
	Note       string         `json:"note,omitempty"`
}

func marketplaceDomainSubdomain(ctx context.Context, raw json.RawMessage) (any, error) {
	var args marketplaceDomainSubdomainArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("marketplace.domain.subdomain: invalid arguments: %w", err)
	}
	args.Subdomain = strings.ToLower(strings.TrimSpace(args.Subdomain))
	args.ParentZone = strings.ToLower(strings.TrimSpace(args.ParentZone))
	if args.Subdomain == "" {
		return nil, errors.New("marketplace.domain.subdomain: `subdomain` is required")
	}
	if !mpSubdomainRE.MatchString(args.Subdomain) {
		return nil, fmt.Errorf("marketplace.domain.subdomain: `subdomain` must match %s", mpSubdomainRE.String())
	}
	if strings.Contains(args.Subdomain, "--") {
		return nil, errors.New("marketplace.domain.subdomain: `subdomain` must not contain consecutive hyphens")
	}
	if args.ParentZone != "" && !mpParentZoneRE.MatchString(args.ParentZone) {
		return nil, fmt.Errorf("marketplace.domain.subdomain: `parent_zone` must be a valid FQDN (matched against %s)", mpParentZoneRE.String())
	}
	env := EnvFrom(ctx)
	if err := requireMarketplaceProxy(env); err != nil {
		return nil, err
	}
	// When the caller omits parent_zone we leave the TLD field empty
	// and let the domain service apply its allow-list default. That
	// keeps Sandbox-side decisions out of the way of the per-Sovereign
	// pool config (which lives in core/services/domain/store.AllowedTLDs
	// + the chart-level config, NOT in the MCP server).
	payload := map[string]any{
		"tenant_id": env.TenantID,
		"subdomain": args.Subdomain,
	}
	if args.ParentZone != "" {
		payload["tld"] = args.ParentZone
	}
	urlStr := strings.TrimRight(env.DomainAPIURL, "/") + "/domain/subdomains"
	body, status, err := mpDo(ctx, env, urlStr, payload)
	if err != nil {
		return nil, fmt.Errorf("marketplace.domain.subdomain: %w", err)
	}
	switch status {
	case http.StatusCreated:
		var record map[string]any
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, fmt.Errorf("marketplace.domain.subdomain: decode response: %w", err)
		}
		parent, _ := record["tld"].(string)
		if parent == "" {
			parent = args.ParentZone
		}
		full, _ := record["domain"].(string)
		if full == "" {
			full = args.Subdomain
			if parent != "" {
				full = full + "." + parent
			}
		}
		return marketplaceDomainSubdomainResponse{
			Status:     "Registered",
			FQDN:       full,
			Subdomain:  args.Subdomain,
			ParentZone: parent,
			Domain:     record,
			Note:       "Subdomain reserved + DNS verified at the pool zone. HTTPRoute / Gateway publication is the operator's downstream step.",
		}, nil
	case http.StatusConflict:
		return map[string]any{
			"status":      "AlreadyExists",
			"subdomain":   args.Subdomain,
			"parent_zone": args.ParentZone,
			"note":        "Subdomain is already taken (idempotent no-op).",
		}, nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, fmt.Errorf("marketplace.domain.subdomain: domain service rejected the Sandbox bearer (HTTP %d): %s", status, body)
	default:
		return nil, fmt.Errorf("marketplace.domain.subdomain: domain service returned %d: %s", status, body)
	}
}

func schemaMarketplaceDomainSubdomain() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"subdomain"},
		"properties": map[string]any{
			"subdomain": map[string]any{
				"type":        "string",
				"description": "Per-pool subdomain label (2-32 chars, lowercase alnum + hyphen).",
				"pattern":     mpSubdomainRE.String(),
			},
			"parent_zone": map[string]any{
				"type":        "string",
				"description": "Optional parent pool zone (`omani.homes`, `omani.rest`, `omani.trade`, `omani.works`). When omitted the domain service uses its configured AllowedTLDs default.",
				"pattern":     mpParentZoneRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// mpAppSlugRE constrains the `slug` arg of marketplace.app.install.
// Mirrors core/services/catalog/store.App.Slug shape: lowercase alnum
// + hyphen, 2-63 chars, no leading/trailing hyphen. Strict enough to
// reject obvious junk before the tenant-service round-trip.
var mpAppSlugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// marketplaceAppInstallArgs — input schema for marketplace.app.install.
// Only `slug` is required; `plan` and `params` are forwarded verbatim
// to the tenant-service which validates against the catalog Blueprint.
type marketplaceAppInstallArgs struct {
	Slug   string         `json:"slug"`
	Plan   string         `json:"plan,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// marketplaceAppInstall — Pillar 4 step 2d (#2040). Forwards an app
// install request to the Organization tenant-service `POST /tenant/orgs/{id}/apps`
// canonical handler (core/services/tenant/handlers/apps.go::InstallApp)
// which publishes `tenant.app_install_requested` on NATS; provisioning
// consumes + creates the Flux HelmRelease on the tenant's vCluster.
//
// Auth: SandboxToken forwarded as `Authorization: Bearer …` (HS256 Organization
// wire contract). The tenant-service jwtMiddleware validates against
// its own Organization JWT secret; the Sandbox's per-Org claims propagate
// downstream into the provisioning consumer (org_id, owner_id audit).
func marketplaceAppInstall(ctx context.Context, raw json.RawMessage) (any, error) {
	var args marketplaceAppInstallArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("marketplace.app.install: invalid arguments: %w", err)
	}
	args.Slug = strings.ToLower(strings.TrimSpace(args.Slug))
	if args.Slug == "" {
		return nil, errors.New("marketplace.app.install: `slug` is required")
	}
	if !mpAppSlugRE.MatchString(args.Slug) {
		return nil, fmt.Errorf("marketplace.app.install: `slug` must match %s", mpAppSlugRE.String())
	}
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("marketplace.app.install: server env missing on context")
	}
	if strings.TrimSpace(env.TenantAPIURL) == "" {
		return nil, errors.New("marketplace.app.install: SANDBOX_TENANT_API_URL not set (sandbox-controller didn't wire the tenant-service URL)")
	}
	if strings.TrimSpace(env.TenantID) == "" {
		return nil, errors.New("marketplace.app.install: SANDBOX_TENANT_ID not set (controller hasn't bound this Sandbox to a tenant)")
	}
	if strings.TrimSpace(env.SandboxToken) == "" {
		return nil, errors.New("marketplace.app.install: SANDBOX_TOKEN not set (no Organization bearer to forward to tenant-service)")
	}
	payload := map[string]any{
		"slug": args.Slug,
	}
	if args.Plan != "" {
		payload["plan"] = args.Plan
	}
	if args.Params != nil {
		payload["params"] = args.Params
	}
	urlStr := strings.TrimRight(env.TenantAPIURL, "/") + "/orgs/" + env.TenantID + "/apps"
	body, status, err := mpDo(ctx, env, urlStr, payload)
	if err != nil {
		return nil, fmt.Errorf("marketplace.app.install: %w", err)
	}
	switch status {
	case http.StatusOK, http.StatusAccepted, http.StatusCreated:
		// tenant-service returns {status:"queued", apps:[…], app_states:{…},
		// deployed:[…]}. Surface the whole shape so the agent can branch
		// on app_states (e.g., agent waits for "installed" before binding
		// HTTPRoute or running smoke tests).
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("marketplace.app.install: decode response: %w", err)
		}
		resp["slug"] = args.Slug
		resp["http_status"] = status
		return resp, nil
	case http.StatusConflict:
		// App already installed — idempotent.
		return map[string]any{
			"status": "AlreadyInstalled",
			"slug":   args.Slug,
			"note":   "App is already deployed on this tenant (idempotent no-op).",
		}, nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, fmt.Errorf("marketplace.app.install: tenant-service rejected the Sandbox bearer (HTTP %d): %s", status, body)
	default:
		return nil, fmt.Errorf("marketplace.app.install: tenant-service returned %d: %s", status, body)
	}
}

func schemaMarketplaceAppInstall() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"slug"},
		"properties": map[string]any{
			"slug": map[string]any{
				"type":        "string",
				"description": "Blueprint slug from the Sovereign catalog (e.g. `wordpress`, `ghost`, `gitea`). Lowercase alnum + hyphen, 2-63 chars.",
				"pattern":     mpAppSlugRE.String(),
			},
			"plan": map[string]any{
				"type":        "string",
				"description": "Optional plan slug (e.g. `s`, `m`, `l`). Defaults to the tenant's current plan when omitted.",
			},
			"params": map[string]any{
				"type":                 "object",
				"description":          "Optional per-Blueprint configSchema values (e.g. replicas, disk_gb). Forwarded verbatim to the install handler.",
				"additionalProperties": true,
			},
		},
		"additionalProperties": false,
	}
}
