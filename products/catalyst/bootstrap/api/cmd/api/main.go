package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

func main() {
	port := env("PORT", "8080")
	corsOrigin := env("CORS_ORIGIN", "*")

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{corsOrigin},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Content-Type", "Authorization"},
		MaxAge:         300,
	}))

	h := handler.New(log)
	r.Get("/healthz", h.Health)
	r.Post("/api/v1/credentials/validate", h.ValidateCredentials)
	r.Post("/api/v1/subdomains/check", h.CheckSubdomain)
	// SSH keypair generator — wizard's "auto-generate" Mode A path
	// (issue #160). Returns publicKey + privateKey + fingerprint; the
	// handler logs ONLY the fingerprint and never persists either half.
	r.Post("/api/v1/sshkey/generate", h.GenerateSSHKey)
	r.Post("/api/v1/deployments", h.CreateDeployment)
	r.Get("/api/v1/deployments/{id}", h.GetDeployment)
	r.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
	// Buffered event history endpoint (issue #180). Returns the full event
	// slice + state JSON so the wizard's ProvisionPage can render history
	// for a deployment that already finished — the SSE replay-on-connect
	// covers the same path, but the GET is a stateless fast-path test
	// + reconnect target.
	r.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
	// Kubeconfig endpoint — wizard StepSuccess "Download kubeconfig"
	// button + Sovereign Admin break-glass download + the source the
	// internal/helmwatch HelmRelease watcher reads from when the
	// catalyst-api Pod cold-starts mid-Phase-1 and has to reattach
	// to a deployment whose kubeconfig is on the PVC.
	r.Get("/api/v1/deployments/{id}/kubeconfig", h.GetKubeconfig)
	// Registrar proxy — wizard's BYO Flow B (#169). /validate is called
	// pre-submit so a typo'd token surfaces at the prompt; /set-ns is
	// called from CreateDeployment when domainMode == byo-api.
	r.Post("/api/v1/registrar/{registrar}/validate", h.ValidateRegistrar)
	r.Post("/api/v1/registrar/{registrar}/set-ns", h.SetNSRegistrar)
	// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
	// Phase 0 retries re-run `tofu apply` against the existing workdir;
	// Phase 1 retries emit operator instructions per the architectural
	// contract (Flux owns Phase 1 reconciliation).
	r.Post("/api/v1/deployments/{id}/phases/{phase}/retry", h.RetryPhase)

	log.Info("catalyst api listening", "port", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
