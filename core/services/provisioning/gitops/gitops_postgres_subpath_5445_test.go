package gitops

import (
	"regexp"
	"strings"
	"testing"
)

// #5445 — the per-Org postgres PVC must be mounted at a SUBDIRECTORY, never at
// the volume root.
//
// Every Huawei EVS volume is ext4, so its filesystem root always contains a
// `lost+found` directory. Mounting the PVC root directly at PGDATA hands initdb
// a non-empty directory and it refuses outright:
//
//	initdb: error: directory "/var/lib/postgresql/data" exists but is not empty
//	initdb: detail: It contains a lost+found directory, perhaps due to it being
//	                a mount point.
//
// Live on hw290 Org theta-corp this was postgres CrashLoopBackOff x50, with
// umami and uptime-kuma crashlooping downstream — on an Organization where the
// GitOps write succeeded, Flux applied cleanly, and all 20 inventory entries
// were present. Every upstream stage was green and not one purchased app ran.
// That is Pillar 1's terminal acceptance failing at the last step, and it is
// invisible to any check that stops at Flux inventory.
//
// Asserts on the RENDERED manifest rather than on a source constant, so a
// refactor that reintroduces a root mount by another route still trips it.
func renderPostgresForTest() string {
	return generatePostgres(
		"theta-corp",
		"pw-not-a-real-secret",
		[]string{"umami", "uptime-kuma"},
		map[string]any{},
		"",
	)
}

func TestPerOrgPostgres_MountsSubPathNotVolumeRoot_5445(t *testing.T) {
	rendered := renderPostgresForTest()

	const pgdataMount = "mountPath: /var/lib/postgresql/data"
	if !strings.Contains(rendered, pgdataMount) {
		t.Fatalf("postgres manifest no longer mounts %q — if PGDATA moved, move this guard with it "+
			"rather than deleting it; the lost+found hazard follows the mount, not the path", pgdataMount)
	}

	// mountPath and subPath must travel together: capture the pgdata block and
	// require subPath inside it, before the next list entry.
	block := regexp.MustCompile(`(?s)- name: pgdata\s*\n(.*?)(?:\n\s*- name: |\n\s*volumes:)`)
	m := block.FindStringSubmatch(rendered)
	if m == nil {
		t.Fatal("could not locate the pgdata volumeMount block in the rendered manifest")
	}
	mount := m[1]

	if !strings.Contains(mount, pgdataMount) {
		t.Fatalf("the pgdata block does not carry %q:\n%s", pgdataMount, mount)
	}
	if !strings.Contains(mount, "subPath:") {
		t.Errorf("pgdata is mounted at the PVC ROOT with no subPath — on ext4 EVS the root contains "+
			"lost+found, so initdb refuses and NO purchased app ever runs (#5445). Block was:\n%s", mount)
	}
}

// The failure this guards is silent at every layer above the container: the
// manifest is valid, Flux applies it, inventory is non-zero, the Deployment
// exists. Only the pod logs reveal it. Assert the pre-fix shape directly so a
// regression cannot hide behind a green Kustomization.
func TestPerOrgPostgres_RootMountIsNeverRendered_5445(t *testing.T) {
	rendered := renderPostgresForTest()

	bad := regexp.MustCompile(`- name: pgdata\s*\n\s*mountPath: /var/lib/postgresql/data\s*\n\s*- name:`)
	if bad.MatchString(rendered) {
		t.Error("rendered manifest mounts the pgdata PVC root straight at PGDATA with no subPath — " +
			"this is the #5445 defect verbatim: initdb sees lost+found and never initialises")
	}
}
