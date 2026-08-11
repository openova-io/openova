// Render guards for platform/keycloak/chart.
//
// UAT row 219 (#6087) — the catalyst-pin IdP's backchannel legs.
//
// The row's failing clause is "chepherd console reachable in his Org". Two
// walkers agreed the Application converges (HelmRelease Ready, pod Ready,
// route + gate healthy) and disagreed only on the last hop: following the
// gate's redirect to completion in a real browser terminates at
// https://auth.<fqdn>/realms/sovereign/broker/catalyst-pin/endpoint?code=...
// with HTTP 502, rendering Keycloak's own error page ("Unexpected error when
// authenticating with identity provider"). Not the region-B transport artifact:
// a sibling asset on the SAME page load answered 200 with an
// x-envoy-upstream-service-time header, so the origin was reached.
//
// /broker/<alias>/endpoint is the token-exchange callback. Keycloak's
// AbstractOAuth2IdentityProvider answers 502 there when its own SERVER-SIDE
// call to the upstream fails — which is exactly what the realm config asked for
// by pointing tokenUrl / userInfoUrl / jwksUrl at the PUBLIC api.<sov-fqdn>.
package bootstrapkit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testFQDN = "hw293.omantel.biz"

// miniChart builds an offline, dependency-free copy of this wrapper chart
// containing only the realm ConfigMap template and the helpers it includes.
func miniChart(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(repoRoot(t), "platform", "keycloak", "chart")

	rawChart, err := os.ReadFile(filepath.Join(src, "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart map[string]any
	if uerr := yaml.Unmarshal(rawChart, &chart); uerr != nil {
		t.Fatalf("parse Chart.yaml: %v", uerr)
	}
	delete(chart, "dependencies")
	outChart, err := yaml.Marshal(chart)
	if err != nil {
		t.Fatalf("marshal Chart.yaml: %v", err)
	}
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
		t.Fatalf("mkdir templates: %v", merr)
	}
	for _, name := range []string{"_helpers.tpl", "configmap-sovereign-realm.yaml"} {
		raw, rerr := os.ReadFile(filepath.Join(src, "templates", name))
		if rerr != nil {
			t.Fatalf("read templates/%s: %v", name, rerr)
		}
		if werr := os.WriteFile(filepath.Join(dir, "templates", name), raw, 0o644); werr != nil {
			t.Fatalf("write templates/%s: %v", name, werr)
		}
	}
	return dir
}

// renderRealm runs `helm template` and returns the decoded sovereign realm JSON.
func renderRealm(t *testing.T, extraArgs ...string) map[string]any {
	t.Helper()

	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}

	// Render from a MINIMAL offline copy: the wrapper's realm ConfigMap plus
	// the helpers it includes, with the upstream bitnami/keycloak dependency
	// stripped from Chart.yaml. The realm JSON is authored entirely by this
	// wrapper template, so nothing under test is lost — and the guard then
	// needs no `helm dependency build` and no network.
	chartDir := miniChart(t)

	args := []string{
		"template", "kc", ".",
		"--set", "sovereignFQDN=" + testFQDN,
		"--set", "sovereignRealm.enabled=true",
	}
	args = append(args, extraArgs...)

	cmd := exec.Command(helmBin, args...)
	cmd.Dir = chartDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\noutput:\n%s", err, out)
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
		if kind, _ := doc["kind"].(string); kind != "ConfigMap" {
			continue
		}
		data, _ := doc["data"].(map[string]any)
		for key, v := range data {
			if !strings.Contains(key, "realm") {
				continue
			}
			raw, ok := v.(string)
			if !ok {
				continue
			}
			var realm map[string]any
			if jerr := json.Unmarshal([]byte(raw), &realm); jerr != nil {
				continue
			}
			if _, has := realm["identityProviders"]; has {
				return realm
			}
		}
	}
	t.Fatalf("no ConfigMap carried a realm JSON with identityProviders\nrendered:\n%s", out)
	return nil
}

