package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/intent"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// intentWatchUser is the subject under which the watches of package intent
// subscribe to the informer cache.
const intentWatchUser = "system:catalyst-api:intent"

// intentCache adapts the informer cache to intent.ObjectCache. It reads
// the cache only; it never calls the API server. Each kind is subscribed to
// once, on its first Watch, and the subscription stays open: closing a
// subscription of the cache while it sends an event is a data race, so the
// adapter fans the events out to its own watchers instead.
type intentCache struct {
	f    *k8scache.Factory
	mu   sync.Mutex
	hubs map[string]*intentHub // by the cache's kind name
}

// intentHub holds the watchers of one kind.
type intentHub struct {
	next int
	subs map[int]intentSub
}

type intentSub struct {
	cluster string
	ch      chan intent.CacheEvent
}

var _ intent.ObjectCache = (*intentCache)(nil)

func newIntentCache(f *k8scache.Factory) *intentCache {
	return &intentCache{f: f, hubs: map[string]*intentHub{}}
}

// Get returns the cached object of one kind with this namespace and name
// in one cluster, or nil.
func (c *intentCache) Get(clusterID, kind, namespace, name string) *unstructured.Unstructured {
	items, _, err := c.f.List(clusterID, kind, nil)
	if err != nil {
		return nil
	}
	for _, it := range items {
		if it.GetNamespace() == namespace && it.GetName() == name {
			return it
		}
	}
	return nil
}

// Watch delivers the later events of one kind in one cluster until cancel
// is called; cancel closes the channel. When the channel is full, the
// oldest event is dropped, as the cache does for its own subscribers.
func (c *intentCache) Watch(clusterID, kind string) (<-chan intent.CacheEvent, func()) {
	name := k8scache.CanonicalKindName(kind)
	if k, ok := c.f.Registry().Get(kind); ok {
		name = k8scache.CanonicalKindName(k.Name)
	}
	c.mu.Lock()
	h, ok := c.hubs[name]
	if !ok {
		h = &intentHub{subs: map[int]intentSub{}}
		c.hubs[name] = h
		src, _ := c.f.Subscribe(intentWatchUser, map[string]struct{}{name: {}})
		go c.pump(h, src)
	}
	id := h.next
	h.next++
	ch := make(chan intent.CacheEvent, 16)
	h.subs[id] = intentSub{cluster: clusterID, ch: ch}
	c.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			delete(h.subs, id)
			close(ch)
		})
	}
}

// pump hands every event of one kind to the watchers of its cluster.
func (c *intentCache) pump(h *intentHub, src <-chan k8scache.Event) {
	for ev := range src {
		ce := intent.CacheEvent{Type: string(ev.Type), Object: ev.Object}
		c.mu.Lock()
		for _, s := range h.subs {
			if s.cluster != ev.Cluster {
				continue
			}
			select {
			case s.ch <- ce:
			default:
				select {
				case <-s.ch:
				default:
				}
				select {
				case s.ch <- ce:
				default:
				}
			}
		}
		c.mu.Unlock()
	}
}

// intentGitConfig is the configuration of the commit library: the Gitea
// URL and token catalyst-api uses today (CATALYST_GITEA_URL and
// CATALYST_GITEA_TOKEN) and the robot e-mail domain robots.<SOVEREIGN_FQDN>.
func intentGitConfig(getenv func(string) string) (gitwrite.Config, error) {
	base := strings.TrimSpace(getenv("CATALYST_GITEA_URL"))
	token := strings.TrimSpace(getenv("CATALYST_GITEA_TOKEN"))
	fqdn := strings.TrimSpace(getenv("SOVEREIGN_FQDN"))
	if base == "" || token == "" || fqdn == "" {
		return gitwrite.Config{}, errors.New("intent: CATALYST_GITEA_URL, CATALYST_GITEA_TOKEN and SOVEREIGN_FQDN are required")
	}
	return gitwrite.Config{BaseURL: base, Tokens: gitwrite.SharedTokenSource(token), RobotDomain: "robots." + fqdn}, nil
}

// newIntentDeps builds the dependencies of package intent. clusterID is the
// cache's identifier of the cluster that applies the intent; getenv reads
// the environment (os.Getenv in production). CATALYST_INTENT_SYNC_WAIT is a
// Go duration above zero, default 15s. Nothing calls it yet except its test.
func newIntentDeps(f *k8scache.Factory, clusterID string, getenv func(string) string) (intent.Deps, error) {
	if f == nil || clusterID == "" {
		return intent.Deps{}, errors.New("intent: the informer cache and its cluster identifier are required")
	}
	cfg, err := intentGitConfig(getenv)
	if err != nil {
		return intent.Deps{}, err
	}
	wait := intent.DefaultSyncWait
	if v := strings.TrimSpace(getenv("CATALYST_INTENT_SYNC_WAIT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return intent.Deps{}, fmt.Errorf("intent: CATALYST_INTENT_SYNC_WAIT=%q is not a duration above zero", v)
		}
		wait = d
	}
	w, err := gitwrite.New(cfg)
	if err != nil {
		return intent.Deps{}, err
	}
	return intent.Deps{
		Git:       w,
		Cache:     newIntentCache(f),
		ClusterID: clusterID,
		FQDN:      strings.TrimSpace(getenv("SOVEREIGN_FQDN")),
		SyncWait:  wait,
	}, nil
}
