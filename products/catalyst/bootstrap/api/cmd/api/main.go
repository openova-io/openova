package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
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

	// OpenBao client — wired ONLY when CATALYST_OPENBAO_ADDR +
	// CATALYST_OPENBAO_TOKEN are both set. Production catalyst-api Pods
	// running on a Sovereign cluster set both (the new Sovereign IS the
	// handover target — see issue #317). Catalyst-Zero leaves them
	// unset and ReceiveTofuArchive returns 503 ("not handover target")
	// for any misrouted POST.
	if addr := os.Getenv("CATALYST_OPENBAO_ADDR"); addr != "" {
		if token := os.Getenv("CATALYST_OPENBAO_TOKEN"); token != "" {
			h.SetOpenBao(openbao.New(addr, token))
			log.Info("openbao: handover-archive receiver enabled",
				"addr", addr,
			)
		} else {
			log.Warn("openbao: CATALYST_OPENBAO_ADDR set but CATALYST_OPENBAO_TOKEN missing; receiver disabled")
		}
	}

	// Auth (issue #608, Phase-8b Agent A) — Keycloak OIDC / magic-link.
	// Wired only on Catalyst-Zero (CATALYST_KC_ADDR set). Sovereign
	// clusters leave this unset; RequireSession receives nil and becomes
	// a transparent passthrough per the nil-safe contract in
	// internal/auth/middleware.go.
	if kcAddr := os.Getenv("CATALYST_KC_ADDR"); kcAddr != "" {
		realm := env("CATALYST_KC_REALM", "openova")
		clientID := env("CATALYST_KC_CLIENT_ID", "catalyst-zero-ui")
		redirectURI := os.Getenv("CATALYST_KC_REDIRECT_URI")
		cookieSecret := os.Getenv("CATALYST_SESSION_COOKIE_SECRET")
		jwksURL := kcAddr + "/realms/" + realm + "/protocol/openid-connect/certs"
		authCfg := &auth.Config{
			KeycloakAddr: kcAddr,
			Realm:        realm,
			ClientID:     clientID,
			RedirectURI:  redirectURI,
			CookieSecret: cookieSecret,
			JWKSCache:    auth.NewJWKSCache(jwksURL, 10*time.Minute),
		}
		h.SetAuthConfig(authCfg)
		log.Info("auth: Keycloak OIDC enabled",
			"addr", kcAddr,
			"realm", realm,
			"client_id", clientID,
		)
	}

	// K8s data-plane (issue #321) — informer cache + SSE + disk
	// snapshot. Wired only when at least one kubeconfig is mountable;
	// in test/CI without a real cluster the catalyst-api still
	// serves every other endpoint and /healthz returns plain "ok"
	// per the legacy contract.
	ctx := context.Background()
	homeCore := mustHomeCoreClient(log)
	k8sFactory, err := k8scache.FactoryFromEnv(ctx, log, homeCore)
	if err != nil {
		log.Warn("k8scache: factory init failed; data plane disabled",
			"err", err,
		)
	} else if k8sFactory != nil {
		if err := k8sFactory.Start(ctx); err != nil {
			log.Warn("k8scache: factory start failed; data plane disabled",
				"err", err,
			)
		} else {
			sar := k8scache.NewSARCache()
			h.SetK8sCache(k8sFactory, sar, env("CATALYST_K8SCACHE_USER_HEADER", "X-Forwarded-User"))
			log.Info("k8scache: data plane started",
				"sovereigns", len(k8sFactory.Clusters()),
			)
		}
	}

	// /healthz is LIVENESS — always 200 if the process is up and the
	// HTTP server is serving. /readyz is READINESS — 200 only when
	// the primary Sovereign's informers are synced (or no Sovereigns
	// registered yet). The chart wires livenessProbe → /healthz and
	// readinessProbe → /readyz; failing readiness pulls the Pod out
	// of the Service rotation without restarting it. See the Health
	// + Ready handler comments and issue #530 for the crashloop
	// failure mode this split prevents.
	r.Get("/healthz", h.Health)
	r.Get("/readyz", h.Ready)
	r.Handle("/metrics", promhttp.Handler())

	// ── Unauthenticated auth endpoints (issue #608, Phase-8b Agent A) ────
	// These three must sit OUTSIDE the auth-gated group because they are
	// part of the login flow itself (no session yet).
	//
	// POST /api/v1/auth/magic-link  — triggers KC Execute Actions Email
	// GET  /api/v1/auth/callback    — PKCE code exchange → session cookie
	// DELETE /api/v1/auth/session   — logout / clear session cookie
	r.Post("/api/v1/auth/magic-link", h.HandleMagicLink)
	r.Get("/api/v1/auth/callback", h.HandleAuthCallback)
	r.Delete("/api/v1/auth/session", h.HandleAuthLogout)

	// ── Auth-gated wizard routes (issue #608) ─────────────────────────────
	// RequireSession is nil-safe: when authConfig is nil (Sovereign clusters,
	// CI without CATALYST_KC_ADDR), the middleware is a transparent passthrough
	// so all existing Sovereign-side behaviour is preserved unchanged.
	r.Group(func(rg chi.Router) {
		rg.Use(auth.RequireSession(h.GetAuthConfig(), log))

		// /api/v1/whoami — identity endpoint (success criterion for issue #608)
		rg.Get("/api/v1/whoami", h.HandleWhoami)

		// K8s data-plane endpoints — list + SSE stream + sync map per
		// Sovereign cluster (issue #321). Per ADR-0001 §5 the catalyst-api
		// is the consolidator; reads flow off the in-process Indexer,
		// never directly from the apiserver.
		rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}", h.HandleK8sList)
		rg.Get("/api/v1/sovereigns/{id}/k8s/stream", h.HandleK8sStream)
		rg.Get("/api/v1/sovereigns/{id}/k8s/sync", h.HandleK8sSync)
		rg.Post("/api/v1/credentials/validate", h.ValidateCredentials)
		// Hetzner Object Storage credential validator (issue #371). The wizard's
		// StepCredentials Object-Storage section POSTs here BEFORE allowing the
		// operator to advance to StepReview, so a typo'd access/secret pair
		// surfaces as a wizard-step error card rather than 5 minutes into
		// `tofu apply`. Hetzner exposes no API for credential issuance — the
		// operator generates them once in the Hetzner Console; this endpoint
		// confirms the operator-supplied keys can authenticate against the
		// chosen region's S3 endpoint via ListBuckets.
		rg.Post("/api/v1/credentials/object-storage/validate", h.ValidateObjectStorageCredentials)
		rg.Post("/api/v1/subdomains/check", h.CheckSubdomain)
		// SSH keypair generator — wizard's "auto-generate" Mode A path
		// (issue #160). Returns publicKey + privateKey + fingerprint; the
		// handler logs ONLY the fingerprint and never persists either half.
		rg.Post("/api/v1/sshkey/generate", h.GenerateSSHKey)
		rg.Post("/api/v1/deployments", h.CreateDeployment)
		rg.Get("/api/v1/deployments/{id}", h.GetDeployment)
		rg.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
		// Buffered event history endpoint (issue #180). Returns the full event
		// slice + state JSON so the wizard's ProvisionPage can render history
		// for a deployment that already finished — the SSE replay-on-connect
		// covers the same path, but the GET is a stateless fast-path test
		// + reconnect target.
		rg.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
		// Kubeconfig endpoint — wizard StepSuccess "Download kubeconfig"
		// button + Sovereign Admin break-glass download + the source the
		// internal/helmwatch HelmRelease watcher reads from when the
		// catalyst-api Pod cold-starts mid-Phase-1 and has to reattach
		// to a deployment whose kubeconfig is on the PVC.
		rg.Get("/api/v1/deployments/{id}/kubeconfig", h.GetKubeconfig)
		// PUT — cloud-init postback (issue #183, Option D). The new
		// Sovereign's control plane PUTs its rewritten kubeconfig here
		// with an Authorization: Bearer header. The handler verifies
		// SHA-256 of the bearer against the persisted hash, writes the
		// kubeconfig file to the PVC at mode 0600, and triggers the
		// Phase-1 helmwatch goroutine.
		rg.Put("/api/v1/deployments/{id}/kubeconfig", h.PutKubeconfig)
		// Registrar proxy — wizard's BYO Flow B (#169). /validate is called
		// pre-submit so a typo'd token surfaces at the prompt; /set-ns is
		// called from CreateDeployment when domainMode == byo-api.
		rg.Post("/api/v1/registrar/{registrar}/validate", h.ValidateRegistrar)
		rg.Post("/api/v1/registrar/{registrar}/set-ns", h.SetNSRegistrar)
		// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
		// Phase 0 retries re-run `tofu apply` against the existing workdir;
		// Phase 1 retries emit operator instructions per the architectural
		// contract (Flux owns Phase 1 reconciliation).
		rg.Post("/api/v1/deployments/{id}/phases/{phase}/retry", h.RetryPhase)
		// Cancel & Wipe endpoint (issue #318). Operator-triggered purge of a
		// failed or abandoned deployment: tofu destroy + Hetzner orphan purge
		// + PDM release + local state cleanup. Idempotent. Returns 200 with a
		// PurgeReport summary. The wizard's failed-state banner renders the
		// operator confirmation modal that POSTs here.
		rg.Post("/api/v1/deployments/{id}/wipe", h.WipeDeployment)
		// Subdomain-only release endpoint (issue #489). Releases the PDM
		// allocation row for a failed-or-abandoned deployment WITHOUT
		// requiring the operator to re-enter their HetznerToken. Lets a
		// franchise customer retry under the same pool subdomain after a
		// botched provision instead of being forced to pick acmeN+1. Does
		// NOT touch Hetzner — the Cancel & Wipe flow remains the canonical
		// path for live cloud cleanup. Refuses on in-flight deployments
		// (409), wiped deployments (410), or adopted Sovereigns (422).
		rg.Delete("/api/v1/deployments/{id}/release-subdomain", h.ReleaseSubdomain)
		// Handover finalisation (issue #317). Catalyst-Zero side: stops the
		// helmwatch informer, ships the OpenTofu state to the new Sovereign's
		// catalyst-api, and purges every local trace once the new side
		// confirms the archive is sealed in its OpenBao. Sovereign side:
		// receives the archive on /handover/tofu-archive and writes it to
		// `secret/catalyst/tofu-phase0-archive`. The two endpoints live on
		// the same binary; Catalyst-Zero leaves CATALYST_OPENBAO_ADDR unset,
		// so a misrouted archive POST hits 503 instead of 200.
		rg.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
		rg.Post("/api/v1/handover/tofu-archive", h.ReceiveTofuArchive)
		// Jobs/Executions REST surface — the canvas + per-job detail
		// pages read this in parallel to the existing SSE events feed.
		// All endpoints are read-only; every mutation flows through the
		// helmwatch bridge in internal/jobs. Each Job carries parentId +
		// childIds so the FE can render the recursive Job tree without
		// any batch-specific endpoint (issue #351).
		rg.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
		rg.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
		rg.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
		// Backfill endpoints — give the FE an explicit handshake to
		// re-attach the helmwatch goroutine after a Pod restart and to
		// snapshot the in-memory informer cache. The bridge seeds a Job
		// per HR observed on initial-list so HRs that have been
		// Ready=True for an hour materialise rows immediately rather
		// than only on state transitions.
		rg.Post("/api/v1/deployments/{depId}/refresh-watch", h.RefreshWatch)
		rg.Get("/api/v1/deployments/{depId}/components/state", h.GetComponentsState)
		// Sovereign Dashboard treemap (resource utilisation). Read-only.
		// V1 emits a static placeholder shape — see dashboard.go header
		// for the metrics-server upgrade plan.
		rg.Get("/api/v1/dashboard/treemap", h.GetDashboardTreemap)
		// Sovereign Infrastructure surface — unified topology read +
		// Day-2 CRUD via Crossplane XRC writes (issue #227 + Day-2 IaC).
		// Read endpoints compose from the deployment record + live
		// cluster informer cache; mutation endpoints write Composite
		// Resource Claims to the Sovereign cluster's kubeconfig per
		// docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the ONLY
		// Day-2 IaC seam). Every mutation also commits a Job entry to
		// the existing /jobs surface for full audit-trail.
		rg.Get("/api/v1/deployments/{depId}/infrastructure/topology", h.GetInfrastructureTopology)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/compute", h.GetInfrastructureCompute)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/storage", h.GetInfrastructureStorage)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/network", h.GetInfrastructureNetwork)

		// CRUD — every endpoint writes a Crossplane XRC + a mutation Job.
		// The third-sibling chart authors the matching Compositions; until
		// they land Crossplane sits the claim Pending and the catalyst-api
		// surfaces "Awaiting Composition for <kind>" in the audit log.
		rg.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/regions/{id}", h.PatchInfrastructureRegion)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/regions/{id}/clusters", h.CreateInfrastructureCluster)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/clusters/{id}", h.PatchInfrastructureCluster)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/vclusters", h.CreateInfrastructureVCluster)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/vclusters/{id}", h.PatchInfrastructureVCluster)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/pools", h.CreateInfrastructurePool)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/pools/{id}", h.PatchInfrastructurePool)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/nodes", h.CreateInfrastructureWorkerNode)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/nodes/{id}", h.PatchInfrastructureWorkerNode)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/loadbalancers", h.CreateInfrastructureLoadBalancer)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/loadbalancers/{id}", h.PatchInfrastructureLoadBalancer)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/networks", h.CreateInfrastructureNetwork)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/networks/{id}", h.PatchInfrastructureNetwork)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/pvcs", h.CreateInfrastructurePVC)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/pvcs/{id}", h.PatchInfrastructurePVC)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/buckets", h.CreateInfrastructureBucket)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/buckets/{id}", h.PatchInfrastructureBucket)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/volumes", h.CreateInfrastructureVolume)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/volumes/{id}", h.PatchInfrastructureVolume)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/peerings", h.CreateInfrastructurePeering)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/firewalls/{id}/rules", h.CreateInfrastructureFirewallRule)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/nodes/{id}/{action}", h.CreateInfrastructureNodeAction)
		rg.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)

		// Sovereign IAM — UserAccess CR editor (issue #323). The UI's
		// /sovereign/users page calls these endpoints to list / create /
		// update / delete UserAccess CRs against the Sovereign cluster.
		// The CRD shape (`access.openova.io/v1alpha1`) is shipped by
		// issue #322's chart; catalyst-api consumes it via dynamic
		// client so there's no Go-type build dependency between the
		// two PRs.
		rg.Get("/api/v1/deployments/{depId}/admin/user-access", h.ListUserAccess)
		rg.Post("/api/v1/deployments/{depId}/admin/user-access", h.CreateUserAccess)
		rg.Put("/api/v1/deployments/{depId}/admin/user-access/{name}", h.UpdateUserAccess)
		rg.Delete("/api/v1/deployments/{depId}/admin/user-access/{name}", h.DeleteUserAccess)
	}) // end auth-gated wizard routes

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

// mustHomeCoreClient returns a typed kubernetes.Interface for the
// catalyst-api's own (home) cluster. Used to read the optional
// kinds-registry ConfigMap. A nil return value disables ConfigMap
// loading — the default kinds registry is sufficient.
//
// In production the catalyst-api Pod runs with a ServiceAccount that
// has `get` on ConfigMaps in the catalyst namespace; out-of-cluster
// (CI, smoke test) the in-cluster config build fails and we return
// nil + a warn log.
func mustHomeCoreClient(log *slog.Logger) kubernetes.Interface {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Info("k8scache: in-cluster config unavailable; kinds ConfigMap loading disabled",
			"err", err,
		)
		return nil
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Warn("k8scache: home core client build failed",
			"err", err,
		)
		return nil
	}
	return c
}
