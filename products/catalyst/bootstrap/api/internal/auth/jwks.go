package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// jwksEntry represents a single JWK entry from a JWKS endpoint.
type jwksEntry struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKSCache is a thread-safe cache for JWKS keys with a configurable TTL.
// On first access or after TTL expiry, it re-fetches the keys from the
// configured JWKS URL.
type JWKSCache struct {
	mu        sync.RWMutex
	url       string
	keys      []jwksEntry
	fetchedAt time.Time
	ttl       time.Duration
}

// NewJWKSCache returns a new JWKSCache that fetches from jwksURL and caches
// for ttl duration. The cache is lazy — keys are fetched on first call to Keys.
func NewJWKSCache(jwksURL string, ttl time.Duration) *JWKSCache {
	return &JWKSCache{url: jwksURL, ttl: ttl}
}

// Keys returns the cached JWKS keys, re-fetching if the TTL has expired.
func (c *JWKSCache) Keys(ctx context.Context) ([]jwksEntry, error) {
	// Fast path: cache is fresh.
	c.mu.RLock()
	if len(c.keys) > 0 && time.Since(c.fetchedAt) < c.ttl {
		keys := c.keys
		c.mu.RUnlock()
		return keys, nil
	}
	c.mu.RUnlock()

	// Slow path: need to refresh.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if len(c.keys) > 0 && time.Since(c.fetchedAt) < c.ttl {
		return c.keys, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks: build req: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch %s: %w", c.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: unexpected status %d from %s", resp.StatusCode, c.url)
	}

	var body struct {
		Keys []jwksEntry `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("jwks: decode response: %w", err)
	}

	c.keys = body.Keys
	c.fetchedAt = time.Now()
	return c.keys, nil
}
