// Package handler — endpoint_handler.go: G117.3 catalyst-api endpoint
// CRUD + Gitea-IaC PR pipeline + silent-SSO Launch URL minter +
// multi-instance Application create. Backs the OpenAPI contract at
// docs/api/catalyst-api-openapi.yaml.
//
// The seven routes:
//
//	GET    /catalyst/v1/apps/{id}/endpoints
//	POST   /catalyst/v1/apps/{id}/endpoints
//	PATCH  /catalyst/v1/apps/{id}/endpoints/{name}
//	DELETE /catalyst/v1/apps/{id}/endpoints/{name}
//	GET    /catalyst/v1/apps/{id}/launch-url
//	POST   /catalyst/v1/apps/instances
//	GET    /catalyst/v1/catalog/{blueprint}/instances
//
// Architecture (locked decisions #1-#7 in the G117 brief):
//
//   - Endpoint mutations are PR-pipelined through `gitea.<sov>/<org>/iac`
//     per ADR-0009. catalyst-api opens the PR, runs the three named
//     pre-checks (decision #4), and auto-merges on green. Flux
//     reconciles ~30s after merge.
//
//   - Multi-instance creates require Blueprint.spec.multiInstance.enabled
//     true OR a fresh Application name in the Org's namespace. The
//     handler also honors the optional `topology` override which must
//     be present in Blueprint.spec.topology.supported.
//
//   - Launch URL is server-side built from Blueprint.spec.sso +
//     Blueprint.spec.endpoints[]. We DO NOT mint OIDC tokens here —
//     the URL targets the endpoint's hostname with `prompt=none&kc_idp_hint=catalyst-pin`
//     so the browser's existing KC session handles the SSO bounce
//     silently per decision #3.
//
//   - Application UID (path `{id}` in the OpenAPI) addresses
//     Applications regardless of Org namespace. The handler walks
//     the cluster-wide Application list once per request and matches
//     on UID.
//
// Anti-theater: every handler returns a concrete response that
// validates against the OpenAPI schema (`schema/EndpointPR`,
// `schema/Application`, etc.). No null-guards on never-empty data;
// no `enabled:false` defaults; no scaffold-only paths.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	yamlv3 "gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/controllers/application/admission"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/precheck"
)

// ── Wire shapes (mirror docs/api/catalyst-api-openapi.yaml) ─────────

// endpointMutationRequest is the body of POST /apps/{id}/endpoints
// and PATCH /apps/{id}/endpoints/{name}.
type endpointMutationRequest struct {
	Name       string `json:"name"`
	Hostname   string `json:"hostname,omitempty"`
	Port       int    `json:"port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	TLS        *bool  `json:"tls,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	SSOEnabled *bool  `json:"ssoEnabled,omitempty"`
}

// endpointPRResponse mirrors `schema/EndpointPR`.
type endpointPRResponse struct {
	PRURL           string                  `json:"prURL"`
	Status          string                  `json:"status"`
	PreCheckResults endpointPreCheckResults `json:"preCheckResults"`
}

type endpointPreCheckResults struct {
	Kyverno     string `json:"kyverno"`
	CertManager string `json:"certManager"`
	DNSConflict string `json:"dnsConflict"`
}

// resolvedEndpoint mirrors `schema/ResolvedEndpoint`.
type resolvedEndpoint struct {
	Name              string `json:"name"`
	HostnameTemplate  string `json:"hostnameTemplate"`
	Hostname          string `json:"hostname"`
	Port              int    `json:"port,omitempty"`
	Protocol          string `json:"protocol,omitempty"`
	TLS               bool   `json:"tls,omitempty"`
	Visibility        string `json:"visibility,omitempty"`
	LaunchDefault     bool   `json:"launchDefault,omitempty"`
	SSOEnabled        bool   `json:"ssoEnabled,omitempty"`
	SSOInitPath       string `json:"ssoInitPath,omitempty"`
	SSOShim           bool   `json:"ssoShim,omitempty"`
	Status            string `json:"status"`
	CertificateStatus string `json:"certificateStatus,omitempty"`
	LaunchURL         string `json:"launchURL,omitempty"`
}

// endpointStatusUnresolved — #5389. The Blueprint's hostnameTemplate could
// not be fully substituted (unknown token, or a token that substituted to the
// empty string). The endpoint is reported with an EMPTY hostname + no
// launchURL so no consumer can construct a dead link from it.
const endpointStatusUnresolved = "Unresolved"

// applicationSummary mirrors `schema/ApplicationSummary`.
type applicationSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Blueprint string `json:"blueprint"`
	Org       string `json:"org"`
	Topology  string `json:"topology"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt,omitempty"`
	// Contexts — #3370. The Contexts on a SHAREABLE instance: the
	// first-class child entities consumer applications occupy (for
	// postgres a db, for valkey a keyspace, …). Projected GENERICALLY
	// from the instance's declared IaC values using the Blueprint's
	// contextSchema declaration — no blueprint-specific code. Omitted
	// for non-shareable blueprints.
	Contexts []contextRow `json:"contexts,omitempty"`
}

// contextRow — #3370. One Context on a shareable instance. Field names
// follow the Catalyst wire convention (lowercase camelCase json tags,
// memory feedback_ts_field_casing_vs_go_json_tag).
//
//   - Name/Kind render entity-first as `<kind>/<name>` (db/gitea).
//   - OccupiedBy is the consumer (blueprint id, bp- prefix stripped).
//   - Credential is the reflected credential Secret declared by the
//     context entry (contextSchema `produces: [credentialSecret]`).
//   - Status derives from the materialized IaC: `ready` when the
//     credential Secret exists in every declared consumer namespace,
//     `pending` while reflection is in flight, `declared` when the
//     entry produces nothing checkable.
type contextRow struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	OccupiedBy string `json:"occupiedBy,omitempty"`
	Credential string `json:"credential,omitempty"`
	Status     string `json:"status"`
}

// application mirrors `schema/Application` (Summary plus per-cluster
// detail + endpoints[]).
type application struct {
	applicationSummary
	PerCluster []applicationClusterStatus `json:"perCluster,omitempty"`
	Endpoints  []resolvedEndpoint         `json:"endpoints,omitempty"`

	// Placement — #3373. The instance's placement: the EFFECTIVE
	// value from `status.placement` when the controller has
	// reconciled (source of truth: the Git-committed spec, resolved),
	// falling back to the declared `spec.placement` object before the
	// first reconcile. Omitted for legacy string-form CRs that have
	// no placement signal yet.
	Placement *placementInfo `json:"placement,omitempty"`
}

// placementInfo mirrors the object form of placement on the wire
// (lowercase camelCase json per feedback_ts_field_casing_vs_go_json_tag).
type placementInfo struct {
	VCluster string   `json:"vcluster,omitempty"`
	Regions  []string `json:"regions,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
	Source   string   `json:"source,omitempty"`
}

// placementInfoFromCR extracts the placement view from an Application
// CR: status.placement (effective, post-reconcile) wins; the
// spec.placement OBJECT form is the pre-reconcile fallback; legacy
// string-form specs yield nil (no WHERE signal).
func placementInfoFromCR(u *unstructured.Unstructured) *placementInfo {
	readFrom := func(path ...string) *placementInfo {
		m, ok, err := unstructured.NestedMap(u.Object, path...)
		if err != nil || !ok || m == nil {
			return nil
		}
		out := &placementInfo{}
		if s, ok := m["vcluster"].(string); ok {
			out.VCluster = s
		}
		if s, ok := m["source"].(string); ok {
			out.Source = s
		}
		if items, ok := m["regions"].([]interface{}); ok {
			for _, it := range items {
				if s, ok := it.(string); ok {
					out.Regions = append(out.Regions, s)
				}
			}
		}
		if items, ok := m["clusters"].([]interface{}); ok {
			for _, it := range items {
				if s, ok := it.(string); ok {
					out.Clusters = append(out.Clusters, s)
				}
			}
		}
		if out.VCluster == "" && out.Source == "" && len(out.Regions) == 0 && len(out.Clusters) == 0 {
			return nil
		}
		return out
	}
	if pi := readFrom("status", "placement"); pi != nil {
		return pi
	}
	return readFrom("spec", "placement")
}

type applicationClusterStatus struct {
	Cluster string `json:"cluster"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	HR      string `json:"hr,omitempty"`
	Message string `json:"message,omitempty"`
}

// launchURLResponse mirrors GET /apps/{id}/launch-url body.
type launchURLResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	Endpoint  string `json:"endpoint,omitempty"`
}

// listInstancesResponse / listEndpointsResponse are the items-envelope
// shapes the OpenAPI declares.
type listInstancesResponse struct {
	Items []applicationSummary `json:"items"`
}

type listEndpointsResponse struct {
	Items []resolvedEndpoint `json:"items"`
}

// ── Endpoint precheck wiring (test seam) ────────────────────────────

// EndpointPrecheckDeps is the bundle of lookups + writer the endpoint
// handlers consume. Production main.go wires production implementations;
// tests inject in-memory fakes.
type EndpointPrecheckDeps struct {
	CertLookup    precheck.CertConflictLookup
	DNSLookup     precheck.DNSConflictLookup
	KyvernoLookup precheck.KyvernoLookup
	// ExpectedDNSTarget is the canonical Gateway target hostname for
	// this Sovereign. Used by precheck.CheckDNSConflict; empty means
	// "any pre-existing record is a conflict".
	ExpectedDNSTarget string

	// Writer is the giteapr.Writer wrapping the per-Org IaC repo.
	// Production wires a real one; tests inject a stub via the
	// WriterFactory below.
	WriterFactory func(org string) (*giteapr.Writer, error)

	// DynamicClient returns the cluster client for Application CR
	// reads/writes. Production = rest.InClusterConfig wrapper; tests
	// inject a fake.NewSimpleDynamicClient.
	DynamicClient func() (dynamic.Interface, error)

	// Requester is read by FormatPRBody so the PR body cites the
	// caller. Production reads from the auth.Claims on the request
	// context; tests inject a stable value.
	Requester func(ctx context.Context) string

	// SovereignFQDN is the host suffix used when evaluating endpoint
	// hostname templates and when building launch URLs. Reads
	// SOVEREIGN_FQDN env var by default.
	SovereignFQDN string

	// RegionsCounter returns the number of regions in the active
	// Sovereign CR (`Sovereign.spec.regions`). Used by chooseTopology
	// to pick the multi-region default per locked decision #7
	// (len(Sovereign.spec.regions) > 1 = multi-region).
	//
	// Production wires this to a client-go reader that queries the
	// Sovereign CR via the dynamic client. Tests inject a stub returning
	// a fixed count.
	//
	// When nil, chooseTopology falls back to inspecting SOVEREIGN_REGIONS
	// env var for backward-compat with the W1.B4 ship — this fallback is
	// the env-var path the W1.B4 verifier flagged (#2780), kept ONLY for
	// the rollover window. Production main.go MUST wire the CR-based
	// counter so the env fallback never executes on a live Sovereign.
	RegionsCounter func(ctx context.Context) (int, error)

	// OrgMembership reports whether the caller belongs to the given Org
	// (or is a sovereign-admin / sovereign-operator with cross-Org
	// authority). When nil, the gate falls back to a single-claim check
	// `claims.Org == app-Org` which is sufficient for the chroot dev
	// path but does NOT cover users with multi-Org membership emitted
	// via the `groups` claim. Production main.go SHOULD wire a real
	// implementation that walks claims.Groups.
	OrgMembership func(ctx context.Context, claims *auth.Claims, org string) bool
}

// SetEndpointPrecheckDeps wires the dependency bundle. Tests override
// this with stubs; production main.go calls it once at startup.
func (h *Handler) SetEndpointPrecheckDeps(d EndpointPrecheckDeps) { h.endpointDeps = d }

// EndpointPrecheckDepsForTest returns the wired bundle (for tests).
func (h *Handler) EndpointPrecheckDepsForTest() EndpointPrecheckDeps { return h.endpointDeps }

// inClusterDynamicClient — fallback dynamic client builder when no
// per-deployment kubeconfig is available. Used by endpoint handlers
// because the OpenAPI surface addresses Applications by UID, not by
// (sovereignId, name) — every catalyst-api Pod runs in the cluster
// it serves, so the in-cluster config is the canonical source.
func inClusterDynamicClient() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	return dynamic.NewForConfig(cfg)
}

// dynamicClientOrFallback chooses the wired factory if present, else
// falls back to in-cluster.
func (h *Handler) dynamicClientOrFallback() (dynamic.Interface, error) {
	if h.endpointDeps.DynamicClient != nil {
		return h.endpointDeps.DynamicClient()
	}
	return inClusterDynamicClient()
}

// endpointSovereignFQDN reads the per-Sovereign FQDN — wired override
// wins, else SOVEREIGN_FQDN env, else empty. Named with the package
// prefix to avoid collision with the existing Handler.sovereignFQDN
// in auth_handover.go (different semantics; that one falls back to
// the in-context request host).
func (h *Handler) endpointSovereignFQDN() string {
	if h.endpointDeps.SovereignFQDN != "" {
		return h.endpointDeps.SovereignFQDN
	}
	return strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
}

// ── App-by-UID lookup ───────────────────────────────────────────────

