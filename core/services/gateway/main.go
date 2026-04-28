package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/openova-io/openova/core/services/shared/db"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	port := getEnv("PORT", "8080")
	jwtSecret := getEnv("JWT_SECRET", "dev-secret")
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	valkeyAddr := getEnv("VALKEY_ADDR", "valkey:6379")
	rateLimit := getEnvInt("RATE_LIMIT_RPM", 120)
	// Trusted proxy CIDRs are consulted when deciding whether to honour
	// X-Forwarded-For / X-Real-IP headers. Defaults to the k3s pod CIDR so
	// Traefik (which sits in that subnet) is trusted but direct callers
	// from outside the cluster are not.
	trustedProxies := parseTrustedProxies(getEnv("GATEWAY_TRUSTED_PROXIES", "10.42.0.0/16"))
	if len(trustedProxies) == 0 {
		log.Printf("warning: GATEWAY_TRUSTED_PROXIES empty — forwarded-IP headers will be ignored for all callers")
	} else {
		log.Printf("gateway: trusting X-Forwarded-For / X-Real-IP from %d CIDR(s)", len(trustedProxies))
	}

	// Service URLs (all in same K8s namespace).
	authURL := getEnv("AUTH_URL", "http://auth:8081")
	catalogURL := getEnv("CATALOG_URL", "http://catalog:8082")
	tenantURL := getEnv("TENANT_URL", "http://tenant:8083")
	provisioningURL := getEnv("PROVISIONING_URL", "http://provisioning:8084")
	billingURL := getEnv("BILLING_URL", "http://billing:8085")
	domainURL := getEnv("DOMAIN_URL", "http://domain:8086")
	notificationURL := getEnv("NOTIFICATION_URL", "http://notification:8087")

	routes := []Route{
		// Auth routes are public (auth handles its own validation).
		{PathPrefix: "/api/auth/", Upstream: authURL, StripPrefix: "/api", Public: true},
		// Catalog public endpoints.
		{PathPrefix: "/api/catalog/apps", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/catalog/industries", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/catalog/bundles", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/catalog/plans", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/catalog/addons", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		// Catalog admin (requires auth).
		{PathPrefix: "/api/catalog/admin/", Upstream: catalogURL, StripPrefix: "/api", Public: false},
		// Tenant — slug availability is public so the checkout page can check
		// before auth; everything else requires auth.
		{PathPrefix: "/api/tenant/check-slug/", Upstream: tenantURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/tenant/", Upstream: tenantURL, StripPrefix: "/api", Public: false},
		// Provisioning — status polling is public, admin/start require auth.
		{PathPrefix: "/api/provisioning/status/", Upstream: provisioningURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/provisioning/tenant/", Upstream: provisioningURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/provisioning/", Upstream: provisioningURL, StripPrefix: "/api", Public: false},
		// Billing (mixed — webhook is public, rest requires auth).
		{PathPrefix: "/api/billing/webhook", Upstream: billingURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/billing/", Upstream: billingURL, StripPrefix: "/api", Public: false},
		// Domain (requires auth).
		{PathPrefix: "/api/domain/", Upstream: domainURL, StripPrefix: "/api", Public: false},
		// Notification (internal, requires auth).
		{PathPrefix: "/api/notification/", Upstream: notificationURL, StripPrefix: "/api", Public: false},
	}

	// Connect to Valkey for rate limiting.
	valkeyClient, err := db.ConnectValkey(valkeyAddr)
	if err != nil {
		log.Printf("warning: valkey unavailable (%v), rate limiting disabled", err)
	}

	rl := NewRateLimiter(valkeyClient, rateLimit, trustedProxies)
	proxy := NewProxyHandler(routes, []byte(jwtSecret), trustedProxies)

	// Build handler chain: outermost listed first.
	handler := middleware.Chain(
		proxy,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
		rl.Middleware,
	)

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler())
	mux.Handle("/", handler)

	log.Printf("gateway listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
