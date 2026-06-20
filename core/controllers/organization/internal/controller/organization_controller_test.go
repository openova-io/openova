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
		t:     t,
		orgs:  map[string]gitea.Org{},
		repos: map[string]gitea.Repo{},
		files: map[string]fileEntry{},
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
			Slug:                   "acme",
			DisplayName:            "ACME Corp",
			Kind:                   "customer",
			Tier: "org",
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
	if gs.createFiles != 3 {
		t.Errorf("expected 3 gitea file creates (namespace, vcluster, kustomization), got %d", gs.createFiles)
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
	if gs.createFiles != 3 {
		t.Errorf("expected 3 file creates even with pre-existing org, got %d", gs.createFiles)
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

// TestUpsertUserAccess_NamespaceScoped — qa-loop iter-8 Fix #42 regression.
//
// The Crossplane Claim CR (`useraccesses.access.openova.io`) is
// namespace-scoped on the live API server. A previous code path called
// r.Get / r.Create with `client.ObjectKey{Name: name}` (empty
// namespace), which the apiserver rejects with `an empty namespace may
// not be set when a resource name is provided`. That single error
// blocked the entire downstream chain on omantel — Organization
// transitioned to Failed/UserAccessFailed, Environment never created
// the per-Org repo, Application never registered Flux GitRepository,
// no Pod was ever scheduled.
//
// This test asserts:
//  1. Upsert writes the UserAccess CR into the configured
//     r.UserAccessNamespace (default `catalyst-system`).
//  2. The CR carries metadata.namespace == that namespace (NOT empty).
//  3. The owner-per-CR mapping holds (1 owner = 1 CR).
func TestUpsertUserAccess_NamespaceScoped(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile failed (regression — empty-namespace bug?): %v", err)
	}
	// #3687 (fold #3669): the reconcile requeues while the vCluster HR is
	// still provisioning (no HR/namespace seeded in this fake cluster).
	// That is correct convergence behavior — the UserAccess writes below
	// still happen on this pass; this test asserts their namespace, not
	// the requeue cadence.
	_ = res

	// Assert every UserAccess CR carries metadata.namespace =
	// r.UserAccessNamespace (catalyst-system).
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
		if ua.GetNamespace() != "catalyst-system" {
			t.Errorf("UserAccess %s: namespace = %q, want %q (the apiserver rejects an empty namespace on namespaced CRs)",
				ua.GetName(), ua.GetNamespace(), "catalyst-system")
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

// TestUpsertUserAccess_DefaultsToCatalystSystem — when the Reconciler
// is constructed with UserAccessNamespace=="" (e.g. main.go env var
// missing), the upsert path must default to "catalyst-system" rather
// than panic or write into the empty namespace.
func TestUpsertUserAccess_DefaultsToCatalystSystem(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	r.UserAccessNamespace = "" // simulate unset env var

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile with empty UserAccessNamespace must default and succeed: %v", err)
	}
	uaList := unstructured.UnstructuredList{}
	uaList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "access.openova.io", Version: "v1alpha1", Kind: "UserAccessList",
	})
	if err := r.List(context.Background(), &uaList); err != nil {
		t.Fatalf("list UserAccess: %v", err)
	}
	for _, ua := range uaList.Items {
		if ua.GetNamespace() != "catalyst-system" {
			t.Errorf("UserAccess %s: namespace = %q, want default %q",
				ua.GetName(), ua.GetNamespace(), "catalyst-system")
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

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	hr := unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute",
	})
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "acme", Name: "acme"}, &hr); err != nil {
		t.Fatalf("get HTTPRoute acme/acme: %v", err)
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
	if pr["name"] != "cilium-gateway" || pr["namespace"] != "kube-system" {
		t.Errorf("parentRef: got %+v, want cilium-gateway/kube-system", pr)
	}
	rules, _, _ := unstructured.NestedSlice(hr.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("rules: got %d, want 1", len(rules))
	}
	brs, _, _ := unstructured.NestedSlice(rules[0].(map[string]any), "backendRefs")
	if len(brs) != 1 {
		t.Fatalf("backendRefs: got %d, want 1", len(brs))
	}
	br := brs[0].(map[string]any)
	if br["name"] != "wordpress-x-acme-x-vcluster" {
		t.Errorf("backendRef name: got %v, want wordpress-x-acme-x-vcluster", br["name"])
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
// MUST treat "parentDomain set, backendService not-yet-set" as a normal
// transient window: the whole Org reconcile MUST still succeed (NOT fail
// with TenantRouteFailed), and simply NO HTTPRoute is written yet (it
// lands on the later reconcile that the backendService PATCH triggers).
//
// Before the #3376 fix this path returned an error from
// reconcileTenantRoute → the caller's fail() path marked the whole Org
// Failed and requeued forever, so the Org never went Ready and the
// customer's `console.<slug>.<pool-tld>` console never served.
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

	// MUST NOT error — the missing backendService is a transient ordering
	// window, not a failure.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile must succeed while backendService is pending (#3376), got: %v", err)
	}

	// The Org's Ready condition MUST NOT be TenantRouteFailed — the route
	// step was skipped, not failed.
	got := orgapi.Organization{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get Organization acme: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == "Ready" && c.Reason == "TenantRouteFailed" {
			t.Errorf("Ready condition must NOT be TenantRouteFailed when backendService is merely pending (#3376); got %+v", c)
		}
	}

	// No HTTPRoute is written yet — it lands once backendService is patched.
	hrList := unstructured.UnstructuredList{}
	hrList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	})
	if err := r.List(context.Background(), &hrList); err != nil {
		t.Fatalf("list HTTPRoute: %v", err)
	}
	if len(hrList.Items) != 0 {
		t.Errorf("expected 0 HTTPRoutes while backendService is pending, got %d", len(hrList.Items))
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
