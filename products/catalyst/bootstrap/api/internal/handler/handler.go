// Package handler holds shared state for all HTTP handlers.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/flowemit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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

	// clusterMeshLoopActive guards against double-starting the establish +
	// steady-state loop for the same deployment (#4811). runAutoEstablishClusterMesh
	// LoadOrStores the dep ID on entry and Deletes it on return, so a second
	// concurrent trigger — the startup retry racing markPhase1Done /
	// converged-late, or two restore passes — becomes a cheap no-op instead of
	// two competing loops thrashing the same idempotent Secret writes. Cleared
	// on return so a later legitimate re-trigger (next catalyst-api roll) still
	// runs. map[string]struct{}.
	clusterMeshLoopActive sync.Map

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

	// flowEmit — HTTP client that POSTs FlowMessage envelopes into
	// openova-flow-server's CNPG-backed store. Replaces the in-process
	// snapshot composition from flow_snapshot_local.go. The emit path
	// is fire-and-forget with bounded retry; an emit failure does not
	// block the helmwatch event flow. Nil-tolerant: when
	// OPENOVA_FLOW_SERVER_URL is empty the client is a no-op (every
	// method returns nil), matching test/dev fixtures.
	flowEmit *flowemit.Client

	// flowEmitMu + flowEmitters — per-deployment background goroutines
	// that compose snapshots from jobs.Store and POST them to
	// openova-flow-server every defaultEmitInterval (5s). State changes
	// can trigger out-of-cycle emits via triggerFlowEmit(). See
	// flow_emitter.go for the loop body.
	flowEmitMu   sync.Mutex
	flowEmitters map[string]*flowEmitter

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

	// phase1WatchWG — test-only WaitGroup that the background
	// phase1-watch goroutine launched by PutKubeconfig joins (Add on
	// the request goroutine before `go`, Done when the goroutine fully
	// unwinds). Nil in production (New leaves it zero) so the goroutine
	// stays fire-and-forget. Tests set it to a &sync.WaitGroup{} and
	// Wait on it in a t.Cleanup so the goroutine's async
	// markPhase1Done → persistDeployment write can never race the
	// testing package's t.TempDir() RemoveAll cleanup ("directory not
	// empty"). See makePutFixture in kubeconfig_test.go.
	phase1WatchWG *sync.WaitGroup

	// suppressPostHandoverHooks — test-only gate. markPhase1Done, at its
	// OutcomeReady/OutcomeTimeout terminal, spawns fire-and-forget day-2
	// goroutines on context.Background() (runAutoEstablishClusterMesh +
	// the runPostHandover* hooks). They carry 15-min / 6-hour budgets, so
	// in a store+kubeconfigsDir fixture backed by t.TempDir() they outlive
	// the test body and their emitWatchEvent → recordEventAndPersist write
	// (or a spine-seam read) races Go's testing RemoveAll cleanup
	// ("TempDir RemoveAll cleanup: directory not empty") / trips -race.
	// phase1WatchWG only awaits runPhase1Watch's OWN return, not these
	// descendant goroutines. Nil/false in production (New leaves it zero)
	// so the hooks always fire on a real Sovereign; the phase1-watch
	// fixtures set it true so no goroutine outlives the test. The hooks
	// keep their own dedicated coverage in post_handover_*_test.go /
	// clustermesh_test.go. See makePutFixture in kubeconfig_test.go.
	suppressPostHandoverHooks bool

	// phase1ReadySentinel is the component id (bp- prefix stripped) whose
	// terminal-install gates OutcomeReady (#4746). The PRIMARY watch refuses
	// to classify a prov "ready" until this component (the console's own
	// backend, catalyst-platform) has been observed — closing the narrow-
	// census false-"failed" on slow 2-region provs where an early HR reaches
	// Ready long before the console backend even exists. The zero value ("")
	// DISABLES the gate: production New() sets it to
	// helmwatch.DefaultReadySentinelComponent, while unit tests build
	// &Handler{} inline and leave it empty so a small fake kit (no
	// catalyst-platform) still exercises the historical terminate path.
	// Operators override at runtime with CATALYST_PHASE1_READY_SENTINEL.
	phase1ReadySentinel string

	// consoleProbe is the external console-reachability gate applied before
	// markPhase1Done flips a converged deployment to "ready" (#4706). NIL =
	// gate OFF (the pre-#4706 behaviour): production turns it on by calling
	// EnableConsoleReachabilityGate() at startup; unit tests leave it nil so
	// they never touch the network, and set a stub here directly when they
	// want to assert the gate (reachable → ready, down → failed).
	consoleProbe func(fqdn string) error

	// phase1LatePollTimeout / phase1LatePollInterval — test-only
	// overrides for the eventual-consistency late-poll window
	// (issue #910). Zero means "fall back to env var →
	// DefaultLatePollTimeout / DefaultLatePollInterval"; tests inject
	// tiny values (e.g. 200ms / 50ms) so the late-poll path can be
	// exercised in milliseconds.
	phase1LatePollTimeout  time.Duration
	phase1LatePollInterval time.Duration

	// phase1RecensusInterval — test-only override for the informer-
	// independent periodic re-census cadence (#5269). Zero means "fall
	// back to env var → helmwatch.DefaultRecensusInterval (45s)";
	// tests inject a tiny value (e.g. 50ms) so the ticker-driven
	// conclusion path can be exercised in milliseconds. Production
	// reads CATALYST_PHASE1_RECENSUS_INTERVAL on every Pod start.
	phase1RecensusInterval time.Duration

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

	// fluxCRDProbeBudget / fluxCRDProbePollInterval — runtime knobs for
	// the bounded Flux-CRD-presence probe runPhase1Watch runs AFTER the
	// kubeconfig lands and BEFORE the helmwatch informer is built (#5042).
	// The probe classifies OutcomeFluxCRDsAbsent when the new Sovereign is
	// healthy (kubeconfig PUT, CNI up) but cloud-init's flux-install stage
	// never installed the helmreleases.helm.toolkit.fluxcd.io CRD — so the
	// deployment fails fast with an actionable diagnostic instead of idling
	// "phase1-watching" to the 120m WatchTimeout. Zero falls back to the
	// env var → DefaultFluxCRDProbeBudget / DefaultFluxCRDProbePollInterval.
	// Tests inject tiny values (e.g. 200ms / 50ms) plus a fake dynamic
	// client so the probe path is exercised in milliseconds.
	fluxCRDProbeBudget       time.Duration
	fluxCRDProbePollInterval time.Duration

	// phase1StorageGate — test-only override for the #3971 default-
	// StorageClass durability gate run at the end of markPhase1Done.
	// Production uses helmwatch.DefaultStorageClassFromKubeconfig
	// (typed client against the new Sovereign's kubeconfig, rest.Config
	// 30s timeout, read-only StorageClass List). Tests inject a closure
	// returning a canned helmwatch.DefaultStorageClassInfo so the gate's
	// ready→failed flip can be exercised without a real cluster. Nil =
	// "use production default". When the kubeconfig is unreadable the
	// gate is SKIPPED (never downgrades a ready deployment on probe
	// error) — the gate only fires on a POSITIVE local-path observation.
	phase1StorageGate func(ctx context.Context, kubeconfigPath string) (helmwatch.DefaultStorageClassInfo, error)

	// ClusterMesh level-triggered reconcile knobs (#3241). The
	// runAutoEstablishClusterMesh wrapper re-runs the idempotent
	// AutoEstablishClusterMesh until every region is fully meshed:
	// exponential backoff from InitialBackoff doubling to MaxBackoff,
	// one attempt bounded by AttemptTimeout, the whole loop bounded by
	// RetryBudget. Zero falls back to the clusterMeshRetry*Default
	// constants in clustermesh.go. Tests inject sub-second values so
	// the retry loop converges in milliseconds; production leaves all
	// four at zero.
	clusterMeshRetryInitialBackoff time.Duration
	clusterMeshRetryMaxBackoff     time.Duration
	clusterMeshRetryBudget         time.Duration
	clusterMeshAttemptTimeout      time.Duration

	// clusterMeshSteadyStateInterval (#3583) — cadence of the
	// post-convergence steady-state heal phase. Once the retry loop
	// converges, runAutoEstablishClusterMesh re-runs the idempotent
	// establish at this interval (on a long-lived ctx, bounded only by the
	// deployment staying status=ready) so a collaterally-deleted
	// replica-auth Secret self-heals. Zero falls back to
	// clusterMeshSteadyStateIntervalDefault. Tests inject a sub-second
	// value so the steady-state pass is exercised in milliseconds;
	// production leaves it at zero.
	clusterMeshSteadyStateInterval time.Duration

	// consumerHubSyncVerify* (#5317) — bounded inner verify-and-re-write
	// budget for syncSharedPGConsumerHubSecrets. After each write of a
	// consumer-hub Secret onto a replica region the sync READS THE SECRET
	// BACK to confirm the bytes actually landed. copySecretAcrossClusters
	// returns success the instant the replica apiserver ACCEPTS the write,
	// but on a fresh 2-region prov the replica's bp-postgres flux
	// Kustomization (owns shared-data with prune=true) can garbage-collect
	// the not-yet-flux-owned Secret straight back out of the namespace during
	// its early reconcile window — the hw283 no-op where 'synced 13' was
	// logged while region-b's shared-data held ZERO consumer secrets and
	// keycloak-0 sat Init:0/1 for 78min on `secret
	// "keycloak-database-secret" not found`. When the read-back shows the
	// Secret absent/divergent the sync re-asserts it up to Attempts times,
	// pacing by Backoff, before deferring the rest to the next
	// level-triggered pass. Zero falls back to the consumerHubSyncVerify*Default
	// constants in clustermesh.go. Tests inject tiny values so the
	// verify-retry runs in microseconds; production leaves both at zero.
	consumerHubSyncVerifyAttempts int
	consumerHubSyncVerifyBackoff  time.Duration

	// clusterMeshStartupRetry* (#4811) — bounded retry for the STARTUP
	// ClusterMesh reconcile when restoreFromStore finds a ready multi-region
	// deployment whose primary kubeconfig was merely UNRESOLVED at restore.
	// restoreFromStore runs inside New(), sometimes milliseconds BEFORE the
	// k8scache dir-load writes the primary kubeconfig file to the PVC, so the
	// resolvePrimaryKubeconfigPath os.Stat misses it and
	// shouldStartupClusterMeshReconcile returns false. The old one-shot dropped
	// the reconcile forever; retryStartupClusterMeshReconcile instead re-checks
	// on Interval and launches runAutoEstablishClusterMesh the first time it
	// passes, giving up after Budget so a genuinely-lost kubeconfig still stops.
	// Zero falls back to the clusterMeshStartupRetry*Default constants in
	// clustermesh.go. Tests inject sub-second values so the self-heal is
	// exercised in milliseconds; production leaves both at zero.
	clusterMeshStartupRetryInterval time.Duration
	clusterMeshStartupRetryBudget   time.Duration

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
	// (DefaultWatchTimeout = 120m post-F8 2026-05-12) so a deployment
	// that's still converging gets multiple watch-budget multiples
	// before any external wipe can destroy it. otech106 incident, 2026-05-05:
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
	// spawnCutoverEngineFn — test-only seam over spawnCutoverEngine so the
	// handover-archive-seal path (ReceiveTofuArchive, issue #933/#3052) can
	// assert the engine is fired exactly once WITHOUT standing up the real
	// engine + cluster. Production leaves this nil and fireCutoverEngine
	// dispatches to the real h.spawnCutoverEngine.
	spawnCutoverEngineFn func(ctx context.Context, deps *cutoverDeps, source string) (cutoverSpawnResult, error)

	// cutoverActivityDepID — test/override of the deployment id the
	// cutover activity bridge projects its step rows under (issue #3646).
	// Empty in production: cutoverActivityBridge() resolves the chroot's
	// imported deployment from the jobs store (the deployment carrying the
	// bootstrap-kit group). Tests set this directly so the projection can
	// be asserted against a known id without standing up the import path.
	cutoverActivityDepID string
	// cutoverActivityProj — lazily-constructed projector that owns the
	// jobs.ActivityBridge translating cutover step transitions into the
	// `Cutover` group of Job rows the canvas reads (issue #3646). See
	// cutover_activity_bridge.go.
	cutoverActivityProj cutoverActivityProjector

	// ── Unified RBAC Organization-tier (issue #802, ADR-0003) ────────────────────────
	// tenantRegistry — host → tenant lookup table backing the public
	// /api/v1/tenant/discover endpoint. Same registry is used by the
	// Organization user endpoints to scope every operation to the calling tenant.
	tenantRegistry *store.TenantRegistry
	// orgDeps — bundle of dependencies for the ADR-0003 user-create hook
	// (Keycloak admin client, NewAPI client, K8s Secret applier, NATS
	// emitter, base-URL template). Wired from main.go at startup; tests
	// inject stubs.
	orgDeps OrgDeps

	// ── Organization tenant provisioning pipeline (issue #804) ───────────────────────
	// orgTenantDeps — bundle of dependencies for the Organization tenant
	// provisioning pipeline (state-machine store, GitOps overlay
	// writer, DNS provisioner, Keycloak client provisioner, NATS
	// emitter, OTECH FQDN). Wired from main.go at startup; tests
	// inject stubs.
	orgTenantDeps OrganizationDeps

	// ── Organization HS256 bridge secret (PR #1625 follow-up) ───────────────────────
	// orgJWTSecret — raw bytes of `org-services-secrets/JWT_SECRET` (mirrored
	// from the `org-services` namespace into catalyst-system by
	// emberstack/reflector — see the annotation block on
	// products/catalyst/chart/templates/org-services/org-services-secrets.yaml).
	// Used by org_billing_vouchers.go's proxyOrgVoucher() (and any
	// future /api/v1/org/* proxy) to mint a short-lived HS256 token
	// the Organization gateway (core/services/gateway/proxy.go) and downstream
	// services (billing / catalog / tenant) will accept — they reject
	// the operator's RS256 Keycloak token outright.
	//
	// Wired from main.go at startup via SetOrgJWTSecret(). Nil-tolerant:
	// when empty (Sovereign without marketplace, or stale chart that
	// hasn't grown the reflector annotation yet), the bridged proxies
	// surface a 503 with a clear `org-jwt-bridge-unwired` error so the
	// FE renders an actionable message rather than the silent 401 the
	// pre-bridge state produced.
	orgJWTSecret []byte

	// ── Sovereign SMTP seed (issue #883) ────────────────────────────────────
	// sovereignSMTPSeedClientFactory — test-only override for building a
	// kubernetes.Interface from a kubeconfig YAML. Production wires
	// helmwatch.NewKubernetesClientFromKubeconfig. Tests inject a
	// closure returning a fake.NewSimpleClientset so the seed path is
	// exercised without standing up a real cluster.
	sovereignSMTPSeedClientFactory SovereignSMTPSeedClientFactory

	// ── NewAPI admin-token OpenBao seed (issue #4477 secondary; ADR-0003) ───
	// newapiAdminTokenSecretReader — test-only override for reading the
	// bridge ADMIN_SECRET from the in-cluster
	// `newapi-bp-newapi-token-signing-key` Secret. Production leaves this
	// nil and seedNewapiAdminToken falls back to
	// inClusterNewapiBridgeAdminSecret (rest.InClusterConfig). Tests inject
	// a closure returning a fixed value (or NotFound) so the OpenBao-write
	// path is exercised without a real apiserver.
	newapiAdminTokenSecretReader NewapiAdminTokenSecretReader

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

	// ── Keycloak admin proxy (EPIC-3 #1098 slice U2/U3/U4) ─────────
	// kcAdminClient — proxy seam the keycloak_proxy.go handlers
	// consume to satisfy U2 (user search), U3 (group browser), U4
	// (role browser). Nil-tolerant: when nil the proxy handlers
	// resolve via h.keycloakClientFor() (auth_handover.go); when both
	// are nil the endpoints return 503 ("keycloak-not-configured").
	// Tests inject a stub directly via SetKCAdminClient — production
	// leaves this nil and lets the lazy resolver in keycloak_proxy.go
	// build the client from env on first call.
	kcAdminClient KeycloakAdminClient

	// ── Blueprint publishing (EPIC-2 #1097 slice T+O+P) ───────────
	// giteaClient — the unified Gitea client used by the
	// /blueprints/publish + /blueprints/curate handlers to write
	// per-Org Blueprints into `<org>/shared-blueprints` and curate
	// into `catalog-sovereign`. Per ADR-0001 §4.3 Gitea is the
	// source-of-truth for Blueprints; this handler is a thin wrapper.
	// Nil-tolerant: when nil the publish + curate endpoints return
	// 503 ("gitea-not-wired") so existing tests keep working.
	// Wired from main.go via NewGiteaClientFromEnv (reads
	// CATALYST_GITEA_URL + CATALYST_GITEA_TOKEN); tests inject a stub
	// directly via SetGiteaClient.
	giteaClient GiteaBlueprintClient

	// ── RBAC audit bus (EPIC-3 #1098 slice U8) ─────────────────────
	// auditBus — in-process ring buffer + SSE fan-out + optional
	// NATS-publisher forwarder. The /audit/rbac list endpoint reads
	// from the ring; the /audit/rbac/stream SSE endpoint subscribes
	// to the fan-out. The rbac_assign.go handler publishes to it on
	// every successful create/update/delete (rbac-grant-* events).
	// Nil-tolerant: when nil the audit endpoints return 503 and the
	// publish-side is a no-op so the rbac_assign hot path never
	// fails because audit isn't wired. Wired from main.go at startup
	// (in-process by default; tests inject a deterministic instance).
	auditBus *audit.Bus

	// ── Tenant-event publisher (TBD-D35b / #1776) ──────────────────
	// tenantEvents — NATS publisher for the `catalyst.tenant.*` event
	// taxonomy (per ADR-0001 §6 + core/services/shared/events/bridge.go
	// CanonicalSubject). The sandbox_sessions.go handler publishes to
	// `catalyst.tenant.sandbox_requested` after every successful
	// Sandbox CR Create. Nil-tolerant: when nil the publish-side is a
	// no-op so the Sandbox-create hot path never fails because NATS
	// isn't wired. Wired from main.go at startup when
	// CATALYST_NATS_URL is set; tests inject a recorder via
	// SetTenantEventPublisher.
	tenantEvents TenantEventPublisher

	// ── Continuum DR clock (EPIC-6 #1101 slice U-DR-1) ─────────────
	// continuumClock — test seam for the Continuum switchover/failback
	// handlers. nil ⇒ time.Now. Tests inject a deterministic clock so
	// the audit-event timestamps + spec.switchover.requestedAt are
	// reproducible. continuum.go is the only consumer.
	continuumClock func() time.Time

	// ── Guacamole exec session client (EPIC-4 #1099 slice E) ───────
	// guacamole — narrow client surface for the per-Sovereign Apache
	// Guacamole REST API. Nil-tolerant: when nil the /k8s/exec/.../session
	// endpoint synthesizes an in-memory session (so the chroot Sovereign
	// flow + CI fixture render cleanly) and the /sessions list endpoint
	// reads from that in-memory store. Production wires a real client via
	// SetGuacamoleClient.
	//
	// guacamoleStore — fallback in-memory session ledger used when
	// guacamole is nil. Lazily allocated by ensureInMemoryStore.
	guacamoleMu    sync.RWMutex
	guacamole      GuacamoleClient
	guacamoleStore *inMemoryGuacamoleStore

	// ── Exec WebSocket fallback (EPIC-4 #1099 slice E2) ────────────
	// execStreamFactory — bridge to the apiserver `pods/exec`
	// subresource for the direct-WebSocket fallback used when
	// Guacamole's iframe is blocked. Nil ⇒ /k8s/exec/{...} WebSocket
	// endpoint returns 503. Production wires a real client-go SPDY
	// executor; tests bind a fake duplex pipe.
	execStreamMu      sync.RWMutex
	execStreamFactory ExecStreamFactory

	// ── G117.3 endpoint CRUD + IaC PR pipeline ─────────────────────
	// endpointDeps — bundle of lookup closures + giteapr.Writer factory
	// that the endpoint_handler.go HTTP handlers consume. Nil-tolerant:
	// when WriterFactory is nil the mutation handlers return 503
	// `gitea-writer-unwired`; when DynamicClient is nil they fall back
	// to rest.InClusterConfig(). Tests inject stubs via
	// SetEndpointPrecheckDeps. See products/catalyst/bootstrap/api/
	// internal/handler/endpoint_handler.go for the route registrations
	// and the per-handler contract.
	endpointDeps EndpointPrecheckDeps

	// ── Mimir-backed Pod sparkline (issue #1753, slice G3b) ────────
	// mimirURL — base URL of the bp-mimir (slot 23) query-frontend
	// HTTP API. The Pod metrics endpoint
	// (`/api/v1/sovereigns/{id}/k8s/metrics/pod/{ns}/{name}`) issues
	// a Prometheus `query_range` against this base to build a 5-min
	// sparkline at 5s step (60 buckets). Empty ⇒ the handler falls
	// back to the legacy metrics-server single-point path so the
	// sparkline degrades gracefully on clusters where bp-mimir is
	// disabled (e.g. a stripped-down Catalyst-Zero flavor). Wired
	// from main.go via SetMimirURL from CATALYST_MIMIR_URL env var;
	// tests inject a httptest.Server URL directly.
	//
	// mimirHTTPClient — HTTP client the mimir path uses. Empty ⇒ a
	// shared 5s-timeout http.Client. Tests inject a stub.
	mimirURL        string
	mimirHTTPClient *http.Client
}

