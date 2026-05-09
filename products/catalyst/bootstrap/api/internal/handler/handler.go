// Package handler holds shared state for all HTTP handlers.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Handler holds shared state for all HTTP handlers.
//
// dynadotAPIKey + dynadotAPISecret remain on the Handler so the OpenTofu
// module's `dynadot_*` variables can still receive credentials for the
// Phase-0 DNS bootstrap that runs at first `tofu apply` time. After #163
// Phase 4 lands the Crossplane Composition that wraps PDM as a declarative
// MR, even those fields go away (PDM holds the credentials; catalyst-api
// merely calls PDM via the in-cluster service FQDN).
//
// pdm is the central authority for OpenOva-pool subdomain allocation
// (introduced by #163). catalyst-api never calls api.dynadot.com directly
// for the availability check / reservation lifecycle after this lands —
// every interaction with the Dynadot zone flows through PDM.
//
// store is the flat-file persistence layer for deployments. The
// catalyst-api Pod has been observed restarting 6+ times within a
// 30-minute provisioning run (image rolls, OOM, cluster maintenance) —
// without persistence, a wizard mid-flight loses every deployment id
// to the next Pod's empty sync.Map and the user sees "Unreachable /
// SSE connection closed before completion / Deployment id <id>".
// Persisting after every event + on terminal state, and walking the
// PVC at New() time to repopulate sync.Map, closes that gap.
type Handler struct {
	log              *slog.Logger
	deployments      sync.Map // map[string]*Deployment
	dynadotAPIKey    string
	dynadotAPISecret string

	// pdm — pool-domain-manager client. Required in production; tests can
	// inject a fake via NewWithPDM. The default URL points at the in-cluster
	// service FQDN so a stock Catalyst-Zero deployment "just works" without
	// per-pod configuration.
	pdm pdmClient

	// store — deployment persistence. Nil-tolerant: a nil store disables
	// persistence so existing tests (that build Handler{} or use
	// NewWithPDM without a directory) keep working unchanged. Production
	// always wires this via New() reading CATALYST_DEPLOYMENTS_DIR.
	store *store.Store

	// jobs — Jobs/Executions/LogLines persistence (issue #205, sub of
	// epic #204). Nil-tolerant the same way as `store` above: tests
	// building Handler{} directly run without a jobs store; production
	// New() wires this from CATALYST_EXECUTIONS_DIR. The 4 jobs HTTP
	// handlers respond 503 when this is nil.
	jobs *jobs.Store

	// kubeconfigsDir — directory the cloud-init postback handler
	// (PutKubeconfig, issue #183) writes the new Sovereign's
	// kubeconfig file into, mode 0600 per file. Same PVC as the
	// deployments store but a separate subdirectory so the JSON
	// records and the kubeconfig YAMLs don't co-mingle in one ls.
	// Empty disables the PUT endpoint (returns 503).
	kubeconfigsDir string

	// Phase-1 watch knobs — every field below is a test-only override
	// for internal/helmwatch.Config. Production reads
	// CATALYST_PHASE1_WATCH_TIMEOUT,
	// CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS,
	// CATALYST_PHASE1_FIRST_SEEN_TIMEOUT from env and uses
	// helmwatch.NewDynamicClientFromKubeconfig /
	// NewKubernetesClientFromKubeconfig. Tests inject a
	// fake.NewSimpleDynamicClient via dynamicFactory and a tiny
	// timeout via phase1WatchTimeout so termination behaviour is
	// deterministic.
	//
	// phase1MinBootstrapKitHRs lets tests with a small fake cluster
	// (3-5 HRs) still exercise the all-done termination path without
	// having to seed the canonical 11-component bootstrap-kit. Zero
	// means "fall back to env var → DefaultMinBootstrapKitHRs",
	// non-zero means "use this value". phase1FirstSeenTimeout works
	// the same way for the first-seen warn window.
	dynamicFactory           func(string) (dynamic.Interface, error)
	coreFactory              func(string) (kubernetes.Interface, error)
	phase1WatchTimeout       time.Duration
	phase1WatchResync        time.Duration
	phase1MinBootstrapKitHRs int
	phase1FirstSeenTimeout   time.Duration

	// phase1LatePollTimeout / phase1LatePollInterval — test-only
	// overrides for the eventual-consistency late-poll window
	// (issue #910). Zero means "fall back to env var →
	// DefaultLatePollTimeout / DefaultLatePollInterval"; tests inject
	// tiny values (e.g. 200ms / 50ms) so the late-poll path can be
	// exercised in milliseconds.
	phase1LatePollTimeout  time.Duration
	phase1LatePollInterval time.Duration

	// phase1Reachability — test-only override for the pre-flight
	// apiserver reachability probe (issue #923). Production uses
	// helmwatch.NewReachabilityProbeFromKubeconfig (discovery client
	// against /version with rest.Config.Timeout = 30s). Tests inject
	// a closure that returns N transient errors then nil so the
	// reconnect path can be exercised without standing up an
	// apiserver. Zero / nil = "use production default".
	//
	// phase1ReachabilityBudget / phase1ReachabilitySleep / probe
	// timing knobs are the test-friendly overrides for the same
	// loop. Production reads
	// CATALYST_PHASE1_REACHABILITY_BUDGET on every Pod start and
	// passes the parsed Duration into Config.ReachabilityOverallBudget;
	// the per-attempt + backoff knobs default to the helmwatch
	// constants and tests inject tiny values for fast runtimes.
	phase1Reachability             func(kubeconfigYAML string) func(ctx context.Context) error
	phase1ReachabilityBudget       time.Duration
	phase1ReachabilitySleep        func(ctx context.Context, d time.Duration)
	phase1ReachabilityProbeTimeout time.Duration
	phase1ReachabilityRetryInitial time.Duration
	phase1ReachabilityRetryMax     time.Duration

	// kubeconfigArrivalTimeout / kubeconfigArrivalPollInterval —
	// runtime knobs for the polling loop that waits for cloud-init
	// to PUT the new Sovereign's kubeconfig. Zero falls back to the
	// env var → DefaultKubeconfigArrivalTimeout /
	// DefaultKubeconfigArrivalPollInterval. Tests inject tiny
	// values (e.g. 200ms / 50ms) so the kubeconfig-arrival path
	// can be exercised in milliseconds. Issue #538.
	kubeconfigArrivalTimeout      time.Duration
	kubeconfigArrivalPollInterval time.Duration

	// handoverCertWaitTimeout / handoverCertPollInterval — runtime knobs
	// for the loop that waits for the sovereign-wildcard-tls Certificate
	// Ready=True before the handover auto-fire mints the JWT. Zero falls
	// back to the env var (CATALYST_HANDOVER_CERT_WAIT_TIMEOUT /
	// CATALYST_HANDOVER_CERT_POLL_INTERVAL) → DefaultHandoverCertWaitTimeout
	// / DefaultHandoverCertPollInterval. Tests inject tiny values
	// (e.g. 200ms / 20ms) so the cert-wait path can be exercised
	// deterministically. Issue #780.
	handoverCertWaitTimeout  time.Duration
	handoverCertPollInterval time.Duration

	// refreshWatchSeedTimeout — bound on how long
	// POST /api/v1/deployments/{id}/refresh-watch blocks waiting for
	// the bridge seed hook to fire. Zero falls back to
	// defaultRefreshWatchSeedTimeout (30s). Tests inject a tiny value
	// (e.g. 200ms) to exercise the 504-on-seed-timeout path
	// deterministically against a stuck fake informer.
	refreshWatchSeedTimeout time.Duration

	// wipeMinLifeProtection — issue #914. Minimum age below which a
	// POST /api/v1/deployments/{id}/wipe call is REJECTED with 409 when
	// the deployment is still in `phase1-watching` (i.e. mid-converge).
	// The operator can override with `?force=true` query param. Zero
	// falls back to env var (CATALYST_WIPE_MIN_LIFE_PROTECTION) →
	// DefaultWipeMinLifeProtection (30m). Tests inject a tiny value
	// (e.g. 100ms) to exercise the protection path in milliseconds.
	//
	// The threshold is sized vs the Phase-1 watcher's overall budget
	// (DefaultWatchTimeout = 60m) so a deployment that's still
	// converging gets at least half the watch budget before any
	// external wipe can destroy it. otech106 incident, 2026-05-05:
	// an external POST /wipe at T+24m killed a still-converging
	// 28/40-installed Sovereign because no minimum-life guard
	// existed.
	wipeMinLifeProtection time.Duration

	// k8sCache — catalyst-wide informer cache (issue #321). Owns one
	// SharedInformerFactory per managed Sovereign cluster. The k8s
	// REST + SSE handlers in k8s.go read its Indexer + Subscribe.
	// Nil-tolerant: a nil k8sCache yields 503 from the k8s.go
	// handlers so existing tests (NewWithPDM / Handler{}) keep
	// working unchanged.
	k8sCache *k8scache.Factory

	// sarCache — per-process SubjectAccessReview decision cache that
	// gates every K8s read. Decisions live for ~30s (configurable via
	// CATALYST_K8SCACHE_SAR_TTL_SECONDS).
	sarCache *k8scache.SARCache

	// k8sUserHeader — request header that carries the authenticated
	// user identity. Production deploys put an OIDC proxy in front
	// of catalyst-api which sets X-Forwarded-User. Tests inject
	// directly via this field.
	k8sUserHeader string

	// openbao — KV-v2 client the handover-archive receiver endpoint
	// (issue #317) uses to seal `secret/catalyst/tofu-phase0-archive`
	// on a Sovereign cluster. Nil on Catalyst-Zero — the receiver
	// then returns 503 ("not handover target"). Production main.go
	// constructs this from CATALYST_OPENBAO_ADDR +
	// CATALYST_OPENBAO_TOKEN; tests inject via SetOpenBao.
	openbao *openbao.Client

	// handoverTargetURL — override for the URL FinaliseHandover POSTs
	// the Tofu archive to. Empty in production; the handler falls
	// back to https://api.<sovereignFQDN>/api/v1/handover/tofu-archive.
	// Tests set this to a httptest.Server URL.
	handoverTargetURL string

	// handoverHTTPClient — override for the HTTP client used by the
	// finalise handler. Empty in production; falls back to a 60s-timeout
	// http.Client. Tests inject a client that points at a test server.
	handoverHTTPClient *http.Client

	// ── auth config (issue #608, Phase-8b Agent A) ──────────────────────────
	// authConfig — Keycloak OIDC config for the Catalyst-Zero wizard session.
	// Nil when CATALYST_KC_ADDR is not set (Sovereign-side, CI).
	authConfig *auth.Config

	// ── handover JWT signer (issue #605, Phase-8b Agent B) ──────────────────
	// handoverSigner — RSA-2048 signer that mints the one-time JWT for the
	// seamless single-identity redirect:
	//   POST /api/v1/deployments/{id}/mint-handover-token
	//   GET  /api/v1/handover/public-key
	// Nil when CATALYST_HANDOVER_KEY_PATH is unset (Sovereign-side).
	handoverSigner *handoverjwt.Signer

	// ── /auth/handover fields (issue #606, Phase-8b Agent C) ─────────────────
	// kc — Keycloak client for the /auth/handover endpoint (Sovereign realm).
	kc keycloakClient
	// jtiStore — single-use JTI store for /auth/handover replay protection.
	jtiStore jtiStorer
	// handoverJWTPublicKeyPath — path to the RS256 public key JWK file.
	handoverJWTPublicKeyPath string
	// authHandoverSovereignFQDN — test override for SOVEREIGN_FQDN env.
	authHandoverSovereignFQDN string
	// kcAudience — OIDC client id for token-exchange. Defaults to "catalyst-ui".
	kcAudience string
	// authHandoverRedirect — post-handover redirect target. Defaults to "/console/dashboard".
	authHandoverRedirect string

	// ── /auth/pin fields (issue #688, replaces magic-link flow) ─────────────
	// openovaKC — Keycloak client for the openova realm PIN flow.
	// Separate from kc (Sovereign realm) — different realm, different SA client.
	// Nil when CATALYST_OPENOVA_KC_SA_CLIENT_SECRET is not set (Sovereign-side, CI).
	openovaKC keycloakClient
	// pinStore — in-memory store for outstanding 6-digit PINs. Lazy-wired
	// in pinStoreFor(); tests inject a deterministic instance directly.
	// PINs are credentials and per docs/INVIOLABLE-PRINCIPLES.md #10 are
	// NEVER persisted to disk — a Pod restart invalidates every code.
	pinStore *pinStore

	// ── Self-Sovereignty Cutover (issue #792) ───────────────────────────────
	// cutoverBus — in-process broadcaster that fans cutover state-change
	// events to every active SSE subscriber. Lazy-wired in cutoverBusFor();
	// tests inject a deterministic instance via SetCutoverBroadcaster.
	cutoverBus     *cutoverBroadcaster
	cutoverBusOnce sync.Once
	// cutoverDepsFactory — test-only override that builds the in-cluster
	// kubernetes.Interface + namespace pair the cutover engine reads
	// from. Production leaves this nil and cutoverDepsFromEnv runs.
	cutoverDepsFactory CutoverDepsFactory

	// ── Unified RBAC SME-tier (issue #802, ADR-0003) ────────────────────────
	// tenantRegistry — host → tenant lookup table backing the public
	// /api/v1/tenant/discover endpoint. Same registry is used by the
	// SME user endpoints to scope every operation to the calling tenant.
	tenantRegistry *store.TenantRegistry
	// smeDeps — bundle of dependencies for the ADR-0003 user-create hook
	// (Keycloak admin client, NewAPI client, K8s Secret applier, NATS
	// emitter, base-URL template). Wired from main.go at startup; tests
	// inject stubs.
	smeDeps SMEDeps

	// ── SME tenant provisioning pipeline (issue #804) ───────────────────────
	// smeTenantDeps — bundle of dependencies for the SME tenant
	// provisioning pipeline (state-machine store, GitOps overlay
	// writer, DNS provisioner, Keycloak client provisioner, NATS
	// emitter, OTECH FQDN). Wired from main.go at startup; tests
	// inject stubs.
	smeTenantDeps SMETenantDeps

	// ── Sovereign SMTP seed (issue #883) ────────────────────────────────────
	// sovereignSMTPSeedClientFactory — test-only override for building a
	// kubernetes.Interface from a kubeconfig YAML. Production wires
	// helmwatch.NewKubernetesClientFromKubeconfig. Tests inject a
	// closure returning a fake.NewSimpleClientset so the seed path is
	// exercised without standing up a real cluster.
	sovereignSMTPSeedClientFactory SovereignSMTPSeedClientFactory

	// ── Multi-zone PowerDNS (issue #827, parent epic #825) ──────────────────
	// powerdnsZoneClient — narrow client for runtime parent-zone creation.
	// The bootstrap-kit's bp-powerdns Helm hook Job creates the operator's
	// initial parent zones at install time; this client is the catalyst-api
	// side of the SAME contract for runtime zone additions via the admin
	// console "Add another parent domain" flow (#829).
	//
	// Nil-tolerant: when CATALYST_POWERDNS_API_URL +
	// CATALYST_POWERDNS_API_KEY are unset (local dev, CI), the parent-zone
	// handler returns 503 "powerdns client not wired" so callers can
	// distinguish "config gap" from "powerdns down".
	powerdnsZoneClient powerdnsZoneClient

	// ── Sovereign Console populated views (issue #933) ─────────────────
	// sovereignDepsFactory — test-only override that builds the
	// in-cluster (kubernetes.Interface, dynamic.Interface) pair the
	// /api/v1/sovereign/{status,jobs,apps,cloud} endpoints read.
	// Production leaves this nil and sovereignDepsFromEnv runs against
	// rest.InClusterConfig() — catalyst-api always runs in the cluster
	// it serves (mothership AND post-handover Sovereign).
	sovereignDepsFactory SovereignDepsFactory

	// ── Compliance score aggregator (EPIC-1 #1096 slice S) ─────────
	// compliance — joins PolicyReport + ClusterPolicyReport +
	// compliance-evaluator events into per-resource +
	// per-Application + per-Org + per-Sovereign weighted scores.
	// Nil-tolerant: when nil the /api/v1/sovereigns/{id}/compliance/*
	// endpoints return 503 ("compliance handler not wired") so the
	// UI can render the "compliance disabled" empty state. Wired
	// from main.go at startup AFTER k8sCache is up; tests set this
	// directly via SetComplianceHandler.
	compliance *ComplianceHandler

	// ── Live install flow (EPIC-2 #1097 slice I) ───────────────────
	// catalogClient — REST client to catalyst-catalog (slice L,
	// #1148). The install + preview handlers use this to fetch
	// Blueprint definitions before validating parameters and
	// creating Application CRs. Nil-tolerant: when nil the
	// /api/v1/sovereigns/{id}/applications endpoints return 503
	// ("catalog client not wired"). Wired from main.go at startup
	// from CATALYST_CATALOG_URL; tests inject a stub via
	// SetCatalogClient.
	catalogClient CatalogClient
}

