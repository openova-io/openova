package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	envv1 "github.com/openova-io/openova/core/controllers/environment/api/v1"
	"github.com/openova-io/openova/core/controllers/internal/gitea"
)

// fakeGitea is a deterministic test double for the GiteaClient
// interface. It records every PutFile call so tests can assert
// idempotency, multi-region fan-out, and drift handling.
type fakeGitea struct {
	orgs            map[string]*gitea.Org
	orgErrors       map[string]error
	files           map[string][]byte // key = org|repo|branch|path
	upsertErrorPath string             // when set, return upsertError on this path
	upsertError     error
	upsertCalls     []upsertCall
}

type upsertCall struct {
	Org, Repo, Branch, Path string
	Content                 []byte
	Committed               bool
}

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		orgs:      make(map[string]*gitea.Org),
		orgErrors: make(map[string]error),
		files:     make(map[string][]byte),
	}
}

func (f *fakeGitea) GetOrg(_ context.Context, org string) (gitea.Org, error) {
	if err, ok := f.orgErrors[org]; ok {
		return gitea.Org{}, err
	}
	if o, ok := f.orgs[org]; ok {
		return *o, nil
	}
	return gitea.Org{}, gitea.ErrOrgNotFound
}

func (f *fakeGitea) PutFile(
	_ context.Context,
	org, repo, branch, path string,
	content []byte,
	_ string,
	_ ...gitea.PutFileOpts,
) (gitea.File, bool, error) {
	if f.upsertErrorPath != "" && path == f.upsertErrorPath {
		f.upsertCalls = append(f.upsertCalls, upsertCall{Org: org, Repo: repo, Branch: branch, Path: path, Content: content, Committed: false})
		return gitea.File{}, false, f.upsertError
	}
	key := org + "|" + repo + "|" + branch + "|" + path
	committed := true
	if existing, ok := f.files[key]; ok && string(existing) == string(content) {
		committed = false
	}
	f.files[key] = append([]byte(nil), content...)
	f.upsertCalls = append(f.upsertCalls, upsertCall{
		Org:       org,
		Repo:      repo,
		Branch:    branch,
		Path:      path,
		Content:   append([]byte(nil), content...),
		Committed: committed,
	})
	return gitea.File{Path: path}, committed, nil
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, envv1.AddToScheme(scheme))
	return scheme
}

func newReconciler(t *testing.T, fg *fakeGitea, objs ...client.Object) (*EnvironmentReconciler, client.Client) {
	t.Helper()
	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&envv1.Environment{}).
		WithObjects(objs...).
		Build()
	r := &EnvironmentReconciler{
		Client: c,
		Scheme: scheme,
		Gitea:  fg,
		Cfg: Config{
			FluxNamespace:       "flux-system",
			FluxIntervalSeconds: 60,
			GiteaPublicURL:      "https://gitea.hfmp.acme.openova.io",
			GiteaSecretRef:      "gitea-flux-token",
			CommitAuthorName:    "test-bot",
			CommitAuthorEmail:   "test-bot@openova.io",
			EnvRepoSuffix:       "-environment",
			RequeueAfter:        2 * time.Minute,
		},
	}
	return r, c
}

func mustGet(t *testing.T, c client.Client, name string) *envv1.Environment {
	t.Helper()
	var got envv1.Environment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name}, &got))
	return &got
}

// T1: happy-path single-region reconcile.
func TestReconcile_SingleRegionHappyPath(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["acme"] = &gitea.Org{Username: "acme"}

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "acme-prod",
			UID:        "abc-123",
			Generation: 5,
		},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "acme",
			EnvType:         "prod",
			Placement:       "single-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
			},
		},
	}
	r, c := newReconciler(t, fg, env)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-prod"}})
	require.NoError(t, err)
	assert.Equal(t, 2*time.Minute, res.RequeueAfter, "should requeue at the configured drift-detection interval")

	got := mustGet(t, c, "acme-prod")
	assert.Equal(t, envv1.PhaseReady, got.Status.Phase)
	assert.Equal(t, int32(1), got.Status.RegionCount)
	assert.Equal(t, "main", got.Status.GiteaRepoRef.Branch, "prod env_type maps to main branch")
	assert.Equal(t, "acme", got.Status.GiteaRepoRef.Org)
	assert.Equal(t, "ws.acme-prod.>", got.Status.JetstreamSubjectPrefix)
	assert.Equal(t, int64(5), got.Status.ObservedGeneration)
	require.Len(t, got.Status.VClusters, 1)
	assert.Equal(t, "hetzner-fsn-rtz-prod", got.Status.VClusters[0].Host)
	assert.Equal(t, "acme", got.Status.VClusters[0].Name)

	// Conditions
	requireCondition(t, got, envv1.ConditionGiteaOrgReady, envv1.ConditionTrue)
	requireCondition(t, got, envv1.ConditionGitRepositoryWritten, envv1.ConditionTrue)
	requireCondition(t, got, envv1.ConditionReady, envv1.ConditionTrue)

	// Gitea-side: exactly one commit at the expected path.
	require.Len(t, fg.upsertCalls, 1)
	call := fg.upsertCalls[0]
	assert.Equal(t, "acme", call.Org)
	assert.Equal(t, "acme-environment", call.Repo)
	assert.Equal(t, "main", call.Branch)
	assert.Equal(t, "clusters/hetzner-fsn-rtz-prod/environments/acme-prod/gitrepository.yaml", call.Path)
	assert.True(t, call.Committed, "first reconcile should commit")
	assert.Contains(t, string(call.Content), "kind: GitRepository")
	assert.Contains(t, string(call.Content), "branch: main")
	assert.Contains(t, string(call.Content), "https://gitea.hfmp.acme.openova.io/acme/acme-environment.git")
	assert.Contains(t, string(call.Content), "secretRef:")
	assert.Contains(t, string(call.Content), "name: gitea-flux-token")
}

