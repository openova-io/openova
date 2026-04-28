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

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/tenant/catalog"
	"github.com/openova-io/openova/core/services/tenant/handlers"
	"github.com/openova-io/openova/core/services/tenant/store"
)

func main() {
	// Configuration from environment.
	mongoURI := getEnv("MONGODB_URI", "mongodb://ferretdb:27017")
	mongoDBName := getEnv("MONGODB_DB", "tenants")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.sme.svc.cluster.local:8082")
	provisioningURL := getEnv("PROVISIONING_URL", "http://provisioning.sme.svc.cluster.local:8084")
	port := getEnv("PORT", "8083")

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
	tenantStore := store.New(client, mongoDBName)
	catalogClient := catalog.New(catalogURL)
	h := &handlers.Handler{
		Store:           tenantStore,
		Producer:        producer,
		Catalog:         catalogClient,
		ProvisioningURL: provisioningURL,
		DayTwoLocks:     handlers.NewTenantLocks(),
	}
	slog.Info("catalog client configured", "url", catalogURL)
	slog.Info("provisioning URL configured", "url", provisioningURL)

	// Subscribe to provision events so tenant status reflects provisioning outcome.
	provConsumer, err := events.NewConsumer(redpandaBrokers, "tenant-service", []string{"sme.provision.events"})
	if err != nil {
		slog.Error("failed to create provision consumer", "error", err)
		os.Exit(1)
	}
	defer provConsumer.Close()
	consumerHandler := &handlers.ConsumerHandler{Store: tenantStore}
	go func() {
		if err := consumerHandler.Start(context.Background(), provConsumer); err != nil {
			slog.Error("provision consumer stopped", "error", err)
		}
	}()

	// Members-cleanup consumer — purges member rows as soon as a tenant is
	// soft-deleted so authz checks during the teardown window don't see
	// stale membership. Separate consumer group so offsets don't contend
	// with the provision-events subscriber above. See issue #96.
	membersConsumer, err := events.NewConsumer(
		redpandaBrokers,
		"tenant-members-cleanup",
		[]string{"sme.tenant.events"},
	)
	if err != nil {
		slog.Error("failed to create members-cleanup consumer", "error", err)
		os.Exit(1)
	}
	defer membersConsumer.Close()
	membersCleanup := &handlers.MembersCleanupConsumer{Store: tenantStore}
	go func() {
		if err := membersCleanup.Start(context.Background(), membersConsumer); err != nil {
			slog.Error("tenant members-cleanup consumer stopped", "error", err)
		}
	}()
	slog.Info("tenant members-cleanup consumer started",
		"topic", "sme.tenant.events", "group", "tenant-members-cleanup")

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Tenant routes — JWT middleware applied to all except slug check.
	tenantRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/tenant/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slug check is public — no JWT required.
		if strings.HasPrefix(r.URL.Path, "/tenant/check-slug/") {
			tenantRoutes.ServeHTTP(w, r)
			return
		}
		// Everything else requires JWT authentication.
		jwtMiddleware(tenantRoutes).ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting tenant service", "port", port)
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
