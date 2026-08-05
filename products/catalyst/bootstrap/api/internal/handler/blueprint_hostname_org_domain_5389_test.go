// blueprint_hostname_org_domain_5389_test.go — the three #5389 guards.
//
// Root cause (measured live on hw292, dep 1c56518035a83e03, cutoverComplete=true,
// 2026-08-06): every per-Org Blueprint declared its front door as
// `<app>.{{.OrgSlug}}.{{.SovereignFQDN}}`, and the count of live HTTPRoute
// hostnames matching that shape was ZERO. Per-Org apps are served on the
// Organization's POOL domain — agenity.uatco.omani.homes,
// wordpress.uatco.omani.homes, console.uatco.omani.homes. The Open button
// therefore emitted a well-formed URL with no HTTPRoute, no DNS and no cert:
// UAT rows 110/112/114, "launch does not land the user in the app".
//
// Three defects, three guards, each vacuity-checked in BOTH directions —
// every guard below has a sub-test that feeds it the PRE-FIX input and
// asserts the guard FAILS on it, so a green run proves the guard is
// load-bearing rather than proving the scan matched nothing.
//
//  1. TestBlueprints_NoEndpointComposesOrgSlugWithSovereignFQDN
//  2. TestBlueprints_ListedWithHTTPRouteChartDeclaresEndpoints
//  3. TestResolveHostnameTemplate_FailsLoudOnUnresolvedToken (+ the handler
//     path in TestGetLaunchURL_FailsLoud_OnUnresolvedToken)
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ── shared catalog walk ──────────────────────────────────────────────

// blueprintDoc is the slice of a blueprint.yaml these guards read.
type blueprintDoc struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Version    string `yaml:"version"`
		Visibility string `yaml:"visibility"`
		Endpoints  []struct {
			Name             string `yaml:"name"`
			HostnameTemplate string `yaml:"hostnameTemplate"`
		} `yaml:"endpoints"`
	} `yaml:"spec"`
}

// catalogBlueprintFiles returns every platform/<x>/blueprint.yaml and
// products/<x>/blueprint.yaml in the monorepo. Fails the test when the glob
// returns an implausibly small set — a broken glob must never read as "no
// violations found".
func catalogBlueprintFiles(t *testing.T) (root string, files []string) {
	t.Helper()
	root = repoRootFor5389(t)
	for _, tree := range []string{"platform", "products"} {
		matches, err := filepath.Glob(filepath.Join(root, tree, "*", "blueprint.yaml"))
		if err != nil {
			t.Fatalf("glob %s: %v", tree, err)
		}
		files = append(files, matches...)
	}
	if len(files) < 40 {
		t.Fatalf("discovered only %d blueprint.yaml files under platform/ + products/ — the glob is broken "+
			"and this guard would pass on an empty scan", len(files))
	}
	return root, files
}

func readBlueprintDoc(t *testing.T, path string) blueprintDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc blueprintDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

// ── Guard 1 — no template may compose OrgSlug with SovereignFQDN ─────

// reOrgSlugToken / reSovereignFQDNToken match the token IDENTIFIER, not a
// specific brace syntax. resolveHostnameTemplate accepts three source forms
// ({X}, {{.X}}, {{ .X }}) and the catalog-seed adds a fourth by escaping the
// braces for Helm — `{{ "{{" }}.OrgSlug{{ "}}" }}`, in which `.OrgSlug` is
// preceded by `}}` rather than `{`. A brace-anchored pattern silently missed
// that fourth form (caught by the vacuity check below, which is exactly what
// it is for). Since these regexes are only ever applied to hostnameTemplate
// VALUES, the bare identifier cannot appear for any other reason.
var (
	reOrgSlugToken       = regexp.MustCompile(`\bOrgSlug\b`)
	reSovereignFQDNToken = regexp.MustCompile(`\bSovereignFQDN\b`)
)

// composesOrgSlugWithSovereignFQDN is the mechanical rule: a hostnameTemplate
// that names BOTH the Org slug and the Sovereign FQDN is building
// `<app>.<org>.<sovereign-fqdn>`, the host that does not exist. Use
// {OrgDomain} instead — it resolves to the Org's pool domain, falling back to
// `<slug>.<SovereignFQDN>` for a single-domain Org, so it is a strict
// replacement rather than a second dialect.
func composesOrgSlugWithSovereignFQDN(tmpl string) bool {
	return reOrgSlugToken.MatchString(tmpl) && reSovereignFQDNToken.MatchString(tmpl)
}

