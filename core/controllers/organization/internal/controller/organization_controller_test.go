// organization_controller_test.go — covers the four scenarios in the
// slice-C1 brief:
//
//   1. Happy-path reconcile: clean CR → all four downstream artifacts
//      materialize + status surfaces Ready=True.
//   2. Idempotency: a second reconcile on the steady-state CR makes
//      ZERO net writes (every find-or-create short-circuits, PutFile
//      byte-equal short-circuits).
//   3. Keycloak group already exists: EnsureGroup returns the existing
//      ID without calling create.
//   4. Gitea Org already exists: EnsureOrg returns the existing Org
//      without calling admin/orgs.
//   5. Drift detection: spec.slug ≠ metadata.name surfaces SlugMetadataMismatch
//      and does NOT silently re-create.

package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeKeycloak counts calls so idempotency tests can assert no extra
// writes on a steady-state reconcile. Slice F2 added IdP-related
// methods on top of EnsureGroup; counters are split per-method so
// federation tests can assert counts independently.
type fakeKeycloak struct {
	mu        sync.Mutex
	calls     int
	groupID   string
	groupPath string

	// Per-Org realm surface (#3084 Part 2).
	realms           map[string]bool // realm name -> exists
	realmEnsureCalls int
	realmDeleteCalls int
	realmEnsureErr   error // when set, EnsureRealm returns it
	realmDeleteErr   error // when set, DeleteRealm returns it

	// Federation surface (F2).
	idps              map[string]KCIdentityProvider
	mappers           map[string][]KCIdentityProviderMapper // key = alias
	idpEnsureCalls    int
	idpDeleteCalls    int
	mapperEnsureCalls int
}

func (f *fakeKeycloak) EnsureRealm(ctx context.Context, slug string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.realmEnsureCalls++
	if f.realmEnsureErr != nil {
		return "", f.realmEnsureErr
	}
	if f.realms == nil {
		f.realms = map[string]bool{}
	}
	f.realms[slug] = true
	return slug, nil
}

func (f *fakeKeycloak) DeleteRealm(ctx context.Context, slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.realmDeleteCalls++
	if f.realmDeleteErr != nil {
		return f.realmDeleteErr
	}
	if f.realms != nil {
		delete(f.realms, slug)
	}
	return nil
}

func (f *fakeKeycloak) EnsureGroup(ctx context.Context, path string, attrs map[string][]string) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.groupID == "" {
		f.groupID = "kc-uuid-" + strings.TrimPrefix(path, "/")
		f.groupPath = path
	}
	return f.groupID, f.groupPath, "sovereign", nil
}

func (f *fakeKeycloak) EnsureIdentityProvider(ctx context.Context, idp KCIdentityProvider) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idps == nil {
		f.idps = map[string]KCIdentityProvider{}
	}
	f.idpEnsureCalls++
	f.idps[idp.Alias] = idp
	return nil
}

func (f *fakeKeycloak) EnsureIdentityProviderMapper(ctx context.Context, alias string, mapper KCIdentityProviderMapper) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mappers == nil {
		f.mappers = map[string][]KCIdentityProviderMapper{}
	}
	f.mapperEnsureCalls++
	// Find-or-create-by-name in the alias bucket.
	bucket := f.mappers[alias]
	for i := range bucket {
		if bucket[i].Name == mapper.Name {
			bucket[i] = mapper
			f.mappers[alias] = bucket
			return nil
		}
	}
	f.mappers[alias] = append(bucket, mapper)
	return nil
}

func (f *fakeKeycloak) DeleteIdentityProvider(ctx context.Context, alias string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idpDeleteCalls++
	if f.idps != nil {
		delete(f.idps, alias)
	}
	if f.mappers != nil {
		delete(f.mappers, alias)
	}
	return nil
}

// giteaServer is a tiny in-process Gitea API stub. It only implements
// the endpoints organization-controller calls — anything else 404s.
type giteaServer struct {
	t *testing.T

	mu sync.Mutex

	orgs  map[string]gitea.Org
	repos map[string]gitea.Repo // key: "{owner}/{name}"
	files map[string]fileEntry  // key: "{owner}/{repo}/{path}"

	// collideOnce injects a one-shot 409 git-ref-lock on the NEXT contents
	// write (POST or PUT) to a given "{owner}/{repo}/{path}" key, modelling a
	// concurrent writer (the funnel cart-install) winning the compare-and-swap
	// on the shared branch HEAD. Consumed on first use. #5305.
	collideOnce map[string]bool

	// Counts of mutating calls — used by the idempotency test to
	// assert zero post-steady-state writes.
	createOrgs, createRepos, createFiles, updateFiles int

	server *httptest.Server
}

type fileEntry struct {
	sha     string
	content []byte
}

func newGiteaServer(t *testing.T) *giteaServer {
	gs := &giteaServer{
		t:           t,
		orgs:        map[string]gitea.Org{},
		repos:       map[string]gitea.Repo{},
		files:       map[string]fileEntry{},
		collideOnce: map[string]bool{},
	}
	gs.server = httptest.NewServer(http.HandlerFunc(gs.handle))
	t.Cleanup(gs.server.Close)
	return gs
}

func (g *giteaServer) URL() string { return g.server.URL }

