// Package pdns — REST client for the PowerDNS Authoritative HTTP API.
//
// PDM is the canonical writer for every Sovereign zone in the OpenOva fleet:
// /reserve creates an empty child zone in PowerDNS + adds NS-delegation
// records into the parent pool zone; /commit writes A records into the child
// zone and activates DNSSEC signing; /release drops the child zone, removes
// the parent's NS delegation, and retires the DNSSEC key material.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//   - #2 — The client never silently degrades. Every PowerDNS call retries
//     once on 5xx with exponential backoff (250ms then 1s), then hard-fails
//     with the upstream error surfaced verbatim.
//
//   - #3 — The client follows the documented PowerDNS REST contract exactly
//     (https://doc.powerdns.com/authoritative/http-api/zone.html). No
//     bespoke shortcuts; we always PATCH RRSets to write records, POST a
//     full Zone object to create, DELETE the zone resource to drop it.
//
//   - #4 — Base URL, API key, and TTLs are all runtime configuration. The
//     defaults baked into helpers (TTL=300 for child A records, TTL=3600
//     for parent NS delegation) come from PLATFORM-POWERDNS.md and can be
//     overridden by the caller passing explicit RRSet entries.
//
//   - #10 — The API key is provided to New() from a K8s Secret; it never
//     appears in logs, error strings, or the URL. Outbound requests carry
//     it in the X-API-Key header.
package pdns

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
)

// Default TTLs per docs/PLATFORM-POWERDNS.md.
const (
	// DefaultChildRecordTTL — child-zone A/AAAA records use a short TTL so
	// regional failover (LB IP rotation) propagates quickly.
	DefaultChildRecordTTL = 300
	// DefaultParentNSDelegationTTL — parent-zone NS delegations use the
	// standard 1h TTL; delegation rarely changes after initial setup.
	DefaultParentNSDelegationTTL = 3600
)

// ZoneKind enumerates the PowerDNS zone kinds we use. We always use
// "Native" — PowerDNS replication between replicas is handled by the shared
// CNPG-backed Postgres backend rather than DNS NOTIFY/AXFR. The other valid
// kinds (Master, Slave, Producer, Consumer) are not exercised by Catalyst.
type ZoneKind string

const (
	// ZoneKindNative — replication via the shared backend database. Default
	// for all Catalyst-authored zones.
	ZoneKindNative ZoneKind = "Native"
)

// Client is a PowerDNS Authoritative REST API client. Construct once with
// New and reuse — concurrent calls on the same Client are safe (the
// underlying http.Client is goroutine-safe).
type Client struct {
	// BaseURL — root of the PowerDNS API, e.g. "http://powerdns.openova-system.svc.cluster.local:8081"
	// or "https://pdns.openova.io". Trailing slash is normalised away.
	BaseURL string
	// ServerID — PowerDNS server identifier. The string "localhost" is the
	// universal default; PowerDNS uses it for the embedded server even when
	// the listening address is not loopback. Override if running behind a
	// virtual-host that maps to a different ID.
	ServerID string
	// APIKey — value of the X-API-Key header. Read from K8s secret
	// powerdns-api-credentials/api-key by the caller; never hardcoded.
	APIKey string
	// HTTP — underlying client. Defaults to a 30s timeout in New.
	HTTP *http.Client
	// AuthorizationHeader — optional Authorization header value used when
	// the API is fronted by a Traefik basicAuth middleware (the public
	// pdns.openova.io ingress). In-cluster traffic to the ClusterIP service
	// bypasses Traefik and leaves this empty.
	AuthorizationHeader string
}

