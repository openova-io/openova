// Render guards for the Keycloak account-console intercept.
//
// UAT row 109 clause (b): "if the Keycloak account console is reached directly
// it fails loud and legibly, and NO console navigation links a User to it."
//
// The no-link half passed on hw293. The loud-and-legible half failed in the
// opposite direction from a refusal: reaching
// https://auth.<fqdn>/realms/sovereign/account/ rendered the branded sign-in,
// the catalyst-pin IdP completed SILENTLY with no PIN prompt, and the browser
// landed on a page whose entire body text was "Loading the Account Console" —
// held 20+ seconds, document.title "Account Management", zero h1/h2 elements,
// no error text, no diagnosis, no way out. Not a backend fault:
// /realms/sovereign/account/config on that same page answered 200 with
// x-envoy-upstream-service-time 12.
//
// That the console cannot work here is definitional, not a defect — a
// Keycloak-native account console cannot mint a REST token from a PIN-brokered
// session (#688). The defect is that it hangs instead of saying so.
package bootstrapkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderKeycloakRoute renders the chart's HTTPRoute template offline.
func renderKeycloakRoute(t *testing.T, extraArgs ...string) map[string]any {
	t.Helper()

	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}

	src := filepath.Join(repoRoot(t), "platform", "keycloak", "chart")
	dir := t.TempDir()

	rawChart, err := os.ReadFile(filepath.Join(src, "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart map[string]any
	if uerr := yaml.Unmarshal(rawChart, &chart); uerr != nil {
		t.Fatalf("parse Chart.yaml: %v", uerr)
	}
	delete(chart, "dependencies")
	outChart, _ := yaml.Marshal(chart)
	if werr := os.WriteFile(filepath.Join(dir, "Chart.yaml"), outChart, 0o644); werr != nil {
		t.Fatalf("write Chart.yaml: %v", werr)
	}
	rawValues, err := os.ReadFile(filepath.Join(src, "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "values.yaml"), rawValues, 0o644); werr != nil {
		t.Fatalf("write values.yaml: %v", werr)
	}
	if merr := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); merr != nil {
		t.Fatalf("mkdir: %v", merr)
	}
	for _, name := range []string{"_helpers.tpl", "httproute.yaml"} {
		raw, rerr := os.ReadFile(filepath.Join(src, "templates", name))
		if rerr != nil {
			t.Fatalf("read templates/%s: %v", name, rerr)
		}
		if werr := os.WriteFile(filepath.Join(dir, "templates", name), raw, 0o644); werr != nil {
			t.Fatalf("write templates/%s: %v", name, werr)
		}
	}

	args := []string{
		"template", "kc", ".",
		"--set", "sovereignFQDN=" + testFQDN,
		"--set", "gateway.enabled=true",
		"--set", "gateway.host=auth." + testFQDN,
	}
	args = append(args, extraArgs...)

	cmd := exec.Command(helmBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template httproute: %v\noutput:\n%s", err, out)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc map[string]any
		if derr := dec.Decode(&doc); derr != nil {
			break
		}
		if doc == nil {
			continue
		}
		if kind, _ := doc["kind"].(string); kind == "HTTPRoute" {
			return doc
		}
	}
	t.Fatalf("chart rendered NO HTTPRoute — the guard would be vacuous\nrendered:\n%s", out)
	return nil
}

// routeRules returns spec.rules, failing loudly when the shape is not what the
// rest of this file assumes.
func routeRules(t *testing.T, route map[string]any) []any {
	t.Helper()
	spec, _ := route["spec"].(map[string]any)
	rules, _ := spec["rules"].([]any)
	if len(rules) == 0 {
		t.Fatal("HTTPRoute rendered ZERO rules — vacuous")
	}
	return rules
}

// findExactPathRule returns the rule whose matches include an Exact path equal
// to want, or nil.
func findExactPathRule(rules []any, want string) map[string]any {
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		matches, _ := rule["matches"].([]any)
		for _, mraw := range matches {
			m, _ := mraw.(map[string]any)
			p, _ := m["path"].(map[string]any)
			if pt, _ := p["type"].(string); pt != "Exact" {
				continue
			}
			if pv, _ := p["value"].(string); pv == want {
				return rule
			}
		}
	}
	return nil
}

