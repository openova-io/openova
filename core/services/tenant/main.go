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
	// the chart. NATS is the only event bus: when this is empty the
	// publisher refuses to construct and the service exits.
	natsURL := getEnv("NATS_URL", "")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.org-services.svc.cluster.local:8082")
	provisioningURL := getEnv("PROVISIONING_URL", "http://provisioning.org-services.svc.cluster.local:8084")
	// TENANT_PARENT_DOMAIN — the Sovereign's EFFECTIVE org-pool apps zone
	// (e.g. "omani.homes"), the SAME env the provisioning service reads. #4821
	// Finding-2: CreateOrg resolves the server-authoritative console_host
	// (`console.<slug>.<parentDomain>`) against THIS value (apps pool wins over
	// the funnel-selected TLD) so the returned/persisted/emitted host matches the
	// pool the org-controller actually provisions the per-Org DNS/TLS/HTTPRoute
	// under. Empty preserves the legacy behaviour (honour the funnel pick).
	appsParentDomain := getEnv("TENANT_PARENT_DOMAIN", "")
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

	// Wire the broker publisher. NATS is the only bus (publish to
	// `catalyst.tenant.<event>` per ADR-0001 §6). Without it
	// NewMultiPublisher refuses to construct a silently-no-op publisher.
	var natsConn *events.NATSConn
	if natsURL != "" {
		nc, err := events.ConnectNATS(natsURL)
		if err != nil {
			slog.Error("failed to connect to NATS", "url", natsURL, "error", err)
			os.Exit(1)
		}
		natsConn = nc
		slog.Info("connected to NATS JetStream", "url", natsURL)
	}
	producer, err := events.NewMultiPublisher(natsConn)
	if err != nil {
		slog.Error("event bus misconfigured — NATS_URL is not set", "error", err)
		os.Exit(1)
	}
	defer producer.Close()

	// Initialize store and handler.
	tenantStore := store.New(client, mongoDBName)
	catalogClient := catalog.New(catalogURL)
	h := &handlers.Handler{
		Store:            tenantStore,
		Producer:         producer,
		Catalog:          catalogClient,
		ProvisioningURL:  provisioningURL,
		AppsParentDomain: appsParentDomain,
		// #4999: served org-pool TLD set (env TENANT_POOL_DOMAINS; nil → the
		// canonical four .omani.X zones) so CreateOrg can HONOR the funnel's
		// chosen pool TLD instead of dropping it to AppsParentDomain.
		PoolDomains: handlers.PoolDomainsFromEnv(),
		DayTwoLocks: handlers.NewTenantLocks(),
	}
	slog.Info("catalog client configured", "url", catalogURL)
	slog.Info("provisioning URL configured", "url", provisioningURL)
	slog.Info("apps parent domain configured (console_host pool)",
		"tenant_parent_domain", appsParentDomain, "enabled", appsParentDomain != "")

	// Provision-events consumer drives the tenant state machine when
	// provisioning publishes provision.completed / .failed / .app_ready
	// / .app_removed / .app_failed / .tenant_removed. Listens on the
	// canonical NATS subjects `catalyst.provision.*`.
	provSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:  natsConn,
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
		"nats_enabled", natsConn != nil,
		"group", "tenant-service")

	// Members-cleanup consumer — purges member rows as soon as a tenant is
	// soft-deleted so authz checks during the teardown window don't see
	// stale membership. Separate consumer group so offsets don't contend
	// with the provision-events subscriber above. See issue #96.
	membersSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:       natsConn,
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
		"nats_enabled", natsConn != nil,
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
