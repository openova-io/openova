// organization_gitops_pivot_during_cutover_test.go — an Organization written
// while the cutover runs gets the Sovereign's own catalog once step 06 has
// written its credential and trust, and what was written before is rewritten
// before the egress hold, within a deadline.
//
// Measured 21 Nov 2026 on a two-region install: step 06 moved the existing
// HelmRepositories (04:03:25-04:06:10); an Organization written at 04:08:19, by
// the catalyst-api Pod that did not yet carry step 07's issuer pair (its
// successor was created 04:09:37), wrote clusters/<fqdn>/org-tenants/
// helmrepositories.yaml on oci://ghcr.io/openova-io; nothing rewrote it, and
// step 08 failed three Pods on OFFENDER bp-agenity, bp-openclaw,
// bp-stalwart-tenant, bp-wordpress-tenant=oci://ghcr.io/openova-io.
//
// The writer runs against a real Git remote: the tenants branch of a bare
// repository, reached through the same http URL and token shape the chart
// configures (git's url.<file>.insteadOf maps it onto the bare repository).
package handler

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/orgtenantrepos"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

const pivotTestFQDN = "hw311.omani.works"

// pivotTestLocalURL is the url line every block must carry after the pivot.
const pivotTestLocalURL = "url: oci://registry." + pivotTestFQDN + "/openova-io"

// sharedRepoNames is the six blocks of the shared file; the last four are the
// ones that stayed on the public catalog on 21 Nov.
var sharedRepoNames = []string{"bp-keycloak", "bp-newapi", "bp-agenity", "bp-openclaw", "bp-stalwart-tenant", "bp-wordpress-tenant"}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// tenantsRemote makes a bare repository whose org-tenants branch holds files
// (path -> content) and points the writer's gitops env at it. Returns the
// bare repository's directory.
func tenantsRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "seed")
	bare := filepath.Join(root, "openova.git")
	gitT(t, root, "init", "-q", work)
	for p, c := range files {
		full := filepath.Join(work, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, work, "add", "-A")
	gitT(t, work, "-c", "user.name=seed", "-c", "user.email=seed@example.invalid", "commit", "-q", "-m", "seed")
	gitT(t, root, "init", "-q", "--bare", bare)
	gitT(t, work, "push", "-q", bare, "HEAD:refs/heads/org-tenants")

	const remote = "http://gitea-http.gitea.svc.cluster.local:3000/openova/openova"
	t.Setenv("CATALYST_GITOPS_REPO_URL", remote)
	t.Setenv("CATALYST_GITOPS_BRANCH", "org-tenants")
	t.Setenv("CATALYST_GITOPS_USER", "gitea_admin")
	t.Setenv("CATALYST_GITOPS_TOKEN", "test-token")
	t.Setenv("CATALYST_GITOPS_TMPDIR", t.TempDir())
	t.Setenv("TENANT_GITOPS_PER_ORG", "")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+bare+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "http://gitea_admin:test-token@gitea-http.gitea.svc.cluster.local:3000/openova/openova")
	return bare
}

func remoteFile(t *testing.T, bare, path string) string {
	t.Helper()
	return gitT(t, bare, "show", "org-tenants:"+path) + "\n"
}

