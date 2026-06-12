// Package dnsquorum implements witness.Client across N (default 3)
// DNS authoritative servers that we have TXT-write access to. Quorum
// = ceil(N/2 + 1) = 2-of-3 for the default fleet.
//
// Wire shape:
//
//	Slot key: <slot>.<domain>     e.g. "ns_cr.lease.openova.io"
//	          ('/' in slot becomes '_' to keep DNS-label-safe)
//	TXT record value: "<holder>:<expires-at-rfc3339>:<generation>"
//	                  (empty/no record = "free")
//
// The CAS protocol (per K-Cont-3 brief item 2):
//
//   - Acquire: read all N servers in parallel; compute quorum view of
//     current generation; PUT new value with generation+1 on all N;
//     succeed iff ≥ quorum acks. Quorum split / generation skew on
//     read → defensively return ErrLeaseHeldByAnother.
//
//   - Renew: same as Acquire but the generation MUST bump and the
//     holder MUST already match — otherwise ErrLeaseLost.
//
//   - Release: PUT empty value (cleared) on all N; idempotent.
//
//   - Read: parallel TXT lookup; majority wins; tie → empty.
//
// For Phase-1 POC, the WRITE side uses the PowerDNS REST API
// directly (PATCH /api/v1/servers/localhost/zones/<zone>) on each of
// the N servers. The TXT-write endpoint is an injected TXTWriter so
// tests don't need a real PowerDNS. In production the same writer
// hits the existing PDM `/v1/txt` endpoint (planned addition to PDM
// — K-Cont-3 ships against the contract, K-Cont-{4|5} adds the PDM
// endpoint).
//
// Why std-lib net.Resolver vs github.com/miekg/dns?
//   - Zero new dependency in core/controllers/go.mod (per
//     INVIOLABLE-PRINCIPLES quality bar — fewer transitive deps =
//     fewer attack surfaces + smaller binary).
//   - net.Resolver supports DialContext to target a specific
//     resolver IP, which is exactly what we need for per-server
//     TXT lookups.
//   - We never need DNSSEC validation here (the witness data is
//     non-sensitive — just lease state); a recursive resolver shape
//     suffices.
package dnsquorum

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func init() {
	witness.Register("dns-quorum", factory)
}

// TXTWriter writes a TXT record at <slot>.<domain> on a single
// authoritative server. value="" means "delete the record".
//
// Implementations:
//   - HTTPTXTWriter: PowerDNS REST API (production)
//   - in-memory map: tests
type TXTWriter interface {
	// WriteTXT writes (or deletes when value=="") the TXT record.
	// MUST be idempotent — repeated identical calls are fine.
	// Returns nil on ack.
	WriteTXT(ctx context.Context, server, fqdn, value string, ttl time.Duration) error
}

// TXTReader reads TXT records at fqdn from a single authoritative
// server. Returns the record values (typically a 1-element slice for
// a TXT we wrote). Empty slice = no record.
type TXTReader interface {
	ReadTXT(ctx context.Context, server, fqdn string) ([]string, error)
}

// DNSQuorumClient implements witness.Client across N DNS servers.
//
// Concurrent-safe — every write fans out to all servers in parallel.
type DNSQuorumClient struct {
	// Servers is the authoritative-server list. Each entry is the
	// server's reachable address (e.g. "ns1.openova.io:53" for DNS
	// reads; the corresponding PowerDNS API endpoint is derived by
	// the TXTWriter).
	Servers []string

	// TSIGKey is the shared key the production HTTPTXTWriter uses
	// to sign PUT requests. Per Inviolable Principle #5 the value
	// is in memory only — NEVER log.
	TSIGKey string

	// Domain is the DNS suffix for lease records (e.g.
	// "lease.openova.io"). The full FQDN is `<encodedSlot>.<domain>`.
	Domain string

	// Slot is the per-CR identifier (`<namespace>/<name>`); '/' is
	// replaced with '_' to keep DNS-label-safe.
	Slot string

	// Writer + Reader are the per-server TXT I/O backends. Defaults
	// applied by New() when nil.
	Writer TXTWriter
	Reader TXTReader

	// QuorumOverride lets tests force a specific quorum threshold
	// (production = ceil(N/2+1)). 0 = use the canonical formula.
	QuorumOverride int
}

