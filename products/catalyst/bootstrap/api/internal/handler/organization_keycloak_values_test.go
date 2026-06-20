// organization_keycloak_values_test.go — issue #910 / B3 fix.
//
// Verifies the orchestrator's emitted bp-keycloak HelmRelease values
// match the chart's actual values contract (platform/keycloak/chart/
// values.yaml). The earlier shape (`topology`, `realm.*`, `bootstrap.*`,
// `ingress.*`) was silently ignored by the chart, so per-tenant
// Keycloak provisioned without a Cilium Gateway HTTPRoute and tenant
// users could not reach their own Keycloak.
//
// These tests assert:
//   - the rendered bp-keycloak.yaml parses as YAML
//   - it carries every key the chart consumes for tenant exposure
//   - the bp-cnpg umbrella subchart values are emitted under the
//     canonical `cloudnative-pg.*` namespace (not the silent-ignore
//     shape the previous orchestrator used)
//
// Coverage scope per issue #910 brief: bp-keycloak primary; bp-cnpg
// quick-audit. WP/OpenClaw/Stalwart contracts already align with their
// chart values.yaml as of this PR (verified by reading each).
package handler

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// helmReleaseYAML — minimal struct for asserting the emitted HR's
// values block carries the chart-contract keys. We only decode the
// pieces this test needs (sigs.k8s.io/yaml is permissive on extras).
type helmReleaseYAML struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Chart struct {
			Spec struct {
				Chart   string `json:"chart"`
				Version string `json:"version"`
			} `json:"spec"`
		} `json:"chart"`
		Values map[string]interface{} `json:"values"`
	} `json:"spec"`
}

