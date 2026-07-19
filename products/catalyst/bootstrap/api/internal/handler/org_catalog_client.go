// org_catalog_client.go — best-effort client for the in-cluster Organization
// catalog microservice (services/catalog) used by HandleSovereignApps
// and HandleSovereignAppPublish.
//
// Lives at http://catalog.org-services.svc.cluster.local:8082 when the chart's
// Organization services tier is deployed. When the Organization services tier is NOT
// deployed (Sovereigns with marketplace.enabled=false, like omantel.biz),
// every call returns nil/false with no error logged at warn level —
// the chroot Sovereign Console renders without a Publish chip on the
// app cards, which is the correct empty state.
//
// 30-second response cache so HandleSovereignApps doesn't fan out a
// fresh GET on every poll. The cache is per-process; pod restart
// invalidates.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultOrgCatalogURL  = "http://catalog.org-services.svc.cluster.local:8082"
	orgCatalogURLEnv      = "CATALYST_ORG_CATALOG_URL"
	orgCatalogCacheTTL    = 30 * time.Second
	orgCatalogProbeBudget = 1500 * time.Millisecond

	// catalogEditGitBudget is the budget for the catalog-edit IaC commit
	// (#3668). The commit is the SOURCE-OF-TRUTH write — it must NOT share
	// the 1500ms commerce-store probe budget (orgCatalogProbeBudget), which
	// is sized for a single in-cluster HTTP probe, not a git round-trip.
	// writeCatalogEditToGit performs FOUR sequential Gitea API calls
	// (EnsureOrg, EnsureRepo, GetFile, PutFile) against a Gitea that — on a
	// cut-over Sovereign — is under Flux + mirror-job load. Sized for that
	// round-trip so a busy-but-healthy Gitea commits durably instead of the
	// write silently deadline-ing at 1500ms minus the commerce hop and the
	// edit evaporating on the next store rebuild (§3C). Overridable via the
	// canonical env so an operator can widen it on a slow Sovereign without a
	// code change (Inviolable Principle #4).
	catalogEditGitBudget    = 15 * time.Second
	catalogEditGitBudgetEnv = "CATALYST_CATALOG_EDIT_GIT_BUDGET"
)

