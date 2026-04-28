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

	"github.com/openova-io/openova/core/services/catalog/handlers"
	"github.com/openova-io/openova/core/services/catalog/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	// Configuration from environment.
	mongoURI := getEnv("MONGODB_URI", "mongodb://ferretdb:27017")
	mongoDBName := getEnv("MONGODB_DB", "catalog")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8082")

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
	catalogStore := store.New(client, mongoDBName)
	h := &handlers.Handler{
		Store:    catalogStore,
		Producer: producer,
	}

	// Seed default data if the database is empty.
	h.SeedIfEmpty(context.Background())

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Catalog routes — JWT middleware applied only to admin endpoints.
	catalogRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/catalog/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/catalog/admin/") {
			jwtMiddleware(catalogRoutes).ServeHTTP(w, r)
			return
		}
		catalogRoutes.ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting catalog service", "port", port)
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