// powerdnsZoneClient is the narrow interface the parent-zone handler
// needs from internal/powerdns.Client. Defined as an interface (rather
// than a *powerdns.Client field) so tests can inject a stub without
// pulling httptest into every test file. The production binding is
// SetPowerDNSZoneClient(powerdns.New(...)) from main.go.
type powerdnsZoneClient interface {
	CreateZone(ctx context.Context, spec powerdns.ZoneSpec) error
	ZoneExists(ctx context.Context, name string) (bool, error)
	PatchRRSets(ctx context.Context, zone string, rrsets []powerdns.RRSet) error
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
		// #4746 — arm the phase-1 ready sentinel so the PRIMARY watch cannot
		// declare a prov ready before the console's own backend
		// (catalyst-platform) has converged. Tests build &Handler{} inline
		// and leave this empty (gate off); the env override still applies in
		// phase1WatchConfigForDeployment.
		phase1ReadySentinel: helmwatch.DefaultReadySentinelComponent,
	}

	// #4614: arm the in-band VPC-quota reclaim (provisioner pre-flight) with
	// the SAME live-deployment allowlist the background janitor uses. Without
	// this the reclaim's protect-set holds only the firing prov's own prefix
	// and treats every OTHER live Sovereign's VPC as a reclaimable orphan —
	// the path that cascade-deleted production omantel.biz on 2026-06-28.
	// buildActivePrefixes reads h.deployments at call (reclaim) time, so
	// binding the method here — before restore — is safe.
	provisioner.ActiveDepPrefixesHook = h.buildActivePrefixes

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

	// flowemit — POST events to openova-flow-server's CNPG-backed
	// store. URL from OPENOVA_FLOW_SERVER_URL env (same var the
	// snapshot/stream proxy uses). Empty → no-op client; production
	// chart wires this to http://openova-flow-server.catalyst-system
	// .svc.cluster.local on mothership (Service lives in catalyst-system
	// per bootstrap-kit slot 56 targetNamespace), in-cluster service
	// DNS on Sovereigns.
	h.flowEmit = flowemit.NewClient(os.Getenv("OPENOVA_FLOW_SERVER_URL"), log)

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

	// #5285 — make the terminal-failed quarantine DURABLE across a
	// catalyst-api restart. The QuarantineDeployment call on terminal
	// conclusion is in-memory only; a Pod restart re-scans every lingering
	// kubeconfig on the PVC (a failed env's kubeconfig is kept for the
	// eventual wipe) and would re-establish the 404-flood the running Pod
	// had stopped. Re-derive the quarantine from the persisted
	// Status=="failed" records that restoreFromStore() loaded in New()
	// (which runs before main.go calls SetK8sCache), so a restarted Pod
	// does not resurrect the reflector flood. Only fully-"failed" records
	// quarantine — a "partial-failure" primary's CRDs exist (no flood) and
	// its reflectors power the console view.
	if f != nil {
		h.deployments.Range(func(_, val any) bool {
			dep, ok := val.(*Deployment)
			if !ok || dep == nil {
				return true
			}
			dep.mu.Lock()
			id, failed := dep.ID, dep.Status == "failed"
			dep.mu.Unlock()
			if failed {
				f.QuarantineDeployment(id)
			}
			return true
		})
	}
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

