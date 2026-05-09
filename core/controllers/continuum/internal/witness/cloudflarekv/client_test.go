// Tests for CFKVClient.
//
// Strategy: stand up an httptest.Server that mocks the K-Cont-4
// Cloudflare Worker — its JSON wire shape is documented in
// client.go's package doc and is THE contract K-Cont-4 must satisfy.
// The mock implements the same atomic-CAS semantics as the in-memory
// witness reference. The shared parametric contract suite from
// `internal/witness/testing` then runs against CFKVClient — a
// behavioral diff between the in-memory reference and CF surfaces
// here.

package cloudflarekv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
	contracttest "github.com/openova-io/openova/core/controllers/continuum/internal/witness/testing"
)

// fakeWorker mimics the K-Cont-4 Cloudflare Worker over httptest.
// One fakeWorker holds N slot states keyed by slot string; multiple
// CFKVClient instances bound to the same slot contend through it.
type fakeWorker struct {
	mu     sync.Mutex
	slots  map[string]*kvState
	now    func() time.Time
	bearer string
}

func newFakeWorker(bearer string, now func() time.Time) *fakeWorker {
	return &fakeWorker{
		slots:  map[string]*kvState{},
		now:    now,
		bearer: bearer,
	}
}

func (w *fakeWorker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/lease/", w.serveLease)
	return mux
}

func (w *fakeWorker) serveLease(rw http.ResponseWriter, req *http.Request) {
	// Auth check — every request must carry the bearer token.
	if got := req.Header.Get("Authorization"); got != "Bearer "+w.bearer {
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}
	slot := strings.TrimPrefix(req.URL.Path, "/lease/")
	if slot == "" {
		http.Error(rw, "missing slot", http.StatusBadRequest)
		return
	}
	// CFKVClient encodes `/` as `%2F`; net/http decodes it back to
	// `/` automatically before serving.

	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()

	switch req.Method {
	case http.MethodGet:
		st, ok := w.slots[slot]
		if !ok {
			http.NotFound(rw, req)
			return
		}
		// TTL eviction at the witness side: if the stored ExpiresAt
		// is in the past, surface an empty record (the holder field
		// is cleared but Generation is preserved so the next
		// Acquire's If-Match has a defined value to match against).
		writeJSON(rw, http.StatusOK, st)
		return

	case http.MethodPut:
		var body kvWriteRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		ifMatch, _ := strconv.ParseInt(req.Header.Get("If-Match"), 10, 64)
		cur := w.slots[slot]
		curGen := int64(0)
		if cur != nil {
			curGen = cur.Generation
		}
		if ifMatch != curGen {
			// CAS conflict.
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusPreconditionFailed)
			if cur != nil {
				_ = json.NewEncoder(rw).Encode(cur)
			}
			return
		}
		// Decide whether the slot is takeable. Takeable when:
		//   - slot is empty (cur==nil)
		//   - slot is expired
		//   - holder matches (re-acquire / renew)
		var (
			holder   = body.Holder
			ttl      = time.Duration(body.TTLSeconds) * time.Second
			takeable = cur == nil
			sameHold = cur != nil && cur.Holder == holder
			expired  = cur != nil && !beforeRFC3339(now, cur.ExpiresAt)
		)
		if !takeable && !sameHold && !expired {
			// Held by another, non-expired holder.
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusPreconditionFailed)
			_ = json.NewEncoder(rw).Encode(cur)
			return
		}
		// Renew op MUST require sameHold + non-expired (matches
		// K-Cont-2 contract). The CFKVClient pre-checks this client-
		// side, so a stray renew arriving here is malformed; reject.
		if body.Op == "renew" && !(sameHold && !expired) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusPreconditionFailed)
			if cur != nil {
				_ = json.NewEncoder(rw).Encode(cur)
			}
			return
		}
		var (
			acquiredAt time.Time
		)
		if sameHold && !expired {
			// Preserve the original acquisition time on
			// re-acquire/renew.
			acquiredAt, _ = time.Parse(time.RFC3339, cur.AcquiredAt)
		} else {
			acquiredAt = now
		}
		next := &kvState{
			Holder:     holder,
			AcquiredAt: acquiredAt.Format(time.RFC3339),
			ExpiresAt:  now.Add(ttl).Format(time.RFC3339),
			Generation: curGen + 1,
		}
		w.slots[slot] = next
		writeJSON(rw, http.StatusOK, next)
		return

	case http.MethodDelete:
		ifMatch, _ := strconv.ParseInt(req.Header.Get("If-Match"), 10, 64)
		holder := req.Header.Get("X-Holder")
		cur := w.slots[slot]
		if cur == nil {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		if cur.Generation != ifMatch || cur.Holder != holder {
			rw.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		// Bump generation on release so a subsequent Acquire sees a
		// non-zero If-Match-required generation. This matches the
		// in-memory reference's Generation+1 on Release.
		w.slots[slot] = &kvState{Generation: cur.Generation + 1}
		rw.WriteHeader(http.StatusNoContent)
		return

	default:
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func beforeRFC3339(now time.Time, expiresAt string) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return now.Before(t)
}

// fakeClock is a thread-safe clock the tests advance.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestCFKV_ContractSuite runs the parametric witness contract against
// CFKVClient over a fake Worker. Behavioral drift between the
// in-memory reference and CF surfaces here.
func TestCFKV_ContractSuite(t *testing.T) {
	t.Parallel()
	contracttest.RunContractSuite(t, func() *contracttest.Backend {
		clk := newFakeClock()
		w := newFakeWorker("test-token", clk.Now)
		srv := httptest.NewServer(w.handler())
		mkClient := func(slot string) witness.Client {
			c, err := New(srv.URL, "test-token", slot, srv.Client())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return c
		}
		return &contracttest.Backend{
			A:       mkClient("ns/cr-main"),
			B:       mkClient("ns/cr-main"),
			Other:   mkClient("ns/cr-other"),
			Advance: clk.Advance,
			Cleanup: srv.Close,
		}
	})
}

// TestCFKV_ConstructorValidation — happy + sad path on New().
func TestCFKV_ConstructorValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, base, token, slot string
		wantErr                 bool
	}{
		{"missing baseURL", "", "tok", "ns/cr", true},
		{"missing token", "http://x", "", "ns/cr", true},
		{"missing slot", "http://x", "tok", "", true},
		{"all set", "http://x", "tok", "ns/cr", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.base, tc.token, tc.slot, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c == nil {
				t.Fatalf("nil client")
			}
		})
	}
}

