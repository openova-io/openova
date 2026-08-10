package gitops

import (
	"fmt"
	"strings"
	"testing"
)

// #5987 — the funnel-rendered bp-newapi HelmRelease could not install, and
// once it could, it installed EMPTY.
//
// TWO defects, both in generateNewAPIHR's `values:` block, both proven by
// `helm template` against platform/newapi/chart (chart, command and
// --api-versions held identical across every run; only the values file
// changed):
//
//  1. RENDER-BLOCKING. The block passed no `catalystIntegration` at all.
//     bp-newapi's values.yaml defaults `catalystIntegration.enabled: true` +
//     `externalSecret.enabled: true` with `remoteRef.key: ""`, and
//     templates/external-secret.yaml turns that combination into an explicit
//     `{{- fail }}`. Rendering the funnel values exited 1 with
//     "catalystIntegration.externalSecret.remoteRef.key is empty".
//     Live impact: #5969 added `openclaw ⇒ newapi` to impliedHelmReleaseApps,
//     so EVERY openclaw Org — not just an Org that bought newapi — renders
//     this HR.
//
//  2. SILENTLY-EMPTY. The block stamped a TOP-LEVEL `oidc.issuerURL` and a
//     TOP-LEVEL `httpRoute:` — neither key exists in bp-newapi's values.yaml
//     (the chart reads `auth.adminUI.keycloak.*` and `ingress.httpRoute.*`).
//     Helm drops unknown values silently, so deployment.yaml's $kcConfigured
//     gate (mode==keycloak AND issuer AND existingSecret) was never met and
//     httproute.yaml's `.Values.ingress.httpRoute.enabled` gate was never
//     true. Fixing (1) alone produced a release with NO Deployment and NO
//     HTTPRoute — i.e. `api.<slug>.<parent>`, the exact host
//     generateOpenClawHR stamps into openclaw's llm.baseURL, still served
//     nothing. That is UAT row 225's dangling-host shape, unfixed.
//
// These tests pin the CHART-SIDE contract, not the wording of the template:
// each assertion below restates a gate that lives in platform/newapi/chart
// and names it.

// ─────────────────────────────────────────────────────────────────────────
// yamlScalar — a deliberately small, indentation-aware lookup for a dotted
// path through a 2-space-indented YAML mapping. It exists so these tests
// assert on the VALUE at a chart-recognised PATH instead of grepping for a
// substring: `grep "issuer:"` passes just as happily on a top-level `oidc:`
// block the chart never reads, which is precisely the defect (2) above.
//
// TestYAMLScalar_SelfCheck below is its vacuity guard — it proves the lookup
// can miss, and that it refuses to skip a level.
// ─────────────────────────────────────────────────────────────────────────
func yamlScalar(block, path string) (string, bool) {
	segs := strings.Split(path, ".")
	lines := strings.Split(block, "\n")
	depth, idx := 0, 0
	for si, seg := range segs {
		found := false
		for ; idx < len(lines); idx++ {
			raw := lines[idx]
			trimmed := strings.TrimLeft(raw, " ")
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			ind := len(raw) - len(trimmed)
			if ind < depth {
				// Left the parent mapping without finding the key.
				return "", false
			}
			if ind > depth {
				// Belongs to a sibling's sub-tree.
				continue
			}
			if !strings.HasPrefix(trimmed, seg+":") {
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, seg+":"))
			if si == len(segs)-1 {
				return rest, true
			}
			idx++
			depth += 2
			found = true
			break
		}
		if !found {
			return "", false
		}
	}
	return "", false
}

