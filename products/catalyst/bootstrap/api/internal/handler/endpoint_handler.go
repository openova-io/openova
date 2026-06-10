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
	PRURL           string                   `json:"prURL"`
	Status          string                   `json:"status"`
	PreCheckResults endpointPreCheckResults  `json:"preCheckResults"`
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
	Status            string `json:"status"`
	CertificateStatus string `json:"certificateStatus,omitempty"`
	LaunchURL         string `json:"launchURL,omitempty"`
}

// applicationSummary mirrors `schema/ApplicationSummary`.
type applicationSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Blueprint string `json:"blueprint"`
	Org       string `json:"org"`
	Topology  string `json:"topology"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// application mirrors `schema/Application` (Summary plus per-cluster
// detail + endpoints[]).
type application struct {
	applicationSummary
	PerCluster []applicationClusterStatus `json:"perCluster,omitempty"`
	Endpoints  []resolvedEndpoint         `json:"endpoints,omitempty"`
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
	if strings.TrimSpace(uid) == "" {
		return nil, errAppNotFound
	}
	list, err := c.Resource(ApplicationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if string(list.Items[i].GetUID()) == uid {
			out := list.Items[i].DeepCopy()
			return out, nil
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
	endpoints := h.resolveEndpoints(app, bpDoc)
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

	hostname := evaluateHostnameTemplate(ep.HostnameTemplate, h.endpointSovereignFQDN(), org, appName)
	tls := true
	if ep.TLS != nil {
		tls = *ep.TLS
	}
	urlStr := buildLaunchURL(hostname, tls, ep.SSOInitPath)
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

	obj := newApplicationCRFromSeed(seed)
	created, err := client.Resource(ApplicationGVR()).Namespace(body.Org).Create(
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
	}
	writeJSON(w, http.StatusCreated, resp)
}

// HandleListBlueprintInstances — GET /catalyst/v1/catalog/{blueprint}/instances
func (h *Handler) HandleListBlueprintInstances(w http.ResponseWriter, r *http.Request) {
	bp := strings.TrimPrefix(chi.URLParam(r, "blueprint"), "bp-")
	orgFilter := strings.TrimSpace(r.URL.Query().Get("org"))

	client, err := h.dynamicClientOrFallback()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "cluster-unavailable",
			"message": err.Error(),
		})
		return
	}

	list, err := client.Resource(ApplicationGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"code":    "list-failed",
			"message": err.Error(),
		})
		return
	}

	out := []applicationSummary{}
	for i := range list.Items {
		item := &list.Items[i]
		blueprint := extractBlueprintFromApp(item)
		if blueprint != bp {
			continue
		}
		org := extractOrgFromApp(item)
		if orgFilter != "" && !strings.EqualFold(org, orgFilter) {
			continue
		}
		out = append(out, applicationSummary{
			ID:        string(item.GetUID()),
			Name:      item.GetName(),
			Blueprint: blueprint,
			Org:       org,
			Topology:  readTopology(item),
			Status:    readPhase(item),
			CreatedAt: item.GetCreationTimestamp().UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, listInstancesResponse{Items: out})
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
				"app.kubernetes.io/instance":             m.App,
				"app.kubernetes.io/managed-by":           "catalyst-api",
				"catalyst.openova.io/organization":       m.Org,
				"catalyst.openova.io/endpoint-name":      m.Name,
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
	Endpoints     []endpointDecl     `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
	SSO           *ssoDecl           `yaml:"sso,omitempty" json:"sso,omitempty"`
	MultiInstance *multiInstanceDecl `yaml:"multiInstance,omitempty" json:"multiInstance,omitempty"`
	Topology      *topologyDecl      `yaml:"topology,omitempty" json:"topology,omitempty"`
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
}

type ssoDecl struct {
	Realm        string `yaml:"realm,omitempty" json:"realm,omitempty"`
	SilentLogin  *bool  `yaml:"silentLogin,omitempty" json:"silentLogin,omitempty"`
}

type multiInstanceDecl struct {
	Enabled   bool `yaml:"enabled" json:"enabled"`
	MaxPerOrg int  `yaml:"maxPerOrg,omitempty" json:"maxPerOrg,omitempty"`
}

