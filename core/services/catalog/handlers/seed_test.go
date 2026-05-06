package handlers

import "testing"

// TestDeployableAppSlugs_OpenclawStalwartDisabled asserts that openclaw and
// stalwart-mail are NOT in the deployable map. They were briefly enabled by
// #941 (catalog flag flipped) but the SME provisioning generator at
// core/services/provisioning/gitops/apps.go has no AppSpec for either, so
// the rendered Deployment manifests are invalid (missing image + ports).
// Live failure 2026-05-06 on tenant "test11":
//
//	tenant-test11-apps Kustomization rejected:
//	Deployment.apps "openclaw" is invalid: containers[0].image: Required value
//
// Re-enabling these requires per-app HelmRelease overlays beyond the single
// Deployment template the generator currently supports.
func TestDeployableAppSlugs_OpenclawStalwartDisabled(t *testing.T) {
	d := DeployableAppSlugs()
	for _, slug := range []string{"openclaw", "stalwart-mail"} {
		if d[slug] {
			t.Errorf("%q must NOT be deployable until per-app overlay exists in the SME provisioning generator", slug)
		}
	}
}

// TestDeployableAppSlugs_StableShape locks the map's exported keys so a
// rename (or accidental delete) of a deployable slug fails the test
// instead of silently flipping a marketplace card to COMING SOON.
func TestDeployableAppSlugs_StableShape(t *testing.T) {
	d := DeployableAppSlugs()
	expected := []string{
		"wordpress", "ghost", "nextcloud", "bookstack", "uptime-kuma",
		"gitea", "vaultwarden", "umami", "nocodb", "cal-com",
		"invoiceshelf", "formbricks", "listmonk",
		"postgres", "mysql", "redis",
	}
	if got, want := len(d), len(expected); got != want {
		t.Errorf("deployable map size = %d, want %d", got, want)
	}
	for _, slug := range expected {
		if _, ok := d[slug]; !ok {
			t.Errorf("expected %q in deployable map, missing", slug)
		}
	}
}