// New constructs a DNSQuorumClient. servers / domain / slot required;
// writer + reader default to the production HTTP-PowerDNS + std-lib
// TXTReader respectively when nil.
func New(servers []string, tsigKey, domain, slot string, writer TXTWriter, reader TXTReader) (*DNSQuorumClient, error) {
	if len(servers) < 3 {
		return nil, fmt.Errorf("dnsquorum: at least 3 servers required, got %d", len(servers))
	}
	if strings.TrimSpace(domain) == "" {
		return nil, errors.New("dnsquorum: Domain is required")
	}
	if strings.TrimSpace(slot) == "" {
		return nil, errors.New("dnsquorum: Slot is required")
	}
	c := &DNSQuorumClient{
		Servers: servers,
		TSIGKey: strings.TrimSpace(tsigKey),
		Domain:  strings.TrimRight(strings.TrimSpace(domain), "."),
		Slot:    strings.TrimSpace(slot),
		Writer:  writer,
		Reader:  reader,
	}
	if c.Reader == nil {
		c.Reader = NewStdLibTXTReader(2 * time.Second)
	}
	if c.Writer == nil {
		// In production the writer is wired separately (PowerDNS
		// API client). New() returning a nil writer is allowed for
		// the read-path-only case; write methods will surface a
		// clear error if invoked.
	}
	return c, nil
}

