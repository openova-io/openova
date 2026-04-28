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
	brokers := strings.Split(getEnv("REDPANDA_BROKERS", "redpanda.talentmesh.svc.cluster.local:9092"), ",")
	tenantURL := getEnv("TENANT_URL", "http://tenant.sme.svc.cluster.local:8083")
	authURL := getEnv("AUTH_URL", "http://auth.sme.svc.cluster.local:8081")

	mailer := handlers.NewMailer(smtpHost, smtpPort, smtpFrom)

	producer, err := events.NewProducer(brokers)
	if err != nil {
		slog.Warn("failed to create events producer", "error", err)
	}

	enricher := handlers.NewEnricher(tenantURL, authURL, []byte(jwtSecret))

	h := &handlers.Handler{
		Mailer:   mailer,
		Producer: producer,
		Enricher: enricher,
	}

	// Fan in every topic the service reacts to. Legacy topic names
	// (auth.events, domain-events) are listed alongside the canonical
	// sme.<producer>.events names so a publisher-side rename (issues
	// #69, #70) does not require a consumer flag-day. See
	// services/shared/events/topics.go for the canonical list.
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
	consumer, err := events.NewConsumer(brokers, "notification", topics)
	if err != nil {
		slog.Warn("failed to create events consumer", "error", err)
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Wrap the consumer with the DLQ subscriber so poison records
		// land in sme.dlq after 3 retries instead of blocking the
		// partition (issue #72). The producer is reused for DLQ
		// publishes; if it is nil the subscriber falls back to
		// logging and committing (still better than a hung
		// partition).
		sub := events.NewDLQSubscriber(consumer, producer, "notification", events.DefaultMaxRetries, events.TopicDLQ)

		go func() {
			if err := h.StartConsumer(ctx, sub); err != nil {
				slog.Error("consumer error", "error", err)
			}
		}()

		// Graceful shutdown
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			cancel()
			if consumer != nil {
				consumer.Close()
			}
			if producer != nil {
				producer.Close()
			}
			os.Exit(0)
		}()
	}

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
