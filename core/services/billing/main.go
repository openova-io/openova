package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/billing/handlers"
	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/db"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	databaseURL := getEnv("DATABASE_URL", "postgres://billing:billing@localhost:5432/billing?sslmode=disable")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8085")
	successURL := getEnv("SUCCESS_URL", "https://sme.openova.io/checkout")
	cancelURL := getEnv("CANCEL_URL", "https://sme.openova.io/checkout")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.sme.svc.cluster.local:8082")
	tenantURL := getEnv("TENANT_URL", "http://tenant.sme.svc.cluster.local:8083")
	// NOTIFICATION_SERVICE_URL — sme-notification's POST /notification/send
	// endpoint, used by D28 (voucher-issued gifting email). Default points at
	// the in-cluster ClusterIP DNS the chart wires per sovereign.
	notificationURL := getEnv("NOTIFICATION_SERVICE_URL", "http://notification.sme.svc.cluster.local:8087/notification/send")
	// SOVEREIGN_FQDN — per-Sovereign apex domain (e.g. "omani.works") used to
	// build the public marketplace redeem URL in voucher emails. NEVER
	// hardcoded; the chart pipes it from `billing.sovereignFQDN`. Empty is
	// tolerated for dev loops — the template emits a relative-ish fallback.
	sovereignFQDN := getEnv("SOVEREIGN_FQDN", "")
	// NATS_URL — JetStream broker URL for the catalyst.usage.recorded
	// metering stream (#798). Empty disables the metering subscriber so
	// developer environments without NATS can still run the legacy
	// RedPanda consumer + HTTP API. Per #795 [Q-mine-3] this is the
	// canonical event spine going forward; per ADR-0001 §6 we never
	// fall back to RedPanda for new metering subjects.
	natsURL := getEnv("NATS_URL", "")

	pg := db.MustConnect(databaseURL)
	defer pg.Close()
	slog.Info("connected to PostgreSQL")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	billingStore := store.New(pg)
	if err := billingStore.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("database migration complete")

	producer, err := events.NewProducer(redpandaBrokers)
	if err != nil {
		slog.Error("failed to create events producer", "error", err)
		os.Exit(1)
	}
	defer producer.Close()
	slog.Info("connected to RedPanda")

	h := &handlers.Handler{
		Store:           billingStore,
		Producer:        producer,
		SuccessURL:      successURL,
		CancelURL:       cancelURL,
		CatalogURL:      catalogURL,
		TenantURL:       tenantURL,
		NotificationURL: notificationURL,
		SovereignFQDN:   sovereignFQDN,
		NotificationClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	// Start the tenant-events consumer so tenant.deleted cascades clean up
	// Stripe subs, draft/open invoices, and credit-ledger audit rows. See
	// issue #94. Runs in a background goroutine; broker outages log + retry.
	tenantConsumer, err := events.NewConsumer(
		redpandaBrokers,
		"billing-tenant-events",
		[]string{"sme.tenant.events"},
	)
	if err != nil {
		slog.Error("failed to create tenant-events consumer", "error", err)
		os.Exit(1)
	}
	defer tenantConsumer.Close()
	billingTenantHandler := &handlers.TenantConsumer{Store: billingStore}
	go func() {
		if err := billingTenantHandler.Start(context.Background(), tenantConsumer); err != nil {
			slog.Error("billing tenant-events consumer stopped", "error", err)
		}
	}()
	slog.Info("billing tenant-events consumer started",
		"topic", "sme.tenant.events", "group", "billing-tenant-events")

	// NATS metering consumer (#798 §B). Per #795 [Q-mine-3] +
	// ADR-0001 §6, NATS JetStream is the canonical bus for new
	// subjects; the RedPanda consumer above is legacy and intentionally
	// left in place for sme.tenant.events. When NATS_URL is unset the
	// subscriber is skipped — the synchronous HTTP path
	// (POST /billing/metering/record) still works against the same
	// store schema, so unit tests + dev loops keep functioning.
	if natsURL != "" {
		natsConn, err := events.ConnectNATS(natsURL)
		if err != nil {
			slog.Error("failed to connect to NATS", "url", natsURL, "error", err)
			os.Exit(1)
		}
		// EnsureUsageStream is idempotent — safe to call on every
		// startup. sme-billing owns the Stream lifecycle because it is
		// the canonical consumer; publishers (the NewAPI metering
		// sidecar) rely on the Stream existing.
		ensureCtx, ensureCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := natsConn.EnsureUsageStream(ensureCtx); err != nil {
			ensureCancel()
			slog.Error("failed to ensure JetStream catalyst.usage stream", "error", err)
			os.Exit(1)
		}
		ensureCancel()
		defer natsConn.Close()
		slog.Info("connected to NATS JetStream", "url", natsURL,
			"stream", events.StreamCatalystUsage,
			"subject", events.SubjectUsageRecorded)

		subCtx := context.Background()
		usageSub, err := natsConn.SubscribeUsageRecorded(subCtx)
		if err != nil {
			slog.Error("failed to subscribe to catalyst.usage.recorded", "error", err)
			os.Exit(1)
		}
		defer usageSub.Close()

		meteringConsumer := &handlers.MeteringConsumer{
			Store:            billingStore,
			CustomerResolver: handlers.DefaultCustomerResolver{Store: billingStore},
		}
		go func() {
			if err := meteringConsumer.Start(subCtx, usageSub); err != nil {
				slog.Error("billing metering consumer stopped", "error", err)
			}
		}()
		slog.Info("billing metering consumer started",
			"subject", events.SubjectUsageRecorded,
			"durable", events.ConsumerSMEBillingMetering)
	} else {
		slog.Warn("NATS_URL is empty — metering consumer disabled (HTTP path still active)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler())

	billingRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	// Public paths that bypass JWT validation. These match the gateway's
	// public routes (D29 voucher-redeem zero-touch flow). The gateway
	// passes these through with no auth header; this service must accept
	// them OR the marketplace /redeem landing returns 401 to unauth visitors.
	// Caught live on t132 2026-05-16 after PR #1559 made the gateway public —
	// the billing service was still JWT-gating internally.
	publicBillingPaths := map[string]bool{
		"/billing/webhook":                  true, // Stripe (sig-verified)
		"/billing/vouchers/redeem-preview":  true, // D29 voucher landing
		"/billing/plans":                    true, // marketplace pricing
		"/billing/addons":                   true, // marketplace add-on pricing
	}

	mux.Handle("/billing/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicBillingPaths[r.URL.Path] {
			billingRoutes.ServeHTTP(w, r)
			return
		}
		jwtMiddleware(billingRoutes).ServeHTTP(w, r)
	}))

	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting billing service", "port", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
