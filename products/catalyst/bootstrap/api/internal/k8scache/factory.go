// factory.go — owns one dynamic SharedInformerFactory per managed
// Sovereign cluster.
//
// Architecture, per ADR-0001 §5:
//
//	Hetzner / cloud provider API
//	        ▲
//	        │ provider-hcloud reconciles every ~30s (Crossplane's job)
//	        │
//	[Crossplane managed-resource CRD]   ←── cloud state lands as a K8s object
//	        │
//	[K8s api-server (etcd + watch cache)]
//	        │ watch stream — long-running HTTP/2, kube-apiserver pushes events
//	        ▼ (sub-second latency, event-driven, no polling)
//	[catalyst-api in-process Indexer (client-go SharedInformerFactory)]
//	        │ event handler fires on every ADDED/MODIFIED/DELETED
//	        ▼
//	[SSE endpoint /api/v1/sovereigns/{id}/k8s/stream]
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (waterfall) — every kind from the registry is watched on every
//	   managed Sovereign at process start. There is no "for now"
//	   subset.
//	#3 (event-driven) — the Factory uses client-go's dynamicinformer
//	   (SharedInformerFactory equivalent for unstructured GVRs); no
//	   time.Tick, no poll loops. Resync is set to 0 so the informer
//	   never re-LISTs on a timer — only on watch reconnect.
//	#4 (never hardcode) — kubeconfig directory, kinds ConfigMap name,
//	   and snapshot directory are all env-overridable.
//
// Concurrency model: Factory.Start() spawns one SharedInformerFactory
// per cluster. Each informer runs its own goroutine that pumps events
// to the registered EventHandler — which delivers a typed Event onto
// the Factory's events channel, fan-out to N SSE subscribers happens
// in subscribers.go.
package k8scache

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/transport"
)

// EventType mirrors cache.DeltaType / watch.EventType. Wire-compatible
// with the SSE encoder.
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
)

// KindComplianceEvaluator is the canonical kind name carried on every
// synthetic PolicyReport-shaped event published by evaluators in
// internal/k8scache/evaluators (EPIC-1 #1096 slice W2). The score
// aggregator (slice S1) subscribes to this kind alongside the upstream
// `policyreport` / `clusterpolicyreport` kinds — a single SSE stream,
// uniform shape.
//
// The synthetic kind is NOT registered as a watch target — it has no
// GVR and the factory never LISTs a real K8s resource for it.
// Subscribers filter on it via the same `?kinds=` query parameter that
// gates pods, services, etc.
const KindComplianceEvaluator = "compliance-evaluator"

// Event is a single K8s state-change notification. Encoded directly
// into the SSE frame — field names are part of the public wire
// contract and must stay stable.
type Event struct {
	// Cluster — Sovereign id this event came from.
	Cluster string `json:"cluster"`
	// Kind — short canonical name from the Kind registry ("pod",
	// "server.hcloud", …). NOT the GVR.
	Kind string `json:"kind"`
	// Type — ADDED / MODIFIED / DELETED.
	Type EventType `json:"type"`
	// Object — the unstructured K8s object (with sensitive fields
	// redacted via redactObject).
	Object *unstructured.Unstructured `json:"object"`
	// At — server-side timestamp the event was recorded.
	At time.Time `json:"at"`
}

// ClusterRef binds a Sovereign id to a kubeconfig source. Production
// loads these from a directory of YAML files; tests inject directly
// via NewFactoryWithClients.
type ClusterRef struct {
	// ID — short Sovereign identifier ("omantel", "primary"). Forms
	// the URL segment in /api/v1/sovereigns/{id}/k8s/...
	ID string

	// KubeconfigPath — absolute path to the kubeconfig YAML on disk.
	// Optional when DynamicClient is provided directly (tests).
	KubeconfigPath string

	// DynamicClient — pre-built dynamic.Interface. When non-nil the
	// factory skips the kubeconfig parse step (used by tests with
	// fake.NewSimpleDynamicClient and by the in-cluster mode where
	// the catalyst-api watches its own cluster via in-cluster config).
	DynamicClient dynamic.Interface

	// CoreClient — typed kubernetes.Interface. Required for the
	// SubjectAccessReview gating path (auth/v1).
	CoreClient kubernetes.Interface
}

// Config — runtime configuration, mostly env-driven. See FactoryFromEnv
// for the env wiring.
type Config struct {
	// Logger — required. Production passes the catalyst-api's slog.
	Logger *slog.Logger

	// Clusters — list of Sovereign clusters to watch. Empty in the
	// "no clusters yet" cold-start case (factory still serves /healthz
	// and the SSE endpoint, just with no events).
	Clusters []ClusterRef

	// Registry — kinds the factory watches on every cluster. Pass
	// the result of LoadRegistry; defaults to DefaultKinds.
	Registry *Registry

	// SnapshotDir — base directory the disk-snapshot path writes to.
	// Per-cluster files are named "{sovereign}-{kind}.json" inside
	// this directory. Empty disables snapshot+hydrate (tests).
	SnapshotDir string

	// SnapshotInterval — how often each (cluster, kind) Indexer is
	// serialised to disk. Defaults to 60s. Tests override to a
	// smaller value for deterministic exercise.
	SnapshotInterval time.Duration

	// SnapshotMaxAge — snapshots older than this are dropped on
	// startup (i.e. the factory does a full LIST instead of
	// hydrating). Defaults to 1h per the issue spec.
	SnapshotMaxAge time.Duration

	// Resync — informer resync period. Defaults to 0 (event-driven
	// only). The hydrate path explicitly seeds the Indexer before
	// the watch starts, so a resync is unnecessary and would only
	// add load. Tests may set a non-zero value to exercise resync
	// metrics.
	Resync time.Duration

	// EventBufferSize — per-Factory event channel buffer. Defaults
	// to 4096; on overrun the producer drops the oldest event and
	// records `informer_event_drops_total`.
	EventBufferSize int

	// KubeconfigsDir — the on-disk directory FactoryFromEnv read at
	// startup. Persisted on the Factory so the periodic rescan loop
	// can re-walk it without re-reading env (and so tests can inject
	// a tmp dir without touching the process env).
	//
	// Empty disables periodic rescan — the factory only watches the
	// clusters passed in Config.Clusters.
	KubeconfigsDir string

	// RescanInterval — how often the rescan loop walks
	// KubeconfigsDir for new kubeconfigs and re-evaluates chroot
	// self-registration (when SOVEREIGN_FQDN was empty at startup).
	// Defaults to 30s. Tests may override to a smaller value.
	//
	// The rescan is robust to the two regressions caught in the
	// 30-row matrix run on t22 (2026-05-18):
	//
	//   (a) catalyst-api Pod restarts after the mothership has
	//       finished POSTing secondary kubeconfigs but BEFORE the
	//       Pod's startup LoadClustersFromDir finishes — without
	//       rescan, every new Pod from then on starts with zero
	//       clusters and the mothership never re-fires the fan-out
	//       (idempotent re-POST only triggers at handover).
	//
	//   (b) the sovereign-fqdn ConfigMap is committed to etcd AFTER
	//       the Pod starts (Helm install race on fresh prov, ~30s
	//       gap observed on t22). The Pod's SOVEREIGN_FQDN env stays
	//       empty for the life of the Pod (Reloader v1.4.16 does not
	//       reload env vars per a longstanding upstream limitation),
	//       so the chroot self-register branch in FactoryFromEnv
	//       returns false and `sovereigns:0`. The rescan loop reads
	//       the on-cluster ConfigMap each tick via the typed client
	//       and self-registers when fqdn is non-empty.
	RescanInterval time.Duration

	// HomeCoreClient — typed kubernetes.Interface for the catalyst-api's
	// own cluster. Required by the rescan loop's chroot self-register
	// recovery path (reads ConfigMap `sovereign-fqdn` in the
	// catalyst-api's own namespace). Optional — when nil the rescan
	// loop only walks the kubeconfigs dir and skips the ConfigMap
	// recovery branch.
	HomeCoreClient kubernetes.Interface
}