func TestBlueprints_NoEndpointComposesOrgSlugWithSovereignFQDN(t *testing.T) {
	root, files := catalogBlueprintFiles(t)

	scanned := 0
	for _, f := range files {
		doc := readBlueprintDoc(t, f)
		rel, _ := filepath.Rel(root, f)
		for _, ep := range doc.Spec.Endpoints {
			tmpl := strings.TrimSpace(ep.HostnameTemplate)
			if tmpl == "" {
				continue
			}
			scanned++
			if composesOrgSlugWithSovereignFQDN(tmpl) {
				t.Errorf("#5389 DEAD PER-ORG HOST: %s endpoint %q\n"+
					"    hostnameTemplate = %q\n"+
					"  composes {OrgSlug} with {SovereignFQDN} → <app>.<org>.<sovereign-fqdn>.\n"+
					"  Measured live on hw292: ZERO HTTPRoutes serve that shape; per-Org apps live on the\n"+
					"  Organization's pool domain (agenity.uatco.omani.homes). The Open button would open a\n"+
					"  well-formed URL with no route, no DNS and no certificate behind it.\n"+
					"  Fix: replace `{{.OrgSlug}}.{{.SovereignFQDN}}` with the single token `{{.OrgDomain}}`.",
					rel, ep.Name, tmpl)
			}
		}
	}
	if scanned < 30 {
		t.Fatalf("scanned only %d hostnameTemplates across the catalog — the parser is broken and this "+
			"guard would pass on nothing (expected dozens)", scanned)
	}

	// The catalog-seed is the SECOND declaration of the same endpoints — the
	// in-cluster fallback the bp-catalog-client uses when the gitea
	// catalog-sovereign repo 404s. check-catalog-seed-lockstep.sh compares
	// endpoints[].name and endpoints[].launchDefault but NOT hostnameTemplate,
	// so a seed left on the dead host is invisible to it. Scan the raw
	// template text for the Helm-escaped composition.
	seedPath := filepath.Join(root, "products", "catalyst", "chart", "templates", "catalog-seed", "blueprints.yaml")
	seedRaw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read catalog seed: %v", err)
	}
	seedHostLines := 0
	for i, ln := range strings.Split(string(seedRaw), "\n") {
		if !strings.Contains(ln, "hostnameTemplate") {
			continue
		}
		seedHostLines++
		if composesOrgSlugWithSovereignFQDN(ln) {
			t.Errorf("#5389 DEAD PER-ORG HOST in the catalog-seed fallback: blueprints.yaml:%d\n    %s\n"+
				"  Use the Helm-escaped {{ \"{{\" }}.OrgDomain{{ \"}}\" }} form instead.", i+1, strings.TrimSpace(ln))
		}
	}
	if seedHostLines < 30 {
		t.Fatalf("found only %d hostnameTemplate lines in the catalog seed — the line scan is broken", seedHostLines)
	}

	t.Logf("#5389 guard 1: %d source hostnameTemplates + %d catalog-seed hostnameTemplate lines scanned; none compose OrgSlug with SovereignFQDN",
		scanned, seedHostLines)
}

// TestBlueprints_NoEndpointComposesOrgSlugWithSovereignFQDN_VacuityCheck —
// the OTHER direction. The rule above is only meaningful if it REJECTS the
// pre-#5389 declarations. These are the literal strings that shipped on main
// before this fix (platform/neo4j:55, platform/stalwart-tenant:282,
// platform/wordpress-tenant:278, products/agenity:175, and the seed's escaped
// form), plus the post-fix forms which must be ACCEPTED.
func TestBlueprints_NoEndpointComposesOrgSlugWithSovereignFQDN_VacuityCheck(t *testing.T) {
	mustReject := []string{
		"neo4j.{{.OrgSlug}}.{{.SovereignFQDN}}",
		"mail.{{.OrgSlug}}.{{.SovereignFQDN}}",
		"{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}",
		"agenity.{{.OrgSlug}}.{{.SovereignFQDN}}",
		"{AppName}.{OrgSlug}.{SovereignFQDN}",
		"chat.{{ .OrgSlug }}.{{ .SovereignFQDN }}",
		`      hostnameTemplate: "opensearch.{{ "{{" }}.OrgSlug{{ "}}" }}.{{ "{{" }}.SovereignFQDN{{ "}}" }}"`,
	}
	for _, tmpl := range mustReject {
		if !composesOrgSlugWithSovereignFQDN(tmpl) {
			t.Errorf("guard is VACUOUS: %q is a pre-#5389 dead-host declaration that shipped on main, "+
				"and the rule accepts it", tmpl)
		}
	}

	mustAccept := []string{
		"neo4j.{{.OrgDomain}}",
		"{{.AppName}}.{{.OrgDomain}}",
		"openclaw.{{.OrgDomain}}",
		"{AppName}.{OrgDomain}",
		// Sovereign-singleton apps legitimately name only the Sovereign FQDN.
		"newapi.{{.SovereignFQDN}}",
		"grafana.{{.SovereignFQDN}}",
		"auth.{{.SovereignFQDN}}",
		`      hostnameTemplate: "n8n.{{ "{{" }}.OrgDomain{{ "}}" }}"`,
	}
	for _, tmpl := range mustAccept {
		if composesOrgSlugWithSovereignFQDN(tmpl) {
			t.Errorf("guard is OVER-BROAD: %q is a correct declaration and the rule rejects it", tmpl)
		}
	}
}

