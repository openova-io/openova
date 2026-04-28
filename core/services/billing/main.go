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
		Store:      billingStore,
		Producer:   producer,
		SuccessURL: successURL,
		CancelURL:  cancelURL,
		CatalogURL: catalogURL,
		TenantURL:  tenantURL,
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler())

	billingRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/billing/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Webhook endpoint is public (Stripe signature verification handles auth).
		if r.URL.Path == "/billing/webhook" && r.Method == http.MethodPost {
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
