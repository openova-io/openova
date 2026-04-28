package gitops

import (
	"strings"
	"testing"
)

// TestListmonkManifestShape locks in the fix for issue #101: listmonk's
// generated deployment must emit LISTMONK_db__* envs (not DATABASE_URL,
// which listmonk ignores) and an initContainer that bootstraps the schema.
func TestListmonkManifestShape(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	files := g.GenerateAllWithPassword("e2etest", "flexi", []string{"listmonk"}, "deadbeef")

	var manifest string
	for path, content := range files {
		if strings.HasSuffix(path, "app-listmonk.yaml") {
			manifest = content
			break
		}
	}
	if manifest == "" {
		t.Fatal("app-listmonk.yaml not generated")
	}

	// Must use the LISTMONK_db__ env shape, not the generic DATABASE_URL one.
	mustContain := []string{
		"LISTMONK_db__host",
		"LISTMONK_db__port",
		"LISTMONK_db__user",
		"LISTMONK_db__password",
		"LISTMONK_db__database",
		"LISTMONK_db__ssl_mode",
		`value: "postgres"`,       // host
		`value: "db_listmonk"`,    // database
		`value: "deadbeef"`,       // password
		"initContainers",          // schema bootstrap
		"listmonk-init",           // init container name
		"--install --yes",         // listmonk install flag
	}
	for _, s := range mustContain {
		if !strings.Contains(manifest, s) {
			t.Errorf("listmonk manifest missing %q\n--- manifest ---\n%s", s, manifest)
		}
	}

	// Must NOT emit the generic DATABASE_URL that listmonk ignores.
	if strings.Contains(manifest, "DATABASE_URL") {
		t.Errorf("listmonk manifest still emits DATABASE_URL (listmonk ignores it) — should be LISTMONK_db__* only")
	}
}
