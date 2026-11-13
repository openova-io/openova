// Package sovself serves GET /api/v1/sovereign/self: who this Sovereign is
// (its deployment id and its name) and the run-time state the console
// reads: the mode, the hand-over phase, where it pulls charts and packages
// from, who holds custody, the release it follows and its Kubernetes
// version.
//
// The mode, the source and the release come from the variables document:
// the ConfigMap flux-system/sovereign-variables (contract C5, published as
// docs/contracts/c5-upstream-variables.schema.json). Each key is read on
// its own. A key that is absent, or whose value the contract does not
// allow, gives the contract default. A document that holds only the
// bootstrap-kit keys therefore answers with the defaults.
//
// A cluster without that ConfigMap was installed before contract C5. There
// the mode follows the marker such a process already carries: a
// catalyst-api without SOVEREIGN_FQDN is the mothership, one with it is a
// Sovereign.
//
// The phase and the custody holder come from the cutover status ConfigMap:
// catalyst/self-sovereign-cutover-status, or the namespace and name in
// CATALYST_CUTOVER_NAMESPACE and CATALYST_CUTOVER_STATUS_CONFIGMAP, as the
// cutover engine reads them. An absent phase is Bootstrapping. An absent
// custody holder is parent.
//
// One answer is kept for CacheTTL, so an anonymous caller cannot turn every
// request into reads of the cluster. A read that fails is logged and
// counts as an absent object; such an answer is not kept.
package sovself

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
)

// The values of the mode, and the phase an absent key stands for.
const (
	ModeSovereign      = "sovereign"
	ModeMothership     = "mothership"
	PhaseBootstrapping = "Bootstrapping"
)

// CacheTTL is how long one answer is kept.
const CacheTTL = 15 * time.Second

const readTimeout = 5 * time.Second

// contractDefaults holds the contract C5 default of each key this route
// reads. TestDefaultsAreTheContracts compares them with the schema.
var contractDefaults = map[string]string{
	"SOVEREIGN_MODE":    "sovereign",
	"SOVEREIGN_CONSUME": "origin",
	"RELEASE_CHANNEL":   "stable",
	"RELEASE_VERSION":   "",
}

