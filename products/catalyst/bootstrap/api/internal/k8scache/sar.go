// sar.go — SubjectAccessReview cache for the K8s data-plane handlers.
//
// Per ADR-0001 §8 + INVIOLABLE-PRINCIPLES.md (Keycloak is the IAM
// system) the catalyst-api authorises every K8s read against the
// SOVEREIGN cluster's apiserver via SubjectAccessReview. The naive
// "one SAR per event" model would generate hundreds of POSTs to the
// apiserver per second on a busy cluster — same problem the apiserver
// itself solves with its own watch cache.
//
// The cache below records (user, cluster, kind, namespace, verb) →
// allowed/denied decisions for ~30 seconds. Every cache miss issues
// one SAR and stores the result; every cache hit is a map lookup.
//
// 30s is the documented "good enough" window: long enough to
// amortise the cost of a busy SSE stream, short enough that a
// just-revoked permission stops being honoured within one human
// reaction time.
package k8scache

import (
	"context"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// sarTTL — how long a positive or negative SAR decision is cached.
// Tunable via CATALYST_K8SCACHE_SAR_TTL_SECONDS at FactoryFromEnv
// time; default is what every "K8s authz cache" implementation
// converges on.
const sarTTL = 30 * time.Second

// SARKey identifies a single authorization decision. Cluster is part
// of the key because the same user might have different roles on
// different Sovereigns.
type sarKey struct {
	User      string
	Cluster   string
	Kind      string // canonical name (the GVR resource string is
	// derived from the registry on miss)
	Namespace string
	Verb      string
}

type sarEntry struct {
	allowed  bool
	expires  time.Time
}

// SARCache wraps the per-process decision cache.
type SARCache struct {
	mu      sync.RWMutex
	entries map[sarKey]sarEntry
	ttl     time.Duration
}

// NewSARCache returns an empty cache with the default TTL.
func NewSARCache() *SARCache {
	return &SARCache{
		entries: map[sarKey]sarEntry{},
		ttl:     sarTTL,
	}
}

// WithTTL — test/operator override.
func (c *SARCache) WithTTL(ttl time.Duration) *SARCache {
	c.ttl = ttl
	return c
}

// Allowed answers "may `user` perform `verb` on `kind` in `namespace`
// on `cluster`?". The Factory passes the typed client so the SAR is
// issued against the SOVEREIGN cluster's apiserver, not the
// catalyst-api's home cluster.
//
// Failures from the apiserver fail CLOSED — denied by default. That's
// the safer default: a temporary apiserver hiccup must not leak data.
func (c *SARCache) Allowed(ctx context.Context, factory *Factory, user, cluster, kindName, namespace, verb string) bool {
	k := sarKey{User: user, Cluster: cluster, Kind: kindName, Namespace: namespace, Verb: verb}

	c.mu.RLock()
	if e, ok := c.entries[k]; ok && time.Now().Before(e.expires) {
		c.mu.RUnlock()
		metricSARHits.WithLabelValues(cluster, kindName).Inc()
		return e.allowed
	}
	c.mu.RUnlock()

	// Miss path — issue the SAR. Look up the cluster's typed client.
	factory.mu.RLock()
	cs, ok := factory.clusters[cluster]
	factory.mu.RUnlock()
	if !ok || cs == nil || cs.core == nil {
		// No client → can't authorise → denied.
		c.set(k, false)
		return false
	}
	kind, ok := factory.registry.Get(kindName)
	if !ok {
		c.set(k, false)
		return false
	}

	metricSARMiss.WithLabelValues(cluster, kindName).Inc()

	sar := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     kind.GVR.Group,
				Version:   kind.GVR.Version,
				Resource:  kind.GVR.Resource,
			},
			User: user,
		},
	}
	res, err := cs.core.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		// Apiserver error → fail closed.
		c.set(k, false)
		return false
	}
	allowed := res.Status.Allowed
	c.set(k, allowed)
	return allowed
}

func (c *SARCache) set(k sarKey, allowed bool) {
	c.mu.Lock()
	c.entries[k] = sarEntry{
		allowed: allowed,
		expires: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// CompileTimeAuthCheck — a no-op that asserts kubernetes.Interface
// resolves the AuthorizationV1 surface. Keeps the import live + makes
// any future client-go major-version drift fail at compile time.
func CompileTimeAuthCheck(_ kubernetes.Interface) {}