// TestCFKV_AuthHeaderRequired — the Worker rejects without bearer
// and the client surfaces 401 as a clear error.
func TestCFKV_AuthHeaderRequired(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	w := newFakeWorker("real-token", clk.Now)
	srv := httptest.NewServer(w.handler())
	defer srv.Close()

	c, err := New(srv.URL, "wrong-token", "ns/cr", srv.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Acquire(context.Background(), "fsn", 30*time.Second); err == nil {
		t.Fatalf("expected auth-rejected error")
	} else if !strings.Contains(err.Error(), "auth rejected") {
		t.Fatalf("expected auth rejected, got: %v", err)
	}
}

// TestCFKV_FactoryFromCfg — the registered factory parses cfg
// correctly and resolves SecretRef tokens.
func TestCFKV_FactoryFromCfg(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	w := newFakeWorker("from-secret-token", clk.Now)
	srv := httptest.NewServer(w.handler())
	defer srv.Close()

	secrets := witness.SecretReaderFunc(func(_ context.Context, name, key string) ([]byte, error) {
		if name != "cf-token" || key != "token" {
			return nil, errors.New("not found")
		}
		return []byte("from-secret-token"), nil
	})

	cfg := map[string]any{
		"slot":    "ns/cr",
		"baseURL": srv.URL,
		"tokenSecretRef": map[string]any{
			"name": "cf-token",
			"key":  "token",
		},
	}
	cli, err := factory(cfg, secrets)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	// Override transport so we hit the test server's TLS-less listener.
	cli.(*CFKVClient).HTTPClient = srv.Client()
	st, err := cli.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
}

func TestCFKV_FactoryRejectsMissingFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"empty", map[string]any{}},
		{"missing slot", map[string]any{"baseURL": "http://x", "apiToken": "t"}},
		{"missing baseURL", map[string]any{"slot": "x", "apiToken": "t"}},
		{"missing token", map[string]any{"slot": "x", "baseURL": "http://x"}},
		{
			"secretRef without reader",
			map[string]any{
				"slot":           "x",
				"baseURL":        "http://x",
				"tokenSecretRef": map[string]any{"name": "n", "key": "k"},
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := factory(tc.cfg, nil)
			if err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

// TestCFKV_GenerationBumpedOnRelease — the Worker's contract bumps
// Generation on Release so a subsequent Acquire's If-Match has a
// well-defined non-zero baseline. This belongs in CF-specific tests
// because the in-memory reference also does this; we verify CF
// matches.
func TestCFKV_GenerationBumpedOnRelease(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	w := newFakeWorker("t", clk.Now)
	srv := httptest.NewServer(w.handler())
	defer srv.Close()
	c, _ := New(srv.URL, "t", "ns/cr", srv.Client())

	st1, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := c.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	st2, err := c.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st2.Generation <= st1.Generation {
		t.Fatalf("Generation did not bump on Release: %d -> %d", st1.Generation, st2.Generation)
	}
	if st2.Holder != "" {
		t.Fatalf("Holder = %q want empty after Release", st2.Holder)
	}
}