// findApplicationByUID lists Applications across every namespace and
// returns the one whose .metadata.uid matches. Returns ErrAppNotFound
// when no match.
func findApplicationByUID(ctx context.Context, c dynamic.Interface, uid string) (*unstructured.Unstructured, error) {
	key := strings.TrimSpace(uid)
	if key == "" {
		return nil, errAppNotFound
	}
	list, err := c.Resource(ApplicationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// Primary match: metadata.uid (the canonical launch-url key for
	// Org-scoped apps the FE learned from a GET-Application response).
	for i := range list.Items {
		if string(list.Items[i].GetUID()) == key {
			return list.Items[i].DeepCopy(), nil
		}
	}
	// #3374 — fallback match on metadata.name. The AppsPage grid INSTANCE
	// card keys its launch on the Application CR's NAME (the instance
	// identity it projects: sovereignAppItem.ID = app.GetName()), not the
	// uid the AppDetail page carries. Without this fallback, a grid Open
	// click on an Org-scoped instance (e.g. "qa-wp") missed the uid match,
	// fell through to the HR-backed branch with an EMPTY org, and produced a
	// wrong hostname → the operator landed on a login form (the exact
	// silent-SSO bypass #3374 closes). Matching by name resolves the real CR
	// so the correct org + blueprint flow through. uid (a 36-char UUID) and
	// name (a DNS label) never collide, and uid is tried first, so this is a
	// strict superset that only ever resolves MORE valid apps.
	for i := range list.Items {
		if list.Items[i].GetName() == key {
			return list.Items[i].DeepCopy(), nil
		}
	}
	return nil, errAppNotFound
}

var errAppNotFound = errors.New("application not found")

// extractOrgFromApp lifts the Org slug from the Application labels.
// Falls back to the namespace name when no label is set (legacy CRs).
func extractOrgFromApp(app *unstructured.Unstructured) string {
	if app == nil {
		return ""
	}
	if labels := app.GetLabels(); labels != nil {
		if v := labels["catalyst.openova.io/organization"]; v != "" {
			return v
		}
	}
	return app.GetNamespace()
}

// extractBlueprintFromApp returns the blueprint name (without `bp-`
// prefix) from the Application spec.
func extractBlueprintFromApp(app *unstructured.Unstructured) string {
	if app == nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
	return strings.TrimPrefix(v, "bp-")
}

// ── HTTP handlers ───────────────────────────────────────────────────

// HandleListAppEndpoints — GET /catalyst/v1/apps/{id}/endpoints
func (h *Handler) HandleListAppEndpoints(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}
	app, err := findApplicationByUID(r.Context(), client, uid)
	if err != nil {
		writeAppNotFound(w, uid, err)
		return
	}
	bp := extractBlueprintFromApp(app)
	bpDoc, bpErr := h.fetchBlueprint(r.Context(), r, bp)
	if bpErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "blueprint-unavailable",
			"message": bpErr.Error(),
		})
		return
	}
	endpoints := h.resolveEndpoints(r.Context(), client, app, bpDoc)
	writeJSON(w, http.StatusOK, listEndpointsResponse{Items: endpoints})
}

// HandleCreateAppEndpoint — POST /catalyst/v1/apps/{id}/endpoints
func (h *Handler) HandleCreateAppEndpoint(w http.ResponseWriter, r *http.Request) {
	h.mutateAppEndpoint(w, r, precheck.OpCreate)
}

// HandlePatchAppEndpoint — PATCH /catalyst/v1/apps/{id}/endpoints/{name}
func (h *Handler) HandlePatchAppEndpoint(w http.ResponseWriter, r *http.Request) {
	h.mutateAppEndpoint(w, r, precheck.OpUpdate)
}

// HandleDeleteAppEndpoint — DELETE /catalyst/v1/apps/{id}/endpoints/{name}
func (h *Handler) HandleDeleteAppEndpoint(w http.ResponseWriter, r *http.Request) {
	h.mutateAppEndpoint(w, r, precheck.OpDelete)
}

// mutateAppEndpoint is the shared body for create/update/delete.
func (h *Handler) mutateAppEndpoint(w http.ResponseWriter, r *http.Request, op precheck.Operation) {
	uid := chi.URLParam(r, "id")
	pathName := chi.URLParam(r, "name")

	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}
	app, err := findApplicationByUID(r.Context(), client, uid)
	if err != nil {
		writeAppNotFound(w, uid, err)
		return
	}

	// Authz — same role gate as application install (tier-admin or
	// higher) PLUS the G117.3a #2757 per-Org membership check. The
	// realm-role check alone allowed an Org-A tier-admin to mutate an
	// App owned by Org-B by addressing the App UID; the per-Org IaC
	// robot would then PR into the wrong Org's repo. The membership
	// gate closes that hole.
	appOrg := extractOrgFromApp(app)
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":    "forbidden",
				"message": "endpoint mutation requires tier-admin or higher",
			})
			return
		}
		if !h.callerInOrg(r.Context(), claims, appOrg) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":    "forbidden-cross-org",
				"message": fmt.Sprintf("caller is not a member of Organization %q (the Application's Org)", appOrg),
			})
			return
		}
	}

	body := endpointMutationRequest{}
	if op != precheck.OpDelete {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code":    "invalid-body",
				"message": err.Error(),
			})
			return
		}
	} else {
		// DELETE — endpoint name comes from the path.
		body.Name = pathName
	}
	if op == precheck.OpUpdate {
		// Name on path wins for PATCH; allow the body to omit it.
		if body.Name == "" {
			body.Name = pathName
		}
	}

	mut := precheck.Mutation{
		Org:        extractOrgFromApp(app),
		App:        app.GetName(),
		Name:       body.Name,
		Hostname:   body.Hostname,
		Port:       body.Port,
		Protocol:   body.Protocol,
		Visibility: body.Visibility,
		Op:         op,
	}
	if body.TLS != nil {
		mut.TLS = *body.TLS
	}
	if body.SSOEnabled != nil {
		mut.SSOEnabled = *body.SSOEnabled
	}
	if err := precheck.ValidateMutation(mut); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-mutation",
			"message": err.Error(),
		})
		return
	}

	// Run pre-checks BEFORE opening the PR (per locked decision #4).
	bundle := precheck.Run(r.Context(), mut,
		h.endpointDeps.ExpectedDNSTarget,
		h.endpointDeps.CertLookup,
		h.endpointDeps.DNSLookup,
		h.endpointDeps.KyvernoLookup,
	)
	if !bundle.AllPass() {
		// Don't open a known-bad PR. Surface the precheck verdict
		// to the caller so the FE can render a precise error.
		writeJSON(w, http.StatusUnprocessableEntity, endpointPRResponse{
			Status: "failed-precheck",
			PreCheckResults: endpointPreCheckResults{
				Kyverno:     mapResultPass(bundle.Kyverno),
				CertManager: mapResultPass(bundle.CertManager),
				DNSConflict: mapResultPass(bundle.DNSConflict),
			},
		})
		return
	}

	// Build manifest YAML.
	var manifest []byte
	if op != precheck.OpDelete {
		manifest = h.buildEndpointManifest(mut)
	}

	// Hand off to giteapr.Writer.
	if h.endpointDeps.WriterFactory == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "gitea-writer-unwired",
			"message": "endpoint PR pipeline not wired (writer factory missing)",
		})
		return
	}
	writer, wErr := h.endpointDeps.WriterFactory(mut.Org)
	if wErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "gitea-writer-init",
			"message": wErr.Error(),
		})
		return
	}

	requester := ""
	if h.endpointDeps.Requester != nil {
		requester = h.endpointDeps.Requester(r.Context())
	}

	prMut := giteapr.Mutation{
		Org:           mut.Org,
		App:           mut.App,
		EndpointName:  mut.Name,
		ManifestYAML:  manifest,
		Op:            mapPrecheckOp(op),
		CommitMessage: formatCommit(op, mut),
		PRTitle:       formatPRTitle(op, mut),
		PRBody:        giteapr.FormatPRBody(giteapr.Mutation{Org: mut.Org, App: mut.App, EndpointName: mut.Name, Op: mapPrecheckOp(op)}, requester),
	}
	res, err := writer.OpenAndMerge(r.Context(), prMut)
	if err != nil {
		// Surface the partial result so the caller can see the PR URL
		// + per-check state if it was opened before failing.
		writeJSON(w, http.StatusBadGateway, endpointPRResponse{
			PRURL:  res.PRURL,
			Status: string(res.Status),
			PreCheckResults: endpointPreCheckResults{
				Kyverno:     allPass(bundle.Kyverno),
				CertManager: allPass(bundle.CertManager),
				DNSConflict: allPass(bundle.DNSConflict),
			},
		})
		return
	}
	code := http.StatusAccepted
	writeJSON(w, code, endpointPRResponse{
		PRURL:  res.PRURL,
		Status: string(res.Status),
		PreCheckResults: endpointPreCheckResults{
			Kyverno:     allPass(bundle.Kyverno),
			CertManager: allPass(bundle.CertManager),
			DNSConflict: allPass(bundle.DNSConflict),
		},
	})
}

// HandleGetLaunchURL — GET /catalyst/v1/apps/{id}/launch-url?endpoint={name}
func (h *Handler) HandleGetLaunchURL(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	epName := strings.TrimSpace(r.URL.Query().Get("endpoint"))

	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}
	// #3150 — the `{id}` is normally an Application CR's metadata.uid.
	// But every bootstrap-kit app (grafana, harbor, openbao, gitea,
	// keycloak, guacamole, powerdns-admin, …) ships as a HelmRelease with
	// NO companion Application CR — so findApplicationByUID misses and the
	// silent-SSO launch URL 404s, forcing the console's Open button to
	// fall back to the plain externalURL where the app shows its own login
	// form. To close that gap we resolve the blueprint two ways:
	//
	//   (1) Application CR by uid (the original path, Org-scoped apps); or
	//   (2) HR-backed: treat `{id}` as the blueprint/release name
	//       ("grafana" or "bp-grafana") and resolve the Blueprint metadata
	//       directly (fetchBlueprint already chains to the in-cluster
	//       Blueprint CR seeded by the chart). The hostname template for
	//       these Sovereign-singleton apps does not reference an Org
	//       slug, so an empty org is correct.
	app, appErr := findApplicationByUID(r.Context(), client, uid)
	var (
		bp      string
		org     string
		appName string
	)
	if appErr == nil {
		bp = extractBlueprintFromApp(app)
		org = extractOrgFromApp(app)
		appName = app.GetName()
	} else {
		// HR-backed fallback: the id is the blueprint/release name.
		bp = strings.TrimPrefix(strings.TrimSpace(uid), "bp-")
		appName = bp
	}

	bpDoc, bpErr := h.fetchBlueprint(r.Context(), r, bp)
	if bpErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "blueprint-unavailable",
			"message": bpErr.Error(),
		})
		return
	}

	ep := pickEndpoint(bpDoc, epName)
	if ep == nil {
		// No Application CR AND no resolvable Blueprint endpoint → the id
		// names neither an app nor a known bootstrap blueprint. Surface
		// the original app-not-found when the CR lookup is what failed so
		// the error stays debuggable.
		if appErr != nil && bp == "" {
			writeAppNotFound(w, uid, appErr)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{
			"code":    "endpoint-not-found",
			"message": fmt.Sprintf("Blueprint %s has no endpoint %q", bp, epName),
		})
		return
	}
	if !ep.SSOEnabled {
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":    "sso-not-enabled",
			"message": fmt.Sprintf("endpoint %q is not SSO-enabled; silent-SSO URL not available", ep.Name),
		})
		return
	}

	hostname, hostErr := resolveHostnameTemplate(ep.HostnameTemplate, hostnameVars{
		SovereignFQDN: h.endpointSovereignFQDN(),
		OrgSlug:       org,
		AppName:       appName,
		OrgDomain:     h.resolveOrgDomain(r.Context(), client, org),
	})
	if hostErr != nil {
		// #5389 fail LOUD. Pre-fix, an unsupported/typo'd token survived the
		// replacer verbatim and this handler answered 200 with
		// `https://neo4j.{{.orgdomain}}/…` — a well-formed, dead Open button.
		// Answering 409 makes the console show its error path instead of
		// navigating the operator into the void.
		h.log.Warn("launch-url: hostnameTemplate unresolved; refusing to emit a dead URL",
			"app", appName, "org", org, "blueprint", bp, "endpoint", ep.Name,
			"hostnameTemplate", ep.HostnameTemplate, "error", hostErr.Error())
		writeJSON(w, http.StatusConflict, map[string]string{
			"code": "hostname-unresolved",
			"message": fmt.Sprintf("endpoint %q hostnameTemplate %q could not be resolved: %v",
				ep.Name, ep.HostnameTemplate, hostErr),
		})
		return
	}
	tls := true
	if ep.TLS != nil {
		tls = *ep.TLS
	}
	var urlStr string
	if shim := buildSSOShimURL(h.endpointSovereignFQDN(), uid); ep.SSOShim && shim != "" {
		// #3226 — return the server-side shim URL (catalyst-api origin)
		// instead of the app deep-link. window.open() follows the shim's
		// 302 to Keycloak for zero-click parity. The {id} we echo is the
		// same one the FE addressed us with so the shim resolves the same
		// blueprint. When the fqdn is unknown (shim=="") we fall through to
		// the app deep-link so the URL is never host-less.
		urlStr = shim
	} else {
		urlStr = buildLaunchURL(hostname, tls, ep.SSOInitPath)
	}
	expiresAt := time.Now().Add(60 * time.Second).UTC().Format(time.RFC3339)

	writeJSON(w, http.StatusOK, launchURLResponse{
		URL:       urlStr,
		ExpiresAt: expiresAt,
		Endpoint:  ep.Name,
	})
}

