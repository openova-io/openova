package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/billing/handlers"
	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/db"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	databaseURL := getEnv("DATABASE_URL", "postgres://billing:billing@localhost:5432/billing?sslmode=disable")
	// REDPANDA_BROKERS — legacy Kafka-protocol bus. Empty on Sovereigns
	// (no Redpanda exists in cluster); populated on Catalyst-Zero for
	// backward-compatibility with not-yet-migrated consumers.
	redpandaBrokersRaw := getEnv("REDPANDA_BROKERS", "")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8085")
	successURL := getEnv("SUCCESS_URL", "https://openova.io/checkout")
	cancelURL := getEnv("CANCEL_URL", "https://openova.io/checkout")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.org-services.svc.cluster.local:8082")
	tenantURL := getEnv("TENANT_URL", "http://tenant.org-services.svc.cluster.local:8083")
	// NOTIFICATION_SERVICE_URL — org-notification's POST /notification/send
	// endpoint, used by D28 (voucher-issued gifting email). Default points at
	// the in-cluster ClusterIP DNS the chart wires per sovereign.
	notificationURL := getEnv("NOTIFICATION_SERVICE_URL", "http://notification.org-services.svc.cluster.local:8087/notification/send")
	// SOVEREIGN_FQDN — per-Sovereign apex domain (e.g. "omani.works") used to
	// build the public marketplace redeem URL in voucher emails. NEVER
	// hardcoded; the chart pipes it from `billing.sovereignFQDN`. Empty is
	// tolerated for dev loops — the template emits a relative-ish fallback.
	sovereignFQDN := getEnv("SOVEREIGN_FQDN", "")
	// NATS_URL — JetStream broker URL for BOTH:
	//   (a) the catalyst.usage.recorded metering stream (#798), and
	//   (b) the canonical billing event bus per ADR-0001 §6
	//       (`catalyst.billing.order.placed` etc., wired via
	//       events.MultiPublisher below).
	// On Sovereigns this is wired to
	// nats-jetstream.nats-system.svc.cluster.local:4222 by the chart.
	// Empty disables the NATS leg and the service falls back to the
	// legacy Redpanda-only Producer (Catalyst-Zero / dev loops).
	natsURL := getEnv("NATS_URL", "")

	pg := db.MustConnect(databaseURL)
	defer pg.Close()
	slog.Info("connected to PostgreSQL")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	billingStore := store.New(pg)
	if err := billingStore.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("database migration complete")

	// Wire the broker publisher up front so both the metering subscriber
	// (below) and the Handler share a single NATS connection. ADR-0001 §6
	// requires NATS for the canonical `catalyst.billing.order.placed`
	// subject; Redpanda stays in place as a legacy bridge so any
	// not-yet-migrated consumers (e.g. on contabo) keep receiving
	// org.order.events. At least one transport MUST be wired.
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
		// Bootstrap the canonical CATALYST_ORG Stream (subjects
		// `catalyst.>`) at startup so the metering sidecar can publish
		// immediately on a fresh Sovereign cold-start. We previously
		// created a separate CATALYST_USAGE Stream here, but on t20
		// (2026-05-18) that crashed billing with NATS error code 10065
		// "subjects overlap with an existing stream" — by the time
		// billing started, CATALYST_ORG (subject filter `catalyst.>`)
		// had already been created by the tenant / provisioning
		// MultiSubscribers, and JetStream rejects overlapping subject
		// filters across Streams. The fix is to share CATALYST_ORG and
		// scope billing's reads via a consumer-side FilterSubject
		// below (SubscribeUsageRecordedOnOrg).
		//
		// EnsureCatalystOrgStream is idempotent — first-writer-wins
		// with whatever subscriber bootstraps the Stream first.
		ensureCtx, ensureCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := nc.EnsureCatalystOrgStream(ensureCtx); err != nil {
			ensureCancel()
			slog.Error("failed to ensure JetStream CATALYST_ORG stream", "error", err)
			os.Exit(1)
		}
		ensureCancel()
		natsConn = nc
		slog.Info("connected to NATS JetStream", "url", natsURL,
			"stream", events.StreamCatalystOrg,
			"subject", events.SubjectUsageRecorded)
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

	h := &handlers.Handler{
		Store:           billingStore,
		Producer:        producer,
		SuccessURL:      successURL,
		CancelURL:       cancelURL,
		CatalogURL:      catalogURL,
		TenantURL:       tenantURL,
		NotificationURL: notificationURL,
		SovereignFQDN:   sovereignFQDN,
		NotificationClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		// JWTSecret — same bytes the inbound JWTAuth middleware below
		// validates against (org-services-secrets/JWT_SECRET). Used by
		// sendVoucherIssuedEmail to mint a short-lived service token for
		// the billing→notification hop so notification's matching
		// JWTAuth middleware accepts the dispatch. Pre-#1999 this hop
		// carried no Authorization header → silent 401 → voucher email
		// never delivered despite voucher row persisting.
		JWTSecret: jwtSecret,
	}

	// Tenant-events consumer drives the billing-side cascade on
	// tenant.deleted (Stripe sub-cancel + invoice void + ledger marker).
	// Listens on canonical NATS subject `catalyst.tenant.deleted` on
	// Sovereigns AND legacy Kafka topic `org.tenant.events` on Catalyst-
	// Zero. PR #1627 wired the consumer side after PR #1626 wired publish.
	var billingKafkaConsumer *events.Consumer
	if kafkaProd != nil {
		kc, err := events.NewConsumer(
			strings.Split(redpandaBrokersRaw, ","),
			"billing-tenant-events",
			[]string{"org.tenant.events"},
		)
		if err != nil {
			slog.Error("failed to create tenant-events kafka consumer", "error", err)
			os.Exit(1)
		}
		billingKafkaConsumer = kc
	}
	billingTenantSub, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:       natsConn,
		Kafka:      billingKafkaConsumer,
		Group:      "billing-tenant-events",
		EventTypes: []string{"tenant.deleted"},
	})
	if err != nil {
		slog.Error("failed to create tenant-events subscriber", "error", err)
		os.Exit(1)
	}
	defer billingTenantSub.Close()
	billingTenantHandler := &handlers.TenantConsumer{Store: billingStore}
	go func() {
		if err := billingTenantHandler.Start(context.Background(), billingTenantSub); err != nil {
			slog.Error("billing tenant-events consumer stopped", "error", err)
		}
	}()
	slog.Info("billing tenant-events consumer started",
		"nats_subject", "catalyst.tenant.deleted",
		"kafka_topic", "org.tenant.events",
		"kafka_enabled", billingKafkaConsumer != nil,
		"nats_enabled", natsConn != nil,
		"group", "billing-tenant-events")

	// NATS metering subscriber (#798 §B) — uses the same NATS connection
	// opened for the publisher above so we have a single TCP socket per
	// process. When NATS_URL is unset the subscriber is skipped (HTTP
	// path POST /billing/metering/record still works for dev loops).
	//
	// t20 fix (2026-05-18): consume off CATALYST_ORG with a FilterSubject
	// scoped to `catalyst.usage.recorded` instead of a separate
	// CATALYST_USAGE Stream — JetStream rejects overlapping subject
	// filters across Streams. See EnsureCatalystOrgStream above.
	if natsConn != nil {
		subCtx := context.Background()
		usageSub, err := natsConn.SubscribeUsageRecordedOnOrg(subCtx)
		if err != nil {
			slog.Error("failed to subscribe to catalyst.usage.recorded on CATALYST_ORG", "error", err)
			os.Exit(1)
		}
		defer usageSub.Close()

		meteringConsumer := &handlers.MeteringConsumer{
			Store:            billingStore,
			CustomerResolver: handlers.DefaultCustomerResolver{Store: billingStore},
		}
		go func() {
			if err := meteringConsumer.Start(subCtx, usageSub); err != nil {
				slog.Error("billing metering consumer stopped", "error", err)
			}
		}()
		slog.Info("billing metering consumer started",
			"subject", events.SubjectUsageRecorded,
			"durable", events.ConsumerOrgBillingMetering)
	} else {
		slog.Warn("NATS_URL is empty — metering consumer disabled (HTTP path still active)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler())

	billingRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	// Public paths that bypass JWT validation. These match the gateway's
	// public routes (D29 voucher-redeem zero-touch flow). The gateway
	// passes these through with no auth header; this service must accept
	// them OR the marketplace /redeem landing returns 401 to unauth visitors.
	// Caught live on t132 2026-05-16 after PR #1559 made the gateway public —
	// the billing service was still JWT-gating internally.
	publicBillingPaths := map[string]bool{
		"/billing/webhook":                  true, // Stripe (sig-verified)
		"/billing/vouchers/redeem-preview":  true, // D29 voucher landing
		"/billing/plans":                    true, // marketplace pricing
		"/billing/addons":                   true, // marketplace add-on pricing
	}

	mux.Handle("/billing/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicBillingPaths[r.URL.Path] {
			billingRoutes.ServeHTTP(w, r)
			return
		}
		jwtMiddleware(billingRoutes).ServeHTTP(w, r)
	}))

	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting billing service", "port", port)
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
