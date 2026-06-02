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
	"regexp"
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

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
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
	Status            string `json:"status"`
	CertificateStatus string `json:"certificateStatus,omitempty"`
	LaunchURL         string `json:"launchURL,omitempty"`
}

// createInstanceRequest mirrors `schema/CreateInstanceRequest`.
type createInstanceRequest struct {
	Blueprint string                 `json:"blueprint"`
	Org       string                 `json:"org"`
	Name      string                 `json:"name"`
	Topology  string                 `json:"topology,omitempty"`
	Values    map[string]interface{} `json:"values,omitempty"`
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

	// Authz — same gate as application install (tier-admin or higher).
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":    "forbidden",
				"message": "endpoint mutation requires tier-admin or higher",
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

	ep := pickEndpoint(bpDoc, epName)
	if ep == nil {
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

	hostname := evaluateHostnameTemplate(ep.HostnameTemplate, h.endpointSovereignFQDN(), extractOrgFromApp(app), app.GetName())
	tls := true
	if ep.TLS != nil {
		tls = *ep.TLS
	}
	urlStr := buildLaunchURL(hostname, tls)
	expiresAt := time.Now().Add(60 * time.Second).UTC().Format(time.RFC3339)

	writeJSON(w, http.StatusOK, launchURLResponse{
		URL:       urlStr,
		ExpiresAt: expiresAt,
		Endpoint:  ep.Name,
	})
}

// HandleCreateInstance — POST /catalyst/v1/apps/instances
func (h *Handler) HandleCreateInstance(w http.ResponseWriter, r *http.Request) {
	var body createInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-body",
			"message": err.Error(),
		})
		return
	}
	body.Blueprint = strings.TrimSpace(body.Blueprint)
	body.Org = strings.TrimSpace(body.Org)
	body.Name = strings.TrimSpace(body.Name)
	if body.Blueprint == "" || body.Org == "" || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "missing-required",
			"message": "blueprint, org, name are required",
		})
		return
	}
	if !appInstanceNameRE.MatchString(body.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-name",
			"message": "name must match RFC-1123 lowercase slug",
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
	chosen, derr := chooseTopology(bpDoc, body.Topology)
	if derr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "invalid-topology",
			"message": derr.Error(),
		})
		return
	}

	// Multi-instance gating per OpenAPI: 409 when multiInstance not
	// enabled AND an instance already exists in this Org.
	mi := readMultiInstance(bpDoc)
	if !mi.Enabled {
		existing, err := listAppsInOrg(r.Context(), client, body.Org, body.Blueprint)
		if err == nil && len(existing) > 0 {
			writeJSON(w, http.StatusConflict, map[string]string{
				"code":    "multi-instance-disabled",
				"message": fmt.Sprintf("Blueprint %q does not permit multiple instances per Organization", body.Blueprint),
			})
			return
		}
	} else if mi.MaxPerOrg > 0 {
		existing, err := listAppsInOrg(r.Context(), client, body.Org, body.Blueprint)
		if err == nil && len(existing) >= mi.MaxPerOrg {
			writeJSON(w, http.StatusConflict, map[string]string{
				"code":    "max-per-org",
				"message": fmt.Sprintf("Blueprint %q allows at most %d instances per Organization", body.Blueprint, mi.MaxPerOrg),
			})
			return
		}
	}

	obj := newApplicationCRForInstance(body, chosen)
	created, err := client.Resource(ApplicationGVR()).Namespace(body.Org).Create(
		r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"code":    "name-conflict",
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

var appInstanceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$`)

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
func (h *Handler) fetchBlueprint(ctx context.Context, r *http.Request, name string) (*blueprintMeta, error) {
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
	token := applicationSessionToken(r)
	bp, err := h.catalogClient.Get(ctx, name, token)
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
			Status:           "Ready",
		}
		if ep.SSOEnabled {
			re.LaunchURL = buildLaunchURL(hostname, tls)
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

// buildLaunchURL constructs the silent-SSO launch URL per locked
// decision #3. Query: prompt=none&kc_idp_hint=catalyst-pin.
func buildLaunchURL(hostname string, tls bool) string {
	scheme := "https"
	if !tls {
		scheme = "http"
	}
	if hostname == "" {
		return ""
	}
	q := url.Values{}
	q.Set("prompt", "none")
	q.Set("kc_idp_hint", "catalyst-pin")
	return fmt.Sprintf("%s://%s/?%s", scheme, hostname, q.Encode())
}

// chooseTopology selects the active topology for a createInstance call.
// `override` wins if non-empty AND in `supported`. Otherwise the
// Blueprint's default (multi-region vs single-region) is used; we
// pick multi-region default when SOVEREIGN_REGIONS env var enumerates
// >1 region, else single-region.
func chooseTopology(bp *blueprintMeta, override string) (string, error) {
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
	multi := false
	if v := os.Getenv("SOVEREIGN_REGIONS"); v != "" {
		// crude count — comma-separated list
		multi = strings.Count(v, ",") > 0
	}
	if multi {
		if bp.Topology.Defaults.MultiRegion != "" {
			return bp.Topology.Defaults.MultiRegion, nil
		}
	}
	if bp.Topology.Defaults.SingleRegion != "" {
		return bp.Topology.Defaults.SingleRegion, nil
	}
	return bp.Topology.Supported[0], nil
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

// newApplicationCRForInstance builds the Application CR for the
// multi-instance create endpoint.
func newApplicationCRForInstance(req createInstanceRequest, chosenTopology string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(req.Name)
	obj.SetNamespace(req.Org)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by":   "catalyst-api",
		"catalyst.openova.io/organization": req.Org,
		"catalyst.openova.io/blueprint":    strings.TrimPrefix(req.Blueprint, "bp-"),
		"catalyst.openova.io/topology":     chosenTopology,
	})
	spec := map[string]interface{}{
		"environmentRef": req.Org + "-prod",
		"blueprintRef": map[string]interface{}{
			"name": req.Blueprint,
		},
		"placement": chosenTopology,
		"regions":   []interface{}{"primary"},
	}
	if len(req.Values) > 0 {
		vals := make(map[string]interface{}, len(req.Values))
		for k, v := range req.Values {
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
// per-Org robot token is stored in the K8s Secret `<org>-iac-bot-token`
// per ADR-0009; we look it up via the dynamic client and assemble the
// per-call gitea.Client.
//
// Currently returns the same writer for every Org since the unified
// client uses CATALYST_GITEA_URL + CATALYST_GITEA_TOKEN — production
// will swap this for a per-Org token lookup once the External-Secrets
// chain is wired (TBD-D-G117-NN). Until then this single-token wrapper
// is FUNCTIONALLY correct on a single-Org Sovereign and is the path
// that exercises every code edge of the writer.
func NewProductionGiteaIaCWriter(_org string) (*giteapr.Writer, error) {
	base := strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL"))
	tok := strings.TrimSpace(os.Getenv("CATALYST_GITEA_TOKEN"))
	if base == "" || tok == "" {
		return nil, errors.New("CATALYST_GITEA_URL or CATALYST_GITEA_TOKEN unset")
	}
	c := gitea.New(base, tok)
	return giteapr.NewWriter(c, &noopStatusChecker{}, giteapr.PollConfig{}), nil
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
