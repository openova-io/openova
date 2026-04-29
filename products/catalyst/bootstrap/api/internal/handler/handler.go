// Package handler holds shared state for all HTTP handlers.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
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

	// kubeconfigsDir — directory the cloud-init postback handler
	// (PutKubeconfig, issue #183) writes the new Sovereign's
	// kubeconfig file into, mode 0600 per file. Same PVC as the
	// deployments store but a separate subdirectory so the JSON
	// records and the kubeconfig YAMLs don't co-mingle in one ls.
	// Empty disables the PUT endpoint (returns 503).
	kubeconfigsDir string

	// Phase-1 watch knobs — every field below is a test-only override
	// for internal/helmwatch.Config. Production reads
	// CATALYST_PHASE1_WATCH_TIMEOUT from env and uses
	// helmwatch.NewDynamicClientFromKubeconfig /
	// NewKubernetesClientFromKubeconfig. Tests inject a
	// fake.NewSimpleDynamicClient via dynamicFactory and a tiny
	// timeout via phase1WatchTimeout so termination behaviour is
	// deterministic.
	dynamicFactory     func(string) (dynamic.Interface, error)
	coreFactory        func(string) (kubernetes.Interface, error)
	phase1WatchTimeout time.Duration
	phase1WatchResync  time.Duration
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
