// sme_catalog_client.go — best-effort client for the in-cluster SME
// catalog microservice (services/catalog) used by HandleSovereignApps
// and HandleSovereignAppPublish.
//
// Lives at http://catalog.sme.svc.cluster.local:8082 when the chart's
// SME services tier is deployed. When the SME services tier is NOT
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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultSMECatalogURL  = "http://catalog.sme.svc.cluster.local:8082"
	smeCatalogURLEnv      = "CATALYST_SME_CATALOG_URL"
	smeCatalogCacheTTL    = 30 * time.Second
	smeCatalogProbeBudget = 1500 * time.Millisecond
)

// smeCatalogApp — minimal projection of the SME catalog's GET /catalog/apps
// response shape. Only the fields HandleSovereignApps consumes.
type smeCatalogApp struct {
	Slug      string `json:"slug"`
	Published bool   `json:"published"`
}

type smeCatalogClient struct {
	baseURL string
	http    *http.Client

	mu          sync.Mutex
	cached      map[string]bool // slug → published
	cachedAt    time.Time
	cachedError error
}

var smeCatalogSingleton *smeCatalogClient
var smeCatalogOnce sync.Once

// smeCatalog returns the package-level SME catalog client, lazily
// constructed on first use.
func smeCatalog() *smeCatalogClient {
	smeCatalogOnce.Do(func() {
		base := strings.TrimSpace(os.Getenv(smeCatalogURLEnv))
		if base == "" {
			base = defaultSMECatalogURL
		}
		smeCatalogSingleton = &smeCatalogClient{
			baseURL: strings.TrimRight(base, "/"),
			http: &http.Client{
				Timeout: smeCatalogProbeBudget,
			},
		}
	})
	return smeCatalogSingleton
}

// PublishedBySlug returns a slug → published map. Returns nil + nil
// (no map, no error) when the SME catalog is not deployed on this
// Sovereign. Caller must treat nil as "marketplace feature unavailable
// on this Sovereign — don't render the Publish chip on any card".
//
// On transient errors (timeout, 5xx) returns the previous cached
// result if still within TTL, else nil + nil. This handler is never
// the operator's source of truth; the SME catalog itself is.
func (c *smeCatalogClient) PublishedBySlug(ctx context.Context) (map[string]bool, error) {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < smeCatalogCacheTTL {
		out := c.cached
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	url := c.baseURL + "/catalog/apps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("sme-catalog: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Common case: SME not deployed → DNS NXDOMAIN. Treat as
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
		Apps []smeCatalogApp `json:"apps"`
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
// SME catalog. Used by HandleSovereignAppPublish. Returns the upstream
// HTTP status verbatim (so a 404 from SME catalog stays 404, etc.).
//
// Wire-shape: the SME catalog's SetAppPublished
// (core/services/catalog/handlers/handlers.go:293) accepts the new
// state via the `?value=true|false` query param — no JSON body. The
// request body is therefore empty; passing one would be ignored.
//
// Auth: the SME catalog mounts JWTAuth on every /catalog/admin/* path
// (core/services/catalog/main.go:79-85) and requireAdmin then enforces
// role=superadmin OR sovereign-admin (the latter was added in the same
// PR so franchisee operators can manage their own Sovereign's catalog
// per docs/FRANCHISE-MODEL.md §3). Without an Authorization header the
// upstream returns 401 from JWTAuth ("missing or invalid authorization
// header") — that's the C4-012 / #1735 symptom. The bearer is minted
// by the caller (HandleSovereignAppPublish) via the canonical SME
// bridge (sme_billing_vouchers.go's mintSMEBridgeToken — same HS256
// `sme-secrets/JWT_SECRET` the gateway + billing service use) and
// passed in here as the `bearer` argument. Empty bearer signals the
// caller has no session; the SME catalog will then return 401 and the
// chroot surfaces it verbatim so the UI shows the auth gap rather
// than a silent success.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the token is NEVER logged.
func (c *smeCatalogClient) SetPublished(ctx context.Context, slug string, published bool, bearer string) (int, error) {
	if strings.TrimSpace(slug) == "" {
		return 0, fmt.Errorf("sme-catalog: slug is required")
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