// Factory owns the full data-plane: one dynamicinformer factory per
// cluster, the Indexers behind each informer, the snapshot loop, the
// metrics, and the SSE subscriber registry.
type Factory struct {
	cfg      Config
	log      *slog.Logger
	registry *Registry

	// Per-cluster state. Keyed by ClusterRef.ID. Each entry has the
	// dynamicinformer factory, a per-kind informer map, a per-kind
	// Indexer reference (for List paths), and the synced bool the
	// /healthz handler reads.
	mu       sync.RWMutex
	clusters map[string]*clusterState

	// subscribers — fan-out registry for SSE clients. Keyed by an
	// opaque subscriber id; value is a buffered channel the SSE
	// handler reads. Mutated under subMu (separate from mu so a
	// slow SSE client cannot block informer event delivery).
	subMu       sync.Mutex
	subscribers map[int64]*subscriber
	nextSubID   int64

	// stopped fires when Stop is called; the snapshot loop drains
	// it.
	stopOnce sync.Once
	stop     chan struct{}

	// loopWG tracks the Start-spawned background loops (snapshot writer +
	// kubeconfigs rescan) so Stop() can WAIT for them to fully exit — draining
	// any in-flight snapshot write — before returning. Without the wait,
	// Stop() only closed `stop` and returned, so a test's t.TempDir() cleanup
	// (RemoveAll) raced runSnapshotLoop's eager snapshotOnce write →
	// "directory not empty" flake (TestSnapshot_HydrateStaleThenRelist).
	loopWG sync.WaitGroup

	// started + runCtx (#4000) — set once Start() has run. AddCluster
	// consults these so a cluster registered AFTER the factory started
	// (the rescan loop, the secondary-kubeconfig POST handler, the #4001
	// self-heal) has its informers ACTUALLY STARTED and its sync-watcher
	// launched — pre-#4000 AddCluster only BUILT the informers and relied
	// on Start()'s one-time per-cluster `cs.factory.Start`, which had
	// already run, so every runtime-added cluster's informers stayed dead
	// and List() returned no pods. That is the second hw174 root cause:
	// region-b's delivered kubeconfig registered in Clusters() but its
	// informers never synced → placement saw an empty region-b pod list →
	// false `singleton`. Guarded by mu.
	started bool
	runCtx  context.Context
}

type clusterState struct {
	id string
	// kubeconfigPath — the on-disk kubeconfig this cluster was loaded
	// from (empty for chroot self-registered + test-injected clusters
	// that carry a pre-built DynamicClient). #3987: the rescan-prune
	// loop evicts a file-backed cluster once its kubeconfig disappears
	// from KubeconfigsDir — the eviction half of the cross-deployment
	// Nodes leak (a wiped deployment whose .yaml lingered, or whose
	// RemoveCluster ran in-process but the Pod then restarted and the
	// hydrate/rescan path could otherwise resurrect it). Clusters with
	// an empty path are NEVER pruned (the chroot's own in-cluster
	// cluster has no backing file and must survive every rescan).
	kubeconfigPath string
	// factory — the dynamicinformer factory for this cluster. Watches
	// every registered Kind with no per-Kind LIST bound.
	//
	// TBD-V50 (#2125): the separate `boundedFactory` previously created
	// here for HighCardinality kinds (event) was removed because the
	// containment-only approach left the in-process WATCH store growing
	// unbounded — every received event was retained for the lifetime of
	// the Pod, so even with a 5000-row initial-LIST cap the watch keeps
	// accumulating and the Pod still OOM-cycled. The fix is upstream
	// (remove event from DefaultKinds entirely; consumers hit the
	// apiserver directly with Limit + FieldSelector) — see kinds.go.
	// Every Kind still in DefaultKinds has a naturally-bounded population
	// (Pods/Nodes/Services/Deployments/etc.) so a single unbounded factory
	// is correct again.
	factory dynamicinformer.DynamicSharedInformerFactory
	dyn     dynamic.Interface
	core    kubernetes.Interface
	// G95.1 (Refs #2642) — the *rest.Config parsed from this cluster's
	// kubeconfig. Stored so the handler layer can build a SPDY
	// executor against the apiserver's `pods/exec` subresource via
	// remotecommand.NewSPDYExecutor. Without this, the
	// /api/v1/sovereigns/{id}/k8s/exec/... WebSocket handler had no
	// way to dial the apiserver — every call to ExecStreamFactory
	// returned nil → handler returned 503 → browser saw an empty
	// terminal (the "WebSocket opens but renders empty" symptom
	// founder reported). Nil when the cluster was registered with a
	// pre-built DynamicClient (the test path); production
	// kubeconfig-loaded clusters populate it.
	restCfg *rest.Config
	informers     map[string]cache.SharedIndexInformer // keyed by Kind.Name
	indexers      map[string]cache.Indexer             // keyed by Kind.Name
	synced        map[string]bool                      // keyed by Kind.Name
	lastEventAt   map[string]time.Time                 // keyed by Kind.Name
	lastEventLock sync.RWMutex

	// stop — per-cluster shutdown channel passed into
	// dynamicinformer.Factory.Start. Closing it tears down every
	// per-kind informer goroutine for THIS cluster only — a fan-out of
	// the Factory-wide `f.stop` channel that survives until process
	// exit. RemoveCluster closes this channel so a wiped Sovereign's
	// reflectors stop their TLS-loop spam against the destroyed
	// apiserver (issue #156). Idempotent close protected by stopOnce.
	stop     chan struct{}
	stopOnce sync.Once
}

// subscriber — one SSE consumer.
type subscriber struct {
	id    int64
	kinds map[string]struct{} // canonical names; empty == all
	user  string              // for SAR gating; empty == anonymous
	ch    chan Event
}

// LoadRegistry consults the optional ConfigMap referenced by
// CATALYST_K8SCACHE_KINDS_CONFIGMAP and merges it on top of
// DefaultKinds. ConfigMap shape:
//
//	apiVersion: v1
//	kind: ConfigMap
//	metadata:
//	  name: catalyst-k8scache-kinds
//	data:
//	  kinds.txt: |
//	    # name,group,version,resource,namespaced[,sensitive]
//	    helmrelease,helm.toolkit.fluxcd.io,v2,helmreleases,true
//	    kustomization,kustomize.toolkit.fluxcd.io,v1,kustomizations,true
//
// Lines starting with '#' or empty are skipped. core means an empty
// group field. namespaced is "true"/"false". The default registry is
// always included; ConfigMap entries with the same name override the
// default.
//
// When the ConfigMap is absent or unreadable, returns
// NewRegistry().AddAll(DefaultKinds) — never an error. Operators see
// the warning in catalyst-api logs at startup.
func LoadRegistry(ctx context.Context, log *slog.Logger, core kubernetes.Interface) *Registry {
	r := NewRegistry()
	for _, k := range DefaultKinds {
		_ = r.Add(k)
	}

	cmName := os.Getenv("CATALYST_K8SCACHE_KINDS_CONFIGMAP")
	cmNamespace := os.Getenv("CATALYST_K8SCACHE_KINDS_CONFIGMAP_NAMESPACE")
	if cmNamespace == "" {
		cmNamespace = "catalyst"
	}
	if cmName == "" || core == nil {
		return r
	}

	cm, err := core.CoreV1().ConfigMaps(cmNamespace).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		log.Warn("k8scache: kinds ConfigMap unreadable; using default registry only",
			"name", cmName, "namespace", cmNamespace, "err", err)
		return r
	}
	body := firstNonEmpty(cm.Data, "kinds.txt", "kinds")
	if body == "" {
		return r
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			log.Warn("k8scache: malformed kinds ConfigMap line", "line", line)
			continue
		}
		name := strings.TrimSpace(parts[0])
		group := strings.TrimSpace(parts[1])
		version := strings.TrimSpace(parts[2])
		resource := strings.TrimSpace(parts[3])
		namespaced := strings.EqualFold(strings.TrimSpace(parts[4]), "true")
		sensitive := false
		if len(parts) >= 6 {
			sensitive = strings.EqualFold(strings.TrimSpace(parts[5]), "true")
		}
		k := Kind{
			Name: name,
			GVR: schema.GroupVersionResource{
				Group:    group,
				Version:  version,
				Resource: resource,
			},
			Namespaced: namespaced,
			Sensitive:  sensitive,
		}
		if err := r.Add(k); err != nil {
			log.Warn("k8scache: skipping invalid kind", "line", line, "err", err)
		}
	}
	return r
}

