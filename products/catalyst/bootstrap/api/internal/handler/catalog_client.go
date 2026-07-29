// Package handler — catalog_client.go: EPIC-2 Slice I (#1097) thin
// REST client to catalyst-catalog (slice L, #1148).
//
// The catalyst-api install + preview handlers call into catalyst-catalog
// via this client (proxy mode, NOT direct from the UI):
//
//   UI ─POST /api/v1/sovereigns/{id}/applications─▶ catalyst-api ──┐
//                                                                   │ GET /api/v1/catalog/{name}/versions/{version}
//                                                                   ▼
//                                                          catalyst-catalog
//
// Why proxy: the UI presents one tier-prefixed origin; catalyst-api is
// the single auth-enforcement seam (Keycloak JWT validation already
// happens here). Adding a second cross-origin call from the browser to
// catalyst-catalog would force CORS + duplicate token-handling for no
// architectural gain. catalyst-catalog stays behind the catalyst-api
// Cilium-Gateway HTTPRoute as designed in slice L's DESIGN.md §"Auth
// model".
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the upstream URL is configurable
// (CATALYST_CATALOG_URL env var); the in-cluster service FQDN
// `http://catalyst-catalog.openova-system.svc.cluster.local:8080` is
// the production default but a debug deployment can point this at a
// localhost catalog for offline iteration.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// CatalogBlueprint is the wire shape catalyst-catalog returns. Mirrors
// `core/services/catalyst-catalog/internal/source.Blueprint`.
//
// `Raw` is populated on the GET-by-version endpoint so the install
// handler can validate parameters against `spec.configSchema` without
// a second round-trip to the catalog.
type CatalogBlueprint struct {
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	Visibility      string                 `json:"visibility,omitempty"`
	Card            CatalogBlueprintCard   `json:"card"`
	PlacementSchema *CatalogPlacement      `json:"placementSchema,omitempty"`
	UpgradeFrom     []string               `json:"upgradeFrom,omitempty"`
	UpgradeBlocks   []string               `json:"upgradeBlocks,omitempty"`
	Origin          int                    `json:"origin"`
	Source          string                 `json:"source"`
	Org             string                 `json:"org,omitempty"`
	Raw             map[string]interface{} `json:"raw,omitempty"`

	// DefaultPlacement + AllowedPlacements — #3373 instance-placement
	// class declaration (`spec.defaultPlacement` /
	// `spec.allowedPlacements`). Pass-through from catalyst-catalog so
	// the console's generic advanced selector renders blueprint
	// defaults; field names mirror source.Blueprint verbatim.
	DefaultPlacement  *CatalogDefaultPlacement `json:"defaultPlacement,omitempty"`
	AllowedPlacements []string                 `json:"allowedPlacements,omitempty"`

	// Versions — the per-version index the catalog UI's version-picker
	// consumes. Mirrors `spec.versions` from the upstream Blueprint CR
	// so consumers can render the version dropdown without an extra GET
	// per blueprint. Each entry carries `version` + `chartRef` (OCI ref
	// to the bp-<name>:<version> chart) + `valueSchema` (the
	// per-version configSchema). Per `feedback_no_mvp_no_workarounds.md`
	// this field is REAL data — when the upstream catalog response only
	// carries the headline version, the handler stamps a single-entry
	// list pointing at the canonical OCI chartRef; never a placeholder.
	Versions []CatalogBlueprintVersion `json:"versions"`

	// ChartRef — convenience top-level alias for the headline version's
	// OCI chart reference. Lets clients address the chart without
	// walking the Versions array. Same value as Versions[0].ChartRef
	// when populated.
	ChartRef string `json:"chartRef,omitempty"`

	// SupportedTopologies — #3602 (EPIC #3597) admin-curated placement
	// modes for this catalog entry, surfaced by the editable-catalog
	// overlay. Empty on a pure-seed entry (the console then falls back to
	// PlacementSchema.Modes / the Blueprint's own spec.topology.supported);
	// populated when a sovereign-admin edits the entry's supported set.
	// Additive — only set when an admin edit carries it.
	SupportedTopologies []string `json:"supportedTopologies,omitempty"`
}

