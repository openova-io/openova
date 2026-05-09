// Package cloudflarekv implements witness.Client over a Cloudflare
// Worker backed by Workers KV. The Worker is the K-Cont-4 deliverable;
// THIS client speaks the Worker's HTTP CAS contract.
//
// The contract (which K-Cont-4 must implement):
//
//	GET    /lease/<slot>            → 200 {holder, acquiredAt,
//	                                       expiresAt, generation} | 404
//	PUT    /lease/<slot>            req body: {holder, ttlSeconds, op}
//	                                req header: If-Match: <generation>
//	                                            (use "0" for first
//	                                            acquire on an empty slot)
//	                                → 200 {…new state…}    on CAS success
//	                                → 412                 on CAS conflict
//	                                                       (held by another)
//	DELETE /lease/<slot>            req header: If-Match: <generation>
//	                                req header: X-Holder: <holder>
//	                                → 204                 success
//	                                → 412                 lost CAS or
//	                                                       not the holder
//
// `op` discriminator on PUT: "acquire" or "renew" — for the Worker's
// log and for asymmetric handling (acquire allows Generation=0;
// renew requires the current Generation to MATCH).
//
// Authentication: the catalyst-controllers ServiceAccount references
// a SealedSecret holding the Worker API token; the controller passes
// it as `Authorization: Bearer <token>`. Per Inviolable Principle #5
// the plaintext lives in memory only.
//
// Why this shape vs raw KV REST?
//   - The Worker centralises the CAS check (KV doesn't natively
//     expose If-Match — the Worker computes the comparison).
//   - One HTTPS endpoint per witness (vs per-account KV creds).
//   - Worker logs per-CR access for audit.
//
// K-Cont-3 (this package) ships the CLIENT; K-Cont-4 (separate slice)
// ships the WORKER source.
package cloudflarekv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func init() {
	witness.Register("cloudflare-kv", factory)
}

// CFKVClient implements witness.Client over a Cloudflare Worker.
//
// Concurrent-safe: a single CFKVClient is used by the per-Continuum-CR
// goroutine for all its Renew calls; the Worker is the
// concurrency-arbitration point.
type CFKVClient struct {
	// BaseURL is the Worker root, e.g.
	// "https://lease.openova.workers.dev". Trailing slash stripped.
	BaseURL string

	// APIToken is the bearer token resolved from a K8s Secret. NEVER
	// log this value.
	APIToken string

	// Slot is the per-CR identifier (`<namespace>/<name>`). URL-encoded
	// at request time so slashes don't confuse the Worker's routing.
	Slot string

	// HTTPClient is the underlying transport. Tests inject a
	// httptest.Server-backed client.
	HTTPClient *http.Client
}

// New constructs a CFKVClient. baseURL + apiToken + slot are required.
//
// httpClient is optional — when nil, a 10-second-timeout client is
// used. The 10s ceiling matches the lease renew interval (per the
// Continuum CRD default 10s) so a stuck request can't outlive a renew
// cycle and double-acquire.
func New(baseURL, apiToken, slot string, httpClient *http.Client) (*CFKVClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("cloudflarekv: BaseURL is required")
	}
	if strings.TrimSpace(apiToken) == "" {
		return nil, errors.New("cloudflarekv: APIToken is required")
	}
	if strings.TrimSpace(slot) == "" {
		return nil, errors.New("cloudflarekv: Slot is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &CFKVClient{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIToken:   strings.TrimSpace(apiToken),
		Slot:       strings.TrimSpace(slot),
		HTTPClient: httpClient,
	}, nil
}

