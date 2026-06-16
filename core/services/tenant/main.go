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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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
	catalogURL := getEnv("CATALOG_URL", "http://catalog.org-services.svc.cluster.local:8082")
	provisioningURL := getEnv("PROVISIONING_URL", "http://provisioning.org-services.svc.cluster.local:8084")
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

	// Sandbox orchestrator — consumes tenant.sandbox_requested (PR #1633)
	// and materialises a Sandbox CR in catalyst-system that the
	// sandbox-controller (PR #1622) reconciles into namespace + RBAC +
	// PVCs + pty-server. The Kubernetes write surface is optional: when
	// no in-cluster config OR KUBECONFIG is available (CI / dev loops)
	// we skip starting the orchestrator with a Warn so the rest of the
	// tenant service still boots. On Sovereigns the ServiceAccount-bound
	// in-cluster config is always present so the consumer is always on.
	sandboxNamespace := getEnv("SANDBOX_NAMESPACE", handlers.DefaultSandboxNamespace)
	dyn := buildDynamicClient()
	if dyn == nil {
		slog.Warn("sandbox-orchestrator: kubernetes client unavailable — orchestrator disabled",
			"namespace", sandboxNamespace,
			"reason", "no in-cluster config and KUBECONFIG unset (or build failed)")
	} else {
		var sandboxKafkaConsumer *events.Consumer
		if redpandaBrokersRaw != "" {
			kc, err := events.NewConsumer(
				strings.Split(redpandaBrokersRaw, ","),
				"sandbox-orchestrator",
				[]string{"sme.tenant.events"},
			)
			if err != nil {
				slog.Error("failed to create sandbox-orchestrator kafka consumer", "error", err)
				os.Exit(1)
			}
			sandboxKafkaConsumer = kc
		}
		sandboxSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
			NATS:       natsConn,
			Kafka:      sandboxKafkaConsumer,
			Group:      "sandbox-orchestrator",
			EventTypes: []string{"tenant.sandbox_requested"},
		})
		if err != nil {
			slog.Error("failed to create sandbox-orchestrator subscriber", "error", err)
			os.Exit(1)
		}
		defer sandboxSub.Close()
		orchestrator := &handlers.SandboxOrchestrator{
			Client:       newDynamicSandboxClient(dyn),
			Namespace:    sandboxNamespace,
			Members:      tenantStore,
			DefaultQuota: handlers.DefaultSandboxQuota(),
		}
		go func() {
			if err := orchestrator.Start(context.Background(), sandboxSub); err != nil {
				slog.Error("sandbox-orchestrator consumer stopped", "error", err)
			}
		}()
		slog.Info("sandbox-orchestrator consumer started",
			"nats_subject", "catalyst.tenant.sandbox_requested",
			"kafka_topic", "sme.tenant.events",
			"namespace", sandboxNamespace,
			"nats_enabled", natsConn != nil,
			"kafka_enabled", sandboxKafkaConsumer != nil,
			"group", "sandbox-orchestrator")
	}

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

// buildDynamicClient returns an in-cluster dynamic.Interface (production
// Sovereign path) or one built from $KUBECONFIG (dev), or nil when both
// fail. Returning nil is the explicit "orchestrator disabled" signal —
// see the caller's Warn log; the rest of the tenant service still boots.
func buildDynamicClient() dynamic.Interface {
	if cfg, err := rest.InClusterConfig(); err == nil {
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			slog.Warn("sandbox-orchestrator: in-cluster dynamic client build failed",
				"error", err)
			return nil
		}
		return c
	}
	kc := os.Getenv("KUBECONFIG")
	if kc == "" {
		return nil
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		slog.Warn("sandbox-orchestrator: kubeconfig load failed",
			"kubeconfig", kc, "error", err)
		return nil
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		slog.Warn("sandbox-orchestrator: kubeconfig dynamic client build failed",
			"error", err)
		return nil
	}
	return c
}

// dynamicSandboxClient is the production adapter implementing
// handlers.SandboxClient on top of dynamic.Interface. Kept here (not
// in handlers/) so handlers/ stays free of client-go transitive deps
// and unit tests can run without a Kubernetes test rig.
type dynamicSandboxClient struct {
	dyn dynamic.Interface
}

func newDynamicSandboxClient(d dynamic.Interface) *dynamicSandboxClient {
	return &dynamicSandboxClient{dyn: d}
}

// Get fetches the Sandbox CR in (namespace, name).
func (c *dynamicSandboxClient) Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return c.dyn.Resource(handlers.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// Create writes the Sandbox CR. Returns the underlying api error so
// callers can distinguish IsAlreadyExists.
func (c *dynamicSandboxClient) Create(ctx context.Context, namespace string, obj *unstructured.Unstructured) error {
	_, err := c.dyn.Resource(handlers.SandboxGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}
