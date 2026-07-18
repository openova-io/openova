package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/openova-io/openova/core/services/shared/db"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/valkey-io/valkey-go"
)

func main() {
	port := getEnv("PORT", "8080")
	jwtSecret := getEnv("JWT_SECRET", "dev-secret")
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	valkeyAddr := getEnv("VALKEY_ADDR", "valkey:6379")
	// Cross-ns bp-valkey deployments require auth (bitnami default).
	// Issue #863: empty username + password keep contabo-mkt's auth-less
	// in-namespace Valkey working unchanged.
	valkeyUsername := getEnv("VALKEY_USERNAME", "")
	valkeyPassword := getEnv("VALKEY_PASSWORD", "")
	rateLimit := getEnvInt("RATE_LIMIT_RPM", 120)
	// #3376 row-92: a tighter burst limit for the voucher-redeem path so a
	// >5-in-a-few-seconds redeem/checkout flood trips a 429 (the global
	// RATE_LIMIT_RPM is too lenient). Default 5 requests per 10s, per IP.
	redeemRateLimit := getEnvInt("REDEEM_RATE_LIMIT", 5)
	redeemRateWindow := getEnvInt("REDEEM_RATE_WINDOW_SEC", 10)
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
		// #4525 — the Sovereign's REAL configured regions, so the marketplace
		// funnel BCP picker fetches a live region set instead of hardcoded
		// Hetzner names. Public (the picker renders pre-auth, like plans/addons).
		{PathPrefix: "/api/catalog/regions", Upstream: catalogURL, StripPrefix: "/api", Public: true},
		// Catalog admin (requires auth).
		{PathPrefix: "/api/catalog/admin/", Upstream: catalogURL, StripPrefix: "/api", Public: false},
		// Organization directory + CRUD (#3383: canonical `/api/organizations`,
		// served by the tenant service's `/organizations` mux). slug
		// availability is public so the checkout page can check before auth;
		// everything else requires auth.
		{PathPrefix: "/api/organizations", Upstream: tenantURL, StripPrefix: "/api", Public: false},
		// naming-guard: alias — legacy `/api/tenant/*` paths, one-release
		// deprecation (the marketplace SPA stays on these until removal).
		{PathPrefix: "/api/tenant/check-slug/", Upstream: tenantURL, StripPrefix: "/api", Public: true},
		// naming-guard: alias — legacy `/api/tenant/*`, one-release deprecation.
		{PathPrefix: "/api/tenant/", Upstream: tenantURL, StripPrefix: "/api", Public: false},
		// Provisioning — status polling is public, admin/start require auth.
		{PathPrefix: "/api/provisioning/status/", Upstream: provisioningURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/provisioning/tenant/", Upstream: provisioningURL, StripPrefix: "/api", Public: true},
		// #5205 — same-origin readiness proxy the funnel completion
		// interstitial polls instead of a cross-origin no-cors fetch straight
		// at the per-Org console host (whose opaque Response the browser can
		// never read the status of). Public: polled before the customer has
		// any session, from the marketplace's own origin.
		{PathPrefix: "/api/provisioning/console-ready", Upstream: provisioningURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/provisioning/", Upstream: provisioningURL, StripPrefix: "/api", Public: false},
		// Billing (mixed — public list of plans/addons/redeem-preview for
		// the marketplace landing + /redeem flow; webhook for Stripe;
		// everything else requires auth).
		// D29 fix 2026-05-16: /redeem?code=XXX page calls
		// /api/billing/vouchers/redeem-preview unauthenticated per
		// docs/FRANCHISE-MODEL.md §3; the catch-all entry was returning
		// 401 and breaking the entire voucher-redeem zero-touch flow.
		// Plans + addons must also be public so the marketplace landing
		// can render pricing without a session.
		{PathPrefix: "/api/billing/webhook", Upstream: billingURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/billing/vouchers/redeem-preview", Upstream: billingURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/billing/plans", Upstream: billingURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/billing/addons", Upstream: billingURL, StripPrefix: "/api", Public: true},
		{PathPrefix: "/api/billing/", Upstream: billingURL, StripPrefix: "/api", Public: false},
		// Domain (requires auth).
		{PathPrefix: "/api/domain/", Upstream: domainURL, StripPrefix: "/api", Public: false},
		// Notification (internal, requires auth).
		{PathPrefix: "/api/notification/", Upstream: notificationURL, StripPrefix: "/api", Public: false},
	}

	// Connect to Valkey for rate limiting. Use the auth-aware constructor
	// when a password is configured; otherwise fall through to the no-auth
	// path so existing deployments without `requirepass` keep working.
	var valkeyClient valkey.Client
	var err error
	if valkeyPassword != "" {
		valkeyClient, err = db.ConnectValkeyWithAuth(valkeyAddr, valkeyUsername, valkeyPassword)
	} else {
		valkeyClient, err = db.ConnectValkey(valkeyAddr)
	}
	if err != nil {
		log.Printf("warning: valkey unavailable (%v), rate limiting disabled", err)
	}

	rl := NewRateLimiter(valkeyClient, rateLimit, redeemRateLimit, redeemRateWindow, trustedProxies)
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