// powerdnsZoneClient is the narrow interface the parent-zone handler
// needs from internal/powerdns.Client. Defined as an interface (rather
// than a *powerdns.Client field) so tests can inject a stub without
// pulling httptest into every test file. The production binding is
// SetPowerDNSZoneClient(powerdns.New(...)) from main.go.
type powerdnsZoneClient interface {
	CreateZone(ctx context.Context, spec powerdns.ZoneSpec) error
	ZoneExists(ctx context.Context, name string) (bool, error)
}

// defaultDeploymentsDir is the on-PVC path the chart mounts. A separate
// env var (`CATALYST_DEPLOYMENTS_DIR`) overrides it per
// docs/INVIOLABLE-PRINCIPLES.md #4 — the path is configuration, not code.
const defaultDeploymentsDir = "/var/lib/catalyst/deployments"

// defaultKubeconfigsDir — sister directory to defaultDeploymentsDir
// on the same PVC. Holds <id>.yaml plaintext kubeconfigs the new
// Sovereign POSTs back via the bearer-token endpoint (issue #183).
// Overridable via CATALYST_KUBECONFIGS_DIR.
const defaultKubeconfigsDir = "/var/lib/catalyst/kubeconfigs"

// New creates a Handler with the runtime configuration loaded from env.
//
// POOL_DOMAIN_MANAGER_URL — defaults to the in-cluster service FQDN. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 the URL is configuration, not code; an
// air-gapped install can override it to point at the operator's own
// PDM endpoint.
//
// CATALYST_DEPLOYMENTS_DIR — directory the deployment store persists to.
// Defaults to /var/lib/catalyst/deployments (the chart's PVC mount).
// If the directory cannot be created or is not writable (e.g. a CI
// environment without root, or a misconfigured PVC), New logs a warning
// and continues with an in-memory-only Handler. Production failures
// surface via the readinessProbe + a startup error log; CI tests
// running as a non-root user with a read-only / will exercise the
// in-memory fallback path so the load tests don't need a writable
// /var/lib.
func New(log *slog.Logger) *Handler {
	pdmURL := os.Getenv("POOL_DOMAIN_MANAGER_URL")
	if pdmURL == "" {
		pdmURL = "http://pool-domain-manager.openova-system.svc.cluster.local:8080"
	}

	dir := os.Getenv("CATALYST_DEPLOYMENTS_DIR")
	if dir == "" {
		dir = defaultDeploymentsDir
	}

	kubeconfigsDir := os.Getenv("CATALYST_KUBECONFIGS_DIR")
	if kubeconfigsDir == "" {
		kubeconfigsDir = defaultKubeconfigsDir
	}
	// Pre-create the kubeconfigs dir at 0o700 so the PUT handler
	// doesn't have to MkdirAll on every request. A failure here is
	// non-fatal — production will surface it via the PUT handler's
	// 503 path; a CI runner without write access falls back to the
	// in-memory-only Handler the same way the deployment store
	// does.
	if err := os.MkdirAll(kubeconfigsDir, 0o700); err != nil {
		log.Warn("kubeconfigs directory create failed; PUT /kubeconfig will return 503 until resolved",
			"dir", kubeconfigsDir,
			"err", err,
		)
		kubeconfigsDir = ""
	}

	h := &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              pdm.New(pdmURL),
		kubeconfigsDir:   kubeconfigsDir,
	}

	st, err := store.New(dir)
	if err != nil {
		// Persistence is a hard requirement in production but a CI
		// runner without write access to /var/lib would fail at
		// import-time without this fallback. We log the failure with
		// enough detail that an operator can tell whether this is the
		// "PVC missing" case (must fix) or the "test environment"
		// case (expected).
		log.Warn("deployment store unavailable; running with in-memory state only — restarts will lose deployments",
			"dir", dir,
			"err", err,
		)
	} else {
		h.store = st
		// Restore on startup. Failed records are logged but do not
		// abort the handler — a single corrupt file must not prevent
		// every other in-flight deployment from rehydrating.
		h.restoreFromStore()
	}

	// Jobs/Executions store (issue #205) — same PVC mount, different
	// subdirectory. CATALYST_EXECUTIONS_DIR overrides the default per
	// docs/INVIOLABLE-PRINCIPLES.md #4. A failure to create the store
	// is logged + non-fatal, mirroring the deployment store's CI
	// fallback path.
	jobsDir := os.Getenv(jobs.EnvDir)
	if jobsDir == "" {
		jobsDir = jobs.DefaultDir
	}
	js, err := jobs.NewStore(jobsDir)
	if err != nil {
		log.Warn("jobs store unavailable; jobs/executions API will return 503",
			"dir", jobsDir,
			"err", err,
		)
	} else {
		h.jobs = js
	}

	return h
}

