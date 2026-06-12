// Tests for DNSQuorumClient.
//
// We mock the TXT read/write side with an in-memory map keyed by
// (server, fqdn). The map enforces atomic per-server PUTs so the
// quorum logic is exercised faithfully — split-brain, partial
// failures, generation skew — without standing up real PowerDNS.
//
// The shared parametric contract suite from
// `internal/witness/testing` then runs against DNSQuorumClient — a
// behavioral diff between the in-memory reference and the quorum
// surface fails here.

package dnsquorum

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
	contracttest "github.com/openova-io/openova/core/controllers/continuum/internal/witness/testing"
)

// fakeBackend mocks N authoritative DNS servers. Each server holds
// an independent map (server → fqdn → value). Writes are atomic
// per-server. Tests can FAIL specific servers (writes return error,
// reads return error) to simulate quorum loss.
type fakeBackend struct {
	mu      sync.Mutex
	servers map[string]map[string]string
	failed  map[string]bool
}

func newFakeBackend(servers []string) *fakeBackend {
	b := &fakeBackend{
		servers: map[string]map[string]string{},
		failed:  map[string]bool{},
	}
	for _, s := range servers {
		b.servers[s] = map[string]string{}
	}
	return b
}

func (b *fakeBackend) WriteTXT(_ context.Context, server, fqdn, value string, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed[server] {
		return fmt.Errorf("server %s unavailable", server)
	}
	if value == "" {
		delete(b.servers[server], fqdn)
		return nil
	}
	b.servers[server][fqdn] = value
	return nil
}

func (b *fakeBackend) ReadTXT(_ context.Context, server, fqdn string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed[server] {
		return nil, fmt.Errorf("server %s unavailable", server)
	}
	v, ok := b.servers[server][fqdn]
	if !ok || v == "" {
		return nil, nil
	}
	return []string{v}, nil
}

// fail marks server as down (writes error, reads error).
func (b *fakeBackend) fail(server string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failed[server] = true
}

// recover re-enables a previously failed server.
func (b *fakeBackend) recover(server string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failed, server)
}

// setRaw lets tests inject divergent state on a specific server (for
// split-brain scenarios).
func (b *fakeBackend) setRaw(server, fqdn, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if value == "" {
		delete(b.servers[server], fqdn)
		return
	}
	b.servers[server][fqdn] = value
}

// TestDNSQuorum_ContractSuite runs the parametric witness contract
// against DNSQuorumClient over a fake-backend with 3 healthy servers.
// Behavioral drift between the in-memory reference and the quorum
// surface fails here.
func TestDNSQuorum_ContractSuite(t *testing.T) {
	t.Parallel()
	contracttest.RunContractSuite(t, func() *contracttest.Backend {
		servers := []string{"ns1", "ns2", "ns3"}
		be := newFakeBackend(servers)
		mkClient := func(slot string) witness.Client {
			c, err := New(servers, "tsig", "lease.openova.io", slot, be, be)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return c
		}
		return &contracttest.Backend{
			A:     mkClient("ns/cr-main"),
			B:     mkClient("ns/cr-main"),
			Other: mkClient("ns/cr-other"),
			// DNS-quorum has no in-band clock — the witness clock
			// IS wall-clock (TXT TTLs encode an absolute
			// expires-at). Advance must really sleep.
			Advance: time.Sleep,
		}
	})
}

// TestDNSQuorum_ConstructorValidation
func TestDNSQuorum_ConstructorValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		servers []string
		domain  string
		slot    string
		wantErr bool
	}{
		{"too few servers", []string{"a", "b"}, "lease.openova.io", "ns/cr", true},
		{"missing domain", []string{"a", "b", "c"}, "", "ns/cr", true},
		{"missing slot", []string{"a", "b", "c"}, "lease.openova.io", "", true},
		{"happy path", []string{"a", "b", "c"}, "lease.openova.io", "ns/cr", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			be := newFakeBackend(tc.servers)
			_, err := New(tc.servers, "k", tc.domain, tc.slot, be, be)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestDNSQuorum_QuorumWith3of3 — happy path: all three servers ack.
func TestDNSQuorum_QuorumWith3of3(t *testing.T) {
	t.Parallel()
	servers := []string{"ns1", "ns2", "ns3"}
	be := newFakeBackend(servers)
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)

	st, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
	// All three servers should have the value.
	for _, s := range servers {
		v, _ := be.ReadTXT(context.Background(), s, c.fqdn())
		if len(v) == 0 || v[0] == "" {
			t.Fatalf("server %s missing value", s)
		}
	}
}

// TestDNSQuorum_QuorumWith2of3_AcquireSucceeds — one server is
// failed before Acquire; the remaining 2/3 are quorum, Acquire must
// succeed.
func TestDNSQuorum_QuorumWith2of3_AcquireSucceeds(t *testing.T) {
	t.Parallel()
	servers := []string{"ns1", "ns2", "ns3"}
	be := newFakeBackend(servers)
	be.fail("ns3")
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)

	st, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire 2-of-3 failed: %v", err)
	}
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
}