// SetAuditBus wires the RBAC audit Bus (EPIC-3 #1098 slice U8). Called
// by main.go at startup; tests inject a deterministic Bus directly.
// Nil is allowed and disables every audit endpoint (returning 503) and
// makes the rbac_assign emit-side a no-op.
func (h *Handler) SetAuditBus(bus *audit.Bus) { h.auditBus = bus }

// SetOrgJWTSecret wires the raw bytes of `org-services-secrets/JWT_SECRET` so
// the /api/v1/org/* proxies (org_billing_vouchers.go + future siblings)
// can mint a short-lived HS256 bridge token the Organization gateway will
// accept. Empty / nil secret disables the mint path; proxies surface
// 503 `org-jwt-bridge-unwired` rather than forging a 401 upstream.
// Called by main.go at startup from CATALYST_ORG_JWT_SECRET (Pod env
// fed via secretKeyRef from the reflector-mirrored org-services-secrets Secret).
func (h *Handler) SetOrgJWTSecret(secret []byte) { h.orgJWTSecret = secret }

// AuditBus returns the wired Bus or nil. Test helper.
func (h *Handler) AuditBus() *audit.Bus { return h.auditBus }

// SetTenantEventPublisher wires the NATS publisher used by the
// sandbox_sessions.go handler (TBD-D35b / #1776). Called by main.go at
// startup; tests inject a recorder directly. Nil is allowed and turns
// the publish-side into a no-op so the Sandbox-create hot path stays
// resilient when NATS isn't wired (CI, chroot without CATALYST_NATS_URL).
func (h *Handler) SetTenantEventPublisher(p TenantEventPublisher) { h.tenantEvents = p }