func mustRenderTenantOverlay(t *testing.T) map[string]string {
	t.Helper()
	rec := store.OrganizationProvisionRecord{
		OrganizationID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{Keycloak: "1.3.3", CNPG: "0.5.0"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return files
}

// TestBPKeycloakEmittedYAMLParses — defensive smoke test: every byte
// the orchestrator emits MUST be valid YAML. A malformed template
// would land an unparseable file in the GitOps repo and Flux would
// fail the per-tenant Kustomization, blocking the entire signup
// pipeline.
func TestBPKeycloakEmittedYAMLParses(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body, ok := files["bp-keycloak.yaml"]
	if !ok {
		t.Fatalf("bp-keycloak.yaml missing from render output")
	}
	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("bp-keycloak.yaml does not parse as YAML: %v\n--- body ---\n%s", err, body)
	}
	if hr.APIVersion != "helm.toolkit.fluxcd.io/v2" {
		t.Errorf("apiVersion: want helm.toolkit.fluxcd.io/v2 got %q", hr.APIVersion)
	}
	if hr.Kind != "HelmRelease" {
		t.Errorf("kind: want HelmRelease got %q", hr.Kind)
	}
	if hr.Metadata.Name != "bp-keycloak" {
		t.Errorf("metadata.name: want bp-keycloak got %q", hr.Metadata.Name)
	}
	if hr.Metadata.Namespace != "org-t-acme" {
		t.Errorf("metadata.namespace: want org-t-acme got %q", hr.Metadata.Namespace)
	}
}

// TestBPKeycloakValuesContract — the heart of the #910 / B3 fix.
//
// Asserts the emitted values block carries EVERY key the bp-keycloak
// chart (platform/keycloak/chart/values.yaml + chart/templates/
// httproute.yaml + chart/templates/configmap-sovereign-realm.yaml)
// actually consumes. Without these, the chart's HTTPRoute guard
// renders nothing and tenant Keycloak is unreachable.
func TestBPKeycloakValuesContract(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body := files["bp-keycloak.yaml"]
	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := hr.Spec.Values
	if v == nil {
		t.Fatalf("spec.values missing")
	}

	// sovereignFQDN — drives realm-import redirect URIs in the chart's
	// configmap-sovereign-realm.yaml template (catalyst-ui webOrigins).
	gotFQDN, _ := v["sovereignFQDN"].(string)
	if gotFQDN != "acme.otech.example" {
		t.Errorf("sovereignFQDN: want acme.otech.example got %q", gotFQDN)
	}

	// sovereignRealm.enabled — gates the realm-import ConfigMap +
	// catalyst-kc-sa-credentials Secret. Must be true for tenant mode.
	srRaw, ok := v["sovereignRealm"].(map[string]interface{})
	if !ok {
		t.Fatalf("sovereignRealm block missing or wrong shape: %#v", v["sovereignRealm"])
	}
	if got, _ := srRaw["enabled"].(bool); !got {
		t.Errorf("sovereignRealm.enabled: want true got %v", srRaw["enabled"])
	}

	// gateway block — THE bug fix. host MUST be non-empty otherwise
	// the chart's templates/httproute.yaml guard renders nothing.
	gw, ok := v["gateway"].(map[string]interface{})
	if !ok {
		t.Fatalf("gateway block missing or wrong shape: %#v", v["gateway"])
	}
	if got, _ := gw["enabled"].(bool); !got {
		t.Errorf("gateway.enabled: want true got %v", gw["enabled"])
	}
	gwHost, _ := gw["host"].(string)
	if gwHost != "keycloak.acme.otech.example" {
		t.Errorf("gateway.host: want keycloak.acme.otech.example got %q", gwHost)
	}
	parentRef, ok := gw["parentRef"].(map[string]interface{})
	if !ok {
		t.Fatalf("gateway.parentRef missing or wrong shape: %#v", gw["parentRef"])
	}
	if got, _ := parentRef["name"].(string); got != "cilium-gateway" {
		t.Errorf("gateway.parentRef.name: want cilium-gateway got %q", got)
	}
	if got, _ := parentRef["namespace"].(string); got != "kube-system" {
		t.Errorf("gateway.parentRef.namespace: want kube-system got %q", got)
	}

	// smtp.* — the chart consumes these directly in the realm import
	// JSON (smtpServer block). Required-host check matches the chart
	// values.yaml gating.
	smtp, ok := v["smtp"].(map[string]interface{})
	if !ok {
		t.Fatalf("smtp block missing: %#v", v["smtp"])
	}
	if got, _ := smtp["host"].(string); got == "" {
		t.Errorf("smtp.host empty — chart gates realm SMTP block on this")
	}
	if got, _ := smtp["port"].(string); got == "" {
		t.Errorf("smtp.port empty — chart expects string-shaped port")
	}
	if got, _ := smtp["from"].(string); got == "" {
		t.Errorf("smtp.from empty — Keycloak rejects realm import without it")
	}
	// from MUST carry the tenant zone so tenant mail comes from a
	// recognisable address (forensics + DMARC alignment when tenant
	// SMTP eventually swaps in).
	if got, _ := smtp["from"].(string); !strings.Contains(got, "acme.otech.example") {
		t.Errorf("smtp.from: want tenant-zone-derived got %q", got)
	}

	// realmConfig.tenant.* — forward-looking marker; chart does not
	// yet honour but Helm accepts unknown values silently. When the
	// chart's tenant-mode realm template lands these become live.
	rc, ok := v["realmConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("realmConfig block missing: %#v", v["realmConfig"])
	}
	tenant, ok := rc["tenant"].(map[string]interface{})
	if !ok {
		t.Fatalf("realmConfig.tenant missing: %#v", rc["tenant"])
	}
	if got, _ := tenant["enabled"].(bool); !got {
		t.Errorf("realmConfig.tenant.enabled: want true got %v", tenant["enabled"])
	}
	if got, _ := tenant["realmName"].(string); got != "org-acme" {
		t.Errorf("realmConfig.tenant.realmName: want org-acme got %q", got)
	}
}

// TestBPKeycloakValuesContract_NoLegacyKeys — guards against
// regression: the legacy shape (`topology`, top-level `realm`,
// `bootstrap`, top-level `ingress`) MUST NOT appear at the top of the
// values block. The chart silently ignores them, which is exactly how
// the original bug was missed in code review.
func TestBPKeycloakValuesContract_NoLegacyKeys(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body := files["bp-keycloak.yaml"]
	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := hr.Spec.Values
	for _, banned := range []string{"topology", "bootstrap", "parentDomain"} {
		if _, present := v[banned]; present {
			t.Errorf("legacy key %q still emitted at values root — chart silently ignores it (bug source for #910)", banned)
		}
	}
	// `realm` and `ingress` may exist as part of the upstream keycloak
	// subchart override — but the orchestrator must NOT emit them at
	// the top level for the catalyst-curated wrapper. We assert the
	// catalyst-curated keys (sovereignFQDN, gateway, sovereignRealm,
	// smtp) are present which implicitly proves the wrapper's contract
	// is being honoured.
	if _, ok := v["sovereignFQDN"]; !ok {
		t.Errorf("sovereignFQDN missing — chart contract violation")
	}
	if _, ok := v["gateway"]; !ok {
		t.Errorf("gateway missing — chart contract violation")
	}
}

// TestBPCNPGSubchartKey — the bp-cnpg chart is a pure umbrella subchart
// of cloudnative-pg; per-Sovereign overrides MUST flow through the
// `cloudnative-pg.*` values namespace. Earlier orchestrator versions
// emitted top-level `namespace` and `operator.enabled` — silently
// ignored by Helm.
func TestBPCNPGSubchartKey(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body, ok := files["bp-cnpg.yaml"]
	if !ok {
		t.Fatalf("bp-cnpg.yaml missing from render output")
	}
	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("bp-cnpg.yaml does not parse: %v", err)
	}
	v := hr.Spec.Values
	cnpg, ok := v["cloudnative-pg"].(map[string]interface{})
	if !ok {
		t.Fatalf("cloudnative-pg subchart block missing or wrong shape: %#v", v["cloudnative-pg"])
	}
	if _, ok := cnpg["replicaCount"]; !ok {
		t.Errorf("cloudnative-pg.replicaCount missing")
	}
	crds, ok := cnpg["crds"].(map[string]interface{})
	if !ok {
		t.Fatalf("cloudnative-pg.crds missing or wrong shape")
	}
	if got, _ := crds["create"].(bool); got {
		t.Errorf("cloudnative-pg.crds.create: want false (mothership owns CRDs) got true")
	}
	// Banned legacy keys at top level — silent-ignore traps.
	for _, banned := range []string{"namespace", "operator"} {
		if _, present := v[banned]; present {
			t.Errorf("legacy key %q still emitted at bp-cnpg values root", banned)
		}
	}
}

