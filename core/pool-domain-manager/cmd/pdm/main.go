// Command pdm — pool-domain-manager service entrypoint.
//
// Wires CNPG/Postgres (store), the PowerDNS Authoritative REST client
// (pdns), the registrar adapters (#170), and the chi-based HTTP router.
// At startup it bootstraps every managed pool zone in PowerDNS so /reserve
// can issue NS-delegation records into a parent zone that exists.
//
// All configuration is read from environment variables — per
// docs/INVIOLABLE-PRINCIPLES.md #4 nothing here is hardcoded:
//
//	PORT                       — listen port (default 8080)
//	PDM_DATABASE_URL           — postgres DSN, REQUIRED
//	PDM_PDNS_BASE_URL          — PowerDNS REST API base URL, REQUIRED
//	                              (e.g. http://powerdns.openova-system.svc.cluster.local:8081)
//	PDM_PDNS_API_KEY           — PowerDNS X-API-Key header value, REQUIRED
//	PDM_PDNS_SERVER_ID         — PowerDNS server identifier, default "localhost"
//	PDM_NAMESERVERS            — comma-separated FQDNs for child-zone NS RRsets and
//	                              parent NS delegation records, default
//	                              "ns1.openova.io,ns2.openova.io,ns3.openova.io"
//	DYNADOT_MANAGED_DOMAINS    — comma-separated managed pool list (for /check
//	                              gating + parent-zone bootstrap)
//	DYNADOT_DOMAIN             — legacy single-domain fallback
//	DYNADOT_API_KEY            — kept for the registrar adapter (#170 BYO flow)
//	DYNADOT_API_SECRET         — kept for the registrar adapter (#170 BYO flow)
//	POOL_DOMAIN_MANAGER_NS_GLUE_IP — IPv4 of the mothership PowerDNS LB. When
//	                              set, the Dynadot registrar adapter pre-
//	                              registers every out-of-bailiwick NS hostname
//	                              against the customer's Dynadot account before
//	                              set_ns, fixing the parent-domain onboard
//	                              flow that previously failed on Dynadot's
//	                              "'ns3.openova.io' needs to be registered
//	                              with an ip address" rejection (issue #1500).
//	PDM_RESERVATION_TTL        — go duration string, default "10m"
//	PDM_SWEEPER_INTERVAL       — go duration string, default "30s"
//	PDM_REAP_STALE_PARENT_WILDCARDS — reap a foreign `*.<pool>` wildcard from each
//	                              managed parent pool zone at bootstrap (#5007),
//	                              default true. pdm never writes a parent wildcard,
//	                              so this only removes stale/foreign leftovers that
//	                              would shadow delegated child names.
//	PDM_LOG_LEVEL              — debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/allocator"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/handler"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/pdns"
	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
	regCloudflare "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar/cloudflare"
	regDynadot "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar/dynadot"
	regGoDaddy "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar/godaddy"
	regNamecheap "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar/namecheap"
	regOVH "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar/ovh"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