// TestDNSQuorum_BelowQuorum_AcquireFails — two of three servers are
// failed, leaving only 1/3 healthy; Acquire fails defensively.
func TestDNSQuorum_BelowQuorum_AcquireFails(t *testing.T) {
	t.Parallel()
	servers := []string{"ns1", "ns2", "ns3"}
	be := newFakeBackend(servers)
	be.fail("ns2")
	be.fail("ns3")
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)

	_, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	// #3195: below-quorum is the DISTINCT ErrQuorumUnavailable now —
	// same no-promote safety, honest operator signal (was misreported
	// as held-by-another, live on hw130).
	if !errors.Is(err, witness.ErrQuorumUnavailable) {
		t.Fatalf("Acquire below-quorum: err = %v want ErrQuorumUnavailable (defensive, distinct)", err)
	}
}

// TestDNSQuorum_SplitBrain_1_1_1_TreatedAsHeldByAnother — three
// distinct values across three servers. Defensive: no quorum →
// ErrLeaseHeldByAnother.
func TestDNSQuorum_SplitBrain_1_1_1_TreatedAsHeldByAnother(t *testing.T) {
	t.Parallel()
	servers := []string{"ns1", "ns2", "ns3"}
	be := newFakeBackend(servers)
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)
	fqdn := c.fqdn()

	be.setRaw("ns1", fqdn, fmt.Sprintf("fsn|%s|5", time.Now().Add(time.Minute).UTC().Format(time.RFC3339)))
	be.setRaw("ns2", fqdn, fmt.Sprintf("hel|%s|5", time.Now().Add(time.Minute).UTC().Format(time.RFC3339)))
	be.setRaw("ns3", fqdn, fmt.Sprintf("ash|%s|5", time.Now().Add(time.Minute).UTC().Format(time.RFC3339)))

	_, err := c.Acquire(context.Background(), "iad", 30*time.Second)
	if !errors.Is(err, witness.ErrQuorumUnavailable) {
		t.Fatalf("split-brain Acquire: err = %v want ErrQuorumUnavailable (1/1/1 disagreement = no readable quorum, distinct from a real holder)", err)
	}
}

// TestDNSQuorum_2of3_DivergentValue_QuorumWins — two of three
// servers agree on holder=fsn, third has a stale "hel" value.
// Acquire by hel must fail (held by another); Acquire by fsn must
// succeed (re-acquire).
func TestDNSQuorum_2of3_DivergentValue_QuorumWins(t *testing.T) {
	t.Parallel()
	servers := []string{"ns1", "ns2", "ns3"}
	be := newFakeBackend(servers)
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)
	fqdn := c.fqdn()

	expires := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	val := fmt.Sprintf("fsn|%s|7", expires)
	be.setRaw("ns1", fqdn, val)
	be.setRaw("ns2", fqdn, val)
	be.setRaw("ns3", fqdn, fmt.Sprintf("hel|%s|7", expires))

	if _, err := c.Acquire(context.Background(), "hel", 30*time.Second); !errors.Is(err, witness.ErrLeaseHeldByAnother) {
		t.Fatalf("hel Acquire: err = %v want ErrLeaseHeldByAnother", err)
	}
	if _, err := c.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("fsn re-Acquire: %v", err)
	}
}

// TestDNSQuorum_FactoryFromCfg — the registered factory parses cfg
// correctly and resolves SecretRef TSIG keys.
func TestDNSQuorum_FactoryFromCfg(t *testing.T) {
	t.Parallel()
	secrets := witness.SecretReaderFunc(func(_ context.Context, name, key string) ([]byte, error) {
		if name != "tsig-secret" || key != "tsig" {
			return nil, errors.New("not found")
		}
		return []byte("real-tsig-key"), nil
	})

	cfg := map[string]any{
		"slot":       "ns/cr",
		"dnsServers": []any{"ns1", "ns2", "ns3"},
		"domain":     "lease.openova.io",
		"tsigSecretRef": map[string]any{
			"name": "tsig-secret",
			"key":  "tsig",
		},
	}
	cli, err := factory(cfg, secrets)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	c := cli.(*DNSQuorumClient)
	if c.TSIGKey != "real-tsig-key" {
		t.Fatalf("TSIGKey = %q want real-tsig-key", c.TSIGKey)
	}
	if c.Domain != "lease.openova.io" {
		t.Fatalf("Domain = %q want lease.openova.io", c.Domain)
	}
	if len(c.Servers) != 3 {
		t.Fatalf("Servers len = %d want 3", len(c.Servers))
	}
}