// factory is the witness.Factory entry registered at init() time.
//
// cfg keys:
//
//	slot              (string)            REQUIRED — `<ns>/<name>`
//	dnsServers        ([]string|[]any)    REQUIRED — N≥3 servers
//	domain            (string)            REQUIRED — DNS suffix
//	tsigSecretRef     (map)               {name, key} — TSIG key Secret
//	tsigKey           (string)            ALTERNATE — direct (tests)
//
// For Phase-1 POC the same factory is used in production; if the CR
// supplies a CRD-shaped `resolvers` field instead of `dnsServers`,
// the reconciler is expected to translate before calling Select.
func factory(cfg map[string]any, secrets witness.SecretReader) (witness.Client, error) {
	slot, _ := cfg["slot"].(string)
	domain, _ := cfg["domain"].(string)
	if domain == "" {
		domain = "lease.openova.io" // brief's recommended default
	}

	servers, err := stringSlice(cfg["dnsServers"])
	if err != nil {
		return nil, fmt.Errorf("dnsquorum: dnsServers: %w", err)
	}
	if len(servers) == 0 {
		// CRD shape uses "resolvers" — try that field too for
		// forward-compat with the K-Cont-2 CRD.
		servers, err = stringSlice(cfg["resolvers"])
		if err != nil {
			return nil, fmt.Errorf("dnsquorum: resolvers: %w", err)
		}
	}

	tsigKey, _ := cfg["tsigKey"].(string)
	if tsigKey == "" {
		ref, _ := cfg["tsigSecretRef"].(map[string]any)
		if ref != nil {
			name, _ := ref["name"].(string)
			key, _ := ref["key"].(string)
			if key == "" {
				key = "tsig"
			}
			if name == "" {
				return nil, errors.New("dnsquorum: tsigSecretRef.name is empty")
			}
			if secrets == nil {
				return nil, errors.New("dnsquorum: SecretReader not configured (controller must wire DefaultSelector.SecretReader)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			b, sErr := secrets.ReadSecret(ctx, name, key)
			if sErr != nil {
				return nil, fmt.Errorf("dnsquorum: read tsigSecretRef %s/%s: %w", name, key, sErr)
			}
			tsigKey = strings.TrimSpace(string(b))
		}
	}

	// Writer is nil here — production wiring injects a real
	// PowerDNS-API writer separately. For now the client errors on
	// write methods if invoked — this matches the Phase-1 POC
	// strategy in the brief (CF-KV is operational; DNS-quorum
	// requires PDM /v1/txt endpoint which is K-Cont-{4|5}).
	return New(servers, tsigKey, domain, slot, nil, nil)
}

// stringSlice converts a cfg value (which may be []string,
// []interface{}, or nil) into []string. nil → empty + nil.
func stringSlice(v any) ([]string, error) {
	switch s := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return s, nil
	case []any:
		out := make([]string, 0, len(s))
		for i, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string", i, e)
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("want []string, got %T", v)
	}
}

// fqdn returns `<encodedSlot>.<Domain>` for this client.
func (c *DNSQuorumClient) fqdn() string {
	return encodeSlot(c.Slot) + "." + c.Domain
}

// encodeSlot replaces '/' with '_' so the slot is DNS-label-safe.
// '/' is the canonical Continuum slot separator (`<ns>/<name>`); '_'
// is permitted in DNS labels per RFC 2181 §11 (label characters are
// arbitrary octets) and PowerDNS handles them fine.
func encodeSlot(slot string) string {
	return strings.ReplaceAll(slot, "/", "_")
}

// quorum returns the quorum threshold. Tests can override via
// QuorumOverride.
func (c *DNSQuorumClient) quorum() int {
	if c.QuorumOverride > 0 {
		return c.QuorumOverride
	}
	return len(c.Servers)/2 + 1
}

// Acquire implements witness.Client.
func (c *DNSQuorumClient) Acquire(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	cur, ok := c.readQuorum(ctx)
	if !ok {
		// Read quorum failed — same SAFETY posture as held-by-another
		// (never promote blind), but the DISTINCT sentinel so the
		// operator sees "fix the witness wiring" instead of hunting a
		// phantom competing holder (#3195 hw130).
		return cur, witness.ErrQuorumUnavailable
	}
	// Truncate to second precision — matches RFC3339 wire format
	// granularity (the wire round-trip would truncate anyway, so
	// returning a truncated value avoids same-holder Equal-check
	// drift in tests + matches what subsequent Read() would see).
	now := time.Now().UTC().Truncate(time.Second)
	// Slot is takeable iff free, expired, or already ours.
	takeable := cur.Holder == "" || !now.Before(cur.ExpiresAt) || cur.Holder == holder
	if !takeable {
		return cur, witness.ErrLeaseHeldByAnother
	}
	// Compute next state — preserve AcquiredAt on re-acquire.
	acquiredAt := now
	if cur.Holder == holder && now.Before(cur.ExpiresAt) && !cur.AcquiredAt.IsZero() {
		acquiredAt = cur.AcquiredAt
	}
	next := witness.State{
		Holder:     holder,
		AcquiredAt: acquiredAt,
		ExpiresAt:  now.Add(ttl),
		Generation: cur.Generation + 1,
	}
	if err := c.writeQuorum(ctx, encodeValue(next), ttl); err != nil {
		return witness.State{}, err
	}
	return next, nil
}

// Renew implements witness.Client.
func (c *DNSQuorumClient) Renew(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	cur, ok := c.readQuorum(ctx)
	if !ok {
		return cur, witness.ErrLeaseLost
	}
	now := time.Now().UTC().Truncate(time.Second)
	if cur.Holder != holder {
		return cur, witness.ErrLeaseLost
	}
	if !now.Before(cur.ExpiresAt) {
		return cur, witness.ErrLeaseLost
	}
	next := witness.State{
		Holder:     holder,
		AcquiredAt: cur.AcquiredAt,
		ExpiresAt:  now.Add(ttl),
		Generation: cur.Generation + 1,
	}
	if err := c.writeQuorum(ctx, encodeValue(next), ttl); err != nil {
		return witness.State{}, err
	}
	return next, nil
}

// Release implements witness.Client.
func (c *DNSQuorumClient) Release(ctx context.Context, holder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cur, ok := c.readQuorum(ctx)
	if !ok {
		// Quorum read failed — idempotent Release: don't surface an
		// error, but skip the write.
		return nil
	}
	if cur.Holder == "" || cur.Holder != holder {
		return nil
	}
	// Clear the record. Bump generation so a subsequent Acquire
	// sees a non-zero baseline (matches in-memory + CF reference).
	released := witness.State{Generation: cur.Generation + 1}
	return c.writeQuorum(ctx, encodeValue(released), 30*time.Second)
}

// Read implements witness.Client.
func (c *DNSQuorumClient) Read(ctx context.Context) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	st, _ := c.readQuorum(ctx)
	return st, nil
}