// HandleCreateInstance — POST /catalyst/v1/apps/instances
//
// G117 W2.C2 wiring: the handler decodes + sanitises via the locked
// `instances.CreateInstanceRequest` shape, delegates the 4 admission
// gates (multi-instance-disabled, max-per-org-exceeded, name-collision,
// isolation-level-invalid) to `admission.EvaluateCreate`, and writes
// `spec.instanceId`, `spec.isolationLevel`, `spec.namingTemplate` on
// the resulting Application CR per the W2.C2 brief + OpenAPI contract.
func (h *Handler) HandleCreateInstance(w http.ResponseWriter, r *http.Request) {
	var body instances.CreateInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-body",
			"message": err.Error(),
		})
		return
	}
	body.Sanitise()
	if shapeErr := body.ValidateShape(); shapeErr != nil {
		status := http.StatusBadRequest
		if shapeErr.Code == "isolation-level-invalid" {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]string{
			"code":    shapeErr.Code,
			"message": shapeErr.Message,
		})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		// #4937 — an Org-scoped customer session (marketplace→console
		// handover, tier=org-admin) is the authority over its OWN Org: this
		// is the RBAC parity of what the Org console UI offers. Reaching this
		// OrgScopeGuard-allowlisted route on a tenant_kind=org console host
		// already proves a session confined to its own Org (host-anchored via
		// X-Forwarded-Host, which the browser cannot forge) — the same own-Org
		// binding HandleOrgApplicationInstall relies on. So for that path we
		// FORCE the target to the caller's OWN Org namespace (a customer can
		// never create outside it, regardless of the body's `org`) and the
		// Sovereign-tier gate + cross-Org membership check are
		// satisfied-by-construction and skipped.
		//
		// A non-Org-scoped session (operator on the Sovereign console) keeps
		// the existing tier-admin-or-higher + own-Org checks unchanged — ZERO
		// behaviour change for the operator console.
		if ownOrg, scoped := h.orgScopeForRequest(r); scoped && ownOrg != "" {
			if ns := h.orgNamespaceForRequest(r); ns != "" {
				body.Org = ns
			} else {
				body.Org = ownOrg
			}
		} else {
			if !applicationInstallCallerAuthorized(claims) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"code":    "forbidden",
					"message": "instance create requires tier-admin or higher",
				})
				return
			}
			// G117.3a #2757 — must be a member of the target Org.
			if !h.callerInOrg(r.Context(), claims, body.Org) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"code":    "forbidden-cross-org",
					"message": fmt.Sprintf("caller is not a member of Organization %q", body.Org),
				})
				return
			}
		}
	}

	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}

	bpDoc, bpErr := h.fetchBlueprint(r.Context(), r, body.Blueprint)
	if bpErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "blueprint-unavailable",
			"message": bpErr.Error(),
		})
		return
	}

	// Topology gating per locked decision #1+#7.
	// G117.3d #2780: multi-region detection MUST consult
	// Sovereign.spec.regions, not the SOVEREIGN_REGIONS env var.
	multiRegion := h.detectMultiRegion(r.Context())
	chosen, derr := chooseTopology(bpDoc, body.Topology, multiRegion)
	if derr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-topology",
			"message": derr.Error(),
		})
		return
	}

	// Build the existing-applications projection for the admission gates.
	existingList, _ := listAppsInOrg(r.Context(), client, body.Org, body.Blueprint)
	existing := make([]admission.ExistingApplication, 0, len(existingList))
	for i := range existingList {
		instanceID, _, _ := unstructured.NestedString(existingList[i].Object, "spec", "instanceId")
		existing = append(existing, admission.ExistingApplication{
			Name:       existingList[i].GetName(),
			InstanceID: instanceID,
			Blueprint:  extractBlueprintFromApp(&existingList[i]),
		})
	}

	mi := readMultiInstance(bpDoc)
	decision := admission.EvaluateCreate(
		admission.CreateRequest{
			Blueprint:      body.Blueprint,
			Org:            body.Org,
			Name:           body.Name,
			IsolationLevel: body.IsolationLevel,
		},
		admission.BlueprintMultiInstance{Enabled: mi.Enabled, MaxPerOrg: mi.MaxPerOrg},
		existing,
	)
	if rejection := instances.MapDecision(decision); rejection != nil {
		writeJSON(w, rejection.StatusCode, map[string]string{
			"code":    string(rejection.Code),
			"message": rejection.Message,
		})
		return
	}

	seed, err := body.Build(chosen)
	if err != nil {
		// Should not occur after ValidateShape, but be defensive.
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-body",
			"message": err.Error(),
		})
		return
	}
	// #4556 Item 2 — stamp the Sovereign's own FQDN so a bp-agenity instance
	// created via this path gets spec.parameters.sovereignFqdn (the chart
	// derives the openova-MCP catalyst-api URL from it; empty → the mothership
	// console.openova.io). No-op for non-agenity Blueprints.
	seed.SovereignFQDN = h.sovereignFQDN()
	// #4624 — stamp the ORG's public console host (console.<slug>.<pool>,
	// from the tenant registry) so a bp-agenity instance gets
	// spec.parameters.openovaMCP.tenantHost: the OPENOVA_MCP_TENANT_HOST the
	// agent's MCP forwards as X-Tenant-Host must be the Org host, NOT the
	// Sovereign console host derived from sovereignFqdn (that host is not a
	// registered tenant → every agent create_application 404'd, live-proven
	// on hw220 2026-07-04). Empty (mothership / no registry row) ⇒ no stamp,
	// fail-closed. No-op for non-agenity Blueprints.
	seed.OrgConsoleHost = h.orgConsoleHostFor(seed.Namespace)

	// #3598 (EPIC #3597) — ensure the Org/Environment namespace exists
	// BEFORE creating any Application CR. Without this the create races the
	// organization controller's GitOps namespace reconcile and fails with
	// `namespaces "<org>" not found` (the founder-reported "namespace not
	// found"). Ensured here so BOTH the backing-service CRs below and the
	// consumer CR land in a present namespace.
	if nsErr := ensureOrgNamespace(r.Context(), client, body.Org); nsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "namespace-ensure-failed",
			"message": fmt.Sprintf("could not ensure namespace for Organization %q: %v", body.Org, nsErr),
		})
		return
	}

	// #3370 — backing-service journey. BEFORE creating the consumer:
	// for each selection, either auto-create the backing service as its
	// OWN instance-application (own card; default) or append a Context
	// to the named existing instance's IaC (reuse). Both return the
	// spec.dependsOn entries the consumer CR is created with, so the
	// Flux wiring is present from the first reconcile.
	dependsOnEntries, backingErr := h.wireBackingServices(r.Context(), client, &body)
	if backingErr != nil {
		writeJSON(w, backingErr.status, map[string]string{
			"code":    backingErr.code,
			"message": backingErr.message,
		})
		return
	}

	obj := newApplicationCRFromSeed(seed)
	// #3370 — stamp the resolved Blueprint version: the CRD requires an
	// exact semver on blueprintRef.version (admission rejected every CR
	// from the old version-less seed on a real apiserver).
	if v := strings.TrimSpace(bpDoc.Version); v != "" {
		_ = unstructured.SetNestedField(obj.Object, v, "spec", "blueprintRef", "version")
	}
	if len(dependsOnEntries) > 0 {
		_ = unstructured.SetNestedSlice(obj.Object, dependsOnEntries, "spec", "dependsOn")
	}
	// #3830 — create into the slugged namespace, matching the CR's
	// metadata.namespace (newApplicationCRFromSeed) and the namespace
	// ensureOrgNamespace created above. body.Org (the FQDN) stays the Org
	// identity in user-facing messages + labels.
	created, err := client.Resource(ApplicationGVR()).Namespace(orgNamespace(body.Org)).Create(
		r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Belt-and-suspenders — admission gate should have caught
			// this; if it slipped past (race), return the same code
			// the admission gate would have returned.
			writeJSON(w, http.StatusConflict, map[string]string{
				"code":    string(admission.CodeNameCollision),
				"message": fmt.Sprintf("Application %q already exists in Org %s", body.Name, body.Org),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"code":    "create-failed",
			"message": err.Error(),
		})
		return
	}

	// #3687 (fold #3694) — the Application CR's authoring home is Git.
	// Commit the desired-state CR into the per-Org `iac` repo at
	// `applications/<name>.yaml` so Flux reconciles it and a hand
	// `git push` round-trips (the canonical representation stops being
	// etcd-only). Best-effort: a missing Gitea backend (chroot/CI) or a
	// write failure does NOT fail the create — the etcd projection above
	// already succeeded and keeps the app-list warm while the Flux loop
	// catches up. We commit `obj` (the clean desired state) rather than
	// `created` (which carries server-populated status/managedFields).
	if committed, gErr := h.commitApplicationCRToGit(r.Context(), body.Org, obj); gErr != nil {
		h.log.Warn("application IaC git commit failed (etcd projection still created)",
			"org", body.Org, "application", body.Name, "error", gErr)
	} else if committed {
		h.log.Info("application IaC committed to Gitea",
			"org", body.Org, "application", body.Name,
			"path", applicationManifestPath(body.Name))
	}

	resp := application{
		applicationSummary: applicationSummary{
			ID:        string(created.GetUID()),
			Name:      created.GetName(),
			Blueprint: body.Blueprint,
			Org:       body.Org,
			Topology:  chosen,
			Status:    "Pending",
			CreatedAt: created.GetCreationTimestamp().UTC().Format(time.RFC3339),
		},
		// #3373 — echo the instance placement the CR was created
		// with (nil when the user silently accepted the Blueprint
		// defaults; the controller's status.placement carries the
		// resolved effective value after the first reconcile).
		Placement: placementInfoFromCR(created),
	}
	writeJSON(w, http.StatusCreated, resp)
}

// backingError is the structured error wireBackingServices maps to the
// HTTP response.
type backingError struct {
	status  int
	code    string
	message string
}