// CatalogDefaultPlacement mirrors `spec.defaultPlacement` (#3373) —
// the object form of Application.spec.placement at the class level.
type CatalogDefaultPlacement struct {
	VCluster string   `json:"vcluster,omitempty"`
	Regions  []string `json:"regions,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
}

// CatalogBlueprintVersion is one entry in CatalogBlueprint.Versions.
// Mirrors `spec.versions[]` in the Blueprint CRD. The `valueSchema` is
// the per-version JSON Schema the install handler uses to validate
// `parameters`.
type CatalogBlueprintVersion struct {
	Version     string                 `json:"version"`
	ChartRef    string                 `json:"chartRef,omitempty"`
	ValueSchema map[string]interface{} `json:"valueSchema,omitempty"`
}

// catalogBlueprintChartRef — canonical OCI registry path for blueprint
// charts. Per INVIOLABLE-PRINCIPLES #4 the registry is overridable via
// CATALYST_BLUEPRINT_OCI_BASE; defaults to the production registry the
// blueprint-release.yaml workflow pushes to.
// #5475: this is the registry BASE only. It previously carried a trailing
// `bp-`, which was then concatenated with a blueprint name that already
// begins with `bp-` — producing `ghcr.io/openova-io/bp-bp-alloy:1.0.2` on
// all 80 blueprints, a package that 404s while the real `bp-alloy` has 11
// published tags.
const catalogBlueprintChartRef = "ghcr.io/openova-io/"

// PopulateVersionsAlias stamps Versions + ChartRef from the headline
// version when the upstream response doesn't carry them inline. Called
// by the catalog handlers right before writeJSON. Idempotent: when
// Versions is already populated (upstream supplied it) the function is
// a no-op. Per `feedback_no_mvp_no_workarounds.md` the chartRef is the
// REAL OCI reference assembled from the canonical registry + the
// blueprint name + the headline version — never a placeholder.
func (b *CatalogBlueprint) PopulateVersionsAlias() {
	if b == nil {
		return
	}
	if b.Version == "" {
		return
	}
	if b.ChartRef == "" {
		// Normalise to EXACTLY one `bp-` prefix: catalog names arrive
		// already-qualified (`bp-alloy`), but tolerate a bare name so a
		// future caller cannot silently reintroduce the #5475 doubling.
		chart := b.Name
		if !strings.HasPrefix(chart, "bp-") {
			chart = "bp-" + chart
		}
		b.ChartRef = catalogBlueprintChartRef + chart + ":" + b.Version
	}
	if len(b.Versions) > 0 {
		return
	}
	b.Versions = []CatalogBlueprintVersion{{
		Version:  b.Version,
		ChartRef: b.ChartRef,
	}}
	// When Raw carries spec.configSchema (populated on the GET-by-version
	// endpoint), surface it as the headline version's valueSchema so the
	// UI's install form can read it without a second hop.
	if b.Raw != nil {
		if spec, ok := b.Raw["spec"].(map[string]interface{}); ok {
			if cs, ok := spec["configSchema"].(map[string]interface{}); ok {
				b.Versions[0].ValueSchema = cs
			}
		}
	}
}

// CatalogBlueprintCard mirrors `spec.card` block.
type CatalogBlueprintCard struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tagline     string   `json:"tagline,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	// IconLight / IconDark — #3602 (EPIC #3597) theme-specific icon
	// overrides surfaced by the admin-editable catalog overlay. Empty on
	// a pure-seed entry; populated when a sovereign-admin edits the entry
	// and uploads/links per-theme icons. The console renders IconLight in
	// the light theme and IconDark in the dark theme, falling back to the
	// single `Icon` (then the build-time logo) when a theme icon is
	// absent. Additive — never set unless an admin edit carries them.
	IconLight   string   `json:"iconLight,omitempty"`
	IconDark    string   `json:"iconDark,omitempty"`
	Category    string   `json:"category,omitempty"`
	Family      string   `json:"family,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	License     string   `json:"license,omitempty"`
	Docs        string   `json:"docs,omitempty"`
}

// CatalogPlacement mirrors `spec.placementSchema` (subset).
type CatalogPlacement struct {
	Modes      []string `json:"modes,omitempty"`
	Default    string   `json:"default,omitempty"`
	MinRegions int      `json:"minRegions,omitempty"`
	MaxRegions int      `json:"maxRegions,omitempty"`
}

// CatalogListResponse is the body of GET /api/v1/catalog.
type CatalogListResponse struct {
	Items []CatalogBlueprint `json:"items"`
}

// ErrBlueprintNotFound surfaces a 404 from catalyst-catalog so the
// install handler can return its own 404 to the UI.
var ErrBlueprintNotFound = errors.New("catalog: blueprint not found")

// CatalogClient is a narrow REST client to catalyst-catalog. The caller
// passes the request context through so catalyst-api's per-request
// timeouts / cancellations propagate to the upstream call.
type CatalogClient interface {
	List(ctx context.Context, org, sessionToken string) ([]CatalogBlueprint, error)
	Get(ctx context.Context, name, sessionToken string) (*CatalogBlueprint, error)
	GetVersion(ctx context.Context, name, version, sessionToken string) (*CatalogBlueprint, error)
}

// httpCatalogClient is the production implementation. Tests substitute
// a stub via SetCatalogClient.
type httpCatalogClient struct {
	baseURL    string
	httpClient *http.Client
}

// defaultCatalogURL is the in-cluster service FQDN. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 it is configuration-overridable via
// CATALYST_CATALOG_URL.
const defaultCatalogURL = "http://catalyst-catalog.openova-system.svc.cluster.local:8080"

// NewCatalogClientFromEnv reads CATALYST_CATALOG_URL (defaulting to the
// in-cluster service FQDN) and returns the production client.
func NewCatalogClientFromEnv() CatalogClient {
	base := strings.TrimRight(os.Getenv("CATALYST_CATALOG_URL"), "/")
	if base == "" {
		base = defaultCatalogURL
	}
	return &httpCatalogClient{
		baseURL: base,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// List fetches `/api/v1/catalog?org=<slug>` and returns the items array.
// `sessionToken` is forwarded as a Cookie matching catalyst-catalog's
// configured SessionCookieName when non-empty; otherwise the call is
// anonymous (catalyst-catalog falls through to its AnonymousReads
// policy).
func (c *httpCatalogClient) List(ctx context.Context, org, sessionToken string) ([]CatalogBlueprint, error) {
	u := c.baseURL + "/api/v1/catalog"
	if org != "" {
		u += "?org=" + url.QueryEscape(org)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog list: build request: %w", err)
	}
	c.attachAuth(req, sessionToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog list: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog list: upstream %d %s", resp.StatusCode, readErrSnippet(resp.Body))
	}
	var body CatalogListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("catalog list: decode: %w", err)
	}
	return body.Items, nil
}

// Get fetches `/api/v1/catalog/{name}` (latest visible version).
func (c *httpCatalogClient) Get(ctx context.Context, name, sessionToken string) (*CatalogBlueprint, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("catalog get: name is required")
	}
	u := c.baseURL + "/api/v1/catalog/" + url.PathEscape(name)
	return c.fetchOne(ctx, u, sessionToken)
}

// GetVersion fetches `/api/v1/catalog/{name}/versions/{version}` —
// returns the Blueprint at the requested version. The Raw field is
// populated by catalyst-catalog on this endpoint per slice L's contract.
func (c *httpCatalogClient) GetVersion(ctx context.Context, name, version, sessionToken string) (*CatalogBlueprint, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return nil, errors.New("catalog getVersion: name and version are required")
	}
	u := c.baseURL + "/api/v1/catalog/" + url.PathEscape(name) + "/versions/" + url.PathEscape(version)
	return c.fetchOne(ctx, u, sessionToken)
}

func (c *httpCatalogClient) fetchOne(ctx context.Context, u, sessionToken string) (*CatalogBlueprint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog get: build request: %w", err)
	}
	c.attachAuth(req, sessionToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog get: do request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var bp CatalogBlueprint
		if err := json.NewDecoder(resp.Body).Decode(&bp); err != nil {
			return nil, fmt.Errorf("catalog get: decode: %w", err)
		}
		return &bp, nil
	case http.StatusNotFound:
		return nil, ErrBlueprintNotFound
	default:
		return nil, fmt.Errorf("catalog get: upstream %d %s", resp.StatusCode, readErrSnippet(resp.Body))
	}
}

// attachAuth wires the session token into the upstream request. The
// catalog service reads its session cookie by configurable name. For
// the in-cluster proxy hop we forward as both `Authorization: Bearer
// <tok>` and `Cookie: catalyst_session=<tok>` so the catalog accepts
// whichever pattern its env-config selects.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the credential never reaches
// the terminal: this function does NOT log the token.
func (c *httpCatalogClient) attachAuth(req *http.Request, sessionToken string) {
	if sessionToken == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	// Catalog's default cookie name is `catalyst_session` per the
	// slice L config; we forward under that name. If the deployment
	// overrides it, the operator must set the same name on both sides
	// — unrelated to this client.
	req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: sessionToken})
}

// readErrSnippet returns up to the first 256 bytes of an error body so
// upstream failures surface a short, log-safe context to the caller.
// Truncation is by RUNES of the head; we never echo unbounded upstream
// responses to the catalyst-api log line.
func readErrSnippet(r io.Reader) string {
	buf := make([]byte, 256)
	n, _ := io.ReadFull(io.LimitReader(r, 256), buf)
	out := strings.TrimSpace(string(buf[:n]))
	if out == "" {
		return ""
	}
	return out
}

// ── Wiring on *Handler ───────────────────────────────────────────────────

// SetCatalogClient injects a CatalogClient. main.go calls this once at
// startup; tests inject a stub directly.
func (h *Handler) SetCatalogClient(c CatalogClient) { h.catalogClient = c }

// CatalogClient returns the wired client (or nil if unwired).
func (h *Handler) CatalogClient() CatalogClient { return h.catalogClient }