// LoadClustersFromDir walks `dir` for *.kubeconfig / *.yaml files and
// constructs ClusterRef entries. Each filename's stem becomes the
// Sovereign id ("omantel.kubeconfig" → "omantel"). Returns the
// (possibly empty) list and a nil error on success; an unreadable
// directory yields nil + the wrapped err.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the directory path is an env
// var (CATALYST_K8SCACHE_KUBECONFIGS_DIR), not a hardcoded path.
//
// The function logs a warning per malformed file and skips it; one
// bad kubeconfig must not prevent the rest from loading.
func LoadClustersFromDir(log *slog.Logger, dir string) ([]ClusterRef, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("k8scache: read kubeconfigs dir %q: %w", dir, err)
	}
	out := make([]ClusterRef, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Accept .kubeconfig, .yaml, .yml — anything else is
		// considered noise.
		ext := filepath.Ext(name)
		switch ext {
		case ".kubeconfig", ".yaml", ".yml":
		default:
			continue
		}
		stem := strings.TrimSuffix(name, ext)
		if stem == "" {
			continue
		}
		path := filepath.Join(dir, name)
		// Validate the kubeconfig now so a malformed file fails
		// loud at boot, not at first watch.
		if _, err := clientcmd.LoadFromFile(path); err != nil {
			log.Warn("k8scache: skipping unparseable kubeconfig",
				"path", path, "err", err)
			continue
		}
		out = append(out, ClusterRef{
			ID:             stem,
			KubeconfigPath: path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// NewFactory constructs a Factory but does NOT start it.
//
// The two-step new-then-start sequence lets the caller register the
// SSE handler, hydrate from disk, and only then begin the watch — so
// the first event the SSE consumer sees corresponds to a real cluster
// state change, not the seed ADD pass.
func NewFactory(cfg Config) (*Factory, error) {
	if cfg.Logger == nil {
		return nil, errors.New("k8scache: Config.Logger is required")
	}
	if cfg.Registry == nil {
		cfg.Registry = NewRegistry()
		for _, k := range DefaultKinds {
			_ = cfg.Registry.Add(k)
		}
	}
	if cfg.SnapshotInterval <= 0 {
		cfg.SnapshotInterval = 60 * time.Second
	}
	if cfg.SnapshotMaxAge <= 0 {
		cfg.SnapshotMaxAge = 1 * time.Hour
	}
	if cfg.EventBufferSize <= 0 {
		cfg.EventBufferSize = 4096
	}
	if cfg.RescanInterval <= 0 {
		cfg.RescanInterval = 30 * time.Second
	}

	f := &Factory{
		cfg:         cfg,
		log:         cfg.Logger,
		registry:    cfg.Registry,
		clusters:    map[string]*clusterState{},
		subscribers: map[int64]*subscriber{},
		stop:        make(chan struct{}),
	}
	for _, c := range cfg.Clusters {
		if err := f.AddCluster(c); err != nil {
			f.log.Warn("k8scache: skipping cluster",
				"id", c.ID, "err", err)
			continue
		}
	}
	return f, nil
}

// AddCluster registers a Sovereign + spawns its informer set. Safe to
// call before or after Start. Idempotent — re-adding a cluster id
// shadows the previous entry.
func (f *Factory) AddCluster(c ClusterRef) error {
	if c.ID == "" {
		return errors.New("k8scache: ClusterRef.ID is required")
	}

	dyn := c.DynamicClient
	core := c.CoreClient
	// G95.1 (Refs #2642) — capture the *rest.Config for this cluster
	// so the handler layer (k8s_exec_ws.go) can build a SPDY executor
	// against the apiserver's pods/exec subresource. Nil on the
	// pre-built-DynamicClient test path.
	var restCfg *rest.Config
	if dyn == nil {
		if c.KubeconfigPath == "" {
			return errors.New("k8scache: ClusterRef requires either DynamicClient or KubeconfigPath")
		}
		raw, err := os.ReadFile(c.KubeconfigPath)
		if err != nil {
			return fmt.Errorf("read kubeconfig %q: %w", c.KubeconfigPath, err)
		}
		restCfg, err = clientcmd.RESTConfigFromKubeConfig(raw)
		if err != nil {
			return fmt.Errorf("parse kubeconfig %q: %w", c.KubeconfigPath, err)
		}
		// Per ADR-0001 the watch is event-driven; client-go default
		// QPS=5/Burst=10 starves on a multi-kind cold LIST. Bump to
		// 50/100 so the per-cluster cold-start completes in seconds,
		// not minutes.
		restCfg.QPS = 50
		restCfg.Burst = 100
		// Skip TLS verify on Sovereign k3s self-signed CAs. See the
		// matching comment in helmwatch/kubeconfig.go for the full
		// rationale — bearer-token auth is preserved, this only
		// affects mothership informers reading Sovereign resource
		// state. Without this, the reflector x509-fails on every
		// fresh prov and the CloudPage resource explorer stays
		// empty.
		restCfg.TLSClientConfig.Insecure = true
		restCfg.TLSClientConfig.CAData = nil
		restCfg.TLSClientConfig.CAFile = ""
		dyn, err = dynamic.NewForConfig(restCfg)
		if err != nil {
			return fmt.Errorf("dynamic client: %w", err)
		}
		if core == nil {
			core, err = kubernetes.NewForConfig(restCfg)
			if err != nil {
				return fmt.Errorf("typed client: %w", err)
			}
		}
	}

	// TBD-V50 (#2125): the separate filtered factory for HighCardinality
	// kinds was removed — see clusterState godoc above and kinds.go for
	// the architectural rationale. Every Kind in DefaultKinds now has a
	// naturally-bounded population, so one unbounded factory is correct.
	cs := &clusterState{
		id:             c.ID,
		kubeconfigPath: c.KubeconfigPath, // #3987 — empty for pre-built-client (chroot/test) clusters; rescan-prune only evicts file-backed clusters whose file disappeared.
		dyn:            dyn,
		core:           core,
		restCfg:        restCfg, // G95.1 (Refs #2642) — nil on pre-built-client test path; production kubeconfig path populates it for exec-stream SPDY dials.
		factory:     dynamicinformer.NewDynamicSharedInformerFactory(dyn, f.cfg.Resync),
		informers:   map[string]cache.SharedIndexInformer{},
		indexers:    map[string]cache.Indexer{},
		synced:      map[string]bool{},
		lastEventAt: map[string]time.Time{},
		stop:        make(chan struct{}),
	}

	// Spawn one informer per registered kind. AddCluster MUST stay
	// synchronous-network-free so a dead Sovereign kubeconfig — e.g. a
	// decommissioned otech whose kubeconfig file was never removed
	// from /var/lib/catalyst/kubeconfigs — cannot block catalyst-api
	// startup.
	//
	// Earlier this site held a discovery probe that gated Optional
	// kinds (metrics.k8s.io/PodMetrics) by calling
	//   core.Discovery().ServerResourcesForGroupVersion(gv)
	// to skip the informer when the GVR wasn't served. That call
	// issues a synchronous REST GET against the cluster's apiserver
	// with NO context timeout. On a kubeconfig pointing at a dead
	// machine the call hangs until the underlying TCP connect times
	// out (often minutes), and AddCluster is on the main startup
	// path — so the contabo mothership boot blocked while iterating
	// dead clusters. Removed.
	for _, k := range f.registry.All() {
		// TBD-V50 (#2125): no more per-Kind routing — every Kind uses the
		// single unbounded factory because every Kind in DefaultKinds has
		// a naturally-bounded population. Events (the only kind that ever
		// grew unbounded) are no longer registered at all; consumers
		// query the apiserver directly. See kinds.go for the architectural
		// rationale.
		inf := cs.factory.ForResource(k.GVR).Informer()
		k := k // capture per-iteration
		_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				f.dispatch(cs, k, EventAdded, obj)
			},
			UpdateFunc: func(_, obj any) {
				f.dispatch(cs, k, EventModified, obj)
			},
			DeleteFunc: func(obj any) {
				if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = d.Obj
				}
				f.dispatch(cs, k, EventDeleted, obj)
			},
		})
		if err != nil {
			f.log.Warn("k8scache: AddEventHandler failed",
				"cluster", c.ID, "kind", k.Name, "err", err)
			continue
		}
		cs.informers[k.Name] = inf
		cs.indexers[k.Name] = inf.GetIndexer()
	}

	f.mu.Lock()
	// If a cluster with the same id was already registered, close its
	// stop channel so its informer goroutines exit before we overwrite
	// the map entry. Without this the previous reflectors would leak —
	// a re-AddCluster after a kubeconfig rotation (or a chroot
	// self-register that races a posted-back kubeconfig) silently
	// stacked another set of informers on top of the dead ones.
	if prev, ok := f.clusters[c.ID]; ok && prev != nil {
		prev.stopOnce.Do(func() { close(prev.stop) })
	}
	f.clusters[c.ID] = cs
	// #4000 — if the factory has already Start()ed, this cluster was added at
	// RUNTIME (rescan loop / secondary-kubeconfig POST / #4001 self-heal). Its
	// informers were BUILT above but are NOT running — Start()'s one-time
	// per-cluster `cs.factory.Start` already ran before this cluster existed.
	// Start them NOW (+ launch the sync-watcher) so List() actually returns
	// this cluster's objects. Pre-#4000 this was missing, so a delivered
	// region-b kubeconfig registered in Clusters() but its informers never
	// synced → the placement resolver saw an empty region-b pod list → every
	// multi-region app collapsed to a false `singleton` (hw174). Clusters
	// added by NewFactory before Start() are left for Start() to launch (the
	// `!started` branch), preserving the new-then-start contract.
	started := f.started
	runCtx := f.runCtx
	f.mu.Unlock()
	if started {
		if runCtx == nil {
			runCtx = context.Background()
		}
		f.startClusterInformers(runCtx, cs)
		f.log.Info("k8scache: cluster registered + informers started (runtime add)",
			"cluster", c.ID, "kinds", len(cs.informers))
		return nil
	}
	f.log.Info("k8scache: cluster registered",
		"cluster", c.ID, "kinds", len(cs.informers))
	return nil
}