// T2: idempotent re-reconcile produces zero new commits.
func TestReconcile_IdempotentReReconcile(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["acme"] = &gitea.Org{Username: "acme"}

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-stg", UID: "abc-stg", Generation: 1},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "acme",
			EnvType:         "stg",
			Placement:       "single-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
			},
		},
	}
	r, _ := newReconciler(t, fg, env)

	// First reconcile commits.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-stg"}})
	require.NoError(t, err)
	require.Len(t, fg.upsertCalls, 1)
	require.True(t, fg.upsertCalls[0].Committed)

	// Second reconcile with identical content produces no new commit (committed=false).
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-stg"}})
	require.NoError(t, err)
	require.Len(t, fg.upsertCalls, 2)
	assert.False(t, fg.upsertCalls[1].Committed, "re-reconcile with unchanged spec MUST short-circuit (zero new commit)")
}

// T3: parent Org missing → Pending phase + GiteaOrgReady=False, no panic.
func TestReconcile_OrgMissingSurfacesCondition(t *testing.T) {
	fg := newFakeGitea()
	// No orgs registered → GetOrg returns ErrOrgNotFound.

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "ghostly-prod", Generation: 1},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "ghostly",
			EnvType:         "prod",
			Placement:       "single-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
			},
		},
	}
	r, c := newReconciler(t, fg, env)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ghostly-prod"}})
	require.NoError(t, err, "missing-org must NOT return an error from Reconcile (would crashloop the controller)")
	assert.Equal(t, 2*time.Minute, res.RequeueAfter)

	got := mustGet(t, c, "ghostly-prod")
	assert.Equal(t, envv1.PhasePending, got.Status.Phase)
	requireCondition(t, got, envv1.ConditionGiteaOrgReady, envv1.ConditionFalse)
	requireCondition(t, got, envv1.ConditionReady, envv1.ConditionFalse)
	assert.Empty(t, fg.upsertCalls, "no Gitea writes when org is missing")
	// JetStream prefix and branch should still be derived (they're
	// pure functions of spec).
	assert.Equal(t, "ws.ghostly-prod.>", got.Status.JetstreamSubjectPrefix)
	assert.Equal(t, "main", got.Status.GiteaRepoRef.Branch)
}

// T4: multi-region reconcile fans out one gitrepository.yaml per region.
func TestReconcile_MultiRegionFanOut(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["bankdhofar"] = &gitea.Org{Username: "bankdhofar"}

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "bankdhofar-prod", UID: "bd-uid", Generation: 2},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "bankdhofar",
			EnvType:         "prod",
			Placement:       "multi-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
				{Provider: "hetzner", Region: "hel", BuildingBlock: "rtz"},
				{Provider: "huawei", Region: "muc", BuildingBlock: "rtz", HostCluster: "hw-muc-rtz-custom"},
			},
		},
	}
	r, c := newReconciler(t, fg, env)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bankdhofar-prod"}})
	require.NoError(t, err)

	got := mustGet(t, c, "bankdhofar-prod")
	assert.Equal(t, envv1.PhaseReady, got.Status.Phase)
	assert.Equal(t, int32(3), got.Status.RegionCount)
	require.Len(t, got.Status.VClusters, 3)

	// Three commits, one per region.
	require.Len(t, fg.upsertCalls, 3)
	paths := []string{}
	for _, c := range fg.upsertCalls {
		paths = append(paths, c.Path)
		assert.Equal(t, "main", c.Branch, "all regions share the same branch")
		assert.Equal(t, "bankdhofar", c.Org)
		assert.Equal(t, "bankdhofar-environment", c.Repo)
	}
	assert.Contains(t, paths, "clusters/hetzner-fsn-rtz-prod/environments/bankdhofar-prod/gitrepository.yaml")
	assert.Contains(t, paths, "clusters/hetzner-hel-rtz-prod/environments/bankdhofar-prod/gitrepository.yaml")
	assert.Contains(t, paths, "clusters/hw-muc-rtz-custom/environments/bankdhofar-prod/gitrepository.yaml",
		"explicit hostCluster override must win over the derived name")
}