// hrValuesBlock returns the HelmRelease document's `spec.values:` mapping,
// dedented to column 0 so yamlScalar paths read like chart values paths.
func hrValuesBlock(t *testing.T, doc string) string {
	t.Helper()
	var hr string
	for _, d := range strings.Split(doc, "\n---\n") {
		if strings.Contains(d, "kind: HelmRelease") {
			hr = d
			break
		}
	}
	if hr == "" {
		t.Fatalf("no HelmRelease document in:\n%s", doc)
	}
	var out []string
	grabbing := false
	for _, l := range strings.Split(hr, "\n") {
		if l == "  values:" {
			grabbing = true
			continue
		}
		if !grabbing {
			continue
		}
		if strings.TrimSpace(l) == "" {
			out = append(out, "")
			continue
		}
		if !strings.HasPrefix(l, "    ") {
			break
		}
		out = append(out, l[4:])
	}
	if len(out) == 0 {
		t.Fatalf("HelmRelease carries no spec.values block:\n%s", hr)
	}
	return strings.Join(out, "\n")
}

// newapiValuesFaults applies the three bp-newapi chart gates the funnel HR
// must satisfy, and returns one line per violated gate. Empty result == the
// chart renders a NewAPI that actually exists.
//
// Every gate below is a literal condition in platform/newapi/chart:
//
//   - templates/external-secret.yaml:42 + templates/admin-token-pushsecret.yaml:71
//     `{{- fail }}` when catalystIntegration is on and remoteRef.key is empty.
//   - templates/deployment.yaml:35,81 $kcConfigured — no Deployment without
//     auth.adminUI.keycloak.issuer AND .existingSecret.
//   - templates/httproute.yaml:32 — no HTTPRoute without
//     ingress.httpRoute.enabled.
func newapiValuesFaults(values string) []string {
	var faults []string

	ciEnabled, ciSet := yamlScalar(values, "catalystIntegration.enabled")
	remoteKey, _ := yamlScalar(values, "catalystIntegration.externalSecret.remoteRef.key")
	// The chart DEFAULTS catalystIntegration.enabled + externalSecret.enabled
	// to true, so "unset" is the failing case, not the safe one.
	if !ciSet || ciEnabled != "false" {
		if strings.TrimSpace(remoteKey) == "" {
			faults = append(faults, "catalystIntegration is left at its enabled-by-default state with an empty externalSecret.remoteRef.key — external-secret.yaml:42 `fail`s the render")
		}
	}

	issuer, _ := yamlScalar(values, "auth.adminUI.keycloak.issuer")
	if strings.TrimSpace(issuer) == "" {
		faults = append(faults, "auth.adminUI.keycloak.issuer is empty — deployment.yaml's $kcConfigured gate is unmet and the release renders NO Deployment")
	}
	kcSecret, _ := yamlScalar(values, "auth.adminUI.keycloak.existingSecret")
	if strings.TrimSpace(kcSecret) == "" {
		faults = append(faults, "auth.adminUI.keycloak.existingSecret is empty — deployment.yaml's $kcConfigured gate is unmet and the release renders NO Deployment")
	}

	route, _ := yamlScalar(values, "ingress.httpRoute.enabled")
	if strings.TrimSpace(route) != "true" {
		faults = append(faults, "ingress.httpRoute.enabled is not true — httproute.yaml renders nothing and api.<slug>.<parent> is served by no route")
	}

	return faults
}