func remoteCommits(t *testing.T, bare string) int {
	t.Helper()
	n, err := strconv.Atoi(gitT(t, bare, "rev-list", "--count", "org-tenants"))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// blockURL returns the url line of the HelmRepository named name in doc.
func blockURL(doc, name string) string {
	i := strings.Index(doc, "  name: "+name+"\n")
	if i < 0 {
		return ""
	}
	rest := doc[i:]
	if end := strings.Index(rest, "\n---"); end >= 0 {
		rest = rest[:end]
	}
	for _, l := range strings.Split(rest, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "url:") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// assertSixOnOwnCatalog checks every one of the six blocks by name, so a block
// that is missing cannot pass as "no public url left", and checks that each
// names the trust Secret (orgtenantrepos.TrustSecretName) as source-controller
// reads it, or none when trust is "" (the Secret is absent).
func assertSixOnOwnCatalog(t *testing.T, doc, trust string) {
	t.Helper()
	if n := hrBlockCount(doc); n != len(sharedRepoNames) {
		t.Fatalf("want %d HelmRepository blocks, got %d:\n%s", len(sharedRepoNames), n, doc)
	}
	for _, name := range sharedRepoNames {
		if got := blockURL(doc, name); got != pivotTestLocalURL {
			t.Errorf("%s: %q, want %q", name, got, pivotTestLocalURL)
		}
	}
	if strings.Contains(doc, "url: "+orgtenantrepos.GHCRBase) {
		t.Errorf("a block still names the public catalog:\n%s", doc)
	}
	blocks := sharedBlocks(t, doc)
	for _, name := range sharedRepoNames {
		b := blocks[name]
		got := ""
		if b.Spec.CertSecretRef != nil {
			got = b.Spec.CertSecretRef.Name
		}
		if got != trust {
			t.Errorf("%s: spec.certSecretRef.name %q, want %q", name, got, trust)
		}
		if b.Spec.SecretRef == nil || b.Spec.SecretRef.Name != "ghcr-pull" {
			t.Errorf("%s: spec.secretRef %+v, want ghcr-pull", name, b.Spec.SecretRef)
		}
	}
}

// sharedBlock is what source-controller reads of one shared HelmRepository.
type sharedBlock struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		URL       string `json:"url"`
		SecretRef *struct {
			Name string `json:"name"`
		} `json:"secretRef"`
		CertSecretRef *struct {
			Name string `json:"name"`
		} `json:"certSecretRef"`
	} `json:"spec"`
}

// sharedBlocks parses doc into its HelmRepositories by name.
func sharedBlocks(t *testing.T, doc string) map[string]sharedBlock {
	t.Helper()
	out := map[string]sharedBlock{}
	for _, part := range strings.Split(doc, "\n---\n") {
		var b sharedBlock
		if err := yaml.Unmarshal([]byte(part), &b); err != nil {
			t.Fatalf("parse the shared file: %v\n%s", err, part)
		}
		if b.Kind == "HelmRepository" {
			out[b.Metadata.Name] = b
		}
	}
	return out
}

// assertTrustOnEveryBlock checks that each of the blocks HelmRepositories of
// doc is off the public catalog and names the trust Secret.
func assertTrustOnEveryBlock(t *testing.T, doc string, blocks int) {
	t.Helper()
	n := 0
	for name, b := range sharedBlocks(t, doc) {
		if b.Spec.URL == orgtenantrepos.GHCRBase || b.Spec.CertSecretRef == nil || b.Spec.CertSecretRef.Name != orgtenantrepos.TrustSecretName {
			t.Errorf("%s: url %q certSecretRef %+v, want the Sovereign's registry and %s", name, b.Spec.URL, b.Spec.CertSecretRef, orgtenantrepos.TrustSecretName)
			continue
		}
		n++
	}
	if n != blocks {
		t.Fatalf("want %d blocks with the trust reference, got %d:\n%s", blocks, n, doc)
	}
}

// trustPresent is the lookup of a cluster where step 06 has written the trust
// Secret.
func trustPresent(context.Context) (string, error) { return orgtenantrepos.TrustSecretName, nil }

// trustLookupMustNotRun fails the test when the public catalog looks up trust.
func trustLookupMustNotRun(t *testing.T) orgtenantrepos.TrustFunc {
	return func(context.Context) (string, error) {
		t.Error("the trust Secret was looked up for the public catalog")
		return "", nil
	}
}

func pivotTestRecord() store.OrganizationProvisionRecord {
	return store.OrganizationProvisionRecord{
		OrganizationID:  "040755fec9",
		Subdomain:       "iso",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@iso.test",
		OTECHFQDN:       pivotTestFQDN,
		ParentDomain:    "omani.homes",
		TenantNamespace: "org-040755fec9",
	}
}

func cutoverStatusFixed(kv map[string]string) func(context.Context) map[string]string {
	return func(context.Context) map[string]string { return kv }
}

// The old engine Pod: SOVEREIGN_FQDN set, step 07's issuer pair not yet.
func oldEnginePodEnv(t *testing.T) {
	t.Helper()
	clearCutoverEnv(t)
	t.Setenv("SOVEREIGN_FQDN", pivotTestFQDN)
}

const emptyTenantsIndex = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"

