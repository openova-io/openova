// Package powerdns — HTTP client for the per-Sovereign PowerDNS
// Authoritative REST API.
//
// Issue #827 (parent epic #825): a franchised Sovereign supports N parent
// zones, NOT one. The Sovereign's bootstrap-kit slot 11 (bp-powerdns 1.2.0+)
// creates the operator's initial parent zones at install time via a Helm
// hook Job. This package is the catalyst-api side of the SAME contract for
// runtime zone additions — the admin-console "Add another parent domain"
// flow (#829) calls catalyst-api, which calls this package, which calls the
// in-cluster PowerDNS REST API.
//
// Idempotency contract:
//   - CreateZone returns ErrZoneAlreadyExists when PowerDNS responds with
//     HTTP 409 Conflict (or 412 Precondition Failed during the gpgsql-
//     backend bootstrap window). Callers treat ErrZoneAlreadyExists as
//     success — the zone exists, end of story.
//   - All other 4xx/5xx errors are surfaced verbatim so the caller can
//     distinguish "your input is wrong" (4xx) from "PowerDNS is unhappy"
//     (5xx) and react appropriately.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the API base URL +
// API-key are constructor inputs; nothing is read from os.Getenv inside the
// client. The handler layer wires production defaults from env vars
// (CATALYST_POWERDNS_API_URL / CATALYST_POWERDNS_API_KEY) so a stock
// catalyst-api Pod with the powerdns-api-credentials Reflector mirror
// "just works."
package powerdns

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

// Client is the catalyst-api → PowerDNS REST API client.
type Client struct {
	// BaseURL — e.g. "http://powerdns.powerdns.svc.cluster.local:8081"
	// or "https://pdns.<sovereign-fqdn>" depending on whether the caller
	// is inside or outside the cluster. Never has a trailing slash.
	BaseURL string
	// APIKey — value of the PowerDNS X-API-Key header. Pulled from the
	// powerdns-api-credentials Secret's `api-key` data key.
	APIKey string
	// ServerID — virtually always "localhost" per the PowerDNS API
	// contract. Operator-overridable for multi-tenant PowerDNS setups.
	ServerID string
	// HTTP client. Caller may inject a custom transport (mTLS, proxy,
	// etc.) before issuing requests.
	HTTP *http.Client
}

// New constructs a Client. baseURL is normalised by trimming trailing
// slashes; serverID defaults to "localhost" when empty.
func New(baseURL, apiKey, serverID string) *Client {
	if serverID == "" {
		serverID = "localhost"
	}
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		APIKey:   apiKey,
		ServerID: serverID,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// ZoneSpec mirrors the create-zone request body.
//
// Only the fields catalyst-api uses are exposed; the full PowerDNS Zone
// schema has many more (rrsets, soa_edit, ...). When a caller needs them,
// add them here rather than letting clients pass arbitrary maps — the
// strict-typed boundary is what makes the idempotency contract testable.
type ZoneSpec struct {
	// Name — apex domain. Trailing dot is added by CreateZone if missing
	// (PowerDNS requires the trailing dot but humans never type one).
	Name string `json:"name"`
	// Kind — "Native" (default), "Master", or "Slave". The gpgsql backend
	// only fully supports Native without additional axfr peer config.
	Kind string `json:"kind,omitempty"`
	// Nameservers — list of NS records, each with trailing dot. Defaults
	// to ns1/2/3.<name>. when empty.
	Nameservers []string `json:"nameservers,omitempty"`
	// DNSSEC — enable per-zone DNSSEC at create time. Pairs with
	// `gpgsql-dnssec=yes` in the PowerDNS additional-config.
	DNSSEC bool `json:"dnssec,omitempty"`
	// APIRectify — auto-run rectify-zone after every API change. Stable
	// at 'true' for Catalyst Sovereigns.
	APIRectify bool `json:"api_rectify,omitempty"`
	// Masters — for Slave zones, the primary masters. Empty for Native.
	Masters []string `json:"masters,omitempty"`
}

// ErrZoneAlreadyExists — PowerDNS returned 409 Conflict (or 412 during
// the gpgsql-backend bootstrap window). The caller treats this as
// idempotent success.
var ErrZoneAlreadyExists = errors.New("zone already exists")

// CreateZone POSTs the spec to /api/v1/servers/<serverID>/zones. Returns
// nil on 201 Created, ErrZoneAlreadyExists on 409/412, or a wrapped
// error on any other status. The body is JSON-encoded with sensible
// defaults so the caller only needs to set Name in the common case.
func (c *Client) CreateZone(ctx context.Context, spec ZoneSpec) error {
	if c.BaseURL == "" {
		return errors.New("powerdns: BaseURL not set")
	}
	if c.APIKey == "" {
		return errors.New("powerdns: APIKey not set")
	}
	if spec.Name == "" {
		return errors.New("powerdns: zone name required")
	}

	// Trailing dot is REQUIRED by the PowerDNS API.
	name := spec.Name
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	// Apply sensible defaults so the caller can pass {Name: "omani.works"}
	// and get a working Native + DNSSEC zone delegated to ns1/2/3.<name>.
	kind := spec.Kind
	if kind == "" {
		kind = "Native"
	}
	nameservers := spec.Nameservers
	if len(nameservers) == 0 {
		bare := strings.TrimSuffix(spec.Name, ".")
		nameservers = []string{
			fmt.Sprintf("ns1.%s.", bare),
			fmt.Sprintf("ns2.%s.", bare),
			fmt.Sprintf("ns3.%s.", bare),
		}
	}

	body := map[string]any{
		"name":        name,
		"kind":        kind,
		"masters":     spec.Masters,
		"nameservers": nameservers,
		"dnssec":      spec.DNSSEC,
		"api_rectify": spec.APIRectify || true, // default true
	}
	if body["masters"] == nil {
		body["masters"] = []string{}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("powerdns: marshal zone body: %w", err)
	}

	u := fmt.Sprintf("%s/api/v1/servers/%s/zones", c.BaseURL, c.ServerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("powerdns: build request: %w", err)
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("powerdns: do request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict, http.StatusPreconditionFailed:
		return ErrZoneAlreadyExists
	default:
		return fmt.Errorf("powerdns: create zone %q status %d: %s",
			spec.Name, resp.StatusCode, truncate(string(respBody), 256))
	}
}

// ZoneExists probes /api/v1/servers/<serverID>/zones/<zone> with HEAD/GET.
// Returns (true, nil) on 200, (false, nil) on 404, (false, err) on
// network failures or unexpected statuses. Used by the admin-console
// add-domain page to render a "this zone is already claimed by your
// Sovereign" hint without forcing the operator through a POST attempt.
func (c *Client) ZoneExists(ctx context.Context, name string) (bool, error) {
	if c.BaseURL == "" {
		return false, errors.New("powerdns: BaseURL not set")
	}
	if c.APIKey == "" {
		return false, errors.New("powerdns: APIKey not set")
	}

	zone := name
	if !strings.HasSuffix(zone, ".") {
		zone = zone + "."
	}

	u := fmt.Sprintf("%s/api/v1/servers/%s/zones/%s", c.BaseURL, c.ServerID, zone)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, fmt.Errorf("powerdns: build request: %w", err)
	}
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("powerdns: do request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("powerdns: get zone %q status %d: %s",
			name, resp.StatusCode, truncate(string(respBody), 256))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
