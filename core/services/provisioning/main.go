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

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/handlers"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	// Configuration from environment.
	mongoURI := getEnv("MONGODB_URI", "mongodb://ferretdb:27017")
	mongoDBName := getEnv("MONGODB_DB", "provisioning")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8084")
	gitBasePath := getEnv("GIT_BASE_PATH", "clusters/contabo-mkt/tenants")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.sme.svc.cluster.local:8082")

	// GitHub API credentials for committing manifests.
	githubToken := getEnv("GITHUB_TOKEN", "")
	githubOwner := getEnv("GITHUB_OWNER", "openova-io")
	githubRepo := getEnv("GITHUB_REPO", "openova-private")

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

	// Initialize store, manifest generator, GitHub client, and handler.
	provisionStore := store.New(client, mongoDBName)

	// Ensure the unique IdempotencyKey index backing the job-dedup guarantee
	// (issue #71). Index creation is idempotent; we fail-loud on unexpected
	// errors so the process doesn't silently start without dedup protection.
	idxCtx, idxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := provisionStore.EnsureJobIndexes(idxCtx); err != nil {
		idxCancel()
		slog.Error("failed to create job indexes", "error", err)
		os.Exit(1)
	}
	idxCancel()
	slog.Info("provisioning job indexes ensured")
	generator := gitops.NewManifestGenerator(gitBasePath)

	var gc *ghclient.Client
	if githubToken != "" {
		gc = ghclient.NewClient(githubToken, githubOwner, githubRepo)
		slog.Info("GitHub client configured", "owner", githubOwner, "repo", githubRepo)
	} else {
		slog.Warn("GITHUB_TOKEN not set — provisioning will fail to commit manifests")
	}

	h := &handlers.Handler{
		Store:        provisionStore,
		Producer:     producer,
		Generator:    generator,
		GitHubClient: gc,
		CatalogURL:   catalogURL,
	}

	// Start event consumer in a background goroutine.
	consumer, err := events.NewConsumer(redpandaBrokers, "provisioning", []string{"sme.order.events", "sme.tenant.events"})
	if err != nil {
		slog.Error("failed to create events consumer", "error", err)
		os.Exit(1)
	}
	defer consumer.Close()

	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	go func() {
		if err := h.StartConsumer(consumerCtx, consumer); err != nil {
			slog.Error("event consumer stopped", "error", err)
		}
	}()
	slog.Info("event consumer started",
		"topics", []string{"sme.order.events", "sme.tenant.events"},
		"group", "provisioning",
	)

	// Start the kubeconfig mirror reconciler (issue #104). Self-heals
	// tenants whose provisioning pod was killed between the DNS-ready wait
	// and the kubeconfig mirror step — without this, a CI deploy or OOM
	// kill mid-provision leaves a tenant's Flux Kustomization permanently
	// stuck with "secret not found". Reuses consumerCtx so the goroutine
	// stops cleanly on shutdown.
	h.StartKubeconfigReconciler(consumerCtx)
	// Pod-truth reconciler (issue #115): advances stuck provision steps +
	// clears 'installing' app_states when apps are actually Ready. Essential
	// for the case where a pod restart orphans the in-memory workflow mid-
	// provision — without this the UI sits on "INSTALLING" while the pods
	// are happily running.
	h.StartPodTruthReconciler(consumerCtx)

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Provisioning routes — public status endpoints + JWT-protected admin endpoints.
	provisionRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/provisioning/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Admin and start endpoints require JWT.
		if strings.HasPrefix(r.URL.Path, "/provisioning/admin/") || r.URL.Path == "/provisioning/start" {
			jwtMiddleware(provisionRoutes).ServeHTTP(w, r)
			return
		}
		// Status and tenant lookups are public (used by frontend polling).
		provisionRoutes.ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting provisioning service", "port", port)
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