// wireBackingServices — #3370 provisioning journey, both modes.
//
// DEFAULT (mode=create): the required backing service is auto-created
// as its OWN instance-application — an Application CR named
// `<consumer>-<backing-slug>` in the same Org, whose IaC values declare
// ONE Context for this consumer per the backing Blueprint's
// contextSchema. The operator sees TWO new cards.
//
// ADVANCED (mode=reuse): NO new backing application. The flow appends
// the consumer's Context entry to the EXISTING instance's declared IaC
// (Application spec.parameters[valuesKey] — the controller re-renders
// and Git-commits it on the next reconcile; Flux materializes the
// Context + reflected credential). The operator sees ONE new card.
//
// Both modes return the consumer's spec.dependsOn entries (backing
// Application name/namespace + the occupied Context as <kind>/<name>)
// so the application-controller stamps Flux dependsOn on every HR it
// renders for the consumer.
//
// Bootstrap-owned reuse targets are rejected with a structured 409:
// their Context declarations live in the bootstrap-kit slot values in
// Git, not in the Application CR — appending here would render a row
// whose materialization never happens.
func (h *Handler) wireBackingServices(ctx context.Context, client dynamic.Interface, body *instances.CreateInstanceRequest) ([]interface{}, *backingError) {
	if len(body.Backing) == 0 {
		return nil, nil
	}
	// #3830 — slugged namespace for this Org (body.Org is the FQDN Org
	// identity). The reflected-credential target namespace, the backing CR
	// create, and the consumer's dependsOn pointer all use this so they
	// agree with where the consumer + backing CRs actually live.
	orgNS := orgNamespace(body.Org)
	dependsOn := make([]interface{}, 0, len(body.Backing))
	for _, sel := range body.Backing {
		bpFull := sel.Blueprint
		if !strings.HasPrefix(bpFull, "bp-") {
			bpFull = "bp-" + bpFull
		}
		slug := strings.TrimPrefix(bpFull, "bp-")

		ctxSchema := h.contextSchemaFor(ctx, bpFull)
		if ctxSchema == nil {
			return nil, &backingError{
				status:  http.StatusUnprocessableEntity,
				code:    "backing-not-shareable",
				message: fmt.Sprintf("Blueprint %s declares no shareable contextSchema — it cannot serve as a backing service", bpFull),
			}
		}

		// The Context entry — the canonical context shape every
		// shareable chart materializes from (the bp-postgres
		// databases[] machinery, generalized by declaration only).
		contextName := body.Name
		credential := fmt.Sprintf("%s-%s-credential", body.Name, ctxSchema.Kind)
		entry := map[string]interface{}{
			"name":  contextName,
			"owner": body.Name,
			"consumer": map[string]interface{}{
				"blueprint": "bp-" + strings.TrimPrefix(body.Blueprint, "bp-"),
				"mode":      "shared",
			},
			"reflect": map[string]interface{}{
				"secretName": credential,
				// #3830 — reflect the credential into the slugged consumer
				// namespace (where the consumer CR + its pods live).
				"namespaces": []interface{}{orgNS},
			},
		}

		switch sel.Mode {
		case "", "create":
			backingName := fmt.Sprintf("%s-%s", body.Name, slug)
			backing := instances.CreateInstanceRequest{
				Blueprint: bpFull,
				Org:       body.Org,
				Name:      backingName,
				Values: map[string]interface{}{
					ctxSchema.ValuesKey: []interface{}{entry},
				},
			}
			bpDoc, bpErr := h.resolveBlueprintMeta(ctx, bpFull, "")
			if bpErr != nil {
				return nil, &backingError{status: http.StatusServiceUnavailable, code: "blueprint-unavailable", message: bpErr.Error()}
			}
			backingTopology := ""
			if bpDoc != nil && bpDoc.Topology != nil {
				if h.detectMultiRegion(ctx) {
					backingTopology = bpDoc.Topology.Defaults.MultiRegion
				} else {
					backingTopology = bpDoc.Topology.Defaults.SingleRegion
				}
			}
			backingSeed, serr := backing.Build(backingTopology)
			if serr != nil {
				return nil, &backingError{status: http.StatusBadRequest, code: "backing-invalid", message: serr.Error()}
			}
			backingObj := newApplicationCRFromSeed(backingSeed)
			// Stamp the backing Blueprint's resolved version — the CRD
			// requires an exact semver on blueprintRef.version (#3370,
			// same admission contract as the consumer CR below).
			if bpDoc != nil {
				if v := strings.TrimSpace(bpDoc.Version); v != "" {
					_ = unstructured.SetNestedField(backingObj.Object, v, "spec", "blueprintRef", "version")
				}
			}
			if _, cerr := client.Resource(ApplicationGVR()).Namespace(orgNS).Create(ctx, backingObj, metav1.CreateOptions{}); cerr != nil {
				if !apierrors.IsAlreadyExists(cerr) {
					return nil, &backingError{status: http.StatusInternalServerError, code: "backing-create-failed", message: cerr.Error()}
				}
			}
			// #3687 (fold #3694) — the auto-created backing instance is its
			// OWN Application; commit its desired-state CR to the per-Org
			// `iac` repo so the new card's IaC is Git-resident too.
			if _, gErr := h.commitApplicationCRToGit(ctx, body.Org, backingObj); gErr != nil {
				h.log.Warn("backing-service Application IaC git commit failed (etcd projection still created)",
					"org", body.Org, "application", backingObj.GetName(), "error", gErr)
			}
			dependsOn = append(dependsOn, map[string]interface{}{
				"name":      backingName,
				"namespace": orgNS,
				"context":   ctxSchema.Kind + "/" + contextName,
			})

		case "reuse":
			target, terr := h.findInstanceByName(ctx, client, bpFull, sel.Instance)
			if terr != nil {
				return nil, &backingError{
					status:  http.StatusNotFound,
					code:    "backing-instance-not-found",
					message: fmt.Sprintf("no %s instance named %q", bpFull, sel.Instance),
				}
			}
			if b, _, _ := unstructured.NestedBool(target.Object, "spec", "bootstrap"); b {
				return nil, &backingError{
					status:  http.StatusConflict,
					code:    "backing-bootstrap-owned",
					message: fmt.Sprintf("instance %q is bootstrap-owned — its Contexts are declared in the bootstrap-kit slot values in Git; add the Context there", sel.Instance),
				}
			}
			// Append the Context entry to the instance's declared IaC.
			params, _, _ := unstructured.NestedMap(target.Object, "spec", "parameters")
			if params == nil {
				params = map[string]interface{}{}
			}
			entries, _ := params[ctxSchema.ValuesKey].([]interface{})
			for _, e := range entries {
				if em, ok := e.(map[string]interface{}); ok {
					if n, _ := em["name"].(string); n == contextName {
						return nil, &backingError{
							status:  http.StatusConflict,
							code:    "context-exists",
							message: fmt.Sprintf("instance %q already declares Context %s/%s", sel.Instance, ctxSchema.Kind, contextName),
						}
					}
				}
			}
			params[ctxSchema.ValuesKey] = append(entries, entry)
			if err := unstructured.SetNestedMap(target.Object, params, "spec", "parameters"); err != nil {
				return nil, &backingError{status: http.StatusInternalServerError, code: "context-write-failed", message: err.Error()}
			}
			if _, uerr := client.Resource(ApplicationGVR()).Namespace(target.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{}); uerr != nil {
				return nil, &backingError{status: http.StatusInternalServerError, code: "context-write-failed", message: uerr.Error()}
			}
			// #3687 (fold #3694) — the appended Context is "the declared
			// IaC": commit the reused instance's updated CR (now carrying
			// the new Context in spec.parameters) to its per-Org `iac` repo
			// so the Context delta lands in Git, not only etcd.
			if _, gErr := h.commitApplicationCRToGit(ctx, target.GetNamespace(), target); gErr != nil {
				h.log.Warn("Context-append Application IaC git commit failed (etcd projection still applied)",
					"org", target.GetNamespace(), "application", target.GetName(), "error", gErr)
			}
			dependsOn = append(dependsOn, map[string]interface{}{
				"name":      target.GetName(),
				"namespace": target.GetNamespace(),
				"context":   ctxSchema.Kind + "/" + contextName,
			})
		}
	}
	return dependsOn, nil
}

// findInstanceByName resolves an Application CR by instance name +
// blueprint across every namespace (the reuse selector lists instances
// cluster-wide; an Org member reusing a foreign-Org instance is gated
// by the credential reflection, not by this lookup).
func (h *Handler) findInstanceByName(ctx context.Context, client dynamic.Interface, bpFull, name string) (*unstructured.Unstructured, error) {
	list, err := client.Resource(ApplicationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		item := &list.Items[i]
		if item.GetName() != name {
			continue
		}
		bpName, _, _ := unstructured.NestedString(item.Object, "spec", "blueprintRef", "name")
		if bpName == bpFull {
			return item.DeepCopy(), nil
		}
	}
	return nil, errAppNotFound
}

// HandleListBlueprintInstances — GET /catalyst/v1/catalog/{blueprint}/instances
func (h *Handler) HandleListBlueprintInstances(w http.ResponseWriter, r *http.Request) {
	bp := strings.TrimPrefix(chi.URLParam(r, "blueprint"), "bp-")
	orgFilter := strings.TrimSpace(r.URL.Query().Get("org"))

	// #4937 — an Org-scoped customer session (marketplace→console handover,
	// tier=org-admin) is confined to its OWN Organization. It gets a 200
	// listing of ONLY its own instances; a Sovereign-admin / operator session
	// is unaffected (scoped=false) and keeps its cluster-wide reach — ZERO
	// behaviour change for the operator console.
	//
	// Confinement is Kubernetes-authoritative: when the request host resolves
	// to the caller's Org namespace we list ONLY that namespace (listNS),
	// which is robust to the `catalyst.openova.io/organization` label differing
	// across install paths (slug vs. real `org-<uuid>`). When the namespace
	// can't be resolved (claims-only scope, no registry) we fall back to the
	// slug label filter, which still never leaks another Org.
	ownOrg, scoped := h.orgScopeForRequest(r)
	listNS := ""
	if scoped {
		if ownOrg == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":    "org-scope-unresolved",
				"message": "could not resolve the Organization for this Org-scoped session",
			})
			return
		}
		if orgFilter != "" && !strings.EqualFold(orgFilter, ownOrg) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":    "forbidden-cross-org",
				"message": fmt.Sprintf("this Organization session cannot list instances for Organization %q", orgFilter),
			})
			return
		}
		orgFilter = ownOrg
		listNS = h.orgNamespaceForRequest(r)
	}

	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}

	list, err := client.Resource(ApplicationGVR()).Namespace(listNS).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"code":    "list-failed",
			"message": err.Error(),
		})
		return
	}

	// #3370 — resolve the Blueprint's shareability declaration ONCE so
	// every row's Contexts project from the same contextSchema. nil =
	// non-shareable blueprint = no Contexts on any row.
	ctxSchema := h.contextSchemaFor(r.Context(), "bp-"+bp)

	out := []applicationSummary{}
	for i := range list.Items {
		item := &list.Items[i]
		blueprint := extractBlueprintFromApp(item)
		if blueprint != bp {
			continue
		}
		org := extractOrgFromApp(item)
		// When the listing is already namespace-confined (listNS != "", the
		// #4937 Org-scoped path) the org-label filter is skipped: the label
		// may legitimately be the real `org-<uuid>` namespace rather than the
		// slug, and the namespace scope is the authoritative boundary. The
		// unconfined operator listing (listNS == "") still honours ?org=.
		if listNS == "" && orgFilter != "" && !strings.EqualFold(org, orgFilter) {
			continue
		}
		params, _, _ := unstructured.NestedMap(item.Object, "spec", "parameters")
		out = append(out, applicationSummary{
			ID:        string(item.GetUID()),
			Name:      item.GetName(),
			Blueprint: blueprint,
			Org:       org,
			Topology:  readTopology(item),
			Status:    readPhase(item),
			CreatedAt: item.GetCreationTimestamp().UTC().Format(time.RFC3339),
			Contexts:  h.projectContexts(r.Context(), client, ctxSchema, params),
		})
	}

	// #3188 (hw129/hw130 round-1): bootstrap-kit installs of this
	// blueprint ship as Flux HelmReleases. Since #3370 those carry a
	// chart-self-registered Application CR (bp-postgres 0.1.6
	// bootstrapOwned) — already projected above — but the CR only
	// materialises on the first post-bootstrap upgrade after the
	// Application CRD lands, so the HR projection stays as the GENERIC
	// fallback for not-yet-registered installs. Dedup is by the HR's
	// spec.releaseName (the instance identity the self-registered CR is
	// named after), so an adopted install never renders twice.
	// Org-scoped filtering: bootstrap HRs are platform-owned, so any
	// explicit ?org= filter excludes them.
	if orgFilter == "" {
		out = append(out, h.bootstrapHRInstances(r.Context(), client, bp, ctxSchema, out)...)
	}
	writeJSON(w, http.StatusOK, listInstancesResponse{Items: out})
}

// bootstrapHRInstances projects Flux HelmReleases whose chart is
// bp-<blueprint> into applicationSummary rows — the GENERIC fallback
// for bootstrap-kit installs whose chart-self-registered Application CR
// (#3370) hasn't materialised yet (the CR renders on the first
// post-bootstrap upgrade after the Application CRD lands). `existing`
// suppresses duplicates: the row identity is the HR's spec.releaseName
// (the instance name the self-registered CR is also named after, e.g.
// HR bp-postgres-shared → releaseName shared-pg → CR shared-pg).
func (h *Handler) bootstrapHRInstances(ctx context.Context, client dynamic.Interface, bp string, ctxSchema *contextSchemaDecl, existing []applicationSummary) []applicationSummary {
	hrs, err := client.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		// Best-effort projection: the Application-CR rows already in
		// `existing` stay authoritative; an HR list failure must not
		// fail the endpoint.
		return nil
	}
	seen := map[string]struct{}{}
	for _, e := range existing {
		seen[strings.ToLower(e.Name)] = struct{}{}
	}
	rows := []applicationSummary{}
	for i := range hrs.Items {
		hr := &hrs.Items[i]
		chart, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
		if chart != "bp-"+bp {
			continue
		}
		// Instance identity: spec.releaseName (the Helm release IS the
		// instance — shared-pg). Falls back to the HR name with the
		// bp- prefix stripped (Flux default: release name = HR name).
		name, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
		if name == "" {
			name = strings.TrimPrefix(hr.GetName(), "bp-")
		}
		if _, dup := seen[strings.ToLower(name)]; dup {
			continue
		}
		status := "NotReady"
		conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cm["type"] == "Ready" && cm["status"] == "True" {
				status = "Ready"
				break
			}
		}
		// #3370 — the HR's spec.values is the SAME Git-committed IaC
		// the self-registered CR carries in spec.parameters, so the
		// generic Contexts projection applies identically here.
		values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
		rows = append(rows, applicationSummary{
			ID:        string(hr.GetUID()),
			Name:      name,
			Blueprint: bp,
			Org:       "platform",
			Topology:  readTopologyFromValues(values),
			Status:    status,
			CreatedAt: hr.GetCreationTimestamp().UTC().Format(time.RFC3339),
			Contexts:  h.projectContexts(ctx, client, ctxSchema, values),
		})
	}
	return rows
}

// ── #3370 — generic Contexts projection ─────────────────────────────
//
// One mechanism platform-wide: a SHAREABLE Blueprint declares a
// contextSchema (kind label + valuesKey + needs/produces); its
// instances declare Context entries as Git-committed IaC under
// values[valuesKey] (the bp-postgres databases[] machinery shape:
// name + consumer.blueprint + reflect.secretName + reflect.namespaces).
// The projection below reads ONLY that declaration — adding a new
// shareable blueprint (valkey keyspaces, kafka topics, seaweedfs
// buckets) requires zero code here.

// secretGVR — core/v1 Secrets, read for the context-status check.
var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// contextSchemaDecl mirrors Blueprint.spec.contextSchema (#3370).
type contextSchemaDecl struct {
	Kind      string   `yaml:"kind" json:"kind"`
	ValuesKey string   `yaml:"valuesKey" json:"valuesKey"`
	Needs     []string `yaml:"needs" json:"needs"`
	Produces  []string `yaml:"produces" json:"produces"`
}