// T5: drift detection — operator hand-edits the manifest in Gitea, the
// controller re-reconciles and overwrites with the canonical version.
func TestReconcile_DriftIsCorrected(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["acme"] = &gitea.Org{Username: "acme"}

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-dev", UID: "acme-dev-uid", Generation: 1},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "acme",
			EnvType:         "dev",
			Placement:       "single-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
			},
		},
	}
	r, _ := newReconciler(t, fg, env)

	// First reconcile commits the canonical version.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-dev"}})
	require.NoError(t, err)
	require.Len(t, fg.upsertCalls, 1)
	canonical := fg.upsertCalls[0].Content

	// Operator drifts the file in Gitea.
	for k := range fg.files {
		if strings.Contains(k, "acme-dev") {
			fg.files[k] = []byte("---\n# drifted by operator hand-edit\n")
		}
	}

	// Re-reconcile should detect drift and overwrite (committed=true).
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-dev"}})
	require.NoError(t, err)
	require.Len(t, fg.upsertCalls, 2)
	assert.True(t, fg.upsertCalls[1].Committed, "drifted manifest must be overwritten on next reconcile")
	assert.Equal(t, string(canonical), string(fg.upsertCalls[1].Content),
		"controller must restore the canonical bytes")
}

// T6: invalid placement-vs-regions cardinality → Failed phase.
func TestReconcile_PlacementCardinalityViolations(t *testing.T) {
	tests := []struct {
		name      string
		placement string
		nRegions  int
	}{
		{"single-region with 2 regions", "single-region", 2},
		{"multi-region with 1 region", "multi-region", 1},
		{"unknown placement", "global", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fg := newFakeGitea()
			fg.orgs["acme"] = &gitea.Org{Username: "acme"}

			regions := make([]envv1.EnvironmentRegion, tc.nRegions)
			for i := range regions {
				regions[i] = envv1.EnvironmentRegion{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"}
			}

			env := &envv1.Environment{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-prod", Generation: 1},
				Spec: envv1.EnvironmentSpec{
					OrganizationRef: "acme",
					EnvType:         "prod",
					Placement:       tc.placement,
					Regions:         regions,
				},
			}
			r, c := newReconciler(t, fg, env)

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "bad-prod"}})
			require.NoError(t, err)
			assert.Equal(t, time.Duration(0), res.RequeueAfter, "Failed spec should not requeue (spec edit re-triggers)")

			got := mustGet(t, c, "bad-prod")
			assert.Equal(t, envv1.PhaseFailed, got.Status.Phase)
			requireCondition(t, got, envv1.ConditionReady, envv1.ConditionFalse)
			assert.Empty(t, fg.upsertCalls, "invalid spec must not trigger Gitea writes")
		})
	}
}

// T7: env_type-to-branch mapping table.
func TestReconcile_BranchPerEnvTypeMapping(t *testing.T) {
	tests := []struct {
		envType, expected string
	}{
		{"dev", "develop"},
		{"stg", "staging"},
		{"prod", "main"},
		{"uat", "uat"},
		{"poc", "poc"},
	}
	for _, tc := range tests {
		t.Run(tc.envType, func(t *testing.T) {
			fg := newFakeGitea()
			fg.orgs["acme"] = &gitea.Org{Username: "acme"}

			env := &envv1.Environment{
				ObjectMeta: metav1.ObjectMeta{Name: "acme-" + tc.envType, Generation: 1},
				Spec: envv1.EnvironmentSpec{
					OrganizationRef: "acme",
					EnvType:         tc.envType,
					Placement:       "single-region",
					Regions: []envv1.EnvironmentRegion{
						{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
					},
				},
			}
			r, c := newReconciler(t, fg, env)
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-" + tc.envType}})
			require.NoError(t, err)

			got := mustGet(t, c, "acme-"+tc.envType)
			assert.Equal(t, tc.expected, got.Status.GiteaRepoRef.Branch)
			require.Len(t, fg.upsertCalls, 1)
			assert.Equal(t, tc.expected, fg.upsertCalls[0].Branch)
		})
	}
}

// T8: Gitea repo missing → Pending + GiteaRepoMissing reason.
func TestReconcile_RepoMissingSurfacesPending(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["acme"] = &gitea.Org{Username: "acme"}
	fg.upsertErrorPath = "clusters/hetzner-fsn-rtz-prod/environments/acme-prod/gitrepository.yaml"
	fg.upsertError = gitea.ErrRepoNotFound

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-prod", Generation: 1},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "acme",
			EnvType:         "prod",
			Placement:       "single-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
			},
		},
	}
	r, c := newReconciler(t, fg, env)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-prod"}})
	require.NoError(t, err, "missing repo is recoverable; controller must not crash")
	assert.Greater(t, res.RequeueAfter, time.Duration(0))

	got := mustGet(t, c, "acme-prod")
	assert.Equal(t, envv1.PhasePending, got.Status.Phase)
	requireCondition(t, got, envv1.ConditionGiteaOrgReady, envv1.ConditionFalse)
	// Reason should mention the repo
	cond := getCondition(got, envv1.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "GiteaRepoMissing", cond.Reason)
}