// factory is the witness.Factory entry registered at init() time. Per
// the K-Cont-3 brief cfg may carry:
//
//	slot              (string)  REQUIRED — `<namespace>/<name>`
//	baseURL           (string)  REQUIRED — Worker URL
//	tokenSecretRef    (map)     {name, key} — K8s Secret holding the
//	                                          Worker bearer token
//	apiToken          (string)  ALTERNATE to tokenSecretRef — direct
//	                                          token for tests; NOT used
//	                                          in production paths.
func factory(cfg map[string]any, secrets witness.SecretReader) (witness.Client, error) {
	slot, _ := cfg["slot"].(string)
	baseURL, _ := cfg["baseURL"].(string)
	if baseURL == "" {
		// Tolerate the alternate spelling per the CRD shape — the
		// CRD has accountId + kvNamespaceId + tokenSecretRef and
		// expects the Worker URL to be derived. K-Cont-3 takes a
		// straight baseURL key; if the CR uses the CRD shape, the
		// reconciler is responsible for translating to baseURL
		// before calling Select. Fall back to "workerURL" alias for
		// flexibility.
		if v, ok := cfg["workerURL"].(string); ok {
			baseURL = v
		}
	}

	// Token resolution path A: direct (tests only).
	apiToken, _ := cfg["apiToken"].(string)

	// Path B: SecretRef. Production path. Required when apiToken is
	// empty.
	if apiToken == "" {
		ref, _ := cfg["tokenSecretRef"].(map[string]any)
		if ref == nil {
			// alternate field-name from K-Cont-2's `tokenSecretRef`
			// slice may pass "tokenSecretRef" as
			// map[string]interface{}{} OR as a marshaled JSON object.
			// We don't try harder than this; if neither is present,
			// surface a clear error.
			return nil, errors.New("cloudflarekv: cfg must include tokenSecretRef{name,key} or apiToken")
		}
		name, _ := ref["name"].(string)
		key, _ := ref["key"].(string)
		if key == "" {
			key = "token"
		}
		if name == "" {
			return nil, errors.New("cloudflarekv: tokenSecretRef.name is empty")
		}
		if secrets == nil {
			return nil, errors.New("cloudflarekv: SecretReader not configured (controller must wire DefaultSelector.SecretReader)")
		}
		// 5s budget for the secret read — controller startup must
		// not block forever on a missing Secret.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, err := secrets.ReadSecret(ctx, name, key)
		if err != nil {
			return nil, fmt.Errorf("cloudflarekv: read tokenSecretRef %s/%s: %w", name, key, err)
		}
		apiToken = strings.TrimSpace(string(b))
		if apiToken == "" {
			return nil, fmt.Errorf("cloudflarekv: tokenSecretRef %s/%s is empty", name, key)
		}
	}

	return New(baseURL, apiToken, slot, nil)
}

// kvState mirrors the Worker's response body. Field names are JSON
// camelCase per the Worker contract.
type kvState struct {
	Holder     string `json:"holder"`
	AcquiredAt string `json:"acquiredAt"` // RFC3339 — server-stamped
	ExpiresAt  string `json:"expiresAt"`  // RFC3339 — server-stamped
	Generation int64  `json:"generation"`
}

// kvWriteRequest is the PUT body for both acquire and renew.
type kvWriteRequest struct {
	Holder     string `json:"holder"`
	TTLSeconds int    `json:"ttlSeconds"`
	Op         string `json:"op"` // "acquire" | "renew"
}

// Acquire implements witness.Client.
func (c *CFKVClient) Acquire(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	// Read current state to learn the generation for If-Match. On
	// fresh slots Read returns Generation=0; the Worker accepts
	// If-Match: 0 for the first acquire.
	cur, err := c.Read(ctx)
	if err != nil {
		return witness.State{}, err
	}
	return c.write(ctx, holder, ttl, "acquire", cur.Generation)
}

// Renew implements witness.Client.
func (c *CFKVClient) Renew(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	cur, err := c.Read(ctx)
	if err != nil {
		return witness.State{}, err
	}
	// If we don't currently hold the lease (or it's expired), Renew
	// MUST surface ErrLeaseLost regardless of what the Worker says.
	// This matches the K-Cont-2 contract: Renew is for the holder
	// only.
	if cur.Holder != holder {
		return cur, witness.ErrLeaseLost
	}
	if !time.Now().Before(cur.ExpiresAt) {
		return cur, witness.ErrLeaseLost
	}
	st, err := c.write(ctx, holder, ttl, "renew", cur.Generation)
	if err != nil {
		// Map ErrLeaseHeldByAnother → ErrLeaseLost on the renew
		// path (K-Cont-2 contract: Renew never returns
		// ErrLeaseHeldByAnother — it's only "we lost the lease").
		if errors.Is(err, witness.ErrLeaseHeldByAnother) {
			return st, witness.ErrLeaseLost
		}
		return st, err
	}
	return st, nil
}

// Release implements witness.Client.
func (c *CFKVClient) Release(ctx context.Context, holder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cur, err := c.Read(ctx)
	if err != nil {
		return err
	}
	if cur.Holder == "" || cur.Holder != holder {
		// Idempotent: non-holder Release is a no-op (per K-Cont-2
		// contract). DO NOT round-trip to the Worker.
		return nil
	}
	url := c.BaseURL + "/lease/" + pathEscapeSlot(c.Slot)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("cloudflarekv: build DELETE: %w", err)
	}
	c.applyAuth(req)
	req.Header.Set("If-Match", strconv.FormatInt(cur.Generation, 10))
	req.Header.Set("X-Holder", holder)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflarekv: DELETE: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusPreconditionFailed:
		// CAS lost between our Read and our DELETE — by the K-Cont-2
		// contract Release is idempotent, so a non-holder DELETE
		// returns nil. Worker reports the new state but we don't
		// care.
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("cloudflarekv: auth rejected (status %d)", resp.StatusCode)
	default:
		body := readSnippet(resp.Body)
		return fmt.Errorf("cloudflarekv: DELETE status %d: %s", resp.StatusCode, body)
	}
}