// NewWithPDM is exposed for tests; production code uses New.
//
// Tests get an in-memory-only Handler (store = nil) so they don't have
// to manage a temp directory unless they're specifically exercising
// persistence. Persistence-aware tests use NewWithStore below.
func NewWithPDM(log *slog.Logger, client pdmClient) *Handler {
	return &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              client,
	}
}

// NewWithStore wires both PDM and the deployment store. Used by the
// persistence test suite to point the store at a t.TempDir() and prove
// the round-trip across simulated Pod restarts.
func NewWithStore(log *slog.Logger, client pdmClient, st *store.Store) *Handler {
	h := &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              client,
		store:            st,
	}
	if st != nil {
		h.restoreFromStore()
	}
	return h
}

// NewWithJobsStore is a test-only constructor that wires an in-memory
// Handler with a jobs.Store rooted at a t.TempDir(). The 4 jobs HTTP
// handlers can be exercised end-to-end against this construction
// without standing up the full deployment store / PDM stack.
func NewWithJobsStore(log *slog.Logger, js *jobs.Store) *Handler {
	return &Handler{
		log:  log,
		jobs: js,
	}
}

// NewWithStoreAndKubeconfigsDir is the persistence-aware constructor
// the issue-#183 test suite uses to point both the deployment store
// AND the kubeconfigs directory at t.TempDir() subdirectories. The
// production New() reads both paths from env vars and is equivalent;
// this constructor exists so tests can prove the PUT handler writes
// to the right place without touching the global env.
func NewWithStoreAndKubeconfigsDir(log *slog.Logger, client pdmClient, st *store.Store, kubeconfigsDir string) *Handler {
	h := &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              client,
		store:            st,
		kubeconfigsDir:   kubeconfigsDir,
	}
	if kubeconfigsDir != "" {
		_ = os.MkdirAll(kubeconfigsDir, 0o700)
	}
	if st != nil {
		h.restoreFromStore()
	}
	return h
}

