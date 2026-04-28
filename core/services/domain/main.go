package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/openova-io/openova/core/services/domain/handlers"
	"github.com/openova-io/openova/core/services/domain/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	// Configuration from environment.
	mongoURI := getEnv("MONGODB_URI", "mongodb://ferretdb:27017")
	mongoDBName := getEnv("MONGODB_DB", "domains")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8086")
	cnameTarget := getEnv("CNAME_TARGET", "sme.openova.io")
	tenantURL := getEnv("TENANT_URL", "http://tenant.sme.svc.cluster.local:8083")

	// Connect to MongoDB (FerretDB).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		slog.Error("failed to connect to MongoDB", "error", err)
		os.Exit(1)
	}
	if err := client.Ping(ctx, nil); err != nil {
		slog.Error("failed to ping MongoDB", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			slog.Error("failed to disconnect MongoDB", "error", err)
		}
	}()
	slog.Info("connected to FerretDB", "uri", mongoURI, "db", mongoDBName)

	// Create events producer.
	producer, err := events.NewProducer(redpandaBrokers)
	if err != nil {
		slog.Error("failed to create events producer", "error", err)
		os.Exit(1)
	}
	defer producer.Close()
	slog.Info("connected to RedPanda")

	// Initialize store and handler.
	domainStore := store.New(client, mongoDBName)
	h := &handlers.Handler{
		Store:       domainStore,
		Producer:    producer,
		CNAMETarget: cnameTarget,
		TenantURL:   tenantURL,
	}

	// Start the tenant-events consumer so tenant.deleted cascades remove
	// domain records (subdomains + BYOD). See issue #95. Broker outages
	// log + retry — shared Consumer commits only after a nil handler return.
	tenantEventsConsumer, err := events.NewConsumer(
		redpandaBrokers,
		"domain-tenant-events",
		[]string{"sme.tenant.events"},
	)
	if err != nil {
		slog.Error("failed to create tenant-events consumer", "error", err)
		os.Exit(1)
	}
	defer tenantEventsConsumer.Close()
	domainTenantHandler := &handlers.TenantConsumer{Store: domainStore}
	go func() {
		if err := domainTenantHandler.Start(context.Background(), tenantEventsConsumer); err != nil {
			slog.Error("domain tenant-events consumer stopped", "error", err)
		}
	}()
	slog.Info("domain tenant-events consumer started",
		"topic", "sme.tenant.events", "group", "domain-tenant-events")

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Domain routes — the gateway already validates the JWT, but we parse it
	// again here so handlers can read the caller's identity (user_id, role)
	// for tenant-membership authorization (issue #79). The availability check
	// endpoint is public so the console can query it pre-login.
	domainRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)
	mux.Handle("/domain/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/domain/check/") {
			domainRoutes.ServeHTTP(w, r)
			return
		}
		jwtMiddleware(domainRoutes).ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting domain service", "port", port)
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
