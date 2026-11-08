package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

const (
	testRegionA = "hw-me-east-215-a-rtz-prod"
	testRegionB = "hw-me-east-215-b-rtz-prod"
)

var testRegions = regions.New(testRegionA, testRegionB)

// fakeRegionGit is an in-memory RegionGit: owner/repo → path → bytes.
type fakeRegionGit struct {
	mu      sync.Mutex
	files   map[string][]byte
	commits int
}

func newFakeRegionGit() *fakeRegionGit { return &fakeRegionGit{files: map[string][]byte{}} }

func (f *fakeRegionGit) EnsureOrg(_ context.Context, slug, _, _, _ string) (gitea.Org, error) {
	return gitea.Org{Username: slug}, nil
}
func (f *fakeRegionGit) EnsureRepo(_ context.Context, org, name, _ string, _ bool) (gitea.Repo, error) {
	return gitea.Repo{Name: name, FullName: org + "/" + name}, nil
}
func (f *fakeRegionGit) PutFile(_ context.Context, org, repo, _, path string, data []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := org + "/" + repo + "/" + path
	if bytes.Equal(f.files[k], data) {
		return gitea.File{Path: path}, false, nil
	}
	f.files[k] = append([]byte(nil), data...)
	f.commits++
	return gitea.File{Path: path}, true, nil
}
func (f *fakeRegionGit) DeleteFile(_ context.Context, org, repo, _, path, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, org+"/"+repo+"/"+path)
	return true, nil
}

func (f *fakeRegionGit) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[key]
	return b, ok
}

// ensureTestRegionWiring gives a test Reconciler the region registry and an
// in-memory region Git when the test did not configure them.
func ensureTestRegionWiring(r *Reconciler) *fakeRegionGit {
	if r.Regions.Len() == 0 {
		r.Regions = testRegions
	}
	if fg, ok := r.RegionGit.(*fakeRegionGit); ok {
		return fg
	}
	fg := newFakeRegionGit()
	r.RegionGit = fg
	return fg
}

func decodeDocs(t *testing.T, data []byte) []*unstructured.Unstructured {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return out
			}
			t.Fatalf("decode: %v", err)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, u)
	}
}

// fluxApplyRegionFile stands in for one region's `sovereign-region`
// Kustomization: it applies the Org's committed file for that region to the
// cluster client, substituting the region postBuild variables with their
// defaults, and prunes the Org's objects when the file is gone.
func fluxApplyRegionFile(t *testing.T, r *Reconciler, fg *fakeRegionGit, org *orgapi.Organization, region string) {
	t.Helper()
	owner, repo := r.sovereignRegionRepo()
	data, ok := fg.get(owner + "/" + repo + "/" + perOrgRegionFilePath(r.regionFQDN(org), region, org.Spec.Slug))
	if !ok {
		return // teardownPerOrgFlux already deleted this region's objects
	}
	// postBuild default substitution: ${VAR:=default} → default.
	s := string(data)
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "}")
		expr := s[i+2 : i+j]
		val := ""
		if k := strings.Index(expr, ":="); k >= 0 {
			val = expr[k+2:]
		}
		s = s[:i] + val + s[i+j+1:]
	}
	ctx := context.Background()
	for _, o := range decodeDocs(t, []byte(s)) {
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(o.GroupVersionKind())
		err := r.Get(ctx, client.ObjectKey{Namespace: o.GetNamespace(), Name: o.GetName()}, cur)
		switch {
		case apierrors.IsNotFound(err):
			if err := r.Create(ctx, o); err != nil {
				t.Fatalf("apply create %s: %v", o.GetName(), err)
			}
		case err != nil:
			t.Fatalf("apply get %s: %v", o.GetName(), err)
		default:
			if string(mustJSON(t, cur.Object["spec"])) == string(mustJSON(t, o.Object["spec"])) {
				continue // server-side apply of an unchanged spec is a no-op
			}
			cur.Object["spec"] = o.Object["spec"]
			cur.SetLabels(o.GetLabels())
			if err := r.Update(ctx, cur); err != nil {
				t.Fatalf("apply update %s: %v", o.GetName(), err)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reconcilePerOrgFluxApplied runs reconcilePerOrgFlux and then region A's
// Flux apply, so assertions read the objects off the cluster client exactly
// as before ADR-0015 (when the controller created them itself).
func reconcilePerOrgFluxApplied(t *testing.T, r *Reconciler, ctx context.Context, org *orgapi.Organization) error {
	t.Helper()
	fg := ensureTestRegionWiring(r)
	if err := r.reconcilePerOrgFlux(ctx, org); err != nil {
		return err
	}
	if strings.TrimSpace(r.GiteaInClusterURL) != "" {
		fluxApplyRegionFile(t, r, fg, org, testRegionA)
	}
	return nil
}

func teardownPerOrgFluxApplied(t *testing.T, r *Reconciler, ctx context.Context, org *orgapi.Organization) error {
	t.Helper()
	ensureTestRegionWiring(r)
	return r.teardownPerOrgFlux(ctx, org)
}