// releaseVersion is the contract C5 pattern of RELEASE_VERSION.
var releaseVersion = regexp.MustCompile(`^$|^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// contractAllows tells whether the contract allows value for key.
var contractAllows = map[string]func(string) bool{
	"SOVEREIGN_MODE":    oneOf("sovereign", "mothership"),
	"SOVEREIGN_CONSUME": oneOf("origin", "local"),
	"RELEASE_CHANNEL":   oneOf("stable", "candidate", "edge"),
	"RELEASE_VERSION":   releaseVersion.MatchString,
}

func oneOf(values ...string) func(string) bool {
	return func(v string) bool {
		for _, ok := range values {
			if v == ok {
				return true
			}
		}
		return false
	}
}

// Release is the release a Sovereign follows. An empty version means it
// follows the newest release of its channel.
type Release struct {
	Channel string `json:"channel"`
	Version string `json:"version"`
}

// Response is the answer of GET /api/v1/sovereign/self.
type Response struct {
	DeploymentID      string  `json:"deploymentId"`
	SovereignFQDN     string  `json:"sovereignFQDN"`
	Mode              string  `json:"mode"`
	Phase             string  `json:"phase"`
	Consume           string  `json:"consume"`
	CustodyHolder     string  `json:"custodyHolder"`
	Release           Release `json:"release"`
	KubernetesVersion string  `json:"kubernetesVersion"`
}

// Resolver returns the deployment id and the name of the Sovereign this
// process runs on. ok is false when the process is not on a Sovereign.
type Resolver func(r *http.Request) (deploymentID, fqdn string, ok bool)

// state is everything of the answer that is read from the cluster.
type state struct {
	mode, consume, channel, version, phase, custody, kube string
}

// Route serves GET /api/v1/sovereign/self.
type Route struct {
	resolve   Resolver
	log       *slog.Logger
	newClient func() (kubernetes.Interface, error)
	env       func(string) string
	now       func() time.Time

	mu       sync.Mutex
	client   kubernetes.Interface
	kept     state
	keptAt   time.Time
	haveKept bool
}

// New returns the route. It reads the cluster only when a request comes.
func New(resolve Resolver, log *slog.Logger) *Route {
	return newRoute(resolve, log, inCluster, os.Getenv, time.Now)
}

func newRoute(resolve Resolver, log *slog.Logger, newClient func() (kubernetes.Interface, error), env func(string) string, now func() time.Time) *Route {
	if log == nil {
		log = slog.Default()
	}
	return &Route{resolve: resolve, log: log, newClient: newClient, env: env, now: now}
}

// inCluster returns a client of the cluster the process runs in. Every
// request it makes, the server version included, ends after readTimeout.
func inCluster() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = readTimeout
	return kubernetes.NewForConfig(cfg)
}

// Serve answers the route. On a process that is not on a Sovereign it
// answers 404 not-a-sovereign with the body it had before this package
// (error, detail, status and code), and the mode added.
func (rt *Route) Serve(w http.ResponseWriter, r *http.Request) {
	s := rt.current()
	id, fqdn, ok := rt.resolve(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api Pod is not running on a Sovereign cluster (no CATALYST_OTECH_FQDN/SELF_DEPLOYMENT_ID env)",
			"mode":   s.mode,
			"status": http.StatusNotFound,
			"code":   "404",
		})
		return
	}
	writeJSON(w, http.StatusOK, Response{
		DeploymentID:      id,
		SovereignFQDN:     fqdn,
		Mode:              s.mode,
		Phase:             s.phase,
		Consume:           s.consume,
		CustodyHolder:     s.custody,
		Release:           Release{Channel: s.channel, Version: s.version},
		KubernetesVersion: s.kube,
	})
}

// current returns the kept answer while it is younger than CacheTTL, and
// reads the cluster otherwise.
func (rt *Route) current() state {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.haveKept && rt.now().Sub(rt.keptAt) < CacheTTL {
		return rt.kept
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	s, complete := rt.read(ctx)
	if complete {
		rt.kept, rt.keptAt, rt.haveKept = s, rt.now(), true
	}
	return s
}

// read reads the two ConfigMaps and the server version. complete is false
// when a read failed for another reason than an absent object.
func (rt *Route) read(ctx context.Context) (state, bool) {
	legacyMothership := strings.TrimSpace(rt.env("SOVEREIGN_FQDN")) == ""
	if rt.client == nil {
		c, err := rt.newClient()
		if err != nil {
			rt.log.Warn("sovereign self: no cluster client; answering with the defaults", "err", err)
			return resolve(nil, false, legacyMothership, nil, ""), false
		}
		rt.client = c
	}
	vars, present, varsRead := rt.configMap(ctx, upstream.Namespace, upstream.Name)
	ns := strings.TrimSpace(rt.env("CATALYST_CUTOVER_NAMESPACE"))
	if ns == "" {
		ns = "catalyst"
	}
	name := strings.TrimSpace(rt.env("CATALYST_CUTOVER_STATUS_CONFIGMAP"))
	if name == "" {
		name = "self-sovereign-cutover-status"
	}
	status, _, statusRead := rt.configMap(ctx, ns, name)
	complete := varsRead && statusRead
	kube := ""
	if v, err := rt.client.Discovery().ServerVersion(); err == nil {
		kube = v.GitVersion
	} else {
		rt.log.Warn("sovereign self: cannot read the server version", "err", err)
		complete = false
	}
	return resolve(vars, present, legacyMothership, status, kube), complete
}

// configMap returns the data of one ConfigMap. present is false when it
// does not exist; ok is false when it could not be read.
func (rt *Route) configMap(ctx context.Context, ns, name string) (data map[string]string, present, ok bool) {
	cm, err := rt.client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		return cm.Data, true, true
	case apierrors.IsNotFound(err):
		return nil, false, true
	default:
		rt.log.Warn("sovereign self: cannot read a ConfigMap; it counts as absent", "namespace", ns, "name", name, "err", err)
		return nil, false, false
	}
}

// resolve turns what was read into the answer. vars is the data of the
// variables document and present tells whether it exists; status is the
// data of the cutover status ConfigMap.
func resolve(vars map[string]string, present, legacyMothership bool, status map[string]string, kube string) state {
	s := state{
		mode:    contractValue(vars, "SOVEREIGN_MODE"),
		consume: contractValue(vars, "SOVEREIGN_CONSUME"),
		channel: contractValue(vars, "RELEASE_CHANNEL"),
		version: contractValue(vars, "RELEASE_VERSION"),
		phase:   PhaseBootstrapping,
		custody: "parent",
		kube:    kube,
	}
	if !present && legacyMothership {
		s.mode = ModeMothership
	}
	switch p := strings.TrimSpace(status["phase"]); p {
	case "HandedOver", "Independent":
		s.phase = p
	}
	if strings.TrimSpace(status["custodyHolder"]) == "self" {
		s.custody = "self"
	}
	return s
}

// contractValue returns the value of key in vars when the contract allows
// it, and the contract default otherwise.
func contractValue(vars map[string]string, key string) string {
	if v, ok := vars[key]; ok {
		v = strings.TrimSpace(v)
		if contractAllows[key](v) {
			return v
		}
	}
	return contractDefaults[key]
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