// RemoveCluster tears down the per-Sovereign informer factory for
// `id` and removes it from the Factory's cluster map. Safe to call
// from any goroutine and idempotent — calling on an unknown id, or
// twice in succession, is a no-op.
//
// Issue #156 — when a Sovereign is wiped (POST /wipe → tofu destroy +
// Hetzner orphan purge), the cluster's apiserver IP becomes
// unreachable. Without RemoveCluster the per-kind reflectors keep
// retrying with the cached CA bundle and spam catalyst-api logs
// hundreds-per-second with x509: unknown-authority errors against
// the destroyed control plane IP. They also burn CPU and (worst of
// all) hold stale Indexer state that subsequent fresh provisions
// see via the SSE event stream.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (event-driven, no polling):
// stopping is via channel close, not a wait-loop. The
// dynamicinformer factory's Start(stopCh) plumbs the channel down
// into every per-resource Reflector; closing the channel triggers
// the reflector goroutines to return on their next select tick.
//
// Optional snapshot cleanup: when SnapshotDir is configured, drop
// the per-(cluster, kind) snapshot files so a Pod restart after
// the wipe doesn't hydrate from stale disk state.
func (f *Factory) RemoveCluster(id string) {
	if id == "" {
		return
	}
	f.mu.Lock()
	cs, ok := f.clusters[id]
	if ok {
		delete(f.clusters, id)
	}
	f.mu.Unlock()
	if !ok || cs == nil {
		return
	}
	cs.stopOnce.Do(func() { close(cs.stop) })

	if f.cfg.SnapshotDir != "" {
		for kindName := range cs.informers {
			path := snapshotPath(f.cfg.SnapshotDir, id, kindName)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				f.log.Warn("k8scache: snapshot cleanup failed",
					"cluster", id, "kind", kindName, "err", err)
			}
		}
	}
	f.log.Info("k8scache: cluster removed",
		"cluster", id, "kinds", len(cs.informers))
}

// Start kicks off every informer + the snapshot loop. Safe to call
// once; subsequent calls return nil immediately.
//
// Cold-start sequence:
//  1. For each cluster × kind, attempt to hydrate the Indexer from
//     disk if a fresh snapshot exists (see hydrate.go).
//  2. Start every dynamicinformer factory — informers run their own
//     goroutines, LIST + WATCH against the apiserver.
//  3. Spawn the periodic snapshot goroutine.
//  4. Spawn the per-cluster sync-watcher that flips synced[] true
//     once each informer's cache has caught up.
func (f *Factory) Start(ctx context.Context) error {
	// #4000 — record that the factory has started + capture the run ctx so
	// AddCluster can START the informers of any cluster registered AFTER this
	// point (rescan loop / secondary-kubeconfig POST / #4001 self-heal).
	f.mu.Lock()
	f.started = true
	f.runCtx = ctx
	f.mu.Unlock()

	// Hydrate before factory.Start so the watch picks up at the
	// stored resourceVersion (Indexer-seeded). Hydrate failures are
	// logged but never block start — a cold LIST is the fallback.
	if f.cfg.SnapshotDir != "" {
		f.hydrateAll(ctx)
	}

	f.mu.RLock()
	clusters := make([]*clusterState, 0, len(f.clusters))
	for _, cs := range f.clusters {
		clusters = append(clusters, cs)
	}
	f.mu.RUnlock()

	for _, cs := range clusters {
		f.startClusterInformers(ctx, cs)
	}

	if f.cfg.SnapshotDir != "" {
		f.loopWG.Add(1)
		go func() { defer f.loopWG.Done(); f.runSnapshotLoop(ctx) }()
	}

	// Periodic kubeconfigs-dir rescan + chroot self-register recovery.
	// Cheap (one os.ReadDir + at most one ConfigMap GET per tick) and
	// only registers NEW clusters — already-registered IDs short-
	// circuit at AddCluster's idempotent map check.
	//
	// Two regressions this loop fixes (caught on t22 2026-05-18,
	// 30-row matrix rows 9 + 27 — /cloud /list?kind=nodes + dashboard
	// treemap multi-region only showed 1 cluster). See Config.RescanInterval
	// godoc for the full root-cause analysis.
	if f.cfg.KubeconfigsDir != "" || f.cfg.HomeCoreClient != nil {
		f.loopWG.Add(1)
		go func() { defer f.loopWG.Done(); f.runKubeconfigsRescanLoop(ctx) }()
	}

	return nil
}