// contextSchemaFor resolves the contextSchema declaration for a
// Blueprint (bp- prefix optional — callers address blueprints both
// ways). Resolution order: the EMBEDDED build-time catalog FIRST
// (blueprints.json — in-binary, zero network, the same SHA-pinned
// blueprint.yaml truth the catalog-seed lockstep guards), then the
// catalog/Blueprint-CR chain (covers org-authored blueprints, which
// pay the network dial). Order matters: the listing handlers call this
// per unique blueprint inside latency-budgeted paths (the
// /sovereign/apps deps block carries a 10s context), and each chained
// catalog miss is a multi-second dial — embedded-first keeps the hot
// path O(memory). nil = not shareable.
func (h *Handler) contextSchemaFor(ctx context.Context, blueprint string) *contextSchemaDecl {
	bare := strings.TrimPrefix(strings.TrimSpace(blueprint), "bp-")
	if bare == "" {
		return nil
	}
	full := "bp-" + bare
	if bp, ok := catalog.BlueprintByID(full); ok && bp.Shareable && bp.ContextSchema != nil && bp.ContextSchema.Kind != "" {
		return &contextSchemaDecl{
			Kind:      bp.ContextSchema.Kind,
			ValuesKey: bp.ContextSchema.ValuesKey,
			Needs:     bp.ContextSchema.Needs,
			Produces:  bp.ContextSchema.Produces,
		}
	}
	for _, candidate := range []string{full, bare} {
		meta, err := h.resolveBlueprintMeta(ctx, candidate, "")
		if err != nil || meta == nil {
			continue
		}
		if meta.Shareable && meta.ContextSchema != nil && meta.ContextSchema.Kind != "" {
			return meta.ContextSchema
		}
	}
	return nil
}

// projectContexts projects the Context rows of one instance from its
// declared IaC values (HR spec.values or Application spec.parameters —
// the same bytes). Generic over the contextSchema declaration.
func (h *Handler) projectContexts(ctx context.Context, client dynamic.Interface, ctxSchema *contextSchemaDecl, values map[string]interface{}) []contextRow {
	if ctxSchema == nil || ctxSchema.ValuesKey == "" || values == nil {
		return nil
	}
	entriesRaw, ok := values[ctxSchema.ValuesKey].([]interface{})
	if !ok || len(entriesRaw) == 0 {
		return nil
	}
	rows := make([]contextRow, 0, len(entriesRaw))
	for _, e := range entriesRaw {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := em["name"].(string)
		if name == "" {
			continue
		}
		row := contextRow{Name: name, Kind: ctxSchema.Kind}
		if consumer, ok := em["consumer"].(map[string]interface{}); ok {
			if cbp, ok := consumer["blueprint"].(string); ok {
				row.OccupiedBy = strings.TrimPrefix(cbp, "bp-")
			}
		}
		var reflectNamespaces []string
		if reflect, ok := em["reflect"].(map[string]interface{}); ok {
			row.Credential, _ = reflect["secretName"].(string)
			if nss, ok := reflect["namespaces"].([]interface{}); ok {
				for _, n := range nss {
					if s, ok := n.(string); ok && s != "" {
						reflectNamespaces = append(reflectNamespaces, s)
					}
				}
			}
		}
		row.Status = h.contextStatus(ctx, client, row.Credential, reflectNamespaces)
		rows = append(rows, row)
	}
	return rows
}

// contextStatus derives a Context's status from its materialized IaC:
// `ready` when the declared credential Secret is present in every
// consumer namespace it reflects into; `pending` while reflection is in
// flight (or the namespaces don't exist yet); `declared` when the entry
// produces nothing checkable (no reflected credential).
func (h *Handler) contextStatus(ctx context.Context, client dynamic.Interface, credential string, namespaces []string) string {
	if credential == "" || len(namespaces) == 0 {
		return "declared"
	}
	if client == nil {
		return "pending"
	}
	for _, ns := range namespaces {
		if _, err := client.Resource(secretGVR).Namespace(ns).Get(ctx, credential, metav1.GetOptions{}); err != nil {
			return "pending"
		}
	}
	return "ready"
}

// readTopologyFromValues lifts the conventional `topology.mode` knob
// from an instance's declared IaC values; "singleton" when absent.
func readTopologyFromValues(values map[string]interface{}) string {
	if values != nil {
		if topo, ok := values["topology"].(map[string]interface{}); ok {
			if mode, ok := topo["mode"].(string); ok && mode != "" {
				return mode
			}
		}
	}
	return "singleton"
}

// ── Helpers ─────────────────────────────────────────────────────────

func writeAppNotFound(w http.ResponseWriter, uid string, err error) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"code":    "application-not-found",
		"message": fmt.Sprintf("no Application with id %q (%s)", uid, err.Error()),
	})
}

func mapPrecheckOp(op precheck.Operation) giteapr.Op {
	switch op {
	case precheck.OpCreate:
		return giteapr.OpCreate
	case precheck.OpUpdate:
		return giteapr.OpUpdate
	case precheck.OpDelete:
		return giteapr.OpDelete
	}
	return giteapr.OpUnknown
}

func mapResultPass(r precheck.Result) string {
	if r.Pass {
		return "pass"
	}
	return "fail"
}

func allPass(r precheck.Result) string {
	if r.Pass {
		return "pass"
	}
	return "fail"
}

// formatCommit builds a stable conventional-commit-shaped message for
// the per-PR commit on the IaC repo.
func formatCommit(op precheck.Operation, m precheck.Mutation) string {
	verb := "update"
	switch op {
	case precheck.OpCreate:
		verb = "add"
	case precheck.OpDelete:
		verb = "remove"
	}
	return fmt.Sprintf("feat(endpoint): %s %s/%s/%s", verb, m.Org, m.App, m.Name)
}

func formatPRTitle(op precheck.Operation, m precheck.Mutation) string {
	verb := "update"
	switch op {
	case precheck.OpCreate:
		verb = "add"
	case precheck.OpDelete:
		verb = "remove"
	}
	return fmt.Sprintf("endpoint(%s): %s/%s/%s", verb, m.Org, m.App, m.Name)
}

// buildEndpointManifest emits the per-endpoint YAML the Flux
// kustomization picks up.
func (h *Handler) buildEndpointManifest(m precheck.Mutation) []byte {
	doc := map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1",
		"kind":       "Endpoint",
		"metadata": map[string]interface{}{
			"name":      m.Name,
			"namespace": m.Org,
			"labels": map[string]interface{}{
				"app.kubernetes.io/instance":        m.App,
				"app.kubernetes.io/managed-by":      "catalyst-api",
				"catalyst.openova.io/organization":  m.Org,
				"catalyst.openova.io/endpoint-name": m.Name,
			},
		},
		"spec": map[string]interface{}{
			"applicationRef": m.App,
			"hostname":       m.Hostname,
			"port":           portOrDefault(m.Port, m.Protocol),
			"protocol":       protocolOrDefault(m.Protocol),
			"tls":            m.TLS,
			"visibility":     visibilityOrDefault(m.Visibility),
			"ssoEnabled":     m.SSOEnabled,
		},
	}
	out, err := yamlv3.Marshal(doc)
	if err != nil {
		return []byte("# marshal failed: " + err.Error() + "\n")
	}
	return out
}

func portOrDefault(p int, proto string) int {
	if p > 0 {
		return p
	}
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "http":
		return 80
	default:
		return 443
	}
}

func protocolOrDefault(p string) string {
	if v := strings.ToLower(strings.TrimSpace(p)); v != "" {
		return v
	}
	return "https"
}

func visibilityOrDefault(v string) string {
	if vv := strings.ToLower(strings.TrimSpace(v)); vv != "" {
		return vv
	}
	return "public"
}

// ── Blueprint metadata helpers ──────────────────────────────────────

// blueprintMeta is the trimmed projection of Blueprint.spec the
// endpoint handlers care about. We unmarshal from the catalog's `Raw`
// map rather than introducing a structured CRD type.
type blueprintMeta struct {
	// Version — Blueprint.spec.version. Stamped onto created
	// Application CRs' blueprintRef.version (#3370 — the CRD requires
	// exact semver; the old create path omitted it and every
	// console-created CR failed admission on a real apiserver).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	Endpoints     []endpointDecl     `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
	SSO           *ssoDecl           `yaml:"sso,omitempty" json:"sso,omitempty"`
	MultiInstance *multiInstanceDecl `yaml:"multiInstance,omitempty" json:"multiInstance,omitempty"`
	Topology      *topologyDecl      `yaml:"topology,omitempty" json:"topology,omitempty"`

	// Shareable + ContextSchema — #3370. The multi-application-reuse
	// declaration every generic surface (catalog badge, Contexts tab,
	// reuse selector) renders from.
	Shareable     bool               `yaml:"shareable,omitempty" json:"shareable,omitempty"`
	ContextSchema *contextSchemaDecl `yaml:"contextSchema,omitempty" json:"contextSchema,omitempty"`
}

type endpointDecl struct {
	Name             string `yaml:"name" json:"name"`
	HostnameTemplate string `yaml:"hostnameTemplate" json:"hostnameTemplate"`
	Port             int    `yaml:"port,omitempty" json:"port,omitempty"`
	Protocol         string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	TLS              *bool  `yaml:"tls,omitempty" json:"tls,omitempty"`
	Visibility       string `yaml:"visibility,omitempty" json:"visibility,omitempty"`
	LaunchDefault    bool   `yaml:"launchDefault,omitempty" json:"launchDefault,omitempty"`
	SSOEnabled       bool   `yaml:"ssoEnabled,omitempty" json:"ssoEnabled,omitempty"`
	// SSOInitPath — #3150. The app-local path that *initiates* the OIDC
	// login dance (e.g. Grafana `/login/generic_oauth`, Harbor
	// `/c/oidc/login`). When non-empty, buildLaunchURL targets
	// `https://<host><ssoInitPath>` instead of the app root, so the
	// launch lands the browser straight on the app's OIDC-init route and
	// the silent KC bounce completes without the app showing its own
	// login form. The `kc_idp_hint=catalyst-pin` PIN hint is NOT appended
	// here — it already lives baked into the app's OIDC `auth_url`
	// (synced via the app's *-sso-oidc-credentials ExternalSecret). Empty
	// → legacy behaviour (app root + prompt=none&kc_idp_hint query).
	SSOInitPath string `yaml:"ssoInitPath,omitempty" json:"ssoInitPath,omitempty"`

	// SSOShim — #3226. When true, the silent-SSO launch URL is NOT the
	// app's own host/ssoInitPath but the catalyst-api server-side shim
	// (`https://api.<fqdn>/catalyst/v1/apps/<id>/openbao-sso-init`). The
	// shim asks Vault for the OIDC auth_url and 302s the browser to
	// Keycloak — the zero-click parity OpenBao's client-side SPA can't
	// achieve via a static ssoInitPath alone. ssoInitPath stays declared
	// because the shim uses it as its own deep-link fallback when the
	// auth_url POST fails (Vault sealed / oidc not mounted). Empty/false
	// → the ssoInitPath behaviour above applies unchanged.
	SSOShim bool `yaml:"ssoShim,omitempty" json:"ssoShim,omitempty"`
}

type ssoDecl struct {
	Realm       string `yaml:"realm,omitempty" json:"realm,omitempty"`
	SilentLogin *bool  `yaml:"silentLogin,omitempty" json:"silentLogin,omitempty"`
}

type multiInstanceDecl struct {
	Enabled   bool `yaml:"enabled" json:"enabled"`
	MaxPerOrg int  `yaml:"maxPerOrg,omitempty" json:"maxPerOrg,omitempty"`
}

type topologyDecl struct {
	Supported []string         `yaml:"supported" json:"supported"`
	Defaults  topologyDefaults `yaml:"defaults" json:"defaults"`
}

type topologyDefaults struct {
	MultiRegion  string `yaml:"multi-region" json:"multi-region"`
	SingleRegion string `yaml:"single-region" json:"single-region"`
}

// fetchBlueprint resolves Blueprint metadata using the wired catalog
// client. Returns nil + nil-error when the catalog is unwired AND the
// blueprint has no on-cluster CR (so handlers don't 500 on a chroot
// without catalog-svc).
//
// The caller-identity session token is lifted from the request so the
// proxy hop to catalyst-catalog carries the same identity; the actual
// resolution is shared with resolveBlueprintMeta (no-request callers
// pass an empty token).
func (h *Handler) fetchBlueprint(ctx context.Context, r *http.Request, name string) (*blueprintMeta, error) {
	return h.resolveBlueprintMeta(ctx, name, applicationSessionToken(r))
}