// catalogEditGitBudgetDuration returns the catalog-edit git-commit budget,
// honouring CATALYST_CATALOG_EDIT_GIT_BUDGET (a Go duration string, e.g.
// "30s") when set + parseable, else the catalogEditGitBudget default.
func catalogEditGitBudgetDuration() time.Duration {
	if v := strings.TrimSpace(os.Getenv(catalogEditGitBudgetEnv)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return catalogEditGitBudget
}

// orgCatalogApp — minimal projection of the Organization catalog's GET /catalog/apps
// response shape. Only the fields HandleSovereignApps consumes.
type orgCatalogApp struct {
	Slug      string `json:"slug"`
	Published bool   `json:"published"`
}

type orgCatalogClient struct {
	baseURL string
	http    *http.Client

	mu          sync.Mutex
	cached      map[string]bool // slug → published
	cachedAt    time.Time
	cachedError error
}

var orgCatalogSingleton *orgCatalogClient
var orgCatalogOnce sync.Once

// orgCatalogTestOverride lets tests point orgCatalog() at an httptest
// server WITHOUT racing the sync.Once (which would otherwise pin the first
// caller's baseURL for the rest of the process). nil in production — the
// override is only ever set by the in-package test helper. Checked first in
// orgCatalog() so a test can swap + restore deterministically.
var orgCatalogTestOverride *orgCatalogClient

// orgCatalog returns the package-level Organization catalog client, lazily
// constructed on first use.
func orgCatalog() *orgCatalogClient {
	if orgCatalogTestOverride != nil {
		return orgCatalogTestOverride
	}
	orgCatalogOnce.Do(func() {
		base := strings.TrimSpace(os.Getenv(orgCatalogURLEnv))
		if base == "" {
			base = defaultOrgCatalogURL
		}
		orgCatalogSingleton = &orgCatalogClient{
			baseURL: strings.TrimRight(base, "/"),
			http: &http.Client{
				Timeout: orgCatalogProbeBudget,
			},
		}
	})
	return orgCatalogSingleton
}

// PublishedBySlug returns a slug → published map. Returns nil + nil
// (no map, no error) when the Organization catalog is not deployed on this
// Sovereign. Caller must treat nil as "marketplace feature unavailable
// on this Sovereign — don't render the Publish chip on any card".
//
// On transient errors (timeout, 5xx) returns the previous cached
// result if still within TTL, else nil + nil. This handler is never
// the operator's source of truth; the Organization catalog itself is.
func (c *orgCatalogClient) PublishedBySlug(ctx context.Context) (map[string]bool, error) {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < orgCatalogCacheTTL {
		out := c.cached
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	url := c.baseURL + "/catalog/apps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("org-catalog: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Common case: Organization not deployed → DNS NXDOMAIN. Treat as
		// "feature unavailable", not an error to surface.
		c.mu.Lock()
		c.cached = nil
		c.cachedAt = time.Now()
		c.mu.Unlock()
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var body struct {
		Apps []orgCatalogApp `json:"apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil
	}
	out := make(map[string]bool, len(body.Apps))
	for _, a := range body.Apps {
		if a.Slug == "" {
			continue
		}
		out[a.Slug] = a.Published
	}
	c.mu.Lock()
	c.cached = out
	c.cachedAt = time.Now()
	c.cachedError = nil
	c.mu.Unlock()
	return out, nil
}

// SetPublished — proxy PATCH /catalog/admin/apps/{slug}/publish to the
// Organization catalog. Used by HandleSovereignAppPublish. Returns the upstream
// HTTP status verbatim (so a 404 from Organization catalog stays 404, etc.).
//
// Wire-shape: the Organization catalog's SetAppPublished
// (core/services/catalog/handlers/handlers.go:293) accepts the new
// state via the `?value=true|false` query param — no JSON body. The
// request body is therefore empty; passing one would be ignored.
//
// Auth: the Organization catalog mounts JWTAuth on every /catalog/admin/* path
// (core/services/catalog/main.go:79-85) and requireAdmin then enforces
// role=superadmin OR sovereign-admin (the latter was added in the same
// PR so franchisee operators can manage their own Sovereign's catalog
// per docs/FRANCHISE-MODEL.md §3). Without an Authorization header the
// upstream returns 401 from JWTAuth ("missing or invalid authorization
// header") — that's the C4-012 / #1735 symptom. The bearer is minted
// by the caller (HandleSovereignAppPublish) via the canonical Organization
// bridge (org_billing_vouchers.go's mintOrgBridgeToken — same HS256
// `org-services-secrets/JWT_SECRET` the gateway + billing service use) and
// passed in here as the `bearer` argument. Empty bearer signals the
// caller has no session; the Organization catalog will then return 401 and the
// chroot surfaces it verbatim so the UI shows the auth gap rather
// than a silent success.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the token is NEVER logged.
func (c *orgCatalogClient) SetPublished(ctx context.Context, slug string, published bool, bearer string) (int, error) {
	if strings.TrimSpace(slug) == "" {
		return 0, fmt.Errorf("org-catalog: slug is required")
	}
	value := "false"
	if published {
		value = "true"
	}
	url := fmt.Sprintf("%s/catalog/admin/apps/%s/publish?value=%s", c.baseURL, slug, value)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Invalidate cache so the next HandleSovereignApps reflects the
	// new state without waiting 30s.
	c.mu.Lock()
	c.cached = nil
	c.cachedAt = time.Time{}
	c.mu.Unlock()
	return resp.StatusCode, nil
}

// PublicProxy forwards a GET to the Organization commerce catalog's PUBLIC list
// endpoint ("<baseURL>/catalog/{sub}") and returns (status, body,
// contentType, error). It is the read counterpart of AdminProxy for the
// Organizations commerce editors (issue #3378 DoD 7/8): the editors read
// the EXISTING public /catalog/{kind} list endpoints (no new business
// endpoint, §6).
//
// On the Sovereign console (served at console.<sovereign>), /api/* proxies
// through catalyst-api, which does NOT route /api/catalog/* to the catalog
// service the way the Organization/marketplace gateway does — so a bare
// GET /api/catalog/plans 404s on the console host. Routing the console's
// reads through catalyst-api (which CAN reach catalog.org-services.svc) closes that
// gap so the console plans/addons/bundles/industries/apps tables render the
// same rows the marketplace storefront shows.
//
// The public list endpoints carry no auth (core/services/catalog/main.go
// applies JWT only to /catalog/admin/*), so no bearer is sent.
func (c *orgCatalogClient) PublicProxy(
	ctx context.Context,
	subPath string,
) (int, []byte, string, error) {
	sub := "/" + strings.TrimLeft(strings.TrimSpace(subPath), "/")
	url := c.baseURL + "/catalog" + sub

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, resp.Header.Get("Content-Type"), nil
}

// AdminProxy forwards an arbitrary method + sub-path + JSON body to the
// Organization commerce catalog (services/catalog) and returns (status, body,
// contentType, error). It is the generic transport for the Organizations
// commerce editors (issue #3378 DoD 7/8) — the editors ride the EXISTING
// /catalog/admin/* endpoints (no new business endpoint, §6); this proxy
// is the same thin transport SetPublished uses, generalized.
//
// `subPath` is appended to "<baseURL>/catalog/admin" (e.g.
// "/plans", "/plans/{id}"). `body` is the verbatim request body bytes
// (nil for DELETE). The bearer is the canonical Organization bridge token minted
// by the caller (mintOrgBridgeToken) — empty bearer ⇒ the upstream
// JWTAuth returns 401 which we surface verbatim. Per
// docs/INVIOLABLE-PRINCIPLES.md #10 the token is never logged.
//
// Caching: the Organization catalog admin mutations invalidate the published
// cache so a publish/unpublish or app edit reflects on the next read.
func (c *orgCatalogClient) AdminProxy(
	ctx context.Context,
	method, subPath string,
	body []byte,
	bearer string,
) (int, []byte, string, error) {
	sub := "/" + strings.TrimLeft(strings.TrimSpace(subPath), "/")
	url := c.baseURL + "/catalog/admin" + sub

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")

	// A successful mutation invalidates the published cache.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.mu.Lock()
		c.cached = nil
		c.cachedAt = time.Time{}
		c.mu.Unlock()
	}
	return resp.StatusCode, respBody, ct, nil
}
