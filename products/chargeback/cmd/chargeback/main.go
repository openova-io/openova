// chargeback is the standalone chargeback service (ADR-0014, EPIC #6723):
// customers, cloud cost sources, the usage ledger, price books, rating and
// statements, served as a JSON API with an embedded UI.
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

	"github.com/openova-io/openova/products/chargeback/internal/api"
	"github.com/openova-io/openova/products/chargeback/internal/collector/huawei"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/ui"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(2)
	}
	keys, err := crypto.NewKeyring(cfg.EncryptionKeyB64)
	if err != nil {
		slog.Error("APP_ENCRYPTION_KEY must be a base64-encoded 32-byte key", "error", err)
		os.Exit(2)
	}
	if len(cfg.OperatorEmails) == 0 {
		slog.Warn("OPERATOR_EMAILS is empty: nobody can sign in as operator")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dbCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	db, err := store.Open(dbCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		slog.Error("connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	st := store.New(db)
	migCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = st.Migrate(migCtx)
	cancel()
	if err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}
	slog.Info("database ready")

	reg := metrics.Default
	client := huawei.NewClient(cfg.HuaweiEndpointTemplate, cfg.HuaweiInsecureTLS, huawei.DefaultTimeout, reg)
	collector := &huawei.Collector{
		Store:           st,
		Client:          client,
		Keys:            keys,
		Metrics:         reg,
		CollectInterval: cfg.CollectInterval,
		CTSInterval:     cfg.CTSPollInterval,
		CESInterval:     cfg.CESInterval,
	}

	handler := api.New(api.Deps{
		Store:    st,
		Keys:     keys,
		Mail:     mail.New(mail.Options{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.SMTPFrom}),
		Verifier: verifier{collector},
		Config:   cfg,
		Metrics:  reg,
		UI:       api.UIFromDist(ui.Dist),
		Version:  version,
	})

	if cfg.CollectorEnabled {
		go collector.Run(ctx)
		slog.Info("collector started", "collect_interval", cfg.CollectInterval, "cts_interval", cfg.CTSPollInterval, "ces_interval", cfg.CESInterval, "endpoint_template", cfg.HuaweiEndpointTemplate, "insecure_tls", cfg.HuaweiInsecureTLS)
	} else {
		slog.Info("collector disabled by COLLECTOR_ENABLED=false")
	}
	go housekeeping(ctx, st)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("chargeback listening", "addr", cfg.ListenAddr, "profile", cfg.Profile, "public_url", cfg.PublicURL, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
	slog.Info("chargeback stopped")
}

// verifier adapts the collector's gateway errors to the API's classification.
type verifier struct {
	c *huawei.Collector
}

func (v verifier) VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error {
	err := v.c.VerifyProject(ctx, region, projectID, accessKey, secretKey)
	if err == nil {
		return nil
	}
	var ge *huawei.GatewayError
	if errors.As(err, &ge) {
		return &api.VerifyError{Code: ge.Code, Message: ge.Message, Unauthorized: ge.Unauthorized(), NotPublished: ge.NotPublished()}
	}
	return &api.VerifyError{Message: err.Error()}
}

// housekeeping purges expired sessions, pins and invites hourly.
func housekeeping(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := st.PurgeExpired(ctx); err != nil {
				slog.Warn("housekeeping", "error", err)
			}
		}
	}
}
