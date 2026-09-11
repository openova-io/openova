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

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/chargeback/internal/adapter/openova"
	"github.com/openova-io/openova/products/chargeback/internal/api"
	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/collections"
	"github.com/openova-io/openova/products/chargeback/internal/collector/huawei"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/report"
	"github.com/openova-io/openova/products/chargeback/internal/rollup"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
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
	// DESIGN.md §20 — the daily cost rollup. The switch governs the READ as
	// well as the build: with it off every window is rated over the hourly
	// usage_records instead. The answers are the same either way; §20.8
	// measures what the cache is worth.
	st.SetCostRollupEnabled(cfg.CostRollupEnabled)

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

	deps := api.Deps{
		Store:    st,
		Keys:     keys,
		Mail:     mail.New(mail.Options{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.SMTPFrom}),
		Verifier: verifier{collector},
		Config:   cfg,
		Metrics:  reg,
		UI:       api.UIFromDist(ui.Dist),
		Version:  version,
	}
	// The payment-gateway seam (DESIGN.md §8). Customers paid by bank
	// transfer or settled as an internal recharge need no registration —
	// nothing external collects for them. A gateway is registered under the
	// gateway_name its customers carry.
	//
	// ADR-0014 D6: statements issued for Organizations collected through the
	// stripe gateway debit their credit through the platform billing
	// service. Off when the URL is unset. Adding Omantel's gateway is one
	// more Register call here, under the name "omantel".
	settlement := settle.NewRegistry()
	// The stripe gateway is registered when EITHER half of it is configured:
	// the outbound hook (URL — debits issued statements through billing) or
	// the inbound callback secret (verifies the gateway's payment
	// confirmations on POST /api/v1/gateways/stripe/callback). Registering on
	// the URL alone left a Sovereign with only the secret answering 404 "no
	// gateway is registered under stripe" to every signed callback (hw307,
	// 2026-09-10). With the URL empty, RequestSettlement answers
	// not-applicable and the callback path still works.
	if cfg.BillingHookURL != "" || cfg.BillingHookCallbackSecret != "" {
		hook := &openova.BillingHook{URL: cfg.BillingHookURL, Token: cfg.BillingHookToken, Metrics: reg, CallbackSecret: cfg.BillingHookCallbackSecret}
		settlement.Register(settle.GatewayStripe, hook)
		if cfg.BillingHookURL != "" {
			deps.StatementHook = hook
			slog.Info("billing hook enabled", "url", cfg.BillingHookURL)
		} else {
			slog.Info("stripe gateway registered for callbacks only (no billing hook URL)")
		}
	}
	deps.Settlement = settlement
	slog.Info("payment gateways registered", "gateways", settlement.Gateways())

	// WHO invoices on this Sovereign (DESIGN.md §8.10). The setting lives in
	// billing_settings and defaults to `internal`, so this wiring changes
	// nothing until an operator switches it. In external mode issuing queues
	// the rated bill in the outbox and the deliverer below pushes it out.
	var exporter commercial.Exporter
	if cfg.CommercialExportDir != "" {
		exporter = commercial.NewCSVFileExporter(cfg.CommercialExportDir)
		slog.Info("commercial export enabled", "dir", cfg.CommercialExportDir)
	}
	deps.Commercial = commercial.NewSelector(st, exporter)
	deliverer := &commercial.Deliverer{Store: st, Exporter: exporter}
	deps.Deliverer = deliverer
	// DESIGN.md §9.6 — the platform seam enforcement runs through. Every
	// suspension is recorded and audited whether or not a platform is wired.
	// On a Sovereign the chart sets PLATFORM_API_URL to the in-cluster
	// sovereign-admin API and PLATFORM_API_BEARER_FILE (deprecated alias:
	// PLATFORM_API_TOKEN_FILE — a TOKEN-named env with a literal value trips
	// the Sovereign's Kyverno secret-not-in-env policy) to the projected
	// ServiceAccount token; the client re-reads that file on every call.
	var plat platform.Client = platform.Nop{}
	if cfg.PlatformAPIURL != "" {
		plat = platform.NewHTTP(cfg.PlatformAPIURL, cfg.PlatformAPIToken, cfg.PlatformAPITokenFile)
		slog.Info("platform enforcement enabled", "url", cfg.PlatformAPIURL, "token_file", cfg.PlatformAPITokenFile, "token_literal", cfg.PlatformAPIToken != "")
	} else {
		slog.Info("platform enforcement off: PLATFORM_API_URL unset; suspensions are recorded here only")
	}
	enforcer := &collections.Enforcer{Store: st, Platform: plat}
	deps.Enforcer = enforcer
	if cfg.CommercialImportSecret != "" {
		deps.Importer = &commercial.Importer{Store: st, Secret: cfg.CommercialImportSecret, Enforcer: enforcer}
		slog.Info("commercial import webhooks enabled")
	}
	handler := api.New(deps)

	if cfg.CollectorEnabled {
		go collector.Run(ctx)
		slog.Info("collector started", "collect_interval", cfg.CollectInterval, "cts_interval", cfg.CTSPollInterval, "ces_interval", cfg.CESInterval, "endpoint_template", cfg.HuaweiEndpointTemplate, "insecure_tls", cfg.HuaweiInsecureTLS)
	} else {
		slog.Info("collector disabled by COLLECTOR_ENABLED=false")
	}
	// DESIGN.md §20 — keep the daily cost rollup current: rebuild every
	// (source, day) partition a usage write has marked, so the Overview's
	// month is read from the aggregated ledger rather than rated over every
	// hourly record (#6926). Stale partitions are served live meanwhile, so
	// this loop can never make a figure wrong — only a page slower.
	if cfg.CostRollupEnabled {
		go (&rollup.Builder{Store: st, Interval: cfg.CostRollupInterval, Metrics: reg}).Run(ctx)
		slog.Info("cost rollup builder started", "interval", cfg.CostRollupInterval)
	} else {
		slog.Info("cost rollup disabled by COST_ROLLUP_ENABLED=false: every window is rated over the hourly ledger")
	}
	go housekeeping(ctx, st)
	// DESIGN.md §8.10 — drain the commercial outbox: at-least-once delivery of
	// every rated bill queued for the operator's billing system, with backoff.
	// A no-op when the Sovereign invoices internally.
	go deliverer.Run(ctx)
	// #6867 — hourly budget evaluator: records each threshold crossing once
	// per period (budget_alerts), audits it and mails the budget's
	// recipients. First run one minute after start, then hourly.
	go (&budget.Evaluator{Store: st, Mail: deps.Mail}).Run(ctx)
	// DESIGN.md §9.6 — the daily collections evaluator: reminders on the
	// schedule, the escalation at its age, resumption once settled. A no-op
	// when the external billing system owns collections.
	go (&collections.Evaluator{Store: st, Mail: deps.Mail, Enforcer: enforcer, PublicURL: cfg.PublicURL, Owns: deps.Commercial.OwnsCollections}).Run(ctx)
	// DESIGN.md §9.1 — the polling fallback of the import webhooks.
	if cfg.CommercialImportDir != "" && deps.Importer != nil {
		poller := &external.DirectoryPoller{Dir: cfg.CommercialImportDir, Applier: deps.Importer}
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if ok, failed, err := poller.Poll(ctx); err != nil {
						slog.Warn("commercial import poller", "error", err)
					} else if ok+failed > 0 {
						slog.Info("commercial import poller", "applied", ok, "failed", failed)
					}
				}
			}
		}()
		slog.Info("commercial import poller enabled", "dir", cfg.CommercialImportDir)
	}
	// #6867 follow-up — scheduled cost reports: every 5 minutes, mail each
	// due schedule's report for the window its cadence implies (yesterday /
	// last 7 days / last month), record the delivery and advance next_at.
	// First poll one minute after start.
	go (&report.Scheduler{Store: st, Mail: deps.Mail, PublicURL: cfg.PublicURL}).Run(ctx)

	// OpenOva adapter (ADR-0014 D2 case 1): Organization → Customer sync +
	// the platform collector, in this same binary. On by default only for
	// the sovereign profile with in-cluster access; ADAPTER_ENABLED
	// overrides (openova.Decide). The operator-central profile and every
	// off-cluster run keep the standalone engine untouched (D5 invariant).
	restCfg, restErr := rest.InClusterConfig()
	adapterOn, why := openova.Decide(cfg.Profile, cfg.AdapterEnabled, restErr == nil)
	if adapterOn {
		dyn, derr := dynamic.NewForConfig(restCfg)
		clientset, cerr := kubernetes.NewForConfig(restCfg)
		if derr != nil || cerr != nil {
			slog.Error("openova adapter: build Kubernetes clients", "dynamic_error", derr, "clientset_error", cerr)
		} else {
			platform := &openova.PlatformCollector{Client: clientset, Repo: st, Metrics: reg}
			// #6850 — OrgSync tells the collector which Organization carries
			// the platform-overhead line, so the Sovereign's own footprint is
			// metered instead of dropped.
			orgSync := &openova.OrgSync{Dyn: dyn, Core: clientset, Repo: st, Keys: keys, Verifier: collector, Metrics: reg, OverheadSink: platform}
			go orgSync.Run(ctx)
			go platform.Run(ctx)
			slog.Info("openova adapter started", "reason", why)
		}
	} else {
		slog.Info("openova adapter off", "reason", why)
	}

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
