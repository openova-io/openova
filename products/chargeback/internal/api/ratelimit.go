package api

import (
	"math"
	"sync"
	"time"
)

// ipLimiter is a token bucket per client key for the unauthenticated public
// routes (DESIGN.md §11): perMinute tokens refill continuously, the burst is
// one minute's worth, and a caller over budget is told how long to wait.
// Buckets idle for longer than idleTTL are swept on the next call once the
// map has grown past sweepAt entries, so an address seen once does not stay
// in memory forever.
type ipLimiter struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	now       func() time.Time
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	limiterIdleTTL = 10 * time.Minute
	limiterSweepAt = 4096
)

func newIPLimiter(perMinute int, now func() time.Time) *ipLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if now == nil {
		now = time.Now
	}
	return &ipLimiter{perSecond: float64(perMinute) / 60, burst: float64(perMinute), now: now, buckets: map[string]*bucket{}, lastSweep: now()}
}

// allow takes one token for key. When the bucket is empty it reports the
// time until the next token, rounded up to whole seconds for Retry-After.
func (l *ipLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= limiterSweepAt && now.Sub(l.lastSweep) > time.Minute {
			l.sweep(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.perSecond)
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := (1 - b.tokens) / l.perSecond
	return false, time.Duration(math.Ceil(wait)) * time.Second
}

func (l *ipLimiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > limiterIdleTTL {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}
