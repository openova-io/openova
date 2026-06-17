package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openova-io/openova/core/services/notification/handlers"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := getEnv("PORT", "8087")
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	smtpHost := getEnv("SMTP_HOST", "stalwart.stalwart.svc.cluster.local")
	smtpPort := getEnv("SMTP_PORT", "25")
	smtpFrom := getEnv("SMTP_FROM", "noreply@openova.io")
	jwtSecret := getEnv("JWT_SECRET", "")
	// REDPANDA_BROKERS — legacy Kafka-protocol bus. Empty on Sovereigns
	// (no Redpanda exists in cluster); populated on Catalyst-Zero.
	redpandaBrokersRaw := getEnv("REDPANDA_BROKERS", "")
	// NATS_URL — canonical JetStream bus per ADR-0001 §6. On Sovereigns
	// wired to nats-jetstream.nats-system.svc.cluster.local:4222.
	natsURL := getEnv("NATS_URL", "")
	tenantURL := getEnv("TENANT_URL", "http://tenant.org-services.svc.cluster.local:8083")
	authURL := getEnv("AUTH_URL", "http://auth.org-services.svc.cluster.local:8081")
	// TBD-A67 issue #1990: the per-Sovereign parent zone (e.g.
	// "omani.homes") drives WorkspaceURL rendering. Same env name the
	// provisioning service uses for Handler.TenantParentDomain so the
	// Sovereign operator wires it once via bootstrap-kit slot 13 and
	// every back-end service reads the same value. Empty disables the
	// URL field rather than fall back to a hardcoded domain — the old
	// `.openova.io` fallback leaked the platform marketing host into
	// tenant onboarding emails on every non-openova.io Sovereign.
	parentZone := getEnv("TENANT_PARENT_DOMAIN", "")

	mailer := handlers.NewMailer(smtpHost, smtpPort, smtpFrom)

	// Wire NATS + Kafka legs. Kafka leg is optional — when
	// REDPANDA_BROKERS is empty (Sovereign default) we skip it; same
	// for NATS_URL on Catalyst-Zero. At least one MUST be present or
	// MultiSubscriber refuses to construct.
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
			slog.Warn("failed to create RedPanda producer", "error", err)
		} else {
			kafkaProd = kp
			slog.Info("connected to RedPanda (legacy)", "brokers", redpandaBrokersRaw)
		}
	}
	enricher := handlers.NewEnricher(tenantURL, authURL, parentZone, []byte(jwtSecret))

	h := &handlers.Handler{
		Mailer:   mailer,
		Producer: kafkaProd,
		Enricher: enricher,
	}

	// Fan in every event type the service reacts to. The MultiSubscriber
	// maps each to its canonical NATS subject (catalyst.<event.Type>)
	// AND wraps the legacy Kafka topic list for Catalyst-Zero.
	eventTypes := []string{
		// user / auth
		"user.login",
		// billing / orders
		"payment.received",
		"order.placed",
		// provisioning day-1 + day-2
		"provision.started",
		"provision.completed",
		"provision.failed",
		"provision.app_ready",
		"provision.app_removed",
		"provision.app_failed",
		// domain
		"domain.registered",
		"domain.verified",
		"domain.removed",
		// member
		"member.invited",
	}
	var kafkaConsumer *events.Consumer
	if kafkaProd != nil {
		topics := []string{
			events.TopicUserEvents,
			events.TopicOrderEvents,
			events.TopicBillingEvents,
			events.TopicProvisionEvents,
			events.TopicTenantEvents,
			events.TopicDomainEvents,
			events.LegacyTopics.AuthEvents,
			events.LegacyTopics.DomainEvents,
		}
		kc, err := events.NewConsumer(strings.Split(redpandaBrokersRaw, ","), "notification", topics)
		if err != nil {
			slog.Warn("failed to create kafka consumer", "error", err)
		} else {
			kafkaConsumer = kc
		}
	}
	subscriber, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:       natsConn,
		Kafka:      kafkaConsumer,
		Group:      "notification",
		EventTypes: eventTypes,
	})
	if err != nil {
		slog.Error("event bus misconfigured — neither NATS_URL nor REDPANDA_BROKERS set", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := h.StartConsumer(ctx, subscriber); err != nil {
			slog.Error("consumer error", "error", err)
		}
	}()

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		cancel()
		subscriber.Close()
		if kafkaProd != nil {
			kafkaProd.Close()
		}
		os.Exit(0)
	}()

	// HTTP server
	routes := h.Routes()
	jwtMiddleware := middleware.JWTAuth([]byte(jwtSecret))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler())
	mux.Handle("/notification/", jwtMiddleware(routes))

	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting notification service", "port", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