// ── Guard 2 — a listed Blueprint with an HTTPRoute chart must declare
//    endpoints (the bp-newapi row-114 shape; bp-openclaw was the second
//    instance) ──────────────────────────────────────────────────────────

// chartRendersHTTPRoute reports whether the Blueprint's co-located chart has a
// template that emits a Gateway-API HTTPRoute — i.e. the Blueprint publishes a
// real front door on a Sovereign.
func chartRendersHTTPRoute(t *testing.T, blueprintDir string) bool {
	t.Helper()
	tmplDir := filepath.Join(blueprintDir, "chart", "templates")
	found := false
	_ = filepath.Walk(tmplDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || found {
			return nil //nolint:nilerr // a missing chart/templates is "no HTTPRoute", not an error
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != ".yaml" && ext != ".yml" && ext != ".tpl" {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil //nolint:nilerr
		}
		if regexp.MustCompile(`(?m)^\s*kind:\s*HTTPRoute\s*$`).Match(raw) {
			found = true
		}
		return nil
	})
	return found
}

func TestBlueprints_ListedWithHTTPRouteChartDeclaresEndpoints(t *testing.T) {
	root, files := catalogBlueprintFiles(t)

	examined := 0
	for _, f := range files {
		doc := readBlueprintDoc(t, f)
		if !strings.EqualFold(strings.TrimSpace(doc.Spec.Visibility), "listed") {
			// unlisted / private / public-but-unlisted platform plumbing —
			// catalyst-edge-routes, gateway-api, oidc-gate, openova-mcp all
			// render an HTTPRoute for the PLATFORM, have no apps-grid card,
			// and are legitimately exempt.
			continue
		}
		dir := filepath.Dir(f)
		if !chartRendersHTTPRoute(t, dir) {
			continue
		}
		examined++
		rel, _ := filepath.Rel(root, f)
		if len(doc.Spec.Endpoints) == 0 {
			t.Errorf("#5389 DARK OPEN BUTTON: %s is `visibility: listed` and its chart renders an HTTPRoute, "+
				"but it declares NO endpoints.\n"+
				"  Handler.userUIGatePasses fails closed on an empty endpoints[], so AppDetail renders no hero\n"+
				"  Open CTA and the AppsPage grid renders no button; pickEndpoint returns nil so\n"+
				"  GET /catalyst/v1/apps/%s/launch-url answers 404 endpoint-not-found.\n"+
				"  This is the bp-newapi row-114 defect. Declare the front door the chart already publishes.",
				rel, doc.Metadata.Name)
		}
	}
	if examined < 5 {
		t.Fatalf("only %d listed Blueprints with an HTTPRoute-rendering chart were examined — the "+
			"HTTPRoute detection or the visibility filter is broken, and this guard would pass on nothing",
			examined)
	}
	t.Logf("#5389 guard 2: %d listed Blueprints render an HTTPRoute; all declare endpoints", examined)
}