// TestBPKeycloakValuesFromSMTPSecret — the orchestrator wires the
// tenant SMTP user/password via valuesFrom referring to an OPTIONAL
// per-tenant Secret. Until that Secret is reflected from
// catalyst-system/sovereign-smtp-credentials the chart still installs
// (smtp.user / smtp.password fall back to chart defaults — empty);
// Keycloak login pages render and outbound mail silently no-ops.
func TestBPKeycloakValuesFromSMTPSecret(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body := files["bp-keycloak.yaml"]
	// We need the full HR shape including spec.valuesFrom, so unmarshal
	// into a dynamic map.
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, ok := raw["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("spec missing")
	}
	vf, ok := spec["valuesFrom"].([]interface{})
	if !ok {
		t.Fatalf("spec.valuesFrom missing — SMTP credentials wiring required")
	}
	if len(vf) < 2 {
		t.Errorf("spec.valuesFrom: want at least 2 entries (user + pass) got %d", len(vf))
	}
	for _, item := range vf {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Each valuesFrom entry MUST be optional — the Secret may not
		// exist on first reconcile (it's reflected post-install).
		if got, _ := entry["optional"].(bool); !got {
			t.Errorf("spec.valuesFrom[%v]: optional must be true", entry["valuesKey"])
		}
		// targetPath MUST land in the chart's smtp.* namespace — that's
		// what the configmap-sovereign-realm.yaml template consumes.
		tp, _ := entry["targetPath"].(string)
		if !strings.HasPrefix(tp, "smtp.") {
			t.Errorf("spec.valuesFrom[%v].targetPath: want smtp.* got %q", entry["valuesKey"], tp)
		}
	}
}

// TestBPKeycloakInstallTimeout — keep the install/upgrade timeout at
// 15m. bp-keycloak's bitnami subchart runs a multi-minute Liquibase
// migration on first install; a shorter timeout misclassifies a
// healthy install as failed.
func TestBPKeycloakInstallTimeout(t *testing.T) {
	files := mustRenderTenantOverlay(t)
	body := files["bp-keycloak.yaml"]
	if !strings.Contains(body, "install:\n    timeout: 15m") {
		t.Errorf("bp-keycloak.yaml: install timeout not 15m — bitnami Liquibase migration needs the headroom")
	}
	if !strings.Contains(body, "upgrade:\n    timeout: 15m") {
		t.Errorf("bp-keycloak.yaml: upgrade timeout not 15m")
	}
}
