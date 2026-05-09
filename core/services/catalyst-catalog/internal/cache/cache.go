// Package cache implements a thread-safe LRU cache with per-entry TTL
// for blueprint.yaml reads. The cache key is opaque (string); callers
// compose keys from `(source, org, name, version)` per the brief.
//
// Invalidation is TTL-only — Gitea-side changes propagate within at
// most CacheTTL seconds. The trade-off is documented in DESIGN.md.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// LRU is a fixed-capacity LRU cache with per-entry TTL.
//
// Methods are safe for concurrent use.
type LRU struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	clock    func() time.Time

	ll      *list.List
	items   map[string]*list.Element

	// metrics
	hits   uint64
	misses uint64
}

type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

// New returns a new LRU. capacity must be > 0; ttl == 0 disables
// expiry (entries only evicted by LRU).
func New(capacity int, ttl time.Duration) *LRU {
	if capacity <= 0 {
		capacity = 1
	}
	return &LRU{
		capacity: capacity,
		ttl:      ttl,
		clock:    time.Now,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

// Get returns (value, true) on hit (and refreshes the item's recency)
// or (nil, false) on miss/expiry.
func (c *LRU) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	en := el.Value.(*entry)
	if c.ttl > 0 && c.clock().After(en.expiresAt) {
		c.removeElement(el)
		c.misses++
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	// Defensive copy so callers cannot mutate the cached payload.
	out := make([]byte, len(en.value))
	copy(out, en.value)
	return out, true
}

// Put inserts or refreshes the entry under key. Defensively copies
// value so the caller can mutate without affecting cached state.
func (c *LRU) Put(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		en := el.Value.(*entry)
		en.value = append(en.value[:0], value...)
		en.expiresAt = c.clock().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	en := &entry{
		key:       key,
		value:     cp,
		expiresAt: c.clock().Add(c.ttl),
	}
	el := c.ll.PushFront(en)
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.removeElement(oldest)
		}
	}
}

// Invalidate removes the entry for key (no-op if absent).
func (c *LRU) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

// Stats returns hit/miss counts. Useful for /healthz and tests.
func (c *LRU) Stats() (hits, misses uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// Len returns the current number of cached entries (mostly for tests).
func (c *LRU) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// SetClock injects a deterministic clock for tests.
func (c *LRU) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clock = now
}

func (c *LRU) removeElement(el *list.Element) {
	en := el.Value.(*entry)
	delete(c.items, en.key)
	c.ll.Remove(el)
}
