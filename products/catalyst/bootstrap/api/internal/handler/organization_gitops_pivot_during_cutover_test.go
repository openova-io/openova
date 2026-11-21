// organization_gitops_pivot_during_cutover_test.go — an Organization written
// while the cutover runs gets the Sovereign's own catalog, and what was
// written before the engine knew is rewritten before the egress hold.
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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

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
// Secret; trustAbsent of one where it has not.
func trustPresent(context.Context) (string, error) { return orgtenantrepos.TrustSecretName, nil }

func trustAbsent(context.Context) (string, error) { return "", nil }

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

// An Organization written after the registry pivot and before the engine's Pod
// is replaced gets the Sovereign's own catalog in all six blocks. On main the
// writer decided from step 07's issuer pair alone, which this Pod does not
// carry, and wrote oci://ghcr.io/openova-io six times. Each block names the
// trust Secret once step 06 has written it (21 Nov: the Organization came after
// step 06), as step 06 does for every source it pivots; without it the source
// fails x509 on the registry's LE-staging certificate while step 08 passes it.
// Before the Secret exists the blocks name none, as step 06 does then.
func TestOrganizationWrittenAfterPivotBeforePodReplacementGetsOwnCatalog(t *testing.T) {
	for name, tc := range map[string]struct {
		lookup orgtenantrepos.TrustFunc
		want   string
	}{
		"after step 06 wrote the trust Secret": {trustPresent, orgtenantrepos.TrustSecretName},
		"before it (steps 03 to 06)":           {trustAbsent, ""},
	} {
		t.Run(name, func(t *testing.T) {
			oldEnginePodEnv(t)
			bare := tenantsRemote(t, map[string]string{
				"clusters/" + pivotTestFQDN + "/org-tenants/kustomization.yaml": emptyTenantsIndex,
			})
			w := DefaultOrganizationGitOpsWriter{CutoverStatus: cutoverStatusFixed(map[string]string{
				"registriesYamlActive": "v2", "currentStep": "catalyst-api-env-patch",
			}), HarborTrust: tc.lookup}
			if _, err := w.WriteTenantOverlay(context.Background(), pivotTestRecord()); err != nil {
				t.Fatalf("WriteTenantOverlay: %v", err)
			}
			assertSixOnOwnCatalog(t, remoteFile(t, bare, "clusters/"+pivotTestFQDN+"/org-tenants/helmrepositories.yaml"), tc.want)
		})
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
	status := map[string]string{"registriesYamlActive": "v2"}

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
		// A step result alone is not the fact: the engine clears results it re-runs.
		"step 06 result only": {"registriesYamlActive": "v1", "step.helmrepository-patches.result": "success"},
	} {
		t.Run(name, func(t *testing.T) {
			oldEnginePodEnv(t)
			bare := tenantsRemote(t, map[string]string{
				dir + "kustomization.yaml":    emptyTenantsIndex,
				dir + "helmrepositories.yaml": orgtenantrepos.Template,
			})
			before := remoteCommits(t, bare)
			w := DefaultOrganizationGitOpsWriter{CutoverStatus: cutoverStatusFixed(status), HarborTrust: trustLookupMustNotRun(t)}
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

// A file written on the Sovereign's registry before step 06 wrote the trust
// Secret (steps 03 to 06) gets the reference from the re-render before the
// egress hold, and a second re-render pushes nothing.
func TestSharedHelmRepositoriesWrittenBeforeTheTrustSecretGetItBeforeTheHold(t *testing.T) {
	oldEnginePodEnv(t)
	path := "clusters/" + pivotTestFQDN + "/org-tenants/helmrepositories.yaml"
	bare := tenantsRemote(t, map[string]string{
		path: orgtenantrepos.Render("oci://registry."+pivotTestFQDN+"/openova-io", ""),
	})
	before := remoteCommits(t, bare)
	w := DefaultOrganizationGitOpsWriter{HarborTrust: trustPresent}
	status := map[string]string{"registriesYamlActive": "v2"}
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
	sha, err := w.RepointSharedHelmRepositories(context.Background(), map[string]string{"registriesYamlActive": "v2"})
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