// Read implements witness.Client.
func (c *CFKVClient) Read(ctx context.Context) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	url := c.BaseURL + "/lease/" + pathEscapeSlot(c.Slot)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: build GET: %w", err)
	}
	c.applyAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: GET: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Empty slot — return zero state with Generation=0 so a
		// subsequent Acquire knows to PUT with If-Match: 0.
		return witness.State{}, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return witness.State{}, fmt.Errorf("cloudflarekv: auth rejected (status %d)", resp.StatusCode)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// fall through
	default:
		body := readSnippet(resp.Body)
		return witness.State{}, fmt.Errorf("cloudflarekv: GET status %d: %s", resp.StatusCode, body)
	}

	var k kvState
	if err := json.NewDecoder(resp.Body).Decode(&k); err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: decode GET: %w", err)
	}
	return parseState(k)
}

// write performs the PUT-with-If-Match dance for both acquire and
// renew.
func (c *CFKVClient) write(ctx context.Context, holder string, ttl time.Duration, op string, ifMatch int64) (witness.State, error) {
	body, err := json.Marshal(kvWriteRequest{
		Holder:     holder,
		TTLSeconds: int(ttl / time.Second),
		Op:         op,
	})
	if err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: marshal write: %w", err)
	}
	url := c.BaseURL + "/lease/" + pathEscapeSlot(c.Slot)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: build PUT: %w", err)
	}
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("If-Match", strconv.FormatInt(ifMatch, 10))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return witness.State{}, fmt.Errorf("cloudflarekv: PUT: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var k kvState
		if err := json.NewDecoder(resp.Body).Decode(&k); err != nil {
			return witness.State{}, fmt.Errorf("cloudflarekv: decode PUT: %w", err)
		}
		st, perr := parseState(k)
		if perr != nil {
			return witness.State{}, perr
		}
		return st, nil
	case http.StatusPreconditionFailed:
		// CAS lost — the Worker may include the current state. Try
		// to decode for the caller's benefit; but ALWAYS surface
		// ErrLeaseHeldByAnother.
		var k kvState
		_ = json.NewDecoder(resp.Body).Decode(&k)
		st, _ := parseState(k)
		return st, witness.ErrLeaseHeldByAnother
	case http.StatusUnauthorized, http.StatusForbidden:
		return witness.State{}, fmt.Errorf("cloudflarekv: auth rejected (status %d)", resp.StatusCode)
	default:
		body := readSnippet(resp.Body)
		return witness.State{}, fmt.Errorf("cloudflarekv: PUT status %d: %s", resp.StatusCode, body)
	}
}

// applyAuth stamps the bearer token. Centralised so we never miss it.
func (c *CFKVClient) applyAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	// Belt-and-suspenders for Worker logging: include the slot as a
	// header so the Worker can log it without parsing the URL.
	req.Header.Set("X-Lease-Slot", c.Slot)
}

// parseState decodes the wire shape into a witness.State.
func parseState(k kvState) (witness.State, error) {
	out := witness.State{
		Holder:     k.Holder,
		Generation: k.Generation,
	}
	if k.AcquiredAt != "" {
		t, err := time.Parse(time.RFC3339, k.AcquiredAt)
		if err != nil {
			return out, fmt.Errorf("cloudflarekv: parse acquiredAt %q: %w", k.AcquiredAt, err)
		}
		out.AcquiredAt = t
	}
	if k.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, k.ExpiresAt)
		if err != nil {
			return out, fmt.Errorf("cloudflarekv: parse expiresAt %q: %w", k.ExpiresAt, err)
		}
		out.ExpiresAt = t
	}
	return out, nil
}

// pathEscapeSlot URL-encodes the slot for safe inclusion in the
// Worker's path. We use a simple substitution for `/` because the
// Worker treats the slot as an opaque key — `%2F` is the canonical
// encoding.
func pathEscapeSlot(slot string) string {
	// Use net/url style escaping; net/url.PathEscape encodes `/` to
	// `%2F` which is what we want.
	return strings.ReplaceAll(slot, "/", "%2F")
}

// readSnippet returns at most 256 chars of the body for an error
// message. Never logs.
func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	s := string(b)
	if len(s) > 256 {
		s = s[:256] + "..."
	}
	return s
}

// Compile-time assertion that CFKVClient satisfies witness.Client.
var _ witness.Client = (*CFKVClient)(nil)