// catalystPinConfig digs out the catalyst-pin IdP's config block.
//
// It FAILS rather than skipping when the provider is absent: a guard that
// silently finds nothing to check is the "tested a surface that cannot fail"
// shape, and this test's whole value is that it renders a real provider.
func catalystPinConfig(t *testing.T, realm map[string]any) map[string]any {
	t.Helper()
	idps, _ := realm["identityProviders"].([]any)
	if len(idps) == 0 {
		t.Fatalf("realm rendered ZERO identityProviders — the fixture is inert and this guard could not fail")
	}
	for _, raw := range idps {
		idp, _ := raw.(map[string]any)
		if alias, _ := idp["alias"].(string); alias != "catalyst-pin" {
			continue
		}
		cfg, _ := idp["config"].(map[string]any)
		if len(cfg) == 0 {
			t.Fatalf("catalyst-pin rendered an EMPTY config block")
		}
		return cfg
	}
	t.Fatalf("catalyst-pin identity provider not found among %d providers", len(idps))
	return nil
}

func mustString(t *testing.T, cfg map[string]any, key string) string {
	t.Helper()
	v, ok := cfg[key].(string)
	if !ok {
		t.Fatalf("catalyst-pin config.%s is absent or not a string (got %#v) — "+
			"asserting on the KEY alone would have passed here", key, cfg[key])
	}
	if strings.TrimSpace(v) == "" {
		t.Fatalf("catalyst-pin config.%s rendered EMPTY", key)
	}
	return v
}

// TestCatalystPin_BackchannelLegsAreInCluster is the SUBJECT.
//
// tokenUrl, userInfoUrl and jwksUrl are dialed server-side from the Keycloak
// Pod. They must not name the Sovereign's own public host.
func TestCatalystPin_BackchannelLegsAreInCluster(t *testing.T) {
	cfg := catalystPinConfig(t, renderRealm(t))

	publicHost := "api." + testFQDN
	for _, key := range []string{"tokenUrl", "userInfoUrl", "jwksUrl"} {
		got := mustString(t, cfg, key)
		if strings.Contains(got, publicHost) {
			t.Errorf("catalyst-pin config.%s = %q — this leg is dialed SERVER-SIDE from "+
				"inside the Keycloak Pod, and %q is the Sovereign's own public EIP. The "+
				"hairpin does not work on Huawei (#3241/#3844), which is the HTTP 502 at "+
				"/broker/catalyst-pin/endpoint recorded in UAT row 219.", key, got, publicHost)
		}
		// #6172: the host is the backchannel ANCHOR Service bp-keycloak renders
		// itself, NOT `catalyst-api`. Keycloak accepts a plaintext
		// identity-provider URL only while its host resolves to a private
		// address, and `catalyst-api` is created by bp-catalyst-platform at
		// bootstrap-kit slot 13 — behind bp-gitea, behind bp-keycloak at slot
		// 09 — so at import time it is NXDOMAIN and the realm import 400s with
		// `The url [token_url] requires secure connections`. Asserting the
		// literal `catalyst-api` here would re-pin the wedge.
		if !strings.HasPrefix(got, "http://catalyst-api-backchannel.catalyst-system.svc") {
			t.Errorf("catalyst-pin config.%s = %q — want the in-cluster backchannel anchor "+
				"Service catalyst-api-backchannel.catalyst-system, which bp-keycloak renders "+
				"at slot 09 so the host resolves before bp-catalyst-platform creates "+
				"catalyst-api at slot 13 (#6172)", key, got)
		}
		// The anchor must not be collapsed back onto bp-catalyst-platform's own
		// Service name: Helm refuses a resource another release owns, so that
		// would move the wedge from slot 09 to slot 13.
		if strings.HasPrefix(got, "http://catalyst-api.catalyst-system.svc") {
			t.Errorf("catalyst-pin config.%s = %q — this is the Service name "+
				"products/catalyst/chart/templates/api-service.yaml owns in the same "+
				"namespace. bp-keycloak cannot render it without failing "+
				"bp-catalyst-platform's install with `exists and cannot be imported "+
				"into the current release` (#6172).", key, got)
		}
		if !strings.HasSuffix(got, oidcPath(key)) {
			t.Errorf("catalyst-pin config.%s = %q — lost its %q path in the rewrite", key, got, oidcPath(key))
		}
	}
}

func oidcPath(key string) string {
	switch key {
	case "tokenUrl":
		return "/oidc/token"
	case "userInfoUrl":
		return "/oidc/userinfo"
	case "jwksUrl":
		return "/oidc/certs"
	}
	return ""
}