// afterStep06 is the cutover status once step 06 has written the registry's
// credential (Phase 0) and trust (Phase 0.5) and succeeded.
var afterStep06 = map[string]string{"registriesYamlActive": "v2", "step.helmrepository-patches.result": "success", "currentStep": "catalyst-api-env-patch"}

// publicAuthHeld is the lookup of a cluster where step 06 has not stripped the
// public catalog's auth from ghcr-pull; publicAuthStripped of one where it has.
func publicAuthHeld(context.Context) (bool, error)     { return true, nil }
func publicAuthStripped(context.Context) (bool, error) { return false, nil }

// The source an Organization written at each moment of the cutover gets, and
// that it can use then: the public catalog until step 06 has written the
// credential and the trust of the Sovereign's registry (egress stays open
// until step 08), the Sovereign's own with certSecretRef from then on. On main
// the two middle moments got the Sovereign's registry, which source-controller
// can neither authenticate to nor trust before step 06, and a cutover that
// stopped there left every later Organization on a source that cannot work.
// Once step 06 has stripped the public catalog's auth (Phase 3b) the own source
// is the only one that works, also when the step then failed (a secondary not
// reached) or a re-run blanked its row: the public catalog there would fail
// "no auth config for 'ghcr.io'" on all six shared blocks.
func TestOrganizationGetsTheSourceThatCanBeUsedAtEachMoment(t *testing.T) {
	for name, tc := range map[string]struct {
		stamp    bool
		status   map[string]string
		stripped bool
		own      bool
	}{
		"before step 03 ends":                        {status: map[string]string{"registriesYamlActive": "v1", "currentStep": "harbor-prewarm"}},
		"between step 03's end and step 06's phases": {status: map[string]string{"registriesYamlActive": "v2", "currentStep": "helmrepository-patches", "step.helmrepository-patches.result": "running"}},
		"a cutover stopped after step 03":            {status: map[string]string{"registriesYamlActive": "v2", "failedStep": "registry-pivot", "step.registry-pivot.result": "failed"}},
		"a cutover stopped in step 06 after its strip": {
			status:   map[string]string{"registriesYamlActive": "v2", "failedStep": "helmrepository-patches", "step.helmrepository-patches.result": "failed"},
			stripped: true, own: true,
		},
		"a re-run blanked step 06's row after an earlier strip": {
			status:   map[string]string{"registriesYamlActive": "v2", "currentStep": "harbor-prewarm", "step.helmrepository-patches.result": ""},
			stripped: true, own: true,
		},
		"after step 06":               {status: afterStep06, own: true},
		"after step 07 (the new Pod)": {stamp: true, own: true},
	} {
		t.Run(name, func(t *testing.T) {
			oldEnginePodEnv(t)
			if tc.stamp {
				t.Setenv("CATALYST_PIN_ISSUER", "https://console."+pivotTestFQDN)
			}
			bare := tenantsRemote(t, map[string]string{
				"clusters/" + pivotTestFQDN + "/org-tenants/kustomization.yaml": emptyTenantsIndex,
			})
			// publicAuthHeld would turn the last two moments to the public
			// catalog: they must answer without looking the strip up.
			w := DefaultOrganizationGitOpsWriter{CutoverStatus: cutoverStatusFixed(tc.status), PublicAuth: publicAuthHeld, HarborTrust: trustLookupMustNotRun(t)}
			if tc.stripped {
				w.PublicAuth = publicAuthStripped
			}
			if tc.own {
				w.HarborTrust = trustPresent
			}
			if _, err := w.WriteTenantOverlay(context.Background(), pivotTestRecord()); err != nil {
				t.Fatalf("WriteTenantOverlay: %v", err)
			}
			got := remoteFile(t, bare, "clusters/"+pivotTestFQDN+"/org-tenants/helmrepositories.yaml")
			if !tc.own {
				if got != orgtenantrepos.Template {
					t.Fatalf("want the public catalog, byte for byte the template:\n%s", got)
				}
				return
			}
			assertSixOnOwnCatalog(t, got, orgtenantrepos.TrustSecretName)
		})
	}
}