// resolveBlueprintMeta is the request-free core of fetchBlueprint. It
// resolves Blueprint.spec metadata for `name` via the wired catalog
// client, carrying `sessionToken` for caller-identity passthrough (may be
// empty for internal/no-request lookups such as the AppDetail Open-button
// gate). Returns (&blueprintMeta{}, nil) — an empty, non-nil projection —
// whenever the catalog is unwired or the blueprint is absent, so callers
// can distinguish "resolved, no UI endpoint" from "could not resolve" by
// checking the returned error.
func (h *Handler) resolveBlueprintMeta(ctx context.Context, name, sessionToken string) (*blueprintMeta, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("blueprint name empty")
	}
	if h.catalogClient == nil {
		// Soft-fail: return an empty metadata so callers get an empty
		// endpoints list rather than 500. The catalog being unwired
		// is a configuration gap, not a logical error — but the
		// caller still sees an empty result.
		return &blueprintMeta{}, nil
	}
	bp, err := h.catalogClient.Get(ctx, name, sessionToken)
	if err != nil {
		if errors.Is(err, ErrBlueprintNotFound) {
			return &blueprintMeta{}, nil
		}
		return nil, err
	}
	if bp == nil || bp.Raw == nil {
		return &blueprintMeta{}, nil
	}
	spec, _ := bp.Raw["spec"].(map[string]interface{})
	if spec == nil {
		return &blueprintMeta{}, nil
	}
	out := blueprintMeta{}
	jsonBytes, jerr := json.Marshal(spec)
	if jerr != nil {
		return &out, nil
	}
	_ = json.Unmarshal(jsonBytes, &out)
	return &out, nil
}

// blueprintHasUserUIEndpoint reports whether a Blueprint declares a
// user-facing UI endpoint — the SAME signal the silent-SSO launch-url
// endpoint (HandleGetLaunchURL) reads to decide an app is launchable.
//
// An endpoint qualifies as a user UI when ANY of:
//
//   - SSOEnabled == true   (the app fronts an OIDC login the operator uses), OR
//   - LaunchDefault == true (the chart-tagged "open me" front door), OR
//   - Name == "ui"          (the conventional UI-endpoint name).
//
// API/protocol-only endpoints — bp-newapi's backend, bp-openova-flow-server's
// proxy target, an OCI registry endpoint, keycloak's admin-only realm — set
// none of these and therefore do NOT qualify. The AppDetail "Open" button +
// "External URL" row are gated on this so they never render for an app with
// no user UI (#3224).
//
// Returns false for nil metadata or an empty endpoint list — the caller owns
// the fail-open policy for the "could not resolve the blueprint" case.
func blueprintHasUserUIEndpoint(bp *blueprintMeta) bool {
	if bp == nil {
		return false
	}
	for i := range bp.Endpoints {
		ep := &bp.Endpoints[i]
		if ep.SSOEnabled || ep.LaunchDefault || strings.EqualFold(ep.Name, "ui") {
			return true
		}
	}
	return false
}

// resolveEndpoints joins Blueprint.spec.endpoints with per-instance
// hostname template evaluation. Status defaults to Ready (the
// application-controller's per-cluster Helm reconciler is the actual
// source of truth; for the first cut we always answer Ready for an
// existing Application).
func (h *Handler) resolveEndpoints(ctx context.Context, client dynamic.Interface, app *unstructured.Unstructured, bp *blueprintMeta) []resolvedEndpoint {
	out := []resolvedEndpoint{}
	if bp == nil {
		return out
	}
	org := extractOrgFromApp(app)
	appName := app.GetName()
	fqdn := h.endpointSovereignFQDN()
	vars := hostnameVars{
		SovereignFQDN: fqdn,
		OrgSlug:       org,
		AppName:       appName,
		// #5389 — the Organization's own domain. Resolved once per request,
		// not per endpoint: every endpoint of one Application belongs to the
		// same Org.
		OrgDomain: h.resolveOrgDomain(ctx, client, org),
	}
	for _, ep := range bp.Endpoints {
		tls := true
		if ep.TLS != nil {
			tls = *ep.TLS
		}
		hostname, hostErr := resolveHostnameTemplate(ep.HostnameTemplate, vars)
		re := resolvedEndpoint{
			Name:             ep.Name,
			HostnameTemplate: ep.HostnameTemplate,
			Hostname:         hostname,
			Port:             ep.Port,
			Protocol:         ep.Protocol,
			TLS:              tls,
			Visibility:       ep.Visibility,
			LaunchDefault:    ep.LaunchDefault,
			SSOEnabled:       ep.SSOEnabled,
			SSOInitPath:      ep.SSOInitPath,
			SSOShim:          ep.SSOShim,
			Status:           "Ready",
		}
		if hostErr != nil {
			// #5389 fail-loud: an unresolved template must NOT be published
			// as a hostname or a launch URL. Surface the endpoint as
			// Unresolved so the console can say "this app has no reachable
			// front door" instead of handing the operator a dead link.
			re.Status = endpointStatusUnresolved
			if h.log != nil {
				h.log.Warn("endpoint: hostnameTemplate unresolved; suppressing launch URL",
					"app", appName, "org", org, "endpoint", ep.Name,
					"hostnameTemplate", ep.HostnameTemplate, "error", hostErr.Error())
			}
			out = append(out, re)
			continue
		}
		if ep.SSOEnabled {
			// #3226 — prefer the server-side shim URL when the endpoint
			// declares ssoShim (zero-click parity for SPA apps); else the
			// app deep-link / legacy silent-SSO shape.
			if shim := buildSSOShimURL(fqdn, appName); ep.SSOShim && shim != "" {
				re.LaunchURL = shim
			} else {
				re.LaunchURL = buildLaunchURL(hostname, tls, ep.SSOInitPath)
			}
		}
		out = append(out, re)
	}
	return out
}

// pickEndpoint returns the EndpointSpec for `name` from the Blueprint,
// or the one tagged `launchDefault: true` when `name` is empty, or
// the first SSO-enabled https endpoint as a final fallback. Returns
// nil when no candidate exists.
func pickEndpoint(bp *blueprintMeta, name string) *endpointDecl {
	if bp == nil {
		return nil
	}
	if name != "" {
		for i := range bp.Endpoints {
			if bp.Endpoints[i].Name == name {
				return &bp.Endpoints[i]
			}
		}
		return nil
	}
	for i := range bp.Endpoints {
		if bp.Endpoints[i].LaunchDefault {
			return &bp.Endpoints[i]
		}
	}
	for i := range bp.Endpoints {
		ep := &bp.Endpoints[i]
		if ep.SSOEnabled && strings.EqualFold(ep.Protocol, "https") {
			return ep
		}
	}
	return nil
}

// hostnameVars is the substitution vocabulary a Blueprint's
// hostnameTemplate may reference.
type hostnameVars struct {
	// SovereignFQDN — the catalyst-api SOVEREIGN_FQDN env / wired override.
	SovereignFQDN string
	// OrgSlug — the Application's Organization slug.
	OrgSlug string
	// AppName — the Application CR name.
	AppName string
	// OrgDomain — the Organization's own domain suffix (#5389). See
	// endpoint_org_domain.go for the derivation; this is the token every
	// per-Org Blueprint must compose its host from.
	OrgDomain string
}

// errHostnameUnresolved marks a hostnameTemplate that could not be fully
// substituted. Callers MUST treat it as "no launch URL", never as a
// best-effort host.
var errHostnameUnresolved = errors.New("hostname template unresolved")

// resolveHostnameTemplate is a minimal substitution engine for the
// fields the Blueprint's hostnameTemplate may reference. The full Go
// text/template engine isn't needed because the field is constrained
// to a small set of well-known tokens.
//
// Supported tokens (single curly):
//
//	{SovereignFQDN}  → the catalyst-api SOVEREIGN_FQDN env
//	{OrgSlug}        → the Application's Org slug
//	{AppName}        → the Application's name
//	{OrgDomain}      → the Organization's domain suffix (#5389)
//
// Plus the Go-template-style {{.X}} aliases for compatibility with
// the existing exemplar blueprint files.
//
// # Fail LOUD, not open (#5389)
//
// The pre-#5389 engine returned `strings.ToLower(rep.Replace(tmpl))`
// unconditionally. strings.NewReplacer leaves any token it does not know
// LITERAL, so a typo'd or unsupported token — `{{.OrgDomian}}`, or
// `{{.OrgDomain}}` itself before the engine was taught about it — produced a
// "hostname" still containing braces, which buildLaunchURL then happily
// wrapped in `https://…/` and the console published as the Open button's
// target. Equally, an EMPTY substitution (no Org slug on a per-Org template)
// collapsed to `chat..example.com` / `.example.com` — syntactically a URL,
// semantically nowhere.
//
// Both are now hard failures: an unresolved template yields
// ("", errHostnameUnresolved) and the caller suppresses the launch URL and
// logs a Warn naming the template. A missing Open button is a visible,
// diagnosable gap; a dead Open button is a silent lie.
func resolveHostnameTemplate(tmpl string, v hostnameVars) (string, error) {
	raw := strings.TrimSpace(tmpl)
	if raw == "" {
		return "", fmt.Errorf("%w: empty hostnameTemplate", errHostnameUnresolved)
	}
	rep := strings.NewReplacer(
		"{SovereignFQDN}", v.SovereignFQDN,
		"{OrgSlug}", v.OrgSlug,
		"{AppName}", v.AppName,
		"{OrgDomain}", v.OrgDomain,
		"{{.SovereignFQDN}}", v.SovereignFQDN,
		"{{ .SovereignFQDN }}", v.SovereignFQDN,
		"{{.OrgSlug}}", v.OrgSlug,
		"{{ .OrgSlug }}", v.OrgSlug,
		"{{.AppName}}", v.AppName,
		"{{ .AppName }}", v.AppName,
		"{{.OrgDomain}}", v.OrgDomain,
		"{{ .OrgDomain }}", v.OrgDomain,
	)
	host := strings.ToLower(strings.TrimSpace(rep.Replace(raw)))
	if host == "" {
		return "", fmt.Errorf("%w: %q resolved to the empty string", errHostnameUnresolved, tmpl)
	}
	// (1) An unknown token survived the replacer verbatim.
	if strings.ContainsAny(host, "{}") {
		return "", fmt.Errorf("%w: %q → %q still carries an unsubstituted token "+
			"(supported: {SovereignFQDN} {OrgSlug} {AppName} {OrgDomain})", errHostnameUnresolved, tmpl, host)
	}
	// (2) A known token substituted to "" and collapsed a DNS label.
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return "", fmt.Errorf("%w: %q → %q has an empty DNS label (a token substituted to \"\")",
			errHostnameUnresolved, tmpl, host)
	}
	return host, nil
}

// evaluateHostnameTemplate is the lenient wrapper for call sites that only
// need "the host, or nothing". It never returns a partially substituted
// string — see resolveHostnameTemplate.
func evaluateHostnameTemplate(tmpl string, v hostnameVars) string {
	host, err := resolveHostnameTemplate(tmpl, v)
	if err != nil {
		return ""
	}
	return host
}

// buildLaunchURL constructs the silent-SSO launch URL.
//
// Two shapes, selected by ssoInitPath (#3150):
//
//   - ssoInitPath != "" (OIDC-init path, e.g. Grafana
//     `/login/generic_oauth`): the URL targets the app's OWN OIDC-login
//     route — `https://<host><ssoInitPath>`. Hitting that route makes the
//     app immediately 302 to Keycloak's authorize endpoint with its
//     pre-baked `kc_idp_hint=catalyst-pin` (the hint lives in the app's
//     synced OIDC auth_url, not here), Keycloak silently re-uses the
//     browser's PIN session, and the browser lands inside the app already
//     logged in — no app login form, no "Sign in with OpenOva" button.
//     No query string is appended: bare-root Keycloak params
//     (`prompt=none`) are meaningless on an app-local route and some apps
//     reject unknown query params on their login endpoint.
//
//   - ssoInitPath == "" (legacy / locked decision #3): the URL targets
//     the app root with `prompt=none&kc_idp_hint=catalyst-pin`. This
//     works only for apps that themselves treat root-visit + those query
//     params as an OIDC trigger; most OSS apps (Grafana, Harbor) ignore
//     them and show their login form, which is exactly the gap #3150
//     closes via ssoInitPath.
func buildLaunchURL(hostname string, tls bool, ssoInitPath string) string {
	scheme := "https"
	if !tls {
		scheme = "http"
	}
	if hostname == "" {
		return ""
	}
	if p := strings.TrimSpace(ssoInitPath); p != "" {
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return fmt.Sprintf("%s://%s%s", scheme, hostname, p)
	}
	q := url.Values{}
	q.Set("prompt", "none")
	q.Set("kc_idp_hint", "catalyst-pin")
	return fmt.Sprintf("%s://%s/?%s", scheme, hostname, q.Encode())
}

// buildSSOShimURL constructs the absolute URL of the catalyst-api
// server-side silent-SSO shim (#3226) for the given app id. The shim lives
// on the API origin (`api.<fqdn>`, the same canonical pattern used by the
// handover export + tofu-archive POST targets) at
// `/catalyst/v1/apps/<id>/openbao-sso-init`. Returns "" when fqdn is empty
// so the caller falls back to the deep-link rather than emitting a
// host-less URL.
func buildSSOShimURL(fqdn, id string) string {
	fqdn = strings.TrimSpace(fqdn)
	id = strings.TrimSpace(id)
	if fqdn == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("https://api.%s/catalyst/v1/apps/%s/openbao-sso-init", fqdn, url.PathEscape(id))
}