// TenantEventPublisherForTest returns the currently wired publisher
// (nil when unset). Exported for tests so the assertion helper can
// recover the recorder without re-importing the handler internals.
func (h *Handler) TenantEventPublisherForTest() TenantEventPublisher { return h.tenantEvents }

// SetMimirURL wires the bp-mimir (slot 23) query-frontend base URL the
// Pod metrics sparkline path uses. Called by main.go at startup from
// CATALYST_MIMIR_URL; tests pass a httptest.Server URL directly.
// Empty disables the mimir path — the handler falls back to the
// metrics-server single-point shape so clusters without bp-mimir
// continue rendering a (degraded) Pod metrics response.
func (h *Handler) SetMimirURL(u string) { h.mimirURL = u }

// SetMimirHTTPClient is a test seam — production leaves this empty and
// the handler uses a shared 5s-timeout http.Client.
func (h *Handler) SetMimirHTTPClient(c *http.Client) { h.mimirHTTPClient = c }

// writeJSON serializes v as JSON with the given status code.
//
// For error responses (code >= 400), the body is enriched at the seam with
// two extra fields — `status` (numeric, e.g. 403) and `code` (string token,
// e.g. "403") — so debuggers, non-Playwright clients, and assertion frameworks
// that scan the body for the literal status code (matrix tests, curl pipes,
// log greps) can find it without parsing HTTP headers separately. This is API
// best-practice: the status code belongs in BOTH the header and the body.
//
// Backward-compat: existing fields (`error`, `detail`, etc.) are preserved
// untouched. Enrichment only adds `status` / `code` when they are not already
// present and only applies to map[string]any / map[string]string / structs.
// Non-object bodies (slices, scalars) are left unchanged — those have no key
// space to enrich without breaking the JSON shape.
//
// 2xx/3xx responses are NEVER enriched (they typically carry domain payloads
// where injecting an HTTP status field would be confusing).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if code >= 400 {
		v = enrichErrorBody(code, v)
	}
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// enrichErrorBody injects `status` (int) and `code` (string) fields into the
// JSON body for error responses, when the body is a map or convertible to one.
// If the body is already a map containing those keys, the original values win
// (caller intent is preserved). Non-object bodies pass through unchanged.
func enrichErrorBody(code int, v any) any {
	codeStr := strconv.Itoa(code)
	switch m := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(m)+2)
		for k, val := range m {
			out[k] = val
		}
		if _, ok := out["status"]; !ok {
			out["status"] = code
		}
		if _, ok := out["code"]; !ok {
			out["code"] = codeStr
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(m)+2)
		for k, val := range m {
			out[k] = val
		}
		if _, ok := out["status"]; !ok {
			out["status"] = code
		}
		if _, ok := out["code"]; !ok {
			out["code"] = codeStr
		}
		return out
	case nil:
		return map[string]any{"status": code, "code": codeStr}
	default:
		// Struct or other shape — round-trip through JSON to inject fields.
		// If marshalling fails or the result is not an object, fall back to
		// the original value (don't break existing contracts).
		raw, err := json.Marshal(v)
		if err != nil {
			return v
		}
		var asMap map[string]any
		if err := json.Unmarshal(raw, &asMap); err != nil || asMap == nil {
			return v
		}
		if _, ok := asMap["status"]; !ok {
			asMap["status"] = code
		}
		if _, ok := asMap["code"]; !ok {
			asMap["code"] = codeStr
		}
		return asMap
	}
}