// startClusterInformers starts cs's dynamicinformer factory (LIST+WATCH
// goroutines), wires the per-cluster stop channel to f.stop, and launches the
// sync-watcher that flips cs.synced[kind] true once each informer's cache has
// caught up. Extracted from Start (#4000) so AddCluster can run the SAME
// startup for a cluster registered AFTER the factory started — the missing
// piece that left every runtime-added cluster's informers dead (region-b on
// hw174). Idempotent per dynamicinformer.Start contract (a second Start on an
// already-running informer is a no-op).
func (f *Factory) startClusterInformers(ctx context.Context, cs *clusterState) {
	// Start with the per-cluster stop channel so RemoveCluster can tear down
	// THIS cluster's informers without affecting others (issue #156).
	// Process-shutdown semantics are preserved by the fan-out goroutine below:
	// when f.stop closes (Factory.Stop), every per-cluster stop channel closes.
	cs.factory.Start(cs.stop)
	go func() {
		select {
		case <-f.stop:
			cs.stopOnce.Do(func() { close(cs.stop) })
		case <-cs.stop:
			// Already removed via RemoveCluster — exit goroutine.
		}
	}()
	go f.runSyncWatcher(ctx, cs)
}

// runKubeconfigsRescanLoop ticks every Config.RescanInterval and:
//
//  1. Walks Config.KubeconfigsDir for new *.kubeconfig/*.yaml/*.yml
//     files and AddCluster for each one whose stem isn't already a
//     registered cluster ID.
//
//  2. When SOVEREIGN_FQDN env was empty at process start (chroot
//     self-register branch in FactoryFromEnv returned false), reads
//     the on-cluster sovereign-fqdn ConfigMap via the typed home
//     client and self-registers the chroot when fqdn is non-empty.
//     Idempotent — re-running on a Pod where chroot is already
//     registered is a no-op.
//
// Exits on ctx.Done or f.stop close.
func (f *Factory) runKubeconfigsRescanLoop(ctx context.Context) {
	interval := f.cfg.RescanInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	f.log.Info("k8scache: kubeconfigs rescan loop started",
		"interval", interval.String(),
		"dir", f.cfg.KubeconfigsDir,
		"chrootSelfRegisterRecovery", f.cfg.HomeCoreClient != nil,
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.stop:
			return
		case <-t.C:
			f.rescanOnce(ctx)
		}
	}
}

// rescanOnce executes one rescan pass. Split from runKubeconfigsRescanLoop
// so tests can drive it deterministically.
func (f *Factory) rescanOnce(ctx context.Context) {
	if dir := f.cfg.KubeconfigsDir; dir != "" {
		refs, err := LoadClustersFromDir(f.log, dir)
		if err != nil {
			f.log.Warn("k8scache: rescan — read kubeconfigs dir failed",
				"dir", dir, "err", err)
		}
		for _, ref := range refs {
			f.mu.RLock()
			_, already := f.clusters[ref.ID]
			f.mu.RUnlock()
			if already {
				continue
			}
			if err := f.AddCluster(ref); err != nil {
				f.log.Warn("k8scache: rescan — AddCluster failed",
					"id", ref.ID, "err", err)
				continue
			}
			f.log.Info("k8scache: rescan — registered new cluster",
				"id", ref.ID, "kubeconfig", ref.KubeconfigPath)
		}

		// #3987 — prune file-backed clusters whose kubeconfig vanished from
		// the dir. A wiped deployment's RemoveCluster runs in-process, but a
		// lingering or re-loaded .yaml (or a Pod restart that re-scanned a
		// stale file) could otherwise keep a dead cluster registered — its
		// Nodes then leak into a sibling deployment's Cloud page through the
		// process cache. Computing the prune set here (rescan tier) is the
		// eviction half of the cross-deployment Nodes leak; the read tier's
		// per-deployment scoping is the other half (handler.deploymentScoped
		// ClusterIDs). Clusters with an empty kubeconfigPath (chroot's own
		// in-cluster cluster, test-injected pre-built clients) are NEVER
		// pruned. RemoveCluster is idempotent + tears down the reflectors.
		for _, id := range f.clustersToPrune() {
			f.log.Info("k8scache: rescan — pruning cluster whose kubeconfig disappeared",
				"id", id)
			f.RemoveCluster(id)
		}
	}

	// Chroot self-register recovery: when the Pod started before the
	// sovereign-fqdn ConfigMap was committed to etcd, SOVEREIGN_FQDN
	// env stays empty for the Pod's lifetime. Read the ConfigMap
	// directly here so a late-arriving fqdn can still self-register.
	if f.cfg.HomeCoreClient == nil {
		return
	}
	fqdn := f.readSovereignFQDNFromConfigMap(ctx)
	if fqdn == "" {
		return
	}
	// Build the chroot ClusterRef as FactoryFromEnv would have. Skip
	// if already registered under the same id.
	ref, ok := buildChrootClusterRef(f.log, fqdn)
	if !ok {
		return
	}
	f.mu.RLock()
	_, already := f.clusters[ref.ID]
	f.mu.RUnlock()
	if already {
		return
	}
	if err := f.AddCluster(ref); err != nil {
		f.log.Warn("k8scache: rescan — chroot self-register AddCluster failed",
			"id", ref.ID, "err", err)
		return
	}
	f.log.Info("k8scache: rescan — chroot self-registered from ConfigMap",
		"id", ref.ID, "sovereignFQDN", fqdn)
}

// clustersToPrune returns the ids of file-backed clusters whose
// kubeconfig file no longer exists on disk. Clusters registered with a
// pre-built DynamicClient (empty kubeconfigPath — the chroot's own
// in-cluster cluster + test-injected clients) are never returned, so
// the prune can never evict a cluster that has no backing file to
// disappear. Pure read of f.clusters under the lock + an os.Stat per
// candidate; the caller (rescanOnce) does the actual RemoveCluster
// outside the lock. #3987.
func (f *Factory) clustersToPrune() []string {
	f.mu.RLock()
	type cand struct{ id, path string }
	cands := make([]cand, 0, len(f.clusters))
	for id, cs := range f.clusters {
		if cs == nil || cs.kubeconfigPath == "" {
			continue
		}
		cands = append(cands, cand{id: id, path: cs.kubeconfigPath})
	}
	f.mu.RUnlock()

	var stale []string
	for _, c := range cands {
		if _, err := os.Stat(c.path); err != nil && os.IsNotExist(err) {
			stale = append(stale, c.id)
		}
	}
	sort.Strings(stale)
	return stale
}

// readSovereignFQDNFromConfigMap returns the non-empty fqdn from the
// sovereign-fqdn ConfigMap in the catalyst-api's own namespace, or "".
// Namespace defaults to "catalyst-system" but is overridable via
// CATALYST_NAMESPACE env (mirrors the chart's release namespace).
//
// Failures are warn-logged at most once per N ticks (silent on
// not-found to keep the contabo mothership log clean — that branch
// never has the ConfigMap and the env is empty by design).
func (f *Factory) readSovereignFQDNFromConfigMap(ctx context.Context) string {
	ns := strings.TrimSpace(os.Getenv("CATALYST_NAMESPACE"))
	if ns == "" {
		ns = "catalyst-system"
	}
	cm, err := f.cfg.HomeCoreClient.CoreV1().ConfigMaps(ns).Get(ctx, "sovereign-fqdn", metav1.GetOptions{})
	if err != nil {
		// not-found is expected on the contabo mothership; suppress.
		return ""
	}
	if cm == nil || cm.Data == nil {
		return ""
	}
	return strings.TrimSpace(cm.Data["fqdn"])
}

// Stop signals every informer + the snapshot loop to exit, then WAITS for
// the Start-spawned background loops to fully drain (any in-flight snapshot
// write completes) before returning. Idempotent. The wait is what makes
// Stop() safe to pair with a test's t.TempDir() cleanup — without it the
// RemoveAll raced runSnapshotLoop's write ("directory not empty").
func (f *Factory) Stop() {
	f.stopOnce.Do(func() { close(f.stop) })
	f.loopWG.Wait()
}

