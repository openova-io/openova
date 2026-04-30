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
		// PUT enabled for the cloud-init kubeconfig postback (issue
		// #183) — the new Sovereign's cloud-init writes its own
		// kubeconfig to /api/v1/deployments/{id}/kubeconfig with a
		// bearer token. CORS is irrelevant for that caller (curl
		// from the new VM, not a browser), but enabling PUT here
		// keeps the policy consistent for any future browser-side
		// resume flow that re-uses the same endpoint.
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
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
	// PUT — cloud-init postback (issue #183, Option D). The new
	// Sovereign's control plane PUTs its rewritten kubeconfig here
	// with an Authorization: Bearer header. The handler verifies
	// SHA-256 of the bearer against the persisted hash, writes the
	// kubeconfig file to the PVC at mode 0600, and triggers the
	// Phase-1 helmwatch goroutine.
	r.Put("/api/v1/deployments/{id}/kubeconfig", h.PutKubeconfig)
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
	// Jobs/Executions REST surface (issue #205, sub of epic #204) — the
	// table-view UX reads this in parallel to the existing SSE events
	// feed. The 4 endpoints are read-only; every mutation flows
	// through the helmwatch bridge in internal/jobs.
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
	r.Get("/api/v1/deployments/{depId}/jobs/batches", h.ListBatches)
	r.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
	r.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
	// Backfill endpoints — give the FE an explicit handshake to
	// re-attach the helmwatch goroutine after a Pod restart and to
	// snapshot the in-memory informer cache. The bridge seeds a Job
	// per HR observed on initial-list so HRs that have been
	// Ready=True for an hour materialise rows immediately rather
	// than only on state transitions.
	r.Post("/api/v1/deployments/{depId}/refresh-watch", h.RefreshWatch)
	r.Get("/api/v1/deployments/{depId}/components/state", h.GetComponentsState)
	// Sovereign Dashboard treemap (resource utilisation). Read-only.
	// V1 emits a static placeholder shape — see dashboard.go header
	// for the metrics-server upgrade plan.
	r.Get("/api/v1/dashboard/treemap", h.GetDashboardTreemap)
	// Sovereign Infrastructure surface — unified topology read +
	// Day-2 CRUD via Crossplane XRC writes (issue #227 + Day-2 IaC).
	// Read endpoints compose from the deployment record + live
	// cluster informer cache; mutation endpoints write Composite
	// Resource Claims to the Sovereign cluster's kubeconfig per
	// docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the ONLY
	// Day-2 IaC seam). Every mutation also commits a Job entry to
	// the existing /jobs surface for full audit-trail.
	r.Get("/api/v1/deployments/{depId}/infrastructure/topology", h.GetInfrastructureTopology)
	r.Get("/api/v1/deployments/{depId}/infrastructure/compute", h.GetInfrastructureCompute)
	r.Get("/api/v1/deployments/{depId}/infrastructure/storage", h.GetInfrastructureStorage)
	r.Get("/api/v1/deployments/{depId}/infrastructure/network", h.GetInfrastructureNetwork)

	// CRUD — every endpoint writes a Crossplane XRC + a mutation Job.
	// The third-sibling chart authors the matching Compositions; until
	// they land Crossplane sits the claim Pending and the catalyst-api
	// surfaces "Awaiting Composition for <kind>" in the audit log.
	r.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
	r.Post("/api/v1/deployments/{depId}/infrastructure/regions/{id}/clusters", h.CreateInfrastructureCluster)
	r.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/vclusters", h.CreateInfrastructureVCluster)
	r.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/pools", h.CreateInfrastructurePool)
	r.Patch("/api/v1/deployments/{depId}/infrastructure/pools/{id}", h.PatchInfrastructurePool)
	r.Post("/api/v1/deployments/{depId}/infrastructure/loadbalancers", h.CreateInfrastructureLoadBalancer)
	r.Post("/api/v1/deployments/{depId}/infrastructure/peerings", h.CreateInfrastructurePeering)
	r.Post("/api/v1/deployments/{depId}/infrastructure/firewalls/{id}/rules", h.CreateInfrastructureFirewallRule)
	r.Post("/api/v1/deployments/{depId}/infrastructure/nodes/{id}/{action}", h.CreateInfrastructureNodeAction)
	r.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)

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