// New constructs a Client with sensible defaults. baseURL is required;
// serverID falls back to "localhost"; apiKey is required.
func New(baseURL, serverID, apiKey string) *Client {
	if serverID == "" {
		serverID = "localhost"
	}
	return &Client{
		BaseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		ServerID: strings.TrimSpace(serverID),
		APIKey:   strings.TrimSpace(apiKey),
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// RRSet is the PowerDNS API shape for an "edit a resource record set"
// payload. Aligned 1:1 with the PATCH /zones/<zone> body.
type RRSet struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	TTL        int      `json:"ttl,omitempty"`
	ChangeType string   `json:"changetype"` // "REPLACE" or "DELETE"
	Records    []Record `json:"records,omitempty"`
}

// Record is one record inside an RRSet.
type Record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// Zone is a subset of the PowerDNS Zone JSON shape — only the fields
// CreateZone needs to send.
type Zone struct {
	Name        string   `json:"name"`
	Kind        ZoneKind `json:"kind"`
	Nameservers []string `json:"nameservers,omitempty"`
	DNSSEC      bool     `json:"dnssec,omitempty"`
}

// Cryptokey is the shape returned by GET /zones/<zone>/cryptokeys.
type Cryptokey struct {
	ID        int    `json:"id"`
	KeyType   string `json:"keytype"` // "ksk" | "zsk" | "csk"
	Active    bool   `json:"active"`
	Published bool   `json:"published"`
	Algorithm string `json:"algorithm"`
}

// CreateZone creates an empty authoritative zone of the given kind. The
// nameservers slice is the list of NS records PowerDNS will pre-populate at
// the apex of the new zone — pass the canonical OpenOva NS endpoints
// (ns1/ns2/ns3.openova.io) so the child zone is self-contained.
//
// Per docs/PLATFORM-POWERDNS.md the child zone owns its own NS RRset at
// apex (matching the parent's delegation), so the resolver answers
// authoritatively without requiring a glue lookup back to the parent.
//
// Returns nil on 201 Created. Returns nil on 409 Conflict (zone already
// exists) — CreateZone is idempotent so PDM startup can retry without
// short-circuiting on already-bootstrapped parents.
func (c *Client) CreateZone(ctx context.Context, name string, kind ZoneKind, nameservers []string) error {
	zone := Zone{
		Name:        canonicaliseZone(name),
		Kind:        kind,
		Nameservers: canonicaliseNameservers(nameservers),
	}
	body, err := json.Marshal(zone)
	if err != nil {
		return fmt.Errorf("marshal zone: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, c.zonesPath(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict:
		// Idempotent — zone already exists. The bootstrap path relies on this.
		return nil
	default:
		return decodeAPIError(resp, "create zone "+name)
	}
}

// DeleteZone drops a zone, all its records, and its DNSSEC keys. Idempotent
// — returns nil if the zone is already gone (404).
func (c *Client) DeleteZone(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.zonePath(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return nil
	default:
		return decodeAPIError(resp, "delete zone "+name)
	}
}

// ZoneExists returns whether a zone is currently present in PowerDNS.
func (c *Client) ZoneExists(ctx context.Context, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, c.zonePath(name), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, decodeAPIError(resp, "get zone "+name)
	}
}

// PatchRRSets applies a set of RRset edits in a single PATCH. PowerDNS
// treats this as atomic — either every RRset edit lands or none of them
// does. Use ChangeType "REPLACE" to upsert, "DELETE" to remove.
func (c *Client) PatchRRSets(ctx context.Context, zone string, rrsets []RRSet) error {
	if len(rrsets) == 0 {
		return nil
	}
	for i := range rrsets {
		rrsets[i].Name = canonicaliseRRName(rrsets[i].Name)
	}
	payload := struct {
		RRSets []RRSet `json:"rrsets"`
	}{RRSets: rrsets}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal rrsets: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPatch, c.zonePath(zone), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	default:
		return decodeAPIError(resp, "patch rrsets in "+zone)
	}
}

// AddARecord upserts an A record. name is the full FQDN (e.g.
// "console.omantel.omani.works"); value is an IPv4 address; ttl uses
// DefaultChildRecordTTL when 0.
func (c *Client) AddARecord(ctx context.Context, zone, name, ipv4 string, ttl int) error {
	if ttl <= 0 {
		ttl = DefaultChildRecordTTL
	}
	return c.PatchRRSets(ctx, zone, []RRSet{{
		Name:       name,
		Type:       "A",
		TTL:        ttl,
		ChangeType: "REPLACE",
		Records:    []Record{{Content: ipv4}},
	}})
}

