// Package pdm — thin HTTP client for the pool-domain-manager (PDM)
// `/v1/lua/commit` endpoint that the continuum-controller calls to
// publish lua-record bodies into PowerDNS.
//
// PDM owns the canonical zone-write path for every Sovereign zone
// (PLATFORM-POWERDNS.md "In-cluster consumers"). The
// continuum-controller hands lua-record bodies to PDM rather than
// driving PowerDNS REST directly so:
//
//   - Auth-token, retry, and DNSSEC rectify are centralised.
//   - The audit log of "who wrote what when" lives in one place.
//   - K-Cont-3's dns-quorum implementation can also use PDM to write
//     its lease TXT records via the same client.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the PDM URL + auth token are
// runtime config — never hardcoded. Tests inject a `Doer`-shaped
// fake so the unit suite has zero network IO.
package pdm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
)

// Doer is the subset of *http.Client the PDM client needs. Tests
// inject a fake.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client wraps the PDM API. Construct with New(). Concurrent calls
// are safe.
type Client struct {
	// BaseURL is the PDM root, e.g.
	// "http://pool-domain-manager.openova-system.svc.cluster.local:8080".
	// Trailing slash is stripped at construction time.
	BaseURL string

	// AuthToken (optional) is set on the X-Catalyst-Token header. PDM
	// rejects unauth'd writes when token-auth is enabled in its
	// envconfig.
	AuthToken string

	// HTTP is the underlying transport. Defaults to a 30s-timeout
	// http.Client.
	HTTP Doer
}

// New returns a Client with sensible defaults. baseURL is required;
// authToken is optional (PDM may run with token-auth disabled in
// dev).
func New(baseURL, authToken string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		AuthToken: strings.TrimSpace(authToken),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// CommitLuaRecords posts a batch of lua-record bodies to PDM, which
// in turn PATCHes them as RRsets onto the appropriate Sovereign zone
// in PowerDNS.
//
// Per the K-Cont-2 brief item #5 + EPICS-1-6-unified-design.md §9.3
// step 4, this is the DNS-flip step of the switchover sequence.
//
// Idempotent: same input = same on-the-wire body, PDM upserts
// RRsets via PATCH (replace), so repeated calls do not add duplicates.
//
// Returns ErrNoOp when records is empty (caller may pass an empty set
// after a synth pass that produced zero records — it's legal but a
// no-op so we surface it for logging). Otherwise wraps any transport
// error in a PDM-prefixed message.
func (c *Client) CommitLuaRecords(ctx context.Context, zone string, records []dns.Record) error {
	if c == nil {
		return errors.New("pdm: nil client")
	}
	if c.BaseURL == "" {
		return errors.New("pdm: BaseURL is required")
	}
	if zone == "" {
		return errors.New("pdm: zone is required")
	}
	if len(records) == 0 {
		return ErrNoOp
	}

	body, err := dns.MarshalRecords(records)
	if err != nil {
		return fmt.Errorf("pdm: marshal records: %w", err)
	}
	// Wrap in zone envelope so PDM knows which zone to PATCH.
	envelope := map[string]json.RawMessage{
		"zone":    json.RawMessage(`"` + jsonEscape(zone) + `"`),
		"records": rawRecordsField(body),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("pdm: marshal envelope: %w", err)
	}

	url := c.BaseURL + "/v1/lua/commit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("pdm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.AuthToken != "" {
		req.Header.Set("X-Catalyst-Token", c.AuthToken)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pdm: http: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("pdm: auth rejected (status %d)", resp.StatusCode)
	default:
		body, _ := io.ReadAll(resp.Body)
		snippet := string(body)
		if len(snippet) > 256 {
			snippet = snippet[:256] + "..."
		}
		return fmt.Errorf("pdm: status %d: %s", resp.StatusCode, snippet)
	}
}

// rawRecordsField extracts the "records":[...] sub-field from
// dns.MarshalRecords' output. We do this rather than re-marshal so
// the byte-stable output of MarshalRecords is preserved into the
// envelope (idempotency depends on it).
func rawRecordsField(b []byte) json.RawMessage {
	var top struct {
		Records json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		// Fallback to raw — the envelope still ships, PDM error
		// surfaces upstream.
		return json.RawMessage(b)
	}
	return top.Records
}

// jsonEscape returns a JSON-safe copy of `s` for use inside a
// pre-marshalled string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// Strip the surrounding quotes.
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}

// ErrNoOp signals that CommitLuaRecords was called with an empty
// records slice — surfacing this lets callers log "skipped DNS write
// for app X" without conflating it with a real failure.
var ErrNoOp = errors.New("pdm: no records to commit (no-op)")