// Synced returns true when at least one informer per cluster has
// completed its initial LIST. Used by /healthz.
func (f *Factory) Synced() map[string]map[string]bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := map[string]map[string]bool{}
	for id, cs := range f.clusters {
		out[id] = map[string]bool{}
		for k, v := range cs.synced {
			out[id][k] = v
		}
	}
	return out
}

// Clusters returns the currently registered Sovereign IDs (sorted).
// The /healthz handler renders this so an operator sees the watch
// surface.
func (f *Factory) Clusters() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.clusters))
	for id := range f.clusters {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Registry exposes the kinds registry so the REST handler can resolve
// path-segment → Kind without re-loading.
func (f *Factory) Registry() *Registry { return f.registry }

// CoreClient returns the typed kubernetes.Interface for the given
// Sovereign cluster id, or nil when the cluster is not registered.
//
// Used by handlers that need to talk to the apiserver beyond the
// informer cache — chiefly EPIC-4 X1 `/k8s/logs` (kubelet log
// streaming has no informer equivalent; client-go must call the
// apiserver subresource directly). Adding this accessor here keeps
// the per-cluster CoreClient under the same lifecycle as the
// dynamic + informer state — no second per-cluster client cache.
func (f *Factory) CoreClient(clusterID string) kubernetes.Interface {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cs, ok := f.clusters[clusterID]
	if !ok {
		return nil
	}
	return cs.core
}

// RestConfigFor returns the *rest.Config for a registered Sovereign
// cluster, or an error when no such cluster is registered (or when
// the cluster was injected with a pre-built dynamic.Interface and
// therefore has no captured rest.Config — the test-only path).
//
// G95.1 (Refs #2642) — used by k8s_exec_ws.go's ExecStreamFactory to
// build a remotecommand.NewSPDYExecutor against the apiserver's
// `pods/exec` subresource. Without this accessor the handler had no
// way to dial the apiserver; the factory was never wired in
// production main.go → ExecStreamFactory always returned nil → every
// exec WebSocket returned 503 → browser saw an empty terminal (the
// symptom founder reported on #2642).
//
// Lifecycle parity with CoreClient + DynamicClientFor: same map, same
// lock, no new per-cluster pool.
func (f *Factory) RestConfigFor(clusterID string) (*rest.Config, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cs, ok := f.clusters[clusterID]
	if !ok {
		return nil, fmt.Errorf("k8scache: cluster %q not registered", clusterID)
	}
	if cs.restCfg == nil {
		return nil, fmt.Errorf("k8scache: cluster %q has no captured rest.Config (registered with pre-built DynamicClient)", clusterID)
	}
	return cs.restCfg, nil
}

// DynamicClientFor returns the dynamic.Interface for a registered
// Sovereign cluster, or an error when no such cluster is registered.
//
// Used by EPIC-4 Slice R (#1099) — the resource-detail GET, the
// per-resource action handlers (scale/restart/delete), and the YAML
// editor's apply/dry-run path all need an apiserver-direct mutation
// channel beyond the informer cache. Per ADR-0001 §5 the same
// dynamic client used by the informer is the canonical one — this
// accessor exposes it without leaking the cluster bookkeeping.
//
// Lifecycle parity with CoreClient: same map, same lock, no new
// per-cluster pool.
func (f *Factory) DynamicClientFor(clusterID string) (dynamic.Interface, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cs, ok := f.clusters[clusterID]
	if !ok {
		return nil, fmt.Errorf("k8scache: cluster %q not registered", clusterID)
	}
	if cs.dyn == nil {
		return nil, fmt.Errorf("k8scache: cluster %q has no dynamic client", clusterID)
	}
	return cs.dyn, nil
}

// RedactForKind returns a deep-copy of the supplied unstructured object
// with sensitive fields stripped per the kind's Sensitive flag. Same
// rules as the watch-side redactor — Secret/ConfigMap data + stringData
// are scrubbed and replaced with the redaction marker.
//
// Resource-GET handlers in slice R (#1099) call this after fetching live
// from apiserver so the wire payload matches the SSE/list path's bytes.
func (f *Factory) RedactForKind(k Kind, u *unstructured.Unstructured) *unstructured.Unstructured {
	return redactObject(k, u)
}

// runSyncWatcher polls each informer's HasSynced under the stop
// channel. This is event-driven from the informer's own
// goroutine — we are NOT polling the apiserver here. The 250ms tick
// is a per-process bookkeeping interval; once Synced flips true the
// loop exits.
//
// Per ADR-0001: this is process-internal coordination, not data-plane
// polling. The informer itself uses the apiserver's watch stream.
func (f *Factory) runSyncWatcher(ctx context.Context, cs *clusterState) {
	for kind, inf := range cs.informers {
		kind := kind
		inf := inf
		go func() {
			// HasSynced flips true exactly once per cluster lifetime.
			// We block on cache.WaitForCacheSync which itself is
			// event-driven against the informer's controller.
			synced := cache.WaitForCacheSync(ctx.Done(), inf.HasSynced)
			f.mu.Lock()
			cs.synced[kind] = synced
			f.mu.Unlock()
			if synced {
				f.log.Info("k8scache: informer synced",
					"cluster", cs.id, "kind", kind)
			}
		}()
	}
}

// dispatch is invoked from the informer's event handler goroutine on
// every ADD/UPDATE/DELETE. It records last-event metadata, redacts
// sensitive fields, and fans out to every SSE subscriber.
//
// The subscriber send is non-blocking with a single-event drop
// fallback so a slow SSE consumer never wedges the informer
// goroutine. Metric `informer_event_drops_total` increments on drop.
func (f *Factory) dispatch(cs *clusterState, k Kind, t EventType, obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		f.log.Warn("k8scache: dispatch received non-unstructured obj",
			"cluster", cs.id, "kind", k.Name)
		return
	}
	// Always redact before fan-out.
	red := redactObject(k, u)

	// Metric — last-event timestamp.
	now := time.Now()
	cs.lastEventLock.Lock()
	cs.lastEventAt[k.Name] = now
	cs.lastEventLock.Unlock()
	metricLastEvent.WithLabelValues(cs.id, k.Name).Set(float64(now.Unix()))
	metricEvents.WithLabelValues(cs.id, k.Name, string(t)).Inc()

	ev := Event{
		Cluster: cs.id,
		Kind:    k.Name,
		Type:    t,
		Object:  red,
		At:      now,
	}
	f.fanout(ev)
}

// Publish injects a non-watch-derived Event onto the SSE fanout. Used
// by internal/k8scache/evaluators (EPIC-1 #1096 slice W2) to emit
// synthetic PolicyReport-shaped events on the
// `compliance-evaluator` kind. The event MUST set Cluster, Kind,
// Type, Object, At — the factory does NOT mutate or redact synthetic
// payloads (the producer owns them).
//
// Concurrency: safe for concurrent callers; the underlying fanout
// holds subMu briefly to snapshot the subscriber set, then sends
// non-blocking with the same drop-oldest backpressure as informer
// events.
func (f *Factory) Publish(ev Event) {
	f.fanout(ev)
}

// fanout — non-blocking send to each subscriber. On full channel we
// drop the OLDEST event (drain one then push) so the consumer
// catches up automatically without the producer waiting.
func (f *Factory) fanout(ev Event) {
	f.subMu.Lock()
	subs := make([]*subscriber, 0, len(f.subscribers))
	for _, s := range f.subscribers {
		// Filter on subscribed kinds. Empty kinds map == all kinds.
		if len(s.kinds) == 0 {
			subs = append(subs, s)
			continue
		}
		if _, ok := s.kinds[ev.Kind]; ok {
			subs = append(subs, s)
		}
	}
	f.subMu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			// Drop oldest, push new.
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
			metricSubDrops.WithLabelValues(ev.Cluster, ev.Kind).Inc()
		}
	}
}

