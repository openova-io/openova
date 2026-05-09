// Package config parses runtime configuration from environment
// variables. Per docs/INVIOLABLE-PRINCIPLES.md #4 nothing here is
// hardcoded — every URL, FQDN, and scope name is operator-overridable.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config carries the runtime configuration for catalyst-catalog.
type Config struct {
	// ListenAddr is the HTTP listen address. Default ":8080".
	ListenAddr string

	// GiteaURL is the Gitea root URL WITHOUT `/api/v1` (the unified
	// Gitea client appends it). Per ADR-0001 §2.7 catalog data lives in
	// Gitea — there is no separate database.
	GiteaURL string

	// GiteaToken is the static Gitea admin access token used for HTTP
	// API auth. Must be present at startup.
	GiteaToken string

	// PublicCatalogOrg is the Gitea Org slug holding the public-mirror
	// blueprint repositories. Defaults to "catalog" per
	// docs/EPICS-1-6-unified-design.md §5.4.
	PublicCatalogOrg string

	// SovereignCatalogOrg is the Gitea Org slug holding the Sovereign-
	// curated blueprint repositories. Defaults to "catalog-sovereign".
	SovereignCatalogOrg string

	// OrgPrivateRepoSuffix is the per-Org private blueprint repo name
	// (one repo per Org under the Org's namespace). Defaults to
	// "shared-blueprints".
	OrgPrivateRepoSuffix string

	// CacheTTL is the in-memory cache TTL for blueprint.yaml reads.
	// Defaults to 30s.
	CacheTTL time.Duration

	// CacheCapacity is the max number of cache entries (LRU eviction).
	// Defaults to 1024.
	CacheCapacity int

	// SovereignFQDN is purely informational (logged at startup) — the
	// catalog never embeds it in URLs since the Gitea client targets the
	// in-cluster Service. Empty when not set.
	SovereignFQDN string

	// SessionCookieName is the name of the session cookie carrying the
	// caller's Keycloak JWT. Default "catalyst_session" (matches
	// catalyst-api's seam at products/catalyst/bootstrap/api/internal/auth).
	SessionCookieName string

	// AnonymousReads — when true, callers without a session can list the
	// public + sovereign-curated catalog (no per-Org private). Default
	// false (closed by default per docs/INVIOLABLE-PRINCIPLES.md #1).
	AnonymousReads bool
}

// Load returns a Config populated from the environment, applying
// defaults for omitted values and validating required fields.
func Load() (Config, error) {
	c := Config{
		ListenAddr:           envDefault("CATALOG_LISTEN_ADDR", ":8080"),
		GiteaURL:             os.Getenv("CATALYST_GITEA_URL"),
		GiteaToken:           os.Getenv("CATALYST_GITEA_TOKEN"),
		PublicCatalogOrg:     envDefault("CATALOG_PUBLIC_ORG", "catalog"),
		SovereignCatalogOrg:  envDefault("CATALOG_SOVEREIGN_ORG", "catalog-sovereign"),
		OrgPrivateRepoSuffix: envDefault("CATALOG_ORG_PRIVATE_REPO", "shared-blueprints"),
		SovereignFQDN:        os.Getenv("SOVEREIGN_FQDN"),
		SessionCookieName:    envDefault("CATALOG_SESSION_COOKIE", "catalyst_session"),
		AnonymousReads:       parseBool(os.Getenv("CATALOG_ANONYMOUS_READS")),
	}

	ttlStr := envDefault("CATALOG_CACHE_TTL_SECONDS", "30")
	ttlSec, err := strconv.Atoi(ttlStr)
	if err != nil || ttlSec < 0 {
		return Config{}, fmt.Errorf("config: CATALOG_CACHE_TTL_SECONDS must be a non-negative integer, got %q", ttlStr)
	}
	c.CacheTTL = time.Duration(ttlSec) * time.Second

	capStr := envDefault("CATALOG_CACHE_CAPACITY", "1024")
	cap, err := strconv.Atoi(capStr)
	if err != nil || cap <= 0 {
		return Config{}, fmt.Errorf("config: CATALOG_CACHE_CAPACITY must be a positive integer, got %q", capStr)
	}
	c.CacheCapacity = cap

	if c.GiteaURL == "" {
		return Config{}, errors.New("config: CATALYST_GITEA_URL is required")
	}
	if strings.HasSuffix(c.GiteaURL, "/api/v1") {
		return Config{}, fmt.Errorf("config: CATALYST_GITEA_URL must NOT include /api/v1 (per pkg/gitea convention); got %q", c.GiteaURL)
	}
	if c.GiteaToken == "" {
		return Config{}, errors.New("config: CATALYST_GITEA_TOKEN is required")
	}
	return c, nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
