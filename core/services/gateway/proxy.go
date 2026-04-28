package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Route defines a path prefix to upstream mapping.
type Route struct {
	PathPrefix  string // e.g., "/api/auth/"
	Upstream    string // e.g., "http://auth:8081"
	StripPrefix string // prefix to strip before forwarding, e.g., "/api"
	Public      bool   // if true, skip JWT validation
}

// ProxyHandler routes incoming requests to upstream services based on path prefix.
type ProxyHandler struct {
	routes         []Route // sorted by PathPrefix length descending (longest first)
	jwtSecret      []byte
	proxies        map[string]*httputil.ReverseProxy
	trustedProxies []*net.IPNet
}

// NewProxyHandler creates a ProxyHandler with pre-built reverse proxies for each upstream.
func NewProxyHandler(routes []Route, jwtSecret []byte, trustedProxies []*net.IPNet) *ProxyHandler {
	// Sort routes by prefix length descending for longest-prefix-first matching.
	sorted := make([]Route, len(routes))
	copy(sorted, routes)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i].PathPrefix) > len(sorted[j].PathPrefix)
	})

	proxies := make(map[string]*httputil.ReverseProxy)
	for _, route := range sorted {
		if _, exists := proxies[route.Upstream]; exists {
			continue
		}
		target, err := url.Parse(route.Upstream)
		if err != nil {
			panic("invalid upstream URL: " + route.Upstream + ": " + err.Error())
		}
		proxy := httputil.NewSingleHostReverseProxy(target)

		// Override the Director to handle path stripping and header propagation.
		defaultDirector := proxy.Director
		proxy.Director = func(r *http.Request) {
			defaultDirector(r)
			// Host is already set by the default director to the target.
		}

		proxies[route.Upstream] = proxy
	}

	return &ProxyHandler{
		routes:         sorted,
		jwtSecret:      jwtSecret,
		proxies:        proxies,
		trustedProxies: trustedProxies,
	}
}

// ServeHTTP matches the request path to a route and proxies the request.
func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, route := range ph.routes {
		if !strings.HasPrefix(r.URL.Path, route.PathPrefix) {
			continue
		}

		// JWT validation for non-public routes.
		if !route.Public {
			claims, err := ph.validateJWT(r)
			if err != nil {
				respond.Error(w, http.StatusUnauthorized, "invalid or missing token")
				return
			}
			// Set identity headers for the upstream service.
			if sub, _ := claims["sub"].(string); sub != "" {
				r.Header.Set("X-User-ID", sub)
			}
			if role, _ := claims["role"].(string); role != "" {
				r.Header.Set("X-User-Role", role)
			}
			// Forward the Authorization header so upstream services can validate if needed.
		}

		// Strip prefix from path before forwarding.
		if route.StripPrefix != "" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, route.StripPrefix)
			if r.URL.RawPath != "" {
				r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, route.StripPrefix)
			}
		}

		// Propagate client IP to upstreams via X-Forwarded-For, but only
		// after stripping any forged value (clientIP consults the header
		// only when the direct peer is a trusted proxy).
		if xff := clientIP(r, ph.trustedProxies); xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		// X-Request-ID is already set by the RequestID middleware.

		proxy := ph.proxies[route.Upstream]
		proxy.ServeHTTP(w, r)
		return
	}

	respond.Error(w, http.StatusNotFound, "no route matched")
}

// validateJWT parses and validates the Bearer token from the Authorization header.
func (ph *ProxyHandler) validateJWT(r *http.Request) (jwt.MapClaims, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return nil, jwt.ErrSignatureInvalid
	}

	tokenStr := strings.TrimPrefix(auth, "Bearer ")
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return ph.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, jwt.ErrSignatureInvalid
	}
	return claims, nil
}