// An Organization written between step 03 and step 06 gets the public catalog,
// and the re-render before step 08 moves it onto the Sovereign's own with the
// trust reference once step 06 has succeeded.
func TestOrganizationWrittenBeforeStep06IsMovedByTheReRenderBeforeStep08(t *testing.T) {
	oldEnginePodEnv(t)
	path := "clusters/" + pivotTestFQDN + "/org-tenants/helmrepositories.yaml"
	bare := tenantsRemote(t, map[string]string{
		"clusters/" + pivotTestFQDN + "/org-tenants/kustomization.yaml": emptyTenantsIndex,
	})
	between := DefaultOrganizationGitOpsWriter{CutoverStatus: cutoverStatusFixed(map[string]string{"registriesYamlActive": "v2"}), PublicAuth: publicAuthHeld, HarborTrust: trustLookupMustNotRun(t)}
	if _, err := between.WriteTenantOverlay(context.Background(), pivotTestRecord()); err != nil {
		t.Fatalf("WriteTenantOverlay: %v", err)
	}
	if got := remoteFile(t, bare, path); got != orgtenantrepos.Template {
		t.Fatalf("written between step 03 and step 06: want the public catalog:\n%s", got)
	}
	before := remoteCommits(t, bare)
	sha, err := DefaultOrganizationGitOpsWriter{HarborTrust: trustPresent}.RepointSharedHelmRepositories(context.Background(), afterStep06)
	if err != nil || sha == "" || remoteCommits(t, bare) != before+1 {
		t.Fatalf("re-render before step 08: sha=%q err=%v commits=%d (was %d), want one commit", sha, err, remoteCommits(t, bare), before)
	}
	assertSixOnOwnCatalog(t, remoteFile(t, bare, path), orgtenantrepos.TrustSecretName)
}

// silentRemote is a Git remote that accepts the connection and never answers.
func silentRemote(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return "http://" + ln.Addr().String() + "/openova/openova"
}

// The re-render before step 08 against a Gitea that never answers returns
// within its deadline and records the named warning as a cutover event; on
// main it ran with no deadline and held the cutover there with no event.
func TestReRenderBeforeStep08AgainstASilentRemoteReturnsWithinItsDeadline(t *testing.T) {
	oldEnginePodEnv(t)
	t.Setenv("CATALYST_GITOPS_REPO_URL", silentRemote(t))
	t.Setenv("CATALYST_GITOPS_BRANCH", "org-tenants")
	t.Setenv("CATALYST_GITOPS_USER", "gitea_admin")
	t.Setenv("CATALYST_GITOPS_TOKEN", "test-token")
	t.Setenv("CATALYST_GITOPS_TMPDIR", t.TempDir())
	t.Setenv("TENANT_GITOPS_PER_ORG", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	saved := orgTenantRepointDeadline
	orgTenantRepointDeadline = time.Second
	t.Cleanup(func() { orgTenantRepointDeadline = saved })
	_, client := fakeHandlerWithCutover(t, statusCMWith(afterStep06))

	var logged bytes.Buffer
	var events []cutoverEvent
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		repointOrgTenantSources(context.Background(), slog.New(slog.NewTextHandler(&logged, nil)),
			DefaultOrganizationGitOpsWriter{HarborTrust: trustPresent}, &cutoverDeps{core: client, ns: cutoverTestNS},
			func(ev cutoverEvent) { events = append(events, ev) })
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the re-render against a remote that never answers did not return within 30 s")
	}
	if took := time.Since(start); took > orgTenantRepointDeadline+10*time.Second {
		t.Errorf("returned after %s, want at the deadline (%s)", took, orgTenantRepointDeadline)
	}
	if len(events) != 1 || events[0].Level != "warn" || events[0].Step != cutoverStepEgressBlockTest || !strings.Contains(events[0].Message, orgtenantrepos.RepointTimedOut) {
		t.Fatalf("events %+v, want one warn event of step %s naming %s", events, cutoverStepEgressBlockTest, orgtenantrepos.RepointTimedOut)
	}
	if !strings.Contains(logged.String(), orgtenantrepos.RepointTimedOut) {
		t.Errorf("the warning was not logged: %s", logged.String())
	}
}

