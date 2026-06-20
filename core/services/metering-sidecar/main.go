// Package main is the catalyst-metering-sidecar — a transparent
// reverse proxy in front of NewAPI that emits one
// catalyst.usage.recorded NATS envelope per completed LLM request.
//
// Why a sidecar instead of patching NewAPI source:
//
//  NewAPI is consumed as the upstream image
//  ghcr.io/openova-io/openova/newapi-mirror — a pinned mirror of the
//  upstream Go binary, NOT a fork we own. Patching its source would
//  fork the upstream and create a long-tail rebase debt for every
//  subsequent upstream release. Per #795 [Q-mine-3] + ADR-0001 §6 the
//  metering surface MUST be NATS JetStream, but the metering itself
//  doesn't need to live inside NewAPI's process — observing the
//  request/response pair at the network edge is sufficient because the
//  OpenAI-compatible /v1/* response carries `usage.{prompt_tokens,
//  completion_tokens, total_tokens}` for every successful call.
//
// Deployment shape: bp-newapi (#799) renders this sidecar as a second
// container in the NewAPI Pod. Customer traffic flows
//   ingress :443 → sidecar :8086 → newapi :3000
// and the response body is observed on its way back. Failed requests
// (non-2xx, network errors) are NOT billed — only successfully-completed
// LLM calls produce a NATS envelope.
//
// At-least-once delivery: the sidecar publishes via JetStream synchronous
// Publish (broker-acked). On NATS unreachable >5s, the envelope is
// persisted to /var/lib/metering-sidecar/spool/<request_id>.json and
// retried in the background. The customer-facing LLM call is NEVER
// blocked on metering — billing is observability for the response, not
// in the critical path.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openova-io/openova/core/services/metering-sidecar/handlers"
	"github.com/openova-io/openova/core/services/metering-sidecar/proxy"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
)

func main() {
	natsURL := getEnv("NATS_URL", "nats://nats-jetstream.nats-system.svc.cluster.local:4222")
	upstreamURL := getEnv("NEWAPI_UPSTREAM_URL", "http://localhost:3000")
	listenPort := getEnv("LISTEN_PORT", "8086")
	spoolDir := getEnv("SPOOL_DIR", "/var/lib/metering-sidecar/spool")
	priceMicroOMRPerToken := mustParseInt64(getEnv("PRICE_MICRO_OMR_PER_TOKEN", "156"))
	publishTimeout := mustParseDuration(getEnv("NATS_PUBLISH_TIMEOUT", "5s"))
	tenantIDFromHeader := getEnv("TENANT_ID_HEADER", "x-tenant-id")
	customerIDFromHeader := getEnv("CUSTOMER_ID_HEADER", "x-customer-id")

	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		slog.Error("invalid NEWAPI_UPSTREAM_URL", "url", upstreamURL, "error", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		slog.Error("failed to create spool directory", "dir", spoolDir, "error", err)
		os.Exit(1)
	}

	natsConn, err := events.ConnectNATS(natsURL)
	if err != nil {
		// Sidecar startup MUST NOT crash on NATS being unreachable —
		// the customer-facing LLM proxy must come up regardless. We
		// log loudly + run with publisher=nil; envelopes spool to
		// disk and a background retry drains them once NATS recovers.
		slog.Warn("NATS unavailable at startup — metering will spool to disk",
			"url", natsURL, "error", err)
	} else {
		// Per ADR-0001 §6 the canonical consumer (org-billing) owns
		// the Stream lifecycle. The sidecar does NOT call
		// EnsureUsageStream — if the Stream is missing, org-billing
		// will create it on its next startup and the spool drain
		// will succeed afterward.
		defer natsConn.Close()
	}

	publisher := &proxy.MeteringPublisher{
		NATS:           natsConn,
		PublishTimeout: publishTimeout,
		SpoolDir:       spoolDir,
	}

	// Background spool drain: every 30 seconds, attempt to publish
	// any envelopes persisted during a prior NATS outage. Per
	// docs/INVIOLABLE-PRINCIPLES.md #1 (event-driven, never polling)
	// this is a localised retry loop, not a CronJob — the sidecar
	// owns its own spool and no external trigger is needed.
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	go publisher.DrainSpoolLoop(drainCtx, 30*time.Second)

	revProxy := &proxy.MeteringProxy{
		Upstream:              upstream,
		Publisher:             publisher,
		PriceMicroOMRPerToken: priceMicroOMRPerToken,
		TenantIDHeader:        strings.ToLower(tenantIDFromHeader),
		CustomerIDHeader:      strings.ToLower(customerIDFromHeader),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler())
	mux.HandleFunc("GET /metrics", handlers.MetricsHandler(publisher))
	// Everything else proxies through to NewAPI.
	mux.Handle("/", revProxy)

	server := &http.Server{
		Addr:              ":" + listenPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Long client read/write timeouts — LLM requests can be slow.
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("metering sidecar listening",
			"addr", server.Addr,
			"upstream", upstream.String(),
			"price_micro_omr_per_token", priceMicroOMRPerToken,
			"nats_url", natsURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: SIGTERM from K8s drains the spool one last
	// time so envelopes generated during the final 30s of the pod's
	// life are not lost.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("shutdown signal received — draining spool one last time")
	cancelDrain()
	drainOnce, cancelOnce := context.WithTimeout(context.Background(), 10*time.Second)
	publisher.DrainSpoolOnce(drainOnce)
	cancelOnce()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	server.Shutdown(shutdownCtx)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustParseInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		slog.Error("invalid integer env value", "value", s, "error", err)
		os.Exit(1)
	}
	return v
}

func mustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Error("invalid duration env value", "value", s, "error", err)
		os.Exit(1)
	}
	return d
}