// Subscribe registers an SSE consumer with optional kind filter +
// user identity (for SAR gating in the SSE handler). Returns the
// channel + an unsubscribe func.
//
// kinds is the canonicalised name set; empty means "all".
func (f *Factory) Subscribe(user string, kinds map[string]struct{}) (<-chan Event, func()) {
	ch := make(chan Event, f.cfg.EventBufferSize)
	f.subMu.Lock()
	f.nextSubID++
	id := f.nextSubID
	s := &subscriber{
		id:    id,
		kinds: kinds,
		user:  user,
		ch:    ch,
	}
	f.subscribers[id] = s
	count := len(f.subscribers)
	f.subMu.Unlock()
	metricSubscribers.Set(float64(count))
	f.log.Info("k8scache: SSE subscriber connected",
		"id", id, "user", user, "kinds", len(kinds), "total", count)
	return ch, func() {
		f.subMu.Lock()
		delete(f.subscribers, id)
		count := len(f.subscribers)
		f.subMu.Unlock()
		metricSubscribers.Set(float64(count))
		close(ch)
		f.log.Info("k8scache: SSE subscriber disconnected",
			"id", id, "total", count)
	}
}

// List returns a slice of unstructured objects from the named
// (cluster, kind)'s Indexer. The label/field selectors are applied
// via cache.Indexer.ListByOptions equivalent — selectors are
// in-memory filters, no apiserver hit.
//
// Returns the cache age (now - last event) so the handler can set
// X-Cache-Stale-Seconds. age == 0 when the kind has never received
// an event (cold informer); the handler treats that as "stale" and
// hits apiserver directly only when no Indexer entries exist.
func (f *Factory) List(clusterID, kindName string, sel labels.Selector) ([]*unstructured.Unstructured, time.Duration, error) {
	f.mu.RLock()
	cs, ok := f.clusters[clusterID]
	f.mu.RUnlock()
	if !ok {
		return nil, 0, fmt.Errorf("k8scache: cluster %q not registered", clusterID)
	}
	// Resolve the inbound kind name (which may be a plural alias like
	// "services" or a kubectl short-form like "pvc") to the canonical
	// singular Name informers are keyed under. Registry.Get handles the
	// alias mapping; without this hop the indexer lookup below would
	// see "services" / "pvc" and return "kind not registered" even
	// though the registry resolved the same query at the handler tier.
	canonName := CanonicalKindName(kindName)
	if k, ok := f.registry.Get(kindName); ok {
		canonName = CanonicalKindName(k.Name)
	}
	idx, ok := cs.indexers[canonName]
	if !ok {
		return nil, 0, fmt.Errorf("k8scache: kind %q not registered", kindName)
	}
	out := make([]*unstructured.Unstructured, 0)
	for _, item := range idx.List() {
		u, ok := item.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if sel != nil && !sel.Matches(labels.Set(u.GetLabels())) {
			continue
		}
		out = append(out, redactObject(mustKind(f.registry, canonName), u))
	}
	cs.lastEventLock.RLock()
	last := cs.lastEventAt[canonName]
	cs.lastEventLock.RUnlock()
	var age time.Duration
	if !last.IsZero() {
		age = time.Since(last)
	}
	// Use the canonical singular Name on the metric label so prometheus
	// rolls up alias forms (services + service + svc) into one
	// time-series instead of fanning out to N inflated cardinalities.
	metricCacheSize.WithLabelValues(clusterID, canonName).Set(float64(len(out)))
	return out, age, nil
}

func mustKind(r *Registry, name string) Kind {
	k, ok := r.Get(name)
	if !ok {
		// Caller already validated via Get earlier; return a
		// non-sensitive synthetic Kind so redactObject is a no-op.
		return Kind{Name: CanonicalKindName(name)}
	}
	return k
}

// helpers ---------------------------------------------------------

func firstNonEmpty(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

// FactoryFromEnv loads runtime configuration from environment
// variables and returns a started Factory. The catalyst-api `main.go`
// calls this once at startup.
//
// Env contract (per docs/INVIOLABLE-PRINCIPLES.md #4):
//
//	CATALYST_K8SCACHE_KUBECONFIGS_DIR — directory of kubeconfigs to watch.
//	  Default: /var/lib/catalyst/kubeconfigs (the same PVC the cloud-init
//	  postback writes into; one file per Sovereign).
//	CATALYST_K8SCACHE_SNAPSHOT_DIR — directory the snapshot loop writes to.
//	  Default: /var/cache/sov-cache (mounted from a 5Gi PVC by the chart).
//	CATALYST_K8SCACHE_KINDS_CONFIGMAP — name of the ConfigMap providing
//	  additional Kinds to watch. Optional.
//	CATALYST_K8SCACHE_KINDS_CONFIGMAP_NAMESPACE — namespace for the above.
//	  Default: catalyst.
func FactoryFromEnv(ctx context.Context, log *slog.Logger, core kubernetes.Interface) (*Factory, error) {
	dir := os.Getenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR")
	if dir == "" {
		dir = "/var/lib/catalyst/kubeconfigs"
	}
	snapDir := os.Getenv("CATALYST_K8SCACHE_SNAPSHOT_DIR")
	if snapDir == "" {
		snapDir = "/var/cache/sov-cache"
	}
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		log.Warn("k8scache: snapshot dir create failed; running without disk snapshot",
			"dir", snapDir, "err", err)
		snapDir = ""
	}
	clusters, err := LoadClustersFromDir(log, dir)
	if err != nil {
		log.Warn("k8scache: kubeconfigs dir unreadable; starting with no clusters",
			"dir", dir, "err", err)
	}
	// G94 (Refs #2641, 2026-06-01) — Startup diagnostic. Founder reports
	// chroot Pod restarts lose secondary cluster registration on every
	// roll. PVC persistence + LoadClustersFromDir + AddCluster path is
	// supposed to auto-recover. This INFO-level enumeration surfaces
	// what FactoryFromEnv actually saw at boot so live evidence on hw86
	// can confirm whether (a) files are missing from PVC, (b) files
	// present but skipped by filter, or (c) files present + loaded but
	// downstream AddCluster fails on apiserver reachability.
	for _, c := range clusters {
		log.Info("k8scache: startup — loaded cluster from kubeconfigs dir",
			"id", c.ID,
			"path", c.KubeconfigPath,
		)
	}
	log.Info("k8scache: startup — kubeconfigs dir scan complete",
		"dir", dir,
		"clusterCount", len(clusters),
	)
	// Chroot self-registration: when catalyst-api runs ON a Sovereign
	// cluster (post-cutover, i.e. SOVEREIGN_FQDN env is set), there is
	// no posted-back kubeconfig in /var/lib/catalyst/kubeconfigs/ — the
	// chroot IS the cluster it must watch. Build a ClusterRef from
	// rest.InClusterConfig() so /api/v1/sovereigns/{depId}/k8s/* resolves
	// against the local apiserver. Cluster id is the deployment id from
	// CATALYST_SELF_DEPLOYMENT_ID env (orchestrator-stamped) or the
	// store-fallback path mirroring HandleSovereignSelf. On the mother,
	// SOVEREIGN_FQDN is unset → this branch is a no-op and the
	// kubeconfig-dir path is the only data source.
	if selfFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")); selfFQDN != "" {
		if ref, ok := buildChrootClusterRef(log, selfFQDN); ok {
			// De-dup: if a kubeconfig with the same stem already
			// exists, prefer the explicit kubeconfig path (operator
			// override). This is unusual on a chroot but harmless.
			haveID := false
			for _, c := range clusters {
				if c.ID == ref.ID {
					haveID = true
					break
				}
			}
			if !haveID {
				clusters = append(clusters, ref)
				log.Info("k8scache: chroot self-registered",
					"id", ref.ID, "sovereignFQDN", selfFQDN)
			}
		}
	}
	registry := LoadRegistry(ctx, log, core)
	cfg := Config{
		Logger:         log,
		Clusters:       clusters,
		Registry:       registry,
		SnapshotDir:    snapDir,
		KubeconfigsDir: dir,
		HomeCoreClient: core,
	}
	return NewFactory(cfg)
}