// TestCatalystPin_FrontchannelAndIssuerStayPublic is the CONTROL that stops the
// fix from being "rewrite every URL inward".
//
// authorizationUrl is a BROWSER redirect — an in-cluster host there would send
// the user's browser to a name it cannot resolve, breaking login outright.
// issuer is compared against the `iss` claim catalyst-api mints from its own
// SOVEREIGN_FQDN, so an internal issuer trades a connectivity failure for a
// signature-validation failure. Both must stay public.
func TestCatalystPin_FrontchannelAndIssuerStayPublic(t *testing.T) {
	cfg := catalystPinConfig(t, renderRealm(t))

	wantAuthorize := "https://api." + testFQDN + "/oidc/auth"
	if got := mustString(t, cfg, "authorizationUrl"); got != wantAuthorize {
		t.Errorf("catalyst-pin config.authorizationUrl = %q, want %q — the browser "+
			"resolves this one; an in-cluster host here breaks login for every User", got, wantAuthorize)
	}
	wantIssuer := "https://api." + testFQDN
	if got := mustString(t, cfg, "issuer"); got != wantIssuer {
		t.Errorf("catalyst-pin config.issuer = %q, want %q — this is matched against "+
			"the id_token `iss` catalyst-api mints from SOVEREIGN_FQDN", got, wantIssuer)
	}
}

// TestCatalystPin_SignatureValidationStaysOn is the CONTROL against the
// tempting wrong fix.
//
// Turning validateSignature/useJwksUrl off would ALSO stop the 502, by removing
// the jwks fetch that was failing — and it would silently accept unverified
// id_tokens from the broker. A fix that made the subject pass by weakening this
// is strictly worse than the defect.
func TestCatalystPin_SignatureValidationStaysOn(t *testing.T) {
	cfg := catalystPinConfig(t, renderRealm(t))

	for key, want := range map[string]string{"validateSignature": "true", "useJwksUrl": "true"} {
		if got := mustString(t, cfg, key); got != want {
			t.Errorf("catalyst-pin config.%s = %q, want %q — the backchannel fix must not "+
				"buy its green by disabling signature verification", key, got, want)
		}
	}
}

// TestCatalystPin_EmptyInternalURLRestoresPublicLegs is the CONTROL that keeps
// the knob a knob. A cluster whose EIP does hairpin must be able to opt out and
// get exactly the pre-fix render back.
func TestCatalystPin_EmptyInternalURLRestoresPublicLegs(t *testing.T) {
	cfg := catalystPinConfig(t, renderRealm(t, "--set", "catalystAPIInternalURL="))

	for _, key := range []string{"tokenUrl", "userInfoUrl", "jwksUrl"} {
		got := mustString(t, cfg, key)
		if !strings.HasPrefix(got, "https://api."+testFQDN) {
			t.Errorf("with catalystAPIInternalURL empty, config.%s = %q — want the public "+
				"leg restored; the knob must be able to turn the behaviour OFF", key, got)
		}
	}
}

// TestCatalystPin_HonoursAnOperatorSuppliedInternalURL proves the value is
// really read, rather than the template having hardcoded the Service name.
func TestCatalystPin_HonoursAnOperatorSuppliedInternalURL(t *testing.T) {
	cfg := catalystPinConfig(t, renderRealm(t,
		"--set", "catalystAPIInternalURL=http://elsewhere.example:9090/"))

	// The trailing slash is deliberate: it proves the join does not produce
	// `//oidc/token`.
	want := map[string]string{
		"tokenUrl":    "http://elsewhere.example:9090/oidc/token",
		"userInfoUrl": "http://elsewhere.example:9090/oidc/userinfo",
		"jwksUrl":     "http://elsewhere.example:9090/oidc/certs",
	}
	for key, exp := range want {
		if got := mustString(t, cfg, key); got != exp {
			t.Errorf("config.%s = %q, want %q", key, got, exp)
		}
	}
}

// TestRealmRendersTheProviderAtAll is the VACUITY CHECK.
//
// Every assertion above is reached through catalystPinConfig. If the realm ever
// stopped rendering the provider — a values rename, a guard clause, a broken
// template — those tests would t.Fatal rather than pass, but this names the
// precondition explicitly so a reader can see the guard has something to bite.
func TestRealmRendersTheProviderAtAll(t *testing.T) {
	realm := renderRealm(t)
	idps, _ := realm["identityProviders"].([]any)
	if len(idps) == 0 {
		t.Fatal("realm rendered ZERO identityProviders — every other test here is vacuous")
	}
	cfg := catalystPinConfig(t, realm)
	if _, ok := cfg["tokenUrl"]; !ok {
		t.Fatal("catalyst-pin config has no tokenUrl key at all — the guard is vacuous")
	}
}