func main() {
	log := newLogger(env("PDM_LOG_LEVEL", "info"))
	slog.SetDefault(log)

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()

	s, err := store.New(startCtx, cfg.DatabaseURL)
	if err != nil {
		log.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	pdnsClient := pdns.New(cfg.PDNSBaseURL, cfg.PDNSServerID, cfg.PDNSAPIKey)

	alloc := allocator.New(s, pdnsClient, log, allocator.Config{
		Nameservers:              cfg.Nameservers,
		ReservationTTL:           cfg.ReservationTTL,
		ReapStaleParentWildcards: cfg.ReapStaleParentWildcards,
	})

	// Bootstrap every managed pool zone before HTTP serves traffic. /reserve
	// requires the parent zone to exist so the NS-delegation RRset has
	// somewhere to land. Per docs/PLATFORM-POWERDNS.md the parent zone is
	// authoritative for the OpenOva pool (e.g. `omani.works`) and signs
	// the DS records that anchor each Sovereign's DNSSEC chain.
	bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, 60*time.Second)
	if err := alloc.BootstrapParentZones(bootstrapCtx, dynadot.ManagedDomains()); err != nil {
		bootstrapCancel()
		log.Error("parent-zone bootstrap failed",
			"managedDomains", dynadot.ManagedDomains(),
			"err", err)
		os.Exit(1)
	}
	bootstrapCancel()

	go alloc.RunSweeper(ctx, cfg.SweeperInterval)

	h := handler.New(alloc, s, log)

	// Build the registrar registry: every adapter wires up unconditionally
	// because the customer's API token is supplied per request, not at
	// service-start. Disabling an adapter would only mean omitting it from
	// the map; today we ship all 5.
	//
	// Dynadot adapter is constructed with NSGlueIP from
	// POOL_DOMAIN_MANAGER_NS_GLUE_IP (when set) so SetNameservers can
	// pre-register every out-of-bailiwick NS hostname against the
	// customer's account before set_ns. This unblocks the mothership
	// parent-domain onboard flow for fresh Dynadot domains that haven't
	// yet had ns1/ns2/ns3.openova.io registered as glue records (issue
	// #1500, 2026-05-15). Empty value → adapter falls back to its
	// pre-fix behaviour and the handler-level glueIP path (BYO Flow B)
	// remains authoritative for per-request glue.
	dynadotAdapter := regDynadot.New()
	dynadotAdapter.NSGlueIP = strings.TrimSpace(os.Getenv("POOL_DOMAIN_MANAGER_NS_GLUE_IP"))
	reg := registrar.Registry{
		regCloudflare.New().Name(): regCloudflare.New(),
		regGoDaddy.New().Name():    regGoDaddy.New(),
		regNamecheap.New().Name():  regNamecheap.New(),
		regOVH.New().Name():        regOVH.New(),
		dynadotAdapter.Name():      dynadotAdapter,
	}
	h.SetRegistry(reg)
	log.Info("registrar adapters wired",
		"registrars", reg.Names(),
		"dynadotGlueAutoRegister", dynadotAdapter.NSGlueIP != "",
	)

	root := chi.NewRouter()
	root.Use(middleware.RequestID)
	root.Use(middleware.RealIP)
	root.Use(middleware.Logger)
	root.Use(middleware.Recoverer)
	root.Mount("/", h.Routes())

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Surface the managed-domain list at startup so operators can grep logs
	// for misconfiguration (e.g. typo in the secret's `domains` key).
	log.Info("pool-domain-manager starting",
		"port", cfg.Port,
		"reservationTTL", cfg.ReservationTTL.String(),
		"sweeperInterval", cfg.SweeperInterval.String(),
		"managedDomains", dynadot.ManagedDomains(),
		"nameservers", cfg.Nameservers,
		"pdnsBaseURL", cfg.PDNSBaseURL,
		"pdnsServerID", cfg.PDNSServerID,
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received, draining")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

// config bundles the runtime configuration so loadConfig can return a single
// struct + error.
type config struct {
	Port            string
	DatabaseURL     string
	PDNSBaseURL     string
	PDNSAPIKey      string
	PDNSServerID    string
	Nameservers     []string
	ReservationTTL  time.Duration
	SweeperInterval time.Duration
	// ReapStaleParentWildcards — reap a foreign `*.<pool>` wildcard from each
	// managed parent pool zone at bootstrap (#5007). Default true.
	ReapStaleParentWildcards bool
}

func loadConfig() (*config, error) {
	c := &config{
		Port: env("PORT", "8080"),
	}
	c.DatabaseURL = strings.TrimSpace(os.Getenv("PDM_DATABASE_URL"))
	if c.DatabaseURL == "" {
		return nil, errors.New("PDM_DATABASE_URL is required")
	}

	c.PDNSBaseURL = strings.TrimSpace(os.Getenv("PDM_PDNS_BASE_URL"))
	if c.PDNSBaseURL == "" {
		return nil, errors.New("PDM_PDNS_BASE_URL is required")
	}
	c.PDNSAPIKey = strings.TrimSpace(os.Getenv("PDM_PDNS_API_KEY"))
	if c.PDNSAPIKey == "" {
		return nil, errors.New("PDM_PDNS_API_KEY is required")
	}
	c.PDNSServerID = strings.TrimSpace(env("PDM_PDNS_SERVER_ID", "localhost"))

	nsRaw := strings.TrimSpace(os.Getenv("PDM_NAMESERVERS"))
	if nsRaw == "" {
		// Default per docs/PLATFORM-POWERDNS.md — these are the canonical
		// NS endpoints documented for the OpenOva fleet. Configurable via
		// PDM_NAMESERVERS so a Sovereign-overlay can rebadge.
		nsRaw = "ns1.openova.io,ns2.openova.io,ns3.openova.io"
	}
	c.Nameservers = parseNameservers(nsRaw)
	if len(c.Nameservers) == 0 {
		return nil, errors.New("PDM_NAMESERVERS contained no valid hostnames")
	}

	ttlStr := env("PDM_RESERVATION_TTL", "10m")
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		return nil, errors.New("PDM_RESERVATION_TTL is not a valid duration: " + err.Error())
	}
	c.ReservationTTL = ttl

	swStr := env("PDM_SWEEPER_INTERVAL", "30s")
	sw, err := time.ParseDuration(swStr)
	if err != nil {
		return nil, errors.New("PDM_SWEEPER_INTERVAL is not a valid duration: " + err.Error())
	}
	c.SweeperInterval = sw

	// #5007 — reap a stale/foreign `*.<pool>` wildcard from each managed parent
	// pool zone at bootstrap. Default true (pdm never writes a parent wildcard, so
	// reaping one is always safe in its delegation model). Set false only where a
	// deployment intentionally serves a flat parent wildcard managed elsewhere.
	c.ReapStaleParentWildcards = envBool("PDM_REAP_STALE_PARENT_WILDCARDS", true)

	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean env var, returning fallback when unset/blank.
// Accepts the usual truthy/falsey spellings; an unparseable value falls back.
func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func parseNameservers(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", " ")
	parts := strings.Fields(raw)
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