// buildChrootClusterRef constructs an in-cluster ClusterRef for the
// catalyst-api running ON a Sovereign. Resolves the deployment id from
// the orchestrator-stamped env var or by scanning the persistent
// deployment-store directory for a record matching SOVEREIGN_FQDN.
//
// Returns (ref, false) when:
//   - rest.InClusterConfig() fails (catalyst-api running off-cluster
//     in dev),
//   - no deployment id can be resolved (handover hasn't fired yet —
//     re-discovery happens on next pod restart),
//   - dynamic/typed client construction fails.
//
// The caller treats false as "skip self-registration"; the chroot's
// k8scache stays empty and /k8s endpoints return 404, which is the
// correct degraded behavior pre-handover.
func buildChrootClusterRef(log *slog.Logger, sovereignFQDN string) (ClusterRef, bool) {
	depID := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	if depID == "" {
		depID = scanStoreForDeploymentID(sovereignFQDN)
	}
	if depID == "" {
		// Post-cutover the chroot's catalyst-api typically has neither
		// CATALYST_SELF_DEPLOYMENT_ID stamped (cutover step-07 patches
		// CATALYST_GITOPS_REPO_URL only) nor a per-deployment record
		// imported into the local store (no cutover step POSTs the
		// mother's record to /api/v1/internal/deployments/import).
		// Earlier builds deferred chroot self-register here and left
		// /k8s/stream returning 404 forever — every Cloud list-view
		// chip showed "Stream temporarily unreachable".
		//
		// Fall back to a deterministic ID derived from SOVEREIGN_FQDN
		// (which IS guaranteed to be set on every chroot via the
		// Helm chart). The handler layer already aliases unknown URL
		// cluster IDs onto the single chroot cluster when SOVEREIGN_FQDN
		// is set, so the FE's existing behaviour (URL still carries
		// the mother's deployment id) keeps working.
		depID = "sovereign-" + sovereignFQDN
		log.Info("k8scache: chroot self-register using SOVEREIGN_FQDN-derived id",
			"sovereignFQDN", sovereignFQDN, "deploymentID", depID)
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Warn("k8scache: chroot self-register skipped — InClusterConfig unavailable",
			"err", err)
		return ClusterRef{}, false
	}
	cfg.QPS = 50
	cfg.Burst = 100
	// #5210 — force the in-cluster informer/client transport to re-read the
	// rotated projected SA token on a 401 instead of pinning a cached/expired
	// one. Without this the reflector LIST/WATCH + the resource-GET client
	// 401-loop after a cutover disruption rotates the bound token (see the
	// helper godoc for the full client-go transport analysis).
	wrapInClusterTokenRefreshOn401(cfg)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Warn("k8scache: chroot dynamic client build failed", "err", err)
		return ClusterRef{}, false
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Warn("k8scache: chroot typed client build failed", "err", err)
		return ClusterRef{}, false
	}
	return ClusterRef{
		ID:            depID,
		DynamicClient: dyn,
		CoreClient:    core,
	}, true
}

// wrapInClusterTokenRefreshOn401 upgrades an in-cluster *rest.Config so its
// HTTP transport force-refreshes the projected ServiceAccount token whenever
// the apiserver answers 401 Unauthorized.
//
// Root cause (#5210, hw270 post-cutover): rest.InClusterConfig() returns a
// config carrying BearerToken (the token file read ONCE at process start) plus
// BearerTokenFile (the projected-token path). client-go's HasTokenAuth() branch
// wraps that with a bearerAuthRoundTripper, which re-reads the token file on a
// ~1-minute timer but NEVER resets its cache in response to a 401 — unlike the
// tokenSourceTransport client-go installs for a ResettableTokenSource, which
// calls ResetTokenOlderThan on every 401. After a cutover disruption (the
// step-08 10-minute deny-egress hold and/or the step-04 registry-pivot node
// rolls) interrupts the long-lived watch streams and the bound token rotates,
// the reflector LIST/WATCH goroutines and the DynamicClientFor resource-GET can
// pin a cached/expired token and 401-loop against the local apiserver even
// though a fresh read of the projected token file authenticates fine.
//
// Swapping to transport.ResettableTokenSourceWrapTransport makes the very next
// request after a 401 re-read the rotated projected token instead of waiting on
// the timer — the recovery path the plain bearer transport lacks. BearerToken /
// BearerTokenFile are cleared so client-go does NOT also install a
// bearerAuthRoundTripper: that wrapper pre-sets the Authorization header, and
// tokenSourceTransport skips (no reset) any request whose Authorization is
// already set, which would defeat the fix.
//
// No-op when BearerTokenFile is empty (off-cluster dev via a kubeconfig — that
// path carries a static embedded token with no file to re-read).
func wrapInClusterTokenRefreshOn401(cfg *rest.Config) {
	tokenFile := cfg.BearerTokenFile
	if tokenFile == "" {
		return
	}
	ts := transport.NewCachedFileTokenSource(tokenFile)
	cfg.WrapTransport = transport.Wrappers(
		cfg.WrapTransport,
		transport.ResettableTokenSourceWrapTransport(ts),
	)
	cfg.BearerToken = ""
	cfg.BearerTokenFile = ""
}

// scanStoreForDeploymentID walks the deployment store dir for a
// record whose Request.SovereignFQDN matches the given FQDN. Returns
// the matching id, or empty string. Mirrors HandleSovereignSelf's
// store-fallback path so the two stay in sync.
//
// Limited to a single fast pass (≤8 files) because the chroot's
// store typically has exactly one deployment record after
// cutover-import.
func scanStoreForDeploymentID(sovereignFQDN string) string {
	dir := strings.TrimSpace(os.Getenv("CATALYST_DEPLOYMENTS_DIR"))
	if dir == "" {
		dir = "/var/lib/catalyst/deployments"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for i, e := range entries {
		if i >= 8 {
			break
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec struct {
			ID      string `json:"ID"`
			Request struct {
				SovereignFQDN string `json:"SovereignFQDN"`
			} `json:"Request"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if strings.EqualFold(rec.Request.SovereignFQDN, sovereignFQDN) && rec.ID != "" {
			return rec.ID
		}
	}
	// Allow base64-encoded fallback for any future store-format change.
	_ = base64.StdEncoding
	return ""
}

// secretRedactionMarker is the literal value substituted into a
// Secret's .data / .stringData when the redactor scrubs the body.
// Tests assert exact equality on this so a regex change here surfaces
// as a test diff, not a leak.
const secretRedactionMarker = "<redacted>"

// redactObject returns a deep-copy with sensitive fields stripped.
// Per ADR-0001 + INVIOLABLE-PRINCIPLES this is the ONLY path through
// which Secret/ConfigMap bytes ever leave the informer goroutine —
// the SSE encoder and the snapshot writer both consume the redacted
// pointer.
func redactObject(k Kind, u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}
	if !k.Sensitive {
		return u
	}
	clone := u.DeepCopy()
	// Strip data + stringData on Secret / ConfigMap.
	unstructuredDelete(clone, "data")
	unstructuredDelete(clone, "stringData")
	// Substitute a sentinel into a synthetic field so consumers can
	// see "yes there was a body, here are its keys" without the
	// values. The keys list is allocation-light; it's the union of
	// .data and .stringData key names from the original object.
	keys := unionKeys(u, "data", "stringData")
	if len(keys) > 0 {
		_ = setNested(clone.Object, keys, "redactedKeys")
		_ = setNestedString(clone.Object, secretRedactionMarker, "redactedValue")
	}
	return clone
}

