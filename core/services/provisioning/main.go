package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/openova-io/openova/core/services/provisioning/gitguard"
	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/handlers"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	// Configuration from environment.
	mongoURI := getEnv("MONGODB_URI", "mongodb://ferretdb:27017")
	mongoDBName := getEnv("MONGODB_DB", "provisioning")
	// REDPANDA_BROKERS — legacy Kafka-protocol bus. Empty on Sovereigns
	// (no Redpanda exists in cluster — NATS is the canonical bus per
	// ADR-0001 §6); populated on Catalyst-Zero for backward-compat with
	// any not-yet-migrated publishers.
	redpandaBrokersRaw := getEnv("REDPANDA_BROKERS", "")
	// NATS_URL — canonical JetStream bus per ADR-0001 §6. On Sovereigns
	// this is wired to nats-jetstream.nats-system.svc.cluster.local:4222
	// by the chart. Empty disables the NATS leg (Catalyst-Zero / dev
	// loops). At least one of NATS_URL / REDPANDA_BROKERS MUST be set or
	// the consumer refuses to start (silent no-op was the convergence-
	// blocking failure mode before PR #1627).
	natsURL := getEnv("NATS_URL", "")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8084")
	gitBasePath := getEnv("GIT_BASE_PATH", "clusters/contabo-mkt/tenants")
	sovereignFQDN := getEnv("SOVEREIGN_FQDN", "")
	catalogURL := getEnv("CATALOG_URL", "http://catalog.org-services.svc.cluster.local:8082")
	// Per-Sovereign org-pool parent zone (e.g. "omani.homes"). Empty
	// disables the Organization.spec.tenantPublic patch in
	// handlers/tenant_public_patch.go — the existing
	// Sovereign-wide tenant-wildcard route keeps legacy tenants
	// reachable. Per docs/INVIOLABLE-PRINCIPLES.md #4 this is never
	// hardcoded; every Sovereign picks its own pool zone via env.
	tenantParentDomain := getEnv("TENANT_PARENT_DOMAIN", "")

	// GitHub API credentials for committing manifests.
	githubToken := getEnv("GITHUB_TOKEN", "")
	githubOwner := getEnv("GITHUB_OWNER", "openova-io")
	githubRepo := getEnv("GITHUB_REPO", "openova-private")

	// ── Cross-cluster pollution guard (issue #944, CRITICAL) ─────────
	// Validate GIT_BASE_PATH against SOVEREIGN_FQDN at startup. Failure
	// is fail-loud — the binary refuses to start so an operator notices
	// the misconfiguration the moment the Pod fails readiness rather
	// than after alice signups have leaked into a foreign cluster's
	// tree.
	if err := gitguard.ValidateBasePath(gitBasePath, sovereignFQDN); err != nil {
		slog.Error("FATAL: GIT_BASE_PATH validation failed",
			"git_base_path", gitBasePath,
			"sovereign_fqdn", sovereignFQDN,
			"error", err)
		os.Exit(1)
	}
	slog.Info("git base path validated against sovereign fqdn",
		"git_base_path", gitBasePath,
		"sovereign_fqdn", sovereignFQDN)

	// ── Placeholder token guard (issue #940) ─────────────────────────
	// Reject leaked placeholder Secret bytes at startup so the operator
	// sees the failure before alice signups start hitting "401 Bad
	// credentials" buried in service logs.
	if err := gitguard.ValidateGitHubToken(githubToken); err != nil {
		slog.Error("FATAL: GITHUB_TOKEN validation failed", "error", err)
		os.Exit(1)
	}

	// Connect to MongoDB (FerretDB).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		slog.Error("failed to connect to MongoDB", "error", err)
		os.Exit(1)
	}
	if err := client.Ping(ctx, nil); err != nil {
		slog.Error("failed to ping MongoDB", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			slog.Error("failed to disconnect MongoDB", "error", err)
		}
	}()
	slog.Info("connected to FerretDB", "uri", mongoURI, "db", mongoDBName)

	// Wire the broker publisher + subscriber up front so the goroutines
	// below all use the same NATS/Kafka connections. ADR-0001 §6 makes
	// NATS the canonical convergence bus on Sovereigns; Redpanda stays
	// in place as a legacy bridge for Catalyst-Zero. At least one
	// transport MUST be configured or both MultiPublisher and
	// MultiSubscriber refuse to construct (silent no-op was the
	// convergence-blocking bug this binary's wiring exists to prevent).
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
		natsConn = nc
		slog.Info("connected to NATS JetStream", "url", natsURL)
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
	publisher, err := events.NewMultiPublisher(natsConn, kafkaProd)
	if err != nil {
		slog.Error("event bus misconfigured — neither NATS_URL nor REDPANDA_BROKERS set", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()

	// Initialize store, manifest generator, GitHub client, and handler.
	provisionStore := store.New(client, mongoDBName)

	// Ensure the unique IdempotencyKey index backing the job-dedup guarantee
	// (issue #71). Index creation is idempotent; we fail-loud on unexpected
	// errors so the process doesn't silently start without dedup protection.
	idxCtx, idxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := provisionStore.EnsureJobIndexes(idxCtx); err != nil {
		idxCancel()
		slog.Error("failed to create job indexes", "error", err)
		os.Exit(1)
	}
	// Ensure the partial unique index on (tenant_id, in-flight status) backing
	// the provision-dedup guarantee (#3744) so a credit-covered checkout that
	// fires the create entrypoint twice (event + HTTP) can't race itself into a
	// failed tenant via a duplicate Gitea commit on the shared org-tenants branch.
	if err := provisionStore.EnsureProvisionIndexes(idxCtx); err != nil {
		idxCancel()
		slog.Error("failed to create provision indexes", "error", err)
		os.Exit(1)
	}
	idxCancel()
	slog.Info("provisioning job + provision indexes ensured")
	generator := gitops.NewManifestGenerator(gitBasePath)
	// #3760 (Refs #3376 #3754) MIRROR-EVERYTHING: the Sovereign-local Harbor
	// host the per-tenant vCluster images pull through. Without proxying, the
	// harbor-proxy-pull Kyverno ClusterPolicy DENIES the vCluster StatefulSet
	// (its loft-sh/kubernetes + loft-sh/vcluster-oss initContainers) and the
	// tenant's app never runs. Default harbor.openova.io; cutover Step-04
	// flips it to harbor.<sovereign-fqdn>.
	generator.RegistryMirror = getEnv("VCLUSTER_IMAGE_REGISTRY", "harbor.openova.io")

	// #4060 PILLAR-3: the IaC cloud provider this Sovereign runs on
	// ("hetzner" | "huawei"). Selects the block-storage StorageClass the
	// per-tenant active-hot-standby CNPG pair PVCs bind to — hcloud-volumes
	// on Hetzner (hcloud-csi), evs-ssd on Huawei (huaweicloud-csi-driver
	// EVS). Without this, generateCNPGPair hardcoded hcloud-volumes and
	// every customer-Org CNPG PVC on a Huawei Sovereign stayed Pending
	// forever → Pillar-3 silently dead for tenant Orgs. Empty / unknown
	// defaults to the Hetzner class inside gitops.cnpgStorageClass().
	generator.CloudProvider = getEnv("CLOUD_PROVIDER", "hetzner")

	// #4706 — pin the per-Org HelmRelease-shaped apps' chart versions so a
	// funnel Org never installs off a floating `version: "*"` (one mis-tagged
	// ghcr artifact broke every Org install; majors could jump ungated).
	// Defaults = the current catalog-seed pins; operators override via env.
	// A missing/malformed entry falls back to "*" (never fatal).
	generator.HelmReleaseAppVersions = gitops.ParseHRAppVersions(getEnv(
		"CATALYST_HR_APP_CHART_VERSIONS", "openclaw=0.2.13,stalwart-mail=0.1.12"))

	// #4282/#4275 CROSS-REGION STANDBY: the flux-system Secret name holding
	// region-B's (the STANDBY region's) host-cluster kubeconfig. The per-Org
	// active-hot-standby bp-cnpg-pair is now split-side — the primary HR lands
	// in region A (host Flux's own cluster), the REPLICA HR is installed THROUGH
	// this kubeconfig INTO region B, where the openova.io/region=<replica_region>
	// nodes the standby's node-affinity requires actually live. Without this the
	// standby Cluster landed in region A → 0/N node match → pgbasebackup Pending
	// forever + no standby for the region-kill pillar (#4275). Empty falls back
	// to the deterministic default `sovereign-replica-region-kubeconfig` the
	// bootstrap mirrors region-B's kubeconfig into.
	generator.ReplicaRegionKubeSecret = getEnv("CATALYST_REPLICA_REGION_KUBECONFIG_SECRET", "")

	// #4272/#4307: the per-Sovereign org-pool parent zone (e.g. "omani.homes")
	// the funnel stamps onto the HelmRelease-shaped per-Org app hostnames
	// (bp-openclaw → openclaw.<slug>.<parent>, bp-stalwart-tenant →
	// mail.<slug>.<parent>). Same TENANT_PARENT_DOMAIN env the Handler reads
	// for the tenant-public patch — wired through the generator so the
	// openclaw/stalwart overlays emit the correct console-isolation hostnames.
	// Empty falls back to the catalog-canon default pool inside the generator.
	generator.ParentDomain = tenantParentDomain

	// #4272: the per-Sovereign apex domain (e.g. "omantel.biz") + the shared
	// Keycloak realm name. On a Sovereign whose per-Org Keycloak realm is
	// DISABLED (the default — CATALYST_PER_ORG_REALM_ENABLED=false → no
	// keycloak.<slug>.<parent> host is ever provisioned, so that host is
	// NXDOMAIN), the HelmRelease-shaped per-Org apps (bp-openclaw #4272,
	// bp-stalwart-tenant #4307) MUST point their OIDC issuer at the resolvable
	// SHARED realm fronted at auth.<fqdn> — the SAME issuer the console + every
	// other app uses — or openclaw's controller /readyz (which fetches the
	// issuer's JWKS) hangs at 503 forever. SOVEREIGN_FQDN is the same env the
	// git-base-path guard + KC-broker URL read; CATALYST_KC_REALM defaults to
	// "sovereign" (platform/keycloak/blueprint.yaml realm: sovereign). Empty
	// SOVEREIGN_FQDN (Catalyst-Zero) leaves the generator on the legacy per-Org
	// realm host — harmless on a cluster that genuinely runs per-Org realms.
	generator.SovereignFQDN = sovereignFQDN
	generator.SharedRealmName = getEnv("CATALYST_KC_REALM", "sovereign")

	// ── Git host coordinates (issue #940) ────────────────────────────
	// On Sovereigns the canonical Git target is the local Gitea (the
	// cutover step flipped the Sovereign's GitRepository CR to point
	// there). The provisioning binary's GitHub-Data-API client targets
	// whatever GITHUB_API_URL specifies — Gitea exposes a GitHub-
	// compatible Git Data API at /api/v1, so the wire format is
	// unchanged. Empty value falls back to https://api.github.com (the
	// contabo path).
	githubAPIURL := getEnv("GITHUB_API_URL", "")
	githubBranch := getEnv("GITHUB_BRANCH", "main")

	var gc *ghclient.Client
	if githubToken != "" {
		gc = ghclient.NewClientWithAPIURL(githubToken, githubOwner, githubRepo, githubAPIURL)
		slog.Info("GitHub client configured", "owner", githubOwner, "repo", githubRepo, "api_url_set", githubAPIURL != "", "branch", githubBranch)
	} else {
		slog.Warn("GITHUB_TOKEN not set — provisioning will fail to commit manifests")
	}

	// #4384 — Sovereign per-Org commit target. On a Sovereign the funnel/day-2
	// cart install commits the customer's purchased Applications into the
	// per-Org `<slug>/catalyst-tenant` repo's `vcluster/apps/` tree (the one
	// the org-controller bootstrapped + wired a Flux Kustomization for) instead
	// of the global catalog repo (GITHUB_OWNER/GITHUB_REPO = openova/openova),
	// which is the WRONG target for per-Org apps on a Sovereign and 404'd on
	// the empty-SHA tree path. Default ON when SOVEREIGN_FQDN is set (the same
	// signal that flips the chart's git coordinates to the local Gitea);
	// explicitly overridable via TENANT_GITOPS_PER_ORG (true|false).
	perOrgGitops := sovereignFQDN != ""
	if v := getEnv("TENANT_GITOPS_PER_ORG", ""); v != "" {
		perOrgGitops = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	perOrgRepoName := getEnv("TENANT_GITOPS_REPO", "catalyst-tenant")
	// The per-Org repo tracks `main` (the org-controller's per-Org Flux
	// GitRepository ref.branch) — NOT the global GITHUB_BRANCH (`org-tenants`
	// on a Sovereign, the cutover-mirror-protected branch of openova/openova).
	perOrgBranch := getEnv("TENANT_GITOPS_BRANCH", "main")

	h := &handlers.Handler{
		Store:              provisionStore,
		Producer:           publisher,
		Generator:          generator,
		GitHubClient:       gc,
		CatalogURL:         catalogURL,
		GitBasePath:        gitBasePath,
		SovereignFQDN:      sovereignFQDN,
		GitBranch:          githubBranch,
		TenantParentDomain: tenantParentDomain,
		// #4421: the EFFECTIVE apps pool — same gitops.ResolveParentDomain the
		// generator uses (TENANT_PARENT_DOMAIN, defaulting to omani.homes).
		// Resolved here so the Org-CR DNS-writer pool the tenant.created handler
		// stamps always equals the pool the apps-HTTPRoute renders under.
		AppsParentDomain: gitops.ResolveParentDomain(tenantParentDomain),
		PerOrgGitops:     perOrgGitops,
		PerOrgRepoName:   perOrgRepoName,
		PerOrgBranch:     perOrgBranch,
	}
	slog.Info("tenant-public patch wired",
		"tenant_parent_domain", tenantParentDomain,
		"enabled", tenantParentDomain != "")
	slog.Info("per-Org gitops commit target wired (#4384)",
		"per_org_gitops", perOrgGitops, "per_org_repo", perOrgRepoName,
		"per_org_branch", perOrgBranch, "sovereign_fqdn_set", sovereignFQDN != "")

	// Start event consumer in a background goroutine. The subscriber
	// fans events in from BOTH transports (whichever the operator wired
	// — NATS on Sovereigns, Redpanda on Catalyst-Zero, both during a
	// migration window). Event types map to canonical NATS subjects
	// `catalyst.tenant.created`, `catalyst.tenant.deleted`,
	// `catalyst.tenant.app_install_requested`,
	// `catalyst.tenant.app_uninstall_requested`, and
	// `catalyst.billing.order.placed` per ADR-0001 §6.
	var kafkaConsumer *events.Consumer
	if redpandaBrokersRaw != "" {
		kc, err := events.NewConsumer(
			strings.Split(redpandaBrokersRaw, ","),
			"provisioning",
			[]string{"org.order.events", "org.tenant.events"},
		)
		if err != nil {
			slog.Error("failed to create kafka consumer", "error", err)
			os.Exit(1)
		}
		kafkaConsumer = kc
	}
	subscriber, err := events.NewMultiSubscriber(events.MultiSubscriberConfig{
		NATS:  natsConn,
		Kafka: kafkaConsumer,
		Group: "provisioning",
		EventTypes: []string{
			"tenant.created",
			"tenant.deleted",
			"tenant.app_install_requested",
			"tenant.app_uninstall_requested",
			"order.placed",
		},
	})
	if err != nil {
		slog.Error("failed to create event subscriber", "error", err)
		os.Exit(1)
	}
	defer subscriber.Close()

	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	go func() {
		if err := h.StartConsumer(consumerCtx, subscriber); err != nil {
			slog.Error("event consumer stopped", "error", err)
		}
	}()
	slog.Info("event consumer started",
		"nats_subjects", []string{
			"catalyst.tenant.created",
			"catalyst.tenant.deleted",
			"catalyst.tenant.app_install_requested",
			"catalyst.tenant.app_uninstall_requested",
			"catalyst.billing.order.placed",
		},
		"kafka_topics", []string{"org.order.events", "org.tenant.events"},
		"kafka_enabled", kafkaConsumer != nil,
		"nats_enabled", natsConn != nil,
		"group", "provisioning",
	)

	// Start the kubeconfig mirror reconciler (issue #104). Self-heals
	// tenants whose provisioning pod was killed between the DNS-ready wait
	// and the kubeconfig mirror step — without this, a CI deploy or OOM
	// kill mid-provision leaves a tenant's Flux Kustomization permanently
	// stuck with "secret not found". Reuses consumerCtx so the goroutine
	// stops cleanly on shutdown.
	h.StartKubeconfigReconciler(consumerCtx)
	// Pod-truth reconciler (issue #115): advances stuck provision steps +
	// clears 'installing' app_states when apps are actually Ready. Essential
	// for the case where a pod restart orphans the in-memory workflow mid-
	// provision — without this the UI sits on "INSTALLING" while the pods
	// are happily running.
	h.StartPodTruthReconciler(consumerCtx)
	// Pending-install self-heal reconciler (#4404): re-attempts day-2 cart
	// installs whose step-0 commit could not land because the funnel raced the
	// org-controller's per-Org Gitea org/repo create and the in-line retry
	// budget exhausted before the repo appeared. Drains the parked installs
	// once the repo exists so a slow per-Org create never drops the purchased
	// app. Reuses consumerCtx so the goroutine stops cleanly on shutdown.
	h.StartPendingInstallReconciler(consumerCtx)

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Provisioning routes — public status endpoints + JWT-protected admin endpoints.
	provisionRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	mux.Handle("/provisioning/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Admin and start endpoints require JWT.
		if strings.HasPrefix(r.URL.Path, "/provisioning/admin/") || r.URL.Path == "/provisioning/start" {
			jwtMiddleware(provisionRoutes).ServeHTTP(w, r)
			return
		}
		// Status and tenant lookups are public (used by frontend polling).
		provisionRoutes.ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting provisioning service", "port", port)
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