// AddNSDelegation upserts the NS delegation RRset for a child zone inside
// its parent zone. nameservers must be the FQDN form (no trailing dot
// required — canonicaliseRRName fixes it). ttl uses
// DefaultParentNSDelegationTTL when 0.
//
// Example:
//
//	AddNSDelegation(ctx, "omani.works",
//	    "omantel.omani.works", []string{"ns1.openova.io","ns2.openova.io"})
//
// inserts an NS RRset at owner-name "omantel.omani.works" inside the
// parent zone "omani.works", which is what the resolver follows when it
// looks up "*.omantel.omani.works".
func (c *Client) AddNSDelegation(ctx context.Context, parentZone, childName string, nameservers []string, ttl int) error {
	if ttl <= 0 {
		ttl = DefaultParentNSDelegationTTL
	}
	if len(nameservers) == 0 {
		return errors.New("AddNSDelegation: nameservers required")
	}
	records := make([]Record, 0, len(nameservers))
	for _, ns := range canonicaliseNameservers(nameservers) {
		records = append(records, Record{Content: ns})
	}
	return c.PatchRRSets(ctx, parentZone, []RRSet{{
		Name:       childName,
		Type:       "NS",
		TTL:        ttl,
		ChangeType: "REPLACE",
		Records:    records,
	}})
}

// RemoveNSDelegation removes the NS RRset at <childName> inside the parent
// zone. Idempotent — succeeds even if the RRset is already gone.
func (c *Client) RemoveNSDelegation(ctx context.Context, parentZone, childName string) error {
	return c.PatchRRSets(ctx, parentZone, []RRSet{{
		Name:       childName,
		Type:       "NS",
		ChangeType: "DELETE",
	}})
}

// EnableDNSSEC turns on DNSSEC for the zone and generates a KSK + ZSK
// key pair (algorithm 13, ECDSAP256SHA256, per docs/PLATFORM-POWERDNS.md).
//
// PowerDNS exposes this via two API calls:
//  1. PUT /zones/<zone> with {"dnssec": true} — flip the dnssec flag
//  2. POST /zones/<zone>/cryptokeys per key — KSK then ZSK, keytype + active
//
// We then call POST /zones/<zone>/rectify to publish the resulting RRSIGs.
//
// Idempotent — if the zone already has active KSK + ZSK with the requested
// algorithm we skip the create call.
func (c *Client) EnableDNSSEC(ctx context.Context, zone string) error {
	// Step 1: flip the dnssec flag via PUT.
	flag := struct {
		DNSSEC bool `json:"dnssec"`
		// PowerDNS recommends this knob for signed zones — keeps SOA
		// serials in lock-step across replicas.
		SOAEdit       string `json:"soa_edit,omitempty"`
		SOAEditAPI    string `json:"soa_edit_api,omitempty"`
	}{
		DNSSEC:     true,
		SOAEdit:    "INCEPTION-EPOCH",
		SOAEditAPI: "INCEPTION-EPOCH",
	}
	body, err := json.Marshal(flag)
	if err != nil {
		return fmt.Errorf("marshal dnssec flag: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPut, c.zonePath(zone), body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("enable dnssec on %s: status %d", zone, resp.StatusCode)
	}

	// Step 2: list existing cryptokeys; only generate the missing ones.
	keys, err := c.ListCryptokeys(ctx, zone)
	if err != nil {
		return fmt.Errorf("list cryptokeys: %w", err)
	}
	hasKSK, hasZSK := false, false
	for _, k := range keys {
		if !k.Active {
			continue
		}
		switch strings.ToLower(k.KeyType) {
		case "ksk", "csk":
			hasKSK = true
		case "zsk":
			hasZSK = true
		}
	}
	if !hasKSK {
		if err := c.createCryptokey(ctx, zone, "ksk"); err != nil {
			return fmt.Errorf("create ksk: %w", err)
		}
	}
	if !hasZSK {
		if err := c.createCryptokey(ctx, zone, "zsk"); err != nil {
			return fmt.Errorf("create zsk: %w", err)
		}
	}

	// Step 3: rectify so RRSIGs are emitted and NSEC chain is built.
	rresp, err := c.do(ctx, http.MethodPut, c.zonePath(zone)+"/rectify", nil)
	if err != nil {
		return err
	}
	rresp.Body.Close()
	if rresp.StatusCode >= 300 {
		return fmt.Errorf("rectify %s: status %d", zone, rresp.StatusCode)
	}
	return nil
}

// ListCryptokeys returns the DNSSEC keys associated with the zone.
func (c *Client) ListCryptokeys(ctx context.Context, zone string) ([]Cryptokey, error) {
	resp, err := c.do(ctx, http.MethodGet, c.zonePath(zone)+"/cryptokeys", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp, "list cryptokeys "+zone)
	}
	var keys []Cryptokey
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		return nil, fmt.Errorf("decode cryptokeys: %w", err)
	}
	return keys, nil
}