// TestYAMLScalar_SelfCheck is the vacuity guard for the two helpers above.
// Without it, a lookup that silently returned ("", false) for EVERY path
// would make newapiValuesFaults report faults forever (or, with the
// polarity flipped, never) regardless of the code under test.
func TestYAMLScalar_SelfCheck(t *testing.T) {
	fixture := strings.Join([]string{
		"sovereignFQDN: acme.omani.homes",
		"auth:",
		"  adminUI:",
		"    # a comment that must be skipped",
		"    mode: keycloak",
		"    keycloak:",
		"      issuer: https://kc.example/realms/org-acme",
		"      existingSecret: newapi-oidc-client-secret",
		"ingress:",
		"  enabled: false",
		"  httpRoute:",
		"    enabled: true",
	}, "\n")

	hits := map[string]string{
		"sovereignFQDN":                        "acme.omani.homes",
		"auth.adminUI.mode":                    "keycloak",
		"auth.adminUI.keycloak.issuer":         "https://kc.example/realms/org-acme",
		"ingress.enabled":                      "false",
		"ingress.httpRoute.enabled":            "true",
		"auth.adminUI.keycloak.existingSecret": "newapi-oidc-client-secret",
	}
	for path, want := range hits {
		got, ok := yamlScalar(fixture, path)
		if !ok || got != want {
			t.Errorf("yamlScalar(%q) = (%q, %v), want (%q, true)", path, got, ok, want)
		}
	}

	// It must MISS what is absent, and it must not skip a level — a lookup
	// that flattened the tree would find `mode` under `auth.mode` and would
	// therefore have found the old top-level `oidc.issuerURL` acceptable.
	for _, path := range []string{
		"catalystIntegration.enabled",
		"auth.mode",
		"auth.adminUI.keycloak.nonexistent",
		"httpRoute.enabled", // the WRONG (top-level) key the defect used
	} {
		if got, ok := yamlScalar(fixture, path); ok {
			t.Errorf("yamlScalar(%q) = (%q, true), want a miss", path, got)
		}
	}
}

// TestNewAPIValuesFaults_FlagsThePreFixBlock is the second half of the
// vacuity guard, and the one that matters most: it feeds newapiValuesFaults
// the EXACT values block generateNewAPIHR emitted before #5987 and asserts
// all three chart gates are reported. A fixture that omitted the fields the
// bug keys on would let the checker pass on data that could never exercise
// the defect — this pins that it cannot.
func TestNewAPIValuesFaults_FlagsThePreFixBlock(t *testing.T) {
	preFix := strings.Join([]string{
		"sovereignFQDN: acme.omani.homes",
		"oidc:",
		"  issuerURL: https://keycloak.acme.omani.homes/realms/org-acme",
		"ingress:",
		"  enabled: false",
		"httpRoute:",
		"  enabled: true",
		"  hostnames:",
		"    - api.acme.omani.homes",
		"  parentRef:",
		"    name: cilium-gateway-console",
		"    namespace: kube-system",
	}, "\n")

	faults := newapiValuesFaults(preFix)
	if len(faults) != 4 {
		t.Fatalf("pre-#5987 values must trip all four chart gates (render fail, issuer, existingSecret, httpRoute); got %d:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}
	joined := strings.Join(faults, "\n")
	for _, want := range []string{"external-secret.yaml:42", "$kcConfigured", "httproute.yaml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fault report must name the chart gate %q; got:\n%s", want, joined)
		}
	}
}

