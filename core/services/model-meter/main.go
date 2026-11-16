// Package main is the model meter, model-meter: a reverse proxy between the model gateway and
// the model back ends that publishes one usage event, catalyst.usage.recorded.v1, per answered
// request. It takes the identity of a request from the headers the gateway sets and from no
// other header, and it publishes quantities, never a price (docs/contracts/model-contract.md).
//
// Configuration (environment):
//
//	LISTEN_ADDR      proxy listener, default ":8080"
//	BACKENDS         required: JSON object from a Host header name to
//	                 {"backend": "<name>", "url": "<base URL>"}
//	NATS_URL         bus, default "nats://nats-jetstream.nats-system.svc.cluster.local:4222"
//	SPOOL_DIR        spool directory, default "/var/lib/model-meter/spool"
//	EVENT_SOURCE     required: model-gateway/<sovereign-fqdn>/<region-id>
//	PUBLISH_TIMEOUT  JetStream publish time-out before an event is spooled, default "5s"
//
// The meter does not create the stream CATALYST_ORG: the chart bp-nats-jetstream declares it.
// When the bus cannot be reached at start, every event is spooled.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openova-io/openova/core/services/model-meter/meter"
	"github.com/openova-io/openova/core/services/shared/events"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	listenAddr := getEnv("LISTEN_ADDR", ":8080")
	natsURL := getEnv("NATS_URL", "nats://nats-jetstream.nats-system.svc.cluster.local:4222")
	spoolDir := getEnv("SPOOL_DIR", "/var/lib/model-meter/spool")
	source := os.Getenv("EVENT_SOURCE")
	if source == "" {
		slog.Error("EVENT_SOURCE is required")
		os.Exit(1)
	}
	backends, err := meter.ParseBackends(os.Getenv("BACKENDS"))
	if err != nil {
		slog.Error("BACKENDS is required and must be valid", "error", err)
		os.Exit(1)
	}
	publishTimeout, err := time.ParseDuration(getEnv("PUBLISH_TIMEOUT", "5s"))
	if err != nil || publishTimeout <= 0 {
		slog.Error("PUBLISH_TIMEOUT must be a positive duration", "value", os.Getenv("PUBLISH_TIMEOUT"))
		os.Exit(1)
	}
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		slog.Error("failed to create the spool directory", "dir", spoolDir, "error", err)
		os.Exit(1)
	}

	publisher := &meter.SpoolPublisher{Source: source, PublishTimeout: publishTimeout, SpoolDir: spoolDir}
	natsConn, err := events.ConnectNATS(natsURL)
	if err != nil {
		// The meter comes up without the bus; events spool to disk.
		slog.Warn("NATS unavailable at start, usage events will be spooled", "url", natsURL, "error", err)
	} else {
		publisher.Bus = natsConn
		defer natsConn.Close()
	}

	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	go publisher.DrainSpoolLoop(drainCtx, 30*time.Second)

	handler, err := meter.NewHandler(meter.Config{Backends: backends, Publisher: publisher, Source: source})
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Model requests can be slow.
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		slog.Info("model meter listening", "addr", server.Addr, "backends", len(backends), "nats_url", natsURL)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// On SIGTERM, drain the spool one last time so that events of the last 30 seconds are not
	// left behind.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("shutdown signal received, draining the spool one last time")
	cancelDrain()
	drainOnce, cancelOnce := context.WithTimeout(context.Background(), 10*time.Second)
	publisher.DrainSpoolOnce(drainOnce)
	cancelOnce()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "error", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
