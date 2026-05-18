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
	// NATS_URL — canonical event bus per ADR-0001 §6. On Sovereigns this
	// is wired to nats-jetstream.nats-system.svc.cluster.local:4222 by
	// the chart. Empty disables the NATS leg and the service falls back
	// to the legacy Redpanda-only Producer (Catalyst-Zero / dev loops).
	natsURL := getEnv("NATS_URL", "")
	// REDPANDA_BROKERS — legacy Kafka-protocol bus retained for
	// backward-compatibility with not-yet-migrated consumers. Empty
	// disables the Redpanda leg entirely. On Sovereigns this is left
	// empty (no Redpanda exists in cluster).
	redpandaBrokersRaw := getEnv("REDPANDA_BROKERS", "")
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

	// Wire the broker publisher. NATS is the canonical bus on Sovereigns
	// (publish to `catalyst.tenant.<event>` per ADR-0001 §6); Redpanda
	// is the legacy bus retained for backward-compatibility on
	// Catalyst-Zero. At least one MUST be configured or NewMultiPublisher
	// refuses to construct a silently-no-op publisher.
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
	producer, err := events.NewMultiPublisher(natsConn, kafkaProd)
	if err != nil {
		slog.Error("event bus misconfigured — neither NATS_URL nor REDPANDA_BROKERS set", "error", err)
		os.Exit(1)
	}
	defer producer.Close()

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

	// Provision-events consumer drives the tenant state machine when
	// provisioning publishes provision.completed / .failed / .app_ready
	// / .app_removed / .app_failed / .tenant_removed. Listens on the
	// canonical NATS subjects `catalyst.provision.*` on Sovereigns AND
	// the legacy Kafka topic `sme.provision.events` on Catalyst-Zero.
	var provKafkaConsumer *events.Consumer
	if redpandaBrokersRaw != "" {
		kc, err := events.NewConsumer(
			strings.Split(redpandaBrokersRaw, ","),
			"tenant-service",
			[]string{"sme.provision.events"},
		)
		if err != nil {
			slog.Error("failed to create provision kafka consumer", "error", err)
			os.Exit(1)
		}
		provKafkaConsumer = kc
	}
	provSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:  natsConn,
		Kafka: provKafkaConsumer,
		Group: "tenant-service",
		EventTypes: []string{
			"provision.completed",
			"provision.failed",
			"provision.app_ready",
			"provision.app_removed",
			"provision.app_failed",
			"provision.tenant_removed",
		},
	})
	if err != nil {
		slog.Error("failed to create provision subscriber", "error", err)
		os.Exit(1)
	}
	defer provSub.Close()
	consumerHandler := &handlers.ConsumerHandler{Store: tenantStore}
	go func() {
		if err := consumerHandler.Start(context.Background(), provSub); err != nil {
			slog.Error("provision consumer stopped", "error", err)
		}
	}()
	slog.Info("tenant provision-events consumer started",
		"nats_subjects", []string{
			"catalyst.provision.completed",
			"catalyst.provision.failed",
			"catalyst.provision.app_ready",
			"catalyst.provision.app_removed",
			"catalyst.provision.app_failed",
			"catalyst.provision.tenant_removed",
		},
		"kafka_topic", "sme.provision.events",
		"nats_enabled", natsConn != nil,
		"kafka_enabled", provKafkaConsumer != nil,
		"group", "tenant-service")

	// Members-cleanup consumer — purges member rows as soon as a tenant is
	// soft-deleted so authz checks during the teardown window don't see
	// stale membership. Separate consumer group so offsets don't contend
	// with the provision-events subscriber above. See issue #96.
	var membersKafkaConsumer *events.Consumer
	if redpandaBrokersRaw != "" {
		kc, err := events.NewConsumer(
			strings.Split(redpandaBrokersRaw, ","),
			"tenant-members-cleanup",
			[]string{"sme.tenant.events"},
		)
		if err != nil {
			slog.Error("failed to create members-cleanup kafka consumer", "error", err)
			os.Exit(1)
		}
		membersKafkaConsumer = kc
	}
	membersSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:       natsConn,
		Kafka:      membersKafkaConsumer,
		Group:      "tenant-members-cleanup",
		EventTypes: []string{"tenant.deleted"},
	})
	if err != nil {
		slog.Error("failed to create members-cleanup subscriber", "error", err)
		os.Exit(1)
	}
	defer membersSub.Close()
	membersCleanup := &handlers.MembersCleanupConsumer{Store: tenantStore}
	go func() {
		if err := membersCleanup.Start(context.Background(), membersSub); err != nil {
			slog.Error("tenant members-cleanup consumer stopped", "error", err)
		}
	}()
	slog.Info("tenant members-cleanup consumer started",
		"nats_subject", "catalyst.tenant.deleted",
		"kafka_topic", "sme.tenant.events",
		"nats_enabled", natsConn != nil,
		"kafka_enabled", membersKafkaConsumer != nil,
		"group", "tenant-members-cleanup")

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Tenant routes — JWT middleware applied to all except slug check.
	tenantRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/tenant/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slug check is public — no JWT required.
		// /tenant/internal/* is service-to-service (#105 / #1018): the
		// route was registered with that intent in routes.go but the
		// JWT bypass here was never wired, so billing's
		// dispatchOrderPlaced lookupTenantSubdomain got a 401 and
		// shipped an empty subdomain into order.placed, which
		// provisioning then rejected as "invalid subdomain". Both
		// gateways already 401 /tenant/internal/* externally, so the
		// bypass only opens the path to in-cluster callers (the
		// documented threat model — subdomain values are public DNS).
		if strings.HasPrefix(r.URL.Path, "/tenant/check-slug/") ||
			strings.HasPrefix(r.URL.Path, "/tenant/internal/") {
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