// TestBlueprints_ListedWithHTTPRouteChartDeclaresEndpoints_VacuityCheck —
// the OTHER direction, proven against the REAL trees rather than a fixture:
//
//	(a) bp-openclaw — the blueprint this PR fixes — must be seen by BOTH
//	    halves of the predicate (listed AND HTTPRoute-rendering); if either
//	    half missed it, the guard would have been green on the broken tree.
//	(b) the predicate must be capable of firing: with bp-openclaw's endpoints
//	    removed (its exact pre-#5389 shape) the rule must reject it.
//	(c) a Blueprint with no HTTPRoute template must NOT be examined, so the
//	    guard cannot be trivially true by matching everything.
func TestBlueprints_ListedWithHTTPRouteChartDeclaresEndpoints_VacuityCheck(t *testing.T) {
	root := repoRootFor5389(t)

	openclawDir := filepath.Join(root, "platform", "openclaw")
	doc := readBlueprintDoc(t, filepath.Join(openclawDir, "blueprint.yaml"))

	// (a) both halves of the predicate see bp-openclaw.
	if !strings.EqualFold(doc.Spec.Visibility, "listed") {
		t.Fatalf("bp-openclaw visibility = %q — the guard's `listed` filter would skip the very blueprint "+
			"this PR fixes, making it vacuous", doc.Spec.Visibility)
	}
	if !chartRendersHTTPRoute(t, openclawDir) {
		t.Fatal("chartRendersHTTPRoute(platform/openclaw) = false, but platform/openclaw/chart/templates/" +
			"httproute.yaml exists — the HTTPRoute detector is broken and the guard would never fire")
	}

	// (b) the rule fires on bp-openclaw's exact pre-#5389 shape.
	pre := doc
	pre.Spec.Endpoints = nil
	if len(pre.Spec.Endpoints) != 0 {
		t.Fatal("test setup error")
	}
	violates := strings.EqualFold(pre.Spec.Visibility, "listed") &&
		chartRendersHTTPRoute(t, openclawDir) &&
		len(pre.Spec.Endpoints) == 0
	if !violates {
		t.Fatal("the pre-#5389 bp-openclaw shape (listed + HTTPRoute chart + endpoints absent) does NOT " +
			"trip the rule — the guard is vacuous")
	}

	// (b') and the SHIPPED shape must not.
	if len(doc.Spec.Endpoints) == 0 {
		t.Fatal("platform/openclaw/blueprint.yaml still declares NO endpoints — #5389 is not fixed")
	}
	if got, want := strings.TrimSpace(doc.Spec.Endpoints[0].HostnameTemplate), "openclaw.{{.OrgDomain}}"; got != want {
		t.Fatalf("bp-openclaw hostnameTemplate = %q, want %q — README.md §Exposure and the org-gitops "+
			"overlay (organization_gitops.go owHost) both serve openclaw.<subdomain>.<parentDomain>", got, want)
	}

	// (c) a Blueprint whose chart renders no HTTPRoute is not examined.
	if chartRendersHTTPRoute(t, filepath.Join(root, "platform", "cnpg")) {
		t.Error("chartRendersHTTPRoute(platform/cnpg) = true — CNPG publishes no HTTP front door; the " +
			"detector matches too broadly and the guard would flag unrelated blueprints")
	}
}

// ── Guard 3 — the resolver fails LOUD, not open ──────────────────────