// An Organization written before the pivot is rewritten after it: the engine's
// re-render (run before the egress hold) moves every block, the four that
// stayed on 21 Nov included, and touches nothing else of the tree.
func TestSharedHelmRepositoriesWrittenBeforePivotAreRewrittenAfterIt(t *testing.T) {
	oldEnginePodEnv(t)
	dir := "clusters/" + pivotTestFQDN + "/org-tenants/"
	index := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - helmrepositories.yaml\n  - 040755fec9\n"
	overlay := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
	bare := tenantsRemote(t, map[string]string{
		dir + "kustomization.yaml":            index,
		dir + "helmrepositories.yaml":         orgtenantrepos.Template,
		dir + "040755fec9/kustomization.yaml": overlay,
	})
	before := remoteCommits(t, bare)
	w := DefaultOrganizationGitOpsWriter{HarborTrust: trustPresent}
	status := afterStep06

	sha, err := w.RepointSharedHelmRepositories(context.Background(), status)
	if err != nil {
		t.Fatalf("RepointSharedHelmRepositories: %v", err)
	}
	if sha == "" || remoteCommits(t, bare) != before+1 {
		t.Fatalf("want one pushed commit, got sha=%q and %d commits (was %d)", sha, remoteCommits(t, bare), before)
	}
	assertSixOnOwnCatalog(t, remoteFile(t, bare, dir+"helmrepositories.yaml"), orgtenantrepos.TrustSecretName)
	if got := remoteFile(t, bare, dir+"kustomization.yaml"); got != index {
		t.Errorf("the parent index changed:\n%s", got)
	}
	if got := remoteFile(t, bare, dir+"040755fec9/kustomization.yaml"); got != overlay {
		t.Errorf("the Organization's overlay changed:\n%s", got)
	}

	// A second re-render finds nothing on the public catalog and pushes nothing.
	again, err := w.RepointSharedHelmRepositories(context.Background(), status)
	if err != nil || again != "" || remoteCommits(t, bare) != before+1 {
		t.Fatalf("second re-render: sha=%q err=%v commits=%d, want no commit", again, err, remoteCommits(t, bare))
	}
}

// Nothing is rewritten on a Sovereign that has not pivoted, and an Organization
// written there still gets the public catalog, byte for byte the template.
func TestNothingIsRewrittenOnASovereignThatHasNotPivoted(t *testing.T) {
	dir := "clusters/" + pivotTestFQDN + "/org-tenants/"
	for name, status := range map[string]map[string]string{
		"no status":             nil,
		"before harbor-prewarm": {"registriesYamlActive": "v1", "currentStep": "harbor-prewarm"},
		"rolled back":           {"registriesYamlActive": "rollback"},
		// The registry pivot without step 06 (no credential, no trust yet).
		"after step 03, before step 06": {"registriesYamlActive": "v2"},
		// Step 06's result without the registry pivot is not enough either.
		"step 06 result only": {"registriesYamlActive": "v1", "step.helmrepository-patches.result": "success"},
	} {
		t.Run(name, func(t *testing.T) {
			oldEnginePodEnv(t)
			bare := tenantsRemote(t, map[string]string{
				dir + "kustomization.yaml":    emptyTenantsIndex,
				dir + "helmrepositories.yaml": orgtenantrepos.Template,
			})
			before := remoteCommits(t, bare)
			w := DefaultOrganizationGitOpsWriter{CutoverStatus: cutoverStatusFixed(status), PublicAuth: publicAuthHeld, HarborTrust: trustLookupMustNotRun(t)}
			sha, err := w.RepointSharedHelmRepositories(context.Background(), status)
			if err != nil || sha != "" || remoteCommits(t, bare) != before {
				t.Fatalf("re-render before the pivot: sha=%q err=%v commits=%d (was %d), want nothing", sha, err, remoteCommits(t, bare), before)
			}
			if _, err := w.WriteTenantOverlay(context.Background(), pivotTestRecord()); err != nil {
				t.Fatalf("WriteTenantOverlay: %v", err)
			}
			if got := remoteFile(t, bare, dir+"helmrepositories.yaml"); got != orgtenantrepos.Template {
				t.Errorf("an Organization written before the pivot must get the template unchanged:\n%s", got)
			}
		})
	}
}

