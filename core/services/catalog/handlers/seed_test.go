package handlers

import "testing"

// TestDeployableAppSlugs_Issue941 asserts that openclaw + stalwart-mail
// are present in the deployable map (issue #941). C5-final hit "27 apps
// COMING SOON" on otech113 because both were missing — gates 4 (LLM)
// and 5 (mail) blocked before alice could click Install.
func TestDeployableAppSlugs_Issue941(t *testing.T) {
	d := DeployableAppSlugs()
	wantTrue := []string{
		"openclaw",      // #941 — bp-openclaw via SME-tenant overlay
		"stalwart-mail", // #941 — bp-stalwart-tenant via SME-tenant overlay
		// Sanity — the canonical alice baseline apps.
		"wordpress",
		"ghost",
		"nextcloud",
	}
	for _, slug := range wantTrue {
		if !d[slug] {
			t.Errorf("expected %q to be deployable, got false (or missing)", slug)
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
		"openclaw", "stalwart-mail",
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