// readQuorum queries all N servers in parallel and returns the
// majority State + a quorum-ok flag.
//
// Quorum rule: count distinct State values (by encoded value); if
// any single value has ≥ quorum agreement, it wins. Otherwise
// ok=false.
//
// Empty TXT (no record) is a valid value (= "free"). N readable
// "free" responses count as quorum.
func (c *DNSQuorumClient) readQuorum(ctx context.Context) (witness.State, bool) {
	type result struct {
		val string
		err error
	}
	results := make([]result, len(c.Servers))
	var wg sync.WaitGroup
	for i, srv := range c.Servers {
		i, srv := i, srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			vals, err := c.Reader.ReadTXT(ctx, srv, c.fqdn())
			if err != nil {
				results[i] = result{err: err}
				return
			}
			// First TXT entry wins. Empty slice → "" (= free).
			val := ""
			if len(vals) > 0 {
				val = vals[0]
			}
			results[i] = result{val: val}
		}()
	}
	wg.Wait()
	// Count distinct successful responses.
	tally := map[string]int{}
	for _, r := range results {
		if r.err != nil {
			continue
		}
		tally[r.val]++
	}
	q := c.quorum()
	// Find the value with the largest tally. We need DETERMINISTIC
	// tie-breaking so a 1+1+1 split is reported as "no quorum"
	// reliably.
	bestVal := ""
	best := 0
	for val, cnt := range tally {
		switch {
		case cnt > best:
			bestVal, best = val, cnt
		case cnt == best && val < bestVal:
			// Lexicographic tie-break — stable across runs.
			bestVal = val
		}
	}
	if best < q {
		return witness.State{}, false
	}
	st, err := decodeValue(bestVal)
	if err != nil {
		return witness.State{}, false
	}
	return st, true
}

// writeQuorum PUTs the new value on all N servers in parallel and
// returns nil iff ≥ quorum acks. On sub-quorum success we log nothing
// here — the caller (controller) decides how to react (typically: a
// follow-up Read will detect drift and surface ErrLeaseLost on next
// Renew).
//
// We DELETE-on-empty-value to avoid leaving stale records.
func (c *DNSQuorumClient) writeQuorum(ctx context.Context, value string, ttl time.Duration) error {
	if c.Writer == nil {
		return errors.New("dnsquorum: Writer not configured (Phase-1 POC needs PDM /v1/txt — K-Cont-{4|5})")
	}
	errs := make([]error, len(c.Servers))
	var wg sync.WaitGroup
	for i, srv := range c.Servers {
		i, srv := i, srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.Writer.WriteTXT(ctx, srv, c.fqdn(), value, ttl)
		}()
	}
	wg.Wait()
	acks := 0
	var firstErr error
	for _, err := range errs {
		if err == nil {
			acks++
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if acks < c.quorum() {
		if firstErr != nil {
			return fmt.Errorf("dnsquorum: write quorum %d/%d: %w", acks, c.quorum(), firstErr)
		}
		return fmt.Errorf("dnsquorum: write quorum %d/%d", acks, c.quorum())
	}
	return nil
}

// encodeValue serializes a State to the wire format
// `<holder>|<acquired-at>|<expires-at>|<generation>`. Empty State →
// empty string.
//
// The `|` separator is chosen because RFC3339 timestamps contain `:`
// (time component). PowerDNS escapes characters in TXT records via
// the standard quoting rules; `|` is plain ASCII and survives the
// PATCH→DNS→LookupTXT round-trip without translation.
//
// AcquiredAt is preserved so re-acquire-by-same-holder doesn't reset
// the original-acquisition timestamp (matches K-Cont-2 contract item
// 4 — same-holder Acquire = lease extension, not a new acquisition).
func encodeValue(s witness.State) string {
	if s.Holder == "" && s.Generation == 0 {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%d",
		s.Holder,
		s.AcquiredAt.UTC().Format(time.RFC3339),
		s.ExpiresAt.UTC().Format(time.RFC3339),
		s.Generation,
	)
}

// decodeValue parses the wire format back into a State. Empty string
// returns the zero State (free slot).
//
// Backward-compat: also accepts the 3-field shape (no AcquiredAt) for
// records written by an older controller version mid-rollout — the
// AcquiredAt is left zero in that case (acceptable: the same-holder
// preservation invariant degrades to a fresh-acquisition timestamp,
// not a wrong one).
func decodeValue(v string) (witness.State, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return witness.State{}, nil
	}
	parts := strings.Split(v, "|")
	switch len(parts) {
	case 4:
		return decode4(parts)
	case 3:
		// Legacy 3-field shape — degrade gracefully.
		return decode3(parts)
	}
	// A bare numeric value like "5" is treated as a released state
	// with that generation (defensive — let it survive downgrade).
	if g, err := strconv.ParseInt(v, 10, 64); err == nil {
		return witness.State{Generation: g}, nil
	}
	return witness.State{}, fmt.Errorf("dnsquorum: malformed TXT %q", v)
}