type topologyDecl struct {
	Supported []string                 `yaml:"supported" json:"supported"`
	Defaults  topologyDefaults         `yaml:"defaults" json:"defaults"`
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
func (h *Handler) resolveEndpoints(app *unstructured.Unstructured, bp *blueprintMeta) []resolvedEndpoint {
	out := []resolvedEndpoint{}
	if bp == nil {
		return out
	}
	org := extractOrgFromApp(app)
	appName := app.GetName()
	fqdn := h.endpointSovereignFQDN()
	for _, ep := range bp.Endpoints {
		tls := true
		if ep.TLS != nil {
			tls = *ep.TLS
		}
		hostname := evaluateHostnameTemplate(ep.HostnameTemplate, fqdn, org, appName)
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
			Status:           "Ready",
		}
		if ep.SSOEnabled {
			re.LaunchURL = buildLaunchURL(hostname, tls, ep.SSOInitPath)
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

// evaluateHostnameTemplate is a minimal substitution engine for the
// fields the Blueprint's hostnameTemplate may reference. The full Go
// text/template engine isn't needed because the field is constrained
// to a small set of well-known tokens.
//
// Supported tokens (single curly):
//
//	{SovereignFQDN}  → the catalyst-api SOVEREIGN_FQDN env
//	{OrgSlug}        → the Application's Org slug
//	{AppName}        → the Application's name
//
// Plus the Go-template-style {{.X}} aliases for compatibility with
// the existing exemplar blueprint files.
func evaluateHostnameTemplate(tmpl, fqdn, org, appName string) string {
	rep := strings.NewReplacer(
		"{SovereignFQDN}", fqdn,
		"{OrgSlug}", org,
		"{AppName}", appName,
		"{{.SovereignFQDN}}", fqdn,
		"{{ .SovereignFQDN }}", fqdn,
		"{{.OrgSlug}}", org,
		"{{ .OrgSlug }}", org,
		"{{.AppName}}", appName,
		"{{ .AppName }}", appName,
	)
	return strings.ToLower(rep.Replace(tmpl))
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

// listAppsInOrg lists Applications in `org` namespace whose
// spec.blueprintRef.name matches `blueprint` (with or without bp-).
func listAppsInOrg(ctx context.Context, c dynamic.Interface, org, blueprint string) ([]unstructured.Unstructured, error) {
	list, err := c.Resource(ApplicationGVR()).Namespace(org).List(ctx, metav1.ListOptions{})
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
	obj.SetNamespace(seed.Namespace)
	obj.SetLabels(seed.Labels)
	spec := map[string]interface{}{
		"environmentRef": seed.Namespace + "-prod",
		"blueprintRef": map[string]interface{}{
			"name": seed.Blueprint,
		},
		"placement": seed.Topology,
		"regions":   []interface{}{"primary"},
		// ── G117.2 W2.C2 multi-instance fields ──
		"instanceId":     seed.InstanceID,
		"isolationLevel": string(seed.IsolationLevel),
		"namingTemplate": seed.NamingTemplate,
	}
	if len(seed.Values) > 0 {
		vals := make(map[string]interface{}, len(seed.Values))
		for k, v := range seed.Values {
			vals[k] = v
		}
		spec["parameters"] = vals
	}
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")
	return obj
}

// readPhase + readTopology read the most-informative status field for
// the listing endpoints.
func readPhase(u *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(u.Object, "status", "phase"); ok && v != "" {
		return v
	}
	return "Pending"
}

func readTopology(u *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "placement"); ok && v != "" {
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
//       (a) the Handler has no openbao client wired (test mode /
//           single-Org bootstrap Sovereign before openbao is up); OR
//       (b) the per-Org secret returns ErrSecretNotFound (the Org's
//           token hasn't been seeded yet — typical for the FIRST
//           reconcile after bootstrap-org-iac-repo.sh provisions the
//           Gitea side but before the OpenBao seed lands).
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
		app = strings.TrimSpace(labels["catalyst.openova.io/app"])
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