// chooseTopology selects the active topology for a createInstance call.
// `override` wins if non-empty AND in `supported`. Otherwise the
// Blueprint's default (multi-region vs single-region) is used; the
// caller passes `multiRegion=true` when the active Sovereign has >1
// region per locked decision #7 (`len(Sovereign.spec.regions) > 1`).
//
// G117.3d #2780 (was: this function read SOVEREIGN_REGIONS env directly,
// which the W1.B4 verifier flagged as fragile because env vars are a
// bastion-only artefact and drift from the live Sovereign CR). The
// per-call decision is now computed by Handler.detectMultiRegion and
// passed in here so this helper stays pure + table-test-friendly.
func chooseTopology(bp *blueprintMeta, override string, multiRegion bool) (string, error) {
	// #3648 — the catalyst-ui posts the placement-editor dialect
	// (single-region / active-active / active-hotstandby); Blueprint
	// SupportedTopologies use the canonical vocabulary (singleton /
	// active-active / active-hot-standby / active-passive). Canonicalise the
	// operator override so either spelling resolves against Supported. Empty
	// stays empty (the default-selection path below).
	override = canonicalizeTopology(override)
	if bp == nil || bp.Topology == nil || len(bp.Topology.Supported) == 0 {
		// Permissive fallback — no topology declared → singleton.
		if override != "" && override != "singleton" && override != "active-hot-standby" &&
			override != "active-active" && override != "active-passive" {
			return "", fmt.Errorf("topology %q not in supported {singleton}", override)
		}
		if override == "" {
			return "singleton", nil
		}
		return override, nil
	}
	if override != "" {
		for _, s := range bp.Topology.Supported {
			if s == override {
				return override, nil
			}
		}
		return "", fmt.Errorf("topology %q not in supported %v", override, bp.Topology.Supported)
	}
	if multiRegion && bp.Topology.Defaults.MultiRegion != "" {
		return bp.Topology.Defaults.MultiRegion, nil
	}
	if bp.Topology.Defaults.SingleRegion != "" {
		return bp.Topology.Defaults.SingleRegion, nil
	}
	return bp.Topology.Supported[0], nil
}

// canonicalizeTopology maps the catalyst-ui placement-editor dialect
// (single-region / active-active / active-hotstandby) onto the canonical
// Blueprint topology vocabulary (singleton / active-active /
// active-hot-standby / active-passive) so a create or placement-change
// posted with either spelling resolves against bp.Topology.Supported
// (#3648). Empty input returns empty (preserves the default-selection
// path); an unknown non-empty value is returned trimmed so the caller
// still rejects it as unsupported with its original spelling.
func canonicalizeTopology(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active-hot-standby", "active-hotstandby", "active_hot_standby":
		return "active-hot-standby"
	case "active-passive", "active_passive":
		return "active-passive"
	case "active-active", "active_active":
		return "active-active"
	case "singleton", "single-region", "single_region":
		return "singleton"
	default:
		return strings.TrimSpace(raw)
	}
}

// detectMultiRegion computes whether the active Sovereign is
// multi-region. Per G117 locked decision #7 the canonical truth-source
// is `len(Sovereign.spec.regions) > 1` — read via the injected
// RegionsCounter callback. Falls back to SOVEREIGN_REGIONS env when no
// counter is wired (the legacy W1.B4 path; emitted as a WARN log line
// once per process via debugMultiRegionFallbackOnce).
func (h *Handler) detectMultiRegion(ctx context.Context) bool {
	if h.endpointDeps.RegionsCounter != nil {
		n, err := h.endpointDeps.RegionsCounter(ctx)
		if err == nil {
			return n > 1
		}
		// Counter wired but errored — fall through to env var so we
		// FAIL OPEN to single-region (the safer default; pinning to
		// multi-region without proof would mis-place workloads).
	}
	if v := strings.TrimSpace(os.Getenv("SOVEREIGN_REGIONS")); v != "" {
		// Count comma-separated region codes; >0 commas = >1 region.
		return strings.Count(v, ",") > 0
	}
	return false
}

// callerInOrg returns true when the caller's claims show membership in
// the named Org. The injected OrgMembership callback (if wired) takes
// precedence so production main.go can plug in a real group-walker.
// Otherwise the default predicate accepts:
//
//   - sovereign-admins / sovereign-operators (any of the privileged
//     realm-roles in `sovereignCrossOrgRoles`) regardless of claims.Org;
//   - sovereign-operator email match (re-using isSovereignOperatorClaim);
//   - claims.Org equal to `org` (case-insensitive trim);
//   - any `claims.Groups` entry that starts with `/<org>/` (KC group
//     path convention) or equals `<org>` (flat group claim).
//
// Empty `org` is rejected as defence-in-depth: it would otherwise match
// the empty claims.Org from a buggy mapper and silently grant access.
//
// Per G117.3a #2757 the membership check is REQUIRED on every endpoint
// mutation + multi-instance create. A nil claims context (test mode
// without auth middleware) is treated as "no claim required" by the
// caller — this method is only reached when claims != nil.
func (h *Handler) callerInOrg(ctx context.Context, claims *auth.Claims, org string) bool {
	if claims == nil {
		return false
	}
	org = strings.TrimSpace(strings.ToLower(org))
	if org == "" {
		return false
	}
	// Sovereign-cross-org roles bypass the per-Org check (the operator
	// MUST be able to manage every Org on their Sovereign).
	for _, r := range sovereignCrossOrgRoles {
		if claims.HasRealmRole(r) {
			return true
		}
	}
	if isSovereignOperatorClaim(claims) {
		return true
	}
	// Custom hook — production main.go can wire a richer matcher.
	if h.endpointDeps.OrgMembership != nil {
		return h.endpointDeps.OrgMembership(ctx, claims, org)
	}
	// Built-in default — claims.Org direct match or KC groups path match.
	if strings.EqualFold(strings.TrimSpace(claims.Org), org) {
		return true
	}
	prefix := "/" + org + "/"
	for _, g := range claims.Groups {
		gl := strings.ToLower(strings.TrimSpace(g))
		if gl == org || gl == "/"+org || strings.HasPrefix(gl, prefix) {
			return true
		}
	}
	return false
}

// sovereignCrossOrgRoles is the set of Keycloak realm-roles whose
// holders may mutate Applications across any Org on this Sovereign.
// Kept narrow on purpose — any addition here grants cross-Org write.
// The list mirrors the privileged-role allow-list from the policy-mode
// gate; see applications.go::rbacAssignPrivilegedRoles for the source.
var sovereignCrossOrgRoles = []string{
	"sovereign-admin",
	"sovereign-operator",
	"catalyst-owner",
	"catalyst-admin",
}

// SovereignGVR returns the Sovereign CR's GroupVersionResource. Kept
// adjacent to ApplicationGVR for symmetry; the chart ships the CRD at
// `sovereigns.catalyst.openova.io`.
func SovereignGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1alpha1",
		Resource: "sovereigns",
	}
}

// CountSovereignRegions returns the number of entries in
// Sovereign.spec.regions across all Sovereigns in the cluster. Used by
// the production RegionsCounter wired in main.go. On a chroot or
// preview Sovereign with no CR list this returns (0, nil) so the
// detectMultiRegion fallback path runs.
func CountSovereignRegions(ctx context.Context, c dynamic.Interface) (int, error) {
	list, err := c.Resource(SovereignGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		// Sovereign CRD may legitimately not be installed (chroot dev).
		// Don't propagate a fatal error; let caller fall through.
		return 0, nil
	}
	maxRegions := 0
	for i := range list.Items {
		regions, _, _ := unstructured.NestedSlice(list.Items[i].Object, "spec", "regions")
		if len(regions) > maxRegions {
			maxRegions = len(regions)
		}
	}
	return maxRegions, nil
}

// readMultiInstance pulls the MultiInstanceSpec from the Blueprint.
// Returns a zero (Enabled=false) when not declared.
func readMultiInstance(bp *blueprintMeta) multiInstanceDecl {
	if bp == nil || bp.MultiInstance == nil {
		return multiInstanceDecl{}
	}
	return *bp.MultiInstance
}

// listAppsInOrg lists Applications in the `org` namespace whose
// spec.blueprintRef.name matches `blueprint` (with or without bp-).
//
// #3830 — `org` is the Org ref (often a dotted FQDN); it is slugged via
// orgNamespace() so this existing-instance lookup addresses the SAME
// namespace the create path writes into (the admission name-collision
// gate that consumes this list must see the already-installed CRs).
func listAppsInOrg(ctx context.Context, c dynamic.Interface, org, blueprint string) ([]unstructured.Unstructured, error) {
	list, err := c.Resource(ApplicationGVR()).Namespace(orgNamespace(org)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	want := strings.TrimPrefix(blueprint, "bp-")
	out := []unstructured.Unstructured{}
	for i := range list.Items {
		bp := extractBlueprintFromApp(&list.Items[i])
		if bp == want {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

// newApplicationCRFromSeed builds the Application CR for the
// multi-instance create endpoint from an `instances.ApplicationSeed`.
// Includes the W2.C2 spec fields (instanceId, isolationLevel,
// namingTemplate) plus the legacy environmentRef / blueprintRef /
// placement / regions / parameters envelope.
func newApplicationCRFromSeed(seed instances.ApplicationSeed) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(seed.Name)
	// #3830 — seed.Namespace carries the Org ref (often a dotted FQDN).
	// metadata.namespace must be the slugged RFC-1123 form; the verbatim
	// Org identity is preserved on the catalyst.openova.io/organization
	// label (set in instances.Build) and on environmentRef below.
	obj.SetNamespace(orgNamespace(seed.Namespace))
	obj.SetLabels(seed.Labels)
	// One vocabulary (#3375 DoD-1): both the string AND object forms
	// stamp the CANONICAL placement token. placementForTopology folds
	// the chosen topology (which may already be a canonical G117 class,
	// e.g. singleton / active-hot-standby) onto the canonical posture;
	// the object form's `mode` is canonicalised too so the two forms
	// never disagree.
	var placementValue interface{} = placementForTopology(seed.Topology)
	regions := []interface{}{"primary"}
	if seed.Placement != nil {
		pl := map[string]interface{}{
			"mode": canonicalizeTopology(seed.Topology),
		}
		if seed.Placement.VCluster != "" {
			pl["vcluster"] = seed.Placement.VCluster
		}
		if len(seed.Placement.Regions) > 0 {
			plRegions := make([]interface{}, 0, len(seed.Placement.Regions))
			for _, r := range seed.Placement.Regions {
				plRegions = append(plRegions, r)
			}
			pl["regions"] = plRegions
			regions = plRegions
		}
		if len(seed.Placement.Clusters) > 0 {
			plClusters := make([]interface{}, 0, len(seed.Placement.Clusters))
			for _, c := range seed.Placement.Clusters {
				plClusters = append(plClusters, c)
			}
			pl["clusters"] = plClusters
		}
		placementValue = pl
	}
	spec := map[string]interface{}{
		// #3922 — slug the env ref so a dotted Sovereign FQDN (seed.Namespace
		// is the Org, often an FQDN like "hw171.omantel.biz") does not produce
		// a dotted spec.environmentRef the apiserver rejects against the CRD
		// pattern ^[a-z][a-z0-9-]{2,63}$ (HTTP 500). environmentRefForOrg
		// appends the canonical "-prod" suffix and slugs the whole value.
		"environmentRef": environmentRefForOrg(seed.Namespace),
		"blueprintRef": map[string]interface{}{
			"name": seed.Blueprint,
		},
		// #3370+#3373 merged — placementValue carries the instance's
		// placement OBJECT when the advanced view set one (#3373); the
		// string fallback maps through placementForTopology so the CRD
		// enum, not the raw topology string, is stamped (#3370's
		// admission fix). Topology stays on the label.
		"placement": placementValue,
		"regions":   regions,
		// ── G117.2 W2.C2 multi-instance fields ──
		"instanceId":     seed.InstanceID,
		"isolationLevel": string(seed.IsolationLevel),
		"namingTemplate": seed.NamingTemplate,
	}
	// #4283 / #4282 Root-B — ALWAYS stamp a non-null spec.parameters
	// OBJECT. The auto-created backing-service path (wireBackingServices)
	// builds postgres seeds (`shared-pg-d`/`-e`) with NO Values, so the CR
	// used to be emitted with `parameters` entirely absent → it
	// materialised as `parameters: null` after the per-Org IaC Git
	// round-trip → the application-controller's configSchema validation
	// failed ("#: expected object, but got null") BEFORE anything else
	// reconciled (phase=Failed). Now seed.Values (when present) is used
	// verbatim; otherwise we emit at least `{}` plus a configSchema-valid
	// topology.mode for bp-postgres.
	spec["parameters"] = defaultedParameters(seed.Blueprint, seed.Topology, seed.SovereignFQDN, seed.Namespace, seed.OrgConsoleHost, seed.Values)
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")
	return obj
}

// placementForTopology maps a BCP topology choice onto the Application
// CRD's spec.placement value (#3370). Post-#3375 DoD-1 it produces the
// ONE canonical placement vocabulary (singleton / active-active /
// active-hot-standby / active-passive) — the same set the catalog
// placementSchema, both editors, and the application-controller's
// resolver speak. It accepts any spelling (canonical OR the legacy
// editor dialect) and folds it onto the canonical token via
// canonicalizeTopology. Empty / unknown → singleton (the safe default
// posture).
func placementForTopology(topology string) string {
	if c := canonicalizeTopology(topology); c == "singleton" ||
		c == "active-active" || c == "active-hot-standby" || c == "active-passive" {
		return c
	}
	// empty / unknown → the singleton posture (one cluster, no failover).
	return "singleton"
}

// readPhase + readTopology read the most-informative status field for
// the listing endpoints.
func readPhase(u *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(u.Object, "status", "phase"); ok && v != "" {
		return v
	}
	return "Pending"
}

// placementFromSpec resolves `spec.placement` across BOTH shapes the CRD
// accepts, WITHOUT inventing a value when it is genuinely absent (#5422).
//
// Shapes: the legacy bare string, and the #3373 object
// `{mode, vcluster, regions, clusters}` where the posture rides in `mode`.
// A raw NestedString against the object form returns ok=false, so a caller
// reading only the string form silently drops the field — and the console
// then converts that absence into a confident wrong value
// (`?? 'singleton'`, AppDetail.tsx:255), rendering `singleton` for a
// two-region app directly above its own two-region REGIONS list.
//
// This deliberately does NOT share readTopology's "singleton" default:
// readTopology serves LISTING endpoints where a display default is
// reasonable, whereas a detail response must distinguish "unset" from
// "singleton". Returning "" lets `omitempty` drop the field so the console
// can render unknown honestly instead of being handed a guess.
func placementFromSpec(u *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "placement"); ok && v != "" {
		return v
	}
	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "placement", "mode"); ok && v != "" {
		return v
	}
	return ""
}

func readTopology(u *unstructured.Unstructured) string {
	if v, ok, err := unstructured.NestedString(u.Object, "spec", "placement"); err == nil && ok && v != "" {
		return v
	}
	// #3373 — object form: the legacy posture rides in `mode`.
	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "placement", "mode"); ok && v != "" {
		return v
	}
	if labels := u.GetLabels(); labels != nil {
		if v := labels["catalyst.openova.io/topology"]; v != "" {
			return v
		}
	}
	return "singleton"
}

