// Command projector consumes K8s resource Events from the catalyst.events
// JetStream and projects them into Valkey for cross-replica fan-out
// to catalyst-api SSE consumers.
//
// Architecture, per docs/EPICS-1-6-unified-design.md §7.5:
//
//	[catalyst-api k8scache.Factory] — produces watch events
//	         │
//	         │ (publishes to NATS catalyst.events)
//	         ▼
//	[NATS JetStream "catalyst.events"] — durable, R=3, 24h retention
//	         │
//	         ▼
//	[projector binary] — consumes via durable consumer, multi-replica
//	         │
//	         ▼
//	[Valkey KV: cluster:{c}:kind:{k}:{ns}/{name}]
//	         │
//	         ▼
//	[catalyst-api SSE handlers read from Valkey]
//
// Why a separate binary?
//
//   1. catalyst-api scales horizontally; without an external KV every
//      SSE consumer per replica needs its own copy of the cluster
//      state. Valkey collapses that to one shared store.
//   2. Cold-start: a new catalyst-api Pod no longer has to LIST every
//      kind on every Sovereign — it reads from Valkey on boot.
//   3. The projector is single-purpose, so it can run with N replicas
//      without affecting catalyst-api's RPC latency budget.
//
// Configuration is environment-variable driven per
// docs/INVIOLABLE-PRINCIPLES.md #4.
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

	"github.com/valkey-io/valkey-go"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	natsx "github.com/openova-io/openova/core/cmd/projector/internal/nats"
	"github.com/openova-io/openova/core/cmd/projector/internal/lister"
	pruntime "github.com/openova-io/openova/core/cmd/projector/internal/runtime"
	pvalkey "github.com/openova-io/openova/core/cmd/projector/internal/valkey"
)

const (
	exitConfigError = 2
	exitListenError = 1
)

func main() {
	cfg, err := pruntime.LoadFromEnv()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: pruntime.ParseLogLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(exitConfigError)
	}

	// Wire Valkey.
	vk, err := newValkey(cfg)
	if err != nil {
		logger.Error("valkey connect failed", "err", err)
		os.Exit(exitListenError)
	}
	defer vk.Close()
	kv := &valkeyKV{cli: vk}
	projector := pvalkey.NewProjector(kv, cfg.ValkeyTTL)

	// Wire NATS.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cons, err := natsx.Connect(ctx, natsx.Config{
		URL:           cfg.NATSURL,
		Stream:        cfg.NATSStream,
		Subject:       cfg.NATSSubject,
		DurableName:   cfg.NATSDurable,
		AckWait:       cfg.NATSAckWait,
		MaxDeliver:    cfg.NATSMaxDeliver,
		BackoffMin:    cfg.NATSBackoffMin,
		BackoffMax:    cfg.NATSBackoffMax,
		ConsumerLogr:  logger,
	})
	if err != nil {
		logger.Error("nats connect failed", "err", err)
		os.Exit(exitListenError)
	}
	defer cons.Close()

	// Cold-start: full LIST against the in-cluster apiserver, then
	// hook up the streaming consumer. INVIOLABLE-PRINCIPLES #1: ship
	// the target-state shape on the first run — no "for now" subset.
	if cfg.ColdStart {
		if err := runColdStart(ctx, logger, cfg.Cluster, projector); err != nil {
			logger.Warn("cold-start failed; continuing to streaming",
				"err", err)
		}
	}

	// /healthz + /readyz for kubelet probes.
	go runHealthServer(logger)

	logger.Info("projector starting consumer",
		"cluster", cfg.Cluster,
		"natsStream", cfg.NATSStream,
		"natsSubject", cfg.NATSSubject,
		"natsDurable", cfg.NATSDurable,
		"valkeyAddr", cfg.ValkeyAddr,
		"valkeyTTL", cfg.ValkeyTTL,
	)
	if err := cons.Run(ctx, projector); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("consumer ended with error", "err", err)
		os.Exit(exitListenError)
	}
	logger.Info("projector shutdown clean")
}

// runColdStart performs a full LIST + project pass for every kind in
// the default registry. Errors are logged WARN and do not halt
// startup — the streaming consumer is the long-term source of truth
// and overlapping projection is idempotent.
func runColdStart(ctx context.Context, log *slog.Logger, cluster string, p *pvalkey.Projector) error {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	c := &lister.ColdStarter{
		Cluster:   cluster,
		Dyn:       dyn,
		Projector: p,
		Logger:    log,
	}
	count, err := c.Run(ctx, lister.DefaultKinds)
	log.Info("cold-start complete", "items", count, "err", err)
	return err
}

// runHealthServer serves /healthz + /readyz on :8081. The kubelet
// probes use it to gate Pod readiness. Returning 200 unconditionally
// is fine because the consumer's main loop is the source of truth —
// liveness failures appear as repeated NACKs in metrics rather than
// here.
func runHealthServer(log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("health server exited", "err", err)
	}
}

// newValkey constructs the Valkey client. Authenticated when password is set.
func newValkey(cfg pruntime.Config) (valkey.Client, error) {
	opt := valkey.ClientOption{InitAddress: []string{cfg.ValkeyAddr}}
	if cfg.ValkeyPassword != "" {
		opt.Username = cfg.ValkeyUsername
		opt.Password = cfg.ValkeyPassword
	}
	return valkey.NewClient(opt)
}

// valkeyKV adapts the upstream valkey-go client to the projector's
// minimal KV interface. SET uses EX <ttl-seconds>; DEL is direct.
type valkeyKV struct {
	cli valkey.Client
}

func (v *valkeyKV) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	cmd := v.cli.B().Set().Key(key).Value(string(value)).ExSeconds(int64(ttl.Seconds())).Build()
	return v.cli.Do(ctx, cmd).Error()
}

func (v *valkeyKV) Del(ctx context.Context, key string) error {
	cmd := v.cli.B().Del().Key(key).Build()
	return v.cli.Do(ctx, cmd).Error()
}

func (v *valkeyKV) Close() {
	if v.cli != nil {
		v.cli.Close()
	}
}