// TestAccountConsole_EntryPathsAreIntercepted is the SUBJECT.
func TestAccountConsole_EntryPathsAreIntercepted(t *testing.T) {
	rules := routeRules(t, renderKeycloakRoute(t))

	for _, want := range []string{"/realms/sovereign/account", "/realms/sovereign/account/"} {
		rule := findExactPathRule(rules, want)
		if rule == nil {
			t.Errorf("no rule matches Exact %q — the account-console SPA still boots and "+
				"hangs forever on 'Loading the Account Console' with no error text (UAT row 109)", want)
			continue
		}
		filters, _ := rule["filters"].([]any)
		if len(filters) == 0 {
			t.Errorf("rule for %q has no filters — it would proxy to Keycloak, not intercept", want)
			continue
		}
		f, _ := filters[0].(map[string]any)
		if ft, _ := f["type"].(string); ft != "RequestRedirect" {
			t.Errorf("rule for %q filter type = %q, want RequestRedirect", want, ft)
			continue
		}
		rr, _ := f["requestRedirect"].(map[string]any)
		if got, _ := rr["hostname"].(string); got != "console."+testFQDN {
			t.Errorf("rule for %q redirect hostname = %q, want %q — the User must land on "+
				"the console surface that actually serves their profile", want, got, "console."+testFQDN)
		}
		// Port is asserted as a VALUE because omitting it is the #3310 class:
		// Gateway API copies the cilium listener port into Location.
		if got, _ := rr["port"].(int); got != 443 {
			t.Errorf("rule for %q redirect port = %v, want 443 (#3310 — an omitted port "+
				"leaks the cilium listener port into Location)", want, rr["port"])
		}
		path, _ := rr["path"].(map[string]any)
		if got, _ := path["replaceFullPath"].(string); got != "/settings" {
			t.Errorf("rule for %q redirect path = %q, want /settings", want, got)
		}
	}
}

// TestAccountConsole_RestSurfaceIsNotIntercepted is the CONTROL that holds the
// intercept to Exact matches.
//
// A PathPrefix on /realms/<realm>/account would also capture
// /realms/<realm>/account/config and the rest of the account REST API, turning
// a UI hang into an API outage — and /account/config is precisely the endpoint
// that answered 200 during the walk, proving the backend healthy.
func TestAccountConsole_RestSurfaceIsNotIntercepted(t *testing.T) {
	rules := routeRules(t, renderKeycloakRoute(t))

	for _, rule := range rules {
		r, _ := rule.(map[string]any)
		filters, _ := r["filters"].([]any)
		if len(filters) == 0 {
			continue // the proxy rule
		}
		matches, _ := r["matches"].([]any)
		for _, mraw := range matches {
			m, _ := mraw.(map[string]any)
			p, _ := m["path"].(map[string]any)
			pt, _ := p["type"].(string)
			pv, _ := p["value"].(string)
			if pt == "PathPrefix" && strings.Contains(pv, "account") {
				t.Errorf("a REDIRECT rule uses PathPrefix %q — this captures "+
					"/realms/sovereign/account/config and the whole account REST surface", pv)
			}
		}
	}
}

// TestAccountConsole_ProxyCatchAllSurvives is the CONTROL that the intercept did
// not eat the route. Every other path on auth.<fqdn> — the realm endpoints, the
// admin console, the broker legs row 219 depends on — must still reach Keycloak.
func TestAccountConsole_ProxyCatchAllSurvives(t *testing.T) {
	rules := routeRules(t, renderKeycloakRoute(t))

	found := false
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		backends, _ := rule["backendRefs"].([]any)
		if len(backends) == 0 {
			continue
		}
		matches, _ := rule["matches"].([]any)
		for _, mraw := range matches {
			m, _ := mraw.(map[string]any)
			p, _ := m["path"].(map[string]any)
			if pt, _ := p["type"].(string); pt != "PathPrefix" {
				continue
			}
			if pv, _ := p["value"].(string); pv == "/" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the PathPrefix / → keycloak backend rule is gone — every realm, broker " +
			"and admin-console path on the auth host would stop resolving")
	}
}

// TestAccountConsole_InterceptIsDisableable is the CONTROL that keeps the knob a
// knob: an operator who wants the stock console back must be able to have it.
func TestAccountConsole_InterceptIsDisableable(t *testing.T) {
	rules := routeRules(t, renderKeycloakRoute(t,
		"--set", "gateway.accountConsoleRedirect.enabled=false"))

	if rule := findExactPathRule(rules, "/realms/sovereign/account"); rule != nil {
		t.Error("intercept still rendered with gateway.accountConsoleRedirect.enabled=false")
	}
}

// TestAccountConsole_InterceptFollowsTheRealmName proves the path is built from
// the configured realm rather than a hardcoded "sovereign". A Sovereign whose
// realm is its tenant short-name would otherwise be silently unprotected.
func TestAccountConsole_InterceptFollowsTheRealmName(t *testing.T) {
	rules := routeRules(t, renderKeycloakRoute(t, "--set", "sovereignRealm.name=omantel"))

	if rule := findExactPathRule(rules, "/realms/omantel/account"); rule == nil {
		t.Error("with sovereignRealm.name=omantel, no rule matches /realms/omantel/account — " +
			"the intercept is pinned to a hardcoded realm and misses every renamed realm")
	}
	if rule := findExactPathRule(rules, "/realms/sovereign/account"); rule != nil {
		t.Error("the intercept still matches /realms/sovereign/account on a realm named omantel")
	}
}
