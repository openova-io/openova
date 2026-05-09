package cache

import (
	"sync"
	"testing"
	"time"
)

func TestLRU_HitMiss(t *testing.T) {
	c := New(3, 30*time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("Get on empty cache returned hit")
	}
	c.Put("a", []byte("alpha"))
	v, ok := c.Get("a")
	if !ok || string(v) != "alpha" {
		t.Fatalf("Get(a) = (%q, %v), want (alpha, true)", v, ok)
	}
	hits, misses := c.Stats()
	if hits != 1 || misses != 1 {
		t.Errorf("Stats = (%d, %d), want (1, 1)", hits, misses)
	}
}

func TestLRU_TTLExpiry(t *testing.T) {
	c := New(3, 30*time.Second)
	now := time.Unix(0, 0)
	c.SetClock(func() time.Time { return now })

	c.Put("k", []byte("v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get immediately after Put missed")
	}

	// Advance past TTL.
	now = now.Add(31 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get after TTL elapsed returned hit (should expire)")
	}
	// After miss, the entry should be removed.
	if c.Len() != 0 {
		t.Errorf("Len after expiry = %d, want 0", c.Len())
	}
}

func TestLRU_LRUEviction(t *testing.T) {
	c := New(2, 30*time.Second)
	c.Put("a", []byte("1"))
	c.Put("b", []byte("2"))
	// Access a so b is the LRU.
	c.Get("a")
	// Add c — should evict b.
	c.Put("c", []byte("3"))
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted (was LRU)")
	}
	if v, ok := c.Get("a"); !ok || string(v) != "1" {
		t.Errorf("a should still be cached, got (%q, %v)", v, ok)
	}
	if v, ok := c.Get("c"); !ok || string(v) != "3" {
		t.Errorf("c should be cached, got (%q, %v)", v, ok)
	}
}

func TestLRU_Invalidate(t *testing.T) {
	c := New(3, 30*time.Second)
	c.Put("a", []byte("alpha"))
	c.Invalidate("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("Get after Invalidate returned hit")
	}
}

func TestLRU_PutRefresh(t *testing.T) {
	c := New(3, 30*time.Second)
	now := time.Unix(0, 0)
	c.SetClock(func() time.Time { return now })

	c.Put("k", []byte("v1"))
	now = now.Add(20 * time.Second)
	c.Put("k", []byte("v2")) // should refresh expiry to now+30s
	now = now.Add(20 * time.Second)
	v, ok := c.Get("k")
	if !ok || string(v) != "v2" {
		t.Errorf("Get after refresh = (%q, %v), want (v2, true)", v, ok)
	}
}

func TestLRU_DefensiveCopy(t *testing.T) {
	c := New(3, 30*time.Second)
	val := []byte("alpha")
	c.Put("k", val)
	val[0] = 'X' // mutate caller copy
	got, ok := c.Get("k")
	if !ok || string(got) != "alpha" {
		t.Errorf("cache mutated by caller: got %q", got)
	}
	got[0] = 'Y' // mutate returned copy
	got2, _ := c.Get("k")
	if string(got2) != "alpha" {
		t.Errorf("cache mutated by returned-value mutation: got %q", got2)
	}
}

func TestLRU_ConcurrentSafe(t *testing.T) {
	c := New(64, 30*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%8))
			for j := 0; j < 100; j++ {
				c.Put(key, []byte{byte(i)})
				c.Get(key)
			}
		}(i)
	}
	wg.Wait()
}
