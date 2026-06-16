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
	// REDPANDA_BROKERS — legacy Kafka-protocol bus. Empty on Sovereigns
	// (no Redpanda exists in cluster); populated on Catalyst-Zero.
	redpandaBrokersRaw := getEnv("REDPANDA_BROKERS", "")
	// NATS_URL — canonical JetStream bus per ADR-0001 §6. On Sovereigns
	// wired to nats-jetstream.nats-system.svc.cluster.local:4222.
	natsURL := getEnv("NATS_URL", "")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8086")
	cnameTarget := getEnv("CNAME_TARGET", "sme.openova.io")
	tenantURL := getEnv("TENANT_URL", "http://tenant.org-services.svc.cluster.local:8083")

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

	// Wire NATS + Kafka legs. At least one MUST be present or
	// MultiPublisher / MultiSubscriber refuse to construct.
	var (
		natsConn  *events.NATSConn
		kafkaProd *events.Producer
	)
	if natsURL != "" {
		nc, err := events.ConnectNATS(natsURL)
		if err != nil {
			slog.Error("failed to connect to NATS", "url", natsURL, "error", err)
			os.Exit(1)
		}
		natsConn = nc
		slog.Info("connected to NATS JetStream", "url", natsURL)
	}
	if redpandaBrokersRaw != "" {
		kp, err := events.NewProducer(strings.Split(redpandaBrokersRaw, ","))
		if err != nil {
			slog.Error("failed to create RedPanda producer", "error", err)
			os.Exit(1)
		}
		kafkaProd = kp
		slog.Info("connected to RedPanda (legacy)", "brokers", redpandaBrokersRaw)
	}
	publisher, err := events.NewMultiPublisher(natsConn, kafkaProd)
	if err != nil {
		slog.Error("event bus misconfigured — neither NATS_URL nor REDPANDA_BROKERS set", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()

	// Initialize store and handler.
	domainStore := store.New(client, mongoDBName)
	h := &handlers.Handler{
		Store:       domainStore,
		Producer:    publisher,
		CNAMETarget: cnameTarget,
		TenantURL:   tenantURL,
	}

	// Start the tenant-events consumer so tenant.deleted cascades remove
	// domain records (subdomains + BYOD). See issue #95. Listens on
	// `catalyst.tenant.deleted` (NATS, Sovereign default) AND legacy
	// `sme.tenant.events` (Kafka, Catalyst-Zero default).
	var kafkaConsumer *events.Consumer
	if redpandaBrokersRaw != "" {
		kc, err := events.NewConsumer(
			strings.Split(redpandaBrokersRaw, ","),
			"domain-tenant-events",
			[]string{"sme.tenant.events"},
		)
		if err != nil {
			slog.Error("failed to create kafka consumer", "error", err)
			os.Exit(1)
		}
		kafkaConsumer = kc
	}
	tenantSubscriber, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:       natsConn,
		Kafka:      kafkaConsumer,
		Group:      "domain-tenant-events",
		EventTypes: []string{"tenant.deleted"},
	})
	if err != nil {
		slog.Error("failed to create tenant-events subscriber", "error", err)
		os.Exit(1)
	}
	defer tenantSubscriber.Close()
	domainTenantHandler := &handlers.TenantConsumer{Store: domainStore}
	go func() {
		if err := domainTenantHandler.Start(context.Background(), tenantSubscriber); err != nil {
			slog.Error("domain tenant-events consumer stopped", "error", err)
		}
	}()
	slog.Info("domain tenant-events consumer started",
		"nats_subject", "catalyst.tenant.deleted",
		"kafka_topic", "sme.tenant.events",
		"kafka_enabled", kafkaConsumer != nil,
		"nats_enabled", natsConn != nil,
		"group", "domain-tenant-events")

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