func TestDNSQuorum_FactoryAcceptsResolversField(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{
		"slot":      "ns/cr",
		"resolvers": []any{"1.1.1.1", "8.8.8.8", "9.9.9.9"},
		"domain":    "lease.openova.io",
		"tsigKey":   "k",
	}
	cli, err := factory(cfg, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	c := cli.(*DNSQuorumClient)
	if len(c.Servers) != 3 {
		t.Fatalf("Servers len = %d want 3", len(c.Servers))
	}
}

func TestDNSQuorum_FactoryRejectsMissingFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"empty", map[string]any{}},
		{"missing servers", map[string]any{"slot": "x", "tsigKey": "k", "domain": "lease.openova.io"}},
		{"too few servers", map[string]any{"slot": "x", "dnsServers": []any{"a", "b"}, "domain": "lease.openova.io", "tsigKey": "k"}},
		{
			"secretRef without reader",
			map[string]any{
				"slot":       "x",
				"dnsServers": []any{"a", "b", "c"},
				"domain":     "lease.openova.io",
				"tsigSecretRef": map[string]any{
					"name": "n",
					"key":  "k",
				},
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

// TestDNSQuorum_EncodeDecodeRoundTrip — wire-format invariant.
func TestDNSQuorum_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []witness.State{
		{},
		{Holder: "fsn", AcquiredAt: now, ExpiresAt: now.Add(30 * time.Second), Generation: 1},
		{Holder: "hel-deu-eu-1", AcquiredAt: now, ExpiresAt: now.Add(30 * time.Second), Generation: 999},
	}
	for i, st := range cases {
		v := encodeValue(st)
		got, err := decodeValue(v)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if got.Holder != st.Holder {
			t.Errorf("case %d: Holder = %q want %q", i, got.Holder, st.Holder)
		}
		if got.Generation != st.Generation {
			t.Errorf("case %d: Generation = %d want %d", i, got.Generation, st.Generation)
		}
		if !got.AcquiredAt.Equal(st.AcquiredAt) {
			t.Errorf("case %d: AcquiredAt = %v want %v", i, got.AcquiredAt, st.AcquiredAt)
		}
		if !got.ExpiresAt.Equal(st.ExpiresAt) {
			t.Errorf("case %d: ExpiresAt = %v want %v", i, got.ExpiresAt, st.ExpiresAt)
		}
	}
}

// TestDNSQuorum_DecodeLegacy3Field — backward-compat: an older
// controller may have written `<holder>|<expires>|<gen>` records.
// Decode must accept and degrade gracefully (AcquiredAt zero).
func TestDNSQuorum_DecodeLegacy3Field(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	v := fmt.Sprintf("fsn|%s|7", now.Format(time.RFC3339))
	st, err := decodeValue(v)
	if err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if st.Holder != "fsn" {
		t.Errorf("Holder = %q", st.Holder)
	}
	if st.Generation != 7 {
		t.Errorf("Generation = %d", st.Generation)
	}
	if !st.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt = %v want %v", st.ExpiresAt, now)
	}
}

// TestDNSQuorum_EncodeSlot — DNS-label safety.
func TestDNSQuorum_EncodeSlot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"foo", "foo"},
		{"ns/cr", "ns_cr"},
		{"acme/api-prod", "acme_api-prod"},
		{"a/b/c", "a_b_c"},
	}
	for _, tc := range cases {
		if got := encodeSlot(tc.in); got != tc.want {
			t.Errorf("encodeSlot(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestDNSQuorum_WriterNotConfigured — read-only DNSQuorumClient
// surfaces a clear error on write attempts.
func TestDNSQuorum_WriterNotConfigured(t *testing.T) {
	t.Parallel()
	servers := []string{"a", "b", "c"}
	be := newFakeBackend(servers)
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", nil, be)
	_, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "Writer not configured") {
		t.Fatalf("got: %v", err)
	}
}

// TestDNSQuorum_ContextCancel — cancel propagates.
func TestDNSQuorum_ContextCancel(t *testing.T) {
	t.Parallel()
	servers := []string{"a", "b", "c"}
	be := newFakeBackend(servers)
	c, _ := New(servers, "k", "lease.openova.io", "ns/cr", be, be)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Acquire(ctx, "fsn", 30*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after cancel: %v", err)
	}
}