// TestFunnelNewAPIHR_SatisfiesChartValuesContract_5987 is the regression
// proof. It runs the REAL generator across both boundary tiers and the
// shared-realm/per-Org-realm split, and asserts the rendered HR trips none
// of the chart gates.
func TestFunnelNewAPIHR_SatisfiesChartValuesContract_5987(t *testing.T) {
	cases := []struct {
		name string
		opt  helmReleaseAppOpts
	}{
		{"host-tier", helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes"}},
		{"vcluster-tier", helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes", kubeSecret: "tenant-acme-kubeconfig"}},
		{"shared-realm", helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes", sharedRealmIssuer: "https://auth.t99.omani.works/realms/sovereign"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := hrValuesBlock(t, generateNewAPIHR(tc.opt))
			if faults := newapiValuesFaults(values); len(faults) > 0 {
				t.Fatalf("funnel bp-newapi HR violates the bp-newapi chart values contract (#5987):\n  - %s\n\nrendered values:\n%s",
					strings.Join(faults, "\n  - "), values)
			}

			// The route must carry the host openclaw is pointed at, and it
			// must ride the console Gateway (a Sovereign runs no traefik for
			// per-Org apps — the same stance generateOpenClawHR takes).
			wantHost := fmt.Sprintf("api.%s.%s", tc.opt.slug, tc.opt.parentDomain)
			if got, _ := yamlScalar(values, "ingress.httpRoute.host"); got != wantHost {
				t.Errorf("ingress.httpRoute.host = %q, want %q", got, wantHost)
			}
			if got, _ := yamlScalar(values, "ingress.httpRoute.parentRef.name"); got != "cilium-gateway-console" {
				t.Errorf("ingress.httpRoute.parentRef.name = %q, want cilium-gateway-console", got)
			}
			if got, _ := yamlScalar(values, "ingress.enabled"); got != "false" {
				t.Errorf("ingress.enabled = %q, want false (the traefik Ingress is inert on a Cilium-Gateway Sovereign)", got)
			}
		})
	}
}

// TestFunnelNewAPIHR_NeverClobbersTheSovereignAdminToken_5987 pins the
// reason the funnel HR turns catalystIntegration OFF rather than copying the
// BSS door's block verbatim.
//
// bp-newapi's catalystIntegration ships a PushSecret
// (templates/admin-token-pushsecret.yaml, default enabled, updatePolicy
// Replace) that writes THIS release's own random token-signing-key
// ADMIN_SECRET to the OpenBao path in externalSecret.remoteRef.key. That path
// is cluster-shared and singular: catalyst-api's seedNewapiAdminToken seam
// writes `catalyst/newapi/admin-token`
// (products/catalyst/bootstrap/api/internal/handler/sovereign_newapi_admin_token_seed.go),
// the OpenBao `external-secrets-push` policy grants create/update on exactly
// that one key, and catalyst-api's unified-rbac consumes the resulting bearer
// against the SOVEREIGN's newapi (`newapi.newapi.svc`). A per-Org install
// pointed at that path would overwrite the Sovereign-wide bearer with an Org-
// local secret and 401 the whole Sovereign's per-user key issuance, and its
// target Secret carries reflector auto-mirror annotations into
// catalyst-system, where the Sovereign's own copy already lives.
//
// Nothing in the per-Org release consumes the Secret, so the correct funnel
// value is `enabled: false` — and this test fails if a future edit turns it
// back on without also proving a per-Org, per-Org-writable path exists.
func TestFunnelNewAPIHR_NeverClobbersTheSovereignAdminToken_5987(t *testing.T) {
	values := hrValuesBlock(t, generateNewAPIHR(helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes"}))

	got, ok := yamlScalar(values, "catalystIntegration.enabled")
	if !ok || got != "false" {
		t.Fatalf("catalystIntegration.enabled = %q (set=%v), want false — see this test's doc comment", got, ok)
	}
	if strings.Contains(values, "catalyst/newapi/admin-token") {
		t.Errorf("the funnel HR must NOT name the cluster-shared admin-token path; the chart's PushSecret would overwrite the Sovereign-wide bearer with this Org's own")
	}
}

// TestFunnelNewAPIHR_ServesOpenClawLLMBaseURL_5987 pins the coupling that
// motivated impliedHelmReleaseApps (openclaw ⇒ newapi, #5969): openclaw's
// llm.baseURL is generated, not configured, so if the two templates ever
// disagree on the host the implication delivers a gateway nobody can reach.
func TestFunnelNewAPIHR_ServesOpenClawLLMBaseURL_5987(t *testing.T) {
	opt := helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes"}

	newapiValues := hrValuesBlock(t, generateNewAPIHR(opt))
	routeHost, ok := yamlScalar(newapiValues, "ingress.httpRoute.host")
	if !ok {
		t.Fatalf("bp-newapi HR carries no ingress.httpRoute.host:\n%s", newapiValues)
	}

	openclawValues := hrValuesBlock(t, generateOpenClawHR(opt))
	llmBase, ok := yamlScalar(openclawValues, "llm.baseURL")
	if !ok {
		t.Fatalf("bp-openclaw HR carries no llm.baseURL:\n%s", openclawValues)
	}

	if want := "https://" + routeHost + "/v1"; llmBase != want {
		t.Errorf("openclaw llm.baseURL = %q but bp-newapi's HTTPRoute serves %q (want llm.baseURL == %q) — the implied-app closure would install a gateway openclaw cannot reach",
			llmBase, routeHost, want)
	}
}