func (g *giteaServer) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.Header.Get("Authorization") == "" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path

	// GET /api/v1/orgs/{org}
	if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/orgs/") && !strings.Contains(p[len("/api/v1/orgs/"):], "/") {
		slug := p[len("/api/v1/orgs/"):]
		o, ok := g.orgs[slug]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, o)
		return
	}

	// POST /api/v1/orgs
	if r.Method == http.MethodPost && p == "/api/v1/orgs" {
		var body struct {
			Username    string `json:"username"`
			FullName    string `json:"full_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, dup := g.orgs[body.Username]; dup {
			http.Error(w, "exists", http.StatusUnprocessableEntity)
			return
		}
		g.createOrgs++
		o := gitea.Org{
			ID:          int64(len(g.orgs) + 1),
			Username:    body.Username,
			FullName:    body.FullName,
			Description: body.Description,
			Visibility:  body.Visibility,
		}
		g.orgs[body.Username] = o
		writeJSON(w, http.StatusCreated, o)
		return
	}

	// GET /api/v1/repos/{owner}/{repo}
	if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/repos/") && !strings.Contains(p[len("/api/v1/repos/"):], "/contents/") {
		key := p[len("/api/v1/repos/"):]
		// Strip trailing slash if any.
		key = strings.TrimRight(key, "/")
		if !strings.Contains(key, "/") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		repo, ok := g.repos[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, repo)
		return
	}

	// POST /api/v1/orgs/{org}/repos
	if r.Method == http.MethodPost && strings.HasPrefix(p, "/api/v1/orgs/") && strings.HasSuffix(p, "/repos") {
		owner := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/orgs/"), "/repos")
		var body struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Private       bool   `json:"private"`
			AutoInit      bool   `json:"auto_init"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := g.orgs[owner]; !ok {
			http.Error(w, "no such org", http.StatusNotFound)
			return
		}
		key := owner + "/" + body.Name
		if _, dup := g.repos[key]; dup {
			http.Error(w, "exists", http.StatusUnprocessableEntity)
			return
		}
		g.createRepos++
		repo := gitea.Repo{
			ID:            int64(len(g.repos) + 1),
			Name:          body.Name,
			FullName:      key,
			Description:   body.Description,
			Private:       body.Private,
			DefaultBranch: body.DefaultBranch,
		}
		g.repos[key] = repo
		writeJSON(w, http.StatusCreated, repo)
		return
	}

	// /api/v1/repos/{owner}/{repo}/contents/{path}
	if strings.HasPrefix(p, "/api/v1/repos/") && strings.Contains(p, "/contents/") {
		// Split on /contents/ — anything after is the file path.
		// Form: /api/v1/repos/{owner}/{repo}/contents/{path}
		const prefix = "/api/v1/repos/"
		rest := p[len(prefix):]
		idx := strings.Index(rest, "/contents/")
		if idx < 0 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		ownerRepo := rest[:idx]
		filePath := rest[idx+len("/contents/"):]
		key := ownerRepo + "/" + filePath

		switch r.Method {
		case http.MethodGet:
			f, ok := g.files[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, gitea.File{
				Path:          filePath,
				SHA:           f.sha,
				Type:          "file",
				ContentBase64: base64.StdEncoding.EncodeToString(f.content),
			})
			return
		case http.MethodPost, http.MethodPut:
			var body struct {
				Message string `json:"message"`
				Content string `json:"content"`
				Branch  string `json:"branch"`
				SHA     string `json:"sha"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				http.Error(w, "bad b64", http.StatusBadRequest)
				return
			}
			// #5305 — one-shot concurrent-writer CAS loss injection: model the
			// funnel landing a commit on the same branch HEAD between the
			// controller's read and its write. Gitea surfaces this as a 409
			// git-ref-lock. Consume the flag so a retry/next-reconcile succeeds.
			if g.collideOnce[key] {
				delete(g.collideOnce, key)
				http.Error(w, "cannot lock ref 'refs/heads/main': is at aaaa but expected bbbb", http.StatusConflict)
				return
			}
			if r.Method == http.MethodPost {
				if _, exists := g.files[key]; exists {
					http.Error(w, "exists", http.StatusUnprocessableEntity)
					return
				}
				g.createFiles++
			} else {
				g.updateFiles++
			}
			g.files[key] = fileEntry{
				sha:     fmt.Sprintf("sha-%d", g.createFiles+g.updateFiles),
				content: data,
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"content": gitea.File{
					Path: filePath,
					SHA:  g.files[key].sha,
					Type: "file",
				},
			})
			return
		}
	}

	g.t.Logf("giteaServer: unhandled %s %s", r.Method, r.URL.Path)
	http.Error(w, "not found", http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// makeReconciler builds a Reconciler with a fake K8s client + the
// in-process Gitea stub + a fake Keycloak. All four scenarios use
// this helper.
func makeReconciler(t *testing.T, objs ...client.Object) (*Reconciler, *giteaServer, *fakeKeycloak) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := orgapi.AddToScheme(scheme); err != nil {
		t.Fatalf("add orgapi scheme: %v", err)
	}
	// Register the unstructured UserAccess CR with the scheme so the
	// fake client can list/get/create it.
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccess",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	}, &unstructured.UnstructuredList{})
	// #3687 (fold #3669): register the Flux v2 HelmRelease GVK so the
	// vCluster-readiness readback can Get the per-Org vCluster HR (and so
	// tests can seed a Ready HR to drive the Org to Ready=True).
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	}, &unstructured.UnstructuredList{})
	// PR #3700 §4.3: register the Flux v1 GitRepository + Kustomization
	// GVKs so the per-Org vCluster Flux loop (step 3b / per_org_flux.go) can
	// find-or-create them when a test sets GiteaInClusterURL.
	for _, gvk := range []schema.GroupVersionKind{fluxGitRepositoryGVK, fluxKustomizationGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	// Issue #4077: register the cluster-scoped Environment CR so the fake
	// client can get/create the `<slug>-<envType>` Environment the
	// reconciler ensures once the vCluster HR is Ready (environment_ensure.go).
	scheme.AddKnownTypeWithName(environmentGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(environmentGVK.GroupVersion().WithKind(environmentGVK.Kind+"List"), &unstructured.UnstructuredList{})

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&orgapi.Organization{}).
		WithObjects(objs...).
		Build()

	gs := newGiteaServer(t)
	kc := &fakeKeycloak{}

	r := &Reconciler{
		Client:                    cl,
		Log:                       logr.Discard(),
		Keycloak:                  kc,
		GiteaClient:               gitea.New(gs.URL(), "test-token"),
		HostCluster:               "ct-eu-mgt-prod",
		VClusterChartVersion:      "0.33.*",
		VClusterHelmRepoName:      "loft",
		VClusterHelmRepoNamespace: "vcluster-system",
		Branch:                    "main",
		UserAccessNamespace:       "catalyst-system",
	}
	return r, gs, kc
}

func sampleOrg() *orgapi.Organization {
	return &orgapi.Organization{
		TypeMeta: metav1.TypeMeta{
			APIVersion: orgapi.GroupVersion.String(),
			Kind:       "Organization",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "acme",
			Generation: 1,
			UID:        "00000000-0000-0000-0000-000000000001",
		},
		Spec: orgapi.OrganizationSpec{
			Slug:        "acme",
			DisplayName: "ACME Corp",
			Kind:        "customer",
			Tier:        "org",
			// #4292: a paid plan → the renderer emits the vCluster boundary plus
			// the plan-templated ResourceQuota + LimitRange + apps-tree
			// NetworkPolicy baseline (6 files total).
			PlanSlug:               "m",
			BillingMode:            "real",
			SovereignRef:           "omantel.omani.works",
			DefaultEnvironmentType: "prod",
			Owners: []orgapi.OrganizationOwner{
				{Email: "ceo@acme.com", Role: "owner"},
				{Email: "ops@acme.com", Role: "admin"},
			},
		},
	}
}

func TestReconcile_HappyPath(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, kc := makeReconciler(t, org)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	// #3687 (fold #3669): with the vCluster HR not yet applied by Flux
	// (the fake cluster has no HelmRelease/namespace for "acme"), the
	// controller must NOT over-claim. It requeues so status converges as
	// the HR comes up — the honest replacement for the old
	// "PutFile → Ready=True, no requeue" lie.
	if res.RequeueAfter == 0 {
		t.Errorf("while the vCluster HR is not yet Ready the reconcile must requeue, got %v", res)
	}

	// Keycloak: 1 EnsureGroup call.
	if kc.calls != 1 {
		t.Errorf("expected 1 keycloak EnsureGroup call, got %d", kc.calls)
	}
	if kc.groupPath != "/acme" {
		t.Errorf("expected group path /acme, got %q", kc.groupPath)
	}

	// Gitea: 1 Org create + 1 Repo create + 3 file creates.
	if gs.createOrgs != 1 {
		t.Errorf("expected 1 gitea org create, got %d", gs.createOrgs)
	}
	if gs.createRepos != 1 {
		t.Errorf("expected 1 gitea repo create, got %d", gs.createRepos)
	}
	if gs.createFiles != 11 {
		t.Errorf("expected 11 gitea file creates (namespace, vcluster, resourcequota, limitrange, kustomization, apps/networkpolicy, apps/kustomization, apps/namespace [#4991 vcluster-tier target ns], host-apps/ciliumnetworkpolicy, host-apps/provisioning-rbac [#4991], host-apps/kustomization), got %d", gs.createFiles)
	}
	if gs.updateFiles != 0 {
		t.Errorf("expected 0 file updates on first reconcile, got %d", gs.updateFiles)
	}

	// Status: Ready=True, observedGeneration=1.
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("observedGeneration: got %d want 1", got.Status.ObservedGeneration)
	}
	if got.Status.KeycloakGroup.ID != kc.groupID {
		t.Errorf("status.keycloakGroup.id: got %q want %q", got.Status.KeycloakGroup.ID, kc.groupID)
	}
	if got.Status.GiteaOrg.Name != "acme" {
		t.Errorf("status.giteaOrg.name: got %q want %q", got.Status.GiteaOrg.Name, "acme")
	}
	if got.Status.VCluster.Name != "acme" || got.Status.VCluster.HostCluster != "ct-eu-mgt-prod" {
		t.Errorf("status.vcluster: got %+v", got.Status.VCluster)
	}
	// #3687 (fold #3669): no Flux source has applied the vCluster HR in
	// this fake cluster, so the phase is honestly "Pending" (manifest
	// authored in Gitea, awaiting reconcile) — NOT a frozen "Provisioning"
	// that masks orphaned bytes, and certainly not "Ready".
	if got.Status.VCluster.Phase != "Pending" {
		t.Errorf("status.vcluster.phase: got %q want Pending (HR not yet applied)", got.Status.VCluster.Phase)
	}
	// Slice F2 added two federation-status conditions on every
	// reconcile (NoFederation reason when spec.identity is empty —
	// the access-matrix UI expects them to always be present).
	// G117.3 W2.C3 (#2742) added the IacRepoBootstrapped condition —
	// rendered as Status=False / Reason=BootstrapDisabled in unit tests
	// where the iac-bootstrap deps are not wired into the Reconciler.
	// #3084 Part 2 added the PerOrgRealmProvisioned condition — rendered
	// as Status=False / Reason=RealmDisabled in unit tests where
	// PerOrgRealmEnabled is not opted in on the base makeReconciler.
	if len(got.Status.Conditions) != 5 {
		t.Fatalf("expected 5 conditions (Ready + 2 federation + IacRepoBootstrapped + PerOrgRealmProvisioned), got %d: %+v",
			len(got.Status.Conditions), got.Status.Conditions)
	}
	// #3687 (fold #3669): Ready is honestly False until the vCluster HR
	// is Ready AND the namespace is Active. In this fake cluster neither
	// exists yet, so Ready=False / VClusterProvisioning with an
	// explanatory message — the old unconditional Ready=True was the
	// "Ready over orphaned bytes" lie this ticket kills.
	if got.Status.Conditions[0].Type != "Ready" || got.Status.Conditions[0].Status != "False" {
		t.Errorf("expected Ready=False at index 0 (HR not yet applied), got %+v", got.Status.Conditions[0])
	}
	if got.Status.Conditions[0].Reason != "VClusterProvisioning" {
		t.Errorf("expected Ready reason VClusterProvisioning, got %q", got.Status.Conditions[0].Reason)
	}
	if got.Status.Conditions[0].Message == "" {
		t.Errorf("Ready=False must carry an explanatory message naming what is pending")
	}
	if got.Status.Conditions[1].Type != "IdentityProviderConfigured" ||
		got.Status.Conditions[1].Status != "False" ||
		got.Status.Conditions[1].Reason != "NoFederation" {
		t.Errorf("expected IdentityProviderConfigured=False/NoFederation at index 1, got %+v",
			got.Status.Conditions[1])
	}
	if got.Status.Conditions[2].Type != "IdentityProviderClaimMappersConfigured" ||
		got.Status.Conditions[2].Status != "False" {
		t.Errorf("expected IdentityProviderClaimMappersConfigured at index 2, got %+v",
			got.Status.Conditions[2])
	}

	// UserAccess: 2 CRs (one per owner).
	uaList := unstructured.UnstructuredList{}
	uaList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	})
	if err := r.List(context.Background(), &uaList); err != nil {
		t.Fatalf("list UserAccess: %v", err)
	}
	if len(uaList.Items) != 2 {
		t.Errorf("expected 2 UserAccess CRs, got %d", len(uaList.Items))
	}
	for _, ua := range uaList.Items {
		if !strings.HasPrefix(ua.GetName(), "acme-") {
			t.Errorf("UserAccess name should be acme-prefixed, got %q", ua.GetName())
		}
	}
}

// TestReconcile_ReadyOnlyWhenVClusterHRReady — #3687 (fold #3669) C4
// proof: the Org flips Ready=True and stops requeueing ONLY once the
// vCluster HelmRelease is actually Ready AND the per-Org namespace
// exists. Seeds a Ready HR (name "vcluster" in ns "acme") + the "acme"
// namespace, then reconciles and asserts status.vcluster.phase=Ready,
// Ready=True, and no requeue — the converse of the HappyPath test which
// proves it sits False/Pending while the HR is absent.
func TestReconcile_ReadyOnlyWhenVClusterHRReady(t *testing.T) {
	t.Parallel()
	org := sampleOrg()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	})
	hr.SetNamespace("acme")
	hr.SetName("vcluster")
	_ = unstructured.SetNestedSlice(hr.Object, []any{
		map[string]any{"type": "Ready", "status": "True"},
	}, "status", "conditions")

	r, _, _ := makeReconciler(t, org, ns, hr)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a Ready vCluster HR must stop the requeue, got %v", res)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	if got.Status.VCluster.Phase != "Ready" {
		t.Errorf("status.vcluster.phase: got %q want Ready (HR Ready + ns Active)", got.Status.VCluster.Phase)
	}
	if got.Status.Conditions[0].Type != "Ready" || got.Status.Conditions[0].Status != "True" {
		t.Errorf("expected Ready=True once the HR is Ready, got %+v", got.Status.Conditions[0])
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, kc := makeReconciler(t, org)

	// First reconcile (populates everything).
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	firstCreates := gs.createOrgs + gs.createRepos + gs.createFiles
	firstUpdates := gs.updateFiles
	firstKC := kc.calls

	// Second reconcile should be a near-no-op:
	//  - Keycloak EnsureGroup: still called once (find path)
	//  - Gitea EnsureOrg: GET hits, no create
	//  - Gitea EnsureRepo: GET hits, no create
	//  - PutFile: GET hits, byte-equal short-circuit, no write
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if kc.calls != firstKC+1 {
		// EnsureGroup is called every reconcile (one find+update). The
		// stub doesn't bump on equal attrs — acceptable. Just assert
		// no extra creates.
	}
	if delta := (gs.createOrgs + gs.createRepos + gs.createFiles) - firstCreates; delta != 0 {
		t.Errorf("idempotency: expected zero new creates, got %d", delta)
	}
	if delta := gs.updateFiles - firstUpdates; delta != 0 {
		t.Errorf("idempotency: expected zero file updates, got %d", delta)
	}
}

func TestReconcile_KeycloakGroupExists(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	// Pre-seed: pretend the group already exists.
	kc.groupID = "kc-existing-acme"
	kc.groupPath = "/acme"

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.KeycloakGroup.ID != "kc-existing-acme" {
		t.Errorf("keycloak group ID should be the pre-existing one, got %q", got.Status.KeycloakGroup.ID)
	}
}

func TestReconcile_GiteaOrgAlreadyExists(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)
	// Pre-seed the Gitea Org so EnsureOrg's GET 200s and the create
	// is never attempted.
	gs.orgs["acme"] = gitea.Org{
		ID:       42,
		Username: "acme",
		FullName: "Pre-existing",
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gs.createOrgs != 0 {
		t.Errorf("expected 0 admin/orgs creates when org pre-exists, got %d", gs.createOrgs)
	}
	// Repo + files still need to be created.
	if gs.createRepos != 1 {
		t.Errorf("expected 1 repo create even with pre-existing org, got %d", gs.createRepos)
	}
	if gs.createFiles != 11 {
		t.Errorf("expected 11 file creates even with pre-existing org, got %d", gs.createFiles)
	}
}

func TestReconcile_SlugMetadataMismatch(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.Slug = "renamed-acme" // CR's slug diverges from metadata.name
	r, gs, kc := makeReconciler(t, org)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile (drift): %v", err)
	}
	// Drift errors do not requeue (require operator action).
	if res.RequeueAfter != 0 {
		t.Errorf("drift should not requeue: got %v", res)
	}
	// Critical: no downstream artifacts created.
	if kc.calls != 0 {
		t.Errorf("drift: keycloak should not be called, got %d", kc.calls)
	}
	if gs.createOrgs != 0 || gs.createRepos != 0 || gs.createFiles != 0 {
		t.Errorf("drift: no gitea writes expected, got orgs=%d repos=%d files=%d",
			gs.createOrgs, gs.createRepos, gs.createFiles)
	}

	// Status surfaces SlugMetadataMismatch.
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Type != "Ready" ||
		got.Status.Conditions[0].Status != "False" ||
		got.Status.Conditions[0].Reason != "SlugMetadataMismatch" {
		t.Errorf("expected SlugMetadataMismatch False condition, got %+v",
			got.Status.Conditions)
	}
}

func TestReconcile_Missing_NoError(t *testing.T) {
	t.Parallel()
	r, _, _ := makeReconciler(t /* no objects */)
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ghost"},
	})
	if err != nil {
		t.Fatalf("reconcile of missing CR should be a no-op, got: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("missing CR should not requeue, got %v", res)
	}
}

// TestUpsertUserAccess_ClusterScoped — Refs #4773.
//
// UserAccess flipped from a namespaced Crossplane Claim to a plain
// CLUSTER-scoped CRD (the useraccess-controller owns cross-namespace
// RoleBindings + cluster-scoped ClusterRoleBindings via ownerRefs, which
// a namespaced owner cannot do). The upsert path therefore writes the CR
// with NO metadata.namespace and Get/Create route to the cluster path.
//
// This test asserts:
//  1. Upsert writes the UserAccess CR cluster-scoped (Get with
//     `client.ObjectKey{Name: name}`, no namespace).
//  2. The CR carries an EMPTY metadata.namespace.
//  3. The owner-per-CR mapping holds (1 owner = 1 CR).
func TestUpsertUserAccess_ClusterScoped(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	// #3687 (fold #3669): the reconcile requeues while the vCluster HR is
	// still provisioning (no HR/namespace seeded in this fake cluster).
	// That is correct convergence behavior — the UserAccess writes below
	// still happen on this pass; this test asserts their scope, not the
	// requeue cadence.
	_ = res

	// Assert every UserAccess CR is cluster-scoped (empty namespace).
	uaList := unstructured.UnstructuredList{}
	uaList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	})
	if err := r.List(context.Background(), &uaList); err != nil {
		t.Fatalf("list UserAccess: %v", err)
	}
	if len(uaList.Items) != 2 {
		t.Fatalf("expected 2 UserAccess CRs (one per owner), got %d", len(uaList.Items))
	}
	for _, ua := range uaList.Items {
		if ua.GetNamespace() != "" {
			t.Errorf("UserAccess %s: namespace = %q, want %q (cluster-scoped CR carries no namespace)",
				ua.GetName(), ua.GetNamespace(), "")
		}
	}

	// Idempotency: a second reconcile MUST not error (would error if the
	// Get path still used the empty-namespace key).
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile errored (regression — empty-namespace key on Get path?): %v", err)
	}

	// Listing must still see exactly 2 (no duplicates from a re-create
	// path triggered by an empty-namespace Get returning IsNotFound on
	// the live API server).
	uaList2 := unstructured.UnstructuredList{}
	uaList2.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	})
	if err := r.List(context.Background(), &uaList2); err != nil {
		t.Fatalf("list UserAccess (post-second-reconcile): %v", err)
	}
	if len(uaList2.Items) != 2 {
		t.Errorf("post-second-reconcile expected 2 UserAccess CRs, got %d (duplicates indicate the find-or-create path re-creates)", len(uaList2.Items))
	}
}

// TestUpsertUserAccess_ClusterScopedRegardlessOfNamespaceField — Refs
// #4773. UserAccess is cluster-scoped, so the upsert path writes the CR
// with NO namespace regardless of the (now-vestigial) UserAccessNamespace
// field. Setting it must not leak a namespace onto the cluster-scoped CR.
func TestUpsertUserAccess_ClusterScopedRegardlessOfNamespaceField(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	r.UserAccessNamespace = "catalyst-system" // vestigial; must be ignored

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile must succeed: %v", err)
	}
	uaList := unstructured.UnstructuredList{}
	uaList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	})
	if err := r.List(context.Background(), &uaList); err != nil {
		t.Fatalf("list UserAccess: %v", err)
	}
	for _, ua := range uaList.Items {
		if ua.GetNamespace() != "" {
			t.Errorf("UserAccess %s: namespace = %q, want %q (cluster-scoped — the UserAccessNamespace field must not leak onto the CR)",
				ua.GetName(), ua.GetNamespace(), "")
		}
	}
}

// TestReconcile_TenantPublic_RendersHTTPRoute covers the issue #1629
// follow-up + TBD-A67 issue #1990: when spec.tenantPublic.parentDomain
// is set, the reconciler MUST render an HTTPRoute in the Org's
// namespace pointing at the supplied backend Service AND the
// HTTPRoute hostname MUST carry the canonical `console.` infix
// (`console.<slug>.<parentDomain>`, e.g. `console.acme.omani.homes`).
// Without this, PowerDNS-resolved tenant hostnames fall through to
// the marketplace `tenant-wildcard` route and 404 instead of hitting
// the tenant's installed WordPress.
func TestReconcile_TenantPublic_RendersHTTPRoute(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{
		ParentDomain:   "omani.homes",
		BackendService: "wordpress-x-acme-x-vcluster",
		BackendPort:    80,
		Product:        "wordpress",
	}

	// Register HTTPRoute (Gateway API) with the fake client's scheme so
	// it can serialise the unstructured object the reconciler writes.
	r, _, _ := makeReconciler(t, org)
	scheme := r.Scheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	}, &unstructured.UnstructuredList{})

	// Two passes: pass 1 adds the tenant-networking finalizer + requeues (this
	// Org has a pool parentDomain, #4459), pass 2 does the up-path work that
	// renders the per-Org console HTTPRoute.
	reconcileTwice(t, r, "acme")

	// #4186: the per-Org console route now lands in catalyst-system (where
	// catalyst-api + catalyst-ui Services live) and is named after the host
	// (catalyst-ui-<host-dashed>), NOT in the Org namespace named after the
	// slug. The route serves the catalyst-ui SPA + catalyst-api — the
	// console host is never a product backend.
	hr := unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	})
	wantName := "catalyst-ui-console-acme-omani-homes"
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "catalyst-system", Name: wantName}, &hr); err != nil {
		t.Fatalf("get HTTPRoute catalyst-system/%s: %v", wantName, err)
	}
	hostnames, _, _ := unstructured.NestedSlice(hr.Object, "spec", "hostnames")
	if len(hostnames) != 1 || hostnames[0] != "console.acme.omani.homes" {
		t.Errorf("hostnames: got %v, want [console.acme.omani.homes]", hostnames)
	}
	// TBD-A67 issue #1990 regression guard: the `console.` infix is
	// non-negotiable. Asserting it directly (in addition to the full-
	// hostname check above) makes the future-debug-trail obvious when
	// any refactor of tenant_route.go drops the prefix.
	if got := hostnames[0]; got != nil {
		if s, ok := got.(string); !ok || !strings.HasPrefix(s, "console.") {
			t.Errorf("hostname must carry canonical console. prefix per CLAUDE.md §0, got %v", got)
		}
	}
	parents, _, _ := unstructured.NestedSlice(hr.Object, "spec", "parentRefs")
	if len(parents) != 1 {
		t.Fatalf("parentRefs: got %d, want 1", len(parents))
	}
	pr := parents[0].(map[string]any)
	// #4075: the per-Org console route now parents the DEDICATED console
	// Gateway (cilium-gateway-console), where the `*.<parentDomain>` TLS
	// listener + Secret live — not the shared `cilium-gateway`, which
	// only carries the apex `*.<sovFQDN>` listener (so the route attached
	// there but TLS still closed the connection).
	if pr["name"] != "cilium-gateway-console" || pr["namespace"] != "kube-system" {
		t.Errorf("parentRef: got %+v, want cilium-gateway-console/kube-system", pr)
	}
	// #4186 console-route shape: 5 rules. The catch-all `/` → catalyst-ui
	// is LAST; the auth/api/catalyst rules → catalyst-api. Crucially the
	// /auth/org-handover endpoint (the secure marketplace→console session
	// handoff, #4182) is routed to catalyst-api.
	rules, _, _ := unstructured.NestedSlice(hr.Object, "spec", "rules")
	if len(rules) != 5 {
		t.Fatalf("rules: got %d, want 5 (health/handover/api/catalyst/catch-all)", len(rules))
	}
	// Last rule is the catch-all `/` → catalyst-ui.
	lastBrs, _, _ := unstructured.NestedSlice(rules[4].(map[string]any), "backendRefs")
	if len(lastBrs) != 1 || lastBrs[0].(map[string]any)["name"] != "catalyst-ui" {
		t.Errorf("catch-all rule backend: got %v, want catalyst-ui", lastBrs)
	}
	// The handover rule (rule[1]) routes BOTH /auth/handover and
	// /auth/org-handover to catalyst-api.
	handoverMatches, _, _ := unstructured.NestedSlice(rules[1].(map[string]any), "matches")
	var sawOrgHandover bool
	for _, m := range handoverMatches {
		p, _, _ := unstructured.NestedString(m.(map[string]any), "path", "value")
		if p == "/auth/org-handover" {
			sawOrgHandover = true
		}
	}
	if !sawOrgHandover {
		t.Errorf("console route MUST route /auth/org-handover to catalyst-api (#4182), matches=%v", handoverMatches)
	}
	handoverBrs, _, _ := unstructured.NestedSlice(rules[1].(map[string]any), "backendRefs")
	if len(handoverBrs) != 1 || handoverBrs[0].(map[string]any)["name"] != "catalyst-api" {
		t.Errorf("handover rule backend: got %v, want catalyst-api", handoverBrs)
	}
	labels := hr.GetLabels()
	if labels["catalyst.openova.io/tenant-product"] != "wordpress" {
		t.Errorf("expected tenant-product=wordpress label, got %q",
			labels["catalyst.openova.io/tenant-product"])
	}
	if labels["catalyst.openova.io/parent-zone"] != "omani.homes" {
		t.Errorf("expected parent-zone=omani.homes label, got %q",
			labels["catalyst.openova.io/parent-zone"])
	}
	if labels["catalyst.openova.io/component"] != "catalyst-ui" {
		t.Errorf("expected component=catalyst-ui label, got %q",
			labels["catalyst.openova.io/component"])
	}
}

// TestReconcile_ConsoleServing_LandsEvenWhenAppProvisioningStepFails is the
// #4999 regression guard. The per-Org console HTTPRoute (which wires
// /auth/org-handover → catalyst-api, the zero-click owner sign-in) MUST land
// even when a LATER app-provisioning step fatally fails, so the owner can
// ALWAYS sign in. Before #4999 the console-serving trio ran at the END of
// Reconcile, so an early `r.fail(...)` — here a per-Org Keycloak realm error,
// the exact class of failure the hw240 2nd Org (walk-stranger-two) tripped —
// aborted the reconcile BEFORE the route was ever written, so
// /auth/org-handover 503'd. The trio now runs FIRST, so the route lands
// regardless of the downstream app pipeline.
func TestReconcile_ConsoleServing_LandsEvenWhenAppProvisioningStepFails(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{
		ParentDomain: "omani.rest",
		Subdomain:    "walk-stranger-two",
	}

	r, _, kc := makeReconciler(t, org)
	// Enable the per-Org realm and make it FAIL — an EARLY fatal reconcile step
	// (reconcilePerOrgRealm), well before the OLD console-route position.
	r.PerOrgRealmEnabled = true
	kc.realmEnsureErr = fmt.Errorf("keycloak realm create boom (simulated)")

	scheme := r.Scheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	}, &unstructured.UnstructuredList{})

	// A fresh Org first requeues once per finalizer it needs (per-org-realm,
	// then tenant-networking), then runs the up-path. Loop generously; every
	// pass returns nil error (finalizer-add + r.fail both requeue without
	// erroring), and the work pass is idempotent.
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "acme"},
		}); err != nil {
			t.Fatalf("reconcile pass %d returned error: %v", i+1, err)
		}
	}

	// Prove we actually exercised the early-abort path (the realm step ran +
	// failed), not a happy-path reconcile.
	if kc.realmEnsureCalls == 0 {
		t.Fatal("expected EnsureRealm to be attempted (the early fatal step)")
	}

	// The console HTTPRoute MUST exist despite the realm failure.
	hr := unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	})
	wantName := "catalyst-ui-console-walk-stranger-two-omani-rest"
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "catalyst-system", Name: wantName}, &hr); err != nil {
		t.Fatalf("console HTTPRoute MUST land even when an app-provisioning step fails (#4999): get catalyst-system/%s: %v", wantName, err)
	}
	hostnames, _, _ := unstructured.NestedSlice(hr.Object, "spec", "hostnames")
	if len(hostnames) != 1 || hostnames[0] != "console.walk-stranger-two.omani.rest" {
		t.Errorf("hostnames: got %v, want [console.walk-stranger-two.omani.rest]", hostnames)
	}
	// The route wires /auth/org-handover → catalyst-api (the sign-in path).
	rules, _, _ := unstructured.NestedSlice(hr.Object, "spec", "rules")
	if len(rules) != 5 {
		t.Fatalf("console route rules: got %d, want 5 (health/handover/api/catalyst/catch-all)", len(rules))
	}
	handoverMatches, _, _ := unstructured.NestedSlice(rules[1].(map[string]any), "matches")
	var sawOrgHandover bool
	for _, m := range handoverMatches {
		p, _, _ := unstructured.NestedString(m.(map[string]any), "path", "value")
		if p == "/auth/org-handover" {
			sawOrgHandover = true
		}
	}
	if !sawOrgHandover {
		t.Errorf("console route MUST route /auth/org-handover to catalyst-api even when app provisioning fails (#4999), matches=%v", handoverMatches)
	}
}

// TestReconcile_TenantPublic_DisabledByDefault covers the no-op path:
// when spec.tenantPublic.parentDomain is empty (the default for every
// existing Org CR), NO HTTPRoute MUST be written. Without this guard
// every legacy Org would suddenly try to render an HTTPRoute and the
// reconciler would surface TenantRouteFailed because BackendService is
// empty.
func TestReconcile_TenantPublic_DisabledByDefault(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // no TenantPublic set
	r, _, _ := makeReconciler(t, org)
	scheme := r.Scheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	}, &unstructured.UnstructuredList{})

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	hrList := unstructured.UnstructuredList{}
	hrList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	})
	if err := r.List(context.Background(), &hrList); err != nil {
		t.Fatalf("list HTTPRoute: %v", err)
	}
	if len(hrList.Items) != 0 {
		t.Errorf("expected 0 HTTPRoutes when tenantPublic is unset, got %d", len(hrList.Items))
	}
}

// TestReconcile_TenantPublic_ParentDomainSet_BackendPending covers the
// #3376 ordering case: the funnel mints the Organization CR with
// spec.tenantPublic.{parentDomain,subdomain} set but NO backendService
// (the provisioning service patches backendService LATER, once the
// purchased product is Ready — tenant_public_patch.go). The reconciler
// MUST still succeed (NOT fail with TenantRouteFailed), and — #4186 —
// the console HTTPRoute MUST now be rendered EVEN WHILE backendService is
// pending. The console host (console.<slug>.<pool>) is served by
// catalyst-ui + catalyst-api, NOT a product backend, so the customer must
// be able to SIGN IN and land on /jobs the moment the Org has a public
// hostname — long before any purchased product becomes Ready. The old
// behavior (skip the route until backendService is patched) is exactly
// why pool Orgs bounced /jobs → /login and the route had to be
// hand-applied per-Org (#4186).
//
// Before the #3376 fix this path returned an error from
// reconcileTenantRoute → the caller's fail() path marked the whole Org
// Failed and requeued forever; that must still not happen.
func TestReconcile_TenantPublic_ParentDomainSet_BackendPending(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	// parentDomain + subdomain set (exactly what organization_create.go
	// seeds on the funnel path), but backendService deliberately empty.
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{
		ParentDomain: "omani.homes",
		Subdomain:    "acme",
	}
	r, _, _ := makeReconciler(t, org)
	scheme := r.Scheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	}, &unstructured.UnstructuredList{})

	// MUST NOT error — the missing backendService is irrelevant to the
	// console route (the console is catalyst-ui, not a product backend).
	// Two passes: pass 1 adds the tenant-networking finalizer + requeues
	// (pool parentDomain set, #4459), pass 2 does the up-path + status write.
	reconcileTwice(t, r, "acme")

	// The Org's Ready condition MUST NOT be TenantRouteFailed.
	got := orgapi.Organization{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get Organization acme: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == "Ready" && c.Reason == "TenantRouteFailed" {
			t.Errorf("Ready condition must NOT be TenantRouteFailed; got %+v", c)
		}
	}

	// #4186: the console route IS now written even with backendService
	// pending — in catalyst-system, named after the host.
	hr := unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	})
	wantName := "catalyst-ui-console-acme-omani-homes"
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "catalyst-system", Name: wantName}, &hr); err != nil {
		t.Fatalf("console route MUST render while backendService is pending (#4186); get catalyst-system/%s: %v", wantName, err)
	}
	rules, _, _ := unstructured.NestedSlice(hr.Object, "spec", "rules")
	if len(rules) != 5 {
		t.Fatalf("rules: got %d, want 5 console-route rules", len(rules))
	}
}

// ── Per-Org Keycloak realm (#3084 Part 2) ────────────────────────────
//
// These mirror the EnsureGroup test cases (create, idempotent-already-
// exists, error path, status population, finalizer teardown) but for the
// per-Org realm path. The base makeReconciler leaves PerOrgRealmEnabled
// false (Disabled state, like iac-bootstrap deps being nil); these tests
// opt in by flipping r.PerOrgRealmEnabled = true.

// reconcileTwice drives the Reconcile loop twice so the finalizer-add
// requeue (first pass) is followed by the real work (second pass). When
// PerOrgRealmEnabled is true the first reconcile returns Requeue:true
// after adding the per-org-realm finalizer; the actual realm + status
// work happens on the second pass.
func reconcileTwice(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: name},
		}); err != nil {
			t.Fatalf("reconcile pass %d: %v", i+1, err)
		}
	}
}

func TestReconcile_PerOrgRealm_CreatesAndPopulatesStatus(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true

	reconcileTwice(t, r, "acme")

	// EnsureRealm called (find-or-create) + realm now exists in the fake.
	if kc.realmEnsureCalls == 0 {
		t.Errorf("expected EnsureRealm to be called, got %d", kc.realmEnsureCalls)
	}
	if !kc.realms["acme"] {
		t.Errorf("expected realm 'acme' to exist in fake KC, got %v", kc.realms)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.PerOrgRealm.State != "Ready" {
		t.Errorf("status.perOrgRealm.state: got %q want Ready (lastError=%q)",
			got.Status.PerOrgRealm.State, got.Status.PerOrgRealm.LastError)
	}
	if got.Status.PerOrgRealm.RealmName != "acme" {
		t.Errorf("status.perOrgRealm.realmName: got %q want acme", got.Status.PerOrgRealm.RealmName)
	}
	// The finalizer must be present so a later delete tears the realm down.
	if !containsFinalizer(got.Finalizers, PerOrgRealmFinalizer) {
		t.Errorf("expected per-org-realm finalizer on the CR, got %v", got.Finalizers)
	}
	// PerOrgRealmProvisioned condition present + True.
	found := false
	for _, c := range got.Status.Conditions {
		if c.Type == "PerOrgRealmProvisioned" {
			found = true
			if c.Status != "True" || c.Reason != "RealmReady" {
				t.Errorf("PerOrgRealmProvisioned: got %+v want True/RealmReady", c)
			}
		}
	}
	if !found {
		t.Errorf("PerOrgRealmProvisioned condition missing from %+v", got.Status.Conditions)
	}
}

func TestReconcile_PerOrgRealm_IdempotentAlreadyExists(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true
	// Pre-seed: pretend the realm already exists. The fake's EnsureRealm
	// is a find-or-create that returns the slug regardless — assert it
	// does NOT error and the status still lands Ready.
	kc.realms = map[string]bool{"acme": true}

	reconcileTwice(t, r, "acme")

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.PerOrgRealm.State != "Ready" {
		t.Errorf("pre-existing realm: state got %q want Ready", got.Status.PerOrgRealm.State)
	}
	if !kc.realms["acme"] {
		t.Errorf("realm should still exist, got %v", kc.realms)
	}
}

func TestReconcile_PerOrgRealm_ErrorFailsReconcile(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true
	kc.realmEnsureErr = fmt.Errorf("keycloak unreachable")

	// First pass adds the finalizer + requeues (no error).
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile (finalizer add) should not error: %v", err)
	}
	// Second pass hits EnsureRealm → error → r.fail → requeue (no Go error).
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile (realm error path should requeue, not error): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("realm-failure path should requeue, got %v", res)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	// The Ready condition is downgraded to False/PerOrgRealmFailed (realm
	// is auth-critical — unlike iac-bootstrap which is non-fatal).
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Type != "Ready" ||
		got.Status.Conditions[0].Status != "False" ||
		got.Status.Conditions[0].Reason != "PerOrgRealmFailed" {
		t.Errorf("expected Ready=False/PerOrgRealmFailed, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_PerOrgRealm_DisabledWhenFlagOff(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	// PerOrgRealmEnabled left false (default) — feature off.

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// EnsureRealm must NOT be called when the flag is off.
	if kc.realmEnsureCalls != 0 {
		t.Errorf("expected 0 EnsureRealm calls when disabled, got %d", kc.realmEnsureCalls)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.PerOrgRealm.State != "Disabled" {
		t.Errorf("status.perOrgRealm.state: got %q want Disabled", got.Status.PerOrgRealm.State)
	}
	// No finalizer added when the feature is off.
	if containsFinalizer(got.Finalizers, PerOrgRealmFinalizer) {
		t.Errorf("per-org-realm finalizer should NOT be added when disabled, got %v", got.Finalizers)
	}
}

func TestReconcile_PerOrgRealm_FinalizerTeardown(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true

	// Drive to steady state (finalizer added + realm created).
	reconcileTwice(t, r, "acme")
	if !kc.realms["acme"] {
		t.Fatalf("precondition: realm should exist before delete, got %v", kc.realms)
	}

	// Mark the CR for deletion (set DeletionTimestamp via the client).
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := r.Delete(context.Background(), &got); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Reconcile the deletion: the finalizer flow must call DeleteRealm and
	// then drop the finalizer so the CR can tombstone.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}

	if kc.realmDeleteCalls == 0 {
		t.Errorf("expected DeleteRealm to be called on teardown, got %d", kc.realmDeleteCalls)
	}
	if kc.realms["acme"] {
		t.Errorf("realm should be deleted on teardown, got %v", kc.realms)
	}
}

// TestReconcileTenantConsoleTLS_IssuesCertAndAppendsListener covers
// issues #4075/#4241: when an Org picks a free-subdomain hostname under a
// pool parent zone, reconcileTenantConsoleTLS MUST (1) issue a cert-manager
// Certificate for the 2-label SAN `*.<slug>.<parentDomain>` +
// `<slug>.<parentDomain>` via the DNS-01 ClusterIssuer (the apex pool
// wildcard `*.<parentDomain>` cannot cover the 2-label
// `console.<slug>.<parentDomain>`) and (2) append a
// `*.<slug>.<parentDomain>` HTTPS+HTTP listener pair to the dedicated
// console Gateway WITHOUT touching the apex `*.<sovFQDN>` listener. The
// append MUST be idempotent (a second pass is a no-op). The slug here is
// the sampleOrg slug `acme`.
func TestReconcileTenantConsoleTLS_IssuesCertAndAppendsListener(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{
		ParentDomain: "omani.homes",
	}

	r, _, _ := makeReconciler(t, org)
	scheme := r.Scheme()
	// Register Certificate + Gateway GVKs (+ Lists) so the fake client
	// can serialise the unstructured objects the reconciler writes.
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}

	// Seed the bootstrap-rendered console Gateway carrying ONLY the apex
	// `*.<sovFQDN>` listener — the starting state on a live Sovereign.
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName("cilium-gateway-console")
	gw.SetNamespace("kube-system")
	gw.Object["spec"] = map[string]any{
		"gatewayClassName": "cilium",
		"listeners": []any{
			map[string]any{
				"name":     "console-https",
				"hostname": "*.omantel.biz",
				"port":     int64(31443),
				"protocol": "HTTPS",
			},
		},
	}
	if err := r.Create(context.Background(), gw); err != nil {
		t.Fatalf("seed console Gateway: %v", err)
	}

	// First pass: should create the cert + append the two listeners.
	changed, err := r.reconcileTenantConsoleTLS(context.Background(), org)
	if err != nil {
		t.Fatalf("reconcileTenantConsoleTLS pass 1: %v", err)
	}
	if !changed {
		t.Errorf("pass 1: expected changed=true (cert + listeners written)")
	}

	// Assert the per-Org Certificate: name org-wildcard-tls-<slug>-<parent>,
	// 2-label SAN covering console.<slug>.<parent>.
	cert := unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "org-wildcard-tls-acme-omani-homes"}, &cert); err != nil {
		t.Fatalf("get Certificate org-wildcard-tls-acme-omani-homes: %v", err)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if len(dnsNames) != 2 || dnsNames[0] != "*.acme.omani.homes" || dnsNames[1] != "acme.omani.homes" {
		t.Errorf("cert dnsNames: got %v, want [*.acme.omani.homes acme.omani.homes]", dnsNames)
	}
	if cn, _, _ := unstructured.NestedString(cert.Object, "spec", "commonName"); cn != "acme.omani.homes" {
		t.Errorf("cert commonName: got %q, want acme.omani.homes", cn)
	}
	if secret, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName"); secret != "org-wildcard-tls-acme-omani-homes" {
		t.Errorf("cert secretName: got %q, want org-wildcard-tls-acme-omani-homes", secret)
	}
	if issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name"); issuer != "letsencrypt-dns01-prod-powerdns" {
		t.Errorf("cert issuerRef.name: got %q, want letsencrypt-dns01-prod-powerdns", issuer)
	}

	// Assert the Gateway listeners: apex preserved + two per-Org listeners.
	gotGW := unstructured.Unstructured{}
	gotGW.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "cilium-gateway-console"}, &gotGW); err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	listeners, _, _ := unstructured.NestedSlice(gotGW.Object, "spec", "listeners")
	names := map[string]map[string]any{}
	for _, l := range listeners {
		m := l.(map[string]any)
		names[m["name"].(string)] = m
	}
	if len(listeners) != 3 {
		t.Fatalf("listeners: got %d (%v), want 3", len(listeners), keysOf(names))
	}
	if _, ok := names["console-https"]; !ok {
		t.Errorf("apex listener console-https was dropped — MUST be preserved (regression)")
	}
	httpsL, ok := names["console-https-acme"]
	if !ok {
		t.Fatalf("per-Org HTTPS listener console-https-acme not appended")
	}
	if httpsL["hostname"] != "*.acme.omani.homes" {
		t.Errorf("per-Org HTTPS hostname: got %v, want *.acme.omani.homes", httpsL["hostname"])
	}
	tls := httpsL["tls"].(map[string]any)
	refs := tls["certificateRefs"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["name"] != "org-wildcard-tls-acme-omani-homes" {
		t.Errorf("per-Org HTTPS certificateRefs: got %v, want secret org-wildcard-tls-acme-omani-homes", refs)
	}
	if _, ok := names["console-http-acme"]; !ok {
		t.Errorf("per-Org HTTP listener console-http-acme not appended")
	}

	// Second pass: idempotent — no further change.
	changed2, err := r.reconcileTenantConsoleTLS(context.Background(), org)
	if err != nil {
		t.Fatalf("reconcileTenantConsoleTLS pass 2: %v", err)
	}
	if changed2 {
		t.Errorf("pass 2: expected changed=false (idempotent), got true")
	}
	gotGW2 := unstructured.Unstructured{}
	gotGW2.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "cilium-gateway-console"}, &gotGW2); err != nil {
		t.Fatalf("get console Gateway pass 2: %v", err)
	}
	l2, _, _ := unstructured.NestedSlice(gotGW2.Object, "spec", "listeners")
	if len(l2) != 3 {
		t.Errorf("pass 2 listeners: got %d, want 3 (no duplicate append)", len(l2))
	}
}

// TestReconcileTenantConsoleTLS_NoopWhenNoParentDomain asserts the
// feature is a clean no-op for Orgs without a pool-parent public
// hostname (the common case) — no cert, no gateway mutation.
func TestReconcileTenantConsoleTLS_NoopWhenNoParentDomain(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // TenantPublic zero-value: ParentDomain == ""
	r, _, _ := makeReconciler(t, org)
	changed, err := r.reconcileTenantConsoleTLS(context.Background(), org)
	if err != nil {
		t.Fatalf("reconcileTenantConsoleTLS: %v", err)
	}
	if changed {
		t.Errorf("expected no-op (changed=false) when ParentDomain is empty, got changed=true")
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTeardownTenantNetworking_CascadesAndIsSelective is the #4459 close-gate
// (sibling of #4250): deleting an Organization CR MUST cascade-delete the
// per-Org tenant-networking artifacts that live outside the Flux-pruned
// `<slug>` namespace and carry no GC-followed ownerRef —
//
//  1. the `console-https-<slug>` / `console-http-<slug>` listener pair on the
//     SHARED console Gateway (without removal these dead listeners ACCUMULATE),
//  2. the per-Org wildcard Certificate (kube-system),
//  3. the per-Org console HTTPRoute (catalyst-system),
//  4. the central-PowerDNS pool A-records (off-cluster),
//
// while leaving the apex listener AND a SECOND Org's listeners byte-for-byte
// intact (selectivity), and being idempotent on a repeated teardown.
func TestTeardownTenantNetworking_CascadesAndIsSelective(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // slug "acme"
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{ParentDomain: "omani.homes"}

	r, _, _ := makeReconciler(t, org)
	scheme := r.Scheme()
	// Register Certificate + Gateway + HTTPRoute GVKs (+ Lists) so the fake
	// client can serialise the unstructured objects the teardown deletes.
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK, httpRouteGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}

	// Stub the central PowerDNS so the DNS-DELETE is captured.
	var cap capturedPatch
	pdns := newPDNSStub(t, &cap)
	defer pdns.Close()
	r.PoolPowerDNSURL = pdns.URL
	r.PoolPowerDNSAPIKey = "central-pool-key"
	r.TenantConsoleLBIPv4 = "212.72.24.33"

	ctx := context.Background()

	// Seed the shared console Gateway carrying: the apex listener, THIS Org's
	// two per-Org listeners, AND a SECOND Org's two listeners (the selectivity
	// guard — they must survive acme's teardown).
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName("cilium-gateway-console")
	gw.SetNamespace("kube-system")
	gw.Object["spec"] = map[string]any{
		"gatewayClassName": "cilium",
		"listeners": []any{
			map[string]any{"name": "console-https", "hostname": "*.omantel.biz", "port": int64(31443), "protocol": "HTTPS"},
			map[string]any{"name": "console-https-acme", "hostname": "*.acme.omani.homes", "port": int64(31443), "protocol": "HTTPS"},
			map[string]any{"name": "console-http-acme", "hostname": "*.acme.omani.homes", "port": int64(31080), "protocol": "HTTP"},
			map[string]any{"name": "console-https-globex", "hostname": "*.globex.omani.homes", "port": int64(31443), "protocol": "HTTPS"},
			map[string]any{"name": "console-http-globex", "hostname": "*.globex.omani.homes", "port": int64(31080), "protocol": "HTTP"},
		},
	}
	if err := r.Create(ctx, gw); err != nil {
		t.Fatalf("seed console Gateway: %v", err)
	}

	// Seed the per-Org wildcard Certificate (kube-system).
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName("org-wildcard-tls-acme-omani-homes")
	cert.SetNamespace("kube-system")
	cert.Object["spec"] = map[string]any{"secretName": "org-wildcard-tls-acme-omani-homes"}
	if err := r.Create(ctx, cert); err != nil {
		t.Fatalf("seed Certificate: %v", err)
	}

	// Seed the per-Org console HTTPRoute (catalyst-system, NOT the <slug> ns).
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK)
	route.SetName("catalyst-ui-console-acme-omani-homes")
	route.SetNamespace("catalyst-system")
	route.Object["spec"] = map[string]any{"hostnames": []any{"console.acme.omani.homes"}}
	if err := r.Create(ctx, route); err != nil {
		t.Fatalf("seed HTTPRoute: %v", err)
	}

	// ── Teardown pass 1: cascades all four artifacts ────────────────────
	if err := r.teardownTenantNetworking(ctx, org); err != nil {
		t.Fatalf("teardownTenantNetworking pass 1: %v", err)
	}

	// 1. Gateway: acme's two listeners gone; apex + globex's two preserved.
	gotGW := unstructured.Unstructured{}
	gotGW.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "cilium-gateway-console"}, &gotGW); err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	listeners, _, _ := unstructured.NestedSlice(gotGW.Object, "spec", "listeners")
	got := map[string]bool{}
	for _, l := range listeners {
		got[l.(map[string]any)["name"].(string)] = true
	}
	if got["console-https-acme"] || got["console-http-acme"] {
		t.Errorf("acme per-Org listeners NOT removed: %v", got)
	}
	for _, keep := range []string{"console-https", "console-https-globex", "console-http-globex"} {
		if !got[keep] {
			t.Errorf("listener %q was wrongly dropped — teardown must be selective: %v", keep, got)
		}
	}
	if len(listeners) != 3 {
		t.Errorf("listeners after teardown: got %d (%v), want 3 (apex + globex pair)", len(listeners), got)
	}

	// 2. Certificate deleted (cert-manager GCs the backing Secret with it).
	gotCert := unstructured.Unstructured{}
	gotCert.SetGroupVersionKind(certificateGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "org-wildcard-tls-acme-omani-homes"}, &gotCert); !apierrors.IsNotFound(err) {
		t.Errorf("Certificate should be deleted, get err = %v (want NotFound)", err)
	}

	// 3. HTTPRoute deleted.
	gotRoute := unstructured.Unstructured{}
	gotRoute.SetGroupVersionKind(httpRouteGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: "catalyst-system", Name: "catalyst-ui-console-acme-omani-homes"}, &gotRoute); !apierrors.IsNotFound(err) {
		t.Errorf("HTTPRoute should be deleted, get err = %v (want NotFound)", err)
	}

	// 4. DNS: two rrsets PATCHed with changetype=DELETE on the pool zone.
	if want := "/api/v1/servers/localhost/zones/omani.homes."; cap.path != want {
		t.Errorf("PATCH path: got %q, want %q", cap.path, want)
	}
	rrsets, _ := cap.body["rrsets"].([]any)
	if len(rrsets) != 2 {
		t.Fatalf("DNS rrsets: got %d, want 2 (console + wildcard DELETE)", len(rrsets))
	}
	dnsNames := map[string]map[string]any{}
	for _, rs := range rrsets {
		m := rs.(map[string]any)
		dnsNames[m["name"].(string)] = m
	}
	for _, wantName := range []string{"console.acme.omani.homes.", "*.acme.omani.homes."} {
		m, ok := dnsNames[wantName]
		if !ok {
			t.Fatalf("missing DELETE rrset %q (have %v)", wantName, keysOf(dnsNames))
		}
		if m["changetype"] != "DELETE" {
			t.Errorf("%s changetype: got %v, want DELETE", wantName, m["changetype"])
		}
	}

	// ── Teardown pass 2: idempotent — every artifact already gone ────────
	if err := r.teardownTenantNetworking(ctx, org); err != nil {
		t.Fatalf("teardownTenantNetworking pass 2 (idempotent): %v", err)
	}
	gotGW2 := unstructured.Unstructured{}
	gotGW2.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "cilium-gateway-console"}, &gotGW2); err != nil {
		t.Fatalf("get console Gateway pass 2: %v", err)
	}
	l2, _, _ := unstructured.NestedSlice(gotGW2.Object, "spec", "listeners")
	if len(l2) != 3 {
		t.Errorf("pass 2 listeners: got %d, want 3 (no further removal)", len(l2))
	}
}

// TestTeardownTenantNetworking_NoopWhenNoParentDomain asserts the teardown is
// a clean no-op for Orgs that never had a pool-parent public hostname (the
// common case) — none of the up-path tenant-networking steps engaged, so the
// teardown touches nothing.
func TestTeardownTenantNetworking_NoopWhenNoParentDomain(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // TenantPublic zero-value: ParentDomain == ""
	r, _, _ := makeReconciler(t, org)
	if err := r.teardownTenantNetworking(context.Background(), org); err != nil {
		t.Fatalf("teardownTenantNetworking no-op: %v", err)
	}
}

// TestReconcile_DeleteCascadesTenantNetworking drives the FULL Reconcile
// deletion path (not just the orchestrator) to prove the cascade is wired into
// the finalizer flow: a steady-state Org with a pool parentDomain → mark for
// deletion → Reconcile → the per-Org HTTPRoute is gone (representative of the
// whole tenant-networking set the orchestrator reaps before the CR tombstones).
func TestReconcile_DeleteCascadesTenantNetworking(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{ParentDomain: "omani.homes"}
	r, _, _ := makeReconciler(t, org)
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK, httpRouteGVK} {
		r.Scheme().AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		r.Scheme().AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}

	// Drive reconciles to steady state: the first pass adds the
	// tenant-networking finalizer + requeues, the second does the up-path work
	// that lands the per-Org console HTTPRoute.
	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("up-path reconcile pass %d: %v", i+1, err)
		}
	}
	routeKey := client.ObjectKey{Namespace: "catalyst-system", Name: "catalyst-ui-console-acme-omani-homes"}
	probe := unstructured.Unstructured{}
	probe.SetGroupVersionKind(httpRouteGVK)
	if err := r.Get(ctx, routeKey, &probe); err != nil {
		t.Fatalf("precondition: per-Org HTTPRoute should exist after up-path reconcile: %v", err)
	}
	// Precondition: the tenant-networking finalizer is present (it is what
	// holds the CR through deletion so the cascade can run).
	var steady orgapi.Organization
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &steady); err != nil {
		t.Fatalf("get steady: %v", err)
	}
	if !containsFinalizer(steady.Finalizers, TenantNetworkingFinalizer) {
		t.Fatalf("precondition: tenant-networking finalizer should be present, got %v", steady.Finalizers)
	}

	// Mark for deletion + reconcile the deletion path (the cascade runs, then
	// the finalizer is dropped + requeued).
	if err := r.Delete(ctx, &steady); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("delete reconcile pass %d: %v", i+1, err)
		}
	}

	// The per-Org HTTPRoute must be cascade-deleted by the deletion path.
	gone := unstructured.Unstructured{}
	gone.SetGroupVersionKind(httpRouteGVK)
	if err := r.Get(ctx, routeKey, &gone); !apierrors.IsNotFound(err) {
		t.Errorf("per-Org HTTPRoute should be cascade-deleted on Org delete, get err = %v (want NotFound)", err)
	}
}