func decode4(parts []string) (witness.State, error) {
	holder, acquiredStr, expiresStr := parts[0], parts[1], parts[2]
	gen, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return witness.State{}, fmt.Errorf("dnsquorum: parse generation %q: %w", parts[3], err)
	}
	st := witness.State{Holder: holder, Generation: gen}
	if acquiredStr != "" {
		t, err := time.Parse(time.RFC3339, acquiredStr)
		if err != nil {
			return st, fmt.Errorf("dnsquorum: parse acquiredAt %q: %w", acquiredStr, err)
		}
		st.AcquiredAt = t
	}
	if expiresStr != "" {
		t, err := time.Parse(time.RFC3339, expiresStr)
		if err != nil {
			return st, fmt.Errorf("dnsquorum: parse expiresAt %q: %w", expiresStr, err)
		}
		st.ExpiresAt = t
	}
	return st, nil
}

func decode3(parts []string) (witness.State, error) {
	holder, expiresStr := parts[0], parts[1]
	gen, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return witness.State{}, fmt.Errorf("dnsquorum: parse generation %q: %w", parts[2], err)
	}
	st := witness.State{Holder: holder, Generation: gen}
	if expiresStr != "" {
		t, err := time.Parse(time.RFC3339, expiresStr)
		if err != nil {
			return st, fmt.Errorf("dnsquorum: parse expiresAt %q: %w", expiresStr, err)
		}
		st.ExpiresAt = t
	}
	return st, nil
}

// SortedServers returns Servers sorted lexically — used by tests for
// deterministic output and exposed as a helper for diagnostics. Pure
// function, no I/O.
func (c *DNSQuorumClient) SortedServers() []string {
	out := append([]string(nil), c.Servers...)
	sort.Strings(out)
	return out
}

// Compile-time assertion that DNSQuorumClient satisfies witness.Client.
var _ witness.Client = (*DNSQuorumClient)(nil)

// ----------------------------------------------------------------------
// std-lib TXTReader implementation — production read path

// StdLibTXTReader uses net.Resolver with DialContext to target a
// specific authoritative server. No external DNS-library dependency.
type StdLibTXTReader struct {
	Timeout time.Duration
}

// NewStdLibTXTReader returns a TXTReader using the std-lib resolver.
func NewStdLibTXTReader(timeout time.Duration) *StdLibTXTReader {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &StdLibTXTReader{Timeout: timeout}
}

// ReadTXT queries `fqdn` TXT records at the specified server.
//
// `server` accepts either "host" (port 53 implied) or "host:port".
func (r *StdLibTXTReader) ReadTXT(ctx context.Context, server, fqdn string) ([]string, error) {
	addr := server
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "53")
	}
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(dctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: r.Timeout}
			// Force connection to the specific authoritative
			// server. We always dial UDP; failover to TCP would
			// require a second round-trip we can skip for the
			// short TXT values we use.
			return d.DialContext(dctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	vals, err := res.LookupTXT(ctx, fqdn)
	if err != nil {
		// NXDOMAIN / NoData are normal for a "free" slot — return
		// empty + nil.
		var derr *net.DNSError
		if errors.As(err, &derr) && (derr.IsNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("dnsquorum: LookupTXT %s @ %s: %w", fqdn, addr, err)
	}
	return vals, nil
}