// A file on the Sovereign's registry without the trust reference (written by
// an engine that switched at step 03) gets the reference from the re-render
// before the egress hold, and a second re-render pushes nothing.
func TestSharedHelmRepositoriesWrittenBeforeTheTrustSecretGetItBeforeTheHold(t *testing.T) {
	oldEnginePodEnv(t)
	path := "clusters/" + pivotTestFQDN + "/org-tenants/helmrepositories.yaml"
	bare := tenantsRemote(t, map[string]string{
		path: orgtenantrepos.Render("oci://registry."+pivotTestFQDN+"/openova-io", ""),
	})
	before := remoteCommits(t, bare)
	w := DefaultOrganizationGitOpsWriter{HarborTrust: trustPresent}
	status := afterStep06
	sha, err := w.RepointSharedHelmRepositories(context.Background(), status)
	if err != nil || sha == "" || remoteCommits(t, bare) != before+1 {
		t.Fatalf("re-render: sha=%q err=%v commits=%d (was %d), want one commit", sha, err, remoteCommits(t, bare), before)
	}
	assertSixOnOwnCatalog(t, remoteFile(t, bare, path), orgtenantrepos.TrustSecretName)
	if again, err := w.RepointSharedHelmRepositories(context.Background(), status); err != nil || again != "" || remoteCommits(t, bare) != before+1 {
		t.Fatalf("second re-render: sha=%q err=%v, want no commit", again, err)
	}
}

// Pivoted without an fqdn the file stays the public catalog, so the re-render
// has nothing to write and pushes no commit.
func TestRepointWithoutAnFQDNPushesNothing(t *testing.T) {
	clearCutoverEnv(t)
	path := "clusters/" + pivotTestFQDN + "/org-tenants/helmrepositories.yaml"
	bare := tenantsRemote(t, map[string]string{path: orgtenantrepos.Template})
	before := remoteCommits(t, bare)
	w := DefaultOrganizationGitOpsWriter{HarborTrust: trustLookupMustNotRun(t)}
	sha, err := w.RepointSharedHelmRepositories(context.Background(), afterStep06)
	if err != nil || sha != "" || remoteCommits(t, bare) != before {
		t.Fatalf("re-render without an fqdn: sha=%q err=%v commits=%d (was %d), want nothing", sha, err, remoteCommits(t, bare), before)
	}
}

// repointRecorder is a gitops writer that records the engine's re-render call.
type repointRecorder struct {
	mu          sync.Mutex
	calls       int
	status      map[string]string
	egressJobs  int
	egressCount func(string) int
}

func (r *repointRecorder) WriteTenantOverlay(context.Context, store.OrganizationProvisionRecord) (string, error) {
	return "", nil
}

func (r *repointRecorder) DeleteTenantOverlay(context.Context, store.OrganizationProvisionRecord) (string, error) {
	return "", nil
}

func (r *repointRecorder) RepointSharedHelmRepositories(_ context.Context, status map[string]string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.status = status
	r.egressJobs = r.egressCount(cutoverStepEgressBlockTest)
	return "0123abcd", nil
}

// The engine asks the writer to re-render before it starts the egress-block-test
// step, with the live cutover status, once per run.
func TestCutoverRepointsOrgTenantSourcesBeforeTheEgressHold(t *testing.T) {
	installEgressReapCounter(t)
	h, client := fakeHandlerWithCutover(t, statusCMWith(map[string]string{}))
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)
	rec := &repointRecorder{egressCount: created}
	h.SetOrganizationDeps(OrganizationDeps{GitOps: rec})
	chain := []cutoverStep{
		{stepName: cutoverStepHarborPrewarm, order: 3, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: cutoverStepEgressBlockTest, order: 8, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}

	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, chain, cutoverRunOpts{operatorRetry: true})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.calls != 1 {
		t.Fatalf("re-render calls = %d, want 1", rec.calls)
	}
	if rec.status["registriesYamlActive"] != "v2" {
		t.Errorf("re-render got status %v, want the live one (registriesYamlActive=v2)", rec.status)
	}
	if rec.egressJobs != 0 {
		t.Errorf("egress-block-test Jobs at the re-render = %d, want 0 (before the step)", rec.egressJobs)
	}
	if got := created(cutoverStepEgressBlockTest); got != 1 {
		t.Errorf("egress-block-test Jobs after the run = %d, want 1", got)
	}
}