// T9: transient Gitea error on one region of a multi-region Env →
// Degraded with that region marked Failed but the others recorded.
func TestReconcile_PartialFailureOneRegionMarksDegraded(t *testing.T) {
	fg := newFakeGitea()
	fg.orgs["acme"] = &gitea.Org{Username: "acme"}
	// Force a non-404 error on the second region's path.
	fg.upsertErrorPath = "clusters/hetzner-hel-rtz-prod/environments/acme-prod/gitrepository.yaml"
	fg.upsertError = errors.New("gitea: 503 service unavailable")

	env := &envv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-prod", UID: "acme-prod-uid", Generation: 1},
		Spec: envv1.EnvironmentSpec{
			OrganizationRef: "acme",
			EnvType:         "prod",
			Placement:       "multi-region",
			Regions: []envv1.EnvironmentRegion{
				{Provider: "hetzner", Region: "fsn", BuildingBlock: "rtz"},
				{Provider: "hetzner", Region: "hel", BuildingBlock: "rtz"},
			},
		},
	}
	r, c := newReconciler(t, fg, env)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme-prod"}})
	require.NoError(t, err)

	got := mustGet(t, c, "acme-prod")
	assert.Equal(t, envv1.PhaseDegraded, got.Status.Phase)
	assert.Equal(t, int32(2), got.Status.RegionCount)
	require.Len(t, got.Status.VClusters, 2)

	// The first region committed; the second should be Failed.
	var failedFound bool
	for _, vc := range got.Status.VClusters {
		if vc.Host == "hetzner-hel-rtz-prod" {
			assert.Equal(t, "Failed", vc.Phase)
			failedFound = true
		}
	}
	assert.True(t, failedFound, "the failing region must be reflected in status")

	requireCondition(t, got, envv1.ConditionReady, envv1.ConditionFalse)
	requireCondition(t, got, envv1.ConditionGitRepositoryWritten, envv1.ConditionFalse)
}

// T10: Config.Defaults applies the documented defaults.
func TestConfig_Defaults(t *testing.T) {
	cfg := Config{}.Defaults()
	assert.Equal(t, "flux-system", cfg.FluxNamespace)
	assert.Equal(t, 60, cfg.FluxIntervalSeconds)
	assert.Equal(t, "environment-controller", cfg.CommitAuthorName)
	assert.Equal(t, "environment-controller@openova.io", cfg.CommitAuthorEmail)
	assert.Equal(t, "-environment", cfg.EnvRepoSuffix)
	assert.Equal(t, 5*time.Minute, cfg.RequeueAfter)
}

// T11: deletion between dequeue and Get is benign (controller-runtime
// pattern — informer can race the API server).
func TestReconcile_NotFoundIsBenign(t *testing.T) {
	fg := newFakeGitea()
	r, _ := newReconciler(t, fg) // no Environment in store

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ghost"}})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
	assert.Empty(t, fg.upsertCalls)
}

func requireCondition(t *testing.T, env *envv1.Environment, condType, status string) {
	t.Helper()
	c := getCondition(env, condType)
	require.NotNil(t, c, "expected condition %q on Environment %q", condType, env.Name)
	assert.Equal(t, status, c.Status, "condition %q has wrong status", condType)
}

func getCondition(env *envv1.Environment, condType string) *envv1.EnvironmentCondition {
	for i := range env.Status.Conditions {
		if env.Status.Conditions[i].Type == condType {
			return &env.Status.Conditions[i]
		}
	}
	return nil
}