// createCryptokey POSTs a new active KSK or ZSK using algorithm 13
// (ECDSAP256SHA256), the algorithm mandated by docs/PLATFORM-POWERDNS.md.
func (c *Client) createCryptokey(ctx context.Context, zone, keytype string) error {
	body, err := json.Marshal(map[string]any{
		"keytype":   keytype,
		"active":    true,
		"published": true,
		"algorithm": "ecdsa256", // PowerDNS string alias for algorithm 13
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, c.zonePath(zone)+"/cryptokeys", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeAPIError(resp, "create "+keytype+" on "+zone)
	}
	return nil
}

// EnsureZone creates the zone if it doesn't exist. Used by the
// startup-time parent-zone bootstrap. Returns nil on success, including
// when the zone already exists.
func (c *Client) EnsureZone(ctx context.Context, name string, kind ZoneKind, nameservers []string) error {
	exists, err := c.ZoneExists(ctx, name)
	if err != nil {
		return fmt.Errorf("zone exists check: %w", err)
	}
	if exists {
		return nil
	}
	return c.CreateZone(ctx, name, kind, nameservers)
}

// ── HTTP plumbing ─────────────────────────────────────────────────────

// do performs an HTTP request with retry-once-on-5xx and exponential
// backoff (250ms then 1s). The response body is left for the caller to
// close on success; on transport-layer error we close it before returning.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := c.BaseURL + path
	backoffs := []time.Duration{0, 250 * time.Millisecond, 1 * time.Second}
	var lastErr error
	for i, sleep := range backoffs {
		if sleep > 0 {
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		if c.APIKey != "" {
			req.Header.Set("X-API-Key", c.APIKey)
		}
		if c.AuthorizationHeader != "" {
			req.Header.Set("Authorization", c.AuthorizationHeader)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if i == len(backoffs)-1 {
				return nil, fmt.Errorf("powerdns api %s %s: %w", method, path, err)
			}
			continue
		}
		// Retry only on 5xx; 4xx is a caller error and surfaces immediately.
		if resp.StatusCode >= 500 && resp.StatusCode < 600 && i < len(backoffs)-1 {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			lastErr = fmt.Errorf("powerdns api %s %s: status %d", method, path, resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func (c *Client) zonesPath() string {
	return "/api/v1/servers/" + c.ServerID + "/zones"
}

func (c *Client) zonePath(zone string) string {
	return c.zonesPath() + "/" + canonicaliseZone(zone)
}

// canonicaliseZone — PowerDNS requires zone names with a trailing dot
// (the API normalises this internally for some endpoints but not all;
// always sending the canonical form sidesteps inconsistency).
func canonicaliseZone(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return name
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

// canonicaliseRRName — same as canonicaliseZone; RRset owner names also
// need a trailing dot.
func canonicaliseRRName(name string) string {
	return canonicaliseZone(name)
}

// canonicaliseNameservers — canonicalises each NS hostname to FQDN-with-dot
// form, drops empties.
func canonicaliseNameservers(in []string) []string {
	out := make([]string, 0, len(in))
	for _, ns := range in {
		ns = strings.ToLower(strings.TrimSpace(ns))
		if ns == "" {
			continue
		}
		if !strings.HasSuffix(ns, ".") {
			ns += "."
		}
		out = append(out, ns)
	}
	return out
}

// decodeAPIError reads a non-2xx response body and surfaces the PowerDNS
// "error" field when present, or the raw body truncated to 256 chars.
func decodeAPIError(resp *http.Response, op string) error {
	body, _ := io.ReadAll(resp.Body)
	var apiErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != "" {
		return fmt.Errorf("powerdns %s: status %d: %s", op, resp.StatusCode, apiErr.Error)
	}
	snippet := string(body)
	if len(snippet) > 256 {
		snippet = snippet[:256] + "..."
	}
	return fmt.Errorf("powerdns %s: status %d: %s", op, resp.StatusCode, snippet)
}