// SetK8sCache wires a started k8scache.Factory + a SAR cache into the
// Handler. The catalyst-api `main.go` calls this once at startup,
// AFTER the Factory's Start has been invoked. The k8sUserHeader is
// the request header the SSE/REST handlers read for the authenticated
// user identity (default X-Forwarded-User).
func (h *Handler) SetK8sCache(f *k8scache.Factory, sar *k8scache.SARCache, userHeader string) {
	h.k8sCache = f
	h.sarCache = sar
	h.k8sUserHeader = userHeader
}

// K8sCache exposes the wired Factory so /healthz can read its synced
// map without reaching into private state.
func (h *Handler) K8sCache() *k8scache.Factory { return h.k8sCache }

// GetAuthConfig returns the wired auth.Config. Used by main.go to pass to
// auth.RequireSession middleware. Returns nil when KC is unconfigured.
func (h *Handler) GetAuthConfig() *auth.Config { return h.authConfig }

// SetAuthConfig wires an auth.Config into the Handler.
func (h *Handler) SetAuthConfig(cfg *auth.Config) { h.authConfig = cfg }

// GetHandoverSigner returns the wired handoverjwt.Signer (or nil if unset).
// Used by main.go to wire the public key into auth.Config.LocalPublicKey
// so the session middleware can validate self-signed session JWTs.
func (h *Handler) GetHandoverSigner() *handoverjwt.Signer { return h.handoverSigner }

// SetOpenovaKC wires the openova-realm Keycloak client (PIN auth, #688).
// Called by main.go at startup when CATALYST_OPENOVA_KC_SA_CLIENT_SECRET is set.
func (h *Handler) SetOpenovaKC(kc keycloakClient) { h.openovaKC = kc }

// SetPowerDNSZoneClient wires the multi-zone PowerDNS client used by the
// runtime add-parent-zone endpoint (issue #827). Called by main.go at
// startup when CATALYST_POWERDNS_API_URL + CATALYST_POWERDNS_API_KEY are
// set. Tests inject a stub directly via this setter.
func (h *Handler) SetPowerDNSZoneClient(c powerdnsZoneClient) { h.powerdnsZoneClient = c }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