func TestResolveHostnameTemplate_FailsLoudOnUnresolvedToken(t *testing.T) {
	full := hostnameVars{
		SovereignFQDN: "hw292.omani.works",
		OrgSlug:       "uatco",
		AppName:       "wp",
		OrgDomain:     "uatco.omani.homes",
	}

	t.Run("resolves the supported vocabulary", func(t *testing.T) {
		cases := map[string]string{
			"agenity.{{.OrgDomain}}":            "agenity.uatco.omani.homes",
			"{{.AppName}}.{{.OrgDomain}}":       "wp.uatco.omani.homes",
			"newapi.{{.SovereignFQDN}}":         "newapi.hw292.omani.works",
			"{AppName}.{OrgSlug}.{OrgDomain}":   "wp.uatco.uatco.omani.homes",
			"{{ .AppName }}.{{ .OrgDomain }}":   "wp.uatco.omani.homes",
			"sandbox-metrics.svc.cluster.local": "sandbox-metrics.svc.cluster.local",
		}
		for tmpl, want := range cases {
			got, err := resolveHostnameTemplate(tmpl, full)
			if err != nil {
				t.Errorf("resolveHostnameTemplate(%q) errored: %v", tmpl, err)
				continue
			}
			if got != want {
				t.Errorf("resolveHostnameTemplate(%q) = %q, want %q", tmpl, got, want)
			}
		}
	})

	t.Run("unknown token is REJECTED, not passed through", func(t *testing.T) {
		// Pre-#5389 these returned the literal lowercased template and
		// buildLaunchURL wrapped it in https://…/ — a dead Open button.
		// `{{.InstanceID}}` is not hypothetical: platform/sandbox ships it and
		// the resolver has never implemented it.
		for _, tmpl := range []string{
			"neo4j.{{.OrgDomian}}",                   // typo
			"sandbox-{{.InstanceID}}.{{.OrgDomain}}", // documented in the CRD, never implemented
			"app.{{.ClusterID}}.example.com",         // ditto
			"{{.Unknown}}",
		} {
			got, err := resolveHostnameTemplate(tmpl, full)
			if err == nil {
				t.Errorf("resolveHostnameTemplate(%q) = %q with NO error — the resolver still fails OPEN; "+
					"buildLaunchURL would publish https://%s/ as the Open button target", tmpl, got, got)
				continue
			}
			if !strings.Contains(err.Error(), "unsubstituted token") {
				t.Errorf("resolveHostnameTemplate(%q) error = %v, want an unsubstituted-token diagnosis", tmpl, err)
			}
			if got != "" {
				t.Errorf("resolveHostnameTemplate(%q) returned host %q alongside the error — a failed "+
					"resolution must yield NO hostname at all", tmpl, got)
			}
		}
	})

	t.Run("empty substitution collapses a label and is REJECTED", func(t *testing.T) {
		// The launch-url handler's HR-backed fallback passes an EMPTY org.
		// Pre-#5389 a per-Org template under that path produced
		// `chat..hw292.omani.works` / `.hw292.omani.works`.
		noOrg := hostnameVars{SovereignFQDN: "hw292.omani.works", AppName: "chat"}
		for _, tmpl := range []string{
			"chat.{{.OrgSlug}}.{{.SovereignFQDN}}",
			"{{.OrgDomain}}",
			"chat.{{.OrgDomain}}",
			"{{.OrgSlug}}.{{.SovereignFQDN}}",
		} {
			got, err := resolveHostnameTemplate(tmpl, noOrg)
			if err == nil {
				t.Errorf("resolveHostnameTemplate(%q) with no Org = %q, want an error — an empty DNS label "+
					"is a host that cannot exist", tmpl, got)
			}
			if got != "" {
				t.Errorf("resolveHostnameTemplate(%q) returned %q alongside the error", tmpl, got)
			}
		}
	})

	t.Run("vacuity: the SAME inputs resolve once the vars are supplied", func(t *testing.T) {
		// Proves the rejections above are caused by the MISSING value, not by
		// the rule rejecting everything.
		for _, tmpl := range []string{
			"chat.{{.OrgSlug}}.{{.SovereignFQDN}}",
			"{{.OrgDomain}}",
			"chat.{{.OrgDomain}}",
		} {
			if _, err := resolveHostnameTemplate(tmpl, full); err != nil {
				t.Errorf("resolveHostnameTemplate(%q) with a full var set errored: %v — the rule rejects "+
					"valid input and the sub-tests above prove nothing", tmpl, err)
			}
		}
	})
}

// TestGetLaunchURL_FailsLoud_OnUnresolvedToken — the same fail-loud contract
// at the HTTP seam the console actually calls, driven through the real router.
// Both directions: a resolvable template answers 200 with the URL, an
// unresolvable one answers 409 hostname-unresolved and NO url field.
func TestGetLaunchURL_FailsLoud_OnUnresolvedToken(t *testing.T) {
	ep := func(tmpl string) []map[string]interface{} {
		return []map[string]interface{}{{
			"name":             "ui",
			"protocol":         "https",
			"port":             443,
			"hostnameTemplate": tmpl,
			"ssoEnabled":       true,
			"launchDefault":    true,
		}}
	}

	t.Run("resolvable template still answers 200", func(t *testing.T) {
		h, _, _ := newTestHandlerWithEndpoint(t)
		h.SetCatalogClient(fakeBlueprintInCatalog("demo", ep("demo.{{.SovereignFQDN}}"), false, []string{"singleton"}))

		rec := httptest.NewRecorder()
		newTestRouter(h).ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/bp-demo/launch-url", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("launch-url = %d (body=%s), want 200", rec.Code, rec.Body.String())
		}
		var resp launchURLResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !strings.HasPrefix(resp.URL, "https://demo.t01.omani.works/") {
			t.Fatalf("launch-url = %q, want the demo.t01.omani.works front door", resp.URL)
		}
	})

	t.Run("unresolved token answers 409 instead of a dead URL", func(t *testing.T) {
		h, _, _ := newTestHandlerWithEndpoint(t)
		h.SetCatalogClient(fakeBlueprintInCatalog("demo", ep("demo.{{.OrgDomian}}"), false, []string{"singleton"}))

		rec := httptest.NewRecorder()
		newTestRouter(h).ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/bp-demo/launch-url", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("launch-url with an unknown token = %d (body=%s), want 409 — pre-#5389 this answered "+
				"200 with https://demo.{{.orgdomian}}/ as the Open button target",
				rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "hostname-unresolved") {
			t.Fatalf("body = %s, want code hostname-unresolved", body)
		}
		var resp launchURLResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.URL != "" {
			t.Fatalf("error response still carries url=%q — no consumer may be handed a dead link", resp.URL)
		}
	})
}