// NewProductionGiteaIaCWriter — convenience wrapper main.go calls to
// build a per-Org giteapr.Writer from the unified gitea.Client. The
// per-Org robot token is stored in OpenBao at
// `kv/data/org/<slug>/iac-bot-token` per ADR-0009; we look it up via
// the per-Handler OpenBao client (h.openbao) and assemble the per-call
// gitea.Client with the per-Org credential.
//
// Token resolution order:
//
//  1. OpenBao `kv/data/org/<org>/iac-bot-token` (key `token`) — the
//     canonical per-Org path. Each Org's `tools/bootstrap-org-iac-repo.sh`
//     seeds this with the robot account's PAT scoped to ONLY that
//     Org's `iac` repo, so Org-A's writer physically cannot mutate
//     Org-B's IaC repo (per the W3.D4 #2765 integration test).
//
//  2. Fallback to the global `CATALYST_GITEA_TOKEN` env var when:
//     (a) the Handler has no openbao client wired (test mode /
//     single-Org bootstrap Sovereign before openbao is up); OR
//     (b) the per-Org secret returns ErrSecretNotFound (the Org's
//     token hasn't been seeded yet — typical for the FIRST
//     reconcile after bootstrap-org-iac-repo.sh provisions the
//     Gitea side but before the OpenBao seed lands).
//
//  3. Transport errors from OpenBao (NOT not-found) are surfaced as
//     errors — we do NOT silently fall back through a transient
//     network blip onto the wrong-scope global token. The caller
//     retries the endpoint mutation and the next reconcile pass
//     succeeds once OpenBao is reachable.
//
// G117.3b (Refs #2765). Cross-Org leak impossibility test lives in
// endpoint_handler_per_org_token_test.go (TestPerOrgTokenIsolation).
func (h *Handler) NewProductionGiteaIaCWriter(ctx context.Context, org string) (*giteapr.Writer, error) {
	base := strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL"))
	if base == "" {
		return nil, errors.New("CATALYST_GITEA_URL unset")
	}
	tok, err := h.resolveGiteaTokenForOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	c := gitea.New(base, tok)
	return giteapr.NewWriter(c, &noopStatusChecker{}, giteapr.PollConfig{}), nil
}

// NewProductionGiteaIaCWriter is the package-level back-compat shim.
// Existing callers without a Handler context fall through to the env
// var only — used by tests + tools that intentionally exercise the
// "no per-Org secret store wired" path. Production wiring should use
// the Handler method above.
func NewProductionGiteaIaCWriter(_org string) (*giteapr.Writer, error) {
	base := strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL"))
	tok := strings.TrimSpace(os.Getenv("CATALYST_GITEA_TOKEN"))
	if base == "" || tok == "" {
		return nil, errors.New("CATALYST_GITEA_URL or CATALYST_GITEA_TOKEN unset")
	}
	c := gitea.New(base, tok)
	return giteapr.NewWriter(c, &noopStatusChecker{}, giteapr.PollConfig{}), nil
}

// resolveGiteaTokenForOrg reads the per-Org Gitea robot token from
// OpenBao at `kv/data/org/<slug>/iac-bot-token` (key `token`). Returns
// the global env-var fallback only when (a) no openbao client is
// wired, or (b) the per-Org secret does not yet exist. Real transport
// errors propagate so the caller can distinguish "use env" from "retry
// later".
//
// G117.3b (Refs #2765).
func (h *Handler) resolveGiteaTokenForOrg(ctx context.Context, org string) (string, error) {
	org = strings.TrimSpace(strings.ToLower(org))
	if org == "" {
		return "", errors.New("giteapr: org slug required for token lookup")
	}
	envTok := strings.TrimSpace(os.Getenv("CATALYST_GITEA_TOKEN"))

	if h == nil || h.openbao == nil {
		// Test / pre-openbao bootstrap mode — fall through to env.
		if envTok == "" {
			return "", errors.New("CATALYST_GITEA_TOKEN unset and no openbao client wired")
		}
		return envTok, nil
	}

	secretPath := "org/" + org + "/iac-bot-token"
	data, err := h.openbao.GetKVv2(ctx, "secret", secretPath)
	if err != nil {
		if errors.Is(err, openbao.ErrSecretNotFound) {
			// Per-Org token not yet seeded — fall back to env.
			if envTok == "" {
				return "", fmt.Errorf("giteapr: no per-Org token at kv/data/%s and CATALYST_GITEA_TOKEN unset", secretPath)
			}
			h.log.Info("giteapr: per-Org token absent; falling back to global env token",
				"org", org, "path", secretPath,
			)
			return envTok, nil
		}
		// Real transport error — surface, never silent fall-through.
		return "", fmt.Errorf("giteapr: openbao GetKVv2 %s: %w", secretPath, err)
	}

	tokAny, ok := data["token"]
	if !ok {
		return "", fmt.Errorf("giteapr: openbao secret %s missing required key `token`", secretPath)
	}
	tok, ok := tokAny.(string)
	if !ok || strings.TrimSpace(tok) == "" {
		return "", fmt.Errorf("giteapr: openbao secret %s key `token` is empty or non-string", secretPath)
	}
	return strings.TrimSpace(tok), nil
}

// noopStatusChecker is the placeholder StatusChecker used until the
// real Gitea-Actions status-check poller lands. It always returns
// pending — the writer's budget-elapse path takes over and the caller
// sees `status: open`. This is the SAFE default: the per-Org repo's
// branch-protection requires the three named checks to flip green
// before merge regardless of what we poll.
type noopStatusChecker struct{}

func (n *noopStatusChecker) GetStatuses(_ context.Context, _, _, _ string) (map[string]giteapr.CheckStatus, error) {
	return map[string]giteapr.CheckStatus{}, nil
}

// ── Application GVR shadow (in case dep handlers don't import ours) ─

// _ = silence unused-import linter when build tags vary.
var _ = schema.GroupVersionResource{}

// ── Production CertConflictLookup (G117 #2864 Gap 6) ────────────────

// NewProductionCertConflictLookup returns a precheck.CertConflictLookup
// that walks every cert-manager.io/v1 Certificate in the cluster and
// matches by spec.commonName + spec.dnsNames[]. When the covering
// Certificate uses a wildcard pattern (e.g. `*.<sov-fqdn>`) the
// returned CertOwner sets IsWildcard=true + WildcardDomain to the
// base domain so CheckCertManager can auto-PASS new hostnames under
// the wildcard's scope (per G117 #2864 / Gap 6 of #2856).
//
// Owner labels are sourced from the Certificate's
// `catalyst.openova.io/organization` + `catalyst.openova.io/app`
// labels. The Sovereign-Gateway's wildcard Certificate typically
// carries no per-Application owner labels — in that case we surface
// (`sovereign`, `gateway`) as a deterministic placeholder so the
// auto-PASS-by-wildcard branch fires cleanly without leaking
// namespace/name internals into the verdict.
//
// Lookup precedence:
//
//  1. Exact match on spec.commonName (or any spec.dnsNames[] entry) —
//     returns the owner of THAT Certificate directly. IsWildcard
//     reflects whether the matched entry began with `*.`.
//  2. Wildcard match: any Certificate whose commonName/dnsNames
//     contains a `*.<base>` pattern that covers `hostname` (one
//     label of depth, exact base-domain suffix). Returns the owner
//     of the wildcard Certificate with IsWildcard=true +
//     WildcardDomain=<base>.
//  3. No match — (CertOwner{}, false, nil).
//
// Transport errors from the apiserver propagate as the err return so
// CheckCertManager surfaces `lookup-failed` (HTTP 503-class). A
// missing cert-manager CRD (chroot dev surface) returns
// (CertOwner{}, false, nil) — no Certificates means no conflict.
func NewProductionCertConflictLookup(dynClient dynamic.Interface) precheck.CertConflictLookup {
	return func(ctx context.Context, hostname string) (precheck.CertOwner, bool, error) {
		if dynClient == nil {
			return precheck.CertOwner{}, false, errors.New("cert-manager lookup: dynamic client nil")
		}
		hostname = strings.ToLower(strings.TrimSpace(hostname))
		if hostname == "" {
			return precheck.CertOwner{}, false, nil
		}
		list, err := dynClient.Resource(certificateGVR).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return precheck.CertOwner{}, false, nil
			}
			return precheck.CertOwner{}, false, fmt.Errorf("cert-manager lookup: list certificates: %w", err)
		}
		var wildcardHit *precheck.CertOwner
		for i := range list.Items {
			c := &list.Items[i]
			cn, _, _ := unstructured.NestedString(c.Object, "spec", "commonName")
			dnsNames, _, _ := unstructured.NestedStringSlice(c.Object, "spec", "dnsNames")
			candidates := make([]string, 0, len(dnsNames)+1)
			if cn != "" {
				candidates = append(candidates, cn)
			}
			candidates = append(candidates, dnsNames...)
			owner := certOwnerFromObject(c)
			for _, name := range candidates {
				name = strings.ToLower(strings.TrimSpace(name))
				if name == "" {
					continue
				}
				// Exact match short-circuits regardless of wildcard.
				if name == hostname {
					o := owner
					o.IsWildcard = strings.HasPrefix(name, "*.")
					if o.IsWildcard {
						o.WildcardDomain = strings.TrimPrefix(name, "*.")
					}
					return o, true, nil
				}
				// Wildcard candidate? Stash the first hit; an exact
				// match later in the scan still wins (continues loop).
				if strings.HasPrefix(name, "*.") {
					base := strings.TrimPrefix(name, "*.")
					if wildcardCovers(hostname, base) && wildcardHit == nil {
						o := owner
						o.IsWildcard = true
						o.WildcardDomain = base
						wildcardHit = &o
					}
				}
			}
		}
		if wildcardHit != nil {
			return *wildcardHit, true, nil
		}
		return precheck.CertOwner{}, false, nil
	}
}

// certOwnerFromObject lifts (Org, App) from the Certificate's
// canonical catalyst.openova.io labels. Sovereign-Gateway wildcard
// certs typically carry no per-Application labels — we surface
// (`sovereign`, `gateway`) as a deterministic placeholder so the
// CheckCertManager auto-PASS-by-wildcard branch (G117 #2864) fires
// without leaking namespace/name into the verdict.
func certOwnerFromObject(c *unstructured.Unstructured) precheck.CertOwner {
	labels := c.GetLabels()
	org := ""
	app := ""
	if labels != nil {
		org = strings.TrimSpace(labels["catalyst.openova.io/organization"])
		app = strings.TrimSpace(labels[labelCatalystApp])
	}
	if org == "" {
		org = "sovereign"
	}
	if app == "" {
		app = "gateway"
	}
	return precheck.CertOwner{Org: org, App: app}
}

// wildcardCovers mirrors the precheck-package wildcard-scope rule
// (TLS spec: `*.<base>` covers ONE label of depth, not the apex). We
// inline it here so the production lookup is self-contained and the
// precheck package's helper stays unexported.
func wildcardCovers(hostname, base string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	base = strings.ToLower(strings.TrimSpace(base))
	if hostname == "" || base == "" {
		return false
	}
	suffix := "." + base
	if !strings.HasSuffix(hostname, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(hostname, suffix)
	if prefix == "" || strings.Contains(prefix, ".") {
		return false
	}
	return true
}
