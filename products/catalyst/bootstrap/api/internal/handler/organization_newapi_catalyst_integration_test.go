// Guard for UAT row G2 (#5987) — a per-Organization bp-newapi release
// must NOT switch on the Sovereign-scoped catalystIntegration seam.
//
// WHAT ROW G2 SAW, AND WHY THE OBVIOUS READING WAS WRONG
// ──────────────────────────────────────────────────────
// G2 was recorded as "bp-newapi installs at chart 1.4.153 yet
// `kubectl get externalsecrets` returned ZERO in the same namespace,
// so the delivery half works and the ExternalSecret half does not."
// Two separate things are wrong with that.
//
// First, the measurement: run without -n, `kubectl get externalsecrets`
// reports on the `default` namespace, which legitimately holds none.
// Live on hw293 (dep a0077ba47e3720e5) the cluster carried 22
// ExternalSecrets, four of them in the tenant Org namespace g7doora.
// The producer was never missing.
//
// Second, the direction: the per-Org ExternalSecret that DID exist was
// failing, and the fix is to delete it rather than to repair it. The
// admin-token seam is SOVEREIGN-scoped. Its consumer is a single
// catalyst-api Pod in catalyst-system reading ADMIN_API_TOKEN via
// secretKeyRef on ONE reflector-mirrored Secret (see
// platform/newapi/chart/templates/external-secret.yaml:55-70). A per-Org
// release that enables the seam renders a second ExternalSecret
// contending for that one mirror, and its paired PushSecret — driven off
// the SAME remoteRef.key — would overwrite the shared OpenBao path with
// that Org's own random ADMIN_SECRET, 401-ing per-user key issuance for
// the whole Sovereign.
//
// On hw293 it failed earlier than that, on a 403, because the per-Org
// key used the `sovereign/` prefix while ESO's external-secrets-push
// policy grants writes only on the newapi admin-token path:
//
//	g7doora/catalyst-newapi-admin-token-push   Errored  (403 permission denied)
//	newapi/catalyst-newapi-admin-token-push    Synced   (Sovereign-level)
//
// so the ExternalSecret sat SecretSyncedError while the HelmRelease
// still reported Ready. Merely re-pointing the path would have made the
// write succeed and converted a visible error into a silent cross-Org
// clobber — which is why this guard asserts the seam is OFF, not that
// the path is prettier.
//
// core/services/provisioning/gitops/helmrelease_apps.go:512-527 already
// reached this conclusion for the funnel path; this guard keeps the
// per-Org GitOps writer from drifting back out of line with it.
package handler

import (
	"strings"
	"testing"
)

// TestPerOrgNewAPIDoesNotEnableCatalystIntegration is the row-G2 guard.
func TestPerOrgNewAPIDoesNotEnableCatalystIntegration(t *testing.T) {
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	newapi, ok := files["bp-newapi.yaml"]
	if !ok {
		t.Fatalf("bp-newapi.yaml missing from overlay; got %v", fileNames(files))
	}

	// Assert on the VALUE of the rendered toggle, not on the presence of
	// the key: the key is present either way, and "present" was never
	// the question.
	got := catalystIntegrationEnabled(newapi)
	if got != "false" {
		t.Errorf("per-Org bp-newapi renders catalystIntegration.enabled=%q, want \"false\".\n"+
			"The admin-token seam is Sovereign-scoped: one catalyst-api consumer, one\n"+
			"reflector mirror in catalyst-system, one shared OpenBao path. A per-Org\n"+
			"copy either 403s on write (what hw293 measured) or clobbers the\n"+
			"Sovereign's token for every Org. See helmrelease_apps.go:512-527.", got)
	}

	// The seam being off must take its OpenBao path with it — a stray
	// remoteRef would keep rendering the failing ExternalSecret.
	//
	// These checks run against COMMENT-STRIPPED yaml on purpose. The
	// template documents the defect it fixes, so it legitimately mentions
	// `sovereign/...` and ADMIN_API_TOKEN in prose; a raw substring match
	// would flag the explanation rather than a live field. Assert on what
	// renders into the object, not on what the file says.
	live := stripYAMLComments(newapi)
	if strings.Contains(live, "sovereign/") && strings.Contains(live, "admin-token") {
		t.Errorf("per-Org bp-newapi still carries a sovereign/-prefixed newapi admin-token "+
			"OpenBao path; ESO's external-secrets-push policy does not grant writes there "+
			"(403 permission denied, measured on hw293 g7doora):\n%s", live)
	}
	if strings.Contains(live, "ADMIN_API_TOKEN") {
		t.Errorf("per-Org bp-newapi still references ADMIN_API_TOKEN — the Sovereign-scoped "+
			"admin-token seam should be fully absent from a per-Org release:\n%s", live)
	}

	// CONTROL — this change must not switch OFF the per-Org seams that
	// legitimately work. The agenity mcp-bearer ExternalSecret reads
	// catalyst/agenity/<subdomain>/mcp-bearer and is SecretSynced=True on
	// hw293; ssoBridgeSync/oidc wiring likewise stays untouched. If this
	// control goes red the change has over-reached from "disable one
	// Sovereign-scoped seam" into "disable per-Org secret wiring".
	all := strings.Join(overlayManifests(files), "\n")
	if !strings.Contains(all, "catalyst/agenity/") {
		t.Errorf("CONTROL FAILED: the per-Org agenity mcp-bearer OpenBao path " +
			"(catalyst/agenity/<subdomain>/mcp-bearer, SecretSynced=True on hw293) " +
			"disappeared — this fix must not touch working per-Org secret seams")
	}
	// CONTROL — the Sovereign-level install is a DIFFERENT code path
	// (clusters/_template/bootstrap-kit/80-newapi.yaml) and must keep the
	// seam ON. Its absence from this overlay is the expected asymmetry,
	// so assert the overlay is genuinely the per-Org one.
	if !strings.Contains(newapi, "bp-newapi") {
		t.Fatalf("fixture drift: bp-newapi.yaml does not look like a bp-newapi release")
	}
}

// catalystIntegrationEnabled returns the rendered value of
// catalystIntegration.enabled, or "" when the block is absent.
func catalystIntegrationEnabled(manifest string) string {
	i := strings.Index(manifest, "catalystIntegration:")
	if i < 0 {
		return ""
	}
	for _, ln := range strings.Split(manifest[i:], "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "enabled:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "enabled:"))
		}
	}
	return ""
}

func overlayManifests(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// stripYAMLComments removes whole-line and trailing `#` comments so an
// assertion sees only what actually renders into the object.
func stripYAMLComments(manifest string) string {
	var b strings.Builder
	for _, ln := range strings.Split(manifest, "\n") {
		if i := strings.Index(ln, "#"); i >= 0 {
			ln = ln[:i]
		}
		if strings.TrimSpace(ln) == "" {
			continue
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}
