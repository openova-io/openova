// organization_funnel_apps.go — ADR-0015 D5: one install path.
//
// The signup funnel (runOrganizationPipeline) used to commit the Org's
// applications — bp-newapi, bp-wordpress-tenant, bp-openclaw,
// bp-stalwart-tenant, bp-agenity — into the legacy org-tenants GitOps
// overlay as BARE HelmReleases, beside (never through) the Application CR
// the console creates. Two install primitives meant two status models, two
// placement models and Organizations whose apps were invisible to every
// Application-keyed surface (acmewalk307 on hw307: six HelmReleases, zero
// Application CRs).
//
// Now the overlay carries only the Org substrate (namespace, the per-Org
// Keycloak IdP, the BYO certificate), and each application becomes an
// Application CR through createApplicationInstance — THE function the
// console dialog calls — once the Organization's boundary is observed
// Ready. The Helm values the overlay used to inline become the
// Application's spec.parameters verbatim, so the rendered workload is
// unchanged; placement, regions and Environment are resolved by the same
// reference validation every other door uses (ADR-0015 D4).
package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/application/admission"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// funnelApplicationFiles are the overlay templates that describe an
// APPLICATION (not Org substrate). They are rendered — their values are
// the Application parameters — but never committed as HelmReleases.
var funnelApplicationFiles = map[string]bool{
	"bp-newapi.yaml":           true,
	"bp-wordpress-tenant.yaml": true,
	"bp-openclaw.yaml":         true,
	"bp-stalwart-tenant.yaml":  true,
	"bp-agenity.yaml":          true,
}

// isFunnelApplicationFile reports whether an overlay file is an
// Application the funnel installs through createApplicationInstance.
func isFunnelApplicationFile(name string) bool { return funnelApplicationFiles[name] }

// funnelApplication is one application the funnel installs.
type funnelApplication struct {
	// Name — the Application name (the HelmRelease name without "bp-").
	Name string
	// Blueprint — the Blueprint CR name (the chart name, e.g. bp-openclaw).
	Blueprint string
	// Parameters — the Helm values, verbatim.
	Parameters map[string]interface{}
}

// funnelApplicationsFromOverlay extracts the applications from a rendered
// overlay. Order is deterministic (by file name).
func funnelApplicationsFromOverlay(files map[string]string) ([]funnelApplication, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		if isFunnelApplicationFile(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]funnelApplication, 0, len(names))
	for _, n := range names {
		var hr struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Chart struct {
					Spec struct {
						Chart string `json:"chart"`
					} `json:"spec"`
				} `json:"chart"`
				Values map[string]interface{} `json:"values"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(files[n]), &hr); err != nil {
			return nil, fmt.Errorf("parse %s: %w", n, err)
		}
		bp := strings.TrimSpace(hr.Spec.Chart.Spec.Chart)
		if bp == "" {
			return nil, fmt.Errorf("%s: no chart name", n)
		}
		params := hr.Spec.Values
		if params == nil {
			params = map[string]interface{}{}
		}
		out = append(out, funnelApplication{
			Name:       strings.TrimPrefix(hr.Metadata.Name, "bp-"),
			Blueprint:  bp,
			Parameters: params,
		})
	}
	return out, nil
}

// tenantOverlayRenderer is implemented by the production GitOps writer: it
// renders (without committing) the overlay the funnel applications are
// read from. Test doubles that do not implement it install nothing.
type tenantOverlayRenderer interface {
	RenderTenantOverlay(rec store.OrganizationProvisionRecord) (map[string]string, error)
}

// RenderTenantOverlay renders the per-tenant overlay without writing it.
func (w DefaultOrganizationGitOpsWriter) RenderTenantOverlay(rec store.OrganizationProvisionRecord) (map[string]string, error) {
	return renderOrganizationOverlay(rec, w.ChartVersions)
}

// installFunnelApplications creates the Org's Application CRs through the
// shared create function. Returns ready=false while the Organization's
// references (Organization CR, Environment, namespace) are not yet
// resolvable, so the pipeline holds non-terminal and retries; an
// Application that already exists counts as installed (idempotent).
func (h *Handler) installFunnelApplications(ctx context.Context, rec store.OrganizationProvisionRecord) (bool, error) {
	if perOrgGitopsEnabled() {
		// The per-Org GitOps tree owns this Org; the legacy overlay never
		// carried its applications either (#5425).
		return true, nil
	}
	renderer, ok := h.orgTenantDeps.GitOps.(tenantOverlayRenderer)
	if !ok {
		return true, nil
	}
	files, err := renderer.RenderTenantOverlay(rec)
	if err != nil {
		return false, fmt.Errorf("render funnel applications: %w", err)
	}
	apps, err := funnelApplicationsFromOverlay(files)
	if err != nil {
		return false, err
	}
	if len(apps) == 0 {
		return true, nil
	}
	client, err := h.dynamicClientOrFallback()
	if err != nil {
		return false, err
	}
	org := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	allReady := true
	for _, app := range apps {
		body := instances.CreateInstanceRequest{
			Blueprint: app.Blueprint,
			Org:       org,
			Name:      app.Name,
			Values:    app.Parameters,
		}
		body.Sanitise()
		if shapeErr := body.ValidateShape(); shapeErr != nil {
			return false, fmt.Errorf("funnel application %s: %s", app.Name, shapeErr.Message)
		}
		_, _, cerr := h.createApplicationInstance(ctx, client, &body, "")
		switch {
		case cerr == nil:
			h.log.Info("org-tenant: funnel Application created (ADR-0015 D5)",
				"org", org, "application", app.Name, "blueprint", app.Blueprint)
		case cerr.Status == http.StatusConflict && (cerr.Code == string(admission.CodeNameCollision) ||
			cerr.Code == string(admission.CodeMultiInstanceDisabled)):
			// Already installed (this run's earlier pass, or the Org already
			// holds its single instance of the Blueprint) — idempotent.
		case cerr.Status == http.StatusConflict || cerr.Status == http.StatusUnprocessableEntity ||
			cerr.Status == http.StatusServiceUnavailable:
			// The Organization's references are not resolvable YET (no
			// Environment / namespace) or the apiserver blipped: hold.
			h.log.Info("org-tenant: funnel Application waiting for references",
				"org", org, "application", app.Name, "code", cerr.Code, "detail", cerr.Message)
			allReady = false
		default:
			h.log.Warn("org-tenant: funnel Application create failed",
				"org", org, "application", app.Name, "code", cerr.Code, "detail", cerr.Message)
			allReady = false
		}
	}
	return allReady, nil
}
